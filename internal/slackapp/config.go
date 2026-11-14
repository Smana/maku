// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/webhook"
)

// minMaxCost is the factory's floor: it refuses a max_cost under one cent.
const minMaxCost = 0.01

// Config is the App's config file, loaded strictly as the broker's config is.
type Config struct {
	TeamID            string `json:"teamID"`
	BotUserIDFile     string `json:"botUserIDFile"`
	BotTokenFile      string `json:"botTokenFile"`
	SigningSecretFile string `json:"signingSecretFile"`
	WebhookKeyFile    string `json:"webhookKeyFile"`
	BrokerURL         string `json:"brokerURL"`
	RoomsURL          string `json:"roomsURL"`
	IssuerURL         string `json:"issuerURL"`
	ClientIDFile      string `json:"clientIDFile"`
	ClientSecretFile  string `json:"clientSecretFile"`
	ProjectIDFile     string `json:"projectIDFile"`
	// Preset is "" or "zitadel" (today's token request: ZITADEL's fixed path, the project's
	// audience scope, ProjectIDFile required), or "generic": any OIDC issuer (v4 D8).
	Preset string `json:"preset,omitempty"`
	// TokenURL is the token endpoint under generic; empty, the issuer's discovery document names it.
	TokenURL string `json:"tokenURL,omitempty"`
	// Scopes and Audience are the generic client-credentials request's: the audience is sent as a
	// parameter (Auth0), and is the value the broker's and the factory's audience pin.
	Scopes    []string           `json:"scopes,omitempty"`
	Audience  string             `json:"audience,omitempty"`
	Namespace string             `json:"namespace"`
	Channels  map[string]Channel `json:"channels"`
	// Proposals is the proposal message's switch (CD7): off, a mention drafts as it does today.
	Proposals Proposals `json:"proposals,omitempty"`
	// The three listeners (M11): Slack's events (public), the broker's webhooks (cluster only),
	// and metrics with the probes. Defaults :8080, :8081 and :9090; a test passes 127.0.0.1:0.
	// APIBaseURL is Slack's Web API base, a test seam (the e2e stub): empty is https://slack.com/api/.
	APIBaseURL   string `json:"apiBaseURL,omitempty"`
	EventsAddr   string `json:"eventsAddr,omitempty"`
	WebhooksAddr string `json:"webhooksAddr,omitempty"`
	OpsAddr      string `json:"opsAddr,omitempty"`
}

// Proposals is the proposal message's switch (CD7, CD20): on, a mention is planned by the broker and
// posted as one proposal edited in place; off, a mention drafts exactly as it does today. Absent is
// off, so a deployment that says nothing gets the behaviour it has.
type Proposals struct {
	Enabled bool `json:"enabled,omitempty"`
}

// Channel is a channel the App serves: the repositories a draft there may name, the max_cost a ✅
// there sends, whether its forge.push approvals are drawn as a card, and which webhook transitions
// post a line there (CD11, CD24 — declared once in ai-native-ref's slack-bindings.yaml).
type Channel struct {
	Repositories []string `json:"repositories"`
	MaxCostUSD   float64  `json:"maxCostUSD"`
	// Approvals draws this channel's forge.push approvals as a card with two buttons (v3.x Slack push
	// approval D14). Off, the thread keeps today's line and a press answers not_enabled.
	Approvals bool `json:"approvals,omitempty"`
	// Intents is the second reading of a filed thread's replies (natural-language intents, SI20):
	// off (the default), shadow or on. Shadow classifies a reply tier 0 does not know, counts and logs
	// it, and posts today's unknown line; until the read intents ship, on behaves as shadow.
	Intents string `json:"intents,omitempty"`
	// CollectReplies is the channel's agreement that its replies are stored with their label for
	// retentionDays (SI19). Without it nothing a person typed here is kept. It needs intents.
	CollectReplies bool `json:"collectReplies,omitempty"`
	// IntentActions is the App's per-action switch for the write intents (SI21): any of stop, retry,
	// steer, amend, each once. None is served yet; the list is validated and ignored.
	IntentActions []string `json:"intentActions,omitempty"`
	// Notify is which webhook transitions post a line in this channel (CD11). Absent means
	// webhook.Notify() — the nine a person is told about; an explicit empty list means none, which
	// is a choice, not an omission.
	Notify []string `json:"notify,omitempty"`
}

