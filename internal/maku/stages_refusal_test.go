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

// Any other reason the refusal names is said as the broker said it; a bare 404 keeps its old words.
func TestARefusalSaysTheReasonItsBodyNames(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"error":"unknown_workflow"}`, "workflow dry-run: 404: unknown_workflow"},
		{`{"error":"link_unverified"}`, "link_unverified: the factory could not check your GitHub link"},
		{"", "workflow dry-run: 404: no such room"},
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
