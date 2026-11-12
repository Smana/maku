// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// OutputApprovals is the store's digest-bound approvals, as the factory reaches them (hardening
// HR27, HR28); *store.Store implements it.
type OutputApprovals interface {
	RequestOutputApproval(ctx context.Context, a store.Approval, d envelope.Draft) (store.Approval, envelope.Event, error)
	CloseOutputApproval(ctx context.Context, id, state string) (store.Approval, error)
	Approval(ctx context.Context, id string) (store.Approval, error)
	Prompters(ctx context.Context, roomID, runID string) ([]string, error)
}

const (
	// maxOutputApprovalBytes bounds a request: the action names at most a few hundred paths.
	maxOutputApprovalBytes = 64 << 10
	// The deadline the factory may ask for: hardening.applier.approvalTTL, at most 7 days.
	minOutputApprovalTTL = time.Minute
	maxOutputApprovalTTL = 7 * 24 * time.Hour
)

var (
	outputIDRE     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	agentBranchRE  = regexp.MustCompile(`^agent/[a-z0-9][a-z0-9._/-]{0,100}$`)
	approvalULIDRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

// OutputApprovalView is a digest-bound approval as the factory reads it: its state, its binding,
// and who closed it.
type OutputApprovalView struct {
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

func outputApprovalView(a store.Approval) OutputApprovalView {
	return OutputApprovalView{ApprovalID: a.ID, RoomID: a.RoomID, RunID: a.RunID, State: a.State, Branch: a.BindingBranch,
		Digest: a.BindingDigest, Output: a.BindingOutput, DecidedBy: a.DecidedBy, ExpiresAt: a.ExpiresAt.UTC()}
}

// requestOutputApproval: POST /v1/rooms/{id}/approvals, OutputClient only (hardening HR28). The
// factory asks the room's approvers to approve one push bundle: {runId, output, digest, branch,
// ttlSeconds, action}. The row is a forge.push approval bound to the branch, the digest and the
// output; the run's prompters may not decide it (four eyes, as today). Idempotent per output: a
// retry answers the stored approval. 200 {approvalId, state, expiresAt}.
func (s *Server) requestOutputApproval(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.outputCaller(w, r, s.OutputApprovals != nil)
	if !ok {
		return
	}
	defer release()
	ctx, id := r.Context(), r.PathValue("id")
	if !envelope.ValidID(id) {
		fail(w, http.StatusBadRequest, wire.ReasonBadRoom)
		return
	}
	var in struct {
		RunID      string          `json:"runId"`
		Output     string          `json:"output"`
		Digest     string          `json:"digest"`
		Branch     string          `json:"branch"`
		TTLSeconds int64           `json:"ttlSeconds"`
		Action     json.RawMessage `json:"action"`
	}
	ttl := func() time.Duration { return time.Duration(in.TTLSeconds) * time.Second }
	if err := decodeStrict(r.Body, &in); err != nil || !envelope.ValidID(in.RunID) || !outputIDRE.MatchString(in.Output) ||
		!bundleDigestRE.MatchString(in.Digest) || !agentBranchRE.MatchString(in.Branch) ||
		ttl() < minOutputApprovalTTL || ttl() > maxOutputApprovalTTL || !isObject(in.Action) {
		fail(w, http.StatusBadRequest, wire.ReasonBadApproval)
		return
	}
	if _, err := s.Log.Room(ctx, id); err != nil {
		s.logFailure(w, err, "read a room", "room", id)
		return
	}
	switch mine, err := s.runOfRoom(r, id, in.RunID); {
	case err != nil:
		s.logFailure(w, err, "read a run's events", "room", id)
		return
	case !mine:
		fail(w, http.StatusBadRequest, wire.ReasonBadApproval)
		return
	}
	var principal string
	if s.Watch != nil {
		if run, _, _ := s.Watch.Live(in.RunID); run.ID != "" {
			// D5: the branch is the run's, as its claim carries it; the factory never names another.
			if run.Branch != "" && run.Branch != in.Branch {
				fail(w, http.StatusBadRequest, wire.ReasonBadApproval)
				return
			}
			principal = run.Principal
		}
	}
	action, rules, err := s.Redactor.Payload(ctx, in.Action)
	switch {
	case err != nil && ctx.Err() != nil && !errors.Is(err, redact.ErrKeyCollision):
		fail(w, http.StatusServiceUnavailable, wire.ReasonTimedOut)
		return
	case err != nil, len(action) > maxApprovalAction, !isObject(action):
		fail(w, http.StatusBadRequest, wire.ReasonBadAction)
		return
	}
	prompters, err := s.OutputApprovals.Prompters(ctx, id, in.RunID)
	if err != nil {
		s.logFailure(w, err, "read a run's prompters", "room", id, "run", in.RunID)
		return
	}
	if principal != "" && !slices.Contains(prompters, principal) {
		prompters = append(prompters, principal)
	}
	a, _, err := s.OutputApprovals.RequestOutputApproval(ctx, store.Approval{ID: ulid.Make().String(), RoomID: id, RunID: in.RunID,
		Class: "forge.push", BindingBranch: in.Branch, BindingDigest: in.Digest, BindingOutput: in.Output, Action: action,
		Prompters: prompters, ExpiresAt: s.now().Add(ttl())},
		envelope.Draft{Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: p.ID}, Origin: envelope.OriginClient,
			OriginClient: p.ID + ":approvals", OriginSeq: outputSeq(in.Output), Redactions: rules})
	switch {
	case errors.Is(err, store.ErrBindingConflict):
		fail(w, http.StatusConflict, wire.ReasonBindingConflict)
		return
	case err != nil:
		s.logFailure(w, err, "request an output approval", "room", id, "run", in.RunID)
		return
	}
	reply(w, http.StatusOK, map[string]any{"approvalId": a.ID, "state": a.State, "expiresAt": a.ExpiresAt.UTC()})
}

// supersedeOutputApproval: POST /v1/approvals/{id}/supersede, OutputClient only (HR27). A later
// push of the run replaced the one this approval holds: a pending digest-bound approval closes
// superseded, and a closed one answers as it is. 200 {approvalId, state}.
func (s *Server) supersedeOutputApproval(w http.ResponseWriter, r *http.Request) {
	_, release, ok := s.outputCaller(w, r, s.OutputApprovals != nil)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	if !approvalULIDRE.MatchString(id) {
		fail(w, http.StatusBadRequest, wire.ReasonBadApproval)
		return
	}
	a, err := s.OutputApprovals.CloseOutputApproval(r.Context(), id, store.ApprovalSuperseded)
	if !s.outputApprovalFailure(w, err, id, "supersede an output approval") {
		return
	}
	reply(w, http.StatusOK, map[string]string{"approvalId": a.ID, "state": a.State})
}

// outputApproval: GET /v1/approvals/{id}, OutputClient only (HR27). A digest-bound approval's
// state, binding and decider, which the factory re-reads before it applies the push. Any other
// approval is 404, as a missing one is.
func (s *Server) outputApproval(w http.ResponseWriter, r *http.Request) {
	_, release, ok := s.outputCaller(w, r, s.OutputApprovals != nil)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	if !approvalULIDRE.MatchString(id) {
		fail(w, http.StatusBadRequest, wire.ReasonBadApproval)
		return
	}
	a, err := s.OutputApprovals.Approval(r.Context(), id)
	if err == nil && !a.DigestBound() {
		err = store.ErrNoApproval
	}
	if !s.outputApprovalFailure(w, err, id, "read an output approval") {
		return
	}
	reply(w, http.StatusOK, outputApprovalView(a))
}

// outputApprovalFailure answers err, if any, and reports whether the caller may go on.
func (s *Server) outputApprovalFailure(w http.ResponseWriter, err error, id, what string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNoApproval):
		fail(w, http.StatusNotFound, wire.ReasonNoApproval)
	default:
		s.logFailure(w, err, what, "approval", id)
	}
	return false
}
