// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runwatch"
)

// FieldManager is the server-side apply field manager of the run token Secrets.
const FieldManager = "maku-factory-minter"

// The binding refusals (plan PR4): a closed set, the refusal metric's reason label.
const (
	RefusedUnboundTask        = "unbound_task"
	RefusedUnboundAPI         = "unbound_api"
	RefusedRepositoryMismatch = "repository_mismatch"
	RefusedRoleUnknown        = "role_unknown"
)

const (
	defaultTick = 30 * time.Second
	// refreshWithin re-mints a token with less than this to live (plan PR5): the run-controller
	// refuses to start a pod on one with less than 5 minutes, and the kubelet takes a minute or two
	// to sync a Secret volume.
	refreshWithin = 10 * time.Minute
)

// mintRevoker is what RunTokens asks of the Minter.
type mintRevoker interface {
	Mint(ctx context.Context, repo, role string, enforced []hardening.OutputType) (forge.Token, error)
	Revoke(ctx context.Context, token string) error
}

var _ mintRevoker = (*Minter)(nil)

// RefusalMetrics counts the runs refused a token: fmetrics.Set's RunTokenRefusal.
type RefusalMetrics interface {
	RunTokenRefusal(ctx context.Context, reason string)
}

// RunTokens keeps one Secret, <run name>-git, per live run of the portable kind, holding a token
// the Minter minted for that run's repository and role (v4 D10, plan PR3 to PR5). It writes with
// server-side apply and deletes, and never reads a Secret: the factory's Role in the runs
// namespace has no get. Ensure applies a run's first token right after its claim is created, on
// any replica; Start, the leader's loop, refreshes, revokes and deletes.
type RunTokens struct {
	Minter mintRevoker
	// Client applies and deletes the Secrets; it is never asked to read one.
	Client client.Client
	// Runs lists the runs kind's claims (the manager's cache); Tasks reads the factory's Tasks.
	Runs  client.Reader
	Tasks client.Reader
	Kind  runwatch.Kind
	// TaskNamespace is the factory's namespace, where its Tasks live.
	TaskNamespace string
	// Reserved reports whether the day ledgers hold a reservation under run id (ledger.Holds).
	Reserved func(ctx context.Context, id string) (bool, error)
	Metrics  RefusalMetrics // nil counts nothing
	Log      *slog.Logger   // nil logs nothing
	Now      func() time.Time
	Every    time.Duration // the loop's tick, 30 s when zero

	mu    sync.Mutex
	state map[types.UID]*runToken
}

// runToken is what the factory keeps of one run's token, in memory only: a new leader keeps
// nothing and mints afresh for every live run.
type runToken struct {
	value     string
	expiresAt time.Time
	suspended bool
	refused   string // the refusal logged for this run, so it is logged once
	deleted   bool   // the terminal run's Secret is gone
}

// NeedLeaderElection is true: the loop runs on the leader only. Ensure runs anywhere.
func (t *RunTokens) NeedLeaderElection() bool { return true }

func (t *RunTokens) log() *slog.Logger {
	if t.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return t.Log
}

func (t *RunTokens) now() time.Time {
	if t.Now == nil {
		return time.Now()
	}
	return t.Now()
}

// Start ticks until ctx ends: every live run's token is kept fresh, every ended run's revoked
// and its Secret deleted.
func (t *RunTokens) Start(ctx context.Context) error {
	every := t.Every
	if every <= 0 {
		every = defaultTick
	}
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		if err := t.Tick(ctx); err != nil {
			t.log().Warn("run token: listing the runs failed; retrying at the next tick", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
		}
	}
}

// Tick is one pass over the runs kind's claims.
func (t *RunTokens) Tick(ctx context.Context) error {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(t.Kind.GVK().GroupVersion().WithKind(t.Kind.GVK().Kind + "List"))
	if err := t.Runs.List(ctx, l, client.InNamespace(t.Kind.Namespace)); err != nil {
		return fmt.Errorf("run tokens: list runs: %w", err)
	}
	seen := map[types.UID]bool{}
	for i := range l.Items {
		seen[l.Items[i].GetUID()] = true
		t.Ensure(ctx, &l.Items[i])
	}
	// A claim gone altogether took its Secret with it (garbage collection): revoke what we hold.
	t.mu.Lock()
	var gone []string
	for uid, s := range t.state {
		if !seen[uid] {
			if s.value != "" {
				gone = append(gone, s.value)
			}
			delete(t.state, uid)
		}
	}
	t.mu.Unlock()
	for _, v := range gone {
		_ = t.Minter.Revoke(ctx, v) // best effort; the minter counts and never names the token
	}
	return nil
}

