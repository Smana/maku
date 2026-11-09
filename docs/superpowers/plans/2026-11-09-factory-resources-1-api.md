# Factory resources, plan 1 of 4: the API, the CRDs and the offline tools

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the `Factory`, `Repository` and `MergePolicy` CRDs in the factory chart, a pure
`resources.Assemble` that turns them plus today's file into the `config.Config` the factory already
reads, and `maku config migrate` and `maku config validate`. The running factory does not read the
new kinds yet (plan 2), so nothing changes at runtime.

**Architecture:** The CR specs reference `internal/factory/config`'s own structs (D16), so the CRD
schema, the YAML file and the CRs share one set of Go types. `Assemble` works on the objects' raw
specs: it merges them into the file's YAML map and calls `config.Parse`, so defaulting and
validation are exactly today's. It adds one object at a time and refuses the one whose addition
makes `Parse` fail (D5's "keep the last accepted").

**Tech Stack:** Go, controller-gen (`go tool controller-gen`), kubebuilder markers and CEL, Helm,
`sigs.k8s.io/yaml`.

**Spec:** `docs/superpowers/specs/2026-11-09-factory-resources-design.md` (D1–D16). Read D3, D8,
D12, D15, D16, "The resources" and "Validation" before starting.

## Global Constraints

- Group `maku.ogenki.io`, version `v1alpha1`, all three kinds namespaced, status subresource, `observedGeneration`, `[]metav1.Condition`.
- Field names and shapes are today's config keys, unchanged (spec, "The resources").
- Every string in a CRD has a `maxLength` (or an enum, or `date-time`), every list a `maxItems`, every map a `maxProperties` (review I3, `api/factory/v1alpha1/task_crd_test.go`).
- Every `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`.
- `task check` exits 0 before every commit (`AGENTS.md`). Lint through `task lint` only.
- CEL transition rules reading an `omitempty` field of `oldSelf` guard it with `has()` (wiki `cel-transition-rule-omitempty-needs-has`). Write CEL markers with straight quotes: gofmt curls quotes inside kubebuilder markers (wiki `gofmt-curls-quotes-in-kubebuilder-cel-marker`; `curlyquotes_test.go` checks it).
- Conventional commits, in English, no trailers, no co-author.
- Refusal reasons are exactly: `NotThisFactory`, `DefinedTwice`, `MissingRef`, `NoMergePolicy`, `DuplicatePolicy`, `Invalid`.

## Plans 2–4 (not here)

| Plan | Delivers |
|---|---|
| 2 | The config controller: every replica watches the kinds, calls `Assemble`, publishes through `config.Live` (`atomic.Pointer[Config]`); the 24 `*config.Config` readers move to the accessor; startup derivations re-derived on publish (D5, D12, D13, D14); `status.configGenerations` on Task |
| 3 | The status writer (leader only): conditions, `MergePolicy` authority and per-class mode and last decision, `Repository.Connected`, `Factory.Ready`; the settings pages read the CRs and output a `MergePolicy` patch (D9, D10, D11) |
| 4 | ai-native-ref: the generated manifests, `maku config validate` in `task check`, the values file shrunk; then release N+1's refusal of the file's catalogue (D8) |

## File structure

| File | Responsibility |
|---|---|
| `internal/factory/config/doc.go` (modify, or create if the package comment lives elsewhere) | nothing new beyond the deepcopy marker note |
| `internal/factory/config/classes.go`, `config.go`, `mergepolicy.go` (modify) | per-type `+kubebuilder:object:generate=true`, bound markers, `Duration` schema override |
| `internal/factory/config/zz_generated.deepcopy.go` (generated) | deepcopy for the types the CRs embed |
| `api/factory/v1alpha1/factoryconfig_types.go` (create) | `Factory`, `Repository`, `MergePolicy`, their specs, statuses, lists, CEL, printer columns |
| `api/factory/v1alpha1/factoryconfig_crd_test.go` (create) | the generated CRDs carry the rules and the bounds |
| `config/crd/maku.ogenki.io_{factories,repositories,mergepolicies}.yaml` (generated) | CRDs |
| `charts/maku-factory/crds/` (copied by `crd:gen`) | the chart's CRD copies |
| `charts/maku-factory/templates/rbac.yaml`, `templates/clusterroles.yaml` (modify, create) | the factory's read access; aggregated admin, editor and viewer roles |
| `charts/maku-factory/tests/render.sh` (modify) | assertions for the above |
| `taskfile.yaml` (modify) | controller-gen paths and `allowDangerousTypes`, CRD copies |
| `internal/factory/resources/assemble.go` (create) | `Object`, `Refusal`, `Assemble` |
| `internal/factory/resources/assemble_test.go`, `testdata/file.yaml` (create) | its tests |
| `internal/factory/resources/migrate.go`, `migrate_test.go` (create) | `Migrate`: file → manifests + residual file |
| `internal/factory/resources/manifests.go`, `manifests_test.go` (create) | `Decode`: multi-document YAML → strictly checked `Object`s |
| `internal/app/maku_config.go`, `maku_config_test.go` (create); `internal/app/maku.go` (modify) | `maku config migrate` and `maku config validate` |
| `docs/merge-policies.md` (modify) | a short "Factory resources" section naming the two commands |

---

### Task 1: Deepcopy and schema markers for the types the CRs embed

**Files:**
- Modify: `internal/factory/config/classes.go`, `internal/factory/config/config.go`, `internal/factory/config/mergepolicy.go`
- Modify: `taskfile.yaml:76-90` (`crd:gen`)
- Generated: `internal/factory/config/zz_generated.deepcopy.go`
- Test: `internal/factory/config/deepcopy_test.go`

**Interfaces:**
- Produces: `DeepCopy()` / `DeepCopyInto()` on `Class`, `Author`, `Adopt`, `Monitor`, `VerificationWatch`, `FluxWatch`, `Oracle`, `Merge`, `Breaker`, `Duration`, `Schedule`, `Checks`, `Control`, `Defaults`, `Tier`, `Template`, `Caps`, `Facts`, `Brief`, `Budgets`, `PerPerson`, `Resume`, `Scoper`, `Loop`, `SplitPolicy`, `HarnessProfile`, `MergeGuardrails`, `Price`. These are the types task 2's specs embed. `Config` itself gets **no** deepcopy: it holds a func field (`classSource`).

- [ ] **Step 1: Write the failing test**

```go
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// The CRs embed these types (factory resources D16), so the API machinery deep-copies them: a
// copy shares no map, slice or pointer with its source.
func TestDeepCopySharesNothing(t *testing.T) {
	src := Class{Live: true, AllowedFiles: []string{"^docs/"},
		Monitor: &Monitor{Flux: &FluxWatch{Source: "flux-system/flux-system"}},
		Author:  &Author{Adopt: &Adopt{Logins: []string{"renovate[bot]"}}}}
	cp := src.DeepCopy()
	cp.AllowedFiles[0] = "x"
	cp.Monitor.Flux.Source = "x"
	cp.Author.Adopt.Logins[0] = "x"
	if src.AllowedFiles[0] != "^docs/" || src.Monitor.Flux.Source != "flux-system/flux-system" ||
		src.Author.Adopt.Logins[0] != "renovate[bot]" {
		t.Fatalf("the copy shares memory with its source: %+v", src)
	}
	l := Loop{Models: map[string]map[string]string{"reviewer": {"frontier": "kimi"}}}
	lc := l.DeepCopy()
	lc.Models["reviewer"]["frontier"] = "x"
	if l.Models["reviewer"]["frontier"] != "kimi" {
		t.Fatal("Loop.Models is shared")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/factory/config/ -run TestDeepCopySharesNothing`
Expected: FAIL to compile: `src.DeepCopy undefined`.

- [ ] **Step 3: Add the markers**

Above each type listed under **Produces**, add on its own line directly above `type`:

```go
// +kubebuilder:object:generate=true
```

On `Duration` (`config.go:72`), which marshals as a Go duration string, add these too, so the
schema is a bounded string rather than an object:

```go
// +kubebuilder:validation:Type=string
// +kubebuilder:validation:MaxLength=32
// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
```

In `taskfile.yaml`'s `crd:gen`, change the controller-gen line and the SPDX loop to cover config:

```yaml
      - go tool controller-gen object crd:allowDangerousTypes=true paths=./api/... paths=./internal/factory/config/... output:crd:dir=./config/crd
```

and add `internal/factory/config/zz_generated.deepcopy.go` to the `for f in …` list of files whose
first line the loop rewrites to the SPDX header.

- [ ] **Step 4: Generate, then run the test**

Run: `task crd:gen && go test ./internal/factory/config/ -run TestDeepCopySharesNothing`
Expected: PASS. `git status` shows the new `zz_generated.deepcopy.go` and no CRD for config (it has
no root types).

If controller-gen reports an unsupported field on one of the listed types, the field's type is
missing from the list: add the marker to that type too.

- [ ] **Step 5: Run the gate and commit**

Run: `task check`
Expected: exit 0.

```bash
git add internal/factory/config taskfile.yaml
git commit -m "feat(config): deepcopy and schema markers for the types the factory resources embed"
```

---

### Task 2: The `Factory`, `Repository` and `MergePolicy` kinds

**Files:**
- Create: `api/factory/v1alpha1/factoryconfig_types.go`
- Modify: `api/factory/v1alpha1/task_crd_test.go` (`bounded` learns maps)
- Create: `api/factory/v1alpha1/factoryconfig_crd_test.go`
- Generated: `config/crd/maku.ogenki.io_factories.yaml`, `maku.ogenki.io_repositories.yaml`, `maku.ogenki.io_mergepolicies.yaml`, `api/factory/v1alpha1/zz_generated.deepcopy.go`
- Modify (bounds markers, as the test demands): `internal/factory/config/classes.go`, `config.go`, `mergepolicy.go`

**Interfaces:**
- Consumes: task 1's deepcopy on the config types.
- Produces (package `v1alpha1`): `Factory{Spec FactorySpec; Status FactoryStatus}`, `Repository{Spec RepositorySpec; Status RepositoryStatus}`, `MergePolicy{Spec MergePolicySpec; Status MergePolicyStatus}` and their `…List`; constants `ConditionAccepted = "Accepted"` and the six reasons `ReasonNotThisFactory`, `ReasonDefinedTwice`, `ReasonMissingRef`, `ReasonNoMergePolicy`, `ReasonDuplicatePolicy`, `ReasonInvalid`.

- [ ] **Step 1: Teach `bounded` maps, and write the failing CRD test**

In `task_crd_test.go`, extend `bounded`'s `case "object":` so a map is bounded too:

```go
	case "object":
		for name, p := range s.Properties {
			bounded(t, path+"."+name, &p)
		}
		if ap := s.AdditionalProperties; ap != nil && ap.Schema != nil {
			if s.MaxProperties == nil {
				t.Errorf("%s: a map without maxProperties", path)
			}
			bounded(t, path+"{}", ap.Schema)
		}
```

Run `go test ./api/factory/v1alpha1/` to confirm the Task CRD still passes with the stricter helper
(it has no maps; if it fails, bound the map it names in `task_types.go`).

Create `factoryconfig_crd_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"os"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func readCRD(t *testing.T, plural string) (string, *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	raw, err := os.ReadFile("../../../config/crd/maku.ogenki.io_" + plural + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(raw, &crd); err != nil {
		t.Fatal(err)
	}
	return string(raw), &crd
}

// Factory resources D3, D7, D10, D11: namespaced, a status subresource, the printer columns, the
// fixed references, and live and shadow never together on one class.
func TestFactoryResourceCRDsCarryTheDesignRules(t *testing.T) {
	for plural, wants := range map[string][]string{
		"factories": {"kind: Factory", "scope: Namespaced", "subresources:", "name: Accepted", "name: Ready", "name: Repos", "name: Stopped"},
		"repositories": {"kind: Repository", "scope: Namespaced", "subresources:", "name: Repo", "name: Factory", "name: Accepted", "name: Connected",
			"spec.factoryRef is fixed once set", "spec.repository is fixed once set"},
		"mergepolicies": {"kind: MergePolicy", "scope: Namespaced", "subresources:", "name: Repo", "name: Authority", "name: Classes", "name: Live", "name: Accepted",
			"spec.repositoryRef is fixed once set", "a class is live or shadow, never both"},
	} {
		raw, _ := readCRD(t, plural)
		for _, w := range wants {
			if !strings.Contains(raw, w) {
				t.Errorf("%s CRD lacks %q", plural, w)
			}
		}
	}
}

// Review I3: CEL costs every rule against the declared bounds.
func TestFactoryResourceCRDsBoundEveryStringListAndMap(t *testing.T) {
	for _, plural := range []string{"factories", "repositories", "mergepolicies"} {
		_, crd := readCRD(t, plural)
		v := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
		spec, status := v.Properties["spec"], v.Properties["status"]
		bounded(t, plural+".spec", &spec)
		bounded(t, plural+".status", &status)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./api/factory/v1alpha1/ -run FactoryResource`
Expected: FAIL: `open ../../../config/crd/maku.ogenki.io_factories.yaml: no such file or directory`.

- [ ] **Step 3: Write the types**

Create `api/factory/v1alpha1/factoryconfig_types.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/internal/factory/config"
)

// The condition every factory resource carries, and the reasons it is false (factory resources
// D5, D8, D15).
const (
	ConditionAccepted = "Accepted"

	ReasonNotThisFactory  = "NotThisFactory"  // another factory's object (D15)
	ReasonDefinedTwice    = "DefinedTwice"    // the repository is also in the file (D8)
	ReasonMissingRef      = "MissingRef"      // a reference names no object
	ReasonNoMergePolicy   = "NoMergePolicy"   // a Repository no MergePolicy names
	ReasonDuplicatePolicy = "DuplicatePolicy" // two MergePolicies name one Repository
	ReasonInvalid         = "Invalid"         // the factory's own validation refused it
)

// FactoryTriage is the live part of triage (D12): the classifier's URL stays in the values.
type FactoryTriage struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ControlPercent int `json:"controlPercent,omitempty"`
}

// FactorySpec is the factory's catalogue (D3, D12), in today's config keys. prices is today's
// meter.prices: the meter's URL and queries stay in the values.
type FactorySpec struct {
	Control  config.Control  `json:"control,omitzero"`
	Defaults config.Defaults `json:"defaults,omitzero"`
	Triage   FactoryTriage   `json:"triage,omitzero"`
	// +kubebuilder:validation:MaxProperties=16
	Tiers map[string]config.Tier `json:"tiers,omitempty"`
	// +kubebuilder:validation:MaxProperties=16
	Templates map[string]config.Template `json:"templates,omitempty"`
	Caps      config.Caps                `json:"caps,omitzero"`
	Budgets   config.Budgets             `json:"budgets,omitzero"`
	Facts     config.Facts               `json:"facts,omitzero"`
	Brief     config.Brief               `json:"brief,omitzero"`
	Resume    config.Resume              `json:"resume,omitzero"`
	Scoper    config.Scoper              `json:"scoper,omitzero"`
	// +kubebuilder:validation:MaxProperties=32
	Loops map[string]config.Loop `json:"loops,omitempty"`
	// +kubebuilder:validation:MaxProperties=16
	HarnessProfiles map[string]config.HarnessProfile `json:"harnessProfiles,omitempty"`
	MergeGuardrails config.MergeGuardrails           `json:"mergeGuardrails,omitzero"`
	// +kubebuilder:validation:MaxProperties=32
	Prices map[string]config.Price `json:"prices,omitempty"`
}

// FactoryStatus is what the factory made of its Factory (D11, narrowed for v1).
type FactoryStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:validation:Minimum=0
	Repositories int  `json:"repositories,omitempty"`
	Stopped      bool `json:"stopped,omitempty"`
}

// Factory is one factory's catalogue (D3). Its name is config.factory, the fence's (D15).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Accepted,type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name=Ready,type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name=Repos,type=integer,JSONPath=`.status.repositories`
// +kubebuilder:printcolumn:name=Stopped,type=boolean,JSONPath=`.status.stopped`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type Factory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FactorySpec   `json:"spec,omitempty"`
	Status            FactoryStatus `json:"status,omitempty"`
}

// FactoryList is a list of Factory.
//
// +kubebuilder:object:root=true
type FactoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Factory `json:"items"`
}

// RepositorySpec is one repository served (D3): today's repositories[] entry less its classes
// and merge block, which are its MergePolicy's.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.factoryRef) || self.factoryRef == oldSelf.factoryRef",message="spec.factoryRef is fixed once set"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.repository) || self.repository == oldSelf.repository",message="spec.repository is fixed once set"
type RepositorySpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	FactoryRef string `json:"factoryRef"`
	// Repository is owner/name on GitHub.
	// +kubebuilder:validation:MaxLength=140
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	Repository string `json:"repository"`
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=39
	Maintainers []string `json:"maintainers,omitempty"`
	// +kubebuilder:validation:MaxLength=50
	TriggerLabel string        `json:"triggerLabel,omitempty"`
	Checks       config.Checks `json:"checks,omitzero"`
	// +kubebuilder:validation:MaxItems=16
	Schedules []config.Schedule `json:"schedules,omitempty"`
	// +kubebuilder:validation:Enum=central;repository
	ClassAuthority string `json:"classAuthority,omitempty"`
}

// RepositoryStatus is what the factory made of its Repository (D11, narrowed for v1).
type RepositoryStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// MergePolicy names the MergePolicy that applies.
	// +kubebuilder:validation:MaxLength=253
	MergePolicy string `json:"mergePolicy,omitempty"`
}

// Repository is one repository a Factory serves (D3).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Repo,type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name=Factory,type=string,JSONPath=`.spec.factoryRef`
// +kubebuilder:printcolumn:name=Accepted,type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name=Connected,type=string,JSONPath=`.status.conditions[?(@.type=="Connected")].status`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type Repository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RepositorySpec   `json:"spec"`
	Status            RepositoryStatus `json:"status,omitempty"`
}

// RepositoryList is a list of Repository.
//
// +kubebuilder:object:root=true
type RepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Repository `json:"items"`
}

// MergePolicySpec is one repository's classes and merge rules (D3, D4): today's
// repositories[].classes and merge, unchanged in shape.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.repositoryRef) || self.repositoryRef == oldSelf.repositoryRef",message="spec.repositoryRef is fixed once set"
type MergePolicySpec struct {
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	RepositoryRef string `json:"repositoryRef"`
	// +kubebuilder:validation:MaxProperties=32
	// +kubebuilder:validation:XValidation:rule="self.all(k, !((has(self[k].live) && self[k].live) && (has(self[k].shadow) && self[k].shadow)))",message="a class is live or shadow, never both"
	Classes map[string]config.Class `json:"classes,omitempty"`
	Merge   config.Merge            `json:"merge,omitzero"`
}

// ClassStatus is one class's mode and last decision (D10, narrowed for v1).
type ClassStatus struct {
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=live;shadow;unarmed
	Mode string `json:"mode"`
	// +kubebuilder:validation:MaxLength=63
	LastDecision   string       `json:"lastDecision,omitempty"`
	LastDecisionAt *metav1.Time `json:"lastDecisionAt,omitempty"`
}

// MergePolicyStatus is what the factory made of its MergePolicy and of the repository's file (D10).
type MergePolicyStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:validation:Enum=central;repository
	Authority string `json:"authority,omitempty"`
	// +kubebuilder:validation:Minimum=0
	ClassCount int `json:"classCount,omitempty"`
	// +kubebuilder:validation:Minimum=0
	LiveCount int `json:"liveCount,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	Classes []ClassStatus `json:"classes,omitempty"`
}

