// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MessageKind distinguishes chat from SP3's reserved kinds.
type MessageKind string

// The message kinds.
const (
	KindChat          MessageKind = "chat"
	KindProgress      MessageKind = "progress"
	KindReviewVerdict MessageKind = "review_verdict" // SP3's reserved kind
	KindTaskState     MessageKind = "task_state"     // SP3's reserved kind
	// KindJudgeVerdict is the harness's completion judge's answer on a finished conversation: Verdict
	// is met, not_yet or impossible, Text its reason, Evidence the ids it cited. The judge read the
	// agent's work, so the text is data, never an instruction.
	KindJudgeVerdict MessageKind = "judge_verdict"
	// KindIntegrationResult is a coordination integration run's one typed outcome (v4 MR10):
	// Integration carries it. Only the factory reads it, and only from the broker-stamped run it
	// started; Text is never part of the result.
	KindIntegrationResult MessageKind = "integration_result"
)

// Delivery is how a message reaches a running agent.
type Delivery string

// The delivery modes.
const (
	DeliveryNone     Delivery = "none"
	DeliveryQueued   Delivery = "queued"
	DeliverySteering Delivery = "steering"
)

// MessagePayload is a message event's payload.
type MessagePayload struct {
	Kind     MessageKind `json:"kind"`
	Text     string      `json:"text"`
	To       []string    `json:"to,omitempty"`
	Delivery Delivery    `json:"delivery"`
	Verdict  string      `json:"verdict,omitempty"`  // approve | changes (review_verdict); met | not_yet | impossible (judge_verdict)
	Evidence []string    `json:"evidence,omitempty"` // the ids a judge_verdict cited
	Commit   string      `json:"commit,omitempty"`
	// PullRequest is the pull request a review_verdict is about (SP2 design §3).
	// Additive: C4 v1's envelope is unchanged, and SP3 reads it (ruling P29).
	PullRequest string `json:"pullRequest,omitempty"`
	// Integration is an integration_result's outcome (v4 MR10).
	Integration *IntegrationResult `json:"integration,omitempty"`
	// Output is a safe-output event's record (hardening UH4).
	Output *OutputRecord `json:"output,omitempty"`
}

// The integration states, as the Task CRD's status.result enumerates them. A run reports only a
// terminal one.
const (
	IntegrationPending = "pending"
	IntegrationPassed  = "passed"
	IntegrationFailed  = "failed"
)

// IntegrationStates are the values IntegrationResult.State may hold on the Task.
func IntegrationStates() []string {
	return []string{IntegrationPending, IntegrationPassed, IntegrationFailed}
}

// IntegrationResult mirrors the Task CRD's status.result field for field: this package imports no
// Kubernetes types, as it mirrors TaskScope, and the reconciler asserts the two agree.
type IntegrationResult struct {
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	ManifestHash string `json:"manifestHash,omitempty"`
}

