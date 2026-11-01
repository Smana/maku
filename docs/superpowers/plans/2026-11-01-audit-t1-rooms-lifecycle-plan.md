# Audit T1 Rooms Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the audit's theme T1: a sealed room hands out no bridge lease (H-1), a failed fork's log is closed even when the act's context is spent (R-1), an unchanged Room status is not patched at every resync (R-2), and room reads are rate-limited and folded incrementally (S-9).

**Architecture:** No design change. Each finding is one commit whose first step is the test that fails today. H-1 changes the store's lease claim, R-1 the fork's cleanup, R-2 one comparison in roomctrl, S-9 adds a per-principal read budget on the human API's `Server` and a bounded per-room cache of the summary fold.

**Tech Stack:** Go (`task check`: spdx, tidy, golangci-lint v2, govulncheck, `go test -race`), testcontainers Postgres for `internal/store`, controller-runtime's fake client with interceptors, `httptest`, `golang.org/x/time/rate`.

**Spec:** [`docs/superpowers/specs/2026-11-01-audit-fixes-design.md`](../specs/2026-11-01-audit-fixes-design.md), theme T1; findings in [`2026-11-01-project-audit.md`](../specs/2026-11-01-project-audit.md) (H-1, R-1, R-2, S-9).

## Global Constraints

- D0: **one PR per theme**, each finding one commit with the test that pins it first; `task check` exits 0 on the PR.
- D5: **a hello on a sealed room answers 410 `sealed`.** Revoking a closed room's live runs is out of scope.
- Branch: `fix/audit-t1-rooms-lifecycle`, from `main` once the audit-fixes spec has merged (until then, from `docs/audit-fixes-spec`).
- `AGENTS.md` holds for every change: `ctx` first, errors wrapped with `%w` and compared with `errors.Is`/`As`, `log/slog` injected, no package-level state beyond `version.Version`, no `time.Sleep` outside tests, SQL only in `internal/store` and parameterised, the broker role append-only, no payload text in a log line or an error.
- Tests: stdlib `testing` only, table-driven `t.Run`, hand-written fakes, `-race` always. testcontainers Postgres for `internal/store` only: **store tests need a running Docker daemon**.
- Every new `.go` file opens with `// SPDX-License-Identifier: Apache-2.0`.
- Commit messages: Conventional Commits in the repository's style (`fix(<scope>): <behaviour, lower case>`), English, **no `Co-Authored-By` line and no "Generated with" line**. PR title and body in English, no attribution line.

## Spec deviations

Found while reading the code at `e14b7949`. Each is planned as corrected below.

| # | Spec says | Evidence | Plan does |
|---|---|---|---|
| 1 (S-9) | The two GET routes pass `Actor.limited` | `Server.Actor` is optional: `createRoom` checks `s.Actor == nil` (`internal/humanapi/rooms.go:79`), and the test harness `setup` (`internal/humanapi/ws_test.go:280-330`) builds a `Server` with no `Actor`, so `s.Actor.limited` would nil-deref in every summary and room-list test. And the act bucket (`acts.go:152`) would then also pay for the room page's summary refetch (`web/src/room.ts:195`, up to once a second per open room tab) and the room list's 10 s refresh: a viewer with a few tabs open would get `rate_limited` on their next act | A **read budget on `Server`**, same numbers (10/s, burst 20, per principal, per replica), separate from acts. It reuses the shape `/mcp` already has (`mcpAllow`, `internal/humanapi/mcp.go:353`), with one shared `take` helper |
| 2 (S-9) | "`summaryOf` keeps a fold cache … and folds only events after it" | `summary.State.Add` writes in place to `Pending`, `Agents`, `Notes` and the `*RunModel` that `tell` returns (`internal/summary/summary.go:211-340`). A cached fold handed to two concurrent requests would race, and one request's events would end up in the other's fold. Also, the spec's own pinning test ("a second summary of an unchanged room reads no event page") needs more than "fold after the cached seq": that would still read one empty page | Adds `summary.State.Clone()` and the cache hands out clones. `summaryOf` already reads the row (`readRoom` returns `store.RoomState`, which it currently throws away): when the cached `LastSeq` is at or past the row's `LastSeq`, it reads no page |
| 3 (H-1) | Only `claimFree` checks `sealed` | `ClaimBridge`'s takeover from a dead holder is a separate compare-and-swap (`internal/store/bridges.go:48-49`). It runs after `live(holder)`, outside any lock, and never reads `sealed`. A room closed while `live` is being asked would still be taken over | The CAS also requires `NOT sealed`. A test seals the room from inside `live` to pin it |
| 4 (H-1) | "bridgeapi: hello on a sealed room answers 410" as a failing test | `hello` already routes any `ClaimBridge` error other than `ErrNoRoom` through `logFailure`, which maps `ErrSealed` to 410 `sealed` (`internal/bridgeapi/server.go:225,336`). The fault is in the store alone | The store test is the one that fails first. The bridgeapi test pins the 410 contract and **passes as soon as its fake models a seal**. That is expected |
| 5 (R-1) | Marked *plausible* in the audit | Reproduced by reading: the WebSocket act context is `context.WithTimeout(v.life, actWait)` (`ws.go:417`, 15 s), and `store.closeRoom` begins its transaction on the context it is given (`store.go:324`). A `Create` that ran out the act's time leaves `CloseRoom` failing at `Begin` | Planned as the spec says |

## Review Focus

