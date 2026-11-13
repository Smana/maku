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
)

// defaultDrillMaxAge is how recent a passing drill must be for readiness (R6): one weekly cadence.
const defaultDrillMaxAge = 168 * time.Hour

// The drill runner's waits (HR38): a drill's tasks get 6 hours, at most 48, and one gateway-log
// query 10 seconds, at most a minute.
const (
	defaultDrillTimeout      = 6 * time.Hour
	maxDrillTimeout          = 48 * time.Hour
	defaultGatewayLogTimeout = 10 * time.Second
	maxGatewayLogTimeout     = time.Minute
)

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
// observes and never refuses; enforce arrives with stage H4, and the classifier's with H5.
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
// .github/chainguard/publisher.sts.yaml (O5, HR11), or, with Minter, the factory's own minter from
// the agents App (v4 D10, plan PR7). Either way it is the agents App, so its login is agentsLogin.
type Publisher struct {
	OctoSTS PublisherOctoSTS
	Minter  bool
}

// PublisherOctoSTS is the exchange: octo-sts's base URL, the factory's projected ServiceAccount
// token (the chart's hardening.publisher.audience), and the trust policy's name.
type PublisherOctoSTS struct {
	URL, TokenFile, Identity string
}

// HardeningApplier is the applier's poll, how long a run's end waits for its answers, how long a
// protected-path push waits for a maintainer (HR27), and the size of one repository's git mirror
// (HR24).
type HardeningApplier struct {
	Interval, AnswerWait Duration
	// ApprovalTTL is how long a push touching a protected path waits for a maintainer's approval of
	// its digest: 24h by default, at most 7 days (168h).
	ApprovalTTL Duration
	// MirrorMaxBytes bounds one repository's bare, blobless mirror under MAKU_GIT_CACHE: a mirror
	// past it is re-created before its next inspection. 1 GiB by default.
	MirrorMaxBytes int64
}

// Publisher defaults: the chart mounts the projected token here, and the policy is publisher.sts.yaml.
const (
	defaultPublisherTokenFile = "/var/run/secrets/octo-sts/token" //nolint:gosec // G101: a mount path, not a credential
	defaultPublisherIdentity  = "publisher"
	defaultApplierInterval    = 2 * time.Second
	defaultApplierAnswerWait  = 5 * time.Minute
	defaultApprovalTTL        = 24 * time.Hour
	maxApprovalTTL            = 7 * 24 * time.Hour
	defaultMirrorMaxBytes     = 1 << 30
	minMirrorMaxBytes         = 64 << 20
)

// HardeningLayers sets the layers that may differ from the mode: T5 alone, since T1 to T4 run at
// the mode and cannot be set below it.
type HardeningLayers struct {
	Classifier hardening.Mode
}

// HardeningDrills bounds how recent a passing drill must be for readiness (R6), how long the drill
// runner waits for a drill's tasks before it scores them anyway (HR38), and where it reads the
// gateway's audit log.
type HardeningDrills struct {
	MaxAge Duration
	// Timeout is how long after its manifest a drill is scored even with a task still open: 6h by
	// default, at most 48h. A scenario whose role never ran then fails not_run.
	Timeout Duration
	// GatewayLog is the VictoriaLogs instance holding the model gateway's audit log: marker hits and
	// guardrail verdicts (AC5). With no URL the drill runner is off, since withheld text reaching a
	// model could not be checked.
	GatewayLog GatewayLog
	// Repository is the drill repository, owner/name: when set, only its tasks are drill tasks. Empty,
	// any repository's issue labelled factory/drill-injection makes one while the drills are on.
	Repository string
}

