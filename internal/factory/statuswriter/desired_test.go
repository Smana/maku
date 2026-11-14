// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"errors"
	"strings"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

var now = metav1.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))

const repo = "acme/app"

func cfg(repos ...config.Repository) *config.Config { return &config.Config{Repositories: repos} }

func obj(name string, gen int64) v1alpha1.Factory {
	f := v1alpha1.Factory{}
	f.Name, f.Generation = name, gen
	return f
}

func repoObj(name, repository string, gen int64) v1alpha1.Repository {
	r := v1alpha1.Repository{Spec: v1alpha1.RepositorySpec{Repository: repository}}
	r.Name, r.Generation = name, gen
	return r
}

func policyObj(name string, gen int64) v1alpha1.MergePolicy {
	p := v1alpha1.MergePolicy{}
	p.Name, p.Generation = name, gen
	return p
}

func served(kind resources.Kind, name string) configsource.Outcome {
	return configsource.Outcome{Kind: kind, Name: name, Repository: repo, Accepted: true, ObservedGeneration: 3, ServedGeneration: 3}
}

func cond(t *testing.T, cs []metav1.Condition, typ string) metav1.Condition {
	t.Helper()
	c := apimeta.FindStatusCondition(cs, typ)
	if c == nil {
		t.Fatalf("no %s condition in %+v", typ, cs)
	}
	return *c.DeepCopy()
}

func want(t *testing.T, cs []metav1.Condition, typ string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()
	c := cond(t, cs, typ)
	if c.Status != status || c.Reason != reason {
		t.Fatalf("%s = %s/%s (%q), want %s/%s", typ, c.Status, c.Reason, c.Message, status, reason)
	}
	return c
}

func TestAccepted(t *testing.T) {
	tests := []struct {
		name    string
		outcome *configsource.Outcome
		status  metav1.ConditionStatus
		reason  string
		message string
	}{
		{"served", &configsource.Outcome{Kind: resources.KindRepository, Name: "r", Accepted: true}, metav1.ConditionTrue, "Served", ""},
		{"refused, serving an older generation", &configsource.Outcome{Kind: resources.KindRepository, Name: "r", Reason: v1alpha1.ReasonInvalid, Message: "bad", ServedGeneration: 2}, metav1.ConditionFalse, v1alpha1.ReasonInvalid, "bad; serving generation 2"},
		{"refused, never served", &configsource.Outcome{Kind: resources.KindRepository, Name: "r", Reason: v1alpha1.ReasonDefinedTwice, Message: "twice"}, metav1.ConditionFalse, v1alpha1.ReasonDefinedTwice, "twice"},
		{"no outcome", nil, metav1.ConditionUnknown, "Pending", "the factory has not looked at this object yet"},
		{"another kind with the same name", &configsource.Outcome{Kind: resources.KindFactory, Name: "r", Accepted: true}, metav1.ConditionUnknown, "Pending", "the factory has not looked at this object yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Inputs{Config: cfg()}
			if tt.outcome != nil {
				in.Outcomes = []configsource.Outcome{*tt.outcome}
			}
			got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 5)}, nil, now).Repositories["r"]
			c := want(t, got.Conditions, v1alpha1.ConditionAccepted, tt.status, tt.reason)
			if c.Message != tt.message {
				t.Errorf("message %q, want %q", c.Message, tt.message)
			}
			wantGen := int64(5) // no outcome: the object's generation
			if tt.outcome != nil && tt.outcome.Kind == resources.KindRepository {
				wantGen = tt.outcome.ObservedGeneration
			}
			if c.ObservedGeneration != wantGen || got.ObservedGeneration != wantGen {
				t.Errorf("observedGeneration condition %d status %d, want %d", c.ObservedGeneration, got.ObservedGeneration, wantGen)
			}
			if !c.LastTransitionTime.Equal(&now) {
				t.Errorf("lastTransitionTime %v, want now", c.LastTransitionTime)
			}
		})
	}
}

func TestAcceptedEveryKind(t *testing.T) {
	in := Inputs{Config: cfg(), Outcomes: []configsource.Outcome{
		{Kind: resources.KindFactory, Name: "f", Accepted: true},
		{Kind: resources.KindMergePolicy, Name: "p", Reason: v1alpha1.ReasonMissingRef, Message: "no repo"},
	}}
	got := Desired(in, []v1alpha1.Factory{obj("f", 1)}, nil, []v1alpha1.MergePolicy{policyObj("p", 1)}, now)
	want(t, got.Factories["f"].Conditions, v1alpha1.ConditionAccepted, metav1.ConditionTrue, "Served")
	want(t, got.MergePolicies["p"].Conditions, v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonMissingRef)
}

