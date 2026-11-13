// SPDX-License-Identifier: Apache-2.0

// Package config is the broker's one config file (a ConfigMap, Flux-substituted).
// Load decodes it strictly, applies the defaults and validates it, so a typo or a
// bad pattern fails the rollout rather than the first request.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/authn"
	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/sponsored"
	"github.com/Smana/maku/internal/webhook"
)

// The :8443 key pair's defaults: cert-manager's Certificate room-broker-tls (GP-18).
const (
	DefaultCertFile = "/etc/room-broker/tls/tls.crt"
	DefaultKeyFile  = "/etc/room-broker/tls/tls.key"
)

// IssuerConfig is one allowlisted token issuer. SubPattern, for a run issuer
// only, maps a subject to its runId with exactly one capture group.
type IssuerConfig struct {
	Issuer     string `json:"issuer"`
	JWKSURL    string `json:"jwksURL"`
	SubPattern string `json:"subPattern,omitempty"`
	// Mode names how the issuer's tokens are verified: "" or "jwks" against JWKSURL; "tokenreview"
	// asks the apiserver, for a cluster whose issuer is not reachable (kind, adoption 3b).
	Mode string `json:"mode,omitempty"`
}

// How a token issuer is verified. An empty mode is JWKS: the bytes a deployed broker already
// carries need no edit.
const (
	// IssuerModeJWKS verifies a token offline against the issuer's JWKS.
	IssuerModeJWKS = "jwks"
	// IssuerModeTokenReview asks the apiserver, so no issuer URL has to be reachable from the pod.
	IssuerModeTokenReview = "tokenreview"
)

// EffectiveMode is the mode this issuer serves: JWKS unless another is set.
func (is IssuerConfig) EffectiveMode() string {
	if is.Mode == "" {
		return IssuerModeJWKS
	}
	return is.Mode
}

// The human listener's modes. An empty mode is OIDC: the bytes a deployed broker already carries
// need no edit, and a local one is explicit (adoption 3b).
const (
	// HumanModeOIDC is the ZITADEL-backed default: oauth2-proxy in front, a token per member.
	HumanModeOIDC = "oidc"
	// HumanModeLocal is one loopback user behind a port-forward, for an install with no IdP.
	HumanModeLocal = "local"
)

// HumanConfig is the viewers' identity provider (phase 2). The client and project
// ids are read from mounted files at use, never literals (Ruling AS-a): the IdP
// mints them, anew on every gcp-0 build, and a rotation needs no restart.
type HumanConfig struct {
	// Mode is "" or "oidc" (the ZITADEL-backed default), or "local": one loopback user behind a
	// port-forward, for an install with no IdP (adoption 3b).
	Mode string `json:"mode,omitempty"`
	// Preset is "" or "zitadel" (today's token rules, byte for byte), or "generic": any OIDC issuer,
	// its roles claim named by RolesClaim (v4 D8).
	Preset string `json:"preset,omitempty"`
	Issuer string `json:"issuer"`
	// JWKSURL is optional when Issuer is https: its discovery document names jwks_uri.
	JWKSURL             string `json:"jwksURL"`
	ClientIDFile        string `json:"clientIDFile"`                  // the rooms-proxy client id, from agents-secrets
	RoomctlClientIDFile string `json:"roomctlClientIDFile,omitempty"` // phase 6
	ProjectIDFile       string `json:"projectIDFile"`                 // a human token's aud must hold it (Ruling AS)
	// TokenUse is what a human presents: "id" (the ID token is the credential, generic's default)
	// or "access" (the ID-plus-access pair, zitadel's only one).
	TokenUse string `json:"tokenUse,omitempty"`
	// Audience, or AudienceFile read at use, is the aud value every access token must hold under
	// preset generic: required with tokenUse access, and with a sponsored principal. The zitadel
	// preset's is ProjectIDFile.
	Audience     string `json:"audience,omitempty"`
	AudienceFile string `json:"audienceFile,omitempty"`
	// RolesClaim names the claim holding Groups' values, or an RFC 6901 pointer to it when it
	// starts with "/": required with preset generic. Unset under zitadel: groups, else the keys
	// of urn:zitadel:iam:org:project:roles.
	RolesClaim string `json:"rolesClaim,omitempty"`
	// Scopes are added to openid profile email offline_access by maku login.
	Scopes []string     `json:"scopes,omitempty"`
	Origin string       `json:"origin"`
	Groups GroupsConfig `json:"groups"`
	// Access turns on D7: a member sees a room only if they can read its repository
	// on GitHub. Unset, rooms are visible to admins only, never to every member.
	Access *AccessConfig `json:"access,omitempty"`
	// MCP serves /mcp, the MCP server maku mcp relays (factory interfaces F2). Off unless
	// enabled: v3 ships each feature behind a switch (D4).
	MCP MCPConfig `json:"mcp,omitzero"`
	// Needs serves GET /api/needs, needs/v1's human door (CD28): every room's needs for one caller,
	// read-only, with a `since` cursor. On unless a file turns it off.
	Needs NeedsConfig `json:"needs,omitzero"`
	// Dashboards are the room page's links out, such as Grafana's, filled with the room's ids.
	// None by default: the page then shows no group at all.
	Dashboards []DashboardConfig `json:"dashboards,omitempty"`
	// SlackLink turns on the Slack self-serve link: the Slack App mints one-time codes, and a
	// signed-in human confirms one on /link/slack. Off unless set.
	SlackLink *SlackLinkConfig `json:"slackLink,omitempty"`
	// SlackDecide turns on POST /api/slack/decisions: the Slack App's Approve/Deny buttons decide a
	// forge.push approval. Off unless set: the path is then the mux's 404.
	SlackDecide *SlackDecideConfig `json:"slackDecide,omitempty"`
	// SlackIntents serves intent_classify, the Slack App's read-only classification of a thread reply
	// (natural-language intents, SI3). Off unless set: the tool is then on no list.
	SlackIntents *SlackIntentsConfig `json:"slackIntents,omitempty"`
	// Intake is the governed planner (v3.x conversational intake, CD5, CD17, CD18). Off unless the
	// block turns it on: no block, no tool on any list.
	Intake IntakeConfig `json:"intake,omitzero"`
	// ApprovalMemory serves saved approval grants (merge policy stage 5): the
	// /settings/approval-memory page and its API to humans, and the grant routes on :8443 to the
	// factory. Off unless set; the factory consults grants only under its own
	// policyManagement.approvalMemory.
	ApprovalMemory bool `json:"approvalMemory,omitempty"`
}

// IntakeConfig is human.intake. It holds one block today; it exists so the planner's switch has a
// name of its own beside the other v3 switches, rather than growing on the listener's config.
type IntakeConfig struct {
	Plan PlanConfig `json:"plan,omitzero"`
}

// The planner's one route, and the values that are not a deployment's to choose.
const (
	// PlanModel is the one alias the planner may name: the gateway serves `tier-frontier` to the
	// `planner` client, and any other alias is a different, unreviewed spend (CD17).
	PlanModel = "tier-frontier"
	// DefaultPlanDeadline is 20 s, under /mcp's 25 s bound (mcp.go:64): a plan that outlives it is
	// refused with planner_timeout rather than hanging the tool.
	DefaultPlanDeadline = 20 * time.Second
	// DefaultDailyPlans is the per-requester daily cap (spec §budget): one call per plan attempt,
	// never a retry loop.
	DefaultDailyPlans = 50
	// DefaultMaxRounds is the clarification-round cap. Reaching it accepts the remaining
	// recommendations deterministically rather than making another model call.
	DefaultMaxRounds = 10
)

// The degraded composer's two positions (CD18): it is off unless a deployment says `composer`, and
// every surface that renders it labels it as not the natural-language experience.
const (
	PlanFallbackOff      = "off"
	PlanFallbackComposer = "composer"
)

// planEfforts are the reasoning efforts the planner may ask for: OpenAI's chat-completions
// `reasoning_effort` values. Whether the model behind `tier-frontier` takes each is the provider's:
// Z.ai's glm-5.3 names low, high and max.
var planEfforts = []string{"low", "medium", "high"}

// planRoles are the roles the factory runs. A class's template names them, and a role the factory
// does not have would be a plan it cannot run.
var planRoles = []string{"scoper", "tester", "implementer", "reviewer", "triager"}

