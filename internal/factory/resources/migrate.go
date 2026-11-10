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
// Keys move verbatim, so a defaulted field stays omitted.
func Migrate(file []byte, ns string) (manifests, residual []byte, err error) {
	var base map[string]any
	if err := yaml.Unmarshal(file, &base); err != nil {
		return nil, nil, fmt.Errorf("config file: %w", err)
	}
	if base == nil {
		base = map[string]any{}
	}
	if _, legacy := base["repository"]; legacy {
		return nil, nil, errors.New("migrate: move repository, maintainers, triggerLabel, classes, merge and schedules into repositories[] first")
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

	var docs [][]byte
	add := func(kind, name string, spec map[string]any) error {
		b, err := yaml.Marshal(map[string]any{
			"apiVersion": "maku.ogenki.io/v1alpha1",
			"kind":       kind,
			"metadata":   map[string]any{"name": name, "namespace": ns},
			"spec":       spec,
		})
		docs = append(docs, b)
		return err
	}
	if err := add(string(KindFactory), factory, spec); err != nil {
		return nil, nil, err
	}

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
		if err := add(string(KindRepository), name, rs); err != nil {
			return nil, nil, err
		}
		if err := add(string(KindMergePolicy), name, ps); err != nil {
			return nil, nil, err
		}
	}

	residual, err = yaml.Marshal(base)
	if err != nil {
		return nil, nil, err
	}
	return []byte(strings.Join(func() []string {
		s := make([]string, len(docs))
		for i, d := range docs {
			s[i] = string(d)
		}
		return s
	}(), "---\n")), residual, nil
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
