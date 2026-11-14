// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/preflight"
	"github.com/Smana/maku/internal/store"
)

// fakeDB is the broker's database as the preflight sees it: its check and its schema check.
type fakeDB struct {
	recheck, schema error
	calls           atomic.Int32
}

func (f *fakeDB) Recheck(context.Context) error       { f.calls.Add(1); return f.recheck }
func (f *fakeDB) SchemaCurrent(context.Context) error { return f.schema }

func byCheck(rs []preflight.Result) map[string]preflight.Result {
	out := map[string]preflight.Result{}
	for _, r := range rs {
		out[r.Check] = r
	}
	return out
}

// The database check maps each refusal of step 5's checks to its code, an unreadable ledger
// included (step 8 R10), and no message ever holds the DSN, whatever the driver's error says.
func TestBrokerPreflightDatabase(t *testing.T) {
	const dsn = "postgresql://rooms_broker:hunter2@db.example:5432/rooms"
	for name, c := range map[string]struct {
		recheck, schema error
		outcome         preflight.Outcome
		code            string
	}{
		"healthy":               {nil, nil, preflight.Pass, ""},
		"Postgres 15":           {fmt.Errorf("x: %w", store.ErrServerTooOld), nil, preflight.Fail, "db_too_old"},
		"a role with BYPASSRLS": {fmt.Errorf("x: %w", store.ErrBypassesRLS), nil, preflight.Fail, "db_role_bypasses_rls"},
		"another role":          {fmt.Errorf("x: %w", store.ErrWrongRole), nil, preflight.Fail, "db_wrong_role"},
		"unreachable":           {fmt.Errorf("dial %s: %w", dsn, store.ErrUnavailable), nil, preflight.Fail, "db_unreachable"},
		"a malformed DSN":       {errors.New("cannot parse `" + dsn + "`: failed to parse"), nil, preflight.Fail, "db_unreachable"},
		"an unreadable ledger":  {nil, fmt.Errorf("x: %w", store.ErrLedgerUnreadable), preflight.Fail, "db_ledger_unreadable"},
		"a ledger behind":       {nil, fmt.Errorf("x: %w", store.ErrSchemaBehind), preflight.Fail, "db_schema_behind"},
		"a ledger ahead":        {nil, fmt.Errorf("x: %w", store.ErrSchemaAhead), preflight.Fail, "db_schema_ahead"},
	} {
		t.Run(name, func(t *testing.T) {
			b := brokerChecks{DB: &fakeDB{recheck: c.recheck, schema: c.schema}, Human: config.HumanConfig{Mode: config.HumanModeLocal}}
			r := byCheck(b.source()(t.Context()))["database"]
			if r.Outcome != c.outcome || r.Code != c.code {
				t.Fatalf("%+v", r)
			}
			if strings.Contains(r.Message, "hunter2") || strings.Contains(r.Message, "postgresql://") || strings.Contains(r.Message, "db.example") {
				t.Fatalf("the message leaks the DSN: %q", r.Message)
			}
		})
	}
}

func rsaJWKS(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}})
	return b
}

// The OIDC check: discovery answers with this issuer, and the JWKS fetches.
func TestBrokerPreflightOIDC(t *testing.T) {
	jwks := rsaJWKS(t)
	for name, c := range map[string]struct {
		issuer func(srv string) string // what discovery claims
		keys   int                     // the JWKS status
		code   string
	}{
		"healthy":            {func(s string) string { return s }, http.StatusOK, ""},
		"an issuer mismatch": {func(string) string { return "https://elsewhere.example" }, http.StatusOK, "oidc_issuer_mismatch"},
		"a JWKS 500":         {func(s string) string { return s }, http.StatusInternalServerError, "oidc_jwks"},
	} {
		t.Run(name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					_ = json.NewEncoder(w).Encode(map[string]string{"issuer": c.issuer(srv.URL), "jwks_uri": srv.URL + "/keys"})
				case "/keys":
					w.WriteHeader(c.keys)
					_, _ = w.Write(jwks)
				}
			}))
			defer srv.Close()
			b := brokerChecks{DB: &fakeDB{}, HC: srv.Client(), Human: config.HumanConfig{Preset: config.PresetGeneric, Issuer: srv.URL}}
			r := byCheck(b.source()(t.Context()))["oidc"]
			if c.code == "" && r.Outcome != preflight.Pass || c.code != "" && r.Code != c.code {
				t.Fatalf("%+v", r)
			}
		})
	}
	b := brokerChecks{DB: &fakeDB{}, HC: http.DefaultClient, Human: config.HumanConfig{Mode: config.HumanModeLocal}} //nolint:forbidigo // never called: local mode
	if r := byCheck(b.source()(t.Context()))["oidc"]; r.Outcome != preflight.Skip {
		t.Fatalf("local mode has no IdP: %+v", r)
	}
}

