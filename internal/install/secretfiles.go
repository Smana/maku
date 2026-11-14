// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// secretManifest is one Secret, its keys in order; an empty value is one the user fills.
type secretManifest struct {
	name    string
	comment string
	keys    []kv
}

type kv struct{ k, v string }

func (s secretManifest) render(ns string) string {
	var b strings.Builder
	if s.comment != "" {
		for _, l := range strings.Split(s.comment, "\n") {
			b.WriteString("# " + l + "\n")
		}
	}
	fmt.Fprintf(&b, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\nstringData:\n", s.name, ns)
	for _, e := range s.keys {
		if strings.Contains(e.v, "\n") {
			fmt.Fprintf(&b, "  %s: |\n", e.k)
			for _, l := range strings.Split(strings.TrimRight(e.v, "\n"), "\n") {
				b.WriteString("    " + l + "\n")
			}
			continue
		}
		fmt.Fprintf(&b, "  %s: %s\n", e.k, strconv.Quote(e.v))
	}
	return b.String()
}

func join(ns string, ms ...secretManifest) []byte {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.render(ns)
	}
	return []byte(strings.Join(parts, "---\n"))
}

// secretFiles is secrets/ (R3): what the wizard generated (the Apps' keys and OAuth client, the
// cookie and webhook keys), the client ids, and, unless ExternalSecrets deliver them, templates with
// empty keys for what it never asks: the DSNs, the provider's key, the IdP's client secret, Slack's.
func secretFiles(in Inputs) map[string][]byte {
	a := in.Answers
	ns := in.ns()
	out := map[string][]byte{}
	if in.Agents.Created() {
		out["agents-app.yaml"] = join(ns, secretManifest{name: in.agentsSecret(), comment: "The agents App (" + in.Agents.Slug + "): it mints each run's token (D10, D12).",
			keys: []kv{{"app_id", strconv.FormatInt(in.Agents.ID, 10)}, {"private_key", in.Agents.PEM}}})
	}
	link := secretManifest{name: secretGitHubLink, comment: "The factory App's OAuth client: the GitHub account link (R5, docs/github-link.md).",
		keys: []kv{{"client_id", in.Factory.ClientID}, {"client_secret", in.Factory.ClientSecret}}}
	if in.Factory.Created() {
		out["factory-app.yaml"] = join(ns, secretManifest{name: in.factorySecret(), comment: "The factory App (" + in.Factory.Slug + "), the merger folded in.",
			keys: []kv{{"app_id", strconv.FormatInt(in.Factory.ID, 10)}, {"private_key", in.Factory.PEM}}}, link)
	} else {
		link.comment += "\nFill client_id and client_secret from the factory App's settings page."
		out["templates/github-link.yaml"] = join(ns, link)
	}
	out["web-proxy.yaml"] = join(ns, secretManifest{name: secretWebProxy,
		comment: "The web front door (oauth2-proxy). Fill client-secret: the web client's secret at your issuer.",
		keys:    []kv{{"client-id", a.String("identity.webClientID")}, {"client-secret", ""}, {"cookie-secret", in.CookieSecret}}})
	brokerIDs := []kv{{"web-client-id", a.String("identity.webClientID")}, {"cli-client-id", a.String("identity.cliClientID")}}
	if a.Bool("slack.enabled") {
		brokerIDs = append(brokerIDs, kv{"slack-app-client-id", a.String("slack.clientID")})
	}
	factoryIDs := []kv{{"web-client-id", a.String("identity.webClientID")}, {"cli-client-id", a.String("identity.cliClientID")}}
	sponsored := []kv{{"slack-app-client-id", a.String("slack.clientID")}}
	if p := a.String("identity.projectID"); p != "" {
		brokerIDs = append(brokerIDs, kv{"project-id", p})
		factoryIDs = append(factoryIDs, kv{"project-id", p})
		sponsored = append(sponsored, kv{"project-id", p})
	}
	ids := []secretManifest{
		{name: secretBrokerOIDC, comment: "The client ids the broker admits (ids, not secrets).", keys: brokerIDs},
		{name: secretFactoryOIDC, comment: "The client ids the factory's API admits.", keys: factoryIDs},
	}
	if a.Bool("slack.enabled") {
		ids = append(ids, secretManifest{name: secretSponsored, comment: "The Slack App's client id, which the factory admits as a sponsored principal.",
			keys: sponsored})
		out["slack-webhook.yaml"] = join(ns, secretManifest{name: secretSlackWebhook, comment: "The key the broker signs room events with, and the Slack App verifies.",
			keys: []kv{{"signing-key", in.WebhookKey}}})
	}
	out["client-ids.yaml"] = join(ns, ids...)
	if a.String("secrets.mode") == "eso" {
		return out
	}
	sslmode := defaultStr(a, "database.sslmode", "require")
	dsn := func(role, name string) secretManifest {
		query := "sslmode=" + sslmode
		if a.String("database.caSecret") != "" {
			query += "&sslrootcert=/etc/room-broker/db-ca/ca.crt"
		}
		return secretManifest{name: name, comment: fmt.Sprintf("Role %s (docs/database.md): postgresql://%s:<password>@<host>:5432/rooms?%s", role, role, query),
			keys: []kv{{defaultStr(a, "database.key", "uri"), ""}}}
	}
	db := []secretManifest{
		dsn("rooms_broker", defaultStr(a, "database.brokerSecret", "rooms-broker-dsn")),
		dsn("rooms_owner", defaultStr(a, "database.ownerSecret", "rooms-owner-dsn")),
		dsn("rooms_retention", defaultStr(a, "database.retentionSecret", "rooms-retention-dsn")),
	}
	if ca := a.String("database.caSecret"); ca != "" {
		db = append(db, secretManifest{name: ca, comment: "The database's CA.", keys: []kv{{"ca.crt", ""}}})
	}
	out["templates/database.yaml"] = join(ns, db...)
	out["templates/model-provider.yaml"] = join(ns, secretManifest{name: defaultStr(a, "models.secret", "model-provider"),
		comment: "The " + defaultStr(a, "models.provider", "anthropic") + " API key the model gateway sends (D7): never a run's.",
		keys:    []kv{{defaultStr(a, "models.key", "apiKey"), ""}}})
	if a.Bool("slack.enabled") {
		out["templates/slack.yaml"] = join(ns,
			secretManifest{name: secretSlackApp, comment: "The Slack App's own credentials (docs/slack.md#install).",
				keys: []kv{{"signing-secret", ""}, {"bot-token", ""}, {"bot-user-id", ""}}},
			secretManifest{name: secretSlackPrinc, comment: "The Slack App's machine client at your issuer (client credentials).",
				keys: []kv{{"client-id", a.String("slack.clientID")}, {"client-secret", ""}}})
	}
	return out
}

// secretNames is every Secret manifest in t's secrets/, sorted: what the README applies.
func secretNames(t Tree) []string {
	var names []string
	for p := range t {
		if strings.HasPrefix(p, "secrets/") && strings.HasSuffix(p, ".yaml") {
			names = append(names, p)
		}
	}
	sort.Strings(names)
	return names
}

// holdsKeyMaterial reports whether b holds a PEM key, or one of secrets' values: what no file outside
// secrets/ may hold.
func holdsKeyMaterial(b []byte, secrets ...string) bool {
	if bytes.Contains(b, []byte("PRIVATE KEY")) {
		return true
	}
	for _, s := range secrets {
		if s != "" && bytes.Contains(b, []byte(s)) {
			return true
		}
	}
	return false
}
