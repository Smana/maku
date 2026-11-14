// SPDX-License-Identifier: Apache-2.0

// Package install is what an install of Maku needs from its operator, as data (v4 D14): every
// Secret a component reads, by name, with its keys. The docs' table (docs/install-secrets.md), the
// umbrella chart's ExternalSecrets (templates/_seams.tpl, written by `task chart:seams`), the
// components' start-up messages, and step 7's `maku preflight` read this one list.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Namespace is where a seam's Secret lives: the release's namespace, or the runs' one.
type Namespace string

// The namespaces.
const (
	Release Namespace = "release"
	Runs    Namespace = "runs"
)

// ReadAs is how a component reads a seam: an environment variable, a mounted file, or the
// Kubernetes API.
type ReadAs string

// The readers.
const (
	Env  ReadAs = "env"
	File ReadAs = "file"
	API  ReadAs = "api"
)

// Generated names what writes a seam's Secret in some installs, where an ExternalSecret of the same
// name would fight it: the umbrella's bundled Postgres, the umbrella itself (its TLS), the demo's
// model or App values, the factory's run-token writer, or cert-manager. "" is the operator's alone.
type Generated string

// The writers.
const (
	GeneratedPostgres  Generated = "postgres"
	GeneratedUmbrella  Generated = "umbrella"
	GeneratedDemoModel Generated = "demo-model"
	GeneratedDemoApp   Generated = "demo-app"
	// GeneratedCertManager: cert-manager's Certificate writes it when the chart's issuerRef is set.
	GeneratedCertManager Generated = "cert-manager"
)

// Key is one key of a seam's Secret.
type Key struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	// From is a values path, in the seam's chart, that renames this key when set.
	From string `json:"from,omitempty"`
	Doc  string `json:"-"`
}

// Seam is one Secret an install provides: its id, who reads it and how, the chart value that
// names it and its default name, and its keys.
type Seam struct {
	ID        string `json:"id"`
	Component string `json:"component"`
	Chart     string `json:"chart"` // the chart whose values name it
	// ValuesKey is the chart's key in the umbrella's values when its alias differs (maku-slack is
	// slack); "" is Chart.
	ValuesKey string    `json:"valuesKey,omitempty"`
	Value     string    `json:"value"`   // its values path in that chart; "" is a fixed name
	Default   string    `json:"default"` // the name when the values set none; "" is none
	Namespace Namespace `json:"namespace"`
	Keys      []Key     `json:"keys"`
	ReadAs    ReadAs    `json:"readAs"`
	MountPath string    `json:"mountPath,omitempty"` // a file seam's directory in the component
	Optional  bool      `json:"optional"`
	// MountOptional: the chart mounts it optional, so a missing file is a configuration the
	// component refuses at use (an intake with no token), never a failed start.
	MountOptional bool      `json:"mountOptional,omitempty"`
	Generated     Generated `json:"generated,omitempty"`
	// DynamicKeys: the keys are named by the component's config (one per client, one per
	// sponsored principal), so a list of keys cannot be checked.
	DynamicKeys bool `json:"dynamicKeys,omitempty"`
	// Instances: Value is a map whose every entry names its own Secret in existingSecret, its key
	// in key (default Keys[0]); the ExternalSecret id is <id>.<entry>.
	Instances bool   `json:"instances,omitempty"`
	Doc       string `json:"-"`
}

var (
	uri       = []Key{{Name: "uri", Required: true, From: "broker.database.secretKey", Doc: "the role's DSN, `postgresql://…`"}}
	tlsPair   = []Key{{Name: "tls.crt", Required: true}, {Name: "tls.key", Required: true}}
	caCrt     = []Key{{Name: "ca.crt", Required: true}}
	githubApp = []Key{{Name: "app_id", Required: true}, {Name: "private_key", Required: true}}
)

// UmbrellaKey is the seam's chart's key in the umbrella's values.
func (s Seam) UmbrellaKey() string {
	if s.ValuesKey != "" {
		return s.ValuesKey
	}
	return s.Chart
}

