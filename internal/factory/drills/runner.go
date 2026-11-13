// SPDX-License-Identifier: Apache-2.0

package drills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/gitbundle"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

// DefaultInterval is the runner's poll.
const DefaultInterval = time.Minute

// baseBranch is the branch every task's work is cut from and pushed onto (the applier's baseRef).
const baseBranch = "main"

// Rooms is the broker as the runner reads and writes it; *rooms.Client implements it.
type Rooms interface {
	EventsSince(ctx context.Context, room string, afterSeq int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error)
	Bundle(ctx context.Context, room, runID, digest string) ([]byte, error)
	PutDrillReport(ctx context.Context, r hardening.DrillReport) error
	DrillPassed(ctx context.Context, id string) (bool, error)
}

// Claims reads a run's claim for its canary; runs.Client implements it.
type Claims interface {
	Get(ctx context.Context, id string) (runs.Run, bool, error)
}

// BundleReader reads an applied push's whole change after the fact; *gitbundle.Mirror implements it.
type BundleReader interface {
	Read(ctx context.Context, bundle []byte, branch, base string) (gitbundle.Inspection, error)
}

// Gateway is the model gateway's audit log; *GatewayLog implements it.
type Gateway interface {
	MarkerHits(ctx context.Context, runIDs []string, marker string, from, to time.Time) (int, error)
	Guardrails(ctx context.Context, runIDs []string, from, to time.Time) (map[string]int, error)
}

// Metrics records each scored drill; fmetrics.Set implements it.
type Metrics interface {
	HardeningDrillScored(ctx context.Context, pass bool, at time.Time)
}

// Runner scores the drills whose manifests the factory accepted (HR38): once every planted task has
// ended, or the drill's timeout has passed, it builds one hardening.Observation per run of a role
// the scenario names, scores them against the suite, and stores the report through the broker. It
// runs on the leader alone.
type Runner struct {
	// Client reads the manifests' ConfigMaps and the Tasks, and annotates both once scored: a live
	// client, since the manager's ConfigMap cache holds the stop object alone.
	Client    client.Client
	Namespace string
	Rooms     Rooms
	Claims    Claims
	// Readers is a repository's bundle reader, the applier's git mirror; nil, or an error, leaves an
	// applied push unread, and the scorer fails it unscorable.
	Readers func(ctx context.Context, repo string) (BundleReader, error)
	Gateway Gateway
	Suite   hardening.Suite
	// Key is what a drill scored now proves: the Maku version and the live hardening hash.
	Key func() hardening.DrillKey
	// Timeout is drills.timeout, read live.
	Timeout  func() time.Duration
	Interval time.Duration
	Now      func() time.Time
	Metrics  Metrics
	Log      *slog.Logger
}

// NeedLeaderElection is true: one replica scores each drill.
func (*Runner) NeedLeaderElection() bool { return true }

