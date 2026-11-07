# Merge policy view Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a factory room shows the merge policy that governs its task: class, mode, conditions,
what it takes to merge, the gate's decision, and the repository's class table.

**Architecture:** the factory adds a bounded `policy` task fact in `reconciler.factsFor`; the
broker stores it like every task fact; `summary.View` passes it through as `status.policy` with
two Go-built sentences (`how`, `decision`); the web room page renders a closed `Merge policy` fold.

**Tech Stack:** Go 1.x stdlib `testing`, TypeScript + vitest (happy-dom).

**Spec:** `docs/superpowers/specs/2026-11-07-merge-policy-view-design.md`

## Global Constraints

- At most 16 classes (`envelope.MaxPolicyClasses`); each pattern/login/check list at most 8
  entries (`envelope.MaxPolicyList`) of at most 120 runes (`envelope.MaxPolicyEntry`).
- Class names match `^[a-z0-9]([-a-z0-9]{0,62}[a-z0-9])?$`.
- `mode` ∈ {`live`, `shadow`, `unarmed`}; `mergedHow` ∈ {``, `class`, `approved`}.
- The factory clamps and sets `truncated`; it never posts facts the broker refuses.
- Every `how` ends with `A pull request that touches a gate path always needs a human merge.`
- Every Go file starts `// SPDX-License-Identifier: Apache-2.0`. stdlib `testing` only, table-driven.
- Test runs wrapped: `systemd-run --user --scope -q -p MemoryMax=6G …`.
- Commits: conventional, English, no attribution lines.

---

### Task 1: `envelope.PolicyFact`, validated

**Files:** Create `internal/envelope/policy.go`, `internal/envelope/policy_test.go`;
modify `internal/envelope/payloads.go` (`TaskFacts.Policy`, `Validate`); `docs/event-envelope.md`
(the `task` row).

**Produces:**
```go
type PolicyFact struct {
	Class          string            `json:"class,omitempty"`
	Mode           string            `json:"mode"`
	GateOff        bool              `json:"gateOff,omitempty"`
	ApproveToMerge bool              `json:"approveToMerge,omitempty"`
	Adopted        bool              `json:"adopted,omitempty"`
	Conditions     *PolicyConditions `json:"conditions,omitempty"`
	Classes        []PolicyClass     `json:"classes,omitempty"`
	MergedHow      string            `json:"mergedHow,omitempty"`
	Truncated      bool              `json:"truncated,omitempty"`
}
type PolicyConditions struct {
	AllowedFiles, ProtectedFiles, HoldoutChecks, AdoptLogins []string // omitempty each
	ModifyOnly bool; MaxPatchFiles, MaxModifiedLines, MaxPerDay int
	DiffShape, RequiredLabel string; Oracle bool
}
type PolicyClass struct { Name, Mode string; Adopted bool }
const PolicyLive, PolicyShadow, PolicyUnarmed = "live", "shadow", "unarmed"
func ValidClassName(s string) bool
func Clip(list []string) ([]string, bool) // clamps to MaxPolicyList entries of MaxPolicyEntry runes
func (p PolicyFact) validate() error
```

- [ ] Step 1: failing table test `TestPolicyFactValidate`: valid live fact passes; refused:
  mode `armed`, class `Bad_Name`, 17 classes, a class row with mode `x`, 9 allowedFiles, a
  121-rune pattern, mergedHow `auto`, adoptLogins `not a login`. Plus `TestClip` (9→8, true;
  a 130-rune entry cut to 120; nil stays nil, false). Plus a `TaskFacts.Validate` case that a bad
  policy surfaces as `task facts: policy: …`.
- [ ] Step 2: run `go test ./internal/envelope -run 'Policy|Clip'` → FAIL (undefined).
- [ ] Step 3: implement `policy.go`; add `Policy *PolicyFact \`json:"policy,omitempty"\`` to
  `TaskFacts` and `if f.Policy != nil { if err := f.Policy.validate(); err != nil { return fmt.Errorf("task facts: policy: %w", err) } }` to `Validate`.
- [ ] Step 4: run → PASS. Document `policy?{…}` in `docs/event-envelope.md`'s `task` row.
- [ ] Step 5: commit `feat(envelope): a policy task fact, the merge class that governs the task`.

### Task 2: the factory posts the policy fact

**Files:** Create `internal/factory/reconciler/policyfact.go`, `policyfact_test.go`; modify
`internal/factory/reconciler/facts.go` (`factsFor`).

**Consumes:** Task 1. `config.Config.NeedsMerger()`, `Repository.Classes`, `Repository.Merge.ApproveToMerge`,
`gateOf(rc, class)` (fills the built-in diff shape), `r.repo(t)`, `t.Status.PullRequest.MergedHow`.

