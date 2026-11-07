// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/envelope"
)

// The needs/v1 kinds, in the one order every surface lists them (spec CD21). The declared order is
// kindRank's: a deadline first, then this order, then the room, so two surfaces cannot disagree.
const (
	NeedPlanConfirmation = "plan_confirmation" // a scoper's proposed split, for a maintainer to accept
	NeedApproval         = "approval"          // a requested approval the caller may decide here
	NeedQuestion         = "question"          // the issue's author answers the scoper (SC8)
	NeedMerge            = "merge"             // a maintainer merges, or approves, the pull request
	NeedReview           = "review"            // the caller is one of the pull request's requested reviewers
	NeedFailedCheck      = "failed_check"      // a check the factory waited on never settled
	NeedRecovery         = "recovery"          // nothing runs on alone: a maintainer decides whether to retry
	NeedHold             = "hold"              // a person's hold on the room keeps the task Queued (N3)
)

// kindOrder is the constants' declared order: the second key of Ordered (CD21).
var kindOrder = [...]string{NeedPlanConfirmation, NeedApproval, NeedQuestion, NeedMerge, NeedReview,
	NeedFailedCheck, NeedRecovery, NeedHold}

// kindRank is a kind's place in kindOrder; a kind this package does not know sorts last.
func kindRank(kind string) int {
	if i := slices.Index(kindOrder[:], kind); i >= 0 {
		return i
	}
	return len(kindOrder)
}

// NeedsIn is one room, as the caller sees it (CD21). Sealed, and facts that cannot be read,
// contribute nothing.
type NeedsIn struct {
	Room       string
	URL        string // the room page
	Repository string // owner/name, for the sentences and the pull request's URL
	Sealed     bool
	FactsRead  bool // false: the room's state could not be read
	Facts      *envelope.TaskFacts
	FactsAt    time.Time      // the ts of the event that carried Facts
	Approvals  []Approval     // the live approvals the caller may decide, as the fold lists them
	GitHub     GitHubStanding // what only the server can resolve
	Caller     Caller         // what the policy says, and how the caller reads the room
	Now        time.Time
	// Coordination is the room's family as this caller may read it, when the room is a coordination
	// parent (coordinationview.NeedsOf); CoordinationStanding is the caller's standing in each
	// visible child's repository.
	Coordination         *CoordinationNeeds
	CoordinationStanding map[string]GitHubStanding
}

// GitHubStanding is the caller's standing where a merge, a review and an answer happen (CD26): their
// login, whether they may write the room's repository, and the pull request's requested reviewers. The
// server resolves it once per room and hands it to every derivation, so the rows, the summary and the
// tools agree.
type GitHubStanding struct {
	Login     string
	MayWrite  bool
	Reviewers []string
}

// Caller is who asks, from where the action happens: the policy decides an approval, closing a room and
// releasing a hold; GitHub decides the rest (CD26). RoomScoped is a sponsored principal: the App reads a
// room's needs, never a login's, and every item's action is a link it cannot follow for anyone.
type Caller struct {
	WebUI      bool // decides approvals in the browser
	CanDecide  bool // the policy's Decide: an approval item
	CanClose   bool // the policy's Close: a question, a recovery
	CanRelease bool // the policy's StartRun, as summary.Hold asks it: a hold
	RoomScoped bool
}

// NeedAction is a need's one thing to do, and where.
type NeedAction struct {
	Kind  string `json:"kind"`  // decide | comment | answer | merge | review | retry | release
	Label string `json:"label"` // one sentence
	URL   string `json:"url,omitempty"`
}

// NeedSource says where an item was derived from. It is for the tests, not for the reader, so it is
// never in the object the surfaces render.
type NeedSource struct {
	Kind   string `json:"kind"` // approval | facts | review_request | room
	Seq    int64  `json:"seq,omitempty"`
	Reason string `json:"reason,omitempty"` // the factory's wait reason
}

// Items is needs/v1 for one room as the caller sees it (CD21, CD26): the one derivation the room rows,
// the room summary, /api/needs, the CLI, the tools and the web panel all read. A sealed room, and a room
// whose state cannot be read, contribute nothing: no item's link may point at work that moved (Review
// Focus 2). The items are not ordered here: Ordered is the only order (CD21).
func Items(in NeedsIn) []Need {
	if in.Sealed || !in.FactsRead {
		return nil
	}
	var out []Need
	if n, ok := factsNeed(in); ok {
		out = append(out, n)
	}
	if in.Caller.CanDecide {
		out = append(out, approvalNeeds(in)...)
	}
	return append(out, coordinationNeeds(in)...)
}

