# Audit T5: Shutdown and Ownership Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the audit's F-7, F-8 and F-10: no factory listener returns before its drain, no hung-up RunLore caller orphans a Task, and the Slack App runs events on a bounded, owned pool that shutdown waits for.

**Architecture:** The run-request API and the RunLore intake each get a package-local `serve(ctx, srv, ln, drain)` in the broker's `serveHTTP` shape (`internal/app/broker.go:429`): `Serve` in a goroutine, `Shutdown` on a `WithoutCancel` context, and a return only after `Serve` has. RunLore files the issue and patches the Task on `context.WithoutCancel(r.Context())` bounded by 30 s, as `api/internal.go:171` already does. `slackapp.App` replaces `go f()` with 8 workers over a 64-slot queue; a full or draining queue answers 503 and forgets the event id so Slack's retry is handled; `App.Wait` joins the workers and the Slack App's drain calls it.

**Tech Stack:** Go 1.27.3, `net/http`, controller-runtime v0.25.2 fake client and `interceptor`, stdlib `testing` only.

**Spec:** `docs/superpowers/specs/2026-11-01-audit-fixes-design.md` (theme T5, D0); findings in `docs/superpowers/specs/2026-11-01-project-audit.md` (F-7, F-8, F-10).

**Branch:** `fix/audit-t5-shutdown`, one PR. T6's Task 3 builds on this plan's Task 1 (`Server.listen`, `serve`): merge T5 first, or rebase T6 onto it before T6's Task 3.

## Spec deviations

| # | Spec says | Code shows | This plan |
|---|---|---|---|
| 1 | F-7 names `api.Server.Start` only | `intake.RunLore.Start` (`internal/factory/intake/runlore.go:86-101`) has the same body: a goroutine calls `Shutdown`, and `Start` returns on `ErrServerClosed` while handlers still run. F-8's fix is moot on SIGTERM without it: the process exits mid-filing | Task 2 fixes it the same way, in its own commit |
| 2 | F-7: no drain bound given | Both `Start`s drain 5 s; a handler past `Create(Task)` files an issue under a 30 s bound (`api/internal.go:171`, F-8), so 5 s still cuts it | Both drains become 15 s: under the manager's 20 s `GracefulShutdownTimeout` (`managerDrain`, `internal/app/broker.go:66`, used by the factory's manager) and the chart's 30 s `terminationGracePeriodSeconds` |
| 3 | F-10: "65 queued events, the 65th gets 503" | With 8 workers each holding one event, 8 + 64 = 72 are accepted | The test holds all 8 workers, fills the 64 slots, and the 73rd answers 503 |
| 4 | F-10: "a full queue answers 503 so Slack retries" | `Events` adds the `event_id` to the dedup set (`a.events.add`) before handing the event off; a retry of a refused event would read `duplicate` and be dropped | A refused event's id is forgotten (`a.events.forget`) before the 503 |
| 5 | F-10: "the app's drain calls it" | `startSlackApp` builds the `App` inside the function; no test can reach it without a live Slack | The wiring change (`internal/app/slackapp.go`) is covered by the existing `TestStartSlackApp` (Wait still returns nil) and by review; the behaviour is pinned in `internal/slackapp` |

## Global Constraints

