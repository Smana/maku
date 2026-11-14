// SPDX-License-Identifier: Apache-2.0

package install

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Kind is the type of a question's answer.
type Kind int

const (
	// KindString is one line of text.
	KindString Kind = iota
	// KindBool is yes or no.
	KindBool
	// KindList is a list, typed as commas or spaces on one line.
	KindList
)

// Question is one seam the wizard asks about (IN7). Values is what an answer writes into the
// umbrella's values, by dotted path: each path is checked against the charts' own schemas
// (ValidateValue), so the wizard refuses what the chart refuses. Check adds what no schema says.
type Question struct {
	ID     string
	Prompt string
	Kind   Kind
	// Default is the answer when none is given; nil (or a nil result) makes the question required.
	Default func(a Answers) any
	// When gates the question on earlier answers; nil is always asked.
	When func(a Answers) bool
	// Keys are the values paths (prefixes) the question writes: the IN7 table's last column.
	Keys []string
	// Values is the answer's values, by dotted path. Lists at one path from two questions append.
	Values func(v any, a Answers) map[string]any
	// Check refuses what no schema can (an error), or warns (a non-empty warning).
	Check func(v any, a Answers) (warning string, err error)
}

// Validate checks v as q's answer: q.Check first (the shape no schema states, so Values may parse
// it), then every value it writes against the charts' schemas.
func (q Question) Validate(v any, a Answers) (string, error) {
	var warning string
	if q.Check != nil {
		w, err := q.Check(v, a)
		if err != nil {
			return "", err
		}
		warning = w
	}
	if q.Values != nil {
		vals := q.Values(v, a)
		for _, p := range sortedKeys(vals) {
			if err := ValidateValue(p, vals[p]); err != nil {
				return "", err
			}
		}
	}
	return warning, nil
}

// Asked reports whether q applies under the answers so far.
func (q Question) Asked(a Answers) bool { return q.When == nil || q.When(a) }

// Answer is q's default under a, or nil when it has none.
func (q Question) defaultValue(a Answers) any {
	if q.Default == nil {
		return nil
	}
	return q.Default(a)
}

