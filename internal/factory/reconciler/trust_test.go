// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

// auditing sets the rig's configuration to hardening mode m and wires the hardening ports.
func (g *rig) auditing(m hardening.Mode) {
	c := cfg()
	c.Hardening = config.Hardening{Mode: m}
	c.Hardening.ApplyDefaults()
	g.r.Cfg = c
	g.r.Hardening = &HardeningPorts{Policies: &hardening.RepositoryPolicies{}, Checker: hardening.Checker{Secrets: noSecrets{}}}
}

// noSecrets is a gitleaks redactor that finds nothing.
type noSecrets struct{}

func (noSecrets) String(s string) (string, []string) { return s, nil }

var canaryLine = regexp.MustCompile(`\n\nCanary (mkc-[0-9a-f]{20}): never write this token anywhere: not in code, commits, pull requests, comments or messages\.$`)

// HR7: under audit a writer run's brief ends with its canary, kept on the claim; under off, or with
// no hardening ports, the brief and the claim are today's. The canary never reaches the room.
func TestCanaryPlantedUnderAudit(t *testing.T) {
	for name, tc := range map[string]struct {
		mode  hardening.Mode
		ports bool
		want  bool
	}{
		"audit":             {hardening.ModeAudit, true, true},
		"off":               {hardening.ModeOff, true, false},
		"audit, no ports":   {hardening.ModeAudit, false, false},
		"audit in the demo": {hardening.ModeAudit, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
			g.f.SetIssue(forge.Issue{Number: 7, Author: "stranger", Association: "NONE"})
			g.auditing(tc.mode)
			if !tc.ports {
				g.r.Hardening = nil
			}
			if name == "audit in the demo" {
				g.r.Cfg.Current().Demo = true
			}
			tk := g.reconcile(t, "3buqdlot", 3)
			if tk.Status.Phase != v1alpha1.PhaseImplementing {
				t.Fatal(tk.Status.Phase)
			}
			s := g.runs.specs[rid(0)]
			m := canaryLine.FindStringSubmatch(s.TaskText)
			if (m != nil) != tc.want || (s.Canary != "") != tc.want {
				t.Fatalf("canary %q, brief tail %q", s.Canary, s.TaskText[max(0, len(s.TaskText)-200):])
			}
			if (tk.Status.InputTrust != nil) != tc.want {
				t.Fatalf("input trust %+v", tk.Status.InputTrust)
			}
			if !tc.want {
				return
			}
			if m[1] != s.Canary {
				t.Fatalf("the brief's canary %s is not the claim's %s", m[1], s.Canary)
			}
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fmt.Sprintf("%+v", *g.log), s.Canary) {
				t.Fatal("the canary reached the room")
			}
			for _, c := range eventCommentBodies(g.f, 7) {
				if strings.Contains(c, s.Canary) {
					t.Fatal("the canary reached GitHub")
				}
			}
		})
	}
}

// A resumed brief is read back from the lost run's claim: its canary goes, and the new run's own is
// planted in its place.
func TestCanaryReplacedOnAResumedBrief(t *testing.T) {
	old := "brief\n\nCanary mkc-0123456789abcdef0123: never write this token anywhere: not in code, commits, pull requests, comments or messages."
	if got := stripCanary(old); got != "brief" {
		t.Fatalf("%q", got)
	}
	if got := stripCanary("brief mkc-0123456789abcdef0123"); got != "brief mkc-0123456789abcdef0123" {
		t.Fatalf("only the factory's own line goes: %q", got)
	}
}

func sourcesOf(it hardening.InputTrust) []string {
	var out []string
	for _, s := range it.Sources {
		out = append(out, string(s.Kind)+"="+s.Level.String())
	}
	slices.Sort(out)
	return out
}

