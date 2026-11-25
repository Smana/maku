// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/filefence"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/factory/steering"
)

// ArmInputs is everything §5.1's rule decides on. ReportedHead, Verifiers and Files serve the
// amendment (external reviews R02, R03; ruling R52): ReportedHead is the commit of the
// latest handoff or final room event whose broker-stamped actor is one of the task's implementer
// runs ("" when none named one); Verifiers are the task's reviewer run records, each
// carrying the head it was given; Files is the forge.Diff read of the pull request's diff.
type ArmInputs struct {
	PR           forge.PR
	Checks       forge.Checks
	Task         *v1alpha1.Task
	Cfg          *config.Config `json:"-"` // a fixture is decided under the config it is run with
	ArmedToday   int
	Paused       bool
	ReportedHead string
	// HeadPushedAt is GitHub's server time of the push that put the head on the branch (headPush);
	// zero when the activity was not read, could not be, or its newest move is to another commit.
	// Row 3 reads it only with merge.approveToMerge.
	HeadPushedAt time.Time
	Verifiers    []v1alpha1.RunRecord
	Files        struct{ Base, Head map[string]string }
	// NotOwned: the repository's fence file does not name this factory (v2 D7). It never merges
	// there, whatever else holds.
	NotOwned bool
	// Stats is GitHub's list of the changed files, and DiffRead is true once Files and Stats hold
	// the pull request's diff: until then the declared conditions on the diff hold (MC1).
	Stats    []forge.FileStat
	DiffRead bool
	// ClassArmedToday is today's merges of the task's class, against the class's maxPerDay.
	ClassArmedToday int
	// ArmedByClass and PausedByClass are each policy's merges today and breaker (MR1), keyed by
	// policy name: the gate reads every candidate's. A policy they do not name reads
	// ClassArmedToday and Paused when it is the task's class, else none and closed.
	ArmedByClass  map[string]int  `json:",omitempty"`
	PausedByClass map[string]bool `json:",omitempty"`
	// Adoption is what an adopted class's author mode reads beyond the pull request (MC10): the
	// commits' authors and who pushed the branch. Read is false until awaitingCI has read them.
	Adoption Adoption
	// Contract is what row 2c reads of a task with a contract: the files changed since the tester's
	// commit. Read is false until awaitingCI has read them, and an unread one never passes.
	Contract ContractRead
	// Integration is a coordination dependent's integration hold (v4 MR9), applied to every
	// decision DecideArm returns.
	Integration IntegrationGate
	// Grants are the saved approval grants of the task's repository and class, read immediately
	// before the decision (merge policy stage 5): withApproval consults them in a live class only.
	// Never part of a fixture: a replay reads the subset as a human's, as the gate did without them.
	Grants GrantRead `json:"-"`
	// GrantsByClass holds per-class grant lookups when the policy is chosen from the diff (MR1).
	GrantsByClass map[string]GrantRead `json:"-"`
}

// grantsOf returns the grant read for class: GrantsByClass[class] when present, else Grants.
func (in ArmInputs) grantsOf(class string) GrantRead {
	if gr, ok := in.GrantsByClass[class]; ok {
		return gr
	}
	return in.Grants
}

// GrantRead is one lookup of a repository's grants: Read is false when it failed or was not made,
// and then no grant authorizes (MP13). PolicyHash is the repository policy decided under, At the
// lookup's time, against which a grant's expiry is read.
type GrantRead struct {
	Read       bool
	List       []grant.Grant
	PolicyHash string
	At         time.Time
}

// ContractRead is the files a pull request changed since the tester's contract commit (row 2c).
type ContractRead struct {
	Read    bool
	Changed []string
}

// Adoption is what an adopted class's author mode reads beyond the pull request (MC10).
type Adoption struct {
	Read          bool
	CommitAuthors []string
	Pushed        pushCheck
	// LabelledBy is the newest actor of the class's required label: "" when unread or truncated.
	LabelledBy string
}

// ArmDecision is the verdict and why. Verdict ∈ arm | shadow | wait | ci_red | ci_cancelled | escalate | human.
// Matched is what the class policy-bot's verdict implies: the predicted class on a clean success,
// review on a pending one, gate on an error; a difference from the prediction is a class_mismatch (§2).
type ArmDecision struct {
	Verdict string
	Reason  string
	Matched string
	// Approver is the maintainer whose approval armed row 3 (AM1); "" otherwise.
	Approver string
	// Paused and Capped say what a shadow decision would have met as a live one (AM4).
	Paused, Capped bool
	// Rows are what the decision read, row by row (AM8): its record, on every decision.
	Rows decision.Rows
	// Shadows are the shadow policies that held beside the one that decided (MR1): each records
	// its own would-merge, whatever the decision.
	Shadows []ShadowHold
}