// Start ticks until ctx ends. A failed tick is logged and retried on the next.
func (r *Runner) Start(ctx context.Context) error {
	every := r.Interval
	if every <= 0 {
		every = DefaultInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			r.Log.Warn("drill runner: a pass failed; retried on the next", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick scores every accepted, unscored drill that is due.
func (r *Runner) Tick(ctx context.Context) error {
	var l corev1.ConfigMapList
	if err := r.Client.List(ctx, &l, client.InNamespace(r.Namespace), client.MatchingLabels{ManifestLabel: "true"}); err != nil {
		return fmt.Errorf("list drill manifests: %w", err)
	}
	var errs []error
	for i := range l.Items {
		cm := &l.Items[i]
		if cm.Annotations[v1alpha1.AnnotationDrillScored] != "" {
			continue
		}
		if err := r.one(ctx, cm); err != nil {
			errs = append(errs, fmt.Errorf("drill %s: %w", cm.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (r *Runner) one(ctx context.Context, cm *corev1.ConfigMap) error {
	m, err := ParseManifest([]byte(cm.Data[manifestKey]), r.Suite)
	if err != nil {
		r.Log.Error("drill runner: an unreadable manifest is left unscored", "configmap", cm.Name)
		return nil
	}
	now := r.Now()
	tasks := map[string]*v1alpha1.Task{}
	ended := true
	for _, p := range m.Scenarios {
		var t v1alpha1.Task
		switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: p.Task}, &t); {
		case apierrors.IsNotFound(err): // never made, or gone: its roles fail not_run
		case err != nil:
			return fmt.Errorf("read task %s: %w", p.Task, err)
		default:
			tasks[p.Task] = &t
			ended = ended && v1alpha1.TerminalPhase(t.Status.Phase)
		}
	}
	if !ended && now.Before(cm.CreationTimestamp.Add(r.Timeout())) {
		return nil // not due
	}
	var obs []hardening.Observation
	for _, p := range m.Scenarios {
		if t := tasks[p.Task]; t != nil {
			o, err := r.observe(ctx, p, t, now)
			if err != nil {
				return err
			}
			obs = append(obs, o...)
		}
	}
	key := r.Key()
	rep := hardening.Score(r.Suite, hardening.DrillMeta{ID: m.ID, Version: key.Version, ConfigHash: key.ConfigHash, At: now}, obs)
	pass := rep.Pass
	switch err := r.Rooms.PutDrillReport(ctx, rep); {
	case errors.Is(err, rooms.ErrDrillExists):
		// An earlier pass stored the report and lost its mark: the stored report is the drill's
		// result, not this fresh score (#520's review).
		if pass, err = r.Rooms.DrillPassed(ctx, m.ID); err != nil {
			return fmt.Errorf("read the stored report: %w", err)
		}
	case err != nil:
		return fmt.Errorf("store the report: %w", err)
	}
	for _, t := range tasks {
		if err := r.annotate(ctx, t, m.ID); err != nil {
			return err
		}
	}
	// The manifest last, by update: the factory's Role grants no patch on ConfigMaps, and a failed
	// update leaves the drill unscored, so the next tick marks the tasks again and retries it.
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[v1alpha1.AnnotationDrillScored] = map[bool]string{true: "pass", false: "fail"}[pass]
	if err := r.Client.Update(ctx, cm); err != nil {
		return fmt.Errorf("mark the manifest scored: %w", err)
	}
	if r.Metrics != nil {
		r.Metrics.HardeningDrillScored(ctx, pass, now)
	}
	r.Log.Info("drill scored", "drill", m.ID, "pass", pass, "failures", len(rep.Failures))
	return nil
}

// annotate marks a task scored with value, by merge patch (the Role grants patch on Tasks).
func (r *Runner) annotate(ctx context.Context, o client.Object, value string) error {
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{v1alpha1.AnnotationDrillScored: value}}})
	if err := r.Client.Patch(ctx, o, client.RawPatch(types.MergePatchType, patch)); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("mark %s scored: %w", o.GetName(), err)
	}
	return nil
}

// persona is the drill role a run of t played: the triager is the scoper, or investigate on the
// investigate template.
func persona(t *v1alpha1.Task, role string) string {
	if role == "triager" {
		if t.Spec.Template == "investigate" {
			return "investigate"
		}
		return "scoper"
	}
	return role
}

// observe is one Observation per run of t whose role p's scenario names. The judge is the
// implementer run's completion judge: an implementer run that wrote a judge_verdict is also observed
// as judge, from the same events and the same gateway requests.
func (r *Runner) observe(ctx context.Context, p Planted, t *v1alpha1.Task, now time.Time) ([]hardening.Observation, error) {
	sc := r.scenario(p.Scenario)
	ids := map[string]bool{}
	for _, rec := range t.Status.Runs {
		ids[rec.ID] = true
	}
	if len(ids) == 0 || t.Status.RoomRef == "" {
		return nil, nil
	}
	evs, err := r.roomEvents(ctx, t.Status.RoomRef, func(e envelope.Event) bool { return ids[e.RunID] })
	if err != nil {
		return nil, err
	}
	from := t.CreationTimestamp.Add(-time.Minute)
	var out []hardening.Observation
	for _, rec := range t.Status.Runs {
		role := persona(t, rec.Role)
		var roles []string
		if slices.Contains(sc.Roles, role) {
			roles = append(roles, role)
		}
		mine := slices.DeleteFunc(slices.Clone(evs), func(e envelope.Event) bool { return e.RunID != rec.ID })
		if role == "implementer" && slices.Contains(sc.Roles, "judge") && judged(mine, rec.ID) {
			roles = append(roles, "judge")
		}
		if len(roles) == 0 {
			continue
		}
		o := hardening.Observation{Scenario: p.Scenario, Marker: p.Marker, Events: mine}
		claim, found, err := r.Claims.Get(ctx, rec.ID)
		if err != nil {
			return nil, fmt.Errorf("read run %s's claim: %w", rec.ID, err)
		}
		if found {
			o.Canary = claim.Canary
		} else {
			o.CanaryUnknown = true // fail closed: the scorer cannot rule out a canary leak
		}
		if o.Pushes, err = r.pushes(ctx, t, rec.ID, mine); err != nil {
			return nil, err
		}
		for _, e := range mine {
			if in, ok := envelope.ReadInput(e, rec.ID); ok {
				for _, n := range in.Withheld {
					o.Withheld += n
				}
			}
		}
		// Only a withheld scenario's marker must never reach a model: a planted issue body is the
		// task's text, which the model reads by design.
		if sc.Withheld {
			if o.MarkerInModelRequests, err = r.Gateway.MarkerHits(ctx, []string{rec.ID}, p.Marker, from, now); err != nil {
				return nil, fmt.Errorf("read the gateway log: %w", err)
			}
		}
		if o.Guardrails, err = r.Gateway.Guardrails(ctx, []string{rec.ID}, from, now); err != nil {
			return nil, fmt.Errorf("read the gateway log: %w", err)
		}
		for _, role := range roles {
			x := o
			x.Role = role
			out = append(out, x)
		}
	}
	return out, nil
}

func (r *Runner) scenario(id string) hardening.Scenario {
	for _, sc := range r.Suite.Scenarios {
		if sc.ID == id {
			return sc
		}
	}
	return hardening.Scenario{}
}

// roomEvents reads all of room's events keep selects, page after page.
func (r *Runner) roomEvents(ctx context.Context, room string, keep func(envelope.Event) bool) ([]envelope.Event, error) {
	var all []envelope.Event
	cursor := int64(0)
	for {
		evs, next, err := r.Rooms.EventsSince(ctx, room, cursor, keep)
		if err != nil {
			return nil, fmt.Errorf("read room %s: %w", room, err)
		}
		all = append(all, evs...)
		if next <= cursor {
			return all, nil
		}
		cursor = next
	}
}

// judged reports whether run wrote a judge_verdict.
func judged(evs []envelope.Event, run string) bool {
	for _, e := range evs {
		var p envelope.MessagePayload
		if e.Type == envelope.Message && e.RunID == run && e.Actor.Kind == envelope.ActorAgent &&
			json.Unmarshal(e.Payload, &p) == nil && p.Kind == envelope.KindJudgeVerdict {
			return true
		}
	}
	return false
}

// pushes reads each applied push of run as its whole change onto the base: changed paths, then
// added lines and commit messages, keyed by the request's digest. A push the drill cannot read is
// left out, and the scorer fails it unscorable; a broker or git failure is retried.
func (r *Runner) pushes(ctx context.Context, t *v1alpha1.Task, run string, evs []envelope.Event) (map[string]string, error) {
	requested := map[string]string{}
	out := map[string]string{}
	for _, e := range evs {
		kind, rec, ok := envelope.ReadOutput(e, run)
		switch {
		case !ok || rec.Type != string(hardening.Push):
		case kind == envelope.KindOutputRequested:
			requested[rec.ID] = rec.Digest
		case kind == envelope.KindOutputApplied && requested[rec.ID] != "":
			digest := requested[rec.ID]
			if _, done := out[digest]; done || r.Readers == nil {
				continue
			}
			text, err := r.readPush(ctx, t, run, digest)
			switch {
			case errors.Is(err, errUnreadable):
				r.Log.Warn("drill runner: an applied push could not be read; it scores unscorable", "task.id", t.Name, "run.id", run)
			case err != nil:
				return nil, err
			default:
				out[digest] = text
			}
		}
	}
	return out, nil
}

var errUnreadable = errors.New("drills: the push cannot be read")

func (r *Runner) readPush(ctx context.Context, t *v1alpha1.Task, run, digest string) (string, error) {
	reader, err := r.Readers(ctx, t.Spec.Repository)
	if err != nil || reader == nil {
		return "", errUnreadable
	}
	b, err := r.Rooms.Bundle(ctx, t.Status.RoomRef, run, digest)
	switch {
	case errors.Is(err, rooms.ErrNoBundle), errors.Is(err, rooms.ErrBundleDigest):
		return "", errUnreadable
	case err != nil:
		return "", fmt.Errorf("read a bundle: %w", err)
	}
	in, err := reader.Read(ctx, b, "agent/"+t.Name, baseBranch)
	var refused *gitbundle.Refused
	switch {
	case errors.As(err, &refused):
		return "", errUnreadable
	case err != nil:
		return "", fmt.Errorf("read a pushed bundle: %w", err)
	}
	lines := make([]string, 0, len(in.Files)+len(in.Added))
	for _, f := range in.Files {
		lines = append(lines, f.Path)
	}
	return strings.Join(append(lines, in.Added...), "\n"), nil
}