var (
	integrationReason = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	manifestHash      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Validate refuses a result that is not passed or failed with a bounded snake_case reason and the
// exact 64-hex manifest hash: no prose ever becomes a result.
func (r IntegrationResult) Validate() error {
	switch {
	case r.State != IntegrationPassed && r.State != IntegrationFailed:
		return fmt.Errorf("integration state %q is not passed or failed", r.State)
	case r.Reason != "" && !integrationReason.MatchString(r.Reason):
		return errors.New("integration reason is at most 64 bytes of snake_case")
	case !manifestHash.MatchString(r.ManifestHash):
		return errors.New("integration manifest hash is not 64-hex")
	}
	return nil
}

// TurnPayload is a turn event's payload.
type TurnPayload struct {
	RunID  string `json:"runId"`
	TurnID string `json:"turnId"`
	Phase  string `json:"phase"` // started | completed | cancelled | failed
}

// ToolCallPayload is a tool_call event's payload.
type ToolCallPayload struct {
	CallID    string          `json:"callId"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
	Class     string          `json:"class,omitempty"`
	Risk      string          `json:"risk,omitempty"`
	DecidedBy *string         `json:"decidedBy"` // policy | human | null
}

// ToolResultPayload is a tool_result event's payload.
type ToolResultPayload struct {
	CallID    string `json:"callId"`
	Status    string `json:"status"` // ok | error | rejected
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	Bytes     int    `json:"bytes"`
}

// ApprovalRequestedPayload is an approval_requested event's payload.
type ApprovalRequestedPayload struct {
	ApprovalID string `json:"approvalId"`
	CallID     string `json:"callId"`
	Class      string `json:"class"`
	// Branch is the run's branch, set by the bridge for the classes it forwards (D5): the one
	// thing a forge.push approval binds. Empty for every other class and for older requests.
	Branch string `json:"branch,omitempty"`
	// Digest and Output bind a factory-requested forge.push approval to one push bundle's sha256
	// and the output id that carries it (hardening HR28, amending D5): the bundle exists before the
	// push, so its digest is what a maintainer approves. Empty on every other approval.
	Digest    string          `json:"digest,omitempty"`
	Output    string          `json:"output,omitempty"`
	Action    json.RawMessage `json:"action"` // the raw, redacted call (T3)
	ExpiresAt time.Time       `json:"expiresAt"`
	// Prompters are the principals who prompted the turn: with four-eyes on, none of
	// them may decide it. The page reads them to say who may.
	Prompters []string `json:"prompters,omitempty"`
}

// ApprovalDecidedPayload is an approval_decided event's payload.
type ApprovalDecidedPayload struct {
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"` // approved | denied | expired | superseded
	Reason     string `json:"reason,omitempty"`
}

// ParticipantPayload is a participant event's payload.
type ParticipantPayload struct {
	Principal string `json:"principal"`
	Change    string `json:"change"` // joined | left | role_changed
	Role      string `json:"role,omitempty"`
	Approver  bool   `json:"approver,omitempty"`
}

// DriverPayload is a driver event's payload: the driver token moved.
type DriverPayload struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Epoch  int64  `json:"epoch"`
	Reason string `json:"reason"` // given | requested | taken | lease_expired
}

// OverlapAssessment is an outcome-overlap scoper's read-only classification. Citations name only
// candidate IDs supplied in its brief; the reconciler additionally checks them against those candidates.
type OverlapAssessment struct {
	Classification string   `json:"classification"`
	Citations      []string `json:"citations,omitempty"`
	Sources        []string `json:"sources,omitempty"`
	BlindSpots     []string `json:"blindSpots,omitempty"`
	Note           string   `json:"note,omitempty"`
}

// OverlapAssessmentSchema is the triager-only overlap property of room_handoff's input schema.
const OverlapAssessmentSchema = `{"type":"object","additionalProperties":false,"required":["classification"],"properties":{` +
	`"classification":{"type":"string","enum":["active_duplicate","completed_duplicate","prior_attempt","partial_overlap","unverified_overlap","no_match"]},` +
	`"citations":{"type":"array","maxItems":20,"items":{"type":"string","minLength":1,"maxLength":128}},` +
	`"sources":{"type":"array","maxItems":4,"items":{"type":"string","enum":["tasks","runs","rooms","github"]}},` +
	`"blindSpots":{"type":"array","maxItems":8,"items":{"type":"string","minLength":1,"maxLength":256}},` +
	`"note":{"type":"string","maxLength":512}}}`

// ErrOverlapAssessment is every refusal of an assessment's bounded shape.
var ErrOverlapAssessment = errors.New("overlap: one known classification; at most 20 citations, four checked sources and eight blind spots")

// Validate checks the assessment's independent shape and bounds. Candidate-specific claims are the
// reconciler's contract: only it knows which IDs and active candidates were actually supplied.
func (a OverlapAssessment) Validate() error {
	classifications := []string{"active_duplicate", "completed_duplicate", "prior_attempt", "partial_overlap", "unverified_overlap", "no_match"}
	sources := []string{"tasks", "runs", "rooms", "github"}
	text := func(s string, limit int, required bool) bool {
		return (!required || strings.TrimSpace(s) != "") && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, Control)
	}
	list := func(values []string, limit, each int) bool {
		if len(values) > limit {
			return false
		}
		for _, value := range values {
			if !text(value, each, true) {
				return false
			}
		}
		return true
	}
	good := slices.Contains(classifications, a.Classification) && list(a.Citations, 20, 128) && list(a.BlindSpots, 8, 256) &&
		text(a.Note, 512, false) && len(a.Sources) <= 4
	for _, source := range a.Sources {
		good = good && slices.Contains(sources, source)
	}
	if !good {
		return ErrOverlapAssessment
	}
	return nil
}

