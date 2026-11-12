// SPDX-License-Identifier: Apache-2.0

package rooms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/wire"
)

const (
	// maxLimit is the broker's own cap on a page (bridgeapi maxRange): above it the broker
	// answers its default page of 100 without saying so, so the client refuses instead.
	maxLimit = 500
	// pageSize and maxPages bound EventsSince: the broker's default page, 10,000 events per call.
	pageSize = 100
	maxPages = 100
	// eventBytes bounds one event in a reply: a payload at the C4 cap plus its envelope.
	eventBytes = envelope.MaxPayload + 16<<10
	// maxReplyOverhead bounds the rest of a reply: lastSeq, an error, the JSON around the events.
	maxReplyOverhead = 4 << 10

	eventsRoute   = "/v1/rooms/{id}/events"
	messagesRoute = "/v1/rooms/{id}/messages"
	taskRoute     = "/v1/rooms/{id}/task"
	outputsRoute  = "/v1/rooms/{id}/outputs"
	queueRoute    = "/v1/rooms/{id}/queue"
	consumeRoute  = "/v1/rooms/{id}/queue/consume"

	// maxConsume is the broker's cap on one consume's refs (bridgeapi maxConsume): Consume
	// sends more in batches.
	maxConsume = 100
	// maxQueueReply bounds a queue listing, which the broker does not page: 16 MiB, so about 170
	// messages at the 16 KiB cap in the worst case, since the broker's encoder escapes <, > and &
	// six-fold (\u003c); 500 if they double. A longer queue fails Queue until a broker ?limit=
	// lands (FA-2 follow-up); a brief quotes 13 KiB at most.
	maxQueueReply = 500*2*envelope.MaxHumanMessage + maxReplyOverhead
)

// streamRE is the broker's queue stream: lowercase letters, no colon (it ends the origin).
var streamRE = regexp.MustCompile(`^[a-z]{1,16}$`)

// APIError is the broker's refusal: its status and wire reason (wire.Reason*), which callers
// branch on (no_room, sealed, rate_limited).
type APIError struct {
	Status int
	Reason string
}

func (e *APIError) Error() string { return fmt.Sprintf("broker: %d %s", e.Status, e.Reason) }

// The refusals callers branch on, matched by errors.Is against an *APIError.
var (
	// ErrNoRoom is 404 no_room: the broker has not made the room's log yet. Retry later.
	ErrNoRoom = errors.New("rooms: the broker has no log for the room yet")
	// ErrNotPermitted is 403 not_permitted: the broker's systemPrincipals does not list the
	// factory. Expected until FR-1 enables the entry (SP2 M9); a config fix, not a retry.
	ErrNotPermitted = errors.New("rooms: the broker does not allow system:factory")
	// ErrSealed is 410 sealed: the room was closed, and its log takes no event again.
	ErrSealed = errors.New("rooms: the room's log is sealed")
	// ErrNoRoute is a 404 or 405 with no reason: the broker's mux does not serve the route, so the
	// broker is older than the factory (a v0.7 broker has no task route). An upgrade, not a retry.
	ErrNoRoute = errors.New("rooms: the broker does not serve this route yet")
	// ErrGrantState is 409 grant_state: the grant's status or version does not allow the move,
	// such as disabling a grant already revoked.
	ErrGrantState = errors.New("rooms: the grant does not move that way")
	// ErrBadMessage is 400 bad_message: the broker refused the body as malformed, such as an output
	// record its Validate rejects. The same body is refused again: a bug, not a retry.
	ErrBadMessage = errors.New("rooms: the broker refused the message as malformed")
	// ErrNoBundle is 404 no_bundle: the room holds no bundle for that run and digest.
	ErrNoBundle = errors.New("rooms: the broker holds no such bundle")
	// ErrNoApproval is 404 no_approval: no approval has that id.
	ErrNoApproval = errors.New("rooms: the broker holds no such approval")
	// ErrBindingConflict is 409 binding_conflict: the output's approval already binds another
	// digest or branch. The same request is refused again: a bug, not a retry.
	ErrBindingConflict = errors.New("rooms: the output's approval binds another digest or branch")
	// ErrBundleDigest is a bundle whose bytes do not hash to the digest it was read by.
	ErrBundleDigest = errors.New("rooms: the bundle's sha256 is not its digest")
)