- D0: one PR for the theme; each finding one commit, its pinning test written and seen failing first; `task check` exits 0 on the PR.
- stdlib `testing` only, hand-written fakes, `httpx.New` (or an `httptest` server's client) for any HTTP client in tests; `-race` always.
- Every `http.Server` sets `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` and `MaxHeaderBytes`.
- Bounded concurrency: a fixed worker count, every goroutine owned and waited for, every buffer capped. `time.Sleep` is banned outside tests (`forbidigo`).
- `ctx context.Context` first; errors wrap with `%w`; `context.Background()` only in `main` and shutdown drains (tests excepted).
- New `.go` files start with `// SPDX-License-Identifier: Apache-2.0`.
- Logs carry ids and codes, never a finding's text or a payload.
- Commit messages: Conventional Commits in English, the repo's style (`fix(factory): …`, `fix(slackapp): …`), no `Co-Authored-By` and no "Generated with" line.

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/factory/api/server.go` | Modify `Start` (:202-216) | `Start` = `listen` + `serve`; `httpServer` carries the bounds; `apiDrain` |
| `internal/factory/api/serve.go` | Create | `serve`: the drain that returns only after `Serve` |
| `internal/factory/api/start_test.go` | Create | F-7 pin and the bounds check |
| `internal/factory/intake/runlore.go` | Modify `Start` (:86-101), `intake` (:231-241) | Same drain; filing outlives the caller |
| `internal/factory/intake/serve.go` | Create | `serve`, the intake's copy (T9 removes `intake → api`, so no shared import) |
| `internal/factory/intake/serve_test.go` | Create | Deviation 1 pin |
| `internal/factory/intake/runlore_test.go` | Modify | F-8 pin |
| `internal/slackapp/app.go` | Modify `App`, `run` (:174-180), `Events` (:251-255) | The worker pool, 503 on full, `Wait` |
| `internal/slackapp/pool_test.go` | Create | F-10 pins and their gated fakes |
| `internal/app/slackapp.go` | Modify `startSlackApp`'s `Wait` (:145-157) | The drain calls `App.Wait` |
| `docs/slack.md` | Modify :150 | The `busy` outcome |

---

### Task 1: F-7, the run-request API's Start awaits its drain

**Files:**
- Create: `internal/factory/api/serve.go`, `internal/factory/api/start_test.go`
- Modify: `internal/factory/api/server.go:202-216`

**Interfaces:**
- Consumes: `Server.Handler() http.Handler`, `Server.Cfg.API.Listen`.
- Produces (T6 Task 3 relies on these exact names):
  - `func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error`
  - `func (s *Server) listen(ctx context.Context) (net.Listener, error)`
  - `func (s *Server) httpServer() *http.Server`
  - `const apiDrain = 15 * time.Second`

- [ ] **Step 1: Write the failing test**

Create `internal/factory/api/start_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/httpx"
)

// F-7: serve returns only once Shutdown has, so a handler between its Task's create and its issue
// finishes before the process exits. Before, Start returned on ErrServerClosed while the handler
// was still writing, and the manager let the process go.
func TestServeReturnsOnlyAfterTheInFlightHandler(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var wrote atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "filed")
		wrote.Store(true)
	}), ReadHeaderTimeout: time.Second}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, 5*time.Second) }()

	body := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		if err != nil {
			body <- "request: " + err.Error()
			return
		}
		resp, err := httpx.New(5*time.Second, nil).Do(req)
		if err != nil {
			body <- "do: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()

	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("serve returned (%v) while a handler was still writing", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !wrote.Load() {
		t.Fatal("serve returned before the handler finished")
	}
	if got := <-body; got != "filed" {
		t.Fatalf("the client read %q", got)
	}
}

// AGENTS.md: every http.Server sets its five bounds.
func TestTheAPIListenerSetsItsBounds(t *testing.T) {
	srv := (&Server{Cfg: &config.Config{}}).httpServer()
	for name, v := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout, "ReadTimeout": srv.ReadTimeout,
		"WriteTimeout": srv.WriteTimeout, "IdleTimeout": srv.IdleTimeout,
	} {
		if v <= 0 {
			t.Errorf("%s unset", name)
		}
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes unset")
	}
}
```

- [ ] **Step 2: Run it: compile failure**

Run: `go test -race -count=1 ./internal/factory/api -run 'TestServeReturnsOnlyAfterTheInFlightHandler|TestTheAPIListenerSetsItsBounds'`
Expected: FAIL to build: `undefined: serve` and `s.httpServer undefined`.

- [ ] **Step 3: Extract today's body verbatim, to see the defect itself**

Create `internal/factory/api/serve.go` holding the current `Start` logic unchanged, and add `httpServer`/`listen` to `server.go` with `Start` calling them (Step 5 replaces `serve`'s body):

```go
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