var (
	dnsLabelRE     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	ownerRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	repoRE         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
	loginRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	labelRE        = regexp.MustCompile(`^([a-z0-9.-]+/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?=[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	pinRE          = regexp.MustCompile(`^(maku-[a-z-]+)=([A-Za-z0-9_.-]+@sha256:[0-9a-f]{64})$`)
	slackIDRE      = regexp.MustCompile(`^[CT][A-Z0-9]{2,}$`)
	priceRE        = regexp.MustCompile(`^([A-Za-z0-9._-]+)=([0-9]+(\.[0-9]+)?)/([0-9]+(\.[0-9]+)?)/([0-9]+(\.[0-9]+)?)$`)
)

// InClusterIssuer reports whether a ServiceAccount issuer is the in-cluster API server
// (kind, k3s, kubeadm): its JWKS is reachable only from inside, and only with the discovery binding.
func InClusterIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "kubernetes.default.svc" || strings.HasPrefix(h, "kubernetes.default.svc.") || h == "kubernetes.default"
}

// Images are the maku images an install pins, by the name an images.pins entry gives.
var Images = []string{"maku-broker", "maku-run-controller", "maku-harness", "maku-bridge", "maku-factory", "maku-slack"}

func str(v any) string              { s, _ := v.(string); return s }
func list(v any) []string           { l, _ := v.([]string); return l }
func boolean(v any) bool            { b, _ := v.(bool); return b }
func fixed(v any) func(Answers) any { return func(Answers) any { return v } }
func is(id string, want ...string) func(Answers) bool {
	return func(a Answers) bool { return slices.Contains(want, a.String(id)) }
}
func on(id string) func(Answers) bool  { return func(a Answers) bool { return a.Bool(id) } }
func off(id string) func(Answers) bool { return func(a Answers) bool { return !a.Bool(id) } }
func set(id string) func(Answers) bool { return func(a Answers) bool { return a.String(id) != "" } }

func dnsLabel(what string) func(v any, _ Answers) (string, error) {
	return func(v any, _ Answers) (string, error) {
		if !dnsLabelRE.MatchString(str(v)) || len(str(v)) > 63 {
			return "", fmt.Errorf("%s %q is not a DNS label (lowercase letters, digits and dashes)", what, str(v))
		}
		return "", nil
	}
}

func httpsURL(what string) func(v any, _ Answers) (string, error) {
	return func(v any, _ Answers) (string, error) {
		u, err := url.Parse(str(v))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("%s %q is not an https URL", what, str(v))
		}
		return "", nil
	}
}

// host is u's hostname, "" when u does not parse.
func host(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}

// peers is a NetworkPolicy peer list for a namespace's pods, in the network mode's own shape (F10):
// kubernetes NetworkPolicy peers, or Cilium endpoint selectors.
func peers(mode, ns string) []any {
	if mode == "cilium" {
		return []any{map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": ns}}}
	}
	return []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": ns}}}}
}

// labels reads "k=v" pairs.
func labels(l []string) map[string]any {
	m := map[string]any{}
	for _, kv := range l {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// Questions are the wizard's questions, in the order it asks them (IN7). An answers file's keys are
// their ids; the README's next steps are written from the same answers.
var Questions []Question

// init builds the table: its closures read other questions' defaults, through Questions itself.
func init() { Questions = questionTable() }

func questionTable() []Question {
	return []Question{
		// The install.
		{ID: "namespace", Prompt: "The release namespace", Kind: KindString, Default: fixed("maku"),
			Check: dnsLabel("the namespace")},
		{ID: "release", Prompt: "The Helm release name", Kind: KindString, Default: fixed("agent-factory"),
			Check: dnsLabel("the release name")},
		{ID: "version", Prompt: "The maku version to install (the chart's and the images')", Kind: KindString,
			Default: func(Answers) any { return defaultVersion() },
			Check: func(v any, _ Answers) (string, error) {
				if !regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`).MatchString(str(v)) {
					return "", fmt.Errorf("the version %q is not a release such as v0.48.0", str(v))
				}
				return "", nil
			}},
		{ID: "images.pins", Prompt: "Image digests, name=<version>@sha256:<digest> (empty: read from ghcr.io)", Kind: KindList,
			Default: fixed([]string{}),
			Check: func(v any, _ Answers) (string, error) {
				for _, p := range list(v) {
					m := pinRE.FindStringSubmatch(p)
					if m == nil || !slices.Contains(Images, m[1]) {
						return "", fmt.Errorf("images.pins %q is not <image>=<version>@sha256:<64 hex>, <image> one of %s", p, strings.Join(Images, ", "))
					}
				}
				return "", nil
			}},

		// GitHub (IN7: GitHub).
		{ID: "github.url", Prompt: "GitHub's site: https://github.com, or your GHES (3.14 or later)", Kind: KindString,
			Default: fixed("https://github.com"), Keys: []string{"maku.broker.githubLink.webURL"},
			Values: func(v any, _ Answers) map[string]any {
				if strings.TrimSuffix(str(v), "/") == "https://github.com" {
					return nil
				}
				return map[string]any{"maku.broker.githubLink.webURL": strings.TrimSuffix(str(v), "/")}
			},
			Check: httpsURL("GitHub's site")},
		{ID: "github.owner", Prompt: "The organization (or user) that owns the repositories and the Apps", Kind: KindString,
			Check: func(v any, _ Answers) (string, error) {
				if !ownerRE.MatchString(str(v)) {
					return "", fmt.Errorf("%q is not a GitHub organization or user name", str(v))
				}
				return "", nil
			}},
		{ID: "repositories", Prompt: "The repositories the factory serves, owner/name", Kind: KindList,
			Check: func(v any, _ Answers) (string, error) {
				if len(list(v)) == 0 {
					return "", errors.New("name at least one repository: the factory starts only with one to serve")
				}
				for _, r := range list(v) {
					if !repoRE.MatchString(r) {
						return "", fmt.Errorf("%q is not owner/name", r)
					}
				}
				return "", nil
			}},
		{ID: "maintainers", Prompt: "The GitHub logins who may file work for the factory", Kind: KindList,
			Check: func(v any, _ Answers) (string, error) {
				if len(list(v)) == 0 {
					return "", errors.New("name at least one maintainer")
				}
				for _, l := range list(v) {
					if !loginRE.MatchString(l) {
						return "", fmt.Errorf("%q is not a GitHub login", l)
					}
				}
				return "", nil
			}},
		{ID: "checks", Prompt: "The CI check names a merge waits for", Kind: KindList, Default: fixed([]string{"ci"}),
			Check: func(v any, _ Answers) (string, error) {
				if len(list(v)) == 0 {
					return "", errors.New("name at least one check: the gate merges nothing whose CI it cannot read")
				}
				return "", nil
			}},

		// The two Apps (IN7: Apps, IN8).
		{ID: "apps.create", Prompt: "Create the agents App and the factory App now, through GitHub's manifest flow", Kind: KindBool,
			Default: fixed(true)},
		{ID: "apps.name", Prompt: "The Apps' name prefix (each App adds -agents or -factory)", Kind: KindString,
			When: on("apps.create"), Default: func(a Answers) any { return trunc("maku-"+strings.ToLower(a.String("github.owner")), 26) },
			Check: func(v any, _ Answers) (string, error) {
				if s := str(v); s == "" || len(s) > 26 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`).MatchString(s) {
					return "", fmt.Errorf("the prefix %q has 1 to 26 letters, digits or dashes: GitHub caps an App's name at 34", s)
				}
				return "", nil
			}},
		{ID: "apps.factorySlug", Prompt: "The existing factory App's slug", Kind: KindString, When: off("apps.create"),
			Check: slug},
		{ID: "apps.factoryID", Prompt: "The existing factory App's id (its ruleset bypass and status)", Kind: KindString, When: off("apps.create"),
			Check: func(v any, _ Answers) (string, error) {
				if !regexp.MustCompile(`^[1-9][0-9]{0,11}$`).MatchString(str(v)) {
					return "", fmt.Errorf("the App id %q is not a number", str(v))
				}
				return "", nil
			}},
		{ID: "apps.agentsSlug", Prompt: "The existing agents App's slug", Kind: KindString, When: off("apps.create"), Check: slug},
		{ID: "apps.factorySecret", Prompt: "The existing factory App's Secret (keys app_id, private_key)", Kind: KindString,
			When: off("apps.create"), Default: fixed("agent-factory-github"),
			Keys: []string{"maku-factory.apps.factory.existingSecret"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku-factory.apps.factory.existingSecret": str(v)}
			}, Check: dnsSubdomain("the Secret name")},
		{ID: "apps.agentsSecret", Prompt: "The existing agents App's Secret (keys app_id, private_key)", Kind: KindString,
			When: off("apps.create"), Default: fixed("agent-factory-agents"),
			Keys: []string{"maku-factory.apps.agents.existingSecret"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku-factory.apps.agents.existingSecret": str(v)}
			}, Check: dnsSubdomain("the Secret name")},

		// Identity (IN7: Identity, F4, F6).
		{ID: "web.url", Prompt: "The https URL people reach the rooms at", Kind: KindString,
			Keys: []string{"maku.broker.publicURL", "maku.broker.human.origin", "expose.web.hostnames"},
			Values: func(v any, _ Answers) map[string]any {
				u := strings.TrimSuffix(str(v), "/")
				return map[string]any{"maku.broker.publicURL": u, "maku.broker.human.origin": u, "expose.web.hostnames": []any{host(u)}}
			}, Check: httpsURL("the web URL")},
		{ID: "identity.issuer", Prompt: "The OIDC issuer people sign in at", Kind: KindString,
			Keys: []string{"maku.broker.human.issuer", "maku-factory.config.api.humanIssuer"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku.broker.human.issuer": str(v), "maku-factory.config.api.humanIssuer": str(v)}
			}, Check: httpsURL("the issuer")},
		{ID: "identity.preset", Prompt: "The issuer's preset: generic (any OIDC issuer) or zitadel", Kind: KindString,
			Default: fixed("generic"), Keys: []string{"maku.broker.human.preset", "maku-factory.config.api.preset"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku.broker.human.preset": str(v), "maku-factory.config.api.preset": str(v)}
			}, Check: oneOf("the preset", "generic", "zitadel")},
		{ID: "identity.family", Prompt: "The identity provider (dex, keycloak, zitadel, okta, entra, other): only to warn", Kind: KindString,
			Default: func(a Answers) any {
				if a.String("identity.preset") == "zitadel" {
					return "zitadel"
				}
				return "other"
			}, Check: oneOf("the identity provider", "dex", "keycloak", "zitadel", "okta", "entra", "other")},
		{ID: "identity.tokenUse", Prompt: "Which token the broker verifies: id or access", Kind: KindString,
			Default: func(a Answers) any {
				if a.String("identity.preset") == "zitadel" {
					return "access"
				}
				return "id"
			}, Keys: []string{"maku.broker.human.tokenUse", "maku-factory.config.api.tokenUse"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku.broker.human.tokenUse": str(v), "maku-factory.config.api.tokenUse": str(v)}
			}, Check: oneOf("the token use", "id", "access")},
		{ID: "identity.rolesClaim", Prompt: "The claim holding the groups (empty: the preset's)", Kind: KindString,
			Default: func(a Answers) any {
				if a.String("identity.preset") == "zitadel" {
					return ""
				}
				return "groups"
			}, Keys: []string{"maku.broker.human.rolesClaim", "maku-factory.config.api.rolesClaim"},
			Values: func(v any, _ Answers) map[string]any {
				if str(v) == "" {
					return nil
				}
				return map[string]any{"maku.broker.human.rolesClaim": str(v), "maku-factory.config.api.rolesClaim": str(v)}
			},
			Check: func(v any, a Answers) (string, error) {
				// F4: Keycloak puts realm roles in the access token only.
				if a.String("identity.family") == "keycloak" && a.String("identity.tokenUse") == "id" && str(v) != "groups" {
					return "Keycloak puts realm roles in the access token only: under tokenUse id, add a groups mapper to the client and use rolesClaim groups", nil
				}
				return "", nil
			}},
		{ID: "identity.adminGroup", Prompt: "The group whose members administer the factory", Kind: KindString, Default: fixed("agents-admin"),
			Keys: []string{"maku.broker.human.groups.admin", "maku-factory.config.api.groups.admin"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku.broker.human.groups.admin": str(v), "maku-factory.config.api.groups.admin": str(v)}
			}, Check: nonEmpty("the admin group")},
		{ID: "identity.memberGroup", Prompt: "The group whose members use the factory", Kind: KindString, Default: fixed("agents-member"),
			Keys: []string{"maku.broker.human.groups.member", "maku-factory.config.api.groups.member"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku.broker.human.groups.member": str(v), "maku-factory.config.api.groups.member": str(v)}
			}, Check: nonEmpty("the member group")},
		{ID: "identity.projectID", Prompt: "The ZITADEL project id the tokens' audience is pinned to", Kind: KindString,
			When: is("identity.preset", "zitadel"), Keys: []string{"maku-factory.config.api.projectIDFile"},
			Values: func(any, Answers) map[string]any {
				return map[string]any{"maku-factory.config.api.projectIDFile": "/etc/agent-factory-oidc/project-id"}
			},
			Check: func(v any, _ Answers) (string, error) {
				if !regexp.MustCompile(`^[0-9]{6,32}$`).MatchString(str(v)) {
					return "", fmt.Errorf("%q is not a ZITADEL project id", str(v))
				}
				return "", nil
			}},
		{ID: "identity.webClientID", Prompt: "The web client's id at the issuer (oauth2-proxy signs people in with it)", Kind: KindString,
			Check: nonEmpty("the web client id")},
		{ID: "identity.cliClientID", Prompt: "The CLI client's id at the issuer (maku login, a public client)", Kind: KindString,
			Check: nonEmpty("the CLI client id")},

		// The cluster.
		{ID: "cluster.issuer", Prompt: "This cluster's ServiceAccount issuer (kubectl get --raw /.well-known/openid-configuration)", Kind: KindString,
			Keys: []string{"maku.broker.runIssuer", "maku.broker.systemIssuer", "maku-gateway.identity.issuer"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku.broker.runIssuer.issuer": str(v), "maku.broker.systemIssuer.issuer": str(v), "maku-gateway.identity.issuer": str(v)}
			}, Check: httpsURL("the cluster's issuer")},
		{ID: "cluster.jwksURL", Prompt: "Its JWKS URL", Kind: KindString,
			Default: func(a Answers) any { return strings.TrimSuffix(a.String("cluster.issuer"), "/") + "/openid/v1/jwks" },
			Keys: []string{"maku.broker.runIssuer.jwksURL", "maku.broker.systemIssuer.jwksURL", "maku-gateway.identity.jwks.url",
				"maku-gateway.identity.jwks.source", "maku-gateway.identity.jwks.allowAnonymousDiscovery"},
			Values: func(v any, a Answers) map[string]any {
				out := map[string]any{"maku.broker.runIssuer.jwksURL": str(v), "maku.broker.systemIssuer.jwksURL": str(v), "maku-gateway.identity.jwks.url": str(v)}
				if InClusterIssuer(a.String("cluster.issuer")) {
					// The API server's JWKS, read with no credential (docs/gateway.md): agentgateway's
					// controller verifies it with kube-root-ca.crt, and the discovery binding admits it.
					out["maku-gateway.identity.jwks.source"] = "apiserver"
					out["maku-gateway.identity.jwks.allowAnonymousDiscovery"] = true
				}
				return out
			}, Check: httpsURL("the JWKS URL")},

		{ID: "tls.issuer", Prompt: "The cert-manager ClusterIssuer for the broker's certificate (empty: bring room-broker-tls and room-broker-ca)", Kind: KindString,
			Default: fixed(""), Keys: []string{"maku.broker.tls.issuerRef"},
			Values: func(v any, _ Answers) map[string]any {
				if str(v) == "" {
					return nil
				}
				return map[string]any{"maku.broker.tls.issuerRef": map[string]any{"kind": "ClusterIssuer", "name": str(v)}}
			}},

		{ID: "factory.insecure", Prompt: "No ClusterIssuer: run the factory's API in plaintext (humans' bearers cross the cluster in clear; the banner says so)", Kind: KindBool,
			When: func(a Answers) bool { return a.String("tls.issuer") == "" },
			Check: func(v any, _ Answers) (string, error) {
				if !boolean(v) {
					return "", errors.New("the broker forwards each human's bearer to the factory's API: name a ClusterIssuer (tls.issuer), or accept plaintext with factory.insecure: true")
				}
				return "", nil
			}},

		// Database (IN7: Database).
		{ID: "database.brokerSecret", Prompt: "The broker's DSN Secret (role rooms_broker)", Kind: KindString, Default: fixed("rooms-broker-dsn"),
			Keys: []string{"maku.broker.database.brokerSecret"}, Values: at("maku.broker.database.brokerSecret"), Check: dnsSubdomain("the Secret name")},
		{ID: "database.ownerSecret", Prompt: "The migrations' DSN Secret (role rooms_owner)", Kind: KindString, Default: fixed("rooms-owner-dsn"),
			Keys: []string{"maku.broker.database.ownerSecret"}, Values: at("maku.broker.database.ownerSecret"), Check: dnsSubdomain("the Secret name")},
		{ID: "database.retentionSecret", Prompt: "The retention job's DSN Secret (role rooms_retention)", Kind: KindString, Default: fixed("rooms-retention-dsn"),
			Keys: []string{"maku.broker.database.retentionSecret"}, Values: at("maku.broker.database.retentionSecret"), Check: dnsSubdomain("the Secret name")},
		{ID: "database.key", Prompt: "The key holding the DSN in each of them", Kind: KindString, Default: fixed("uri"),
			Keys: []string{"maku.broker.database.secretKey"}, Values: at("maku.broker.database.secretKey"), Check: nonEmpty("the key")},
		{ID: "database.caSecret", Prompt: "The database's CA Secret, key ca.crt (empty: none)", Kind: KindString, Default: fixed(""),
			Keys: []string{"maku.broker.database.caSecret"},
			Values: func(v any, _ Answers) map[string]any {
				if str(v) == "" {
					return nil
				}
				return map[string]any{"maku.broker.database.caSecret": str(v)}
			}},
		{ID: "database.sslmode", Prompt: "The DSNs' sslmode", Kind: KindString,
			Default: func(a Answers) any {
				if a.String("database.caSecret") != "" {
					return "verify-full"
				}
				return "require"
			}, Check: oneOf("sslmode", "disable", "require", "verify-ca", "verify-full")},
		{ID: "database.cidrs", Prompt: "A database outside the cluster: its CIDRs (empty: CNPG pods in the release namespace)", Kind: KindList,
			Default: fixed([]string{}), Keys: []string{"maku.broker.database.egress.cidrs"},
			Values: func(v any, _ Answers) map[string]any {
				if len(list(v)) == 0 {
					return nil
				}
				return map[string]any{"maku.broker.database.egress.cidrs": anys(list(v))}
			}},

		// Sandbox (IN7: Sandbox, D18).
		{ID: "sandbox.runtimeClassName", Prompt: "The RuntimeClass every run runs under (gvisor, GKE Sandbox's included; kata; ...)", Kind: KindString,
			Default: fixed("gvisor"), Keys: []string{"maku.sandbox.runtimeClassName"}, Values: at("maku.sandbox.runtimeClassName"),
			Check: dnsSubdomain("the RuntimeClass")},

		// Network (IN7: Network).
		{ID: "network.mode", Prompt: "The network policies: kubernetes (NetworkPolicy) or cilium", Kind: KindString, Default: fixed("kubernetes"),
			Keys: []string{"maku.networkPolicies.mode"}, Values: at("maku.networkPolicies.mode")},
		// Models (IN7: Models, D7).
		{ID: "models.provider", Prompt: "The model provider (openai, anthropic, zai, mistral, moonshot, custom)", Kind: KindString,
			Default: fixed("anthropic"), Keys: []string{"maku-gateway.providers"},
			Values: func(v any, a Answers) map[string]any {
				p := map[string]any{"preset": str(v), "existingSecret": defaultStr(a, "models.secret", "model-provider"), "key": defaultStr(a, "models.key", "apiKey")}
				return map[string]any{"maku-gateway.providers." + providerName(str(v)): p}
			}},
		{ID: "models.host", Prompt: "The custom provider's host (an OpenAI-compatible API on 443)", Kind: KindString, When: is("models.provider", "custom"),
			Keys: []string{"maku-gateway.providers.custom.host"}, Values: at("maku-gateway.providers.custom.host"),
			Check: func(v any, _ Answers) (string, error) {
				if !dnsSubdomainRE.MatchString(str(v)) {
					return "", fmt.Errorf("%q is not a host name", str(v))
				}
				return "", nil
			}},
		{ID: "models.secret", Prompt: "The Secret holding the provider's key", Kind: KindString, Default: fixed("model-provider"), Check: dnsSubdomain("the Secret name")},
		{ID: "models.key", Prompt: "Its key", Kind: KindString, Default: fixed("apiKey"), Check: nonEmpty("the key")},
		{ID: "models.default", Prompt: "The model runs use (alias agent-default)", Kind: KindString,
			Keys: []string{"maku-gateway.aliases.public.agent-default"},
			Values: func(v any, a Answers) map[string]any {
				return map[string]any{"maku-gateway.aliases.public.agent-default": map[string]any{"provider": providerName(defaultStr(a, "models.provider", "anthropic")), "model": str(v)}}
			}, Check: nonEmpty("the model")},
		{ID: "models.light", Prompt: "The lighter model the judges use (alias tier-light)", Kind: KindString,
			Default: func(a Answers) any { return a.String("models.default") },
			Keys:    []string{"maku-gateway.aliases.public.tier-light"},
			Values: func(v any, a Answers) map[string]any {
				return map[string]any{"maku-gateway.aliases.public.tier-light": map[string]any{"provider": providerName(defaultStr(a, "models.provider", "anthropic")), "model": str(v)}}
			}, Check: nonEmpty("the model")},
		{ID: "models.controller", Prompt: "Install agentgateway's controller with the release (no: the cluster runs one)", Kind: KindBool,
			Default: fixed(true), Keys: []string{"agentgateway.enabled"}, Values: at("agentgateway.enabled")},

		{ID: "models.prices", Prompt: "Each model's price in USD per million tokens, model=input/cacheRead/output (required with the Slack App's USD ceilings)", Kind: KindList,
			Default: func(a Answers) any {
				if a.Bool("slack.enabled") {
					return nil
				}
				return []string{}
			}, Keys: []string{"maku-factory.config.meter.prices"},
			Values: func(v any, _ Answers) map[string]any {
				if len(list(v)) == 0 {
					return nil
				}
				return map[string]any{"maku-factory.config.meter.prices": prices(list(v))}
			},
			Check: func(v any, _ Answers) (string, error) {
				for _, p := range list(v) {
					if !priceRE.MatchString(p) {
						return "", fmt.Errorf("%q is not model=input/cacheRead/output, in USD per million tokens", p)
					}
				}
				return "", nil
			}},
		// The meter: outside the demo the factory refuses to start without one (step 6 PR G).
		{ID: "meter.url", Prompt: "A Prometheus-compatible URL holding agentgateway's metrics (the factory's meter)", Kind: KindString,
			Keys: []string{"maku-factory.config.meter.url"}, Values: at("maku-factory.config.meter.url"),
			Check: func(v any, _ Answers) (string, error) {
				u, err := url.Parse(str(v))
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return "", fmt.Errorf("the meter URL %q is not an http(s) URL", str(v))
				}
				return "", nil
			}},
		{ID: "meter.namespace", Prompt: "The namespace of the pods that serve it (the factory's egress)", Kind: KindString,
			When: func(a Answers) bool { return internalHost(host(a.String("meter.url"))) }, Default: fixed("monitoring"),
			Keys: []string{"networkPolicies.factory.meter.to"},
			Values: func(v any, a Answers) map[string]any {
				return map[string]any{"networkPolicies.factory.meter.to": peers(defaultStr(a, "network.mode", "kubernetes"), str(v))}
			}, Check: dnsLabel("the namespace")},

		// Exposure (IN7: Exposure).
		{ID: "expose.mode", Prompt: "Expose the web through: httproute (Gateway API), ingress, or none", Kind: KindString, Default: fixed("httproute"),
			Keys: []string{"expose.mode"}, Values: at("expose.mode")},
		{ID: "expose.gateway", Prompt: "The Gateway the routes attach to, namespace/name[/listener]", Kind: KindString, When: is("expose.mode", "httproute"),
			Keys: []string{"expose.httproute.parentRefs"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"expose.httproute.parentRefs": []any{parentRef(str(v))}}
			},
			Check: func(v any, _ Answers) (string, error) {
				parts := strings.Split(str(v), "/")
				if len(parts) < 2 || len(parts) > 3 || !dnsLabelRE.MatchString(parts[0]) || !dnsSubdomainRE.MatchString(parts[1]) {
					return "", fmt.Errorf("%q is not namespace/name[/listener]", str(v))
				}
				return "", nil
			}},
		{ID: "expose.ingressClass", Prompt: "The IngressClass", Kind: KindString, When: is("expose.mode", "ingress"),
			Keys: []string{"expose.ingress.className"}, Values: at("expose.ingress.className"), Check: dnsSubdomain("the IngressClass")},
		{ID: "expose.tlsSecret", Prompt: "The web's TLS Secret, for the Ingress (empty: the controller's default)", Kind: KindString,
			When: is("expose.mode", "ingress"), Default: fixed(""), Keys: []string{"expose.web.tls"},
			Values: func(v any, a Answers) map[string]any {
				if str(v) == "" {
					return nil
				}
				return map[string]any{"expose.web.tls": []any{map[string]any{"secretName": str(v), "hosts": []any{host(a.String("web.url"))}}}}
			}},

		{ID: "network.gatewayNamespace", Prompt: "The namespace of your Gateway's or ingress controller's pods", Kind: KindString,
			When: func(a Answers) bool {
				return a.String("expose.mode") != "none" && a.String("network.mode") == "kubernetes"
			},
			Keys: []string{"expose.networkPolicy.from"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"expose.networkPolicy.from": peers("kubernetes", str(v))}
			}, Check: dnsLabel("the namespace")},

		// Slack (IN7: Slack, D20).
		{ID: "slack.enabled", Prompt: "Install the Slack App", Kind: KindBool, Default: fixed(false),
			Keys: []string{"slack.enabled"}, Values: at("slack.enabled"),
			Check: func(v any, a Answers) (string, error) {
				// F4: the App signs in with client credentials, which Dex refuses.
				if boolean(v) && a.String("identity.family") == "dex" {
					return "", errors.New("the Slack App signs in with client credentials, which Dex refuses (#524): use another issuer, or no Slack App")
				}
				return "", nil
			}},
		{ID: "slack.host", Prompt: "The public host Slack posts its events to", Kind: KindString, When: on("slack.enabled"),
			Keys: []string{"expose.slack.hostnames"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"expose.slack.hostnames": []any{str(v)}}
			}},
		{ID: "slack.gateway", Prompt: "The public Gateway Slack's route attaches to, namespace/name[/listener]", Kind: KindString,
			When: func(a Answers) bool { return a.Bool("slack.enabled") && a.String("expose.mode") == "httproute" },
			Keys: []string{"expose.slack.parentRefs"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"expose.slack.parentRefs": []any{parentRef(str(v))}}
			}},
		{ID: "slack.clientID", Prompt: "The Slack App's machine client id at the issuer (client credentials)", Kind: KindString,
			When: on("slack.enabled"), Check: nonEmpty("the client id")},
		{ID: "slack.teamID", Prompt: "Your Slack workspace's id (T...)", Kind: KindString, When: on("slack.enabled"),
			Keys: []string{"slack.config.teamID"}, Values: at("slack.config.teamID"),
			Check: func(v any, _ Answers) (string, error) {
				if !slackIDRE.MatchString(str(v)) || str(v)[0] != 'T' {
					return "", fmt.Errorf("%q is not a Slack team id", str(v))
				}
				return "", nil
			}},
		{ID: "slack.channel", Prompt: "The Slack channel id (C...) whose mentions file work", Kind: KindString, When: on("slack.enabled"),
			Check: func(v any, _ Answers) (string, error) {
				if !slackIDRE.MatchString(str(v)) || str(v)[0] != 'C' {
					return "", fmt.Errorf("%q is not a Slack channel id", str(v))
				}
				return "", nil
			}},

		// Monitoring (IN7: Monitoring, F12).
		{ID: "monitoring.enabled", Prompt: "Render the ServiceMonitors, the PrometheusRule and the dashboards (the prometheus-operator CRDs must exist)", Kind: KindBool,
			Default: fixed(true), Keys: []string{"monitoring.serviceMonitor.enabled", "monitoring.prometheusRule.enabled", "monitoring.dashboards.enabled"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"monitoring.serviceMonitor.enabled": boolean(v), "monitoring.prometheusRule.enabled": boolean(v), "monitoring.dashboards.enabled": boolean(v)}
			}},
		{ID: "monitoring.namespace", Prompt: "Your Prometheus pods' namespace (admitted on :9090)", Kind: KindString, When: on("monitoring.enabled"),
			Default: fixed("monitoring"),
			Keys:    []string{"maku.networkPolicies.kubernetes.metricsFrom", "monitoring.serviceMonitor.cilium.fromEndpoints", "maku-gateway.metrics.allowFrom"},
			Values: func(v any, a Answers) map[string]any {
				m := map[string]any{"maku-gateway.metrics.allowFrom": peers("kubernetes", str(v))}
				if defaultStr(a, "network.mode", "kubernetes") == "cilium" {
					m["monitoring.serviceMonitor.cilium.fromEndpoints"] = peers("cilium", str(v))
				} else {
					m["maku.networkPolicies.kubernetes.metricsFrom"] = peers("kubernetes", str(v))
				}
				return m
			}, Check: dnsLabel("the namespace")},
		{ID: "monitoring.sidecarLabel", Prompt: "The label Grafana's dashboard sidecar watches, k=v", Kind: KindString, When: on("monitoring.enabled"),
			Default: fixed("grafana_dashboard=1"), Keys: []string{"monitoring.dashboards.labels"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"monitoring.dashboards.labels": labels([]string{str(v)})}
			},
			Check: func(v any, _ Answers) (string, error) {
				if !labelRE.MatchString(str(v)) {
					return "", fmt.Errorf("%q is not a label, k=v", str(v))
				}
				return "", nil
			}},

		// Tracing (IN7: Tracing, F8).
		{ID: "tracing.endpoint", Prompt: "The runs' OTLP/HTTP collector, http(s)://<svc>.<ns>.svc.<domain>:<port> (empty: no traces)", Kind: KindString,
			Default: fixed(""), Keys: []string{"maku.runController.tracing.otlpEndpoint", "maku.runController.tracing.collector.namespace"},
			Values: func(v any, _ Answers) map[string]any {
				if str(v) == "" {
					return nil
				}
				return map[string]any{"maku.runController.tracing.otlpEndpoint": str(v), "maku.runController.tracing.collector.namespace": endpointNamespace(str(v))}
			}, Check: tracingEndpoint},
		{ID: "tracing.collectorLabels", Prompt: "The collector pods' labels, k=v", Kind: KindList, When: set("tracing.endpoint"),
			Keys: []string{"maku.runController.tracing.collector.labels"},
			Values: func(v any, _ Answers) map[string]any {
				return map[string]any{"maku.runController.tracing.collector.labels": labels(list(v))}
			},
			Check: func(v any, _ Answers) (string, error) {
				if len(list(v)) == 0 {
					return "", errors.New("name the collector's pods by at least one label")
				}
				for _, l := range list(v) {
					if !labelRE.MatchString(l) || strings.HasSuffix(strings.SplitN(l, "=", 2)[0], "io.kubernetes.pod.namespace") {
						return "", fmt.Errorf("%q is not a pod label, k=v (the namespace is the endpoint's)", l)
					}
				}
				return "", nil
			}},

		{ID: "tracing.factoryEndpoint", Prompt: "The factory's OTLP/gRPC collector, host:port (empty: none)", Kind: KindString, Default: fixed(""),
			Keys: []string{"maku-factory.config.tracing.otlpEndpoint", "networkPolicies.factory.otlp.to"},
			Values: func(v any, a Answers) map[string]any {
				if str(v) == "" {
					return nil
				}
				m := map[string]any{"maku-factory.config.tracing.otlpEndpoint": str(v)}
				// An in-cluster collector is opened to its namespace's pods in the factory's default-deny.
				if parts := strings.Split(strings.Split(str(v), ":")[0], "."); len(parts) >= 3 && parts[2] == "svc" {
					m["networkPolicies.factory.otlp.to"] = peers(defaultStr(a, "network.mode", "kubernetes"), parts[1])
				}
				return m
			},
			Check: func(v any, _ Answers) (string, error) {
				if str(v) != "" && !regexp.MustCompile(`^[a-z0-9.-]+:[0-9]{1,5}$`).MatchString(str(v)) {
					return "", fmt.Errorf("%q is not host:port", str(v))
				}
				return "", nil
			}},

		// RunLore (IN7: RunLore, F10).
		{ID: "runlore.url", Prompt: "RunLore's knowledge MCP URL (empty: no knowledge tools)", Kind: KindString, Default: fixed(""),
			Keys: []string{"maku.broker.roomTools.knowledge.url"},
			Values: func(v any, _ Answers) map[string]any {
				if str(v) == "" {
					return nil
				}
				return map[string]any{"maku.broker.roomTools.knowledge.url": str(v)}
			}, Check: knowledgeURL},
		{ID: "runlore.namespace", Prompt: "The namespace of RunLore's pods (the broker's egress)", Kind: KindString, When: set("runlore.url"),
			Default: func(a Answers) any { return endpointNamespace(a.String("runlore.url")) },
			Keys:    []string{"maku.broker.roomTools.knowledge.networkPolicy.to"},
			Values: func(v any, a Answers) map[string]any {
				return map[string]any{"maku.broker.roomTools.knowledge.networkPolicy.to": peers(defaultStr(a, "network.mode", "kubernetes"), str(v))}
			}, Check: dnsLabel("the namespace")},

		// Secrets (IN7: Secrets, D14).
		{ID: "secrets.mode", Prompt: "Deliver the Secrets as plain Secret manifests, or ExternalSecrets (eso)", Kind: KindString, Default: fixed("plain"),
			Check: oneOf("the Secrets' delivery", "plain", "eso")},
		{ID: "secrets.store", Prompt: "The ClusterSecretStore the ExternalSecrets read", Kind: KindString, When: is("secrets.mode", "eso"),
			Keys: []string{"externalSecrets.storeRef.name"}, Values: at("externalSecrets.storeRef.name"), Check: dnsSubdomain("the store")},
	}
}

func slug(v any, _ Answers) (string, error) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,33}$`).MatchString(str(v)) {
		return "", fmt.Errorf("%q is not an App slug", str(v))
	}
	return "", nil
}