1. A hello from the lease's own holder after a human closed the room: it must get 410, and `bridge_run` must keep that holder (the store test's second case). A sealed room's lease must never move.
2. Two summary requests for one room at the same time: each folds its own clone, and the cache keeps the fold that went further (`foldCache.put`). Neither may see the other's events.
3. A summary of a room whose fold is cached while the room is being forked or sealed: nothing below `LastSeq` ever changes (the log is append-only, and retention deletes whole rooms whose ids never come back). The cache may therefore skip events only at or below a seq it has folded.

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/store/bridges.go` | Modify | `claimFree` and the takeover CAS refuse a sealed room |
| `internal/store/bridges_test.go` | Modify | H-1 store test, helper `bridgeRun` |
| `internal/bridgeapi/server_test.go` | Modify | `memLog` models a seal on `ClaimBridge`; the 410 hello test |
| `docs/api.md` | Modify | hello's `410 sealed` row; the read budget's `429` on the two GET routes |
| `internal/humanapi/fork.go` | Modify | the fork's cleanup `CloseRoom` on `context.WithoutCancel`, bounded, logged |
| `internal/humanapi/acts_test.go` | Modify | `actLog.CloseRoom` honours a spent ctx and records the room, the deadline and an injected error |
| `internal/humanapi/fork_test.go` | Modify | R-1 test |
| `internal/roomctrl/reconciler.go` | Modify | `patchStatus` compares with `equality.Semantic.DeepEqual` |
| `internal/roomctrl/reconciler_test.go` | Modify | R-2 test |
| `internal/humanapi/server.go` | Modify | `limitMu`, `readLimits`, `folds` fields; `take`, `readAllowed` |
| `internal/humanapi/mcp.go` | Modify | `mcpAllow` goes through `take` |
| `internal/humanapi/rooms.go` | Modify | `listRooms` takes the read budget |
| `internal/humanapi/summary.go` | Modify | `roomSummary` takes the read budget; `summaryOf` folds through `s.folds` |
| `internal/humanapi/foldcache.go` | Create | `foldCache`: per-room fold, bounded at 256 rooms, least recently read evicted |
| `internal/humanapi/foldcache_test.go` | Create | cache unit tests |
| `internal/humanapi/summary_test.go` | Modify | read-budget test; incremental-fold test, `rangeLog` fake |
| `internal/summary/summary.go` | Modify | `State.Clone` |
| `internal/summary/summary_test.go` | Modify | clone test |

---

### Task 0: Branch

- [ ] **Step 1: Create the branch**

```bash
git switch -c fix/audit-t1-rooms-lifecycle main
```

(Until the spec PR has merged, use `docs/audit-fixes-spec` as the base instead of `main`.)

---

### Task 1 (H-1): A sealed room hands out no bridge lease

**Files:**
- Modify: `internal/store/bridges.go:30-84` (`ClaimBridge`, `claimFree`)
- Test: `internal/store/bridges_test.go`
- Modify: `internal/bridgeapi/server_test.go:68-87` (`memLog`), `:202-213` (`memLog.ClaimBridge`)
- Modify: `docs/api.md:72-80` (hello's status table)

**Interfaces:**
- Consumes: `store.ErrSealed` (`internal/store/store.go:26`), `wire.ReasonSealed`.
- Produces: `(*store.Store).ClaimBridge` returns an error wrapping `store.ErrSealed` for a sealed room and leaves `bridge_run` unchanged. Its signature is unchanged.

- [ ] **Step 1: Write the failing store test.** Append to `internal/store/bridges_test.go`:

```go
// bridgeRun is the room's lease holder as stored, "" for none.
func bridgeRun(t *testing.T, s *Store) string {
	t.Helper()
	var holder string
	if err := s.pool.QueryRow(context.Background(), `SELECT coalesce(bridge_run, '') FROM rooms WHERE room_id = $1`, room).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	return holder
}

// Audit H-1: a sealed room hands out no bridge lease, so its hello answers 410 and no harness is
// released into a room that records nothing. The lease, held or not, stays as the seal left it.
func TestASealedRoomHandsOutNoBridgeLease(t *testing.T) {
	seal := func(t *testing.T, s *Store) {
		t.Helper()
		if err := s.CloseRoom(context.Background(), room, "closed by human:own"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name       string
		holder     string // claims the lease before the seal; "" for none
		sealFirst  bool   // seal before the claim; else while the claim asks the holder's liveness
		run        string
		wantHolder string // bridge_run after the refused claim
	}{
		{"a room nobody holds", "", true, "aaaaaaaa", ""},
		{"the holder claims again", "7f3cq2xz", true, "7f3cq2xz", "7f3cq2xz"},
		{"another run, the holder ended", "7f3cq2xz", true, "aaaaaaaa", "7f3cq2xz"},
		{"sealed while the holder's liveness is asked", "7f3cq2xz", false, "aaaaaaaa", "7f3cq2xz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := open(t)
			ctx := context.Background()
			if tc.holder != "" {
				if _, ok, err := s.ClaimBridge(ctx, room, tc.holder, alive); err != nil || !ok {
					t.Fatalf("the holder claims: %v %v", ok, err)
				}
			}
			live := ended
			if tc.sealFirst {
				seal(t, s)
			} else {
				live = func(context.Context, string) bool { seal(t, s); return false }
			}
			if _, took, err := s.ClaimBridge(ctx, room, tc.run, live); took || !errors.Is(err, ErrSealed) {
				t.Fatalf("claim on a sealed room: took=%v err=%v, want ErrSealed", took, err)
			}
			if got := bridgeRun(t, s); got != tc.wantHolder {
				t.Fatalf("bridge_run = %q, want %q", got, tc.wantHolder)
			}
		})
	}
}
```

- [ ] **Step 2: Run it** (Docker must be running):

Run: `go test -race -count=1 ./internal/store/ -run TestASealedRoomHandsOutNoBridgeLease`
Expected: FAIL in all four cases with `claim on a sealed room: took=true err=<nil>, want ErrSealed`. `claimFree` never reads `sealed`, and the takeover CAS does not either.

- [ ] **Step 3: Implement.** In `internal/store/bridges.go`, replace `claimFree` and the CAS inside `ClaimBridge`:

```go
		tag, err := s.pool.Exec(ctx, `UPDATE rooms SET bridge_run = $3
			WHERE room_id = $1 AND bridge_run = $2 AND NOT sealed`, roomID, holder, runID)
