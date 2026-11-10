// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/config"
	findingfmt "github.com/Smana/maku/internal/factory/finding"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/factory/taskid"
)

// LabelSourceKey carries taskid.Name of a finding's dedup key on every task made from that key
// (§1): tasks of one key share the label, so a replay finds the open one and a new generation
// starts after the last task ends.
const LabelSourceKey = findingfmt.LabelSourceKey

// Finding is what FR-9's notify.templated template renders from RunLore's payload.
type Finding struct {
	Title       string  `json:"title"`
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	AlertName   string  `json:"alert_name"`
	ResourceRef string  `json:"resource_ref"`
	Severity    string  `json:"severity"`
	Cluster     string  `json:"cluster"`
	Text        string  `json:"text"`
}

// RunLoreForge is the part of the forge the intake uses, as the factory App (issues:write is in
// its scope).
type RunLoreForge interface {
	CreateIssue(ctx context.Context, title, body string, labels []string) (forge.Issue, error)
}

// RunLore is the intake of §1: RunLore POSTs a finding, and an actionable one becomes a Task and
// a proposed issue. It serves POST /intake/runlore on runlore.listen over the tailnet (FR-9's
// notify route), on every replica like the run-request API. Token re-reads the mounted bearer
// secret at each request, so a rotation needs no restart (AGENTS.md).
type RunLore struct {
	// Forges is the factory App's connection to a repository, false when it has none. The intake
	// files on cfg.RunLoreRepository() (v2 D6), looked up per request: the repository set is live
	// (factory resources D12).
	Forges func(repository string) (RunLoreForge, bool)
	// Owned is v2 D7's fence; nil owns nothing.
	Owned     func(repository string) bool
	Client    client.Client
	Namespace string
	Cfg       config.Source
	Token     func() string
	Stopped   func(context.Context) bool // the stop object (§6.1): intake pauses while it holds
	Now       func() time.Time
	Errors    errorCounter
	Log       *slog.Logger

	warnedAt atomic.Int64 // unix nanoseconds of the last unconfigured warning
}

// unconfiguredWarnEvery rate-limits the missing-token warning: Alertmanager retries every finding.
const unconfiguredWarnEvery = time.Minute

// fileTimeout bounds filing a finding's issue and linking its Task, past the request's own
// context (F-8), as the run-request API's internal intake bounds them.
const fileTimeout = 30 * time.Second

// NeedLeaderElection is false: every replica serves intake, and the task's name is the key, so a
// replay racing across replicas loses on AlreadyExists and opens no second issue (SC-9).
func (h *RunLore) NeedLeaderElection() bool { return false }

// runloreDrain bounds Start's shutdown, as the run-request API's: under the manager's 20 s
// GracefulShutdownTimeout and the pod's 30 s grace.
const runloreDrain = 15 * time.Second

// Start serves POST /intake/runlore on runlore.listen until ctx ends, then drains for at most
// runloreDrain, and returns only once the drain has, in the run-request API server's shape
// (internal/factory/api): a finding past its Task's create files its issue before the exit.
func (h *RunLore) Start(ctx context.Context) error {
	cfg := h.Cfg.Current()
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.RunLore.Listen)
	if err != nil {
		return fmt.Errorf("runlore: listen %s: %w", cfg.RunLore.Listen, err)
	}
	return serve(ctx, h.httpServer(), ln, runloreDrain)
}

