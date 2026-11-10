// SPDX-License-Identifier: Apache-2.0

// Package configsource builds the factory's live config from its file and its Factory,
// Repository and MergePolicy objects, and publishes it to a config.Live (factory resources
// D5, D13, D14).
//
// The contract:
//   - Sync lists the three kinds in one namespace through an uncached reader, so it works
//     before any cache starts, hands them to resources.Assemble with the file, and publishes
//     the result. Each replica runs its own Source: it needs no leader election (D13).
//   - A refused object does not take its repository down: the last version of it that was
//     served is put back and the set assembled again (D5). Its Outcome says it is not accepted
//     and which generation is still served.
//   - An error is returned only when nothing can be served and nothing is published yet
//     (startup, D14). Afterwards the current config is kept and the refusals are in Outcomes.
//   - With no CRDs installed the objects are none: the factory runs from the file alone, and
//     says so once.
//   - Outcomes are what the status writer reports; the Source never writes to the cluster.
//     OnSynced tells it a Sync completed: it runs with no lock held, so it may call Outcomes.
//     A config.Live subscriber must never call Outcomes: Publish runs under the Source's lock.
package configsource

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/resources"
)

// Outcome is what the factory made of one object, for the status writer.
type Outcome struct {
	Kind resources.Kind
	Name string
	// Repository is the owner/name the object configures: a Repository's spec.repository, a
	// MergePolicy's through its spec.repositoryRef when that Repository is listed; empty otherwise.
	Repository         string
	ObservedGeneration int64  // the object's current generation
	ServedGeneration   int64  // the generation in the published config; 0 when not served
	Accepted           bool   // the current generation is served
	Reason, Message    string // why not, when !Accepted (a v1alpha1.Reason*)
}

// Synced is the last Sync's completion: when, and its error (nil on success). The error is a list
// error, or Assemble's when nothing new could be served (the current config is then kept).
type Synced struct {
	At  time.Time
	Err error
}

type key struct {
	kind resources.Kind
	name string
}

// Source builds the factory's config from its file and its resources.
type Source struct {
	Reader    client.Reader // uncached (mgr.GetAPIReader() or a plain client): works before the cache starts
	Namespace string
	File      []byte
	// Live is nil until the first successful Sync, which creates it. That first Sync must complete
	// before Live is shared: the field itself is not synchronized, only what Live serves.
	Live   *config.Live
	Log    *slog.Logger
	Resync time.Duration // 5m when zero
	// OnSynced, optional, runs after each Sync completes, with no lock held: it may call Outcomes.
	OnSynced func()
	Now      func() time.Time // time.Now when nil

	mu         sync.Mutex // serializes Sync and guards what follows
	lastGood   map[key]resources.Object
	outcomes   []Outcome
	synced     Synced
	crdsAbsent bool

	kickOnce sync.Once
	kick     chan struct{}
}

func (s *Source) kickCh() chan struct{} {
	s.kickOnce.Do(func() { s.kick = make(chan struct{}, 1) })
	return s.kick
}

func (s *Source) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// NeedLeaderElection is false: every replica serves from its own config (D13).
func (s *Source) NeedLeaderElection() bool { return false }

// Outcomes returns a copy of the last Sync's outcomes, sorted by Kind then Name, and when that
// Sync completed with its error. It takes the Source's lock: never call it from a config.Live
// subscriber, which runs under that lock (Publish is called inside Sync).
func (s *Source) Outcomes() ([]Outcome, Synced) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.outcomes), s.synced
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Kick asks for a Sync soon (non-blocking; coalesced).
func (s *Source) Kick() {
	select {
	case s.kickCh() <- struct{}{}:
	default:
	}
}

// Start runs Sync on every Kick and every Resync until ctx ends. A Sync error is logged.
func (s *Source) Start(ctx context.Context) error {
	every := cmp.Or(s.Resync, 5*time.Minute)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.kickCh():
		case <-t.C:
		}
		if err := s.Sync(ctx); err != nil {
			s.log().Error("config sync", "error", err)
		}
	}
}

// Kinds are the three kinds the config is built from, in the order Sync lists them.
func Kinds() []client.Object {
	return []client.Object{&v1alpha1.Factory{}, &v1alpha1.Repository{}, &v1alpha1.MergePolicy{}}
}

// handler kicks on an add, a delete, and an update that changed the spec: a status write leaves
// metadata.generation alone, and must not make every replica sync again.
func (s *Source) handler() toolscache.ResourceEventHandlerFuncs {
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(any) { s.Kick() },
		UpdateFunc: func(prev, next any) {
			p, ok1 := prev.(metav1.Object)
			n, ok2 := next.(metav1.Object)
			if ok1 && ok2 && p.GetGeneration() == n.GetGeneration() {
				return
			}
			s.Kick()
		},
		DeleteFunc: func(any) { s.Kick() },
	}
}