func TestRepository(t *testing.T) {
	boom := errors.New("no installation")
	tests := []struct {
		name   string
		cfg    *config.Config
		wire   error
		conn   func(string) (metav1.ConditionStatus, string)
		status metav1.ConditionStatus
		reason string
		msg    string
	}{
		{"served and healthy", cfg(config.Repository{Name: repo}), nil, func(string) (metav1.ConditionStatus, string) { return metav1.ConditionTrue, "" }, metav1.ConditionTrue, "Healthy", ""},
		{"served and unhealthy", cfg(config.Repository{Name: repo}), nil, func(string) (metav1.ConditionStatus, string) { return metav1.ConditionFalse, "rate limited" }, metav1.ConditionFalse, "Unhealthy", "rate limited"},
		{"served, name differs in case", cfg(config.Repository{Name: "Acme/App"}), nil, func(string) (metav1.ConditionStatus, string) { return metav1.ConditionTrue, "" }, metav1.ConditionTrue, "Healthy", ""},
		{"not served, wiring failed", cfg(), boom, nil, metav1.ConditionFalse, "WiringFailed", "no installation"},
		{"not served", cfg(), nil, func(string) (metav1.ConditionStatus, string) {
			t.Fatal("Connected asked for an unserved repository")
			return metav1.ConditionTrue, ""
		}, metav1.ConditionFalse, "NotServed", ""},
		{"served, wiring error ignored", cfg(config.Repository{Name: repo}), boom, func(string) (metav1.ConditionStatus, string) { return metav1.ConditionTrue, "" }, metav1.ConditionTrue, "Healthy", ""},
		{"served, never checked", cfg(config.Repository{Name: repo}), nil, func(string) (metav1.ConditionStatus, string) { return metav1.ConditionUnknown, "" }, metav1.ConditionUnknown, "NotChecked", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Inputs{Config: tt.cfg, Connected: tt.conn, WireErr: func(string) error { return tt.wire }}
			got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 1)}, nil, now).Repositories["r"]
			c := want(t, got.Conditions, "Connected", tt.status, tt.reason)
			if c.Message != tt.msg {
				t.Errorf("message %q, want %q", c.Message, tt.msg)
			}
		})
	}
}

func decisionsAt(at time.Time) func(string) map[string]decision.Last {
	return func(string) map[string]decision.Last {
		return map[string]decision.Last{"docs": {Verdict: "arm", Reason: "all gates passed", At: at}}
	}
}

// repoIn serves the Repository r, bound to docs, with c.
func repoIn(c *config.Config) Inputs {
	o := served(resources.KindRepository, "r")
	o.Bindings = []resources.BindingOutcome{{Repository: "r", Name: "docs", Mode: config.ModeLive, PolicyGeneration: 2, Accepted: true, Served: true}}
	return Inputs{Config: c, Outcomes: []configsource.Outcome{o}}
}

func TestRepositoryAuthorityAndClasses(t *testing.T) {
	classes := map[string]config.Class{"zeta": {}, "docs": {Live: true}, "alpha": {Shadow: true}}
	at := now.Add(-time.Hour)
	t.Run("central", func(t *testing.T) {
		in := repoIn(cfg(config.Repository{Name: repo, Classes: classes}))
		in.Decisions = decisionsAt(at)
		got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]
		if got.Authority != "central" || got.ClassCount != 1 || got.LiveCount != 1 {
			t.Errorf("got %+v", got)
		}
		d := got.Classes[0]
		if d.Name != "docs" || d.Mode != config.ModeLive || d.PolicyGeneration != 2 || d.LastDecision != "arm:all gates passed" ||
			d.LastDecisionAt == nil || !d.LastDecisionAt.Time.Equal(at) {
			t.Errorf("docs %+v", d)
		}
	})
	t.Run("cut over", func(t *testing.T) {
		c := cfg(config.Repository{Name: repo, ClassAuthority: config.ClassAuthorityRepository})
		c.PolicyManagement.RepositoryAuthority = true
		if got := Desired(repoIn(c), nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]; got.Authority != "repository" {
			t.Errorf("authority %q", got.Authority)
		}
	})
	t.Run("repository authority needs the central switch", func(t *testing.T) {
		c := cfg(config.Repository{Name: repo, ClassAuthority: config.ClassAuthorityRepository})
		if got := Desired(repoIn(c), nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]; got.Authority != "central" {
			t.Errorf("authority %q", got.Authority)
		}
	})
	t.Run("decision text is cut at 63", func(t *testing.T) {
		in := repoIn(cfg(config.Repository{Name: repo, Classes: classes}))
		in.Decisions = func(string) map[string]decision.Last {
			return map[string]decision.Last{"docs": {Verdict: "hold", Reason: strings.Repeat("é", 100), At: at}}
		}
		got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]
		if l := []rune(got.Classes[0].LastDecision); len(l) != 63 || !strings.HasPrefix(string(l), "hold:é") {
			t.Errorf("lastDecision %q (%d)", string(l), len(l))
		}
	})
	t.Run("not served has no authority, no classes", func(t *testing.T) {
		got := Desired(Inputs{Config: cfg()}, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]
		if got.Authority != "" || got.Classes != nil {
			t.Errorf("got %+v", got)
		}
	})
}

