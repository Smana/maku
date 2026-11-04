# Audit T2, factory liveness: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No factory Task can hold a slot forever or go silent: Verifying and AwaitingCI are bounded,
main's checks are read whole, the outbox always writes, a dead issue never blocks narration, an
escalation after a retry is heard, a span retry never delays a room's close, a GraphQL merge refusal
is classified and a revert is never opened twice.

**Architecture:** Seven independent fixes in the factory, one task and one commit each (D0), each
pinned by a failing test first. The reconciler gains a bounded Verifying (D1), an outbox cap, a
permanent-failure path in `drain`, a retry-aware escalation key and a folded span requeue. The forge
gains paginated `CommitChecks` (D3), a `*PermanentError` class, GraphQL refusal classification on
`Merge` and an adopting `RevertPR`. The config defaults `merge.awaitingCIFor` to 24h (D2). F-1 and
F-11 match strings GitHub chooses, so each starts with the owner recording them live.

**Tech Stack:** Go (go.mod), go-github v92, shurcooL/githubv4 + shurcooL/graphql, controller-runtime
fake client and `interceptor`, `httptest`, stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-11-01-audit-fixes-design.md` (theme T2, Decisions D0 to D3,
"Open to the owner" rows for F-1 and F-11); findings in
`docs/superpowers/specs/2026-11-01-project-audit.md` (H-2, F-1, F-2, F-3, F-4, F-9, F-11).

## Spec deviations

Read against the code at `e14b7949` (branch `docs/audit-fixes-spec`). Each finding reproduces; where
the spec's fix direction does not fit the code, the corrected fix below is what the tasks implement.

| # | Spec says | The code shows | This plan |
|---|---|---|---|
| SD1 | D2 "in `config.ApplyDefaults`"; an explicit `0` keeps no bound | The factory config has no `ApplyDefaults`: defaults are applied inside `config.Parse` (`config.go:1018-1105`). `Merge.AwaitingCIFor` is a `Duration` value (`config.go:390`), which cannot tell "omitted" from `0s` | `AwaitingCIFor` becomes `*Duration`; `Parse` sets `24h` when it is nil; callers read `Merge.AwaitingCIBound()` (nil and `0s` both mean no bound, so a hand-built test `Config` keeps today's behaviour) |
| SD2 | H-2: D1 only touches `verifying` | `mainSignal` (`monitor.go:313-318`) counts every auto-merge `healthy` because "verifying watched it", and `webhook.Tracker` emits `room.merged` only for reasons `merged` / `merged_verified` (`internal/webhook/events.go:137`) | Task 1 also reads a `merged_unverified` auto-merge like a human merge in `mainSignal`, and adds `merged_unverified` to the webhook's merged reasons and `docs/api.md` |
| SD3 | D3: "a listing that reaches the cap reads as PENDING" | `CommitChecks` returns `[]Check`; `MainState` reads only the watched names, so a synthetic PENDING entry would be ignored | At the cap, every run read that is not `FAILURE` is returned with `State: "PENDING"`; a `FAILURE` read anywhere stays `FAILURE`. No caller changes |
| SD4 | F-2: "`narrateLater` merges entries with the same key" | `narrateOn` already skips a key queued or posted (`reconciler.go:572-577`) | Nothing to add for the key merge |
| SD5 | F-2: "`drain` runs before the pass's first status write" | `TestNothingIsPostedBeforeItsTransitionIsWritten` (`outbox_test.go:156`, review M-a) pins the opposite: a comment posts only once the write that queued it landed, since a conflicting write's replay may take another path | Dropped. The cap alone keeps the first write valid, and drain then runs as today. The cap drops the oldest entries **but an ending's comment last** (it carries the outcome and token total) |
| SD6 | F-2 pinning test: "fake client enforcing `maxItems`" | The controller-runtime fake does not validate CRD schemas | The test wraps the client with `interceptor.Funcs.SubResourceUpdate` refusing an outbox over 16 with `apierrors.NewInvalid`, as the API server would, and asserts the length directly |
| SD7 | F-3: `forge.Comment` returns a `*PermanentError` | `narrate.PostOnce` reads `RecentComments` (the marker search) before `Comment` (`narrate.go:67-85`): a deleted issue fails there first, with a 404 | `RecentComments` classifies the same way. A dropped key is recorded in `status.narrated` (`narrate.Abandon`), so a narration that fires again does not queue a post that can only fail again |
| SD8 | F-11: `RevertPR` lists `revert-<n>-` heads of the merger's open PRs | `RevertPR(ctx, nodeID, title, body)` knows neither the reverted PR's number nor the merger's login | The Merger method becomes `RevertPR(ctx, forge.RevertOf) (forge.Revert, error)`; `RevertOf{Number, NodeID, Title, Body, By}`; `PRSummary` gains `NodeID` and `URL` so the adopted revert needs no second read |
| SD9 | F-1: classify `errors[]` "alongside the existing HTTP checks" | shurcooL/graphql decodes only `errors[].message` (no `type`) and returns the first message as the error text (`graphql.go:108-119`); a non-200 answer is turned into a body-less `*statusError` by `statusRT` | The match is on the first message, by prefix, against strings recorded live, and only for an error that is not a `*url.Error` (transport and status errors). If the live recording is not HTTP 200, the task stops for the owner (Task 6 Step 1) |
| SD10 | F-9: "folds into the terminal block's `min()`" | `TestABlackHoledCollectorNeverStallsTheWorker` (`trace_test.go:260`) asserts `RequeueAfter == spanRetry` on a task that is still settling | The fold makes that requeue the settle's (sooner, as it should be); the assertion becomes "no later than `spanRetry`" |

## Global Constraints

- **Branch and worktree:** `fix/audit-t2-factory-liveness` in its own worktree, from a freshly fetched
  `origin/main` (use `superpowers:using-git-worktrees`):
  `git -C /home/smana/Sources/maku worktree add /home/smana/Sources/maku-audit-t2 -b fix/audit-t2-factory-liveness origin/main`.
  If the spec PR (`docs/audit-fixes-spec`) is not merged yet, branch from it instead. Never `git add -A`:
  `.claude/` is untracked in the main checkout.
- **D0, verbatim:** "One PR per theme, each finding one commit with the test that pins it first; `task check` exits 0 on every PR".
- **D1, verbatim:** "Verifying ends `Done` with reason `merged_unverified` once `2 × merge.verifyFor` has passed since the merge with no FAILURE and no qualifying SUCCESS, and narrates the merge SHA and the revert window. A CANCELLED check keeps meaning "keep watching" until then".
- **D2, verbatim:** "`merge.awaitingCIFor` defaults to `24h`; an explicit `0` keeps "no bound"".
- **D3, verbatim:** "`forge.CommitChecks` paginates up to 10 pages of 100; a listing that reaches the cap reads as PENDING, never SUCCESS".
- **Out of scope (spec):** re-running cancelled checks (D1); splitting `awaitingCI` or `config.go`.
- **AGENTS.md:** `// SPDX-License-Identifier: Apache-2.0` first line of every new `.go` file; stdlib
  `testing` only, table-driven `t.Run`, hand-written fakes, controller-runtime's fake client (with
  `interceptor`) for the reconciler, `httptest` for HTTP; injected clocks (`g.r.Now`); `-race` always;
  errors wrap with `%w`, compared with `errors.Is`/`errors.As`; logs carry ids and keys, never a payload.
- **Test command shape:** `go test -race -count=1 ./internal/factory/<pkg>/ -run '<Names>'`. envtest
  cases skip without `KUBEBUILDER_ASSETS`; `task test` sets it.
- **Before every commit:** `task check` exits 0 (AGENTS.md). Store tests need a running Docker daemon.
  No CRD or API type changes in this plan, so `task crd:check` (part of `task check`) must stay clean.
- **Commits:** Conventional Commits in the repository's style (`fix(factory): …`, `fix(forge): …`),
  English, **no** `Co-Authored-By` and **no** "Generated with" line, in the commit or the PR.
- **Owner-blocked steps:** Tasks 6 and 7 start with a live recording only the owner can make (network,
  GitHub writes). An executing agent stops at that step and reports; it never runs those commands.

## File structure

| File | Task | Change | Responsibility |
|---|---|---|---|
| `internal/factory/reconciler/merge.go` | 1, 7 | modify | `verifying` bounded (D1); `AwaitingCIBound()` read; `revert` passes `forge.RevertOf` |
| `internal/factory/reconciler/monitor.go` | 1 | modify | `mainSignal` reads a `merged_unverified` auto-merge |
| `internal/factory/reconciler/reconciler.go` | 2, 3, 5 | modify | `capOutbox`, `maxOutbox`; `drain` drops a permanent refusal; `soonest` and the folded span requeue |
| `internal/factory/reconciler/watch.go` | 4 | modify | `escalated` answers a budget-task retry |
| `internal/factory/narrate/narrate.go` | 1, 3, 4 | modify | `merged_unverified` prose, `MergedUnverified`; `Abandon`; escalation key with retries, `TaskCapHolds` |
| `internal/factory/config/config.go` | 1 | modify | `AwaitingCIFor *Duration`, `AwaitingCIBound()`, `defaultAwaitingCIFor`, the default in `Parse` |
| `internal/factory/forge/forge.go` | 3, 7 | modify | `PermanentError`; `RevertOf`; `PRSummary.NodeID`, `URL`; `Merger.RevertPR` signature |
| `internal/factory/forge/github.go` | 1, 3, 6, 7 | modify | `CommitChecks` pages (D3); `permanent`; `mergeRefusals`; `RevertPR` adopts |
| `internal/factory/forge/absent.go`, `forgetest/fake.go` | 7 | modify | `RevertPR(ctx, RevertOf)` |
| `internal/factory/forge/testdata/merge-*.json`, `revert-naming.json` | 6, 7 | create | live-recorded GitHub answers (owner) and one synthetic refusal |
| `internal/webhook/events.go` | 1 | modify | `merged_unverified` is a merge for `room.merged` |
| Tests: `reconciler/verifying_test.go` (create), `reconciler/cibound_test.go`, `reconciler/outbox_test.go`, `reconciler/watch_test.go`, `reconciler/team_test.go`, `reconciler/trace_test.go`, `reconciler/merge_test.go`, `config/config_test.go`, `forge/github_test.go`, `webhook/events_test.go` | 1–7 | modify | the pinning tests |
| `docs/factory.md`, `docs/api.md` | 1–4 | modify | reasons, defaults, the Verifying edge, narration's drops, the budget-task retry |

