// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

func file(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/file.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mergeBlock(t *testing.T) map[string]any {
	t.Helper()
	c, err := config.Parse(file(t))
	if err != nil {
		t.Fatal(err)
	}
	m := c.RepositoryFor("Smana/cloud-native-ref").Merge
	return map[string]any{"requiredChecks": toAny(m.RequiredChecks), "verifyChecks": toAny(m.VerifyChecks),
		"leakScanCheck": m.LeakScanCheck, "policyBotLogin": m.PolicyBotLogin, "mergerLogin": m.MergerLogin,
		"autoMergesPerDay": m.AutoMergesPerDay, "fixRuns": m.FixRuns, "checkReruns": m.CheckReruns,
		"verifyFor": "30m", "revertWindow": "168h", "breaker": map[string]any{"window": 10, "maxReverts": 1}}
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func repo(name, repository string) Object {
	return Object{Kind: KindRepository, Name: name, Generation: 1, Spec: map[string]any{
		"factoryRef": "aws-0", "repository": repository, "maintainers": []any{"Smana"}, "triggerLabel": "factory/ready"}}
}

// served is a Repository with its merge and the smallest bindings Parse admits: docs-links and
// revert are required (OD-8). It binds specPolicies.
func served(t *testing.T, name, repository string) Object {
	t.Helper()
	return boundRepo(t, name, repository, 1, `
- {name: docs-links, mode: shadow, holdoutChecks: [validate]}
- {name: revert, mode: shadow, holdoutChecks: [validate]}
- {name: docs, mode: "off"}
`)
}

// bad is a served Repository Parse refuses: a schedule of a class it does not bind.
func bad(t *testing.T, name, repository string) Object {
	t.Helper()
	o := served(t, name, repository)
	o.Spec["schedules"] = []any{map[string]any{"name": "s", "cron": "0 6 * * 1", "class": "nothing-binds-this", "text": "a schedule"}}
	return o
}

// with is specPolicies, then objs.
func with(t *testing.T, objs ...Object) []Object { return append(specPolicies(t), objs...) }

func refused(r Result, kind Kind, name string) string {
	for _, x := range r.Refused {
		if x.Kind == kind && x.Name == name {
			return x.Reason
		}
	}
	return ""
}

func TestAssembleFileAloneIsParse(t *testing.T) {
	want, _ := config.Parse(file(t))
	r, err := Assemble(file(t), nil)
	if err != nil || r.Config.Hash != want.Hash || len(r.Refused) != 0 {
		t.Fatalf("no objects must give Parse's config: %v %+v", err, r.Refused)
	}
}

func TestAssembleAddsARepository(t *testing.T) {
	r, err := Assemble(file(t), with(t, served(t, "maku", "Smana/maku")))
	if err != nil {
		t.Fatal(err)
	}
	rc := r.Config.RepositoryFor("Smana/maku")
	if rc == nil || len(rc.Classes) != 3 || rc.Merge.MergerLogin == "" || len(r.Accepted) != 5 {
		t.Fatalf("Smana/maku not served: %+v %+v", rc, r.Refused)
	}
}

func TestAssembleRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		objs   func(t *testing.T) []Object
		kind   Kind
		obj    string
		reason string
	}{
		{"another factory's Factory", func(*testing.T) []Object {
			return []Object{{Kind: KindFactory, Name: "gcp-0", Spec: map[string]any{}}}
		}, KindFactory, "gcp-0", v1alpha1.ReasonNotThisFactory},
		{"another factory's Repository", func(t *testing.T) []Object {
			o := served(t, "maku", "Smana/maku")
			o.Spec["factoryRef"] = "gcp-0"
			return with(t, o)
		}, KindRepository, "maku", v1alpha1.ReasonNotThisFactory},
		{"a repository also in the file", func(t *testing.T) []Object {
			return with(t, served(t, "cnr", "smana/Cloud-Native-Ref"))
		}, KindRepository, "cnr", v1alpha1.ReasonDefinedTwice},
		{"a Repository Parse refuses", func(t *testing.T) []Object {
			return with(t, bad(t, "maku", "Smana/maku"))
		}, KindRepository, "maku", v1alpha1.ReasonInvalid},
		// B8, plan PR20: the per-repository shape is no MergePolicy any more; it never serves.
		{"a per-repository MergePolicy", func(t *testing.T) []Object {
			return with(t, served(t, "maku", "Smana/maku"), Object{Kind: KindMergePolicy, Name: "maku", Generation: 1,
				Spec: map[string]any{"repositoryRef": "maku", "classes": map[string]any{"docs": map[string]any{}}}})
		}, KindMergePolicy, "maku", v1alpha1.ReasonInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Assemble(file(t), tc.objs(t))
			if err != nil {
				t.Fatal(err)
			}
			if got := refused(r, tc.kind, tc.obj); got != tc.reason {
				t.Fatalf("%s %s: reason %q, want %q (%+v)", tc.kind, tc.obj, got, tc.reason, r.Refused)
			}
			if r.Config.RepositoryFor("Smana/cloud-native-ref") == nil {
				t.Fatal("a refusal must leave the file's repository served")
			}
		})
	}
}