// Seams is every Secret the charts and components read, in docs order.
var Seams = []Seam{
	{ID: "databaseBroker", Component: "room-broker", Chart: "maku", Value: "broker.database.brokerSecret",
		Default: "rooms-broker-dsn", Namespace: Release, Keys: uri, ReadAs: Env, Generated: GeneratedPostgres,
		Doc: "The broker's DSN, as `rooms_broker` (`ROOMS_DATABASE_URL`)"},
	{ID: "databaseOwner", Component: "room-broker (migrate)", Chart: "maku", Value: "broker.database.ownerSecret",
		Default: "rooms-owner-dsn", Namespace: Release, Keys: uri, ReadAs: Env, Optional: true, Generated: GeneratedPostgres,
		Doc: "The migrations' DSN, as `rooms_owner` (`ROOMS_MIGRATION_DATABASE_URL`); without it nothing is migrated"},
	{ID: "databaseRetention", Component: "room-broker-retention", Chart: "maku", Value: "broker.database.retentionSecret",
		Default: "rooms-retention-dsn", Namespace: Release, Keys: uri, ReadAs: Env, Optional: true, Generated: GeneratedPostgres,
		Doc: "The retention CronJob's DSN, as `rooms_retention`; without it no CronJob renders"},
	{ID: "databaseCA", Component: "room-broker", Chart: "maku", Value: "broker.database.caSecret", Namespace: Release, Keys: caCrt, ReadAs: File,
		MountPath: "/etc/room-broker/db-ca", Optional: true, Doc: "The database's CA, for `sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt`"},
	{ID: "databasePasswords", Component: "rooms-db (demo)", Chart: "agent-factory", Default: "rooms-db-passwords", Namespace: Release,
		Keys:   []Key{{Name: "postgres", Required: true}, {Name: "rooms_owner", Required: true}, {Name: "rooms_broker", Required: true}, {Name: "rooms_retention", Required: true}},
		ReadAs: Env, Generated: GeneratedPostgres, Doc: "The bundled Postgres's passwords (`postgres.enabled`, the demo only)"},
	{ID: "brokerOIDC", Component: "room-broker", Chart: "maku", Value: "broker.oidc.existingSecret", Namespace: Release,
		Keys: []Key{{Name: "web-client-id", Required: true, From: "broker.oidc.keys.web", Doc: "oauth2-proxy's client id"},
			{Name: "cli-client-id", From: "broker.oidc.keys.cli", Doc: "maku's client id"},
			{Name: "project-id", From: "broker.oidc.keys.project", Doc: "the ZITADEL project id (preset zitadel)"}},
		ReadAs: File, MountPath: "/etc/room-broker/oidc", Optional: true, Doc: "The OIDC client ids (ids, not secrets): `human.clientIDFile`, `roomctlClientIDFile`, `projectIDFile`"},
	{ID: "githubLink", Component: "room-broker", Chart: "maku", Value: "broker.githubLink.existingSecret", Namespace: Release,
		Keys: []Key{{Name: "client_id", Required: true, From: "broker.githubLink.keys.clientID"},
			{Name: "client_secret", Required: true, From: "broker.githubLink.keys.clientSecret"}},
		ReadAs: File, MountPath: "/etc/room-broker/github-link", Optional: true,
		Doc: "The GitHub account link's OAuth client (`human.githubLink`): the factory App's, or an OAuth App's. Its state is bound by a per-flow nonce, so no key material is stored"},
	{ID: "brokerTLS", Component: "room-broker", Chart: "maku", Value: "broker.tls.secretName", Default: "room-broker-tls", Namespace: Release,
		Keys: tlsPair, ReadAs: File, MountPath: "/etc/room-broker/tls", Generated: GeneratedUmbrella, Doc: "The broker's :8443 serving pair"},
	{ID: "brokerCA", Component: "run-controller", Chart: "maku", Value: "broker.tls.caSecretName", Default: "room-broker-ca", Namespace: Release,
		Keys: caCrt, ReadAs: API, Generated: GeneratedUmbrella, Doc: "The CA of the broker's pair, which the run-controller hands each run's bridge"},
	{ID: "factoryCA", Component: "room-broker", Chart: "maku", Value: "broker.factoryCA.secretName", Namespace: Release, Keys: caCrt, ReadAs: File,
		MountPath: "/etc/room-broker/factory-ca", Optional: true, Doc: "The CA of the factory's API certificate (`factoryCAFile`); empty is the system roots"},
	{ID: "runModel", Component: "run pods", Chart: "maku", Value: "runController.modelSecret", Default: "agent-model", Namespace: Runs,
		Keys:   []Key{{Name: "base_url", Required: true}, {Name: "model", Required: true}, {Name: "api_key", Required: true}, {Name: "judge_model"}},
		ReadAs: Env, Generated: GeneratedDemoModel, Doc: "The runs' model, in `runController.model.mode` secret"},
	{ID: "runGit", Component: "run pods", Chart: "maku", Value: "runController.gitSecret", Namespace: Runs,
		Keys: []Key{{Name: "token", Required: true}}, ReadAs: Env, Optional: true,
		Doc: "A laptop's one PAT, which every role mounts (demo-only: `demoValues`). Unset, each run mounts its own token, which the factory mints"},
	{ID: "factoryApp", Component: "agent-factory", Chart: "maku-factory", Value: "apps.factory.existingSecret",
		Default: "agent-factory-github", Namespace: Release, Keys: githubApp, ReadAs: File, MountPath: "/etc/agent-factory-github",
		Generated: GeneratedDemoApp, Doc: "The factory App"},
	{ID: "mergerApp", Component: "agent-factory", Chart: "maku-factory", Value: "apps.merger.existingSecret", Default: "agent-factory-merger", Namespace: Release,
		Keys: githubApp, ReadAs: File, MountPath: "/etc/agent-factory-merger", Optional: true, MountOptional: true, Doc: "The merger App (`apps.merger.mode` separate)"},
	{ID: "agentsApp", Component: "agent-factory", Chart: "maku-factory", Value: "apps.agents.existingSecret", Namespace: Release,
		Keys: githubApp, ReadAs: File, MountPath: "/etc/agent-factory-agents", Optional: true, Doc: "The agents App: each run's token and the publisher's"},
	{ID: "factoryOIDC", Component: "agent-factory", Chart: "maku-factory", Value: "oidcSecret", Default: "agent-factory-oidc", Namespace: Release,
		Keys: []Key{{Name: "<client>", Doc: "one key per file `config.api.clientIDFiles` names"}}, DynamicKeys: true, ReadAs: File,
		MountPath: "/etc/agent-factory-oidc", Doc: "The client ids the run API accepts human tokens from (ids, not secrets)"},
	{ID: "intake", Component: "agent-factory", Chart: "maku-factory", Value: "intakeSecret", Default: "agent-factory-intake", Namespace: Release,
		Keys: []Key{{Name: "token", Required: true}}, ReadAs: File, MountPath: "/etc/agent-factory-intake", Optional: true, MountOptional: true,
		Doc: "The RunLore intake's bearer token (`config.runlore.tokenFile`)"},
	{ID: "zitadelReader", Component: "agent-factory", Chart: "maku-factory", Value: "zitadelReaderSecret", Default: "agent-factory-zitadel-reader",
		Namespace: Release, Keys: []Key{{Name: "reader.json", Required: true, Doc: `{"pat", "githubIdpId"}`}}, ReadAs: File,
		MountPath: "/etc/agent-factory-zitadel-reader", Optional: true, MountOptional: true, Doc: "The ZITADEL reader (`config.api.access.readerFile`), until the GitHub link replaces it"},
	{ID: "factorySponsored", Component: "agent-factory", Chart: "maku-factory", Value: "sponsoredSecret", Default: "agent-factory-sponsored",
		Namespace: Release, Keys: []Key{{Name: "<name>-client-id", Doc: "one per sponsored principal"}, {Name: "project-id", Doc: "preset zitadel"}},
		DynamicKeys: true, ReadAs: File, MountPath: "/etc/agent-factory-sponsored", Optional: true, MountOptional: true, Doc: "The sponsored principals' client ids"},
	{ID: "factoryBrokerCA", Component: "agent-factory", Chart: "maku-factory", Value: "brokerCASecret", Default: "openbao-ca", Namespace: Release,
		Keys: caCrt, ReadAs: File, MountPath: "/etc/agent-factory/openbao-ca", Generated: GeneratedUmbrella, Doc: "The CA of the broker's pair (`config.broker.caFile`)"},
	{ID: "factoryAPITLS", Component: "agent-factory", Chart: "maku-factory", Value: "api.tls.secretName", Namespace: Release, Keys: tlsPair,
		ReadAs: File, MountPath: "/etc/agent-factory-api-tls", Optional: true, Generated: GeneratedCertManager, Doc: "The run API's serving pair (`api.tls`), cert-manager's when `issuerRef` is set"},
	{ID: "webProxy", Component: "rooms-web-proxy", Chart: "agent-factory", Value: "webProxy.existingSecret", Namespace: Release,
		Keys: []Key{{Name: "client-id", Required: true, From: "webProxy.keys.clientID", Doc: "the web client"},
			{Name: "client-secret", Required: true, From: "webProxy.keys.clientSecret"},
			{Name: "cookie-secret", Required: true, From: "webProxy.keys.cookieSecret", Doc: "32 random bytes, base64url"}},
		ReadAs: Env, Optional: true, Doc: "The web front door's client and cookie secret (`webProxy.enabled`)"},
	{ID: "slackApp", Component: "factory-slack-app", Chart: "maku-slack", ValuesKey: "slack", Value: "slackSecret", Default: "factory-slack-app",
		Namespace: Release, Keys: []Key{{Name: "signing-secret", Required: true}, {Name: "bot-token", Required: true}, {Name: "bot-user-id", Required: true}},
		ReadAs: File, MountPath: "/etc/factory-slack-app/slack", Optional: true, Doc: "The Slack App's signing secret and bot (`slack.enabled`)"},
	{ID: "slackPrincipal", Component: "factory-slack-app", Chart: "maku-slack", ValuesKey: "slack", Value: "principalSecret", Default: "factory-slack-principal",
		Namespace: Release, Keys: []Key{{Name: "client-id", Required: true}, {Name: "client-secret", Required: true}, {Name: "project-id", Doc: "preset zitadel"}},
		ReadAs: File, MountPath: "/etc/factory-slack-app/principal", Optional: true, Doc: "The Slack App's client credentials at the IdP: its sponsored principal"},
	{ID: "slackWebhook", Component: "factory-slack-app, room-broker", Chart: "maku-slack", ValuesKey: "slack", Value: "webhookSecret",
		Default: "room-broker-webhook-slack-app", Namespace: Release, Keys: []Key{{Name: "signing-key", Required: true}},
		ReadAs: File, MountPath: "/etc/factory-slack-app/webhook", Optional: true,
		Doc: "The key the broker signs the App's webhooks with; the broker mounts it too (`maku.broker.webhooks[slack-app].existingSecret`)"},
	{ID: "gatewayProvider", Component: "maku-gateway", Chart: "maku-gateway", Value: "providers", Namespace: Release,
		Keys: []Key{{Name: "apiKey", Required: true, Doc: "renamed by the provider's `key`"}}, ReadAs: API, Optional: true, Instances: true,
		Doc: "Each model provider's key: `providers.<name>.existingSecret`; its ExternalSecret id is `gatewayProvider.<name>`"},
}

