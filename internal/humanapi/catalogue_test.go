// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/runrequest"
)

type fakeCatalogue struct {
	calls    []string
	err      error
	repos    []string
	reposErr error
	byUser   map[string]bool // access token -> maintainer
}

func (f *fakeCatalogue) Catalogue(_ context.Context, token, repository, onBehalfOf string) (catalogue.Catalogue, error) {
	f.calls = append(f.calls, token+"|"+repository+"|"+onBehalfOf)
	if f.err != nil {
		return catalogue.Catalogue{}, f.err
	}
	return catalogue.Catalogue{Repository: repository, Maintainer: f.byUser[token]}, nil
}

func (f *fakeCatalogue) Repositories(_ context.Context, token string) ([]string, error) {
	f.calls = append(f.calls, token+"|*repos*")
	if f.reposErr != nil {
		return nil, f.reposErr
	}
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.repos), nil
}

type countReads map[string]int

func (c countReads) IntakeCatalogueRead(_ context.Context, outcome string) { c[outcome]++ }

type tick struct{ t time.Time }

func (c *tick) now() time.Time { return c.t }

func human(sub string) authn.Principal {
	return authn.Principal{Kind: envelope.ActorHuman, ID: "human:" + sub, Sub: sub, AccessToken: "tok-" + sub}
}

func TestCatalogueCacheTTL(t *testing.T) {
	clock := &tick{time.Unix(1_000, 0)}
	src := &fakeCatalogue{}
	c := NewCatalogueCache(src, nil, clock.now)
	p := human("1")
	for range 3 {
		if _, err := c.Get(t.Context(), p, "Smana/cloud-native-ref", ""); err != nil {
			t.Fatal(err)
		}
	}
	clock.t = clock.t.Add(59 * time.Second)
	if _, err := c.Get(t.Context(), p, "smana/CLOUD-native-ref", ""); err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 1 {
		t.Fatalf("calls = %q: within 60 s, whatever the case, one read", src.calls)
	}
	clock.t = clock.t.Add(2 * time.Second)
	if _, err := c.Get(t.Context(), p, "Smana/cloud-native-ref", ""); err != nil || len(src.calls) != 2 {
		t.Fatalf("calls = %q, %v: after 60 s, a new read", src.calls, err)
	}
}

// G7: the answer carries the caller's maintainer flag, so one caller's is never served to another.
func TestCatalogueCacheKeyedByCaller(t *testing.T) {
	src := &fakeCatalogue{byUser: map[string]bool{"tok-1": true}}
	c := NewCatalogueCache(src, nil, nil)
	a, _ := c.Get(t.Context(), human("1"), "Smana/r", "")
	b, _ := c.Get(t.Context(), human("2"), "Smana/r", "")
	if !a.Maintainer || b.Maintainer || len(src.calls) != 2 {
		t.Fatalf("a=%t b=%t calls=%q: two callers, two entries", a.Maintainer, b.Maintainer, src.calls)
	}
	// A sponsored principal's answer is the Slack user's.
	slack := authn.Principal{Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, AccessToken: "tok-s"}
	_, _ = c.Get(t.Context(), slack, "Smana/r", "slack:T/U1")
	_, _ = c.Get(t.Context(), slack, "Smana/r", "slack:T/U2")
	_, _ = c.Get(t.Context(), slack, "Smana/r", "slack:T/U1")
	if len(src.calls) != 4 {
		t.Fatalf("calls = %q: two Slack users, two entries", src.calls)
	}
}