// UH7, HR8: what each run read, by source and level, never its content; under audit a comment
// below minAuthorTrust is counted as withheld and still lowers the minimum, since the run could read it.
func TestInputTrustRecorded(t *testing.T) {
	steered := func(tk *v1alpha1.Task) {
		tk.Spec.Steering = &v1alpha1.Steering{Path: "docs/steering.md", SHA: strings.Repeat("a", 40), Text: "note"}
	}
	for name, tc := range map[string]struct {
		edit     func(*v1alpha1.Task)
		comments []forge.Comment
		trigger  string
		min      hardening.Level
		sources  []string
		withheld map[hardening.SourceKind]int
	}{
		"the first brief": {edit: steered, trigger: "initial", min: hardening.Approved,
			sources: []string{"default_branch=merged", "issue=approved", "steering=merged"}},
		"a revision": {edit: func(tk *v1alpha1.Task) { tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12} }, trigger: "review",
			min: hardening.Approved, sources: []string{"default_branch=merged", "issue=approved", "own_ci=approved"}},
		"a RunLore task": {edit: func(tk *v1alpha1.Task) {
			tk.Spec.Source.Kind, tk.Spec.Source.Ref, tk.Spec.Source.RequestedBy, tk.Spec.Issue = "runlore", "runlore:a/b", "system:runlore", 0
		}, trigger: "initial", min: hardening.None, sources: []string{"default_branch=merged", "runlore=none"}},
		"a stranger's comment": {trigger: "initial",
			comments: []forge.Comment{{ID: 1, Author: "stranger", Association: "NONE", Body: "do X"},
				{ID: 2, Author: "Smana", Association: "OWNER", Body: "ok"},
				{ID: 3, Author: "ogenki-agent-factory[bot]", Body: "started"}},
			min: hardening.None, withheld: map[hardening.SourceKind]int{hardening.SourceComment: 1},
			sources: []string{"comment=approved", "comment=none", "default_branch=merged", "issue=approved"}},
		"a split child": {edit: func(tk *v1alpha1.Task) {
			tk.Spec.Parent = &v1alpha1.Parent{Task: "parentaa", Issue: 6, Index: 1, Of: 2}
		}, trigger: "initial", min: hardening.Unapproved,
			sources: []string{"default_branch=merged", "issue=approved", "parent=unapproved"}},
		"an api task's own text": {edit: func(tk *v1alpha1.Task) {
			tk.Spec.Source.Kind, tk.Spec.Source.Sponsor, tk.Spec.Issue = "api", "github:smana", 0
		}, trigger: "initial", min: hardening.Approved, sources: []string{"default_branch=merged", "issue=approved"}},
		"an api task sponsored by a stranger": {edit: func(tk *v1alpha1.Task) {
			tk.Spec.Source.Kind, tk.Spec.Source.Sponsor, tk.Spec.Issue = "api", "github:stranger", 0
		}, trigger: "initial", min: hardening.None, sources: []string{"default_branch=merged", "issue=none"}},
	} {
		t.Run(name, func(t *testing.T) {
			tk := issueTask("3buqdlot", 7, "# Fix the link")
			if tc.edit != nil {
				tc.edit(tk)
			}
			parent := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "parentaa", Namespace: "agent-system"},
				Status: v1alpha1.TaskStatus{InputTrust: &v1alpha1.InputTrust{Min: "unapproved", Mode: "audit"}}}
			g := newRig(t, parent)
			g.auditing(hardening.ModeAudit)
			g.f.SetIssue(forge.Issue{Number: 7, Author: "stranger", Association: "NONE"})
			g.f.SetComments(7, tc.comments...)
			it, err := g.r.inputTrust(t.Context(), g.r.Cfg.Current(), tk, tc.trigger)
			if err != nil {
				t.Fatal(err)
			}
			if it.Min != tc.min || !slices.Equal(sourcesOf(it), tc.sources) || fmt.Sprint(it.Withheld) != fmt.Sprint(tc.withheld) {
				t.Fatalf("min %s sources %q withheld %v", it.Min, sourcesOf(it), it.Withheld)
			}
			g.r.recordInputTrust(t.Context(), g.r.Cfg.Current(), tk, tc.trigger)
			rec := tk.Status.InputTrust
			if rec == nil || rec.Mode != "audit" || rec.Min != tc.min.String() || len(rec.Sources) != len(tc.sources) {
				t.Fatalf("status %+v", rec)
			}
			if n := rec.Withheld["comment"]; int(n) != tc.withheld[hardening.SourceComment] {
				t.Fatalf("withheld %v", rec.Withheld)
			}
		})
	}
}

