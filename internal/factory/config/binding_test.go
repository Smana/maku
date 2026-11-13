// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// deadLinksPolicy is the spec's dead-links MergePolicy, with requires per plan PR3.
func deadLinksPolicy() PolicySpec {
	return PolicySpec{
		Class: Class{
			Description: "Retargets dead external links a detector found, and nothing else.",
			ModifyOnly:  true, MaxModifiedLines: 20, DiffShape: "links-only", MaxPerDay: 1,
			ProtectedFiles: []string{`^docs/(superpowers|specs)/`},
			Oracle:         &Oracle{Probe: "dead-links", MaxFindings: 10},
			Monitor:        &Monitor{OracleRuns: 2, FollowUpWindow: Duration{168 * time.Hour}},
		},
		Requires: []string{"allowedFiles", "holdoutChecks", "oracleWorkflow"},
	}
}

// deadLinksBinding is ai-native-ref's binding of it.
func deadLinksBinding() Binding {
	return Binding{Name: "dead-links", Mode: ModeShadow,
		AllowedFiles: []string{`^(docs|website/content)/.+\.md$`}, HoldoutChecks: []string{"External links 🔗"},
		OracleWorkflow: "dead-links.yaml"}
}

func adoptPolicy(shape string) PolicySpec {
	return PolicySpec{Class: Class{DiffShape: shape, ModifyOnly: true,
		Author: &Author{Adopt: &Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}}}}
}