// Watch registers handlers that Kick on kinds, a subset of Kinds: the ones the cache was told
// about (a ByObject entry), so it never starts an informer the manager did not configure. A kind
// with no CRD is skipped.
func (s *Source) Watch(ctx context.Context, c cache.Cache, kinds []client.Object) error {
	h := s.handler()
	for _, obj := range kinds {
		inf, err := c.GetInformer(ctx, obj)
		if err != nil {
			if meta.IsNoMatchError(err) {
				continue
			}
			return fmt.Errorf("watch %T: %w", obj, err)
		}
		if _, err := inf.AddEventHandler(h); err != nil {
			return fmt.Errorf("watch %T: %w", obj, err)
		}
	}
	return nil
}

// Sync lists, assembles and publishes once. It returns an error only when no config can be
// served and none is published yet (startup, D14). OnSynced runs once it has completed.
func (s *Source) Sync(ctx context.Context) error {
	err := s.sync(ctx)
	if s.OnSynced != nil {
		s.OnSynced()
	}
	return err
}

func (s *Source) sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	objs, err := s.list(ctx)
	if err != nil {
		s.synced = Synced{At: s.now(), Err: err}
		return err
	}
	listed := slices.Clone(objs)

	r, aerr := resources.Assemble(s.File, objs)

	// D5: put each refused object's last good version back and assemble again.
	first := map[key]resources.Refusal{}
	replaced := map[key]bool{}
	for _, f := range r.Refused {
		k := key{f.Kind, f.Name}
		good, ok := s.lastGood[k]
		if !ok {
			continue
		}
		for i := range objs {
			if objs[i].Kind == f.Kind && objs[i].Name == f.Name {
				objs[i] = good
				first[k], replaced[k] = f, true
			}
		}
	}
	if len(replaced) > 0 {
		r, aerr = resources.Assemble(s.File, objs)
	}
	for _, w := range r.Warnings {
		s.log().Warn("config", "warning", w)
	}

	if aerr != nil {
		s.outcomes = outcomes(listed, r, first, aerr)
		s.synced = Synced{At: s.now(), Err: aerr}
		if s.Live == nil {
			return fmt.Errorf("no config can be served: %w", aerr)
		}
		s.log().Error("config not servable, keeping the current one", "error", aerr)
		return nil
	}

	r.Config.Generations = generations(r.Accepted)
	if s.Live == nil {
		s.Live = config.NewLive(r.Config)
	} else {
		s.Live.Publish(r.Config)
	}

	// The accepted objects whose current spec was used are the new last good; an object that
	// was replaced keeps its entry, a deleted one is dropped.
	good := map[key]resources.Object{}
	for _, o := range r.Accepted {
		k := key{o.Kind, o.Name}
		if !replaced[k] {
			good[k] = o
		}
	}
	for _, o := range listed {
		k := key{o.Kind, o.Name}
		if _, ok := good[k]; !ok {
			if old, ok := s.lastGood[k]; ok {
				good[k] = old
			}
		}
	}
	s.lastGood = good
	s.outcomes = outcomes(listed, r, first, nil)
	s.synced = Synced{At: s.now()}
	return nil
}

