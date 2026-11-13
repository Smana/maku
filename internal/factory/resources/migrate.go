// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

// Migrate moves a config file's catalogue and repositories into manifests (D8): it returns the
// manifests, as multi-document YAML in namespace ns, and the file with those keys removed.
// Keys move verbatim, so a defaulted field stays omitted. Each repository's classes and merge are
// split into class-shape MergePolicies and the Repository's bindings (Split, B17). A file still in
// the single-repository shape, which the factory refuses, is read as its one-entry list first (R7).
func Migrate(file []byte, ns string) (manifests, residual []byte, err error) {
	if file, err = LiftLegacy(file); err != nil {
		return nil, nil, err
	}
	var base map[string]any
	if err := yaml.Unmarshal(file, &base); err != nil {
		return nil, nil, fmt.Errorf("config file: %w", err)
	}
	if base == nil {
		base = map[string]any{}
	}
	// Parse's refusals (an empty factory name, an entry without a name) are Migrate's: what it
	// would split must already be a valid file.
	if _, err := config.Parse(file); err != nil {
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	factory := str(base, "factory")

	spec := map[string]any{}
	for _, k := range CatalogueKeys {
		if v, ok := base[k]; ok {
			spec[k] = v
			delete(base, k)
		}
	}
	if meter, ok := base["meter"].(map[string]any); ok {
		if v, ok := meter["prices"]; ok {
			spec["prices"] = v
			delete(meter, "prices")
			if len(meter) == 0 {
				delete(base, "meter")
			}
		}
	}
	if triage, ok := base["triage"].(map[string]any); ok {
		if v, ok := triage["controlPercent"]; ok {
			spec["triage"] = map[string]any{"controlPercent": v}
			delete(triage, "controlPercent")
			if len(triage) == 0 {
				delete(base, "triage")
			}
		}
	}

	// YAML 1.1 reads a bare off as false; the CRD wants the string.
	offFields(spec, func(_ string, row map[string]any, key string) {
		if b, ok := row[key].(bool); ok && !b {
			row[key] = "off"
		}
	})

	objs := []Object{{Kind: KindFactory, Name: factory, Namespace: ns, Spec: spec}}

	entries, _ := base["repositories"].([]any)
	delete(base, "repositories")
	var repoNames []string
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			repoNames = append(repoNames, str(m, "name"))
		}
	}
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("migrate: repositories[] entry is not a map: %v", e)
		}
		repository := str(m, "name")
		name := ObjectName(repository, repoNames)
		rs := map[string]any{"factoryRef": factory, "repository": repository}
		for _, k := range []string{"maintainers", "triggerLabel", "checks", "schedules", "classAuthority"} {
			if v, ok := m[k]; ok {
				rs[k] = v
			}
		}
		ps := map[string]any{"repositoryRef": name}
		for _, k := range []string{"classes", "merge"} {
			if v, ok := m[k]; ok {
				ps[k] = v
			}
		}
		objs = append(objs, Object{Kind: KindRepository, Name: name, Namespace: ns, Spec: rs},
			Object{Kind: KindMergePolicy, Name: name, Namespace: ns, Spec: ps})
	}
	if objs, err = Split(objs, ns); err != nil {
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	if manifests, err = Encode(objs); err != nil {
		return nil, nil, err
	}
	residual, err = yaml.Marshal(base)
	if err != nil {
		return nil, nil, err
	}
	return manifests, residual, nil
}

// LiftLegacy is file in the repositories shape: a file still in the single-repository shape (v2
// D1), which the factory refuses (R7), as the one-entry list it stood for, and any other file as is.
// maku config migrate reads the old file through it.
func LiftLegacy(file []byte) ([]byte, error) {
	var base map[string]any
	if err := yaml.Unmarshal(file, &base); err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	if _, legacy := base["repository"]; !legacy {
		return file, nil
	}
	if err := liftLegacy(base); err != nil {
		return nil, err
	}
	return yaml.Marshal(base)
}