// Is matches ErrNoRoom, ErrNotPermitted, ErrSealed, ErrNoRoute, ErrGrantState, ErrBadMessage,
// ErrNoBundle, ErrNoApproval and ErrBindingConflict.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNoBundle:
		return e.Reason == wire.ReasonNoBundle
	case ErrNoApproval:
		return e.Reason == wire.ReasonNoApproval
	case ErrBindingConflict:
		return e.Reason == wire.ReasonBindingConflict
	case ErrBadMessage:
		return e.Reason == wire.ReasonBadMessage
	case ErrNoRoute:
		return e.Reason == "" && (e.Status == http.StatusNotFound || e.Status == http.StatusMethodNotAllowed)
	case ErrNoRoom:
		return e.Reason == wire.ReasonNoRoom
	case ErrNotPermitted:
		return e.Reason == wire.ReasonNotPermitted
	case ErrSealed:
		return e.Reason == wire.ReasonSealed
	case ErrGrantState:
		return e.Reason == wire.ReasonGrantState
	}
	return false
}

// Client calls the broker's system API as system:factory. The token is a projected
// ServiceAccount token (audience rooms-system) re-read before every call: kubelet rotates it.
type Client struct {
	base      *url.URL
	tokenFile string
	hc        *http.Client
	tracer    trace.Tracer
}

// New is the client for the broker at rawURL. It refuses anything but https://<host>[:port]
// (ruling SC): hc comes from httpx.New with the broker's CA (LoadCA). A nil tp traces nothing.
func New(rawURL, tokenFile string, hc *http.Client, tp trace.TracerProvider) (*Client, error) {
	u, err := url.Parse(rawURL)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.Trim(u.Path, "/") != "" ||
		u.RawQuery != "" || u.Fragment != "":
		return nil, errors.New("rooms: the broker URL must be https://<host>[:port], without credentials, path or query")
	case tokenFile == "":
		return nil, errors.New("rooms: a token file is required")
	case hc == nil:
		return nil, errors.New("rooms: an HTTP client from httpx.New is required")
	}
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	u.Path = ""
	return &Client{base: u, tokenFile: tokenFile, hc: hc, tracer: tp.Tracer("github.com/Smana/maku/internal/factory/rooms")}, nil
}

// LoadCA reads the broker's CA bundle (broker.caFile, Secret openbao-ca) for httpx.New. It is read
// once at startup: the transport holds its pool, so a CA rotation needs a restart.
func LoadCA(path string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("rooms: broker CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("rooms: broker CA %s holds no PEM certificate", path)
	}
	return roots, nil
}

// Events reads one page of room after afterSeq: the events and the room's lastSeq. The reply is
// checked so that no caller can place a cursor wrong: every event is the room's, after afterSeq,
// in strictly increasing order, and there are at most limit of them.
func (c *Client) Events(ctx context.Context, room string, afterSeq int64, limit int) ([]envelope.Event, int64, error) {
	switch {
	case !envelope.ValidID(room):
		return nil, 0, fmt.Errorf("rooms: %q is not a C2 room id", room)
	case afterSeq < 0:
		return nil, 0, fmt.Errorf("rooms: afterSeq %d is negative", afterSeq)
	case limit < 1 || limit > maxLimit:
		return nil, 0, fmt.Errorf("rooms: limit %d is not 1..%d", limit, maxLimit)
	}
	var out struct {
		Events  []envelope.Event `json:"events"`
		LastSeq int64            `json:"lastSeq"`
	}
	q := url.Values{"afterSeq": {strconv.FormatInt(afterSeq, 10)}, "limit": {strconv.Itoa(limit)}}
	bound := int64(limit)*eventBytes + maxReplyOverhead
	if err := c.do(ctx, http.MethodGet, eventsRoute, room, "?"+q.Encode(), nil, bound, &out); err != nil {
		return nil, 0, err
	}
	if len(out.Events) > limit {
		return nil, 0, fmt.Errorf("rooms: the broker sent %d events for a limit of %d", len(out.Events), limit)
	}
	prev := afterSeq
	for _, e := range out.Events {
		if e.RoomID != room || e.Seq <= prev {
			return nil, 0, fmt.Errorf("rooms: the broker sent seq %d of room %q after seq %d of %s", e.Seq, e.RoomID, prev, room)
		}
		prev = e.Seq
	}
	return out.Events, out.LastSeq, nil
}

// LastSeq is the room's current sequence: a run records it at creation, and its verdicts and
// end are read after it.
func (c *Client) LastSeq(ctx context.Context, room string) (int64, error) {
	_, last, err := c.Events(ctx, room, 0, 1)
	return last, err
}

