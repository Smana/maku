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

// CD11: proposals are off unless the deployment says otherwise, and the decoder is strict, so a
// switch never lands ahead of the release that reads it.
func TestLoadProposals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		want   bool
		errHas string
	}{
		{name: "absent, the switch is off", text: goodConfig},
		{name: "enabled", text: goodConfig + "proposals:\n  enabled: true\n", want: true},
		{name: "explicitly off", text: goodConfig + "proposals:\n  enabled: false\n"},
		{name: "not an object", text: goodConfig + "proposals: true\n", errHas: "proposals"},
		{name: "unknown key", text: goodConfig + "proposals:\n  on: true\n", errHas: "on"},
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
			if got := c.Proposals.Enabled; got != tc.want {
				t.Fatalf("Proposals.Enabled = %v, want %v", got, tc.want)
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
				// The default is webhook.Meaningful's table, per type: a local list that drifted
				// from it would fail here rather than at the renderer.
				for _, typ := range webhook.Types() {
					if got, want := ch.notifies(typ), webhook.Meaningful(typ); got != want {
						t.Errorf("notifies(%q) = %v, want %v: absent must be the meaningful table", typ, got, want)
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