// The knowledge check lists the endpoint's tools: one missing kb_get is named.
func TestBrokerPreflightKnowledge(t *testing.T) {
	tools := []string{"kb_search"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{}}}`, req.ID)
		case "tools/list":
			var ts []string
			for _, n := range tools {
				ts = append(ts, `{"name":"`+n+`"}`)
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[%s]}}`, req.ID, strings.Join(ts, ","))
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	b := brokerChecks{DB: &fakeDB{}, HC: srv.Client(), Human: config.HumanConfig{Mode: config.HumanModeLocal},
		Knowledge: config.KnowledgeConfig{URL: srv.URL, Tools: []string{"kb_search", "kb_get"}}}
	r := byCheck(b.source()(t.Context()))["knowledge"]
	if r.Code != "knowledge_tools_missing" || !strings.Contains(r.Message, "kb_get") {
		t.Fatalf("%+v", r)
	}
	tools = append(tools, "kb_get")
	if r := byCheck(b.source()(t.Context()))["knowledge"]; r.Outcome != preflight.Pass {
		t.Fatalf("%+v", r)
	}
	b.Knowledge.URL = "http://127.0.0.1:1"
	if r := byCheck(b.source()(t.Context()))["knowledge"]; r.Code != "knowledge_unreachable" {
		t.Fatalf("%+v", r)
	}
}

// The models check: the planner's and the classifier's clients each answer a one-token completion;
// the gateway's 401 for the planner is model_refused, and the key never leaves the file.
func TestBrokerPreflightModels(t *testing.T) {
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if strings.Contains(r.URL.Path, "planner") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key sk-secret-echo"}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"o"}}]}`))
	}))
	defer srv.Close()
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("sk-planner-key\n"), 0o600)
	b := brokerChecks{DB: &fakeDB{}, HC: srv.Client(), Human: config.HumanConfig{Mode: config.HumanModeLocal},
		Models: []modelClient{{Name: "planner", URL: srv.URL + "/clients/planner/v1", KeyFile: key, Alias: "tier-frontier"},
			{Name: "classifier", URL: srv.URL + "/clients/classifier/v1", KeyFile: key, Alias: "tier-light"}}}
	r := byCheck(b.source()(t.Context()))["models"]
	if r.Code != "model_refused" || !strings.Contains(r.Message, "planner") || strings.Contains(r.Message, "sk-") {
		t.Fatalf("%+v", r)
	}
	if len(auths) == 0 || auths[0] != "Bearer sk-planner-key" {
		t.Fatalf("the key was not sent: %v", auths)
	}
}

// The ops port serves /preflight: computed once per 30 s, so a second call costs no round trip.
func TestBrokerPreflightServed(t *testing.T) {
	db := &fakeDB{}
	h := preflight.Handler("broker", "v1", brokerChecks{DB: db, Human: config.HumanConfig{Mode: config.HumanModeLocal}}.source(), nil)
	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/preflight", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"check":"database"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	if db.calls.Load() != 1 {
		t.Fatalf("the second call reached the database: %d", db.calls.Load())
	}
}

// The GitHub link's check probes the OAuth client with a code that cannot exist: GitHub then names
// the client or the callback it refused.
func TestBrokerPreflightGitHubLink(t *testing.T) {
	var answer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/login/oauth/access_token" || r.Form.Get("redirect_uri") != "https://rooms.example/link/github/callback" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"error":"` + answer + `"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "client_id"), []byte("Iv1.abc"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "client_secret"), []byte("s3cret-value"), 0o600)
	b := brokerChecks{DB: &fakeDB{}, HC: srv.Client(), PublicURL: "https://rooms.example",
		Human: config.HumanConfig{Mode: config.HumanModeLocal, GitHubLink: &config.GitHubLinkConfig{
			ClientIDFile: filepath.Join(dir, "client_id"), ClientSecretFile: filepath.Join(dir, "client_secret"), WebURL: srv.URL}}}
	for a, want := range map[string]string{"bad_verification_code": "", "incorrect_client_credentials": "link_client",
		"redirect_uri_mismatch": "link_callback_mismatch"} {
		answer = a
		r := byCheck(b.source()(t.Context()))["github-link"]
		if (want == "" && r.Outcome != preflight.Pass) || (want != "" && r.Code != want) || strings.Contains(r.Message, "s3cret") {
			t.Errorf("%s: %+v", a, r)
		}
	}
}