// PlanConfig is human.intake.plan: the switch, the route to the gateway, the cap, the repositories
// the planner may ever name (CD5) and the class table its roles and money come from (CD17).
type PlanConfig struct {
	// Enabled serves intake_plan and plan_check. Off unless set: neither tool is on any list, so a
	// deployment that has not declared the block answers `unknown tool`, as run_create does to a
	// human token today.
	Enabled bool `json:"enabled,omitempty"`
	// URL is the model gateway's in-cluster route (agentgateway, `maku-gateway`: its clients
	// listener's /clients/planner/v1), straight to it: a package never hard-codes a platform
	// endpoint, and factoryURL is configured the same way.
	URL string `json:"url,omitempty"`
	// Model is the pinned alias, and the only value it may hold (CD17); empty takes PlanModel.
	Model string `json:"model,omitempty"`
	// APIKeyFile is the `planner` client's bearer (the broker's projected token, or a mounted key),
	// read at use so a rotation needs no restart.
	APIKeyFile string `json:"apiKeyFile,omitempty"`
	// Deadline bounds the one model call; empty takes DefaultPlanDeadline.
	Deadline Duration `json:"deadline,omitzero"`
	// ReasoningEffort is sent as the call's `reasoning_effort` when set (low, medium or high); empty
	// sends nothing and the model's default stands. A model that always reasons (glm-5.3) spends the
	// whole 20 s deadline thinking at its default, and answers in about 4 s at "low".
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// DailyPlans is the cap per requester per UTC day; empty takes DefaultDailyPlans.
	DailyPlans int `json:"dailyPlans,omitempty"`
	// MaxRounds is the clarification-round cap; empty takes DefaultMaxRounds. A revision that
	// reaches it is finished deterministically, with every remaining recommendation accepted.
	MaxRounds int `json:"maxRounds,omitempty"`
	// Fallback is "off" or "composer": only the second reaches the degraded deterministic composer,
	// and it is unreachable otherwise (CD18).
	Fallback string `json:"fallback,omitempty"`
	// Repositories is the allowlist the candidate set is drawn from (CD5): the planner never plans
	// for a repository this list does not carry. It is deliberately not human.mcp.sessionStart,
	// which means "the form may offer to start the factory here".
	Repositories []string `json:"repositories,omitempty"`
	// Classes is the class table: the roles a plan's issues carry and the money they are charged
	// against both come from here, never from the model (CD17).
	Classes map[string]PlanClass `json:"classes,omitempty"`
}

// PlanClass is one class's template: the roles the factory runs for it, the ceiling the class's
// `taskUSD` sets, and the source string the plan displays as where that ceiling came from.
type PlanClass struct {
	Roles      []string `json:"roles"`
	CeilingUSD float64  `json:"ceilingUSD"`
	Source     string   `json:"source"`
}

// IntakePlan is the planner's config with its defaults applied: `tier-frontier`, 20 s, 50 plans a day,
// 10 clarification rounds and the degraded composer off. A caller reads this rather than re-spelling
// a default, so an absent block and a block that leaves them out serve the same values.
func (c Config) IntakePlan() PlanConfig { return c.Human.Intake.Plan.withDefaults() }

// withDefaults fills what a file may leave out. Only the absent value is defaulted: an explicit
// `deadline: -5s` stays negative and is refused, so no typo hides behind a default.
func (p PlanConfig) withDefaults() PlanConfig {
	if p.Model == "" {
		p.Model = PlanModel
	}
	if p.Deadline.Duration == 0 {
		p.Deadline.Duration = DefaultPlanDeadline
	}
	if p.DailyPlans == 0 {
		p.DailyPlans = DefaultDailyPlans
	}
	if p.MaxRounds == 0 {
		p.MaxRounds = DefaultMaxRounds
	}
	if p.Fallback == "" {
		p.Fallback = PlanFallbackOff
	}
	return p
}

// validate reports every problem in human.intake.plan at once, reading the defaulted values: a file
// that leaves out the deadline, the cap or the fallback is validated as the broker would serve it.
//
// A switch that is off is still checked for shape: ai-native-ref's config copies carry the block
// before any deployment flips it, so a route with no scheme or an alias other than `tier-frontier`
// fails the rollout that wrote it, not the first plan it serves.
func (i IntakeConfig) validate() []error {
	p := i.Plan.withDefaults()
	var errs []error
	if p.Enabled {
		if p.URL == "" {
			errs = append(errs, errors.New("human.intake.plan.url: is required when the planner is enabled: the route is named, never guessed"))
		}
		if p.APIKeyFile == "" {
			errs = append(errs, errors.New("human.intake.plan.apiKeyFile: is required when the planner is enabled: a planner with no key would 401 every plan"))
		}
		// A planner with nothing to serve is a switch that reads as on and refuses every request:
		// the allowlist is the candidate set's whole supply (CD5) and the class table is every
		// issue's roles and money (CD17).
		if len(p.Repositories) == 0 {
			errs = append(errs, errors.New("human.intake.plan.repositories: is required when the planner is enabled: the allowlist is every repository a plan may name (CD5)"))
		}
		if len(p.Classes) == 0 {
			errs = append(errs, errors.New("human.intake.plan.classes: is required when the planner is enabled: a plan's roles and ceiling come from its class (CD17)"))
		}
	}
	if p.URL != "" {
		if u, err := url.Parse(p.URL); err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Errorf("human.intake.plan.url: %q must be an absolute http(s)://host[:port]/ URL with no userinfo", p.URL))
		}
	}
	if p.Model != PlanModel {
		errs = append(errs, fmt.Errorf("human.intake.plan.model: %q is not %s, the one alias the gateway serves this client (CD17)", p.Model, PlanModel))
	}
	if p.Deadline.Duration <= 0 || p.Deadline.Duration > DefaultPlanDeadline {
		errs = append(errs, fmt.Errorf("human.intake.plan.deadline: %s must be above 0 and at most %s, under /mcp's 25 s bound", p.Deadline.Duration, DefaultPlanDeadline))
	}
	if p.ReasoningEffort != "" && !slices.Contains(planEfforts, p.ReasoningEffort) {
		errs = append(errs, fmt.Errorf("human.intake.plan.reasoningEffort: %q is not one of %s, or unset", p.ReasoningEffort, strings.Join(planEfforts, ", ")))
	}
	if p.DailyPlans < 1 {
		errs = append(errs, fmt.Errorf("human.intake.plan.dailyPlans: %d must be at least 1 per requester", p.DailyPlans))
	}
	if p.MaxRounds < 1 {
		errs = append(errs, fmt.Errorf("human.intake.plan.maxRounds: %d must be at least 1", p.MaxRounds))
	}
	if p.Fallback != PlanFallbackOff && p.Fallback != PlanFallbackComposer {
		errs = append(errs, fmt.Errorf("human.intake.plan.fallback: %q is not %q or %q (CD18)", p.Fallback, PlanFallbackOff, PlanFallbackComposer))
	}
	seen := map[string]bool{}
	for n, r := range p.Repositories {
		key := strings.ToLower(r)
		switch {
		case !repositoryRE.MatchString(r):
			errs = append(errs, fmt.Errorf("human.intake.plan.repositories[%d]: %q is not owner/name", n, r))
		case seen[key]:
			errs = append(errs, fmt.Errorf("human.intake.plan.repositories: %q is declared twice", r))
		}
		seen[key] = true
	}
	// In name order, so two runs of the same file report the same lines in the same order.
	for _, name := range slices.Sorted(maps.Keys(p.Classes)) {
		errs = append(errs, p.Classes[name].validate(name)...)
	}
	return errs
}

// validate is one class's row: a template the factory can run, and money that says where it came
// from. Roles and budget are the config's alone (CD17) — a reply naming either changes neither.
func (c PlanClass) validate(name string) []error {
	at := fmt.Sprintf("human.intake.plan.classes[%q]", name)
	var errs []error
	if name == "" {
		errs = append(errs, errors.New("human.intake.plan.classes: a class needs a name: a reply names it and the plan hashes it"))
	}
	if len(c.Roles) == 0 {
		errs = append(errs, fmt.Errorf("%s: roles is empty: a plan's roles are its class's template (CD17)", at))
	}
	seen := map[string]bool{}
	for _, r := range c.Roles {
		switch {
		case !slices.Contains(planRoles, r):
			errs = append(errs, fmt.Errorf("%s: role %q is not one of %s: the factory runs no such role", at, r, strings.Join(planRoles, ", ")))
		case seen[r]:
			errs = append(errs, fmt.Errorf("%s: role %q is declared twice", at, r))
		}
		seen[r] = true
	}
	if c.CeilingUSD <= 0 {
		errs = append(errs, fmt.Errorf("%s: ceilingUSD %g must be above 0: a plan with no ceiling has no budget", at, c.CeilingUSD))
	}
	if c.Source == "" {
		errs = append(errs, fmt.Errorf("%s: source is required: the plan displays where its ceiling came from", at))
	}
	return errs
}

// EffectiveMode is the mode the broker serves: an empty mode is the OIDC deployment every existing
// config carries, so nothing has to be added to it.
func (h HumanConfig) EffectiveMode() string {
	if h.Mode == "" {
		return HumanModeOIDC
	}
	return h.Mode
}

// The human block's presets (v4 D8).
const (
	// PresetZITADEL keeps today's token rules: the project id in every token's aud, the groups
	// claim or the project roles, token use access. An empty preset is this one.
	PresetZITADEL = "zitadel"
	// PresetGeneric reads any OIDC issuer: a configured roles claim, token use id by default.
	PresetGeneric = "generic"
)

