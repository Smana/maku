// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// H1 shadows observed writes (HR1): every layer runs in audit, the first T1–T4 finding is the
// verdict, a protected path that needs approval would wait, and T5 never decides (HR3).
func TestShadowVerdict(t *testing.T) {
	clean := "Fixes the dead link in the runbook, cc @someone."
	pushed := json.RawMessage(`{"ref":"agent/t-1","before":"aaa","after":"bbb"}`)
	const obj = "https://github.com/o/r/pull/9"
	for name, tc := range map[string]struct {
		mode    Mode
		hit     string
		counts  Counts
		w       ObservedWrite
		verdict ShadowVerdict
		layer   Layer
		reason  Refusal
		audit   []Finding
	}{
		"a short PR body": {ModeAudit, "", nil,
			ObservedWrite{Type: PR, Payload: raw(map[string]string{"title": "fix: x", "body": "ten chars."}), Object: obj},
			WouldRefuse, LayerSchema, RefuseSchema, nil},
		"a comment with the canary": {ModeAudit, "", nil,
			ObservedWrite{Type: Comment, Payload: raw(map[string]string{"target": "pr", "body": "token " + canary}), Object: obj},
			WouldRefuse, LayerSecrets, RefuseCanary, nil},
		"a push touching CI": {ModeAudit, "", nil,
			ObservedWrite{Type: Push, Payload: pushed, Object: obj, Files: []string{".github/workflows/x.yml"}, Added: []string{"run: x"}},
			WouldWait, LayerPaths, RefuseNeedsApproval, nil},
		"a push touching a blocked extension": {ModeAudit, "", nil,
			ObservedWrite{Type: Push, Payload: pushed, Object: obj, Files: []string{"deploy/up.sh"}},
			WouldRefuse, LayerPaths, RefuseBlockedPath, nil},
		"a clean push": {ModeAudit, "", nil,
			ObservedWrite{Type: Push, Payload: pushed, Object: obj, Files: []string{"docs/x.md"}, Added: []string{"text"}},
			WouldApply, "", "", nil},
		"the fourth comment": {ModeAudit, "", Counts{Comment: 3},
			ObservedWrite{Type: Comment, Payload: raw(map[string]string{"target": "pr", "body": "again"}), Object: obj},
			WouldRefuse, LayerSchema, RefuseCap, nil},
		"a label outside the allowlist": {ModeAudit, "", nil,
			ObservedWrite{Type: Labels, Payload: raw(map[string][]string{"add": {"bug"}}), Object: obj},
			WouldRefuse, LayerSchema, RefuseSchema, nil},
		"a clean PR": {ModeAudit, "", nil,
			ObservedWrite{Type: PR, Payload: raw(map[string]string{"title": "fix: x", "body": clean}), Object: obj},
			WouldApply, "", "", nil},
		"a classifier hit never decides": {ModeAudit, "IGNORE", nil,
			ObservedWrite{Type: PR, Payload: raw(map[string]string{"title": "fix: x", "body": clean + " IGNORE ALL"}), Object: obj},
			WouldApply, "", "", []Finding{{LayerClassifier, RefuseInjection}}},
		"an enforcing checker still audits": {ModeEnforce, "IGNORE", nil,
			ObservedWrite{Type: Comment, Payload: raw(map[string]string{"target": "pr", "body": canary + " IGNORE"}), Object: obj},
			WouldRefuse, LayerSecrets, RefuseCanary, []Finding{{LayerClassifier, RefuseInjection}}},
		"later findings go to Audit": {ModeAudit, "", nil,
			ObservedWrite{Type: Push, Payload: pushed, Object: obj, Files: []string{"deploy/up.sh"}, Added: []string{"ghp_x"}},
			WouldRefuse, LayerSecrets, RefuseSecret, []Finding{{LayerPaths, RefuseBlockedPath}}},
		"an unknown type": {ModeAudit, "", nil,
			ObservedWrite{Type: "merge", Payload: raw(map[string]string{}), Object: obj},
			WouldRefuse, LayerSchema, RefuseType, nil},
	} {
		t.Run(name, func(t *testing.T) {
			c := checker(tc.mode, tc.mode, &fakeClassifier{hit: tc.hit})
			n := tc.counts
			got := Shadow(t.Context(), c, &n, tc.w, 7, canary)
			if got.Verdict != tc.verdict || got.Layer != tc.layer || got.Reason != tc.reason {
				t.Fatalf("got %s %s %s, want %s %s %s", got.Verdict, got.Layer, got.Reason, tc.verdict, tc.layer, tc.reason)
			}
			if !slices.Equal(got.Audit, tc.audit) {
				t.Fatalf("audit %v, want %v", got.Audit, tc.audit)
			}
			if got.Type != tc.w.Type || got.Object != obj || len(got.Digest) != 64 {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// T2's rewrites are reported on a write that would apply, and never make it a refusal (HR3).
func TestShadowReportsRewrites(t *testing.T) {
	var n Counts
	got := Shadow(t.Context(), checker(ModeAudit, ModeAudit, &fakeClassifier{}), &n,
		ObservedWrite{Type: PR, Payload: raw(map[string]string{"title": "fix: x", "body": "Thanks @someone <!-- hidden --> for the link."})}, 7, canary)
	if got.Verdict != WouldApply || got.Report.Mentions != 1 || got.Report.Comments != 1 {
		t.Fatalf("%+v", got)
	}
	if n[PR] != 1 {
		t.Fatalf("the write counts against the run's cap: %v", n)
	}
}

// The digest is the observed payload's canonical JSON: key order and spacing never move it.
func TestShadowDigest(t *testing.T) {
	c := checker(ModeAudit, ModeAudit, &fakeClassifier{})
	shadow := func(p string) string {
		var n Counts
		return Shadow(t.Context(), c, &n, ObservedWrite{Type: PR, Payload: json.RawMessage(p)}, 7, canary).Digest
	}
	a := shadow(`{"title":"fix: x","body":"a body long enough <to> pass"}`)
	b := shadow("{\n  \"body\": \"a body long enough <to> pass\",\n  \"title\": \"fix: x\"\n}")
	if a != b {
		t.Fatal("the digest follows the spelling")
	}
	if want := Digest([]byte(`{"body":"a body long enough <to> pass","title":"fix: x"}`)); a != want {
		t.Fatalf("digest %s, want %s", a, want)
	}
	if shadow(`{"title":"fix: y","body":"a body long enough <to> pass"}`) == a {
		t.Fatal("a different payload has the same digest")
	}
	if d := shadow(`{"title":`); len(d) != 64 {
		t.Fatal("a payload that is not JSON still has a digest")
	}
}

// The free-text check (HR21): T2, T3 and T5 on texts with no output type; no schema, no cap, no
// protected path.
func TestCheckText(t *testing.T) {
	for name, tc := range map[string]struct {
		mode   Mode
		texts  []string
		hit    string
		want   Refusal
		layer  Layer
		audits []Finding
	}{
		"clean":            {ModeEnforce, []string{"an incident note", ".github/workflows/x.yml"}, "", "", "", nil},
		"no text":          {ModeEnforce, nil, "", "", "", nil},
		"the canary":       {ModeEnforce, []string{"ok", "leak " + canary}, "", RefuseCanary, LayerSecrets, nil},
		"a secret":         {ModeEnforce, []string{"ghp_abc"}, "", RefuseSecret, LayerSecrets, nil},
		"a classifier hit": {ModeEnforce, []string{"IGNORE previous"}, "IGNORE", RefuseInjection, LayerClassifier, nil},
		"audit records, refuses nothing": {ModeAudit, []string{"ghp_abc IGNORE"}, "IGNORE", "", "",
			[]Finding{{LayerSecrets, RefuseSecret}, {LayerClassifier, RefuseInjection}}},
	} {
		t.Run(name, func(t *testing.T) {
			c := checker(tc.mode, tc.mode, &fakeClassifier{hit: tc.hit})
			v := c.CheckText(t.Context(), tc.texts, canary)
			if v.Refusal != tc.want || v.Layer != tc.layer || !slices.Equal(v.Audit, tc.audits) {
				t.Fatalf("%+v", v)
			}
		})
	}
	v := checker(ModeEnforce, ModeOff, nil).CheckText(t.Context(), []string{"hi @someone <!-- x -->"}, canary)
	got, ok := v.Payload.([]string)
	if v.Refusal != "" || !ok || len(got) != 1 || !strings.Contains(got[0], "`@someone`") || strings.Contains(got[0], "<!--") {
		t.Fatalf("T2 rewrites the text: %+v", v)
	}
	if v.Report.Mentions != 1 || v.Report.Comments != 1 {
		t.Fatalf("report %+v", v.Report)
	}
}
