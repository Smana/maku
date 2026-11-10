# Factory resources, plan 3 of 4: status, and settings pages that output a manifest

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kubectl get factory,repository,mergepolicy` shows what the factory made of each object.
The leader writes conditions, the served generation, the MergePolicy authority, and each class's
mode and last decision. The settings pages offer the MergePolicy manifest, not the repository-file
text, for a repository served from resources.

**Architecture:** A pure `statuswriter.Desired(...)` computes each object's status from the config
source's outcomes, the published config, and small inputs: per-repository GitHub health, the
policy watch's snapshots, the kill switch, and a last-decision recorder. A leader-only runnable
compares the result with the stored status and writes only on change (`Status().Update` with
retry on conflict). The factory's policy report gains the repository's MergePolicy manifest when it
is served from one, and the UI's file actions use it.

**Tech Stack:** Go, controller-runtime, envtest; TypeScript (plain DOM), vitest.

**Spec:** `docs/superpowers/specs/2026-11-09-factory-resources-design.md`: D9 (read-only pages,
YAML out), D10 and D11 (the v1 status: conditions, authority, per-class mode and last decision,
Connected, Ready, Stopped; **no counts**), D13 (only the leader writes status). **Builds on plans 1
and 2:**
- `configsource.Source.Outcomes() ([]Outcome, Synced)` with `Outcome{Kind, Name, Repository, ObservedGeneration, ServedGeneration, Accepted, Reason, Message}` and `Synced{At, Err}` (the last Sync's time and error), and `Source.OnSynced func()`, called after each Sync with no lock held. **Never call `Outcomes` from a `config.Live` subscriber**: Publish runs under the Source's lock;
- `repoConns.wireErr(name) error`, each repository's last wiring error (nil once wired);
- `config.Live` (`Current`, `Subscribe`);
- the status types in `api/factory/v1alpha1/factoryconfig_types.go`.

**Inventory:** `.superpowers/sdd/plan3-inventory.md` in the implementation worktree, with file:line
for every source.

## Global Constraints

- Only the leader writes status (`NeedLeaderElection() == true`). A write happens only when the computed status differs from the stored one (compared without `lastTransitionTime`). Conflicts retry with `retry.RetryOnConflict(retry.DefaultBackoff, …)`.
- Conditions use `apimeta.SetStatusCondition`, so `lastTransitionTime` changes only when the status changes. `observedGeneration` is the object's generation, both on each condition and at status level.
- Condition types: `Accepted` (all three kinds); `Connected` (Repository); `RepositoryFileValid` and `InSync` (MergePolicy); `Ready` (Factory). Reasons are CamelCase. Accepted's reasons are the `v1alpha1.Reason*` constants, plus `Served` when true.
- When `policyManagement.read` is off there is no policy watch, so `RepositoryFileValid` and `InSync` are `Unknown` with reason `PolicyReadOff`.
- The mode `off` in code is `unarmed` in status.
- The last decision is best-effort and in memory: the leader records it as it posts a decision marker. After a restart or a leader change it is empty until the next decision. Say so in the field's doc comment.
- No counts and no spend (D10, D11: after #420).
- Every `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`. `task check` exits 0 before every commit. Conventional commits in English, no trailers, no co-author.

---

### Task 0: A removed repository's tasks are nobody's (carried from plan 2's review)

**Files:** `internal/factory/intake/issues.go` (`owns`, ~139), `internal/factory/reconciler/reconciler.go` (`notServed`, ~258), and the tests beside them.

- `owns` answers true for every task whenever exactly one repository is configured. After a 2→1 publish, the remaining repository's poller treats the removed repository's tasks as its own: `stops` (~573), `reverts` (~636), `openTasks` (~509) and `publish.go`. A stop or revert label on its issue or PR number *n* can then annotate the removed repository's task with the same number.

  Compare with `strings.EqualFold(t.Spec.Repository, p.repo(cfg).Name)` only. Legacy tasks carry the entry's name, so they still match.
- The `notServed` once-set never forgets a task deleted while its repository is unserved: forget it on the NotFound path.
- [ ] Tests first: after a 2→1 publish, a stop label on the remaining repository's issue *n* does not touch the removed repository's task *n*. A deleted task is forgotten. Run them and watch them fail.
- [ ] Implement. `task check` passes. Commit `fix(intake): a task belongs to its own repository only`.

---

### Task 1: The last-decision recorder

**Files:** create `internal/factory/decision/recorder.go` and `recorder_test.go`. Modify the
reconciler sites that post a `decision.Marker` (`grep -rn "decision.Marker(" internal/factory/reconciler`).

**Interfaces (produces):**

```go
// Last is one class's latest decision on a repository.
type Last struct {
	Verdict, Reason string
	At              time.Time
}
// Recorder keeps the latest decision per repository and class, in memory (best-effort: empty
// after a restart). Safe for concurrent use. The zero value is ready.
type Recorder struct{ /* mu sync.RWMutex; m map[string]map[string]Last */ }
func (r *Recorder) Record(repository, class, verdict, reason string, at time.Time) // keeps the newest by At
func (r *Recorder) Latest(repository string) map[string]Last                       // a copy; repository lowercased
```

- [ ] Test first: record two decisions for one class out of order, and `Latest` returns the newer. Repository keys are case-insensitive. `Latest` returns a copy. Concurrent `Record`/`Latest` passes under `-race`.
- [ ] Implement. Give the reconciler a `Decisions *decision.Recorder` field (nil-safe: skip when nil). At each site that posts a marker, record repository, class, verdict, reason and the time it posts with (the reconciler's clock).
- [ ] Add a reconciler test: a merge decision posted by an existing test path is recorded. Reuse a test that already asserts a marker.
- [ ] **D5, carried from plan 2's final review:** each merge decision records the MergePolicy generation it used. Add `policy=<n>` to the decision marker when `cfg.GenerationsFor(repo)` gives a MergePolicy generation above 0, and keep it in `Last` (`Generation int64`). `policyview.Decisions` keeps parsing markers without it, and an old marker parses with generation 0. Test both.
- [ ] `task check` passes. Commit `feat(decision): remember each class's latest decision`.

