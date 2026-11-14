// SPDX-License-Identifier: Apache-2.0

package install

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/githubapps"
)

// AppInfo is one GitHub App the install runs on: created by the manifest flow (with its
// credentials, which go to secrets/ only), or an existing one named by the answers.
type AppInfo struct {
	ID           int64
	Slug         string
	HTMLURL      string
	ClientID     string
	PEM          githubapps.Secret // created only; prints as [redacted]
	ClientSecret githubapps.Secret // the factory App's OAuth client secret, created only (R5)
}

// Created reports whether the wizard holds the App's key: it made it.
func (a AppInfo) Created() bool { return a.PEM != "" }

// Inputs is everything an install's files are written from.
type Inputs struct {
	Answers Answers
	Factory AppInfo
	Agents  AppInfo
	Pins    map[string]string // image -> <version>@sha256:<digest>
	// Generated random values: the oauth2-proxy cookie secret and the Slack App's webhook key.
	CookieSecret string
	WebhookKey   string
}

// The Secret names the wizard chooses for what it generates; an existing App's are the answers'.
const (
	secretWebProxy     = "rooms-web-proxy"
	secretBrokerOIDC   = "rooms-oidc"
	secretFactoryOIDC  = "agent-factory-oidc"
	secretSponsored    = "agent-factory-sponsored"
	secretGitHubLink   = "maku-github-link" //nolint:gosec // a Secret's name, not a credential
	secretSlackApp     = "factory-slack-app"
	secretSlackPrinc   = "factory-slack-principal"
	secretSlackWebhook = "room-broker-webhook-slack-app" //nolint:gosec // a Secret's name, not a credential
	// secretFactoryCA holds ca.crt, the CA that signs the factory API's certificate: the broker trusts
	// it (broker.factoryCA). cert-manager writes only the leaf's Secret.
	secretFactoryCA = "agent-factory-ca" //nolint:gosec // a Secret's name, not a credential
	slackPrincipal  = "slack-app"
)

func (in Inputs) factorySecret() string {
	if in.Factory.Created() {
		return "agent-factory-github"
	}
	return defaultStr(in.Answers, "apps.factorySecret", "agent-factory-github")
}

func (in Inputs) agentsSecret() string {
	if in.Agents.Created() {
		return "agent-factory-agents"
	}
	return defaultStr(in.Answers, "apps.agentsSecret", "agent-factory-agents")
}

func (in Inputs) factorySlug() string {
	if in.Factory.Slug != "" {
		return in.Factory.Slug
	}
	return in.Answers.String("apps.factorySlug")
}

func (in Inputs) agentsSlug() string {
	if in.Agents.Slug != "" {
		return in.Agents.Slug
	}
	return in.Answers.String("apps.agentsSlug")
}

// FactoryAppID is the factory App's id: the merge gate's status author and bypass actor.
func (in Inputs) FactoryAppID() string {
	if in.Factory.ID != 0 {
		return strconv.FormatInt(in.Factory.ID, 10)
	}
	return in.Answers.String("apps.factoryID")
}

func (in Inputs) ns() string { return defaultStr(in.Answers, "namespace", "maku") }

// githubURL is GitHub's site, "" for github.com.
func (in Inputs) ghes() string {
	u := strings.TrimSuffix(defaultStr(in.Answers, "github.url", "https://github.com"), "/")
	if u == "https://github.com" {
		return ""
	}
	return u
}

