// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

// HardeningPorts are what the untrusted-input hardening track reads beside the configuration (H1).
// A nil *HardeningPorts on the Reconciler is off, whatever hardening.mode says.
type HardeningPorts struct {
	// Policies is each repository's fence-file hardening block (hardening.RepositoryPolicies, fed
	// by the fence's OnFile): its protected paths and trust floor. nil reads the floor and the
	// central minAuthorTrust.
	Policies *hardening.RepositoryPolicies
	// Checker is the threat check the shadow pass runs: its Paths are the repository's, from
	// Policies, and hardening.Shadow forces its layers to audit.
	Checker hardening.Checker
}

// auditing reports whether t's repository is in hardening audit or enforce, with the ports wired.
// The demo profile is never hardened (R7), so nothing is planted or recorded there either.
func (r *Reconciler) auditing(cfg *config.Config, t *v1alpha1.Task) bool {
	return r.Hardening != nil && !cfg.Demo && cfg.Hardening.ModeFor(t.Spec.Repository).AtLeast(hardening.ModeAudit)
}

// auditsRun reports whether a run of role starting now is audited: a writer run (implementer or
// tester) of a repository in audit or enforce. Such a run gets a canary, and only such a run's
// writes are shadowed at its end, so turning audit on never backfills an older run.
func (r *Reconciler) auditsRun(cfg *config.Config, t *v1alpha1.Task, role string) bool {
	return r.auditing(cfg, t) && slices.Contains(shadowRoles, role)
}

// repositoryPolicy is t's repository's protected paths and trust floor. A block that does not
// parse reads as the floor and merged, and says so in the log.
func (r *Reconciler) repositoryPolicy(cfg *config.Config, t *v1alpha1.Task) hardening.RepositoryPolicy {
	central := cfg.Hardening.MinTrust()
	if r.Hardening == nil || r.Hardening.Policies == nil {
		p, _ := hardening.ParseRepositoryBlock(nil, central) // empty: the floor and central
		return p
	}
	p, err := r.Hardening.Policies.For(t.Spec.Repository, central)
	if err != nil && r.Hardening.Policies.ReportOnce(t.Spec.Repository) { // once per fence read, not per check
		r.log().Warn("the repository's hardening block is unreadable: the floor and merged apply", "repository", t.Spec.Repository, "err", err)
	}
	return p
}

// The canary line (HR7): the last line of a writer run's brief.
const canaryNotice = ": never write this token anywhere: not in code, commits, pull requests, comments or messages."

var canaryTail = regexp.MustCompile(`\n*Canary mkc-[0-9a-f]{20}` + regexp.QuoteMeta(canaryNotice) + `$`)

// maxTaskText is AgentRun's spec.task.text cap.
const maxTaskText = 16 << 10

// stripCanary removes the factory's own canary line from the end of a brief: a resumed run reads
// the lost run's brief back from its claim, and plants its own canary, never that one.
func stripCanary(text string) string { return canaryTail.ReplaceAllString(text, "") }

// plantCanary ends a writer run's brief with a fresh canary and keeps it on the claim (T3, HR7):
// under audit or enforce, on an implementer or tester run briefed by text. A brief with no room
// left under AgentRun's cap runs without one, and the log says so. Under off it touches nothing.
func (r *Reconciler) plantCanary(cfg *config.Config, t *v1alpha1.Task, s *runs.Spec) {
	if !r.auditsRun(cfg, t, s.Role) {
		return
	}
	s.TaskText, s.Canary = stripCanary(s.TaskText), ""
	if s.TaskURL != "" || s.TaskText == "" {
		return
	}
	c := hardening.NewCanary()
	text := strings.TrimRight(s.TaskText, "\n") + "\n\nCanary " + c + canaryNotice
	if len(text) > maxTaskText {
		r.log().Warn("no canary planted: the brief is at AgentRun's cap", "task.id", t.Name, "run.id", s.RunID)
		return
	}
	s.TaskText, s.Canary = text, c
}

