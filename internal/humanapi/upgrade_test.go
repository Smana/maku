// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/runrequest"
)

// P14, G2: a plan/v1 confirmed after the upgrade is shown again as plan/v2 with a new hash, on
// plan_check and on run_create alike: nothing is filed and no model is called.
func TestUpgradeIsShownNeverFiled(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
	for _, c := range []struct {
		name, tool string
		surface    string
		args       func(v1 intake.Plan) map[string]any
		headers    string
	}{
		{"plan_check", "plan_check", "session", func(v1 intake.Plan) map[string]any {
			return map[string]any{"plan": v1, "hash": intake.Hash(v1)}
		}, "dev"},
		{"run_create", "run_create", "slack", func(v1 intake.Plan) map[string]any {
			return map[string]any{"repo": v1.Repository, "title": v1.Issues[0].Title, "text": v1.Issues[0].Body,
				"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF",
				"plan": v1, "plan_hash": intake.Hash(v1)}
		}, "slack-app"},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := []option{mcpOn, actorOn(&actLog{}), withIntake(t, c.surface), withTaskFactory(f)}
			if c.surface == "slack" {
				opts = append(opts, withSponsored(sponsoredP))
			}
			e := setup(t, opts...)
			v1 := planFixture(t, "v1-slack-one-issue")
			if c.tool == "plan_check" {
				v1 = planFixture(t, "v1-single")
			}
			h := viaRoomctl("dev")
			if c.headers == "slack-app" {
				h = bearer("slack-app")
			}
			got := call(t, e, h, c.tool, c.args(v1))
			if got.isError {
				t.Fatal(got.text)
			}
			var out planReplanned
			if err := json.Unmarshal(got.structured, &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != "replanned" || out.Plan.APIVersion != intake.APIVersion || out.Hash == intake.Hash(v1) ||
				out.Hash != intake.Hash(out.Plan) || out.Plan.Issues[0].Body != v1.Issues[0].Body || out.Plan.Issues[0].Profile != "" {
				t.Fatalf("%+v", out)
			}
			if !strings.HasPrefix(got.text, replannedLine) {
				t.Fatalf("text %q", got.text)
			}
			if len(f.got) != 0 || e.intake.calls() != 0 {
				t.Fatalf("filed %d, model calls %d", len(f.got), e.intake.calls())
			}
			// The upgraded plan is the one that files.
			if c.tool == "run_create" {
				args := c.args(v1)
				args["plan"], args["plan_hash"] = out.Plan, out.Hash
				if got := call(t, e, h, c.tool, args); got.isError || len(f.got) != 1 {
					t.Fatalf("%s %d", got.text, len(f.got))
				}
			} else if got := call(t, e, h, c.tool, map[string]any{"plan": out.Plan, "hash": out.Hash}); got.isError {
				t.Fatal(got.text)
			}
		})
	}
}