// factsNeed is the one need a room's task facts imply: the phase and the reason say which, and the
// caller's standing says whose it is. One room is one task, so its facts are at most one need; the
// room's live approvals are the other source.
func factsNeed(in NeedsIn) (Need, bool) {
	f := in.Facts
	switch {
	case f == nil:
		return Need{}, false
	// Split: a proposed split reached nobody: Merge needs a pull request and Answer needs needs_author,
	// so the row had no need and no step (G4). A plan confirmation has no pull-request precondition,
	// which is exactly what it needs (needs/v1).
	case f.Phase == "Escalated" && f.Reason == "split_proposed":
		return planConfirmationNeed(in, f), true
	// Review before merge: a requested reviewer is asked for the review the merge then follows, so one
	// pull request is one item for them (CD26). It is GitHub's to decide, as a merge is, and it lives as
	// long as the merge does: the facts keep their PR after it merged, so a phase other than
	// AwaitingHuman's would hand a requested reviewer a review of work that already landed (Review
	// Focus 2).
	case f.Phase == "AwaitingHuman" && f.PR != nil && in.GitHub.Login != "" && slices.ContainsFunc(in.GitHub.Reviewers, func(l string) bool {
		return strings.EqualFold(l, in.GitHub.Login)
	}):
		return reviewNeed(in, f), true
	// CD26: a merge is GitHub's, not the room's: whoever may write the repository sees it, whether or
	// not they own the room the task's log lives in. The prose is waiting.go's MergeNeed, so Approve and
	// "approve the current commit" come from one table (AM3).
	case f.Phase == "AwaitingHuman" && f.PR != nil && (in.GitHub.MayWrite || in.Caller.RoomScoped):
		return mergeNeed(in, f)
	// failed_check: a check the factory waited on is the one thing a person re-runs or the factory
	// retries, and re-running it is GitHub's too.
	case failedChecks()[f.Reason] && (in.GitHub.MayWrite || in.Caller.RoomScoped):
		return failedCheckNeed(in, f), true
	// recovery: the run spent what it had, or the sandbox was lost as often as the factory resumes a
	// task, so nothing runs on alone: a maintainer decides whether to retry (policy's Close).
	case recoveries()[f.Reason] && (in.Caller.CanClose || in.Caller.RoomScoped):
		return recoveryNeed(in, f), true
	// hold: summary.Hold's two Queued reasons, for a caller the policy's StartRun lets release them.
	case f.Phase == "Queued" && holds()[f.Reason] != "" && in.Caller.CanRelease:
		return holdNeed(in, f), true
	// question: the scoper bounced the task to its issue's author (SC8), and the room's owner is who
	// can close the room over it.
	case f.Phase == "Escalated" && f.Reason == "needs_author" && (in.Caller.CanClose || in.Caller.RoomScoped):
		n, ok := QuestionNeed(f)
		if !ok {
			return Need{}, false
		}
		n.Why, n.Issue = whyOf(f.Reason), issueNumber(f)
		n.Action = NeedAction{Kind: "answer", Label: fmt.Sprintf("answer on issue #%s", n.ID), URL: n.URL}
		return stamp(n, in, NeedSource{Kind: "facts", Reason: f.Reason}), true
	}
	return Need{}, false
}

// planConfirmationNeed is a scoper's proposed split (G4): a maintainer comments to accept the parts, or
// to run the task whole. Its sentence is nextStep's, so the need and the NEXT box say the same thing.
func planConfirmationNeed(in NeedsIn, f *envelope.TaskFacts) Need {
	n := Need{Kind: NeedPlanConfirmation, ID: number(issueNumber(f)), Issue: issueNumber(f),
		Why: whyOf(f.Reason), URL: issueURL(in, f),
		What: clipWhat("a maintainer reads the proposed parts on the issue, then comments `/factory split` to accept them, or `/factory retry` to run the task whole")}
	if n.Issue > 0 {
		n.What = clipWhat(fmt.Sprintf("a maintainer comments `/factory split` on issue #%d to accept the parts, or `/factory retry` to run the task whole", n.Issue))
	}
	n.Action = NeedAction{Kind: "comment", Label: "comment on the issue", URL: n.URL}
	return stamp(n, in, NeedSource{Kind: "facts", Reason: f.Reason})
}

// reviewNeed is the review the caller owes: they are in the pull request's requested reviewers, and a
// review is GitHub's to decide (CD26).
func reviewNeed(in NeedsIn, f *envelope.TaskFacts) Need {
	n := Need{Kind: NeedReview, ID: number(prNumber(f)), PR: prNumber(f), Why: whyOf(f.Reason),
		URL:  firstURL(urlOf(f.PR), pullURL(in, f)),
		What: clipWhat(fmt.Sprintf("a maintainer reviews PR #%d: the head is yours to read and the merge follows your review", prNumber(f)))}
	n.Action = NeedAction{Kind: "review", Label: fmt.Sprintf("review PR #%d", prNumber(f)), URL: n.URL}
	return stamp(n, in, NeedSource{Kind: "review_request"})
}