// Values builds the umbrella's values: every applicable question's own values, then what the
// answers together imply. A list written at one path by two writers is appended.
func Values(in Inputs) (map[string]any, error) {
	a := in.Answers
	v := questionValues(a)
	put := func(path string, val any) { setPath(v, path, val) }
	for _, img := range Images {
		if img == "maku-slack" && !a.Bool("slack.enabled") {
			continue
		}
		if in.Pins[img] == "" {
			return nil, fmt.Errorf("no digest for %s %s: answer images.pins, or let maku read it from ghcr.io", img, a.String("version"))
		}
	}
	ns := in.ns()
	factorySA := "system:serviceaccount:" + ns + ":agent-factory"

	// maku: the broker, the run-controller, the runs.
	put("maku.enabled", true)
	put("maku.broker.image.tag", in.Pins["maku-broker"])
	put("maku.runController.image.tag", in.Pins["maku-run-controller"])
	put("maku.runController.images.harness.tag", in.Pins["maku-harness"])
	put("maku.runController.images.bridge.tag", in.Pins["maku-bridge"])
	put("maku.broker.human.mode", "oidc")
	// The preflight's test hook (D24, R2): helm test runs it once the release is up.
	put("maku.preflight.enabled", true)
	put("maku-factory.preflight.enabled", true) // the factory's own checks, which maku.preflight.enabled needs (PR B)
	put("maku.broker.human.access", map[string]any{"ttl": "5m"})
	put("maku.broker.oidc.existingSecret", secretBrokerOIDC)
	put("maku.broker.systemPrincipals", map[string]any{factorySA: "system:factory"})
	// The factory's run API, where the broker forwards each human's bearer: TLS from the ClusterIssuer,
	// trusted through the factory CA's Secret; plaintext only on an explicit factory.insecure answer.
	if issuer := a.String("tls.issuer"); issuer != "" {
		put("maku.broker.factoryURL", fmt.Sprintf("https://agent-factory.%s.svc:8082", ns))
		put("maku.broker.factoryCA.secretName", secretFactoryCA)
		put("maku-factory.api.tls", map[string]any{"secretName": "agent-factory-api-tls", "issuerRef": map[string]any{"kind": "ClusterIssuer", "name": issuer}})
	} else {
		put("maku.broker.factoryURL", fmt.Sprintf("http://agent-factory.%s.svc.cluster.local:8082", ns))
		put("maku.broker.factoryInsecure", true)
	}
	put("maku.broker.webProxy", true)
	put("maku.broker.githubLink.existingSecret", secretGitHubLink)
	// The broker resolves GitHub ids and room access with the factory App's key (docs/integration.md).
	env := []any{map[string]any{"name": "ROOMS_GITHUB_APP_DIR", "value": "/etc/maku/github-app"}}
	if g := in.ghes(); g != "" {
		env = append(env, map[string]any{"name": "ROOMS_GITHUB_API_URL", "value": g + "/api/v3/"})
		put("maku-factory.extraEnv", []any{map[string]any{"name": "ROOMS_GITHUB_API_URL", "value": g + "/api/v3/"}})
	}
	put("maku.broker.extraEnv", env)
	put("maku.broker.extraVolumes", []any{map[string]any{"name": "github-app", "secret": map[string]any{"secretName": in.factorySecret(), "defaultMode": 288}}})
	put("maku.broker.extraVolumeMounts", []any{map[string]any{"name": "github-app", "mountPath": "/etc/maku/github-app", "readOnly": true}})
	// Per-run tokens from the agents App (D10, D12): the factory mints them, the run-controller mounts them.
	put("maku.agentRunPolicies", map[string]any{"creators": []any{factorySA}, "factoryServiceAccount": "agent-factory"})
	put("maku.runController.model.mode", "gateway")
	put("maku.runController.model.gateway.aliases", map[string]any{"public": []any{"agent-default", "tier-light"}, "internal": []any{}})

	// maku-factory.
	put("maku-factory.enabled", true)
	put("maku-factory.image.tag", in.Pins["maku-factory"])
	put("maku-factory.apps.factory.existingSecret", in.factorySecret())
	put("maku-factory.apps.agents.existingSecret", in.agentsSecret())
	put("maku-factory.apps.merger.mode", "factory")
	put("maku-factory.oidcSecret", secretFactoryOIDC)
	put("maku-factory.runTokens.enabled", true)
	put("maku-factory.runTokens.perRun", true)
	cfg, err := factoryResidual(in)
	if err != nil {
		return nil, err
	}
	digMap(v, "maku-factory")["config"] = cfg // the residual holds the questions' own config keys
	networkFactory(in, put)

	// The model gateway (D7).
	put("maku-gateway.enabled", true)
	put("maku-gateway.runs", map[string]any{"namespace": "agents", "namePrefix": "run-"})

	// The web front door (D19).
	put("webProxy.enabled", true)
	put("webProxy.existingSecret", secretWebProxy)

	if a.Bool("slack.enabled") {
		slackValues(in, put)
	}
	if a.String("secrets.mode") == "eso" {
		put("externalSecrets.enabled", true)
		put("externalSecrets.storeRef.kind", "ClusterSecretStore")
		put("externalSecrets.secrets", externalSecrets(in))
	}
	return v, nil
}

// networkFactory turns on the factory's default-deny (D6) in maku's mode, with its GHES egress.
func networkFactory(in Inputs, put func(string, any)) {
	put("networkPolicies.factory.enabled", true)
	if g := in.ghes(); g != "" && defaultStr(in.Answers, "network.mode", "kubernetes") == "cilium" {
		put("networkPolicies.factory.github.to", []any{map[string]any{"toFQDNs": []any{map[string]any{"matchName": host(g)}}}})
	}
}