// httpServer is the intake's listener with its five bounds.
func (h *RunLore) httpServer() *http.Server {
	mux := http.NewServeMux()
	mux.Handle("POST /intake/runlore", h)
	return &http.Server{Handler: mux,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
}

// unconfigured makes a misprovisioned intake visible: an intake error counter on every refusal and
// a warning at most once a minute.
func (h *RunLore) unconfigured(ctx context.Context, cfg *config.Config) {
	if h.Errors != nil {
		h.Errors.IntakeError(fmetrics.WithRepository(ctx, cfg.RunLoreRepository()), "runlore")
	}
	now := h.Now().UnixNano()
	last := h.warnedAt.Load()
	if now-last >= int64(unconfiguredWarnEvery) && h.warnedAt.CompareAndSwap(last, now) {
		h.log().Warn("runlore intake refuses every request: its token is missing or empty",
			"file", cfg.RunLore.TokenFile)
	}
}

func (h *RunLore) log() *slog.Logger {
	if h.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return h.Log
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *RunLore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := h.Cfg.Current()
	// The chart mounts the token Secret optional: without it the intake is off, never open.
	// An empty bearer must not match an empty token.
	token := h.Token()
	if token == "" {
		h.unconfigured(r.Context(), cfg)
		reply(w, http.StatusServiceUnavailable, map[string]string{"error": "intake_unconfigured"})
		return
	}
	raw, err := authn.Bearer(r)
	if err != nil || subtle.ConstantTimeCompare([]byte(raw), []byte(token)) != 1 {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if h.Stopped(r.Context()) {
		reply(w, http.StatusServiceUnavailable, map[string]string{"error": "factory_stopped"})
		return
	}
	if h.Owned == nil || !h.Owned(cfg.RunLoreRepository()) { // v2 D7: files nothing on another factory's
		reply(w, http.StatusConflict, map[string]string{"error": "repository_not_owned"})
		return
	}
	var f Finding
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&f); err != nil || f.AlertName == "" || f.ResourceRef == "" {
		reply(w, http.StatusBadRequest, map[string]string{"error": "bad_finding"})
		return
	}
	if (f.Verdict != "action_required" && f.Verdict != "action_suggested") || f.Confidence < cfg.RunLore.MinConfidence {
		reply(w, http.StatusAccepted, map[string]any{"accepted": false, "reason": "below_threshold"})
		return
	}
	code, body, err := h.intake(r.Context(), cfg, f)
	if err != nil {
		if h.Errors != nil {
			h.Errors.IntakeError(fmetrics.WithRepository(r.Context(), cfg.RunLoreRepository()), "runlore")
		}
		// The finding's alert and resource are attacker-influenced: log the key's task name, never them.
		h.log().Warn("runlore intake failed", "task", taskid.Name(taskid.RunloreKey(f.AlertName, f.ResourceRef)), "err", err)
		reply(w, http.StatusServiceUnavailable, map[string]string{"error": "intake_failed"})
		return
	}
	reply(w, code, body)
}

// intake dedups the finding while a task for its key is open (SC-9), holds OD-9's daily cap,
// then creates the task before the issue: the task's name is the key's hash, so a replay racing
// on the other replica loses on AlreadyExists and opens no second issue.
func (h *RunLore) intake(ctx context.Context, cfg *config.Config, f Finding) (int, any, error) {
	gh, ok := h.Forges(cfg.RunLoreRepository())
	if !ok {
		return 0, nil, fmt.Errorf("runlore.repository %s has no GitHub connection", cfg.RunLoreRepository())
	}
	key := taskid.RunloreKey(f.AlertName, f.ResourceRef)
	group := taskid.Name(key)
	var same v1alpha1.TaskList
	if err := h.Client.List(ctx, &same, client.InNamespace(h.Namespace), client.MatchingLabels{LabelSourceKey: group}); err != nil {
		return 0, nil, err
	}
	for _, t := range same.Items {
		if !v1alpha1.TerminalPhase(t.Status.Phase) {
			return http.StatusOK, map[string]any{"duplicate": true, "task": t.Name}, nil
		}
	}
	var all v1alpha1.TaskList
	if err := h.Client.List(ctx, &all, client.InNamespace(h.Namespace)); err != nil {
		return 0, nil, err
	}
	today := 0
	for _, t := range all.Items {
		if t.Spec.Source.Kind == "runlore" && t.CreationTimestamp.UTC().Format(time.DateOnly) == h.Now().UTC().Format(time.DateOnly) {
			today++
		}
	}
	if today >= cfg.RunLore.DailyCap {
		return http.StatusTooManyRequests, map[string]string{"error": "runlore_daily_cap"}, nil
	}
	text := findingfmt.Text(f.Title, f.Verdict, f.Confidence, f.ResourceRef, f.AlertName, f.Severity, f.Cluster, f.Text)
	sum := sha256.Sum256([]byte(text))
	text, _ = sanitize.Text(text) // G2, R43: alert and log text are written outside the platform too
	if len(text) > cfg.Caps.MaxTextBytes && cfg.Caps.MaxTextBytes > 0 {
		text = strings.ToValidUTF8(text[:max(cfg.Caps.MaxTextBytes-64, 0)], "") + "\n[finding truncated by the factory]"
	}
	name := taskid.Name(fmt.Sprintf("%s:gen%d", key, len(same.Items)+1))
	t := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.Namespace,
			Labels: map[string]string{LabelSourceKey: group},
			// The day bucket above reads the creation time; the apiserver sets its own on
			// create, so this only makes the injected clock decide it in tests.
			CreationTimestamp: metav1.NewTime(h.Now())},
		Spec: v1alpha1.TaskSpec{
			// Alert and log text are attacker-influenced (§8 T1): untrusted, fenced for the harness.
			Source: v1alpha1.Source{Kind: "runlore", Ref: f.AlertName + "/" + f.ResourceRef, Key: key,
				RequestedBy: "system:runlore", Trust: "untrusted", ContentSHA256: hex.EncodeToString(sum[:])},
			Repository: cfg.RunLoreRepository(), Text: text, DataClass: "internal",
		},
	}
	if err := h.Client.Create(ctx, t); apierrors.IsAlreadyExists(err) {
		return http.StatusOK, map[string]any{"duplicate": true, "task": name}, nil // the other replica won
	} else if err != nil {
		return 0, nil, err
	}
	// The issue is public: alert, resource, verdict and the room, never the finding (R33); the
	// identifiers reach it through finding.LabelSafe (TW6).
	title, body := findingfmt.StubIssue(f.AlertName, f.ResourceRef, f.Severity, f.Verdict, f.Confidence, name, cfg.RoomsURL)
	// Past the Task, a caller that hangs up must not orphan it: filing and linking outlive ctx
	// (F-8), as api/internal.go's intake does.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fileTimeout)
	defer cancel()
	iss, err := gh.CreateIssue(fctx, title, body, []string{"factory/proposed"})
	if err != nil {
		return 0, nil, err
	}
	n := iss.Number
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"issue": n}})
	if err := h.Client.Patch(fctx, &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.Namespace}},
		client.RawPatch(types.MergePatchType, patch)); err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, map[string]any{"task": name, "issue": n}, nil
}