// EventsSince pages through room's log after afterSeq, pageSize at a time and at most maxPages
// pages. It returns the events read and the cursor to resume from: the seq of the last event read,
// or afterSeq when there was none. Never the room's lastSeq, which would skip what a capped read
// left behind. Only the events keep selects are retained (nil keeps all), so a long room costs
// one page of memory, not 10,000 events; the cursor still moves past the dropped ones.
func (c *Client) EventsSince(ctx context.Context, room string, afterSeq int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error) {
	var all []envelope.Event
	cursor := afterSeq
	for range maxPages {
		evs, _, err := c.Events(ctx, room, cursor, pageSize)
		if err != nil {
			return nil, 0, err
		}
		for _, e := range evs {
			if keep == nil || keep(e) {
				all = append(all, e)
			}
		}
		if len(evs) > 0 {
			cursor = evs[len(evs)-1].Seq
		}
		if len(evs) < pageSize {
			break
		}
	}
	return all, cursor, nil
}

// TaskState posts a task_state message (C4's reserved kind), system:* only. The broker keys a
// replay on (room, principal, clientSeq) alone: a new text under a seen clientSeq answers 200 and
// is not stored, so every distinct message needs its own clientSeq.
func (c *Client) TaskState(ctx context.Context, room, text string, clientSeq int64) error {
	switch {
	case !envelope.ValidID(room):
		return fmt.Errorf("rooms: %q is not a C2 room id", room)
	case clientSeq < 1:
		return fmt.Errorf("rooms: clientSeq %d is not positive", clientSeq)
	case len(text) > envelope.MaxHumanMessage:
		return fmt.Errorf("rooms: a task_state text is at most %d bytes", envelope.MaxHumanMessage)
	}
	in := struct {
		ClientSeq int64                `json:"clientSeq"`
		Kind      envelope.MessageKind `json:"kind"`
		Text      string               `json:"text"`
	}{clientSeq, envelope.KindTaskState, text}
	var out struct {
		Seq int64 `json:"seq"`
	}
	return c.do(ctx, http.MethodPost, messagesRoute, room, "", in, maxReplyOverhead, &out)
}

// TaskFacts posts the task's structured facts (state_changed{kind:task}), system:* only. Replays
// are keyed on (room, principal, clientSeq): each distinct snapshot needs its own clientSeq.
func (c *Client) TaskFacts(ctx context.Context, room string, f envelope.TaskFacts, clientSeq int64) error {
	switch {
	case !envelope.ValidID(room):
		return fmt.Errorf("rooms: %q is not a C2 room id", room)
	case clientSeq < 1:
		return fmt.Errorf("rooms: clientSeq %d is not positive", clientSeq)
	}
	if err := f.Validate(); err != nil {
		return err
	}
	in := struct {
		ClientSeq int64              `json:"clientSeq"`
		Facts     envelope.TaskFacts `json:"facts"`
	}{clientSeq, f}
	var out struct {
		Seq int64 `json:"seq"`
	}
	return c.do(ctx, http.MethodPost, taskRoute, room, "", in, maxReplyOverhead, &out)
}

// Output posts the factory's answer to, or shadow of, one of run runID's outputs (hardening HR2):
// kind is output_applied, output_refused, output_shadowed or output_waiting (HR27), and rec must
// pass Validate for it. The broker keys a replay on (room, principal, kind, the record's id), so a
// retry of the same answer is never stored twice. A body the broker refuses is ErrBadMessage.
func (c *Client) Output(ctx context.Context, room, runID string, kind envelope.MessageKind, rec envelope.OutputRecord) error {
	switch {
	case !envelope.ValidID(room):
		return fmt.Errorf("rooms: %q is not a C2 room id", room)
	case !envelope.ValidID(runID):
		return fmt.Errorf("rooms: %q is not a C2 run id", runID)
	case kind != envelope.KindOutputApplied && kind != envelope.KindOutputRefused && kind != envelope.KindOutputShadowed &&
		kind != envelope.KindOutputWaiting:
		return fmt.Errorf("rooms: %q is not an output answer", kind)
	}
	if err := rec.Validate(kind); err != nil {
		return err
	}
	in := struct {
		RunID  string                `json:"runId"`
		Kind   envelope.MessageKind  `json:"kind"`
		Record envelope.OutputRecord `json:"record"`
	}{runID, kind, rec}
	var out struct {
		Seq int64 `json:"seq"`
	}
	return c.do(ctx, http.MethodPost, outputsRoute, room, "", in, maxReplyOverhead, &out)
}

