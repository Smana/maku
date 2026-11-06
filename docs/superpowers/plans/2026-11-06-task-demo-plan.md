# `task demo`: implementation plan (plan C of the factory on a laptop)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `git clone` then `task demo` brings the laptop demo up on kind or k3d, creates the GitHub App from a manifest in two clicks, creates the reader's repository from the template, and ends by printing the room page and the issue to label.

**Architecture:** A bash entry point (`hack/demo/up.sh`) drives the steps and keeps a resume state file; one Go helper (`maku demo app`, a `maku` CLI subcommand) does the GitHub App manifest flow with a localhost callback, because it is the one step that needs an HTTP server and a JSON exchange. The script installs plan B's `charts/agent-factory` with `profile: demo` and values it writes from the answers, and keeps the room page's port-forward running in the background.

**Tech Stack:** bash, Go (the `maku` CLI), Helm, kind, k3d, `gh` (optional), GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-06-factory-on-a-laptop-design.md` (Q1, Q3, Q16, Q22-Q27) and the owner decisions D1-D5 recorded in plan B's ledger (`.superpowers/sdd/2026-11-06-agent-factory-chart-plan/progress.md`).

## Global Constraints

- `task check` exits 0 before every commit (`~/.cache/v3/node24.sh task check`); lint via `task lint`; conventional commits, no trailers.
- Q1: run from a clone, `task demo`. Q25: one command, it prompts (or reads the environment or `.env`), resumes where it stopped, no second terminal, no `maku configure`. Q26: 15 minutes from clone to the gate's status; the script prints time per step.
- Q22: kind or k3d (`CLUSTER=kind|k3d`), or the current kubeconfig (`CLUSTER=current`); tools: Docker, Helm, kubectl, kind or k3d, Task.
- Q23: one GitHub App from a manifest; Q27: an existing App's id and key from the environment skip the browser.
- Q15: public repositories only. D2: with `gh` logged in, the script adds a ruleset requiring pull requests and the template's GitHub Actions check (integration id 15368), never `maku/merge-gate`. D4: the template is `Smana/maku-demo`, created from `examples/demo-repo/`. D5: the real-model nightly job skips with a visible summary when its secrets are absent.
- The App's permissions (one list, `docs/factory.md#demo`): contents write, pull_requests write, issues write, checks read, statuses write, metadata read; no webhook.
- Never touch a cluster or kube context the script did not create unless `CLUSTER=current` is set; never print the App key or the model key.

## File structure

| File | Responsibility |
|---|---|
| `examples/demo-repo/` | The template repository's content: README, a docs page with a broken link, `.github/workflows/ci.yaml` (job `ci`), `ISSUES.md` listing the seeded issues |
| `cmd/maku/demo_app.go` (+ test) | `maku demo app`: the manifest flow, writing `app_id`, `private_key` (0600) and `slug` into a directory |
| `hack/demo/up.sh`, `down.sh`, `lib.sh`, `lib_test.sh` | The steps, the state file, prompts, cluster creation, values file, install, port-forward supervision |
| `taskfile.yaml` | `demo`, `demo:down`, `demo:check` (offline: shellcheck + lib_test) — `demo:check` joins `task check` |
| `charts/agent-factory/templates/networkpolicy.yaml` | Plain Kubernetes NetworkPolicies for the demo (final review recommendation) |
| `.github/workflows/demo-e2e.yaml` | k3d install on PRs touching the chart or `hack/demo`; nightly kind real-model run, skipping without secrets |

---

### Task 1: The template repository's content

