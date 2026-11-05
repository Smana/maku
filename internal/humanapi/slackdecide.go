// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/time/rate"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/store"
)

// The Slack decide route (v3.x Slack push approval D2): the one path the App decides an approval
// through. It is not a human's socket, a tool or the room page: one body, one meaning, and every
// refusal a closed code, so the App never retries one and Slack never redelivers one (D1, D13).
// The identity is resolved, never asserted (D3): the click names a Slack user, slack_links names
// the account, and GitHub names what that account may write.

// slackDecidableClass is the one class this route decides (D13): a constant, never config, so a
// widening costs a code change and a design.
const slackDecidableClass = "forge.push"

// slackDecidePath is the route, registered only when human.slackDecide is set (D14): unset, it is
// the mux's 404, as /mcp is when MCP is false.
const slackDecidePath = "/api/slack/decisions"

// slackDecideBody caps the body: §1's is under two hundred bytes.
const slackDecideBody = 4 << 10

// §1's closed result codes, the route's whole vocabulary. Where the room page already answers the
// same string (acts.go's rejections, which the browser frames carry), it is the same constant: one
// vocabulary, two surfaces.
const (
	slackNotPermitted         = rejectNotPermitted
	slackAlreadyDecided       = rejectDecided
	slackApprovalExpired      = rejectExpired
	slackFourEyes             = rejectFourEyes
	slackSealed               = rejectSealed
	slackRateLimited          = rejectRateLimited
	slackNotLinked            = "not_linked"      // no slack_links row, or no GitHub identity on the account
	slackLinkUnverified       = "link_unverified" // ZITADEL or GitHub could not be asked
	slackRepositoryPermission = "repository_permission"
	slackBindingMismatch      = "binding_mismatch"
	slackNotSupported         = "not_supported"
	slackNotFound             = "not_found"
	slackBadRequest           = "bad_request"
	slackUnavailable          = "unavailable"
)

