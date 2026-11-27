// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/httpx"
)

// RoutineSparklineDay holds one UTC day's routine outcome counts.
type RoutineSparklineDay struct {
	Date      string `json:"date"`
	Merged    int    `json:"merged"`
	Closed    int    `json:"closed"`
	NoOp      int    `json:"noOp"`
	Escalated int    `json:"escalated"`
}

// RoutineOverview summarizes one configured routine binding from GET /api/routines.
type RoutineOverview struct {
	Repository        string                `json:"repository"`
	Routine           string                `json:"routine"`
	Description       string                `json:"description,omitempty"`
	Mode              string                `json:"mode"`
	Workflow          string                `json:"workflow,omitempty"`
	ProbeKind         string                `json:"probeKind,omitempty"`
	Cron              string                `json:"cron,omitempty"`
	Paused            bool                  `json:"paused"`
	DeclarativePaused bool                  `json:"declarativePaused,omitempty"`
	PausedReason      string                `json:"pausedReason,omitempty"`
	PausedBy          string                `json:"pausedBy,omitempty"`
	PausedAt          *time.Time            `json:"pausedAt,omitempty"`
	MaxOpenPRs        int                   `json:"maxOpenPRs"`
	MaxCostUSD        float64               `json:"maxCostUSD"`
	OpenCount         int                   `json:"openCount"`
	NextRunAt         *time.Time            `json:"nextRunAt"`
	Sparkline         []RoutineSparklineDay `json:"sparkline"`
	RollingMerged     int                   `json:"rollingMerged"`
	RollingClosed     int                   `json:"rollingClosed"`
	RollingNoOp       int                   `json:"rollingNoOp"`
	MergeRate         *float64              `json:"mergeRate"`
	USDPerMerged      *float64              `json:"usdPerMerged"`
	PromptEditURL     string                `json:"promptEditURL"`
	PromptSHA         string                `json:"promptSHA,omitempty"`
	Skill             string                `json:"skill,omitempty"`
	BreakerState      string                `json:"breakerState"`
	CanWrite          bool                  `json:"canWrite"`
}

// RoutineTaskRun summarizes one recent Task created for a routine.
type RoutineTaskRun struct {
	Name         string     `json:"name"`
	CreatedAt    time.Time  `json:"createdAt"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
	Phase        string     `json:"phase"`
	Reason       string     `json:"reason,omitempty"`
	RoomRef      string     `json:"roomRef,omitempty"`
	PRURL        string     `json:"prURL,omitempty"`
	PRNumber     int        `json:"prNumber,omitempty"`
	PromptSHA    string     `json:"promptSHA,omitempty"`
	PromptSource string     `json:"promptSource,omitempty"`
	RequestedBy  string     `json:"requestedBy,omitempty"`
	CostUSD      float64    `json:"costUSD"`
	Outcome      string     `json:"outcome,omitempty"`
}

// RoutineSkipRecord is one skipped tick diagnostic entry stored for Run History.
type RoutineSkipRecord struct {
	SlotKey string    `json:"slotKey,omitempty"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
}

// RoutineList is GET /api/routines' answer, retaining raw broker JSON for --json.
type RoutineList struct {
	Routines []RoutineOverview `json:"routines"`
	raw      json.RawMessage
}

// Raw is the broker's answer as sent, for --json output.
func (l RoutineList) Raw() json.RawMessage { return l.raw }

// RoutineDetail is GET /api/routines/{owner}/{name}/{routine}'s answer, retaining raw JSON.
type RoutineDetail struct {
	RoutineOverview
	DefaultPrompt string              `json:"defaultPrompt,omitempty"`
	Runs          []RoutineTaskRun    `json:"runs"`
	Skips         []RoutineSkipRecord `json:"skips"`
	raw           json.RawMessage
}

// Raw is the broker's answer as sent, for --json output.
func (d RoutineDetail) Raw() json.RawMessage { return d.raw }