func TestRepositoryFileConditions(t *testing.T) {
	good := mergepolicy.Snapshot{HasClasses: true}
	tests := []struct {
		name                 string
		snap                 func(string) (mergepolicy.Snapshot, bool)
		validS, syncS        metav1.ConditionStatus
		validR, syncR        string
		syncMsgHas, validMsg string
	}{
		{"valid and in sync", func(string) (mergepolicy.Snapshot, bool) { return good, true }, metav1.ConditionTrue, metav1.ConditionTrue, "Valid", "InSync", "", ""},
		{"unreadable", func(string) (mergepolicy.Snapshot, bool) {
			return mergepolicy.Snapshot{Err: errors.New("404"), HasClasses: true}, true
		}, metav1.ConditionFalse, metav1.ConditionFalse, "Invalid", "FileInvalid", "", "404"},
		// A fence-only file (factory: alone) under central authority is what a central-bound
		// repository's file is: the central classes decide, so it is valid and nothing diverges.
		{"fence only, central", func(string) (mergepolicy.Snapshot, bool) { return mergepolicy.Snapshot{}, true }, metav1.ConditionTrue, metav1.ConditionTrue, "FenceOnly", "FenceOnly", "", "the central classes decide"},
		{"diverges", func(string) (mergepolicy.Snapshot, bool) {
			return mergepolicy.Snapshot{HasClasses: true, Divergences: []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7"}}, true
		}, metav1.ConditionTrue, metav1.ConditionFalse, "Valid", "Diverges", "d5", ""},
		{"a binding refused", func(string) (mergepolicy.Snapshot, bool) {
			f := &mergepolicy.File{MergePolicies: []config.Binding{{Name: "docs-links"}, {Name: "foo"}, {Name: "revert"}}}
			return mergepolicy.Snapshot{HasClasses: true, File: f,
				Refused: map[string]config.BindingRefusal{"foo": {Reason: "MissingRef", Message: "the factory serves no MergePolicy foo"}}}, true
		}, metav1.ConditionTrue, metav1.ConditionTrue, "Valid", "InSync", "", "binding foo: MissingRef: the factory serves no MergePolicy foo"},
		{"not read yet", func(string) (mergepolicy.Snapshot, bool) { return mergepolicy.Snapshot{}, false }, metav1.ConditionUnknown, metav1.ConditionUnknown, "NotRead", "NotRead", "", ""},
		{"read is off", nil, metav1.ConditionUnknown, metav1.ConditionUnknown, "PolicyReadOff", "PolicyReadOff", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := repoIn(cfg(config.Repository{Name: repo}))
			in.Snapshot = tt.snap
			got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]
			v := want(t, got.Conditions, "RepositoryFileValid", tt.validS, tt.validR)
			s := want(t, got.Conditions, "InSync", tt.syncS, tt.syncR)
			if !strings.Contains(v.Message, tt.validMsg) || !strings.Contains(s.Message, tt.syncMsgHas) {
				t.Errorf("messages %q / %q", v.Message, s.Message)
			}
			if tt.name == "diverges" && (strings.Contains(s.Message, "d6") || !strings.Contains(s.Message, "d5")) {
				t.Errorf("message names up to 5 divergences: %q", s.Message)
			}
		})
	}
}

// Under repository authority the file is the policy, so a file without classes is still invalid.
func TestFenceOnlyFileUnderRepositoryAuthority(t *testing.T) {
	c := cfg(config.Repository{Name: repo, ClassAuthority: config.ClassAuthorityRepository})
	c.PolicyManagement.RepositoryAuthority = true
	in := repoIn(c)
	in.Snapshot = func(string) (mergepolicy.Snapshot, bool) { return mergepolicy.Snapshot{}, true }
	got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]
	if v := want(t, got.Conditions, "RepositoryFileValid", metav1.ConditionFalse, "Invalid"); !strings.Contains(v.Message, "no classes") {
		t.Errorf("message %q", v.Message)
	}
	want(t, got.Conditions, "InSync", metav1.ConditionFalse, "FileInvalid")
}