// HandoffPayload is a handoff event's payload: work passed between roles.
type HandoffPayload struct {
	FromRole string `json:"fromRole"`
	ToRole   string `json:"toRole"`
	Summary  string `json:"summary"`
	Commit   string `json:"commit"`
	Branch   string `json:"branch"`
	// Scope is the scoper's bounded task (scoper design SC6): a triager's handoff only.
	Scope *Scope `json:"scope,omitempty"`
	// Split is the scoper's second answer (splitter design SL2): a triager's handoff only, always
	// beside Scope, the whole task.
	Split *Split `json:"split,omitempty"`
	// Overlap is an outcome-overlap scoper's classification: a triager's handoff only.
	Overlap *OverlapAssessment `json:"overlap,omitempty"`
}

// MaxProgressNote bounds a progress note, in characters.
const MaxProgressNote = 280

// TaskFacts are what the factory knows about a room's task, written as state_changed{kind:task}
// so the room summary depends on the room log alone.
type TaskFacts struct {
	Phase  string      `json:"phase"`
	Reason string      `json:"reason,omitempty"`
	Run    *RunFact    `json:"run,omitempty"`
	Budget *BudgetFact `json:"budget,omitempty"`
	// Spend is the task's dollars, beside Budget's tokens (BudgetFact is unchanged).
	Spend *SpendFact `json:"spend,omitempty"`
	// SpendFinal is CD23: the Spend beside it is the task's last figure — priced again after the
	// settle window, and never bucketed by the facts key. False while the dollars may still move,
	// and for a task whose runs the meter cannot price: no figure is claimed for those.
	SpendFinal bool `json:"spendFinal,omitempty"`
	// Runs is the task's run history, oldest first, at most MaxRuns.
	Runs  []RunFact  `json:"runs,omitempty"`
	Issue *IssueFact `json:"issue,omitempty"`
	PR    *PRFact    `json:"pr,omitempty"`
	// Origin is the room of the investigate task this task's issue was published from (RunLore
	// agent-to-agent RA10), a C2 id; the webhook tracker repeats it on every event of the room.
	Origin string `json:"origin,omitempty"`
	// MergeByApproval is the gate's hand-off of an AwaitingHuman pull request (human path AM3): a
	// maintainer's approval of the head returns it to the gate, which merges it. False is the
	// bypass: the switch off, an adopted class, or a reason no approval returns.
	MergeByApproval bool `json:"mergeByApproval,omitempty"`
	// Plan is the personas the task expects to run, in order, at most MaxPlan: the factory's
	// forecast, so a view can show the steps ahead.
	Plan []string `json:"plan,omitempty"`
	// Split is the task as a parent of parts (a maintainer accepted the scoper's split).
	Split *SplitFact `json:"split,omitempty"`
	// Parent is set on a part's room: the task it was split from.
	Parent *ParentFact `json:"parent,omitempty"`
	// Monitor is the post-merge monitor's verdict (one of MonitorVerdicts), set only for a task with
	// an origin. A verdict is final once set; the webhook tracker sends it once as room.monitor_verdict.
	Monitor string `json:"monitor,omitempty"`
	// Policy is the merge policy that governs the task, as the factory's config declares it.
	Policy *PolicyFact `json:"policy,omitempty"`
	// Profile is the harness profile and evaluation cohort the task runs under.
	Profile *ProfileFact `json:"profile,omitempty"`
}

// MaxPlan bounds TaskFacts.Plan.
const MaxPlan = 8

// SplitFact is a split parent's view of its parts: Of parts, between MinChildren and MaxChildren.
type SplitFact struct {
	Of    int        `json:"of"`
	Parts []PartFact `json:"parts,omitempty"`
}

// PartFact is one part of a split: its place in the merge order (1-based), its issue and room, and
// how it ended, empty while it has not. Outcome is landed, dropped or reverted.
type PartFact struct {
	Index   int    `json:"index"`
	Issue   int    `json:"issue"`
	Room    string `json:"room"`
	Outcome string `json:"outcome,omitempty"`
}

