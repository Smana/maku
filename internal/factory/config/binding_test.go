// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// deadLinksPolicy is the spec's dead-links MergePolicy.
func deadLinksPolicy() PolicySpec {
	return PolicySpec{
		MergeClass: MergeClass{
			Description: "Retargets dead external links a detector found, and nothing else.",
			ModifyOnly:  true, MaxModifiedLines: 20, DiffShape: "links-only", MaxPerDay: 1,
			ProtectedFiles: []string{`^docs/(superpowers|specs)/`},
			Oracle:         &Oracle{Probe: "dead-links", MaxFindings: 10},
			Monitor:        &Monitor{OracleRuns: 2, FollowUpWindow: Duration{168 * time.Hour}},
		},
	}
}

// deadLinksBinding is ai-native-ref's binding of it.
func deadLinksBinding() Binding {
	return Binding{Name: "dead-links", Mode: ModeShadow,
		AllowedFiles: []string{`^(docs|website/content)/.+\.md$`}, HoldoutChecks: []string{"External links 🔗"},
		OracleWorkflow: "dead-links.yaml"}
}

func adoptPolicy(shape string) PolicySpec {
	return PolicySpec{MergeClass: MergeClass{DiffShape: shape, ModifyOnly: true,
		Author: &Author{Adopt: &Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}}}}
}

var testProfiles = map[string]HarnessProfile{"solo": {Template: "solo", Tier: "standard"}, "tf": {Checks: true}}