```

```go
// claimFree takes the lease when nobody else holds it. Otherwise it returns the
// other holder, with took false and the row lock already released. A sealed room
// hands out no lease at all, its holder's included (audit H-1): ErrSealed.
func (s *Store) claimFree(ctx context.Context, roomID, runID string) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var holder *string
	var sealed bool
	err = tx.QueryRow(ctx, `SELECT bridge_run, sealed FROM rooms WHERE room_id = $1 FOR UPDATE`, roomID).Scan(&holder, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNoRoom
	}
	if err != nil {
		return "", false, err
	}
	if sealed {
		return "", false, ErrSealed
	}
	// SBB: freshness never frees a lease; only live(holder) == false does (ClaimBridge).
	if holder != nil && *holder != runID {
		return *holder, false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE rooms SET bridge_run = $2 WHERE room_id = $1`, roomID, runID); err != nil {
		return "", false, err
	}
	return runID, true, tx.Commit(ctx)
}
```

Add one sentence to `ClaimBridge`'s doc comment, after "…a compare-and-swap on the holder it asked about.":

```go
// A sealed room refuses every claim with ErrSealed, also one sealed while live runs:
// the swap requires the room unsealed, and the retry that follows reads the seal.
```

- [ ] **Step 4: Run the store's bridge tests**:

Run: `go test -race -count=1 ./internal/store/ -run 'Bridge|Lease|Sealed'`
Expected: PASS. Case 4 takes the CAS's 0 rows, retries, and `claimFree` returns `ErrSealed`.

- [ ] **Step 5: Pin the hello contract in bridgeapi.** In `internal/bridgeapi/server_test.go`, add a field to `memLog` (after `touchHeld bool`) and extend its doc comment:

```go
// memLog is the log in memory, with the store's idempotency, fencing and
// sealing semantics. refuse fails chosen appends; touchHeld makes TouchBridge
// report the lease held whoever holds it, as a lease lost between the touch and
// the append does; a room in sealed refuses its bridge lease, as the store does.
```

```go
	sealed    map[string]bool // rooms whose lease ClaimBridge refuses with ErrSealed
```

In `memLog.ClaimBridge`, after the `ErrNoRoom` check:

```go
	if m.sealed[roomID] {
		return "", false, fmt.Errorf("store: claim the bridge of room %s: %w", roomID, store.ErrSealed)
	}
```

Then add the test after `TestAnAbsentLeaseHolderIsTakenOverAfterSync`:

```go
// Audit H-1 (D5): a closed room's hello answers 410 sealed, so the bridge keeps its harness held
// rather than release it into a room that records nothing. No lease is taken, nothing is appended.
func TestHelloOnASealedRoomIsGone(t *testing.T) {
	s, log, w := newServer(t)
	w.Upsert(t.Context(), agentRun(runA, room, "Running"))
	log.mu.Lock()
	log.sealed = map[string]bool{room: true}
	log.mu.Unlock()
	rec := call(t, s.Routes(), http.MethodPost, "/v1/bridge/hello", "run:"+runA, nil)
	if rec.Code != http.StatusGone || reason(t, rec) != wire.ReasonSealed {
		t.Fatalf("hello on a sealed room: %d %s", rec.Code, rec.Body)
	}
	if h, ok := log.leases[room]; ok || len(log.stored()) != 0 {
		t.Fatalf("lease %q, %d events: a sealed room handed out its lease", h, len(log.stored()))
	}
}
```

Run: `go test -race -count=1 ./internal/bridgeapi/ -run 'TestHelloOnASealedRoomIsGone|Lease|Busy'`
Expected: PASS without touching `server.go`. `logFailure` already maps `ErrSealed` (Spec deviation 4); this test keeps that mapping from regressing.

- [ ] **Step 6: Document the status.** In `docs/api.md`, hello's table, insert after the `409 room_busy` row:

```markdown
| `410` | `sealed` | The room is closed: its log takes no more events. The bridge never releases its harness, and the pod fails |
```

- [ ] **Step 7: Commit**

```bash
git add internal/store/bridges.go internal/store/bridges_test.go internal/bridgeapi/server_test.go docs/api.md
git commit -m "fix(store): a sealed room hands out no bridge lease, so its hello answers 410"
```

---

### Task 2 (R-1): A failed fork is closed on its own context

**Files:**
- Modify: `internal/humanapi/fork.go:104-118` (the `Rooms.Create` failure path), imports
- Modify: `internal/humanapi/acts_test.go:40-63` (`actLog` fields), `:212-217` (`actLog.CloseRoom`)
- Test: `internal/humanapi/fork_test.go`

**Interfaces:**
- Consumes: `actLog.forkedRoom` (the `store.NewRoom` the last `Fork` was asked for), `forkFixture`, `interceptor.Funcs`.
- Produces: `forkCleanupWait = 10 * time.Second` (unexported const in `fork.go`); new `actLog` fields `closedRoom string`, `closeBounded bool`, `closeErr error`.

- [ ] **Step 1: Make the fake log behave like the store on a spent context.** In `internal/humanapi/acts_test.go`, add to `actLog` after `closed  string`:

```go
	// closedRoom is the room the last CloseRoom sealed, closeBounded whether its ctx had a
	// deadline; closeErr fails every CloseRoom.
	closedRoom   string
	closeBounded bool
	closeErr     error
```

Replace `actLog.CloseRoom`:

```go
// CloseRoom is the store's: a spent ctx fails it before anything is sealed, as pool.Begin does.
func (l *actLog) CloseRoom(ctx context.Context, roomID, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closeErr != nil {
		return l.closeErr
	}
	_, l.closeBounded = ctx.Deadline()
	l.closed, l.closedRoom = reason, roomID
	return nil
}
```

- [ ] **Step 2: Write the failing test.** Append to `internal/humanapi/fork_test.go`:

```go
// Audit R-1: a Room Create that outlives the act's context still leaves the fork's log closed:
// the close runs on its own bounded context. A close that fails is logged with the room's id,
// since nothing else would ever seal that log.
func TestAForkWhoseCreateOutlivesTheActIsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		closeErr  error
		wantClose bool
		wantLog   string
	}{
		{"the act's context is spent", nil, true, ""},
		{"the close fails", errors.New("conn reset"), false, "level=ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			slow := &interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				cancel() // the act's deadline passes while the API server answers
				return context.Canceled
			}}
			a, log, room, _ := forkFixture(t, slow)
			log.closeErr = tc.closeErr
			var buf bytes.Buffer
			a.Logger = slog.New(slog.NewTextHandler(&buf, nil))
			raw, _ := json.Marshal(Action{Kind: "fork", Seq: 1})
			p := authn.Principal{Kind: envelope.ActorHuman, ID: "human:bob", Groups: []string{"agents-member"}}
			if f := a.Handle(ctx, p, false, "s1", room, wire.ClientFrame{Type: "act", ClientSeq: 1, Action: raw}); f.Rejected != "log_unavailable" {
				t.Fatalf("%+v", f)
			}
			forked := log.forkedRoom.ID
			if closed := log.closedRoom == forked && log.closeBounded; closed != tc.wantClose {
				t.Fatalf("closed %q (bounded %v), fork %q; want closed %v", log.closedRoom, log.closeBounded, forked, tc.wantClose)
			}
			if l := buf.String(); tc.wantLog != "" && (!strings.Contains(l, tc.wantLog) || !strings.Contains(l, "room="+forked)) {
				t.Fatalf("logged %q", l)
			}
		})
	}
}
```

- [ ] **Step 3: Run it**:

Run: `go test -race -count=1 ./internal/humanapi/ -run 'TestAForkWhoseCreateOutlivesTheActIsClosed|TestAForkWhoseRoomFailsIsClosed'`
Expected: `TestAForkWhoseCreateOutlivesTheActIsClosed` FAILs in both cases. Case 1: `closed "" (bounded false)`, because `fork` passes the spent `ctx` to `CloseRoom`. Case 2: no `level=ERROR` line, because the error is dropped with `_ =`. `TestAForkWhoseRoomFailsIsClosed` still PASSes.

- [ ] **Step 4: Implement.** In `internal/humanapi/fork.go`, add `"time"` to the imports, and after `const maxNote = 1 << 10`:

```go
// forkCleanupWait bounds closing the log of a fork whose Room was refused. The act's own
// context may be what failed the Create (a timeout), and the close must still land.
const forkCleanupWait = 10 * time.Second
```

Replace the cleanup line `_ = a.Log.CloseRoom(ctx, id, "fork failed: no Room")` and the comment above it with:

```go
		// A row no Room projects is never closed, so retention would never purge it. The act's
		// context may be spent (audit R-1): the close runs on its own, bounded one.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forkCleanupWait)
		cerr := a.Log.CloseRoom(cctx, id, "fork failed: no Room")
		cancel()
		if cerr != nil && a.Logger != nil {
			a.Logger.ErrorContext(ctx, "forked room's log not closed: retention will not purge it", "room", id)
		}
```

- [ ] **Step 5: Run the package's fork and act tests**:

Run: `go test -race -count=1 ./internal/humanapi/ -run 'Fork|Close|Invite'`
Expected: PASS. The close act (`acts.go:420`) passes a live ctx, so the stricter fake does not change its tests.

- [ ] **Step 6: Commit**

```bash
git add internal/humanapi/fork.go internal/humanapi/fork_test.go internal/humanapi/acts_test.go
git commit -m "fix(rooms): a fork whose Room is refused is closed on its own context, not the spent act's"
```

---

### Task 3 (R-2): An unchanged Room status is not patched

**Files:**
- Modify: `internal/roomctrl/reconciler.go:220-232` (`patchStatus`), imports
- Test: `internal/roomctrl/reconciler_test.go`

**Interfaces:**
- Consumes: `newStore`, `seqPayload`, `taskEvent`, `newRoom`, `scheme`, `mapper`, `request`, `clock`, `deletedAt` (all in `reconciler_test.go`).
- Produces: nothing new. `patchStatus` keeps its signature.

- [ ] **Step 1: Write the failing test.** Append to `internal/roomctrl/reconciler_test.go`:

```go
// Audit R-2: a reconcile whose status, the task's included, is what the Room already holds sends
// no patch. Task is a pointer rebuilt from the log at each reconcile, so == never held for a room
// with a task, and every such room was patched at every 15 s resync.
func TestAnUnchangedStatusIsNotPatched(t *testing.T) {
	facts := envelope.TaskFacts{Phase: "AwaitingHuman",
		Issue: &envelope.IssueFact{Number: 1, URL: "https://github.com/o/r/issues/1", Title: "fix it", Author: "dev1", LabelledBy: "dev1"},
		PR:    &envelope.PRFact{Number: 1, URL: "https://github.com/o/r/pull/1", Author: "bot", Reviewers: []string{"dev2"}}}
	for _, tc := range []struct {
		name   string
		events []seqPayload
	}{
		{"a room with no task", nil},
		{"a room with a task", []seqPayload{taskEvent(2, facts)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patches := 0
			c := fake.NewClientBuilder().WithScheme(scheme()).WithRESTMapper(mapper()).
				WithObjects(newRoom()).WithStatusSubresource(&v1alpha1.Room{}).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, cl client.Client, sub string,
					obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					patches++
					return cl.SubResource(sub).Patch(ctx, obj, p, opts...)
				}}).Build()
			ms := newStore(nil)
			ms.taskEvents = tc.events
			r := &Reconciler{Client: c, Store: ms, Runs: runwatch.New(), Now: clock(deletedAt)}
			for i := range 2 {
				if _, err := r.Reconcile(t.Context(), request()); err != nil {
					t.Fatalf("reconcile %d: %v", i+1, err)
				}
			}
			if patches != 1 {
				t.Fatalf("%d status patches over two reconciles of an unchanged room, want 1", patches)
			}
		})
	}
}
```

- [ ] **Step 2: Run it**:

Run: `go test -race -count=1 ./internal/roomctrl/ -run TestAnUnchangedStatusIsNotPatched`
Expected: `a room with no task` PASSes (it is the control). `a room with a task` FAILs with `2 status patches over two reconciles of an unchanged room, want 1`.

- [ ] **Step 3: Implement.** In `internal/roomctrl/reconciler.go`, add the import `"k8s.io/apimachinery/pkg/api/equality"` (beside `apierrors`), and replace the comparison in `patchStatus`:

```go
// patchStatus writes want when it differs. A merge patch of the status
// subresource cannot conflict with a spec or finalizer write. Semantic, not ==:
// Task is a pointer that taskStatus rebuilds from the log each time (audit R-2).
func (r *Reconciler) patchStatus(ctx context.Context, room *v1alpha1.Room, want v1alpha1.RoomStatus) error {
	if equality.Semantic.DeepEqual(room.Status, want) {
		return nil
	}
```

The rest of the function is unchanged.

- [ ] **Step 4: Run the package**:

Run: `go test -race -count=1 ./internal/roomctrl/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/roomctrl/reconciler.go internal/roomctrl/reconciler_test.go
git commit -m "fix(roomctrl): an unchanged room status, its task included, is not patched at every resync"
```

---

### Task 4 (S-9, limit): Room reads spend a per-principal read budget

**Files:**
- Modify: `internal/humanapi/server.go:172-174` (fields), add `take` and `readAllowed` after `rejected` (`:372-376`)
- Modify: `internal/humanapi/mcp.go:353-366` (`mcpAllow`), imports (drop `golang.org/x/time/rate`)
- Modify: `internal/humanapi/rooms.go:422-431` (`listRooms`)
- Modify: `internal/humanapi/summary.go:19-23` (`roomSummary`)
- Test: `internal/humanapi/summary_test.go`
- Modify: `docs/api.md:348-349` (route table) and `:362-367` (summary status table)

**Interfaces:**
- Consumes: `Server.rejected(ctx, reason)`, `rejectRateLimited` (`acts.go`), the test helpers `setup`, `abc`, `get`, `admin` (`ws_test.go`, `access_test.go`).
- Produces: `func (s *Server) take(limits *map[string]*rate.Limiter, principal string) bool`; `func (s *Server) readAllowed(w http.ResponseWriter, r *http.Request, p authn.Principal) bool`; fields `limitMu sync.Mutex` (replaces `mcpMu`) and `readLimits map[string]*rate.Limiter`.

- [ ] **Step 1: Write the failing test.** Append to `internal/humanapi/summary_test.go`:

```go
// Audit S-9: the room list and a summary spend a per-principal read budget, 10 a second, burst 20,
// per replica. It is apart from acts, so a page that refreshes never refuses its viewer's next act.
func TestRoomReadsAreRateLimited(t *testing.T) {
	for _, path := range []string{"/api/rooms", "/api/rooms/" + roomID + "/summary"} {
		t.Run(path, func(t *testing.T) {
			e := setup(t, abc(t))
			for i := range 20 {
				if code, body := get(t, e, path, admin("boss")); code != http.StatusOK {
					t.Fatalf("read %d of the burst: %d %s", i+1, code, body)
				}
			}
			limited := false
			for range 20 {
				code, body := get(t, e, path, admin("boss"))
				if code == http.StatusTooManyRequests {
					if body != "rate_limited\n" {
						t.Fatalf("429 body %q", body)
					}
					limited = true
					break
				}
			}
			if !limited {
				t.Fatal("40 reads at once were all served")
			}
			if code, body := get(t, e, path, admin("other")); code != http.StatusOK {
				t.Fatalf("the budget is per principal: %d %s", code, body)
			}
		})
	}
}
```

- [ ] **Step 2: Run it**:

Run: `go test -race -count=1 ./internal/humanapi/ -run TestRoomReadsAreRateLimited`
Expected: FAIL on both subtests with `40 reads at once were all served`.

- [ ] **Step 3: Implement the budget.** In `internal/humanapi/server.go`, replace the two fields

```go
	mcpMu     sync.Mutex
	mcpLimits map[string]*rate.Limiter // /mcp requests per principal, per replica
```

with

```go
	limitMu    sync.Mutex
	mcpLimits  map[string]*rate.Limiter // /mcp requests per principal, per replica
	readLimits map[string]*rate.Limiter // room list and summary reads per principal, per replica
```

and add after `rejected`:

```go
// readAllowed takes one of p's room reads, GET /api/rooms or a summary: 10 a second, burst 20,
// per replica (audit S-9). The budget is apart from acts, so a page that refreshes never refuses its
// viewer's next act. A refusal answers 429 rate_limited and is counted.
func (s *Server) readAllowed(w http.ResponseWriter, r *http.Request, p authn.Principal) bool {
	if s.take(&s.readLimits, p.ID) {
		return true
	}
	s.rejected(r.Context(), rejectRateLimited)
	http.Error(w, rejectRateLimited, http.StatusTooManyRequests)
	return false
}

// take takes one request from principal's limiter in limits, made at 10 a second, burst 20.
func (s *Server) take(limits *map[string]*rate.Limiter, principal string) bool {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	if *limits == nil {
		*limits = map[string]*rate.Limiter{}
	}
	l, ok := (*limits)[principal]
	if !ok {
		l = rate.NewLimiter(10, 20)
		(*limits)[principal] = l
	}
	return l.Allow()
}
```

In `internal/humanapi/mcp.go`, replace `mcpAllow`'s body and remove the now-unused `"golang.org/x/time/rate"` import:

```go
// mcpAllow takes one of principal's /mcp requests: 10 a second, burst 20, per replica, as acts.
func (s *Server) mcpAllow(principal string) bool { return s.take(&s.mcpLimits, principal) }
```

In `internal/humanapi/rooms.go` `listRooms`, and in `internal/humanapi/summary.go` `roomSummary`, right after the `principal` check:

```go
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowed(w, r, p) {
		return
	}
```

The MCP tools that read rooms (`rooms_list`, `room_summary`, `tasks/get`) already spend `/mcp`'s own budget, so they do not call `readAllowed`.

- [ ] **Step 4: Run the package**:

Run: `go test -race -count=1 ./internal/humanapi/`
Expected: PASS. If an existing test now gets a 429, it reads more than 20 times at once as one principal. Spread its reads over a second principal (`admin("boss2")`). Do not loosen the budget.

- [ ] **Step 5: Document it.** In `docs/api.md`, in the route table, end the `GET /api/rooms` row's last cell with this sentence (replace `; \`?needs_me=1\`, rows with \`needsMe\` |`):

```markdown
; `?needs_me=1`, rows with `needsMe`. `429 rate_limited` past the caller's read budget: 10/s, burst 20, per replica, shared with the summary and apart from acts |
```

and in the summary's status table insert after the `404` row:

```markdown
| `429` | `rate_limited`: over the caller's read budget (10/s, burst 20, per replica), shared with `GET /api/rooms` |
```

Run: `hack/docs.sh`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/humanapi/server.go internal/humanapi/mcp.go internal/humanapi/rooms.go internal/humanapi/summary.go internal/humanapi/summary_test.go docs/api.md
git commit -m "fix(rooms): the room list and the summary spend a per-principal read budget"
```

---

### Task 5 (S-9, fold): The summary folds only what the log appended

**Files:**
- Modify: `internal/summary/summary.go:59-72` (add `Clone` after `State`)
- Test: `internal/summary/summary_test.go`
- Create: `internal/humanapi/foldcache.go`, `internal/humanapi/foldcache_test.go`
- Modify: `internal/humanapi/server.go:172-176` (field `folds`)
- Modify: `internal/humanapi/summary.go:42-62` (`summaryOf`)
- Test: `internal/humanapi/summary_test.go`

**Interfaces:**
- Consumes: `summary.Fold`, `(*summary.State).Add`, `readRoom` (returns `(*v1alpha1.Room, store.RoomState, policy.Subject, error)`), `pageSize` (`ws.go:32`, 500), humanapi test helpers `setup`, `abc`, `factsLog`, `progress`, `logEvent`, `summaryOf` (test helper), `admin`, `hubView` (embeds `*memLog`).
- Produces: `func (st State) Clone() State` in `internal/summary`; in `internal/humanapi`, `type foldCache` with `get(room string) (summary.State, bool)` and `put(room string, st summary.State)`, `const foldCacheRooms = 256`, the `Server` field `folds foldCache`.

- [ ] **Step 1: Write the failing clone test.** Append to `internal/summary/summary_test.go`:

```go
// A clone folds on apart from its original: Add writes the approvals, the agents, the notes and the
// run models in place, and the broker's fold cache hands one clone to each reader (audit S-9).
func TestACloneFoldsApart(t *testing.T) {
	sys := envelope.Actor{Kind: envelope.ActorSystem, ID: "system:room-broker"}
	running := ev(2, envelope.StateChanged, sys, map[string]any{"kind": "run_phase", "phase": "Running", "model": "agent-default"})
	running.Origin = envelope.OriginBroker
	prefix := []envelope.Event{
		approvalRequested(1, "01A", "push", time.Time{}),
		running,
		ev(3, envelope.Participant, sys, envelope.ParticipantPayload{Principal: "agent:cf4ato2x", Change: "joined", Role: "implementer"}),
		note(4, "n4"),
	}
	base, want := summary.Fold(prefix), summary.Fold(prefix)
	effort := ev(8, envelope.StateChanged, envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:cf4ato2x"},
		map[string]any{"kind": "harness_status", "status": "running", "previous": "", "effort": "high"})
	effort.Origin = envelope.OriginHarness
	c := base.Clone()
	c.Add([]envelope.Event{
		ev(5, envelope.ApprovalDecided, sys, envelope.ApprovalDecidedPayload{ApprovalID: "01A", Decision: "approved"}),
		ev(6, envelope.Participant, sys, envelope.ParticipantPayload{Principal: "agent:cf4ato2x", Change: "left", Role: "implementer"}),
		note(7, "n7"),
		effort,
	})
	if !reflect.DeepEqual(base, want) {
		t.Fatalf("folding the clone changed the original:\ngot  %+v\nwant %+v", base, want)
	}
	if len(c.Pending) != 0 || len(c.Agents) != 0 || len(c.Notes) != 2 || c.LLM.Effort != "high" {
		t.Fatalf("the clone did not fold on: %+v", c)
	}
}
```

- [ ] **Step 2: Run it**:

Run: `go test -race -count=1 ./internal/summary/ -run TestACloneFoldsApart`
Expected: FAIL to compile, `base.Clone undefined (type summary.State has no field or method Clone)`.

- [ ] **Step 3: Implement `Clone`.** In `internal/summary/summary.go`, after the `State` struct (`maps` and `slices` are already imported):

```go
// Clone is a copy of st that folds on apart from it. Add writes Pending, Agents, Notes and the
// run models in place, so a fold kept for later readers hands each a clone. Facts, Verdict and
// Judge are replaced by Add, never written through, so the clone shares them.
func (st State) Clone() State {
	c := st
	c.Pending = maps.Clone(st.Pending)
	c.Agents = maps.Clone(st.Agents)
	c.Notes = slices.Clone(st.Notes)
	if st.LLM != nil {
		m := *st.LLM
		c.LLM = &m
	}
	if st.PrevLLM != nil {
		m := *st.PrevLLM
		c.PrevLLM = &m
	}
	return c
}
```

Run: `go test -race -count=1 ./internal/summary/`
Expected: PASS (the goldens and `TestAddingPagesIsFoldingTheLog` unchanged).

- [ ] **Step 4: Write the failing cache tests.** Create `internal/humanapi/foldcache_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

func TestFoldCache(t *testing.T) {
	t.Run("past its bound it evicts the room read longest ago", func(t *testing.T) {
		c := &foldCache{max: 2}
		c.put("aaaaaaaa", summary.State{LastSeq: 1})
		c.put("bbbbbbbb", summary.State{LastSeq: 2})
		if _, ok := c.get("aaaaaaaa"); !ok {
			t.Fatal("a room within the bound was evicted")
		}
		c.put("cccccccc", summary.State{LastSeq: 3}) // bbbbbbbb is now the one read longest ago
		for id, want := range map[string]bool{"aaaaaaaa": true, "bbbbbbbb": false, "cccccccc": true} {
			if _, ok := c.get(id); ok != want {
				t.Errorf("%s kept = %v, want %v", id, ok, want)
			}
		}
	})
	t.Run("a fold that went less far does not replace the kept one", func(t *testing.T) {
		c := &foldCache{}
		c.put(roomID, summary.State{LastSeq: 9})
		c.put(roomID, summary.State{LastSeq: 5}) // a slower request, racing the first
		if st, _ := c.get(roomID); st.LastSeq != 9 {
			t.Fatalf("lastSeq %d, want 9", st.LastSeq)
		}
	})
	t.Run("a reader's fold is its own", func(t *testing.T) {
		c := &foldCache{}
		c.put(roomID, summary.Fold([]envelope.Event{logEvent(1, envelope.ApprovalRequested,
			envelope.Actor{Kind: envelope.ActorSystem, ID: "system:policy"},
			envelope.ApprovalRequestedPayload{ApprovalID: "01M4A", CallID: "c1", Class: "forge.pr",
				Action: envelope.Must(map[string]string{"command": "push"})})}))
		mine, _ := c.get(roomID)
		delete(mine.Pending, "01M4A")
		if again, _ := c.get(roomID); len(again.Pending) != 1 {
			t.Fatalf("a reader's change reached the cache: %+v", again.Pending)
		}
	})
}
```

Run: `go test -race -count=1 ./internal/humanapi/ -run TestFoldCache`
Expected: FAIL to compile, `undefined: foldCache`.

- [ ] **Step 5: Implement the cache.** Create `internal/humanapi/foldcache.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"math"
	"sync"

	"github.com/Smana/maku/internal/summary"
)