// liftLegacy rewrites, in place, the single-repository shape (v2 D1) that the factory no longer
// reads (v4 step 8, R7) as the one-entry repositories it stood for: the per-repository keys move
// into the entry, controlIssue becomes control on that repository, and api.repositories goes, as
// the list derives it. It refuses an allowlist naming another repository, which the list cannot say.
func liftLegacy(base map[string]any) error {
	name, _ := base["repository"].(string)
	if _, ok := base["repositories"]; ok {
		return errors.New("migrate: repository is the single-repository shape: write repositories or repository, never both")
	}
	entry := map[string]any{"name": name}
	for _, k := range []string{"maintainers", "triggerLabel", "classes", "merge", "schedules"} {
		if v, ok := base[k]; ok {
			entry[k] = v
			delete(base, k)
		}
	}
	delete(base, "repository")
	base["repositories"] = []any{entry}
	if n, ok := base["controlIssue"]; ok {
		if _, set := base["control"]; set {
			return errors.New("migrate: controlIssue and control both set: keep one")
		}
		base["control"] = map[string]any{"repository": name, "issue": n}
		delete(base, "controlIssue")
	}
	if api, ok := base["api"].(map[string]any); ok {
		if list, ok := api["repositories"].([]any); ok {
			for _, r := range list {
				if s, _ := r.(string); !strings.EqualFold(s, name) {
					return fmt.Errorf("migrate: api.repositories names %v, which a repositories list cannot admit: serve it from its own Repository", r)
				}
			}
			delete(api, "repositories")
		}
	}
	return nil
}

// ObjectName is the name Migrate gives repository's Repository and MergePolicy objects: its name
// part, or the whole name when another of repositories shares that part.
func ObjectName(repository string, repositories []string) string {
	n := namePart(repository)
	for _, o := range repositories {
		if o != repository && namePart(o) == n {
			return dnsLabel(repository)
		}
	}
	return n
}

// MergePolicyManifest is the YAML of a MergePolicy named name in ns, for the Repository object
// repositoryRef, holding classes and merge as given (empty ones are left out).
func MergePolicyManifest(name, ns, repositoryRef string, classes, merge any) ([]byte, error) {
	spec := map[string]any{"repositoryRef": repositoryRef}
	for k, v := range map[string]any{"classes": classes, "merge": merge} {
		if v == nil {
			continue
		}
		if rv := reflect.ValueOf(v); rv.IsZero() || ((rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice) && rv.Len() == 0) {
			continue
		}
		spec[k] = v
	}
	return yaml.Marshal(map[string]any{
		"apiVersion": "maku.ogenki.io/v1alpha1",
		"kind":       string(KindMergePolicy),
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	})
}

// RepositoryManifest is the YAML of a Repository named name in ns with spec as given (B2, B3).
func RepositoryManifest(name, ns string, spec map[string]any) ([]byte, error) {
	return yaml.Marshal(map[string]any{
		"apiVersion": "maku.ogenki.io/v1alpha1",
		"kind":       string(KindRepository),
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	})
}

// PolicyManifest is the YAML of a class-shape MergePolicy named name in ns (B1, B7): spec as JSON
// writes it, so an omitted field stays omitted.
func PolicyManifest(name, ns string, spec config.PolicySpec) ([]byte, error) {
	m, err := toMap(spec)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(map[string]any{
		"apiVersion": "maku.ogenki.io/v1alpha1",
		"kind":       string(KindMergePolicy),
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       m,
	})
}

// namePart is the repository's name without its owner, as a DNS-1123 label.
func namePart(repository string) string {
	_, n, ok := strings.Cut(repository, "/")
	if !ok {
		n = repository
	}
	return dnsLabel(n)
}

// dnsLabel lowercases s, turns '/', '_' and '.' into '-' and trims '-' from both ends: a GitHub
// owner or repository name is then a DNS-1123 label.
func dnsLabel(s string) string {
	return strings.Trim(strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(strings.ToLower(s)), "-")
}

// offFields visits loops.*.scope and harnessProfiles.*.loop where set, in name order: the two
// fields where YAML 1.1 reads a bare off as the boolean false.
func offFields(spec map[string]any, visit func(path string, row map[string]any, key string)) {
	for _, f := range []struct{ table, key string }{{"loops", "scope"}, {"harnessProfiles", "loop"}} {
		rows, _ := spec[f.table].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(rows)) {
			if row, ok := rows[name].(map[string]any); ok {
				if _, set := row[f.key]; set {
					visit(fmt.Sprintf("spec.%s.%s.%s", f.table, name, f.key), row, f.key)
				}
			}
		}
	}
}