// ParentFact points a part's room back at the task it was split from, and says which part it is.
type ParentFact struct {
	Room  string `json:"room"`
	Issue int    `json:"issue"`
	Index int    `json:"index"`
	Of    int    `json:"of"`
}

func validPartOutcome(o string) bool {
	switch o {
	case "", "landed", "dropped", "reverted":
		return true
	}
	return false
}

func (s SplitFact) validate() error {
	if s.Of < MinChildren || s.Of > MaxChildren {
		return fmt.Errorf("split is of %d to %d parts, got %d", MinChildren, MaxChildren, s.Of)
	}
	if len(s.Parts) > s.Of {
		return fmt.Errorf("part count %d is more than the split's %d", len(s.Parts), s.Of)
	}
	for i, p := range s.Parts {
		switch {
		case p.Index < 1 || p.Index > s.Of:
			return fmt.Errorf("part[%d] index is between 1 and %d", i, s.Of)
		case p.Issue < 1:
			return fmt.Errorf("part[%d] issue is at least 1", i)
		case !ValidID(p.Room):
			return fmt.Errorf("part[%d] room is a room id", i)
		case !validPartOutcome(p.Outcome):
			return fmt.Errorf("part[%d] outcome %q is not landed, dropped or reverted", i, p.Outcome)
		}
	}
	return nil
}

func (p ParentFact) validate() error {
	switch {
	case !ValidID(p.Room):
		return errors.New("parent room is a room id")
	case p.Issue < 1:
		return errors.New("parent issue is at least 1")
	case p.Of < MinChildren || p.Of > MaxChildren:
		return fmt.Errorf("parent is of %d to %d parts, got %d", MinChildren, MaxChildren, p.Of)
	case p.Index < 1 || p.Index > p.Of:
		return fmt.Errorf("parent index is between 1 and %d", p.Of)
	}
	return nil
}

// MonitorVerdicts are the values TaskFacts.Monitor may hold.
func MonitorVerdicts() []string { return []string{"healthy", "regression", "inconclusive"} }

// MaxRuns bounds TaskFacts.Runs.
const MaxRuns = 20

// MaxMicroUSD bounds every dollar amount in the facts ($1,000,000), so sums of them cannot overflow.
const MaxMicroUSD int64 = 1_000_000_000_000

// MaxVerdict bounds RunFact.Verdict, in bytes, as the Task CRD bounds RunRecord.Verdict.
const MaxVerdict = 32

// A run's outcome, once it has ended. An empty Outcome is a run that has not.
const (
	OutcomeSucceeded       = "succeeded"
	OutcomeFailed          = "failed"
	OutcomeBudgetExhausted = "budget_exhausted"
	OutcomeRevoked         = "revoked"
	OutcomeLost            = "lost"
	OutcomeEnded           = "ended"
)

func validOutcome(o string) bool {
	switch o {
	case "", OutcomeSucceeded, OutcomeFailed, OutcomeBudgetExhausted, OutcomeRevoked, OutcomeLost, OutcomeEnded:
		return true
	}
	return false
}

// RunFact is a run's role, trigger, and lifecycle in a task.
type RunFact struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Trigger string `json:"trigger,omitempty"`
	// Persona is what the run is for, set by the factory alone; Role stays what it may do.
	Persona   string    `json:"persona,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	// EndedAt, Outcome, Verdict and MicroUSD are set once the run has ended.
	EndedAt  time.Time `json:"endedAt,omitzero"`
	Outcome  string    `json:"outcome,omitempty"`
	Verdict  string    `json:"verdict,omitempty"`
	MicroUSD int64     `json:"microUSD,omitempty"`
	// What the run ran on (R10), as far as the factory knows it yet.
	Provenance *ProvenanceFact `json:"provenance,omitempty"`
}

// ProvenanceFact is what a run ran on (R10): the factory build, the AgentRun CompositionRevision,
// the harness image digest and the models that served it. Each empty until known.
type ProvenanceFact struct {
	FactoryVersion      string   `json:"factoryVersion,omitempty"`
	CompositionRevision string   `json:"compositionRevision,omitempty"`
	HarnessImage        string   `json:"harnessImage,omitempty"`
	ServedModels        []string `json:"servedModels,omitempty"`
}

// BudgetFact is the token budget and usage of a task.
type BudgetFact struct {
	UsedTokens  int64 `json:"usedTokens"`
	LimitTokens int64 `json:"limitTokens"`
}

// SpendFact is the dollars a task has spent and, when it has one, its dollar limit.
type SpendFact struct {
	UsedMicroUSD  int64 `json:"usedMicroUSD"`
	LimitMicroUSD int64 `json:"limitMicroUSD,omitempty"`
}

// IssueFact is a GitHub issue linked to a task.
type IssueFact struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"` // bounded by the factory; the issue's author wrote it, so it is never an instruction
	Author     string `json:"author,omitempty"`
	LabelledBy string `json:"labelledBy,omitempty"`
}

