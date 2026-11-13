// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/mcp"
	"github.com/Smana/maku/internal/preflight"
	"github.com/Smana/maku/internal/store"
)

// preflightDB is the broker's database as its preflight reads it: Open's check run again, and the
// schema check.
type preflightDB interface {
	Recheck(ctx context.Context) error
	SchemaCurrent(ctx context.Context) error
}

// modelClient is one of the broker's model clients (the planner, the classifier): its gateway
// route, the file holding its bearer and its pinned alias.
type modelClient struct {
	Name, URL, KeyFile, Alias string
}

// brokerChecks are the broker's own preflight checks (v4 D24, IN3): each runs over the broker's own
// network, DNS and credentials, so a check passes only where the broker's traffic does. No message
// names a value: a DSN, a key or an upstream body never leaves.
type brokerChecks struct {
	DB        preflightDB
	Human     config.HumanConfig
	PublicURL string
	HC        *http.Client
	Knowledge config.KnowledgeConfig
	Models    []modelClient
	// Timeout bounds each check (broker config preflight.timeout); zero is preflight.CheckTimeout.
	Timeout time.Duration
}

func (b brokerChecks) source() preflight.Source {
	return func(ctx context.Context) []preflight.Result {
		bound := preflight.CheckTimeout
		if b.Timeout > 0 {
			bound = b.Timeout
		}
		checks := []struct {
			id  string
			run func(context.Context) preflight.Result
		}{
			{"database", b.database}, {"oidc", b.oidc}, {"github-link", b.githubLink},
			{"knowledge", b.knowledge}, {"models", b.models},
		}
		out := make([]preflight.Result, len(checks))
		done := make(chan struct{}, len(checks))
		for i, c := range checks {
			go func() {
				out[i] = preflight.Bounded(ctx, bound, c.id, c.run)
				done <- struct{}{}
			}()
		}
		for range checks {
			<-done
		}
		return out
	}
}

func pass(msg string) preflight.Result {
	return preflight.Result{Outcome: preflight.Pass, Message: msg}
}
func skip(msg string) preflight.Result {
	return preflight.Result{Outcome: preflight.Skip, Message: msg}
}
func failed(code, msg string) preflight.Result {
	return preflight.Result{Outcome: preflight.Fail, Code: code, Message: msg}
}

// database runs step 5's check of the broker's own role, then the schema's. Its messages are fixed:
// a driver error may quote the DSN.
func (b brokerChecks) database(ctx context.Context) preflight.Result {
	if b.DB == nil {
		return failed(preflight.Undecided, "the broker holds no database pool")
	}
	if err := b.DB.Recheck(ctx); err != nil {
		switch {
		case errors.Is(err, store.ErrServerTooOld):
			return failed("db_too_old", "the server is below Postgres 16 (D26)")
		case errors.Is(err, store.ErrBypassesRLS):
			return failed("db_role_bypasses_rls", "rooms_broker bypasses row-level security: the room log would be writable (docs/database.md)")
		case errors.Is(err, store.ErrWrongRole):
			return failed("db_wrong_role", "the broker's DSN connects as another role than rooms_broker (docs/database.md)")
		case errors.Is(err, store.ErrUnavailable):
			return failed("db_unreachable", "the database does not answer the broker")
		}
		return failed("db_unreachable", "the database refused the broker's connection check (docs/database.md)")
	}
	switch err := b.DB.SchemaCurrent(ctx); {
	case errors.Is(err, store.ErrSchemaUnverified):
		return preflight.Result{Outcome: preflight.Warn, Code: "schema_unverified",
			Message: "rooms_broker cannot read the migration ledger: set broker.database.ownerSecret (docs/database.md)"}
	case errors.Is(err, store.ErrSchemaBehind):
		return failed("db_schema_behind", "the schema is behind this broker: run migrate (broker.database.ownerSecret)")
	case errors.Is(err, store.ErrSchemaAhead):
		return failed("db_schema_ahead", "the schema is newer than this broker: upgrade the broker")
	case err != nil:
		return failed(preflight.Undecided, "the schema version is unreadable")
	}
	return pass("rooms_broker on Postgres 16 or later, the schema current")
}

func host(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "the configured URL"
}

// oidc reads the issuer's discovery document (or the configured JWKS URL) and fetches the keys, as
// the broker's verifier does.
func (b brokerChecks) oidc(ctx context.Context) preflight.Result {
	h := b.Human
	if h.EffectiveMode() == config.HumanModeLocal {
		return skip("mode local: no identity provider")
	}
	jwks := h.JWKSURL
	if jwks == "" {
		d, err := authn.Discover(ctx, b.HC, h.Issuer)
		switch {
		case errors.Is(err, authn.ErrIssuerMismatch):
			return failed("oidc_issuer_mismatch", fmt.Sprintf("%s's discovery document names another issuer: human.issuer must be its issuer, byte for byte", host(h.Issuer)))
		case err != nil:
			return failed("oidc_discovery", fmt.Sprintf("%s's discovery document does not read", host(h.Issuer)))
		}
		jwks = d.JWKSURI
	}
	if _, err := authn.NewVerifier(ctx, h.Issuer, jwks, authn.WithHTTPClient(b.HC)); err != nil {
		return failed("oidc_jwks", fmt.Sprintf("the JWKS at %s does not fetch or holds no usable key", host(jwks)))
	}
	return pass(host(h.Issuer) + ": discovery and its keys")
}

