# Audit T8 UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the room page render what the broker serves: offer only the run roles `start_run` takes (U-1), show and count an `answer` need (U-2), and label an approve-to-merge need "Approve:" (U-3).

**Architecture:** Three fixes in the vanilla TypeScript UI under `web/`, each pinned by a vitest case first, with the committed bundle under `internal/humanapi/ui/dist/` rebuilt in the same commit. U-1 also touches Go: `maku run` offers the same retired role, so the CLI's `QuickstartRoles` loses it too (see Spec deviations). One commit per finding, one PR.

**Tech Stack:** TypeScript 5.9, esbuild, vitest 5 + jsdom; Go 1.27 for the `maku` CLI and its `humanapi` test.

**Spec:** [`docs/superpowers/specs/2026-11-01-audit-fixes-design.md`](../specs/2026-11-01-audit-fixes-design.md) (theme T8, decision D0), with the findings in [`2026-11-01-project-audit.md`](../specs/2026-11-01-project-audit.md) (U-1, U-2, U-3). Read both first.

## Spec deviations

| # | Spec says | Reading shows | Plan does |
|---|---|---|---|
| U-1 | `tester` leaves both role selects, `docs/api.md` and `docs/cli.md` | `docs/cli.md:51` documents `maku run --role implementer\|tester\|triager`, and the code agrees: `internal/maku/run.go:23` `QuickstartRoles = {"implementer", "tester", "triager"}`, `internal/app/maku_run.go:20,31` and `internal/app/maku.go:74` print it. `maku run` sends `start_run`, which `runRoles` (`internal/humanapi/startrun.go:23`, `tester` removed in 96f10daa, tester F3) refuses with `bad_action`, after the room is already made. Fixing the doc alone would document a role the CLI still accepts | Task 1 also drops `tester` from `QuickstartRoles`, the CLI's usage and flag help, `run.go`'s refusal text and its test, and pins it with a Go test in `humanapi` that every `maku.QuickstartRoles` entry is in `runRoles` |
| U-3 | "the approve-to-merge fixture renders Approve:" | No fixture with `approve: true` exists: neither `web/test/fixtures/` nor `internal/summary/testdata/*.json` carries one | Task 3 adds the fixture inline in `web/test/summary.test.ts`, its `what` copied from `summary.ApproveHow` (`internal/summary/waiting.go:70`) |
| U-2 | "the pill count includes it" | `needs.ts`'s `count()` is `approvalCounts().mine + others`, and `others` is `needsItems(s).length` (`needs.ts:110-115`). Rendering the answer need in `needsItems` counts it with no change to `needs.ts`'s logic | Task 2 pins the count in `needs.test.ts` and changes `needsItems` only; `needs.ts` gets comment updates |

## Global Constraints