In `internal/factory/api/server.go`, replace the whole `Start` (lines 202-216: the comment `// Start serves on api.listen until ctx ends, then drains.` through its closing brace) with:

```go
// apiDrain bounds Start's shutdown: under the manager's 20 s GracefulShutdownTimeout
// (internal/app's managerDrain) and the pod's 30 s grace, and long enough for a handler past its
// Task's create to file the issue.
const apiDrain = 15 * time.Second

// Start serves on api.listen until ctx ends, then drains for at most apiDrain, and returns only
// once the drain has (F-7): the manager waits on Start, so a handler between its Task's create
// and its issue finishes before the process exits.
func (s *Server) Start(ctx context.Context) error {
	ln, err := s.listen(ctx)
	if err != nil {
		return err
	}
	return serve(ctx, s.httpServer(), ln, apiDrain)
}

// listen binds api.listen.
func (s *Server) listen(ctx context.Context) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.Cfg.API.Listen)
	if err != nil {
		return nil, fmt.Errorf("api: listen %s: %w", s.Cfg.API.Listen, err)
	}
	return ln, nil
}

// httpServer is the API's listener with its five bounds.
func (s *Server) httpServer() *http.Server {
	return &http.Server{Handler: s.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
}
```

Add `"net"` to `server.go`'s imports (`errors` stays: other code in the file uses it).

- [ ] **Step 4: Run it: the defect fails the test**

Run: `go test -race -count=1 ./internal/factory/api -run 'TestServeReturnsOnlyAfterTheInFlightHandler|TestTheAPIListenerSetsItsBounds'`
Expected: `TestTheAPIListenerSetsItsBounds` passes; `TestServeReturnsOnlyAfterTheInFlightHandler` FAILs with `serve returned (<nil>) while a handler was still writing`: `Shutdown` closes the listener first, `Serve` returns `ErrServerClosed` at once, and `serve` returns while the handler is still blocked.

- [ ] **Step 5: Write the fix**

Replace the body of `internal/factory/api/serve.go` (keep the SPDX line and package clause) with:

```go
import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// serve serves srv on ln until ctx ends, then drains for at most drain, in the broker's shape
// (internal/app's serveHTTP). It returns only after Shutdown has, so every handler in flight when
// ctx ended has finished or been cut by drain (F-7).
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("api: serve %s: %w", ln.Addr(), err)
	case <-ctx.Done():
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	shut := srv.Shutdown(dctx)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		shut = errors.Join(shut, err)
	}
	if shut != nil {
		return fmt.Errorf("api: shut down %s: %w", ln.Addr(), shut)
	}
	return nil
}
```

- [ ] **Step 6: Run the package**

Run: `go test -race -count=1 ./internal/factory/api`
Expected: PASS, the pre-existing tests included.

- [ ] **Step 7: Commit**

```bash
git add internal/factory/api/serve.go internal/factory/api/server.go internal/factory/api/start_test.go
git commit -m "fix(factory): the run-request API's Start returns only after its drain"
```

---

### Task 2: Deviation 1, the RunLore intake's Start awaits its drain

**Files:**
- Create: `internal/factory/intake/serve.go`, `internal/factory/intake/serve_test.go`
- Modify: `internal/factory/intake/runlore.go:84-101`

**Interfaces:**
- Consumes: `RunLore.ServeHTTP`, `RunLore.Cfg.RunLore.Listen`.
- Produces: `func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error` (package `intake`); `func (h *RunLore) httpServer() *http.Server`; `const runloreDrain = 15 * time.Second`.

- [ ] **Step 1: Write the failing test**