// What a human presents (v4 D8, PR3).
const (
	// TokenUseID makes the ID token the credential.
	TokenUseID = authn.TokenUseID
	// TokenUseAccess is the ID-plus-access pair for the web, and maku's access token as a bearer.
	TokenUseAccess = authn.TokenUseAccess
)

// EffectivePreset is the preset the broker serves: an empty one is zitadel, the deployment every
// existing config carries.
func (h HumanConfig) EffectivePreset() string {
	if h.Preset == "" {
		return PresetZITADEL
	}
	return h.Preset
}

// EffectiveTokenUse is the configured token use, else the preset's: zitadel's access, generic's id.
func (h HumanConfig) EffectiveTokenUse() string {
	switch {
	case h.TokenUse != "":
		return h.TokenUse
	case h.EffectivePreset() == PresetGeneric:
		return TokenUseID
	}
	return TokenUseAccess
}

// EffectiveGroups is the two group names the listener admits and the policy matrix reads. Local
// mode has one human and no IdP to name a group, so it takes the two fixed names; every other mode
// keeps the configured ones.
func (h HumanConfig) EffectiveGroups() GroupsConfig {
	if h.EffectiveMode() == HumanModeLocal {
		return localGroups
	}
	return h.Groups
}

// SlackLinkConfig is human.slackLink: the sponsored principal (one of human.mcp.sponsored, the
// Slack App) that may mint link codes, the one Slack workspace a code may be for, and the
// repository whose GitHub App installation names the account's login on the page.
type SlackLinkConfig struct {
	Principal  string `json:"principal"`
	TeamID     string `json:"teamID"`
	Repository string `json:"repository"`
}

// slackTeamRE is a Slack workspace id, as the App's.
var slackTeamRE = regexp.MustCompile(`^T[A-Z0-9]{2,20}$`)

// SlackDecideConfig is human.slackDecide: the sponsored principal (the Slack App) that may post a
// decision, and the one Slack workspace whose clicks are this broker's (v3.x Slack push approval
// D14). On, it serves POST /api/slack/decisions; off, the path is a 404. The class it decides is a
// constant (D13), so it is not configurable here.
type SlackDecideConfig struct {
	Principal string `json:"principal"`
	TeamID    string `json:"teamID"`
}

// The classifier's one alias and its defaults (SI3, SI13, SI18, SI19). The alias is a constant as
// PlanModel is: the gateway serves it to the `classifier` client, and any other alias is a different,
// unreviewed spend.
const (
	// ClassifyModel is the one alias intent_classify may name.
	ClassifyModel = "tier-light"
	// DefaultClassifyDeadline bounds the one call; a reply is waiting in a thread.
	DefaultClassifyDeadline = 5 * time.Second
	// DefaultMinConfidence is SI18's threshold: under it a classification is unknown.
	DefaultMinConfidence = 0.7
	// DefaultConfirmTTL is SI18's lifetime of a confirm message.
	DefaultConfirmTTL = 10 * time.Minute
	// DefaultMaxInputChars is SI18's cap on the reply the model is shown.
	DefaultMaxInputChars = 2000
	// DefaultIntentDailyCalls is SI13's cap on the App's calls in a UTC day.
	DefaultIntentDailyCalls = 500
	// DefaultIntentPerUserPerHour is SI13's per-user cap.
	DefaultIntentPerUserPerHour = 20
	// DefaultIntentRetentionDays is SI19's retention of an opted-in channel's replies.
	DefaultIntentRetentionDays = 30
)

// SlackIntentsConfig is human.slackIntents: the sponsored principal (the Slack App) that may ask for
// a classification, the one workspace whose users are classified, the route to the gateway and the
// bounds of the call (SI3, SI12, SI13, SI18, SI19). On, it serves intent_classify; off, no list names
// it.
type SlackIntentsConfig struct {
	Principal string `json:"principal"`
	TeamID    string `json:"teamID"`
	// URL is the model gateway's in-cluster route (agentgateway, `maku-gateway`: its clients
	// listener's /clients/classifier/v1), and APIKeyFile the `classifier` client's bearer (the
	// broker's projected token), read at use so a rotation needs no restart.
	URL        string `json:"url"`
	APIKeyFile string `json:"apiKeyFile"`
	// Model is the pinned alias, and the only value it may hold; empty takes ClassifyModel.
	Model    string   `json:"model,omitempty"`
	Deadline Duration `json:"deadline,omitzero"`
	// MinConfidence, ConfirmTTL and MaxInputChars are SI18's tunable thresholds.
	MinConfidence float64  `json:"minConfidence,omitempty"`
	ConfirmTTL    Duration `json:"confirmTTL,omitzero"`
	MaxInputChars int      `json:"maxInputChars,omitempty"`
	// DailyCalls and PerUserPerHour are SI13's caps on the App's own budget.
	DailyCalls     int `json:"dailyCalls,omitempty"`
	PerUserPerHour int `json:"perUserPerHour,omitempty"`
	// RetentionDays is how long an opted-in channel's replies are kept (SI19).
	RetentionDays int `json:"retentionDays,omitempty"`
}

// withDefaults fills what a file may leave out; only the absent value is defaulted, so a typo such as
// `confirmTTL: 0s`-by-omission never hides and a negative stays negative and is refused.
func (c SlackIntentsConfig) withDefaults() SlackIntentsConfig {
	if c.Model == "" {
		c.Model = ClassifyModel
	}
	if c.Deadline.Duration == 0 {
		c.Deadline.Duration = DefaultClassifyDeadline
	}
	if c.MinConfidence == 0 {
		c.MinConfidence = DefaultMinConfidence
	}
	if c.ConfirmTTL.Duration == 0 {
		c.ConfirmTTL.Duration = DefaultConfirmTTL
	}
	if c.MaxInputChars == 0 {
		c.MaxInputChars = DefaultMaxInputChars
	}
	if c.DailyCalls == 0 {
		c.DailyCalls = DefaultIntentDailyCalls
	}
	if c.PerUserPerHour == 0 {
		c.PerUserPerHour = DefaultIntentPerUserPerHour
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = DefaultIntentRetentionDays
	}
	return c
}

// SlackIntents is human.slackIntents with its defaults applied, and whether the block is set.
func (c Config) SlackIntents() (SlackIntentsConfig, bool) {
	if c.Human.SlackIntents == nil {
		return SlackIntentsConfig{}, false
	}
	return c.Human.SlackIntents.withDefaults(), true
}

// validate reports every problem in the block at once, reading the defaulted values. sponsored is
// the names human.mcp.sponsored declares.
func (c SlackIntentsConfig) validate(sponsored []SponsoredConfig) []error {
	c = c.withDefaults()
	var errs []error
	if !slices.ContainsFunc(sponsored, func(sp SponsoredConfig) bool { return sp.Name == c.Principal }) {
		errs = append(errs, fmt.Errorf("human.slackIntents.principal: %q is not one of human.mcp.sponsored", c.Principal))
	}
	if !slackTeamRE.MatchString(c.TeamID) {
		errs = append(errs, fmt.Errorf("human.slackIntents.teamID: %q is not a Slack workspace id", c.TeamID))
	}
	if c.URL == "" {
		errs = append(errs, errors.New("human.slackIntents.url: is required: the route is named, never guessed"))
	} else if u, err := url.Parse(c.URL); err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		errs = append(errs, fmt.Errorf("human.slackIntents.url: %q must be an absolute http(s)://host[:port]/ URL with no userinfo", c.URL))
	}
	if c.APIKeyFile == "" {
		errs = append(errs, errors.New("human.slackIntents.apiKeyFile: is required: a classifier with no key would 401 every call"))
	}
	if c.Model != ClassifyModel {
		errs = append(errs, fmt.Errorf("human.slackIntents.model: %q is not %s, the one alias the gateway serves this client", c.Model, ClassifyModel))
	}
	if c.Deadline.Duration <= 0 || c.Deadline.Duration > DefaultPlanDeadline {
		errs = append(errs, fmt.Errorf("human.slackIntents.deadline: %s must be above 0 and at most %s", c.Deadline.Duration, DefaultPlanDeadline))
	}
	if c.MinConfidence <= 0 || c.MinConfidence > 1 {
		errs = append(errs, fmt.Errorf("human.slackIntents.minConfidence: %g must be above 0 and at most 1", c.MinConfidence))
	}
	if c.ConfirmTTL.Duration < time.Minute || c.ConfirmTTL.Duration > time.Hour {
		errs = append(errs, fmt.Errorf("human.slackIntents.confirmTTL: %s must be between 1m and 1h", c.ConfirmTTL.Duration))
	}
	if c.MaxInputChars < 200 {
		errs = append(errs, fmt.Errorf("human.slackIntents.maxInputChars: %d must be at least 200", c.MaxInputChars))
	}
	if c.DailyCalls < 1 {
		errs = append(errs, fmt.Errorf("human.slackIntents.dailyCalls: %d must be at least 1", c.DailyCalls))
	}
	if c.PerUserPerHour < 1 {
		errs = append(errs, fmt.Errorf("human.slackIntents.perUserPerHour: %d must be at least 1", c.PerUserPerHour))
	}
	if c.RetentionDays < 1 {
		errs = append(errs, fmt.Errorf("human.slackIntents.retentionDays: %d must be at least 1", c.RetentionDays))
	}
	return errs
}

