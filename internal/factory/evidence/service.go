// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

// Source is the part of a repository's GitHub connection the evidence reads; *forge.GitHub.
type Source interface {
	MergedRecords(ctx context.Context, filter string) ([]PR, error)
	RevertRecords(ctx context.Context) ([]Revert, error)
	AnyMerged(ctx context.Context, filter string) (bool, error)
	FenceFile(ctx context.Context) ([]byte, bool, error)
	DefaultBranch(ctx context.Context) (string, error)
}

// PolicyPath is the repository file a promotion edits.
const PolicyPath = ".github/agent-factory.yaml"

// The read's bounds. Lookback is how far back merges are read: the decision index's 180 days.
// TTL is how long a repository's evidence is served before GitHub is read again: the page may be
// reloaded at will, GitHub is searched at most once per TTL per repository and replica.
const (
	Lookback      = 180 * 24 * time.Hour
	TTL           = 10 * time.Minute
	defaultWindow = 168 * time.Hour
	// DefaultParallel bounds the searches one read runs at once; DefaultDeadline bounds the read.
	DefaultParallel = 4
	DefaultDeadline = 3 * time.Minute
	// A failed read is tried again after retryAfter; a rate-limited one backs off, doubling, from
	// minBackoff to maxBackoff.
	retryAfter = time.Minute
	minBackoff = 2 * time.Minute
	maxBackoff = 30 * time.Minute
)

// Proposal is the promotion's one-line change: the file as it would be, the line it changes, and
// the GitHub page the maintainer submits it from. Maku never opens the pull request (MP1).
type Proposal struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	File    string `json:"file"`
	EditURL string `json:"editURL"`
}

// ClassReport is one class: its mode where it is declared, its evidence, and, for a shadow class
// of the repository's own file, the promotion's proposal.
type ClassReport struct {
	Class string `json:"class"`
	// Source is where the class is declared: "repository" (the policy file) or "central".
	Source string `json:"source"`
	Mode   string `json:"mode"`
	// Evidence is nil while it is being read: the class was never read, or was added since.
	Evidence *Evidence `json:"evidence,omitempty"`
	Proposal *Proposal `json:"proposal,omitempty"`
	// Unproposed says why a class has no proposal: not_shadow, not_in_repository_policy.
	Unproposed string `json:"unproposed,omitempty"`
}

// PolicyStatus is a MergePolicy's stored status as the leader wrote it, and the object's
// generation and namespace: what `kubectl get mergepolicy` shows.
type PolicyStatus struct {
	Namespace  string                      `json:"namespace"`
	Generation int64                       `json:"generation"`
	Status     factoryv1.MergePolicyStatus `json:"status"`
}

// RepositoryStatusView is a Repository's stored status as the leader wrote it, and the object's
// generation and namespace: what `kubectl get repository` shows.
type RepositoryStatusView struct {
	Namespace  string                     `json:"namespace"`
	Generation int64                      `json:"generation"`
	Status     factoryv1.RepositoryStatus `json:"status"`
}

// BindingView is one binding of a bound repository (B16): its policy, the mode and parameters
// this repository supplies, the generation of the policy its served class came from (0 when none
// is served), and whether it resolved, as the Repository's status says.
type BindingView struct {
	Name             string         `json:"name"`
	Mode             string         `json:"mode"`
	PolicyGeneration int64          `json:"policyGeneration,omitempty"`
	Parameters       config.Binding `json:"parameters"`
	Accepted         bool           `json:"accepted"`
	Reason           string         `json:"reason,omitempty"`
}

// PolicyReport is one class-shape MergePolicy (B16): its definition as served, its manifest and the
// file name to save it under, and its stored status, which lists its consumers.
type PolicyReport struct {
	Name         string                      `json:"name"`
	Generation   int64                       `json:"generation"`
	Spec         config.PolicySpec           `json:"spec"`
	Manifest     string                      `json:"manifest"`
	ManifestPath string                      `json:"manifestPath"`
	Status       factoryv1.MergePolicyStatus `json:"status"`
}

