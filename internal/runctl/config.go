// SPDX-License-Identifier: Apache-2.0

// Package runctl is the run-controller (adoption 3a): it reconciles maku.ogenki.io AgentRuns
// into the pod, ServiceAccount, ConfigMaps and CiliumNetworkPolicy that cloud-native-ref's
// Crossplane composition builds, minus the gateway, octo-sts, traces, Kueue and suspend.
package runctl

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/runwatch"
)

// The values of Config.NetworkPolicy.
const (
	NetworkPolicyCilium = "cilium"
	NetworkPolicyNone   = "none"
)

// Config is the run-controller's file, from the chart.
type Config struct {
	Namespace        string `json:"namespace"`
	NamePrefix       string `json:"namePrefix"`
	RuntimeClassName string `json:"runtimeClassName,omitempty"`
	// AllowUnsandboxed places pods without a RuntimeClass; each run then carries condition
	// Unsandboxed, so the room can say so (3a R5).
	AllowUnsandboxed bool `json:"allowUnsandboxed,omitempty"`
	// NetworkPolicy is "cilium" (default: each run gets a CiliumNetworkPolicy) or "none", for a
	// cluster without Cilium: no policy is created, so nothing confines a run's network.
	NetworkPolicy  string              `json:"networkPolicy,omitempty"`
	ModelSecret    string              `json:"modelSecret"`
	GitSecret      string              `json:"gitSecret"`
	Broker         Broker              `json:"broker"`
	Images         Images              `json:"images"`
	EgressProfiles map[string][]string `json:"egressProfiles,omitempty"`
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

// Images are the run pod's two images, pinned by the chart.
type Images struct {
	Harness string `json:"harness"`
	Bridge  string `json:"bridge"`
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
	if c.NetworkPolicy != NetworkPolicyCilium && c.NetworkPolicy != NetworkPolicyNone {
		errs = append(errs, fmt.Errorf("networkPolicy %q must be %q or %q", c.NetworkPolicy, NetworkPolicyCilium, NetworkPolicyNone))
	}
	req("namespace", c.Namespace)
	req("modelSecret", c.ModelSecret)
	req("gitSecret", c.GitSecret)
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

// roomToolsURL checks roomToolsURL: the broker's own host, so the run CNP's broker rule and DNS
// allowlist cover it, and a path, the MCP endpoint. http is for an in-cluster listener with no TLS.
func (c Config) roomToolsURL() []error {
	if c.RoomToolsURL == "" {
		return nil
	}
	b, _ := url.Parse(c.Broker.URL)
	u, err := url.Parse(c.RoomToolsURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path == "" || u.Path == "/" ||
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
	for _, p := range r.Spec.Egress.Profiles {
		if _, ok := c.EgressProfiles[string(p)]; !ok {
			return fmt.Sprintf("spec.egress.profiles: profile %q is not configured", p)
		}
	}
	return ""
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

// Model is the provider the harness calls directly (3a R4), from modelSecret.
type Model struct {
	BaseURL, Name, Host string
	Port                int
}

const defaultBaseURL = "https://api.anthropic.com/v1"

// ModelFrom reads modelSecret's data: api_key and model required, base_url defaulted.
func ModelFrom(data map[string][]byte) (Model, error) {
	if len(data["api_key"]) == 0 {
		return Model{}, errors.New("modelSecret has no api_key")
	}
	m := Model{BaseURL: strings.TrimSpace(string(data["base_url"])), Name: strings.TrimSpace(string(data["model"]))}
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
		m.Port, _ = strconv.Atoi(p)
	}
	return m, nil
}