// NeedsConfig switches the human listener's needs route (CD28). An absent block is on: needs/v1 is a
// read-only list, which the spec ships on with its release, and only an explicit false closes the door.
type NeedsConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// NeedsEnabled is the needs route's switch: an absent block, or a block with no enabled, is true, and
// false is the opt-out the decoder keeps — a plain bool could not tell the absent block from a refusal.
func (h HumanConfig) NeedsEnabled() bool {
	if h.Needs.Enabled == nil {
		return true
	}
	return *h.Needs.Enabled
}

// MCPConfig switches the human listener's MCP server.
type MCPConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	// Sponsored are the service accounts that may call run_create (factory interfaces F4, R13);
	// each is also declared in the factory's api.sponsored, which decides everything else.
	Sponsored []SponsoredConfig `json:"sponsored,omitempty"`
	// SponsoredClientClaim names the claim holding a sponsored token's client id under preset
	// generic, for an IdP that puts it elsewhere than azp or client_id: cid (Okta) or appid
	// (Entra). It mirrors the factory's api.sponsoredClientClaim, so both admit the same principals.
	SponsoredClientClaim string `json:"sponsoredClientClaim,omitempty"`
	// SessionStart lists the repositories ("owner/name") whose issue_draft says canStart: the
	// handoff prompt may start the factory there for a caller who writes to them. Empty, none does.
	SessionStart []string `json:"sessionStart,omitempty"`
}

// RoomToolsConfig switches the agents' :8090 room tools listener (I8). It sits at the top level, apart
// from human.mcp: the two MCP servers are different trust domains on different ports.
type RoomToolsConfig struct {
	// Enabled unset means on, today's :8090 listener: every deployed config keeps working with no
	// edit. An explicit false binds no port at all — not even one to close again — and starts no
	// server, so a broker with no room-tool client stays clean.
	Enabled *bool `json:"enabled,omitempty"`
	// Auth is who vouches for a caller: "" or "key", a gateway that verified the run's token and
	// injects ROOMS_MCP_KEY and the subject (production); "tokenreview", the broker itself asks the
	// apiserver about the run's own bearer token, where no gateway stands in front (the demo).
	Auth string `json:"auth,omitempty"`
	// PushRepositories are the repositories :8090 serves output_push to (hardening H3): the
	// broker's own gate beside the run's safe-outputs annotation, so a claim alone never opens push.
	// It mirrors the factory's enforceOutputs naming push (stage H3). Empty by default: no run is
	// served output_push.
	PushRepositories []string `json:"pushRepositories,omitempty"`
	// ForgeReadTokenFile is the broker's projected ServiceAccount token for the factory's system
	// audience (hardening H4, HR35): set, :8090 serves forge_read to a run annotated forge-api: deny,
	// read and filtered by the factory at factoryURL. Empty by default: forge_read is not served.
	ForgeReadTokenFile string `json:"forgeReadTokenFile,omitempty"`
	// Knowledge is a knowledge MCP endpoint, RunLore's (EX11, owner ruling R5): :8090 forwards
	// kb_search and kb_get to it for the roles it names. Absent, no knowledge tool is served.
	Knowledge *KnowledgeConfig `json:"knowledge,omitempty"`
}

// KnowledgeConfig is roomTools.knowledge.
type KnowledgeConfig struct {
	// URL is the endpoint, MCP over Streamable HTTP: https, or http to an in-cluster Service.
	URL string `json:"url"`
	// Tools are the ones forwarded, a subset of kb_search and kb_get; both by default.
	Tools []string `json:"tools,omitempty"`
	// Roles are the AgentRun roles that list and may call them; triager by default.
	Roles []string `json:"roles,omitempty"`
	// Timeout bounds one call, 1 s to 12 s, under :8090's 15 s bound on any call; 10 s by default.
	Timeout Duration `json:"timeout,omitzero"`
	// MaxBytes is the most of an answer forwarded, at most 256 KiB; 64 KiB by default.
	MaxBytes int `json:"maxBytes,omitempty"`
}

// The knowledge endpoint's defaults and bounds (EX11).
const (
	DefaultKnowledgeTimeout  = 10 * time.Second
	DefaultKnowledgeMaxBytes = 64 << 10
	minKnowledgeTimeout      = time.Second
	maxKnowledgeTimeout      = 12 * time.Second
	maxKnowledgeMaxBytes     = 256 << 10
)

