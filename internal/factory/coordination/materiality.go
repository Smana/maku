// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"maps"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// Materiality is what an accepted edit changed and who must confirm again (MR12). ChangedSections
// names the table's sections, sorted: title, primary.rationale, children.<id>.<subfield>,
// integration.<field>, budget.<field>. ParentRequired is a new parent execution confirmation; a
// title- or rationale-only edit needs only the parent's review of the repost. Reauthorize is the
// sorted child ids whose maintainers authorize again. NewParentRequired is a primary change: a new
// parent and every authorization, never an edit.
type Materiality struct {
	ChangedSections   []string `json:"changedSections,omitempty"`
	ParentRequired    bool     `json:"parentRequired,omitempty"`
	Reauthorize       []string `json:"reauthorize,omitempty"`
	NewParentRequired bool     `json:"newParentRequired,omitempty"`
}

// ComparePlans applies the design's fixed materiality table to two full snapshots of one plan,
// next exactly one version after old. It compares typed fields only: no patch language, similarity
// score or model decides. A version other than old's plus one, or another plan id, is
// ErrPlanVersion; a primary change is ErrPrimaryRequiresNewParent with NewParentRequired set.
func ComparePlans(old, next v1alpha1.CoordinationPlan) (Materiality, error) {
	o, n := normalize(old), normalize(next)
	if o.ID != n.ID {
		return Materiality{}, refuse(ErrPlanVersion, "id", "a version of another plan")
	}
	if n.Version != o.Version+1 {
		return Materiality{}, refuse(ErrPlanVersion, "version", "%d follows %d only as %d", n.Version, o.Version, o.Version+1)
	}
	if o.Primary.Repository != n.Primary.Repository {
		return Materiality{ParentRequired: true, NewParentRequired: true, Reauthorize: childIDs(n)},
			refuse(ErrPrimaryRequiresNewParent, "primary.repository", "changing the primary starts a new parent (MR3)")
	}
	t := table{o: o, n: n, reauth: map[string]bool{}}
	if o.Title != n.Title {
		t.repost("title")
	}
	if o.Primary.Rationale != n.Primary.Rationale {
		t.repost("primary.rationale")
	}
	for _, id := range unionIDs(o, n) {
		t.child(id)
	}
	t.mergeOrder()
	t.integration()
	if o.Budget.TotalMicroUSD != n.Budget.TotalMicroUSD {
		t.material("budget.totalMicroUSD")
	}
	if o.Budget.PlanningMicroUSD != n.Budget.PlanningMicroUSD {
		t.material("budget.planningMicroUSD")
	}
	return t.result(), nil
}

