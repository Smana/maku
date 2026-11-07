# The quickstart page: implementation plan (plan D of the factory on a laptop)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. One task.

**Goal:** `docs/quickstart.md` leads with `task demo` and its 15-minute promise, and carries the "what production adds" table, so a platform engineer can run the demo from this page alone.

**Spec:** `docs/superpowers/specs/2026-11-06-factory-on-a-laptop-design.md` (Q1-Q27, the table "What production adds"), owner decisions D1-D5 (plan B's ledger), plan C's `hack/demo/up.sh` as built.

## Global Constraints

- The reader is a platform engineer (V9): fluent in Kubernetes, Helm, GitOps and GitHub Apps, new to agents.
- No session history, dates, cluster names or proof IDs in the page; present tense, second person.
- The page states the demo's limits plainly: public repositories only; not isolated (runc); the gate's verdict is advisory against an agent holding the token (D2); the judge calls the run's own model, its alias not pinned by a proxy (D1).
- `task check`'s `docs` step passes (`hack/docs.sh`: no per-row status tags, no dead relative links).

### Task 1: The quickstart page around `task demo`

**Files:** Modify `docs/quickstart.md`, `README.md` (its quickstart lines), `docs/cli.md` only if a command it names changed.

Structure of `docs/quickstart.md`:
1. **Title and promise:** the whole factory loop on a laptop in about 15 minutes, as a demo of how production works.
2. **Before you start:** Docker (with buildx), Helm, kubectl, kind or k3d, Task; a GitHub account; an OpenAI-compatible model endpoint and key; Linux (macOS best-effort, runc).
3. **Run it:** `git clone`, `task demo`; what it asks, the two GitHub clicks (Create, Install), what it creates (a cluster `maku-demo`, your repository from `Smana/maku-demo` with `gh`, or the "Use this template" link), and what it prints. The environment variables it reads (one table: `MODEL_BASE_URL`, `MODEL_NAME`, `MODEL_API_KEY`, `GITHUB_APP_ID`, `GITHUB_APP_KEY_FILE`, `DEMO_SLUG`, `DEMO_REPO`, `CLUSTER`, `MAKU_VERSION`, `DEMO_NO_GH`, `DEMO_LIVE`, and any other `up.sh` reads — read it, don't guess).
4. **Watch the loop:** label the issue; the room (scoper, implementer under the "Not sandboxed" banner, judge, reviewer); the pull request by `<slug>[bot]`; the `maku/merge-gate` status; turning the class live (`DEMO_LIVE=1`, re-run) and a second issue merging with no human.
5. **What production adds:** the design's table, updated: network policies exist in the demo but only for the database and the factory API (runs keep their egress); the judge's model pinned by a proxy (D1); branch protection requires your CI's check, and the gate's verdict is advisory against an agent holding the token (D2); TLS row as amended.
6. **Clean up:** `task demo:down` (`--all` also forgets the App), and deleting the App and repository on GitHub.
7. **Troubleshooting:** from `up.sh`'s own messages (read them): private repository refused, App lookup 401/404, a context mismatch under `CLUSTER=current`, port 8080 busy, a half-made App.
8. **Without the script:** a short pointer to `charts/agent-factory/values-mine.example.yaml` and `helm install` for readers who bring their own cluster.
9. **The one-run quickstart:** today's `task quickstart` (gVisor, one attended run, no factory) moves under a heading at the end, unchanged in substance, so its CI job and links keep working.

- [ ] Step 1: write the page from the scripts and charts as built. Step 2: `~/.cache/v3/node24.sh task check` exit 0. Step 3: commit `docs(quickstart): the factory on a laptop in one command`.