var testProfiles = map[string]HarnessProfile{"solo": {Template: "solo", Tier: "standard"}, "tf": {Checks: true, TestFirst: true}}

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
		"off":             {bind: func(b *Binding) { b.Mode = ModeOff }, check: func(t *testing.T, c Class) { want(t, !c.Live && !c.Shadow, "off is neither (Q7)") }},
		"an unknown mode": {bind: func(b *Binding) { b.Mode = "on" }, reason: ReasonInvalid, msg: `mode "on" is live, shadow or off`},
		"review in shadow": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{} }, bind: func(b *Binding) { *b = Binding{Name: "review", Mode: ModeShadow} },
			reason: ReasonInvalid, msg: "review takes only mode off"},
		"review off": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{Harness: "solo"}} }, bind: func(b *Binding) { *b = Binding{Name: "review", Mode: ModeOff} },
			check: func(t *testing.T, c Class) {
				want(t, c.Harness == "solo" && !c.Live && !c.Shadow, "review off keeps its harness")
			}},
		// Meaning: the policy's.
		"meaning": {policy: func(p *PolicySpec) { p.Harness = "solo" }, check: func(t *testing.T, c Class) {
			want(t, c.Description == deadLinksPolicy().Description && c.Harness == "solo" && c.DiffShape == "links-only" && c.ModifyOnly &&
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
			p.Requires = []string{"holdoutChecks", "oracleWorkflow"}
			p.AllowedFiles = []string{"^docs/.+$"}
		},
			bind: func(b *Binding) { b.AllowedFiles = nil }, check: func(t *testing.T, c Class) {
				want(t, slices.Equal(c.AllowedFiles, []string{"^docs/.+$"}), "allowedFiles from the policy")
			}},
		"allowedFiles twice": {policy: func(p *PolicySpec) {
			p.Requires = []string{"holdoutChecks", "oracleWorkflow"}
			p.AllowedFiles = []string{"^docs/.+$"}
		},
			reason: ReasonParameterTwice, msg: "allowedFiles is set by MergePolicy dead-links and by the binding"},
		"requiredLabel twice": {class: "kb", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{RequiredLabel: "a"}} }, bind: func(b *Binding) { *b = Binding{Name: "kb", Mode: ModeOff, RequiredLabel: "b"} },
			reason: ReasonParameterTwice, msg: "requiredLabel is set by MergePolicy kb and by the binding"},
		"allowedFiles missing":            {bind: func(b *Binding) { b.AllowedFiles = nil }, reason: ReasonMissingParameter, msg: "MergePolicy dead-links requires allowedFiles"},
		"oracleWorkflow fills the oracle": {check: func(t *testing.T, c Class) { want(t, c.Oracle.Workflow == "dead-links.yaml", "oracle.workflow") }},
		"oracleWorkflow without an oracle": {class: "docs", policy: func(p *PolicySpec) { *p = PolicySpec{} }, bind: func(b *Binding) { *b = Binding{Name: "docs", Mode: ModeOff, OracleWorkflow: "x.yaml"} },
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
		"holdoutChecks missing": {bind: func(b *Binding) { b.HoldoutChecks = nil }, reason: ReasonMissingParameter, msg: "MergePolicy dead-links requires holdoutChecks"},
		// A-L2: a binding that only repeats the policy's holdouts adds none.
		"holdoutChecks only the policy's": {policy: func(p *PolicySpec) { p.HoldoutChecks = []string{"lint"} }, bind: func(b *Binding) { b.HoldoutChecks = []string{"lint"} },
			reason: ReasonMissingParameter, msg: "MergePolicy dead-links requires holdoutChecks: the binding adds none of its own"},
		"a binding of another policy": {bind: func(b *Binding) { b.Name = "docs-links" }, reason: ReasonInvalid, msg: `class dead-links: the binding names MergePolicy "docs-links"`},
		"review bound with a parameter": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{} }, bind: func(b *Binding) { *b = Binding{Name: "review", Mode: ModeOff, MaxPerDay: 1} },
			reason: ReasonInvalid, msg: "review is bound with mode off and no parameter"},
		// R17: review keeps checks and testFirst.
		"review with checks and testFirst": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{Checks: true, TestFirst: true}} },
			bind: func(b *Binding) { *b = Binding{Name: "review", Mode: ModeOff} }, merge: Merge{ContractCheck: "contract"},
			check: func(t *testing.T, c Class) {
				want(t, c.Checks && c.TestFirst && !c.Live && !c.Shadow, "review keeps checks and testFirst")
			}},
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
		"testFirst without a contract check": {class: "x", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{Checks: true, TestFirst: true}} }, bind: func(b *Binding) { *b = Binding{Name: "x", Mode: ModeOff} },
			reason: ReasonInvalid, msg: "class x: testFirst needs merge.contractCheck"},
		"a testFirst profile without a contract check": {class: "x", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{Harness: "tf"}} }, bind: func(b *Binding) { *b = Binding{Name: "x", Mode: ModeOff} },
			reason: ReasonInvalid, msg: `class x: profile "tf": testFirst needs merge.contractCheck`},
		"testFirst with a contract check": {class: "x", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{Checks: true, TestFirst: true}} }, bind: func(b *Binding) { *b = Binding{Name: "x", Mode: ModeOff} },
			merge: Merge{ContractCheck: "contract"}, check: func(t *testing.T, c Class) { want(t, c.TestFirst, "testFirst") }},
		// The repository rule a live class without allowedFiles breaks, judged per binding (B9).
		"live without allowedFiles and no policy-bot": {class: "x", policy: func(p *PolicySpec) { *p = PolicySpec{} }, bind: func(b *Binding) { *b = Binding{Name: "x", Mode: ModeLive} },
			merge: Merge{PolicyBot: new(false)}, reason: ReasonInvalid, msg: "class x: merge.policyBot: false needs allowedFiles on every live class"},
		// The variant floor reads the templates (TE2): checked per binding, never left to Parse,
		// where it would refuse every consumer Repository.
		"a variant below its floor": {class: "x", ctx: PolicyContext{
			HarnessProfiles: map[string]HarnessProfile{"rich": {Template: "pair"}, "base": {Template: "solo"}},
			Templates:       map[string]Template{"solo": {Roles: []string{"implementer"}}, "pair": {Roles: []string{"implementer", "reviewer"}}}},
			policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{Harness: "rich", HarnessAllowed: []string{"base"}}} },
			bind:   func(b *Binding) { *b = Binding{Name: "x", Mode: ModeOff} },
			reason: ReasonInvalid, msg: `harnessAllowed "base" drops roles of the floor (TE2)`},
		"an unknown harness": {policy: func(p *PolicySpec) { p.Harness = "nope" }, reason: ReasonInvalid, msg: `harness "nope" is not in harnessProfiles (TE2)`},
		"live on the policy": {policy: func(p *PolicySpec) { p.Live = true }, reason: ReasonInvalid, msg: "mode is the binding's"},
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

