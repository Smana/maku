// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

// BindingResult is one proposed binding as the factory would resolve it (B9): its class's change
// from the one served now, or why it would not resolve. A binding that does not resolve never
// refuses the edit: it drops its own class only.
type BindingResult struct {
	Name     string   `json:"name"`
	Mode     string   `json:"mode"`
	Resolves bool     `json:"resolves"`
	Reason   string   `json:"reason,omitempty"`
	Message  string   `json:"message,omitempty"`
	Change   string   `json:"change,omitempty"` // a mergepolicy verdict, or "new" for a class not served now
	Why      []string `json:"why,omitempty"`
}

// BindingPreview is a proposed spec.mergePolicies of one Repository (B16): the Repository manifest
// to submit and each binding's resolution. Valid false lists the errors and nothing else.
type BindingPreview struct {
	Valid        bool            `json:"valid"`
	Errors       []string        `json:"errors,omitempty"`
	Bindings     []BindingResult `json:"bindings,omitempty"`
	Manifest     string          `json:"manifest,omitempty"`
	ManifestPath string          `json:"manifestPath,omitempty"`
}

// PolicyPreview is a proposed class-shape MergePolicy (B11, B16): the manifest to submit and, per
// consumer the caller reads, its structural change, live consumers first (Q5).
type PolicyPreview struct {
	Valid        bool             `json:"valid"`
	Errors       []string         `json:"errors,omitempty"`
	Consumers    []ConsumerImpact `json:"consumers,omitempty"`
	Manifest     string           `json:"manifest,omitempty"`
	ManifestPath string           `json:"manifestPath,omitempty"`
}

func policyContext(cfg *config.Config) config.PolicyContext {
	return config.PolicyContext{HarnessProfiles: cfg.HarnessProfiles, Guardrails: cfg.MergeGuardrails, Templates: cfg.Templates, Loops: cfg.Loops}
}

// change is a binding's class change from served (ok false: none served) to next.
func change(served config.Class, ok bool, next config.Class) (string, []string) {
	if !ok {
		return "new", nil
	}
	c := mergepolicy.ClassChange(served, next)
	return c.Kind, c.Why
}

// CheckBindings previews bindings as rc's spec.mergePolicies (B16), against the policies the
// factory serves, under the guardrails and rc's merge, as Assemble resolves them. The admission
// rules are checked as the API server would (unique names, OD-8, repository authority); a binding
// that does not resolve is reported, as it would drop its class alone (B9).
func CheckBindings(cfg *config.Config, rc *config.Repository, rep evidence.Report, bindings []config.Binding) BindingPreview {
	bad := func(msgs ...string) BindingPreview { return BindingPreview{Errors: msgs} }
	if rep.RepositoryObject == "" {
		return bad("the repository is served from a per-repository MergePolicy: migrate it (maku config migrate) to edit its bindings")
	}
	var errs []string
	names := map[string]bool{}
	for _, b := range bindings {
		if names[b.Name] {
			errs = append(errs, fmt.Sprintf("%s is bound twice", b.Name))
		}
		names[b.Name] = true
	}
	if len(bindings) > 0 && (!names["docs-links"] || !names["revert"]) {
		errs = append(errs, "bind docs-links and revert when any policy is bound (OD-8)")
	}
	if rc.ClassAuthority == config.ClassAuthorityRepository && len(bindings) > 0 {
		errs = append(errs, "classAuthority repository: the repository's file holds the bindings (MP15)")
	}
	if len(errs) > 0 {
		return bad(errs...)
	}
	pv := BindingPreview{Valid: true}
	ctx, served := policyContext(cfg), map[string]config.Class{}
	for _, b := range bindings {
		r := BindingResult{Name: b.Name, Mode: b.Mode}
		p, ok := cfg.Policies[b.Name]
		if !ok {
			r.Reason, r.Message = v1alpha1.ReasonMissingRef, "no MergePolicy "+b.Name
			pv.Bindings = append(pv.Bindings, r)
			continue
		}
		cl, refused := config.Resolve(b.Name, p.Spec, b, ctx, rc.Merge)
		switch {
		case refused != nil:
			r.Reason, r.Message = refused.Reason, refused.Message
		case config.AmbiguousWithAny(cl, served):
			r.Reason, r.Message = config.ReasonAmbiguousAdoption, "class "+b.Name+" adopts the pull requests another bound class adopts (B6)"
		default:
			served[b.Name] = cl
			was, ok := rc.Classes[b.Name]
			r.Resolves = true
			r.Change, r.Why = change(was, ok, cl)
		}
		pv.Bindings = append(pv.Bindings, r)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(rep.RepositoryManifest), &doc); err != nil {
		return bad("the Repository manifest does not read: " + err.Error())
	}
	spec, _ := doc["spec"].(map[string]any)
	if spec == nil {
		return bad("the Repository manifest has no spec")
	}
	spec["mergePolicies"] = bindings
	m, err := yaml.Marshal(doc)
	if err != nil {
		return bad(err.Error())
	}
	pv.Manifest, pv.ManifestPath = string(m), rep.RepositoryManifestPath
	return pv
}

// CheckPolicy previews spec as the MergePolicy name (B11, B16): valid alone (B9), its manifest in
// ns, and each consumer's change, for the repositories readable says the caller reads.
func CheckPolicy(cfg *config.Config, name string, spec config.PolicySpec, ns string, readable func(repository string) bool) PolicyPreview {
	ctx := policyContext(cfg)
	if problems := config.ValidatePolicy(name, spec, ctx); len(problems) > 0 {
		return PolicyPreview{Errors: problems}
	}
	m, err := resources.PolicyManifest(name, ns, spec)
	if err != nil {
		return PolicyPreview{Errors: []string{err.Error()}}
	}
	pv := PolicyPreview{Valid: true, Manifest: string(m), ManifestPath: "mergepolicy-" + name + ".yaml"}
	for _, rc := range cfg.Repos() {
		i := slices.IndexFunc(rc.Bindings, func(b config.Binding) bool { return b.Name == name })
		if i < 0 || !readable(rc.Name) {
			continue
		}
		b := rc.Bindings[i]
		object := cmp.Or(cfg.Generations.Repos[strings.ToLower(rc.Name)].RepositoryName, rc.Name)
		ci := ConsumerImpact{Repository: object, Mode: b.Mode, Change: "-"}
		if cl, refused := config.Resolve(name, spec, b, ctx, rc.Merge); refused != nil {
			ci.Reason = refused.Reason + ": " + refused.Message
		} else {
			was, ok := rc.Classes[name]
			ci.Resolves = true
			ci.Change, ci.Why = change(was, ok, cl)
		}
		pv.Consumers = append(pv.Consumers, ci)
	}
	slices.SortStableFunc(pv.Consumers, func(x, y ConsumerImpact) int {
		return cmp.Or(cmp.Compare(rank(x.Mode), rank(y.Mode)), cmp.Compare(x.Repository, y.Repository))
	})
	return pv
}