Create `internal/factory/intake/serve_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/httpx"
)

// F-7's twin: the intake's serve returns only once Shutdown has, so a finding past its Task's
// create files its issue before the process exits.
func TestIntakeServeReturnsOnlyAfterTheInFlightHandler(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var wrote atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "filed")
		wrote.Store(true)
	}), ReadHeaderTimeout: time.Second}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, 5*time.Second) }()

	body := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		if err != nil {
			body <- "request: " + err.Error()
			return
		}
		resp, err := httpx.New(5*time.Second, nil).Do(req)
		if err != nil {
			body <- "do: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()

	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("serve returned (%v) while a handler was still writing", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !wrote.Load() {
		t.Fatal("serve returned before the handler finished")
	}
	if got := <-body; got != "filed" {
		t.Fatalf("the client read %q", got)
	}
}

// AGENTS.md: every http.Server sets its five bounds.
func TestTheIntakeListenerSetsItsBounds(t *testing.T) {
	srv := (&RunLore{Cfg: &config.Config{}}).httpServer()
	for name, v := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout, "ReadTimeout": srv.ReadTimeout,
		"WriteTimeout": srv.WriteTimeout, "IdleTimeout": srv.IdleTimeout,
	} {
		if v <= 0 {
			t.Errorf("%s unset", name)
		}
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes unset")
	}
}
```

- [ ] **Step 2: Run it: compile failure**

Run: `go test -race -count=1 ./internal/factory/intake -run 'TestIntakeServeReturnsOnlyAfterTheInFlightHandler|TestTheIntakeListenerSetsItsBounds'`
Expected: FAIL to build: `undefined: serve`, `h.httpServer undefined`.

- [ ] **Step 3: Extract today's body verbatim**

Create `internal/factory/intake/serve.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

In `internal/factory/intake/runlore.go`, replace `Start` (from `// Start serves POST /intake/runlore on runlore.listen until ctx ends, then drains, in the` through its closing brace) with:

```go
// runloreDrain bounds Start's shutdown, as the run-request API's: under the manager's 20 s
// GracefulShutdownTimeout and the pod's 30 s grace.
const runloreDrain = 15 * time.Second

// Start serves POST /intake/runlore on runlore.listen until ctx ends, then drains for at most
// runloreDrain, and returns only once the drain has, in the run-request API server's shape
// (internal/factory/api): a finding past its Task's create files its issue before the exit.
func (h *RunLore) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", h.Cfg.RunLore.Listen)
	if err != nil {
		return fmt.Errorf("runlore: listen %s: %w", h.Cfg.RunLore.Listen, err)
	}
	return serve(ctx, h.httpServer(), ln, runloreDrain)
}

// httpServer is the intake's listener with its five bounds.
func (h *RunLore) httpServer() *http.Server {
	mux := http.NewServeMux()
	mux.Handle("POST /intake/runlore", h)
	return &http.Server{Handler: mux,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
}
```

In `runlore.go`'s imports add `"net"` and remove `"errors"`: the old `Start` was its only user.

- [ ] **Step 4: Run it: the defect fails the test**

Run: `go test -race -count=1 ./internal/factory/intake -run 'TestIntakeServeReturnsOnlyAfterTheInFlightHandler|TestTheIntakeListenerSetsItsBounds'`
Expected: bounds PASS; `TestIntakeServeReturnsOnlyAfterTheInFlightHandler` FAILs with `serve returned (<nil>) while a handler was still writing`.

- [ ] **Step 5: Write the fix**

Replace `internal/factory/intake/serve.go`'s imports and `serve` (keep SPDX and package) with:

```go
import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// serve serves srv on ln until ctx ends, then drains for at most drain, in the broker's shape
// (internal/app's serveHTTP), and returns only after Shutdown has. It copies
// internal/factory/api's serve rather than importing it: intake must not depend on the API
// package (audit A-1).
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("runlore: serve %s: %w", ln.Addr(), err)
	case <-ctx.Done():
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	shut := srv.Shutdown(dctx)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		shut = errors.Join(shut, err)
	}
	if shut != nil {
		return fmt.Errorf("runlore: shut down %s: %w", ln.Addr(), shut)
	}
	return nil
}
```

- [ ] **Step 6: Run the package**