var (
	knowledgeTools = []string{"kb_search", "kb_get"}
	// knowledgeRoles are the roles a run serves under on :8090.
	knowledgeRoles = []string{"implementer", "reviewer", "tester", "triager"}
	// serviceHost is an in-cluster Service: <svc>.<ns>.svc, then the cluster domain if any.
	serviceHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\.[a-z0-9]([a-z0-9-]*[a-z0-9])?\.svc(\.[a-z0-9]([a-z0-9.-]*[a-z0-9])?)?$`)
)

// Knowledge is roomTools.knowledge with its defaults; a zero value (no URL) serves nothing.
func (c Config) Knowledge() KnowledgeConfig {
	if c.RoomTools == nil || c.RoomTools.Knowledge == nil {
		return KnowledgeConfig{}
	}
	return *c.RoomTools.Knowledge
}

func (k *KnowledgeConfig) applyDefaults() {
	if k.Tools == nil {
		k.Tools = slices.Clone(knowledgeTools)
	}
	if k.Roles == nil {
		k.Roles = []string{"triager"}
	}
	if k.Timeout.Duration == 0 {
		k.Timeout.Duration = DefaultKnowledgeTimeout
	}
	if k.MaxBytes == 0 {
		k.MaxBytes = DefaultKnowledgeMaxBytes
	}
}

// knowledgeScheme is the model URL's rule: https, or http to an in-cluster Service.
func knowledgeScheme(u *url.URL) bool {
	return u.Scheme == "https" || u.Scheme == "http" && serviceHost.MatchString(u.Hostname())
}

func (k KnowledgeConfig) validate() []error {
	var errs []error
	u, err := url.Parse(k.URL)
	switch {
	case k.URL == "":
		errs = append(errs, errors.New("roomTools.knowledge.url is required: it is the knowledge MCP endpoint"))
	case err != nil || u.Host == "" || u.User != nil || !knowledgeScheme(u):
		// The value is never quoted: a refused URL may carry credentials or a query token.
		errs = append(errs, errors.New("roomTools.knowledge.url must be https, or http to an in-cluster <svc>.<ns>.svc[.<domain>], with no credentials"))
	}
	if len(k.Tools) == 0 {
		errs = append(errs, errors.New("roomTools.knowledge.tools is empty: name kb_search, kb_get or both"))
	}
	for _, t := range k.Tools {
		if !slices.Contains(knowledgeTools, t) {
			errs = append(errs, fmt.Errorf("roomTools.knowledge.tools: %q is not kb_search or kb_get", t))
		}
	}
	if len(k.Roles) == 0 {
		errs = append(errs, errors.New("roomTools.knowledge.roles is empty"))
	}
	for _, r := range k.Roles {
		if !slices.Contains(knowledgeRoles, r) {
			errs = append(errs, fmt.Errorf("roomTools.knowledge.roles: %q is not implementer, reviewer, tester or triager", r))
		}
	}
	if d := k.Timeout.Duration; d < minKnowledgeTimeout || d > maxKnowledgeTimeout {
		errs = append(errs, fmt.Errorf("roomTools.knowledge.timeout: %s must be within [%s, %s], under :8090's bound on one call", d, minKnowledgeTimeout, maxKnowledgeTimeout))
	}
	if k.MaxBytes < 1 || k.MaxBytes > maxKnowledgeMaxBytes {
		errs = append(errs, fmt.Errorf("roomTools.knowledge.maxBytes: %d must be 1 to %d", k.MaxBytes, maxKnowledgeMaxBytes))
	}
	return errs
}

// PushRepositories are the repositories :8090 serves output_push to: none unless roomTools names
// them.
func (c Config) PushRepositories() []string {
	if c.RoomTools == nil {
		return nil
	}
	return c.RoomTools.PushRepositories
}

// ForgeReadTokenFile is the token forge_read posts to the factory with; "" serves no forge_read.
func (c Config) ForgeReadTokenFile() string {
	if c.RoomTools == nil {
		return ""
	}
	return c.RoomTools.ForgeReadTokenFile
}

// How :8090 authenticates a run. An empty auth is key: the bytes a deployed broker carries need no edit.
const (
	// RoomToolsAuthKey trusts a gateway's injected key and verified subject.
	RoomToolsAuthKey = "key"
	// RoomToolsAuthTokenReview verifies the run's own token (audience room-tools) through the apiserver.
	RoomToolsAuthTokenReview = "tokenreview"
)

// RoomToolsAuth is the auth :8090 serves: key unless tokenreview is set.
func (c Config) RoomToolsAuth() string {
	if c.RoomTools == nil || c.RoomTools.Auth == "" {
		return RoomToolsAuthKey
	}
	return c.RoomTools.Auth
}

// RoomToolsEnabled is the listener's switch: absent block or nil enabled means true, as every
// existing deployment expects.
func (c Config) RoomToolsEnabled() bool {
	if c.RoomTools == nil || c.RoomTools.Enabled == nil {
		return true
	}
	return *c.RoomTools.Enabled
}

// SponsoredConfig is one sponsored principal the broker admits on /mcp.
type SponsoredConfig struct {
	Name         string `json:"name"`
	ClientIDFile string `json:"clientIDFile"`
	// Repositories is the principal's upper bound into a coordination family (v4 MR13): it has
	// no GitHub login, so a child outside this list is restricted. Empty reads no child.
	Repositories []string `json:"repositories,omitempty"`
}

// SponsoredRepositories is each sponsored principal's Repositories, keyed by its principal id
// system:<name>, as humanapi looks it up.
func (m MCPConfig) SponsoredRepositories() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, sp := range m.Sponsored {
		set := map[string]bool{}
		for _, r := range sp.Repositories {
			set[r] = true
		}
		out["system:"+sp.Name] = set
	}
	return out
}

// repositoryRE is an owner/name, as humanapi's.
var repositoryRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// sponsoredNameRE is a sponsored principal's name: a DNS label, as the factory's api.sponsored.
var sponsoredNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// AccessConfig is D7's GitHub check. ReaderFile is the ZITADEL link reader's mounted
// secret, JSON {"pat", "tokenId", "githubIdpId"}, read at use: ZITADEL at the human
// issuer resolves a member to their linked GitHub id. TTL caches each answer.
type AccessConfig struct {
	ReaderFile string   `json:"readerFile"`
	TTL        Duration `json:"ttl,omitempty"`
}

// MaxAccessTTL is the longest a GitHub access answer is trusted, and the default: a
// revoked read must lapse within it (D7).
const MaxAccessTTL = 5 * time.Minute

// Duration is a time.Duration written as a string such as "90s".
type Duration struct{ time.Duration }

// UnmarshalJSON parses a Go duration string; a bare number is refused, since its unit is a guess.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration is a string such as \"90s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// GroupsConfig names the IdP's two agent groups, which feed policy.Groups. They
// are literals: names we choose, not ids the IdP mints.
type GroupsConfig struct {
	Admin  string `json:"admin"`
	Member string `json:"member"`
}

// TLSConfig names the :8443 key pair, re-read when it changes (GP-18).
type TLSConfig struct {
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
}

// Config is the broker's configuration.
type Config struct {
	PublicURL  string         `json:"publicURL"`
	RunIssuers []IssuerConfig `json:"runIssuers"`
	// SystemIssuer is optional: only the factory calls the system routes, and without it they
	// answer 401. systemPrincipals without it is refused: no principal could ever be vouched for.
	SystemIssuer     *IssuerConfig     `json:"systemIssuer,omitempty"`
	SystemPrincipals map[string]string `json:"systemPrincipals"` // token sub -> system:<component>
	Human            HumanConfig       `json:"human"`
	// RoomTools is the agents' :8090 listener (I8). Absent, it starts; explicit false is the opt-out.
	RoomTools  *RoomToolsConfig `json:"roomTools,omitempty"`
	FactoryURL string           `json:"factoryURL,omitempty"`
	// FactoryCAFile is the PEM CA that signs the factory's api certificate, read on every run
	// request so a rotation needs no restart (S-1). Empty: the system roots.
	FactoryCAFile string `json:"factoryCAFile,omitempty"`
	// FactoryInsecure admits an http:// factoryURL. The broker forwards each human's bearer token
	// there, so plaintext is an explicit opt-out, never a default (D6).
	FactoryInsecure bool          `json:"factoryInsecure,omitempty"`
	TLS             TLSConfig     `json:"tls"`
	Suspend         SuspendConfig `json:"suspend,omitzero"`
	// Runs is where AgentRuns live (adoption 3a): cloud-native-ref's XR when omitted.
	Runs runwatch.Kind `json:"runs,omitzero"`
	// Webhooks are the subscribers to room events (factory interfaces F6).
	Webhooks []WebhookConfig `json:"webhooks,omitempty"`
	// Coordination is the factory's multi-repository switch, read here with the factory's own type
	// and rules (v4 MR16): coordination_plan is listed only while it is not off.
	Coordination fconfig.Coordination `json:"coordination,omitzero"`
	// PolicyManagement is the factory's merge-policy switches, read here with the factory's own type
	// (merge policy stage 6): promotion serves the settings pages and the promotion records, and
	// operators approve exceptions. The broker reads nothing else of it.
	PolicyManagement fconfig.PolicyManagement `json:"policyManagement,omitzero"`
	// Install is the install's own state, which GET /api/install shows every signed-in human.
	Install Install `json:"install,omitzero"`
}

// The codes of Install.Unsafe: the chart's unsafe settings (v4 D18, plan PR9), a bounded set.
const (
	UnsafeAllowRunc         = "unsafe_allow_runc"   // runs have no sandbox RuntimeClass
	UnsafeNetworkPolicyNone = "network_policy_none" // nothing confines a run's network
	UnsafeSharedGitToken    = "shared_git_token"    // every role shares one write token

	// UnsafePrivateCodeUnacknowledged is the factory's code, never the chart's: a served repository
	// is private code without spec.privateCode.acknowledged (v4 D3). The broker reads it from the
	// factory's GET /v1/install; install.unsafe refuses it.
	UnsafePrivateCodeUnacknowledged = "private_code_unacknowledged"
)

// FactoryInstallCodes are the codes the broker takes from the factory's GET /v1/install.
var FactoryInstallCodes = []string{UnsafePrivateCodeUnacknowledged}

// Install is what the chart says about the install. Unsafe lists the settings that weaken a run's
// isolation; the web UI shows a banner on every page while it is non-empty.
type Install struct {
	Unsafe []string `json:"unsafe,omitempty"`
}

func (i Install) validate() []error {
	var errs []error
	known := []string{UnsafeAllowRunc, UnsafeNetworkPolicyNone, UnsafeSharedGitToken}
	for n, code := range i.Unsafe {
		switch {
		case !slices.Contains(known, code):
			errs = append(errs, fmt.Errorf("install.unsafe[%d]: %q is not one of %s", n, code, strings.Join(known, ", ")))
		case slices.Index(i.Unsafe, code) != n:
			errs = append(errs, fmt.Errorf("install.unsafe[%d]: %q is listed twice", n, code))
		}
	}
	return errs
}

// WebhookConfig is one subscriber (factory interfaces F6, O7): declared here because subscribers
// are few, this file is a gate path, and ai-native-ref generates the broker's egress from it.
type WebhookConfig struct {
	Name         string   `json:"name"`
	Network      string   `json:"network"` // cluster | internet
	URL          string   `json:"url"`
	Events       []string `json:"events"`
	SecretFile   string   `json:"secretFile"`
	Repositories []string `json:"repositories"`
	// Generation re-enables a subscriber the emitter disabled: changing it is the reviewed act (R17).
	Generation int `json:"generation,omitempty"`
}

var (
	dnsLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	clusterURL = regexp.MustCompile(`^https?://([a-z0-9-]+)\.([a-z0-9-]+)\.svc\.cluster\.local:[0-9]{1,5}/`)
	// privateSuffixes are names that only resolve inside a cluster or a private network.
	privateSuffixes = []string{".local", ".internal", ".svc", ".cluster.local", ".localdomain"}
)