// ShadowHold is one shadow policy that held on the pull request, and what it read.
type ShadowHold struct {
	Name string
	Rows decision.Rows
}

// CIState over the required checks: FAILURE if any Actions-backed run of a required check failed
// or reported a state the rollup's vocabulary does not, PENDING while any required check has no
// Actions-backed run, is still running, or the rollup is truncated, CANCELLED if none failed or
// is pending but one was cancelled (not red: #40), SUCCESS only when every required check has at
// least one Actions-backed run, all matching Actions runs succeeded, and the rollup is not
// truncated. Non-Actions check runs (App != forge.ActionsApp) never satisfy a required check,
// matching GitHub branch protection's app_id pinning.
func CIState(c forge.Checks, required []string) string {
	pending := false
	cancelled := false
	for _, name := range required {
		seen := false
		for _, run := range c.Runs {
			if run.Name != name || run.App != forge.ActionsApp {
				continue
			}
			seen = true
			switch run.State {
			case "SUCCESS":
			case "PENDING":
				pending = true
			case "CANCELLED":
				cancelled = true
			default:
				return "FAILURE"
			}
		}
		if !seen {
			pending = true
		}
	}
	if pending || c.Truncated {
		return "PENDING" // a job is re-run only once its workflow run is complete: GitHub refuses it (403) before
	}
	if cancelled {
		return "CANCELLED" // a failure elsewhere has returned above: it outranks a cancellation
	}
	return "SUCCESS"
}

// contractCheckPassed reports whether at least one GitHub Actions check run of name exists and
// every matching Actions run completed with raw conclusion SUCCESS (never SKIPPED or NEUTRAL).
func contractCheckPassed(c forge.Checks, name string) bool {
	if c.Truncated || name == "" {
		return false
	}
	seen := false
	for _, run := range c.Runs {
		if run.Name != name || run.App != forge.ActionsApp {
			continue
		}
		seen = true
		if run.State != "SUCCESS" || run.Conclusion != "SUCCESS" {
			return false
		}
	}
	return seen
}

// DecideArm is §5.1's merge-actor rule: a fixed base no class can remove, one author mode, and
// the conditions the class declares (merge gate classes MC1). The policy is chosen from the diff
// (MR1): every candidate is judged and the strictest live one that holds decides; a task with
// findings or an adopted pull request is judged on its one class. Every "no" fails towards a human
// (property 3), each with its own reason. Every decision carries the rows it read (AM8).
func DecideArm(in ArmInputs) ArmDecision {
	cfg := in.Cfg
	rc := cfg.RepositoryFor(in.Task.Spec.Repository)
	if rc == nil {
		return notServedDecision(in)
	}
	if !selectsOne(rc.Classes, in.Task) {
		approvals := map[string][]string{}
		for name, cl := range rc.Classes {
			approvals[name] = approvalPaths(cl)
		}
		return withGatePath(decideAmong(in, cfg, rc, rc.Classes, approvals), in, rc, nil)
	}
	cl := gateOf(rc, in.Task.Spec.PredictedClass)
	// B13, PR12: a central class's requiresApproval asks as a repository policy's does.
	return withGatePath(withApproval(decide(in, cfg, rc, cl), in, approvalPaths(cl)), in, rc, cl.ProtectedFiles)
}

// approvalPaths flattens a class's requiresApproval into the subset withApproval reads.
func approvalPaths(cl config.Class) []string {
	var out []string
	for _, a := range cl.RequiresApproval {
		out = append(out, a.ChangedFiles...)
	}
	return out
}