// MergePolicy is one repository's merge policy (D3, D4).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Repo,type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name=Authority,type=string,JSONPath=`.status.authority`
// +kubebuilder:printcolumn:name=Classes,type=integer,JSONPath=`.status.classCount`
// +kubebuilder:printcolumn:name=Live,type=integer,JSONPath=`.status.liveCount`
// +kubebuilder:printcolumn:name=Accepted,type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type MergePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MergePolicySpec   `json:"spec"`
	Status            MergePolicyStatus `json:"status,omitempty"`
}

// MergePolicyList is a list of MergePolicy.
//
// +kubebuilder:object:root=true
type MergePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MergePolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Factory{}, &FactoryList{}, &Repository{}, &RepositoryList{}, &MergePolicy{}, &MergePolicyList{})
}
```

Check `groupversion_info.go` and `task_types.go` for how `Task` registers itself. If it uses
`SchemeBuilder.Register` in an `init`, keep the `init` above. If it registers some other way, follow
that instead (`AGENTS.md` says no `init()` doing work; registration is the existing exception, so
match it exactly).

The printer column for `MergePolicy` names `spec.repositoryRef` (the `Repository` object), not
`owner/name`. That is a v1 simplification of the spec's `REPO` column; plan 3 may add a
`status.repository` and point the column at it.

- [ ] **Step 4: Generate, and bound what the test names**

Run: `task crd:gen && go test ./api/factory/v1alpha1/ -run FactoryResource`

`TestFactoryResourceCRDsBoundEveryStringListAndMap` will list each unbounded path, for example
`mergepolicies.spec.classes{}.allowedFiles: a list without maxItems`. For each one, add a marker
on the config field it names, in `internal/factory/config/`, using these values:

| Field kind | Marker |
|---|---|
| a name, login, label, model, tier, template, class, role | `+kubebuilder:validation:MaxLength=253` |
| a regular expression or file pattern | `+kubebuilder:validation:MaxLength=512` |
| a check name, a URL, a cron line, a source | `+kubebuilder:validation:MaxLength=1024` |
| a free text (`Schedule.Text`) | `+kubebuilder:validation:MaxLength=14336` (today's `caps.maxTextBytes`) |
| a list of strings | `+kubebuilder:validation:MaxItems=64` and `+kubebuilder:validation:items:MaxLength=…` per the rows above |
| a map | `+kubebuilder:validation:MaxProperties=32` |

A bound must never refuse what ai-native-ref's values file holds today: before committing,
check the longest value of each bounded field in
`git -C ~/Sources/ai-native-ref show origin/main:tooling/base/agent-factory/helm-values-configmap.yaml`.
Rerun `task crd:gen` and the test after each batch, until it passes.

- [ ] **Step 5: Prove CEL compiles within its cost budget**

The API server refuses a CRD whose CEL is too expensive, and the tests above only read YAML. Add
the CRDs to the envtest suite: find where envtest loads `config/crd` (`grep -rn "CRDDirectoryPaths" internal`).
If it loads the whole directory, the new CRDs are already installed. Then add:

```go
// The three factory-resource CRDs install: their CEL compiles within the API server's cost budget.
func TestFactoryResourceCRDsInstall(t *testing.T) { /* in the envtest package found above */ }
```

The test lists each CRD (`apiextensionsv1.CustomResourceDefinition`) by name
(`factories.maku.ogenki.io`, `repositories.maku.ogenki.io`, `mergepolicies.maku.ogenki.io`) from
the envtest API server and fails if any is missing. Then create a `MergePolicy` with one class that
has `live: true` and `shadow: true`, and assert the create fails with
`a class is live or shadow, never both`.

Run: `go test ./internal/... -run TestFactoryResourceCRDsInstall`
Expected: PASS.

- [ ] **Step 6: Run the gate and commit**

Run: `task check`
Expected: exit 0.

```bash
git add api config/crd internal/factory/config
git commit -m "feat(api): Factory, Repository and MergePolicy kinds with conditions and printer columns"
```

---

### Task 3: Ship the CRDs and the roles in the factory chart

**Files:**
- Modify: `taskfile.yaml` (`crd:gen` copies)
- Modify: `charts/maku-factory/templates/rbac.yaml`
- Create: `charts/maku-factory/templates/clusterroles.yaml`
- Modify: `charts/maku-factory/tests/render.sh`

**Interfaces:**
- Consumes: task 2's generated CRDs.
- Produces: ClusterRoles `maku-factory-admin`, `maku-mergepolicy-editor` and `maku-viewer` (D7). The factory's Role gains read access to the three kinds and write access to their status.

- [ ] **Step 1: Write the failing assertions**

Append to `charts/maku-factory/tests/render.sh`, before its final `exit`:

```bash
# Factory resources D7: the factory reads the three kinds and writes their status, nothing more.
ykind Role '.rules[] | select(.resources == ["factories","repositories","mergepolicies"]) | .verbs == ["get","list","watch"]' "the factory reads its resources"
ykind Role '.rules[] | select(.resources == ["factories/status","repositories/status","mergepolicies/status"]) | .verbs == ["get","update","patch"]' "the factory writes their status"
ykind ClusterRole 'select(.metadata.name == "maku-viewer") | .metadata.labels."rbac.authorization.k8s.io/aggregate-to-view" == "true"' "maku-viewer aggregates into view"
ykind ClusterRole 'select(.metadata.name == "maku-mergepolicy-editor") | .metadata.labels."rbac.authorization.k8s.io/aggregate-to-edit" == "true"' "maku-mergepolicy-editor aggregates into edit"
ykind ClusterRole 'select(.metadata.name == "maku-factory-admin") | .metadata.labels."rbac.authorization.k8s.io/aggregate-to-admin" == "true"' "maku-factory-admin aggregates into admin"
for crd in factories repositories mergepolicies; do
  [ -f "crds/maku.ogenki.io_${crd}.yaml" ] || { echo "FAIL: the chart ships the ${crd} CRD" >&2; fail=1; }