func (w WebhookConfig) validate(i int, seen map[string]bool) []error {
	at := fmt.Sprintf("webhooks[%d] %s", i, w.Name)
	var errs []error
	if !dnsLabel.MatchString(w.Name) {
		errs = append(errs, fmt.Errorf("%s: name must be a DNS label", at))
	} else if seen[w.Name] {
		errs = append(errs, fmt.Errorf("%s: duplicate name", at))
	}
	seen[w.Name] = true
	switch w.Network {
	case "cluster":
		if !clusterURL.MatchString(w.URL) {
			errs = append(errs, fmt.Errorf("%s: a cluster subscriber's url is http(s)://<svc>.<ns>.svc.cluster.local:<port>/…", at))
		}
	case "internet":
		errs = append(errs, w.validateInternet(at)...)
	default:
		errs = append(errs, fmt.Errorf("%s: network is cluster or internet", at))
	}
	if len(w.Events) == 0 {
		errs = append(errs, fmt.Errorf("%s: events is empty", at))
	}
	for _, e := range w.Events {
		if !slices.Contains(webhook.Types(), e) {
			errs = append(errs, fmt.Errorf("%s: unknown event %q", at, e))
		}
	}
	if w.SecretFile == "" {
		errs = append(errs, fmt.Errorf("%s: secretFile is required", at))
	}
	if len(w.Repositories) == 0 {
		errs = append(errs, fmt.Errorf("%s: repositories is empty", at))
	}
	return errs
}

func (w WebhookConfig) validateInternet(at string) []error {
	u, err := url.Parse(w.URL)
	if err != nil || u.Host == "" {
		return []error{fmt.Errorf("%s: url does not parse", at)}
	}
	if u.Scheme != "https" {
		return []error{fmt.Errorf("%s: an internet subscriber is https", at)}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if net.ParseIP(host) != nil {
		return []error{fmt.Errorf("%s: an internet subscriber's host is a DNS name, never an address", at)}
	}
	if host == "localhost" || !strings.Contains(host, ".") || slices.ContainsFunc(privateSuffixes, func(s string) bool { return strings.HasSuffix(host, s) }) {
		return []error{fmt.Errorf("%s: an internet subscriber's host is not a cluster or private name", at)}
	}
	return nil
}

// SuspendConfig is the broker's suspend trigger (v2 warm-sandboxes design §6): a suspendable
// run in an attended room whose approval waits while the room stays idle for After is
// suspended, and resumed once nothing waits. Off unless Enabled.
type SuspendConfig struct {
	Enabled bool     `json:"enabled,omitempty"`
	After   Duration `json:"after,omitzero"`
}

// The bounds of suspend.after: a suspend and its resume cost a cold start, so not at once,
// and the attended approval expires at 30 minutes, so before that.
const (
	DefaultSuspendAfter = 5 * time.Minute
	minSuspendAfter     = time.Minute
	maxSuspendAfter     = 25 * time.Minute
)

// Load reads, strictly decodes, defaults and validates the file at path.
func Load(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// localGroups are the fixed group names of local mode.
var localGroups = GroupsConfig{Admin: "admin", Member: "member"}

// ApplyDefaults fills what the file may leave out.
func (c *Config) ApplyDefaults() {
	// The mode a deployed broker already serves, spelled out so every reader can compare modes
	// without special-casing the empty string.
	for i := range c.RunIssuers {
		if c.RunIssuers[i].Mode == "" {
			c.RunIssuers[i].Mode = IssuerModeJWKS
		}
	}
	// An all-empty block is the absent one a file may spell out: it was the zero value before
	// systemIssuer became optional, so it must not start failing a deployed broker's rollout.
	if c.SystemIssuer != nil && *c.SystemIssuer == (IssuerConfig{}) {
		c.SystemIssuer = nil
	}
	if c.SystemIssuer != nil && c.SystemIssuer.Mode == "" {
		c.SystemIssuer.Mode = IssuerModeJWKS
	}
	if c.Human.Mode == "" {
		c.Human.Mode = HumanModeOIDC
	}
	if c.TLS.CertFile == "" {
		c.TLS.CertFile = DefaultCertFile
	}
	if c.TLS.KeyFile == "" {
		c.TLS.KeyFile = DefaultKeyFile
	}
	c.Runs = c.Runs.WithDefaults()
	if c.RoomTools != nil && c.RoomTools.Knowledge != nil {
		c.RoomTools.Knowledge.applyDefaults()
	}
	c.Coordination.ApplyDefaults()
	if c.Suspend.After.Duration == 0 {
		c.Suspend.After.Duration = DefaultSuspendAfter
	}
	if a := c.Human.Access; a != nil && a.TTL.Duration == 0 {
		a.TTL.Duration = MaxAccessTTL
	}
	if c.Human.EffectiveMode() == HumanModeLocal {
		// Validate refuses any other value: the Origin check is the only gate, so it accepts
		// authn.LoopbackOrigins(publicURL) (publicURL's port under localhost, 127.0.0.1 and [::1]) and nothing else. A bare `human: {mode: local}` is the port-forward on the
		// pod's :8080, which is what kubectl forwards.
		if c.PublicURL == "" {
			c.PublicURL = "http://localhost:8080"
		}
		if c.Human.Groups == (GroupsConfig{}) {
			c.Human.Groups = localGroups
		}
		if c.Human.Origin == "" {
			c.Human.Origin = c.PublicURL
		}
	}
}

// Validate reports every problem at once. The run issuers' patterns compile here,
// so the broker's own regexp.MustCompile cannot panic.
func (c *Config) Validate() error {
	var errs []error
	if len(c.RunIssuers) == 0 {
		errs = append(errs, errors.New("runIssuers: at least one is required"))
	}
	for i, is := range c.RunIssuers {
		errs = append(errs, issuer(fmt.Sprintf("runIssuers[%d]", i), is, true)...)
		re, err := regexp.Compile(is.SubPattern)
		if err != nil || re.NumSubexp() != 1 {
			errs = append(errs, fmt.Errorf("runIssuers[%d] %s: subPattern must compile with one capture group (the runId)", i, is.Issuer))
		}
	}
	errs = append(errs, c.roomToolsAuth()...)
	if c.RoomTools != nil && c.RoomTools.Knowledge != nil {
		errs = append(errs, c.RoomTools.Knowledge.validate()...)
	}
	for i, r := range c.PushRepositories() {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") || strings.TrimSpace(r) != r {
			errs = append(errs, fmt.Errorf("roomTools.pushRepositories[%d] %q is not owner/name", i, r))
		}
	}
	if err := c.Runs.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("runs: %w", err))
	} else {
		errs = append(errs, c.runIssuerMatchesKind()...)
	}
	// Only the factory calls with a system token: a broker without one has no systemIssuer.
	if c.SystemIssuer != nil {
		errs = append(errs, issuer("systemIssuer", *c.SystemIssuer, false)...)
	} else if len(c.SystemPrincipals) > 0 {
		errs = append(errs, errors.New("systemPrincipals: needs systemIssuer: without it no system caller can be vouched for"))
	}
	for sub, id := range c.SystemPrincipals {
		if !strings.HasPrefix(id, "system:") || id == "system:" {
			errs = append(errs, fmt.Errorf("systemPrincipals[%s]: %q must be system:<component>", sub, id))
		}
	}
	errs = append(errs, c.Human.validate(slices.Collect(maps.Values(c.SystemPrincipals)))...)
	if len(c.Human.MCP.Sponsored) > 0 && c.FactoryURL == "" {
		errs = append(errs, errors.New("human.mcp.sponsored needs factoryURL: run_create posts to the factory's POST /v1/tasks"))
	}
	if c.ForgeReadTokenFile() != "" && c.FactoryURL == "" {
		errs = append(errs, errors.New("roomTools.forgeReadTokenFile needs factoryURL: forge_read posts to the factory's forge-read route"))
	}
	errs = append(errs, checkFactoryURL(c.FactoryURL, c.FactoryInsecure)...)
	errs = append(errs, c.Install.validate()...)
	if c.Human.EffectiveMode() == HumanModeLocal {
		// The one place publicURL may be http: a port-forward carries no TLS. It is exactly what a
		// browser sends as Origin, which the Origin check compares verbatim.
		if c.Human.Origin != c.PublicURL {
			errs = append(errs, errors.New("human.origin: mode local takes no origin: it is publicURL"))
		}
		if c.Human.Groups != localGroups {
			errs = append(errs, errors.New("human.groups: mode local fixes the groups to admin and member"))
		}
		errs = append(errs, loopbackOrigin(c.PublicURL)...)
	}
	if a := c.Suspend.After.Duration; a < minSuspendAfter || a > maxSuspendAfter {
		errs = append(errs, fmt.Errorf("suspend.after: %s must be within [%s, %s]", a, minSuspendAfter, maxSuspendAfter))
	}
	seen := map[string]bool{}
	for i, w := range c.Webhooks {
		errs = append(errs, w.validate(i, seen)...)
	}
	c.Coordination.Validate(func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) })
	if c.Coordination.Mode != fconfig.CoordinationOff && !c.Human.Intake.Plan.Enabled {
		// coordination_plan plans through the governed intake route: a switch on with no route is a
		// tool that would refuse every request.
		errs = append(errs, fmt.Errorf("coordination.mode %s needs human.intake.plan.enabled: coordination_plan plans through its route", c.Coordination.Mode))
	}
	c.PolicyManagement.Validate(func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) })
	if c.PolicyManagement.Promotion && c.FactoryURL == "" {
		errs = append(errs, errors.New("policyManagement.promotion needs factoryURL: the evidence is the factory's GET /v1/policy/evidence"))
	}
	return errors.Join(errs...)
}