Run: `go test -race -count=1 ./internal/factory/intake`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/factory/intake/serve.go internal/factory/intake/serve_test.go internal/factory/intake/runlore.go
git commit -m "fix(factory): the RunLore intake's Start returns only after its drain"
```

---

### Task 3: F-8, a RunLore caller that hangs up no longer orphans its Task

**Files:**
- Modify: `internal/factory/intake/runlore.go:231-241` (`intake`, after `h.Client.Create`)
- Test: `internal/factory/intake/runlore_test.go`

**Interfaces:**
- Consumes: `runlore(t) (*RunLore, client.Client, *forgetest.Fake)`, `finding`, `forgetest.Fake.CreateIssue`, `forgetest.Fake.Created()`, the `runloreForge` interface.
- Produces: `const fileTimeout = 30 * time.Second` (package `intake`).

- [ ] **Step 1: Write the failing test**

Append to `internal/factory/intake/runlore_test.go` (add `"github.com/Smana/maku/internal/factory/forge"` to its imports):

```go
// ctxForge refuses a call on a spent context, as the real client's HTTP request does.
type ctxForge struct{ *forgetest.Fake }

func (f ctxForge) CreateIssue(ctx context.Context, title, body string, labels []string) (forge.Issue, error) {
	if err := ctx.Err(); err != nil {
		return forge.Issue{}, err
	}
	return f.Fake.CreateIssue(ctx, title, body, labels)
}

// F-8: once the Task exists, a caller that hangs up must not orphan it: the issue is filed and
// the Task linked to it on a context that outlives the request.
func TestAHangUpAfterTheTaskStillFilesTheIssue(t *testing.T) {
	h, _, f := runlore(t)
	ctx, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	h.Client = fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.Task{}).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			err := c.Create(ctx, obj, opts...)
			hangUp() // the caller goes once the Task exists
			return err
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			if err := ctx.Err(); err != nil { // the apiserver call fails on a spent context
				return err
			}
			return c.Patch(ctx, obj, p, opts...)
		},
	}).Build()
	h.Forge = ctxForge{f}

	b, _ := json.Marshal(finding)
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/intake/runlore", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(httptest.NewRecorder(), r)

	if n := len(f.Created()); n != 1 {
		t.Fatalf("the issue was not filed after the hang-up: %d issues", n)
	}
	var l v1alpha1.TaskList
	if err := h.Client.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 1 || l.Items[0].Spec.Issue == 0 {
		t.Fatalf("the Task is not linked to its issue: %+v", l.Items)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test -race -count=1 ./internal/factory/intake -run TestAHangUpAfterTheTaskStillFilesTheIssue`
Expected: FAIL with `the issue was not filed after the hang-up: 0 issues`: `CreateIssue` runs on `r.Context()`, already cancelled, so the intake answers 503 and leaves an issue-less Task.

- [ ] **Step 3: Write the fix**

In `internal/factory/intake/runlore.go`, below `const unconfiguredWarnEvery = time.Minute`, add:

```go
// fileTimeout bounds filing a finding's issue and linking its Task, past the request's own
// context (F-8), as the run-request API's internal intake bounds them.
const fileTimeout = 30 * time.Second
```

In `intake`, replace the block from `title, body := findingfmt.StubIssue(` through the `h.Client.Patch(ctx, …)` error check with:

```go
	title, body := findingfmt.StubIssue(f.AlertName, f.ResourceRef, f.Severity, f.Verdict, f.Confidence, name, h.Cfg.RoomsURL)
	// Past the Task, a caller that hangs up must not orphan it: filing and linking outlive ctx
	// (F-8), as api/internal.go's intake does.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fileTimeout)
	defer cancel()
	iss, err := h.Forge.CreateIssue(fctx, title, body, []string{"factory/proposed"})
	if err != nil {
		return 0, nil, err
	}
	n := iss.Number
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"issue": n}})
	if err := h.Client.Patch(fctx, &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.Namespace}},
		client.RawPatch(types.MergePatchType, patch)); err != nil {
		return 0, nil, err
	}
```

Keep the two comment lines above `title, body :=` (`// The issue is public: …`) as they are.