// slackValues wires the Slack App (EX7, EX8): its subchart, the broker's subscriber, both sponsored
// entries, and its route's peers.
func slackValues(in Inputs, put func(string, any)) {
	a := in.Answers
	ns := in.ns()
	mode := defaultStr(a, "network.mode", "kubernetes")
	put("slack.image.tag", in.Pins["maku-slack"])
	put("slack.issuerURL", a.String("identity.issuer"))
	put("slack.roomsURL", strings.TrimSuffix(a.String("web.url"), "/"))
	put("slack.preset", defaultStr(a, "identity.preset", "generic"))
	put("slack.slackSecret", secretSlackApp)
	put("slack.principalSecret", secretSlackPrinc)
	put("slack.webhookSecret", secretSlackWebhook)
	put("slack.config.channels", map[string]any{a.String("slack.channel"): map[string]any{
		"repositories": anys(a.List("repositories")), "maxCostUSD": 2, "approvals": true}})
	put("slack.config.proposals", map[string]any{"enabled": true})
	put("slack.networkPolicy.mode", mode)
	if mode == "kubernetes" && a.String("network.gatewayNamespace") != "" {
		put("slack.networkPolicy.from", peers("kubernetes", a.String("network.gatewayNamespace")))
	}
	put("maku.broker.webhooks", []any{map[string]any{
		"name": slackPrincipal, "network": "cluster",
		"url":            fmt.Sprintf("http://factory-slack-app.%s.svc.cluster.local:8081/webhooks", ns),
		"events":         anys([]string{"room.task_triaged", "room.waiting_for_human", "room.pr_opened", "room.verdict_posted", "room.merged", "room.reverted", "room.budget_reached", "room.ended", "room.approval_closed"}),
		"existingSecret": secretSlackWebhook,
		"repositories":   anys(a.List("repositories")),
	}})
	put("maku.broker.human.mcp", map[string]any{"enabled": true, "sponsored": []any{
		map[string]any{"name": slackPrincipal, "clientIDFile": "/etc/room-broker/oidc/slack-app-client-id"}}})
}

// externalSecrets is one ExternalSecret per third-party seam (D14), each read whole from the store
// at maku/<its Secret's name>: the DSNs, the provider's key, the Slack App's Secrets.
func externalSecrets(in Inputs) map[string]any {
	a := in.Answers
	from := func(name string) map[string]any {
		return map[string]any{"dataFrom": []any{map[string]any{"extract": map[string]any{"key": "maku/" + name}}}}
	}
	es := map[string]any{
		"databaseBroker":    from(defaultStr(a, "database.brokerSecret", "rooms-broker-dsn")),
		"databaseOwner":     from(defaultStr(a, "database.ownerSecret", "rooms-owner-dsn")),
		"databaseRetention": from(defaultStr(a, "database.retentionSecret", "rooms-retention-dsn")),
		"gatewayProvider." + providerName(defaultStr(a, "models.provider", "anthropic")): from(defaultStr(a, "models.secret", "model-provider")),
	}
	if ca := a.String("database.caSecret"); ca != "" {
		es["databaseCA"] = from(ca)
	}
	if a.Bool("slack.enabled") {
		es["slackApp"] = from(secretSlackApp)
		es["slackPrincipal"] = from(secretSlackPrinc)
	}
	return es
}

// setPath sets a dotted path in m; lists already there are appended to, maps merged.
func setPath(m map[string]any, path string, val any) {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	last := parts[len(parts)-1]
	switch old := cur[last].(type) {
	case []any:
		if l, ok := val.([]any); ok {
			cur[last] = append(old, l...)
			return
		}
	case map[string]any:
		if nm, ok := val.(map[string]any); ok {
			cur[last] = mergeMaps(old, nm)
			return
		}
	}
	cur[last] = val
}

// mergeMaps is b over a, recursively; neither is changed.
func mergeMaps(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if am, ok := out[k].(map[string]any); ok {
			if bm, ok := v.(map[string]any); ok {
				out[k] = mergeMaps(am, bm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// digMap is m's map at a dotted path; nil when absent.
func digMap(m map[string]any, path string) map[string]any {
	cur := m
	for _, p := range strings.Split(path, ".") {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

// repoNames is the repositories' names, sorted.
func repoNames(a Answers) []string {
	r := slices.Clone(a.List("repositories"))
	slices.Sort(r)
	return r
}

// Pins reads images.pins: image -> <version>@sha256:<digest>.
func Pins(a Answers) map[string]string {
	m := map[string]string{}
	for _, p := range a.List("images.pins") {
		if s := pinRE.FindStringSubmatch(p); s != nil {
			m[s[1]] = s[2]
		}
	}
	return m
}

// questionValues is every answered question's own values.
func questionValues(a Answers) map[string]any {
	v := map[string]any{}
	for _, q := range Questions {
		ans, ok := a[q.ID]
		if !ok || q.Values == nil {
			continue
		}
		vals := q.Values(ans, a)
		for _, p := range sortedKeys(vals) {
			setPath(v, p, vals[p])
		}
	}
	return v
}