// The values of a channel's intents switch (SI20).
const (
	IntentsOff    = "off"
	IntentsShadow = "shadow"
	IntentsOn     = "on"
)

// intentsServed reports whether tier 1 reads this channel's unknown replies: shadow or on.
func (c Channel) intentsServed() bool { return c.Intents == IntentsShadow || c.Intents == IntentsOn }

// intentActionNames are the write intents a channel may list (SI21).
var intentActionNames = []string{"stop", "retry", "steer", "amend"}

// notifies reports whether t posts a line in this channel. nil and empty are different answers: nil
// is a deployment that said nothing (the nine webhook.Notify() names), empty is a channel where
// nothing is news. Validation reads the decoded slice, so a declared list is checked, never the
// default.
func (c Channel) notifies(t string) bool {
	if c.Notify != nil {
		return slices.Contains(c.Notify, t)
	}
	return slices.Contains(webhook.Notify(), t)
}

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
	for addr, def := range map[*string]string{&c.EventsAddr: ":8080", &c.WebhooksAddr: ":8081", &c.OpsAddr: ":9090"} {
		if *addr == "" {
			*addr = def
		}
	}
	// YAML 1.1 reads a bare off or on as a boolean, which arrives here as "false" or "true": the
	// switch means what the operator wrote, so it is read back as off or on before it is checked.
	for id, ch := range c.Channels {
		switch ch.Intents {
		case "false":
			ch.Intents = IntentsOff
		case "true":
			ch.Intents = IntentsOn
		}
		c.Channels[id] = ch
	}
	if err := c.validate(); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

func (c Config) validate() error {
	var errs []error
	for name, v := range map[string]string{"botUserIDFile": c.BotUserIDFile, "botTokenFile": c.BotTokenFile,
		"signingSecretFile": c.SigningSecretFile, "webhookKeyFile": c.WebhookKeyFile, "namespace": c.Namespace, "brokerURL": c.BrokerURL, "roomsURL": c.RoomsURL} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	for name, v := range map[string]string{"brokerURL": c.BrokerURL, "roomsURL": c.RoomsURL, "apiBaseURL": c.APIBaseURL} {
		if u, err := url.Parse(v); v != "" && (err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Hostname() == "") {
			errs = append(errs, fmt.Errorf("%s %q must be an http(s) URL", name, v))
		}
	}
	errs = append(errs, c.validatePreset()...)
	if !teamRE.MatchString(c.TeamID) {
		errs = append(errs, fmt.Errorf("teamID %q is not a Slack workspace id", c.TeamID))
	}
	if len(c.Channels) == 0 {
		errs = append(errs, errors.New("channels: at least one channel is required"))
	}
	ids := make([]string, 0, len(c.Channels))
	for id := range c.Channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ch := c.Channels[id]
		if !channelRE.MatchString(id) {
			errs = append(errs, fmt.Errorf("channels: %q is not a Slack channel id", id))
		}
		if len(ch.Repositories) == 0 {
			errs = append(errs, fmt.Errorf("channels.%s: repositories is empty", id))
		}
		for _, r := range ch.Repositories {
			if !repoRE.MatchString(r) {
				errs = append(errs, fmt.Errorf("channels.%s: repository %q is not owner/name", id, r))
			}
		}
		if ch.MaxCostUSD < minMaxCost {
			errs = append(errs, fmt.Errorf("channels.%s: maxCostUSD %v is below %v", id, ch.MaxCostUSD, minMaxCost))
		}
		switch ch.Intents {
		case "", IntentsOff, IntentsShadow, IntentsOn:
		default:
			errs = append(errs, fmt.Errorf("channels.%s: intents %q is not off, shadow or on", id, ch.Intents))
		}
		if ch.CollectReplies && !ch.intentsServed() {
			errs = append(errs, fmt.Errorf("channels.%s: collectReplies needs intents shadow or on: nothing is classified, so nothing is collected", id))
		}
		listedActions := make(map[string]struct{}, len(ch.IntentActions))
		for _, act := range ch.IntentActions {
			if !slices.Contains(intentActionNames, act) {
				errs = append(errs, fmt.Errorf("channels.%s: intentActions %q is not one of %s", id, act, strings.Join(intentActionNames, ", ")))
				continue
			}
			if _, twice := listedActions[act]; twice {
				errs = append(errs, fmt.Errorf("channels.%s: intentActions lists %q more than once", id, act))
			}
			listedActions[act] = struct{}{}
		}
		// CD11: a notify list is a closed vocabulary, and a name given twice is a generated
		// declaration that disagrees with itself: both are refused here, not at the renderer.
		notified := make(map[string]struct{}, len(ch.Notify))
		for _, typ := range ch.Notify {
			if !slices.Contains(webhook.Types(), typ) {
				errs = append(errs, fmt.Errorf("channels.%s: notify %q is not a webhook event", id, typ))
				continue
			}
			// A type the App draws no line for (room.task_received, room.run_ended,
			// room.monitor_verdict) would be accepted and never post: line is the one table of what
			// renders, so it is asked rather than copied here.
			if _, renders := (&App{}).line(webhook.Event{Type: typ}); !renders {
				errs = append(errs, fmt.Errorf("channels.%s: notify %q has no line the App posts", id, typ))
				continue
			}
			if _, twice := notified[typ]; twice {
				errs = append(errs, fmt.Errorf("channels.%s: notify lists %q more than once", id, typ))
			}
			notified[typ] = struct{}{}
		}
	}
	return errors.Join(errs...)
}

