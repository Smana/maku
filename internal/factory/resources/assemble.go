// SPDX-License-Identifier: Apache-2.0

// Package resources turns the factory's Factory, Repository and MergePolicy objects, together
// with its config file, into the config.Config the factory reads (factory resources D5, D8, D15).
// It is pure: the controller (plan 2) and maku config validate both call it.
//
// The objects' raw specs are merged into the file's YAML map and the result goes through
// config.Parse, so defaults and validation are exactly the file's. Candidates are judged as a
// whole set, never one object on top of an incomplete base: after migration the file alone holds
// neither a catalogue nor a repository, and Parse refuses a config with no repository.
package resources

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// Kind is one of the three resource kinds.
type Kind string

// The three resource kinds.
const (
	KindFactory     Kind = "Factory"
	KindRepository  Kind = "Repository"
	KindMergePolicy Kind = "MergePolicy"
)

// Object is one resource as the API server stores it: its spec raw, so a field the author
// omitted stays omitted and Parse's defaults apply exactly as they do to the file.
type Object struct {
	Kind Kind
	Name string
	// Namespace is the object's; Assemble ignores it. The controller's cache must be scoped to
	// the factory's namespace (plan 2), so every object it passes is in that namespace.
	Namespace  string
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

// CatalogueKeys are the top-level file keys a Factory replaces (D12), in the file's spelling.
var CatalogueKeys = []string{"control", "defaults", "tiers", "templates", "caps", "budgets", "facts", "brief", "resume", "scoper", "loops", "harnessProfiles", "mergeGuardrails"}

// candidate is one element of the candidate set: a Factory, or a Repository with its MergePolicy.
type candidate struct {
	factory *Object
	repo    *Object
	policy  *Object
}

func (c candidate) objects() []Object {
	if c.factory != nil {
		return []Object{*c.factory}
	}
	return []Object{*c.repo, *c.policy}
}

// Assemble builds the factory's config from the file and the objects (factory resources D5, D8, D15).
//
// When the file and the candidate set do not parse together, culprits are removed greedily: the
// first candidate (pairs by the Repository's name, then the Factory) whose removal makes the rest
// parse, or changes Parse's error, is refused Invalid with the current error, and the search
// starts again on what is left, at most once per candidate. When no removal changes anything, the
// whole set is refused. A culprit pair refuses its MergePolicy with the error and its Repository
// as "not served". The Accepted objects are the ones Config is built from.
//
// An error means no servable config: the file does not read as YAML, or it does not parse once
// every candidate is refused (after migration the file alone holds no repository). Result.Refused
// and Result.Warnings are still valid with an error, so the caller reports the refusals.
//
// Assemble keeps no history. D5's "keep the last accepted version" is the caller's: it substitutes
// each refused object's last accepted spec, when it has one, and calls Assemble again.
func Assemble(file []byte, objs []Object) (Result, error) {
	var base map[string]any
	if err := yaml.Unmarshal(file, &base); err != nil {
		return Result{}, fmt.Errorf("config file: %w", err)
	}
	if base == nil {
		base = map[string]any{}
	}
	name, _ := base["factory"].(string)

	var res Result
	refuse := func(o Object, reason, format string, a ...any) {
		res.Refused = append(res.Refused, Refusal{Kind: o.Kind, Name: o.Name, Reason: reason, Message: fmt.Sprintf(format, a...)})
	}
	refuseInvalid := func(c candidate, msg string) {
		if c.factory != nil {
			refuse(*c.factory, v1alpha1.ReasonInvalid, "%s", msg)
			return
		}
		refuse(*c.policy, v1alpha1.ReasonInvalid, "%s", msg)
		refuse(*c.repo, v1alpha1.ReasonInvalid, "not served: MergePolicy %s was refused", c.policy.Name)
	}

	set := structural(base, name, objs, refuse)

	cfg, warnings, err := tryParse(base, set)
	for err != nil && len(set) > 0 {
		cur := err.Error()
		culprit := -1
		for i := range set {
			if _, _, e := tryParse(base, slices.Concat(set[:i:i], set[i+1:])); e == nil || e.Error() != cur {
				culprit = i
				break
			}
		}
		if culprit < 0 {
			for _, c := range set {
				refuseInvalid(c, cur)
			}
			set = nil
		} else {
			refuseInvalid(set[culprit], cur)
			set = slices.Concat(set[:culprit:culprit], set[culprit+1:])
		}
		cfg, warnings, err = tryParse(base, set)
	}

	for _, c := range set {
		res.Accepted = append(res.Accepted, c.objects()...)
	}
	slices.SortFunc(res.Refused, func(a, b Refusal) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	res.Config, res.Warnings = cfg, warnings
	return res, err
}

// structural applies the refusals that need no Parse and returns the candidate set: pairs sorted
// by the Repository's name, then the Factory.
func structural(base map[string]any, name string, objs []Object, refuse func(Object, string, string, ...any)) []candidate {
	var factories, repos, policies []Object
	for _, o := range objs {
		switch o.Kind {
		case KindFactory:
			factories = append(factories, o)
		case KindRepository:
			repos = append(repos, o)
		case KindMergePolicy:
			policies = append(policies, o)
		}
	}

	// Factories: only this factory's, and only the first by name.
	var factory *Object
	slices.SortFunc(factories, func(a, b Object) int { return cmp.Compare(a.Name, b.Name) })
	for _, f := range factories {
		switch {
		case f.Name != name:
			refuse(f, v1alpha1.ReasonNotThisFactory, "this factory is %q, not %q", name, f.Name)
		case factory != nil:
			refuse(f, v1alpha1.ReasonNotThisFactory, "Factory %q already sets the catalogue", factory.Name)
		default:
			f := f
			factory = &f
		}
	}

	// MergePolicies: one per Repository, each naming a Repository object.
	repoNames := map[string]bool{}
	for _, r := range repos {
		repoNames[r.Name] = true
	}
	count := map[string]int{}
	for _, p := range policies {
		count[str(p.Spec, "repositoryRef")]++
	}
	var live []Object
	for _, p := range policies {
		ref := str(p.Spec, "repositoryRef")
		switch {
		case count[ref] > 1:
			refuse(p, v1alpha1.ReasonDuplicatePolicy, "several MergePolicies name Repository %q", ref)
		case !repoNames[ref]:
			refuse(p, v1alpha1.ReasonMissingRef, "no Repository %q", ref)
		default:
			live = append(live, p)
		}
	}

	// Repositories: this factory's, not already in the file, with a policy that remains.
	fileRepos := fileRepositoryNames(base)
	var usable []Object
	for _, r := range repos {
		repository := str(r.Spec, "repository")
		switch {
		case str(r.Spec, "factoryRef") != name:
			refuse(r, v1alpha1.ReasonNotThisFactory, "this factory is %q, not %q", name, str(r.Spec, "factoryRef"))
		case fileRepos[strings.ToLower(repository)]:
			refuse(r, v1alpha1.ReasonDefinedTwice, "the config file already defines %s", repository)
		default:
			usable = append(usable, r)
		}
	}
	usableNames := map[string]bool{}
	for _, r := range usable {
		usableNames[r.Name] = true
	}
	policyOf := map[string]Object{}
	for _, p := range live {
		ref := str(p.Spec, "repositoryRef")
		if !usableNames[ref] {
			refuse(p, v1alpha1.ReasonMissingRef, "Repository %q is not served", ref)
			continue
		}
		policyOf[ref] = p
	}

	slices.SortFunc(usable, func(a, b Object) int { return cmp.Compare(a.Name, b.Name) })
	var set []candidate
	for _, r := range usable {
		p, ok := policyOf[r.Name]
		if !ok {
			refuse(r, v1alpha1.ReasonNoMergePolicy, "no MergePolicy names Repository %q", r.Name)
			continue
		}
		r := r
		set = append(set, candidate{repo: &r, policy: &p})
	}
	if factory != nil {
		set = append(set, candidate{factory: factory})
	}
	return set
}

// fileRepositoryNames lowercases the names of the file's repositories, in both shapes.
func fileRepositoryNames(base map[string]any) map[string]bool {
	names := map[string]bool{}
	if list, ok := base["repositories"].([]any); ok {
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				names[strings.ToLower(str(m, "name"))] = true
			}
		}
	}
	switch v := base["repository"].(type) {
	case string:
		names[strings.ToLower(v)] = true
	case map[string]any:
		names[strings.ToLower(str(v, "name"))] = true
	}
	return names
}

