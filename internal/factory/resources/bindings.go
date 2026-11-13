// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// BindingKey names one binding: the Repository object's name and the policy's (the class's, B1).
type BindingKey struct{ Repository, Name string }

// ResolvedClass is a binding's resolved class and the generation of the policy it came from.
type ResolvedClass struct {
	Class            config.Class
	PolicyGeneration int64
}

// Options are what the caller keeps between calls.
type Options struct {
	// LastResolved is each binding's last resolved class (D5): put back when the binding no
	// longer resolves, so its class keeps serving while the factory runs (B9). None after a
	// restart (D14).
	LastResolved map[BindingKey]ResolvedClass
}

// BindingOutcome is what Assemble made of one binding of a served Repository (B9, B10).
type BindingOutcome struct {
	Repository, Name, Mode string // the Repository object's name, the policy's, the binding's mode
	// PolicyGeneration is the generation of the policy the served class came from: the current
	// one when Accepted, the last resolved one's when only Served; 0 when not served.
	PolicyGeneration int64
	Accepted         bool   // the binding resolved against the policy served now
	Reason, Message  string // why not, when !Accepted: config.Reason* or v1alpha1.ReasonMissingRef
	Served           bool   // a class is served for it, this one's or the last resolved
}

// resolution is what one assembly made of the class-shape policies and the bindings.
type resolution struct {
	policies map[string]config.ServedPolicy
	accepted []Object  // the class policies accepted
	refused  []Refusal // the class policies refused alone
	bindings []BindingOutcome
	resolved map[BindingKey]ResolvedClass
}

// legacyPolicy reports whether a MergePolicy is in the per-repository shape (B8).
func legacyPolicy(o Object) bool {
	_, ok := o.Spec["repositoryRef"]
	return ok
}

// boundRepository reports whether a Repository holds its own merge settings or bindings (B2, B3):
// it is then served alone, never paired with a MergePolicy.
func boundRepository(o Object) bool {
	_, merge := o.Spec["merge"]
	_, bindings := o.Spec["mergePolicies"]
	return merge || bindings
}

// policyContext reads what resolving reads from the merged catalogue, strictly: a key that does not
// decode leaves every binding unresolved, and Parse's culprit search refuses its Factory (plan PR7).
func policyContext(m map[string]any) (config.PolicyContext, error) {
	var ctx config.PolicyContext
	for _, f := range []struct {
		key string
		dst any
	}{{"harnessProfiles", &ctx.HarnessProfiles}, {"mergeGuardrails", &ctx.Guardrails}} {
		if v, ok := m[f.key]; ok && v != nil {
			if err := decodeStrict(v, f.dst); err != nil {
				return config.PolicyContext{}, fmt.Errorf("%s: %w", f.key, err)
			}
		}
	}
	return ctx, nil
}