func TestAssembleFactoryReplacesTheCatalogue(t *testing.T) {
	f := Object{Kind: KindFactory, Name: "aws-0", Generation: 3, Spec: map[string]any{
		"tiers": map[string]any{
			"light":    map[string]any{"model": "agent-default", "runTokens": 1000, "taskTokens": 2000, "runMinutes": 5},
			"standard": map[string]any{"model": "agent-default", "runTokens": 1000, "taskTokens": 2000, "runMinutes": 5},
			"frontier": map[string]any{"model": "agent-default", "runTokens": 1000, "taskTokens": 2000, "runMinutes": 5}},
		"templates": map[string]any{"solo": map[string]any{"roles": []any{"implementer"}},
			"pair":        map[string]any{"roles": []any{"implementer", "reviewer"}},
			"trio":        map[string]any{"roles": []any{"implementer", "tester", "reviewer"}},
			"investigate": map[string]any{"roles": []any{"triager"}}},
		"defaults": map[string]any{"template": "solo", "tier": "standard", "dataClass": "public", "predictedClass": "review"},
		"caps":     map[string]any{"activeTasks": 1, "concurrentRuns": 1, "tasksPerDay": 1, "maxTextBytes": 14336, "awaitingHumanWIP": 1},
		"triage":   map[string]any{"controlPercent": 20},
		"harnessProfiles": map[string]any{"base": map[string]any{"template": "solo"},
			"rich": map[string]any{"template": "pair"}},
		"evaluation": map[string]any{"controlPercent": 30, "mode": "apply"},
	}}
	r, err := Assemble(file(t), []Object{f})
	if err != nil {
		t.Fatal(err)
	}
	if refused(r, KindFactory, "aws-0") != "" {
		t.Fatalf("refused: %+v", r.Refused)
	}
	if r.Config.Tiers["light"].RunTokens != 1000 || r.Config.Caps.ActiveTasks != 1 || r.Config.Triage.ControlPercent != 20 ||
		r.Config.Evaluation.ControlPercent != 30 || !r.Config.Applies() {
		t.Fatalf("the Factory's catalogue did not apply: %+v", r.Config.Tiers)
	}
	if r.Config.Triage.ClassifierURL == "" {
		t.Fatal("triage.classifierURL is the file's (D12) and must survive")
	}
	// The file sets budgets.enforceTask: false; the Factory omits budgets, so Parse's defaults
	// apply (enforceTask true, OD-10's 42.50): the raw spec is merged, never a re-marshalled struct.
	if !r.Config.Budgets.EnforceTask || r.Config.Budgets.FactoryDailyUSD != 42.50 {
		t.Fatalf("an omitted budgets block takes Parse's defaults: %+v", r.Config.Budgets)
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "tiers in the file is ignored") {
		t.Fatalf("warnings: %v", r.Warnings)
	}
}

func refusal(r Result, kind Kind, name string) *Refusal {
	for i, x := range r.Refused {
		if x.Kind == kind && x.Name == name {
			return &r.Refused[i]
		}
	}
	return nil
}

// F4: a Factory Parse refuses is the culprit; the file's catalogue stays, with no warning that a
// Factory replaces it, and the Repository beside it is served.
func TestAssembleFactoryCulpritKeepsTheFilesCatalogue(t *testing.T) {
	want, _ := config.Parse(file(t))
	f := Object{Kind: KindFactory, Name: "aws-0", Spec: map[string]any{"caps": map[string]any{"activeTasks": -1}}}
	r, err := Assemble(file(t), with(t, f, served(t, "maku", "Smana/maku")))
	if err != nil {
		t.Fatal(err)
	}
	if x := refusal(r, KindFactory, "aws-0"); x == nil || x.Reason != v1alpha1.ReasonInvalid {
		t.Fatalf("the Factory must be refused Invalid: %+v", r.Refused)
	}
	if len(r.Refused) != 1 || r.Config.RepositoryFor("Smana/maku") == nil {
		t.Fatalf("the Repository must be served: %+v", r.Refused)
	}
	if !equalJSON(t, want.Tiers, r.Config.Tiers) || !equalJSON(t, want.Caps, r.Config.Caps) || strings.Contains(strings.Join(r.Warnings, "\n"), "is ignored") {
		t.Fatalf("the file's catalogue must stay, without a replaced-catalogue warning: %v", r.Warnings)
	}
}

