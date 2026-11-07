// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// materialityCase is one row of the design's materiality table, as an edit of the example plan.
// Patches are JSON merge patches (RFC 7386) over the plan with children keyed by id and each
// child's produces keyed by name; base applies to both versions, next to the new one only, and
// reverse swaps the two. next's version is old's plus one unless the patch sets it.
type materialityCase struct {
	Name    string          `json:"name"`
	Base    json.RawMessage `json:"base"`
	Next    json.RawMessage `json:"next"`
	Reverse bool            `json:"reverse"`
	Want    Materiality     `json:"want"`
	Error   string          `json:"error"`
}

func TestMaterialityTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/materiality/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []materialityCase
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			old := patch(t, validPlan(t), tc.Base)
			next := patch(t, old, tc.Next)
			if tc.Reverse {
				old, next = next, old
			}
			if !strings.Contains(string(tc.Next), `"version"`) {
				next.Version = old.Version + 1
			}
			oldCopy, nextCopy := old.DeepCopy(), next.DeepCopy()

			got, err := ComparePlans(old, next)
			if ReasonOf(err) != tc.Error {
				t.Fatalf("ComparePlans error = %v, want reason %q", err, tc.Error)
			}
			if !reflect.DeepEqual(norm(got), norm(tc.Want)) {
				t.Fatalf("ComparePlans =\n %+v\nwant\n %+v", got, tc.Want)
			}
			if !reflect.DeepEqual(&old, oldCopy) || !reflect.DeepEqual(&next, nextCopy) {
				t.Fatal("ComparePlans modified a plan")
			}
			if err != nil {
				return
			}

			// Preserving is safe: every child not reauthorized keeps its fingerprint, so an
			// authorization AuthorizationsFor carries forward still names what it authorized.
			var previous []v1alpha1.ChildAuthorization
			for _, c := range old.Children {
				previous = append(previous, authorization(old, c))
			}
			kept := AuthorizationsFor(next, previous, got)
			var keptIDs []string
			for _, a := range kept {
				keptIDs = append(keptIDs, a.ChildID)
				if a.ChildFingerprint != ChildFingerprint(next, a.ChildID) {
					t.Errorf("kept %s at a stale fingerprint", a.ChildID)
				}
			}
			var wantKept []string
			for _, c := range normalize(next).Children {
				if slices.ContainsFunc(old.Children, func(o v1alpha1.CoordinationChild) bool { return o.ID == c.ID }) && !slices.Contains(got.Reauthorize, c.ID) {
					wantKept = append(wantKept, c.ID)
				}
			}
			slices.Sort(keptIDs)
			if !slices.Equal(keptIDs, wantKept) {
				t.Errorf("AuthorizationsFor kept %v, want %v", keptIDs, wantKept)
			}
		})
	}
}

// A primary change carries nothing forward: a new parent needs every authorization again.
func TestAuthorizationsForNewParent(t *testing.T) {
	p := validPlan(t)
	prev := []v1alpha1.ChildAuthorization{authorization(p, p.Children[0])}
	if got := AuthorizationsFor(p, prev, Materiality{NewParentRequired: true}); len(got) != 0 {
		t.Fatalf("kept %v across a new parent", got)
	}
	// An authorization for another repository is never carried, whatever its fingerprint.
	prev[0].Repository = "Smana/elsewhere"
	if got := AuthorizationsFor(p, prev, Materiality{}); len(got) != 0 {
		t.Fatalf("kept an authorization of another repository: %v", got)
	}
}