done
```

- [ ] **Step 2: Run it and see it fail**

Run: `bash charts/maku-factory/tests/render.sh`
Expected: FAIL lines for each new assertion.

- [ ] **Step 3: Implement**

In `taskfile.yaml` `crd:gen`, replace the factory chart's copy line with:

```yaml
      - cp config/crd/maku.ogenki.io_tasks.yaml config/crd/maku.ogenki.io_factories.yaml config/crd/maku.ogenki.io_repositories.yaml config/crd/maku.ogenki.io_mergepolicies.yaml charts/maku-factory/crds/
```

In `charts/maku-factory/templates/rbac.yaml`, after the `tasks/status` rule, add (same indentation
as its siblings):

```yaml
  - apiGroups: [maku.ogenki.io]
    resources: [factories, repositories, mergepolicies]
    verbs: [get, list, watch]
  - apiGroups: [maku.ogenki.io]
    resources: [factories/status, repositories/status, mergepolicies/status]
    verbs: [get, update, patch]
```

Create `charts/maku-factory/templates/clusterroles.yaml`:

```yaml
{{- /* Factory resources D7: the built-in admin, edit and view roles take these, as they take a Deployment's. */}}
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: maku-factory-admin
  labels:
    {{- include "maku-factory.labels" . | nindent 4 }}
    rbac.authorization.k8s.io/aggregate-to-admin: "true"