// foldCacheRooms bounds the fold cache: the rooms whose summaries were read last.
const foldCacheRooms = 256

// foldCache keeps each recently read room's summary fold, so a summary folds only what the log
// appended since (audit S-9). The log is append-only and retention deletes whole rooms, whose ids
// never come back, so a fold through seq N stays the fold of events 1..N. Readers get clones:
// Add writes in place. The zero value is ready; max 0 means foldCacheRooms.
type foldCache struct {
	mu    sync.Mutex
	max   int
	tick  uint64
	rooms map[string]*foldEntry
}

type foldEntry struct {
	st   summary.State
	used uint64 // the tick of its last get or put: the smallest is evicted first
}

// get is a clone of room's fold, and whether one is kept.
func (c *foldCache) get(room string) (summary.State, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.rooms[room]
	if !ok {
		return summary.State{}, false
	}
	c.tick++
	e.used = c.tick
	return e.st.Clone(), true
}

// put keeps a clone of st as room's fold unless the kept one has folded further, then evicts the
// room read longest ago past the bound.
func (c *foldCache) put(room string, st summary.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rooms == nil {
		c.rooms = map[string]*foldEntry{}
	}
	c.tick++
	if e, ok := c.rooms[room]; ok {
		if st.LastSeq >= e.st.LastSeq {
			e.st = st.Clone()
		}
		e.used = c.tick
		return
	}
	c.rooms[room] = &foldEntry{st: st.Clone(), used: c.tick}
	limit := c.max
	if limit <= 0 {
		limit = foldCacheRooms
	}
	if len(c.rooms) > limit {
		oldest, at := "", uint64(math.MaxUint64)
		for id, e := range c.rooms {
			if e.used < at {
				oldest, at = id, e.used
			}
		}
		delete(c.rooms, oldest)
	}
}
```

Run: `go test -race -count=1 ./internal/humanapi/ -run TestFoldCache`
Expected: PASS.

- [ ] **Step 6: Write the failing summary test.** In `internal/humanapi/summary_test.go`, add `"sync"` to the imports and append:

```go
// rangeLog is the Server's log, recording the after of each event page it reads. The hub reads
// memLog directly, so its polls are not counted.
type rangeLog struct {
	*memLog
	readMu sync.Mutex
	afters []int64
}