// B4: one row per field kind. Each row starts from dead-links (or names its own policy), changes
// one thing, and states the reason or the resolved class.
func TestResolve(t *testing.T) {
	for name, tc := range map[string]struct {
		class  string
		policy func(*PolicySpec)
		bind   func(*Binding)
		ctx    PolicyContext
		merge  Merge
		reason string
		msg    string
		check  func(*testing.T, Class)
	}{
		// Mode.
		"live":            {bind: func(b *Binding) { b.Mode = ModeLive }, check: func(t *testing.T, c Class) { want(t, c.Live && !c.Shadow, "live") }},
		"shadow":          {check: func(t *testing.T, c Class) { want(t, c.Shadow && !c.Live, "shadow") }},
		"disabled":        {bind: func(b *Binding) { b.Mode = ModeDisabled }, check: func(t *testing.T, c Class) { want(t, !c.Live && !c.Shadow, "disabled is neither (Q7)") }},
		"an unknown mode": {bind: func(b *Binding) { b.Mode = "on" }, reason: ReasonInvalid, msg: `mode "on" is live, shadow or disabled`},
		"review refused": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{} }, bind: func(b *Binding) { *b = Binding{Name: "review", Mode: ModeShadow} },
			reason: ReasonInvalid, msg: "review is not a policy"},
		// Meaning: the policy's.
		"meaning": {check: func(t *testing.T, c Class) {
			want(t, c.Description == deadLinksPolicy().Description && c.DiffShape == "links-only" && c.ModifyOnly &&
				c.Oracle.Probe == "dead-links" && c.Oracle.MaxFindings == 10 && c.Monitor.OracleRuns == 2 && c.Monitor.FollowUpWindow.Duration == 168*time.Hour, "the policy's meaning")
		}},
		"adopt is the policy's": {class: "deps-patch", policy: func(p *PolicySpec) { *p = adoptPolicy("semver-patch") }, bind: func(b *Binding) {
			*b = Binding{Name: "deps-patch", Mode: ModeLive, AllowedFiles: []string{"^go\\.mod$"}}
		},
			check: func(t *testing.T, c Class) {
				want(t, c.Adopted() && c.Author.Adopt.Logins[0] == "renovate[bot]" && c.Live && c.AllowedFiles[0] == "^go\\.mod$", "adopt")
			}},
		// Fill once.
		"the binding fills allowedFiles": {check: func(t *testing.T, c Class) {
			want(t, slices.Equal(c.AllowedFiles, deadLinksBinding().AllowedFiles), "allowedFiles from the binding")
		}},
		"the policy fills allowedFiles": {policy: func(p *PolicySpec) {
			p.AllowedFiles = []string{"^docs/.+$"}
		},
			bind: func(b *Binding) { b.AllowedFiles = nil }, check: func(t *testing.T, c Class) {
				want(t, slices.Equal(c.AllowedFiles, []string{"^docs/.+$"}), "allowedFiles from the policy")
			}},
		// MR3: set on both, the binding's allowedFiles narrows the policy's instead of refusing.
		"allowedFiles twice narrows": {policy: func(p *PolicySpec) {
			p.AllowedFiles = []string{"^docs/.+$"}
		},
			check: func(t *testing.T, c Class) {
				want(t, slices.Equal(c.AllowedFiles, []string{"^docs/.+$"}) && slices.Equal(c.NarrowedFiles, deadLinksBinding().AllowedFiles), "the binding narrows")
			}},
		"requiredLabel twice": {class: "kb", policy: func(p *PolicySpec) { *p = PolicySpec{MergeClass: MergeClass{RequiredLabel: "a"}} }, bind: func(b *Binding) { *b = Binding{Name: "kb", Mode: ModeDisabled, RequiredLabel: "b"} },
			reason: ReasonParameterTwice, msg: "requiredLabel is set by MergePolicy kb and by the binding"},
		"allowedFiles missing":            {bind: func(b *Binding) { b.AllowedFiles = nil }, reason: ReasonMissingParameter, msg: "an oracle policy needs allowedFiles"},
		"oracleWorkflow missing":          {bind: func(b *Binding) { b.OracleWorkflow = "" }, reason: ReasonMissingParameter, msg: "an oracle policy needs oracleWorkflow on the binding"},
		"oracleWorkflow fills the oracle": {check: func(t *testing.T, c Class) { want(t, c.Oracle.Workflow == "dead-links.yaml", "oracle.workflow") }},
		"oracleWorkflow without an oracle": {class: "docs", policy: func(p *PolicySpec) { *p = PolicySpec{} }, bind: func(b *Binding) { *b = Binding{Name: "docs", Mode: ModeDisabled, OracleWorkflow: "x.yaml"} },
			reason: ReasonInvalid, msg: "oracleWorkflow needs an oracle on MergePolicy docs"},
		// Additive.
		"additive lists": {policy: func(p *PolicySpec) {
			p.HoldoutChecks = []string{"lint"}
			p.RequiresApproval = []ApprovalLeaf{{ChangedFiles: []string{"^infra/"}}}
		}, bind: func(b *Binding) {
			b.HoldoutChecks = []string{"External links 🔗", "lint"}
			b.ProtectedFiles = []string{"^website/content/docs/decisions/"}
			b.RequiresApproval = []ApprovalLeaf{{ChangedFiles: []string{"^infra/"}}, {ChangedFiles: []string{"^docs/adr/"}}}
		}, check: func(t *testing.T, c Class) {
			want(t, slices.Equal(c.HoldoutChecks, []string{"lint", "External links 🔗"}), "holdoutChecks: the policy's, then the binding's new ones")
			want(t, slices.Equal(c.ProtectedFiles, []string{`^docs/(superpowers|specs)/`, "^website/content/docs/decisions/"}), "protectedFiles")
			want(t, len(c.RequiresApproval) == 2 && c.RequiresApproval[1].ChangedFiles[0] == "^docs/adr/", "requiresApproval")
		}},
		"holdoutChecks missing":       {bind: func(b *Binding) { b.HoldoutChecks = nil }, reason: ReasonMissingParameter, msg: "an oracle policy needs holdoutChecks"},
		"a binding of another policy": {bind: func(b *Binding) { b.Name = "docs-links" }, reason: ReasonInvalid, msg: `class dead-links: the binding names MergePolicy "docs-links"`},
		// Caps.
		"a cap under the policy's": {bind: func(b *Binding) { b.MaxPerDay = 0; b.MaxModifiedLines = 20 }, check: func(t *testing.T, c Class) { want(t, c.MaxModifiedLines == 20 && c.MaxPerDay == 1, "caps") }},
		"a cap lowered":            {policy: func(p *PolicySpec) { p.MaxPatchFiles = 10 }, bind: func(b *Binding) { b.MaxPatchFiles = 5 }, check: func(t *testing.T, c Class) { want(t, c.MaxPatchFiles == 5, "5 under 10") }},
		"a cap over the policy's": {policy: func(p *PolicySpec) { p.MaxPatchFiles = 10 }, bind: func(b *Binding) { b.MaxPatchFiles = 12 },
			reason: ReasonOverPolicy, msg: "maxPatchFiles 12 is over MergePolicy dead-links's 10"},
		"no policy cap": {bind: func(b *Binding) { b.MaxPatchFiles = 5 }, check: func(t *testing.T, c Class) { want(t, c.MaxPatchFiles == 5, "policy 0, binding 5") }},
		"no cap at all": {check: func(t *testing.T, c Class) { want(t, c.MaxPatchFiles == 0, "both 0") }},
		// Repository fact.
		"flux without a source": {policy: func(p *PolicySpec) { p.Monitor.Flux = &FluxWatch{Within: Duration{30 * time.Minute}} },
			reason: ReasonMissingParameter, msg: "monitor.flux needs Repository.spec.merge.fluxSource"},
		"flux with a source": {policy: func(p *PolicySpec) { p.Monitor.Flux = &FluxWatch{Within: Duration{30 * time.Minute}} }, merge: Merge{FluxSource: "flux-system/flux-system"},
			check: func(t *testing.T, c Class) {
				want(t, c.Monitor.Flux.Source == "flux-system/flux-system", "the repository's source")
			}},
		// Guardrails (B14).
		"guardrails add and cap": {ctx: PolicyContext{Guardrails: MergeGuardrails{ProtectedFiles: []string{`^\.github/`}, MandatoryChecks: []string{"Security scanning 🔒"}, MaxPatchFiles: 3}},
			check: func(t *testing.T, c Class) {
				want(t, slices.Contains(c.ProtectedFiles, `^\.github/`) && slices.Contains(c.HoldoutChecks, "Security scanning 🔒") && c.MaxPatchFiles == 3, "guardrails")
			}},
		"a cap over the guardrail": {ctx: PolicyContext{Guardrails: MergeGuardrails{MaxPerDay: 1}}, policy: func(p *PolicySpec) { p.MaxPerDay = 0 }, bind: func(b *Binding) { b.MaxPerDay = 2 },
			reason: ReasonGuardrailMismatch, msg: "maxPerDay 2 is over the guardrail's 1"},
		// Invalid: the resolved class fails the class rules.
		"semver-minor live": {class: "deps-minor", policy: func(p *PolicySpec) { *p = adoptPolicy("semver-minor") }, bind: func(b *Binding) { *b = Binding{Name: "deps-minor", Mode: ModeLive} },
			reason: ReasonInvalid, msg: "class deps-minor: semver-minor is shadow only (M2)"},
		// MR6, judged per binding (B9): an evaluated class without allowedFiles or a shape.
		"live without allowedFiles and no policy-bot": {class: "x", policy: func(p *PolicySpec) { *p = PolicySpec{} }, bind: func(b *Binding) { *b = Binding{Name: "x", Mode: ModeLive} },
			merge: Merge{PolicyBot: new(false)}, reason: ReasonUnbounded, msg: "class x: an evaluated policy needs allowedFiles or a diffShape"},
	} {
		t.Run(name, func(t *testing.T) {
			class, p, b := "dead-links", deadLinksPolicy(), deadLinksBinding()
			if tc.class != "" {
				class = tc.class
			}
			if tc.policy != nil {
				tc.policy(&p)
			}
			if tc.bind != nil {
				tc.bind(&b)
			}
			if tc.ctx.HarnessProfiles == nil {
				tc.ctx.HarnessProfiles = testProfiles
			}
			cl, refused := Resolve(class, p, b, tc.ctx, tc.merge)
			if tc.reason != "" {
				if refused == nil || refused.Reason != tc.reason || !strings.Contains(refused.Message, tc.msg) {
					t.Fatalf("got %+v, want %s: %q", refused, tc.reason, tc.msg)
				}
				return
			}
			if refused != nil {
				t.Fatalf("refused: %+v", refused)
			}
			if tc.check != nil {
				tc.check(t, cl)
			}
		})
	}
}