// decide is DecideArm's rule on the class cl, wherever it was read: the central config, or a
// repository's policy (DecideArmUnder). Everything else is read from the central config. It is
// the one-policy path (MR1): a task with findings, an adopted pull request, or DecideArmUnder.
func decide(in ArmInputs, cfg *config.Config, rc *config.Repository, cl config.Class) ArmDecision {
	class := in.Task.Spec.PredictedClass
	rows := recordOf(in, cl)
	if d, done := front(in, cfg, rc, cl, class); done {
		d.Rows = rows
		return withIntegrationHold(in.Integration, d)
	}
	human := func(reason, matched string) ArmDecision {
		return withIntegrationHold(in.Integration, ArmDecision{Verdict: "human", Reason: reason, Matched: matched, Rows: rows})
	}
	if !cl.Live && !cl.Shadow {
		// The class is one the factory never merges: a maintainer merges it. Not a mismatch: no
		// policy was matched, so none is counted.
		return human(reasonNoPolicy, "")
	}
	if cl.Adopted() {
		rows.Evidence = adoptEvidence(in, cl)
	} else {
		rows.Evidence = agentEvidence(in, cfg, cl)
	}
	if rows.Evidence != "" {
		return human(rows.Evidence, class)
	}
	rows.Evidence = "ok"
	if in.DiffRead && cfg.SteeringOn() && touchesSteering(in.Stats) {
		return human("steering_path", class) // no agent pull request changes a note (scoper SC3)
	}
	d, _ := judge(in, rc, class, cl, rows)
	return withIntegrationHold(in.Integration, d)
}

// front is the rows read before any policy's own: the base, the author mode and a maintainer's
// approval. done is false when the decision goes on to the policies.
func front(in ArmInputs, cfg *config.Config, rc *config.Repository, cl config.Class, class string) (ArmDecision, bool) {
	if d, done := baseChecks(in, rc); done {
		return d, true
	}
	human := func(reason, matched string) (ArmDecision, bool) {
		return ArmDecision{Verdict: "human", Reason: reason, Matched: matched}, true
	}
	if cl.Adopted() {
		if reason := adoptAuthor(in, cl); reason != "" {
			return human(reason, class)
		}
	} else if !forge.SameLogin(in.PR.Author, cfg.AgentsLogin) {
		return human("not_agent_authored", "")
	}
	if !cl.Adopted() && rc.Merge.ApproveToMerge { // R-B1: adopted classes keep today's path (AM1)
		switch rv, st := approval(in, rc); st {
		case "veto":
			return human("changes_requested", "review")
		case "stale":
			return human("approval_stale", "review")
		case "unreported":
			return human("head_unreported", "review")
		case "pushed_after_runs":
			// No approval can bind to this head: hand it off as a commit not the task's runs',
			// with the bypass instructions, rather than asking for an approval that cannot count.
			return human("human_commit_in_pr", "review")
		case "approved": // a gate path is held once, over every variant, by withGatePath
			return ArmDecision{Verdict: "arm", Reason: "maintainer_approved", Matched: "review", Approver: rv.Author}, true
		}
	} else {
		for _, rv := range in.PR.Reviews {
			if rv.State == "APPROVED" && rc.IsMaintainer(rv.Author) {
				return human("maintainer_approved", "review") // success came from a human: a human merges
			}
		}
	}
	// An agents pull request answers a veto with a revision run (watch.go). An adopted one has no
	// run, so the veto is the gate's: nothing merges, or is counted as would-merge, over it.
	// A review list cut at GitHub's page may hide a veto: it is one.
	if cl.Adopted() && (in.PR.ReviewsTruncated || len(openChangeRequests(membersReviews(in.PR.Reviews), true)) > 0) {
		return human("changes_requested", class)
	}
	return ArmDecision{}, false
}

// judge is one policy's own rows, on a pull request whose base, author and evidence hold: its
// declared conditions, then its breaker and its day. holds is true when every declared condition
// held, whatever the breaker and the day then say.
func judge(in ArmInputs, rc *config.Repository, name string, cl config.Class, rows decision.Rows) (ArmDecision, bool) {
	rows, reason, wait := declared(cl, in, rows)
	if wait {
		return ArmDecision{Verdict: "wait", Reason: reason, Matched: name, Rows: rows}, false
	} else if reason != "" {
		return ArmDecision{Verdict: "human", Reason: reason, Matched: name, Rows: rows}, false
	}
	paused := in.pausedOf(name)
	capped := in.ArmedToday >= rc.Merge.PerDay() || (cl.MaxPerDay > 0 && in.armedOf(name) >= cl.MaxPerDay)
	rows.Paused, rows.Capped, rows.Gated = paused, capped, true
	switch {
	case !cl.Live: // R32, AM4: every condition holds; the gate is in shadow, which nothing blocks
		return ArmDecision{Verdict: "shadow", Reason: "shadow_would_arm", Matched: name, Paused: paused, Capped: capped, Rows: rows}, true
	case paused:
		return ArmDecision{Verdict: "human", Reason: "class_paused", Matched: name, Rows: rows}, true
	case capped:
		return ArmDecision{Verdict: "human", Reason: "auto_merge_cap", Matched: name, Rows: rows}, true
	}
	return ArmDecision{Verdict: "arm", Matched: name, Rows: rows}, true
}