---

### Task 2: `statuswriter.Desired`

**Files:** create `internal/factory/statuswriter/desired.go` and `desired_test.go`.

**Interfaces (produces):**

```go
// Inputs are what the factory knows besides the objects themselves.
type Inputs struct {
	Outcomes   []configsource.Outcome
	Synced     configsource.Synced // stale or failing source → Factory Ready=False, reason SyncFailing
	WireErr    func(repository string) error // the repository's last wiring error, nil when wired
	Config     *config.Config                      // the published config
	Connected  func(repository string) (bool, string) // healthy, and a message when not
	Snapshot   func(repository string) (mergepolicy.Snapshot, bool) // nil when policyManagement.read is off
	Decisions  func(repository string) map[string]decision.Last
	Stopped    bool
	GitHubUp   bool // every served repository healthy
}
// Desired returns the status each object should have, keyed "Kind/name".
func Desired(in Inputs, factories []v1alpha1.Factory, repos []v1alpha1.Repository, policies []v1alpha1.MergePolicy, now metav1.Time) map[string]any
```

Return typed values: `v1alpha1.FactoryStatus`, `RepositoryStatus` and `MergePolicyStatus`.

**Rules:**
- **Accepted**, every kind:
  - Find the object's outcome by `Kind` and `Name`. True with reason `Served` when `Accepted`.
  - When it is not accepted but `ServedGeneration > 0`: False, with the outcome's reason and the message `"<message>; serving generation <n>"`.
  - Otherwise: False, with the outcome's reason and message.
  - An object missing from the outcomes: Unknown, reason `Pending`.
- **Repository:**
  - `Connected` comes from `Connected(spec.repository)`, but only when the repository is served (it appears in `Config.Repos()`).
  - When it is not served and `WireErr` returns an error: False, reason `WiringFailed`, with the error as the message.
  - When it is not served otherwise: False, reason `NotServed`.
  - `status.mergePolicy` names the MergePolicy whose `spec.repositoryRef` is this Repository's name.
- **MergePolicy:**
  - `authority` = `Config.RepositoryAuthoritative(r)` ? `repository` : `central`, for the served repository.
  - `classes`: the served repository's `Classes` from `Config.RepositoryFor(...)`, so a cut-over repository shows the repository file's classes. Sort them by name. Each class's mode is live, shadow or unarmed, and its last decision is `verdict:reason`, truncated to 63 characters, with its time from `Decisions`.
  - `classCount` and `liveCount` follow from the classes.
  - `RepositoryFileValid` and `InSync` come from `Snapshot`: valid means `Err == nil && HasClasses`; InSync means valid and `len(Divergences) == 0`. The message names up to 5 divergences. With no snapshot for the repository: Unknown, reason `NotRead`. With `Snapshot == nil`: Unknown, reason `PolicyReadOff`.
- **Factory:**
  - `repositories` = `len(Config.Repos())`.
  - `stopped` = `Stopped`.
  - `Ready` is True when the Factory is accepted, `GitHubUp`, and `Synced.Err == nil` with `Synced.At` within 15 minutes. Otherwise it is False with reason `NotAccepted`, `GitHubDown` or `SyncFailing` (the message carries `Synced.Err`, or "no sync since <At>").
- A refused object's other conditions are still computed when its repository is served, from its last good version.

- [ ] Table tests first, one per rule above, built from plain structs. No API server. Run them and watch them fail.
- [ ] Implement. `task check` passes. Commit `feat(statuswriter): compute what each factory resource's status should say`.

---

### Task 3: The writer, and its wiring

**Files:** create `internal/factory/statuswriter/writer.go`, `writer_envtest_test.go`. Modify `internal/app/factory.go`.

**Interfaces (produces):**