// Report is a repository's policy as read at ReadAt, and its classes' promotion evidence as read
// at EvidenceAt.
type Report struct {
	Repository  string        `json:"repository"`
	Minimum     int           `json:"minimum"`
	PolicyError string        `json:"policyError,omitempty"`
	Classes     []ClassReport `json:"classes"`
	ReadAt      time.Time     `json:"readAt"`
	// EvidenceAt is the last good read of the evidence, nil before the first. EvidenceError is the
	// reason the newest read failed (Reason), "" when it did not: the evidence shown is then older.
	EvidenceAt    *time.Time `json:"evidenceAt,omitempty"`
	EvidenceError string     `json:"evidenceError,omitempty"`
	// The editor's inputs: the policy file as read ("" when absent), its parsed form when it
	// parses, the factory it must keep naming, the default branch its edit link opens, the central
	// harness profiles a class may reference, and the guardrails it is validated against.
	File            string            `json:"file,omitempty"`
	Policy          *mergepolicy.File `json:"policy,omitempty"`
	Factory         string            `json:"factory"`
	DefaultBranch   string            `json:"defaultBranch"`
	HarnessProfiles []string          `json:"harnessProfiles"`
	// AdoptedProfiles are the profiles that run nothing (template none): an adopted class takes
	// only these, an agents class none of them (config.Resolve).
	AdoptedProfiles []string               `json:"adoptedProfiles"`
	Guardrails      config.MergeGuardrails `json:"guardrails"`
	// Hardening is the repository's hardening readiness (H5, HR41), computed afresh by the API for a
	// repository at mode audit or above; nil otherwise.
	Hardening *Readiness `json:"hardening,omitempty"`
	// For a repository served from its Repository's bindings (B3, B16): the object's name, its
	// manifest (spec.mergePolicies and its own merge keys, the Factory's defaults left out), the file
	// name to save it under, its stored status, and each binding with its policy's generation.
	RepositoryObject       string                `json:"repositoryObject,omitempty"`
	RepositoryManifest     string                `json:"repositoryManifest,omitempty"`
	RepositoryManifestPath string                `json:"repositoryManifestPath,omitempty"`
	RepositoryStatus       *RepositoryStatusView `json:"repositoryStatus,omitempty"`
	Bindings               []BindingView         `json:"bindings,omitempty"`
}

// Readiness is one repository's hardening readiness. The operators' route shows it whole: hardened,
// the facts that do not hold (hardening.Missing, or error), and the passing drill it relied on. The
// Evidence page shows a reader hardened, and error alone.
type Readiness struct {
	Repository string   `json:"repository"`
	Hardened   bool     `json:"hardened"`
	Missing    []string `json:"missing,omitempty"`
	Drill      string   `json:"drill,omitempty"`
}

var (
	// ErrUnknownRepository is a repository the config does not list.
	ErrUnknownRepository = errors.New("evidence: the repository is not configured")
	// ErrUnknownPolicy is a MergePolicy the factory does not serve.
	ErrUnknownPolicy = errors.New("evidence: no such MergePolicy is served")
	// ErrInFlight is a refresh asked while one of the same repository runs.
	ErrInFlight = errors.New("evidence: a read of the repository is running")
)

// The reasons a read failed, as logged and reported.
const (
	ReasonRateLimited = "rate_limited"
	ReasonDeadline    = "deadline_exceeded"
	ReasonCancelled   = "cancelled"
	ReasonUnreadable  = "unreadable"
)

// Reason names err for a log line and the page: never GitHub's text.
func Reason(err error) string {
	switch msg := strings.ToLower(err.Error()); {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limited"):
		return ReasonRateLimited
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonDeadline
	case errors.Is(err, context.Canceled):
		return ReasonCancelled
	}
	return ReasonUnreadable
}

// Service reads and caches each configured repository's report. Sources is the repository's
// connection, false without one.
//
// The report splits in two. The policy file and the default branch, two plain reads, are read on
// the caller's request when older than TTL. The evidence, one search per author and label of
// every class and one per merge, never is: a read of it runs on its own context, at most one per
// repository, started by the Refresher or by a report that finds it missing or older than TTL,
// and a report serves what the last good read left (live: a page load cancelled those searches).
type Service struct {
	Cfg     config.Source
	Sources func(repository string) (Source, bool)
	Now     func() time.Time
	// Namespace is the MergePolicy objects' namespace, for the manifest.
	Namespace string
	// PolicyStatus reads the named MergePolicy from the manager's cache; nil reports none.
	PolicyStatus func(ctx context.Context, name string) (PolicyStatus, bool)
	// RepositoryStatus reads the named Repository from the manager's cache; nil reports none.
	RepositoryStatus func(ctx context.Context, name string) (RepositoryStatusView, bool)
	Log              *slog.Logger
	// Parallel bounds the searches one read runs at once, DefaultParallel when 0; Deadline bounds
	// the read, DefaultDeadline when 0.
	Parallel int
	Deadline time.Duration

	mu    sync.Mutex
	repos map[string]*entry
}

