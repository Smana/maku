// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
)

type fakeSource struct {
	file     string
	found    bool
	merged   map[string][]PR // by the filter's first word
	reverts  []Revert
	followed map[string]bool
	filters  []string
	err      error
}

func (f *fakeSource) MergedRecords(_ context.Context, filter string) ([]PR, error) {
	f.filters = append(f.filters, filter)
	return f.merged[strings.Fields(filter)[0]], f.err
}
func (f *fakeSource) RevertRecords(context.Context) ([]Revert, error) { return f.reverts, nil }
func (f *fakeSource) AnyMerged(_ context.Context, filter string) (bool, error) {
	f.filters = append(f.filters, filter)
	return f.followed[filter], nil
}
func (f *fakeSource) FenceFile(context.Context) ([]byte, bool, error) {
	return []byte(f.file), f.found, nil
}
func (f *fakeSource) DefaultBranch(context.Context) (string, error) { return "main", nil }

func cleanPRs(n int) []PR {
	var out []PR
	for i := range n {
		head := fmt.Sprintf("%040x", i)
		out = append(out, PR{Number: 100 + i, HeadRefOid: head, MergedBy: &Actor{Login: "Smana"}, Comments: []Comment{{
			Author: Actor{Login: "ogenki-agent-factory[bot]"},
			Body: "would auto-merge: `docs-links` <!-- agent-factory scope=x event=would-arm-1 head=" + head + " -->\n\n" +
				"<!-- agent-factory-decision head=" + head + " class=docs-links decision=shadow:shadow_would_arm files=1/3 lines=2/20 paused=false capped=false reviewer=glm-5.3 -->"}}})
	}
	return out
}

func serviceCfg() *config.Config {
	return &config.Config{FactoryLogin: "ogenki-agent-factory[bot]", AgentsLogin: "ogenki-agents[bot]",
		Repositories: []config.Repository{{Name: "Smana/cloud-native-ref", Merge: config.Merge{MergerLogin: "ogenki-agent-merger[bot]"},
			Classes: map[string]config.Class{"docs-links": {Shadow: true}}}}}
}

// The canary at its exit: twenty decisions merged unchanged, no bad outcome, a merged drill. The
// report proposes the one-line change and is served from the cache for TTL.
func TestServiceReportsAndProposes(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{file: policyFile, found: true,
		merged:  map[string][]PR{"author:app/ogenki-agents": cleanPRs(20)},
		reverts: []Revert{{Body: "Reverts Smana/cloud-native-ref#100", Labels: []Label{{Name: "factory/drill"}}, State: "MERGED"}}}
	s := &Service{Cfg: serviceCfg(), Sources: func(string) (Source, bool) { return src, true }, Now: func() time.Time { return now }}
	r, err := s.Report(t.Context(), "smana/cloud-native-ref")
	if err != nil {
		t.Fatal(err)
	}
	if r.Repository != "Smana/cloud-native-ref" || r.Minimum != 20 || len(r.Classes) != 2 {
		t.Fatalf("%+v", r)
	}
	c := r.Classes[0]
	if c.Class != "docs-links" || c.Source != "repository" || c.Mode != "shadow" || !c.Evidence.Eligible || c.Evidence.Clean != 20 ||
		c.Proposal == nil || c.Proposal.Line != 6 || c.Proposal.EditURL != "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" ||
		!strings.Contains(c.Proposal.File, "mode: live   # promoted") {
		t.Fatalf("%+v", c)
	}
	if !slices.Contains(src.filters, "author:app/ogenki-agents merged:>=2026-05-12") ||
		!slices.Contains(src.filters, `label:"factory/merged:docs-links" merged:>=2026-05-12`) {
		t.Fatalf("%q", src.filters)
	}
	n := len(src.filters)
	if _, err := s.Report(t.Context(), "Smana/cloud-native-ref"); err != nil || len(src.filters) != n {
		t.Fatalf("read again inside the TTL: %v", err)
	}
}

// Without classes in the file, the central classes are reported with no proposal: a promotion
// edits the repository's file (MP1), never the central config.
func TestServiceFallsBackToTheCentralClasses(t *testing.T) {
	src := &fakeSource{file: "factory: aws-0\n", found: true}
	s := &Service{Cfg: serviceCfg(), Sources: func(string) (Source, bool) { return src, true }, Now: time.Now}
	r, err := s.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil || len(r.Classes) != 1 || r.Classes[0].Source != "central" || r.Classes[0].Proposal != nil ||
		r.Classes[0].Unproposed != "not_in_repository_policy" || r.Classes[0].Evidence.Eligible {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := s.Report(t.Context(), "Smana/elsewhere"); !errors.Is(err, ErrUnknownRepository) {
		t.Fatal(err)
	}
	bad := &fakeSource{file: "factory: aws-0\n", found: true, err: errors.New("github")}
	s = &Service{Cfg: serviceCfg(), Sources: func(string) (Source, bool) { return bad, true }, Now: time.Now}
	if _, err := s.Report(t.Context(), "Smana/cloud-native-ref"); err == nil {
		t.Fatal("a failed read is an error, never an empty report")
	}
}

// The editor gives a template a profile its author may take: the report names those that run
// nothing, which only an adopted class takes.
func TestServiceNamesTheProfilesAnAdoptedClassTakes(t *testing.T) {
	cfg := serviceCfg()
	cfg.HarnessProfiles = map[string]config.HarnessProfile{"plain": {}, "checked": {Checks: true}, "adopted-none": {Template: config.TemplateNone}}
	src := &fakeSource{file: policyFile, found: true}
	s := &Service{Cfg: cfg, Sources: func(string) (Source, bool) { return src, true }, Now: time.Now}
	r, err := s.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.HarnessProfiles, []string{"adopted-none", "checked", "plain"}) || !slices.Equal(r.AdoptedProfiles, []string{"adopted-none"}) {
		t.Fatalf("%v %v", r.HarnessProfiles, r.AdoptedProfiles)
	}
}
