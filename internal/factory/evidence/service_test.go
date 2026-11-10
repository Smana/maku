// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

type fakeSource struct {
	file     string
	found    bool
	merged   map[string][]PR // by the filter's first word
	reverts  []Revert
	followed map[string]bool
	err      error
	// gate, when set, holds every search until it is closed or the search's context ends.
	gate chan struct{}

	mu       sync.Mutex
	filters  []string
	inFlight int
	peak     int
	ctxErr   error // a search's context error when it ended
}

func (f *fakeSource) enter(ctx context.Context, filter string) error {
	f.mu.Lock()
	f.filters = append(f.filters, filter)
	f.inFlight++
	f.peak = max(f.peak, f.inFlight)
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inFlight--; f.mu.Unlock() }()
	if f.gate == nil {
		return nil
	}
	select {
	case <-f.gate:
		return nil
	case <-ctx.Done():
		f.mu.Lock()
		f.ctxErr = ctx.Err()
		f.mu.Unlock()
		return ctx.Err()
	}
}

func (f *fakeSource) MergedRecords(ctx context.Context, filter string) ([]PR, error) {
	if err := f.enter(ctx, filter); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.merged[strings.Fields(filter)[0]], f.err
}
func (f *fakeSource) RevertRecords(context.Context) ([]Revert, error) { return f.reverts, nil }
func (f *fakeSource) AnyMerged(ctx context.Context, filter string) (bool, error) {
	if err := f.enter(ctx, filter); err != nil {
		return false, err
	}
	return f.followed[filter], nil
}
func (f *fakeSource) FenceFile(context.Context) ([]byte, bool, error) {
	return []byte(f.file), f.found, nil
}
func (f *fakeSource) DefaultBranch(context.Context) (string, error) { return "main", nil }

func (f *fakeSource) searches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.filters)
}

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

func service(src Source, now func() time.Time) *Service {
	return &Service{Cfg: serviceCfg(), Sources: func(string) (Source, bool) { return src, true }, Now: now}
}

// wait blocks until no refresh of repository runs.
func (s *Service) wait(t *testing.T, repository string) {
	t.Helper()
	s.mu.Lock()
	idle := s.entry(repository).idle()
	s.mu.Unlock()
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh never ended")
	}
}