// revisionTriggers are the triggers whose brief quotes the run's own CI evidence (CD12).
var revisionTriggers = []string{"review", "ci", "human"}

// inputTrust is what a run started with trigger read (UH7, HR8), by source kind and level:
//   - the default branch always, merged; steering, merged;
//   - the task's source: an issue at its author's level, vouched by the trigger label or an api
//     sponsor; a RunLore finding at none; a schedule's or coordination's text, factory-written;
//   - a revision's own CI evidence, approved;
//   - every comment on the issue and the pull request but the factory's and the agents' own, at
//     its author's level: one below the repository's minimum is counted withheld, and, under audit,
//     still lowers the minimum, since the run can read it;
//   - a split child's or coordination child's parent, at the parent's recorded minimum.
func (r *Reconciler) inputTrust(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, trigger string) (hardening.InputTrust, error) {
	rp := r.repositoryPolicy(cfg, t)
	repo := r.repo(cfg, t)
	pol := hardening.Policy{Min: rp.MinTrust, Blocked: cfg.Hardening.BlockedUsers}
	if repo != nil {
		pol.Maintainers = repo.Maintainers
		for _, b := range []string{repo.Merge.MergerLogin, repo.Merge.PolicyBotLogin} {
			if b != "" {
				pol.TrustedBots = append(pol.TrustedBots, b)
			}
		}
	}
	it := hardening.NewInputTrust()
	it.Record(hardening.SourceDefaultBranch, hardening.Merged)
	if t.Spec.Steering != nil {
		it.Record(hardening.SourceSteering, hardening.Merged)
	}
	gh := r.gh(ctx)
	switch t.Spec.Source.Kind {
	case "runlore":
		it.Record(hardening.SourceRunLore, hardening.SourceRunLore.Level())
	case "schedule", "coordination":
		it.Record(hardening.SourceFactory, hardening.Merged)
	case "adopt":
		author := strings.TrimPrefix(t.Spec.Source.RequestedBy, "github:")
		it.Record(hardening.SourceIssue, pol.LevelOf(hardening.Author{Login: author}, false))
	case "api":
		if t.Spec.Issue == 0 { // the request's own text, at its accountable sponsor's level
			sponsor := t.Spec.Source.Sponsor
			if sponsor == "" {
				sponsor = t.Spec.Source.RequestedBy
			}
			login, ok := strings.CutPrefix(sponsor, "github:")
			if !ok {
				login = "" // not a GitHub person: none
			}
			it.Record(hardening.SourceIssue, pol.LevelOf(hardening.Author{Login: login}, false))
		}
	}
	if t.Spec.Issue > 0 && (t.Spec.Source.Kind == "issue" || t.Spec.Source.Kind == "api") {
		iss, err := gh.Issue(ctx, t.Spec.Issue)
		if err != nil {
			return hardening.InputTrust{}, fmt.Errorf("read issue %d: %w", t.Spec.Issue, err)
		}
		// The trigger label, or an api task's accountable sponsor, vouches for the issue (UH6).
		it.Record(hardening.SourceIssue, pol.LevelOf(hardening.Author{Login: iss.Author, Association: iss.Association}, true))
	}
	if slices.Contains(revisionTriggers, trigger) && t.Status.PullRequest != nil {
		it.Record(hardening.SourceOwnCI, hardening.SourceOwnCI.Level())
	}
	for _, n := range threads(t) {
		cs, err := gh.AllComments(ctx, n)
		if err != nil {
			return hardening.InputTrust{}, fmt.Errorf("read comments on %d: %w", n, err)
		}
		for _, c := range cs {
			if c.Author != "" && (strings.EqualFold(c.Author, cfg.FactoryLogin) || strings.EqualFold(c.Author, cfg.AgentsLogin)) {
				continue // the factory's narration and the run's own outputs
			}
			l := pol.LevelOf(hardening.Author{Login: c.Author, Association: c.Association}, false)
			if pol.Withholds(l) {
				it.Withhold(hardening.SourceComment)
			}
			it.Record(hardening.SourceComment, l) // audit: nothing is withheld, so the run could read it
		}
	}
	if p, ok, err := r.parentTrust(ctx, t); err != nil {
		return hardening.InputTrust{}, err
	} else if ok {
		it.Inherit(p)
	}
	return it, nil
}