// mergeNeed is a pull request a maintainer merges — or approves, when the facts say an approval returns
// the hand-off to the gate (AM3). The standing is this derivation's (CD26), the sentence is waiting.go's.
func mergeNeed(in NeedsIn, f *envelope.TaskFacts) (Need, bool) {
	n, ok := MergeNeed(f)
	if !ok {
		return Need{}, false
	}
	// A room row projects the pull request's number, not its URL: the sentence and the link are then
	// built from the room's repository, never left as "the repository".
	if repoOf(f) == "" && in.Repository != "" {
		n.What = MergeHow(in.Repository, f.PR.Number, f.MergeByApproval)
	}
	n.PR = f.PR.Number
	n.URL = firstURL(urlOf(f.PR), pullURL(in, f))
	n.Why = whyOf(f.Reason)
	n.Action = NeedAction{Kind: "merge", Label: "open the pull request", URL: n.URL}
	if n.Approve { // an approval of the head returns the pull request to the gate: say approve, never merge by hand
		n.Action.Label = "approve the head on the pull request"
	}
	return stamp(n, in, NeedSource{Kind: "facts", Reason: f.Reason}), true
}

// failedCheckNeed is a check the factory waited on and no longer will (ci_wait_expired, the tester's
// contract ones, release_age_red, verdict_stale): a person re-runs it, or the factory retries the task.
func failedCheckNeed(in NeedsIn, f *envelope.TaskFacts) Need {
	n := Need{Kind: NeedFailedCheck, ID: f.Reason, Issue: issueNumber(f), PR: prNumber(f),
		Why: whyOf(f.Reason), URL: firstURL(urlOf(f.PR), firstURL(issueURL(in, f), in.URL))}
	n.What = clipWhat("a maintainer checks the failure and re-runs it, or retries the task")
	if s := nextStep(f.Reason, f.MergeByApproval); s != "" {
		n.What = clipWhat(s)
	}
	n.Action = NeedAction{Kind: "retry", Label: "re-run the check or retry the task", URL: n.URL}
	return stamp(n, in, NeedSource{Kind: "facts", Reason: f.Reason})
}

// recoveryNeed is a task nothing runs on any more (its budget is spent, or the sandbox was lost as
// often as the factory resumes one): a maintainer decides whether to retry it or to close it.
func recoveryNeed(in NeedsIn, f *envelope.TaskFacts) Need {
	n := Need{Kind: NeedRecovery, ID: f.Reason, Issue: issueNumber(f), PR: prNumber(f),
		Why: whyOf(f.Reason), URL: firstURL(issueURL(in, f), in.URL)}
	n.What = clipWhat("a maintainer decides whether the task is retried or closed: a retry is `/factory retry` on the issue")
	if n.Issue > 0 {
		n.What = clipWhat(fmt.Sprintf("a maintainer comments `/factory retry` on issue #%d to run it again, or closes the room", n.Issue))
	}
	n.Action = NeedAction{Kind: "retry", Label: "retry the task or close the room", URL: in.URL}
	return stamp(n, in, NeedSource{Kind: "facts", Reason: f.Reason})
}

// holdNeed is N3's hold: a factory task waits while a person drives or works in the room, and whoever
// may release the room is the one who ends it.
func holdNeed(in NeedsIn, f *envelope.TaskFacts) Need {
	n := Need{Kind: NeedHold, ID: f.Reason, What: holds()[f.Reason], URL: in.URL,
		Action: NeedAction{Kind: "release", Label: "hand the driver back to the factory", URL: in.URL}}
	if f.Reason == "waiting_room_busy" {
		n.Action.Label = "wait for the other run in the room to end"
	}
	return stamp(n, in, NeedSource{Kind: "room", Reason: f.Reason})
}

// approvalNeeds is one item per live, unexpired, still-undecided approval, sorted by id: the caller is
// asked to decide it here, and never given a command (D4, ruling P18).
func approvalNeeds(in NeedsIn) []Need {
	appr := slices.Clone(in.Approvals)
	slices.SortStableFunc(appr, func(a, b Approval) int { return strings.Compare(a.ID, b.ID) })
	var out []Need
	for _, a := range appr {
		if !a.ExpiresAt.IsZero() && in.Now.After(a.ExpiresAt) {
			continue // the sweep's approval_decided{expired} has not landed yet
		}
		n := Need{Kind: NeedApproval, ID: a.ID, What: a.What, Deadline: a.ExpiresAt, At: a.At,
			URL: in.URL + "#" + a.ID, Action: NeedAction{Kind: "decide", Label: "decide in the room", URL: in.URL + "#" + a.ID}}
		if !in.Caller.WebUI {
			n.Action.Label = "open the room page to decide"
		}
		out = append(out, stamp(n, in, NeedSource{Kind: "approval"}))
	}
	return out
}

