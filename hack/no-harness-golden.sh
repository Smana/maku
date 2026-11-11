#!/usr/bin/env bash
# Regenerates internal/factory/reconciler/testdata/no-harness.json, the expectation behind
# TestNoHarnessIsToday: each case's whole Task after one reconcile on a base commit (default
# 88e4c11, origin/main before the team evaluation), normalized as the test normalizes it. The
# generator below is compiled in a detached worktree of the base against the base's own helpers
# (cfg, sponsoredConfig, newRig, issueTask, triage.Classify), so the golden is main's behaviour, not
# the code under test. Its cases mirror TestNoHarnessIsToday's; change both together.
#
#   hack/no-harness-golden.sh [base-commit]
set -euo pipefail

BASE=${1:-88e4c11}
ROOT=$(git rev-parse --show-toplevel)
OUT="$ROOT/internal/factory/reconciler/testdata/no-harness.json"
TMP=$(mktemp -d)
trap 'git -C "$ROOT" worktree remove --force "$TMP/wt" >/dev/null 2>&1 || true; rm -rf "$TMP"' EXIT

git -C "$ROOT" worktree add --detach "$TMP/wt" "$BASE" >/dev/null
cat >"$TMP/wt/internal/factory/reconciler/zz_capture_test.go" <<'EOF'
// SPDX-License-Identifier: Apache-2.0

package reconciler

// Written by hack/no-harness-golden.sh into a worktree of the base commit; never committed.

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

// normalizedTask is the Task as JSON with its volatile fields fixed: the server-set metadata
// dropped, every RFC3339 time "<time>". The same function lives in profile_test.go on the branch.
func normalizedTask(t *testing.T, tk *v1alpha1.Task) any {
	t.Helper()
	raw, err := json.Marshal(tk)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if meta, ok := v.(map[string]any)["metadata"].(map[string]any); ok {
		for _, k := range []string{"resourceVersion", "generation", "uid", "creationTimestamp", "managedFields"} {
			delete(meta, k)
		}
	}
	return scrubTimes(v)
}

func scrubTimes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = scrubTimes(e)
		}
	case []any:
		for i, e := range x {
			x[i] = scrubTimes(e)
		}
	case string:
		if _, err := time.Parse(time.RFC3339, x); err == nil {
			return "<time>"
		}
	}
	return v
}

func TestCaptureNoHarnessGolden(t *testing.T) {
	out := os.Getenv("NO_HARNESS_GOLDEN")
	if out == "" {
		t.Skip("NO_HARNESS_GOLDEN unset")
	}
	base := func() *config.Config {
		c := cfg()
		steps := int32(40)
		c.Loops = map[string]config.Loop{"review": {StallSteps: &steps, Search: true, Scope: config.ScopeScoper}}
		c.HarnessProfiles = map[string]config.HarnessProfile{"review-floor": {Template: "pair", Checks: true}}
		c.Templates["investigate"] = config.Template{Roles: []string{"triager"}}
		c.Classes = map[string]config.Class{"docs-links": {Shadow: true}, "review": {}, "tests": {}}
		for name, usd := range map[string][2]float64{"standard": {2.55, 5.10}, "frontier": {6.80, 13.60}} {
			tr := c.Tiers[name]
			tr.RunUSD, tr.TaskUSD = usd[0], usd[1]
			c.Tiers[name] = tr
		}
		lc := sponsoredConfig(t)
		c.API, c.Budgets = lc.API, lc.Budgets
		c.API.InternalPrincipals = []config.InternalPrincipal{{Sponsored: config.Sponsored{Name: "runlore", Sponsor: "smana"}, DailyTasks: 5}}
		return c
	}
	cf := base()
	sponsoredAPI := issueTask("3buqdlot", 0, "Fix the dead link in docs/a.md.")
	sponsor(sponsoredAPI, 2_000_000)
	internal := sponsoredAPI.DeepCopy()
	internal.Spec.Source.Ref, internal.Spec.Source.RequestedBy = "api:runlore", cf.API.InternalPrincipals[0].Principal()
	internal.Spec.DataClass = "internal"
	schedule := issueTask("3buqdlot", 0, "add tests")
	schedule.Spec.Source.Kind, schedule.Spec.PredictedClass = "schedule", "tests"
	runlore := issueTask("3buqdlot", 7, "a finding")
	runlore.Spec.Source.Kind, runlore.Spec.DataClass = "runlore", "internal"
	origin := issueTask("3buqdlot", 7, "follow-up")
	origin.Spec.Origin, origin.Spec.PredictedClass = "parent1", "tests"
	sponsored := issueTask("3buqdlot", 7, "fix it")
	sponsored.Spec.MaxMicroUSD = 2_000_000
	cases := map[string]struct {
		tk    *v1alpha1.Task
		label string
	}{
		"internal api":    {internal, ""},
		"sponsored api":   {sponsoredAPI, ""},
		"issue":           {issueTask("3buqdlot", 7, "fix it"), ""},
		"labelled issue":  {issueTask("3buqdlot", 7, "fix it"), "class:review"},
		"schedule":        {schedule, ""},
		"runlore":         {runlore, ""},
		"origin":          {origin, ""},
		"sponsored issue": {sponsored, ""},
	}
	golden := map[string]any{}
	for name, c := range cases {
		cf := base()
		tk := c.tk.DeepCopy()
		g := newRig(t, tk)
		g.r.Cfg = cf
		g.r.Triage = triage.Classify{Cfg: cf, Forge: g.f}
		if tk.Spec.Issue != 0 {
			var labels []string
			if c.label != "" {
				labels = []string{c.label}
				g.f.SetEvents(tk.Spec.Issue, forge.LabelEvent{Actor: "Smana", Label: c.label, At: now})
			}
			g.f.SetIssue(forge.Issue{Number: tk.Spec.Issue, Labels: labels})
		}
		got := g.reconcile(t, "3buqdlot", 1)
		if got.Status.Phase != v1alpha1.PhaseTriaged {
			t.Fatalf("%s: %s %s", name, got.Status.Phase, got.Status.Reason)
		}
		golden[name] = normalizedTask(t, got)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(golden); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
EOF

(cd "$TMP/wt" && NO_HARNESS_GOLDEN="$OUT" go test -count=1 ./internal/factory/reconciler/ -run '^TestCaptureNoHarnessGolden$' >/dev/null)
echo "wrote ${OUT#"$ROOT"/} from $(git -C "$ROOT" rev-parse --short "$BASE")"
