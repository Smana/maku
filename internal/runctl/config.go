// SPDX-License-Identifier: Apache-2.0

// Package runctl is the run-controller (adoption 3a): it reconciles maku.ogenki.io AgentRuns
// into the pod, ServiceAccount, ConfigMaps and CiliumNetworkPolicy that cloud-native-ref's
// Crossplane composition builds, minus the gateway, octo-sts, traces, Kueue and suspend.
package runctl

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/runwatch"
)

// The values of Config.NetworkPolicy.
const (
	NetworkPolicyCilium = "cilium"
	NetworkPolicyNone   = "none"
	// NetworkPolicyKubernetes: each run gets a networking.k8s.io/v1 NetworkPolicy, and its harness
	// reaches the web only through the egress proxy, which holds it to its own hosts (v4 D6).
	NetworkPolicyKubernetes = "kubernetes"
)

// Config is the run-controller's file, from the chart.
type Config struct {
	Namespace        string `json:"namespace"`
	NamePrefix       string `json:"namePrefix"`
	RuntimeClassName string `json:"runtimeClassName,omitempty"`
	// AllowUnsandboxed places pods without a RuntimeClass; each run then carries condition
	// Unsandboxed, so the room can say so (3a R5).
	AllowUnsandboxed bool `json:"allowUnsandboxed,omitempty"`
	// NetworkPolicy is "cilium" (default: each run gets a CiliumNetworkPolicy), "kubernetes" (a
	// NetworkPolicy, and the harness behind EgressProxy) or "none", for a cluster that enforces
	// neither: no policy is created, so nothing confines a run's network.
	NetworkPolicy string `json:"networkPolicy,omitempty"`
	// EgressProxy is the in-cluster CONNECT proxy, required in kubernetes mode.
	EgressProxy EgressProxy `json:"egressProxy,omitempty"`
	// DNS selects the cluster DNS pods a kubernetes-mode run's NetworkPolicy opens on 53.
	DNS DNS `json:"dns,omitempty"`
	// ModelSecret holds the provider key and model the harness calls directly (model.mode secret, the
	// default). It is refused in gateway mode, where the gateway holds the keys.
	ModelSecret string `json:"modelSecret,omitempty"`
	// Model is how runs reach a model: secret (ModelSecret) or gateway (v4 step 4, D7).
	Model ModelConfig `json:"model,omitzero"`
	GitSecret   string `json:"gitSecret"`
	// GitReadSecret is the read-only token reviewer and triager runs mount (PR2); gitSecret is the
	// write token implementer and tester runs mount. Unset, those roles are refused unless
	// UnsafeSharedGitToken lets them share gitSecret, as a laptop's one PAT does.
	GitReadSecret        string              `json:"gitReadSecret,omitempty"`
	UnsafeSharedGitToken bool                `json:"unsafeSharedGitToken,omitempty"`
	Broker               Broker              `json:"broker"`
	Images               Images              `json:"images"`
	EgressProfiles       map[string][]string `json:"egressProfiles,omitempty"`
	// RoomToolsURL is the broker's :8090 room tools, by broker.url's host, for a broker serving
	// roomTools.auth tokenreview. Set, a run with a room gets MCP_URL and a projected token of audience
	// room-tools for the harness alone. Unset, the harness wires no MCP server.
	RoomToolsURL string `json:"roomToolsURL,omitempty"`
}

// Broker is where the bridge connects, and what the CNP lets it reach.
type Broker struct {
	URL       string            `json:"url"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
	CASecret  string            `json:"caSecret"`
}

// EgressProxy is where a kubernetes-mode harness sends HTTPS, and what its NetworkPolicy lets it
// reach. DenyCIDRs are the proxy's own: addresses it never dials, beyond the non-public ones.
type EgressProxy struct {
	URL       string            `json:"url"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
	DenyCIDRs []string          `json:"denyCIDRs,omitempty"`
}

// The values of ModelConfig.Mode.
const (
	// ModelModeSecret: the harness calls the provider named by modelSecret with its key (3a R4).
	ModelModeSecret = "secret"
	// ModelModeGateway: the harness calls the model gateway with the run's own projected token, on its
	// data class's listener and its claim's alias (v4 step 4, GW3). No provider key enters the pod.
	ModelModeGateway = "gateway"
)