// githubLink probes the link's OAuth client with a code that cannot exist: GitHub then judges the
// client and the callback before the code, and answers which it refused.
func (b brokerChecks) githubLink(ctx context.Context) preflight.Result {
	l := b.Human.GitHubLink
	if l == nil {
		return skip("human.githubLink is off")
	}
	id, secret := fileValue(l.ClientIDFile), fileValue(l.ClientSecretFile)
	if id == "" || secret == "" {
		return failed("link_client", "the link's client id or secret file is empty (githubLink: docs/install-secrets.md)")
	}
	web := strings.TrimSuffix(l.WebURL, "/")
	if web == "" {
		web = config.DefaultGitHubWebURL
	}
	form := url.Values{"client_id": {id}, "client_secret": {secret}, "code": {"maku-preflight-probe"},
		"redirect_uri": {strings.TrimSuffix(b.PublicURL, "/") + "/link/github/callback"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, web+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return failed(preflight.Undecided, "the link's GitHub URL is malformed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := b.HC.Do(req)
	if err != nil {
		return failed(preflight.Undecided, host(web)+" does not answer the broker")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := httpx.ReadBody(resp.Body, 16<<10)
	var r struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &r)
	switch r.Error {
	case "bad_verification_code":
		return pass(host(web) + ": the OAuth client and its callback")
	case "incorrect_client_credentials":
		return failed("link_client", host(web)+" refused the link's OAuth client id and secret")
	case "redirect_uri_mismatch":
		return failed("link_callback_mismatch", "the OAuth client's callback URL is not "+strings.TrimSuffix(b.PublicURL, "/")+"/link/github/callback")
	}
	return failed(preflight.Undecided, fmt.Sprintf("%s answered the probe with HTTP %d", host(web), resp.StatusCode))
}

// knowledge lists RunLore's tools under the configured timeout (F10).
func (b brokerChecks) knowledge(ctx context.Context) preflight.Result {
	k := b.Knowledge
	if k.URL == "" {
		return skip("roomTools.knowledge is off")
	}
	if k.Timeout.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, k.Timeout.Duration)
		defer cancel()
	}
	names, err := mcp.KnowledgeToolNames(ctx, k.URL, b.HC)
	if err != nil {
		return failed("knowledge_unreachable", host(k.URL)+" does not answer tools/list")
	}
	want := k.Tools
	if len(want) == 0 {
		want = []string{"kb_search", "kb_get"}
	}
	var missing []string
	for _, t := range want {
		if !slices.Contains(names, t) {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return failed("knowledge_tools_missing", fmt.Sprintf("%s does not list %s", host(k.URL), strings.Join(missing, ", ")))
	}
	return pass(host(k.URL) + ": " + strings.Join(want, ", "))
}

// models asks each of the broker's model clients for a one-token completion through its route, as
// its key: the first refusal is the result.
func (b brokerChecks) models(ctx context.Context) preflight.Result {
	if len(b.Models) == 0 {
		return skip("neither the planner nor the classifier is on")
	}
	var names []string
	for _, m := range b.Models {
		if r := probeModel(ctx, b.HC, m); r.Outcome != preflight.Pass {
			return r
		}
		names = append(names, m.Name)
	}
	return pass(strings.Join(names, " and ") + " answer")
}

func probeModel(ctx context.Context, hc *http.Client, m modelClient) preflight.Result {
	key := fileValue(m.KeyFile)
	if key == "" {
		return failed("model_refused", "the "+m.Name+" client's key file is empty")
	}
	payload, _ := json.Marshal(map[string]any{"model": m.Alias, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "ping"}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(m.URL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return failed(preflight.Undecided, "the "+m.Name+" client's URL is malformed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := hc.Do(req)
	if err != nil {
		return failed("model_unreachable", fmt.Sprintf("the %s client's route at %s does not answer", m.Name, host(m.URL)))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return failed("model_refused", fmt.Sprintf("the gateway refused the %s client (HTTP %d)", m.Name, resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		return failed("model_unreachable", fmt.Sprintf("the %s client's route answered HTTP %d", m.Name, resp.StatusCode))
	}
	return pass(m.Name)
}

// brokerPreflight is the broker's checks from its config: the database pool, the identity block,
// the knowledge endpoint and the model clients that are on.
func brokerPreflight(cfg config.Config, db preflightDB) brokerChecks {
	b := brokerChecks{DB: db, Human: cfg.Human, PublicURL: cfg.PublicURL, HC: httpx.New(preflight.CheckTimeout, nil),
		Knowledge: cfg.Knowledge(), Timeout: cfg.Preflight.Timeout.Duration}
	if p := cfg.IntakePlan(); p.Enabled {
		b.Models = append(b.Models, modelClient{Name: "planner", URL: p.URL, KeyFile: p.APIKeyFile, Alias: cmp.Or(p.Model, config.PlanModel)})
	}
	if s, on := cfg.SlackIntents(); on {
		b.Models = append(b.Models, modelClient{Name: "classifier", URL: s.URL, KeyFile: s.APIKeyFile, Alias: cmp.Or(s.Model, config.ClassifyModel)})
	}
	return b
}
