// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/narrate"
)

// Held commands (no silent drops, 2026-11-25). A slash command counts only in the phase it applies
// in (command's window). One a maintainer posts earlier is held in status.held, answered at once,
// and applied once when the task reaches that phase, if still valid then: command reads a held
// comment whatever its age. It expires, with a reply, when the task moves past that phase without
// it, when the task ends, or after heldTTL. A plan command is never applied to a later plan: the
// plan's review comment names it instead.

// heldTTL is how long a held command waits for its phase.
const heldTTL = 24 * time.Hour

// heldScanEvery bounds the issue reads that look for early commands: at most one a task in this
// window. It is under retrySkew, so a command posted just before the phase change is in the window.
const heldScanEvery = 30 * time.Second

// maxHeld is the CRD's cap on status.held.
const maxHeld = 16

// refusalEvery is how often one person is told their input was not acted on, per task.
const refusalEvery = time.Hour

// maxRefusals is the CRD's cap on status.refusals.
const maxRefusals = 16

// heldVerbs are the commands that can be held, by their status name. The order is the parse
// order: a comment holding several command lines is the first of them here.
var heldVerbs = []struct{ name, verb string }{
	{"retry-openhands", cmdRetryRescue},
	{"retry-deeper", cmdRetryDeeper},
	{"retry", cmdRetry},
	{"split", cmdSplit},
	{"fix", cmdFix},
}

// heldVerb is the command a held name stands for.
func heldVerb(name string) string {
	for _, v := range heldVerbs {
		if v.name == name {
			return v.verb
		}
	}
	return "/factory " + strings.Replace(name, "-", " ", 1) // plan-approve: /factory plan approve
}

// heldFamily is what a newer command replaces: the retries are one answer to an escalation.
func heldFamily(name string) string {
	if strings.HasPrefix(name, "retry") {
		return "retry"
	}
	if strings.HasPrefix(name, "plan-") {
		return "plan"
	}
	return name
}

// parseHeld is the command a comment carries, by its status name.
func parseHeld(body string) (string, bool) {
	for _, v := range heldVerbs {
		if commandLine(body, v.verb) {
			return v.name, true
		}
	}
	if dec, _, ok := parsePlanCommand(body); ok {
		return "plan-" + dec, true
	}
	return "", false
}

// heldTarget reports whether t is in the phase the command applies in: there the phase's own
// handler reads it, and command takes a held one whatever its age.
func (r *Reconciler) heldTarget(t *v1alpha1.Task, name string) bool {
	switch heldFamily(name) {
	case "retry":
		if t.Status.Phase == v1alpha1.PhaseEscalated {
			return true
		}
		return name == "retry-deeper" && r.noopRetryable(t)
	case "split":
		return t.Status.Phase == v1alpha1.PhaseEscalated && t.Status.Reason == ReasonSplitProposed &&
			t.Status.Split != nil && t.Status.Split.State == SplitProposed
	case "fix":
		return r.reportFixable(t)
	case "plan":
		return t.Status.Phase == v1alpha1.PhaseAwaitingHuman && t.Status.Reason == ReasonPlanReview
	}
	return false
}

// heldWhen is the phase a held command waits for, as its reply says it.
func heldWhen(name string) string {
	switch heldFamily(name) {
	case "split":
		return "proposes a split"
	case "fix":
		return "posts its findings"
	}
	if name == "retry-deeper" {
		return "is escalated or ends with no change"
	}
	return "is escalated"
}

// notApplicable is why this task can never act on the command, or "" when it can.
func notApplicable(t *v1alpha1.Task, name string) string {
	switch heldFamily(name) {
	case "fix":
		if t.Spec.Outcome != config.OutcomeReport {
			return "`/factory fix` applies only to a report task"
		}
	case "split":
		if t.Spec.Split == nil {
			return "this task's workflow does not offer a split"
		}
	case "plan":
		if !planGateApplies(t) {
			return "this task has no plan waiting for an answer, now or later"
		}
	}
	if name == "retry-openhands" {
		if p := t.Status.Profile; p == nil || p.Harness != config.HarnessOpenCode {
			return "the rescue onto OpenHands applies only to a task running on OpenCode"
		}
	}
	return ""
}

// heldScans remembers each task's last scan for early commands, in this process only: a restart
// scans again, and status.handled and status.held keep a comment from being answered twice.
type heldScans struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (h *heldScans) due(task string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if last, ok := h.last[task]; ok && now.Sub(last) < heldScanEvery && !now.Before(last) {
		return false
	}
	if h.last == nil {
		h.last = map[string]time.Time{}
	}
	h.last[task] = now
	return true
}

