// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/stages"
)

func refusingBroker(t *testing.T, status int, body string) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return Client{URL: srv.URL, HC: srv.Client(), Token: func(context.Context) (string, error) { return "t", nil }}
}

// The factory's not_found on a repository-scoped read is the caller's repository not readable (a missing
// GitHub link), never "no such room": the message names the repository and the way to fix it.
func TestStagesAndDryRunExplainARepositoryNotFound(t *testing.T) {
	calls := map[string]func(Client) error{
		"stages": func(c Client) error {
			_, err := c.Stages(t.Context(), "Smana/ai-native-ref", "", 0)
			return err
		},
		"workflow dry-run": func(c Client) error {
			_, err := c.WorkflowDryRun(t.Context(), stages.DryRunRequest{Workflow: "standard", Repository: "Smana/ai-native-ref"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c := refusingBroker(t, http.StatusNotFound, `{"error":"not_found"}`+"\n")
			err := call(c)
			if err == nil {
				t.Fatal("no refusal")
			}
			msg := err.Error()
			for _, want := range []string{name + ": 404: not_found", "Smana/ai-native-ref", "maku doctor", c.URL + "/link/github"} {
				if !strings.Contains(msg, want) {
					t.Errorf("%q lacks %q", msg, want)
				}
			}
			if strings.Contains(msg, "no such room") {
				t.Errorf("%q still says no such room", msg)
			}
		})
	}
}

// Any other reason the refusal names is said as the broker said it; a bare 404 off a room route is "not found".
func TestARefusalSaysTheReasonItsBodyNames(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"error":"unknown_workflow"}`, "workflow dry-run: 404: unknown_workflow"},
		{`{"error":"link_unverified"}`, "link_unverified: the factory could not check your GitHub link"},
		{"", "workflow dry-run: 404: not found"}, // not a room route: never "no such room"
	}
	for _, tc := range cases {
		status := http.StatusNotFound
		if strings.Contains(tc.body, "link_unverified") {
			status = http.StatusServiceUnavailable
		}
		_, err := refusingBroker(t, status, tc.body).WorkflowDryRun(t.Context(), stages.DryRunRequest{Workflow: "x", Repository: "Smana/a"})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.body, err, tc.want)
		}
	}
}

// A refusal names its real cause from the body's reason: "no such room" is said of a room's own route
// alone, a not_linked points at the broker's link page, and an unknown task is a missing task.
func TestARefusalNamesItsCauseNotARoom(t *testing.T) {
	const broker = "https://maku.example/"
	cases := []struct {
		name string
		r    Refusal
		want string
	}{
		{"not linked, on a 403", Refusal{Op: "stages", Code: 403, Refused: "not_linked", Broker: broker},
			"stages: 403: not_linked: your Maku account is not linked to GitHub: open https://maku.example/link/github"},
		{"not linked, on a 404", Refusal{Op: "workflow dry-run", Code: 404, Refused: "not_linked", Broker: broker},
			"workflow dry-run: 404: not_linked: your Maku account is not linked to GitHub: open https://maku.example/link/github"},
		{"unknown task", Refusal{Op: "cancel", Code: 404, Refused: "unknown_task", Broker: broker}, "cancel: 404: unknown_task: no such task"},
		{"a room's bare 404", Refusal{Op: "room abcdefgh", Code: 404, Room: true}, "room abcdefgh: 404: no such room"},
		{"a room's not_found", Refusal{Op: "room abcdefgh", Code: 404, Refused: "not_found", Room: true}, "room abcdefgh: 404: no such room"},
		{"another route's not_found", Refusal{Op: "policy", Code: 404, Refused: "not_found"}, "policy: 404: not_found"},
		{"another route's bare 404", Refusal{Op: "policy", Code: 404}, "policy: 404: not found: this broker serves no such route"},
		{"a bare 403", Refusal{Op: "room abcdefgh", Code: 403, Room: true}, "room abcdefgh: 403: not in an agents group"},
		{"a 403 that names its reason", Refusal{Op: "policy", Code: 403, Refused: "not_maintainer"}, "policy: 403: not_maintainer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.r.Error()
			if !strings.HasPrefix(got, c.want) {
				t.Fatalf("%q, want %q", got, c.want)
			}
			if !c.r.Room && strings.Contains(got, "no such room") {
				t.Fatalf("%q says no such room off a room route", got)
			}
		})
	}
}

// The refusal a client reads carries its broker, so the not_linked message names the link page.
func TestANotLinkedRefusalNamesTheBrokersLinkPage(t *testing.T) {
	c := refusingBroker(t, http.StatusForbidden, `{"error":"not_linked"}`)
	_, err := c.Stages(t.Context(), "Smana/a", "", 0)
	if err == nil || !strings.Contains(err.Error(), "not linked to GitHub: open "+c.URL+"/link/github") {
		t.Fatalf("%v", err)
	}
}
