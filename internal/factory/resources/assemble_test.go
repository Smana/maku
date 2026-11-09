// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"strings"
	"testing"

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
	}}
	r, err := Assemble(file(t), []Object{f})
	if err != nil {
		t.Fatal(err)
	}
	if refused(r, KindFactory, "aws-0") != "" {
		t.Fatalf("refused: %+v", r.Refused)
	}
	if r.Config.Tiers["light"].RunTokens != 1000 || r.Config.Caps.ActiveTasks != 1 || r.Config.Triage.ControlPercent != 20 {
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