// roomToolsAuth checks roomTools.auth. tokenreview needs a run issuer in mode tokenreview: :8090 asks
// the apiserver through it and maps the subject with its subPattern, so a broker whose run issuers are
// all JWKS has nothing that could vouch for a room tools caller that way.
func (c *Config) roomToolsAuth() []error {
	switch c.RoomToolsAuth() {
	case RoomToolsAuthKey:
		return nil
	case RoomToolsAuthTokenReview:
		for _, is := range c.RunIssuers {
			if is.EffectiveMode() == IssuerModeTokenReview {
				return nil
			}
		}
		return []error{errors.New("roomTools.auth: tokenreview needs a run issuer in mode tokenreview")}
	}
	return []error{fmt.Errorf("roomTools.auth: %q is not key or tokenreview", c.RoomTools.Auth)}
}

// checkFactoryURL is D6: the broker forwards each human's bearer token to the factory, so its URL
// is https, or http under an explicit factoryInsecure, as the bridge refuses a non-https
// BROKER_URL. Unset is fine: start_run then renders a manifest.
func checkFactoryURL(raw string, insecure bool) []error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.User != nil:
		return []error{errors.New("factoryURL: must be an absolute https URL with no userinfo")}
	case u.Scheme == "https", u.Scheme == "http" && insecure:
		return nil
	case u.Scheme == "http":
		return []error{errors.New("factoryURL: http:// sends human tokens in the clear: serve the factory's api.tls and use https, or set factoryInsecure: true")}
	}
	return []error{fmt.Errorf("factoryURL: scheme %q is not https", u.Scheme)}
}

// runIssuerMatchesKind refuses a runs kind no runIssuers pattern recognises: its bridge tokens
// would be denied at :8443 and the room would stay silent, with only authn denials as a trace.
func (c *Config) runIssuerMatchesKind() []error {
	const id = "aaaaaaaa"
	sub := "system:serviceaccount:" + c.Runs.Namespace + ":" + c.Runs.NamePrefix + id
	for _, is := range c.RunIssuers {
		re, err := regexp.Compile(is.SubPattern)
		if err != nil || re.NumSubexp() != 1 {
			continue // reported above
		}
		if m := re.FindStringSubmatch(sub); m != nil && m[1] == id {
			return nil
		}
	}
	return []error{fmt.Errorf("runs / runIssuers: no runIssuers[].subPattern matches %q (namespace %q, namePrefix %q) and captures the run id", sub, c.Runs.Namespace, c.Runs.NamePrefix)}
}

func (h HumanConfig) validate(system []string) []error {
	// human.intake.plan is checked in every mode, local included: the class table and the route are
	// a config file's, and a typo in one must fail the rollout, not the first plan.
	errs := h.Intake.validate()
	if h.EffectiveMode() == HumanModeLocal {
		if h.Intake.Plan.Enabled {
			errs = append(errs, errors.New("human.intake.plan.enabled: mode local serves no /mcp, and the planner is a tool on it"))
		}
		// One field per line, each named: a typo in a local block fails the rollout with the field
		// that does not belong there, not a sentence listing four.
		for _, f := range []struct {
			name    string
			refused bool
			why     string
		}{
			{"human.issuer", h.Issuer != "", "the loopback user is not an IdP principal"},
			{"human.jwksURL", h.JWKSURL != "", "there is no token to verify"},
			{"human.clientIDFile", h.ClientIDFile != "", "there is no OAuth client"},
			{"human.roomctlClientIDFile", h.RoomctlClientIDFile != "", "there is no maku client"},
			{"human.projectIDFile", h.ProjectIDFile != "", "no token carries a project audience"},
			{"human.preset", h.Preset != "", "there is no IdP to shape a token"},
			{"human.tokenUse", h.TokenUse != "", "there is no token to present"},
			{"human.audience", h.Audience != "", "no token carries an audience"},
			{"human.audienceFile", h.AudienceFile != "", "no token carries an audience"},
			{"human.rolesClaim", h.RolesClaim != "", "the local user's groups are fixed"},
			{"human.scopes", len(h.Scopes) > 0, "there is no IdP to ask"},
			{"human.access", h.Access != nil, "the local user is an admin, and D7 gates members only"},
			{"human.slackLink", h.SlackLink != nil, "a link code names a sponsored principal and a GitHub link, neither of which mode local has"},
			{"human.slackDecide", h.SlackDecide != nil, "a decision names a sponsored principal and a GitHub link, neither of which mode local has"},
			{"human.slackIntents", h.SlackIntents != nil, "a classification names a sponsored principal and a Slack link, neither of which mode local has"},
		} {
			if f.refused {
				errs = append(errs, fmt.Errorf("%s: mode local takes no %s: %s", f.name, strings.TrimPrefix(f.name, "human."), f.why))
			}
		}
		// The MCP server is the OIDC viewers': /mcp takes maku's tokens, and its sponsored callers
		// and sessionStart's GitHub check name principals and repositories local mode does not have.
		if h.MCP.Enabled {
			errs = append(errs, errors.New("human.mcp.enabled: mode local serves no /mcp: the loopback user has no token to present"))
		}
		if len(h.MCP.Sponsored) > 0 {
			errs = append(errs, errors.New("human.mcp.sponsored: mode local takes no sponsored principal"))
		}
		if len(h.MCP.SessionStart) > 0 {
			errs = append(errs, errors.New("human.mcp.sessionStart: mode local takes no sessionStart repository"))
		}
		if h.MCP.SponsoredClientClaim != "" {
			errs = append(errs, errors.New("human.mcp.sponsoredClientClaim: mode local takes no sponsored principal"))
		}
		// Dashboards are optional in either mode and checked the same way.
		return append(errs, validateDashboards(h.Dashboards)...)
	}
	if h.EffectiveMode() != HumanModeOIDC {
		return []error{fmt.Errorf("human: mode %q is not oidc or local", h.Mode)}
	}
	// Without jwksURL, an https issuer's discovery document names jwks_uri, read at the first token.
	if h.JWKSURL != "" || !isHTTPS(h.Issuer) {
		errs = append(errs, issuer("human", IssuerConfig{Issuer: h.Issuer, JWKSURL: h.JWKSURL}, false)...)
	}
	if h.ClientIDFile == "" {
		errs = append(errs, errors.New("human.clientIDFile: is required"))
	}
	errs = append(errs, h.validatePreset()...)
	// Compared verbatim with the Origin header, which is scheme://host[:port].
	if u, err := url.Parse(h.Origin); err != nil || u.Scheme != "https" || u.Host == "" || u.Scheme+"://"+u.Host != h.Origin {
		errs = append(errs, errors.New("human.origin: must be https://host[:port], as a browser sends it"))
	}
	if h.Groups.Admin == "" {
		errs = append(errs, errors.New("human.groups.admin: is required"))
	}
	if h.Groups.Member == "" {
		errs = append(errs, errors.New("human.groups.member: is required"))
	}
	if h.Groups.Admin != "" && h.Groups.Admin == h.Groups.Member {
		errs = append(errs, errors.New("human.groups: admin and member must differ"))
	}
	if h.MCP.Enabled && h.RoomctlClientIDFile == "" {
		errs = append(errs, errors.New("human.mcp.enabled: needs human.roomctlClientIDFile, since /mcp takes maku's tokens only"))
	}
	// The planner's two tools exist on /mcp alone, and its candidate set is the write check
	// human.access carries: enabled without either is a switch that reads as on and answers every
	// call with a refusal, so it is refused by the part it is missing.
	if h.Intake.Plan.Enabled && !h.MCP.Enabled {
		errs = append(errs, errors.New("human.intake.plan.enabled needs human.mcp.enabled: both planner tools are on /mcp"))
	}
	if h.Intake.Plan.Enabled && h.Access == nil {
		errs = append(errs, errors.New("human.intake.plan.enabled needs human.access: without the GitHub write check no repository is a candidate"))
	}
	if len(h.MCP.Sponsored) > 0 && !h.MCP.Enabled {
		errs = append(errs, errors.New("human.mcp.sponsored needs human.mcp.enabled"))
	}
	seen := map[string]bool{}
	for _, sp := range h.MCP.Sponsored {
		switch {
		case !sponsoredNameRE.MatchString(sp.Name):
			errs = append(errs, fmt.Errorf("human.mcp.sponsored: %q is not a DNS label", sp.Name))
		case sponsored.IsReserved(sp.Name) || slices.Contains(system, "system:"+sp.Name):
			errs = append(errs, fmt.Errorf("human.mcp.sponsored: %q is reserved", sp.Name))
		case seen[sp.Name]:
			errs = append(errs, fmt.Errorf("human.mcp.sponsored: %q is declared twice", sp.Name))
		case sp.ClientIDFile == "":
			errs = append(errs, fmt.Errorf("human.mcp.sponsored.%s: clientIDFile is required", sp.Name))
		}
		repos := map[string]bool{}
		for _, r := range sp.Repositories {
			switch {
			case !repositoryRE.MatchString(r):
				errs = append(errs, fmt.Errorf("human.mcp.sponsored.%s.repositories: %q is not owner/name", sp.Name, r))
			case repos[r]:
				errs = append(errs, fmt.Errorf("human.mcp.sponsored.%s.repositories: %q is listed twice", sp.Name, r))
			}
			repos[r] = true
		}
		seen[sp.Name] = true
	}
	if len(h.MCP.SessionStart) > 0 && !h.MCP.Enabled {
		errs = append(errs, errors.New("human.mcp.sessionStart needs human.mcp.enabled"))
	}
	if len(h.MCP.SessionStart) > 0 && h.Access == nil {
		errs = append(errs, errors.New("human.mcp.sessionStart needs human.access: without the GitHub check canStart is always off"))
	}
	listed := map[string]bool{}
	for i, r := range h.MCP.SessionStart {
		key := strings.ToLower(r)
		switch {
		case !repositoryRE.MatchString(r):
			errs = append(errs, fmt.Errorf("human.mcp.sessionStart[%d]: %q is not owner/name", i, r))
		case listed[key]:
			errs = append(errs, fmt.Errorf("human.mcp.sessionStart: %q is declared twice", r))
		}
		listed[key] = true
	}
	errs = append(errs, validateDashboards(h.Dashboards)...)
	if l := h.SlackLink; l != nil {
		if !slices.ContainsFunc(h.MCP.Sponsored, func(sp SponsoredConfig) bool { return sp.Name == l.Principal }) {
			errs = append(errs, fmt.Errorf("human.slackLink.principal: %q is not one of human.mcp.sponsored", l.Principal))
		}
		if !slackTeamRE.MatchString(l.TeamID) {
			errs = append(errs, fmt.Errorf("human.slackLink.teamID: %q is not a Slack workspace id", l.TeamID))
		}
		if !repositoryRE.MatchString(l.Repository) {
			errs = append(errs, fmt.Errorf("human.slackLink.repository: %q is not owner/name", l.Repository))
		}
		if h.Access == nil {
			errs = append(errs, errors.New("human.slackLink needs human.access: a link is to the account's GitHub link"))
		}
	}
	// D14: the decide switch names what the route resolves a click through — a declared sponsored
	// principal and the GitHub check that turns the linked human into a login — and nothing else:
	// the one class it decides is a constant (D13). The workspace is the App's, as slackLink's.
	if d := h.SlackDecide; d != nil {
		if !slices.ContainsFunc(h.MCP.Sponsored, func(sp SponsoredConfig) bool { return sp.Name == d.Principal }) {
			errs = append(errs, fmt.Errorf("human.slackDecide.principal: %q is not one of human.mcp.sponsored", d.Principal))
		}
		if !slackTeamRE.MatchString(d.TeamID) {
			errs = append(errs, fmt.Errorf("human.slackDecide.teamID: %q is not a Slack workspace id", d.TeamID))
		}
		if h.Access == nil {
			errs = append(errs, errors.New("human.slackDecide needs human.access: a decision is the linked human's room standing and repository write"))
		}
	}
	if i := h.SlackIntents; i != nil {
		errs = append(errs, i.validate(h.MCP.Sponsored)...)
	}
	if a := h.Access; a != nil {
		if a.ReaderFile == "" {
			errs = append(errs, errors.New("human.access.readerFile: is required"))
		}
		if a.TTL.Duration <= 0 || a.TTL.Duration > MaxAccessTTL {
			errs = append(errs, fmt.Errorf("human.access.ttl: must be above 0 and at most %s (D7)", MaxAccessTTL))
		}
		// The reader's PAT is sent to the issuer: never in clear, never with userinfo.
		if u, err := url.Parse(h.Issuer); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			errs = append(errs, errors.New("human.issuer: must be https://host when human.access is set"))
		}
	}
	return errs
}