// canonicalRepo is the channel's own spelling of repo and whether the channel serves it. GitHub's
// owner and repository names are case-insensitive and the planner lowercases the one it names, so a
// plan's repository is matched with EqualFold; what is filed, drawn and marked is the spelling the
// operator wrote (the factory's run tokens take only that one).
func canonicalRepo(listed []string, repo string) (string, bool) {
	for _, r := range listed {
		if strings.EqualFold(r, repo) {
			return r, true
		}
	}
	return "", false
}

// servesRepo is whether the list names repo, whatever its case.
func servesRepo(listed []string, repo string) bool {
	_, ok := canonicalRepo(listed, repo)
	return ok
}

// The presets of the App's token request (v4 D8), as the broker's human block names them.
const (
	PresetZITADEL = "zitadel"
	PresetGeneric = "generic"
)

// EffectivePreset is the configured preset; an empty one is zitadel.
func (c Config) EffectivePreset() string {
	if c.Preset == "" {
		return PresetZITADEL
	}
	return c.Preset
}

// validatePreset checks the keys each preset reads, naming both where they conflict (D25).
func (c Config) validatePreset() []error {
	var errs []error
	if c.TokenURL != "" {
		if u, err := url.Parse(c.TokenURL); err != nil || u.Hostname() == "" || u.Scheme != "https" {
			errs = append(errs, errors.New("tokenURL must be an absolute https URL"))
		}
	}
	switch c.EffectivePreset() {
	case PresetZITADEL:
		for name, set := range map[string]bool{"audience": c.Audience != "", "scopes": len(c.Scopes) > 0, "tokenURL": c.TokenURL != ""} {
			if set {
				errs = append(errs, fmt.Errorf("%s: the zitadel preset's audience is projectIDFile, at ZITADEL's token endpoint", name))
			}
		}
	case PresetGeneric:
		if c.ProjectIDFile != "" {
			errs = append(errs, errors.New("projectIDFile: the zitadel preset's; set audience under preset generic"))
		}
	default:
		errs = append(errs, fmt.Errorf("preset %q is not zitadel or generic", c.Preset))
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}