// F3: a culprit Repository is refused with Parse's error, and its policies still serve.
func TestAssembleRepositoryCulprit(t *testing.T) {
	r, err := Assemble(file(t), with(t, bad(t, "maku", "Smana/maku")))
	if err != nil {
		t.Fatal(err)
	}
	if rp := refusal(r, KindRepository, "maku"); rp == nil || rp.Reason != v1alpha1.ReasonInvalid || !strings.Contains(rp.Message, "nothing-binds-this") {
		t.Fatalf("Repository: %+v", rp)
	}
	if len(r.Refused) != 1 {
		t.Fatalf("only the Repository is at fault: %+v", r.Refused)
	}
}

// F2: two bad Repositories are both refused, and the good one between them is served.
func TestAssembleTwoCulprits(t *testing.T) {
	r, err := Assemble(file(t), with(t, bad(t, "a", "Smana/a"), served(t, "b", "Smana/b"), bad(t, "c", "Smana/c")))
	if err != nil {
		t.Fatal(err)
	}
	if refused(r, KindRepository, "a") != v1alpha1.ReasonInvalid || refused(r, KindRepository, "c") != v1alpha1.ReasonInvalid {
		t.Fatalf("both bad Repositories must be refused: %+v", r.Refused)
	}
	if refused(r, KindRepository, "b") != "" || r.Config.RepositoryFor("Smana/b") == nil || len(r.Accepted) != 5 {
		t.Fatalf("the good Repository must be served: %+v", r.Refused)
	}
	if x := refusal(r, KindRepository, "c"); strings.Contains(x.Message, "Smana/a") {
		t.Fatalf("c's refusal must carry the error left once a was removed: %q", x.Message)
	}
}

// F2: after migration the file alone serves nothing, so a bad Factory leaves no config: the
// error comes with the refusals, which the caller still reports.
func TestAssembleMigratedBadFactoryReportsRefusals(t *testing.T) {
	manifests, residual, err := Migrate(file(t), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	for i := range objs {
		if objs[i].Kind == KindFactory {
			objs[i].Spec["caps"] = map[string]any{"activeTasks": -1}
		}
	}
	r, err := Assemble(residual, objs)
	if err == nil || r.Config != nil {
		t.Fatalf("no config can be served: err %v", err)
	}
	if refused(r, KindFactory, "aws-0") != v1alpha1.ReasonInvalid {
		t.Fatalf("the Factory's refusal must come with the error: %+v", r.Refused)
	}
}

// Refused is sorted by Kind, then Name, whatever order the refusals were found in.
func TestAssembleRefusedOrder(t *testing.T) {
	r, err := Assemble(file(t), with(t,
		bad(t, "z", "Smana/z"),
		Object{Kind: KindFactory, Name: "gcp-0", Spec: map[string]any{}},
		classPolicy(t, "orphan", 1, "live: true\n"),
		bad(t, "b", "Smana/b"),
	))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range r.Refused {
		got = append(got, string(x.Kind)+"/"+x.Name)
	}
	want := "Factory/gcp-0 MergePolicy/orphan Repository/b Repository/z"
	if strings.Join(got, " ") != want {
		t.Fatalf("order: %v, want %s", got, want)
	}
}

// A bad Factory changes the joined error of every Repository that has a schedule (the text cap is
// a catalogue value), so removing a Repository changes the error. The culprit is the element whose
// removal makes the config parse: the Factory.
func TestAssembleBadFactoryIsTheCulpritNotThePairs(t *testing.T) {
	sched := func(n string) []any {
		return []any{map[string]any{"name": n, "cron": "0 6 * * 1", "class": "review", "text": "a schedule text well over ten bytes"}}
	}
	ra, rb := served(t, "a", "Smana/a"), served(t, "b", "Smana/b")
	ra.Spec["schedules"], rb.Spec["schedules"] = sched("sa"), sched("sb")
	var base map[string]any
	if err := yaml.Unmarshal(file(t), &base); err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{}
	for _, k := range CatalogueKeys {
		if v, ok := base[k]; ok {
			spec[k] = v
		}
	}
	caps, _ := spec["caps"].(map[string]any)
	if caps == nil {
		caps = map[string]any{}
	}
	caps["maxTextBytes"] = 10
	spec["caps"] = caps
	f := Object{Kind: KindFactory, Name: "aws-0", Spec: spec}
	r, err := Assemble(file(t), with(t, ra, rb, f))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Refused) != 1 || r.Refused[0].Kind != KindFactory || r.Refused[0].Reason != v1alpha1.ReasonInvalid {
		t.Fatalf("refused = %+v, want only the Factory, Invalid", r.Refused)
	}
	if len(r.Accepted) != 6 {
		t.Fatalf("accepted = %d objects, want both Repositories and the four policies", len(r.Accepted))
	}
}

