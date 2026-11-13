// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/rooms"
)

// GrantClient is the broker's grant routes as the gate reaches them (merge policy stage 5);
// *rooms.Client implements it. A lookup expires and suspends before it answers.
type GrantClient interface {
	LookupGrants(ctx context.Context, repository, class, policyHash string, maxLifetime time.Duration) ([]grant.Grant, []string, error)
	UseGrant(ctx context.Context, id string, version, pr int, head string) error
	DisableGrant(ctx context.Context, id string, pr int, reason string) error
}

// grantsOn is whether approval memory may read or move a grant: the switch on and the broker wired.
func (r *Reconciler) grantsOn(cfg *config.Config) bool {
	return cfg.PolicyManagement.ApprovalMemory && r.Grants != nil
}

// needsGrants is whether deciding in under the snapshot could consult a grant: a valid policy whose
// class has a live variant and a requiresApproval subset, the diff read. Nothing else is looked up.
func needsGrants(in ArmInputs, snap mergepolicy.Snapshot, ok bool) bool {
	r, has := snap.Classes[in.Task.Spec.PredictedClass]
	return ok && snap.Err == nil && has && len(r.Approval) > 0 && in.DiffRead &&
		slices.ContainsFunc(r.Variants(), func(v config.Class) bool { return v.Live })
}

// needsGrantsUnder is needsGrants for the policy that decides: the repository's snapshot when it
// is authoritative, else the central class, live with a requiresApproval subset, the diff read.
func needsGrantsUnder(in ArmInputs, cfg *config.Config, p gatePolicy) bool {
	if p.on {
		return p.err == nil && needsGrants(in, p.snap, true)
	}
	cl, ok := cfg.RepositoryFor(in.Task.Spec.Repository).Class(in.Task.Spec.PredictedClass)
	return ok && cl.Live && len(cl.RequiresApproval) > 0 && in.DiffRead
}

// snapshotHash is a repository policy's class as a grant binds to it (B13): its variants'
// resolved hash, the requiresApproval leaves included as written, as config.ResolvedHash reads a
// central class's, so a class and its in-sync file hash alike and an edit to the subset suspends
// grants. A Resolved that keeps only the flattened subset hashes it as one leaf.
func snapshotHash(res mergepolicy.Resolved) string {
	leaves := res.Approvals
	if len(leaves) == 0 && len(res.Approval) > 0 {
		leaves = []config.ApprovalLeaf{{ChangedFiles: res.Approval}}
	}
	vs := slices.Clone(res.Variants())
	for i := range vs {
		if len(vs[i].RequiresApproval) == 0 && len(leaves) > 0 {
			v := *vs[i].DeepCopy()
			v.RequiresApproval = make([]config.ApprovalLeaf, len(leaves))
			for j, l := range leaves {
				v.RequiresApproval[j] = config.ApprovalLeaf{ChangedFiles: slices.Clone(l.ChangedFiles)}
			}
			vs[i] = v
		}
	}
	return config.ResolvedHash(vs...)
}

// grantHash is the hash the grants of t's decision bind to (B13, Q9): the resolved hash of the
// class that decides, the repository policy's under its authority, else the central class's.
// False when no class decides: a policy that gives no authority, or a class not declared.
func (r *Reconciler) grantHash(cfg *config.Config, t *v1alpha1.Task, p gatePolicy) (string, bool) {
	class := t.Spec.PredictedClass
	if p.on {
		res, ok := p.snap.Classes[class]
		if p.err != nil || !ok {
			return "", false
		}
		return snapshotHash(res), true
	}
	cl, ok := r.repo(cfg, t).Class(class)
	if !ok {
		return "", false
	}
	return config.ResolvedHash(cl), true
}

// grantRead looks the task's class's grants up immediately before a decision, never from a cache
// (MP13): a failed lookup is a read that authorizes nothing. It records the active count and the
// events the broker made on the way.
func (r *Reconciler) grantRead(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, policyHash string) GrantRead {
	repository, class := strings.ToLower(t.Spec.Repository), t.Spec.PredictedClass
	gs, events, err := r.Grants.LookupGrants(ctx, repository, class, policyHash, cfg.MergeGuardrails.GrantLifetime())
	if err != nil {
		r.log().Warn("approval grants unread: none authorizes", "task.id", t.Name, "class", class, "err", err)
		return GrantRead{}
	}
	record(ctx, func(ctx context.Context) {
		r.Metrics.ApprovalGrantsActive(ctx, t.Spec.Repository, class, len(gs))
		for _, e := range events {
			r.Metrics.ApprovalGrantEvent(ctx, t.Spec.Repository, class, e)
		}
	})
	return GrantRead{Read: true, List: gs, PolicyHash: policyHash, At: r.Now()}
}