// Resolve copies before it appends or fills: two consumers of one policy never share a slice
// or a pointer with the policy or with each other.
func TestResolveNeverAliasesThePolicy(t *testing.T) {
	p := deadLinksPolicy()
	p.HoldoutChecks = make([]string, 1, 8) // spare capacity: an append in place would show
	p.HoldoutChecks[0] = "lint"
	p.Monitor.Flux = &FluxWatch{Within: Duration{30 * time.Minute}}
	a, b := deadLinksBinding(), deadLinksBinding()
	b.HoldoutChecks = []string{"other"}
	m := Merge{FluxSource: "flux-system/a"}
	ca, ra := Resolve("dead-links", p, a, PolicyContext{}, m)
	cb, rb := Resolve("dead-links", p, b, PolicyContext{}, Merge{FluxSource: "flux-system/b"})
	if ra != nil || rb != nil {
		t.Fatal(ra, rb)
	}
	if !slices.Equal(ca.HoldoutChecks, []string{"lint", "External links 🔗"}) || !slices.Equal(cb.HoldoutChecks, []string{"lint", "other"}) {
		t.Fatalf("holdouts %q and %q", ca.HoldoutChecks, cb.HoldoutChecks)
	}
	if ca.Monitor.Flux.Source != "flux-system/a" || cb.Monitor.Flux.Source != "flux-system/b" || p.Monitor.Flux.Source != "" ||
		ca.Oracle.Workflow != "dead-links.yaml" || p.Oracle.Workflow != "" {
		t.Fatal("a resolved class shares a pointer with the policy or with another consumer")
	}
	if len(p.HoldoutChecks) != 1 || len(p.AllowedFiles) != 0 {
		t.Fatalf("the policy changed: %+v", p)
	}
}