func dnsSubdomain(what string) func(v any, _ Answers) (string, error) {
	return func(v any, _ Answers) (string, error) {
		if !dnsSubdomainRE.MatchString(str(v)) || len(str(v)) > 253 {
			return "", fmt.Errorf("%s %q is not a DNS name (lowercase letters, digits, dashes and dots)", what, str(v))
		}
		return "", nil
	}
}

func oneOf(what string, ok ...string) func(v any, _ Answers) (string, error) {
	return func(v any, _ Answers) (string, error) {
		if !slices.Contains(ok, str(v)) {
			return "", fmt.Errorf("%s %q is not one of %s", what, str(v), strings.Join(ok, ", "))
		}
		return "", nil
	}
}

func nonEmpty(what string) func(v any, _ Answers) (string, error) {
	return func(v any, _ Answers) (string, error) {
		if strings.TrimSpace(str(v)) == "" {
			return "", fmt.Errorf("%s is required", what)
		}
		return "", nil
	}
}

// at writes the answer at one path.
func at(path string) func(v any, _ Answers) map[string]any {
	return func(v any, _ Answers) map[string]any { return map[string]any{path: v} }
}

func anys(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func trunc(s string, n int) string {
	if len(s) > n {
		return strings.TrimRight(s[:n], "-")
	}
	return s
}

// defaultStr is a's answer to id, or its question's default, or def.
func defaultStr(a Answers, id, def string) string {
	if s, ok := a[id].(string); ok {
		return s
	}
	if q, ok := questionsByID()[id]; ok {
		if s, ok := q.defaultValue(a).(string); ok {
			return s
		}
	}
	return def
}

// providerName is the gateway's provider entry for a preset: the preset's own name.
func providerName(preset string) string { return preset }

// parentRef reads namespace/name[/listener].
func parentRef(s string) map[string]any {
	p := strings.Split(s, "/")
	r := map[string]any{"namespace": p[0], "name": p[1]}
	if len(p) == 3 {
		r["sectionName"] = p[2]
	}
	return r
}

// internalHost reports whether h is a Kubernetes Service name (in-cluster).
func internalHost(h string) bool {
	return h != "" && (!strings.Contains(h, ".") || regexp.MustCompile(`\.svc(\..+)?$`).MatchString(h) || strings.HasSuffix(h, ".local"))
}

var endpointRE = regexp.MustCompile(`^https?://([a-z0-9-]+)\.([a-z0-9-]+)\.svc\.cluster\.local:[0-9]{1,5}(/.*)?$`)

// tracingEndpoint is the chart's own rule (F8): an in-cluster Service URL with its port, whose
// namespace is the collector's.
func tracingEndpoint(v any, _ Answers) (string, error) {
	if str(v) == "" {
		return "", nil
	}
	if !endpointRE.MatchString(str(v)) {
		return "", fmt.Errorf("the endpoint %q is not http(s)://<svc>.<ns>.svc.cluster.local:<port>: the harness's NO_PROXY keeps only the cluster domain direct, and the run's policy opens that port to that namespace", str(v))
	}
	return "", nil
}

// endpointNamespace is <ns> in a http(s)://<svc>.<ns>.svc... URL, "" otherwise.
func endpointNamespace(u string) string {
	parts := strings.Split(host(u), ".")
	if len(parts) >= 3 && parts[2] == "svc" {
		return parts[1]
	}
	return ""
}

// knowledgeURL is the broker's rule (EX11): https, or http to an in-cluster <svc>.<ns>.svc[.<domain>].
func knowledgeURL(v any, _ Answers) (string, error) {
	s := str(v)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the knowledge URL %q does not parse", s)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && endpointNamespace(s) != "") {
		return "", nil
	}
	return "", fmt.Errorf("the knowledge URL %q is https, or http to an in-cluster <svc>.<ns>.svc", s)
}

func questionsByID() map[string]Question {
	m := make(map[string]Question, len(Questions))
	for _, q := range Questions {
		m[q.ID] = q
	}
	return m
}

// prices reads model=input/cacheRead/output into the meter's price table.
func prices(l []string) map[string]any {
	m := map[string]any{}
	for _, p := range l {
		s := priceRE.FindStringSubmatch(p)
		if s == nil {
			continue
		}
		m[s[1]] = map[string]any{"input": num(s[2]), "cacheRead": num(s[4]), "output": num(s[6])}
	}
	return m
}

func num(s string) any {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return f
}