// #448: after migration the file holds no repository, and control.repository is a Factory value
// naming a repository only a Repository object lists. A bad Factory is the culprit with its own
// error: it is never blamed through "not a listed repository" errors that come from removing a
// Repository.
func TestAssembleBadFactoryMessageHasNoFalseRepositoryErrors(t *testing.T) {
	manifests, residual, err := Migrate(file(t), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	for i := range objs {
		if objs[i].Kind != KindFactory {
			continue
		}
		tpl, _ := objs[i].Spec["templates"].(map[string]any)
		pair, _ := tpl["pair"].(map[string]any)
		pair["roles"] = []any{"implementer", "astronaut"}
	}
	r, _ := Assemble(residual, objs)
	x := refusal(r, KindFactory, "aws-0")
	if x == nil || !strings.Contains(x.Message, "astronaut") || strings.Contains(x.Message, "repositories is empty") ||
		strings.Contains(x.Message, "not a listed repository") {
		t.Fatalf("Factory refusal: %+v", x)
	}
	// No Repository is blamed. A class policy naming a profile only the refused Factory held is
	// refused alone (B9): the catalogue it is judged under is the file's.
	for _, x := range r.Refused {
		if x.Kind == KindRepository || (x.Kind == KindMergePolicy && !strings.Contains(x.Message, "is not in harnessProfiles")) {
			t.Fatalf("only the Factory is at fault: %+v", r.Refused)
		}
	}
}

// A Factory value naming a repository whose Repository was itself refused is that refusal's echo,
// not the Factory's fault: the Factory is never the culprit for it, and the error names the line
// rather than the catalogue a Factory-less parse would then miss (live, 2026-11-13).
func TestAssembleNeverBlamesTheFactoryForARefusedRepository(t *testing.T) {
	manifests, residual, err := Migrate(file(t), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	for i := range objs {
		switch objs[i].Kind {
		case KindFactory:
			objs[i].Spec["control"] = map[string]any{"repository": "Smana/cloud-native-ref", "issue": 1}
		case KindRepository:
			objs[i].Spec["factoryRef"] = "gcp-0" // refused NotThisFactory before any Parse
		}
	}
	// A second repository keeps the set servable but for the control line.
	objs = append(objs, served(t, "maku", "Smana/maku"))
	r, err := Assemble(residual, objs)
	if x := refusal(r, KindFactory, "aws-0"); x != nil {
		t.Fatalf("the Factory is blamed for a refused Repository: %+v", x)
	}
	if err == nil || !strings.Contains(err.Error(), `control.repository "Smana/cloud-native-ref" is not a listed repository`) ||
		strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want the control line alone", err)
	}
}

// v4 D3: a Repository's privateCode reaches the served config.
func TestAssembleCarriesPrivateCode(t *testing.T) {
	r0 := served(t, "maku", "Smana/maku")
	r0.Spec["privateCode"] = map[string]any{"acknowledged": true}
	r, err := Assemble(file(t), with(t, r0))
	if err != nil {
		t.Fatal(err)
	}
	if rc := r.Config.RepositoryFor("Smana/maku"); !rc.PrivateCodeAcknowledged() {
		t.Fatalf("privateCode dropped: %+v %+v", rc, r.Refused)
	}
	if r.Config.RepositoryFor("Smana/cloud-native-ref").PrivateCodeAcknowledged() {
		t.Fatal("a repository without privateCode acknowledges nothing")
	}
}
