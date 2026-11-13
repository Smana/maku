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
	"encoding/json"
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
	// Bindings are the outcomes of the served Repositories' bindings, by Repository then in list
	// order (B9). A binding that is not Accepted never refuses its Repository.
	Bindings []BindingOutcome
	// Resolved are the bindings resolved this time, for the caller to keep as Options.LastResolved.
	Resolved map[BindingKey]ResolvedClass
}

// CatalogueKeys are the top-level file keys a Factory replaces (D12), in the file's spelling.
var CatalogueKeys = []string{"control", "defaults", "tiers", "templates", "caps", "budgets", "facts", "brief", "resume", "scoper", "loops", "harnessProfiles", "mergeGuardrails", "evaluation", "mergeDefaults"}

// candidate is one element of the candidate set: a Factory, a Repository with its per-repository
// MergePolicy, or a Repository holding its own merge and bindings (policy nil).
type candidate struct {
	factory *Object
	repo    *Object
	policy  *Object
}

func (c candidate) objects() []Object {
	switch {
	case c.factory != nil:
		return []Object{*c.factory}
	case c.policy == nil:
		return []Object{*c.repo}
	}
	return []Object{*c.repo, *c.policy}
}

// Assemble builds the factory's config from the file and the objects (factory resources D5, D8, D15).
//
// When the file and the candidate set do not parse together, culprits are removed greedily: the
// first candidate (pairs by the Repository's name, then the Factory) whose removal makes the rest
// parse is refused Invalid with the current error; when none does, the first whose removal changes
// Parse's error is (two culprits). The search starts again on what is left, at most once per
// candidate. When no removal changes anything, the
// whole set is refused. A culprit pair refuses its MergePolicy with the error and its Repository
// as "not served". The Accepted objects are the ones Config is built from.
//
// An error means no servable config: the file does not read as YAML, or it does not parse once
// every candidate is refused (after migration the file alone holds no repository). Result.Refused
// and Result.Warnings are still valid with an error, so the caller reports the refusals.
//
// Assemble keeps no history. D5's "keep the last accepted version" is the caller's: it substitutes
// each refused object's last accepted spec, when it has one, and calls Assemble again.
//
// Reusable merge policies (B1-B9): a class-shape MergePolicy is judged alone and is never a
// candidate. A Repository with merge or mergePolicies is a candidate alone, its bindings resolved
// before Parse under the merged catalogue; a binding that does not resolve drops its class, or
// puts back opts' last resolved one, and never refuses the Repository (Result.Bindings).
func Assemble(file []byte, objs []Object, opts ...Options) (Result, error) {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	// The map below keeps a duplicated switch block's last copy silently: read the raw bytes first.
	if err := config.CheckBlocks(file); err != nil {
		return Result{}, fmt.Errorf("config file: %w", err)
	}
	var base map[string]any
	if err := yaml.Unmarshal(file, &base); err != nil {
		return Result{}, fmt.Errorf("config file: %w", err)
	}
	if base == nil {
		base = map[string]any{}
	}
	// bound and bindings reach Parse only from Repository objects (ParseAssembled admits them):
	// the file's own entries are refused them here, as Parse refuses them in a file alone.
	if list, ok := base["repositories"].([]any); ok {
		for _, e := range list {
			entry, _ := e.(map[string]any)
			for _, k := range []string{"bound", "bindings"} {
				if _, ok := entry[k]; ok {
					return Result{}, fmt.Errorf("config file: %s: bound and bindings are set by the factory from Repository objects: bind policies in a Repository's mergePolicies", str(entry, "name"))
				}
			}
		}
	}
	name, _ := base["factory"].(string)

	var res Result
	refuse := func(o Object, reason, format string, a ...any) {
		res.Refused = append(res.Refused, Refusal{Kind: o.Kind, Name: o.Name, Reason: reason, Message: fmt.Sprintf(format, a...)})
	}
	refuseInvalid := func(c candidate, msg string) {
		switch {
		case c.factory != nil:
			refuse(*c.factory, v1alpha1.ReasonInvalid, "%s", msg)
		case c.policy == nil:
			refuse(*c.repo, v1alpha1.ReasonInvalid, "%s", msg)
		default:
			refuse(*c.policy, v1alpha1.ReasonInvalid, "%s", msg)
			refuse(*c.repo, v1alpha1.ReasonInvalid, "not served: MergePolicy %s was refused", c.policy.Name)
		}
	}

	set, policies := structural(base, name, objs, refuse, func(w string) { res.Warnings = append(res.Warnings, w) })
	tryParse := func(set []candidate) (*config.Config, []string, resolution, error) {
		return assemble(base, set, policies, opt)
	}

	cfg, warnings, rs, err := tryParse(set)
	for err != nil && len(set) > 0 {
		cur := err.Error()
		// Success first: a removal that makes Parse succeed names the culprit. Only when none
		// does, the first removal that changes the error does (two culprits). Judging by a
		// changed error alone would blame a pair whose message merely depends on a bad Factory.
		// A removal that drops one of the current error's lines names its culprit before one that
		// merely changes the text: removing a pair adds "not a listed repository" lines and keeps
		// every old one, so it is never blamed for a bad Factory (#448).
		culprit, dropped, changed := -1, -1, -1
		curLines := errLines(cur)
		for i := range set {
			_, _, _, e := tryParse(slices.Concat(set[:i:i], set[i+1:]))
			if e == nil {
				culprit = i
				break
			}
			if dropped < 0 && !containsAll(errLines(e.Error()), curLines) {
				dropped = i
			}
			if changed < 0 && e.Error() != cur {
				changed = i
			}
		}
		if culprit < 0 {
			culprit = dropped
		}
		if culprit < 0 {
			culprit = changed
		}
		if culprit < 0 {
			for _, c := range set {
				refuseInvalid(c, cur)
			}
			set = nil
		} else {
			refuseInvalid(set[culprit], cur)
			factoryOut := set[culprit].factory != nil
			set = slices.Concat(set[:culprit:culprit], set[culprit+1:])
			if factoryOut {
				// Without the Factory the file may hold no catalogue: what then fails is that, not
				// the pairs, which stay unrefused. The caller puts the Factory's last good back.
				cfg, warnings, rs, err = tryParse(set)
				if err != nil {
					break
				}
			}
		}
		cfg, warnings, rs, err = tryParse(set)
	}

	for _, c := range set {
		res.Accepted = append(res.Accepted, c.objects()...)
	}
	res.Accepted = append(res.Accepted, rs.accepted...)
	res.Refused = append(res.Refused, rs.refused...)
	slices.SortFunc(res.Refused, func(a, b Refusal) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	res.Bindings, res.Resolved = rs.bindings, rs.resolved
	if cfg != nil {
		cfg.Policies = rs.policies
	}
	res.Config, res.Warnings = cfg, append(res.Warnings, warnings...)
	return res, err
}

// errLines are the lines of Parse's joined error, the "config: " prefix of the first one removed:
// it is on whichever line comes first, so it would make one error's line differ from itself.
func errLines(msg string) []string {
	return strings.Split(strings.TrimPrefix(msg, "config: "), "\n")
}

// containsAll is true when every element of want is in have.
func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// structural applies the refusals that need no Parse and returns the candidate set (pairs and
// bound Repositories sorted by the Repository's name, then the Factory) and the class-shape
// policies, sorted by name.
func structural(base map[string]any, name string, objs []Object, refuse func(Object, string, string, ...any), warn func(string)) ([]candidate, []Object) {
	var factories, repos, policies, classPolicies []Object
	for _, o := range objs {
		switch {
		case o.Kind == KindFactory:
			factories = append(factories, o)
		case o.Kind == KindRepository:
			repos = append(repos, o)
		case o.Kind == KindMergePolicy && legacyPolicy(o):
			warn(fmt.Sprintf("MergePolicy %s is in the per-repository shape, removed in the next minor: maku config migrate", o.Name))
			policies = append(policies, o)
		case o.Kind == KindMergePolicy:
			classPolicies = append(classPolicies, o)
		}
	}
	slices.SortFunc(classPolicies, func(a, b Object) int { return cmp.Compare(a.Name, b.Name) })

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
	usableNames, boundNames := map[string]bool{}, map[string]bool{}
	for _, r := range usable {
		usableNames[r.Name] = true
		boundNames[r.Name] = boundRepository(r)
	}
	policyOf := map[string]Object{}
	for _, p := range live {
		ref := str(p.Spec, "repositoryRef")
		if boundNames[ref] {
			// B8, plan PR8: the Repository's own bindings and a per-repository policy never together.
			refuse(p, v1alpha1.ReasonDefinedTwice, "Repository %s holds its own merge and mergePolicies: delete this per-repository MergePolicy (B8)", ref)
			continue
		}
		if !usableNames[ref] {
			refuse(p, v1alpha1.ReasonMissingRef, "Repository %q is not served", ref)
			continue
		}
		policyOf[ref] = p
	}

	slices.SortFunc(usable, func(a, b Object) int { return cmp.Compare(a.Name, b.Name) })
	var set []candidate
	for _, r := range usable {
		if boundNames[r.Name] {
			if count[r.Name] > 0 {
				refuse(r, v1alpha1.ReasonDefinedTwice, "a per-repository MergePolicy names Repository %q, which holds its own merge and mergePolicies: delete that MergePolicy (B8)", r.Name)
				continue
			}
			r := r
			set = append(set, candidate{repo: &r})
			continue
		}
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
	return set, classPolicies
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

// assemble merges set into a copy of base, resolving the bound Repositories' bindings against the
// class policies, and parses it.
func assemble(base map[string]any, set []candidate, policies []Object, opts Options) (*config.Config, []string, resolution, error) {
	m, warnings, rs, err := merge(base, set, policies, opts)
	if err != nil {
		return nil, nil, rs, err
	}
	c, err := parseMap(m)
	return c, warnings, rs, err
}

// merge builds one map from a deep copy of base and the set: the Factory first, since resolving a
// binding reads the merged catalogue (plan PR7), then each Repository's entry.
func merge(base map[string]any, set []candidate, policies []Object, opts Options) (map[string]any, []string, resolution, error) {
	var rs resolution
	m, err := cloneMap(base)
	if err != nil {
		return nil, nil, rs, err
	}
	var warnings []string
	set = slices.Clone(set)
	slices.SortStableFunc(set, func(a, b candidate) int { return cmp.Compare(boolInt(a.factory == nil), boolInt(b.factory == nil)) })
	var ctx config.PolicyContext
	var ctxErr error
	read := false
	readContext := func() {
		read = true
		if ctx, ctxErr = policyContext(m); ctxErr == nil {
			judgePolicies(policies, ctx, &rs)
		}
	}
	for _, c := range set {
		if c.factory == nil && !read {
			readContext()
		}
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
		if c.policy != nil {
			for _, k := range []string{"classes", "merge"} {
				if v, ok := c.policy.Spec[k]; ok {
					entry[k] = v
				}
			}
		} else if err := boundEntry(entry, *c.repo, m, ctx, ctxErr, opts, &rs); err != nil {
			return nil, nil, rs, err
		}
		list, _ := m["repositories"].([]any)
		m["repositories"] = append(list, entry)
	}
	if !read { // no Repository: the policies are still judged
		readContext()
	}
	// The spec values are shared with the caller's objects; round-trip so nothing is aliased.
	m, err = cloneMap(m)
	return m, warnings, rs, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// boundEntry fills a bound Repository's entry (plan Task 8): its merge as written (Parse applies
// mergeDefaults), its resolved classes, its bound names and its bindings as written. When the
// policy context does not decode, no binding resolves, and Parse refuses the Factory.
func boundEntry(entry map[string]any, r Object, m map[string]any, ctx config.PolicyContext, ctxErr error, opts Options, rs *resolution) error {
	if v, ok := r.Spec["merge"]; ok {
		entry["merge"] = v
	}
	classes := map[string]config.Class{}
	if ctxErr == nil {
		refused := map[string]bool{}
		for _, f := range rs.refused {
			refused[f.Name] = true
		}
		defaults, _ := m["mergeDefaults"].(map[string]any)
		classes = resolveBindings(r, defaults, ctx, refused, opts, rs)
	}
	raw, err := json.Marshal(classes)
	if err != nil {
		return err
	}
	var cm map[string]any
	if err := json.Unmarshal(raw, &cm); err != nil {
		return err
	}
	entry["classes"] = cm
	if v, ok := r.Spec["mergePolicies"]; ok {
		entry["bindings"] = v
		var bound []any
		if list, ok := v.([]any); ok {
			for _, b := range list {
				if b, ok := b.(map[string]any); ok {
					bound = append(bound, b["name"])
				}
			}
		}
		entry["bound"] = bound
	}
	return nil
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
	return config.ParseAssembled(b)
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
