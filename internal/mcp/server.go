// SPDX-License-Identifier: Apache-2.0

// Package mcp is the room's MCP port, :8090 (§3). It is reachable only from the
// agent-router data plane (CNP), which authenticates the run first (MCPRoute
// oauth), injects X-Room-Mcp-Key, and sets X-Ar-Agent from the verified token.
// Where no gateway stands in front (roomTools.auth tokenreview, the demo), the
// server verifies the run's own bearer token itself instead. The run comes from
// that subject and its room and role from the AgentRun, never from a header or
// an argument. Tools only: Agent Router authorizes tools/call and
// tools/list and nothing else, so resources and prompts must not exist (§3). The
// server never sends a request of its own (agent-router#2715 drops
// server-to-client pings), so it answers POST only and opens no stream.
package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/version"
)

// The headers agent-router sets on every request it forwards: the key the
// MCPRoute injects (ruling P13) and the verified token's sub (C5).
const (
	KeyHeader      = "X-Room-Mcp-Key"
	IdentityHeader = "X-Ar-Agent"
	// HarnessHeader is set by the run harness's own MCP client, never by the model's (whose client the
	// harness configures without it): a HarnessOnly tool is listed and callable only with it. It is a
	// marker, not a secret: a command the model runs could send it if it can reach the route with the
	// run's credentials.
	HarnessHeader = "X-Agent-Harness"
)

const (
	// maxBody bounds a request. A 16 KiB text escaped by a client that writes
	// every '<' as < is 96 KiB of JSON, so the cap sits above that.
	maxBody = 128 << 10
	// maxHarnessBody bounds a request with HarnessHeader (HR22): an 8 MiB bundle is 11.2 MiB of
	// base64. Past maxBody, only output_push is accepted.
	maxHarnessBody = 12 << 20
	// callEvery is §4's room_* rate: one tool call a second per run.
	callEvery = time.Second
	// maxLimited bounds the limiter's memory. A run's last call over callEvery
	// ago is the same as none, so reaching the cap sweeps those.
	maxLimited = 4096
)

// protocols are the MCP revisions served; an unknown request gets the newest.
var protocols = []string{"2025-06-18", "2025-11-25", "2026-07-28"}

// Caller is the verified run a tool acts for.
type Caller struct{ Run runwatch.Run }

// Tool is one MCP tool. Action is the policy matrix's action the call needs
// (T7), and Roles the AgentRun roles that list and may call it. A tool with no
// Action is never callable: the matrix allows an agent nothing unnamed.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Action      policy.Action
	Roles       []string
	// HarnessOnly hides the tool from the model's list and refuses its call: the harness posts it.
	HarnessOnly bool
	// RoleSchemas replaces InputSchema in tools/list for the roles it names: a property one role
	// alone may pass is shown to that role alone.
	RoleSchemas map[string]json.RawMessage
	// Gate, when set, must also accept the run for the tool to be listed or called: a safe-output
	// tool is served only to a run whose claim names its type (hardening HR14).
	Gate func(runwatch.Run) bool
	// OnRefused, when set, hears a call of this tool the run may not make: the knowledge tools count
	// it as refused (EX11).
	OnRefused func(ctx context.Context)
	Call      func(ctx context.Context, c Caller, args json.RawMessage) (any, error)
}

// servedTo reports whether run lists and may call t.
func (t Tool) servedTo(run runwatch.Run, harness bool) bool {
	return slices.Contains(t.Roles, run.Role) && (!t.HarnessOnly || harness) && (t.Gate == nil || t.Gate(run))
}

// Runs is the liveness check on every request; *runwatch.Watcher implements it.
type Runs interface {
	Live(id string) (run runwatch.Run, live, known bool)
}

// Server is the :8090 handler. Runs and SubPatterns (each with one capture
// group: the run id) are required, and Key unless Tokens is set. OnReject counts
// a refusal by a bounded reason.
type Server struct {
	Key func() string
	// Tokens, when set, is roomTools.auth tokenreview: the caller's own bearer token, verified for
	// authn.AudienceRoomTools, names the subject, and Key and the gateway's headers are never read.
	Tokens      authn.RunVerifier
	Runs        Runs
	SubPatterns []*regexp.Regexp
	Tools       []Tool
	OnReject    func(ctx context.Context, reason string)
	Logger      *slog.Logger
	Now         func() time.Time

	mu       sync.Mutex
	lastCall map[string]time.Time
}

// argError is a tool's refusal of its arguments, shown to the model as is.
type argError string

func (e argError) Error() string { return string(e) }