rules:
  - apiGroups: [maku.ogenki.io]
    resources: [factories, repositories, mergepolicies]
    verbs: [get, list, watch, create, update, patch, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: maku-mergepolicy-editor
  labels:
    {{- include "maku-factory.labels" . | nindent 4 }}
    rbac.authorization.k8s.io/aggregate-to-edit: "true"
rules:
  - apiGroups: [maku.ogenki.io]
    resources: [mergepolicies]
    verbs: [get, list, watch, create, update, patch, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: maku-viewer
  labels:
    {{- include "maku-factory.labels" . | nindent 4 }}
    rbac.authorization.k8s.io/aggregate-to-view: "true"
rules:
  - apiGroups: [maku.ogenki.io]
    resources: [factories, repositories, mergepolicies]
    verbs: [get, list, watch]
```

Check the labels helper's real name in `_helpers.tpl` (`grep define charts/maku-factory/templates/_helpers.tpl`)
and use it.

- [ ] **Step 4: Regenerate and run**

Run: `task crd:gen && bash charts/maku-factory/tests/render.sh && task chart:check`
Expected: no FAIL line, exit 0.

- [ ] **Step 5: Run the gate and commit**

Run: `task check`
Expected: exit 0.

```bash
git add taskfile.yaml charts/maku-factory
git commit -m "feat(chart): ship the factory-resource CRDs and their aggregated roles"
```

---

### Task 4: `resources.Assemble`

**Files:**
- Create: `internal/factory/resources/assemble.go`
- Create: `internal/factory/resources/testdata/file.yaml`
- Test: `internal/factory/resources/assemble_test.go`

**Interfaces:**
- Consumes: `config.Parse(raw []byte) (*config.Config, error)` (`internal/factory/config/config.go:1289`); the `v1alpha1.Reason*` constants (task 2).
- Produces:

```go
// Kind is one of the three resource kinds.
type Kind string
const (
	KindFactory     Kind = "Factory"
	KindRepository  Kind = "Repository"
	KindMergePolicy Kind = "MergePolicy"
)
// Object is one resource as the API server stores it: its spec raw, so a field the author
// omitted stays omitted and Parse's defaults apply exactly as they do to the file.
type Object struct {
	Kind       Kind
	Name       string
	Generation int64
	Spec       map[string]any
}
// Refusal is why an object is not served (Accepted=False).
type Refusal struct {
	Kind    Kind
	Name    string
	Reason  string // a v1alpha1.Reason* constant
	Message string
}
// Result is what Assemble made of the file and the objects.
type Result struct {
	Config   *config.Config
	Accepted []Object  // in the order they were added
	Refused  []Refusal // sorted by Kind, then Name
	Warnings []string  // e.g. the file's catalogue keys a Factory replaces
}
// Assemble builds the factory's config from the file and the objects (factory resources D5, D8, D15).
// It returns an error only when the file alone does not parse.
func Assemble(file []byte, objs []Object) (Result, error)
// CatalogueKeys are the top-level file keys a Factory replaces (D12), in the file's spelling.
var CatalogueKeys = []string{"control", "defaults", "tiers", "templates", "caps", "budgets", "facts", "brief", "resume", "scoper", "loops", "harnessProfiles", "mergeGuardrails"}
```

**Algorithm** (implement exactly this):

1. Decode `file` into `map[string]any` with `sigs.k8s.io/yaml.Unmarshal`. Call it `base`. Read `base["factory"].(string)` as `name`.
2. **Factory.** Of the objects of kind `Factory`, refuse each whose `Name != name` with `NotThisFactory`. If one is left, build a candidate: a copy of `base`. Delete every `CatalogueKeys` key from the copy, and add a warning for each key that was present: `"<key> in the file is ignored: Factory <name> sets the catalogue"`. Copy each key of its spec onto the candidate with these exceptions: `prices` goes to `meter.prices`, creating `meter` if absent; `triage` merges into `base["triage"]` key by key. Call `config.Parse` on the candidate (marshalled with `yaml.Marshal`). If it parses, the candidate becomes `base` and the Factory is accepted. If not, refuse it with `Invalid` and the error's text, and keep `base`.
3. **Repositories.** Index the `MergePolicy` objects by `spec.repositoryRef`. Any repositoryRef named by two or more policies: refuse all of them with `DuplicatePolicy`. Any that names no `Repository` object: refuse with `MissingRef`.
4. For each `Repository`, sorted by `Name`:
   - `spec.factoryRef != name` → `NotThisFactory`.
   - Its `spec.repository` equals (case-insensitively) a `name` in `base["repositories"]` that came from the file → `DefinedTwice`, and the file's entry stays.
   - No accepted `MergePolicy` names it → `NoMergePolicy`.
   - Otherwise build the entry: `name` = `spec.repository`, plus `maintainers`, `triggerLabel`, `checks`, `schedules` and `classAuthority` copied when present, plus `classes` and `merge` from its policy's spec when present. Append it to a copy of `base["repositories"]`, creating the list if absent, and `config.Parse` the result. On success, keep it: both the Repository and its MergePolicy are accepted. On failure, refuse the Repository **and** its MergePolicy with `Invalid` and the error text.
5. `config.Parse` the final `base` into `Result.Config` and return it. If the file alone does not parse (step 1, or step 5 with nothing accepted), return the error.

A legacy single-repository file (`repository:` at the top level) keeps working: `Parse` moves it
into `Repositories`. Step 4's `DefinedTwice` check must also compare against `base["repository"]`
when it is set.

- [ ] **Step 1: Write the fixture**

Create `testdata/file.yaml`. Copy the `good` constant from
`internal/factory/config/config_test.go:20-74`, then replace the legacy top-level
`repository`, `maintainers`, `triggerLabel`, `classes` and `merge` keys with one entry:

```yaml
repositories:
  - name: Smana/cloud-native-ref
    maintainers: [Smana]
    triggerLabel: factory/ready
    classes: {docs-links: {shadow: true}, revert: {shadow: true}, docs: {}, tests: {}, dashboards: {}}
    merge:
      # the same merge block as `good`, verbatim
```

Also delete `api.repositories`: with the `repositories:` shape, `Parse` derives it from
`repositories` and refuses it when set (`config.go:1418`). That is also why a Repository added by
`Assemble` is admitted by the API without further change.

- [ ] **Step 2: Write the failing tests**

```go
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
		"repositoryRef": ref, "classes": map[string]any{"docs": map[string]any{}}, "merge": mergeBlock(t)}}
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
	if rc == nil || len(rc.Classes) != 1 || rc.Merge.MergerLogin == "" || len(r.Accepted) != 2 {
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
			p.Spec["classes"] = map[string]any{"docs": map[string]any{"live": true, "shadow": true}}
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
			"light":    map[string]any{"model": "m", "runTokens": 1000, "taskTokens": 2000, "runMinutes": 5},
			"standard": map[string]any{"model": "m", "runTokens": 1000, "taskTokens": 2000, "runMinutes": 5},
			"frontier": map[string]any{"model": "m", "runTokens": 1000, "taskTokens": 2000, "runMinutes": 5}},
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
```

- [ ] **Step 3: Run them and see them fail**

Run: `go test ./internal/factory/resources/`
Expected: FAIL to compile: `undefined: Assemble`.

- [ ] **Step 4: Implement `assemble.go`**

Follow the algorithm above. Package comment:

```go
// Package resources turns the factory's Factory, Repository and MergePolicy objects, together
// with its config file, into the config.Config the factory reads (factory resources D5, D8, D15).
// It is pure: the controller (plan 2) and maku config validate both call it.
package resources
```

Helpers to write: `cloneMap(map[string]any) map[string]any` (deep, via `yaml.Marshal` then
`yaml.Unmarshal`, which is enough at these sizes), and
`parse(m map[string]any) (*config.Config, error)` (`yaml.Marshal`, then `config.Parse`). Sort
`Refused` with `slices.SortFunc` by `Kind` then `Name`.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/factory/resources/`
Expected: PASS.

