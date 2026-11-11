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

func policy(t *testing.T, name, ref string) Object {
	return Object{Kind: KindMergePolicy, Name: name, Generation: 1, Spec: map[string]any{
		"repositoryRef": ref, "classes": classes(map[string]any{}), "merge": mergeBlock(t)}}
}

// classes is the smallest set Parse admits: docs-links and revert are required (OD-8).
func classes(docs map[string]any) map[string]any {
	return map[string]any{"docs-links": map[string]any{"shadow": true}, "revert": map[string]any{"shadow": true}, "docs": docs}
}

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

func TestAssembleAddsARepositoryAndItsPolicy(t *testing.T) {
	r, err := Assemble(file(t), []Object{repo("maku", "Smana/maku"), policy(t, "maku", "maku")})
	if err != nil {
		t.Fatal(err)
	}
	rc := r.Config.RepositoryFor("Smana/maku")
	if rc == nil || len(rc.Classes) != 3 || rc.Merge.MergerLogin == "" || len(r.Accepted) != 2 {
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
			o := repo("maku", "Smana/maku")
			o.Spec["factoryRef"] = "gcp-0"
			return []Object{o, policy(t, "maku", "maku")}
		}, KindRepository, "maku", v1alpha1.ReasonNotThisFactory},
		{"a repository also in the file", func(t *testing.T) []Object {
			return []Object{repo("cnr", "smana/Cloud-Native-Ref"), policy(t, "cnr", "cnr")}
		}, KindRepository, "cnr", v1alpha1.ReasonDefinedTwice},
		{"a Repository without a policy", func(*testing.T) []Object {
			return []Object{repo("maku", "Smana/maku")}
		}, KindRepository, "maku", v1alpha1.ReasonNoMergePolicy},
		{"a policy naming no Repository", func(t *testing.T) []Object {
			return []Object{policy(t, "orphan", "nothing")}
		}, KindMergePolicy, "orphan", v1alpha1.ReasonMissingRef},
		{"two policies for one Repository", func(t *testing.T) []Object {
			return []Object{repo("maku", "Smana/maku"), policy(t, "a", "maku"), policy(t, "b", "maku")}
		}, KindMergePolicy, "b", v1alpha1.ReasonDuplicatePolicy},
		{"a policy Parse refuses", func(t *testing.T) []Object {
			p := policy(t, "maku", "maku")
			p.Spec["classes"] = classes(map[string]any{"live": true, "shadow": true})
			return []Object{repo("maku", "Smana/maku"), p}
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

func badPolicy(t *testing.T, name string) Object {
	p := policy(t, name, name)
	p.Spec["classes"] = classes(map[string]any{"live": true, "shadow": true})
	return p
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
// Factory replaces it, and the pair beside it is served.
func TestAssembleFactoryCulpritKeepsTheFilesCatalogue(t *testing.T) {
	want, _ := config.Parse(file(t))
	f := Object{Kind: KindFactory, Name: "aws-0", Spec: map[string]any{"caps": map[string]any{"activeTasks": -1}}}
	r, err := Assemble(file(t), []Object{f, repo("maku", "Smana/maku"), policy(t, "maku", "maku")})
	if err != nil {
		t.Fatal(err)
	}
	if x := refusal(r, KindFactory, "aws-0"); x == nil || x.Reason != v1alpha1.ReasonInvalid {
		t.Fatalf("the Factory must be refused Invalid: %+v", r.Refused)
	}
	if len(r.Refused) != 1 || r.Config.RepositoryFor("Smana/maku") == nil {
		t.Fatalf("the pair must be served: %+v", r.Refused)
	}
	if !equalJSON(t, want.Tiers, r.Config.Tiers) || !equalJSON(t, want.Caps, r.Config.Caps) || len(r.Warnings) != 0 {
		t.Fatalf("the file's catalogue must stay, without warnings: %v", r.Warnings)
	}
}

// F3: a pair culprit refuses the MergePolicy with Parse's error and the Repository as not served.
func TestAssemblePairCulpritRefusesBoth(t *testing.T) {
	r, err := Assemble(file(t), []Object{repo("maku", "Smana/maku"), badPolicy(t, "maku")})
	if err != nil {
		t.Fatal(err)
	}
	p, rp := refusal(r, KindMergePolicy, "maku"), refusal(r, KindRepository, "maku")
	if p == nil || p.Reason != v1alpha1.ReasonInvalid || !strings.Contains(p.Message, "live and shadow") {
		t.Fatalf("MergePolicy: %+v", p)
	}
	if rp == nil || rp.Reason != v1alpha1.ReasonInvalid || rp.Message != "not served: MergePolicy maku was refused" {
		t.Fatalf("Repository: %+v", rp)
	}
}

// F2: two bad pairs are both refused, and the good one between them is served.
func TestAssembleTwoCulprits(t *testing.T) {
	r, err := Assemble(file(t), []Object{
		repo("a", "Smana/a"), badPolicy(t, "a"),
		repo("b", "Smana/b"), policy(t, "b", "b"),
		repo("c", "Smana/c"), badPolicy(t, "c"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if refused(r, KindMergePolicy, "a") != v1alpha1.ReasonInvalid || refused(r, KindMergePolicy, "c") != v1alpha1.ReasonInvalid {
		t.Fatalf("both bad policies must be refused: %+v", r.Refused)
	}
	if refused(r, KindRepository, "b") != "" || r.Config.RepositoryFor("Smana/b") == nil || len(r.Accepted) != 2 {
		t.Fatalf("the good pair must be served: %+v", r.Refused)
	}
	if x := refusal(r, KindMergePolicy, "c"); strings.Contains(x.Message, "Smana/a") {
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
	r, err := Assemble(file(t), []Object{
		repo("z", "Smana/z"), badPolicy(t, "z"),
		{Kind: KindFactory, Name: "gcp-0", Spec: map[string]any{}},
		policy(t, "orphan", "nothing"),
		repo("b", "Smana/b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range r.Refused {
		got = append(got, string(x.Kind)+"/"+x.Name)
	}
	want := "Factory/gcp-0 MergePolicy/orphan MergePolicy/z Repository/b Repository/z"
	if strings.Join(got, " ") != want {
		t.Fatalf("order: %v, want %s", got, want)
	}
}

// A bad Factory changes the joined error of every pair that has a schedule (the text cap is a
// catalogue value), so removing a pair changes the error. The culprit is the element whose
// removal makes the config parse: the Factory.
func TestAssembleBadFactoryIsTheCulpritNotThePairs(t *testing.T) {
	sched := func(n string) []any {
		return []any{map[string]any{"name": n, "cron": "0 6 * * 1", "class": "review", "text": "a schedule text well over ten bytes"}}
	}
	ra, rb := repo("a", "Smana/a"), repo("b", "Smana/b")
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
	r, err := Assemble(file(t), []Object{ra, policy(t, "a", "a"), rb, policy(t, "b", "b"), f})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Refused) != 1 || r.Refused[0].Kind != KindFactory || r.Refused[0].Reason != v1alpha1.ReasonInvalid {
		t.Fatalf("refused = %+v, want only the Factory, Invalid", r.Refused)
	}
	if len(r.Accepted) != 4 {
		t.Fatalf("accepted = %d objects, want both pairs", len(r.Accepted))
	}
}

// #448: after migration the file holds no repository, and control.repository is a Factory value
// naming a repository only a Repository object lists. A bad Factory is the culprit with its own
// error: it is never blamed through "not a listed repository" errors that come from removing a pair.
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
	if len(r.Refused) != 1 {
		t.Fatalf("only the Factory is at fault: %+v", r.Refused)
	}
}
