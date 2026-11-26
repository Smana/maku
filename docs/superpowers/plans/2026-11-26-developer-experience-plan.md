# Developer Experience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an application developer with write access start a task, build and test a product stack in the
sandbox, read the pull request in five minutes and unstick a failure without the platform team.

**Architecture:** Each gap of the review is closed by the smallest change at the seam that already owns it:
the factory's narration and brief (`internal/factory/narrate`, `internal/factory/reconciler`), the repository
config (`internal/factory/config`, `api/factory/v1alpha1`), the run pod (`internal/runctl`), the CLI
(`internal/app`) and the web UI (`web/src`). Small, visible changes ship first; the access, runtime and
service-sidecar changes follow.

**Tech Stack:** Go 1.26 (controller-runtime), TypeScript + Vitest (web), Python (harness image), Hugo (ANR site).

**Spec:** ANR `docs/superpowers/specs/2026-11-26-developer-experience-review.md` (ANR #539). Evidence paths
in it (`maku:<path>`) are at commit `a26d990c`; line numbers here are at `0fe85036`.

## Owner decisions (2026-11-26)

These answer the review's four open questions and bind every task below.

| # | Question | Decision |
|---|---|---|
| D1 | Does `write` access starting a task need a per-person daily cap enforced first? | **Yes.** Per-person daily caps are enforced first. `Repository.spec.access.start: write` takes effect only where per-person caps are enforced (`budgets.perPerson.enabled` and `budgets.enforcePrincipal` both true). Elsewhere it reads as `maintainers`, and the Repository says so. |
| D2 | A per-repository platform-approved host list, or named profiles only? | **Named profiles only.** Add `maven` and `node` egress profiles with platform-reviewed hosts, plus `runtime.image` and `runtime.size` per Repository. **No** per-Repository free host list (`checks.egressHosts` is dropped). Stay consistent with the GitHub-egress tightening design (Maku #742): no new GitHub host enters any profile. |
| D3 | Service sidecars in v5, or only "CI is the judge"? | **Both, in order.** Say it now: the first run comment lists the steps the sandbox skips and names CI as the judge. Then opt-in loopback service sidecars (`postgres`, `redis`) per Repository, on the browser sidecar's pattern (native sidecar and admission, #751 and #760). `Smana/image-gallery` (Go and Postgres) is the proving ground. Test secrets stay out. |
| D4 | Where does the sample repository live? | **`Smana/image-gallery`** is the developer drills repository; it is already served on `gcp-0`. No `maku-sample-shop`. |

Gap 5 (pictures) belongs to the screenshots workstream (#758 to #764). Those PRs attach request images to
vision runs on a Repository with `spec.screenshots`, but none tells the author that images were removed
elsewhere. This plan only adds that notice (Task 6).

## Global Constraints

- Worktrees from `origin/main`; rebase onto a freshly fetched `origin/main` before every push.
- Conventional commits in English, no trailers.
- Load limits: every command under `nice -n 10`; `go test -p 2` on touched packages only; `task ui:*` only
  when `web/` changes; Node 24 from `~/.local/share/mise/installs/node/24.21.0/bin` (the default v22 breaks
  Vitest). No full local `task check`; CI runs it.
- Generated files after a rebase conflict (UI bundle, `web/src/reasons.ts`): rebase, `hack/docs.sh`,
  `npm run build` in `web/` with Node 24, commit.
- Developer-facing words: **task, room, workflow, budget, acceptance check, reviewer, merge policy,
  maintainer**. New developer-facing text uses no other Maku term (review, "What to drop or hide").
- A run's egress stays named profiles only (D2). A sidecar is loopback only, has no egress, and its
  resources are the chart's, never a Repository's (as `BrowserConfig`, C10).
- ANR site pages follow ANR `docs/docs-style.md`; `task docs` enforces it.
- Maku reader pages never say "egress profile", "harness profile" or "capability profile" in prose
  (`hack/vocabulary.sh`): a reader page says "registry access" or names the profile in code.

## Order and pull requests

One PR per task; at most two in flight.

| Order | Task | Gap | Size | Repo |
|---|---|---|---|---|
| 1 | Make a repository ready: how-to, four examples, `maku repo check` readiness | 4 | S | Maku |
| 2 | ANR readiness page | 4 | S | ANR |
| 3 | Pull request body contract, dollars in the closing comment | 6 | S | Maku |
| 4 | Answer an ignored comment review; drop "push to the branch yourself" | 7 (S) | S | Maku |
| 5 | Served repositories in the New task picker; an unserved repository names the request | 9 | S | Maku |
| 6 | Say what the sandbox skips; say that images were removed | 3 (say), 5 (notice) | S | Maku |
| 7 | CLI help split, per-command help, settings gear for maintainers | 10 | S | Maku |
| 8 | ANR developer quickstart | 10 | S | ANR |
| 9 | Per-person caps enforced on every start surface | 1 | M | Maku |
| 10 | `Repository.spec.access.start` | 1 | M | Maku |
| 11 | `maven` and `node` egress profiles | 2 | M | Maku |
| 12 | `Repository.spec.runtime.image` and `runtime.size` | 2 | M | Maku |
| 13 | Rebase run on `not_mergeable` | 7 (M) | M | Maku |
| 14 | Developer next steps for the top 20 reasons; `maku why <task>` | 8 | M | Maku |
| 15 | Opt-in loopback service sidecars (`postgres`, `redis`) | 3 (L) | L | Maku |
| 16 | Drills on `Smana/image-gallery` | all | — | gcp-0 |

---

### Task 1: Make a repository ready (gap 4)

**Files:**
- Create: `docs/make-a-repository-ready.md`
- Create: `examples/agent-ready/go/{mise.toml,.github/agent-checks.yaml,AGENTS.md}`, same for `node/`, `python/`, `jvm/`
- Create: `examples/agent-ready/examples_test.go` (package `agentready_test`: every example parses under the harness's rules)
- Modify: `internal/app/maku_repo_check.go` (a `readiness` line), `internal/app/maku_repo_check_test.go`
- Modify: `docs/onboard-a-repository.md` (link), `docs/README.md` (index entry), `internal/app/maku.go` (usage line)

**Interfaces:**
- Produces: `func (r Maku) readinessLine(ctx context.Context, repo string) repoLine`; codes `checks_missing`,
  `toolchain_missing`, `agents_missing` in `repoCheckFixes`.

The harness's schema (`images/maku-harness/agent_checks.py:107-133`): top-level keys `version`,
`timeoutMinutes`, `tools`, `definedBy`, `steps`, `ciOnly`, `tests`; every `ciOnly` entry has exactly
`check`, `part`, `why`; a step name matches `^[a-z0-9][a-z0-9-]{0,39}$`.

- [ ] **Step 1: Write the failing repo-check test**

In `maku_repo_check_test.go`, beside the fence tests, a fake `gh` answering 404 for
`.github/agent-checks.yaml` and 200 for the fence, `mise.toml` and `AGENTS.md`:

```go
func TestRepoCheckWarnsWithoutAgentChecks(t *testing.T) {
	out, err := runRepoCheck(t, ghFiles{".github/agent-factory.yaml": "factory: agent-factory\n", "mise.toml": "[tools]\ngo = \"1.26\"\n", "AGENTS.md": "# x\n"})
	if err == nil {
		t.Fatal("a missing agent-checks.yaml must exit 1")
	}
	for _, want := range []string{"WARN  readiness", ".github/agent-checks.yaml is missing", "make-a-repository-ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRepoCheckPassesAReadyRepository(t *testing.T) {
	out, _ := runRepoCheck(t, ghFiles{".github/agent-factory.yaml": "factory: agent-factory\n", ".github/agent-checks.yaml": "version: 1\nsteps: []\n", "mise.toml": "", "AGENTS.md": ""})
	if !strings.Contains(out, "PASS  readiness") {
		t.Errorf("got:\n%s", out)
	}
}
```

(`runRepoCheck` and `ghFiles` extend the file's existing fake: `ghFiles` maps a path to its content and
answers 404 otherwise.)

- [ ] **Step 2: Run it, expect FAIL** — `nice -n 10 go test -p 2 ./internal/app -run RepoCheck`

- [ ] **Step 3: Implement `readinessLine`**

It reads the three paths on the default branch with the developer's `gh` (`repos/<repo>/contents/<path>?ref=`),
as `fenceLine` does, and reports the first missing one as WARN, `agent-checks.yaml` first. Fixes:

```go
"checks_missing":    "write .github/agent-checks.yaml: docs/make-a-repository-ready.md, with a worked file per stack under examples/agent-ready/.",
"toolchain_missing": "pin the checks' tools in mise.toml: the sandbox installs them from it.",
"agents_missing":    "add AGENTS.md: how to build, test and lint this repository, in a few lines.",
```

Append it after the fence line in `repoCheck`. A WARN already exits 1 (existing rule).

- [ ] **Step 4: Run it, expect PASS**

- [ ] **Step 5: Write the four examples and their test.** Go (`go test ./...`, `golangci-lint`; `ciOnly`:
  integration tests that need Postgres), Node (npm ci, Vitest, ESLint; `ciOnly`: Playwright), Python
  (uv, pytest, ruff; DuckDB fixtures as the local substitute), JVM (Maven `-o` after `dependency:go-offline`;
  marked as needing the `maven` profile, Task 11). `examples_test.go` loads each `agent-checks.yaml` and
  asserts the key set, the `ciOnly` shape and the step-name pattern above.

- [ ] **Step 6: Write `docs/make-a-repository-ready.md`.** Sections: what the sandbox gives a run (one image,
  named registries, no Docker, 15 minutes, 2 CPU and 4 GiB by default); `mise.toml`; `.github/agent-checks.yaml`
  field by field; what belongs in `ciOnly` (anything that needs Docker, a service, a secret or a host outside
  the profiles); `AGENTS.md`; `maku repo check`. Link from `onboard-a-repository.md` and `docs/README.md`.

- [ ] **Step 7: Run `nice -n 10 go test -p 2 ./internal/app ./examples/...`, commit**
  `feat(repo): a how-to and four worked files make a repository ready, and repo check warns without them`

### Task 2: ANR readiness page (gap 4)

**Files:** Create `website/content/docs/use/guide/make-your-repository-ready.md` (ANR); modify
`website/content/docs/use/guide/_index.md` (chapter list) and `before-your-first-task.md` (one link).

- [ ] **Step 1:** Write the page for a developer, not an operator: the three files, a short `agent-checks.yaml`,
  what `ciOnly` means ("the sandbox skips it; CI still runs it and is the judge"), and links to the four
  Maku examples by absolute URL.
- [ ] **Step 2:** `nice -n 10 task docs` then one `nice -n 10 task check`; expect both green.
- [ ] **Step 3:** Commit `docs(guide): make your repository ready`.

### Task 3: Pull request body contract (gap 6)

**Files:**
- Modify: `internal/factory/reconciler/text.go` (`firstBrief`: the four headings)
- Modify: `internal/factory/applier/apply.go` (`applyPR` and `find` build the body through one helper)
- Create: `internal/factory/applier/facts.go`, `internal/factory/applier/facts_test.go`
- Modify: `internal/factory/narrate/narrate.go` (closing comment: dollars, not tokens), its test

**Interfaces:**
- Produces: `func factsBlock(t *v1alpha1.Task, roomsURL string) string` and
  `func prBody(t *v1alpha1.Task, body string, p hardening.Provenance, roomsURL string) string`
  (`= hardening.WithFooter(body + factsBlock(t, roomsURL), p)`). `find` and `applyPR` both call `prBody`:
  `find` compares the body it would write, so the two must never diverge.

- [ ] **Step 1: Failing tests**

```go
func TestFirstBriefAsksForTheFourHeadings(t *testing.T) {
	b := firstBrief(issueTask(), "n0nce", nil)
	for _, h := range []string{"## What changed", "## Why", "## How it was verified", "## What was left out"} {
		if !strings.Contains(b, h) {
			t.Errorf("brief lacks %q", h)
		}
	}
}

func TestPRBodyCarriesTheFactorysFacts(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"}, Status: v1alpha1.TaskStatus{RoomRef: "3buqdlot"}}
	got := prBody(tk, "## What changed\nx", hardening.Provenance{Output: "0123456789abcdef"}, "https://rooms.example")
	for _, want := range []string{"maku checkout 3buqdlot", "https://rooms.example/r/3buqdlot", "Agent-Output: 0123456789abcdef"} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %q:\n%s", want, got)
		}
	}
	if _, id := hardening.SplitFooter(got); id != "0123456789abcdef" {
		t.Errorf("the footer must stay last, got %q", id)
	}
}
```

And in `narrate_test.go`: the `done` closing comment of a task with spend says `$` and not `tokens`.

- [ ] **Step 2: Run, expect FAIL** — `nice -n 10 go test -p 2 ./internal/factory/reconciler ./internal/factory/applier ./internal/factory/narrate`

- [ ] **Step 3: Implement.** In `firstBrief`, after the `Fixes #N` line:

```go
b.WriteString("Write the pull request body under four headings, in this order: \"## What changed\", " +
	"\"## Why\", \"## How it was verified\" (the commands you ran and their result; say which checks you could not run " +
	"and why), \"## What was left out\". The factory adds how to try the change.\n")
```

`factsBlock` is `"\n\n## Try it\n\n- `maku checkout <room>` puts the branch in a new worktree.\n- The room: <url>/r/<room>\n"`.
The applier gets `RoomsURL` from its config (the field the reconciler already passes to narrate). The
closing comment's token figure becomes `narrate.USD(spentMicro)` when the task carries a USD spend.

- [ ] **Step 4: Run, expect PASS.** **Step 5:** Commit `feat(factory): a pull request body has four headings and how to try it`.

### Task 4: Answer an ignored comment review (gap 7, S part)

**Files:** Modify `internal/factory/reconciler/watch.go` (`changesRequested` or its caller),
`internal/factory/narrate/narrate.go` (`CommentReviewIgnored`, escalation hint), tests beside each.

**Interfaces:** Produces `func CommentReviewIgnored(t *v1alpha1.Task, id int64) Event` (key `comment-review-<id>`).
Consumes `refuseInput`'s once-per-login limit (`held.go:337`).

- [ ] **Step 1: Failing tests.** A maintainer's `COMMENTED` review with a body, after the run started, on the
  task's PR: one comment on the PR naming **Request changes**, the review id lands in `Status.Handled`, and a
  second reconcile posts nothing. A non-maintainer's comment review gets nothing new (it is advice).
  The escalation hint no longer contains `push to the branch yourself`.
- [ ] **Step 2: Run, expect FAIL** (`./internal/factory/reconciler -run CommentReview`, `./internal/factory/narrate`).
- [ ] **Step 3: Implement.**

```go
func CommentReviewIgnored(t *v1alpha1.Task, id int64) Event {
	return Event{Key: fmt.Sprintf("comment-review-%d", id), Class: ClassChanged, Body: fmt.Sprintf(
		"Maku task `%s`: a **Comment** review does not start a revision. To have the agent act on it, "+
			"submit your review as **Request changes**; to merge, **Approve**.", t.Name)}
}
```

In `changesRequested`, for each maintainer review with `State == "COMMENTED"`, a non-empty body, after
`since`, not handled: `markHandled`, then `narrateOn(t, pr.Number, narrate.CommentReviewIgnored(t, rv.ID))`
at most once per task (a `Status.Handled` sentinel is not needed: the event key is per review, and the
reply is suppressed when any earlier `comment-review-` reply exists in `t.Status.Narrated`).
The escalation hint becomes: "Comment `/factory retry` on this issue to run it again (a maintainer only).
Without it, it stops when its escalation expires."
- [ ] **Step 4: PASS. Step 5:** Commit `feat(factory): answer a comment review once, and stop suggesting a push to the agent's branch`.

### Task 5: Served repositories and the request to send (gap 9)

**Files:**
- Modify: the broker's human API to proxy the factory's `GET /v1/catalogue` as `GET /api/catalogue`
  (`internal/humanapi/catalogue.go`, its route table, test)
- Modify: `web/src/view.ts` (`knownRepos` merges the served list), `web/src/newtask.ts`, `web/src/newtask.test.ts`
- Modify: `internal/intake/refusal.go` (`repository_not_served_here` names the request), its test

**Interfaces:** Produces `GET /api/catalogue` → `{"repositories": ["owner/name", …]}` with the caller's
token, cached as the per-repository catalogue is (one minute).

- [ ] **Step 1: Failing tests.** Broker: `GET /api/catalogue` answers the factory's list for a member and 401
  without a session. Web: `newTaskForm` offers a repository from `/api/catalogue` that no room names. Intake:
  the refusal says "ask your platform team to serve it: `maku repo add owner/name` writes what they apply".
- [ ] **Step 2: Run, expect FAIL** (`go test -p 2 ./internal/humanapi ./internal/intake`; Vitest `newtask`).
- [ ] **Step 3: Implement.** The refusal text:
  `"%s; ask your platform team to serve it (they run maku repo add <owner/name>)"`. The web reads
  `/api/catalogue` once at load and unions it with the rooms' repositories; a failed read leaves today's list.
- [ ] **Step 4: PASS** (Vitest with Node 24, `task ui:build` because `web/` changed). **Step 5:** Commit
  `feat(web): the New task picker lists every served repository`.

A label on a repository the factory does not serve is out of scope here: the factory never polls such a
repository, so answering it needs the App's installation list. It is filed as a follow-up issue, not built.

### Task 6: Say what the sandbox skips; say that images were removed (gap 3 "say it", gap 5 notice)

**Files:**
- Modify: `internal/factory/reconciler/tester.go` (`checksDecl` gains `CIOnly`), `internal/factory/reconciler/implement.go:379`
- Modify: `internal/factory/narrate/narrate.go` (`SandboxLimits`), test
- Modify: `internal/factory/intake/issues.go` (images notice at intake), test

**Interfaces:**
- Produces: `type CIOnly struct{ Check, Part, Why string }` in `checksDecl` (`json:"ciOnly"`);
  `func SandboxLimits(t *v1alpha1.Task, declared bool, ciOnly []CIOnly) Event` (key `sandbox-limits`);
  `func ImagesRemoved(n int) Event` (key `images-removed`).

- [ ] **Step 1: Failing tests.**

```go
func TestSandboxLimitsNamesCIAsTheJudge(t *testing.T) {
	e := SandboxLimits(task(), true, []CIOnly{{Check: "integration", Part: "api/", Why: "needs Postgres"}})
	for _, want := range []string{"integration", "api/", "needs Postgres", "CI is the judge"} {
		if !strings.Contains(e.Body, want) {
			t.Errorf("lacks %q:\n%s", want, e.Body)
		}
	}
}

func TestSandboxLimitsWithoutADeclaration(t *testing.T) {
	e := SandboxLimits(task(), false, nil)
	if !strings.Contains(e.Body, "declares no agent checks") || !strings.Contains(e.Body, "make-a-repository-ready") {
		t.Errorf("got:\n%s", e.Body)
	}
}
```

Intake: an issue body with two images on a Repository without `screenshots` posts one comment
"2 images in this issue were removed: the agent cannot see them. Describe what they show in words."; with
`screenshots` set, nothing (the screenshots work passes them to a vision run).
- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Implement.** Each `ciOnly` field is untrusted text from the repository: quote it in backticks
  after `sanitize.Text`, cut each to 120 characters, list at most 10. The reconciler posts `SandboxLimits` with
  the first implementer run's `Started` only (`len(t.Status.Runs) == 0` before the append), reading the
  default branch's declaration with `r.checksAt(ctx, defaultBranch)`; a read error posts nothing.
  Intake posts `ImagesRemoved(rep.Images)` through `PostOnce` after a non-withheld snapshot.
- [ ] **Step 4: PASS. Step 5:** Commit `feat(factory): the first run says what the sandbox skips, and intake says which images were removed`.

### Task 7: CLI help and the settings gear (gap 10)

**Files:** Modify `internal/app/maku.go` (split `makuUsage`), create `internal/app/maku_help.go` and
`maku_help_test.go`; modify `web/src/settingslink.ts` and its test; the broker's
`/api/policy/repositories` answer (whether the caller maintains any repository).

**Interfaces:** Produces `everydayUsage`, `adminUsage` (strings), `func commandHelp(name string) (string, bool)`.

- [ ] **Step 1: Failing tests.**

```go
func TestHelpIsEverydayByDefault(t *testing.T) {
	out := runMaku(t, "help")
	if !strings.Contains(out, "login") || strings.Contains(out, "db bootstrap") {
		t.Errorf("got:\n%s", out)
	}
	if !strings.Contains(out, "maku help admin") {
		t.Error("the everyday help must point at the admin help")
	}
}

func TestHelpAdminAndPerCommand(t *testing.T) {
	if out := runMaku(t, "help", "admin"); !strings.Contains(out, "db bootstrap") {
		t.Errorf("admin:\n%s", out)
	}
	if out := runMaku(t, "help", "checkout"); !strings.HasPrefix(strings.TrimSpace(out), "checkout <room>") {
		t.Errorf("checkout:\n%s", out)
	}
	if out := runMaku(t, "checkout", "--help"); !strings.Contains(out, "worktree") {
		t.Errorf("--help:\n%s", out)
	}
}
```

Web: the gear stays hidden when `/api/policy/repositories` answers 200 with an empty list.
- [ ] **Step 2: FAIL. Step 3: Implement.** Admin commands: `install`, `db bootstrap`, `preflight`, `repo add`,
  `repo check`, `demo app`, `config migrate`, `config validate`, `policy *`, `drill plant`, `fork`, `run`,
  `workflow dry-run`, `stages`. `commandHelp` finds the block whose first line starts with two spaces and the
  name, through the next such line. `makuUsage` stays as `everydayUsage + adminUsage` for the flag errors.
- [ ] **Step 4: PASS** (`go test -p 2 ./internal/app`; Vitest; `task ui:build`). **Step 5:** Commit
  `feat(cli): everyday and admin help, and help per command`.

### Task 8: ANR developer quickstart (gap 10)

**Files:** Create `website/content/docs/use/developer-quickstart.md` (ANR); link it first from
`website/content/docs/use/_index.md`.

- [ ] **Step 1:** At most 400 words; only the eight developer words; steps: get access (ask your platform
  team), check the repository is served (the New task picker), make it ready (link to Task 2's page), hand
  off, read the pull request (the four headings), approve to merge.
- [ ] **Step 2:** `nice -n 10 task docs`, one `nice -n 10 task check`. **Step 3:** Commit `docs(use): a developer quickstart`.

### Task 9: Per-person caps enforced on every start surface (gap 1, D1)

Enforcement code exists: `budgets.enforcePrincipal` makes the ledger refuse a person over `perPerson.dailyUSD`
(`internal/factory/api/server.go:738`, `reconciler/implement.go:233`, `meter/meter.go:1276`). What is missing
is proof that every start surface reaches it, and a refusal a developer can read.

**Files:** Tests in `internal/factory/reconciler`, `internal/factory/api`; modify `internal/factory/reasons`
(`budget-person`'s next step), `docs/factory.md` budgets section.

- [ ] **Step 1: Failing table test** over the five starts: issue label, API hand-off, Slack (through the API),
  `/factory retry`, an answer that restarts. With `perPerson.enabled` and `enforcePrincipal` true and the
  person's day spent, each ends `budget-person` with no AgentRun, and the person, not the maintainer who
  labelled earlier, is the one charged (`r.person`).
- [ ] **Step 2: Run; fix each surface that admits** (expected: none or one; record which in the PR).
- [ ] **Step 3:** `budget-person`'s next step: "your daily budget is spent; it resets at 00:00 UTC, or a
  maintainer starts it on their own budget".
- [ ] **Step 4: PASS. Step 5:** Commit `test(factory): per-person caps refuse every start surface`.

### Task 10: `Repository.spec.access.start` (gap 1, D1)

**Files:**
- Modify: `api/factory/v1alpha1` Repository spec (`Access{Start string}`, enum `maintainers;write`, default
  `maintainers`), `hack/` CRD generation, chart CRD copy
- Modify: `internal/factory/config/config.go` (`Repository.Access`, `func (r *Repository) MayStart(login string, canWrite bool, b Budgets) bool`)
- Modify: start checks: `internal/factory/intake/issues.go:366`, `intake/handoff.go:125`,
  `api/tasks.go:298`, `reconciler/watch.go:407` (retry), `api/answers.go:206`
- Modify: the Repository's status (condition `AccessStartEffective`), `internal/app/maku_repo_check.go` (WARN when `write` is not in effect)

**Interfaces:**

```go
// MayStart: a maintainer always; a writer when access.start is write and per-person caps are enforced (D1).
func (r *Repository) MayStart(login string, canWrite bool, b Budgets) bool {
	if r.IsMaintainer(login) {
		return true
	}
	return r.Access.Start == AccessWrite && b.PerPerson.Enabled && b.EnforcePrincipal && canWrite
}
```

Approving a plan, merging, `/factory retry --deeper`, and settings stay with maintainers.

- [ ] **Step 1: Failing tests** for `MayStart` (four rows: maintainer; writer with `write` and caps on;
  writer with `write` and caps off → false; writer with `maintainers` → false), then one intake test: a
  writer's label on a `write` repository starts a task charged to the writer.
- [ ] **Step 2: FAIL. Step 3: Implement**; each start site calls `MayStart`; the refusal reason stays
  `not_maintainer` when `write` is off and is `access_start_inactive` ("starting by write access is set but
  per-person caps are not enforced here; ask your platform team") when set but not effective.
- [ ] **Step 4: PASS. Step 5:** Commit `feat(factory): developers with write access may start tasks where per-person caps are enforced`.

### Task 11: `maven` and `node` egress profiles (gap 2, D2)

**Files:** `internal/runctl/config.go` (`defaultProfiles`), `internal/factory/config/config.go:454`
(`EgressProfiles`), `api/v1alpha1/agentrun_types.go:208` (enum, `MaxItems` 4 → 6), `internal/humanapi/mcp.go:115`
(fork schema enum), `internal/bridge/classify_tables.go:38` (`mvn`, `gradle` → `maven`), chart netpol values and
golden, `docs/make-a-repository-ready.md`.

Hosts, reviewed by the platform (no GitHub host, per #742):

| Profile | Hosts |
|---|---|
| `maven` | `repo.maven.apache.org`, `repo1.maven.org`, `plugins.gradle.org`, `services.gradle.org`, `downloads.gradle.org` |
| `node` | `nodejs.org` (mise's Node download; settles the review's unproven claim) |

- [ ] **Step 1: Failing tests:** `defaultProfiles` has both; a Repository with `checks.egress: [maven]` loads;
  `[artifactory.example]` is refused naming the profiles; the AgentRun CRD accepts `maven`.
- [ ] **Step 2: FAIL. Step 3: Implement**, regenerate CRDs (`hack/docs.sh` and the CRD target).
- [ ] **Step 4: PASS. Step 5:** Commit `feat(runctl): maven and node egress profiles`.
- [ ] **Step 6 (ANR):** the cnref composition's `_EGRESS_PROFILES` gains the two profiles: a host PR first if the
  composition lives in cnref (AGENTS.md rule), then the `HOST_REF` bump.

### Task 12: `runtime.image` and `runtime.size` per Repository (gap 2, D2)

**Files:** Repository spec (`Runtime{Image string; Size string}`), `config.Repository.Runtime`,
`internal/factory/runs/runs.go` (size from the repository, not always small), `internal/runctl/pod.go:227`
(harness image override), runctl config `runtime.allowedImages` (platform list of image prefixes), tests.

- [ ] **Step 1: Failing tests:** a Repository with `runtime.size: medium` yields AgentRuns with `size: medium`;
  `runtime.image` outside `runtime.allowedImages` refuses the Repository (`Accepted=False`, message names the
  list); inside it, the implementer pod's harness container uses it. An image must be pinned by digest.
- [ ] **Step 2: FAIL. Step 3: Implement.** The image must be built `FROM` the harness image (documented); the
  run-controller does not inspect layers. Size stays capped by `budgets` tiers: `large` needs the
  `frontier` tier.
- [ ] **Step 4: PASS. Step 5:** Commit `feat(factory): a repository picks its runtime image and size`.

### Task 13: Rebase run on `not_mergeable` (gap 7, M part)

**Files:** `internal/factory/reconciler/merge.go:359` (start a rebase run instead of waiting),
`internal/factory/reconciler/watch.go` (`revise` with a rebase brief), `config.Merge.RebaseOnConflict`
(default true), narrate, tests.

- [ ] **Step 1: Failing test:** a task whose merge answers `forge.ErrNotMergeable` and whose runs are under
  `fixRuns` starts one implementer run whose brief says "rebase `agent/<task>` onto `main`, resolve the
  conflicts, keep the change's intent, rerun the checks"; a second `not_mergeable` after that run hands off as
  today (`AwaitingHuman`, `not_mergeable`).
- [ ] **Step 2: FAIL. Step 3: Implement** with the existing fix-run counter, so a rebase costs a fix run.
- [ ] **Step 4: PASS. Step 5:** Commit `feat(factory): a conflicting pull request gets one rebase run`.

### Task 14: Developer next steps and `maku why` (gap 8)

**Files:** `internal/factory/reasons/reasons.go` (`Next` for 20 codes), `web/src/reasons.ts` (generated),
`internal/app/maku_why.go` + test, broker route reuse (`status` already returns the reason and the last tail).

- [ ] **Step 1:** Pick the 20 codes from `sum by (reason) (increase(maku_factory_task_end_total[30d]))` on gcp-0;
  if gcp-0 is unreachable, take the escalation and refusal codes the review names (`ci_red`, `not_mergeable`,
  `budget-person`, `budget_run`, `no_change`, `unauthorised_labeller`, `edited_after_label`, `unsanitisable`,
  `class_unarmed`, `shadow_would_arm`, `checks_failed`, `head_moved`, `timeout`, `no_acceptance`,
  `stale_approval`, `waiting_dependency`, `dependency_dropped`, `repository_not_served_here`, `not_maintainer`,
  `access_start_inactive`) and record that choice in the PR.
- [ ] **Step 2: Failing test:** none of the 20 has the default next step; each names one action a developer can
  take, without `kubectl`.
- [ ] **Step 3: Failing test for `maku why <task>`:** prints the reason in words, the failing step's last lines
  (at most 20) and the one command to try.
- [ ] **Step 4: Implement, PASS** (`hack/docs.sh` and the web build regenerate `reasons.ts`). **Step 5:** Commit
  `feat(cli): maku why, and developer next steps for the common reasons`.

### Task 15: Opt-in loopback service sidecars (gap 3, D3)

**Files:**
- Repository spec: `Services []string` (enum `postgres;redis`, max 2); `config.Repository.Services`
- `internal/runctl/services.go` + test, beside `browser.go`: one native sidecar per service
  (`restartPolicy: Always` init container), loopback listen (`127.0.0.1`), `emptyDir` data, the chart's
  resources (`services.<name>.resources`), restricted security context, readiness probe (`pg_isready`,
  `redis-cli ping`)
- `internal/runctl/config.go`: `images.postgres`, `images.redis` (required when a run asks for one)
- AgentRun spec `services` (immutable, like `screenshots`), the admission rule the browser uses (#760)
- Harness: export `DATABASE_URL=postgres://maku@127.0.0.1:5432/maku?sslmode=disable` and
  `REDIS_URL=redis://127.0.0.1:6379` to agent-checks steps (not scrubbed)
- Charts: images, resources, the run-pod rule; golden
- `docs/make-a-repository-ready.md`: `ciOnly` shrinks to what still needs Docker or a secret

- [ ] **Step 1: Failing golden test** `testdata/pod-services.golden.yaml`: a run with `services: [postgres]` has a
  `postgres` init container with `restartPolicy: Always`, no ports exposed off the pod, runs as non-root, and the
  pod's CNP gains nothing.
- [ ] **Step 2: Failing gVisor contract test** in the image (as `maku-browser`'s): Postgres starts as UID 10001
  on a read-only root with `emptyDir` at `/var/lib/postgresql/data` and `/var/run/postgresql`.
- [ ] **Step 3: Implement**, prove with `Smana/image-gallery`'s store tests on gcp-0 (Task 16).
- [ ] **Step 4: PASS. Step 5:** Commit `feat(runctl): opt-in loopback postgres and redis sidecars`.

### Task 16: Drills on `Smana/image-gallery`

Run the review's three drills against `Smana/image-gallery` on gcp-0 after Tasks 10, 11 and 15 land: day one
with write access only; a change to see (with the screenshots work); two developers on a bad day. Record
minutes, platform-team questions and which checks ran, skipped or failed on egress in a ledger under
`docs/superpowers/ledgers/`. Each failure becomes an issue naming its gap.

## Self-review

- Every gap of the ten has a task except 5, which is the screenshots workstream's (only its notice is here,
  Task 6). The "closing comment in tokens" note rides Task 3, as the review asks.
- D1 → Tasks 9 and 10; D2 → Tasks 11 and 12 (no free host list); D3 → Tasks 6 and 15; D4 → Task 16.
- Names used across tasks: `prBody`, `factsBlock`, `SandboxLimits`, `ImagesRemoved`, `CIOnly`, `MayStart`,
  `AccessWrite`, `CommentReviewIgnored`, `commandHelp`.