- [ ] **Step 6: Run the gate and commit**

Run: `task check`
Expected: exit 0.

```bash
git add internal/factory/resources
git commit -m "feat(resources): assemble the factory's config from its file and resources"
```

---

### Task 5: `resources.Decode` and `resources.Migrate`

**Files:**
- Create: `internal/factory/resources/manifests.go`, `manifests_test.go`
- Create: `internal/factory/resources/migrate.go`, `migrate_test.go`

**Interfaces:**
- Consumes: `Object`, `Kind*`, `CatalogueKeys`, `Assemble` (task 4); `v1alpha1.Factory`, `Repository` and `MergePolicy` (task 2).
- Produces:

```go
// Decode reads multi-document YAML manifests of the three kinds. Each document is first decoded
// strictly into its typed kind, so an unknown field is refused here, before the API server would
// prune it (spec, "Validation"). Documents of other kinds are an error.
func Decode(raw []byte) ([]Object, error)

// Migrate moves a config file's catalogue and repositories into manifests (D8): it returns the
// manifests, as multi-document YAML in namespace ns, and the file with those keys removed.
// Keys move verbatim, so a defaulted field stays omitted.
func Migrate(file []byte, ns string) (manifests, residual []byte, err error)
```

**Migrate rules:**
- A file with the legacy top-level `repository:` key is refused with
  `"migrate: move repository, maintainers, triggerLabel, classes, merge and schedules into repositories[] first"`.