// RoutineRunResult is POST /api/routines/{owner}/{name}/{routine}/runs' answer.
type RoutineRunResult struct {
	Task        string `json:"task"`
	Room        string `json:"room"`
	Repository  string `json:"repository"`
	Routine     string `json:"routine"`
	Mode        string `json:"mode"`
	RequestedBy string `json:"requestedBy"`
	raw         json.RawMessage
}

// Raw is the broker's answer as sent, for --json output.
func (r RoutineRunResult) Raw() json.RawMessage { return r.raw }

// RoutinePauseResult is POST /api/routines/{owner}/{name}/{routine}/pause's answer.
type RoutinePauseResult struct {
	Repository        string `json:"repository"`
	Routine           string `json:"routine"`
	Paused            bool   `json:"paused"`
	DeclarativePaused bool   `json:"declarativePaused"`
	PausedReason      string `json:"pausedReason"`
	PausedBy          string `json:"pausedBy"`
	raw               json.RawMessage
}

// Raw is the broker's answer as sent, for --json output.
func (r RoutinePauseResult) Raw() json.RawMessage { return r.raw }

// RoutineResumeResult is POST /api/routines/{owner}/{name}/{routine}/resume's answer.
type RoutineResumeResult struct {
	Repository        string `json:"repository"`
	Routine           string `json:"routine"`
	Paused            bool   `json:"paused"`
	DeclarativePaused bool   `json:"declarativePaused"`
	raw               json.RawMessage
}

// Raw is the broker's answer as sent, for --json output.
func (r RoutineResumeResult) Raw() json.RawMessage { return r.raw }

func validateRoutineTarget(repo, name string) error {
	if !ValidRepository(repo) {
		return fmt.Errorf("invalid repository %q (expected owner/name)", repo)
	}
	if !fconfig.ValidRoutineName(name) {
		return fmt.Errorf("invalid routine name %q", name)
	}
	return nil
}

func routineAPIPath(repo, name string, suffix ...string) string {
	owner, repoName, _ := strings.Cut(repo, "/")
	p := "/api/routines/" + url.PathEscape(owner) + "/" + url.PathEscape(repoName) + "/" + url.PathEscape(name)
	for _, s := range suffix {
		p += "/" + s
	}
	return p
}