// The canary at its exit: twenty decisions merged unchanged, no bad outcome, a merged drill. Once
// refreshed, the report proposes the one-line change and is served from the cache for TTL.
func TestServiceReportsAndProposes(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{file: policyFile, found: true,
		merged:  map[string][]PR{"author:app/ogenki-agents": cleanPRs(20)},
		reverts: []Revert{{Body: "Reverts Smana/cloud-native-ref#100", Labels: []Label{{Name: "factory/drill"}}, State: "MERGED"}}}
	s := service(src, func() time.Time { return now })
	if err := s.Refresh(t.Context(), "smana/cloud-native-ref"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Report(t.Context(), "smana/cloud-native-ref")
	if err != nil {
		t.Fatal(err)
	}
	if r.Repository != "Smana/cloud-native-ref" || r.Minimum != 20 || len(r.Classes) != 2 || r.EvidenceAt == nil || !r.EvidenceAt.Equal(now) {
		t.Fatalf("%+v", r)
	}
	c := r.Classes[0]
	if c.Class != "docs-links" || c.Source != "repository" || c.Mode != "shadow" || c.Evidence == nil || !c.Evidence.Eligible || c.Evidence.Clean != 20 ||
		c.Proposal == nil || c.Proposal.Line != 6 || c.Proposal.EditURL != "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" ||
		!strings.Contains(c.Proposal.File, "mode: live   # promoted") {
		t.Fatalf("%+v", c)
	}
	if !slices.Contains(src.filters, "author:app/ogenki-agents merged:>=2026-05-12") ||
		!slices.Contains(src.filters, `label:"factory/merged:docs-links" merged:>=2026-05-12`) {
		t.Fatalf("%q", src.filters)
	}
	n := src.searches()
	if _, err := s.Report(t.Context(), "Smana/cloud-native-ref"); err != nil {
		t.Fatal(err)
	}
	s.wait(t, "Smana/cloud-native-ref")
	if src.searches() != n {
		t.Fatal("read again inside the TTL")
	}
}

// Without classes in the file, the central classes are reported with no proposal: a promotion
// edits the repository's file (MP1), never the central config.
func TestServiceFallsBackToTheCentralClasses(t *testing.T) {
	src := &fakeSource{file: "factory: aws-0\n", found: true}
	s := service(src, time.Now)
	r, err := s.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil || len(r.Classes) != 1 || r.Classes[0].Source != "central" || r.Classes[0].Proposal != nil ||
		r.Classes[0].Unproposed != "not_in_repository_policy" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := s.Report(t.Context(), "Smana/elsewhere"); !errors.Is(err, ErrUnknownRepository) {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context(), "Smana/elsewhere"); !errors.Is(err, ErrUnknownRepository) {
		t.Fatal(err)
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

// The page's read never waits on GitHub's search: with every search held, the report answers at
// once with the policy, the classes and the editor's inputs, each class's evidence pending.
func TestReportAnswersWhileTheSearchesHang(t *testing.T) {
	src := &fakeSource{file: policyFile, found: true, gate: make(chan struct{})}
	defer close(src.gate)
	s := service(src, time.Now)
	done := make(chan Report, 1)
	go func() {
		r, err := s.Report(t.Context(), "Smana/cloud-native-ref")
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	select {
	case r := <-done:
		if len(r.Classes) != 2 || r.Policy == nil || r.DefaultBranch != "main" || r.File == "" || r.EvidenceAt != nil {
			t.Fatalf("%+v", r)
		}
		for _, c := range r.Classes {
			if c.Evidence != nil {
				t.Fatalf("%s: evidence before any search returned: %+v", c.Class, c.Evidence)
			}
		}
		if r.Classes[0].Proposal == nil {
			t.Fatal("the proposal needs the file only, never the evidence")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the report waited on the evidence")
	}
}

// The request that found the evidence missing started its read, and the read outlives the request:
// its context is its own, bounded by the Service's deadline.
func TestACancelledRequestDoesNotCancelTheRead(t *testing.T) {
	src := &fakeSource{file: policyFile, found: true, gate: make(chan struct{}),
		merged: map[string][]PR{"author:app/ogenki-agents": cleanPRs(3)}}
	s := service(src, time.Now)
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := s.Report(ctx, "Smana/cloud-native-ref"); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond) // a read tied to the request would end here
	close(src.gate)
	s.wait(t, "Smana/cloud-native-ref")
	if src.ctxErr != nil {
		t.Fatalf("the read was cancelled with the request: %v", src.ctxErr)
	}
	r, err := s.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil || r.EvidenceAt == nil || r.Classes[0].Evidence == nil || r.Classes[0].Evidence.Clean != 3 {
		t.Fatalf("%+v %v", r, err)
	}
}

// One read per repository at a time: reports and refreshes that arrive while it runs start none.
func TestOneReadPerRepositoryAtATime(t *testing.T) {
	src := &fakeSource{file: policyFile, found: true, gate: make(chan struct{})}
	s := service(src, time.Now)
	for range 5 {
		if _, err := s.Report(t.Context(), "Smana/cloud-native-ref"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Refresh(t.Context(), "Smana/cloud-native-ref"); !errors.Is(err, ErrInFlight) {
		t.Fatalf("a second read started: %v", err)
	}
	close(src.gate)
	s.wait(t, "Smana/cloud-native-ref")
	// One read: the reverts, then per class one author search and two label searches.
	if n := src.searches(); n != 2*3 {
		t.Fatalf("%d searches: %q", n, src.filters)
	}
}

// The classes are read concurrently, never more than Parallel searches at once.
func TestTheClassReadsAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("factory: aws-0\nclasses:\n")
	for i := range 12 {
		fmt.Fprintf(&b, "  c%02d:\n    mode: shadow\n    author: agents\n    harness: docs-standard\n", i)
	}
	src := &fakeSource{file: b.String(), found: true, gate: make(chan struct{})}
	s := service(src, time.Now)
	s.Parallel = 3
	go func() { _ = s.Refresh(t.Context(), "Smana/cloud-native-ref") }()
	deadline := time.After(2 * time.Second)
	for {
		src.mu.Lock()
		in := src.inFlight
		src.mu.Unlock()
		if in == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("%d searches in flight, want 3", in)
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(20 * time.Millisecond) // room for a fourth to start, were it allowed
	close(src.gate)
	s.wait(t, "Smana/cloud-native-ref")
	if src.peak != 3 {
		t.Fatalf("peak %d searches in flight, want 3", src.peak)
	}
	if r, _ := s.Report(t.Context(), "Smana/cloud-native-ref"); len(r.Classes) != 12 || r.Classes[11].Evidence == nil {
		t.Fatalf("%+v", r)
	}
}

// A failed read keeps the last good evidence and says why it is stale; a rate limit backs off
// before the next read, which then replaces both.
func TestAFailedReadKeepsTheLastGoodEvidence(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{file: policyFile, found: true, merged: map[string][]PR{"author:app/ogenki-agents": cleanPRs(4)}}
	s := service(src, func() time.Time { return now })
	if err := s.Refresh(t.Context(), "Smana/cloud-native-ref"); err != nil {
		t.Fatal(err)
	}
	good := now
	now = now.Add(TTL)
	src.mu.Lock()
	src.err = errors.New(`forge: search pull request records: non-200 OK status code: 403 Forbidden body: "API rate limit exceeded for installation"`)
	src.mu.Unlock()
	if err := s.Refresh(t.Context(), "Smana/cloud-native-ref"); err == nil {
		t.Fatal("a failed read is an error")
	}
	r, err := s.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil || r.EvidenceAt == nil || !r.EvidenceAt.Equal(good) || r.EvidenceError != ReasonRateLimited ||
		r.Classes[0].Evidence == nil || r.Classes[0].Evidence.Clean != 4 {
		t.Fatalf("%+v %v", r, err)
	}
	if s.due("Smana/cloud-native-ref") {
		t.Fatal("a rate limit is read again at once")
	}
	now = now.Add(maxBackoff)
	if !s.due("Smana/cloud-native-ref") {
		t.Fatal("never read again after the backoff")
	}
	src.mu.Lock()
	src.err = nil
	src.mu.Unlock()
	if err := s.Refresh(t.Context(), "Smana/cloud-native-ref"); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Report(t.Context(), "Smana/cloud-native-ref"); r.EvidenceError != "" || !r.EvidenceAt.Equal(now) {
		t.Fatalf("%+v", r)
	}
}

func TestReasonNamesTheFailure(t *testing.T) {
	for err, want := range map[error]string{
		errors.New("You have exceeded a secondary rate limit"): ReasonRateLimited,
		fmt.Errorf("x: %w", context.DeadlineExceeded):          ReasonDeadline,
		fmt.Errorf("x: %w", context.Canceled):                  ReasonCancelled,
		errors.New("boom"):                                     ReasonUnreadable,
	} {
		if got := Reason(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}

// The refresher reads the owned repositories that are due, and records each one's evidence age.
func TestRefresherReadsOwnedRepositories(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{file: policyFile, found: true}
	s := service(src, func() time.Time { return now })
	s.Cfg.Current().Repositories = append(s.Cfg.Current().Repositories, config.Repository{Name: "Smana/elsewhere"})
	ages := map[string]time.Duration{}
	r := &Refresher{Service: s, Owned: func(repo string) bool { return repo == "Smana/cloud-native-ref" },
		OnAge: func(_ context.Context, repo string, age time.Duration) { ages[repo] = age }}
	if !r.NeedLeaderElection() {
		t.Fatal("every replica would read GitHub")
	}
	r.Tick(t.Context())
	if src.searches() == 0 || len(ages) != 1 || ages["Smana/cloud-native-ref"] != 0 {
		t.Fatalf("%d searches, ages %v", src.searches(), ages)
	}
	n := src.searches()
	now = now.Add(time.Minute)
	r.Tick(t.Context())
	if src.searches() != n || ages["Smana/cloud-native-ref"] != time.Minute {
		t.Fatalf("read again inside the TTL, or no age: %v", ages)
	}
}

// A repository served from a MergePolicy carries the manifest of its central classes and merge
// block, which the factory's own decoder accepts; one served from the file carries none.
func TestReportCarriesTheMergePolicyManifest(t *testing.T) {
	cfg := serviceCfg()
	cfg.Generations = config.Generations{Repos: map[string]config.RepoGenerations{"smana/cloud-native-ref": {Repository: 2, MergePolicy: 3}}}
	cfg.HarnessProfiles = map[string]config.HarnessProfile{"plain": {}}
	s := &Service{Cfg: cfg, Namespace: "factory", Sources: func(string) (Source, bool) { return &fakeSource{file: "factory: aws-0\n", found: true}, true }, Now: time.Now}
	r, err := s.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil {
		t.Fatal(err)
	}
	if r.MergePolicy != "cloud-native-ref" || r.ManifestPath != "mergepolicy-cloud-native-ref.yaml" {
		t.Fatalf("%+v", r)
	}
	objs, err := resources.Decode([]byte(r.Manifest))
	if err != nil || len(objs) != 1 || objs[0].Kind != resources.KindMergePolicy || objs[0].Name != "cloud-native-ref" || objs[0].Namespace != "factory" {
		t.Fatalf("%v %+v\n%s", err, objs, r.Manifest)
	}
	if !strings.Contains(r.Manifest, "docs-links") || !strings.Contains(r.Manifest, "repositoryRef: cloud-native-ref") {
		t.Fatal(r.Manifest)
	}
	// The editor edits the served classes in the file's shape, never spec.classes; the preview
	// builds the edited manifest in the same namespace, for the same Repository.
	if r.Served == nil || r.Served.Classes["docs-links"].Mode != mergepolicy.ModeShadow || r.Served.Classes["docs-links"].Harness != "plain" ||
		r.Namespace != "factory" || r.RepositoryRef != "cloud-native-ref" {
		t.Fatalf("%+v %+v", r.Served, r)
	}

	// Hand-named objects: the manifest carries their names, never a derived one.
	cfg.Generations.Repos["smana/cloud-native-ref"] = config.RepoGenerations{Repository: 2, MergePolicy: 3, RepositoryName: "custom-repo", PolicyName: "custom-policy"}
	r, err = s.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil || r.MergePolicy != "custom-policy" || r.ManifestPath != "mergepolicy-custom-policy.yaml" {
		t.Fatalf("%+v %v", r, err)
	}
	if objs, err := resources.Decode([]byte(r.Manifest)); err != nil || objs[0].Name != "custom-policy" || !strings.Contains(r.Manifest, "repositoryRef: custom-repo") {
		t.Fatalf("%v\n%s", err, r.Manifest)
	}

	file := service(&fakeSource{file: "factory: aws-0\n", found: true}, time.Now)
	r, err = file.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil || r.Manifest != "" || r.MergePolicy != "" || r.ManifestPath != "" || r.Served != nil {
		t.Fatalf("%+v %v", r, err)
	}
}