---

### Task 1: H-2, Verifying and AwaitingCI are bounded, main's checks are read whole (D1, D2, D3)

**Files:**
- Modify: `internal/factory/reconciler/merge.go:185` (`awaitingCI` wait arm), `merge.go:902-927` (`verifying`)
- Modify: `internal/factory/reconciler/monitor.go:310-318` (`mainSignal`)
- Modify: `internal/factory/narrate/narrate.go:270` (reasons), after `Ended` (`:797`)
- Modify: `internal/factory/config/config.go:46-66` (consts), `:388-390` (`Merge`), `:1076-1082` (`Parse`), `:1551` (`validateRepository`)
- Modify: `internal/factory/forge/github.go:929-944` (`CommitChecks`)
- Modify: `internal/webhook/events.go:137`
- Create: `internal/factory/reconciler/verifying_test.go`
- Modify tests: `internal/factory/reconciler/cibound_test.go`, `internal/factory/config/config_test.go:197-217`, `internal/factory/forge/github_test.go` (check-runs route, new test), `internal/webhook/events_test.go`
- Modify docs: `docs/factory.md`, `docs/api.md`

**Interfaces:**
- Produces: reason `merged_unverified`; `narrate.MergedUnverified(t *v1alpha1.Task, revertUntil time.Time) narrate.Event`;
  `config.Merge.AwaitingCIFor *config.Duration`; `func (m config.Merge) AwaitingCIBound() time.Duration`;
  `forge.maxCheckPages = 10` (unexported).

- [ ] **Step 1: Write the failing tests.**

Create `internal/factory/reconciler/verifying_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// D1 (H-2): main's CI on the merge commit decides within twice merge.verifyFor. A cancelled or
// unfinished check keeps the task watching until then; past it the merge is done unverified,
// narrated once with its merge commit and the revert window. Red still reverts, green still verifies.
func TestVerifyingIsBounded(t *testing.T) {
	for name, c := range map[string]struct {
		second        string // required[1]'s state on main; required[0] is green
		after         time.Duration
		phase, reason string
	}{
		"cancelled, inside twice verifyFor": {"CANCELLED", 59 * time.Minute, v1alpha1.PhaseVerifying, ""},
		"cancelled, at twice verifyFor":     {"CANCELLED", time.Hour, v1alpha1.PhaseDone, "merged_unverified"},
		"still running past it":             {"PENDING", 2 * time.Hour, v1alpha1.PhaseDone, "merged_unverified"},
		"red past it":                       {"FAILURE", 2 * time.Hour, v1alpha1.PhaseReverted, "main_red"},
		"green past verifyFor":              {"SUCCESS", 31 * time.Minute, v1alpha1.PhaseDone, "merged_verified"},
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseVerifying)
			g.patch(t, func(tk *v1alpha1.Task) {
				merged := metav1.NewTime(now)
				ref := tk.Status.PullRequest
				ref.MergeCommitSHA, ref.AutoMerged, ref.MergedAt = "m1", true, &merged
			})
			g.f.SetCommitChecks("m1", forge.Check{Name: required[0], State: "SUCCESS"}, forge.Check{Name: required[1], State: c.second})
			g.r.Now = func() time.Time { return now.Add(c.after) }
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != c.phase || tk.Status.Reason != c.reason {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
			if c.reason != "merged_unverified" {
				return
			}
			settled := now.Add(c.after + g.r.settleWindow()) // the closing comment waits for the usage (#42)
			g.r.Now = func() time.Time { return settled }
			g.reconcile(t, "3buqdlot", 3)
			n := 0
			for _, b := range g.f.Comments(7) {
				if strings.Contains(b, "Merge commit `m1`") && strings.Contains(b, "`factory/revert` before 2026-10-04 10:00 UTC") {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("the unverified end is narrated once, with its commit and the revert window: %q", g.f.Comments(7))
			}
		})
	}
}

// D1: an auto-merge done unverified was never seen green, so the monitor reads its merge commit
// like any other merge's instead of counting it healthy.
func TestTheMonitorReadsAnUnverifiedAutoMerge(t *testing.T) {
	g := monitorRig(t)
	cancelled := green("SUCCESS").Runs
	for i := range cancelled {
		cancelled[i].State = "CANCELLED"
	}
	g.f.SetCommitChecks(mSHA, cancelled...)
	tk := g.reconcile(t, "3buqdlot", 0)
	tk.Status.PullRequest.AutoMerged = true
	tk.Status.Monitor = &v1alpha1.MonitorStatus{Since: metav1.NewTime(now.Add(-2 * time.Hour))}
	for reason, want := range map[string]string{"merged_verified": "healthy", "merged_unverified": "inconclusive"} {
		tk.Status.Reason = reason
		if got, err := g.r.mainSignal(t.Context(), tk); err != nil || got != want {
			t.Fatalf("%s: %q %v, want %q", reason, got, err, want)
		}
	}
}
```

In `internal/factory/config/config_test.go`, replace `TestAwaitingCIForZeroOrOmittedIsOff` (l.197-217) with:

```go
// D2: awaitingCIFor left out bounds the wait at a day; written 0s keeps no bound; written, it is read.
func TestAwaitingCIForDefaultsToADay(t *testing.T) {
	for name, c := range map[string]struct {
		raw  string
		want time.Duration
	}{
		"omitted": {good, 24 * time.Hour},
		"zero":    {strings.Replace(good, "revertWindow: 168h", "revertWindow: 168h\n  awaitingCIFor: 0s", 1), 0},
		"set":     {strings.Replace(good, "revertWindow: 168h", "revertWindow: 168h\n  awaitingCIFor: 6h", 1), 6 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			if name != "omitted" && c.raw == good {
				t.Fatal("the edit did not apply")
			}
			cfg, err := Parse([]byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if d := cfg.Repos()[0].Merge.AwaitingCIBound(); d != c.want {
				t.Fatalf("awaitingCIFor = %v, want %v", d, c.want)
			}
		})
	}
}
```

In `internal/factory/forge/github_test.go`, replace the check-runs route (l.368-370) with a paging one:

```go
	// Main's check runs: one green run for any commit, but "paged" lists 100 green runs and a
	// failure on page 2, and "endless" lists full pages of green runs past any cap (D3).
	mux.HandleFunc("GET /repos/Smana/demo/commits/{sha}/check-runs", g.authed(func(w http.ResponseWriter, r *http.Request) {
		sha := r.PathValue("sha")
		if sha != "paged" && sha != "endless" {
			_, _ = io.WriteString(w, `{"total_count":1,"check_runs":[{"name":"Pre-commit checks","status":"completed","conclusion":"success"}]}`)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		page = max(page, 1)
		if sha == "endless" || page == 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/Smana/demo/commits/%s/check-runs?per_page=100&page=%d>; rel="next"`, g.srv.URL, sha, page+1))
		}
		n, conclusion := 100, "success"
		if sha == "paged" && page == 2 {
			n, conclusion = 1, "failure"
		}
		runs := make([]string, 0, n)
		for i := range n {
			runs = append(runs, fmt.Sprintf(`{"id":%d,"name":"job %d-%d","status":"completed","conclusion":%q}`, page*1000+i, page, i, conclusion))
		}
		_, _ = fmt.Fprintf(w, `{"total_count":5000,"check_runs":[%s]}`, strings.Join(runs, ","))
	}))
