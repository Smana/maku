// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/factory/config"
)

// The reasons a policy fails to load. Unreadable and stale are counted by the gate of a cut-over
// repository (stage 4), the one reader that depends on a fresh policy.
const (
	ReasonInvalid    = "invalid"
	ReasonGuardrail  = "guardrail_mismatch"
	ReasonUnreadable = "unreadable"
	ReasonStale      = "stale"
)

// Why a cut-over repository's policy gives no authority (stage 4, MP13): the gate pauses its
// auto-merge on each.
var (
	ErrUnreadable = errors.New("the repository policy has not been read")
	ErrStale      = errors.New("the repository policy's last read is older than the fence's staleness window")
	ErrInvalid    = errors.New("the repository policy is invalid or declares no class")
)

// Recorder is the part of the factory's metrics Watch writes.
type Recorder interface {
	PolicyLoadError(ctx context.Context, repository, reason string)
	PolicyDivergence(ctx context.Context, repository string, n int)
	PolicyChange(ctx context.Context, repository, class, change string)
}

// Snapshot is the last file read for a repository, and what it came to: what a decision binds to
// (MP12). Classes holds the resolved classes of a valid file, nil otherwise. HasClasses is true
// when the file declares a policy, in either shape.
type Snapshot struct {
	Hash        string
	HasClasses  bool
	Err         error
	Divergences []string
	Classes     map[string]Resolved
	// Refused are a binding file's bindings that do not resolve (B9, B15): each is absent from
	// Classes, or serves its last resolved class while the process runs.
	Refused map[string]config.BindingRefusal
	// Deprecations are what the next minor refuses in this file (PR4).
	Deprecations []string
	Read         time.Time
	// File is the parsed file of a valid snapshot, for the next one's structural comparison.
	File *File
	// cfgHash is the hash of the config the file was evaluated under: the cache key's second half.
	cfgHash string
}

// Watch observes each owned repository's file as the fence reads it. It decides nothing: the gate
// keeps reading the central config, and dual evaluation reads Snapshot beside it.
type Watch struct {
	Cfg     config.Source
	Metrics Recorder
	Log     *slog.Logger
	Now     func() time.Time
	// Stale is how old a cut-over repository's last read may be and still decide (the fence's
	// staleness window); 0 never ages.
	Stale time.Duration

	mu          sync.RWMutex
	state       map[string]Snapshot
	valid       map[string]Snapshot            // each repository's last valid snapshot with classes
	lastBinding map[string]map[string]Resolved // each repository's bindings' last resolved classes (B9)
	demoted     map[string]map[string][]string // a cut-over repository's relaxed live classes, and why
	evaluations int                            // files parsed and validated, for the cache's test
}