**Produces:** `func policyOf(cfg *config.Config, rc *config.Repository, t *v1alpha1.Task) *envelope.PolicyFact`
(nil when `rc == nil`), and `f.Policy = policyOf(r.Cfg, r.repo(t), t)` in `factsFor`.

- [ ] Step 1: failing table test over `policyOf`: live + approve on → `{Class, Mode: live, ApproveToMerge: true}`;
  shadow; class absent from the table → `unarmed`; adopted class → `Adopted: true, ApproveToMerge: false`,
  `AdoptLogins` copied; no live/shadow class anywhere → `GateOff: true`; `docs-links` with no
  diffShape → `Conditions.DiffShape == "links-only"`; 20 classes → 16 rows sorted, `Truncated`;
  `MergedHow: approved` copied; the result passes `TaskFacts{Phase: "x", Policy: p}.Validate()`.
  A class name the envelope refuses (config allows `^[a-z0-9-]` already, but guard): skipped and `Truncated`.
- [ ] Step 2: run `go test ./internal/factory/reconciler -run PolicyOf` → FAIL.
- [ ] Step 3: implement; wire into `factsFor`.
- [ ] Step 4: run the package's tests → PASS (fact hashes in existing tests move only where a repo is configured; update expected facts there if any assert the whole struct).
- [ ] Step 5: commit `feat(factory): post the task's merge policy with its facts`.

### Task 3: `summary/v1` `status.policy`

**Files:** Create `internal/summary/policy.go`, `policy_test.go`; modify `internal/summary/summary.go`
(`Summary.Status.Policy`, `View`); `docs/api.md` (summary fields).

**Produces:**
```go
type PolicyView struct {
	envelope.PolicyFact
	How      string `json:"how"`
	Decision string `json:"decision,omitempty"`
}
const GatePathRule = "A pull request that touches a gate path always needs a human merge."
func PolicyHow(p envelope.PolicyFact) string
func PolicyDecision(f *envelope.TaskFacts) string
```
`PolicyDecision`: `mergedHow class` → "merged by the merger App: the class's conditions held";
`approved` → "merged by the merger App on @<approvedBy>'s approval" (PRFact.ApprovedBy, else "a maintainer's");
else phase `AwaitingHuman` with a reason in `reasons()` → that prose; else "".

- [ ] Step 1: failing tests: `PolicyHow` table over the spec's seven cases (exact strings, each ending in
  `GatePathRule`); `PolicyDecision` for shadow_would_arm, gate_path, merged class, merged approved, a
  running task (""); `View` of a state whose facts carry a policy has `Status.Policy` non-nil with `How`;
  a sealed room keeps it; no policy → nil.
- [ ] Step 2: run `go test ./internal/summary -run Policy` → FAIL.
- [ ] Step 3: implement; in `View`, inside `if f := st.Facts; f != nil`, set
  `if f.Policy != nil { s.Status.Policy = &PolicyView{PolicyFact: *f.Policy, How: PolicyHow(*f.Policy), Decision: PolicyDecision(f)} }`.
- [ ] Step 4: run `go test ./internal/summary ./internal/humanapi` → PASS. Document in `docs/api.md`.
- [ ] Step 5: commit `feat(summary): status.policy, the merge policy and what it takes to merge`.

### Task 4: the room page's Merge policy fold

**Files:** Create `web/src/policy.ts`, `web/test/policy.test.ts`; modify `web/src/summary.ts`
(type), `web/src/room.ts` (a `policy` block after the notes, rendered on each new summary),
`web/src/app.css` (minimal), `docs/web-ui.md` (room page table row); rebuild
`internal/humanapi/ui/dist/`.

**Produces:** `export function renderPolicy(root: HTMLElement, s: Summary): void`.

- [ ] Step 1: failing vitest: no `status.policy` → root empty; summary line
  `Merge policy · docs-links · live`; `<details>` closed; conditions line lists diff shape, caps,
  allowed files; `how` paragraph text equals `status.policy.how`; decision shown when present;
  class table rows `name · mode`; a class name `<img src=x>` renders as text (no `img` element);
  `gateOff` summary line `Merge policy · gate off`.
- [ ] Step 2: run `npx vitest run test/policy.test.ts` → FAIL.
- [ ] Step 3: implement with `el()` and `textContent` only; mount in `room.ts` next to `renderNotes`.
- [ ] Step 4: `npm run build` (or `task ui:check`'s build), vitest + `tsc --noEmit` → PASS.
- [ ] Step 5: commit `feat(web): a Merge policy fold on the room page`.

### Task 5: full gate, PR

- [ ] `systemd-run --user --scope -q -p MemoryMax=6G task check` → exit 0, cite it.
- [ ] Rebase on `origin/main`, push, `gh pr create`, verify with `gh pr view`, poll `statusCheckRollup`.

One PR, one commit per task: a stack would leave the UI PR without CI (its base is not `main`).
