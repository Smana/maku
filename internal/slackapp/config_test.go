// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/webhook"
)

const goodConfig = `
teamID: T01ABC
botUserIDFile: /s/uid
botTokenFile: /s/tok
signingSecretFile: /s/sig
webhookKeyFile: /s/key
brokerURL: https://room-broker.agents.svc.cluster.local
roomsURL: https://rooms.example
namespace: agents
channels:
  C03:
    repositories: [Smana/ai-native-ref]
    maxCostUSD: 2
`

// withChannelKey is goodConfig with line inserted as the C03 channel's first key. A goodConfig that
// stopped matching would leave the key out, and every case built on it would test nothing.
func withChannelKey(line string) string {
	return strings.Replace(goodConfig, "channels:\n  C03:\n", "channels:\n  C03:\n"+line, 1)
}

// withApprovals is withChannelKey for the D14 switch; the empty string is the key absent.
func withApprovals(line string) string { return withChannelKey(line) }

// D14: who gets the affordance is a per-channel switch, false when absent; an unknown key is still
// refused, so an older App refuses a config that asks for cards rather than ignoring it.
func TestLoadConfigApprovals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		want   bool
		errHas string
	}{
		{name: "absent, the switch is off", text: goodConfig, want: false},
		{name: "true", text: withApprovals("    approvals: true\n"), want: true},
		{name: "false", text: withApprovals("    approvals: false\n"), want: false},
		{name: "not a boolean", text: withApprovals("    approvals: \"yes\"\n"), errHas: "approvals"},
		{name: "unknown channel key", text: withApprovals("    cards: true\n"), errHas: "cards"},
		{name: "no repositories is still refused",
			text:   strings.Replace(withApprovals("    approvals: false\n"), "[Smana/ai-native-ref]", "[]", 1),
			errHas: "repositories is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := loadConfigText(t, tc.text)
			if tc.errHas != "" {
				if err == nil {
					t.Fatalf("accepted, want an error naming %q", tc.errHas)
				}
				if !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("error %q does not name %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Channels["C03"].Approvals; got != tc.want {
				t.Fatalf("Channels[C03].Approvals = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProposalsSwitchRemoved(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "enabled", text: goodConfig + "proposals:\n  enabled: true\n"},
		{name: "disabled", text: goodConfig + "proposals:\n  enabled: false\n"},
		{name: "scalar", text: goodConfig + "proposals: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfigText(t, tc.text)
			if err == nil || !strings.Contains(err.Error(), "proposals") {
				t.Fatalf("got %v, want unknown-field error naming proposals", err)
			}
		})
	}
}

func loadConfigText(t *testing.T, text string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(p)
}

// CD11 and CD24: a channel's notify list is which transitions post a line there. Absent means
// webhook.Notify() (the nine); an explicit empty list means none, which is a choice, not an
// omission. An unknown name and a name given twice are refused, so a generated declaration fails
// here rather than silently posting nothing.
func TestLoadNotify(t *testing.T) {
	for _, tc := range []struct {
		name   string
		line   string   // the C03 channel's first key; "" is the key absent
		want   []string // the transitions that answer true; nil means "the default table"
		errHas []string // every substring the error must carry
	}{
		{name: "absent, the default is the meaningful table", line: ""},
		{name: "two of them", line: "    notify: [room.waiting_for_human, room.approval_closed]\n",
			want: []string{"room.waiting_for_human", "room.approval_closed"}},
		{name: "empty is none, not the default", line: "    notify: []\n", want: []string{}},
		{name: "an unknown transition", line: "    notify: [nope]\n", errHas: []string{"nope", "C03"}},
		{name: "the same transition twice", line: "    notify: [room.waiting_for_human, room.waiting_for_human]\n",
			errHas: []string{"room.waiting_for_human", "C03", "more than once"}},
		{name: "not a list", line: "    notify: room.merged\n", errHas: []string{"notify"}},
		{name: "a transition the App draws no line for", line: "    notify: [room.monitor_verdict]\n",
			errHas: []string{"room.monitor_verdict", "C03", "no line"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := loadConfigText(t, withChannelKey(tc.line))
			if len(tc.errHas) > 0 {
				if err == nil {
					t.Fatalf("accepted, want an error naming %q", tc.errHas)
				}
				for _, want := range tc.errHas {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not name %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ch := c.Channels["C03"]
			if tc.want == nil {
				for _, typ := range webhook.Types() {
					if got, want := ch.notifies(typ), slices.Contains(defaultNotify, typ); got != want {
						t.Errorf("notifies(%q) = %v, want %v: absent must be defaultNotify", typ, got, want)
					}
				}
				// The two anchors the design names, whichever way the table is spelled.
				if ch.notifies("room.run_started") || !ch.notifies("room.task_triaged") {
					t.Fatalf("absent: notifies(room.task_triaged) = %v, notifies(room.run_started) = %v",
						ch.notifies("room.task_triaged"), ch.notifies("room.run_started"))
				}
				return
			}
			for _, typ := range webhook.Types() {
				if got, want := ch.notifies(typ), slices.Contains(tc.want, typ); got != want {
					t.Errorf("notifies(%q) = %v, want %v", typ, got, want)
				}
			}
		})
	}
}

// The App's config is strict like the broker's: no channel, a sub-cent max_cost (the factory
// refuses it), a repository that is not owner/name, or an unknown key is refused; the three
// listener addresses default.
func TestConfig(t *testing.T) {
	c, err := loadConfigText(t, goodConfig)
	if err != nil {
		t.Fatal(err)
	}
	if c.EventsAddr != ":8080" || c.WebhooksAddr != ":8081" || c.OpsAddr != ":9090" {
		t.Fatalf("defaults: %+v", c)
	}
	for name, text := range map[string]string{
		"no channel":  strings.Replace(goodConfig, "channels:\n  C03:\n    repositories: [Smana/ai-native-ref]\n    maxCostUSD: 2\n", "channels: {}\n", 1),
		"sub-cent":    strings.Replace(goodConfig, "maxCostUSD: 2", "maxCostUSD: 0.005", 1),
		"bad repo":    strings.Replace(goodConfig, "Smana/ai-native-ref", "not-a-repo", 1),
		"no repos":    strings.Replace(goodConfig, "[Smana/ai-native-ref]", "[]", 1),
		"unknown key": goodConfig + "surprise: 1\n",
		"no team":     strings.Replace(goodConfig, "teamID: T01ABC\n", "", 1),
		"bad team":    strings.Replace(goodConfig, "T01ABC", "nope", 1),
		"no secret":   strings.Replace(goodConfig, "signingSecretFile: /s/sig\n", "", 1),
		"no token":    strings.Replace(goodConfig, "botTokenFile: /s/tok\n", "", 1),
		"no key":      strings.Replace(goodConfig, "webhookKeyFile: /s/key\n", "", 1),
		"no bot id":   strings.Replace(goodConfig, "botUserIDFile: /s/uid\n", "", 1),
	} {
		if _, err := loadConfigText(t, text); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Natural-language intents: a channel's intents switch is off, shadow or on (SI20), collectReplies is its
// agreement to storing replies and needs a switch that classifies (SI19), and intentActions lists only
// the four write intents, each once (SI21).
func TestLoadIntents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		line    string
		intent  string
		collect bool
		errHas  []string
	}{
		{name: "absent is off", line: "", intent: ""},
		{name: "off", line: "    intents: off\n", intent: "off"},
		{name: "shadow", line: "    intents: shadow\n", intent: "shadow"},
		{name: "on", line: "    intents: \"on\"\n", intent: "on"},
		{name: "bare off, which YAML 1.1 reads as false", line: "    intents: off\n", intent: "off"},
		{name: "bare on, which YAML 1.1 reads as true", line: "    intents: on\n", intent: "on"},
		{name: "a number is not a state", line: "    intents: 1\n", errHas: []string{"intents"}},
		{name: "another word", line: "    intents: live\n", errHas: []string{`intents "live"`, "C03"}},
		{name: "collecting under shadow", line: "    intents: shadow\n    collectReplies: true\n", intent: "shadow", collect: true},
		{name: "collecting with no switch", line: "    collectReplies: true\n", errHas: []string{"collectReplies needs intents"}},
		{name: "collecting under off", line: "    intents: off\n    collectReplies: true\n", errHas: []string{"collectReplies needs intents"}},
		{name: "the four actions", line: "    intents: \"on\"\n    intentActions: [stop, retry, steer, amend]\n", intent: "on"},
		{name: "stop, retry and answer without intents on", line: "    intents: off\n    intentActions: [stop, retry, answer]\n", intent: "off"},
		{name: "a read intent is no action", line: "    intentActions: [status]\n", errHas: []string{`intentActions "status"`}},
		{name: "an action twice", line: "    intentActions: [stop, stop]\n", errHas: []string{"more than once"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := loadConfigText(t, withChannelKey(tc.line))
			if len(tc.errHas) > 0 {
				if err == nil {
					t.Fatalf("loaded %q", tc.line)
				}
				for _, h := range tc.errHas {
					if !strings.Contains(err.Error(), h) {
						t.Fatalf("%v does not name %q", err, h)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Channels["C03"]; got.Intents != tc.intent || got.CollectReplies != tc.collect {
				t.Fatalf("channel = %+v", got)
			}
		})
	}
}

// apiBaseURL (v4 step 6, Task 16's follow-up) is the test seam for Slack's Web API: absent is
// slack.com, set it must be an http(s) URL; the e2e stub stands in for Slack behind it.
func TestConfigAPIBaseURL(t *testing.T) {
	c, err := loadConfigText(t, goodConfig)
	if err != nil || c.APIBaseURL != "" {
		t.Fatalf("absent: %q %v", c.APIBaseURL, err)
	}
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"https://e2e-stub.e2e.svc.cluster.local/api/", true},
		{"http://e2e-stub.e2e.svc.cluster.local:8080/api/", true},
		{"ftp://x/api/", false},
		{"not a url", false},
		{"https:///api/", false},
	} {
		c, err := loadConfigText(t, goodConfig+"apiBaseURL: "+tc.value+"\n")
		if (err == nil) != tc.ok {
			t.Errorf("%q: err %v, want ok %v", tc.value, err, tc.ok)
		}
		if tc.ok && c.APIBaseURL != tc.value {
			t.Errorf("%q: decoded %q", tc.value, c.APIBaseURL)
		}
	}
}

// escalatedHours is the factory's caps.escalatedHours, mirrored: absent is the default, 0 is no
// limit, a negative number is refused as the factory refuses it.
func TestLoadConfigEscalatedHours(t *testing.T) {
	for _, tc := range []struct {
		name   string
		line   string
		want   *int
		errHas string
	}{
		{name: "absent", line: ""},
		{name: "zero is no limit", line: "escalatedHours: 0\n", want: new(0)},
		{name: "set", line: "escalatedHours: 48\n", want: new(48)},
		{name: "negative", line: "escalatedHours: -1\n", errHas: "escalatedHours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := loadConfigText(t, goodConfig+tc.line)
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want one naming %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (c.EscalatedHours == nil) != (tc.want == nil) || (tc.want != nil && *c.EscalatedHours != *tc.want) {
				t.Fatalf("EscalatedHours = %v, want %v", c.EscalatedHours, tc.want)
			}
		})
	}
}

func TestLoadUpkeepChannel(t *testing.T) {
	baseHeader := `
teamID: T01ABC
botUserIDFile: /s/uid
botTokenFile: /s/tok
signingSecretFile: /s/sig
webhookKeyFile: /s/key
brokerURL: https://room-broker.agents.svc.cluster.local
roomsURL: https://rooms.example
namespace: agents
channels:
`
	for _, tc := range []struct {
		name     string
		channels string
		errHas   []string
		check    func(*testing.T, Config)
	}{
		{
			name: "upkeep-only channel without interactive repositories or maxCostUSD",
			channels: `  C03UP:
    upkeep:
      repositories: [Smana/ai-native-ref]
      routines: [dead-links]
      digest:
        cron: "0 9 * * 1"
`,
			check: func(t *testing.T, c Config) {
				ch := c.Channels["C03UP"]
				if ch.Upkeep == nil || len(ch.Repositories) != 0 || ch.MaxCostUSD != 0 {
					t.Fatalf("unexpected channel: %+v", ch)
				}
				if !ch.Upkeep.notifies("room.pr_opened") || !ch.Upkeep.notifies("routine.paused") || ch.Upkeep.notifies("room.task_triaged") {
					t.Fatalf("default upkeep notify mismatch")
				}
			},
		},
		{
			name: "explicit upkeep notify subset",
			channels: `  C03UP:
    upkeep:
      repositories: [Smana/ai-native-ref]
      notify: [room.pr_opened, routine.paused, room.merged]
`,
			check: func(t *testing.T, c Config) {
				u := c.Channels["C03UP"].Upkeep
				if !u.notifies("room.merged") || u.notifies("room.ended") {
					t.Fatalf("explicit upkeep notify mismatch: %+v", u.Notify)
				}
			},
		},
		{
			name: "empty upkeep repositories refused",
			channels: `  C03UP:
    upkeep:
      repositories: []
`,
			errHas: []string{"C03UP", "upkeep", "repositories is empty"},
		},
		{
			name: "bad upkeep repository refused",
			channels: `  C03UP:
    upkeep:
      repositories: [not-a-repo]
`,
			errHas: []string{"C03UP", "upkeep", "not-a-repo"},
		},
		{
			name: "bad routine name refused",
			channels: `  C03UP:
    upkeep:
      repositories: [Smana/ai-native-ref]
      routines: [BAD_ROUTINE]
`,
			errHas: []string{"C03UP", "upkeep", "BAD_ROUTINE"},
		},
		{
			name: "duplicate routine refused",
			channels: `  C03UP:
    upkeep:
      repositories: [Smana/ai-native-ref]
      routines: [dead-links, dead-links]
`,
			errHas: []string{"C03UP", "upkeep", "dead-links", "more than once"},
		},
		{
			name: "disallowed upkeep notify room.task_triaged refused",
			channels: `  C03UP:
    upkeep:
      repositories: [Smana/ai-native-ref]
      notify: [room.task_triaged]
`,
			errHas: []string{"C03UP", "upkeep", "room.task_triaged"},
		},
		{
			name: "duplicate upkeep notify refused",
			channels: `  C03UP:
    upkeep:
      repositories: [Smana/ai-native-ref]
      notify: [routine.paused, routine.paused]
`,
			errHas: []string{"C03UP", "upkeep", "routine.paused", "more than once"},
		},
		{
			name: "invalid digest cron refused",
			channels: `  C03UP:
    upkeep:
      repositories: [Smana/ai-native-ref]
      digest:
        cron: "not a cron"
`,
			errHas: []string{"C03UP", "upkeep", "cron"},
		},
		{
			name: "sub-cent positive maxCostUSD on upkeep channel refused",
			channels: `  C03UP:
    maxCostUSD: 0.005
    upkeep:
      repositories: [Smana/ai-native-ref]
`,
			errHas: []string{"C03UP", "maxCostUSD"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := loadConfigText(t, baseHeader+tc.channels)
			if len(tc.errHas) > 0 {
				if err == nil {
					t.Fatalf("accepted, want error naming %v", tc.errHas)
				}
				for _, want := range tc.errHas {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not name %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}