// loopbackOrigin checks the one plain-HTTP URL a broker serves: the listener a port-forward reaches.
// It is canonical (the browser sends exactly this as Origin), carries an explicit port, and names
// this machine — localhost, or any address the standard library calls loopback.
func loopbackOrigin(publicURL string) []error {
	u, err := url.Parse(publicURL)
	if err == nil && u.Scheme == "http" && u.Scheme+"://"+u.Host == publicURL && u.Port() != "" && u.Hostname() != "" {
		if host := strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")); host == "localhost" {
			return nil
		} else if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return []error{fmt.Errorf("publicURL %q: mode local serves only a port-forward, so publicURL is exactly http://localhost:<port> (or a loopback address)", publicURL)}
}

// issuer validates one issuer's mode and the fields that mode uses. run is true for a run issuer,
// the only kind that maps a subject to a run.
func issuer(field string, is IssuerConfig, run bool) []error {
	switch is.EffectiveMode() {
	case IssuerModeTokenReview:
		var errs []error
		if is.Issuer != "" || is.JWKSURL != "" {
			errs = append(errs, fmt.Errorf("%s: mode tokenreview takes no issuer or jwksURL: the apiserver is the issuer", field))
		}
		if !run && is.SubPattern != "" {
			errs = append(errs, fmt.Errorf("%s: mode tokenreview takes no subPattern: only a run issuer maps a subject to a run", field))
		}
		return errs
	case IssuerModeJWKS:
		var errs []error
		if is.Issuer == "" {
			errs = append(errs, fmt.Errorf("%s: issuer is required", field))
		}
		if u, err := url.Parse(is.JWKSURL); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: jwksURL must be an absolute https:// URL", field))
		}
		return errs
	}
	return []error{fmt.Errorf("%s: mode %q is not jwks or tokenreview", field, is.Mode)}
}

func isHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// validatePreset checks the keys each preset reads, naming both keys where an old and a new one
// conflict (D25).
func (h HumanConfig) validatePreset() []error {
	var errs []error
	if !authn.ValidRolesClaim(h.RolesClaim) {
		errs = append(errs, errors.New("human.rolesClaim: not a claim name or an RFC 6901 pointer (~0 and ~1 are its only escapes)"))
	}
	for _, sc := range h.Scopes {
		if sc == "" || strings.ContainsAny(sc, " \t\n") {
			errs = append(errs, errors.New("human.scopes: an empty or spaced scope"))
			break
		}
	}
	if cc := h.MCP.SponsoredClientClaim; cc != "" && !authn.ValidClientClaim(cc) {
		errs = append(errs, fmt.Errorf("human.mcp.sponsoredClientClaim: %q is not cid, azp, client_id or appid", cc))
	}
	switch h.EffectivePreset() {
	case PresetZITADEL:
		if h.MCP.SponsoredClientClaim != "" {
			errs = append(errs, errors.New("human.mcp.sponsoredClientClaim: the zitadel preset names the client in azp or client_id"))
		}
		if h.ProjectIDFile == "" {
			errs = append(errs, errors.New("human.projectIDFile: is required"))
		}
		if h.Audience != "" {
			errs = append(errs, errors.New("human.audience: the zitadel preset's audience is human.projectIDFile"))
		}
		if h.AudienceFile != "" {
			errs = append(errs, errors.New("human.audienceFile: the zitadel preset's audience is human.projectIDFile"))
		}
		if h.TokenUse != "" && h.TokenUse != TokenUseAccess {
			errs = append(errs, errors.New("human.tokenUse: the zitadel preset uses access"))
		}
	case PresetGeneric:
		if h.RolesClaim == "" {
			errs = append(errs, errors.New("human.rolesClaim: is required with preset generic (the claim holding groups.admin and groups.member)"))
		}
		if h.ProjectIDFile != "" {
			errs = append(errs, errors.New("human.projectIDFile: the zitadel preset's; use human.audienceFile with tokenUse access"))
		}
		if h.Audience != "" && h.AudienceFile != "" {
			errs = append(errs, errors.New("human.audience, human.audienceFile: set one, not both"))
		}
		switch h.EffectiveTokenUse() {
		case TokenUseAccess:
			if h.Audience == "" && h.AudienceFile == "" {
				errs = append(errs, errors.New("human.audience, human.audienceFile: tokenUse access needs exactly one (an aud value every human token must hold)"))
			}
		case TokenUseID:
		default:
			errs = append(errs, fmt.Errorf("human.tokenUse: %q is not id or access", h.TokenUse))
		}
		if len(h.MCP.Sponsored) > 0 && h.Audience == "" && h.AudienceFile == "" {
			errs = append(errs, errors.New("human.mcp.sponsored needs human.audience or human.audienceFile with preset generic: a sponsored token's aud must hold it"))
		}
	default:
		errs = append(errs, fmt.Errorf("human.preset: %q is not zitadel or generic", h.Preset))
	}
	return errs
}