var (
	// slackChannelRE, slackMessageTSRE and slackApprovalIDRE are §1's remaining field shapes.
	// slackTeamRE and slackUserRE are the link's (slacklink.go), reused so the App's two surfaces
	// cannot disagree about who or which workspace a click names.
	slackChannelRE   = regexp.MustCompile(`^[CG][A-Z0-9]{2,20}$`)
	slackMessageTSRE = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{1,8}$`)
	// slackApprovalIDRE is a ULID: bridgeapi is the only producer of an approval id.
	slackApprovalIDRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

// SlackDecision is §1's body. Every field is checked against its shape before any read.
type SlackDecision struct {
	Team       string `json:"team"`
	User       string `json:"user"`
	Channel    string `json:"channel"`
	MessageTS  string `json:"messageTS"`
	ApprovalID string `json:"approvalId"`
	Branch     string `json:"branch"`
	Decision   string `json:"decision"`
}

// slackDecisionResult is the route's whole answer: one key, one closed code, never a sentence.
type slackDecisionResult struct {
	Result string `json:"result"`
}

// SlackDecideStore is the part of the store the route reads and writes; *store.Store has it.
type SlackDecideStore interface {
	SlackLink(ctx context.Context, team, user string) (sub string, found bool, err error)
	Approval(ctx context.Context, id string) (store.Approval, error)
	Decide(ctx context.Context, approvalID, decision, by, reason string, d envelope.Draft) (envelope.Event, store.Approval, error)
	Room(ctx context.Context, id string) (store.RoomState, error)
}

// SlackDecider is human.slackDecide: who may call, for which workspace, and the readings standing
// is resolved from. Groups is left zero on purpose (D15): the broker holds no token for the linked
// human, so standing is exactly the Room CR's, and an agents-admin who is neither the room's owner
// nor one of its approvers is refused here and decides on the room page.
type SlackDecider struct {
	Store     SlackDecideStore
	Principal string // "system:slack-app"
	TeamID    string
	Rooms     client.Reader
	Namespace string
	Groups    policy.Groups
	Identity  *ghidentity.Resolver
	Access    *repoaccess.Checker
	Runs      Runs
	Metrics   *metrics.Set
	OnDecided func(time.Duration)
	Logger    *slog.Logger

	mu     sync.Mutex
	limits map[string]*rate.Limiter // (team,user) → 10/min, burst 5, per replica
}

// slackDecide is POST /api/slack/decisions: the App's one route that records a decision. Every
// step before the resolution answers a transport status; the resolution answers §1's closed code,
// so a refusal is never retried.
func (s *Server) slackDecide(w http.ResponseWriter, r *http.Request) {
	d := s.SlackDecide
	p, ok := s.sponsoredPrincipal(r)
	if !ok || !p.Sponsored || p.ID != d.Principal {
		// A human's, a machine's or another App's token: no code, and nothing of the body read.
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	in, ok := decodeSlackDecision(w, r)
	if !ok {
		d.answer(w, r, slackBadRequest, "")
		return
	}
	if !d.allow(in.Team + "/" + in.User) {
		d.answer(w, r, slackRateLimited, "")
		return
	}
	result, decided := d.resolve(r.Context(), p, in)
	d.answer(w, r, result, decided)
	d.logged(r.Context(), in, result, decided)
}

// decodeSlackDecision reads §1's body: application/json, one value, 4 KiB, and every field its
// shape. false is bad_request, before any read.
func decodeSlackDecision(w http.ResponseWriter, r *http.Request) (SlackDecision, bool) {
	var in SlackDecision
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return in, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, slackDecideBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		return in, false
	}
	return in, slackDecisionShaped(in)
}

// slackDecisionShaped is §1's shapes and the two decisions. The team is deliberately not one of
// them: it is resolve's workspace check, so a peer workspace's click gets a closed code and not a
// 400. The branch is the AgentRun's own pattern (envelope.ValidBranch), never a copy of it (R4).
func slackDecisionShaped(in SlackDecision) bool {
	return slackUserRE.MatchString(in.User) && slackChannelRE.MatchString(in.Channel) &&
		slackMessageTSRE.MatchString(in.MessageTS) && slackApprovalIDRE.MatchString(in.ApprovalID) &&
		envelope.ValidBranch(in.Branch) &&
		(in.Decision == store.ApprovalApproved || in.Decision == store.ApprovalDenied)
}

// allow takes one press of (team,user): 10 a minute, burst 5, per replica (P22). A person presses
// a button a few times and never in a stream, so this is not the room page's 10/s.
func (d *SlackDecider) allow(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.limits == nil {
		d.limits = map[string]*rate.Limiter{}
	}
	l, ok := d.limits[key]
	if !ok {
		l = rate.NewLimiter(rate.Every(6*time.Second), 5)
		d.limits[key] = l
	}
	return l.Allow()
}

// answer writes §1's one-key body with the status its code carries: 400 for a body or a decision
// the route refuses as malformed, 429 over the limiter, 503 for the one retryable code, and 200 for
// every closed refusal.
func (d *SlackDecider) answer(w http.ResponseWriter, r *http.Request, result, decided string) {
	d.counted(r.Context(), result, decided)
	code := http.StatusOK
	switch result {
	case slackBadRequest:
		code = http.StatusBadRequest
	case slackRateLimited:
		code = http.StatusTooManyRequests
	case slackUnavailable:
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	writeJSON(w, slackDecisionResult{Result: result})
}

// counted is rooms_slack_decisions_total{result, decision}: the closed code this request answered
// with, and the decision it recorded, empty when it recorded none.
func (d *SlackDecider) counted(ctx context.Context, result, decided string) {
	if d.Metrics == nil || d.Metrics.SlackDecisions == nil {
		return
	}
	d.Metrics.SlackDecisions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("result", result), attribute.String("decision", decided)))
}

// logged is the one line a press leaves: the ids and the code, never a payload, a branch or a
// login, so the line is safe to keep. It is D2's accepted residual's compensating control.
func (d *SlackDecider) logged(ctx context.Context, in SlackDecision, result, decided string) {
	level := slog.LevelWarn
	if result == store.ApprovalApproved || result == store.ApprovalDenied {
		level = slog.LevelInfo
	}
	d.log().Log(ctx, level, "slack decision", "team", in.Team, "user", in.User, "approval", in.ApprovalID,
		"result", result, "decision", decided)
}

func (d *SlackDecider) log() *slog.Logger {
	if d.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return d.Logger
}

// failed is one unreadable store: the code says nothing about what could not be read (§1: the App
// retries it), and the line names the read, never its value.
func (d *SlackDecider) failed(ctx context.Context, what string, err error) (string, string) {
	d.log().WarnContext(ctx, "slack decision unreadable", "read", what, "err", err)
	return slackUnavailable, ""
}

// resolve is §2's resolution, in its order. Every value comes from the broker's own stores; the
// body is an id to look up and a branch to compare, and a hint at both (D6). It returns §1's code
// and, when it recorded one, the decision, which the counter carries.
func (d *SlackDecider) resolve(ctx context.Context, p authn.Principal, in SlackDecision) (result, decided string) {
	// 1. The App. The shell answers 403 to anything else; it is repeated here so a resolution can
	// never run for another principal, and fails closed if it ever did.
	if !p.Sponsored || p.ID != d.Principal {
		return slackNotPermitted, ""
	}
	// 2. The workspace (D15): a peer workspace's click is not this factory's, and the answer says
	// nothing about which workspace it was.
	if in.Team != d.TeamID {
		return slackNotPermitted, ""
	}
	// 3. The link: the click names a Slack user, and slack_links says which account that is.
	// Nothing in the body can name a human.
	sub, found, err := d.Store.SlackLink(ctx, in.Team, in.User)
	switch {
	case err != nil:
		return d.failed(ctx, "slack link", err)
	case !found || sub == "":
		return slackNotLinked, ""
	}
	// 4. The room: the approval row names it, and the body cannot, so a card cannot be pointed at
	// another room's approval. The log's state says whether the room is sealed.
	ap, err := d.Store.Approval(ctx, in.ApprovalID)
	switch {
	case errors.Is(err, store.ErrNoApproval):
		return slackNotFound, ""
	case err != nil:
		return d.failed(ctx, "approval", err)
	}
	st, err := d.Store.Room(ctx, ap.RoomID)
	switch {
	case errors.Is(err, store.ErrNoRoom):
		return slackNotFound, ""
	case err != nil:
		return d.failed(ctx, "room state", err)
	}
	// 5. The class (D13): free, and before any GitHub read, so a class this route does not decide
	// never costs one.
	if ap.Class != slackDecidableClass {
		return slackNotSupported, ""
	}
	// 6. The Room CR: standing is the CR's alone (D15), so its spec is the whole input.
	room := &v1alpha1.Room{}
	err = d.Rooms.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: ap.RoomID}, room)
	switch {
	case apierrors.IsNotFound(err):
		return slackNotFound, ""
	case err != nil:
		return d.failed(ctx, "room", err)
	case st.Sealed:
		return slackSealed, ""
	}
	// 7–10. The two standings, in one read bounded as the room page's gate is (accessWait). The
	// zero Groups is D15: an empty group name never matches, so the role is exactly the CR's.
	// WebUI true is a fact about the surface, not a grant: Decide is uiOnly, and nothing here is
	// reachable by a token a local agent can drive.
	rctx, cancel := context.WithTimeout(ctx, accessWait)
	defer cancel()
	subj := d.Groups.Resolve(room, authn.Principal{Kind: envelope.ActorHuman, ID: "human:" + sub, Sub: sub}, "", true)
	if !policy.Allowed(subj, policy.Decide) {
		return slackNotPermitted, ""
	}
	// 8. A room without a repository is admins-only on the room page, and nobody is an admin here;
	// with the GitHub check unwired nothing can say who the caller is, so it fails the same way.
	if room.Spec.Repository == "" || d.Identity == nil || d.Access == nil {
		return slackNotPermitted, ""
	}
	// 9. The account: the current login, never one the App cached or the click named.
	login, err := d.Identity.Login(rctx, sub, room.Spec.Repository)
	switch {
	case errors.Is(err, ghidentity.ErrInactive):
		return slackNotLinked, ""
	case err != nil:
		return slackLinkUnverified, ""
	case login == "":
		return slackNotLinked, ""
	}
	// 10. Repository write, which is the second standing a repository maintainer outside the room
	// does not have.
	writes, err := d.Access.CanWrite(rctx, room.Spec.Repository, login)
	switch {
	case err != nil:
		return slackLinkUnverified, ""
	case !writes:
		return slackRepositoryPermission, ""
	}
	// 11. The binding (D5, D6): the row recorded the run's branch when it was requested, and the
	// run is re-read now. The hint must be both, so a card that outlived the state it was drawn from
	// decides nothing, and an approval that binds no branch at all decides nothing.
	expected := ""
	for _, run := range d.Runs.InRoom(ap.RoomID) {
		if run.ID == ap.RunID {
			expected = run.Branch
		}
	}
	if ap.BindingBranch == "" || expected == "" || in.Branch != expected || ap.BindingBranch != expected {
		return slackBindingMismatch, ""
	}
	// 12. Four eyes (OD-16), on the row's own prompters: the room page compares the same field, so
	// the two surfaces can never disagree about who is excluded.
	if room.Spec.Approvals.FourEyes && slices.Contains(ap.Prompters, "human:"+sub) {
		return slackFourEyes, ""
	}
	// 13. The decision, through the store's one fence (D7): unchanged, under an idempotency scope of
	// its own, so a Slack retry replays and the opposite decision is not a replay.
	dr := envelope.Draft{
		RoomID: ap.RoomID, RunID: ap.RunID,
		Actor:  envelope.Actor{Kind: envelope.ActorHuman, ID: "human:" + sub}, // D8: the person, never the App
		Origin: envelope.OriginClient,
		// The surface is in the scope, not the actor: one key per approval, and one per decision
		// within it (D7), so a replayed click replays and the opposite decision is not a replay.
		OriginClient: "human:" + sub + ":slack:" + ap.ID,
		OriginSeq:    decisionSeq[in.Decision],
	}
	// The reason is empty, always (D12): this surface carries a decision and never a sentence.
	_, _, err = d.Store.Decide(ctx, ap.ID, in.Decision, "human:"+sub, "", dr)
	switch {
	case errors.Is(err, store.ErrAlreadyDecided):
		return slackAlreadyDecided, ""
	case errors.Is(err, store.ErrApprovalExpired):
		return slackApprovalExpired, ""
	case errors.Is(err, store.ErrSealed):
		return slackSealed, ""
	case errors.Is(err, store.ErrNoApproval):
		return slackNotFound, ""
	case errors.Is(err, store.ErrBadDecision):
		return slackBadRequest, ""
	case err != nil:
		return d.failed(ctx, "decision", err)
	}
	if ap.State == store.ApprovalPending && d.OnDecided != nil { // a replay was already observed once
		d.OnDecided(time.Since(ap.RequestedAt))
	}
	return in.Decision, in.Decision
}

// decisionSeq is the idempotency key's seq: one key per (approval, decision), so an approval's
// denial is never the stored replay of its approval.
var decisionSeq = map[string]int64{store.ApprovalApproved: 1, store.ApprovalDenied: 2}