func (l *rangeLog) Range(ctx context.Context, room string, after int64, limit int) ([]envelope.Event, error) {
	l.readMu.Lock()
	l.afters = append(l.afters, after)
	l.readMu.Unlock()
	return l.memLog.Range(ctx, room, after, limit)
}

func (l *rangeLog) reads() []int64 {
	l.readMu.Lock()
	defer l.readMu.Unlock()
	return slices.Clone(l.afters)
}

// Audit S-9: a summary folds only what the log appended since the room's last fold, and the
// summary of an unchanged room reads no event page at all.
func TestSummaryFoldsOnlyNewEvents(t *testing.T) {
	var rl *rangeLog
	e := setup(t, abc(t), func(s *Server, _ *fanout.Hub, v *hubView) {
		rl = &rangeLog{memLog: v.memLog}
		s.Log = rl
	})
	factsLog(e.log)
	for _, step := range []struct {
		name      string
		appended  []envelope.Event
		wantReads []int64
		wantNotes int
	}{
		{"the first summary folds the whole log", nil, []int64{0}, 2},
		{"an unchanged room reads no page", nil, []int64{0}, 2},
		{"one append is read from the cached seq", []envelope.Event{progress(5, "merged")}, []int64{0, 4}, 3},
	} {
		e.log.mu.Lock()
		e.log.evs = append(e.log.evs, step.appended...)
		e.log.mu.Unlock()
		s := summaryOf(t, e, roomID, "", admin("boss"))
		if got := rl.reads(); !slices.Equal(got, step.wantReads) || len(s.Notes.Items) != step.wantNotes {
			t.Fatalf("%s: pages read after %v, %d notes; want %v, %d", step.name, got, len(s.Notes.Items), step.wantReads, step.wantNotes)
		}
	}
}
```

Run: `go test -race -count=1 ./internal/humanapi/ -run TestSummaryFoldsOnlyNewEvents`
Expected: FAIL at step 2: `an unchanged room reads no page: pages read after [0 0], 2 notes; want [0], 2`. The summary refolds from seq 0 every time.

- [ ] **Step 7: Fold through the cache.** In `internal/humanapi/server.go`, add after `readLimits`:

```go
	folds foldCache // each recently read room's summary fold (audit S-9)