```go
type Writer struct {
	Client    client.Client // the manager's
	Namespace string
	Inputs    func(ctx context.Context) Inputs
	Every     time.Duration // 30s when zero
	Log       *slog.Logger
	// unexported: kick chan
}
func (w *Writer) Kick()                                   // coalesced
func (w *Writer) Start(ctx context.Context) error          // ticks on Every and Kick
func (w *Writer) NeedLeaderElection() bool { return true }
func (w *Writer) WriteOnce(ctx context.Context) error      // list the three kinds, Desired, update changed ones
```

- [ ] envtest first: create a Repository and a MergePolicy. Run `WriteOnce` with Inputs whose outcomes accept both. Assert `Accepted=True`, `observedGeneration`, `status.mergePolicy`, and the class list. Run `WriteOnce` again and assert the `resourceVersion` is unchanged (no write without a change). Then change the outcome to refused and assert `Accepted=False` with the reason.
- [ ] Implement `WriteOnce`: list the three kinds in the namespace and compute `Desired`. For each object whose status differs, ignoring `lastTransitionTime` (compare after copying the stored times into the desired conditions with `SetStatusCondition` semantics), call `Status().Update` inside `RetryOnConflict`, re-getting the object on each retry.
- [ ] Wiring in `factory.go`:
  - build a `decision.Recorder` and set `rec.Decisions`;
  - build `statuswriter.Writer{Client: mgr.GetClient(), Namespace: ns, Inputs: …}`, where Inputs reads:
    - `src.Outcomes()` and `live.Current()`;
    - `Connected` from `conns.get(name).gh.Healthy(time.Now())`;
    - `Snapshot` from the policy watch when it exists (keep the `*mergepolicy.Watch` in a variable; nil otherwise);
    - `Decisions` from the recorder;
    - `Stopped` from `killswitch.Engaged` (an error counts as false and is logged);
    - `GitHubUp` from `conns.healthy(time.Now())`;
  - `src.OnSynced = w.Kick` (NOT a `live.Subscribe`: a subscriber runs under the Source's lock), `mgr.Add(w)`.
  - Inputs also read `Synced` from `src.Outcomes()` and `WireErr` from `conns.wireErr`.
- [ ] `task check` passes. Commit `feat(factory): the leader writes the factory resources' status`.

---

### Task 4: Settings pages output the MergePolicy manifest

**Files:**
- `internal/factory/evidence/service.go` (the `Report` gains `MergePolicy` and `ManifestPath`)
- `internal/factory/policyview` (if the report is built there)
- `web/src/settings.ts`, `web/src/policy-editor.ts`, and the tests beside them
- `internal/humanapi/ui/dist/` (rebuilt by `task ui:check`)

**Behaviour:**
- **The factory side.** When the repository is served from a MergePolicy, the report carries:
  - `mergePolicy`: the object's name;
  - `manifest`: YAML of a MergePolicy with the current central classes and merge block. Build it from `config.RepositoryFor(...).Classes`/`.Merge` the same way `resources.Migrate` builds one, and reuse its helper if it is exported. Otherwise export a small `resources.MergePolicyManifest(name, ns, repositoryRef string, classes, merge any) ([]byte, error)`.

  The served-from-a-MergePolicy test is that `Config.Generations.Repos[repo].MergePolicy > 0`.
- **The UI.** When `report.manifest` is present and authority is `central`:
  - the editor's file actions show "Copy MergePolicy manifest" and "Download" for `mergepolicy-<name>.yaml`, with no "edit on GitHub";
  - the notice (`settings.ts:248`) says the change is a GitOps pull request on the MergePolicy manifest, and that Flux applies it;
  - the editor's YAML is wrapped as the manifest: `spec.classes` holds what the editor edits.

  Otherwise, today's repository-file behaviour is unchanged.

- [ ] Go test first: the evidence report for a repository with `Generations` carries the manifest, and the manifest decodes with `resources.Decode`, so it is valid. Then the vitest: with `manifest` set, the file actions offer the manifest and no GitHub edit link; without it, they don't change.
- [ ] Implement. `npm` tests and the type check pass, then run `task ui:check` (`~/.cache/v3/node24.sh task ui:check`), which rebuilds and commits the bundle.
- [ ] `task check` passes. Commit `feat(settings): offer the MergePolicy manifest for a repository served from one`.

---

### Task 5: Documentation

- [ ] `docs/merge-policies.md`, the factory resources section: what `kubectl get factory,repository,mergepolicy` shows, with an example output; the conditions and their reasons in a table; that the last decision is in memory; and that counts arrive with #420.
- [ ] `charts/maku-factory/README.md`: the status is written by the leader.
- [ ] `task docs` passes. Commit `docs: the factory resources' status`.

## Self-review

- **Spec coverage:** D5 (each decision records its MergePolicy generation) → task 1. D10 → task 2 (MergePolicy fields) and task 3. D11 → task 2 (Repository, Factory). D13 → task 3 (leader-only). D9 → task 4.
- **Deliberately not here:** counts and spend (#420), and persisting the last decision.
- **Types:** `decision.Recorder`, `decision.Last`, `statuswriter.Inputs`, `Desired`, `Writer` are consistent across tasks.