// The markers between which docs/install-secrets.md holds Table's output; gen-seams writes between them.
const (
	TableStart = "<!-- seams:start (task chart:seams) -->"
	TableEnd   = "<!-- seams:end -->"
)

// Table is the docs' table of every seam.
func Table() string {
	var b strings.Builder
	b.WriteString("| Id | Read by | Value (chart) | Default name | Namespace | Keys | Read as | Optional: what it holds |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, s := range Seams {
		value := "(fixed)"
		if s.Value != "" {
			value = "`" + s.Value + "`"
		}
		value += " (" + s.Chart + ")"
		def := "none"
		if s.Default != "" {
			def = "`" + s.Default + "`"
		}
		keys := make([]string, 0, len(s.Keys))
		for _, k := range s.Keys {
			k1 := "`" + k.Name + "`"
			if k.Required {
				k1 += " (required)"
			}
			if k.From != "" {
				k1 += ", renamed by `" + k.From + "`"
			}
			keys = append(keys, k1)
		}
		read := string(s.ReadAs)
		if s.MountPath != "" {
			read += " `" + s.MountPath + "`"
		}
		opt := "no"
		if s.Optional {
			opt = "yes"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %s. %s |\n", s.ID, s.Component, value, def, s.Namespace,
			strings.Join(keys, ", "), read, opt, s.Doc)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// Template is charts/agent-factory/templates/_seams.tpl: the seams as JSON, for the umbrella's
// ExternalSecrets.
func Template() (string, error) { return template("afumbrella.seams", "") }

// MakuTemplate is charts/maku/templates/_seams.tpl: the maku chart's own seams, for its preflight's
// Secret checks (v4 D24), whose names the chart resolves from its own values.
func MakuTemplate() (string, error) { return template("ap.seams", "maku") }

// template is a define named name holding the seams of chart ("" is all), as JSON.
func template(name, chart string) (string, error) {
	seams := Seams
	if chart != "" {
		seams = nil
		for _, s := range Seams {
			if s.Chart == chart {
				seams = append(seams, s)
			}
		}
	}
	raw, err := json.Marshal(map[string][]Seam{"seams": seams})
	if err != nil {
		return "", err
	}
	return "{{/* Generated by task chart:seams from internal/install/secrets.go (v4 D14): do not edit. */}}\n" +
		"{{- define \"" + name + "\" -}}\n" + string(raw) + "\n{{- end -}}\n", nil
}

// Names maps every Secret name an install with these umbrella values reads to its seam id: each
// seam's value (or its alias), else its default, and every provider's existingSecret.
func Names(umbrella map[string]any) map[string]string {
	out := map[string]string{}
	for _, s := range Seams {
		vals := umbrella
		if s.Chart != "agent-factory" {
			vals, _ = umbrella[s.UmbrellaKey()].(map[string]any)
		}
		if s.Instances {
			entries, _ := dig(vals, s.Value).(map[string]any)
			for _, e := range entries {
				if m, ok := e.(map[string]any); ok {
					if n, ok := m["existingSecret"].(string); ok && n != "" {
						out[n] = s.ID
					}
				}
			}
			continue
		}
		name, _ := dig(vals, s.Value).(string)
		if name == "" {
			name = s.Default
		}
		if name != "" {
			out[name] = s.ID
		}
	}
	return out
}

func dig(v map[string]any, p string) any {
	if p == "" || v == nil {
		return nil
	}
	var cur any = v
	for _, step := range strings.Split(p, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[step]
	}
	return cur
}

// Lookup is the seam and key of a file a component reads, by the chart's mount path.
func Lookup(file string) (Seam, string, bool) {
	dir, key := path.Split(path.Clean(file))
	dir = strings.TrimSuffix(dir, "/")
	for _, s := range Seams {
		if s.MountPath != "" && s.MountPath == dir && key != "" {
			return s, key, true
		}
	}
	return Seam{}, "", false
}

// CheckFiles refuses a file under a seam's mount that is missing or holds nothing but space,
// naming the seam, its Secret and the key. A file under no mount is another check's, and one under
// an optional mount is refused at use, as before. read is os.ReadFile outside tests.
func CheckFiles(read func(string) ([]byte, error), files ...string) error {
	var errs []error
	for _, f := range files {
		s, key, ok := Lookup(f)
		if !ok || s.MountOptional {
			continue
		}
		if b, err := read(f); err == nil && strings.TrimSpace(string(b)) != "" {
			continue
		}
		name := s.Default
		if name == "" {
			name = s.Value
		}
		errs = append(errs, fmt.Errorf("%s: Secret %s key %s is missing or empty (docs/install-secrets.md)", s.ID, name, key))
	}
	return errors.Join(errs...)
}