// B9: a policy is Accepted when it is valid alone, a placeholder in each field a binding may fill.
func TestValidatePolicy(t *testing.T) {
	ctx := PolicyContext{HarnessProfiles: testProfiles}
	for name, tc := range map[string]struct {
		class  string
		policy func(*PolicySpec)
		msg    string
	}{
		"dead-links is valid alone": {},
		"review is not a policy": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{} },
			msg: "review is not a policy"},
		"a flux monitor":         {policy: func(p *PolicySpec) { p.Monitor.Flux = &FluxWatch{Within: Duration{30 * time.Minute}} }},
		"a workflow on a policy": {policy: func(p *PolicySpec) { p.Oracle.Workflow = "x.yaml" }, msg: "oracle.workflow is the repository's: set oracleWorkflow on the binding"},
		"a source on a policy": {policy: func(p *PolicySpec) { p.Monitor.Flux = &FluxWatch{Source: "a/b", Within: Duration{30 * time.Minute}} },
			msg: "the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)"},
	} {
		t.Run(name, func(t *testing.T) {
			class, p := "dead-links", deadLinksPolicy()
			if tc.class != "" {
				class = tc.class
			}
			if tc.policy != nil {
				tc.policy(&p)
			}
			got := ValidatePolicy(class, p, ctx)
			if tc.msg == "" {
				if len(got) > 0 {
					t.Fatalf("refused: %q", got)
				}
				return
			}
			if !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, tc.msg) }) {
				t.Fatalf("got %q, want %q", got, tc.msg)
			}
		})
	}
}

// ApplyGuardrails adds protected paths and mandatory checks once, inherits unset caps and clamps
// a policy cap above the guardrail. Resolve still refuses a binding's explicit raise.
func TestApplyGuardrails(t *testing.T) {
	g := MergeGuardrails{ProtectedFiles: []string{"^a/"}, MandatoryChecks: []string{"c"}, MaxPerDay: 2}
	cl := Class{ProtectedFiles: []string{"^a/"}, MaxPerDay: 0}
	if over := ApplyGuardrails(&cl, g); over != "" {
		t.Fatal(over)
	}
	if !slices.Equal(cl.ProtectedFiles, []string{"^a/"}) || !slices.Equal(cl.HoldoutChecks, []string{"c"}) || cl.MaxPerDay != 2 {
		t.Fatalf("%+v", cl)
	}
	cl = Class{MaxPerDay: 3}
	if over := ApplyGuardrails(&cl, g); over != "" || cl.MaxPerDay != 2 {
		t.Fatalf("over %q, class %+v; want the cap clamped to 2", over, cl)
	}
}

func want(t *testing.T, ok bool, what string) {
	t.Helper()
	if !ok {
		t.Fatalf("want %s", what)
	}
}