// B9: a policy is Accepted when it is valid alone, a placeholder in each field it requires.
func TestValidatePolicy(t *testing.T) {
	ctx := PolicyContext{HarnessProfiles: testProfiles}
	for name, tc := range map[string]struct {
		class  string
		policy func(*PolicySpec)
		msg    string
	}{
		"dead-links is valid alone": {},
		"dead-links without holdoutChecks": {policy: func(p *PolicySpec) { p.Requires = []string{"allowedFiles", "oracleWorkflow"} },
			msg: "an oracle needs holdoutChecks"},
		"review with allowedFiles": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{AllowedFiles: []string{"^docs/.+$"}}} },
			msg: "review takes checks and testFirst only"},
		// A-L1: the fields of review's CEL rule validateClass does not read.
		"review with requires": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{Requires: []string{"holdoutChecks"}} },
			msg: "review takes only description, harness, harnessAllowed, escalation, checks and testFirst"},
		"review with companionFiles": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{CompanionFiles: []string{"^x$"}}} },
			msg: "review takes only description, harness, harnessAllowed, escalation, checks and testFirst"},
		"review with requiredLabel": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{RequiredLabel: "x"}} },
			msg: "review takes only description, harness, harnessAllowed, escalation, checks and testFirst"},
		"review with checks and testFirst": {class: "review", policy: func(p *PolicySpec) { *p = PolicySpec{Class: Class{Checks: true, TestFirst: true}} }},
		"a flux monitor":                   {policy: func(p *PolicySpec) { p.Monitor.Flux = &FluxWatch{Within: Duration{30 * time.Minute}} }},
		"requires an unknown field": {policy: func(p *PolicySpec) { p.Requires = append(p.Requires, "maxPerDay") },
			msg: `requires "maxPerDay" is not one of [allowedFiles companionFiles requiredLabel oracleWorkflow holdoutChecks]`},
		"requires a field twice": {policy: func(p *PolicySpec) { p.Requires = append(p.Requires, "allowedFiles") }, msg: `requires names "allowedFiles" twice`},
		"requires what it sets": {policy: func(p *PolicySpec) { p.AllowedFiles = []string{"^docs/.+$"} },
			msg: "requires allowedFiles, which it sets: a parameter is set in one place (B4)"},
		"live on a policy":       {policy: func(p *PolicySpec) { p.Shadow = true }, msg: "mode is the binding's: live and shadow are refused on a MergePolicy (B4)"},
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

// ApplyGuardrails is mergepolicy's guard, moved: protected paths and mandatory checks added once,
// an unset cap inherited, a cap above refused.
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
	if over := ApplyGuardrails(&cl, g); over != "maxPerDay 3 is over the guardrail's 2" {
		t.Fatalf("got %q", over)
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
	cl := Class{Shadow: true, Monitor: &Monitor{FollowUpWindow: Duration{time.Hour}, Flux: &FluxWatch{Source: "flux-system/old", Within: Duration{30 * time.Minute}}}}
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

// review merges nothing (R30), so the guardrails, which cap what merges, leave it as bound: a cap
// they would give it is a field review may not hold, and would drop its binding.
func TestGuardrailsLeaveReview(t *testing.T) {
	ctx := PolicyContext{Guardrails: MergeGuardrails{MaxPerDay: 2, MandatoryChecks: []string{"lint"}, ProtectedFiles: []string{"^x$"}}}
	cl, refusal := Resolve("review", PolicySpec{}, Binding{Name: "review", Mode: ModeOff}, ctx, Merge{})
	if refusal != nil || cl.MaxPerDay != 0 || cl.HoldoutChecks != nil || cl.ProtectedFiles != nil {
		t.Fatalf("%+v %+v", cl, refusal)
	}
	if _, refusal := Recheck("review", Class{}, ctx, Merge{}); refusal != nil {
		t.Fatalf("%+v", refusal)
	}
}