```

and add the test:

```go
// D3 (H-2): main's checks are read to the last page, up to ten pages of 100: a failure on page 2
// is seen, and a listing still going at the cap reads as running, never as green.
func TestCommitChecksReadsEveryPageUpToTheCap(t *testing.T) {
	r := newRig(t)
	runs, err := r.g.CommitChecks(t.Context(), "paged")
	if err != nil || len(runs) != 101 || runs[100].State != "FAILURE" {
		t.Fatalf("%d runs, %v: the failure on page 2 is read", len(runs), err)
	}
	if runs, err = r.g.CommitChecks(t.Context(), "endless"); err != nil || len(runs) != 1000 {
		t.Fatalf("%d runs, %v: ten pages, then the cap", len(runs), err)
	}
	for _, c := range runs {
		if c.State != "PENDING" {
			t.Fatalf("a capped listing is never green: %+v", c)
		}
	}
}
```

In `internal/webhook/events_test.go`, add:

```go
// D1: an auto-merge done unverified is merged all the same: it says room.merged.
func TestAnUnverifiedMergeEmitsRoomMerged(t *testing.T) {
	var tr Tracker
	got := tr.Events("room0001", "r", fact(1, envelope.TaskFacts{Phase: "Done", Reason: "merged_unverified", PR: &envelope.PRFact{Number: 9}}))
	if !slices.ContainsFunc(got, func(e Event) bool { return e.Type == "room.merged" && e.PR == 9 }) {
		t.Fatalf("want room.merged pr 9: %+v", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail.**

```bash
go test -race -count=1 ./internal/factory/reconciler/ -run 'TestVerifyingIsBounded|TestTheMonitorReadsAnUnverifiedAutoMerge'
go test -race -count=1 ./internal/factory/config/ -run TestAwaitingCIForDefaultsToADay
go test -race -count=1 ./internal/factory/forge/ -run TestCommitChecksReadsEveryPageUpToTheCap
go test -race -count=1 ./internal/webhook/ -run TestAnUnverifiedMergeEmitsRoomMerged
```

Expected: reconciler FAIL, `cancelled, at twice verifyFor: Verifying` (and `still running past it`)
since nothing bounds Verifying, and `merged_unverified: "healthy" <nil>, want "inconclusive"`. config
FAIL to compile, `cfg.Repos()[0].Merge.AwaitingCIBound undefined`. forge FAIL, `100 runs, <nil>: the
failure on page 2 is read` (one page today). webhook FAIL, `want room.merged pr 9: []`.

- [ ] **Step 3: Implement.**

`internal/factory/config/config.go`, in the `const` block after `defaultProbeAuthor = "renovate[bot]"`:

```go
	// D2: a wait verdict in AwaitingCI is bounded unless the config writes 0s.
	defaultAwaitingCIFor = 24 * time.Hour
```

`Merge.AwaitingCIFor` (l.388-390) becomes:

```go
	// AwaitingCIFor bounds a pull request's stay in AwaitingCI on a wait verdict (#182): past it,
	// a human is told which checks never reported. Omitted, Parse sets 24h (D2); 0s is no bound.
	// Read it through AwaitingCIBound.
	AwaitingCIFor *Duration `json:"awaitingCIFor,omitempty"`
```

and after the `Merge` struct:

```go
// AwaitingCIBound is AwaitingCIFor as a bound: 0 is none, whether written 0s or left nil on a
// Config built without Parse.
func (m Merge) AwaitingCIBound() time.Duration {
	if m.AwaitingCIFor == nil {
		return 0
	}
	return m.AwaitingCIFor.Duration
}
```

In `Parse`, the loop over repositories (l.1076) becomes:

```go
	for i := range c.Repositories {
		if m := &c.Repositories[i].Merge; m.AwaitingCIFor == nil {
			m.AwaitingCIFor = &Duration{Duration: defaultAwaitingCIFor} // D2: an unbounded wait holds a slot forever
		}
		for j := range c.Repositories[i].Schedules {
```

and in `validateRepository` (l.1551) `if r.Merge.AwaitingCIFor.Duration < 0 {` becomes
`if r.Merge.AwaitingCIBound() < 0 {`.

`internal/factory/reconciler/merge.go:185`: `if b := r.repo(t).Merge.AwaitingCIFor.Duration; b > 0 &&`
becomes `if b := r.repo(t).Merge.AwaitingCIBound(); b > 0 &&`. In
`internal/factory/reconciler/cibound_test.go`, each of the four `g.r.Cfg.Merge.AwaitingCIFor.Duration = X`
lines becomes `g.r.Cfg.Merge.AwaitingCIFor = &config.Duration{Duration: X}` (`c.bound` in the table,
`time.Hour` in the three others), and the import block gains
`"github.com/Smana/maku/internal/factory/config"`.

`verifying` (`merge.go:902-927`) becomes:

```go
// verifying watches main's CI on the merge commit for merge.verifyFor (§6.4). Red, or a
// maintainer's factory/revert, reverts; not red for the whole window, with nothing still running,
// is done. A check cancelled on main (cancel-in-progress) or one that never finishes gives no
// verdict: past twice verifyFor the merge is done unverified (D1), which frees the task's slot and
// leaves the revert to a maintainer's factory/revert within revertWindow.
func (r *Reconciler) verifying(ctx context.Context, t *v1alpha1.Task) error {
	if t.Annotations[v1alpha1.AnnotationRevert] != "" {
		return r.revert(ctx, t, "revert_requested")
	}
	ref, mg := t.Status.PullRequest, r.repo(t).Merge
	runs, err := r.merger(ctx).CommitChecks(ctx, ref.MergeCommitSHA)
	if err != nil {
		return err
	}
	switch MainState(runs, mg.VerifyChecks) {
	case "FAILURE":
		return r.revert(ctx, t, "main_red")
	case "SUCCESS":
		if ref.MergedAt != nil && r.Now().Sub(ref.MergedAt.Time) >= mg.VerifyFor.Duration {
			return r.end(ctx, t, v1alpha1.PhaseDone, "merged_verified")
		}
	}
	since := ref.MergedAt
	if since == nil {
		since = t.Status.PhaseSince // a status without MergedAt still has the phase's clock
	}
	if since != nil && r.Now().Sub(since.Time) >= 2*mg.VerifyFor.Duration {
		r.finish(ctx, t, v1alpha1.PhaseDone, "merged_unverified", narrate.MergedUnverified(t, since.Add(mg.RevertWindow.Duration)))
	}
	return nil
}
```

`internal/factory/reconciler/monitor.go`, `mainSignal`'s comment's first sentence becomes "An
auto-merge's was watched by verifying, which reached Done "merged_verified": healthy; one done
"merged_unverified" (D1) was never seen green, and is read like any other merge." and its guard:

```go
	if ref.AutoMerged && t.Status.Reason != "merged_unverified" {
		return "healthy", nil
	}
```

`internal/factory/narrate/narrate.go`, in `reasons()` after `"merged_verified"`:

```go
		"merged_unverified":         "merged, and main's CI on the merge commit gave no result within twice merge.verifyFor (a check was cancelled or never finished)",
```

and after `Ended`:

```go
// MergedUnverified is the end of an auto-merge whose merge commit got no result from main's CI
// within twice merge.verifyFor (D1): it names the commit, and until when a maintainer's
// factory/revert still reverts it. It is Ended's event (its key, its token line), so it settles.
func MergedUnverified(t *v1alpha1.Task, revertUntil time.Time) Event {
	e := Ended(t, v1alpha1.PhaseDone, "merged_unverified")
	ref := t.Status.PullRequest
	note := fmt.Sprintf(" Merge commit `%s`: main's CI gave it no result, so nothing shows it green or red. "+
		"Label #%d `factory/revert` before %s to revert it.", ref.MergeCommitSHA, ref.Number, revertUntil.UTC().Format("2006-01-02 15:04 UTC"))
	if i := strings.LastIndex(e.Body, usageLine); i >= 0 {
		e.Body = e.Body[:i] + note + e.Body[i:]
	}
	return e
}
```

`internal/factory/forge/github.go`, `CommitChecks` (l.929-944) becomes:

```go
// maxCheckPages bounds CommitChecks: ten pages of 100 check runs (D3).
const maxCheckPages = 10

// CommitChecks are the check runs on one commit: main's CI after a merge, where no pull request
// carries the verdict anymore. Every page is read, up to maxCheckPages (D3). A listing still going
// at the cap returns each run it read that is not FAILURE as PENDING: the commit reads as still
// running, never green, while a failure read anywhere stays a failure.
func (g *GitHub) CommitChecks(ctx context.Context, sha string) ([]Check, error) {
	var out []Check
	opt := &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for page := 1; ; page++ {
		res, resp, err := g.rest.Checks.ListCheckRunsForRef(ctx, g.owner, g.name, sha, opt)
		g.mark(err)
		if err != nil {
			return nil, wrap("list check runs", err)
		}
		for _, cr := range res.CheckRuns {
			out = append(out, Check{ID: cr.GetID(), Name: cr.GetName(), State: checkState(strings.ToUpper(cr.GetStatus()), strings.ToUpper(cr.GetConclusion())),
				Conclusion: strings.ToUpper(cr.GetConclusion())})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		if page == maxCheckPages {
			for i := range out {
				if out[i].State != "FAILURE" {
					out[i].State = "PENDING"
				}
			}
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}
```

`internal/webhook/events.go:137`: `if !t.mergedSent && (f.Reason == "merged" || f.Reason == "merged_verified") {`
becomes `if !t.mergedSent && slices.Contains([]string{"merged", "merged_verified", "merged_unverified"}, f.Reason) {`.

Docs:
- `docs/factory.md` state diagram, after `  Verifying --> Done: main green for merge.verifyFor` add
  `  Verifying --> Done: no result within twice merge.verifyFor`.
- `docs/factory.md:352`, after the `merged_verified` entry insert: `` `merged_unverified` (auto-merged,
  main's CI gave no result within twice `merge.verifyFor`, a check cancelled or never finished; the
  comment names the merge commit, and `factory/revert` still reverts it within `merge.revertWindow`), ``
- `docs/factory.md:756`: "`holdout_pending`; one that never reports leaves the task waiting (issue
  #182)." becomes "`holdout_pending`; one that never reports holds it for `merge.awaitingCIFor` (24h
  unless set, `0s` for no bound), then hands it off as `ci_wait_expired` (issue #182)."
- `docs/factory.md:759`: after "red reverts, green ends `Done`." add "No result within twice
  `merge.verifyFor` (a check cancelled, as `cancel-in-progress` does on main, or never finished) ends
  `Done` `merged_unverified`."
- `docs/factory.md:1034`: the comment becomes `# bound AwaitingCI on a wait verdict; 24h when omitted, 0s is no bound`.
- `docs/factory.md:1049`: "An auto-merge's was already watched by the task, so it counts healthy"
  becomes "An auto-merge that ended `merged_verified` was already watched by the task, so it counts
  healthy; one that ended `merged_unverified` is read like any other merge".
- `docs/factory.md:1354`: "(`awaitingCIFor`, `maxFollowUps` and `recoverAfter` optional, 0 is off;"
  becomes "(`awaitingCIFor` optional, 24h when omitted, `0s` for no bound; `maxFollowUps` and
  `recoverAfter` optional, 0 is off;".
- `docs/api.md:678`: "or its facts carry reason `merged` or `merged_verified` (a human merge)" becomes
  "or its facts carry reason `merged` (a human merge), `merged_verified` or `merged_unverified`".

- [ ] **Step 4: Run the packages.**

```bash
go test -race -count=1 ./internal/factory/reconciler/ ./internal/factory/config/ ./internal/factory/forge/... \
  ./internal/factory/narrate/ ./internal/factory/fmetrics/ ./internal/webhook/
```

Expected: `ok` for each. `TestVerifyingNeverReadsCancelledAsGreen` (31 min) and `TestAwaitingCIIsBounded`
still pass; fmetrics' reason walk picks up `merged_unverified` through `narrate.Reasons()`.

- [ ] **Step 5: Gate and commit.**

```bash
task check
git add internal/factory/reconciler/merge.go internal/factory/reconciler/monitor.go internal/factory/reconciler/verifying_test.go \
  internal/factory/reconciler/cibound_test.go internal/factory/narrate/narrate.go internal/factory/config/config.go \
  internal/factory/config/config_test.go internal/factory/forge/github.go internal/factory/forge/github_test.go \
  internal/webhook/events.go internal/webhook/events_test.go docs/factory.md docs/api.md
git commit -m "fix(factory): a merge main never judges ends merged_unverified, AwaitingCI waits a day by default, main's checks are read whole (H-2)"
```

### Task 2: F-2, the outbox never outgrows the CRD

**Files:**
- Modify: `internal/factory/reconciler/reconciler.go:250-251` (the pass's first status write), after `drain` (`:597`)
- Test: `internal/factory/reconciler/outbox_test.go`
- Docs: `docs/factory.md` (Narration, l.364)

**Interfaces:**
- Produces: `const maxOutbox = 16`; `func (r *Reconciler) capOutbox(t *v1alpha1.Task)`; log message `narration_dropped` with `task.id`, `key`, `why`.
- Consumes: `monitorRig`, `later`, `FollowUpOfLabel` (monitor tests), `flakyForge` (outbox_test).

- [ ] **Step 1: Write the failing test.** In `outbox_test.go` add `"k8s.io/apimachinery/pkg/util/validation/field"` to the imports, then:

```go
// F-2: a monitor pass that finds 9 follow-ups queues 18 comments (the merge's and the control
// issue's copy of each), over the CRD's 16. The status write must still be accepted, the outbox at
// 16, or the task wedges: every write refused, so the outbox is never drained.
func TestAMonitorPassWithManyFollowUpsStillWrites(t *testing.T) {
	g := monitorRig(t)
	// The API server enforces status.outbox's maxItems; the fake client does not, so this does.
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if tk, ok := o.(*v1alpha1.Task); ok && len(tk.Status.Outbox) > 16 {
			return apierrors.NewInvalid(schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "Task"}, tk.Name,
				field.ErrorList{field.TooMany(field.NewPath("status", "outbox"), len(tk.Status.Outbox), 16)})
		}
		return cl.SubResource(sub).Update(ctx, o, opts...)
	}})
	g.r.Forge = &flakyForge{Fake: g.f, down: true} // nothing posts: the outbox stays as the write left it
	var ps []forge.MergedPull
	for n := 20; n < 29; n++ {
		ps = append(ps, later(n, "Smana", "fix", FollowUpOfLabel(12)))
	}
	g.f.SetMerged(ps...)
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); apierrors.IsInvalid(err) {
		t.Fatalf("the status write is refused: %v", err)
	}
	tk := g.reconcile(t, "3buqdlot", 0)
	if tk.Status.Monitor == nil || len(tk.Status.Monitor.FollowUps) != 9 || len(tk.Status.Outbox) != 16 {
		t.Fatalf("monitor %+v, outbox of %d", tk.Status.Monitor, len(tk.Status.Outbox))
	}
}
```

- [ ] **Step 2: Run it.** `go test -race -count=1 ./internal/factory/reconciler/ -run TestAMonitorPassWithManyFollowUpsStillWrites`
  Expected: FAIL, `the status write is refused: … status.outbox: Too many: 18: must have at most 16 items`.

- [ ] **Step 3: Implement.** In `reconciler.go`, the pass's first write:

```go
	if !equality.Semantic.DeepEqual(*before, t.Status) {
		r.capOutbox(&t)
		if uerr := r.Client.Status().Update(ctx, &t); uerr != nil {
```

and after `drain`:

```go
// maxOutbox is status.outbox's CRD maxItems: a status write carrying more is refused whole.
const maxOutbox = 16

// capOutbox keeps the outbox within maxOutbox before the pass's status write (F-2): past it every
// write is refused, and drain, which posts only a written outbox, never runs, so the task wedges.
// The oldest entries go first, an ending's comment last: it carries the outcome and the token total.
func (r *Reconciler) capOutbox(t *v1alpha1.Task) {
	for len(t.Status.Outbox) > maxOutbox {
		i := max(slices.IndexFunc(t.Status.Outbox, func(o v1alpha1.Narration) bool { return !narrate.Closing(o.Key) }), 0)
		r.log().Warn("narration_dropped", "task.id", t.Name, "key", t.Status.Outbox[i].Key, "why", "outbox_full")
		t.Status.Outbox = slices.Delete(t.Status.Outbox, i, i+1)
	}
}
```

`docs/factory.md:365`, after "from an outbox written with the transition that caused it." add: "The
outbox holds 16 comments: past that the oldest are dropped, an ending's last, and logged
`narration_dropped`."

- [ ] **Step 4: Run.** `go test -race -count=1 ./internal/factory/reconciler/ -run 'TestAMonitorPassWithManyFollowUpsStillWrites|TestFollowUps|Outbox|Posted|Narrat'`
  Expected: PASS (`TestNothingIsPostedBeforeItsTransitionIsWritten` included).

- [ ] **Step 5: Gate and commit.**

```bash
task check
git add internal/factory/reconciler/reconciler.go internal/factory/reconciler/outbox_test.go docs/factory.md
git commit -m "fix(factory): the outbox is capped at the CRD's 16, so a monitor pass with many follow-ups still writes (F-2)"
```

### Task 3: F-3, a comment GitHub refuses for good no longer blocks the rest

**Files:**
- Modify: `internal/factory/forge/forge.go` (after the sentinels, l.56), `internal/factory/forge/github.go:181-230` (`Comment`, `RecentComments`)
- Modify: `internal/factory/narrate/narrate.go` (after `record`, l.108)
- Modify: `internal/factory/reconciler/reconciler.go:579-596` (`drain`)
- Test: `internal/factory/forge/github_test.go`, `internal/factory/reconciler/outbox_test.go`
- Docs: `docs/factory.md` (Narration)

**Interfaces:**
- Produces: `type forge.PermanentError struct{ Status int; Err error }` with `Error()` and `Unwrap()`;
  `func narrate.Abandon(t *v1alpha1.Task, key string)`; unexported `forge.permanent(what string, err error) error`.

- [ ] **Step 1: Write the failing tests.** In `github_test.go`, the issue-comments GET route
  (l.435) starts with:

```go
		if r.PathValue("n") == "404" { // a deleted issue
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
```

and a POST route joins the mux (the literal `issues/7/comments` route stays, as the more specific):

```go
	// GitHub's refusals of a comment: for good (deleted, issues off, locked) or not (403 otherwise, 5xx).
	mux.HandleFunc("POST /repos/Smana/demo/issues/{n}/comments", g.authed(func(w http.ResponseWriter, r *http.Request) {
		refusal := map[string]struct {
			code    int
			message string
		}{
			"404": {http.StatusNotFound, "Not Found"},
			"410": {http.StatusGone, "Issues are disabled for this repo"},
			"423": {http.StatusForbidden, "Unable to create comment because issue is locked."},
			"403": {http.StatusForbidden, "Resource not accessible by integration"},
			"502": {http.StatusBadGateway, "Server Error"},
		}[r.PathValue("n")]
		if refusal.code == 0 {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":2}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(refusal.code)
		_, _ = fmt.Fprintf(w, `{"message":%q}`, refusal.message)
	}))
```

and the test:

```go
// F-3: GitHub refusing a comment for good (the issue deleted, issues turned off, the issue locked)
// is a *PermanentError, on the marker read as on the post; a 403 that is no lock, or a 5xx, is not.
func TestACommentRefusedForGoodIsPermanent(t *testing.T) {
	r := newRig(t)
	for name, c := range map[string]struct {
		n         int
		permanent bool
	}{
		"deleted": {404, true}, "issues off": {410, true}, "locked": {423, true},
		"forbidden otherwise": {403, false}, "a server error": {502, false},
	} {
		t.Run(name, func(t *testing.T) {
			var pe *PermanentError
			if err := r.g.Comment(t.Context(), c.n, "hello"); err == nil || errors.As(err, &pe) != c.permanent {
				t.Fatalf("%v", err)
			}
		})
	}
	var pe *PermanentError
	if _, err := r.g.RecentComments(t.Context(), 404); !errors.As(err, &pe) || pe.Status != http.StatusNotFound {
		t.Fatalf("the marker read of a deleted issue: %v", err)
	}
}
```

In `outbox_test.go`:

```go
// goneForge is GitHub with issue gone deleted: every read or write of it is refused for good (F-3).
type goneForge struct {
	*forgetest.Fake
	gone int
}

func (f *goneForge) RecentComments(ctx context.Context, n int) ([]forge.Comment, error) {
	if n == f.gone {
		return nil, &forge.PermanentError{Status: 404, Err: errors.New("forge: list comments: 404 Not Found")}
	}
	return f.Fake.RecentComments(ctx, n)
}

func (f *goneForge) Comment(ctx context.Context, n int, body string) error {
	if n == f.gone {
		return &forge.PermanentError{Status: 404, Err: errors.New("forge: comment: 404 Not Found")}
	}
	return f.Fake.Comment(ctx, n, body)
}

// F-3: a comment GitHub refuses for good (here its issue was deleted) is dropped and recorded; the
// comments after it still post, the outbox empties and the ended task's room closes.
func TestANarrationRefusedForGoodIsDropped(t *testing.T) {
	tk := endedTask(v1alpha1.PhaseDone)
	tk.Status.Outbox = []v1alpha1.Narration{{Key: "to-the-deleted-issue", Number: 9, Body: "lost"}, {Key: "to-the-issue", Number: 7, Body: "kept"}}
	g := newRig(t, tk)
	withRoom(t, g)
	g.r.Forge = &goneForge{Fake: g.f, gone: 9}
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil {
		t.Fatalf("a permanent refusal is not retried: %v", err)
	}
	got := g.reconcile(t, "3buqdlot", 0)
	if len(got.Status.Outbox) != 0 || !contains(g.f.Comments(7), "kept") || !slices.Contains(got.Status.Narrated, "to-the-deleted-issue") {
		t.Fatalf("outbox %+v, narrated %v, comments %q", got.Status.Outbox, got.Status.Narrated, g.f.Comments(7))
	}
	if !roomGone(t, g) {
		t.Fatal("the room waits on a comment GitHub will never take")
	}
}
```

(`outbox_test.go` gains `"slices"` in its imports.)

- [ ] **Step 2: Run them.**

```bash
go test -race -count=1 ./internal/factory/forge/ -run TestACommentRefusedForGoodIsPermanent
go test -race -count=1 ./internal/factory/reconciler/ -run TestANarrationRefusedForGoodIsDropped
```

Expected: both FAIL to compile, `undefined: PermanentError` / `undefined: forge.PermanentError`.

- [ ] **Step 3: Implement.** `forge.go`, after `ErrMergedTruncated`:

```go
// PermanentError is GitHub refusing a write for good: the issue or pull request is gone (404, 410)
// or locked (a 403 saying so). A retry gets the same answer, so the caller drops the write (F-3).
type PermanentError struct {
	Status int
	Err    error
}

func (e *PermanentError) Error() string { return fmt.Sprintf("%v (permanent, %d)", e.Err, e.Status) }

// Unwrap returns the refusal as the client reported it.
func (e *PermanentError) Unwrap() error { return e.Err }
```

(`forge.go` imports `fmt` if it does not already.) `github.go`, beside `wrap`:

```go
// permanent is wrap, except that a refusal GitHub will repeat forever is a *PermanentError: the
// issue gone (404, 410) or locked (a 403 whose message says so; a rate limit's 403 is go-github's
// own RateLimitError or AbuseRateLimitError, never an ErrorResponse).
func permanent(what string, err error) error {
	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		switch code := er.Response.StatusCode; {
		case code == http.StatusNotFound, code == http.StatusGone,
			code == http.StatusForbidden && strings.Contains(strings.ToLower(er.Message), "locked"):
			return &PermanentError{Status: code, Err: wrap(what, err)}
		}
	}
	return wrap(what, err)
}
```

`Comment` returns `permanent("comment", err)` instead of `wrap("comment", err)`, and `RecentComments`'
`page` returns `nil, nil, permanent("list comments", err)` instead of `wrap(...)`.

`narrate.go`, after `record`:

```go
// Abandon records key as narrated without a post: the forge refused it for good (F-3), and a
// narration that fires again must not queue a post that can only be refused again.
func Abandon(t *v1alpha1.Task, key string) { record(t, key) }
```

`drain` becomes:

```go
// drain posts the outbox in order and stops at the first failure, which the next reconcile
// retries. A post that landed but failed is found by its marker then, not posted twice. A post
// GitHub refuses for good (F-3) is dropped: it would hold back every later one and the room's close.
func (r *Reconciler) drain(ctx context.Context, t *v1alpha1.Task) error {
	for len(t.Status.Outbox) > 0 {
		o := t.Status.Outbox[0]
		if narrate.Closing(o.Key) && r.settling(t) {
			break // its token total is final only once the usage settles (#42); settle requeues
		}
		err := r.narrator(ctx).Post(ctx, t, o.Number, narrate.Event{Key: o.Key, Body: o.Body})
		var pe *forge.PermanentError
		switch {
		case errors.As(err, &pe):
			r.log().Warn("narration_dropped", "task.id", t.Name, "key", o.Key, "number", o.Number, "why", "refused", "status", pe.Status)
			narrate.Abandon(t, o.Key)
		case err != nil:
			return err
		}
		t.Status.Outbox = t.Status.Outbox[1:]
	}
	if len(t.Status.Outbox) == 0 {
		t.Status.Outbox = nil
	}
	return nil
}
```

`docs/factory.md`, after Task 2's sentence: "A comment GitHub refuses for good (its issue deleted,
issues turned off, or the issue locked) is dropped and logged the same way, and the comments after
it still post."

- [ ] **Step 4: Run.**

```bash
go test -race -count=1 ./internal/factory/forge/... ./internal/factory/narrate/ ./internal/factory/intake/
go test -race -count=1 ./internal/factory/reconciler/ -run 'TestANarrationRefusedForGoodIsDropped|Outbox|Posted|Probe|Room'
```

Expected: `ok`. The outage tests (`TestTheEndIsPostedOnceAfterAnOutage`) still retry a transient failure.

- [ ] **Step 5: Gate and commit.**

```bash
task check
git add internal/factory/forge/forge.go internal/factory/forge/github.go internal/factory/forge/github_test.go \
  internal/factory/narrate/narrate.go internal/factory/reconciler/reconciler.go internal/factory/reconciler/outbox_test.go docs/factory.md
git commit -m "fix(factory): a comment GitHub refuses for good is dropped, so later narration posts and the room closes (F-3)"
```

### Task 4: F-4, an escalation after a retry is heard, and a retry cannot lift the task cap

**Files:**
- Modify: `internal/factory/narrate/narrate.go:769-798` (`Ended`'s key), new `TaskCapHolds`
- Modify: `internal/factory/reconciler/watch.go:357-363` (`escalated`)
- Test: `internal/factory/reconciler/watch_test.go`; update `internal/factory/reconciler/team_test.go:137`
- Docs: `docs/factory.md:339`

**Interfaces:**
- Produces: escalation key `end-escalated-<len(Runs)>-<Retries>` (other phases unchanged);
  `func narrate.TaskCapHolds(t *v1alpha1.Task, command int64) narrate.Event` (key `retry-refused-<id>`).
- Consumes: `escalatedTask`, `retryBy`, `roomOf` (watch_test), `runFits` (reconciler.go:832).

- [ ] **Step 1: Write the failing tests.** In `watch_test.go`:

```go
// F-4: a retry that escalates again before any new run (here at the task cap) is a new
// escalation: its comment is posted, not deduplicated against the first one's key.
func TestARetryThatEscalatesAgainIsNarrated(t *testing.T) {
	tk := escalatedTask(false)
	tk.Spec.Budget.TaskTokens = 3_000_000
	tk.Status.Runs[0].Tokens, tk.Status.Usage.Tokens = 1_500_001, 1_500_001
	tk.Status.Narrated = []string{narrate.Ended(tk, v1alpha1.PhaseEscalated, "agent_stuck").Key} // the first escalation, posted
	g := newRig(t, tk, roomOf("3buqdlot"))
	g.r.Cfg.Budgets.EnforceTask = true
	g.f.SetComments(7, retryBy(77, "Smana", time.Minute))
	if got := g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseQueued || got.Status.Retries != 1 {
		t.Fatalf("%s %d", got.Status.Phase, got.Status.Retries)
	}
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "budget-task" {
		t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
	}
	if n := strings.Count(strings.Join(g.f.Comments(7), "\n"), "no room left for another full run"); n != 1 {
		t.Fatalf("the second escalation is narrated once: %q", g.f.Comments(7))
	}
}

// F-4: under budgets.enforceTask the cap is part of the task's spec, so a /factory retry of a
// budget-task escalation is answered and not taken: no retry counted, the escalation stands.
func TestARetryCannotLiftTheTaskCap(t *testing.T) {
	tk := escalatedTask(false)
	tk.Status.Reason = "budget-task"
	tk.Spec.Budget.TaskTokens = 3_000_000
	tk.Status.Runs[0].Tokens, tk.Status.Usage.Tokens = 1_500_001, 1_500_001
	g := newRig(t, tk, roomOf("3buqdlot"))
	g.r.Cfg.Budgets.EnforceTask = true
	g.f.SetComments(7, retryBy(77, "Smana", time.Minute))
	got := g.reconcile(t, "3buqdlot", 2)
	if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "budget-task" || got.Status.Retries != 0 ||
		!slices.Equal(got.Status.Handled, []int64{77}) {
		t.Fatalf("%s %s %d %v", got.Status.Phase, got.Status.Reason, got.Status.Retries, got.Status.Handled)
	}
	if n := strings.Count(strings.Join(g.f.Comments(7), "\n"), "the task's cap is part of its spec: raise it on a new task"); n != 1 {
		t.Fatalf("the retry is answered once: %q", g.f.Comments(7))
	}
	if slices.Contains(g.metrics.recorded, "intervention retry") {
		t.Fatal("a refused retry is no intervention")
	}
}
```

- [ ] **Step 2: Run them.** `go test -race -count=1 ./internal/factory/reconciler/ -run 'TestARetryThatEscalatesAgainIsNarrated|TestARetryCannotLiftTheTaskCap'`
  Expected: FAIL. The first: `the second escalation is narrated once: [... retrying ...]` (count 0: key
  `end-escalated-1` already narrated). The second: `Escalated budget-task 1 [77]` (the retry was taken).

- [ ] **Step 3: Implement.** `Ended`'s last line becomes:

```go
	key := fmt.Sprintf(closingPrefix+"%s-%d", strings.ToLower(phase), len(t.Status.Runs))
	if phase == v1alpha1.PhaseEscalated {
		key += fmt.Sprintf("-%d", t.Status.Retries) // F-4: a retry that escalates again before a run is a new escalation
	}
	return Event{Key: key, Body: b.String()}
```

After `Retrying` in `narrate.go`:

```go
// TaskCapHolds answers a /factory retry of a task escalated at its cap (F-4): Queued would only
// escalate it again, so the retry is not taken. Keyed by the command: each is answered once.
func TaskCapHolds(t *v1alpha1.Task, command int64) Event {
	return Event{Key: fmt.Sprintf("retry-refused-%d", command), Body: fmt.Sprintf("Agent factory task `%s` was not retried: %s, "+
		"and the task's cap is part of its spec: raise it on a new task.", t.Name, Reason("budget-task"))}
}
```

`escalated` (`watch.go`), between `if !ok { return r.expireEscalation(ctx, t) }` and `markHandled(t, c.ID)`:

```go
	if t.Status.Reason == "budget-task" && !runFits(t) && (r.Cfg.Budgets.EnforceTask || t.Sponsored() != "") {
		// F-4: Queued would only escalate it again. The command is answered and handled; the
		// escalation's clock keeps running, and Retries counts only retries that can run.
		markHandled(t, c.ID)
		narrateLater(t, narrate.TaskCapHolds(t, c.ID))
		return nil
	}
```

`team_test.go:137`: the forged marker's `event=end-escalated-6` becomes `event=end-escalated-6-0`, so
the forgery still names the key the escalation now uses.

`docs/factory.md:339`, the `budget-task` row becomes: "| `budget-task` | The task cap has no room
for another whole run. Under `budgets.enforceTask`, `/factory retry` is answered and not taken: the
cap is part of the task's spec, so raise it on a new task |".

- [ ] **Step 4: Run.** `go test -race -count=1 ./internal/factory/narrate/ ./internal/factory/reconciler/ -run 'Retry|Escalat|TaskTokenCap|RoundsExhausted|Ended'`
  Expected: PASS.

- [ ] **Step 5: Gate and commit.**

```bash
task check
git add internal/factory/narrate/narrate.go internal/factory/reconciler/watch.go internal/factory/reconciler/watch_test.go \
  internal/factory/reconciler/team_test.go docs/factory.md
git commit -m "fix(factory): an escalation after a retry is narrated, and a retry cannot lift the task cap (F-4)"
```

### Task 5: F-9, a span retry no longer delays an ended task's room close

**Files:**
- Modify: `internal/factory/reconciler/reconciler.go:278-313` (`Reconcile`'s tail), after `settleLeft` (`:673`)
- Test: `internal/factory/reconciler/trace_test.go`

**Interfaces:**
- Produces: `func soonest(a, b time.Duration) time.Duration` (0 is "no requeue").
- Consumes: `endedTask`, `withRoom`, `roomGone` (roomclose_test), `fakeSink` (trace_test).

- [ ] **Step 1: Write the failing test.** In `trace_test.go`:

```go
// F-9: a span the collector refuses is retried on its own clock and holds nothing else back: the
// ended task's room closes in the same pass, and the requeue is no later than the span's retry.
func TestAnUnexportedSpanDoesNotHoldTheRoomOpen(t *testing.T) {
	tk := endedTask(v1alpha1.PhaseDone)
	tk.Status.Trace = &v1alpha1.TraceRef{TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16)}
	g := newRig(t, tk)
	withRoom(t, g)
	g.r.Trace = &fakeSink{down: true}
	res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	if err != nil {
		t.Fatal(err)
	}
	if !roomGone(t, g) {
		t.Fatal("the room waits on the collector")
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > spanRetry {
		t.Fatalf("requeue %s: the span is retried within spanRetry", res.RequeueAfter)
	}
}
```

and in `TestABlackHoledCollectorNeverStallsTheWorker` (l.260) `res.RequeueAfter != spanRetry` becomes
`res.RequeueAfter <= 0 || res.RequeueAfter > spanRetry` (the task still settles: its settle is sooner).

- [ ] **Step 2: Run it.** `go test -race -count=1 ./internal/factory/reconciler/ -run TestAnUnexportedSpanDoesNotHoldTheRoomOpen`
  Expected: FAIL, `the room waits on the collector` (the early `spanRetry` return skips the close).

- [ ] **Step 3: Implement.** Replace from `if err == nil && r.spanDue(&t) {` to the end of the terminal
block with:

```go
	// A span the collector refused is retried every spanRetry (ST2). It is folded into the terminal
	// block's requeue: an early return here kept an ended task's room open, and its revert watch,
	// settle and monitor on the span's clock, for as long as the collector was down (F-9).
	var spanWait time.Duration
	if err == nil && r.spanDue(&t) {
		spanWait = spanRetry
	}
	if v1alpha1.TerminalPhase(t.Status.Phase) {
		// #56: the room closes once everything the task owes it has landed. Re-evaluated on the
		// state this pass left, so the pass that posts the last fact closes it.
		var closeLeft time.Duration
		if err == nil {
			if due, capped, wait, serr := r.roomState(ctx, &t); serr != nil {
				err = serr
			} else if due {
				var closed bool
				if closed, err = r.closeRoom(ctx, &t, capped != ""); closed && capped != "" {
					r.log().Info("room closed at the cap", "task.id", t.Name, "reason", capped)
					r.Metrics.RoomClosedAtCap(ctx, capped)
				}
			} else {
				closeLeft = wait
			}
		}
		// The revert watch polls: a revert PR merges, closes or stalls on GitHub's clock, not ours.
		if err == nil && (r.revertable(&t) || r.revertPending(&t)) {
			return ctrl.Result{RequeueAfter: soonest(r.Cfg.Poll.Tasks.Duration, spanWait)}, nil
		}
		if err == nil && r.settling(&t) {
			wait := r.settleLeft(&t) // for the settle, at its window, unless a merge under watch is due sooner
			if r.monitorDue(&t) {
				wait = min(wait, monitorEvery)
			}
			return ctrl.Result{RequeueAfter: soonest(wait, spanWait)}, nil
		}
		if err == nil && r.monitorDue(&t) && (closeLeft <= 0 || closeLeft > monitorEvery) {
			return ctrl.Result{RequeueAfter: soonest(monitorEvery, spanWait)}, nil // a merge under watch is read on its own clock
		}
		return ctrl.Result{RequeueAfter: soonest(closeLeft, spanWait)}, err // closeLeft: the revert window or a driving person (#56)
	}
```

(`spanDue` holds only for a terminal task, so the non-terminal tail is unchanged.) After `settleLeft`:

```go
// soonest is the nearer of two requeues, where 0 means none.
func soonest(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	}
	return min(a, b)
}
```

- [ ] **Step 4: Run.** `go test -race -count=1 ./internal/factory/reconciler/ -run 'Span|Trace|Collector|Room|Settle|Monitor'`
  Expected: PASS.

- [ ] **Step 5: Gate and commit.**

```bash
task check
git add internal/factory/reconciler/reconciler.go internal/factory/reconciler/trace_test.go
git commit -m "fix(factory): a span retry no longer delays an ended task's room close (F-9)"
```

### Task 6: F-1, a merge refused in GraphQL `errors[]` reads as its sentinel (blocked on the owner)

**Files:**
- Create: `internal/factory/forge/testdata/merge-head-moved.json`, `merge-not-mergeable.json` (recorded), `merge-unknown.json` (synthetic)
- Modify: `internal/factory/forge/github.go:964-1000` (`Merge`)
- Test: `internal/factory/forge/github_test.go` (mergePullRequest route, new test)

**Interfaces:**
- Produces: unexported `mergeRefusals []struct{ message string; err error }`. No signature change.

- [ ] **Step 1 (owner, BLOCKING): record GitHub's refusals live.** The executing agent stops here and
  reports "Task 6 blocked: needs the owner's F-1 recording". The owner runs, with `gh` authenticated as
  Smana (scopes `repo`, `delete_repo`):

```bash
export PROBE=Smana/maku-merge-probe
gh repo create "$PROBE" --private --add-readme
dir=$(mktemp -d) && gh repo clone "$PROBE" "$dir" && cd "$dir"
MUT='mutation($id: ID!, $oid: GitObjectID!) { mergePullRequest(input: {pullRequestId: $id, mergeMethod: SQUASH, expectedHeadOid: $oid}) { clientMutationId } }'

# 1. The head moved: expectedHeadOid is not the pull request's head.
git switch -c probe-head && echo a > a.txt && git add a.txt && git commit -m "probe: head" && git push -u origin probe-head
gh pr create --head probe-head --title "probe: head moved" --body "fixture for maku F-1"
gh api -i graphql -f query="$MUT" -f id="$(gh pr view probe-head --json id --jq .id)" -f oid="$(git rev-parse main)" > merge-head-moved.txt; echo "exit=$?"

# 2. Not mergeable: a pull request in conflict with main, at its true head.
git switch main && git switch -c probe-conflict && echo theirs > README.md && git commit -am "probe: conflict" && git push -u origin probe-conflict
gh pr create --head probe-conflict --title "probe: conflict" --body "fixture for maku F-1"
git switch main && echo ours > README.md && git commit -am "probe: main moves" && git push origin main
until [ "$(gh pr view probe-conflict --json mergeable --jq .mergeable)" = CONFLICTING ]; do sleep 5; done
gh api -i graphql -f query="$MUT" -f id="$(gh pr view probe-conflict --json id --jq .id)" \
  -f oid="$(gh pr view probe-conflict --json headRefOid --jq .headRefOid)" > merge-not-mergeable.txt; echo "exit=$?"

head -1 merge-head-moved.txt merge-not-mergeable.txt   # the HTTP status lines
```

  Keep the probe repository for Task 7 Step 1 (it deletes it). The owner hands back both `.txt`
  files. **Decision gate:** if either status line is not `HTTP/2.0 200 OK`, stop: GitHub carries that
  refusal in a status code, which `statusRT` reads without a body, and the fix becomes a status case
  in `Merge`'s switch; report to the owner before going on.

- [ ] **Step 2: Write the fixtures and the failing test.** From each recording, keep the JSON body
  only:

```bash
for f in merge-head-moved merge-not-mergeable; do
  awk 'body{print} /^\r?$/{body=1}' "$dir/$f.txt" | jq . > internal/factory/forge/testdata/$f.json
done
jq -r '.errors[0].message' internal/factory/forge/testdata/merge-*.json   # the two strings Step 3 matches
```

  `internal/factory/forge/testdata/merge-unknown.json` (synthetic: a message that is no merge refusal):

```json
{"data": {"mergePullRequest": null}, "errors": [{"type": "FORBIDDEN", "path": ["mergePullRequest"], "locations": [{"line": 1, "column": 44}], "message": "Resource not accessible by integration"}]}
```

  In `github_test.go`, `fakeGitHub` gains a field `mergeReply string // every mergePullRequest answers 200 with it (F-1)`, the helper:

```go
// setMergeReply makes every mergePullRequest answer 200 with body, as GitHub answers a refusal (F-1).
func (g *fakeGitHub) setMergeReply(body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mergeReply = body
}
```

  and the `mergePullRequest(input` case of the GraphQL route starts:

```go
		case strings.Contains(string(b), "mergePullRequest(input"):
			g.mu.Lock()
			reply := g.mergeReply
			g.mu.Unlock()
			if reply != "" {
				_, _ = io.WriteString(w, reply)
				return
			}
			switch {
```

  The test:

```go
// F-1: mergePullRequest answers its refusals 200 with errors[], recorded live in testdata: each
// reads as its sentinel. A message that is not one of GitHub's refusals stays a plain error.
func TestMergeRefusalsInGraphQLErrorsAreTheSentinels(t *testing.T) {
	for file, want := range map[string]error{
		"merge-head-moved.json":    ErrHeadMoved,
		"merge-not-mergeable.json": ErrNotMergeable,
		"merge-unknown.json":       nil,
	} {
		t.Run(file, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatal(err)
			}
			r := newRig(t)
			r.gh.setMergeReply(string(body))
			err = r.g.Merge(t.Context(), "PR_1", "abc123")
			sentinel := errors.Is(err, ErrHeadMoved) || errors.Is(err, ErrNotMergeable)
			switch {
			case want == nil && (err == nil || sentinel):
				t.Fatalf("an unknown refusal is a plain error: %v", err)
			case want != nil && !errors.Is(err, want):
				t.Fatalf("want %v, got %v", want, err)
			}
		})
	}
}
```

- [ ] **Step 3: Run it.** `go test -race -count=1 ./internal/factory/forge/ -run TestMergeRefusalsInGraphQLErrorsAreTheSentinels`
  Expected: FAIL on the two recorded files, `want forge: … head moved…, got forge: merge: <GitHub's message>`; `merge-unknown.json` passes.

- [ ] **Step 4: Implement.** In `github.go`, above `Merge`:

```go
// mergeRefusals are GitHub's own words for the refusals mergePullRequest answers 200 with errors[]
// (F-1), as recorded live in testdata/merge-*.json. The GraphQL client keeps only the first error's
// message; it is matched by prefix against these, never searched, so text a message happens to
// carry cannot pass for a refusal.
var mergeRefusals = []struct {
	message string
	err     error
}{
	{"Head branch was modified", ErrHeadMoved},
	{"Pull Request is not mergeable", ErrNotMergeable},
}
```

  Set each `message` to the leading words of its fixture's `errors[0].message` as printed in Step 2
  (verbatim; cut before any number, sha or login the message carries). Then in `Merge`, between the
  `statusError` switch and `return wrap("merge", err)`:

```go
	// F-1: most refusals come back 200 with errors[]. Only an error that is not the transport's
	// (*url.Error carries statusRT's status and any network failure) is GitHub's message.
	var ue *url.Error
	if !errors.As(err, &ue) {
		for _, m := range mergeRefusals {
			if strings.HasPrefix(err.Error(), m.message) {
				return fmt.Errorf("forge: merge: %w", m.err)
			}
		}
	}
```

  and `Merge`'s doc comment's "GitHub answers a moved head with 409 and a disallowed merge with 405"
  becomes "GitHub answers a moved head or a disallowed merge with 409 or 405, or with 200 and an
  errors[] message (F-1, testdata/merge-*.json)".

- [ ] **Step 5: Run.** `go test -race -count=1 ./internal/factory/forge/... ./internal/factory/reconciler/ -run 'Merge'`
  Expected: PASS, `TestMergeChecksTheHeadAtMergeTime`'s `text409` case included.

- [ ] **Step 6: Gate and commit.**

```bash
task check
git add internal/factory/forge/github.go internal/factory/forge/github_test.go internal/factory/forge/testdata/merge-*.json
git commit -m "fix(forge): a merge GitHub refuses in GraphQL errors reads as head moved or not mergeable (F-1)"
```

### Task 7: F-11, a revert whose answer was lost is adopted, never opened twice (blocked on the owner)

**Files:**
- Create: `internal/factory/forge/testdata/revert-naming.json` (recorded)
- Modify: `internal/factory/forge/forge.go:278-323` (`PRSummary`, `Revert`), `:485` (`Merger.RevertPR`)
- Modify: `internal/factory/forge/github.go:1030-1052` (`RevertPR`), `:1162-1176` (`OpenPullRequests`)
- Modify: `internal/factory/forge/absent.go:61-64`, `internal/factory/forge/forgetest/fake.go:794-804`
- Modify: `internal/factory/reconciler/merge.go:965-966` (`revert`)
- Test: `internal/factory/forge/github_test.go`; update `internal/factory/reconciler/merge_test.go:1095-1098`

**Interfaces:**
- Produces: `type forge.RevertOf struct{ Number int; NodeID, Title, Body, By string }`;
  `Merger.RevertPR(ctx context.Context, of RevertOf) (Revert, error)`; `PRSummary.NodeID`, `PRSummary.URL`.

- [ ] **Step 1 (owner, BLOCKING): record the revert branch's name live.** The executing agent stops
  here and reports "Task 7 blocked: needs the owner's F-11 recording". The owner runs, in the probe
  repository of Task 6 Step 1 (recreate it with that step's first three commands if it is gone):

```bash
cd "$dir"   # Task 6's clone of Smana/maku-merge-probe
gh pr merge probe-head --squash
N=$(gh pr view probe-head --json number --jq .number)
gh api graphql -f query='mutation($id: ID!) { revertPullRequest(input: {pullRequestId: $id, title: "Revert probe", body: "fixture for maku F-11"}) { revertPullRequest { number headRefName } } }' \
  -f id="$(gh pr view probe-head --json id --jq .id)" \
  | jq --argjson n "$N" '{reverted: $n, headRefName: .data.revertPullRequest.revertPullRequest.headRefName}' > revert-naming.json
cat revert-naming.json
gh repo delete "$PROBE" --yes
```

  The owner hands back `revert-naming.json`. **Decision gate:** if `headRefName` does not start with
  `revert-<reverted>-`, stop and report: the prefix below is wrong and the adoption must match what was
  recorded.

- [ ] **Step 2: Write the fixture and the failing test.** Copy the file to
  `internal/factory/forge/testdata/revert-naming.json`. In `github_test.go`, `fakeGitHub` gains:

```go
	open       []string // the open pull requests GET /pulls lists (state=open, no head filter)
	revertN    int      // revertPullRequest calls
	loseRevert bool     // the next revertPullRequest opens the revert, then answers 502
	revertHead string   // the branch revertPullRequest opens; "" opens none in the list
```

  the helpers:

```go
// openPull is one open pull request as GET /pulls lists it, on this repository.
func openPull(n int, nodeID, author, head string) string {
	return fmt.Sprintf(`{"number":%d,"node_id":%q,"html_url":"https://github.com/Smana/demo/pull/%d","user":{"login":%q},`+
		`"head":{"ref":%q,"sha":"h%d","repo":{"full_name":"Smana/demo"}},"base":{"ref":"main","repo":{"full_name":"Smana/demo"}}}`,
		n, nodeID, n, author, head, n)
}

func (g *fakeGitHub) addOpen(p string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = append(g.open, p)
}

// loseRevertAnswer makes the next revertPullRequest open its pull request on head, then fail (F-11).
func (g *fakeGitHub) loseRevertAnswer(head string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loseRevert, g.revertHead = true, head
}

func (g *fakeGitHub) revertCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.revertN
}
```

  the `revertPullRequest` case of the GraphQL route becomes:

```go
		case strings.Contains(string(b), "revertPullRequest"):
			g.mu.Lock()
			g.revertN++
			lose := g.loseRevert
			g.loseRevert = false
			if g.revertHead != "" { // GitHub opened it, whatever becomes of the answer
				g.open = append(g.open, openPull(13, "PR_rev", "ogenki-agent-merger[bot]", g.revertHead))
			}
			g.mu.Unlock()
			if lose {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"revertPullRequest":{"revertPullRequest":{"id":"PR_rev","number":13,"url":"https://github.com/Smana/demo/pull/13"}}}}`)
			return
```

  the `GET /repos/Smana/demo/pulls` route starts:

```go
		if q := r.URL.Query(); q.Get("head") == "" && q.Get("state") == "open" {
			g.mu.Lock()
			body := "[" + strings.Join(g.open, ",") + "]"
			g.mu.Unlock()
			_, _ = io.WriteString(w, body)
			return
		}
```

  `TestChecksAndMutations` (l.1069) calls the new signature:

```go
	rv, err := r.g.RevertPR(ctx, RevertOf{Number: 12, NodeID: "PR_1", Title: `Revert "docs: fix a link"`, Body: "reverts #12",
		By: "ogenki-agent-merger[bot]"})
```

  and the test:

```go
// F-11: revertPullRequest's answer lost after GitHub opened the revert (a timeout, a 502): the
// retry finds the merger's open pull request on GitHub's revert-<n>- branch, as recorded live in
// testdata/revert-naming.json, and returns it; no second revert is opened. A maintainer's own
// revert of the same pull request (GitHub's button, same branch name) is not the merger's.
func TestARevertWhoseAnswerWasLostIsNotOpenedTwice(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "revert-naming.json"))
	if err != nil {
		t.Fatal(err)
	}
	var named struct {
		Reverted    int    `json:"reverted"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	r.gh.addOpen(openPull(14, "PR_14", "Smana", named.HeadRefName))
	r.gh.loseRevertAnswer(named.HeadRefName)
	of := RevertOf{Number: named.Reverted, NodeID: "PR_1", Title: `Revert "docs: fix a link"`, Body: "reverts", By: "ogenki-agent-merger[bot]"}
	if _, err := r.g.RevertPR(t.Context(), of); err == nil {
		t.Fatal("a lost answer is an error")
	}
	rv, err := r.g.RevertPR(t.Context(), of)
	if err != nil || rv.Number != 13 || rv.NodeID != "PR_rev" || rv.URL != "https://github.com/Smana/demo/pull/13" {
		t.Fatalf("%+v %v", rv, err)
	}
	if n := r.gh.revertCalls(); n != 1 {
		t.Fatalf("%d revertPullRequest calls: the retry opened a second revert", n)
	}
}
```

- [ ] **Step 3: Run it.** `go test -race -count=1 ./internal/factory/forge/ -run TestARevertWhoseAnswerWasLostIsNotOpenedTwice`
  Expected: FAIL to compile, `undefined: RevertOf`.

- [ ] **Step 4: Implement.** `forge.go`: `PRSummary` gains, after `Created`,

```go
	// NodeID and URL are the pull request's GraphQL id and page: an adopted revert needs no second read.
	NodeID string
	URL    string
```

  after `Revert`:

```go
// RevertOf is what RevertPR reverts: the merged pull request (GitHub names the revert's branch
// revert-<Number>-<head>) and the merger App's login, the only author whose open revert is adopted.
type RevertOf struct {
	Number      int
	NodeID      string
	Title, Body string
	By          string
}
```

  and in `Merger`: `RevertPR(ctx context.Context, of RevertOf) (Revert, error)`.
  `OpenPullRequests` fills `NodeID: p.GetNodeID(), URL: p.GetHTMLURL()`. `RevertPR` becomes:

```go
// RevertPR produces the revert PR GitHub's own button would (§6.4); its revert-<n>-<head> branch is
// created by the merger App, which is why only that App bypasses agent-merge (R16). A revert already
// open on such a branch by of.By is the one a previous call opened and lost the answer of (F-11):
// it is returned, never doubled, since a second revert would count twice against the breaker. The
// list is one page of 100 open pull requests, newest first, where a revert just opened is.
func (g *GitHub) RevertPR(ctx context.Context, of RevertOf) (Revert, error) {
	open, err := g.OpenPullRequests(ctx)
	if err != nil {
		return Revert{}, err
	}
	branch := fmt.Sprintf("revert-%d-", of.Number)
	for _, p := range open {
		if strings.HasPrefix(p.HeadRef, branch) && !p.CrossRepository && SameLogin(p.Author, of.By) {
			return Revert{Number: p.Number, URL: p.URL, NodeID: p.NodeID}, nil
		}
	}
	var m struct {
		RevertPullRequest struct {
			RevertPullRequest struct {
				ID     string
				Number int
				URL    string `graphql:"url"`
			}
		} `graphql:"revertPullRequest(input: $input)"`
	}
	t, b := githubv4.String(of.Title), githubv4.String(of.Body)
	err = g.v4.Mutate(ctx, &m, githubv4.RevertPullRequestInput{PullRequestID: githubv4.ID(of.NodeID), Title: &t, Body: &b}, nil)
	g.mark(err)
	r := m.RevertPullRequest.RevertPullRequest
	return Revert{Number: r.Number, URL: r.URL, NodeID: r.ID}, wrap("open a revert", err)
}
```

  `absent.go`: `func (Absent) RevertPR(context.Context, RevertOf) (Revert, error) { return Revert{}, ErrNoMerger }`.
  `forgetest/fake.go`: `func (f *Fake) RevertPR(_ context.Context, of forge.RevertOf) (forge.Revert, error)`,
  its body reading `of.NodeID` and `of.Title` where it read `nodeID` and `title` (the `Reverts()`
  record keeps its `"<nodeID> <title>"` shape). `merge_test.go`'s `racingMerger`:

```go
func (m racingMerger) RevertPR(ctx context.Context, of forge.RevertOf) (forge.Revert, error) {
	m.race("revert")
	return m.Merger.RevertPR(ctx, of)
}
```

  `merge.go` `revert`:

```go
		if rv, err = r.merger(ctx).RevertPR(ctx, forge.RevertOf{Number: ref.Number, NodeID: ref.NodeID,
			Title: fmt.Sprintf("Revert %q", pr.Title), By: r.repo(t).Merge.MergerLogin,
			Body:  fmt.Sprintf("Reverts #%d (agent factory task `%s`): %s.", ref.Number, t.Name, narrate.Reason(why))}); err != nil {
			return err
		}
```

- [ ] **Step 5: Run.** `go test -race -count=1 ./internal/factory/forge/... ./internal/factory/reconciler/ ./internal/factory/intake/`
  Expected: `ok` (the adopt poller reads `PRSummary` and is unaffected by the two new fields).

- [ ] **Step 6: Gate and commit.**

```bash
task check
git add internal/factory/forge/forge.go internal/factory/forge/github.go internal/factory/forge/github_test.go \
  internal/factory/forge/absent.go internal/factory/forge/forgetest/fake.go internal/factory/forge/testdata/revert-naming.json \
  internal/factory/reconciler/merge.go internal/factory/reconciler/merge_test.go
git commit -m "fix(forge): a revert whose answer was lost is adopted, never opened twice (F-11)"
```

### Task 8: The gate and the pull request

**Files:** none changed.

- [ ] **Step 1: The full gate.** With Docker running: `task check`. Expected: exit 0, every step
  (`spdx` … `ui:check`), `crd:check` clean (no API type changed).
- [ ] **Step 2: Rebase and push.** `git fetch origin && git rebase origin/main && task check && git push -u origin fix/audit-t2-factory-liveness`.
- [ ] **Step 3: Open the PR** (English, no attribution line):

```bash
gh pr create --base main --head fix/audit-t2-factory-liveness \
  --title "fix(factory): audit T2, factory liveness (H-2, F-1, F-2, F-3, F-4, F-9, F-11)" \
  --body "Theme T2 of docs/superpowers/specs/2026-11-01-audit-fixes-design.md, one commit per finding, each pinned by its test first (D0).

- H-2: Verifying ends Done merged_unverified past 2 x verifyFor (D1); merge.awaitingCIFor defaults to 24h, 0s is no bound (D2); CommitChecks reads up to 10 pages, a capped listing reads PENDING (D3).
- F-2: the outbox is capped at the CRD's 16 before the status write.
- F-3: a comment GitHub refuses for good (404, 410, locked 403) is dropped; the rest posts and the room closes.
- F-4: the escalation key carries the retries; a budget-task retry under enforceTask is answered, not taken.
- F-9: the span retry folds into the terminal requeue.
- F-1: mergePullRequest refusals in errors[] map to ErrHeadMoved / ErrNotMergeable (fixtures recorded live).
- F-11: RevertPR adopts the merger's open revert-<n>- pull request.

Behaviour change: an omitted merge.awaitingCIFor now bounds AwaitingCI at 24h. Deviations from the spec are listed in docs/superpowers/plans/2026-11-01-audit-t2-factory-liveness-plan.md."
```