// A refused Repository whose last good still serves keeps the status of what is served.
func TestRepositoryRefusedStillComputed(t *testing.T) {
	in := repoIn(cfg(config.Repository{Name: repo, Classes: map[string]config.Class{"docs": {Live: true}}}))
	in.Outcomes[0].Accepted, in.Outcomes[0].Reason, in.Outcomes[0].Message, in.Outcomes[0].ServedGeneration = false, v1alpha1.ReasonInvalid, "x", 2
	got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, now).Repositories["r"]
	want(t, got.Conditions, v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonInvalid)
	if got.ClassCount != 1 || got.LiveCount != 1 || got.Authority != "central" {
		t.Errorf("last good version not shown: %+v", got)
	}
}

func TestFactory(t *testing.T) {
	fresh := configsource.Synced{At: now.Add(-14 * time.Minute)}
	acc := []configsource.Outcome{{Kind: resources.KindFactory, Name: "f", Accepted: true, ObservedGeneration: 2}}
	tests := []struct {
		name   string
		in     Inputs
		status metav1.ConditionStatus
		reason string
		msg    string
	}{
		{"ready", Inputs{Outcomes: acc, Synced: fresh, GitHubUp: true}, metav1.ConditionTrue, "Ready", ""},
		{"not accepted", Inputs{Synced: fresh, GitHubUp: true}, metav1.ConditionFalse, "NotAccepted", "the Factory is not accepted"},
		// #448: a refused edit does not take a serving factory down; Ready names what is served.
		{"last good serving", Inputs{Outcomes: []configsource.Outcome{{Kind: resources.KindFactory, Name: "f", ObservedGeneration: 2, ServedGeneration: 1,
			Reason: v1alpha1.ReasonInvalid, Message: "x"}}, Synced: fresh, GitHubUp: true}, metav1.ConditionTrue, "ServingLastGood", "serving generation 1; generation 2 refused"},
		{"last good, github down", Inputs{Outcomes: []configsource.Outcome{{Kind: resources.KindFactory, Name: "f", ObservedGeneration: 2, ServedGeneration: 1,
			Reason: v1alpha1.ReasonInvalid, Message: "x"}}, Synced: fresh}, metav1.ConditionFalse, "GitHubDown", "a served repository's GitHub connection is unhealthy"},
		{"refused, nothing served", Inputs{Outcomes: []configsource.Outcome{{Kind: resources.KindFactory, Name: "f", ObservedGeneration: 2,
			Reason: v1alpha1.ReasonInvalid, Message: "x"}}, Synced: fresh, GitHubUp: true}, metav1.ConditionFalse, "NotAccepted", "the Factory is not accepted"},
		{"github down", Inputs{Outcomes: acc, Synced: fresh}, metav1.ConditionFalse, "GitHubDown", "a served repository's GitHub connection is unhealthy"},
		{"sync failing", Inputs{Outcomes: acc, GitHubUp: true, Synced: configsource.Synced{At: now.Time, Err: errors.New("list denied")}}, metav1.ConditionFalse, "SyncFailing", "list denied"},
		{"sync stale", Inputs{Outcomes: acc, GitHubUp: true, Synced: configsource.Synced{At: now.Add(-16 * time.Minute)}}, metav1.ConditionFalse, "SyncFailing", "no sync since 2026-11-10T11:44:00Z"},
		{"never synced", Inputs{Outcomes: acc, GitHubUp: true}, metav1.ConditionFalse, "SyncFailing", "no sync yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Config = cfg(config.Repository{Name: repo}, config.Repository{Name: "acme/b"})
			tt.in.Stopped = true
			got := Desired(tt.in, []v1alpha1.Factory{obj("f", 2)}, nil, nil, now).Factories["f"]
			c := want(t, got.Conditions, "Ready", tt.status, tt.reason)
			if c.Message != tt.msg {
				t.Errorf("message %q, want %q", c.Message, tt.msg)
			}
			if got.Repositories != 2 || !got.Stopped || got.ObservedGeneration != 2 {
				t.Errorf("status %+v", got)
			}
		})
	}
}