// withGatePath holds policy-bot's S9 without it: with merge.policyBot false, a maintainer's
// approval never arms a change to a gate path, or a rename from one; it is handed off as
// gate_path, a human merge on GitHub. The gate paths are the protectedFiles of every class of the
// repository's central config plus extra (the decided class's, and every class and variant of a
// repository-authoritative policy): a repository retiring policy-bot moves its .policy.yml gate
// list there (D11). It applies once, to the final decision, so no variant of an any-branch class
// can arm over a gate path. The reconciler reads an approved head's diff whenever policy-bot is off.
func withGatePath(d ArmDecision, in ArmInputs, rc *config.Repository, extra []string) ArmDecision {
	if d.Verdict != "arm" || d.Reason != "maintainer_approved" || rc.Merge.UsesPolicyBot() || !in.DiffRead {
		return d
	}
	protected := slices.Clone(extra)
	for _, name := range slices.Sorted(maps.Keys(rc.Classes)) {
		protected = append(protected, rc.Classes[name].ProtectedFiles...)
	}
	if len(protected) == 0 || filefence.ProtectedFiles(config.Class{ProtectedFiles: protected}, in.Stats) == "" {
		return d
	}
	return ArmDecision{Verdict: "human", Reason: "gate_path", Matched: "gate", Rows: d.Rows}
}

// recordOf is the record's start (AM8), known before any row is read: the class's caps, the
// reviewer model, and the diff's counts once the diff is read, whatever the decision refuses.
func recordOf(in ArmInputs, cl config.Class) decision.Rows {
	rows := decision.Rows{MaxFiles: cl.MaxPatchFiles, MaxLines: cl.MaxModifiedLines, Reviewer: reviewerModel(in.Task), Counted: in.DiffRead}
	if in.DiffRead {
		rows.Files = len(in.Stats)
		for _, f := range in.Stats {
			rows.Lines += f.Additions + f.Deletions
		}
	}
	return rows
}

// reviewerModel is the record's reviewer (AM8, AM10): the newest reviewer run's served models
// joined with "+", "none" without a reviewer run, "" (unread) when its provenance names none.
func reviewerModel(t *v1alpha1.Task) string {
	for i := len(t.Status.Runs) - 1; i >= 0; i-- {
		if r := t.Status.Runs[i]; r.Role == "reviewer" {
			return strings.Join(r.Provenance.ServedModels, "+")
		}
	}
	return "none"
}

// approval is row 3's read of an agents pull request with merge.approveToMerge on (human path
// AM1, ruling R-B1): the newest review of each maintainer. veto when one of them is a request for
// changes, or when GitHub cut the list, so a veto may be unseen; stale when the newest approval
// does not bind to the current head (approvesHead, fail closed), or pushed_after_runs when GitHub
// recorded the head's push after the task's pushing runs had all ended, or unreported when it
// names the head but no implementer run reported that head in the room (gmmrmund: that is not an
// older approval, and only the report returns it); approved when it binds. DecideArm
// calls it for no other pull request: off, or adopted, today's loop reads.
func approval(in ArmInputs, rc *config.Repository) (forge.Review, string) {
	if in.PR.ReviewsTruncated {
		return forge.Review{}, "veto"
	}
	var approved forge.Review
	for _, rv := range newestReviews(in.PR, rc) {
		switch rv.State {
		case "CHANGES_REQUESTED":
			return rv, "veto"
		case "APPROVED":
			if !approvesHead(approved, in) { // an approval of the head wins, whatever the map's order
				approved = rv
			}
		}
	}
	switch {
	case approved.Author == "":
		return forge.Review{}, ""
	case approvesHead(approved, in):
		return approved, "approved"
	case pushedAfterRuns(in):
		return approved, "pushed_after_runs"
	case approved.Commit != "" && approved.Commit == in.PR.HeadSHA && in.ReportedHead != in.PR.HeadSHA:
		return approved, "unreported"
	default:
		return approved, "stale"
	}
}