func TestCatalogueCacheBounded(t *testing.T) {
	src := &fakeCatalogue{}
	c := NewCatalogueCache(src, nil, nil)
	for i := range catalogueCap + 1 {
		if _, err := c.Get(t.Context(), human(fmt.Sprint(i)), "Smana/r", ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) != catalogueCap || c.order.Len() != catalogueCap {
		t.Fatalf("entries = %d, order = %d", len(c.entries), c.order.Len())
	}
	n := len(src.calls)
	_, _ = c.Get(t.Context(), human("0"), "Smana/r", "") // the oldest was evicted
	if len(src.calls) != n+1 {
		t.Fatal("the 257th entry did not evict the oldest")
	}
	_, _ = c.Get(t.Context(), human("2"), "Smana/r", "") // the newest ones stay
	if len(src.calls) != n+1 {
		t.Fatal("a recent entry was evicted")
	}
}

func TestCatalogueCacheNeverCachesAnError(t *testing.T) {
	src := &fakeCatalogue{err: errors.New("down")}
	c := NewCatalogueCache(src, nil, nil)
	for range 2 {
		if _, err := c.Get(t.Context(), human("1"), "Smana/r", ""); !errors.Is(err, errCatalogueUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if len(src.calls) != 2 {
		t.Fatalf("calls = %q: an error is read again", src.calls)
	}
	src.err = runrequest.ErrRefused{Status: http.StatusNotFound, Reason: "not_found"}
	_, err := c.Get(t.Context(), human("1"), "Smana/r", "")
	var ref runrequest.ErrRefused
	if !errors.As(err, &ref) || errors.Is(err, errCatalogueUnavailable) || ref.Reason != "not_found" {
		t.Fatalf("a refusal the factory meant stays a refusal: %v", err)
	}
	src.err = runrequest.ErrRefused{Status: http.StatusServiceUnavailable, Reason: "refused"}
	if _, err := c.Get(t.Context(), human("1"), "Smana/r", ""); !errors.Is(err, errCatalogueUnavailable) {
		t.Fatalf("a 503 is unavailable: %v", err)
	}
	var none *CatalogueCache
	if _, err := none.Get(t.Context(), human("1"), "Smana/r", ""); !errors.Is(err, errCatalogueUnavailable) {
		t.Fatalf("no cache: %v", err)
	}
}

func TestCatalogueReadsMetric(t *testing.T) {
	var _ CatalogueReads = (*metrics.Set)(nil)
	reads := countReads{}
	src := &fakeCatalogue{}
	c := NewCatalogueCache(src, reads, nil)
	_, _ = c.Get(t.Context(), human("1"), "Smana/r", "") // miss
	_, _ = c.Get(t.Context(), human("1"), "Smana/r", "") // hit
	src.err = errors.New("down")
	_, _ = c.Get(t.Context(), human("2"), "Smana/r", "") // error
	if reads[metrics.CatalogueMiss] != 1 || reads[metrics.CatalogueHit] != 1 || reads[metrics.CatalogueError] != 1 {
		t.Fatalf("reads = %v", reads)
	}
}

type planBroker struct {
	srv   *Server
	logs  *bytes.Buffer
	clock *tick
}

type planOpt func(*planBroker)

func planEnabled(b *planBroker) {
	b.srv.Intake = &Intake{
		Public: publicRepos{"acme/a": true, "acme/b": true, "acme/gone": true},
		Daily:  50,
	}
}

func planRepositories(repos ...string) planOpt {
	return func(b *planBroker) {
		allow := make(map[string]bool, len(repos))
		for _, r := range repos {
			allow[strings.ToLower(r)] = true
		}
		b.srv.Intake.Allow = allow
	}
}

func brokerWith(opts ...planOpt) *planBroker {
	var logs bytes.Buffer
	clock := &tick{t: time.Unix(1_000, 0)}
	w := world(nil)
	w.set(func() {
		w.writes = map[string][]string{
			"acme/a":    {"dev"},
			"acme/b":    {"dev"},
			"acme/gone": {"dev"},
		}
	})
	srv := &Server{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	w.gate(srv)
	b := &planBroker{srv: srv, logs: &logs, clock: clock}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func factoryServing(repos ...string) *fakeCatalogue {
	return &fakeCatalogue{repos: repos}
}

var factoryDown = &fakeCatalogue{reposErr: errors.New("connection refused")}

func candidatesErr(b *planBroker, src CatalogueSource) ([]string, error) {
	if b.srv.Catalogue == nil {
		b.srv.Catalogue = NewCatalogueCache(src, nil, b.clock.now)
	}
	return b.srv.planRepositories(context.Background(), human("dev"))
}

func candidates(t *testing.T, b *planBroker, src CatalogueSource) []string {
	t.Helper()
	got, err := candidatesErr(b, src)
	if err != nil {
		t.Fatal(err)
	}
	_, reqCandidates, ref := b.srv.requester(t.Context(), human("dev"), intakeArgs{})
	if ref != nil {
		t.Fatalf("requester refused: %v", ref)
	}
	if !slices.Equal(reqCandidates, got) {
		t.Fatalf("requester candidates = %v, planRepositories = %v", reqCandidates, got)
	}
	return got
}

func TestPlannerCandidatesComeFromTheFactory(t *testing.T) { // no plan.repositories in the broker's config
	got := candidates(t, brokerWith(planEnabled), factoryServing("acme/a", "acme/b"))
	if !slices.Equal(got, []string{"acme/a", "acme/b"}) {
		t.Fatalf("got %v", got)
	}
}

func TestPlanRepositoriesNarrows(t *testing.T) {
	b := brokerWith(planEnabled, planRepositories("acme/a", "acme/gone"))
	got := candidates(t, b, factoryServing("acme/a", "acme/b"))
	if !slices.Equal(got, []string{"acme/a"}) {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(b.logs.String(), "acme/gone") {
		t.Fatalf("expected warning naming unserved repository acme/gone, got logs: %s", b.logs.String())
	}
}

func TestPlannerRefusesWhenTheFactoryIsUnreachable(t *testing.T) { // no stale list older than the cache's lifetime
	b := brokerWith(planEnabled)
	_, err := candidatesErr(b, factoryDown)
	if !errors.Is(err, errCatalogueUnavailable) {
		t.Fatalf("got %v", err)
	}
	if _, _, ref := b.srv.requester(t.Context(), human("dev"), intakeArgs{}); ref == nil || ref.Reason != intake.CatalogueUnavailable {
		t.Fatalf("requester refusal = %+v, want %s", ref, intake.CatalogueUnavailable)
	}

	// Warm cache: valid within catalogueTTL, refused once catalogueTTL expires and factory is down.
	warm := brokerWith(planEnabled)
	src := factoryServing("acme/a", "acme/b")
	if got := candidates(t, warm, src); !slices.Equal(got, []string{"acme/a", "acme/b"}) {
		t.Fatalf("got %v", got)
	}
	src.reposErr = errors.New("factory down")
	warm.clock.t = warm.clock.t.Add(catalogueTTL - time.Second)
	if got := candidates(t, warm, src); !slices.Equal(got, []string{"acme/a", "acme/b"}) {
		t.Fatalf("within TTL: got %v", got)
	}
	warm.clock.t = warm.clock.t.Add(2 * time.Second)
	if _, err := candidatesErr(warm, src); !errors.Is(err, errCatalogueUnavailable) {
		t.Fatalf("after TTL: got %v, want errCatalogueUnavailable", err)
	}
}
