// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runwatch"
)

// defaultDrillMaxAge is how recent a passing drill must be for readiness (R6): one weekly cadence.
const defaultDrillMaxAge = 168 * time.Hour

// defaultAgentGuardTimeout bounds one T5 call when the file sets no timeout.
const defaultAgentGuardTimeout = 2 * time.Second

// maxAgentGuardTimeout is the longest a T5 call may take: the shadow pass and the applier wait on it.
const maxAgentGuardTimeout = 10 * time.Second

// modeField is a mode as the file writes it. YAML 1.1 reads an unquoted off as the boolean false,
// so false reads as off; any other non-string is refused.
type modeField hardening.Mode

func (m *modeField) UnmarshalJSON(b []byte) error {
	if string(b) == "false" {
		*m = modeField(hardening.ModeOff)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("hardening.mode, layers.classifier and a repository's mode are off, audit or enforce, not %s", b)
	}
	*m = modeField(s)
	return nil
}

// Hardening is the untrusted-input hardening track's central configuration (hardening design,
// Configuration). A repository may only make it stricter. Stage H1 accepts off and audit, which
// observes and never refuses; enforce arrives with stage H3, and the classifier's with H5.
type Hardening struct {
	Mode           hardening.Mode                 `json:"-"`
	MinAuthorTrust string                         `json:"-"`
	BlockedUsers   []string                       `json:"-"`
	LabelAllowlist []string                       `json:"-"`
	Layers         HardeningLayers                `json:"-"`
	Drills         HardeningDrills                `json:"-"`
	AgentGuard     AgentGuard                     `json:"-"`
	Repositories   map[string]HardeningRepository `json:"-"`
	// EnforceOutputs are the non-code safe outputs every repository requests through the factory
	// (stage H2, HR10); a repository may add to them. Empty by default: nothing is enforced.
	EnforceOutputs []hardening.OutputType `json:"-"`
	// Publisher is the agents App's identity the applier writes enforced outputs as (HR11).
	Publisher Publisher `json:"-"`
	// Applier times the safe-output applier (HR15, HR19).
	Applier HardeningApplier `json:"-"`
}

// Publisher is where the publisher's token comes from: octo-sts, under each enforced repository's
// .github/chainguard/publisher.sts.yaml (O5, HR11).
type Publisher struct {
	OctoSTS PublisherOctoSTS
}

// PublisherOctoSTS is the exchange: octo-sts's base URL, the factory's projected ServiceAccount
// token (the chart's hardening.publisher.audience), and the trust policy's name.
type PublisherOctoSTS struct {
	URL, TokenFile, Identity string
}

// HardeningApplier is the applier's poll and how long a run's end waits for its answers.
type HardeningApplier struct {
	Interval, AnswerWait Duration
}

// Publisher defaults: the chart mounts the projected token here, and the policy is publisher.sts.yaml.
const (
	defaultPublisherTokenFile = "/var/run/secrets/octo-sts/token"
	defaultPublisherIdentity  = "publisher"
	defaultApplierInterval    = 2 * time.Second
	defaultApplierAnswerWait  = 5 * time.Minute
)

// HardeningLayers sets the layers that may differ from the mode: T5 alone, since T1 to T4 run at
// the mode and cannot be set below it.
type HardeningLayers struct {
	Classifier hardening.Mode
}

// HardeningDrills bounds how recent a passing drill must be for readiness (R6).
type HardeningDrills struct {
	MaxAge Duration
}

// AgentGuard is T5's classifier, agent-guard, called over agentgateway's guardrail webhook (HR9).
// With no URL, T5's effective mode is off.
type AgentGuard struct {
	URL     string
	Timeout Duration
}

// HardeningRepository is one repository's stricter setting.
type HardeningRepository struct {
	Mode hardening.Mode
	// EnforceOutputs is the repository's list, the central one when it writes none.
	EnforceOutputs []hardening.OutputType
}