// Observe reads one file. A file with no classes has no policy and records no divergence, and a
// failed read records none either: the gauge keeps the last comparison's value, and a repository
// never compared has no series, so 0 is never "nothing to compare". It logs only when the file or
// its result changes: the fence reads every five minutes. The load-error counter counts every
// failed read, so a rate shows it is still bad. A file whose hash is the last read's is not
// evaluated again under the same config (its Hash): a published config re-evaluates it.
func (w *Watch) Observe(ctx context.Context, repository string, body []byte) {
	cfg := w.Cfg.Current()
	rc := w.listed(cfg, repository)
	if rc == nil {
		return
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	key := strings.ToLower(repository)
	w.mu.RLock()
	prev, seen := w.state[key]
	last := w.lastBinding[key]
	w.mu.RUnlock()
	s := prev
	evaluated := !seen || prev.Hash != hash || prev.cfgHash != cfg.Hash
	if evaluated {
		s = w.evaluate(cfg, rc, hash, body, last)
	}
	s.Read = w.now()
	switch {
	case s.Err != nil && OnlyGuardrail(s.Err):
		w.Metrics.PolicyLoadError(ctx, repository, ReasonGuardrail)
	case s.Err != nil:
		w.Metrics.PolicyLoadError(ctx, repository, ReasonInvalid)
	case s.HasClasses && !cfg.RepositoryAuthoritative(rc):
		w.Metrics.PolicyDivergence(ctx, repository, len(s.Divergences))
	}
	var changes []Change
	w.mu.Lock()
	if w.state == nil {
		w.state, w.valid, w.demoted = map[string]Snapshot{}, map[string]Snapshot{}, map[string]map[string][]string{}
		w.lastBinding = map[string]map[string]Resolved{}
	}
	w.state[key] = s
	if s.Err == nil && s.HasClasses {
		// A binding file is compared again under a new config too: a policy edit changes its classes.
		if prior, ok := w.valid[key]; ok && (prior.Hash != s.Hash || (s.File.Bound() && prior.cfgHash != s.cfgHash)) &&
			cfg.RepositoryAuthoritative(rc) {
			changes = w.demote(cfg, rc, key, prior, s)
		}
		w.valid[key] = s
		if evaluated && s.File.Bound() {
			w.lastBinding[key] = lastResolved(s, last)
		}
	}
	w.mu.Unlock()
	for _, c := range changes {
		w.Metrics.PolicyChange(ctx, repository, c.Class, c.Kind)
		if c.Kind == KindRelaxation {
			w.log().Warn("repository merge policy relaxed: the class runs in shadow", "repository", repository,
				"class", c.Class, "hash", s.Hash, "why", c.Why)
		}
	}
	if !seen || prev.Hash != s.Hash || prev.cfgHash != s.cfgHash {
		w.log().Info("repository merge policy read", "repository", repository, "hash", s.Hash,
			"classes", s.HasClasses, "err", s.Err, "divergences", s.Divergences, "refused", refusedNames(s))
		for _, d := range s.Deprecations {
			w.log().Warn("repository merge policy deprecated", "repository", repository, "hash", s.Hash, "deprecation", d)
		}
	}
}

// refusedNames are a snapshot's refused bindings, as "name (Reason)", in file order.
func refusedNames(s Snapshot) []string {
	if s.File == nil {
		return nil
	}
	var out []string
	for _, b := range s.File.MergePolicies {
		if r, ok := s.Refused[b.Name]; ok {
			out = append(out, b.Name+" ("+r.Reason+")")
		}
	}
	return out
}

// lastResolved is each binding's last resolved class after s: the one resolved now, else the one
// kept before. A binding the file no longer names keeps none.
func lastResolved(s Snapshot, last map[string]Resolved) map[string]Resolved {
	out := map[string]Resolved{}
	for _, b := range s.File.MergePolicies {
		if _, refused := s.Refused[b.Name]; !refused {
			out[b.Name] = s.Classes[b.Name]
		} else if r, ok := last[b.Name]; ok {
			out[b.Name] = r
		}
	}
	return out
}

// evaluate parses, validates, resolves and compares one file. A binding that does not resolve
// serves last's class for it, checked again under cfg and rc's merge (B9): never one that is the
// ambiguity refused or would make one with a served class.
func (w *Watch) evaluate(cfg *config.Config, rc *config.Repository, hash string, body []byte, last map[string]Resolved) Snapshot {
	w.mu.Lock()
	w.evaluations++
	w.mu.Unlock()
	s := Snapshot{Hash: hash, cfgHash: cfg.Hash}
	f, err := Parse(body)
	var rep Report
	if err == nil {
		s.HasClasses = f.Declares()
		rep, err = ValidateFile(f, rc, cfg)
	}
	s.Err, s.Deprecations = err, rep.Deprecations
	if err != nil || !s.HasClasses {
		return s
	}
	s.File = f
	if !cfg.RepositoryAuthoritative(rc) { // a cut-over repository has no central classes to compare with
		s.Divergences = Compare(rc, f, cfg)
	}
	s.Classes, s.Refused = rep.Classes, rep.BindingErrors
	for _, b := range f.MergePolicies {
		refusal, refused := s.Refused[b.Name]
		prior, ok := last[b.Name]
		if !refused || !ok || refusal.Reason == config.ReasonAmbiguousAdoption {
			continue
		}
		served := map[string]config.Class{}
		for name, r := range s.Classes {
			served[name] = r.Class
		}
		if config.AmbiguousWithAny(prior.Class, served) {
			continue
		}
		if cl, again := config.Recheck(b.Name, prior.Class, policyContext(cfg), rc.Merge); again == nil {
			s.Classes[b.Name] = resolvedOf(cl)
		}
	}
	return s
}

func (w *Watch) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

// Snapshot is the last read of repository.
func (w *Watch) Snapshot(repository string) (Snapshot, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s, ok := w.state[strings.ToLower(repository)]
	return s, ok
}

// demote compares a cut-over repository's new valid file with its last (MP8), under w.mu: a
// relaxation of a class the new file declares live runs that class in shadow until the process
// restarts. It returns the changes that are not unchanged, for the caller to count. A comparison
// that fails is a relaxation of every live class: uncertainty fails toward shadow.
func (w *Watch) demote(cfg *config.Config, rc *config.Repository, key string, last, next Snapshot) []Change {
	var changes []Change
	var err error
	if last.File.Bound() || next.File.Bound() {
		changes = StructuralResolved(last.Classes, next.Classes) // each under the config it was read under
	} else {
		changes, err = Structural(last.File, next.File, rc, cfg)
	}
	if err != nil {
		changes = nil
		for name := range next.Classes {
			changes = append(changes, Change{Class: name, Kind: KindRelaxation, Why: []string{err.Error()}})
		}
	}
	var out []Change
	for _, c := range changes {
		if c.Kind == KindUnchanged {
			continue
		}
		out = append(out, c)
		if c.Kind == KindRelaxation && next.Classes[c.Class].Class.Live {
			if w.demoted[key] == nil {
				w.demoted[key] = map[string][]string{}
			}
			w.demoted[key][c.Class] = c.Why
		}
	}
	return out
}

// Authority is a cut-over repository's policy as the gate decides on it (stage 4): its current
// snapshot, fresh within Stale, valid and declaring classes, with each relaxed class in shadow.
// Anything else is an error the gate pauses on (MP13): ErrUnreadable, ErrStale or ErrInvalid.
func (w *Watch) Authority(repository string) (Snapshot, error) {
	key := strings.ToLower(repository)
	w.mu.RLock()
	defer w.mu.RUnlock()
	s, ok := w.state[key]
	switch {
	case !ok:
		return Snapshot{}, ErrUnreadable
	case w.Stale > 0 && w.now().Sub(s.Read) >= w.Stale:
		return s, ErrStale
	case s.Err != nil || !s.HasClasses:
		return s, ErrInvalid
	}
	s.Classes = w.shadowed(s.Classes, w.demoted[key], false)
	return s, nil
}

// Classes are a cut-over repository's classes for every reader but the gate's decision
// (config.SetClassSource): the last valid file's, with relaxed classes in shadow, and every live
// class in shadow while the current file gives no authority, so nothing merges on it.
func (w *Watch) Classes(repository string) (map[string]config.Class, bool) {
	_, authErr := w.Authority(repository)
	key := strings.ToLower(repository)
	w.mu.RLock()
	defer w.mu.RUnlock()
	s, ok := w.valid[key]
	if !ok {
		return nil, false
	}
	out := map[string]config.Class{}
	for name, r := range w.shadowed(s.Classes, w.demoted[key], authErr != nil) {
		out[name] = r.Class
	}
	return out, true
}

// shadowed is classes with each demoted class, or every class when all is set, live no more:
// a live class runs in shadow, every variant of it.
func (w *Watch) shadowed(classes map[string]Resolved, demoted map[string][]string, all bool) map[string]Resolved {
	if !all && len(demoted) == 0 {
		return classes
	}
	out := make(map[string]Resolved, len(classes))
	for name, r := range classes {
		if _, d := demoted[name]; all || d {
			r = r.inShadow()
		}
		out[name] = r
	}
	return out
}

func (w *Watch) listed(cfg *config.Config, repository string) *config.Repository {
	for _, r := range cfg.Repos() {
		if strings.EqualFold(r.Name, repository) {
			return &r
		}
	}
	return nil
}

func (w *Watch) log() *slog.Logger {
	if w.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return w.Log
}