// holdCommands reads the task's issue for commands posted while the task cannot apply them: a
// maintainer's with write is held (or answered as never applicable), anyone else's answered as
// not acted on. A comment older than the task, edited, already handled or held, or whose phase is
// now (its handler reads it) is skipped. A read that fails is logged: the pass goes on.
func (r *Reconciler) holdCommands(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) {
	if t.Spec.Issue <= 0 || v1alpha1.TerminalPhase(t.Status.Phase) || !r.held.due(t.Name, r.Now()) {
		return
	}
	cs, err := r.gh(ctx).RecentComments(ctx, t.Spec.Issue)
	if err != nil {
		r.log().Warn("early commands not read", "task.id", t.Name, "err", err)
		return
	}
	slices.SortFunc(cs, func(a, b forge.Comment) int { return a.At.Compare(b.At) }) // oldest first: the newest of a kind wins
	// A command of a kind already acted on later is superseded, not early: two retries posted in
	// one escalation, the newer applied, leave the older unhandled.
	acted := map[string]time.Time{}
	for _, c := range cs {
		if name, ok := parseHeld(c.Body); ok && slices.Contains(t.Status.Handled, c.ID) && r.repo(cfg, t).IsMaintainer(c.Author) {
			acted[heldFamily(name)] = c.At
		}
	}
	for _, c := range cs {
		name, ok := parseHeld(c.Body)
		if !ok || c.Edited || c.At.Before(t.CreationTimestamp.Time) || forge.SameLogin(c.Author, cfg.FactoryLogin) ||
			slices.Contains(t.Status.Handled, c.ID) || isHeld(t, c.ID) || r.heldTarget(t, name) {
			continue
		}
		if at, ok := acted[heldFamily(name)]; ok && !c.At.After(at) {
			continue
		}
		if err := r.holdComment(ctx, cfg, t, c, name); err != nil {
			r.log().Warn("early command not checked", "task.id", t.Name, "comment", c.ID, "err", err)
			return
		}
	}
}

// holdComment answers one early command: held, never applicable, or not acted on.
func (r *Reconciler) holdComment(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, c forge.Comment, name string) error {
	verb := heldVerb(name)
	var allowed bool
	var err error
	if heldFamily(name) == "plan" {
		allowed, err = r.canAnswerPlan(ctx, cfg, t, c.Author)
	} else {
		allowed, err = r.canCommand(ctx, cfg, t, c.Author)
	}
	if err != nil {
		return err
	}
	if !allowed {
		r.refuseInput(cfg, t, c.ID, c.Author, target(t), "`"+verb+"`")
		return nil
	}
	if why := notApplicable(t, name); why != "" {
		markHandled(t, c.ID)
		narrateLater(t, narrate.CommandNotApplicable(t, c.ID, verb, why))
		return nil
	}
	if len(t.Status.Held) >= maxHeld && !slices.ContainsFunc(t.Status.Held, func(h v1alpha1.HeldCommand) bool {
		return heldFamily(h.Command) == heldFamily(name)
	}) {
		markHandled(t, c.ID)
		narrateLater(t, narrate.CommandNotApplicable(t, c.ID, verb, "the task already holds as many commands as it keeps"))
		return nil
	}
	// The newest of a kind replaces the older: both answer the same phase. The older is handled,
	// so no later scan holds it again.
	t.Status.Held = slices.DeleteFunc(t.Status.Held, func(h v1alpha1.HeldCommand) bool {
		if heldFamily(h.Command) == heldFamily(name) {
			markHandled(t, h.ID)
			return true
		}
		return false
	})
	t.Status.Held = append(t.Status.Held, v1alpha1.HeldCommand{ID: c.ID, Command: name, Author: c.Author, At: metav1.NewTime(c.At)})
	if heldFamily(name) == "plan" {
		narrateLater(t, narrate.PlanCommandNoted(t, c.ID, verb))
	} else {
		narrateLater(t, narrate.CommandHeld(t, c.ID, verb, heldWhen(name)))
	}
	return nil
}

// canCommand: a maintainer of the task's repository with write on it, as command requires.
func (r *Reconciler) canCommand(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, login string) (bool, error) {
	if !r.repo(cfg, t).IsMaintainer(login) || r.CanWrite == nil {
		return false, nil
	}
	return r.CanWrite(ctx, t.Spec.Repository, login)
}