// GatewayLog is a VictoriaLogs base URL and how long one query may take (10s by default, at most
// a minute).
type GatewayLog struct {
	URL     string
	Timeout Duration
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
		MaxAge     *Duration `json:"maxAge,omitempty"`
		Timeout    *Duration `json:"timeout,omitempty"`
		GatewayLog struct {
			URL     string    `json:"url,omitempty"`
			Timeout *Duration `json:"timeout,omitempty"`
		} `json:"gatewayLog,omitzero"`
		Repository string `json:"repository,omitempty"`
	} `json:"drills,omitzero"`
	AgentGuard struct {
		URL     string    `json:"url,omitempty"`
		Timeout *Duration `json:"timeout,omitempty"`
	} `json:"agentGuard,omitzero"`
	EnforceOutputs []hardening.OutputType `json:"enforceOutputs,omitempty"`
	Publisher      struct {
		Minter  bool `json:"minter,omitempty"`
		OctoSTS struct {
			URL       string `json:"url,omitempty"`
			TokenFile string `json:"tokenFile,omitempty"`
			Identity  string `json:"identity,omitempty"`
		} `json:"octoSTS,omitzero"`
	} `json:"publisher,omitzero"`
	Applier struct {
		Interval       *Duration `json:"interval,omitempty"`
		AnswerWait     *Duration `json:"answerWait,omitempty"`
		ApprovalTTL    *Duration `json:"approvalTTL,omitempty"`
		MirrorMaxBytes *int64    `json:"mirrorMaxBytes,omitempty"`
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
		Publisher: Publisher{OctoSTS: PublisherOctoSTS(f.Publisher.OctoSTS), Minter: f.Publisher.Minter}}
	h.Drills.GatewayLog.URL, h.Drills.Repository = f.Drills.GatewayLog.URL, f.Drills.Repository
	if f.Drills.Timeout != nil {
		h.Drills.Timeout = explicitZero(f.Drills.Timeout)
	}
	if f.Drills.GatewayLog.Timeout != nil {
		h.Drills.GatewayLog.Timeout = explicitZero(f.Drills.GatewayLog.Timeout)
	}
	if f.AgentGuard.Timeout != nil {
		h.AgentGuard.Timeout = explicitZero(f.AgentGuard.Timeout)
	}
	if f.Applier.Interval != nil {
		h.Applier.Interval = explicitZero(f.Applier.Interval)
	}
	if f.Applier.AnswerWait != nil {
		h.Applier.AnswerWait = explicitZero(f.Applier.AnswerWait)
	}
	if f.Applier.ApprovalTTL != nil {
		h.Applier.ApprovalTTL = explicitZero(f.Applier.ApprovalTTL)
	}
	if f.Applier.MirrorMaxBytes != nil {
		h.Applier.MirrorMaxBytes = *f.Applier.MirrorMaxBytes
		if h.Applier.MirrorMaxBytes == 0 {
			h.Applier.MirrorMaxBytes = -1 // written as zero: Validate refuses it
		}
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
	if h.Drills.Timeout.Duration == 0 {
		h.Drills.Timeout.Duration = defaultDrillTimeout
	}
	if h.Drills.GatewayLog.Timeout.Duration == 0 {
		h.Drills.GatewayLog.Timeout.Duration = defaultGatewayLogTimeout
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
	if h.Applier.ApprovalTTL.Duration == 0 {
		h.Applier.ApprovalTTL.Duration = defaultApprovalTTL
	}
	if h.Applier.MirrorMaxBytes == 0 {
		h.Applier.MirrorMaxBytes = defaultMirrorMaxBytes
	}
}

// EnforcesPush reports whether any repository enforces push: only then does the factory need its
// git cache (MAKU_GIT_CACHE) and the applier its pusher (H3).
func (h Hardening) EnforcesPush() bool {
	if slices.Contains(h.EnforceOutputs, hardening.Push) {
		return true
	}
	for _, r := range h.Repositories {
		if slices.Contains(r.EnforceOutputs, hardening.Push) {
			return true
		}
	}
	return false
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

// TrustPolicy is the author-trust policy repository's text is levelled by (UH6), at floor, the
// repository's own floor: its maintainers, its merger and policy-bot logins as trusted bots, and
// hardening.blockedUsers. The reconciler's input record and forge_read's filter read the same one.
func (c *Config) TrustPolicy(repository string, floor hardening.Level) hardening.Policy {
	pol := hardening.Policy{Min: floor, Blocked: c.Hardening.BlockedUsers}
	if repo := c.RepositoryFor(repository); repo != nil {
		pol.Maintainers = repo.Maintainers
		for _, b := range []string{repo.Merge.MergerLogin, repo.Merge.PolicyBotLogin} {
			if b != "" {
				pol.TrustedBots = append(pol.TrustedBots, b)
			}
		}
	}
	return pol
}

// DrillsOn reports whether injection drills run (HR38): a gateway log to score them from, outside
// the demo. The drill runner, the manifest route and every effect of a drill label hang on it.
func (c *Config) DrillsOn() bool { return !c.Demo && c.Hardening.Drills.GatewayLog.URL != "" }

// DrillTask reports whether a drill-labelled task of repository is a drill task: the drills are on,
// and repository is the drill repository when one is configured. Otherwise the label is ignored: it
// neither holds a room nor marks a T5 call as a drill's, which would hide a false positive (AC6).
func (c *Config) DrillTask(repository string) bool {
	r := c.Hardening.Drills.Repository
	return c.DrillsOn() && (r == "" || strings.EqualFold(r, repository))
}

// DrillPrincipal is the drill planter's system principal on the factory API: the
// api.systemPrincipals value the drill manifest route admits (HR38).
const DrillPrincipal = "system:drill"

// BrokerPrincipal is the room broker's system principal on the factory API: the api.systemPrincipals
// value forge_read's route admits (HR35).
const BrokerPrincipal = "system:broker"

// validateModeEnforce holds every repository whose effective mode is enforce to what enforce means
// (HR34, HR36): all five output types enforced, runs on the run-controller (on the XR the factory
// cannot see the sandbox's egress, so R5 cannot hold), and a system:broker in api.systemPrincipals,
// the one caller of the forge-read route.
func (c *Config) validateModeEnforce(bad func(string, ...any)) {
	h := c.Hardening
	broker := slices.Contains(slices.Collect(maps.Values(c.API.SystemPrincipals)), BrokerPrincipal)
	for _, r := range c.Repos() {
		if h.ModeFor(r.Name) != hardening.ModeEnforce {
			continue
		}
		key := "hardening mode enforce on " + r.Name
		switch {
		case !c.onRunController():
			bad("%s: R5 cannot hold on the XR: forge_read needs the run-controller's egress (HR36)", key)
		case !slices.Equal(h.EnforcedFor(r.Name), slices.Sorted(slices.Values(enforceable))):
			bad("%s needs all five output types in enforceOutputs: pr, comment, labels, issue, push", key)
		case !broker:
			bad("%s needs api.systemPrincipals to map the broker's ServiceAccount to %s: forge_read reads through the factory", key, BrokerPrincipal)
		}
	}
}

func (c *Config) validateHardening(bad func(string, ...any)) {
	h := c.Hardening
	if _, err := hardening.ParseMode(string(h.Mode)); err != nil {
		bad("hardening.mode: %v", err)
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
	h.Drills.validate(bad)
	if r := h.Drills.Repository; r != "" && c.RepositoryFor(r) == nil {
		bad("hardening.drills.repository %q is not a configured repository", r)
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
		}
	}
	h.validateStricter(bad)
	h.validateEnforce(bad)
	c.validateModeEnforce(bad)
	if h.Publisher.Minter && !c.RunTokens.Enabled {
		bad("hardening.publisher.minter needs runTokens.enabled: the publisher's token is minted from the agents App it names")
	}
}

// enforceable are the output types the factory may publish: stage H2's four, and push from stage
// H3 (HR34).
var enforceable = []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels, hardening.Issue, hardening.Push}

// validateEnforce refuses an enforceOutputs list with an unknown type, a repository list
// that drops a central type, a list on a repository in mode off, a list with no publisher (octo-sts
// or the minter, never both), and an applier timing that is not positive (HR10, HR11, HR15, HR19,
// plan PR7).
func (h Hardening) validateEnforce(bad func(string, ...any)) {
	types := func(key string, list []hardening.OutputType) {
		for _, t := range list {
			if !slices.Contains(enforceable, t) {
				bad("%s: %q is none of pr, comment, labels, issue, push", key, t)
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
	p := h.Publisher.OctoSTS
	switch {
	case h.Publisher.Minter && p.URL != "":
		bad("hardening.publisher.minter and hardening.publisher.octoSTS.url both name the publisher's token: choose one")
	case h.Enforcing() && !h.Publisher.Minter:
		if p.URL == "" {
			bad("hardening.publisher.octoSTS.url is required with enforceOutputs, or hardening.publisher.minter: the applier writes as the publisher (HR11)")
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
	if d := h.Applier.ApprovalTTL.Duration; d <= 0 || d > maxApprovalTTL {
		bad("hardening.applier.approvalTTL must be positive and at most %s (7 days)", maxApprovalTTL)
	}
	if h.Applier.MirrorMaxBytes < minMirrorMaxBytes {
		bad("hardening.applier.mirrorMaxBytes must be at least %d (64 MiB)", minMirrorMaxBytes)
	}
}

// validate refuses a classifier URL that is not plain http(s) with a host, or carries credentials,
// and a timeout outside (0, 10s].
func (d HardeningDrills) validate(bad func(string, ...any)) {
	if d.Timeout.Duration <= 0 || d.Timeout.Duration > maxDrillTimeout {
		bad("hardening.drills.timeout must be positive and at most %s", maxDrillTimeout)
	}
	if g := d.GatewayLog; g.URL != "" {
		if u, err := url.Parse(g.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
			u.RawQuery != "" || u.Fragment != "" {
			bad("hardening.drills.gatewayLog.url must be an http or https URL with a host, and no credentials or query")
		}
	}
	if t := d.GatewayLog.Timeout.Duration; t <= 0 || t > maxGatewayLogTimeout {
		bad("hardening.drills.gatewayLog.timeout must be positive and at most %s", maxGatewayLogTimeout)
	}
}

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

// HardeningSettings are the facts readiness reads from this configuration for repository.
func (c *Config) HardeningSettings(repository, makuVersion string) hardening.Settings {
	h := c.Hardening
	mode := h.ModeFor(repository)
	return hardening.Settings{Mode: mode, Layers: hardening.LayersFor(mode, h.EffectiveClassifier()),
		Demo: c.Demo, DrillMaxAge: h.Drills.MaxAge.Duration, ConfigHash: h.Hash(), Version: makuVersion,
		TokenSource: c.TokenSource(), PublisherMinter: h.Publisher.Minter, ReadFilter: c.readFilter(repository)}
}

// readFilter is R5 as this configuration states it (HR43): the repository is at mode enforce, its
// runs are on the run-controller (on the XR the factory cannot see the sandbox's egress), a
// ServiceAccount maps to system:broker, the forge-read route's one caller, and the factory writes
// forge-api: deny on every run of it, which it does at mode enforce outside the demo. A
// run-controller older than the factory would ignore the annotation: the chart pins both to one
// version, and runbook H4-1 probes it.
func (c *Config) readFilter(repository string) bool {
	return !c.Demo && c.Hardening.ModeFor(repository) == hardening.ModeEnforce && c.onRunController() &&
		slices.Contains(slices.Collect(maps.Values(c.API.SystemPrincipals)), BrokerPrincipal)
}

// onRunController reports whether runs are the run-controller's: a runs group that is set and is
// not cloud-native-ref's XR (omitted is the XR, as TokenSource reads it). Mode enforce's check and
// R5 read the same answer.
func (c *Config) onRunController() bool { return c.Runs.Group != "" && !c.Runs.XR() }

// TokenSource is where the runs' sandbox tokens come from (plan PR8): octo-sts on the XR, the
// minter's role table under runTokens.perRun, and otherwise a shared token that grants writes.
func (c *Config) TokenSource() hardening.TokenSource {
	switch {
	case c.Runs.Group == "" || c.Runs.XR(): // omitted is the XR, as Parse defaults it
		return hardening.TokenSourceXR
	case c.RunTokens.PerRun:
		return hardening.TokenSourceMinter
	}
	return hardening.TokenSourceShared
}