**Files:** Create `examples/demo-repo/README.md`, `examples/demo-repo/docs/guide.md` (two links, one broken), `examples/demo-repo/.github/workflows/ci.yaml` (`name: ci`, on pull_request and push, one job `ci` that runs a markdown link check that ignores the known-broken link only on `main`… keep it `echo ok` if a link checker would make the template's own CI red), `examples/demo-repo/ISSUES.md` (the two seeded issues: "Fix the broken link in docs/guide.md" with `class:docs-links`, and "Add a sentence to the README about what this repository is for"), `examples/demo-repo/LICENSE` (MIT). Modify `hack/docs.sh` or the link check if it would scan `examples/demo-repo` and fail on the deliberate broken link (exclude that one path, with a comment).

- [ ] Step 1: write the files. Step 2: `task check` (docs and link checks must still pass). Step 3: commit `feat(demo): the template repository's content`.

### Task 2: `maku demo app`, the manifest flow

**Files:** Create `cmd/maku/demo_app.go`, `cmd/maku/demo_app_test.go`; register the subcommand where the other `maku` subcommands are registered (read `cmd/maku/main.go`).

Behaviour:
- `maku demo app --dir <path> [--owner <org>] [--name <name>]` starts an HTTP server on `127.0.0.1:<free port>`, prints and opens (`xdg-open`/`open`, ignore failure) `http://127.0.0.1:<port>/`, which serves an auto-submitting form POSTing `manifest=<json>` to `https://github.com/settings/apps/new?state=<random>` (or `/organizations/<org>/settings/apps/new`).
- The manifest: `name`, `url` (`https://github.com/Smana/maku`), `hook_attributes: {active: false}` (url required by GitHub even when inactive: use the same URL), `redirect_url: http://127.0.0.1:<port>/callback`, `public: false`, `default_permissions` exactly the constraint list, `default_events: []`.
- `/callback?code=<code>&state=<state>`: refuse a wrong state; `POST https://api.github.com/app-manifests/<code>/conversions` (no auth); write `app_id`, `private_key` (mode 0600), `slug` and `html_url` into `--dir`; print "Now install the App on your repository: <html_url>/installations/new" and exit 0. A 5-minute timeout exits 1 with a clear message.
- The API base URL is a flag for tests (`--api`, default `https://api.github.com`), as is the GitHub base for the form.

- [ ] Step 1: failing tests with httptest: the served form carries the exact manifest; a wrong state is refused; a conversion response is written with mode 0600 for the key; the key never reaches stdout. Step 2: run, fail. Step 3: implement. Step 4: `go test -race ./cmd/maku/ -count=1`. Step 5: `task check`; commit `feat(maku): demo app, the GitHub App manifest flow`.

### Task 3: `task demo` and `task demo:down`

**Files:** Create `hack/demo/up.sh`, `hack/demo/down.sh`, `hack/demo/lib.sh`, `hack/demo/lib_test.sh`; modify `taskfile.yaml`.

Steps of `up.sh`, each recorded in `.demo/state` (one line per finished step) and skipped on a re-run, each printing its duration:
1. **Preflight:** Linux or macOS (macOS prints the best-effort note, Q2); docker, helm, kubectl, and kind or k3d per `CLUSTER` (default `k3d` if installed, else `kind`); `gh` optional.
2. **Model:** `MODEL_BASE_URL`, `MODEL_NAME`, `MODEL_API_KEY` from the environment or `.env`, else prompt (the key read without echo). Never print the key.
3. **GitHub App:** `GITHUB_APP_ID` and `GITHUB_APP_KEY_FILE` (Q27) skip this; else `maku demo app --dir .demo/app` (built with `go run ./cmd/maku` if no `maku` binary), then wait for the reader to install it.
4. **Repository:** `DEMO_REPO` (owner/name) or prompt. With `gh` logged in and the repository absent: `gh repo create <repo> --public --template Smana/maku-demo`; else print the "Use this template" link and wait. Refuse a private repository (`gh api repos/<repo> --jq .private`, or unauthenticated GET). With `gh`: create the label `factory/ready` and `class:docs-links`, the seeded issues from the template's `ISSUES.md` if none are open, and the ruleset (D2) requiring PRs and the `ci` check from integration 15368; without `gh`, print what to do by hand.
5. **Cluster:** `kind create cluster --name maku-demo` / `k3d cluster create maku-demo`, with a dedicated kubeconfig at `.demo/kubeconfig`; `CLUSTER=current` uses `$KUBECONFIG` after a confirmation naming the context.
6. **Install:** write `.demo/values.yaml` (0600) from the answers — the whole `repositories[0]` entry, the logins `<slug>[bot]`, the checks `ci` — and `helm upgrade --install af charts/agent-factory -n maku --create-namespace` with images pinned by digest: `MAKU_VERSION` (default: the latest release) resolved with `docker buildx imagetools inspect`. Wait for the four workloads.
7. **Room page:** start `kubectl port-forward deploy/room-broker 8080:8080` in the background, pid in `.demo/port-forward.pid`, restarted if it dies while `up.sh` runs; print `http://localhost:8080`.
8. **Next step:** print the issue URL to label and what the reader will see (the room, the PR, `maku/merge-gate`), and the one value to go live (Q13).

`down.sh`: stop the port-forward, delete the cluster the script created (never `current`), keep `.demo/app` unless `--all`.

`lib_test.sh` tests the pure functions: the state file, `.env` parsing, the values file renderer (rendered with plan B's Parse test as in `charts/agent-factory/tests/render.sh`), the private-repository refusal, the CLUSTER choice.

- [ ] Step 1: failing `lib_test.sh`. Step 2: run, fail. Step 3: implement. Step 4: `task demo:check`; then run `task demo` for real on k3d with `GITHUB_APP_ID`/`GITHUB_APP_KEY_FILE` pointing at a dummy key and `DEMO_REPO` at a public repository you only read (no `gh` writes: `DEMO_NO_GH=1`), and assert it reaches the factory's App lookup, then `task demo:down`. Step 5: `task check`; commit `feat(demo): task demo, the laptop demo in one command`.

### Task 4: Plain NetworkPolicies for the demo

**Files:** Create `charts/agent-factory/templates/networkpolicy.yaml`; modify `tests/render.sh`, `values.yaml`.

Under `profile: demo` (`networkPolicies.plain: true` default in the umbrella): ingress to `rooms-db` only from the broker's pods; ingress to the factory's API port only from the broker; nothing else changed (runs keep egress: they need GitHub and the model). k3s enforces NetworkPolicy; kind enforces it with its default CNI from v0.24 — the install test asserts a run-labelled pod cannot reach `rooms-db:5432`.

- [ ] Step 1: failing render rows. Step 2: implement. Step 3: extend `charts/agent-factory/tests/install.sh` with the reachability assertion; run `task chart:install:k3d`. Step 4: `task check`; commit `feat(chart): plain network policies for the demo's database and factory`.

### Task 5: CI for the demo

**Files:** Create `.github/workflows/demo-e2e.yaml`.

- `pull_request` on paths `charts/**`, `hack/demo/**`, `internal/runctl/**`, `internal/factory/**`, `cmd/maku/**`: `task chart:install:k3d` on ubuntu-latest (Docker is there; install k3d by pinned version).
- `schedule` nightly (03:17 UTC) and `workflow_dispatch`: kind, `task demo` headless with secrets `DEMO_MODEL_BASE_URL`, `DEMO_MODEL_NAME`, `DEMO_MODEL_API_KEY`, `DEMO_APP_ID`, `DEMO_APP_KEY`, `DEMO_REPO`; label the seeded issue; wait up to 30 minutes for a `maku/merge-gate` status on a PR by the App; fail otherwise. Without the secrets: skip with a step summary "nothing ran: <the missing secret names>" (D5).
- Pin every action by SHA as the other workflows do.

- [ ] Step 1: write the workflow; `actionlint` if the repo has it (read `task check`). Step 2: `task check`; commit `ci(demo): k3d on pull requests, kind nightly on a real model`.

### Task 6: Publish the template repository (D4)

Controller-run, after Tasks 1-5 are reviewed: `gh repo create Smana/maku-demo --public --description "The Agent Factory's laptop demo template"`, push `examples/demo-repo/` as its `main`, `gh api -X PATCH repos/Smana/maku-demo -f is_template=true`. The owner reviews it before plan C's PR merges.

## Self-review

- Q1, Q2, Q3, Q16, Q22-Q27 and D2, D4, D5: Tasks 1-6. D1 and the quickstart page: plan D. D3 (remove plan A's http and empty-caFile relaxations): after the owner's manual test or the nightly job shows the factory reaching the broker over TLS with a real App; a follow-up commit, not this plan.
- The owner tests `task demo` before plan C's PR merges.