- `Factory`: `metadata.name` is the file's `factory` value. `spec` gets every `CatalogueKeys` key
  present, plus `meter.prices` as `prices` and `triage.controlPercent` as `triage.controlPercent`.
  The residual file keeps `meter` minus `prices` and `triage` minus `controlPercent`, and drops a
  key left empty.
- Each `repositories[]` entry becomes a `Repository` and a `MergePolicy`, both named by the
  repository's name part, lowercased (`Smana/cloud-native-ref` → `cloud-native-ref`). If two entries
  share a name part, both use `owner-name`, lowercased. `Repository.spec` gets `factoryRef`
  (= the factory's name), `repository` (= `name`), and `maintainers`, `triggerLabel`, `checks`,
  `schedules` and `classAuthority` when present. `MergePolicy.spec` gets `repositoryRef` and
  `classes` and `merge` when present. The residual file drops `repositories`.
- Output order: the Factory, then for each repository its Repository and then its MergePolicy,
  separated by `---\n`. Each document has `apiVersion: maku.ogenki.io/v1alpha1`, `kind` and
  `metadata: {name, namespace}`.

- [ ] **Step 1: Write the failing tests**

`migrate_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// D8: migrating, then assembling the manifests with the residual file, gives the config the file
// gave: same repositories, classes, merge rules and catalogue.
func TestMigrateRoundTrips(t *testing.T) {
	want, err := config.Parse(file(t))
	if err != nil {
		t.Fatal(err)
	}
	manifests, residual, err := Migrate(file(t), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatalf("Decode(Migrate(...)): %v\n%s", err, manifests)
	}
	got, err := Assemble(residual, objs)
	if err != nil || len(got.Refused) != 0 {
		t.Fatalf("%v %+v", err, got.Refused)
	}
	for _, k := range CatalogueKeys {
		if strings.Contains(string(residual), "\n"+k+":") {
			t.Errorf("the residual file still has %s", k)
		}
	}
	if !equalRepos(want.Repos(), got.Config.Repos()) || !equalJSON(t, want.Tiers, got.Config.Tiers) ||
		!equalJSON(t, want.Templates, got.Config.Templates) || !equalJSON(t, want.Caps, got.Config.Caps) ||
		!equalJSON(t, want.Budgets, got.Config.Budgets) || want.Triage != got.Config.Triage {
		t.Fatalf("the round trip changed the config\nmanifests:\n%s\nresidual:\n%s", manifests, residual)
	}
}

func TestMigrateRefusesTheLegacyShape(t *testing.T) {
	if _, _, err := Migrate([]byte("factory: aws-0\nrepository: Smana/x\n"), "ns"); err == nil ||
		!strings.Contains(err.Error(), "into repositories[] first") {
		t.Fatalf("err = %v", err)
	}
}
```

Add `equalRepos` (it compares `Name`, `Maintainers`, `TriggerLabel`, `Classes`, `Merge`,
`Schedules`, `Checks` and `ClassAuthority` through `equalJSON`, in order) and
`equalJSON(t, a, b any) bool` (it marshals both with `encoding/json` and compares the bytes) in
the test file.

`manifests_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"strings"
	"testing"
)

func TestDecodeRefusesUnknownFields(t *testing.T) {
	_, err := Decode([]byte(`apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: x, namespace: agent-system}