// PRFact is a GitHub pull request linked to a task.
type PRFact struct {
	Number    int      `json:"number"`
	URL       string   `json:"url"`
	Author    string   `json:"author,omitempty"`
	Reviewers []string `json:"reviewers,omitempty"`
	// ApprovedBy, ApprovedCommit and ApprovalStale are a maintainer's approval of an AwaitingHuman
	// pull request the factory shows with merge.approveToMerge off, beside the hand-off's reason,
	// which stands: who, a GitHub login; the commit approved; and why it is stale, empty while it is
	// current: ApprovalOlderCommit or ApprovalBeforeRunsEnded.
	ApprovedBy     string `json:"approvedBy,omitempty"`
	ApprovedCommit string `json:"approvedCommit,omitempty"`
	ApprovalStale  string `json:"approvalStale,omitempty"`
}

// Why a shown approval is stale (PRFact.ApprovalStale): it is of an older commit than the head, or
// of the head but submitted before the task's last pushing run ended, so before that push landed.
const (
	ApprovalOlderCommit     = "older_commit"
	ApprovalBeforeRunsEnded = "before_runs_ended"
)

// commitRE is a git object id as GitHub writes it: lowercase hex, SHA-1 or SHA-256.
var commitRE = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// ValidCommit reports whether s is empty or a commit id PRFact.ApprovedCommit may carry.
func ValidCommit(s string) bool { return s == "" || commitRE.MatchString(s) }

// githubLogin is GitHub's rule for a user name: 1 to 39 letters and digits, single hyphens between
// them, none at either end. A GitHub App authors as its slug plus the literal suffix "[bot]".
var githubLogin = regexp.MustCompile(`^([A-Za-z0-9]+(-[A-Za-z0-9]+)*)(\[bot\])?$`)

// ValidLogin reports whether s is a GitHub login in that rule's shape: a user, or an App's
// "<slug>[bot]". Facts are not refused for an author outside it (legacy logins with double or
// trailing hyphens, Enterprise Managed Users' "name_shortcode" exist): a view that names the author
// in a sentence of its own checks it here, and says "the issue's author" otherwise.
func ValidLogin(s string) bool {
	m := githubLogin.FindStringSubmatch(s)
	return m != nil && len(m[1]) <= 39
}

