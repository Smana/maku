// SPDX-License-Identifier: Apache-2.0

// Package runrequest asks for the next run. Only the factory creates AgentRuns
// (C3): with SP3, the broker calls POST /v1/runs with the human's own access token
// (C4); before SP3, it renders the claim for the owner to create (ruling P14).
package runrequest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/runwatch"
)

var (
	// ErrBudget is the factory refusing a run over the principal's budget (429).
	ErrBudget = errors.New("over_budget")
	// ErrForbidden is the factory refusing the principal (403), or no token to present.
	ErrForbidden = errors.New("forbidden")
	// ErrNoKey is a factory request with no idempotency key, refused before the call.
	ErrNoKey = errors.New("runrequest: a factory request needs an idempotency key")
)

// factoryTimeout bounds one POST /v1/runs; maxResponse its answer.
const (
	factoryTimeout = 15 * time.Second
	maxResponse    = 64 << 10
)

// Request is one run to start: the claim's fields, the principal asking, and
// that principal's own access token for the factory (C4). IdempotencyKey names
// the act that asks (review 4.4 I1): the same key always means the same run.
type Request struct {
	Role, Repository, BaseRef, Branch, TaskText, TaskURL, DataClass, RoomRef, Principal, AccessToken string
	IdempotencyKey                                                                                   string
	EgressProfiles                                                                                   []string
}

// Result is the requested run: its id, how it was requested ("manifest" or
// "factory"), and before SP3 the rendered claim.
type Result struct {
	RunID    string          `json:"runId"`
	Via      string          `json:"via"`
	Manifest json.RawMessage `json:"manifest,omitempty"`
}

// Requester asks for a run.
type Requester interface {
	Request(ctx context.Context, r Request) (Result, error)
}

// NewID is a fresh C2 id: 8 characters of [a-z2-7], 40 random bits.
func NewID() string { return strings.ToLower(rand.Text()[:8]) }

// idFor is the run id of an idempotency key: a retried act renders the same
// claim name, so applying both claims creates one run. No key, a fresh id.
func idFor(key string) string {
	if key == "" {
		return NewID()
	}
	sum := sha256.Sum256([]byte(key))
	return strings.ToLower(base32.StdEncoding.EncodeToString(sum[:5]))
}

func task(r Request) map[string]string {
	if r.TaskURL != "" {
		return map[string]string{"url": r.TaskURL}
	}
	return map[string]string{"text": r.TaskText}
}

// Manifest renders the AgentRun claim for the owner to create (ruling P14), of Kind: its GVK and
// namespace, and the name prefix runwatch parses.
type Manifest struct {
	// Kind is the runs kind, required: the claim rendered is one of its runs.
	Kind runwatch.Kind
}

// Request renders r as a claim, its run id derived from r's idempotency key.
func (m Manifest) Request(_ context.Context, r Request) (Result, error) {
	if err := m.Kind.Validate(); err != nil {
		return Result{}, fmt.Errorf("runrequest: the manifest needs the runs kind: %w", err)
	}
	id := idFor(r.IdempotencyKey)
	spec := map[string]any{"role": r.Role, "repository": r.Repository, "baseRef": r.BaseRef, "branch": r.Branch,
		"principal": r.Principal, "dataClass": r.DataClass, "roomRef": r.RoomRef, "task": task(r)}
	if len(r.EgressProfiles) > 0 {
		spec["egress"] = map[string]any{"profiles": r.EgressProfiles}
	}
	gvk := m.Kind.GVK()
	b, err := json.Marshal(map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
		"metadata": map[string]string{"name": m.Kind.ClaimName(id), "namespace": m.Kind.Namespace}, "spec": spec})
	if err != nil {
		return Result{}, fmt.Errorf("runrequest: render: %w", err)
	}
	return Result{RunID: id, Via: "manifest", Manifest: b}, nil
}

// Factory is SP3's run API at URL. HC, when set, is the client (tests). Otherwise each call
// builds the audited egress client, trusting the PEM CA in CAFile, read at that call so a rotated
// CA needs no restart (S-1), or the system roots when CAFile is empty.
type Factory struct {
	URL    string
	CAFile string
	HC     *http.Client
}

// HTTPClient is f's HTTP client to the factory, trusting factoryCAFile: forge_read posts with it.
func (f Factory) HTTPClient() (*http.Client, error) { return f.client() }

// client is f's HTTP client for one call.
func (f Factory) client() (*http.Client, error) {
	if f.HC != nil {
		return f.HC, nil
	}
	if f.CAFile == "" {
		return httpx.New(factoryTimeout, nil), nil
	}
	raw, err := os.ReadFile(filepath.Clean(f.CAFile))
	if err != nil {
		return nil, fmt.Errorf("runrequest: factoryCAFile: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("runrequest: factoryCAFile %s holds no PEM certificate", f.CAFile)
	}
	return httpx.New(factoryTimeout, roots), nil
}

// Request posts r to the factory as the human it names, with their own token.
func (f Factory) Request(ctx context.Context, r Request) (Result, error) {
	if r.AccessToken == "" { // never an unauthenticated or asserted request (C4)
		return Result{}, ErrForbidden
	}
	if r.IdempotencyKey == "" { // a retry without one would create a second run
		return Result{}, ErrNoKey
	}
	body, err := json.Marshal(map[string]any{"role": r.Role, "repository": r.Repository, "baseRef": r.BaseRef,
		"task": task(r), "dataClass": r.DataClass, "roomRef": r.RoomRef, "egressProfiles": r.EgressProfiles})
	if err != nil {
		return Result{}, fmt.Errorf("runrequest: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL+"/v1/runs", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("runrequest: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	// SP3's contract: the same key answers the same runId, so a retried act
	// never creates a second run (docs/integration.md).
	req.Header.Set("Idempotency-Key", r.IdempotencyKey)
	hc, err := f.client()
	if err != nil {
		return Result{}, err
	}
	if f.HC == nil {
		defer hc.CloseIdleConnections() // a client per call: its connection goes with it
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("runrequest: factory: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated:
		raw, err := httpx.ReadBody(resp.Body, maxResponse)
		if err != nil {
			return Result{}, fmt.Errorf("runrequest: factory: %w", err)
		}
		var out Result
		if err := json.Unmarshal(raw, &out); err != nil {
			return Result{}, fmt.Errorf("runrequest: factory answer: %w", err)
		}
		return Result{RunID: out.RunID, Via: "factory"}, nil
	case http.StatusTooManyRequests:
		return Result{}, ErrBudget
	case http.StatusForbidden:
		return Result{}, ErrForbidden
	}
	return Result{}, fmt.Errorf("runrequest: factory: %s", resp.Status)
}