// Ensure brings one claim's token Secret to where its run is: minted and fresh while it lives,
// left alone while Suspended, revoked and deleted once it has ended. A failure leaves the Secret
// as it was; the next tick tries again.
func (t *RunTokens) Ensure(ctx context.Context, claim *unstructured.Unstructured) {
	run, ok := runs.FromUnstructured(t.Kind, claim)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == nil {
		t.state = map[types.UID]*runToken{}
	}
	s := t.state[claim.GetUID()]
	if s == nil {
		s = &runToken{}
		t.state[claim.GetUID()] = s
	}
	switch {
	case runs.Terminal(run.Phase) || run.Deleting:
		t.end(ctx, claim, s)
		return
	case run.Phase == "Suspended":
		s.suspended = true
		return
	}
	resumed := s.suspended
	s.suspended = false
	if !resumed && s.value != "" && s.expiresAt.Sub(t.now()) > refreshWithin {
		return
	}
	if why := t.bound(ctx, run); why != "" {
		if s.refused != why {
			s.refused = why
			if t.Metrics != nil {
				t.Metrics.RunTokenRefusal(ctx, why)
			}
			t.log().Warn("run token: refused; the run waits Pending", "run", claim.GetName(), "reason", why)
		}
		return
	}
	s.refused = ""
	tok, err := t.Minter.Mint(ctx, run.Repository, run.Role, enforced(claim))
	if err != nil {
		t.log().Warn("run token: mint failed", "run", claim.GetName(), "role", run.Role, "result", Result(err))
		return
	}
	if err := t.apply(ctx, claim, tok); err != nil {
		t.log().Warn("run token: writing the Secret failed", "run", claim.GetName(), "error", err.Error())
		_ = t.Minter.Revoke(ctx, tok.Value) // never applied: nobody holds it
		return
	}
	// A replaced token is not revoked: a pod may still read it until the kubelet syncs the volume,
	// and it has under 10 minutes left (plan PR5's residual, docs/security.md).
	s.value, s.expiresAt, s.deleted = tok.Value, tok.ExpiresAt, false
}

// end revokes the token held in memory, best effort, and deletes the run's Secret once.
func (t *RunTokens) end(ctx context.Context, claim *unstructured.Unstructured, s *runToken) {
	if s.value != "" {
		_ = t.Minter.Revoke(ctx, s.value) // counted by the minter; its error never names the token
		s.value = ""
	}
	if s.deleted {
		return
	}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: t.Kind.Namespace, Name: runwatch.RunTokenSecret(claim.GetName())}}
	if err := t.Client.Delete(ctx, sec); err != nil && !apierrors.IsNotFound(err) {
		t.log().Warn("run token: deleting the Secret failed; retrying at the next tick", "run", claim.GetName(), "error", err.Error())
		return
	}
	s.deleted = true
}

// bound is "" when the factory can account for run (plan PR4), or the refusal's reason.
func (t *RunTokens) bound(ctx context.Context, run runs.Run) string {
	if _, err := t.table().For(run.Role, nil); err != nil || run.Role == Publisher {
		return RefusedRoleUnknown
	}
	if run.TaskID != "" {
		task := &factoryv1.Task{}
		if err := t.Tasks.Get(ctx, types.NamespacedName{Namespace: t.TaskNamespace, Name: run.TaskID}, task); err != nil ||
			factoryv1.TerminalPhase(task.Status.Phase) {
			return RefusedUnboundTask
		}
		if !strings.EqualFold(task.Spec.Repository, run.Repository) {
			return RefusedRepositoryMismatch
		}
		return ""
	}
	if run.RequestHash == "" || t.Reserved == nil {
		return RefusedUnboundAPI
	}
	if ok, err := t.Reserved(ctx, run.ID); err != nil || !ok {
		return RefusedUnboundAPI
	}
	return ""
}

func (t *RunTokens) table() Table {
	if m, ok := t.Minter.(*Minter); ok {
		return m.table()
	}
	return Roles
}

// enforced is the run's own safe-outputs annotation, written at CREATE (HR14): never the live
// config, so a mid-run publish never widens a token.
func enforced(claim *unstructured.Unstructured) []hardening.OutputType {
	var out []hardening.OutputType
	for _, v := range strings.Split(claim.GetAnnotations()[runwatch.SafeOutputsAnnotation], ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, hardening.OutputType(v))
		}
	}
	return out
}

// apply server-side-applies the run's Secret: the token, the labels, the expiry, and an
// ownerReference to the run, so garbage collection removes it with the run.
func (t *RunTokens) apply(ctx context.Context, claim *unstructured.Unstructured, tok forge.Token) error {
	no := false
	gvk := claim.GroupVersionKind()
	s := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: t.Kind.Namespace, Name: runwatch.RunTokenSecret(claim.GetName()),
			Labels:      map[string]string{runwatch.RunTokenManagedByLabel: runwatch.RunTokenManagedBy, runwatch.RunTokenRunLabel: claim.GetName()},
			Annotations: map[string]string{runwatch.RunTokenExpiresAnnotation: tok.ExpiresAt.UTC().Format(time.RFC3339)},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind,
				Name: claim.GetName(), UID: claim.GetUID(), BlockOwnerDeletion: &no}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{runwatch.RunTokenKey: []byte(tok.Value)},
	}
	if err := t.Client.Patch(ctx, s, client.Apply, client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
		// The status alone: an apply's error could quote the object it was sent.
		var st apierrors.APIStatus
		if errors.As(err, &st) {
			return fmt.Errorf("apply Secret %s: %s (%d)", s.Name, st.Status().Reason, st.Status().Code)
		}
		return fmt.Errorf("apply Secret %s failed", s.Name)
	}
	return nil
}