// Recheck reads today's repository facts: the Flux source is the current one, a removed source
// drops the class, and the class handed in is never changed.
func TestRecheck(t *testing.T) {
	cl := Class{Shadow: true, AllowedFiles: []string{`^x$`}, Monitor: &Monitor{FollowUpWindow: Duration{time.Hour}, Flux: &FluxWatch{Source: "flux-system/old", Within: Duration{30 * time.Minute}}}}
	got, refused := Recheck("x", cl, PolicyContext{}, Merge{FluxSource: "flux-system/new"})
	if refused != nil {
		t.Fatalf("refused: %+v", refused)
	}
	if got.Monitor.Flux.Source != "flux-system/new" || cl.Monitor.Flux.Source != "flux-system/old" {
		t.Fatalf("got %+v %+v, the input %q", got.Monitor.Flux, refused, cl.Monitor.Flux.Source)
	}
	if _, refused := Recheck("x", cl, PolicyContext{}, Merge{}); refused == nil || refused.Reason != ReasonMissingParameter {
		t.Fatalf("without a source: %+v", refused)
	}
}

// review is not a policy: neither Resolve nor Recheck accepts it.
func TestResolveRefusesReview(t *testing.T) {
	ctx := PolicyContext{Guardrails: MergeGuardrails{MaxPerDay: 2, MandatoryChecks: []string{"lint"}, ProtectedFiles: []string{"^x$"}}}
	if _, refusal := Resolve("review", PolicySpec{}, Binding{Name: "review", Mode: ModeDisabled}, ctx, Merge{}); refusal == nil || !strings.Contains(refusal.Message, "review is not a policy") {
		t.Fatalf("%+v", refusal)
	}
	if _, refusal := Recheck("review", Class{}, ctx, Merge{}); refusal == nil || !strings.Contains(refusal.Message, "review is not a policy") {
		t.Fatalf("%+v", refusal)
	}
}

// MR3: a binding's allowedFiles narrows a policy's that already has one: the policy's list stays,
// the binding's is NarrowedFiles, and a path must match both.
func TestBindingNarrowsAllowedFiles(t *testing.T) {
	p := PolicySpec{MergeClass: MergeClass{AllowedFiles: []string{`^.+\.mdx?$`}, MaxModifiedLines: 100}}
	b := Binding{Name: "docs", Mode: ModeShadow, AllowedFiles: []string{`^website/content/.+\.md$`}, MaxModifiedLines: 60}
	cl, refused := Resolve("docs", p, b, PolicyContext{}, Merge{})
	if refused != nil {
		t.Fatalf("refused %+v", *refused)
	}
	if !slices.Equal(cl.AllowedFiles, p.AllowedFiles) || !slices.Equal(cl.NarrowedFiles, b.AllowedFiles) || cl.MaxModifiedLines != 60 {
		t.Fatalf("%+v", cl)
	}
	if len(p.NarrowedFiles) > 0 {
		t.Fatal("Resolve aliased the policy")
	}
	// A binding narrowing nothing fills the policy's empty list, as before.
	cl, refused = Resolve("docs", PolicySpec{}, b, PolicyContext{}, Merge{})
	if refused != nil || !slices.Equal(cl.AllowedFiles, b.AllowedFiles) || cl.NarrowedFiles != nil {
		t.Fatalf("%+v %+v", cl, refused)
	}
	// NarrowedFiles is the resolution's: a policy may not set it.
	if problems := ValidatePolicy("docs", PolicySpec{MergeClass: MergeClass{NarrowedFiles: []string{`^x$`}}}, PolicyContext{}); len(problems) != 1 ||
		!strings.Contains(problems[0], "narrowedFiles is set by a binding's allowedFiles") {
		t.Fatalf("%v", problems)
	}
}

// A binding switches a policy off with disabled; off and unknown modes are refused (MR9).
func TestModeDisabled(t *testing.T) {
	cl, refused := Resolve("docs", PolicySpec{}, Binding{Name: "docs", Mode: ModeDisabled}, PolicyContext{}, Merge{})
	if refused != nil || cl.Live || cl.Shadow {
		t.Errorf("disabled: %+v %+v", cl, refused)
	}
	for _, mode := range []string{"off", "paused"} {
		_, refused := Resolve("docs", PolicySpec{}, Binding{Name: "docs", Mode: mode}, PolicyContext{}, Merge{})
		if refused == nil || refused.Reason != ReasonInvalid || !strings.Contains(refused.Message, `is live, shadow or disabled`) {
			t.Fatalf("%s: %+v", mode, refused)
		}
	}
}
