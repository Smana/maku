<!-- page-type: how-to -->

# Opt-in OpenCode harness

Maku supports OpenCode 2.3.0 as an opt-in second harness alongside OpenHands. **OpenHands remains the default everywhere** until an operator opts a run, task, cohort, or factory into OpenCode.

## Enable the OpenCode runner image

Pin `ghcr.io/smana/maku-opencode` by digest in the `maku` chart values:

```yaml
runController:
  images:
    opencode:
      repository: ghcr.io/smana/maku-opencode
      tag: "<version>@sha256:<digest>"
```

When `runController.images.opencode.tag` is empty (the default), the run-controller config omits `images.opencode` and the run-pod admission policy admits only the OpenHands harness, bridge, and run-controller repositories. Setting `tag` renders `images.opencode` into `run-controller-config` and adds `runController.images.opencode.repository` (still requiring `@sha256:` digests) to `ValidatingAdmissionPolicy/maku-<ns>-run-pods`.

## Selecting OpenCode

Selection follows a strict precedence and is frozen onto `Task.status.profile` at triage so config changes never switch an in-flight task:

1. **Maintainer task label or intake field (`labeled`):** apply `harness:opencode` or `harness:openhands` on a GitHub issue, or pass `"harness": "opencode"` in `POST /v1/tasks`. Non-maintainer labels are refused and narrated; conflicting labels fall back to the default.
2. **Treatment cohort (`treatment`):** set `evaluation.treatmentHarness: opencode` in the factory config to run the treatment arm on OpenCode without changing workflow, template, loop policy, repository checks, model, effort, or tier.
3. **Factory default (`default`):** set `defaults.harness: opencode` (absent by default, which resolves to `openhands`).
4. **One-off API run:** pass `"harness": "opencode"` to `POST /v1/runs` (never modifies an existing task).

Integration-only (no-model) verification tasks are always pinned to `openhands`.

## Rescuing an escalated OpenCode task onto OpenHands

If an OpenCode task escalates, a repository maintainer with write access can rescue it explicitly by commenting alone on a line on the issue or pull request:

```text
/factory retry harness=openhands
```

Before queueing the retry, the factory:
- records the failed OpenCode attempt once under its original cohort and `harness="opencode"`, storing that cohort in `Task.status.outcomeCountedCohorts` so crash replays cannot double-count;
- preserves the task's workflow, branch, and room;
- switches `Task.status.profile` to `harness: openhands`, `harnessSource: rescue`, `cohort: rescue`, and resets `counted: false`.

A plain `/factory retry` keeps the task's frozen cohort and harness unchanged.

## Provider, security, and tracing contract

- **Role permissions:** `implementer`, `reviewer`, `tester`, and `triager` receive least-privilege OpenCode V2 permission rules generated per run (`update: "disable"`, `share: "disabled"`, `OPENCODE_DISABLE_CLAUDE_CODE=1`, no subagents/skills/webfetch/websearch/external directories).
- **Providers & effort:** OpenAI-compatible provider config maps Maku's logical aliases (`agent-default`, `tier-light`, `tier-standard`, `tier-frontier`, `kimi-frontier`, `mistral-standard`, `mistral-light`) with `low`/`medium`/`high` effort variants (`reasoning_effort`). Internal runs set `compatibility.cacheFormat: "anthropic"`; public runs omit it.
- **Rotating MCP credentials:** a loopback credential proxy re-reads `MCP_TOKEN_FILE` on every call, strips redirects, and forwards W3C `traceparent`.
- **Loop guard & search:** Maku's own `loop_guard` detects stuck sessions and interrupts owned shell jobs; bounded synchronous search uses an independent read-only `search` session.
- **Metadata-only tracing:** `OTEL_*` variables are stripped from the OpenCode server environment so OpenCode never exports native OTLP logs or prompt/command payloads; Maku emits metadata-only model and tool spans under the run span.

## Rollback

To stop new OpenCode runs immediately:
- Remove `evaluation.treatmentHarness` and `defaults.harness` (or set them to `openhands`).
- Unset `runController.images.opencode.tag` in the `maku` chart values to remove `maku-opencode` from the run-pod admission allow-list.
- Rescue any escalated OpenCode task with `/factory retry harness=openhands`.

## Offline parity verification

Run the full offline suite against the real pinned OpenCode 2.3.0 binary:

```bash
# Image unit and real-server suites
docker build --target test -f images/maku-opencode/Dockerfile .
docker build --target test images/maku-harness

# Full role x provider x capability matrix (12 required capabilities, zero skips)
python3 images/maku-opencode/tests/contract-matrix.py

# Readiness distribution, recovery, and matrix under gVisor (set DOCKER_RUNTIME='' on hosts without runsc)
images/maku-opencode/tests/runsc-contract.sh

# Repository static and Go gates
task check
```