- [ ] **Step 4: Run the package**

Run: `go test -race -count=1 ./internal/factory/intake`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/intake/runlore.go internal/factory/intake/runlore_test.go
git commit -m "fix(factory): a RunLore caller that hangs up no longer orphans its Task"
```

---

### Task 4: F-10, Slack events on 8 owned workers; a full queue answers 503

**Files:**
- Create: `internal/slackapp/pool_test.go`
- Modify: `internal/slackapp/app.go` (`App` fields :63-80, `run` :174-180, `Events` :245-256), `internal/app/slackapp.go:145-157`, `docs/slack.md:150`

**Interfaces:**
- Consumes: `newAppRig(t, Channel) *appRig`, `appRig.postEvents(body string, signed bool) *httptest.ResponseRecorder`, `appRig.eventBody(id, event string) string`, `slackSign(secret, ts, body string) string`, `fakeSlack`, `stubBroker`, `fakeThreads.get`, `fakeMetrics.count`, `draftPayload`, `seen.add`/`seen.forget`, `rigSecret`.
- Produces:
  - `const eventWorkers = 8`, `const eventQueue = 64`
  - `func (a *App) enqueue(f func()) bool`
  - `func (a *App) Wait()` — stops taking events and returns once every queued and running event has finished.
  - `App.Async` keeps its type; nil now means the pool.
  - Counter outcome `busy` on route `events`.

- [ ] **Step 1: Write the failing tests**

Create `internal/slackapp/pool_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gatedSlack holds every Message call until release closes, signalling each arrival on entered:
// it pins a worker on an event before the handler takes editMu.
type gatedSlack struct {
	*fakeSlack
	entered chan struct{}
	release chan struct{}
}

func (g *gatedSlack) Message(ctx context.Context, channel, ts string) (Message, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.fakeSlack.Message(ctx, channel, ts)
}

// gatedBroker holds every RunCreate until release closes.
type gatedBroker struct {
	*stubBroker
	entered chan struct{}
	release chan struct{}
}

func (g *gatedBroker) RunCreate(ctx context.Context, c RunCreate) (RunResult, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.stubBroker.RunCreate(ctx, c)
}

const checkMark = `{"type":"reaction_added","user":"U02DEF","reaction":"white_check_mark","item":{"type":"message","channel":"C03","ts":"1729.0009"}}`

// F-10: 8 workers each hold an event and 64 wait; the next event answers 503, so Slack retries
// it, and its id is forgotten, so the retry is handled rather than read as a duplicate.
func TestAFullEventQueueAnswers503(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	gate := &gatedSlack{fakeSlack: rig.slack, entered: make(chan struct{}, eventWorkers+eventQueue+1), release: make(chan struct{})}
	rig.app.Slack, rig.app.Async = gate, nil
	react := func(id string) int { return rig.postEvents(rig.eventBody(id, checkMark), true).Code }

	for i := range eventWorkers {
		if st := react(fmt.Sprintf("Ev-busy-%d", i)); st != http.StatusOK {
			t.Fatalf("event %d for a worker: %d", i, st)
		}
	}
	for range eventWorkers { // every worker now holds one event
		<-gate.entered
	}
	for i := range eventQueue {
		if st := react(fmt.Sprintf("Ev-queued-%d", i)); st != http.StatusOK {
			t.Fatalf("queued event %d: %d", i, st)
		}
	}
	if st := react("Ev-over"); st != http.StatusServiceUnavailable {
		t.Fatalf("the event past the queue: %d, want 503", st)
	}
	if n := rig.metrics.count("events", "busy"); n != 1 {
		t.Fatalf("busy counted %d times", n)
	}
	if !rig.app.events.add("Ev-over") {
		t.Fatal("the refused event's id is still marked seen: Slack's retry would be dropped as a duplicate")
	}
	close(gate.release)
	rig.app.Wait()
	if st := react("Ev-after-wait"); st != http.StatusServiceUnavailable {
		t.Fatalf("an event after Wait: %d, want 503", st)
	}
}