// AuthorizationsFor is the previous child authorizations that survive into next under m: a child
// still in next, at the same repository, not in m.Reauthorize, and at the fingerprint next gives
// it. Nothing survives a new parent. Each kept record is unchanged, still naming the version its
// maintainer authorized.
func AuthorizationsFor(next v1alpha1.CoordinationPlan, previous []v1alpha1.ChildAuthorization, m Materiality) []v1alpha1.ChildAuthorization {
	if m.NewParentRequired {
		return nil
	}
	var out []v1alpha1.ChildAuthorization
	for _, a := range previous {
		i := slices.IndexFunc(next.Children, func(c v1alpha1.CoordinationChild) bool { return c.ID == a.ChildID })
		if i < 0 || slices.Contains(m.Reauthorize, a.ChildID) || next.Children[i].Repository != a.Repository ||
			a.ChildFingerprint != ChildFingerprint(next, a.ChildID) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// CheckConfirmation refuses with ErrStalePlan a confirmation of anything but the current plan:
// its plan must hash to planHash, and a parent confirmation must name planHash, a child
// authorization childFingerprint. It reads c only, so a stale confirmation changes nothing. One
// naming neither or both is ErrInvalidConfirmation.
func CheckConfirmation(planHash, childFingerprint string, c CoordinationConfirmation) error {
	switch {
	case (c.Parent == nil) == (c.Child == nil):
		return refuse(ErrInvalidConfirmation, "", "a confirmation is the parent's or one child's")
	case c.PlanHash != planHash || PlanHash(c.Plan) != planHash:
		return refuse(ErrStalePlan, "planHash", "not the current plan")
	case c.Parent != nil && c.Parent.PlanHash != planHash:
		return refuse(ErrStalePlan, "parent.planHash", "not the current plan")
	case c.Child != nil && (childFingerprint == "" || c.Child.ChildFingerprint != childFingerprint ||
		ChildFingerprint(c.Plan, c.Child.ChildID) != childFingerprint):
		return refuse(ErrStalePlan, "child.childFingerprint", "not the child's current fingerprint")
	}
	return nil
}

// table accumulates one comparison's rows.
type table struct {
	o, n     v1alpha1.CoordinationPlan
	sections []string
	parent   bool
	reauth   map[string]bool
}

// repost is a title or rationale row: the parent reviews the repost, nobody reconfirms.
func (t *table) repost(section string) { t.sections = append(t.sections, section) }

// material is any other row: the parent confirms again, and so does each child's maintainer.
func (t *table) material(section string, children ...string) {
	t.sections = append(t.sections, section)
	t.parent = true
	for _, id := range children {
		t.reauth[id] = true
	}
}

func (t *table) result() Materiality {
	m := Materiality{ParentRequired: t.parent}
	slices.Sort(t.sections)
	m.ChangedSections = slices.Compact(t.sections)
	for _, id := range childIDs(t.n) {
		if t.reauth[id] {
			m.Reauthorize = append(m.Reauthorize, id)
		}
	}
	return m
}

// child applies the child rows to id.
func (t *table) child(id string) {
	f := "children." + id
	oc, inOld := find(t.o, id)
	nc, inNew := find(t.n, id)
	switch {
	case !inOld:
		t.material(f, id)
		return
	case !inNew:
		t.material(f, t.downstream(id)...)
		return
	}
	if oc.Rationale != nc.Rationale {
		t.repost(f + ".rationale")
	}
	own := func(sub string, same bool) {
		if !same {
			t.material(f+"."+sub, id)
		}
	}
	own("repository", oc.Repository == nc.Repository)
	own("maintainer", oc.Maintainer == nc.Maintainer)
	own("class", oc.Class == nc.Class)
	own("tier", oc.Tier == nc.Tier)
	own("roles", slices.Equal(oc.Roles, nc.Roles))
	own("budgetMicroUSD", oc.BudgetMicroUSD == nc.BudgetMicroUSD)
	own("scope.state", oc.Scope.State == nc.Scope.State)
	own("scope.task", oc.Scope.Task == nc.Scope.Task)
	own("scope.touches", slices.Equal(oc.Scope.Touches, nc.Scope.Touches))
	own("scope.leave", slices.Equal(oc.Scope.Leave, nc.Scope.Leave))
	own("scope.plan", slices.Equal(oc.Scope.Plan, nc.Scope.Plan))
	own("scope.assumptions", slices.Equal(oc.Scope.Assumptions, nc.Scope.Assumptions))
	own("scope.acceptance", slices.Equal(oc.Scope.Acceptance, nc.Scope.Acceptance))

	// A produced artifact: the producer and every direct and transitive consumer of it.
	for _, name := range union(names(oc.Produces), names(nc.Produces)) {
		if producedBy(t.o, id, name) == nil || producedBy(t.n, id, name) == nil || *producedBy(t.o, id, name) != *producedBy(t.n, id, name) {
			t.material(f+".produces."+name, append(t.consumersOf(id, name), id)...)
		}
	}
	// A consumed edge or a dependency: the consumer and every transitive downstream child.
	for _, d := range symmetricDiff(oc.DependsOn, nc.DependsOn) {
		t.material(f+".dependsOn."+d, append(t.downstream(id), id)...)
	}
	oe, ne := edges(oc.Consumes), edges(nc.Consumes)
	for _, k := range union(slices.Collect(maps.Keys(oe)), slices.Collect(maps.Keys(ne))) {
		o, inOld := oe[k]
		n, inNew := ne[k]
		if inOld != inNew || o != n {
			t.material(f+".consumes."+k, append(t.downstream(id), id)...)
		}
	}
}

// mergeOrder is the mergeOrder row: a child whose order relative to another child in both versions
// flipped, and its downstream children.
func (t *table) mergeOrder() {
	oi := func(id string) int { return slices.Index(t.o.MergeOrder, id) }
	ni := func(id string) int { return slices.Index(t.n.MergeOrder, id) }
	for _, a := range t.n.MergeOrder {
		for _, b := range t.n.MergeOrder {
			if a == b || oi(a) < 0 || oi(b) < 0 {
				continue
			}
			if (oi(a) < oi(b)) != (ni(a) < ni(b)) {
				t.material("children."+a+".mergeOrder", append(t.downstream(a), a)...)
			}
		}
	}
}

// integration is the integration row: family acceptance changed, so every maintainer.
func (t *table) integration() {
	o, n := t.o.Integration, t.n.Integration
	all := childIDs(t.n)
	for _, row := range []struct {
		field string
		same  bool
	}{
		{"role", o.Role == n.Role},
		{"beforeDependents", *o.BeforeDependents == *n.BeforeDependents},
		{"beforeDependentsRationale", o.BeforeDependentsRationale == n.BeforeDependentsRationale},
		{"runMicroUSD", o.RunMicroUSD == n.RunMicroUSD},
		{"maxRuns", o.MaxRuns == n.MaxRuns},
		{"acceptance", slices.Equal(o.Acceptance, n.Acceptance)},
	} {
		if !row.same {
			t.material("integration."+row.field, all...)
		}
	}
}

// downstream is every child that depends on id, transitively, in either version.
func (t *table) downstream(id string) []string {
	var out []string
	frontier := []string{id}
	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		for _, p := range []v1alpha1.CoordinationPlan{t.o, t.n} {
			for _, c := range p.Children {
				if slices.Contains(c.DependsOn, cur) && c.ID != id && !slices.Contains(out, c.ID) {
					out = append(out, c.ID)
					frontier = append(frontier, c.ID)
				}
			}
		}
	}
	return out
}

// consumersOf is every child consuming from's artifact name in either version, and its
// downstream children.
func (t *table) consumersOf(from, name string) []string {
	var out []string
	for _, p := range []v1alpha1.CoordinationPlan{t.o, t.n} {
		for _, c := range p.Children {
			if slices.ContainsFunc(c.Consumes, func(e v1alpha1.ArtifactConsumer) bool { return e.From == from && e.Artifact == name }) {
				out = append(append(out, c.ID), t.downstream(c.ID)...)
			}
		}
	}
	return out
}

func find(p v1alpha1.CoordinationPlan, id string) (v1alpha1.CoordinationChild, bool) {
	i := slices.IndexFunc(p.Children, func(c v1alpha1.CoordinationChild) bool { return c.ID == id })
	if i < 0 {
		return v1alpha1.CoordinationChild{}, false
	}
	return p.Children[i], true
}

func childIDs(p v1alpha1.CoordinationPlan) []string {
	ids := make([]string, 0, len(p.Children))
	for _, c := range p.Children {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	return ids
}

func unionIDs(a, b v1alpha1.CoordinationPlan) []string { return union(childIDs(a), childIDs(b)) }

func names(as []v1alpha1.ArtifactSpec) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

// edges is each consumed artifact, keyed <from>/<artifact>, to its required flag.
func edges(es []v1alpha1.ArtifactConsumer) map[string]bool {
	out := make(map[string]bool, len(es))
	for _, e := range es {
		out[e.From+"/"+e.Artifact] = e.Required
	}
	return out
}

// union is the sorted, deduplicated union of a and b.
func union(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// symmetricDiff is the sorted values in exactly one of a and b.
func symmetricDiff(a, b []string) []string {
	var out []string
	for _, v := range union(a, b) {
		if slices.Contains(a, v) != slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	return out
}