// isHeld reports whether comment id is held.
func isHeld(t *v1alpha1.Task, id int64) bool {
	return slices.ContainsFunc(t.Status.Held, func(h v1alpha1.HeldCommand) bool { return h.ID == id })
}

// heldComments are the held commands for verb, as comments command reads with the live ones.
func heldComments(t *v1alpha1.Task, verb string) []forge.Comment {
	var out []forge.Comment
	for _, h := range t.Status.Held {
		if heldVerb(h.Command) == verb {
			out = append(out, forge.Comment{ID: h.ID, Author: h.Author, Body: verb, At: h.At.Time})
		}
	}
	return out
}

// heldInPhase are the held commands whose phase the task is in at the start of a pass.
func (r *Reconciler) heldInPhase(t *v1alpha1.Task) []int64 {
	var out []int64
	for _, h := range t.Status.Held {
		if r.heldTarget(t, h.Command) {
			out = append(out, h.ID)
		}
	}
	return out
}

// settleHeld ends the held commands this pass decided: applied (handled by their phase's
// handler), or expired because the task left their phase without them, ended, or waited past
// heldTTL. Plan commands end at the plan's review comment (dropPlanHeld). Each is answered and
// handled, so no scan holds it again.
func (r *Reconciler) settleHeld(t *v1alpha1.Task, wasInPhase []int64) {
	if len(t.Status.Held) == 0 {
		return
	}
	t.Status.Held = slices.DeleteFunc(t.Status.Held, func(h v1alpha1.HeldCommand) bool {
		verb := heldVerb(h.Command)
		var why string
		switch {
		case slices.Contains(t.Status.Handled, h.ID):
			narrateLater(t, narrate.CommandApplied(t, h.ID, verb))
			return true
		case slices.Contains(wasInPhase, h.ID) && !r.heldTarget(t, h.Command):
			why = "the task moved on without it (another answer, or the escalation's expiry, came first)"
		case v1alpha1.TerminalPhase(t.Status.Phase) && !r.heldTarget(t, h.Command):
			why = "the task ended " + strings.ToLower(t.Status.Phase) + " before it reached the phase the command applies in"
		case r.Now().Sub(h.At.Time) > heldTTL:
			why = "the task did not reach the phase it applies in within " + narrate.HeldFor
		default:
			return false
		}
		markHandled(t, h.ID)
		narrateLater(t, narrate.CommandExpired(t, h.ID, verb, h.Author, why))
		return true
	})
	if len(t.Status.Held) == 0 {
		t.Status.Held = nil
	}
}

// dropPlanHeld ends the held plan commands once a plan's review comment names them.
func dropPlanHeld(t *v1alpha1.Task) {
	t.Status.Held = slices.DeleteFunc(t.Status.Held, func(h v1alpha1.HeldCommand) bool {
		if heldFamily(h.Command) == "plan" {
			markHandled(t, h.ID)
			return true
		}
		return false
	})
	if len(t.Status.Held) == 0 {
		t.Status.Held = nil
	}
}

// refuseInput answers a command or review the factory does not act on, from someone without the
// standing to steer it: marked handled so it is answered once, and replied to at most once per
// person an hour on this task (refusalEvery), so a loop of such posts cannot make the factory talk
// on. what is the factory's name for it, never the person's text. A ghost (no login) and the
// factory's own login get no reply.
func (r *Reconciler) refuseInput(cfg *config.Config, t *v1alpha1.Task, id int64, login string, on int, what string) {
	markHandled(t, id)
	if login == "" || forge.SameLogin(login, cfg.FactoryLogin) || on == 0 {
		return
	}
	now := r.Now()
	i := slices.IndexFunc(t.Status.Refusals, func(x v1alpha1.RefusalReply) bool { return forge.SameLogin(x.Login, login) })
	if i >= 0 && now.Sub(t.Status.Refusals[i].At.Time) < refusalEvery {
		return
	}
	if i >= 0 {
		t.Status.Refusals = slices.Delete(t.Status.Refusals, i, i+1)
	}
	t.Status.Refusals = append(t.Status.Refusals, v1alpha1.RefusalReply{Login: login, At: metav1.NewTime(now)})
	if over := len(t.Status.Refusals) - maxRefusals; over > 0 {
		t.Status.Refusals = slices.Delete(t.Status.Refusals, 0, over)
	}
	narrateOn(t, on, narrate.NotActedOn(t, id, what))
}
