// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/resources"
)

// boundServed is the factory serving cloud-native-ref from its bindings, as resources.Assemble
// serves it.
func boundServed(t *testing.T) *config.Config {
	t.Helper()
	raw, err := os.ReadFile("../resources/testdata/file.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := yaml.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	delete(base, "repositories") // the Repository object serves cloud-native-ref
	file, err := yaml.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("testdata/bound-cloud-native-ref.yaml")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := resources.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	res, err := resources.Assemble(file, objs)
	if err != nil || len(res.Refused) > 0 {
		t.Fatalf("%v %+v", err, res.Refused)
	}
	for _, b := range res.Bindings {
		if !b.Accepted {
			t.Fatalf("binding %+v", b)
		}
	}
	return res.Config
}

// B15, the spec's N row: cloud-native-ref's file rewritten with export stays in sync with the
// bindings the factory serves, divergence 0.
func TestAnExportedBindingFileIsInSync(t *testing.T) {
	c := boundServed(t)
	rc := c.RepositoryFor("Smana/cloud-native-ref")
	f, err := Export(rc, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Parse(raw)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if len(g.MergePolicies) != 4 || g.Factory != "aws-0" {
		t.Fatalf("%s", raw)
	}
	if d := Compare(rc, g, c); len(d) != 0 {
		t.Fatalf("%q\n%s", d, raw)
	}
	w := &Watch{Cfg: c, Metrics: &rec{}}
	w.Observe(t.Context(), "Smana/cloud-native-ref", raw)
	s, _ := w.Snapshot("Smana/cloud-native-ref")
	if s.Err != nil || !s.HasClasses || len(s.Divergences) != 0 || len(s.Refused) != 0 || len(s.Classes) != 4 {
		t.Fatalf("%+v", s)
	}
	if !equalClass(s.Classes["dead-links"].Class, rc.Classes["dead-links"]) {
		t.Fatalf("dead-links:\n file %+v\n central %+v", s.Classes["dead-links"].Class, rc.Classes["dead-links"])
	}
}

// A binding file comparing against central classes names what differs, a refused binding included.
func TestCompareABindingFile(t *testing.T) {
	c := boundServed(t)
	rc := c.RepositoryFor("Smana/cloud-native-ref")
	f, err := Export(rc, c)
	if err != nil {
		t.Fatal(err)
	}
	f.MergePolicies[1].Mode = config.ModeLive
	f.MergePolicies = append(f.MergePolicies, config.Binding{Name: "foo", Mode: config.ModeShadow})
	d := strings.Join(Compare(rc, f, c), "\n")
	if !strings.Contains(d, "revert: central ") || !strings.Contains(d, "foo: refused, MissingRef") || strings.Count(d, "\n") != 1 {
		t.Fatalf("%s", d)
	}
}

// B16: export writes the binding shape, so a repository the config file serves is migrated first.
func TestExportRefusesALegacyServedRepository(t *testing.T) {
	c := central()
	rc := &config.Repository{Name: "o/r", Classes: map[string]config.Class{"docs-links": {Shadow: true}, "revert": {Shadow: true}}}
	if _, err := Export(rc, c); err == nil || !strings.Contains(err.Error(), "migrate the repository first: maku config migrate") {
		t.Fatalf("got %v", err)
	}
}

// boundAuthorityRig is authorityRig with the repository's file binding the factory's policies.
func boundAuthorityRig() (*Watch, *rec, *swapSource) {
	c, _ := boundCentral()
	c.Hash = "one"
	c.PolicyManagement = config.PolicyManagement{Read: true, RepositoryAuthority: true}
	c.Repositories[0].ClassAuthority = config.ClassAuthorityRepository
	r := &rec{}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	src := &swapSource{c: c}
	w := &Watch{Cfg: src, Metrics: r, Stale: 15 * time.Minute, Now: func() time.Time { return now }}
	c.SetClassSource(w.Classes)
	return w, r, src
}

// republish is a new config from the rig's, with one policy replaced: the published config the
// watch re-evaluates an unchanged file under.
func republish(w *Watch, src *swapSource, hash, name string, p config.PolicySpec) {
	next := *src.c
	next.Hash = hash
	next.Policies = map[string]config.ServedPolicy{}
	for k, v := range src.c.Policies {
		next.Policies[k] = v
	}
	next.Policies[name] = config.ServedPolicy{Spec: p, Generation: src.c.Policies[name].Generation + 1}
	next.SetClassSource(w.Classes)
	src.c = &next
}

// B9, B15 under repository authority: one binding that does not resolve leaves the file valid and
// the other classes serving; while the process runs, a binding that stops resolving serves its last
// resolved class, and none after a restart.
func TestARefusedFileBindingIsIsolated(t *testing.T) {
	w, _, src := boundAuthorityRig()
	body := []byte(bindingFile + "  - {name: foo, mode: shadow}\n")
	w.Observe(t.Context(), "o/r", body)
	s, err := w.Authority("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if s.Refused["foo"].Reason != v1alpha1.ReasonMissingRef || len(s.Classes) != 3 {
		t.Fatalf("%+v %v", s.Refused, s.Classes)
	}
	cl := src.c.RepositoryFor("o/r").Classes
	if !cl["docs-links"].Live || !cl["alert-rules"].Shadow {
		t.Fatalf("through classSource: %+v", cl)
	}
	if _, ok := cl["foo"]; ok {
		t.Fatal("foo has no class")
	}

	republish(w, src, "two", "alert-rules", config.PolicySpec{Class: config.Class{MaxModifiedLines: 40}, Requires: []string{"allowedFiles", "companionFiles"}})
	w.Observe(t.Context(), "o/r", body)
	s, err = w.Authority("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if s.Refused["alert-rules"].Reason != config.ReasonMissingParameter {
		t.Fatalf("%+v", s.Refused)
	}
	if a, ok := s.Classes["alert-rules"]; !ok || !a.Class.Shadow || a.Class.MaxModifiedLines != 40 || a.PolicyGeneration != 2 {
		t.Fatalf("the last resolved class serves: %+v", s.Classes)
	}

	restarted := &Watch{Cfg: src, Metrics: &rec{}}
	restarted.Observe(t.Context(), "o/r", body)
	if s, _ := restarted.Snapshot("o/r"); s.Err != nil || len(s.Classes) != 2 {
		t.Fatalf("after a restart: %+v", s)
	}
}

// MP8 for bindings: a policy edit that relaxes a live binding (a wider allowedFiles) runs the class
// in shadow, though the file did not change.
func TestAPolicyRelaxationDemotesALiveBinding(t *testing.T) {
	w, r, src := boundAuthorityRig()
	src.c.Policies["paths"] = config.ServedPolicy{Spec: config.PolicySpec{Class: config.Class{AllowedFiles: []string{"^a/.+$"}}}, Generation: 1}
	body := []byte(bindingFile + "  - {name: paths, mode: live}\n")
	w.Observe(t.Context(), "o/r", body)
	if s, err := w.Authority("o/r"); err != nil || !s.Classes["paths"].Class.Live {
		t.Fatalf("%v %+v", err, s.Classes["paths"])
	}
	republish(w, src, "two", "paths", config.PolicySpec{Class: config.Class{AllowedFiles: []string{"^a/.+$"}, MaxModifiedLines: 9}})
	w.Observe(t.Context(), "o/r", body)
	if s, _ := w.Authority("o/r"); !s.Classes["paths"].Class.Live {
		t.Fatal("a tightening stays live")
	}
	republish(w, src, "three", "paths", config.PolicySpec{Class: config.Class{AllowedFiles: []string{"^a/.+$", "^b/.+$"}}})
	w.Observe(t.Context(), "o/r", body)
	s, err := w.Authority("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Classes["paths"].Class; p.Live || !p.Shadow {
		t.Fatalf("relaxed: %+v", p)
	}
	if cl := src.c.RepositoryFor("o/r").Classes; cl["paths"].Live || !cl["docs-links"].Live {
		t.Fatalf("every reader sees the demotion, and only of paths: %+v", cl)
	}
	if want := []string{"paths tightening", "paths relaxation"}; !slices.Equal(r.changes, want) {
		t.Fatalf("%q", r.changes)
	}
}

// B11: one class's structural change, on resolved classes.
func TestClassChange(t *testing.T) {
	base := config.Class{Live: true, AllowedFiles: []string{"^a/.+$"}, MaxModifiedLines: 10, Checks: true}
	for name, tc := range map[string]struct {
		next config.Class
		kind string
		why  string
	}{
		"the same":       {base, KindUnchanged, ""},
		"a lower cap":    {config.Class{Live: true, AllowedFiles: []string{"^a/.+$"}, MaxModifiedLines: 5, Checks: true}, KindTightening, ""},
		"a wider fence":  {config.Class{Live: true, AllowedFiles: []string{"^a/.+$", "^b/.+$"}, MaxModifiedLines: 10, Checks: true}, KindRelaxation, "allowedFiles broadened"},
		"checks dropped": {config.Class{Live: true, AllowedFiles: []string{"^a/.+$"}, MaxModifiedLines: 10}, KindRelaxation, "checks removed"},
		"an approval":    {config.Class{Live: true, AllowedFiles: []string{"^a/.+$"}, MaxModifiedLines: 10, Checks: true, RequiresApproval: []config.ApprovalLeaf{{ChangedFiles: []string{"^a/x$"}}}}, KindTightening, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := ClassChange(base, tc.next)
			if c.Kind != tc.kind || (tc.why != "" && !strings.Contains(strings.Join(c.Why, "; "), tc.why)) {
				t.Fatalf("%+v", c)
			}
		})
	}
	old := config.Class{Live: true, RequiresApproval: []config.ApprovalLeaf{{ChangedFiles: []string{"^a/x$"}}}}
	if c := ClassChange(old, config.Class{Live: true}); c.Kind != KindRelaxation || !slices.Contains(c.Why, "requiresApproval narrowed") {
		t.Fatalf("%+v", c)
	}
}

// diff between two binding files compares their resolved classes.
func TestStructuralBetweenBindingFiles(t *testing.T) {
	c, rc := boundCentral()
	old, err := Parse([]byte(bindingFile))
	if err != nil {
		t.Fatal(err)
	}
	next, err := Parse([]byte(strings.Replace(bindingFile, "mode: shadow", "mode: live", 1)))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Structural(old, next, rc, c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range ch {
		got[x.Class] = x.Kind
	}
	if got["alert-rules"] != KindRelaxation || got["docs-links"] != KindUnchanged || got["revert"] != KindUnchanged {
		t.Fatalf("%+v", ch)
	}
}

// A published config reaches a cut-over repository's decision at once: Authority resolves the file
// again under it rather than deciding on the class the last read resolved (B9, B13).
func TestAuthorityReadsANewConfigBeforeTheNextRead(t *testing.T) {
	w, _, src := boundAuthorityRig()
	w.Observe(t.Context(), "o/r", []byte(bindingFile))
	republish(w, src, "two", "alert-rules", config.PolicySpec{Class: config.Class{MaxModifiedLines: 10}, Requires: []string{"allowedFiles"}})
	s, err := w.Authority("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if a := s.Classes["alert-rules"]; a.Class.MaxModifiedLines != 10 || a.PolicyGeneration != 3 {
		t.Fatalf("decided on the old class: %+v", a)
	}
	if cl := src.c.RepositoryFor("o/r").Classes; cl["alert-rules"].MaxModifiedLines != 10 {
		t.Fatalf("classSource: %+v", cl["alert-rules"])
	}
	// The file's read time is the fence's, not this re-resolution's.
	if s.Read != w.now() {
		t.Fatalf("read %v", s.Read)
	}

	// A config no longer listing the repository cannot resolve it: never decide on the old class.
	next := *src.c
	next.Hash, next.Repositories = "three", []config.Repository{{Name: "o/other"}}
	src.c = &next
	if _, err := w.Authority("o/r"); !errors.Is(err, ErrStale) {
		t.Fatalf("got %v, want ErrStale", err)
	}
}