// outcomes reports every listed object; first holds the first-pass refusals of replaced objects.
func outcomes(listed []resources.Object, r resources.Result, first map[key]resources.Refusal, aerr error) []Outcome {
	served := map[key]int64{}
	for _, o := range r.Accepted {
		served[key{o.Kind, o.Name}] = o.Generation
	}
	refused := map[key]resources.Refusal{}
	for _, f := range r.Refused {
		refused[key{f.Kind, f.Name}] = f
	}
	repoOf := map[string]string{} // Repository object name -> owner/name, as listed
	for _, o := range listed {
		if o.Kind == resources.KindRepository {
			repoOf[o.Name], _ = o.Spec["repository"].(string)
		}
	}
	var out []Outcome
	for _, o := range listed {
		k := key{o.Kind, o.Name}
		oc := Outcome{Kind: o.Kind, Name: o.Name, ObservedGeneration: o.Generation}
		switch o.Kind {
		case resources.KindRepository:
			oc.Repository = repoOf[o.Name]
		case resources.KindMergePolicy:
			ref, _ := o.Spec["repositoryRef"].(string)
			oc.Repository = repoOf[ref]
		}
		oc.ServedGeneration = served[k]
		switch f, ok := first[k]; {
		case ok && hasAccepted(r, k) && oc.ServedGeneration == o.Generation:
			// Refused only because its pair was (a Repository whose MergePolicy changed): the
			// object itself is served as it is.
			oc.Accepted = true
		case ok:
			oc.Reason, oc.Message = f.Reason, f.Message
		case hasAccepted(r, k):
			oc.Accepted = true
		default:
			f := refused[k]
			oc.Reason, oc.Message = f.Reason, f.Message
			if oc.Reason == "" {
				oc.Reason = v1alpha1.ReasonInvalid
				if aerr != nil {
					oc.Message = aerr.Error()
				}
			}
		}
		out = append(out, oc)
	}
	slices.SortFunc(out, func(a, b Outcome) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// hasAccepted is true when k is among r's accepted objects, whatever its generation.
func hasAccepted(r resources.Result, k key) bool {
	return slices.ContainsFunc(r.Accepted, func(o resources.Object) bool { return o.Kind == k.kind && o.Name == k.name })
}

// generations are the generations the accepted objects were served at. Repos stays nil when no
// repository object is served: Live.Publish compares with reflect.DeepEqual.
func generations(accepted []resources.Object) config.Generations {
	var g config.Generations
	repoOf := map[string]string{} // Repository object name -> lowercased owner/name
	for _, o := range accepted {
		switch o.Kind {
		case resources.KindFactory:
			g.Factory = o.Generation
		case resources.KindRepository:
			if name, _ := o.Spec["repository"].(string); name != "" {
				repoOf[o.Name] = strings.ToLower(name)
			}
		}
	}
	for _, o := range accepted {
		switch o.Kind {
		case resources.KindRepository:
			if repoOf[o.Name] == "" {
				continue
			}
			if g.Repos == nil {
				g.Repos = map[string]config.RepoGenerations{}
			}
			e := g.Repos[repoOf[o.Name]]
			e.Repository = o.Generation
			g.Repos[repoOf[o.Name]] = e
		case resources.KindMergePolicy:
			ref, _ := o.Spec["repositoryRef"].(string)
			if repoOf[ref] == "" {
				continue // its repository is not served: nothing to record it under
			}
			if g.Repos == nil {
				g.Repos = map[string]config.RepoGenerations{}
			}
			e := g.Repos[repoOf[ref]]
			e.MergePolicy = o.Generation
			g.Repos[repoOf[ref]] = e
		}
	}
	return g
}

// list reads the three kinds. A kind with no CRD is zero objects, logged once.
func (s *Source) list(ctx context.Context) ([]resources.Object, error) {
	var objs []resources.Object
	add := func(kind resources.Kind, name string, gen int64, item any) error {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(item)
		if err != nil {
			return fmt.Errorf("%s %s: %w", kind, name, err)
		}
		spec, _ := u["spec"].(map[string]any)
		if spec == nil {
			spec = map[string]any{}
		}
		objs = append(objs, resources.Object{Kind: kind, Name: name, Namespace: s.Namespace, Generation: gen, Spec: spec})
		return nil
	}
	in := client.InNamespace(s.Namespace)
	var absent bool
	check := func(err error) (bool, error) {
		if err == nil {
			return false, nil
		}
		if meta.IsNoMatchError(err) {
			absent = true
			return true, nil
		}
		return false, err
	}

	var fl v1alpha1.FactoryList
	if skip, err := check(s.Reader.List(ctx, &fl, in)); err != nil {
		return nil, fmt.Errorf("list Factory: %w", err)
	} else if !skip {
		for i := range fl.Items {
			if err := add(resources.KindFactory, fl.Items[i].Name, fl.Items[i].Generation, &fl.Items[i]); err != nil {
				return nil, err
			}
		}
	}
	var rl v1alpha1.RepositoryList
	if skip, err := check(s.Reader.List(ctx, &rl, in)); err != nil {
		return nil, fmt.Errorf("list Repository: %w", err)
	} else if !skip {
		for i := range rl.Items {
			if err := add(resources.KindRepository, rl.Items[i].Name, rl.Items[i].Generation, &rl.Items[i]); err != nil {
				return nil, err
			}
		}
	}
	var pl v1alpha1.MergePolicyList
	if skip, err := check(s.Reader.List(ctx, &pl, in)); err != nil {
		return nil, fmt.Errorf("list MergePolicy: %w", err)
	} else if !skip {
		for i := range pl.Items {
			if err := add(resources.KindMergePolicy, pl.Items[i].Name, pl.Items[i].Generation, &pl.Items[i]); err != nil {
				return nil, err
			}
		}
	}
	if absent && !s.crdsAbsent {
		s.crdsAbsent = true
		s.log().Warn("the factory CRDs are not installed: running from the config file alone")
	}
	return objs, nil
}
