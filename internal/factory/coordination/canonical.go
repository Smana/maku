// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// Canonical is the plan's canonical JSON (MR12): compact, object keys sorted, HTML unescaped,
// children sorted by id, artifacts by name, dependency and consumer edges sorted; scope.plan, the
// role list and mergeOrder keep their order because their order is meaning. A null, an empty list
// and an absent field are the same bytes, and a nil beforeDependents is its default, true. It
// covers only typed fields: a plan has no timestamp, diagnostic or comment to leak in. p is not
// modified.
func Canonical(p v1alpha1.CoordinationPlan) ([]byte, error) {
	return canonicalJSON(normalize(p))
}

// PlanHash is the lowercase hex SHA-256 of Canonical(p): id, version, title, primary, every child,
// dependencies, integration and budget.
func PlanHash(p v1alpha1.CoordinationPlan) string {
	c, err := Canonical(p)
	if err != nil {
		// Canonical marshals strings, integers, bools and their slices: no input fails. Reaching this is
		// a programming error, and a hash of nothing would be a plan that "holds".
		panic(fmt.Sprintf("coordination: hash a plan: %v", err))
	}
	return digest(c)
}

// ChildFingerprint is what childID's maintainer authorizes (MR5, MR12): the family id, the child's
// repository, maintainer, scope, class, tier, roles, allocation, produced and consumed artifacts
// (with the consumed artifact's own spec), dependencies and merge position. Rationales, the title,
// the version and the family-level integration and budget are outside it: they move no child's
// authority. An absent child has no fingerprint, "".
func ChildFingerprint(p v1alpha1.CoordinationPlan, childID string) string {
	n := normalize(p)
	i := slices.IndexFunc(n.Children, func(c v1alpha1.CoordinationChild) bool { return c.ID == childID })
	if i < 0 {
		return ""
	}
	c := n.Children[i]
	c.Rationale = ""
	type consumed struct {
		From     string                 `json:"from"`
		Artifact string                 `json:"artifact"`
		Spec     *v1alpha1.ArtifactSpec `json:"spec,omitempty"`
	}
	in := make([]consumed, 0, len(c.Consumes))
	for _, e := range c.Consumes {
		in = append(in, consumed{From: e.From, Artifact: e.Artifact, Spec: producedBy(n, e.From, e.Artifact)})
	}
	b, err := canonicalJSON(struct {
		Plan          string                     `json:"plan"`
		Child         v1alpha1.CoordinationChild `json:"child"`
		MergePosition int                        `json:"mergePosition"`
		Consumed      []consumed                 `json:"consumed"`
	}{n.ID, c, slices.Index(n.MergeOrder, childID), in})
	if err != nil {
		panic(fmt.Sprintf("coordination: fingerprint a child: %v", err))
	}
	return digest(b)
}

// producedBy is the artifact name child from produces in p, or nil.
func producedBy(p v1alpha1.CoordinationPlan, from, name string) *v1alpha1.ArtifactSpec {
	for _, c := range p.Children {
		if c.ID != from {
			continue
		}
		for i := range c.Produces {
			if c.Produces[i].Name == name {
				return &c.Produces[i]
			}
		}
	}
	return nil
}

// normalize is a deep copy of p in canonical order with its defaults applied.
func normalize(p v1alpha1.CoordinationPlan) v1alpha1.CoordinationPlan {
	n := *p.DeepCopy()
	if n.Integration.BeforeDependents == nil {
		t := true
		n.Integration.BeforeDependents = &t
	}
	slices.SortStableFunc(n.Children, func(a, b v1alpha1.CoordinationChild) int { return cmp.Compare(a.ID, b.ID) })
	for i := range n.Children {
		c := &n.Children[i]
		slices.SortStableFunc(c.Produces, func(a, b v1alpha1.ArtifactSpec) int { return cmp.Compare(a.Name, b.Name) })
		slices.Sort(c.DependsOn)
		slices.SortStableFunc(c.Consumes, func(a, b v1alpha1.ArtifactConsumer) int {
			return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.Artifact, b.Artifact))
		})
	}
	return n
}

// canonicalJSON marshals v, then re-encodes it as a tree: encoding/json sorts map keys, so every
// object's keys come out sorted whatever the struct order. Numbers stay json.Number, so an int64
// above 2^53 keeps its digits.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("re-read: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(prune(tree)); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// prune drops nulls, empty lists and empty objects from object members: each is the same promise
// as the absent field.
func prune(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			x = prune(x)
			if empty(x) {
				delete(t, k)
				continue
			}
			t[k] = x
		}
		return t
	case []any:
		for i := range t {
			t[i] = prune(t[i])
		}
		return t
	default:
		return v
	}
}

func empty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