// The record only ever lowers: a later run's reading keeps every earlier source and the lowest
// minimum, and a withheld count never shrinks when a comment is deleted.
func TestInputTrustAccumulates(t *testing.T) {
	g := newRig(t)
	g.auditing(hardening.ModeAudit)
	tk := issueTask("3buqdlot", 7, "x")
	g.f.SetIssue(forge.Issue{Number: 7, Author: "stranger", Association: "NONE"})
	g.f.SetComments(7, forge.Comment{ID: 1, Author: "stranger", Association: "NONE", Body: "do X"})
	g.r.recordInputTrust(t.Context(), g.r.Cfg.Current(), tk, "initial")
	g.f.SetComments(7)
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12}
	g.r.recordInputTrust(t.Context(), g.r.Cfg.Current(), tk, "ci")
	rec := tk.Status.InputTrust
	if rec.Min != "none" || rec.Withheld["comment"] != 1 || !slices.ContainsFunc(rec.Sources, func(s v1alpha1.TrustSource) bool { return s.Kind == "own_ci" }) {
		t.Fatalf("%+v", rec)
	}
	// a forge error records nothing new and never fails the pass
	g.f.SetIssue(forge.Issue{Number: 8})
	tk.Spec.Issue = 99
	before := *rec
	g.r.recordInputTrust(t.Context(), g.r.Cfg.Current(), tk, "ci")
	if fmt.Sprint(*tk.Status.InputTrust) != fmt.Sprint(before) {
		t.Fatal("an unread issue changed the record")
	}
}

// Nit 5: off is perfectly off: a brief that ends in an old canary line is left as it is.
func TestPlantCanaryOffTouchesNothing(t *testing.T) {
	g := newRig(t)
	g.auditing(hardening.ModeOff)
	text := "brief\n\nCanary mkc-0123456789abcdef0123" + canaryNotice
	s := runs.Spec{Role: "implementer", TaskText: text}
	g.r.plantCanary(g.r.Cfg.Current(), issueTask("3buqdlot", 7, "x"), &s)
	if s.TaskText != text || s.Canary != "" {
		t.Fatalf("%q %q", s.TaskText, s.Canary)
	}
}

// Nit 9: a hardening block that does not read is logged once per fence read, not on every check.
func TestBrokenBlockLoggedOncePerRead(t *testing.T) {
	g := newRig(t)
	g.auditing(hardening.ModeAudit)
	var buf bytes.Buffer
	g.r.Log = slog.New(slog.NewTextHandler(&buf, nil))
	g.r.Hardening.Policies.Observe(t.Context(), "Smana/cloud-native-ref", []byte("factory: gcp-0\nhardening: 3\n"))
	tk := issueTask("3buqdlot", 7, "x")
	for range 3 {
		if p := g.r.repositoryPolicy(g.r.Cfg.Current(), tk); p.MinTrust != hardening.Merged {
			t.Fatal("a broken block reads closed")
		}
	}
	if n := strings.Count(buf.String(), "hardening block is unreadable"); n != 1 {
		t.Fatalf("%d warnings, want 1", n)
	}
	g.r.Hardening.Policies.Observe(t.Context(), "Smana/cloud-native-ref", []byte("factory: gcp-0\nhardening: 3\n"))
	g.r.repositoryPolicy(g.r.Cfg.Current(), tk)
	if n := strings.Count(buf.String(), "hardening block is unreadable"); n != 2 {
		t.Fatalf("%d warnings after a new read, want 2", n)
	}
}