// ListRoutines reads GET /api/routines[?repo=owner/name] from the broker.
func (c Client) ListRoutines(ctx context.Context, repo string) (RoutineList, error) {
	var out RoutineList
	if repo != "" && !ValidRepository(repo) {
		return out, fmt.Errorf("routine list: invalid repository %q (expected owner/name)", repo)
	}
	h, err := c.header(ctx)
	if err != nil {
		return out, err
	}
	u := c.URL + "/api/routines"
	if repo != "" {
		u += "?" + url.Values{"repo": {repo}}.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return out, fmt.Errorf("routine list: %w", err)
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return out, fmt.Errorf("routine list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if repo != "" {
			return out, c.repoRefusal("routine list", repo, resp)
		}
		return out, c.refused("routine list", resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return out, fmt.Errorf("routine list: %w", err)
	}
	out.raw = bytes.TrimSpace(raw)
	if err := json.Unmarshal(raw, &out); err != nil {
		return RoutineList{}, fmt.Errorf("routine list: %w", err)
	}
	return out, nil
}

// GetRoutine reads GET /api/routines/{owner}/{name}/{routine} from the broker.
func (c Client) GetRoutine(ctx context.Context, repo, name string) (RoutineDetail, error) {
	var out RoutineDetail
	if err := validateRoutineTarget(repo, name); err != nil {
		return out, fmt.Errorf("routine status: %w", err)
	}
	h, err := c.header(ctx)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+routineAPIPath(repo, name), nil)
	if err != nil {
		return out, fmt.Errorf("routine status: %w", err)
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return out, fmt.Errorf("routine status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, c.repoRefusal("routine status", repo, resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return out, fmt.Errorf("routine status: %w", err)
	}
	out.raw = bytes.TrimSpace(raw)
	if err := json.Unmarshal(raw, &out); err != nil {
		return RoutineDetail{}, fmt.Errorf("routine status: %w", err)
	}
	return out, nil
}

// RunRoutine triggers a manual routine run via POST /api/routines/{owner}/{name}/{routine}/runs.
// If idempotencyKey is empty, a random 32-hex-character key is generated and sent in both the
// JSON body and the Idempotency-Key header.
func (c Client) RunRoutine(ctx context.Context, repo, name, mode, idempotencyKey string) (RoutineRunResult, error) {
	var out RoutineRunResult
	if err := validateRoutineTarget(repo, name); err != nil {
		return out, fmt.Errorf("routine run: %w", err)
	}
	if idempotencyKey == "" {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return out, fmt.Errorf("routine run: generate idempotency key: %w", err)
		}
		idempotencyKey = hex.EncodeToString(buf[:])
	}
	payload := struct {
		Mode           string `json:"mode,omitempty"`
		IdempotencyKey string `json:"idempotencyKey"`
	}{
		Mode:           mode,
		IdempotencyKey: idempotencyKey,
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return out, fmt.Errorf("routine run: %w", err)
	}
	h, err := c.header(ctx)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+routineAPIPath(repo, name, "runs"), bytes.NewReader(rawBody))
	if err != nil {
		return out, fmt.Errorf("routine run: %w", err)
	}
	req.Header = h
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := c.HC.Do(req)
	if err != nil {
		return out, fmt.Errorf("routine run: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return out, c.repoRefusal("routine run", repo, resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return out, fmt.Errorf("routine run: %w", err)
	}
	out.raw = bytes.TrimSpace(raw)
	if err := json.Unmarshal(raw, &out); err != nil {
		return RoutineRunResult{}, fmt.Errorf("routine run: %w", err)
	}
	return out, nil
}

// PauseRoutine pauses a routine operationally via POST /api/routines/{owner}/{name}/{routine}/pause.
func (c Client) PauseRoutine(ctx context.Context, repo, name, reason string) (RoutinePauseResult, error) {
	var out RoutinePauseResult
	if err := validateRoutineTarget(repo, name); err != nil {
		return out, fmt.Errorf("routine pause: %w", err)
	}
	payload := struct {
		Reason string `json:"reason,omitempty"`
	}{Reason: reason}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return out, fmt.Errorf("routine pause: %w", err)
	}
	raw, err := c.postRoutineAction(ctx, "routine pause", repo, name, "pause", rawBody)
	if err != nil {
		return out, err
	}
	out.raw = bytes.TrimSpace(raw)
	if err := json.Unmarshal(raw, &out); err != nil {
		return RoutinePauseResult{}, fmt.Errorf("routine pause: %w", err)
	}
	return out, nil
}

// ResumeRoutine clears an operational pause via POST /api/routines/{owner}/{name}/{routine}/resume.
func (c Client) ResumeRoutine(ctx context.Context, repo, name string) (RoutineResumeResult, error) {
	var out RoutineResumeResult
	if err := validateRoutineTarget(repo, name); err != nil {
		return out, fmt.Errorf("routine resume: %w", err)
	}
	raw, err := c.postRoutineAction(ctx, "routine resume", repo, name, "resume", []byte("{}"))
	if err != nil {
		return out, err
	}
	out.raw = bytes.TrimSpace(raw)
	if err := json.Unmarshal(raw, &out); err != nil {
		return RoutineResumeResult{}, fmt.Errorf("routine resume: %w", err)
	}
	return out, nil
}

func (c Client) postRoutineAction(ctx context.Context, op, repo, name, action string, body []byte) ([]byte, error) {
	h, err := c.header(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+routineAPIPath(repo, name, action), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	req.Header = h
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.repoRefusal(op, repo, resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return raw, nil
}