// hardeningFile is the block as written: decoded strictly, every key once.
type hardeningFile struct {
	Mode           *modeField `json:"mode,omitempty"`
	MinAuthorTrust string     `json:"minAuthorTrust,omitempty"`
	BlockedUsers   []string   `json:"blockedUsers,omitempty"`
	LabelAllowlist []string   `json:"labelAllowlist,omitempty"`
	Layers         struct {
		Classifier *modeField `json:"classifier,omitempty"`
	} `json:"layers,omitzero"`
	Drills struct {
		MaxAge *Duration `json:"maxAge,omitempty"`
	} `json:"drills,omitzero"`
	AgentGuard struct {
		URL     string    `json:"url,omitempty"`
		Timeout *Duration `json:"timeout,omitempty"`
	} `json:"agentGuard,omitzero"`
	EnforceOutputs []hardening.OutputType `json:"enforceOutputs,omitempty"`
	Publisher      struct {
		OctoSTS struct {
			URL       string `json:"url,omitempty"`
			TokenFile string `json:"tokenFile,omitempty"`
			Identity  string `json:"identity,omitempty"`
		} `json:"octoSTS,omitzero"`
	} `json:"publisher,omitzero"`
	Applier struct {
		Interval   *Duration `json:"interval,omitempty"`
		AnswerWait *Duration `json:"answerWait,omitempty"`
	} `json:"applier,omitzero"`
	Repositories map[string]struct {
		Mode           *modeField              `json:"mode,omitempty"`
		EnforceOutputs *[]hardening.OutputType `json:"enforceOutputs,omitempty"`
	} `json:"repositories,omitempty"`
}

// explicitZero keeps a duration written as zero from being replaced by its default, so Validate
// refuses it.
func explicitZero(d *Duration) Duration {
	if d.Duration == 0 {
		return Duration{Duration: -1}
	}
	return *d
}

// UnmarshalJSON decodes the block strictly and applies its defaults: off, approved, the classifier
// in audit, a week's drill age and a two-second classifier timeout.
func (h *Hardening) UnmarshalJSON(b []byte) error {
	var f hardeningFile
	if err := strictJSON(b, &f); err != nil {
		return fmt.Errorf("hardening: %w", err)
	}
	*h = Hardening{Mode: hardening.ModeOff, MinAuthorTrust: f.MinAuthorTrust, BlockedUsers: f.BlockedUsers,
		LabelAllowlist: f.LabelAllowlist, AgentGuard: AgentGuard{URL: f.AgentGuard.URL}, EnforceOutputs: f.EnforceOutputs,
		Publisher: Publisher{OctoSTS: PublisherOctoSTS(f.Publisher.OctoSTS)}}
	if f.AgentGuard.Timeout != nil {
		h.AgentGuard.Timeout = explicitZero(f.AgentGuard.Timeout)
	}
	if f.Applier.Interval != nil {
		h.Applier.Interval = explicitZero(f.Applier.Interval)
	}
	if f.Applier.AnswerWait != nil {
		h.Applier.AnswerWait = explicitZero(f.Applier.AnswerWait)
	}
	h.ApplyDefaults()
	if f.Mode != nil {
		h.Mode = hardening.Mode(*f.Mode)
	}
	if f.Layers.Classifier != nil {
		h.Layers.Classifier = hardening.Mode(*f.Layers.Classifier)
	}
	if f.Drills.MaxAge != nil {
		h.Drills.MaxAge = *f.Drills.MaxAge
	}
	for name, r := range f.Repositories {
		if h.Repositories == nil {
			h.Repositories = map[string]HardeningRepository{}
		}
		m := h.Mode
		if r.Mode != nil {
			m = hardening.Mode(*r.Mode)
		}
		list := h.EnforceOutputs
		if r.EnforceOutputs != nil {
			list = *r.EnforceOutputs
		}
		h.Repositories[name] = HardeningRepository{Mode: m, EnforceOutputs: list}
	}
	return nil
}

