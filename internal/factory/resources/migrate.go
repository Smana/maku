// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
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
	parts := map[string]int{}
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			parts[namePart(str(m, "name"))]++
		}
	}
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("migrate: repositories[] entry is not a map: %v", e)
		}
		repository := str(m, "name")
		name := namePart(repository)
		if parts[name] > 1 {
			name = strings.ToLower(strings.ReplaceAll(repository, "/", "-"))
		}
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

// namePart is the repository's name without its owner, lowercased.
func namePart(repository string) string {
	_, n, ok := strings.Cut(repository, "/")
	if !ok {
		n = repository
	}
	return strings.ToLower(n)
}
