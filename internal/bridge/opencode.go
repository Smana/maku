// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Smana/maku/internal/httpx"
)

// Default files exchanged on /workspace between opencode_driver and room-bridge.
const (
	DefaultOpenCodeSessionFile  = "/workspace/.agent-opencode-session-id"
	DefaultOpenCodePasswordFile = "/workspace/.agent-opencode-password" //nolint:gosec // file path, not a credential
	DefaultOpenCodeTerminalFile = "/workspace/.agent-opencode-terminal.json"

	maxSessionFileBytes  = 256
	maxTerminalFileBytes = 4096
)

// OpenCodeHarness speaks OpenCode 2.3.0's loopback HTTP API (/api/...) and
// implements HarnessAPI so Bridge, Confirmer and Steering stay shared.
type OpenCodeHarness struct {
	base         string
	sessionFile  string
	passwordFile string
	terminalFile string
	hc           *http.Client
	maxBody      int64

	mu       sync.Mutex
	password string
}

var _ HarnessAPI = (*OpenCodeHarness)(nil)

// NewOpenCodeHarness returns an OpenCode 2.3.0 loopback adapter reading the
// session ID from sessionFile and the server password / terminal marker from
// sibling files in the same directory.
func NewOpenCodeHarness(base, sessionFile string) *OpenCodeHarness {
	dir := filepath.Dir(sessionFile)
	return NewOpenCodeHarnessWithFiles(
		base,
		sessionFile,
		filepath.Join(dir, ".agent-opencode-password"),
		filepath.Join(dir, ".agent-opencode-terminal.json"),
	)
}

// NewOpenCodeHarnessWithFiles constructs an OpenCodeHarness with explicit file
// paths for session ID, server password, and terminal stuck marker.
func NewOpenCodeHarnessWithFiles(base, sessionFile, passwordFile, terminalFile string) *OpenCodeHarness {
	return &OpenCodeHarness{
		base:         strings.TrimRight(base, "/"),
		sessionFile:  sessionFile,
		passwordFile: passwordFile,
		terminalFile: terminalFile,
		hc:           httpx.New(harnessTimeout, nil),
		maxBody:      maxResponseBytes,
	}
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // caller-configured pod workspace file path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

func (o *OpenCodeHarness) sessionID() (string, error) {
	b, err := readBoundedFile(o.sessionFile, maxSessionFileBytes)
	if err != nil {
		return "", &StatusError{Method: http.MethodGet, Path: "/api/session", Code: http.StatusNotFound}
	}
	sid := strings.TrimSpace(string(b))
	if !isValidSessionID(sid) {
		return "", &StatusError{Method: http.MethodGet, Path: "/api/session", Code: http.StatusNotFound}
	}
	return sid, nil
}

func (o *OpenCodeHarness) authHeader(refresh bool) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if refresh || o.password == "" {
		if b, err := readBoundedFile(o.passwordFile, maxSessionFileBytes); err == nil {
			o.password = strings.TrimSpace(string(b))
		}
	}
	if o.password == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+o.password))
}

func (o *OpenCodeHarness) do(ctx context.Context, method, path string, in, out any) error {
	var bodyBytes []byte
	if in != nil {
		var err error
		bodyBytes, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("opencode %s %s: %w", method, path, err)
		}
	}
	for attempt := range 2 {
		var body io.Reader
		if bodyBytes != nil {
			body = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, o.base+path, body)
		if err != nil {
			return fmt.Errorf("opencode %s %s: %w", method, path, err)
		}
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if auth := o.authHeader(attempt > 0); auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := o.hc.Do(req)
		if err != nil {
			return fmt.Errorf("opencode %s %s: %w", method, path, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_ = resp.Body.Close()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			_ = resp.Body.Close()
			return &StatusError{Method: method, Path: path, Code: resp.StatusCode}
		}
		b, err := httpx.ReadBody(resp.Body, o.maxBody)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("opencode %s %s: %w", method, path, err)
		}
		if out == nil || len(b) == 0 {
			return nil
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("opencode %s %s: %w", method, path, err)
		}
		return nil
	}
	return &StatusError{Method: method, Path: path, Code: http.StatusUnauthorized}
}

func (o *OpenCodeHarness) readStuckMarker() bool {
	if o.terminalFile == "" {
		return false
	}
	b, err := readBoundedFile(o.terminalFile, maxTerminalFileBytes)
	if err != nil {
		return false
	}
	var marker struct {
		Reason  string `json:"reason"`
		Pattern string `json:"pattern"`
	}
	if json.Unmarshal(b, &marker) != nil {
		return false
	}
	return marker.Reason == "stuck" && validStuckPatterns[marker.Pattern]
}