var githubURL = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9._-]+/(issues|pull)/[0-9]+$`)

// GitHubURL reports whether s is the URL of a github.com issue or pull request.
func GitHubURL(s string) bool { return githubURL.MatchString(s) }

// validate is deliberately loose where the factory can produce it: an EndedAt without StartedAt,
// an Outcome without EndedAt, and (in SpendFact) Used above Limit, a cap overrun.
func (r RunFact) validate() error {
	switch {
	case r.MicroUSD < 0 || r.MicroUSD > MaxMicroUSD:
		return fmt.Errorf("microUSD is between 0 and %d", MaxMicroUSD)
	case !validOutcome(r.Outcome):
		return fmt.Errorf("outcome %q is not one of the run outcomes", r.Outcome)
	case len(r.Verdict) > MaxVerdict || !utf8.ValidString(r.Verdict) ||
		strings.ContainsFunc(r.Verdict, func(c rune) bool { return Control(c) || c == '\n' || c == '\t' }):
		return fmt.Errorf("verdict is at most %d bytes of plain text, with no control, bidi or zero-width characters", MaxVerdict)
	case !r.EndedAt.IsZero() && r.EndedAt.Before(r.StartedAt):
		return errors.New("endedAt is before startedAt")
	case r.Persona != "" && !ValidPersona(r.Persona):
		return errors.New("persona is a lowercase name of at most 32 characters")
	}
	return nil
}

// Validate refuses facts a broker should not store.
func (f TaskFacts) Validate() error {
	switch {
	case f.Phase == "":
		return errors.New("task facts: a phase is required")
	case f.Budget != nil && (f.Budget.UsedTokens < 0 || f.Budget.LimitTokens < 0):
		return errors.New("task facts: token counts are not negative")
	case f.Spend != nil && (f.Spend.UsedMicroUSD < 0 || f.Spend.UsedMicroUSD > MaxMicroUSD):
		return fmt.Errorf("task facts: spend usedMicroUSD is between 0 and %d", MaxMicroUSD)
	case f.Spend != nil && (f.Spend.LimitMicroUSD < 0 || f.Spend.LimitMicroUSD > MaxMicroUSD):
		return fmt.Errorf("task facts: spend limitMicroUSD is between 0 and %d", MaxMicroUSD)
	case len(f.Runs) > MaxRuns:
		return fmt.Errorf("task facts: runs are at most %d, got %d", MaxRuns, len(f.Runs))
	case f.Issue != nil && !githubURL.MatchString(f.Issue.URL):
		return errors.New("task facts: the issue URL is a github.com issue")
	case f.PR != nil && !githubURL.MatchString(f.PR.URL):
		return errors.New("task facts: the PR URL is a github.com pull request")
	case f.PR != nil && (f.PR.ApprovedBy != "" || f.PR.ApprovedCommit != "" || f.PR.ApprovalStale != "") && !ValidLogin(f.PR.ApprovedBy):
		return errors.New("task facts: the PR's approvedBy is a GitHub login")
	case f.PR != nil && !ValidCommit(f.PR.ApprovedCommit):
		return errors.New("task facts: the PR's approvedCommit is a commit id")
	case f.PR != nil && f.PR.ApprovalStale != "" && f.PR.ApprovalStale != ApprovalOlderCommit && f.PR.ApprovalStale != ApprovalBeforeRunsEnded:
		return errors.New("task facts: the PR's approvalStale is older_commit or before_runs_ended")
	case f.Origin != "" && !ValidID(f.Origin):
		return errors.New("task facts: the origin is a room id")
	case f.Monitor != "" && !slices.Contains(MonitorVerdicts(), f.Monitor):
		return errors.New("task facts: the monitor verdict is healthy, regression or inconclusive")
	case len(f.Plan) > MaxPlan:
		return fmt.Errorf("task facts: plan is at most %d, got %d", MaxPlan, len(f.Plan))
	}
	for i, p := range f.Plan {
		if !ValidPersona(p) {
			return fmt.Errorf("task facts: plan[%d] is a persona name", i)
		}
	}
	if f.Policy != nil {
		if err := f.Policy.validate(); err != nil {
			return fmt.Errorf("task facts: policy: %w", err)
		}
	}
	if f.Profile != nil {
		if err := f.Profile.validate(); err != nil {
			return fmt.Errorf("task facts: profile: %w", err)
		}
	}
	if f.Split != nil {
		if err := f.Split.validate(); err != nil {
			return fmt.Errorf("task facts: split: %w", err)
		}
	}
	if f.Parent != nil {
		if err := f.Parent.validate(); err != nil {
			return fmt.Errorf("task facts: parent: %w", err)
		}
	}
	if f.Run != nil {
		if err := f.Run.validate(); err != nil {
			return fmt.Errorf("task facts: run: %w", err)
		}
	}
	for i, r := range f.Runs {
		if err := r.validate(); err != nil {
			return fmt.Errorf("task facts: runs[%d]: %w", i, err)
		}
	}
	return nil
}

// TaskStatePayload is the state_changed payload of a TaskFacts: {"kind":"task", ...facts}.
func TaskStatePayload(f TaskFacts) json.RawMessage {
	b, _ := json.Marshal(f) // a struct of strings, ints and slices: Marshal cannot fail
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	return StatePayload("task", fields)
}
