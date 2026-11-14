// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
)

// ErrPerRepositoryRemoved is why a MergePolicy with spec.repositoryRef is refused (B8, plan PR20):
// the shape is gone, and only the previous release converts it.
const ErrPerRepositoryRemoved = "the per-repository MergePolicy was removed in this release: maku config migrate --manifests (from the previous release)"

// Decode reads multi-document YAML manifests of the three kinds. Each document is first decoded
// strictly into its typed kind, so an unknown field is refused here, before the API server would
// prune it (spec, "Validation"); a per-repository MergePolicy is refused with
// ErrPerRepositoryRemoved. Documents of other kinds are an error.
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
		if v, _ := m["apiVersion"].(string); v != v1alpha1.GroupVersion.String() {
			return nil, fmt.Errorf("%s %q: apiVersion %q is not %s", kind, name, v, v1alpha1.GroupVersion)
		}
		spec, _ := m["spec"].(map[string]any)
		if _, legacy := spec["repositoryRef"]; legacy && Kind(kind) == KindMergePolicy {
			return nil, fmt.Errorf("%s %q: %s", kind, name, ErrPerRepositoryRemoved)
		}
		if err := yaml.UnmarshalStrict([]byte(doc), typed); err != nil {
			return nil, fmt.Errorf("%s %q: %w", kind, name, err)
		}
		var offErr error
		offFields(spec, func(path string, row map[string]any, key string) {
			if _, ok := row[key].(bool); ok && offErr == nil {
				offErr = fmt.Errorf("%s %q: %s must be a string such as \"off\", not a boolean", kind, name, path)
			}
		})
		if offErr != nil {
			return nil, offErr
		}
		var gen int64
		if g, ok := meta["generation"].(float64); ok {
			gen = int64(g)
		}
		objs = append(objs, Object{Kind: Kind(kind), Name: name, Namespace: str(meta, "namespace"), Generation: gen, Spec: spec})
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