func TestStaleConfirmation(t *testing.T) {
	p := validPlan(t)
	hash, fp := PlanHash(p), ChildFingerprint(p, "consumer")
	q := validPlan(t)
	child(t, &q, "consumer").Scope.Task += "!"
	q.Version++
	oldHash, oldFP := hash, fp
	hash, fp = PlanHash(q), ChildFingerprint(q, "consumer")

	parent := func(h string) *ParentConfirmation {
		return &ParentConfirmation{PlanHash: h, Actor: "a", LinkedGitHubLogin: "Smana"}
	}
	childAuth := func(f string) *ChildAuthorization {
		a := authorization(q, *child(t, &q, "consumer"))
		a.ChildFingerprint = f
		return &a
	}
	cases := []struct {
		name string
		fp   string
		c    CoordinationConfirmation
		want error
	}{
		{"current parent", "", CoordinationConfirmation{Plan: q, PlanHash: hash, Parent: parent(hash)}, nil},
		{"current child", fp, CoordinationConfirmation{Plan: q, PlanHash: hash, Child: childAuth(fp)}, nil},
		{"parent confirms an old hash", "", CoordinationConfirmation{Plan: q, PlanHash: hash, Parent: parent(oldHash)}, ErrStalePlan},
		{"request carries an old hash", "", CoordinationConfirmation{Plan: p, PlanHash: oldHash, Parent: parent(oldHash)}, ErrStalePlan},
		{"plan does not hash to its claim", "", CoordinationConfirmation{Plan: p, PlanHash: hash, Parent: parent(hash)}, ErrStalePlan},
		{"child authorizes an old fingerprint", fp, CoordinationConfirmation{Plan: q, PlanHash: hash, Child: childAuth(oldFP)}, ErrStalePlan},
		{"child against no current fingerprint", "", CoordinationConfirmation{Plan: q, PlanHash: hash, Child: childAuth("")}, ErrStalePlan},
		{"neither", "", CoordinationConfirmation{Plan: q, PlanHash: hash}, ErrInvalidConfirmation},
		{"both", fp, CoordinationConfirmation{Plan: q, PlanHash: hash, Parent: parent(hash), Child: childAuth(fp)}, ErrInvalidConfirmation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := deepCopyConfirmation(t, tc.c)
			err := CheckConfirmation(hash, tc.fp, tc.c)
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("CheckConfirmation = %v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(before, tc.c) {
				t.Fatal("CheckConfirmation modified the confirmation")
			}
		})
	}
}

// authorization is c's maintainer authorizing c as p holds it.
func authorization(p v1alpha1.CoordinationPlan, c v1alpha1.CoordinationChild) v1alpha1.ChildAuthorization {
	return v1alpha1.ChildAuthorization{
		ChildID: c.ID, ChildFingerprint: ChildFingerprint(p, c.ID), PlanVersion: p.Version, Actor: "a",
		Repository: c.Repository, Permission: "write", FenceSHA: strings.Repeat("b", 40), ConfirmedAt: metav1.Unix(1, 0),
	}
}

func deepCopyConfirmation(t *testing.T, c CoordinationConfirmation) CoordinationConfirmation {
	t.Helper()
	out := c
	out.Plan = *c.Plan.DeepCopy()
	if c.Parent != nil {
		out.Parent = c.Parent.DeepCopy()
	}
	if c.Child != nil {
		out.Child = c.Child.DeepCopy()
	}
	return out
}

// norm makes nil and empty lists compare equal.
func norm(m Materiality) Materiality {
	if len(m.ChangedSections) == 0 {
		m.ChangedSections = nil
	}
	if len(m.Reauthorize) == 0 {
		m.Reauthorize = nil
	}
	return m
}

// patch applies a merge patch to p, children keyed by id and produces by name.
func patch(t *testing.T, p v1alpha1.CoordinationPlan, mp json.RawMessage) v1alpha1.CoordinationPlan {
	t.Helper()
	if len(mp) == 0 {
		return *p.DeepCopy()
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc, edit map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mp, &edit); err != nil {
		t.Fatal(err)
	}
	keyed := map[string]any{}
	for _, c := range doc["children"].([]any) {
		c := c.(map[string]any)
		if prods, ok := c["produces"].([]any); ok {
			byName := map[string]any{}
			for _, a := range prods {
				byName[a.(map[string]any)["name"].(string)] = a
			}
			c["produces"] = byName
		}
		keyed[c["id"].(string)] = c
	}
	doc["children"] = keyed
	doc = merge(doc, edit).(map[string]any)
	var children []any
	for id, c := range doc["children"].(map[string]any) {
		c := c.(map[string]any)
		c["id"] = id
		if byName, ok := c["produces"].(map[string]any); ok {
			var prods []any
			for name, a := range byName {
				a.(map[string]any)["name"] = name
				prods = append(prods, a)
			}
			c["produces"] = prods
		}
		children = append(children, c)
	}
	doc["children"] = children
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var q v1alpha1.CoordinationPlan
	d := json.NewDecoder(bytes.NewReader(out))
	d.DisallowUnknownFields()
	if err := d.Decode(&q); err != nil {
		t.Fatalf("patched plan: %v", err)
	}
	return q
}

// merge is RFC 7386: objects merge recursively, null deletes, anything else replaces.
func merge(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = merge(tm[k], v)
	}
	return tm
}