// newestReviews is each maintainer's newest review that is not a comment, by login folded to lower
// case: what approval and shownApproval read.
func newestReviews(pr forge.PR, rc *config.Repository) map[string]forge.Review {
	newest := map[string]forge.Review{}
	for _, rv := range pr.Reviews {
		if !rc.IsMaintainer(rv.Author) || rv.State == "COMMENTED" {
			continue
		}
		login := strings.ToLower(rv.Author)
		// GitHub's times are whole seconds: two reviews in one second are ordered by id, so a
		// request for changes right after an approval is the newest.
		if cur, ok := newest[login]; !ok || rv.At.After(cur.At) || (rv.At.Equal(cur.At) && rv.ID > cur.ID) {
			newest[login] = rv
		}
	}
	return newest
}

// shownApproval is what an AwaitingHuman task shows of its pull request's reviews with
// merge.approveToMerge off, where nothing merges on it: approval's newest review of each
// maintainer; veto as approval reads one; approved when an approval names the current head and came
// after ended, the end of the task's last pushing run (a force push moves a review's commit to the
// new head, so the commit alone does not say the maintainer read it); envelope.ApprovalBeforeRunsEnded
// for an approval of the head before that; envelope.ApprovalOlderCommit for one of another commit;
// "" without one naming a commit, or when the head is unknown. It reads the pull request and the
// task's status only: no approval binds here, so the head's push is not asked. Among several, the
// oldest review id is shown, so every reconcile shows the same one.
func shownApproval(pr forge.PR, rc *config.Repository, ended time.Time) (forge.Review, string) {
	if pr.ReviewsTruncated {
		return forge.Review{}, "veto"
	}
	var shown forge.Review
	st := ""
	for _, rv := range newestReviews(pr, rc) {
		if rv.State == "CHANGES_REQUESTED" {
			return rv, "veto"
		}
		if rv.State != "APPROVED" || rv.Commit == "" || pr.HeadSHA == "" {
			continue // no commit to name: nothing to show
		}
		got := envelope.ApprovalOlderCommit
		switch {
		case rv.Commit != pr.HeadSHA:
		case !ended.IsZero() && rv.At.After(ended):
			got = "approved"
		default:
			got = envelope.ApprovalBeforeRunsEnded
		}
		if st == "" || (got == "approved" && st != "approved") || (got == st && rv.ID < shown.ID) ||
			(st == envelope.ApprovalOlderCommit && got == envelope.ApprovalBeforeRunsEnded) {
			shown, st = rv, got
		}
	}
	return shown, st
}

// approvesHead: rv names the current head, a run of the task reported that head, GitHub recorded
// its push no later than the end of the task's last pushing run, and rv was submitted more than
// retrySkew after that end. Any of them unknown approves nothing.
//
// A review's commit alone does not bind it. A force push moves a review's commit to an equivalent
// new head, in GraphQL and in REST alike (live on ai-native-ref#250, where only policy-bot's
// dismissal stopped the merge). And an approval sent without a commit_id (`gh pr review
// --approve`) binds to whatever the head is when GitHub receives it: a maintainer who read head A
// while a run pushed B approves B. So the head must stop moving before the approval, and the
// factory's own record says when: the task's runs ended. Their end alone is not enough, though:
// any implementer run of any task can push this branch (the push policy is repository-wide, and
// the Agent-Run trailer is a copyable claim), and a run's pod can push a checkpoint after its
// Finished is stamped. Hence the push's own server time (GitHub's activity) must fall within the
// task's runs. retrySkew pads the approval against the factory's clock running behind GitHub's;
// a skew beyond it reopens the window by the excess.
func approvesHead(rv forge.Review, in ArmInputs) bool {
	pr := in.PR
	if rv.Commit == "" || rv.Commit != pr.HeadSHA || in.ReportedHead != pr.HeadSHA {
		return false
	}
	ended := runsEnded(in.Task)
	if ended.IsZero() || in.HeadPushedAt.IsZero() || in.HeadPushedAt.After(ended) {
		return false
	}
	return rv.At.After(ended.Add(retrySkew))
}

// pushedAfterRuns: GitHub recorded the head's push after the task's pushing runs had all ended, so
// none of them pushed it: a human, or another task's run (the push policy is repository-wide).
func pushedAfterRuns(in ArmInputs) bool {
	ended := runsEnded(in.Task)
	return !ended.IsZero() && in.HeadPushedAt.After(ended)
}

// headPush is the server time of the push that put head on the branch: the newest of the branch's
// activity (newest first, as BranchActivity returns it), when it is a push, a force push or the
// branch's creation that moved the branch to head. Zero otherwise: no activity, or a newest move
// to another commit (the record lags, or the branch moved since the pull request was read).
func headPush(ps []forge.Push, head string) time.Time {
	if len(ps) == 0 || head == "" || ps[0].After != head {
		return time.Time{}
	}
	switch ps[0].Type {
	case "push", "force_push", "branch_creation":
		return ps[0].At
	}
	return time.Time{}
}