// ApplyDefaults fills what an omitted block or key leaves out.
func (h *Hardening) ApplyDefaults() {
	if h.Mode == "" {
		h.Mode = hardening.ModeOff
	}
	if h.MinAuthorTrust == "" {
		h.MinAuthorTrust = hardening.Approved.String()
	}
	if h.Layers.Classifier == "" {
		h.Layers.Classifier = hardening.ModeAudit
	}
	if h.Drills.MaxAge.Duration == 0 {
		h.Drills.MaxAge.Duration = defaultDrillMaxAge
	}
	if h.AgentGuard.Timeout.Duration == 0 {
		h.AgentGuard.Timeout.Duration = defaultAgentGuardTimeout
	}
	if h.Publisher.OctoSTS.TokenFile == "" {
		h.Publisher.OctoSTS.TokenFile = defaultPublisherTokenFile
	}
	if h.Publisher.OctoSTS.Identity == "" {
		h.Publisher.OctoSTS.Identity = defaultPublisherIdentity
	}
	if h.Applier.Interval.Duration == 0 {
		h.Applier.Interval.Duration = defaultApplierInterval
	}
	if h.Applier.AnswerWait.Duration == 0 {
		h.Applier.AnswerWait.Duration = defaultApplierAnswerWait
	}
}

// EnforcedFor is repository's effective enforced output types, sorted and once each: its own list
// when it writes one, else the central one (HR10). Empty means the run writes as today.
func (h Hardening) EnforcedFor(repository string) []hardening.OutputType {
	list := h.EnforceOutputs
	if r, ok := h.Repositories[repository]; ok {
		list = r.EnforceOutputs
	}
	out := slices.Clone(list)
	slices.Sort(out)
	return slices.Compact(out)
}

// Enforcing reports whether any repository has an enforced output type: only then does the
// factory build a publisher and start the applier.
func (h Hardening) Enforcing() bool {
	return len(h.EnforceOutputs) > 0 || slices.ContainsFunc(slices.Collect(maps.Values(h.Repositories)),
		func(r HardeningRepository) bool { return len(r.EnforceOutputs) > 0 })
}

// ModeFor is repository's effective mode: its own when listed, else the central one.
func (h Hardening) ModeFor(repository string) hardening.Mode {
	if r, ok := h.Repositories[repository]; ok && r.Mode.AtLeast(h.Mode) {
		return r.Mode
	}
	return h.Mode
}

// EffectiveClassifier is T5's mode as it runs: the configured layer, or off when no classifier URL
// is set, since a T5 with nothing to call could only ever answer classifier_unavailable.
func (h Hardening) EffectiveClassifier() hardening.Mode {
	if h.AgentGuard.URL == "" {
		return hardening.ModeOff
	}
	return h.Layers.Classifier
}

// MinTrust is MinAuthorTrust parsed; Validate refuses a file where it does not parse.
func (h Hardening) MinTrust() hardening.Level {
	l, err := hardening.ParseLevel(h.MinAuthorTrust)
	if err != nil {
		return hardening.Merged // unreachable after Validate; the strictest reading otherwise
	}
	return l
}

// enforceRefused is why this release refuses mode enforce: R1 stays false until H3.
const enforceRefused = "enforce arrives with stage H3; this release offers off and audit"