// grantRef splits a decision's grant, "<id>:<version>".
func grantRef(ref string) (id string, version int, ok bool) {
	id, v, ok := strings.Cut(ref, ":")
	version, err := strconv.Atoi(v)
	return id, version, ok && err == nil && grant.IDRE.MatchString(id) && version > 0
}

// grantHolds reads the grant a decision armed on again, immediately before the merge call
// (MP12, MP13): the same id at the same version, still active under the policy in force. A
// decision no grant armed holds; a lookup that fails, or a grant moved on, does not.
func (r *Reconciler) grantHolds(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, d ArmDecision) bool {
	if d.Rows.Grant == "" {
		return true
	}
	id, version, ok := grantRef(d.Rows.Grant)
	if !ok || !r.grantsOn(cfg) {
		return false
	}
	hash, ok := r.decidingHash(cfg, t)
	if !ok {
		return false
	}
	gr := r.grantRead(ctx, cfg, t, hash)
	return gr.Read && slices.ContainsFunc(gr.List, func(g grant.Grant) bool {
		return g.ID == id && g.Version == version && g.Status == grant.StatusActive && g.BoundTo(hash) && gr.At.Before(g.ExpiresAt)
	})
}

// grantUsed records a merge the grant authorized: on the task, for a revert to find, and with the
// broker. The merge happened, so a failed record is logged and the task goes on.
func (r *Reconciler) grantUsed(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, ref *v1alpha1.PullRequestRef, d ArmDecision, head string) {
	if d.Rows.Grant == "" {
		return
	}
	ref.Grant = d.Rows.Grant
	id, version, ok := grantRef(d.Rows.Grant)
	if !ok || !r.grantsOn(cfg) {
		return
	}
	if err := r.Grants.UseGrant(ctx, id, version, ref.Number, head); err != nil {
		r.log().Error("record a grant's use", "task.id", t.Name, "grant", d.Rows.Grant, "err", err)
		return
	}
	class := t.Spec.PredictedClass
	record(ctx, func(ctx context.Context) {
		r.Metrics.ApprovalGrantEvent(ctx, t.Spec.Repository, class, grant.EventUsed)
	})
}

// disableGrant disables for good the grant a reverted merge was authorized under (saved grants:
// "disabled immediately when an auto-merge they authorize is reverted"). A grant already out of
// active is as good as disabled for this merge; any other failure is returned, so the revert's
// pass retries it.
func (r *Reconciler) disableGrant(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, why string) error {
	ref := t.Status.PullRequest
	if ref == nil || ref.Grant == "" {
		return nil
	}
	id, _, ok := grantRef(ref.Grant)
	if !ok {
		return nil
	}
	if !r.grantsOn(cfg) {
		r.log().Error("a grant's merge was reverted with approval memory off: disable it by hand", "task.id", t.Name, "grant", ref.Grant)
		return nil
	}
	switch err := r.Grants.DisableGrant(ctx, id, ref.Number, why); {
	case errors.Is(err, rooms.ErrGrantState):
		return nil
	case err != nil:
		return err
	}
	class := t.Spec.PredictedClass
	record(ctx, func(ctx context.Context) {
		r.Metrics.ApprovalGrantEvent(ctx, t.Spec.Repository, class, grant.EventDisabled)
	})
	return nil
}

// decidingHash is the hash a grant of t's decision is bound to, read again before the merge: the
// class that decided it (grantHash), a cut-over repository's authority snapshot's while it gives
// authority, else the central class's.
func (r *Reconciler) decidingHash(cfg *config.Config, t *v1alpha1.Task) (string, bool) {
	return r.grantHash(cfg, t, r.gatePolicy(cfg, r.repo(cfg, t), t.Spec.Repository))
}