// ModelConfig is the run-controller's model mode.
type ModelConfig struct {
	Mode    string  `json:"mode,omitempty"`
	Gateway Gateway `json:"gateway,omitzero"`
}

// Gateway is the model gateway (agentgateway, maku-gateway) that gateway-mode runs call.
type Gateway struct {
	// URL is http://<service>.<namespace>.svc.<cluster-domain>, with no port or path: the port is the
	// run's data class's listener, the path its alias.
	URL   string       `json:"url,omitempty"`
	Ports GatewayPorts `json:"ports,omitzero"`
	// AudiencePrefix starts every token audience: <prefix>.<dataClass>.<alias>.
	AudiencePrefix string `json:"audiencePrefix,omitempty"`
	// Aliases are the aliases each listener serves (maku-gateway aliases.<class>'s keys): a run whose
	// alias its listener does not serve is refused before its pod exists.
	Aliases GatewayAliases `json:"aliases,omitzero"`
}

// GatewayPorts are the listeners' ports, one per data class.
type GatewayPorts struct {
	Public   int `json:"public,omitempty"`
	Internal int `json:"internal,omitempty"`
}

// GatewayAliases are the aliases served per data class.
type GatewayAliases struct {
	Public   []string `json:"public,omitempty"`
	Internal []string `json:"internal,omitempty"`
}

// Gateway reports whether runs call the model gateway.
func (c Config) Gateway() bool { return c.Model.Mode == ModelModeGateway }

// aliasRE is a gateway alias: a path segment, and an audience's last label.
var aliasRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// audiencePrefixRE is dot-separated alias-like labels.
var audiencePrefixRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// model checks the model mode: modelSecret in secret mode; in gateway mode, the gateway's in-cluster
// URL, its ports, its audience prefix and its aliases, and no modelSecret.
func (c Config) model() []error {
	g := c.Model.Gateway
	switch c.Model.Mode {
	case ModelModeSecret:
		var errs []error
		if c.ModelSecret == "" {
			errs = append(errs, errors.New("modelSecret is required"))
		}
		if !reflect.ValueOf(g).IsZero() {
			errs = append(errs, errors.New("model.gateway is set but model.mode is secret: set model.mode gateway, or remove model.gateway"))
		}
		return errs
	case ModelModeGateway:
	default:
		return []error{fmt.Errorf("model.mode %q must be %q or %q", c.Model.Mode, ModelModeSecret, ModelModeGateway)}
	}
	var errs []error
	if c.ModelSecret != "" {
		errs = append(errs, errors.New("model.mode gateway and modelSecret: choose one (the gateway holds the keys)"))
	}
	u, err := url.Parse(g.URL)
	if err != nil || u.Scheme != "http" || u.Port() != "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || !brokerHostRE.MatchString(u.Hostname()) {
		errs = append(errs, fmt.Errorf("model.gateway.url %q must be http://<service>.<namespace>.svc.<cluster-domain>, with no port or path: the run's data class picks the port", g.URL))
	}
	for _, p := range []struct {
		class string
		port  int
	}{{"public", g.Ports.Public}, {"internal", g.Ports.Internal}} {
		if !validPort(strconv.Itoa(p.port)) {
			errs = append(errs, fmt.Errorf("model.gateway.ports.%s %d is not 1-65535", p.class, p.port))
		}
	}
	if !audiencePrefixRE.MatchString(g.AudiencePrefix) {
		errs = append(errs, fmt.Errorf("model.gateway.audiencePrefix %q must be dot-separated lowercase labels", g.AudiencePrefix))
	}
	if len(g.Aliases.Public) == 0 {
		errs = append(errs, errors.New("model.gateway.aliases.public is required: the aliases the gateway's public listener serves"))
	}
	for _, cl := range []struct {
		class   string
		aliases []string
	}{{"public", g.Aliases.Public}, {"internal", g.Aliases.Internal}} {
		for _, a := range cl.aliases {
			if !aliasRE.MatchString(a) {
				errs = append(errs, fmt.Errorf("model.gateway.aliases.%s: %q is not an alias", cl.class, a))
			}
		}
	}
	return errs
}

// Port is URL's port (validated at load).
func (p EgressProxy) Port() int {
	u, err := url.Parse(p.URL)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(u.Port())
	return n
}

