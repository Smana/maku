// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

// consumer is one legacy MergePolicy's use of a class: the Repository object it names, and the
// class as that repository declares it.
type consumer struct {
	repo string
	cl   config.Class
}

// Split converts per-repository MergePolicies into class-shape policies and bindings (B17, plan
// PR19), deterministically: one MergePolicy per class name, and each Repository a legacy policy
// named gaining its merge (plus fluxSource) and its bindings, in class-name order. Every other
// object passes through.
//
//   - Meaning fields must be equal across a class's consumers, compared on the decoded value;
//     otherwise the class is refused, naming the fields that differ. Split never renames.
//   - Fill-once fields (allowedFiles, companionFiles, requiredLabel, oracleWorkflow) go on the
//     bindings, and into requires when every consumer sets them.
//   - Additive lists go on the policy when every consumer has the same set (in the first
//     consumer's order), else each consumer's whole list on its binding; holdoutChecks on the
//     bindings is required when every consumer's is non-empty.
//   - Caps go on the policy at the loosest consumer's value (0, no cap, is the loosest), and on a
//     binding only below it.
//   - monitor.flux.source becomes merge.fluxSource: two sources in one repository are refused (Q14).
//
// Split proves nothing: Equal on the configs assembled before and after does.
func Split(objs []Object, ns string) ([]Object, error) {
	out := make([]Object, 0, len(objs))
	var legacy []Object
	for _, o := range objs {
		if o.Kind == KindMergePolicy && legacyPolicy(o) {
			legacy = append(legacy, o)
			continue
		}
		if o.Kind == KindMergePolicy {
			return nil, fmt.Errorf("split: MergePolicy %s is already in the class shape: split the per-repository objects only", o.Name)
		}
		out = append(out, o)
	}
	repos := map[string]int{} // a Repository object's index in out
	for i, o := range out {
		if o.Kind == KindRepository {
			repos[o.Name] = i
		}
	}
	slices.SortFunc(legacy, func(a, b Object) int { return cmp.Compare(str(a.Spec, "repositoryRef"), str(b.Spec, "repositoryRef")) })

	consumers := map[string][]consumer{}
	var errs []error
	for _, p := range legacy {
		ref := str(p.Spec, "repositoryRef")
		i, ok := repos[ref]
		if !ok {
			errs = append(errs, fmt.Errorf("split: MergePolicy %s names Repository %q, which is not among the objects", p.Name, ref))
			continue
		}
		var classes map[string]config.Class
		if err := decodeStrict(p.Spec["classes"], &classes); err != nil {
			errs = append(errs, fmt.Errorf("split: MergePolicy %s: classes: %w", p.Name, err))
			continue
		}
		merge, _ := cloneMap(asMap(p.Spec["merge"]))
		if merge == nil {
			merge = map[string]any{}
		}
		var sources []string
		var bindings []any
		for _, name := range slices.Sorted(maps.Keys(classes)) {
			cl := classes[name]
			consumers[name] = append(consumers[name], consumer{repo: ref, cl: cl})
			if s := fluxSource(cl); s != "" && !slices.Contains(sources, s) {
				sources = append(sources, s)
			}
			bindings = append(bindings, name) // a name until every consumer is known
		}
		switch {
		case len(sources) > 1:
			errs = append(errs, fmt.Errorf("split: Repository %s: its classes watch %d Flux sources (%s): a repository has one, merge.fluxSource (Q14)",
				ref, len(sources), strings.Join(sources, ", ")))
		case len(sources) == 1:
			merge["fluxSource"] = sources[0]
		}
		spec, err := cloneMap(out[i].Spec)
		if err != nil {
			return nil, err
		}
		spec["merge"] = merge
		spec["mergePolicies"] = bindings
		out[i].Spec = spec
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	bound := map[string]map[string]config.Binding{} // repository -> class -> binding
	var policies []Object
	for _, name := range slices.Sorted(maps.Keys(consumers)) {
		spec, bs, err := splitClass(name, consumers[name])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m, err := toMap(spec)
		if err != nil {
			return nil, err
		}
		policies = append(policies, Object{Kind: KindMergePolicy, Name: name, Namespace: ns, Spec: m})
		for repo, b := range bs {
			if bound[repo] == nil {
				bound[repo] = map[string]config.Binding{}
			}
			bound[repo][name] = b
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	for i, o := range out {
		names, ok := o.Spec["mergePolicies"].([]any)
		if o.Kind != KindRepository || !ok || bound[o.Name] == nil {
			continue
		}
		list := make([]any, 0, len(names))
		for _, n := range names {
			m, err := toMap(bound[o.Name][n.(string)])
			if err != nil {
				return nil, err
			}
			list = append(list, m)
		}
		out[i].Spec["mergePolicies"] = list
	}
	return append(out, policies...), nil
}

// splitClass is one class's policy, and each consumer's binding by repository (PR19).
func splitClass(name string, cs []consumer) (config.PolicySpec, map[string]config.Binding, error) {
	// Meaning: equal across consumers, on the decoded value.
	base := meaning(cs[0].cl)
	for _, c := range cs[1:] {
		if diff := fieldDiff(base, meaning(c.cl)); len(diff) > 0 {
			return config.PolicySpec{}, nil, fmt.Errorf("split: class %s: Repository %s and Repository %s declare it differently in %s: "+
				"one policy per class name, and the converter never renames", name, cs[0].repo, c.repo, strings.Join(diff, ", "))
		}
	}
	p := config.PolicySpec{Class: base}
	bs := map[string]config.Binding{}
	for _, c := range cs {
		b := config.Binding{Name: name, Mode: config.ModeOff}
		switch {
		case c.cl.Live:
			b.Mode = config.ModeLive
		case c.cl.Shadow:
			b.Mode = config.ModeShadow
		}
		b.AllowedFiles, b.CompanionFiles, b.RequiredLabel = c.cl.AllowedFiles, c.cl.CompanionFiles, c.cl.RequiredLabel
		if c.cl.Oracle != nil {
			b.OracleWorkflow = c.cl.Oracle.Workflow
		}
		bs[c.repo] = b
	}
	every := func(set func(config.Class) bool) bool {
		return !slices.ContainsFunc(cs, func(c consumer) bool { return !set(c.cl) })
	}
	for _, f := range []struct {
		key string
		set func(config.Class) bool
	}{
		{"allowedFiles", func(cl config.Class) bool { return len(cl.AllowedFiles) > 0 }},
		{"companionFiles", func(cl config.Class) bool { return len(cl.CompanionFiles) > 0 }},
		{"requiredLabel", func(cl config.Class) bool { return cl.RequiredLabel != "" }},
		{"oracleWorkflow", func(cl config.Class) bool { return cl.Oracle != nil && cl.Oracle.Workflow != "" }},
	} {
		if every(f.set) {
			p.Requires = append(p.Requires, f.key)
		}
	}

	// Additive: on the policy when every consumer has the same set.
	if sameSet(cs, func(cl config.Class) []string { return cl.HoldoutChecks }) {
		p.HoldoutChecks = slices.Clone(cs[0].cl.HoldoutChecks)
	} else {
		for _, c := range cs {
			b := bs[c.repo]
			b.HoldoutChecks = c.cl.HoldoutChecks
			bs[c.repo] = b
		}
		if every(func(cl config.Class) bool { return len(cl.HoldoutChecks) > 0 }) {
			p.Requires = append(p.Requires, "holdoutChecks")
		}
	}
	if sameSet(cs, func(cl config.Class) []string { return cl.ProtectedFiles }) {
		p.ProtectedFiles = slices.Clone(cs[0].cl.ProtectedFiles)
	} else {
		for _, c := range cs {
			b := bs[c.repo]
			b.ProtectedFiles = c.cl.ProtectedFiles
			bs[c.repo] = b
		}
	}
	approvals := func(cl config.Class) []string {
		out := make([]string, len(cl.RequiresApproval))
		for i, a := range cl.RequiresApproval {
			out[i] = string(mustJSON(a))
		}
		return out
	}
	if sameSet(cs, approvals) {
		p.RequiresApproval = cs[0].cl.RequiresApproval
	} else {
		for _, c := range cs {
			b := bs[c.repo]
			b.RequiresApproval = c.cl.RequiresApproval
			bs[c.repo] = b
		}
	}

	// Caps: the loosest on the policy, a lower one on the binding.
	for _, f := range []struct {
		policy *int
		get    func(config.Class) int
		bind   func(*config.Binding, int)
	}{
		{&p.MaxPatchFiles, func(cl config.Class) int { return cl.MaxPatchFiles }, func(b *config.Binding, v int) { b.MaxPatchFiles = v }},
		{&p.MaxModifiedLines, func(cl config.Class) int { return cl.MaxModifiedLines }, func(b *config.Binding, v int) { b.MaxModifiedLines = v }},
		{&p.MaxPerDay, func(cl config.Class) int { return cl.MaxPerDay }, func(b *config.Binding, v int) { b.MaxPerDay = v }},
	} {
		loosest := 0
		if every(func(cl config.Class) bool { return f.get(cl) > 0 }) {
			for _, c := range cs {
				loosest = max(loosest, f.get(c.cl))
			}
		}
		*f.policy = loosest
		for _, c := range cs {
			if v := f.get(c.cl); v > 0 && (loosest == 0 || v < loosest) {
				b := bs[c.repo]
				f.bind(&b, v)
				bs[c.repo] = b
			}
		}
	}
	return p, bs, nil
}

// meaning is a class less what the policy does not hold alone: the mode, the fill-once fields,
// the additive lists, the caps and the repository's Flux source.
func meaning(cl config.Class) config.Class {
	m := *cl.DeepCopy()
	m.Live, m.Shadow = false, false
	m.AllowedFiles, m.CompanionFiles, m.RequiredLabel = nil, nil, ""
	m.HoldoutChecks, m.ProtectedFiles, m.RequiresApproval = nil, nil, nil
	m.MaxPatchFiles, m.MaxModifiedLines, m.MaxPerDay = 0, 0, 0
	if m.Oracle != nil {
		m.Oracle.Workflow = ""
	}
	if m.Monitor != nil && m.Monitor.Flux != nil {
		m.Monitor.Flux.Source = ""
	}
	return m
}

func fluxSource(cl config.Class) string {
	if cl.Monitor == nil || cl.Monitor.Flux == nil {
		return ""
	}
	return cl.Monitor.Flux.Source
}

// sameSet reports whether every consumer's list holds the same elements.
func sameSet(cs []consumer, list func(config.Class) []string) bool {
	want := slices.Sorted(slices.Values(list(cs[0].cl)))
	for _, c := range cs[1:] {
		if !slices.Equal(want, slices.Sorted(slices.Values(list(c.cl)))) {
			return false
		}
	}
	return true
}

// fieldDiff names the top-level JSON fields where a and b differ, sorted.
func fieldDiff(a, b any) []string {
	ma, mb := jsonMap(a), jsonMap(b)
	keys := map[string]bool{}
	for k := range ma {
		keys[k] = true
	}
	for k := range mb {
		keys[k] = true
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if !reflect.DeepEqual(ma[k], mb[k]) {
			out = append(out, k)
		}
	}
	return out
}

func jsonMap(v any) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(mustJSON(v), &m)
	return m
}

// toMap is v as a manifest spec.
func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// Equal lists every difference between two configs' resolved classes and merge settings,
// repository by repository and class by class, field by field (B17): empty when no gate decision
// can change. merge.fluxSource, which only the binding shape holds, is not compared: the classes'
// own monitor.flux.source is.
func Equal(before, after *config.Config) []string {
	repos := func(c *config.Config) map[string]config.Repository {
		out := map[string]config.Repository{}
		for _, r := range c.Repos() {
			out[r.Name] = r
		}
		return out
	}
	b, a := repos(before), repos(after)
	names := map[string]bool{}
	for n := range b {
		names[n] = true
	}
	for n := range a {
		names[n] = true
	}
	var out []string
	for _, n := range slices.Sorted(maps.Keys(names)) {
		rb, inB := b[n]
		ra, inA := a[n]
		switch {
		case !inA:
			out = append(out, n+": served before only")
			continue
		case !inB:
			out = append(out, n+": served after only")
			continue
		}
		mb, ma := rb.Merge, ra.Merge
		mb.FluxSource, ma.FluxSource = "", ""
		for _, f := range fieldDiff(mb, ma) {
			out = append(out, fmt.Sprintf("%s merge: %s %s -> %s", n, f, jsonOf(jsonMap(mb)[f]), jsonOf(jsonMap(ma)[f])))
		}
		classes := map[string]bool{}
		for c := range rb.Classes {
			classes[c] = true
		}
		for c := range ra.Classes {
			classes[c] = true
		}
		for _, c := range slices.Sorted(maps.Keys(classes)) {
			cb, inB := rb.Classes[c]
			ca, inA := ra.Classes[c]
			switch {
			case !inA:
				out = append(out, fmt.Sprintf("%s %s: served before only", n, c))
			case !inB:
				out = append(out, fmt.Sprintf("%s %s: served after only", n, c))
			default:
				for _, f := range fieldDiff(cb, ca) {
					out = append(out, fmt.Sprintf("%s %s: %s %s -> %s", n, c, f, jsonOf(jsonMap(cb)[f]), jsonOf(jsonMap(ca)[f])))
				}
			}
		}
	}
	return out
}

func jsonOf(v any) string {
	if v == nil {
		return "unset"
	}
	return string(mustJSON(v))
}

// Encode writes objects as multi-document YAML manifests, in order.
func Encode(objs []Object) ([]byte, error) {
	docs := make([]string, 0, len(objs))
	for _, o := range objs {
		meta := map[string]any{"name": o.Name}
		if o.Namespace != "" {
			meta["namespace"] = o.Namespace
		}
		if o.Generation != 0 {
			meta["generation"] = o.Generation
		}
		b, err := yaml.Marshal(map[string]any{"apiVersion": "maku.ogenki.io/v1alpha1", "kind": string(o.Kind), "metadata": meta, "spec": o.Spec})
		if err != nil {
			return nil, err
		}
		docs = append(docs, string(b))
	}
	return []byte(strings.Join(docs, "---\n")), nil
}