spec:
  repositoryRef: x
  classes: {docs: {live: true, typo: 1}}
`))
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("an unknown field must be refused, naming it: %v", err)
	}
}

func TestDecodeRefusesOtherKinds(t *testing.T) {
	if _, err := Decode([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\n")); err == nil {
		t.Fatal("a ConfigMap is not a factory resource")
	}
}

func TestDecodeKeepsTheRawSpec(t *testing.T) {
	objs, err := Decode([]byte(`apiVersion: maku.ogenki.io/v1alpha1
kind: Factory
metadata: {name: aws-0, namespace: agent-system, generation: 4}
spec: {caps: {activeTasks: 3}}
`))
	if err != nil || len(objs) != 1 {
		t.Fatal(err)
	}
	caps := objs[0].Spec["caps"].(map[string]any)
	if _, set := caps["concurrentRuns"]; set || objs[0].Generation != 4 || objs[0].Kind != KindFactory {
		t.Fatalf("Decode must keep an omitted field omitted: %+v", objs[0])
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/factory/resources/ -run 'Migrate|Decode'`
Expected: FAIL to compile: `undefined: Migrate`.

- [ ] **Step 3: Implement**

`Decode`: split on lines that are exactly `---`, skipping empty documents. For each document, read
`kind` and `metadata.name`/`metadata.generation` from a `map[string]any` (`sigs.k8s.io/yaml.Unmarshal`),
then `sigs.k8s.io/yaml.UnmarshalStrict` the document into `&v1alpha1.Factory{}`, `Repository{}` or
`MergePolicy{}` by kind and return its error wrapped with the kind and name. Keep the map's `spec`
as `Object.Spec`.