// Ready succeeds once opencode_driver wrote a valid session ID and the session
// answers GET /api/session/{id}.
func (o *OpenCodeHarness) Ready(ctx context.Context) error {
	sid, err := o.sessionID()
	if err != nil {
		return err
	}
	return o.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sid), nil, nil)
}

// Conversation returns the normalized status and reasoning effort. A valid stuck
// terminal marker takes precedence over interrupted/paused/idle/running.
func (o *OpenCodeHarness) Conversation(ctx context.Context) (Conversation, error) {
	if o.readStuckMarker() {
		return Conversation{Status: "stuck"}, nil
	}
	sid, err := o.sessionID()
	if err != nil {
		return Conversation{}, err
	}
	esc := url.PathEscape(sid)

	var perms struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := o.do(ctx, http.MethodGet, "/api/session/"+esc+"/permission", nil, &perms); err != nil {
		return Conversation{}, err
	}
	var info struct {
		Data struct {
			Outcome string `json:"outcome"`
			Model   struct {
				Variant string `json:"variant"`
			} `json:"model"`
		} `json:"data"`
	}
	if err := o.do(ctx, http.MethodGet, "/api/session/"+esc, nil, &info); err != nil {
		return Conversation{}, err
	}
	effort := info.Data.Model.Variant
	if len(perms.Data) > 0 {
		return Conversation{Status: statusWaiting, Effort: effort}, nil
	}

	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := o.do(ctx, http.MethodGet, "/api/session/active", nil, &active); err != nil {
		return Conversation{}, err
	}
	if _, running := active.Data[sid]; running {
		return Conversation{Status: statusRunning, Effort: effort}, nil
	}

	switch info.Data.Outcome {
	case "succeeded":
		return Conversation{Status: "finished", Effort: effort}, nil
	case "failed":
		return Conversation{Status: "error", Effort: effort}, nil
	case "interrupted":
		return Conversation{Status: statusPaused, Effort: effort}, nil
	default:
		return Conversation{Status: statusIdle, Effort: effort}, nil
	}
}

func (o *OpenCodeHarness) fetchAllEvents(ctx context.Context, sid string) ([]RawEvent, error) {
	esc := url.PathEscape(sid)
	var all []RawEvent
	seenCursors := map[string]bool{}
	cursor := ""
	for {
		q := url.Values{"limit": {"100"}, "order": {"asc"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Data   []json.RawMessage `json:"data"`
			Cursor struct {
				Next string `json:"next"`
			} `json:"cursor"`
		}
		if err := o.do(ctx, http.MethodGet, "/api/session/"+esc+"/message?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, rawMsg := range page.Data {
			all = append(all, normalizeOpenCode(rawMsg)...)
		}
		next := page.Cursor.Next
		if next == "" || len(page.Data) == 0 || seenCursors[next] {
			break
		}
		seenCursors[next] = true
		cursor = next
	}
	return all, nil
}

// Next returns the normalized events after c.
func (o *OpenCodeHarness) Next(ctx context.Context, c Cursor) ([]RawEvent, Cursor, error) {
	evs, next, _, err := o.next(ctx, c)
	return evs, next, err
}

func (o *OpenCodeHarness) next(ctx context.Context, c Cursor) ([]RawEvent, Cursor, bool, error) {
	sid, err := o.sessionID()
	if err != nil {
		return nil, c, false, err
	}
	all, err := o.fetchAllEvents(ctx, sid)
	if err != nil {
		return nil, c, false, err
	}

	start := 0
	if c.LastID != "" {
		found := -1
		for i, e := range all {
			if !e.Malformed && e.ID == c.LastID {
				found = i
				break
			}
		}
		if found < 0 {
			return nil, c, false, ErrCursorLost
		}
		start = found + 1
	}
	if c.Unkeyed > 0 {
		if start+int(c.Unkeyed) > len(all) {
			return nil, c, false, ErrCursorLost
		}
		for i := range int(c.Unkeyed) {
			if !all[start+i].Malformed {
				return nil, c, false, ErrCursorLost
			}
		}
		start += int(c.Unkeyed)
	}

	seen := map[string]bool{}
	for i := range start {
		if !all[i].Malformed {
			seen[all[i].ID] = true
		}
	}
	var out []RawEvent
	for _, e := range all[start:] {
		if !e.Malformed && seen[e.ID] {
			continue
		}
		out = append(out, e)
		c.Count++
		if e.Malformed {
			c.Unkeyed++
			continue
		}
		c.LastID = e.ID
		c.Unkeyed = 0
		seen[e.ID] = true
	}
	return out, c, true, nil
}

// Skip positions a fresh cursor after the first n normalized events.
func (o *OpenCodeHarness) Skip(ctx context.Context, n int64) (Cursor, error) {
	return o.skip(ctx, n, func(RawEvent) {})
}

func (o *OpenCodeHarness) skip(ctx context.Context, n int64, take func(RawEvent)) (Cursor, error) {
	if n <= 0 {
		return Cursor{}, nil
	}
	sid, err := o.sessionID()
	if err != nil {
		return Cursor{}, err
	}
	all, err := o.fetchAllEvents(ctx, sid)
	if err != nil {
		return Cursor{}, err
	}
	var c Cursor
	seen := map[string]bool{}
	for _, e := range all {
		if c.Count >= n {
			break
		}
		if !e.Malformed && seen[e.ID] {
			continue
		}
		take(e)
		c.Count++
		if e.Malformed {
			c.Unkeyed++
			continue
		}
		c.LastID = e.ID
		c.Unkeyed = 0
		seen[e.ID] = true
	}
	return c, nil
}

// Send appends a user message and starts a turn.
func (o *OpenCodeHarness) Send(ctx context.Context, text string) error {
	return o.Message(ctx, text, true)
}

// Message sends text through POST /api/session/{id}/prompt; when run is false,
// it queues the message rather than interrupting or implicitly approving.
func (o *OpenCodeHarness) Message(ctx context.Context, text string, run bool) error {
	sid, err := o.sessionID()
	if err != nil {
		return err
	}
	body := map[string]any{"text": text}
	if !run {
		body["delivery"] = "queue"
	}
	return o.do(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sid)+"/prompt", body, nil)
}