- **Stack:** no framework, no new npm dependency; DOM nodes and `textContent` only, never `innerHTML` (`render.test.ts` scans `web/src` for it).
- **Links:** every agent- or broker-supplied URL goes through `safeLink` (https only, else text).
- **Tests:** stdlib vitest; Go tests stdlib `testing` only, table-driven where there is a table.
- **Bundle:** any change under `web/src` is followed, in the same commit, by `task ui:build` and the rebuilt `internal/humanapi/ui/dist/` (`task ui:check` fails on a stale or untracked built file).
- **Gate:** `task check` exits 0 before the PR (D0). It needs a running Docker daemon (store tests).
- **Commits:** Conventional Commits, English, one per finding, **no** `Co-Authored-By` and no "Generated with" line (the owner's rule overrides any harness default).
- **Branch:** `fix/audit-t8-ui`, from `main` once the spec PR (`docs/audit-fixes-spec`) has merged. T8 has no "After" dependency: it may run in parallel with other themes in its own worktree.
- **Copy, verbatim:** need labels `Hold: `, `Merge: `, `Approve: `, `Answer: `; link texts `open the PR`, `open the issue`.

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `web/test/roles.test.ts` (new) | the role selects equal the server's `runRoles`, read from `startrun.go` | 1 |
| `web/src/controls.ts` | `runRoles`, the one list both selects take; the Run group's select | 1 |
| `web/src/fork.ts` | the fork panel's role select takes `runRoles` | 1 |
| `web/test/fork.test.ts` | the failed-run case forks as `reviewer`, not `tester` | 1 |
| `internal/humanapi/startrun_test.go` | `maku.QuickstartRoles` ⊆ `runRoles` | 1 |
| `internal/maku/run.go`, `run_test.go` | `QuickstartRoles` without `tester`; the refusal's text | 1 |
| `internal/app/maku_run.go`, `internal/app/maku.go` | the `run` usage and `--role` help | 1 |
| `docs/api.md`, `docs/cli.md` | `start_run`'s roles; `run` and `fork`'s `--role` | 1 |
| `web/src/summary.ts` | `Need` gains `answer` and `author?`; `needsItems` renders answer and approve | 2, 3 |
| `web/src/needs.ts` | comments only: the panel lists answers too | 2 |
| `web/test/summary.test.ts` | answer need rendered and linked; approve-to-merge fixture | 2, 3 |
| `web/test/needs.test.ts` | an answer need is listed and counted | 2 |
| `docs/web-ui.md` | the Needs you row names answers and Approve | 2, 3 |
| `internal/humanapi/ui/dist/*` | the committed bundle | 1, 2, 3 |

## How to run things

```bash
cd web
npm ci                                   # once per worktree, inside this repository's own web/
npx vitest run test/roles.test.ts        # one file
npx tsc --noEmit                         # the type check
cd .. && task ui:build                   # rebuild internal/humanapi/ui/dist
go test -race -count=1 ./internal/humanapi -run TestQuickstartRolesAreRunRoles
```

---

### Task 1: U-1, the role selects offer only what `start_run` takes

**Files:**
- Create: `web/test/roles.test.ts`
- Modify: `web/src/controls.ts` (the Run group's `select("role", …)`, ~:219), `web/src/fork.ts` (~:21), `web/test/fork.test.ts:66`
- Modify: `internal/humanapi/startrun_test.go`, `internal/maku/run.go:21-23,68`, `internal/maku/run_test.go:223`, `internal/app/maku_run.go:20,31`, `internal/app/maku.go:74`
- Modify: `docs/api.md:452`, `docs/cli.md:51,60`
- Rebuild: `internal/humanapi/ui/dist/`

**Interfaces:**
- Consumes: `mountControls(root, conn, state, you, say)` and `mountFork(root, conn, state)` as they are; Go `runRoles map[string]bool` (unexported, `internal/humanapi/startrun.go`).
- Produces: `export const runRoles: string[]` in `web/src/controls.ts` = `["implementer", "reviewer", "triager"]`; Go `maku.QuickstartRoles = []string{"implementer", "triager"}`.

- [ ] **Step 1: Create the branch**

```bash
git switch main && git pull --ff-only
git switch -c fix/audit-t8-ui
cd web && npm ci && cd ..
```

- [ ] **Step 2: Write the failing UI test**

Create `web/test/roles.test.ts`:

```ts
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { mountControls } from "../src/controls";
import { mountFork } from "../src/fork";
import { RoomState } from "../src/room-state";

// The roles the broker starts, read from internal/humanapi/startrun.go's runRoles, so the page
// can never offer a role start_run refuses (U-1).
function serverRunRoles(): string[] {
  const src = readFileSync(join(__dirname, "../../internal/humanapi/startrun.go"), "utf8");
  const m = /runRoles\s*=\s*map\[string\]bool\{([^}]*)\}/.exec(src);
  if (!m) throw new Error("runRoles not found in internal/humanapi/startrun.go");
  return [...m[1].matchAll(/"([a-z]+)":\s*true/g)].map((r) => r[1]).sort();
}

// values are a select's option values, the fork's "no run" ("") left out.
const values = (root: HTMLElement, name: string) =>
  [...root.querySelectorAll<HTMLOptionElement>(`select[name="${name}"] option`)].map((o) => o.value).filter(Boolean).sort();

describe("the run role selects", () => {
  const conn = { send: () => true };
  it("offer exactly the roles the broker starts", () => {
    const want = serverRunRoles();
    expect(want.length).toBeGreaterThan(0);
    const controls = document.createElement("section");
    mountControls(controls, conn, new RoomState("human:a", 1),
      { principal: "human:a", role: "owner", approver: false, driver: true, webUI: true }, () => {});
    expect(values(controls, "role")).toEqual(want);
    const fork = document.createElement("section");
    mountFork(fork, conn, new RoomState("human:a", 1));
    expect(values(fork, "forkRole")).toEqual(want);
  });
});
```

- [ ] **Step 3: Write the failing Go test**

In `internal/humanapi/startrun_test.go`, add `"github.com/Smana/maku/internal/maku"` to the Smana import group, and append:

```go
// U-1: maku run offers only roles start_run takes; one it refused would fail after the room is
// already made.
func TestQuickstartRolesAreRunRoles(t *testing.T) {
	for _, r := range maku.QuickstartRoles {
		if !runRoles[r] {
			t.Errorf("maku run offers %q, which start_run refuses", r)
		}
	}
}
```

- [ ] **Step 4: Run both to verify they fail**

Run: `cd web && npx vitest run test/roles.test.ts; cd ..`
Expected: FAIL, `expected [ 'implementer', 'reviewer', 'tester', 'triager' ] to deeply equal [ 'implementer', 'reviewer', 'triager' ]`.

Run: `go test -race -count=1 ./internal/humanapi -run TestQuickstartRolesAreRunRoles`
Expected: FAIL, `maku run offers "tester", which start_run refuses`.

- [ ] **Step 5: One role list in the UI**

In `web/src/controls.ts`, above `mountControls` (after `group`), add:

```ts
// runRoles are the roles a human starts a run as: the broker's runRoles
// (internal/humanapi/startrun.go), which roles.test.ts reads to keep the two equal. The tester
// is the factory's alone (tester F3).
export const runRoles = ["implementer", "reviewer", "triager"];
```

Replace the Run group's select line:

```ts
  const role = select("role", "Start a run as", [["implementer", "implementer"], ["reviewer", "reviewer"], ["tester", "tester"], ["triager", "triager"]]);
```

with:

```ts
  const role = select("role", "Start a run as", runRoles.map((r): [string, string] => [r, r]));
```

In `web/src/fork.ts`, add `runRoles` to the `./controls` import:

```ts
import { act, claimText, el, named, rejection, runRoles, select, type Sender } from "./controls";
```

and replace:

```ts
  const role = select("forkRole", "role of the fork's first run", [["", "no run"], ["implementer", "implementer"], ["reviewer", "reviewer"], ["tester", "tester"], ["triager", "triager"]]);
```

with:

```ts
  const role = select("forkRole", "role of the fork's first run", [["", "no run"], ...runRoles.map((r): [string, string] => [r, r])]);
```

- [ ] **Step 6: The fork test forks as a role the broker starts**

In `web/test/fork.test.ts`, test "says why the fork's run failed, beside the fork it made" (~:66), replace:

```ts
    f.field<HTMLSelectElement>("forkRole").value = "tester";
```

with:

```ts
    f.field<HTMLSelectElement>("forkRole").value = "reviewer";
```

(jsdom leaves a select's value empty when no option matches, so without this the test sends no role.)

- [ ] **Step 7: `maku run` loses the tester**

`internal/maku/run.go`, replace the `QuickstartRoles` block:

```go
// QuickstartRoles are the roles maku run starts: a reviewer needs a pull request, and a new
// room names none.
var QuickstartRoles = []string{"implementer", "tester", "triager"}
```

with:

```go
// QuickstartRoles are the roles maku run starts: those start_run takes
// (humanapi's TestQuickstartRolesAreRunRoles), less the reviewer, who needs a pull request a new
// room names none of. The tester is the factory's alone.
var QuickstartRoles = []string{"implementer", "triager"}
```

and in `Do`, replace `"role %q: maku run starts an implementer, a tester or a triager"` with `"role %q: maku run starts an implementer or a triager"`.

`internal/maku/run_test.go:223`, replace `"implementer, a tester or a triager"` with `"implementer or a triager"`.

`internal/app/maku_run.go`: replace

```go
const runUsage = "run --repo owner/name --task TEXT [--role implementer|tester|triager]"
```

with

```go
const runUsage = "run --repo owner/name --task TEXT [--role implementer|triager]"
```

and `fs.String("role", "implementer", "implementer, tester or triager")` with `fs.String("role", "implementer", "implementer or triager")`.

`internal/app/maku.go:74`, replace `  run --repo owner/name --task TEXT [--role implementer|tester|triager]` with `  run --repo owner/name --task TEXT [--role implementer|triager]`.

- [ ] **Step 8: The docs**

`docs/api.md:452` (`start_run` row): replace ``| `start_run` | `role` (`implementer`, `reviewer`, `tester`, `triager`), `` with ``| `start_run` | `role` (`implementer`, `reviewer`, `triager`), ``.

`docs/cli.md:51`: replace ``[--role implementer\|tester\|triager]`` with ``[--role implementer\|triager]``.

`docs/cli.md:60` (`fork` row): replace ``--role`: `implementer`, `reviewer`, `tester` or `triager`;`` with ``--role`: `implementer`, `reviewer` or `triager`;``.

Then check nothing else still offers it: `grep -rn 'tester' web/src docs/api.md docs/cli.md internal/maku/run.go internal/app/maku_run.go internal/app/maku.go` prints no role list (the `room_handoff`/`room_verdict` rows at `docs/api.md:835-836` name the harness's tester and stay).

- [ ] **Step 9: Run the tests to verify they pass**

Run: `cd web && npx vitest run test/roles.test.ts test/fork.test.ts test/controls.test.ts && npx tsc --noEmit; cd ..`
Expected: PASS, no type error.

Run: `go test -race -count=1 ./internal/humanapi ./internal/maku ./internal/app`
Expected: PASS.

- [ ] **Step 10: Rebuild the bundle and commit**

```bash
task ui:build
git add web/test/roles.test.ts web/src/controls.ts web/src/fork.ts web/test/fork.test.ts \
  internal/humanapi/startrun_test.go internal/maku/run.go internal/maku/run_test.go \
  internal/app/maku_run.go internal/app/maku.go docs/api.md docs/cli.md internal/humanapi/ui/dist
git commit -m "fix(ui): the role selects and maku run offer only the roles start_run takes, the tester gone (U-1)"
```

---

### Task 2: U-2, an `answer` need shows in Needs you and counts

**Files:**
- Modify: `web/src/summary.ts:39-40` (the `needsYou` type), `:115-128` (`needsItems`)
- Modify: `web/src/needs.ts` (header comment, `count` comment)
- Test: `web/test/summary.test.ts` (`describe("needsItems")`), `web/test/needs.test.ts` (`describe("the needs-you panel")`)
- Modify: `docs/web-ui.md:82`
- Rebuild: `internal/humanapi/ui/dist/`

**Interfaces:**
- Consumes: the Go `summary.Need` shape (`internal/summary/summary.go:127-139`): `kind` (`approval|hold|merge|answer`), `id`, `what`, `deadline` (omitzero), `url`, `author` (omitempty, answer only), `approve` (omitempty, merge only). `summary.Answer` (`internal/summary/waiting.go:95`) sets `ID` to the issue number, `URL` to the issue's URL, `Author` to the issue author's login, and a `What` with two backticked spans.
- Produces: `Summary["needsYou"][number]` = `{ kind: "approval" | "hold" | "merge" | "answer"; id: string; what: string; deadline?: string; url: string; author?: string; approve?: boolean }`; `needsItems` returns one `li` per hold, merge and answer need.

- [ ] **Step 1: Write the failing tests**

In `web/test/summary.test.ts`, inside `describe("needsItems", …)`, after the "renders a merge need's backticked command as code" case, add:

```ts
  // U-2: a task the scoper bounced waits on its issue's author; the room's owner sees it,
  // linked to the issue, each backticked command set as code.
  it("renders an answer need with its instruction and the issue link", () => {
    const s = base();
    s.needsYou = [{ kind: "answer", id: "3", url: "https://github.com/o/r/issues/3", author: "alice",
      what: "@alice answers the scoper's question by editing issue #3, then a maintainer re-applies `factory/ready`; or a maintainer comments `/factory retry` to proceed on its assumption" }];
    const items = needsItems(s);
    expect(items).toHaveLength(1);
    expect(items[0].textContent).toMatch(/^Answer: @alice answers the scoper's question by editing issue #3/);
    expect(items[0].querySelector("a")?.getAttribute("href")).toBe("https://github.com/o/r/issues/3");
    expect(items[0].querySelector("a")?.textContent).toBe("open the issue");
    expect([...items[0].querySelectorAll("code")].map((c) => c.textContent)).toEqual(["factory/ready", "/factory retry"]);
  });
```

In the same `describe`, extend "links only https URLs": replace

```ts
    s.needsYou = [{ kind: "merge", id: "7", what: "merge PR #7", url: "javascript:alert(1)" }];
```

with

```ts
    s.needsYou = [{ kind: "merge", id: "7", what: "merge PR #7", url: "javascript:alert(1)" },
      { kind: "answer", id: "3", what: "answer on issue #3", url: "http://github.com/o/r/issues/3" }];
```

and after `expect(r.textContent).toContain("javascript:alert(1)");` add:

```ts
    expect(r.textContent).toContain("http://github.com/o/r/issues/3");
```

In `web/test/needs.test.ts`, inside `describe("the needs-you panel", …)`, after "lists a merge and a hold from the summary…", add:

```ts
  // U-2: the answer a bounced task waits for is listed, linked to the issue, and counted in the pill.
  it("lists an answer need from the summary and counts it", () => {
    const { root, needs } = mount();
    needs.setSummary(sum([{ kind: "answer", id: "3", what: "@alice answers the scoper's question by editing issue #3",
      url: "https://github.com/o/r/issues/3", author: "alice" }]));
    expect([...root.querySelectorAll("li")].map((l) => l.textContent))
      .toEqual(["Answer: @alice answers the scoper's question by editing issue #3 · open the issue"]);
    expect(needs.count()).toBe(1);
    expect(root.hidden).toBe(false);
  });
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd web && npx vitest run test/summary.test.ts test/needs.test.ts; cd ..`
Expected: FAIL: `expected [] to have a length of 1` (summary), `expected [] to deeply equal [ 'Answer: …' ]` (needs). `npx tsc --noEmit` also fails: `Type '"answer"' is not assignable to type '"approval" | "hold" | "merge"'`.

- [ ] **Step 3: Type and render the answer need**

In `web/src/summary.ts`, replace

```ts
  // approve: a merge need a maintainer's approval settles (approveToMerge); absent otherwise.
  needsYou: { kind: "approval" | "hold" | "merge"; id: string; what: string; deadline?: string; url: string; approve?: boolean }[];
```

with

```ts
  // approve: a merge need a maintainer's approval settles (approveToMerge); absent otherwise.
  // answer: a task the scoper bounced waits on its issue's author (scoper SC8); id is the issue
  // number, url the issue, author its author's GitHub login. Absent for every other kind.
  needsYou: { kind: "approval" | "hold" | "merge" | "answer"; id: string; what: string; deadline?: string; url: string;
    author?: string; approve?: boolean }[];
```

Replace `needsItems` and its comment:

```ts
// needsItems is the summary's merges and holds, one list item each; the approvals come
// from the live log instead (needs.ts), so the summary's approval entries are skipped.
export function needsItems(s: Summary): HTMLLIElement[] {
  const out: HTMLLIElement[] = [];
  for (const n of s.needsYou) {
    if (n.kind === "hold") out.push(el("li", { textContent: `Hold: ${n.what}` })); // the page is the room: nothing to link to
    if (n.kind === "merge") { // the PR is on GitHub: link out, https only
      const li = el("li");
      li.append("Merge: ", ...inlineCode(n.what), " · ", safeLink("open the PR", n.url));
      out.push(li);
    }
  }
  return out;
}
```

with

```ts
// linkedNeed is a need decided on GitHub: its label, its instruction with the backticked commands
// as code, and the link out (https only).
function linkedNeed(label: string, what: string, link: Node): HTMLLIElement {
  const li = el("li");
  li.append(`${label}: `, ...inlineCode(what), " · ", link);
  return li;
}

// needsItems is the summary's merges, answers and holds, one list item each; the approvals come
// from the live log instead (needs.ts), so the summary's approval entries are skipped.
export function needsItems(s: Summary): HTMLLIElement[] {
  const out: HTMLLIElement[] = [];
  for (const n of s.needsYou) {
    if (n.kind === "hold") out.push(el("li", { textContent: `Hold: ${n.what}` })); // the page is the room: nothing to link to
    if (n.kind === "merge") out.push(linkedNeed("Merge", n.what, safeLink("open the PR", n.url)));
    if (n.kind === "answer") out.push(linkedNeed("Answer", n.what, safeLink("open the issue", n.url)));
  }
  return out;
}
```

- [ ] **Step 4: Say so in the panel's comments**

In `web/src/needs.ts`, header comment: replace `pending approval, command first, decided right there, then the summary's merges and` / `// holds.` with `pending approval, command first, decided right there, then the summary's merges,` / `// answers and holds.` (keep the rest of the sentence). Replace `// count is what needs this viewer: the approvals it may decide, then merges and holds.` with `// count is what needs this viewer: the approvals it may decide, then merges, answers and holds.` and `// A viewer who decides nothing here and has no merge or hold reads the cards as` with `// A viewer who decides nothing here and has no merge, answer or hold reads the cards as`.

- [ ] **Step 5: The docs**

`docs/web-ui.md:82`, replace `then the PRs waiting for your merge and the tasks held on your room.` with `then the PRs waiting for your merge, the issues whose author must answer the scoper (linked to the issue), and the tasks held on your room.`

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd web && npx vitest run test/summary.test.ts test/needs.test.ts && npx tsc --noEmit; cd ..`
Expected: PASS, no type error.

- [ ] **Step 7: Rebuild the bundle and commit**

```bash
task ui:build
git add web/src/summary.ts web/src/needs.ts web/test/summary.test.ts web/test/needs.test.ts docs/web-ui.md internal/humanapi/ui/dist
git commit -m "fix(ui): an answer need shows in Needs you, linked to its issue, and counts in the pill (U-2)"
```

---

### Task 3: U-3, an approve-to-merge need reads "Approve:"

**Files:**
- Modify: `web/src/summary.ts` (`needsItems`'s merge line)
- Test: `web/test/summary.test.ts` (`describe("needsItems")`)
- Modify: `docs/web-ui.md:82`
- Rebuild: `internal/humanapi/ui/dist/`

**Interfaces:**
- Consumes: `linkedNeed(label, what, link)` and the `approve?: boolean` field from Task 2; `summary.Merge` sets `approve: true` with `ApproveHow`'s `what` when `TaskFacts.MergeByApproval` (`internal/summary/waiting.go:80-89`).
- Produces: a merge need with `approve: true` renders `Approve: <what> · open the PR`; without it, `Merge: …` as before.

- [ ] **Step 1: Write the failing test**

In `web/test/summary.test.ts`, inside `describe("needsItems", …)`, after the Task 2 case, add:

```ts
  // U-3: the approve-to-merge fixture. Under merge.approveToMerge the need is an approval of the
  // current commit, so it says Approve, as the step and the status line do (#308, #310). Its what
  // is summary.ApproveHow's.
  const approveToMerge: Summary["needsYou"][number] = { kind: "merge", id: "7", url: "https://github.com/o/r/pull/7", approve: true,
    what: "a maintainer approves PR #7: approve the head you read on the PR page, or run `gh api repos/o/r/pulls/7/reviews -f commit_id=<sha> -f event=APPROVE`; the merger merges that head" };
  it("labels an approve-to-merge need Approve, not Merge", () => {
    const s = base();
    s.needsYou = [approveToMerge];
    const block = needs(s);
    expect(block.textContent).toMatch(/^Approve: a maintainer approves PR #7/);
    expect(block.textContent).not.toContain("Merge:");
    expect(block.querySelector("a")?.getAttribute("href")).toBe("https://github.com/o/r/pull/7");
  });
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx vitest run test/summary.test.ts -t "Approve"; cd ..`
Expected: FAIL: `expected 'Merge: a maintainer approves PR #7: …' to match /^Approve: …/`.

- [ ] **Step 3: Label by `approve`**

In `web/src/summary.ts`'s `needsItems`, replace

```ts
    if (n.kind === "merge") out.push(linkedNeed("Merge", n.what, safeLink("open the PR", n.url)));
```

with

```ts
    // approve: under approveToMerge an approval of the current commit merges it (#308, #310).
    if (n.kind === "merge") out.push(linkedNeed(n.approve ? "Approve" : "Merge", n.what, safeLink("open the PR", n.url)));
```

- [ ] **Step 4: The docs**

`docs/web-ui.md:82`, replace `then the PRs waiting for your merge,` with ``then the PRs waiting for your merge (`Approve:` rather than `Merge:` under `merge.approveToMerge`, where approving the current commit merges it),``.

- [ ] **Step 5: Run the UI suite to verify it passes**

Run: `cd web && npm test && npx tsc --noEmit; cd ..`
Expected: PASS, every file; "renders a merge with its instruction and the PR link" still reads `Merge: merge PR #7: tick bypass`.

- [ ] **Step 6: Rebuild the bundle and commit**

```bash
task ui:build
git add web/src/summary.ts web/test/summary.test.ts docs/web-ui.md internal/humanapi/ui/dist
git commit -m "fix(ui): an approve-to-merge need reads Approve, not Merge (U-3)"
```

---

### Task 4: The gate

**Files:** none changed, unless the gate finds something.

- [ ] **Step 1: Run the whole gate**

Run: `task check`
Expected: exit 0. `ui:test` runs `npm ci`, `tsc --noEmit`, `vitest run`; `ui:check` rebuilds and finds `internal/humanapi/ui/dist` unchanged and nothing untracked; `docs` passes on `docs/web-ui.md`, `docs/api.md`, `docs/cli.md`.

- [ ] **Step 2: If `ui:check` fails**

A task committed source without its rebuild. Run `task ui:build`, then `git add internal/humanapi/ui/dist && git commit -m "build(ui): rebuild the bundle"`, and rerun `task check`.

- [ ] **Step 3: Hand off**

`git log --oneline main..` shows three `fix(ui)` commits, one per finding (D0). Hand the branch to superpowers:finishing-a-development-branch; push and open the PR only when the owner asks. The PR description is in English and carries no "Generated with" line.

## Self-review

| Spec item | Task |
|---|---|
| U-1: `tester` out of both selects, `docs/api.md`, `docs/cli.md`; `fork.test.ts` uses `reviewer`; selects equal `runRoles` | 1 (plus the CLI, see Spec deviations) |
| U-2: `kind: "answer"`, `author?`; rendered through `safeLink`; counted | 2 |
| U-3: `approve` renders "Approve:"; the fixture | 3 |
| D0: one PR, one commit per finding, test first, `task check` 0 | 1–4 |