`Migrate`: follow the rules above, working on `map[string]any` throughout and emitting with
`sigs.k8s.io/yaml.Marshal`.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/factory/resources/`
Expected: PASS.

- [ ] **Step 5: Run the gate and commit**

Run: `task check`
Expected: exit 0.

```bash
git add internal/factory/resources
git commit -m "feat(resources): decode manifests strictly and migrate a config file into them"
```

---

### Task 6: `maku config migrate` and `maku config validate`

**Files:**
- Create: `internal/app/maku_config.go`, `internal/app/maku_config_test.go`
- Modify: `internal/app/maku.go` (`makuUsage` and the `switch` near line 175)
- Modify: `docs/merge-policies.md`

**Interfaces:**
- Consumes: `resources.Migrate`, `resources.Decode`, `resources.Assemble` (tasks 4, 5).
- Produces: `func (r Maku) config(args []string) error`, the CLI entry point.

Behaviour:

```text
maku config migrate --from config.yaml [--namespace agent-system] [--residual out.yaml]
    prints the manifests on stdout; writes the residual file to --residual when given
maku config validate --config config.yaml <manifest.yaml>...
    exit 0 and "ok: <n> accepted" when nothing is refused; otherwise one line per refusal,
    "<Kind>/<name>: <Reason>: <message>", then an error "config validate: <n> refused"
    warnings print as "warning: <text>" and never fail
```

- [ ] **Step 1: Write the failing tests**

Look at `internal/app/maku_policy_test.go` for how the CLI is driven in tests (a `Maku` value with
a buffer as stdout, `Run(ctx, args)`), and use the same harness. Then write:

```go
// SPDX-License-Identifier: Apache-2.0

package app

// TestMakuConfigMigrateThenValidate: the migrate output, with its residual, validates.
//   1. Write internal/factory/resources/testdata/file.yaml's bytes to t.TempDir()/config.yaml.
//   2. Run `config migrate --from <dir>/config.yaml --residual <dir>/residual.yaml`; capture stdout
//      into <dir>/manifests.yaml.
//   3. Run `config validate --config <dir>/residual.yaml <dir>/manifests.yaml`.
//   4. Assert no error and stdout contains "ok: 3 accepted".
//
// TestMakuConfigValidateReportsRefusals: a MergePolicy naming no Repository.
//   Run validate with the testdata file and a one-document manifest
//   {kind: MergePolicy, metadata: {name: orphan}, spec: {repositoryRef: nothing}}.
//   Assert the error text is "config validate: 1 refused" and stdout contains
//   "MergePolicy/orphan: MissingRef".
//
// TestMakuConfigValidateRefusesUnknownFields: a manifest with spec.classes.docs.typo set.
//   Assert the error names "typo".
```

Write these three as real Go tests. The comments give the exact inputs and assertions. Build the
YAML inputs as string constants in the test file.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/app/ -run MakuConfig`
Expected: FAIL: `unknown command config` (or a compile error).

- [ ] **Step 3: Implement**

Add `case "config": return r.config(rest)` beside `case "policy":` in `maku.go`, and add both
command lines to `makuUsage` in the same style as the `policy` lines. In `maku_config.go`, follow
`maku_policy.go`'s shape: a `flag.FlagSet` per verb, the existing `flags(fs, args)` helper, errors
wrapped with the verb, output through `r`'s stdout writer (never `fmt.Print*`; `forbidigo` bans
it).

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/app/ -run MakuConfig`
Expected: PASS.

- [ ] **Step 5: Document**

In `docs/merge-policies.md`, add a section `## Factory resources (preview)` of at most 15 lines. It
says that the `Factory`, `Repository` and `MergePolicy` kinds ship in the chart, that the factory
does not read them yet, and how to run the two commands, with one example invocation of each.
Follow the page's existing voice and its terms. Run `task docs`.

- [ ] **Step 6: Run the gate and commit**

Run: `task check`
Expected: exit 0.

```bash
git add internal/app docs/merge-policies.md
git commit -m "feat(cli): maku config migrate and validate for the factory resources"
```

---

## Self-review (done while writing)

- **Spec coverage, plan 1's share:** D3 (kinds, group, namespaced, status, conditions) → task 2.
  D7 (roles) → task 3. D8 (migrate, `DefinedTwice`, the catalogue ignored while a Factory is
  accepted) → tasks 4 and 5. D12 (live keys in `CatalogueKeys`, `triage.classifierURL` and the
  meter URLs kept in the file) → tasks 4 and 5. D15 → task 4. D16 → tasks 1 and 2. "Validation":
  CEL → task 2, the reconciler's checks → task 4, `maku config validate` → task 6. The
  `NoMergePolicy` rule → task 4. Everything else (D5's live publish, D9, D10, D11, D13, D14) is
  in plans 2 and 3.
- **Known v1 simplification:** the `MergePolicy` `Repo` column shows `spec.repositoryRef`. Noted in
  task 2.
- **Types:** `Object`, `Refusal`, `Result`, `Kind*`, `CatalogueKeys`, `Assemble`, `Decode` and
  `Migrate` are named the same everywhere they appear.