```

In `internal/humanapi/summary.go`, replace the head of `summaryOf` (from its doc comment through the end of the paging loop) with:

```go
// summaryOf is the room's summary/v1 as p sees it, notes after after: readRoom's refusals, or
// errLogUnreadable. The fold goes on from the room's cached one (audit S-9): the log is
// append-only, so only the events past the cached seq are read, and none when the row's last seq
// is not past it.
func (s *Server) summaryOf(ctx context.Context, id string, p authn.Principal, after int64) (summary.Summary, error) {
	room, row, sub, err := s.readRoom(ctx, id, p)
	if err != nil {
		return summary.Summary{}, err
	}
	fold, _ := s.folds.get(id)
	if fold.LastSeq < row.LastSeq {
		// Each page is folded, then dropped: the read holds one page, whatever the log's length.
		for last := fold.LastSeq; ; {
			page, err := s.Log.Range(ctx, id, last, pageSize)
			if err != nil {
				s.log().Warn("summary: log unreadable", "room", id, "err", err)
				return summary.Summary{}, errLogUnreadable
			}
			fold.Add(page)
			if len(page) < pageSize {
				break
			}
			last = page[len(page)-1].Seq
		}
		s.folds.put(id, fold)
	}
```

Everything from `view := summary.View(fold, id, …)` on is unchanged. A failed read returns before `put`, so the cache never holds a partial fold.

- [ ] **Step 8: Run the summary and room tests, then the package**:

Run: `go test -race -count=1 ./internal/humanapi/ -run 'Summary|FoldCache|Room|MCP|Task|State'`
Expected: PASS.

Run: `go test -race -count=1 ./internal/humanapi/ ./internal/summary/`
Expected: PASS. If a test fails because it **rewrites** events already summarised on the same `Server` (replacing `e.log.evs` with a different log of equal or shorter length, then reading the summary again), the test breaks the log's append-only contract. Give the second log its own `setup`. Do not add a cache bypass.

- [ ] **Step 9: Commit**

```bash
git add internal/summary/summary.go internal/summary/summary_test.go internal/humanapi/foldcache.go internal/humanapi/foldcache_test.go internal/humanapi/server.go internal/humanapi/summary.go internal/humanapi/summary_test.go
git commit -m "fix(rooms): the summary folds only what the log appended since the room's last fold"
```

---

### Task 6: Gate and pull request

**Files:** none changed.

- [ ] **Step 1: Run the gate** (Docker running, for the store tests):

Run: `task check`
Expected: exit 0. Every step passes: spdx (the new `foldcache.go` and `foldcache_test.go` carry the header), tidy, lint, vuln, test, crd:check, chart:check, migrations, docs, scripts, ui:test, ui:check. On a lint failure, fix the code; never `--disable` a linter.

- [ ] **Step 2: Check the commits**

Run: `git log --oneline main..HEAD`
Expected: five `fix(...)` commits, one per finding (S-9 counts twice: limit and fold), none with a `Co-Authored-By` or "Generated with" line.

- [ ] **Step 3: Push and open the PR**

```bash
git push -u origin fix/audit-t1-rooms-lifecycle
gh pr create --base main --title "fix(rooms): the audit's rooms lifecycle findings (T1)" --body "$(cat <<'EOF'
Theme T1 of docs/superpowers/specs/2026-11-01-audit-fixes-design.md, one commit per finding, each pinned by a test that failed first.

- H-1: a sealed room hands out no bridge lease (the claim and the takeover both check the seal), so hello answers 410 `sealed` (D5). Revoking a closed room's live runs stays out of scope.
- R-1: a fork whose Room Create fails closes its log on its own 10 s context, not the spent act's, and logs a failed close with the room id.
- R-2: roomctrl compares status with `equality.Semantic.DeepEqual`: a room with a task is no longer patched at every resync.
- S-9: `GET /api/rooms` and the summary spend a per-principal read budget (10/s, burst 20, apart from acts). The summary folds only events past a per-room cached fold (LRU of 256 rooms).

Deviations from the spec, with evidence: see "Spec deviations" in docs/superpowers/plans/2026-11-01-audit-t1-rooms-lifecycle-plan.md.

`task check` exits 0, store tests included.
EOF
)"
```

---

## Self-review

| Spec item | Task |
|---|---|
| H-1: `claimFree` selects `bridge_run, sealed … FOR UPDATE`, returns `ErrSealed`; store test leaves `bridge_run` unchanged; bridgeapi hello 410 `sealed` | Task 1 (plus the CAS guard, deviation 3) |
| D5: 410 on hello; no revoke | Task 1; revoke excluded in Global Constraints and the PR body |
| R-1: `CloseRoom` on `context.WithoutCancel(ctx)` bounded by 10 s, failure logged with the room id only; test with a spent act context, fake records the forked id | Task 2 |
| R-2: `equality.Semantic.DeepEqual`; a reconcile with an unchanged status, `Task` included, sends no patch | Task 3 |
| S-9: per-principal limit on both GET routes, n+1th request 429 | Task 4 (a Server read budget, deviation 1) |
| S-9: fold cache keyed by room, State + LastSeq, folds only after it, LRU of 256; second summary reads no page, after one append one page from the cached seq | Task 5 (plus `State.Clone`, deviation 2) |
| D0: one PR, one commit per finding, `task check` exits 0 | Tasks 1-5 commits, Task 6 |