// MaxBundle is a push bundle's cap, the design's 8 MiB (hardening.MaxBundle).
const MaxBundle = 8 << 20

const bundleRoute = "/v1/rooms/{id}/bundles/{digest}"

var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Bundle reads the bundle run runID's output_push stored in room under digest (hardening HR22).
// It reads at most MaxBundle, and the bytes must hash to digest: ErrBundleDigest otherwise. A
// bundle the broker does not hold is ErrNoBundle.
func (c *Client) Bundle(ctx context.Context, room, runID, digest string) ([]byte, error) {
	switch {
	case !envelope.ValidID(room):
		return nil, fmt.Errorf("rooms: %q is not a C2 room id", room)
	case !envelope.ValidID(runID):
		return nil, fmt.Errorf("rooms: %q is not a C2 run id", runID)
	case !digestRE.MatchString(digest):
		return nil, errors.New("rooms: a bundle's digest is 64-hex")
	}
	path := strings.Replace(strings.Replace(bundleRoute, "{id}", room, 1), "{digest}", digest, 1)
	var b []byte
	if err := c.call(ctx, http.MethodGet, bundleRoute, path, room, "?run="+runID, nil, MaxBundle, &b); err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != digest {
		return nil, ErrBundleDigest
	}
	return b, nil
}

// The digest-bound approval routes (hardening HR28).
const (
	approvalsRoute = "/v1/rooms/{id}/approvals"
	approvalRoute  = "/v1/approvals/{approval}"
	supersedeRoute = "/v1/approvals/{approval}/supersede"
)