// headPushedAt reads ArmInputs.HeadPushedAt from the factory App's view of the branch's activity,
// the read pushedBy already makes. An unreadable or truncated history is zero: the approval then
// reads stale, and a later reconcile reads it again.
func (r *Reconciler) headPushedAt(ctx context.Context, pr forge.PR) time.Time {
	ps, err := r.gh(ctx).BranchActivity(ctx, pr.HeadRef)
	if err != nil && !errors.Is(err, forge.ErrActivityTruncated) {
		r.log().Warn("branch activity unreadable: no approval binds", "branch", pr.HeadRef, "err", err)
		return time.Time{}
	}
	return headPush(ps, pr.HeadSHA) // a truncated history still holds its newest move
}

// runsEnded is when the last of the task's runs that push (implementers, and testers for their
// contract commit) ended, as the factory recorded it; zero when none has or one has not ended.
func runsEnded(t *v1alpha1.Task) time.Time {
	var last time.Time
	if t == nil {
		return last
	}
	for _, rec := range t.Status.Runs {
		if rec.Role != "implementer" && rec.Role != "tester" {
			continue
		}
		if rec.Finished == nil {
			return time.Time{}
		}
		if rec.Finished.After(last) {
			last = rec.Finished.Time
		}
	}
	return last
}

// gateOf is the class as the gate reads it: its config, with the diff shape built into
// docs-links and revert (R52) when the config names none.
func gateOf(rc *config.Repository, class string) config.Class {
	cl, _ := rc.Class(class)
	return withShape(cl, class)
}

// notServedDecision is the decision on a task whose repository the config does not serve: a
// wait that reads no class, so no other repository's settings can arm it (factory resources D12).
func notServedDecision(in ArmInputs) ArmDecision {
	return ArmDecision{Verdict: "wait", Reason: reasonRepositoryNotServed, Matched: in.Task.Spec.PredictedClass}
}

// withShape is cl with the built-in diff shape of its name when it declares none.
func withShape(cl config.Class, class string) config.Class {
	if cl.DiffShape == "" {
		cl.DiffShape = config.BuiltInShape(class)
	}
	return cl
}

// requiredFor is row 2's list (R21, F3-R8): merge.requiredChecks, and merge.contractCheck on a task whose
// tester wrote a contract. Every reader of the required checks calls this, never the config's list.
func requiredFor(rc *config.Repository, t *v1alpha1.Task) []string {
	if rc == nil {
		return nil
	}
	if t == nil || t.Status.Contract == nil || rc.Merge.ContractCheck == "" {
		return rc.Merge.RequiredChecks
	}
	return append(slices.Clone(rc.Merge.RequiredChecks), rc.Merge.ContractCheck)
}

// LabelHold on a pull request keeps it from auto-merging (MR10): anyone who can label may apply it,
// as it only ever stops a merge. Only a maintainer's removal lets the next pass decide.
const LabelHold = "factory/hold"