// head is what the page needs at once: the policy file and the default branch.
type head struct {
	body   []byte
	found  bool
	branch string
	readAt time.Time
}

// entry is one repository's cache.
type entry struct {
	head    head
	classes map[string]Evidence
	at      time.Time // the last good read of classes
	reason  string    // the newest read's failure, "" after a success
	retry   time.Time // no read before
	backoff time.Duration
	done    chan struct{} // open while a read runs
}

var closed = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// idle is closed once no read runs.
func (e *entry) idle() <-chan struct{} {
	if e.done == nil {
		return closed
	}
	return e.done
}

func (e *entry) due(now time.Time) bool {
	return e.done == nil && !now.Before(e.retry) && (e.at.IsZero() || now.Sub(e.at) >= TTL)
}

func (s *Service) entry(name string) *entry {
	if s.repos == nil {
		s.repos = map[string]*entry{}
	}
	e := s.repos[name]
	if e == nil {
		e = &entry{}
		s.repos[name] = e
	}
	return e
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

func (s *Service) resolve(cfg *config.Config, repository string) (*config.Repository, Source, error) {
	rc := s.repository(cfg, repository)
	if rc == nil {
		return nil, nil, ErrUnknownRepository
	}
	src, ok := s.Sources(rc.Name)
	if !ok {
		return nil, nil, ErrUnknownRepository
	}
	return rc, src, nil
}

// Report is repository's report: its policy, read again when older than TTL (an older one is
// served when that read fails), and the evidence as last read, whose read it starts when due.
func (s *Service) Report(ctx context.Context, repository string) (Report, error) {
	cfg := s.Cfg.Current()
	rc, src, err := s.resolve(cfg, repository)
	if err != nil {
		return Report{}, err
	}
	now := s.Now()
	s.mu.Lock()
	h := s.entry(rc.Name).head
	s.mu.Unlock()
	if h.readAt.IsZero() || now.Sub(h.readAt) >= TTL {
		fresh, err := readHead(ctx, src, now)
		switch {
		case err == nil:
			h = fresh
			s.mu.Lock()
			if e := s.entry(rc.Name); e.head.readAt.Before(h.readAt) {
				e.head = h
			}
			s.mu.Unlock()
		case h.readAt.IsZero():
			return Report{}, err
		default:
			s.log().WarnContext(ctx, "policy unread, served as read before", "repository", rc.Name, "reason", Reason(err), "error", err)
		}
	}
	s.mu.Lock()
	e := s.entry(rc.Name)
	classes, at, reason := e.classes, e.at, e.reason
	start := e.due(now)
	if start {
		e.done = make(chan struct{})
	}
	s.mu.Unlock()
	if start {
		// The request's values, never its cancellation: the read outlives the page load.
		go func() { _ = s.read(context.WithoutCancel(ctx), cfg, rc, src) }()
	}
	return s.build(ctx, cfg, rc, h, classes, at, reason)
}

// Refresh reads repository's policy and every class's evidence now, unless a read of it runs
// (ErrInFlight). A failure keeps the evidence last read.
func (s *Service) Refresh(ctx context.Context, repository string) error {
	cfg := s.Cfg.Current()
	rc, src, err := s.resolve(cfg, repository)
	if err != nil {
		return err
	}
	s.mu.Lock()
	e := s.entry(rc.Name)
	if e.done != nil {
		s.mu.Unlock()
		return ErrInFlight
	}
	e.done = make(chan struct{})
	s.mu.Unlock()
	return s.read(ctx, cfg, rc, src)
}

// due is whether repository's evidence should be read again: missing or older than TTL, no read
// running, and past any backoff.
func (s *Service) due(repository string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entry(repository).due(s.Now())
}

// evidenceAt is the last good read of repository's evidence, false before the first.
func (s *Service) evidenceAt(repository string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.entry(repository).at
	return at, !at.IsZero()
}

func (s *Service) repository(cfg *config.Config, name string) *config.Repository {
	for _, r := range cfg.Repos() {
		if strings.EqualFold(r.Name, name) {
			return &r
		}
	}
	return nil
}

// class is what reading one class needs: its mode and source, the logins whose pull requests it
// judges, and its follow-up window.
type class struct {
	name, source, mode string
	// manifest: a central binding, promoted in the Repository manifest (B5).
	manifest bool
	authors  []string
	window   time.Duration
}

func readHead(ctx context.Context, src Source, now time.Time) (head, error) {
	body, found, err := src.FenceFile(ctx)
	if err != nil {
		return head{}, fmt.Errorf("evidence: %w", err)
	}
	branch, err := src.DefaultBranch(ctx)
	if err != nil {
		return head{}, fmt.Errorf("evidence: %w", err)
	}
	return head{body: body, found: found, branch: branch, readAt: now}, nil
}

// parse is the head's policy file, nil when absent or invalid ("invalid" is then its error).
func (h head) parse() (*mergepolicy.File, string) {
	if !h.found {
		return nil, ""
	}
	f, err := mergepolicy.Parse(h.body)
	if err != nil {
		return nil, "invalid" // the reason, never the parser's text: the file is the repository's
	}
	return f, ""
}

// read is one read of rc's policy and evidence, the caller having claimed the entry's done: it
// runs under the Service's deadline, its classes Parallel at a time, and releases the claim.
func (s *Service) read(ctx context.Context, cfg *config.Config, rc *config.Repository, src Source) error {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(s.Deadline, DefaultDeadline))
	defer cancel()
	start := s.Now()
	h, classes, err := s.collectAll(ctx, cfg, rc, src, start)
	now := s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(rc.Name)
	defer func() { close(e.done); e.done = nil }()
	if err != nil {
		e.reason = Reason(err)
		if e.reason == ReasonRateLimited {
			e.backoff = min(max(2*e.backoff, minBackoff), maxBackoff)
		} else {
			e.backoff = retryAfter
		}
		e.retry = now.Add(e.backoff)
		args := []any{"repository", rc.Name, "reason", e.reason, "error", err, "retry_in", e.backoff}
		if !e.at.IsZero() {
			args = append(args, "served_age", now.Sub(e.at).Round(time.Second))
		}
		s.log().WarnContext(ctx, "policy evidence unread", args...)
		return err
	}
	if e.head.readAt.Before(h.readAt) {
		e.head = h
	}
	e.classes, e.at, e.reason, e.backoff, e.retry = classes, start, "", 0, time.Time{}
	return nil
}