// F-10: an event in flight when shutdown starts finishes, its thread map Put and its reply
// included, before Wait returns; its request's context being cancelled changes nothing.
func TestWaitFinishesTheEventInFlight(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.slack.seed(Message{TS: "1729.0009", ThreadTS: "1729.0001", User: "U0BOT", Text: "draft",
		Metadata: &Metadata{EventType: draftEventType, EventPayload: draftPayload("Smana/ai-native-ref", "t", "b")}})
	gate := &gatedBroker{stubBroker: rig.broker, entered: make(chan struct{}, 1), release: make(chan struct{})}
	rig.app.Broker, rig.app.Async = gate, nil

	ctx, cancel := context.WithCancel(context.Background())
	body := rig.eventBody("Ev-inflight", checkMark)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/slack/events", strings.NewReader(body))
	ts := strconv.FormatInt(rig.now.Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackSign(rigSecret, ts, body))
	w := httptest.NewRecorder()
	rig.app.Events().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("events: %d", w.Code)
	}
	<-gate.entered // RunCreate has the event
	cancel()       // SIGTERM: the request's context goes

	waited := make(chan struct{})
	go func() { rig.app.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned with an event in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate.release)
	<-waited
	if th, ok := rig.threads.get("aaaaaaaa"); !ok || th.TS != "1729.0001" {
		t.Fatalf("the thread map was not written before Wait returned: %+v %v", th, ok)
	}
	if !strings.Contains(rig.lastPost().text, "Filed #42") {
		t.Fatalf("the reply was not posted before Wait returned: %q", rig.lastPost().text)
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test -race -count=1 ./internal/slackapp -run 'TestAFullEventQueueAnswers503|TestWaitFinishesTheEventInFlight'`
Expected: FAIL to build: `undefined: eventWorkers`, `undefined: eventQueue`, `rig.app.Wait undefined`. The defect itself: `run` starts `go f()` per event, unbounded, and nothing can wait for them.

- [ ] **Step 3: Write the pool**

In `internal/slackapp/app.go`, add to the `const` block at the top (after `maxShown = 300`):

```go
	// eventWorkers handle Slack events after their 200; eventQueue events wait for one. Past
	// both, an event answers 503 and Slack retries it (F-10).
	eventWorkers = 8
	eventQueue   = 64
```

Change the `Async` field's comment and add the pool's fields to `App` after `editMu`:

```go
	Async       func(func()) // runs an event's handling after the 200 (tests); nil means the App's workers
```

```go
	poolOnce sync.Once
	poolMu   sync.Mutex  // guards draining and every send on queue
	draining bool        // Wait has run: queue is closed
	queue    chan func() // eventQueue slots, fed by enqueue, drained by eventWorkers
	workers  sync.WaitGroup
```

Replace `run` (lines 174-180) with:

```go
// enqueue hands f to a worker and reports whether it was taken: false when eventQueue events
// already wait, or Wait has run. Async, when set, runs f itself.
func (a *App) enqueue(f func()) bool {
	if a.Async != nil {
		a.Async(f)
		return true
	}
	a.poolOnce.Do(a.startWorkers)
	a.poolMu.Lock()
	defer a.poolMu.Unlock()
	if a.draining {
		return false
	}
	select {
	case a.queue <- f:
		return true
	default:
		return false
	}
}

// startWorkers starts the eventWorkers that drain queue until Wait closes it.
func (a *App) startWorkers() {
	a.queue = make(chan func(), eventQueue)
	for range eventWorkers {
		a.workers.Go(func() {
			for f := range a.queue {
				f()
			}
		})
	}
}

// Wait stops taking events and returns once every queued and running one has finished: each
// was answered 200, so Slack will not send it again (F-10). Each event is bounded by
// handleTimeout. Safe to call more than once.
func (a *App) Wait() {
	a.poolOnce.Do(a.startWorkers)
	a.poolMu.Lock()
	if !a.draining {
		a.draining = true
		close(a.queue)
	}
	a.poolMu.Unlock()
	a.workers.Wait()
}
```

In `Events`, replace the `case "event_callback":` body (lines 245-256) with:

```go
		case "event_callback":
			if env.EventID == "" || !a.events.add(env.EventID) {
				a.count(ctx, "events", "duplicate")
				w.WriteHeader(http.StatusOK)
				return
			}
			if !a.enqueue(func() {
				hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
				defer cancel()
				a.count(hctx, "events", a.handle(hctx, env))
			}) {
				// Full or draining: Slack retries a non-2xx, so the event is not lost, and its id
				// is forgotten so that retry is handled rather than read as a duplicate.
				a.events.forget(env.EventID)
				a.count(ctx, "events", "busy")
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
```

- [ ] **Step 4: Run the package**

Run: `go test -race -count=1 ./internal/slackapp`
Expected: PASS, every pre-existing test included (they set `Async`, which still runs inline).

- [ ] **Step 5: The Slack App's drain waits for the events**

In `internal/app/slackapp.go`, in the `Wait` closure `startSlackApp` returns, call `a.Wait()` right after `front.Wait()`:

```go
	return &slackRunning{Events: events.Addr(), Webhooks: hooks.Addr(), Ops: opsLn.Addr(), Wait: func() error {
		front.Wait()
		a.Wait() // events answered 200 finish: Slack never sends them again (F-10)
		stopOps() // :9090 stops last, so the drain's final counts are scraped and /readyz says 503 meanwhile
		bak.Wait()
```

(the rest of the closure unchanged).

In `docs/slack.md:150`, add `busy` to the closed list, so the line reads:

```markdown
- Series: `factory_slack_requests_total{route, outcome}` (`events`, `webhooks`; the closed list is `drafted`, `edited`, `refused`, `filed`, `duplicate`, `posted`, `link`, `unlinked`, `ignored`, `bad_signature`, `no_key`, `too_large`, `busy` and `error`). `busy` is an event answered 503 because the App's 8 workers and its 64 queued events are all taken, or it is shutting down: Slack retries it.
```

- [ ] **Step 6: Run the wiring's tests**

Run: `go test -race -count=1 ./internal/app -run 'TestStartSlackApp|TestSlackApp'`
Expected: PASS (`TestStartSlackApp` cancels and `Wait` still returns nil within 10 s).

- [ ] **Step 7: Commit**

```bash
git add internal/slackapp/app.go internal/slackapp/pool_test.go internal/app/slackapp.go docs/slack.md
git commit -m "fix(slackapp): events run on 8 bounded workers, and shutdown waits for them"
```

---

### Task 5: The gate

**Files:** none new.

- [ ] **Step 1: Run the full gate (Docker running, for the store tests)**

Run: `task check`
Expected: exit 0. A `lint` finding (an unused import left by Task 2, a `revive` doc comment) is fixed with `git commit --amend` when it belongs to the last commit, otherwise in one more commit scoped to that finding's package (e.g. `fix(factory): …`). Interactive rebase is not available here.

- [ ] **Step 2: Confirm the spec's checks**

Run: `go test -race -count=1 ./internal/factory/api ./internal/factory/intake ./internal/slackapp ./internal/app`
Expected: PASS. Then `git log --oneline main..` shows four commits, one per task 1-4, none with an attribution line.

## Self-review

| Spec item | Task |
|---|---|
| F-7 `api.Server.Start` returns only after `Shutdown` | 1 |
| F-7 pin: a blocked handler completes before `Start` returns | 1 (`TestServeReturnsOnlyAfterTheInFlightHandler`) |
| F-8 `WithoutCancel(r.Context())` bounded by 30 s, issue and patch | 3 |
| F-8 pin: cancel after the Task's create; issue filed, Task patched | 3 |
| F-10 8 workers, 64 slots, 503 on full, `Wait` joins, drain calls it | 4 |
| F-10 pins: the queue-overflow 503; `Wait` returns after `Threads.Put` | 4 (deviation 3 for the count) |
| D0 one commit per finding, `task check` | 1-5 |