// rateError is a tool's own limit, shown to the model as is, beside the server's one call a second.
type rateError string

func (e rateError) Error() string { return string(e) }

func (s *Server) reject(ctx context.Context, reason string) {
	if s.OnReject != nil {
		s.OnReject(ctx, reason)
	}
}

func (s *Server) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// allow spends runID's call for this second, if it has one.
func (s *Server) allow(runID string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastCall == nil {
		s.lastCall = map[string]time.Time{}
	}
	if last, ok := s.lastCall[runID]; ok && now.Sub(last) < callEvery {
		return false
	}
	if len(s.lastCall) >= maxLimited {
		for id, last := range s.lastCall {
			if now.Sub(last) >= callEvery {
				delete(s.lastCall, id)
			}
		}
	}
	if len(s.lastCall) >= maxLimited {
		return false // maxLimited runs each called within the second: fail closed
	}
	s.lastCall[runID] = now
	return true
}

// caller maps the request's credential to a live run, or answers the refusal.
func (s *Server) caller(w http.ResponseWriter, r *http.Request) (runwatch.Run, bool) {
	subject, ok := s.subject(r)
	if !ok {
		s.reject(r.Context(), "mcp_key")
		w.WriteHeader(http.StatusUnauthorized)
		return runwatch.Run{}, false
	}
	id := ""
	if subject != "" {
		id = s.runID(subject)
	}
	if id == "" {
		s.reject(r.Context(), "mcp_identity")
		w.WriteHeader(http.StatusForbidden)
		return runwatch.Run{}, false
	}
	run, live, _ := s.Runs.Live(id)
	if !live || !envelope.ValidID(run.Room) {
		s.reject(r.Context(), "run_not_live")
		w.WriteHeader(http.StatusForbidden)
		return runwatch.Run{}, false
	}
	return run, true
}

// subject is the caller's verified subject: false when the credential is refused, "" when it is
// accepted but names no single subject. Key mode: the router's key, then its one X-Ar-Agent. Token
// mode: the bearer token's subject, whatever the headers say.
func (s *Server) subject(r *http.Request) (string, bool) {
	if s.Tokens != nil {
		raw, err := authn.Bearer(r)
		if err != nil {
			return "", false
		}
		c, err := s.Tokens.Verify(r.Context(), raw, authn.AudienceRoomTools)
		if err != nil {
			if errors.Is(err, authn.ErrReviewUnavailable) { // not the caller's fault: say so apart
				s.log().Warn("room tools token review failed", "err", err.Error())
			}
			return "", false
		}
		return c.Subject, true
	}
	key, keys := s.Key(), r.Header.Values(KeyHeader)
	// An unset key must not admit a request that sends none.
	if key == "" || len(keys) != 1 || subtle.ConstantTimeCompare([]byte(keys[0]), []byte(key)) != 1 {
		return "", false
	}
	// Exactly one subject: a second value, forged beside the gateway's, is
	// refused rather than guessed between (review M1).
	if subjects := r.Header.Values(IdentityHeader); len(subjects) == 1 {
		return subjects[0], true
	}
	return "", true
}

// runID is the run the subject names under the first issuer pattern that
// matches it whole, or "". m[0] == the subject: a pattern missing its anchors
// must not accept a subject that merely contains a run's name (as authn.Runs).
func (s *Server) runID(subject string) string {
	for _, p := range s.SubPatterns {
		if m := p.FindStringSubmatch(subject); len(m) == 2 && m[0] == subject && envelope.ValidID(m[1]) {
			return m[1]
		}
	}
	return ""
}