func (s *Service) collectAll(ctx context.Context, cfg *config.Config, rc *config.Repository, src Source, now time.Time) (head, map[string]Evidence, error) {
	h, err := readHead(ctx, src, now)
	if err != nil {
		return head{}, nil, err
	}
	file, _ := h.parse()
	reverts, err := src.RevertRecords(ctx)
	if err != nil {
		return head{}, nil, fmt.Errorf("evidence: %w", err)
	}
	l := Logins{Factory: cfg.FactoryLogin, Merger: rc.Merge.MergerLogin}
	since := now.Add(-Lookback).UTC().Format("2006-01-02")
	minimum := cfg.MergeGuardrails.MinPromotion()
	cs := s.classes(cfg, rc, file)
	out := make([]Evidence, len(cs))
	// Each class reads serially, so Parallel classes at once is Parallel searches at once.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cmp.Or(s.Parallel, DefaultParallel))
	for i, c := range cs {
		g.Go(func() error {
			e, err := collect(gctx, src, c, reverts, rc.Name, l, now, since, minimum)
			out[i] = e
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return head{}, nil, err
	}
	m := make(map[string]Evidence, len(cs))
	for i, c := range cs {
		m[c.name] = out[i]
	}
	return h, m, nil
}

// build is the report of h, each class with its evidence in classes when read.
func (s *Service) build(ctx context.Context, cfg *config.Config, rc *config.Repository, h head, classes map[string]Evidence, at time.Time, reason string) (Report, error) {
	file, perr := h.parse()
	r := Report{Repository: rc.Name, Minimum: cfg.MergeGuardrails.MinPromotion(), ReadAt: h.readAt, PolicyError: perr,
		EvidenceError: reason}
	if !at.IsZero() {
		r.EvidenceAt = &at
	}
	r.File, r.Policy, r.Factory, r.DefaultBranch = string(h.body), file, cfg.Factory, h.branch
	r.HarnessProfiles = slices.Sorted(maps.Keys(cfg.HarnessProfiles))
	r.AdoptedProfiles = []string{}
	for _, n := range r.HarnessProfiles {
		if cfg.HarnessProfiles[n].Template == config.TemplateNone {
			r.AdoptedProfiles = append(r.AdoptedProfiles, n)
		}
	}
	r.Guardrails = cfg.MergeGuardrails
	// Served from its Repository object, whether it binds policies itself or, under repository
	// authority (MP15), its file does: only a repository the config file serves is not migrated.
	if _, repo := cfg.GenerationsFor(rc.Name); repo > 0 || rc.Bound != nil || rc.Bindings != nil {
		if err := s.bound(ctx, cfg, rc, &r); err != nil {
			return Report{}, err
		}
	}
	for _, c := range s.classes(cfg, rc, file) {
		cr := ClassReport{Class: c.name, Source: c.source, Mode: c.mode}
		if e, ok := classes[c.name]; ok {
			cr.Evidence = &e
		}
		switch {
		case c.manifest && c.mode == string(mergepolicy.ModeShadow):
			// B5, central authority: the binding's mode, in the Repository manifest a GitOps pull
			// request changes.
			out, line, err := ProposeInManifest([]byte(r.RepositoryManifest), c.name)
			if err != nil {
				return Report{}, fmt.Errorf("evidence: propose %s: %w", c.name, err)
			}
			cr.Proposal = &Proposal{Path: r.RepositoryManifestPath, Line: line, File: string(out)}
		case c.source != sourceRepository && !c.manifest:
			cr.Unproposed = "not_in_repository_policy"
		case c.mode != string(mergepolicy.ModeShadow):
			cr.Unproposed = "not_shadow"
		default:
			out, line, err := Propose(h.body, c.name)
			if err != nil {
				return Report{}, fmt.Errorf("evidence: propose %s: %w", c.name, err)
			}
			cr.Proposal = &Proposal{Path: PolicyPath, Line: line, File: string(out),
				EditURL: EditURL(rc.Name, h.branch)}
		}
		r.Classes = append(r.Classes, cr)
	}
	return r, nil
}

// bound fills a bound repository's report (B16): its Repository manifest, its status and bindings.
func (s *Service) bound(ctx context.Context, cfg *config.Config, rc *config.Repository, r *Report) error {
	g := cfg.Generations.Repos[strings.ToLower(rc.Name)]
	name := g.RepositoryName
	if name == "" {
		names := make([]string, len(cfg.Repositories))
		for i, x := range cfg.Repositories {
			names[i] = x.Name
		}
		name = resources.ObjectName(rc.Name, names)
	}
	m, err := resources.RepositoryManifest(name, s.Namespace, repositorySpec(cfg, rc))
	if err != nil {
		return fmt.Errorf("evidence: repository manifest: %w", err)
	}
	r.RepositoryObject, r.RepositoryManifest, r.RepositoryManifestPath = name, string(m), "repository-"+name+".yaml"
	var status map[string]factoryv1.BindingStatus
	if s.RepositoryStatus != nil {
		if st, ok := s.RepositoryStatus(ctx, name); ok {
			r.RepositoryStatus = &st
			status = map[string]factoryv1.BindingStatus{}
			for _, b := range st.Status.Classes {
				status[b.Name] = b
			}
		}
	}
	for _, b := range rc.Bindings {
		v := BindingView{Name: b.Name, Mode: b.Mode, Parameters: *b.DeepCopy()}
		if _, served := rc.Classes[b.Name]; served {
			v.PolicyGeneration, v.Accepted = g.Classes[b.Name].Policy, true
		}
		if st, ok := status[b.Name]; ok { // the status writer's verdict, when there is one
			v.Accepted, v.Reason = st.Accepted, st.Reason
		}
		r.Bindings = append(r.Bindings, v)
	}
	return nil
}

// repositorySpec is rc as its Repository object's spec: the merge keys the Factory's defaults
// filled are left out, so the manifest is what the author wrote.
func repositorySpec(cfg *config.Config, rc *config.Repository) map[string]any {
	spec := map[string]any{"factoryRef": cfg.Factory, "repository": rc.Name, "maintainers": rc.Maintainers, "triggerLabel": rc.TriggerLabel}
	var merge, defaults map[string]any
	if cfg.MergeDefaults != nil {
		if b, err := json.Marshal(cfg.MergeDefaults); err == nil {
			_ = json.Unmarshal(b, &defaults)
		}
	}
	if b, err := json.Marshal(rc.Merge); err == nil && json.Unmarshal(b, &merge) == nil {
		for _, k := range rc.MergeDefaulted {
			delete(merge, k)
		}
		// A zero value is what an omitted key reads as, so it is left out and a default added later
		// applies; one the Factory defaults to something else is the author's override, and stays.
		for k, v := range merge {
			if d, ok := defaults[k]; zero(v) && (!ok || zero(d)) {
				delete(merge, k)
			}
		}
		spec["merge"] = merge
	}
	if rc.Bindings != nil {
		spec["mergePolicies"] = rc.Bindings
	}
	if len(rc.Schedules) > 0 {
		spec["schedules"] = rc.Schedules
	}
	if len(rc.Checks.Egress) > 0 {
		spec["checks"] = rc.Checks
	}
	if rc.ClassAuthority != "" {
		spec["classAuthority"] = rc.ClassAuthority
	}
	return spec
}

// zero reports whether a decoded JSON value is its type's zero: 0, "", false, null, or an empty or
// all-zero list or map.
func zero(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case bool:
		return !v
	case float64:
		return v == 0
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		for _, e := range v {
			if !zero(e) {
				return false
			}
		}
		return true
	}
	return false
}

// Policies are the class-shape MergePolicies the factory serves (B16), by name.
func (s *Service) Policies(ctx context.Context) []PolicyReport {
	cfg := s.Cfg.Current()
	out := []PolicyReport{}
	for _, name := range slices.Sorted(maps.Keys(cfg.Policies)) {
		if p, err := s.Policy(ctx, name); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Policy is the served MergePolicy name (B16), ErrUnknownPolicy when the factory serves none.
func (s *Service) Policy(ctx context.Context, name string) (PolicyReport, error) {
	p, ok := s.Cfg.Current().Policies[name]
	if !ok {
		return PolicyReport{}, ErrUnknownPolicy
	}
	m, err := resources.PolicyManifest(name, s.Namespace, p.Spec)
	if err != nil {
		return PolicyReport{}, fmt.Errorf("evidence: policy manifest: %w", err)
	}
	r := PolicyReport{Name: name, Generation: p.Generation, Spec: *p.Spec.DeepCopy(), Manifest: string(m), ManifestPath: "mergepolicy-" + name + ".yaml"}
	if s.PolicyStatus != nil {
		if st, ok := s.PolicyStatus(ctx, name); ok {
			r.Status = st.Status
		}
	}
	return r, nil
}

// EditURL is GitHub's edit page of repository's policy file on branch.
func EditURL(repository, branch string) string {
	return "https://github.com/" + repository + "/edit/" + branch + "/" + PolicyPath
}

const (
	sourceRepository = "repository"
	sourceCentral    = "central"
)

// classes are, under central authority, a bound repository's bindings with the mode served (B5);
// otherwise the repository file's classes or bindings when it declares any, else the central
// config's. A binding file is read only when it is authoritative (stage 4): under central
// authority its modes decide nothing.
func (s *Service) classes(cfg *config.Config, rc *config.Repository, f *mergepolicy.File) []class {
	agents := []string{searchLogin(cfg.AgentsLogin)}
	var out []class
	policy := func(c *class) {
		if p, ok := cfg.Policies[c.name]; ok {
			if p.Spec.Adopted() {
				c.authors = adopters(p.Spec.Author.Adopt.Logins)
			}
			if m := p.Spec.Monitor; m != nil && m.FollowUpWindow.Duration > 0 {
				c.window = m.FollowUpWindow.Duration
			}
		}
	}
	if !cfg.RepositoryAuthoritative(rc) && rc.Bindings != nil {
		for _, b := range slices.SortedFunc(slices.Values(rc.Bindings), func(a, b config.Binding) int { return cmp.Compare(a.Name, b.Name) }) {
			mode := b.Mode
			if cl, ok := rc.Classes[b.Name]; ok { // the served mode: a last resolved class's (B9)
				mode = string(mergepolicy.ModeOff)
				switch {
				case cl.Live:
					mode = string(mergepolicy.ModeLive)
				case cl.Shadow:
					mode = string(mergepolicy.ModeShadow)
				}
			}
			c := class{name: b.Name, source: sourceCentral, mode: mode, authors: agents, window: defaultWindow, manifest: true}
			policy(&c)
			out = append(out, c)
		}
		return out
	}
	if f != nil && f.Bound() && cfg.RepositoryAuthoritative(rc) {
		// B5, B15: a binding's mode is promoted in the file; what it adopts and how long its merges
		// are watched are the policy's, as the factory serves it.
		for _, b := range slices.SortedFunc(slices.Values(f.MergePolicies), func(a, b config.Binding) int { return cmp.Compare(a.Name, b.Name) }) {
			c := class{name: b.Name, source: sourceRepository, mode: b.Mode, authors: agents, window: defaultWindow}
			policy(&c)
			out = append(out, c)
		}
		return out
	}
	for _, name := range slices.Sorted(maps.Keys(rc.Classes)) {
		cl := rc.Classes[name]
		mode := string(mergepolicy.ModeOff)
		switch {
		case cl.Live:
			mode = string(mergepolicy.ModeLive)
		case cl.Shadow:
			mode = string(mergepolicy.ModeShadow)
		}
		c := class{name: name, source: sourceCentral, mode: mode, authors: agents, window: defaultWindow}
		if cl.Adopted() {
			c.authors = adopters(cl.Author.Adopt.Logins)
		}
		if cl.Monitor != nil && cl.Monitor.FollowUpWindow.Duration > 0 {
			c.window = cl.Monitor.FollowUpWindow.Duration
		}
		out = append(out, c)
	}
	return out
}

func adopters(logins []string) []string {
	out := make([]string, 0, len(logins))
	for _, l := range logins {
		out = append(out, searchLogin(l))
	}
	return out
}

// searchLogin is a login as GitHub's search names an author: an App's <slug>[bot] is app/<slug>.
func searchLogin(l string) string {
	if s, ok := strings.CutSuffix(l, "[bot]"); ok {
		return "app/" + s
	}
	return l
}

// collect reads one class: the script's two modes, then MP7.
func collect(ctx context.Context, src Source, c class, reverts []Revert, repo string, l Logins, now time.Time, since string, minimum int) (Evidence, error) {
	var judged []PR
	for _, a := range c.authors {
		prs, err := src.MergedRecords(ctx, fmt.Sprintf("author:%s merged:>=%s", a, since))
		if err != nil {
			return Evidence{}, fmt.Errorf("evidence: %s: %w", c.name, err)
		}
		judged = append(judged, prs...)
	}
	var merges []PR
	for _, label := range []string{"factory/merged:", "factory/auto-merged:"} {
		prs, err := src.MergedRecords(ctx, fmt.Sprintf(`label:"%s%s" merged:>=%s`, label, c.name, since))
		if err != nil {
			return Evidence{}, fmt.Errorf("evidence: %s: %w", c.name, err)
		}
		merges = append(merges, prs...)
	}
	followed := map[int]bool{}
	for _, p := range merges {
		if _, seen := followed[p.Number]; seen {
			continue
		}
		hit, err := src.AnyMerged(ctx, fmt.Sprintf(`label:"factory/follow-up-of:#%d"`, p.Number))
		if err != nil {
			return Evidence{}, fmt.Errorf("evidence: %s: %w", c.name, err)
		}
		followed[p.Number] = hit
	}
	sh := Shadow(judged, reverts, c.name, repo, l)
	out := Outcomes(merges, reverts, followed, repo, l, now, c.window)
	return Evaluate(sh, out, reverts, repo, minimum), nil
}