// An outcome older than the object (the Source observed generation 3, the object is at 5): every
// condition and every status level says 3, the generation the status is about.
func TestOneObservedGenerationPerObject(t *testing.T) {
	in := Inputs{Config: cfg(config.Repository{Name: repo, Classes: map[string]config.Class{"docs": {Live: true}}}), GitHubUp: true,
		Synced:    configsource.Synced{At: now.Time},
		Snapshot:  func(string) (mergepolicy.Snapshot, bool) { return mergepolicy.Snapshot{HasClasses: true}, true },
		Connected: func(string) (metav1.ConditionStatus, string) { return metav1.ConditionTrue, "" },
		Outcomes:  []configsource.Outcome{served(resources.KindFactory, "f"), served(resources.KindRepository, "r"), served(resources.KindMergePolicy, "p")}}
	got := Desired(in, []v1alpha1.Factory{obj("f", 5)}, []v1alpha1.Repository{repoObj("r", repo, 5)}, []v1alpha1.MergePolicy{policyObj("p", 5)}, now)
	check := func(kind string, gen int64, cs []metav1.Condition, types ...string) {
		t.Helper()
		if gen != 3 {
			t.Errorf("%s status observedGeneration %d, want 3", kind, gen)
		}
		for _, typ := range types {
			if c := cond(t, cs, typ); c.ObservedGeneration != 3 {
				t.Errorf("%s %s observedGeneration %d, want 3", kind, typ, c.ObservedGeneration)
			}
		}
	}
	f, r, p := got.Factories["f"], got.Repositories["r"], got.MergePolicies["p"]
	check("Factory", f.ObservedGeneration, f.Conditions, v1alpha1.ConditionAccepted, ConditionReady)
	check("Repository", r.ObservedGeneration, r.Conditions, v1alpha1.ConditionAccepted, ConditionConnected, ConditionRepositoryFileValid, ConditionInSync)
	check("MergePolicy", p.ObservedGeneration, p.Conditions, v1alpha1.ConditionAccepted)
}

// v4 D3, step 7 R1: PrivateCode on a served Repository. Private, internal or unread code without
// privateCode.acknowledged is False with D3's sentence; work continues (no other condition moves).
func TestPrivateCode(t *testing.T) {
	acked := config.Repository{Name: repo, PrivateCode: &config.PrivateCode{Acknowledged: true}}
	for name, tc := range map[string]struct {
		served     *config.Repository
		visibility string
		status     metav1.ConditionStatus
		reason     string
		message    string
	}{
		"not served":               {visibility: "private", status: metav1.ConditionUnknown, reason: "NotServed"},
		"not read yet":             {served: &config.Repository{Name: repo}, status: metav1.ConditionUnknown, reason: "NotChecked"},
		"public":                   {served: &config.Repository{Name: repo}, visibility: "public", status: metav1.ConditionTrue, reason: "Public"},
		"private":                  {served: &config.Repository{Name: repo}, visibility: "private", status: metav1.ConditionFalse, reason: v1alpha1.ReasonUnacknowledged, message: config.PrivateCodeSentence},
		"internal":                 {served: &config.Repository{Name: repo}, visibility: "internal", status: metav1.ConditionFalse, reason: v1alpha1.ReasonUnacknowledged, message: config.PrivateCodeSentence},
		"unread counts as private": {served: &config.Repository{Name: repo}, visibility: VisibilityUnknown, status: metav1.ConditionFalse, reason: v1alpha1.ReasonUnacknowledged, message: "its visibility could not be read"},
		"private, acknowledged":    {served: &acked, visibility: "private", status: metav1.ConditionTrue, reason: "Acknowledged"},
		"unread, acknowledged":     {served: &acked, visibility: VisibilityUnknown, status: metav1.ConditionTrue, reason: "Acknowledged"},
		"public, acknowledged":     {served: &acked, visibility: "public", status: metav1.ConditionTrue, reason: "Public"},
	} {
		t.Run(name, func(t *testing.T) {
			in := Inputs{Config: cfg(), Visibility: func(r string) string {
				if r != repo {
					t.Fatalf("visibility of %s", r)
				}
				return tc.visibility
			}}
			if tc.served != nil {
				in.Config = cfg(*tc.served)
			}
			st := Desired(in, nil, []v1alpha1.Repository{repoObj("app", repo, 1)}, nil, now).Repositories["app"]
			c := want(t, st.Conditions, v1alpha1.ConditionPrivateCode, tc.status, tc.reason)
			if !strings.Contains(c.Message, tc.message) {
				t.Fatalf("message %q, want %q", c.Message, tc.message)
			}
		})
	}
	st := Desired(Inputs{Config: cfg(config.Repository{Name: repo})}, nil, []v1alpha1.Repository{repoObj("app", repo, 1)}, nil, now).Repositories["app"]
	want(t, st.Conditions, v1alpha1.ConditionPrivateCode, metav1.ConditionUnknown, "NotChecked")
}