var (
	outputIDRE     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	agentBranchRE  = regexp.MustCompile(`^agent/[a-z0-9][a-z0-9._/-]{0,100}$`)
	approvalULIDRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

// ApprovalRequest is the factory's request for a maintainer's approval of one push bundle: the run
// and output that carry it, its digest, the branch it pushes, how long it waits, and Action, the
// JSON object the card and the room page show (the output, the digest, the heads and the protected
// paths: never commit text).
type ApprovalRequest struct {
	RunID, Output, Digest, Branch string
	TTL                           time.Duration
	Action                        any
}

// OutputApproval is a digest-bound approval as the broker reads it back.
type OutputApproval struct {
	ApprovalID string    `json:"approvalId"`
	RoomID     string    `json:"roomId"`
	RunID      string    `json:"runId"`
	State      string    `json:"state"`
	Branch     string    `json:"branch"`
	Digest     string    `json:"digest"`
	Output     string    `json:"output"`
	DecidedBy  string    `json:"decidedBy,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// RequestApproval asks room's approvers to approve one push bundle (hardening HR28). The broker
// keys it on the output, so a retry answers the first approval: its id, state and deadline.
func (c *Client) RequestApproval(ctx context.Context, room string, req ApprovalRequest) (OutputApproval, error) {
	switch {
	case !envelope.ValidID(room):
		return OutputApproval{}, fmt.Errorf("rooms: %q is not a C2 room id", room)
	case !envelope.ValidID(req.RunID):
		return OutputApproval{}, fmt.Errorf("rooms: %q is not a C2 run id", req.RunID)
	case !outputIDRE.MatchString(req.Output) || !digestRE.MatchString(req.Digest) || !agentBranchRE.MatchString(req.Branch):
		return OutputApproval{}, errors.New("rooms: an approval binds a 16-hex output, a 64-hex digest and an agent/ branch")
	case req.TTL < time.Minute || req.TTL > 7*24*time.Hour:
		return OutputApproval{}, errors.New("rooms: an approval waits a minute to 7 days")
	}
	in := struct {
		RunID      string `json:"runId"`
		Output     string `json:"output"`
		Digest     string `json:"digest"`
		Branch     string `json:"branch"`
		TTLSeconds int64  `json:"ttlSeconds"`
		Action     any    `json:"action"`
	}{req.RunID, req.Output, req.Digest, req.Branch, int64(req.TTL / time.Second), req.Action}
	var out OutputApproval
	if err := c.do(ctx, http.MethodPost, approvalsRoute, room, "", in, maxReplyOverhead, &out); err != nil {
		return OutputApproval{}, err
	}
	out.RoomID, out.RunID, out.Branch, out.Digest, out.Output = room, req.RunID, req.Branch, req.Digest, req.Output
	return out, nil
}

// SupersedeApproval closes a pending digest-bound approval as superseded: a later push of its run
// replaced the one it holds (HR27). It returns the approval's state, which a decision that came
// first keeps. A missing approval is ErrNoApproval.
func (c *Client) SupersedeApproval(ctx context.Context, id string) (string, error) {
	if !approvalULIDRE.MatchString(id) {
		return "", errors.New("rooms: an approval id is a ULID")
	}
	var out struct {
		State string `json:"state"`
	}
	if err := c.call(ctx, http.MethodPost, supersedeRoute, strings.Replace(supersedeRoute, "{approval}", id, 1), "", "",
		struct{}{}, maxReplyOverhead, &out); err != nil {
		return "", err
	}
	return out.State, nil
}

// Approval reads a digest-bound approval back (HR27): the factory re-reads it before it applies the
// push it holds. A missing approval, or one that is not digest-bound, is ErrNoApproval.
func (c *Client) Approval(ctx context.Context, id string) (OutputApproval, error) {
	if !approvalULIDRE.MatchString(id) {
		return OutputApproval{}, errors.New("rooms: an approval id is a ULID")
	}
	var out OutputApproval
	if err := c.call(ctx, http.MethodGet, approvalRoute, strings.Replace(approvalRoute, "{approval}", id, 1), "", "",
		nil, maxReplyOverhead, &out); err != nil {
		return OutputApproval{}, err
	}
	return out, nil
}

// Queued is one live message of the room's FIFO queue (SP2 §2); Ref is its seq in the room.
type Queued struct {
	Ref    int64  `json:"ref"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// Enqueue queues text for the room's next run, as system:factory on stream (review, ci). The
// broker redacts it, and dedupes on (room, stream, clientSeq): a seen clientSeq is never stored
// twice, whatever its order, so each distinct message needs its own clientSeq in its stream.
func (c *Client) Enqueue(ctx context.Context, room, stream, text string, clientSeq int64) error {
	switch {
	case !envelope.ValidID(room):
		return fmt.Errorf("rooms: %q is not a C2 room id", room)
	case !streamRE.MatchString(stream):
		return fmt.Errorf("rooms: stream %q is not [a-z]{1,16}", stream)
	case clientSeq < 1:
		return fmt.Errorf("rooms: clientSeq %d is not positive", clientSeq)
	case strings.TrimSpace(text) == "" || len(text) > envelope.MaxHumanMessage:
		return fmt.Errorf("rooms: a queued text is 1 to %d bytes", envelope.MaxHumanMessage)
	}
	in := struct {
		Text      string `json:"text"`
		ClientSeq int64  `json:"clientSeq"`
		Stream    string `json:"stream"`
	}{text, clientSeq, stream}
	var out struct {
		Seq       int64 `json:"seq"`
		Duplicate bool  `json:"duplicate"`
	}
	return c.do(ctx, http.MethodPost, queueRoute, room, "", in, maxReplyOverhead, &out)
}

// Queue lists the room's messages still queued, oldest first.
func (c *Client) Queue(ctx context.Context, room string) ([]Queued, error) {
	if !envelope.ValidID(room) {
		return nil, fmt.Errorf("rooms: %q is not a C2 room id", room)
	}
	var out struct {
		Queued []Queued `json:"queued"`
	}
	if err := c.do(ctx, http.MethodGet, queueRoute, room, "", nil, maxQueueReply, &out); err != nil {
		return nil, err
	}
	prev := int64(0)
	for _, q := range out.Queued {
		if q.Ref <= prev {
			return nil, fmt.Errorf("rooms: the broker listed ref %d after %d", q.Ref, prev)
		}
		prev = q.Ref
	}
	return out.Queued, nil
}

// Consume marks refs consumed by runID: pass only the refs the run's brief returned
// (reconciler.ReviseBrief's refs), never the rest of a listing, or an unread message is lost. A ref
// no longer queued is skipped by the broker, so a retry is safe. No refs, no call.
func (c *Client) Consume(ctx context.Context, room string, refs []int64, runID string) error {
	switch {
	case !envelope.ValidID(room):
		return fmt.Errorf("rooms: %q is not a C2 room id", room)
	case !envelope.ValidID(runID):
		return fmt.Errorf("rooms: %q is not a C2 run id", runID)
	}
	for batch := range slices.Chunk(refs, maxConsume) {
		in := struct {
			Refs  []int64 `json:"refs"`
			RunID string  `json:"runId"`
		}{batch, runID}
		var out struct {
			Consumed int `json:"consumed"`
		}
		if err := c.do(ctx, http.MethodPost, consumeRoute, room, "", in, maxReplyOverhead, &out); err != nil {
			return err
		}
	}
	return nil
}

// slackLinkRoute is the broker's self-serve link read (system:factory only).
const slackLinkRoute = "/v1/links/slack"

var (
	slackTeamRE = regexp.MustCompile(`^T[A-Z0-9]{2,20}$`)
	slackUserRE = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)
)

// SlackLink is the ZITADEL sub a Slack user linked through the broker's self-serve link; found is
// false when they linked none. The broker answers a sub, never a GitHub login.
func (c *Client) SlackLink(ctx context.Context, team, user string) (string, bool, error) {
	if !slackTeamRE.MatchString(team) || !slackUserRE.MatchString(user) {
		return "", false, fmt.Errorf("rooms: %q/%q is not a Slack team and user", team, user)
	}
	var out struct {
		Sub string `json:"sub"`
	}
	q := "?" + url.Values{"team": {team}, "user": {user}}.Encode()
	err := c.do(ctx, http.MethodGet, slackLinkRoute, "", q, nil, maxReplyOverhead, &out)
	var api *APIError
	switch {
	case errors.As(err, &api) && api.Status == http.StatusNotFound && api.Reason == wire.ReasonNotLinked:
		return "", false, nil
	case err != nil:
		return "", false, err
	case out.Sub == "":
		return "", false, errors.New("rooms: the broker's link names no account")
	}
	return out.Sub, true, nil
}

// spanReason bounds a refusal's reason, which the peer's reply body sets, to the system API's
// vocabulary: anything else is "other" on a span.
func spanReason(r string) string {
	switch r {
	case wire.ReasonBadRoom, wire.ReasonBadMessage, wire.ReasonUnauthenticated, wire.ReasonNotPermitted,
		wire.ReasonNoRoom, wire.ReasonSealed, wire.ReasonRateLimited, wire.ReasonLogUnavailable, wire.ReasonTimedOut,
		wire.ReasonBadStream, wire.ReasonBadConsume, wire.ReasonNoQueue, wire.ReasonNotLinked, wire.ReasonBadSlackUser,
		wire.ReasonBadGrant, wire.ReasonNoGrant, wire.ReasonGrantState:
		return r
	}
	return "other"
}

// do is one call. Its span carries metadata only (O-1 M3): the room id, the method, the route
// template, the status and the broker's reason, never a body, the query or the token.
func (c *Client) do(ctx context.Context, method, route, room, query string, in any, maxReply int64, out any) error {
	return c.call(ctx, method, route, strings.Replace(route, "{id}", url.PathEscape(room), 1), room, query, in, maxReply, out)
}

// call is do with the path given: a grant route's {id} is a grant's, which the span never carries.
func (c *Client) call(ctx context.Context, method, route, path, room, query string, in any, maxReply int64, out any) (err error) {
	ctx, span := c.tracer.Start(ctx, "broker "+method+" "+route, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("agent.room_id", room), attribute.String("http.request.method", method),
			attribute.String("url.template", route)))
	defer span.End()
	defer func() {
		if err == nil {
			return
		}
		why := "transport" // an error's text can quote a URL: only its class goes on the span
		var api *APIError
		if errors.As(err, &api) {
			why = spanReason(api.Reason)
		}
		span.SetAttributes(attribute.String("error.type", why))
		span.SetStatus(codes.Error, why)
	}()
	tok, err := os.ReadFile(filepath.Clean(c.tokenFile))
	if err != nil {
		return fmt.Errorf("rooms: token: %w", err)
	}
	bearer := strings.TrimSpace(string(tok))
	if bearer == "" {
		return errors.New("rooms: the token file is empty")
	}
	var body []byte
	if in != nil {
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("rooms: encode: %w", err)
		}
	}
	target := c.base.JoinPath(path).String() + query
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("rooms: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("rooms: %s %s: %w", method, route, err)
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	span.SetAttributes(attribute.Int("http.response.status_code", status))
	raw, err := httpx.ReadBody(resp.Body, maxReply)
	if err != nil {
		return fmt.Errorf("rooms: %s %s: %w", method, route, err)
	}
	if status < 200 || status > 299 {
		var e wire.Error
		_ = json.Unmarshal(raw, &e)
		return &APIError{Status: status, Reason: e.Reason}
	}
	if b, ok := out.(*[]byte); ok { // a raw reply, such as a bundle
		*b = raw
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("rooms: %s %s: the reply is not JSON", method, route)
	}
	return nil
}