// baseChecks are the base's rows before any class is read: the secret scan, target branch, check
// rollup completeness, CI, contract check, the fence, policy-bot and, last, factory/hold. done is
// false when all of them hold.
func baseChecks(in ArmInputs, rc *config.Repository) (ArmDecision, bool) {
	// R42 (review G6): TruffleHog runs --only-verified, so a red scan is a live credential in the
	// diff. A fix run would remove it from the head and leave it in a public history. Inspect
	// every check run of that name, not only the first.
	if rc.Merge.LeakScanCheck != "" && slices.ContainsFunc(in.Checks.Runs, func(x forge.Check) bool {
		return x.Name == rc.Merge.LeakScanCheck && x.State == "FAILURE"
	}) {
		return ArmDecision{Verdict: "escalate", Reason: "secret_scan_red"}, true
	}
	if rc.Merge.LeakScanCheck != "" && slices.ContainsFunc(in.Checks.Runs, func(x forge.Check) bool {
		return x.Name == rc.Merge.LeakScanCheck && x.State == "SUCCESS" && x.Conclusion != "SUCCESS"
	}) {
		return ArmDecision{Verdict: "escalate", Reason: "secret_scan_red"}, true
	}
	// Only pull requests targeting main are ever gated or merged: Diff compares against main, so
	// another base (or an absent BaseRef) would be judged against the wrong tree and could bypass
	// the gate.
	if in.PR.BaseRef != "main" {
		return ArmDecision{Verdict: "human", Reason: "base_not_main"}, true
	}
	// A rollup read in part may hide a failing secret scan, required check or policy status: fail closed.
	if in.Checks.Truncated {
		return ArmDecision{Verdict: "human", Reason: "checks_truncated"}, true
	}
	// A task holding a contract while the repository no longer names merge.contractCheck has nothing that
	// proves the contract: fail closed to a human, never drop row 2b.
	if in.Task.Status.Contract != nil && rc.Merge.ContractCheck == "" {
		return ArmDecision{Verdict: "human", Reason: "contract_check_unset"}, true
	}
	switch CIState(in.Checks, requiredFor(rc, in.Task)) {
	case "FAILURE":
		return ArmDecision{Verdict: "ci_red", Reason: "ci_red"}, true
	case "CANCELLED":
		return ArmDecision{Verdict: "ci_cancelled", Reason: "ci_cancelled"}, true
	case "PENDING":
		return ArmDecision{Verdict: "wait", Reason: "ci_pending"}, true
	}
	// Row 2b strict conclusion: a skipped or neutral contract check reads State SUCCESS but ran no
	// contract tests. Every Actions run of contractCheck must carry raw Conclusion SUCCESS.
	if in.Task.Status.Contract != nil && !contractCheckPassed(in.Checks, rc.Merge.ContractCheck) {
		return ArmDecision{Verdict: "human", Reason: "contract_check_not_run"}, true
	}
	// Row 2c (R21, F3-R9): the tests the tester wrote are the task's contract, and no pull request changes
	// them. A person decides, never a fix run: it was the implementer's own change (TF8).
	if c := in.Task.Status.Contract; c != nil {
		if !in.Contract.Read {
			return ArmDecision{Verdict: "wait", Reason: "contract_unread"}, true
		}
		if slices.ContainsFunc(c.Files, func(f string) bool { return slices.Contains(in.Contract.Changed, f) }) {
			return ArmDecision{Verdict: "human", Reason: "contract_modified"}, true
		}
	}
	if in.NotOwned {
		return ArmDecision{Verdict: "human", Reason: "not_owned"}, true
	}
	if !rc.Merge.UsesPolicyBot() {
		return holdLabelled(in) // no policy-bot: the gate's own rows decide
	}
	state, creator := in.Checks.StatusOf("policy-bot: main")
	if !forge.SameLogin(creator, rc.Merge.PolicyBotLogin) {
		return ArmDecision{Verdict: "human", Reason: "policy_absent"}, true // policy-bot down, or a status someone else posted
	}
	switch state {
	case "ERROR":
		return ArmDecision{Verdict: "human", Reason: "gate_path", Matched: "gate"}, true // unmergeable, even with an approval (S9)
	case "SUCCESS":
		return holdLabelled(in)
	default:
		return ArmDecision{Verdict: "human", Reason: "policy_pending", Matched: "review"}, true
	}
}

// holdLabelled is the last base check (MR10): factory/hold hands the pull request to a human before any
// class or approval is read, so nothing merges and no shadow would-merge is counted under it.
// A truncated label list may hide factory/hold, so it fails closed to a human.
func holdLabelled(in ArmInputs) (ArmDecision, bool) {
	if in.PR.LabelsTruncated {
		return ArmDecision{Verdict: "human", Reason: "labels_truncated"}, true
	}
	if slices.Contains(in.PR.Labels, LabelHold) {
		return ArmDecision{Verdict: "human", Reason: "held"}, true
	}
	return ArmDecision{}, false
}

// agentEvidence is the agents author mode's evidence for the policy cl (R02, R03, SC-14, MR12):
// "" when the room reported this head, the verifiers approved it, its trailer names one of the
// task's runs and, for a policy with no diff shape and no oracle, a reviewer read it.
func agentEvidence(in ArmInputs, cfg *config.Config, cl config.Class) string {
	if e := runEvidence(in, cfg); e != "" {
		return e
	}
	return verdictEvidence(in, cfg, cl)
}

// verdictEvidence is MR12: a policy with a diff shape or an oracle has the diff checked
// mechanically; any other merges only on a reviewer's favourable verdict, so a workflow without a
// reviewer leaves it reviewer_verdict_missing. A workflow with one has had its approve read.
func verdictEvidence(in ArmInputs, cfg *config.Config, cl config.Class) string {
	if cl.DiffShape != "" || cl.Oracle != nil || slices.Contains(cfg.Templates[in.Task.Spec.Template].Roles, "reviewer") {
		return ""
	}
	return reasonVerdictMissing
}

