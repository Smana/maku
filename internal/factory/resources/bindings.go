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
	catalog  []string  // the catalog's policies, accepted or refused, by name (MR2)
	accepted []Object  // the class policies accepted
	refused  []Refusal // the class policies refused alone
	bindings []BindingOutcome
	resolved map[BindingKey]ResolvedClass
}

// policyContext reads what resolving reads from the merged catalogue, strictly: a key that does not
// decode leaves every binding unresolved, and Parse's culprit search refuses its Factory (plan PR7).
func policyContext(m map[string]any) (config.PolicyContext, error) {
	var ctx config.PolicyContext
	for _, f := range []struct {
		key string
		dst any
	}{{"workflows", &ctx.HarnessProfiles}, {"mergeGuardrails", &ctx.Guardrails}, {"templates", &ctx.Templates}, {"loops", &ctx.Loops}} {
		if v, ok := m[f.key]; ok && v != nil {
			if err := decodeStrict(v, f.dst); err != nil {
				return config.PolicyContext{}, fmt.Errorf("%s: %w", f.key, err)
			}
		}
	}
	ctx.Guardrails = ctx.Guardrails.WithDefaults() // MR7
	// WF2: the shipped workflows, as Parse fills them. A split is no part of a floor (TE6).
	ctx.HarnessProfiles, ctx.Templates, ctx.Loops = config.FillShippedWorkflows(ctx.HarnessProfiles, ctx.Templates, ctx.Loops, false)
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
		if p.Catalog {
			res.catalog = append(res.catalog, p.Name)
		}
		var spec config.PolicySpec
		var problems []string
		if refused := config.RefusePolicySpec(p.Spec); len(refused) > 0 {
			problems = refused
		} else if err := decodeStrict(p.Spec, &spec); err != nil {
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

// resolveBindings resolves one bound Repository's bindings, in list order (B4, B6, B9, B14), then
// a shadow binding of each catalog policy it does not bind (MR2), which it returns as implied. It
// returns the served classes; the outcomes and the newly resolved classes go to res. A binding
// that does not resolve is dropped, or its last resolved class put back. Bindings that do not
// decode are left to Parse, which refuses the Repository.
func resolveBindings(r Object, mergeDefaults map[string]any, ctx config.PolicyContext, refusedPolicies map[string]bool,
	opts Options, res *resolution) (map[string]config.Class, []config.Binding) {
	var bindings []config.Binding
	if raw, ok := r.Spec["mergePolicies"]; ok && raw != nil {
		if err := decodeStrict(raw, &bindings); err != nil {
			return nil, nil
		}
	}
	var implied []config.Binding
	for _, name := range res.catalog {
		if !slices.ContainsFunc(bindings, func(b config.Binding) bool { return b.Name == name }) {
			implied = append(implied, config.Binding{Name: name, Mode: config.ModeShadow})
		}
	}
	bindings = append(bindings, implied...)
	rm, _ := r.Spec["merge"].(map[string]any)
	full, _ := config.ApplyMergeDefaults(mergeDefaults, rm)
	var merge config.Merge
	_ = json.Unmarshal(mustJSON(full), &merge) // a merge that does not decode is Parse's to refuse

	type entry struct {
		out      config.BindingRefusal
		cl       config.Class
		gen      int64
		ok       bool
		disabled bool
	}
	entries := make([]entry, len(bindings))
	for i, b := range bindings {
		if config.Disabled(b.Mode) {
			entries[i].disabled = true
			continue
		}
		p, ok := res.policies[b.Name]
		switch {
		case !ok && refusedPolicies[b.Name]:
			entries[i].out = config.BindingRefusal{Reason: v1alpha1.ReasonMissingRef, Message: fmt.Sprintf("MergePolicy %s is refused", b.Name)}
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
	// B6, plan PR9: the later of two evaluated ambiguous adopted bindings is refused.
	for i := range entries {
		if !entries[i].ok {
			continue
		}
		for j := range i {
			if entries[j].ok && config.Ambiguous(entries[j].cl, entries[i].cl) {
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
		if e.ok && !e.disabled {
			classes[bindings[i].Name] = e.cl
		}
	}
	for i, b := range bindings {
		e := entries[i]
		k := BindingKey{r.Name, b.Name}
		o := BindingOutcome{Repository: r.Name, Name: b.Name, Mode: b.Mode}
		switch {
		case e.disabled:
			o.Accepted = true
		case e.ok:
			o.Accepted, o.Served, o.PolicyGeneration = true, true, e.gen
			if res.resolved == nil {
				res.resolved = map[BindingKey]ResolvedClass{}
			}
			res.resolved[k] = ResolvedClass{Class: e.cl, PolicyGeneration: e.gen}
		default:
			o.Reason, o.Message = e.out.Reason, e.out.Message
			// D5: a last resolved class can serve only at the written mode or stricter, never for a
			// missing policy or an ambiguous adoption. Recheck it under today's catalogue and merge.
			if last, ok := opts.LastResolved[k]; ok && e.out.Reason != config.ReasonAmbiguousAdoption &&
				e.out.Reason != v1alpha1.ReasonMissingRef && !config.AmbiguousWithAny(last.Class, classes) {
				if limited, modeOK := config.LimitMode(last.Class, b.Mode); modeOK {
					if cl, refusal := config.Recheck(b.Name, limited, ctx, merge); refusal != nil {
						o.Message += "; its last resolved class no longer holds: " + refusal.Message
					} else {
						classes[b.Name] = cl
						o.Served, o.PolicyGeneration = true, last.PolicyGeneration
					}
				}
			}
		}
		res.bindings = append(res.bindings, o)
	}
	return classes, implied
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