// live is the approvals the fold still holds, unexpired at now, oldest id first: what a caller of
// Items passes as Approvals, so the expiry screen is the derivation's too.
func live(pending map[string]Approval, now time.Time) []Approval {
	out := make([]Approval, 0, len(pending))
	for _, id := range slices.Sorted(maps.Keys(pending)) {
		if a := pending[id]; a.ExpiresAt.IsZero() || !now.After(a.ExpiresAt) {
			out = append(out, a)
		}
	}
	return out
}

// Ordered sorts items as CD21 states the one order: a deadline ascending (none last), then the kind,
// then the room. No scoring, no routing, no expiry: two surfaces cannot disagree.
func Ordered(items []Need) []Need {
	out := slices.Clone(items)
	slices.SortStableFunc(out, func(a, b Need) int {
		az, bz := a.Deadline.IsZero(), b.Deadline.IsZero()
		switch {
		case az != bz: // a deadline sorts before none
			if az {
				return 1
			}
			return -1
		case !az:
			if c := a.Deadline.Compare(b.Deadline); c != 0 {
				return c
			}
		}
		if c := kindRank(a.Kind) - kindRank(b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.Room, b.Room)
	})
	return out
}

// Key is an item's identity across rooms: "<room>/<kind>/<id>", so two surfaces name the same item.
func (n Need) Key() string { return n.Room + "/" + n.Kind + "/" + n.ID }

// failedChecks are the reasons a check the factory waited on never settled: a person re-runs it.
func failedChecks() map[string]bool {
	return map[string]bool{
		"ci_wait_expired":      true,
		"release_age_red":      true,
		"verdict_stale":        true,
		"contract_invalid":     true,
		"contract_disputed":    true,
		"contract_too_large":   true,
		"contract_check_unset": true,
		"contract_modified":    true,
	}
}

// recoveries are the reasons nothing runs on alone: the budget or the resumes are gone, or a merge was
// reverted, so only a person decides whether the task runs again.
func recoveries() map[string]bool {
	return map[string]bool{
		"budget_reached":    true,
		"budget-task":       true,
		"resume_budget":     true,
		"resumes_exhausted": true,
		"reverted":          true,
	}
}

// stamp is what every item carries whatever its clause: the room it belongs to, when its cause landed,
// and where it came from. A clause that knows its own time (an approval's) keeps it.
func stamp(n Need, in NeedsIn, src NeedSource) Need {
	n.Room, n.Source = in.Room, src
	if n.At.IsZero() {
		n.At = in.FactsAt
	}
	if n.URL == "" {
		n.URL = n.Action.URL
	}
	return n
}

// whyOf is a reason's prose, as the status line says it: the factory's own words, never an agent's, and
// the raw code for a reason the summary has no prose for.
func whyOf(reason string) string {
	if s := reasons()[reason]; s != "" {
		return s
	}
	if r := []rune(reason); len(r) > maxReason {
		return string(r[:maxReason]) + "…"
	}
	return reason
}

// clipWhat bounds a need's one line, as DescribeApproval bounds an approval's.
func clipWhat(s string) string {
	if r := []rune(s); len(r) > maxWhat {
		return string(r[:maxWhat]) + "…"
	}
	return s
}

// issueNumber is the issue the facts name, 0 when they name none.
func issueNumber(f *envelope.TaskFacts) int {
	if f.Issue == nil {
		return 0
	}
	return f.Issue.Number
}

// prNumber is the pull request the facts name, 0 when they name none.
func prNumber(f *envelope.TaskFacts) int {
	if f.PR == nil {
		return 0
	}
	return f.PR.Number
}

// number is a facts number as an item id, "" when the facts name none.
func number(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprint(n)
}

// issueURL is the facts' validated issue URL, built from the room's repository when the facts name the
// number without it. The room page is the last resort: a need never goes without a link.
func issueURL(in NeedsIn, f *envelope.TaskFacts) string {
	if u := urlOfIssue(f.Issue); u != "" {
		return u
	}
	if in.Repository != "" && issueNumber(f) > 0 {
		return fmt.Sprintf("https://github.com/%s/issues/%d", in.Repository, issueNumber(f))
	}
	return in.URL
}

// pullURL is the pull request's URL, as issueURL builds an issue's one.
func pullURL(in NeedsIn, f *envelope.TaskFacts) string {
	if u := urlOf(f.PR); u != "" {
		return u
	}
	if in.Repository != "" && prNumber(f) > 0 {
		return fmt.Sprintf("https://github.com/%s/pull/%d", in.Repository, prNumber(f))
	}
	return in.URL
}

// firstURL is the first non-empty of its arguments.
func firstURL(urls ...string) string {
	for _, u := range urls {
		if u != "" {
			return u
		}
	}
	return ""
}