// decodeStrict decodes a raw value into dst, refusing an unknown field.
func decodeStrict(v, dst any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// judgePolicies validates each class-shape policy alone (B9): one that fails is refused Invalid,
// and the caller's D5 puts its last accepted generation back for every consumer.
func judgePolicies(policies []Object, ctx config.PolicyContext, res *resolution) {
	res.policies = map[string]config.ServedPolicy{}
	for _, p := range policies {
		var spec config.PolicySpec
		var problems []string
		if err := decodeStrict(p.Spec, &spec); err != nil {
			problems = []string{err.Error()}
		} else {
			problems = config.ValidatePolicy(p.Name, spec, ctx)
		}
		if len(problems) > 0 {
			res.refused = append(res.refused, Refusal{Kind: KindMergePolicy, Name: p.Name, Reason: v1alpha1.ReasonInvalid,
				Message: strings.Join(problems, "\n")})
			continue
		}
		res.policies[p.Name] = config.ServedPolicy{Spec: spec, Generation: p.Generation}
		res.accepted = append(res.accepted, p)
	}
}

// resolveBindings resolves one bound Repository's bindings, in list order (B4, B6, B9, B14). It
// returns the served classes; the outcomes and the newly resolved classes go to res. A binding
// that does not resolve is dropped, or its last resolved class put back. Bindings that do not
// decode are left to Parse, which refuses the Repository.
func resolveBindings(r Object, mergeDefaults map[string]any, ctx config.PolicyContext, refusedPolicies map[string]bool,
	opts Options, res *resolution) map[string]config.Class {
	var bindings []config.Binding
	if raw, ok := r.Spec["mergePolicies"]; ok && raw != nil {
		if err := decodeStrict(raw, &bindings); err != nil {
			return nil
		}
	}
	rm, _ := r.Spec["merge"].(map[string]any)
	full, _ := config.ApplyMergeDefaults(mergeDefaults, rm)
	var merge config.Merge
	_ = json.Unmarshal(mustJSON(full), &merge) // a merge that does not decode is Parse's to refuse

	type entry struct {
		out config.BindingRefusal
		cl  config.Class
		gen int64
		ok  bool
	}
	entries := make([]entry, len(bindings))
	for i, b := range bindings {
		p, ok := res.policies[b.Name]
		switch {
		case !ok && refusedPolicies[b.Name]:
			entries[i].out = config.BindingRefusal{Reason: v1alpha1.ReasonMissingRef, Message: fmt.Sprintf("MergePolicy %s is refused, and none was served before", b.Name)}
		case !ok:
			entries[i].out = config.BindingRefusal{Reason: v1alpha1.ReasonMissingRef, Message: fmt.Sprintf("no MergePolicy %s", b.Name)}
		default:
			cl, refusal := config.Resolve(b.Name, p.Spec, b, ctx, merge)
			if refusal != nil {
				entries[i].out = *refusal
				break
			}
			entries[i].cl, entries[i].gen, entries[i].ok = cl, p.Generation, true
		}
	}
	// B6, plan PR9: the later of two ambiguous adopted bindings is refused, whatever its mode.
	for i := range entries {
		if !entries[i].ok {
			continue
		}
		for j := range i {
			if entries[j].ok && ambiguous(entries[j].cl, entries[i].cl) {
				entries[i].ok = false
				entries[i].out = config.BindingRefusal{Reason: config.ReasonAmbiguousAdoption, Message: fmt.Sprintf(
					"class %s adopts the pull requests class %s adopts: the same diffShape %s, an overlapping login and branch prefix; bind one of them (B6)",
					bindings[i].Name, bindings[j].Name, entries[i].cl.DiffShape)}
				break
			}
		}
	}

	classes := map[string]config.Class{}
	for i, e := range entries {
		if e.ok {
			classes[bindings[i].Name] = e.cl
		}
	}
	for i, b := range bindings {
		e := entries[i]
		k := BindingKey{r.Name, b.Name}
		o := BindingOutcome{Repository: r.Name, Name: b.Name, Mode: b.Mode}
		if e.ok {
			o.Accepted, o.Served, o.PolicyGeneration = true, true, e.gen
			if res.resolved == nil {
				res.resolved = map[BindingKey]ResolvedClass{}
			}
			res.resolved[k] = ResolvedClass{Class: e.cl, PolicyGeneration: e.gen}
		} else {
			o.Reason, o.Message = e.out.Reason, e.out.Message
			// D5: the last resolved class, unless it is the ambiguity refused, or would make one.
			if last, ok := opts.LastResolved[k]; ok && e.out.Reason != config.ReasonAmbiguousAdoption && !ambiguousWithAny(last.Class, classes) {
				classes[b.Name] = *last.Class.DeepCopy()
				o.Served, o.PolicyGeneration = true, last.PolicyGeneration
			}
		}
		res.bindings = append(res.bindings, o)
	}
	return classes
}

func ambiguousWithAny(cl config.Class, classes map[string]config.Class) bool {
	for _, other := range classes {
		if ambiguous(cl, other) {
			return true
		}
	}
	return false
}

// ambiguous reports whether two adopted classes could both adopt one pull request (B6, plan PR9):
// the same diffShape, a login in common (GitHub logins compare case-insensitively), and a branch
// prefix of one that is a prefix of one of the other's.
func ambiguous(a, b config.Class) bool {
	if !a.Adopted() || !b.Adopted() || a.DiffShape != b.DiffShape {
		return false
	}
	login := slices.ContainsFunc(a.Author.Adopt.Logins, func(x string) bool {
		return slices.ContainsFunc(b.Author.Adopt.Logins, func(y string) bool { return strings.EqualFold(x, y) })
	})
	branch := slices.ContainsFunc(a.Author.Adopt.Branches, func(x string) bool {
		return slices.ContainsFunc(b.Author.Adopt.Branches, func(y string) bool {
			x, y := strings.TrimPrefix(x, "^"), strings.TrimPrefix(y, "^")
			return strings.HasPrefix(x, y) || strings.HasPrefix(y, x)
		})
	})
	return login && branch
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