// ServeHTTP answers one JSON-RPC request: initialize, ping, tools/list and
// tools/call. Every other method is -32601.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	run, ok := s.caller(w, r)
	if !ok {
		return
	}
	harness := r.Header.Get(HarnessHeader) == "1"
	limit := int64(maxBody)
	if harness {
		limit = maxHarnessBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	req, status, rerr := mcpwire.Decode(bytes.NewReader(body))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge), err == nil && rerr == nil && len(body) > maxBody && !harnessUpload(req):
		// HR22: past 128 KiB, only the harness's output_push; anything else as today.
		status, rerr = http.StatusRequestEntityTooLarge, &mcpwire.Error{Code: mcpwire.InvalidRequest, Message: "request too large"}
	case err != nil:
		status, rerr = http.StatusOK, &mcpwire.Error{Code: mcpwire.ParseError, Message: "parse error"}
	}
	if rerr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		mcpwire.Write(w, nil, nil, rerr)
		return
	}
	if len(req.ID) == 0 { // a notification: accepted, never answered
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := protocols[len(protocols)-1]
		if slices.Contains(protocols, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		mcpwire.Write(w, req.ID, map[string]any{"protocolVersion": v,
			"capabilities": map[string]any{"tools": map[string]bool{"listChanged": false}},
			"serverInfo":   map[string]string{"name": "maku-broker", "version": version.Version}}, nil)
	case "ping":
		mcpwire.Write(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		tools := []map[string]any{}
		for _, t := range s.Tools {
			if t.servedTo(run, harness) {
				schema := t.InputSchema
				if rs, ok := t.RoleSchemas[run.Role]; ok {
					schema = rs
				}
				tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
			}
		}
		mcpwire.Write(w, req.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		s.call(r.Context(), w, req, run, harness)
	default:
		mcpwire.Write(w, req.ID, nil, &mcpwire.Error{Code: mcpwire.MethodNotFound, Message: "the room MCP server exposes tools only"})
	}
}

// harnessUpload reports whether req is a tools/call of output_push, the one call a harness request
// may make past maxBody (HR22). Only a request with HarnessHeader is read that far.
func harnessUpload(req mcpwire.Request) bool {
	var p struct {
		Name string `json:"name"`
	}
	return req.Method == "tools/call" && json.Unmarshal(req.Params, &p) == nil && p.Name == "output_push"
}

func (s *Server) call(ctx context.Context, w http.ResponseWriter, req mcpwire.Request, run runwatch.Run, harness bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) == 0 || json.Unmarshal(req.Params, &p) != nil || p.Name == "" {
		mcpwire.Write(w, req.ID, nil, &mcpwire.Error{Code: mcpwire.InvalidParams, Message: "tools/call takes {name, arguments}"})
		return
	}
	// An unknown tool and one the role lacks answer alike.
	i := slices.IndexFunc(s.Tools, func(t Tool) bool { return t.Name == p.Name })
	agent := policy.Subject{Kind: envelope.ActorAgent, ID: "agent:" + run.ID}
	if i < 0 || !s.Tools[i].servedTo(run, harness) || !policy.Allowed(agent, s.Tools[i].Action) {
		s.reject(ctx, "tool_not_permitted")
		if i >= 0 && s.Tools[i].OnRefused != nil {
			s.Tools[i].OnRefused(ctx)
		}
		mcpwire.Write(w, req.ID, mcpwire.ToolError("not_permitted: this tool is not one of the "+run.Role+" role's"), nil)
		return
	}
	if !s.allow(run.ID) {
		s.reject(ctx, "rate_limited")
		mcpwire.Write(w, req.ID, mcpwire.ToolError("rate_limited: one room tool call per second"), nil)
		return
	}
	t := s.Tools[i]
	out, err := t.Call(ctx, Caller{Run: run}, p.Arguments)
	if err != nil {
		mcpwire.Write(w, req.ID, mcpwire.ToolError(s.failure(ctx, t.Name, run, err)), nil)
		return
	}
	res, err := mcpwire.ToolResult(out)
	if err != nil {
		mcpwire.Write(w, req.ID, mcpwire.ToolError(s.failure(ctx, t.Name, run, err)), nil)
		return
	}
	mcpwire.Write(w, req.ID, res, nil)
}

// failure maps a tool's error to what the model is told, in one place. Only an
// argument refusal is quoted: a database error's text can echo a value.
func (s *Server) failure(ctx context.Context, tool string, run runwatch.Run, err error) string {
	var bad argError
	var slow rateError
	var known knowledgeError
	switch {
	case errors.As(err, &known): // the broker's own words, the endpoint's message cut short
		s.log().Warn("knowledge call failed", "tool", tool, "room", run.Room, "run", run.ID, "reason", known.Error())
		return known.Error()
	case errors.As(err, &bad):
		s.reject(ctx, "invalid_arguments")
		return "invalid_arguments: " + bad.Error()
	case errors.As(err, &slow):
		s.reject(ctx, "rate_limited")
		return "rate_limited: " + slow.Error()
	case errors.Is(err, store.ErrSealed):
		return "room_sealed: the room is closed to new events"
	case errors.Is(err, store.ErrNoRoom):
		return "no_room: the run's room has no log"
	}
	s.log().Error("room tool failed", "tool", tool, "room", run.Room, "run", run.ID, errAttr(err))
	return "log_unavailable: try again later"
}

// errAttr names a failure for the log. A database error's text can quote the
// value it refused, a payload, so only its SQLSTATE is logged.
func errAttr(err error) slog.Attr {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return slog.String("sqlstate", pg.Code)
	}
	return slog.String("err", err.Error())
}