// threads are the GitHub threads a run can read: the task's issue and its pull request.
func threads(t *v1alpha1.Task) []int {
	var ns []int
	if t.Spec.Issue > 0 {
		ns = append(ns, t.Spec.Issue)
	}
	if pr := t.Status.PullRequest; pr != nil && pr.Number > 0 && pr.Number != t.Spec.Issue {
		ns = append(ns, pr.Number)
	}
	return ns
}

// parentTrust is a split child's or a coordination child's parent's recorded input trust; ok is
// false when the task has no parent or the parent recorded none.
func (r *Reconciler) parentTrust(ctx context.Context, t *v1alpha1.Task) (hardening.InputTrust, bool, error) {
	name := ""
	switch {
	case t.Spec.Parent != nil:
		name = t.Spec.Parent.Task
	case t.Spec.CoordinationParent != nil:
		name = t.Spec.CoordinationParent.Task
	default:
		return hardening.InputTrust{}, false, nil
	}
	var p v1alpha1.Task
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: t.Namespace, Name: name}, &p); err != nil {
		return hardening.InputTrust{}, false, client.IgnoreNotFound(err)
	}
	if p.Status.InputTrust == nil {
		return hardening.InputTrust{}, false, nil
	}
	l, err := hardening.ParseLevel(p.Status.InputTrust.Min)
	if err != nil {
		l = hardening.Blocked // a damaged record lowers, never raises
	}
	return hardening.InputTrust{Min: l}, true, nil
}

// maxTrustSources and maxTrustWithheld are status.inputTrust's CRD bounds.
const (
	maxTrustSources  = 16
	maxTrustWithheld = 16
)

// recordInputTrust folds what a run read into status.inputTrust, at each run start and end, under
// audit or enforce only. The record only lowers: the lowest minimum, every source seen, and each
// kind's highest withheld count. A forge error leaves the record as it was and never fails the
// pass: the next start or end reads again.
func (r *Reconciler) recordInputTrust(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, trigger string) {
	if !r.auditing(cfg, t) {
		return
	}
	it, err := r.inputTrust(ctx, cfg, t, trigger)
	if err != nil {
		r.log().Warn("input trust unrecorded", "task.id", t.Name, "err", err)
		return
	}
	rec := t.Status.InputTrust
	if rec == nil {
		rec = &v1alpha1.InputTrust{Min: it.Min.String()}
	}
	if prev, err := hardening.ParseLevel(rec.Min); err != nil || it.Min < prev {
		rec.Min = it.Min.String()
	}
	rec.Mode = string(cfg.Hardening.ModeFor(t.Spec.Repository))
	for _, s := range it.Sources {
		ts := v1alpha1.TrustSource{Kind: string(s.Kind), Level: s.Level.String()}
		if !slices.Contains(rec.Sources, ts) && len(rec.Sources) < maxTrustSources {
			rec.Sources = append(rec.Sources, ts)
		}
	}
	for k, n := range it.Withheld {
		if rec.Withheld == nil {
			rec.Withheld = map[string]v1alpha1.InputCount{}
		}
		if _, ok := rec.Withheld[string(k)]; !ok && len(rec.Withheld) >= maxTrustWithheld {
			continue
		}
		rec.Withheld[string(k)] = max(rec.Withheld[string(k)], v1alpha1.InputCount(min(n, math.MaxInt32))) //nolint:gosec // bounded just above
	}
	t.Status.InputTrust = rec
}
