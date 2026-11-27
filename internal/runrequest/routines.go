// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/httpx"
)

const maxRoutinesResponse = 1 << 20

var (
	routineCode   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
	routineRepoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

func validRoutineRepo(owner, name string) bool {
	if owner == "" || owner == "." || owner == ".." || name == "" || name == "." || name == ".." {
		return false
	}
	return routineRepoRE.MatchString(owner + "/" + name)
}

func validRoutineRepoString(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && !strings.Contains(name, "/") && validRoutineRepo(owner, name)
}

func validRoutineTarget(owner, name, routine string) bool {
	return validRoutineRepo(owner, name) && fconfig.ValidRoutineName(routine)
}

// ListRoutines forwards GET /v1/routines[?repository=owner/name] with the caller's token.
func (f Factory) ListRoutines(ctx context.Context, accessToken, repo string) (json.RawMessage, error) {
	if accessToken == "" {
		return nil, ErrForbidden
	}
	path := "/v1/routines"
	if repo != "" {
		if !validRoutineRepoString(repo) {
			return nil, fmt.Errorf("runrequest: invalid repository %q", repo)
		}
		path += "?repository=" + url.QueryEscape(repo)
	}
	_, raw, err := f.callRoutines(ctx, http.MethodGet, accessToken, path, "", nil)
	return raw, err
}

// GetRoutine forwards GET /v1/routines/{owner}/{name}/{routine} with the caller's token.
func (f Factory) GetRoutine(ctx context.Context, accessToken, owner, name, routine string) (json.RawMessage, error) {
	if accessToken == "" {
		return nil, ErrForbidden
	}
	if !validRoutineTarget(owner, name, routine) {
		return nil, fmt.Errorf("runrequest: invalid routine target")
	}
	path := "/v1/routines/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + url.PathEscape(routine)
	_, raw, err := f.callRoutines(ctx, http.MethodGet, accessToken, path, "", nil)
	return raw, err
}

// RunRoutine forwards POST /v1/routines/{owner}/{name}/{routine}/runs with the caller's token.
func (f Factory) RunRoutine(ctx context.Context, accessToken, owner, name, routine, idempotencyKey string, body json.RawMessage) (int, json.RawMessage, error) {
	if accessToken == "" {
		return 0, nil, ErrForbidden
	}
	if !validRoutineTarget(owner, name, routine) {
		return 0, nil, fmt.Errorf("runrequest: invalid routine target")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = json.RawMessage("{}")
	}
	path := "/v1/routines/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + url.PathEscape(routine) + "/runs"
	return f.callRoutines(ctx, http.MethodPost, accessToken, path, idempotencyKey, body)
}

// PauseRoutine forwards POST /v1/routines/{owner}/{name}/{routine}/pause with the caller's token.
func (f Factory) PauseRoutine(ctx context.Context, accessToken, owner, name, routine string, body json.RawMessage) (json.RawMessage, error) {
	if accessToken == "" {
		return nil, ErrForbidden
	}
	if !validRoutineTarget(owner, name, routine) {
		return nil, fmt.Errorf("runrequest: invalid routine target")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = json.RawMessage("{}")
	}
	path := "/v1/routines/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + url.PathEscape(routine) + "/pause"
	_, raw, err := f.callRoutines(ctx, http.MethodPost, accessToken, path, "", body)
	return raw, err
}

// ResumeRoutine forwards POST /v1/routines/{owner}/{name}/{routine}/resume with the caller's token.
func (f Factory) ResumeRoutine(ctx context.Context, accessToken, owner, name, routine string, body json.RawMessage) (json.RawMessage, error) {
	if accessToken == "" {
		return nil, ErrForbidden
	}
	if !validRoutineTarget(owner, name, routine) {
		return nil, fmt.Errorf("runrequest: invalid routine target")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = json.RawMessage("{}")
	}
	path := "/v1/routines/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + url.PathEscape(routine) + "/resume"
	_, raw, err := f.callRoutines(ctx, http.MethodPost, accessToken, path, "", body)
	return raw, err
}

func (f Factory) callRoutines(ctx context.Context, method, accessToken, path, idempotencyKey string, body json.RawMessage) (int, json.RawMessage, error) {
	if accessToken == "" {
		return 0, nil, ErrForbidden
	}
	var reqBody io.Reader = http.NoBody
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.URL+path, reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("runrequest: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	hc, err := f.client()
	if err != nil {
		return 0, nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("runrequest: factory: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxRoutinesResponse)
	if err != nil {
		return 0, nil, fmt.Errorf("runrequest: factory: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		trimmed := bytes.TrimSpace(raw)
		if !json.Valid(trimmed) {
			return 0, nil, fmt.Errorf("runrequest: factory routines answer: invalid json")
		}
		return resp.StatusCode, trimmed, nil
	case http.StatusNoContent:
		return http.StatusNoContent, nil, nil
	}
	var refusal struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &refusal)
	reason := "refused"
	if routineCode.MatchString(refusal.Error) {
		reason = refusal.Error
	}
	return 0, nil, ErrRefused{Status: resp.StatusCode, Reason: reason}
}