// DNS is the cluster DNS's namespace and pod labels.
type DNS struct {
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

// Images are the run pod's two images, pinned by the chart.
type Images struct {
	Harness string `json:"harness"`
	Bridge  string `json:"bridge"`
	// Probe runs the preflight's netprobe pod: the run-controller's own image, required in
	// kubernetes mode.
	Probe string `json:"probe,omitempty"`
}

// defaultProfiles are the composition's _EGRESS_PROFILES (crossplane-configuration v0.9.17) plus
// RC7's two hosts, until PR 5 brings them into the composition: GitHub's release host, where a
// release download lands (302), and Go's archive host.
var defaultProfiles = map[string][]string{
	"github": {"github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com", "raw.githubusercontent.com", "release-assets.githubusercontent.com"},
	"pypi":   {"pypi.org", "files.pythonhosted.org"},
	"npm":    {"registry.npmjs.org"},
	"golang": {"proxy.golang.org", "sum.golang.org", "dl.google.com"},
	"crates": {"index.crates.io", "static.crates.io"},
}

// The broker host must be the fully qualified service name: the pod's ndots:1 resolver
// would otherwise try search-path expansions that the CNP's DNS allowlist refuses.
var brokerHostRE = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9-]+\.svc\.[a-z0-9.-]+$`)

// LoadConfig reads, strictly decodes, defaults and validates the file at path.
func LoadConfig(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	if c.EgressProfiles == nil {
		c.EgressProfiles = defaultProfiles
	}
	if c.NetworkPolicy == "" {
		c.NetworkPolicy = NetworkPolicyCilium
	}
	if c.DNS.Namespace == "" && len(c.DNS.Labels) == 0 {
		c.DNS = DNS{Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-dns"}}
	}
	c.Model.Mode = cmp.Or(c.Model.Mode, ModelModeSecret)
	if c.Gateway() {
		g := &c.Model.Gateway
		g.Ports.Public, g.Ports.Internal = cmp.Or(g.Ports.Public, 8080), cmp.Or(g.Ports.Internal, 8081)
		g.AudiencePrefix = cmp.Or(g.AudiencePrefix, "maku-gateway")
	}
	if err := c.validate(); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

func (c Config) validate() error {
	var errs []error
	req := func(name, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	switch c.NetworkPolicy {
	case NetworkPolicyCilium, NetworkPolicyNone:
	case NetworkPolicyKubernetes:
		errs = append(errs, c.egressProxy()...)
	default:
		errs = append(errs, fmt.Errorf("networkPolicy %q must be %q, %q or %q", c.NetworkPolicy, NetworkPolicyCilium, NetworkPolicyKubernetes, NetworkPolicyNone))
	}
	req("namespace", c.Namespace)
	errs = append(errs, c.model()...)
	req("gitSecret", c.GitSecret)
	if c.GitReadSecret != "" && c.UnsafeSharedGitToken {
		errs = append(errs, errors.New("gitReadSecret and unsafeSharedGitToken: choose one"))
	}
	req("broker.namespace", c.Broker.Namespace)
	req("broker.caSecret", c.Broker.CASecret)
	req("images.harness", c.Images.Harness)
	req("images.bridge", c.Images.Bridge)
	if err := runwatch.ValidatePrefix(c.NamePrefix); err != nil {
		errs = append(errs, err)
	}
	u, err := url.Parse(c.Broker.URL)
	switch {
	case err != nil || u.Scheme != "https" || u.Hostname() == "":
		errs = append(errs, fmt.Errorf("broker.url %q must be an https URL", c.Broker.URL))
	case !brokerHostRE.MatchString(u.Hostname()):
		errs = append(errs, fmt.Errorf("broker.url host %q must be <service>.<namespace>.svc.<cluster-domain>, e.g. room-broker.agent-platform.svc.cluster.local", u.Hostname()))
	}
	errs = append(errs, c.roomToolsURL()...)
	if len(c.Broker.Labels) == 0 {
		errs = append(errs, errors.New("broker.labels: the CNP needs at least one to select the broker"))
	}
	if len(c.EgressProfiles["github"]) == 0 {
		errs = append(errs, errors.New("egressProfiles.github is required: every run clones from GitHub"))
	}
	return errors.Join(errs...)
}

// egressProxy checks kubernetes mode's proxy and DNS: an http URL with a port on a fully qualified
// in-cluster Service name, and the selectors a run's NetworkPolicy needs.
func (c Config) egressProxy() []error {
	var errs []error
	p := c.EgressProxy
	u, err := url.Parse(p.URL)
	if err != nil || u.Scheme != "http" || !validPort(u.Port()) || u.User != nil || (u.Path != "" && u.Path != "/") || !brokerHostRE.MatchString(u.Hostname()) {
		errs = append(errs, fmt.Errorf("egressProxy.url %q must be http://<service>.<namespace>.svc.<cluster-domain>:<port>: networkPolicy kubernetes sends every run's HTTPS through it", p.URL))
	}
	if p.Namespace == "" || len(p.Labels) == 0 {
		errs = append(errs, errors.New("egressProxy.namespace and egressProxy.labels are required: a run's NetworkPolicy selects the proxy by them"))
	}
	for _, cidr := range p.DenyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			errs = append(errs, fmt.Errorf("egressProxy.denyCIDRs: %q is not a CIDR", cidr))
		}
	}
	if c.DNS.Namespace == "" || len(c.DNS.Labels) == 0 {
		errs = append(errs, errors.New("dns.namespace and dns.labels are required together"))
	}
	if c.Images.Probe == "" {
		errs = append(errs, errors.New("images.probe is required with egressProxy: the preflight's probe pod proves the CNI enforces NetworkPolicy"))
	}
	return errs
}

// clusterDomain is broker.url's domain, after .svc. (validated at load).
func (c Config) clusterDomain() string {
	_, d, _ := strings.Cut(brokerHost(c), ".svc.")
	return d
}

// roomToolsURL checks roomToolsURL: the broker's own host, so the run CNP's broker rule and DNS
// allowlist cover it, and a path, the MCP endpoint. http is for an in-cluster listener with no TLS.
func (c Config) roomToolsURL() []error {
	if c.RoomToolsURL == "" {
		return nil
	}
	b, _ := url.Parse(c.Broker.URL)
	u, err := url.Parse(c.RoomToolsURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Port() != "" && !validPort(u.Port())) || u.User != nil || u.Path == "" || u.Path == "/" ||
		b == nil || u.Hostname() != b.Hostname() || !brokerHostRE.MatchString(u.Hostname()) {
		return []error{fmt.Errorf("roomToolsURL %q must be an http(s) URL with a path on broker.url's host", c.RoomToolsURL)}
	}
	return nil
}

// roomToolsPort is roomToolsURL's port, by its scheme when the URL names none.
func (c Config) roomToolsPort() int {
	u, _ := url.Parse(c.RoomToolsURL) // validated at load
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}

// RunID is the run's C2 id, or "" for a name this controller does not own.
func (c Config) RunID(r *v1alpha1.AgentRun) string {
	id, ok := strings.CutPrefix(r.Name, c.NamePrefix)
	if !ok || !envelope.ValidID(id) {
		return ""
	}
	return id
}

// Invalid says why the controller refuses r, or "" when it accepts it (3a R6, R7).
func (c Config) Invalid(r *v1alpha1.AgentRun) string {
	switch {
	case c.RunID(r) == "":
		return fmt.Sprintf("the name must be namePrefix %q followed by 8 characters of [a-z2-7]", c.NamePrefix)
	case r.Namespace != c.Namespace:
		return fmt.Sprintf("runs live in namespace %q", c.Namespace)
	case r.Spec.Suspendable:
		return "spec.suspendable runs need cloud-native-ref's runtime"
	case r.Spec.DataClass == "internal":
		return "spec.dataClass internal needs the model gateway, which this runtime does not have"
	case c.RuntimeClassName == "" && !c.AllowUnsandboxed:
		return "runtimeClassName is empty and allowUnsandboxed is false: refusing to run unsandboxed"
	}
	if readOnly(r.Spec.Role) && c.GitReadSecret == "" && !c.UnsafeSharedGitToken {
		return fmt.Sprintf("role %s needs a read-only token: set runController.gitReadSecret (or unsafeSharedGitToken on a laptop)", r.Spec.Role)
	}
	if scoperRun(r) && c.RoomToolsURL == "" {
		return "a scoper hands off with room_handoff: set broker.roomTools.auth tokenreview"
	}
	if scoperRun(r) && r.Spec.RoomRef == "" { // the harness gets MCP only for a run with a room (Pod)
		return "a scoper hands off with room_handoff: it needs a room (spec.roomRef)"
	}
	if why := invalidManifest(r); why != "" {
		return why
	}
	for _, p := range r.Spec.Egress.Profiles {
		if _, ok := c.EgressProfiles[string(p)]; !ok {
			return fmt.Sprintf("spec.egress.profiles: profile %q is not configured", p)
		}
	}
	return ""
}

// readOnlyRoles mount gitReadSecret: they read the repository and never push, as cloud-native-ref's
// octo-sts policies for them grant contents: read.
var readOnlyRoles = []string{"reviewer", "triager"}

func readOnly(role string) bool { return slices.Contains(readOnlyRoles, role) }

// scoperRun is a triager whose brief opens with the factory's scoper marker (harness roles HR2).
func scoperRun(r *v1alpha1.AgentRun) bool {
	return r.Spec.Role == "triager" && strings.HasPrefix(r.Spec.Task.Text, runs.ScoperMarkerPrefix)
}

// GitSecretFor is the git Secret a run of role mounts: gitReadSecret for a read-only role, or
// gitSecret under unsafeSharedGitToken; gitSecret for the others. A read-only role with neither
// gets "": Invalid refuses it first, and a path that skipped Invalid builds a pod that cannot
// mount, never one holding the write token.
func (c Config) GitSecretFor(role string) string {
	switch {
	case !readOnly(role) || c.UnsafeSharedGitToken:
		return c.GitSecret
	default:
		return c.GitReadSecret
	}
}

// FQDNs are the hosts the run may reach on 443: github, then its profiles, deduplicated.
func (c Config) FQDNs(r *v1alpha1.AgentRun) []string {
	out := slices.Clone(c.EgressProfiles["github"])
	for _, p := range r.Spec.Egress.Profiles {
		for _, h := range c.EgressProfiles[string(p)] {
			out = addUnique(out, h)
		}
	}
	return out
}

// addUnique appends h unless list already holds it.
func addUnique(list []string, h string) []string {
	if slices.Contains(list, h) {
		return list
	}
	return append(list, h)
}

// Model is the provider the harness calls directly (3a R4), from modelSecret. Judge is the
// optional judge_model: the completion judge's model on the same endpoint, "" for the run's own.
type Model struct {
	BaseURL, Name, Host, Judge string
	Port                       int
}

const defaultBaseURL = "https://api.anthropic.com/v1"

// ModelFrom reads modelSecret's data: api_key and model required, base_url defaulted, judge_model
// optional.
func ModelFrom(data map[string][]byte) (Model, error) {
	if len(data["api_key"]) == 0 {
		return Model{}, errors.New("modelSecret has no api_key")
	}
	m := Model{BaseURL: strings.TrimSpace(string(data["base_url"])), Name: strings.TrimSpace(string(data["model"])),
		Judge: strings.TrimSpace(string(data["judge_model"]))}
	if m.Name == "" {
		return Model{}, errors.New("modelSecret has no model")
	}
	if m.BaseURL == "" {
		m.BaseURL = defaultBaseURL
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return Model{}, fmt.Errorf("modelSecret base_url %q is not an http(s) URL", m.BaseURL)
	}
	// Plain http would send the provider key in cleartext and open port 80: only an in-cluster
	// service (a stub, a gateway) may be reached that way, by its fully qualified name: the run's
	// DNS allowlist cannot resolve a short <svc>.<ns>.svc, which only a search domain completes.
	if u.Scheme == "http" && !brokerHostRE.MatchString(u.Hostname()) {
		return Model{}, fmt.Errorf("modelSecret base_url %q: http is only allowed for an in-cluster service, as <service>.<namespace>.svc.<cluster-domain>; use https otherwise", m.BaseURL)
	}
	m.Host, m.Port = u.Hostname(), 443
	if u.Scheme == "http" {
		m.Port = 80
	}
	if p := u.Port(); p != "" {
		if !validPort(p) {
			return Model{}, fmt.Errorf("modelSecret base_url %q: port %s is not 1-65535", m.BaseURL, p)
		}
		m.Port, _ = strconv.Atoi(p)
	}
	return m, nil
}

// validPort is a TCP port, 1-65535: every port that reaches a NetworkPolicy is checked once, here.
func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= math.MaxUint16
}