// Respond answers every currently pending permission request on the session
// with "once" (never "always") or "reject".
func (o *OpenCodeHarness) Respond(ctx context.Context, accept bool, reason string) error {
	sid, err := o.sessionID()
	if err != nil {
		return err
	}
	esc := url.PathEscape(sid)
	var perms struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := o.do(ctx, http.MethodGet, "/api/session/"+esc+"/permission", nil, &perms); err != nil {
		return err
	}
	decision := "reject"
	if accept {
		decision = "once"
	}
	for _, p := range perms.Data {
		if p.ID == "" {
			continue
		}
		body := map[string]any{"decision": decision}
		if !accept && reason != "" {
			body["message"] = reason
		}
		path := "/api/session/" + esc + "/permission/" + url.PathEscape(p.ID) + "/reply"
		if err := o.do(ctx, http.MethodPost, path, body, nil); err != nil {
			return err
		}
	}
	return nil
}

// Run checks whether the session is already running (returning 409 like
// OpenHands) and otherwise returns nil: OpenCode automatically resumes after
// a permission reply or queued prompt.
func (o *OpenCodeHarness) Run(ctx context.Context) error {
	sid, err := o.sessionID()
	if err != nil {
		return err
	}
	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := o.do(ctx, http.MethodGet, "/api/session/active", nil, &active); err != nil {
		return err
	}
	if _, running := active.Data[sid]; running {
		return &StatusError{Method: http.MethodPost, Path: "/run", Code: http.StatusConflict}
	}
	return nil
}

// Interrupt interrupts the OpenCode session and stops every running shell or
// subagent job owned by it.
func (o *OpenCodeHarness) Interrupt(ctx context.Context) error {
	sid, err := o.sessionID()
	if err != nil {
		return err
	}
	esc := url.PathEscape(sid)
	if err := o.do(ctx, http.MethodPost, "/api/session/"+esc+"/interrupt", nil, nil); err != nil {
		return err
	}
	var jobs struct {
		Data []struct {
			Kind           string `json:"kind"`
			Status         string `json:"status"`
			ShellID        string `json:"shellID"`
			ChildSessionID string `json:"childSessionID"`
		} `json:"data"`
	}
	if err := o.do(ctx, http.MethodGet, "/api/experimental/job?sessionID="+url.QueryEscape(sid)+"&descendants=true", nil, &jobs); err != nil {
		return nil // best effort job cleanup after session interrupt succeeded
	}
	for _, j := range jobs.Data {
		if j.Status != "running" && j.Status != "pending" && j.Status != "queued" {
			continue
		}
		switch j.Kind {
		case "shell":
			if j.ShellID != "" {
				_ = o.do(ctx, http.MethodDelete, "/api/shell/"+url.PathEscape(j.ShellID), nil, nil)
			}
		case "subagent":
			if j.ChildSessionID != "" {
				_ = o.do(ctx, http.MethodPost, "/api/session/"+url.PathEscape(j.ChildSessionID)+"/interrupt", nil, nil)
			}
		}
	}
	return nil
}

// AlwaysConfirm verifies the OpenCode session exists and accepts policy checks
// without granting any permission (opencode_driver generates the role's
// ask/deny ruleset before session start).
func (o *OpenCodeHarness) AlwaysConfirm(ctx context.Context) error {
	return o.Ready(ctx)
}