// tryParse merges set into a copy of base and parses it.
func tryParse(base map[string]any, set []candidate) (*config.Config, []string, error) {
	m, warnings, err := merge(base, set)
	if err != nil {
		return nil, nil, err
	}
	c, err := parseMap(m)
	return c, warnings, err
}

// merge builds one map from a deep copy of base and the set.
func merge(base map[string]any, set []candidate) (map[string]any, []string, error) {
	m, err := cloneMap(base)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	for _, c := range set {
		if c.factory != nil {
			f := c.factory
			for _, k := range CatalogueKeys {
				if _, ok := m[k]; ok {
					delete(m, k)
					warnings = append(warnings, fmt.Sprintf("%s in the file is ignored: Factory %s sets the catalogue", k, f.Name))
				}
			}
			for k, v := range f.Spec {
				switch k {
				case "prices":
					meter, _ := m["meter"].(map[string]any)
					if meter == nil {
						meter = map[string]any{}
					}
					meter["prices"] = v
					m["meter"] = meter
				case "triage":
					triage, _ := m["triage"].(map[string]any)
					if sub, ok := v.(map[string]any); ok {
						if triage == nil {
							triage = map[string]any{}
						}
						for tk, tv := range sub {
							triage[tk] = tv
						}
						m["triage"] = triage
					} else {
						m["triage"] = v
					}
				default:
					m[k] = v
				}
			}
			continue
		}
		entry := map[string]any{"name": str(c.repo.Spec, "repository")}
		for _, k := range []string{"maintainers", "triggerLabel", "checks", "schedules", "classAuthority"} {
			if v, ok := c.repo.Spec[k]; ok {
				entry[k] = v
			}
		}
		for _, k := range []string{"classes", "merge"} {
			if v, ok := c.policy.Spec[k]; ok {
				entry[k] = v
			}
		}
		list, _ := m["repositories"].([]any)
		m["repositories"] = append(list, entry)
	}
	// The spec values are shared with the caller's objects; round-trip so nothing is aliased.
	m, err = cloneMap(m)
	return m, warnings, err
}

// cloneMap deep-copies m through YAML, which is enough at these sizes.
func cloneMap(m map[string]any) (map[string]any, error) {
	b, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func parseMap(m map[string]any) (*config.Config, error) {
	b, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	return config.Parse(b)
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