// reasonVerdictMissing: no reviewer read the change, and its policy has no shape to check it (MR12).
const reasonVerdictMissing = "reviewer_verdict_missing"

// runEvidence is the evidence every agents policy reads alike: the reported head, a fresh approve
// and the trailer (R02, R03, SC-14).
func runEvidence(in ArmInputs, cfg *config.Config) string {
	if slices.Contains(cfg.Templates[in.Task.Spec.Template].Roles, "reviewer") && in.Task.Status.Verdict != "approve" {
		return "no_approving_verdict"
	}
	// R02: the rule can only speak about the commit the room named. An unreported head — a run
	// pushed without a handoff, or a push after the last report, the trailer inside the head
	// notwithstanding — is a commit nobody watched.
	if in.ReportedHead == "" || in.ReportedHead != in.PR.HeadSHA {
		return "head_unreported"
	}
	// R03: an approve is of the head its run was given, and of it only. If the verifier's last
	// approve predates the head the room just reported, the approval was pulled out from under.
	for i := len(in.Verifiers) - 1; i >= 0; i-- {
		r := in.Verifiers[i]
		if r.Verdict != "approve" || r.Role != "reviewer" {
			continue
		}
		if r.HeadSHA != in.PR.HeadSHA {
			return "verdict_stale"
		}
		break
	}
	trailer := in.PR.Trailer("Agent-Run")
	if !slices.ContainsFunc(in.Task.Status.Runs, func(r v1alpha1.RunRecord) bool { return r.ID == trailer }) {
		return "foreign_trailer" // SC-14, SP1 R9: another task's run pushed here
	}
	return ""
}

// adoptAuthor is the adopted author mode's identity rows (MC10): the pull request's author is one
// of the class's logins and its head branch matches one of its branches. Both are read from
// GitHub, never from the description.
func adoptAuthor(in ArmInputs, cl config.Class) string {
	if in.PR.CrossRepository {
		return "fork_pull_request" // the branch name and the author are a fork's to choose
	}
	a := cl.Author.Adopt
	if !slices.ContainsFunc(a.Logins, func(l string) bool { return forge.SameLogin(in.PR.Author, l) }) {
		return "not_adopted_author"
	}
	// A branch pattern is ^ and a literal prefix (Validate): quoted, so no character widens it.
	prefixes := make([]string, len(a.Branches))
	for i, b := range a.Branches {
		prefixes[i] = "^" + regexp.QuoteMeta(strings.TrimPrefix(b, "^"))
	}
	if !matchesAny(compile(prefixes, false), in.PR.HeadRef) {
		return "branch_not_allowed"
	}
	return ""
}

// adoptEvidence is the adopted mode's evidence in place of the agents': every commit of the pull
// request is one of the class's logins' and so is every push of its branch (GitHub's record, not
// a claim). A decision that has not read both never passes: awaitingCI reads them first.
func adoptEvidence(in ArmInputs, cl config.Class) string {
	if !in.Adoption.Read {
		return "adoption_unread"
	}
	logins := cl.Author.Adopt.Logins
	for _, a := range in.Adoption.CommitAuthors {
		if !slices.ContainsFunc(logins, func(l string) bool { return a != "" && forge.SameLogin(a, l) }) {
			return "human_commit_in_pr"
		}
	}
	switch in.Adoption.Pushed {
	case pushFactory:
	case pushUnrecorded:
		return "push_unrecorded" // awaitingCI waits for it a while before it hands off
	default:
		return "human_commit_in_pr"
	}
	if l := cl.RequiredLabel; l != "" {
		switch {
		case !slices.Contains(in.PR.Labels, l):
			return "label_missing"
		case !slices.ContainsFunc(logins, func(a string) bool { return in.Adoption.LabelledBy != "" && forge.SameLogin(in.Adoption.LabelledBy, a) }):
			return "label_not_adopted" // RK3: machine evidence; a maintainer who judges it merges it
		}
	}
	return ""
}

// touchesSteering is true when a changed file, or the path it was renamed from, is under the
// steering notes' directory.
func touchesSteering(stats []forge.FileStat) bool {
	return slices.ContainsFunc(stats, func(f forge.FileStat) bool {
		return strings.HasPrefix(f.Path, steering.Dir) || strings.HasPrefix(f.Previous, steering.Dir)
	})
}