func (c *Config) validateHardening(bad func(string, ...any)) {
	h := c.Hardening
	if _, err := hardening.ParseMode(string(h.Mode)); err != nil {
		bad("hardening.mode: %v", err)
	} else if h.Mode == hardening.ModeEnforce {
		bad("hardening.mode %q: %s", h.Mode, enforceRefused)
	}
	if _, err := hardening.ParseMode(string(h.Layers.Classifier)); err != nil {
		bad("hardening.layers.classifier: %v", err)
	} else if h.Layers.Classifier == hardening.ModeEnforce {
		bad("hardening.layers.classifier %q: the classifier's promotion to enforce arrives with stage H5", h.Layers.Classifier)
	}
	if l, err := hardening.ParseLevel(h.MinAuthorTrust); err != nil || l == hardening.Blocked {
		bad("hardening.minAuthorTrust %q is none of none, unapproved, approved, merged", h.MinAuthorTrust)
	}
	if h.Drills.MaxAge.Duration <= 0 {
		bad("hardening.drills.maxAge must be positive")
	}
	h.AgentGuard.validate(bad)
	repos := c.Repos()
	reserved := map[string]bool{}
	maintainers := []string{}
	for _, r := range repos {
		reserved[r.TriggerLabel] = true
		for cl := range r.Classes {
			reserved[cl] = true
		}
		maintainers = append(maintainers, r.Maintainers...)
	}
	for _, l := range h.LabelAllowlist {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "factory/") || reserved[l] {
			bad("hardening.labelAllowlist: %q is empty, a factory/ label, a trigger label or a class: an agent must not choose its own class", l)
		}
	}
	for _, u := range h.BlockedUsers {
		if strings.TrimSpace(u) == "" || slices.ContainsFunc(maintainers, func(m string) bool { return strings.EqualFold(m, u) }) {
			bad("hardening.blockedUsers: %q is empty or a maintainer", u)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(h.Repositories)) {
		r := h.Repositories[name]
		switch {
		case !slices.ContainsFunc(repos, func(x Repository) bool { return x.Name == name }):
			bad("hardening.repositories: %q is not a configured repository", name)
		case func() bool { _, err := hardening.ParseMode(string(r.Mode)); return err != nil }():
			bad("hardening.repositories.%s.mode %q is none of off, audit, enforce", name, r.Mode)
		case r.Mode == hardening.ModeEnforce:
			bad("hardening.repositories.%s.mode %q: %s", name, r.Mode, enforceRefused)
		}
	}
	h.validateStricter(bad)
	h.validateEnforce(bad)
}

// enforceable are the output types stage H2 enforces; push stays in the sandbox until H3.
var enforceable = []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels, hardening.Issue}

// validateEnforce refuses an enforceOutputs list with push or an unknown type, a repository list
// that drops a central type, a list on a repository in mode off, a list with no octo-sts publisher,
// and an applier timing that is not positive (HR10, HR11, HR15, HR19).
func (h Hardening) validateEnforce(bad func(string, ...any)) {
	types := func(key string, list []hardening.OutputType) {
		for _, t := range list {
			switch {
			case t == hardening.Push:
				bad("%s: push arrives with stage H3; this release enforces pr, comment, labels and issue", key)
			case !slices.Contains(enforceable, t):
				bad("%s: %q is none of pr, comment, labels, issue", key, t)
			}
		}
	}
	types("hardening.enforceOutputs", h.EnforceOutputs)
	if len(h.EnforceOutputs) > 0 && !h.Mode.AtLeast(hardening.ModeAudit) {
		bad("hardening.enforceOutputs needs hardening.mode audit at least")
	}
	for _, name := range slices.Sorted(maps.Keys(h.Repositories)) {
		r := h.Repositories[name]
		key := "hardening.repositories." + name + ".enforceOutputs"
		types(key, r.EnforceOutputs)
		for _, t := range h.EnforceOutputs {
			if !slices.Contains(r.EnforceOutputs, t) {
				bad("%s must be a superset of hardening.enforceOutputs: it drops %q", key, t)
			}
		}
		if len(r.EnforceOutputs) > 0 && !h.ModeFor(name).AtLeast(hardening.ModeAudit) {
			bad("%s needs the repository's mode audit at least", key)
		}
	}
	if h.Enforcing() {
		p := h.Publisher.OctoSTS
		if p.URL == "" {
			bad("hardening.publisher.octoSTS.url is required with enforceOutputs: the applier writes as the publisher (HR11)")
		} else if u, err := url.Parse(p.URL); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			bad("hardening.publisher.octoSTS.url must be an https URL with a host and no credentials")
		}
		if !strings.HasPrefix(p.TokenFile, "/") {
			bad("hardening.publisher.octoSTS.tokenFile must be an absolute path")
		}
	}
	if h.Applier.Interval.Duration <= 0 {
		bad("hardening.applier.interval must be positive")
	}
	if h.Applier.AnswerWait.Duration <= 0 {
		bad("hardening.applier.answerWait must be positive")
	}
}

