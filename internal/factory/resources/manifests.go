// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
)

// Decode reads multi-document YAML manifests of the three kinds. Each document is first decoded
// strictly into its typed kind, so an unknown field is refused here, before the API server would
// prune it (spec, "Validation"). Documents of other kinds are an error.
func Decode(raw []byte) ([]Object, error) {
	var objs []Object
	for i, doc := range splitDocuments(string(raw)) {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			return nil, fmt.Errorf("document %d: %w", i+1, err)
		}
		if len(m) == 0 {
			continue
		}
		kind, _ := m["kind"].(string)
		meta, _ := m["metadata"].(map[string]any)
		name := str(meta, "name")
		var typed any
		switch Kind(kind) {
		case KindFactory:
			typed = &v1alpha1.Factory{}
		case KindRepository:
			typed = &v1alpha1.Repository{}
		case KindMergePolicy:
			typed = &v1alpha1.MergePolicy{}
		default:
			return nil, fmt.Errorf("document %d: kind %q is not a factory resource", i+1, kind)
		}
		if err := yaml.UnmarshalStrict([]byte(doc), typed); err != nil {
			return nil, fmt.Errorf("%s %q: %w", kind, name, err)
		}
		var gen int64
		if g, ok := meta["generation"].(float64); ok {
			gen = int64(g)
		}
		spec, _ := m["spec"].(map[string]any)
		objs = append(objs, Object{Kind: Kind(kind), Name: name, Generation: gen, Spec: spec})
	}
	return objs, nil
}

// splitDocuments splits on lines that are exactly "---" (surrounding whitespace allowed).
func splitDocuments(s string) []string {
	var docs []string
	var cur []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "---" {
			docs = append(docs, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return append(docs, strings.Join(cur, "\n"))
}