// unscopedWarning is HR13's: an enforced repository on a run path whose sandbox token no role
// policy scopes. The factory still starts: the tracer reports every direct write.
func (c *Config) unscopedWarning() (string, bool) {
	if !c.Hardening.Enforcing() || c.Runs.Group == runwatch.DefaultKind.Group {
		return "", false
	}
	return "hardening.enforceOutputs is set and runs are not cloud-native-ref's XR: the sandbox token is not scoped by a role policy, " +
		"so a run can still write directly; the tracer reports each such write (HR13)", true
}

// validate refuses a classifier URL that is not plain http(s) with a host, or carries credentials,
// and a timeout outside (0, 10s].
func (g AgentGuard) validate(bad func(string, ...any)) {
	if g.URL != "" {
		if u, err := url.Parse(g.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			bad("hardening.agentGuard.url must be an http or https URL with a host and no credentials")
		}
	}
	if g.Timeout.Duration <= 0 || g.Timeout.Duration > maxAgentGuardTimeout {
		bad("hardening.agentGuard.timeout must be positive and at most %s", maxAgentGuardTimeout)
	}
}

// validateStricter refuses a repository mode below the central one: a repository may only make
// the configuration stricter.
func (h Hardening) validateStricter(bad func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(h.Repositories)) {
		if !h.Repositories[name].Mode.AtLeast(h.Mode) {
			bad("hardening.repositories.%s.mode %q is below the central %q: a repository may only be stricter", name, h.Repositories[name].Mode, h.Mode)
		}
	}
}

// strictJSON decodes b into v, refusing unknown keys.
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Hash is the sha256 of the effective hardening configuration: the key a drill report proves
// (R6), so any change to the block needs a new passing drill. T5 enters as its effective mode, so
// adding or removing the classifier moves the hash and moving it to another host does not.
func (h Hardening) Hash() string {
	// The enforced lists are omitted when empty, so a file without them keeps its pre-H2 hash.
	b, _ := json.Marshal(struct { // a struct of strings, slices and maps: Marshal cannot fail
		Mode, MinAuthorTrust, Classifier, MaxAge string
		BlockedUsers, LabelAllowlist             []string
		Repositories                             map[string]hardening.Mode
		Enforce                                  []hardening.OutputType            `json:",omitempty"`
		RepositoryEnforce                        map[string][]hardening.OutputType `json:",omitempty"`
	}{string(h.Mode), h.MinAuthorTrust, string(h.EffectiveClassifier()), h.Drills.MaxAge.String(),
		h.BlockedUsers, h.LabelAllowlist, h.repositoryModes(), h.EnforcedFor(""), h.repositoryEnforce()})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// repositoryEnforce is each listed repository's non-empty effective list.
func (h Hardening) repositoryEnforce() map[string][]hardening.OutputType {
	m := map[string][]hardening.OutputType{}
	for name := range h.Repositories {
		if l := h.EnforcedFor(name); len(l) > 0 {
			m[name] = l
		}
	}
	return m
}

func (h Hardening) repositoryModes() map[string]hardening.Mode {
	m := map[string]hardening.Mode{}
	for name, r := range h.Repositories {
		m[name] = r.Mode
	}
	return m
}

// HardeningSettings are the facts readiness reads from this configuration for repository. The read
// filter is off until forge_read is wired (stage H4).
func (c *Config) HardeningSettings(repository, makuVersion string) hardening.Settings {
	h := c.Hardening
	mode := h.ModeFor(repository)
	return hardening.Settings{Mode: mode, Layers: hardening.LayersFor(mode, h.EffectiveClassifier()),
		Demo: c.Demo, DrillMaxAge: h.Drills.MaxAge.Duration, ConfigHash: h.Hash(), Version: makuVersion}
}
