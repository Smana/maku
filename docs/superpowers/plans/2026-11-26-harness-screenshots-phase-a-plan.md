# Harness Visual Verification (Screenshots), Phase A: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An `implementer` or `tester` run of a Repository that opts in gets a loopback-only headless browser (Playwright MCP in a `maku-browser` native sidecar). The agent can look at the dev server it started and check its own visual fix, with the screenshot read by the run's own vision-capable model.

**Architecture:** The factory freezes `Repository.spec.screenshots` onto the task at triage. It stamps `AgentRun.spec.screenshots` on implementer and tester runs only. The run-controller adds a `browser` native sidecar (an init container with `restartPolicy: Always`, the same shape as `room-bridge`) to those pods. The sidecar runs `@playwright/mcp` on `127.0.0.1:9224` behind a small Go gate on `127.0.0.1:9223`. The gate is the one place that filters tools, checks the URL, counts screenshots and caps image size. The harness (OpenCode or OpenHands) adds a `browser` MCP server that points at the gate, and turns on image input only when the run's alias is on the controller's vision list.

**Tech Stack:** Go 1.27 (controller, gate), controller-gen CRDs, Helm (charts `maku`, `agent-factory`, `agent-factory-crds`), Python 3 harness drivers (OpenCode 2.4.1, OpenHands agent-server 1.56.0), Node + `@playwright/mcp` + Chromium headless shell, gVisor `runsc`, agentgateway 1.6/1.7.

**Spec:** [`ai-native-ref/docs/superpowers/specs/2026-11-23-v5-harness-screenshots-design.md`](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-23-v5-harness-screenshots-design.md) (decisions VS1–VS7) and v5 roadmap row Q16 ([`2026-11-22-v5-roadmap-design.md`](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-22-v5-roadmap-design.md)). Read both. Where this plan departs from the spec, [Spec vs code](#spec-vs-code) says why, with file:line.

Grounded on Maku `origin/main` `a26d990c` and ANR `origin/main` `0a110c6` (2026-11-26).

## Global Constraints

- Opt-in is per `Repository` only (spec §7). It is never set by an issue body, a label or `POST /v1/runs`. Default: off.
- Allowed roles: `implementer` and `tester` only (VS5). The run-controller refuses `spec.screenshots.enabled` on any other role.
- The browser reaches loopback only: `http://127.0.0.1:*`, `http://localhost:*`, `http://[::1]:*` (VS3). It never reaches `file:`, `data:`, `chrome:` or any other host.
- MCP listens on `127.0.0.1:9223` (gate). Playwright MCP listens on `127.0.0.1:9224` (upstream), never `0.0.0.0` (spec §5.1).
- Disabled tools: `browser_evaluate`, `browser_run_code`, `browser_file_upload`, `browser_pdf_save`, `browser_install` (spec §5.1).
- Caps: `maxScreenshots` defaults to **10**, with a hard ceiling of **25**. Viewport is `1280×800` by default, with a maximum of `1920×1080`. Each image is at most **512 KiB** (spec §5.3).
- Chromium flags: `--no-sandbox --disable-setuid-sandbox --disable-dev-shm-usage --disable-gpu --disable-software-rasterizer --disable-extensions --disable-sync --mute-audio`, with `tini` as PID 1 (VS6).
- Sidecar resources default to `requests: {cpu: 250m, memory: 512Mi}` and `limits: {cpu: "2", memory: 2Gi}`. `/tmp` is an `emptyDir` of `512Mi`, and the cache is `128Mi` (spec §4.1).
- The sidecar gets no `/workspace` mount, no Secret, no projected token, no `HTTPS_PROXY` and no `env.valueFrom` (spec §4.1, AC4).
- Images are pinned by digest and multi-arch (`linux/amd64`, `linux/arm64`). Playwright never downloads at runtime (`PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1`).
- The `maku-opencode` and `maku-harness` image sizes grow by 0 bytes (AC1). Python goes into `images/maku-harness/browser_tools.py`, which both images already COPY from.
- Platform constitution: restricted securityContext, `readOnlyRootFilesystem`, `drop: [ALL]`, `RuntimeDefault` seccomp, requests and limits, and probes on long-running containers.
- Commits are conventional, in English, with no trailers.

---

## Spec vs code {#spec-vs-code}

The code contradicts the spec, or the spec cannot be built as written, in these places. Each row says what the plan builds instead.

| # | Spec says | Code says | Plan |
|---|---|---|---|
| C1 | §5.2 and §8: the browser gets "zero egress" and the SSRF residual is "None": "outbound packets from `maku-browser` are dropped by the CNI". | Network policy is per pod, not per container. The CNP selects the pod by `agents.ogenki.io/run-id` (`internal/runctl/cnp.go:91-94`). The NetworkPolicy does the same (`internal/runctl/netpol.go`). All containers share one netns, and under gVisor they share one netstack. In `kubernetes` mode (ANR), only the harness gets `HTTPS_PROXY` (`internal/runctl/pod.go:333-336`), but the pod may still dial the egress proxy, the broker and the in-cluster gateway. In Cilium mode it may dial the FQDN allow-list directly (`cnp.go:60-61`). | Chromium is pointed at a dead proxy for every non-loopback host (`--proxy-server=http://127.0.0.1:9` with a loopback bypass list) and gets `--host-resolver-rules` that send all names except localhost to NOTFOUND. The gate refuses a non-loopback `browser_navigate`. The residual is restated honestly: a renderer exploit can reach what the pod reaches, and that is the harness's own trust domain (no credential lives in the sidecar). The spec §8 row needs amending (ANR task A5). |
| C2 | §5.2 layer 1: an "MCP / Chromium origin gate". | Playwright MCP's own docs say `--allowed-origins` "does *not* serve as a security boundary and does *not* affect redirects". | `allowedOrigins` is kept as hygiene only. The enforcing layers are the gate's URL check (agent-requested navigation) and the Chromium proxy and resolver flags (page-initiated requests and redirects). |
| C3 | §4.2: the VAP admits `maku-browser` only "for an `AgentRun` with `spec.screenshots.enabled: true`". | The run-pod VAP matches `pods` only (`charts/maku/templates/run-pod-policy.yaml:30-39`). CEL cannot read the AgentRun. | The VAP keys on the pod's `agents.ogenki.io/role` label (`internal/runctl/objects.go:17-26`), which only the run-controller may set (`run-pod-policy.yaml:54`). It also checks the `browser` container's shape. |
| C4 | §6.1: "Each alias in `maku-gateway`'s model catalog declares `capabilities.vision`." | No such field exists. The catalog is a price table (`charts/maku-gateway/templates/catalog.yaml:1-4`), and aliases are `{provider, model}` (`charts/maku-gateway/values.yaml:81-85`). | The vision list sits beside the aliases the run-controller already reads: `runController.model.gateway.vision.{public,internal}` (`charts/maku/values.yaml:309`, `internal/runctl/config.go:184-211`). The maku-gateway chart is unchanged. |
| C5 | §6.2: a "dedicated `visionAlias`" may stand in for a text-only model. | The screenshot comes back as the MCP tool result inside the run's own conversation, so the run's own model reads it. A second alias would need a describe-the-image sub-call, which the spec does not design. | Phase A needs the run's own alias to be vision-capable. Otherwise the fail-closed fallback applies (§6.3: snapshot only). A vision delegate is listed under Phase B or later. |
| C6 | §4.1: a "sidecar container". | Run status waits on the `harness` container (`internal/runctl/status.go:85-91`). A second regular container would keep the pod `Running` after the harness exits (`restartPolicy: Never`, `pod.go:343`). | `browser` is a native sidecar (an init container with `restartPolicy: Always`), like `room-bridge` (`pod.go:296-305`). The kubelet stops it when the harness exits, and restarts it after an OOM (which helps AC5). |
| C7 | §5.1: the disabled tools are "explicitly disabled". | Playwright MCP has `--caps` (vision, pdf, devtools), but no flag that denies a single tool. | The gate filters `tools/list` and refuses `tools/call` for anything off the allow-list. |
| C8 | §5.3 bullet 4: an "ephemeral vision window" that rewrites old image blocks during condensation. | OpenCode runs with `plugins: []` (`images/maku-opencode/role_config.py:196`) and has no history hook. The OpenHands condenser summarises but does not strip images (`images/maku-harness/openhands_driver.py:121-126`). In both, a past image is part of the cached prefix and is billed at cache-read rates. | Deferred. The [Cost](#cost-and-size) section shows a capped run adds at most about $0.20. AC6 is measured in the drill. If prompt-cache read share drops by more than 5 points, a follow-up builds the window. |
| C9 | §4.3: PNGs are written to `/workspace/.maku-screenshots/`, git-excluded and refused by the push checker. | The sidecar has no `/workspace` (by design, §4.1), and the image comes back inline in the MCP result. Nothing writes a PNG into the workspace. | Phase A writes no PNG into `/workspace`. Playwright's `outputDir` is the sidecar's `/tmp`. The `.git/info/exclude` and push-checker work is dropped (YAGNI) and recorded for Phase B. |
| C10 | §7: per-Repository `memoryLimit`. | Run sizes are chart-level (`pod.go:52-58`). Per-repository resources have no precedent. | Resources live in the chart only (`runController.browser.resources`). `memoryLimit` is left out of the CRD in Phase A. |
| C11 | §1: "working identically across … OpenCode, OpenHands". | OpenCode is still per-issue opt-in on ANR (`ai-native-ref/infrastructure/base/room-broker/helmrelease.yaml:81-87`). Default runs use OpenHands. | Both drivers are wired (Task 10). The drill runs once per harness. |

---

## Open questions that block building

Each question has a recommendation. **Task 1 answers BQ1–BQ3 offline.** Building past Task 1 waits on its ledger.

| # | Question | Why it blocks | Recommendation |
|---|---|---|---|
| **BQ1** | Does an image returned by an MCP tool reach the model through each harness? The gateway speaks Chat Completions, where a `role: tool` message is text-only in OpenAI's schema. | If OpenCode (`@ai-sdk/openai-compatible`) or OpenHands (`openai/<alias>` through LiteLLM) drops the image or turns it into text, Phase A shows the model nothing. | Answer it in Task 1 against a fake OpenAI-compatible server that records the request body. If a harness puts the image in the tool message, check that agentgateway passes it through (Task 2). If a harness drops it, fix it in the driver: OpenHands through `capability_overrides` / `supports_vision`, OpenCode through the model entry's `modalities.input: [text, image]`. Only if neither works, drop that harness from Phase A, and do not fork it. |
| **BQ2** | Which ANR aliases accept images? | This decides the drill's model and the `vision` list. | Expected (to confirm with one owner-run curl per model, under $0.01): **yes** for `claude-{opus,sonnet,haiku}-5-5` (internal) and very likely for `mistral-medium-2604` and `mistral-small-2603`. **Unknown** for `k3.1-pro` on the Kimi Code endpoint. **Likely no** for `glm-5.3*` (Z.ai ships vision as separate `…V` models). Public `agent-default` is therefore probably text-only. Run the ANR website drill on `mistral-standard` (public) and on `agent-default` internal. Do not add a GLM-V alias in Phase A. |
| **BQ3** | Does Chromium headless shell plus Playwright MCP start and screenshot under `runsc` with a read-only root, uid 10001, `/tmp` emptyDir and `drop: ALL`? | VS6 assumes it does. GKE Sandbox and the CI runsc build may differ (systrap vs ptrace). | Task 1 proves it in CI's runsc (the `harness-checks` job pattern, `.github/workflows/ci.yaml:191-237`). gcp-0 GKE Sandbox is proven in the drill. Keep `--no-zygote` off by default and documented. |
| **BQ4** | Native sidecar or regular container? | Changes pod shape, VAP and lifecycle. | Native sidecar (C6). Needs Kubernetes 1.29+, which both hosts already run for `room-bridge`. |
| **BQ5** | Where does the gate live? | Tool filtering and caps need one enforcement point that works for OpenCode, OpenHands and BYO. | A Go binary in the `maku-browser` image (`cmd/maku-browser-gate`, `internal/browsergate`). It is harness-agnostic, tested with `go test`, and does not grow the harness images. |
| **BQ6** | Drop the `/workspace/.maku-screenshots/` copies and the ephemeral window (C8, C9)? | Scope. | Yes for Phase A. Owner to confirm. |
| **BQ7** | Should a Repository with screenshots on, but no vision-capable alias, be refused at admission? | §6.2 asks for a preflight. The factory does not know the run-controller's vision list. | No. The run-controller decides per run (`SCREENSHOTS_VISION=0|1`). The fallback (snapshot only) is fail-closed, and the room gets one bounded note. |

---

## File structure

| Path | Change | Responsibility |
|---|---|---|
| `api/v1alpha1/agentrun_types.go` | modify | `AgentRunScreenshots{Enabled, MaxScreenshots}` on `AgentRunSpec` |
| `api/factory/v1alpha1/factoryconfig_types.go` | modify | `RepositorySpec.Screenshots` |
| `api/factory/v1alpha1/task_types.go` | modify | `ProfileStatus.Screenshots` (frozen at triage) and `RunProvenance.Screenshots` |
| `internal/factory/config/screenshots.go` | create | `config.Screenshots`, `ScreenshotsFor(role)`, defaults and ceiling |
| `internal/factory/config/config.go` | modify | `Repository.Screenshots` |
| `internal/factory/resources/assemble.go:407` | modify | carry the `screenshots` key |
| `internal/factory/reconciler/{reconciler.go,implement.go,tester.go}` | modify | freeze at triage, then stamp implementer and tester specs |
| `internal/factory/runs/runs.go` | modify | `Spec.Screenshots` → `spec.screenshots` |
| `internal/runctl/config.go` | modify | `Images.Browser`, `Browser.Resources`, `Gateway.Vision`, `Invalid` rules |
| `internal/runctl/browser.go` | create | `browserSidecar(...)`, `visionFor(...)` |
| `internal/runctl/pod.go` | modify | append the sidecar and the harness env |
| `internal/browsergate/{policy.go,image.go,proxy.go}` | create | the tool allow-list, URL check, counter, image cap, JSON and SSE proxy |
| `cmd/maku-browser-gate/main.go` | create | the gate binary |
| `images/maku-browser/{Dockerfile,package.json,package-lock.json,config.json,entrypoint.sh,tests/runsc-contract.sh}` | create | the sidecar image and its gVisor contract |
| `images/maku-harness/browser_tools.py` | create | `browser_mcp(env)`, `browser_rules(env)`, `vision_on(env)` shared by both drivers |
| `images/maku-opencode/role_config.py`, `opencode_driver.py`, `Dockerfile` | modify | the `browser` MCP server, model modalities, rules |
| `images/maku-harness/openhands_driver.py`, `Dockerfile` | modify | `mcp_config.browser`, vision override, rules |
| `charts/maku/{values.yaml,values.schema.json,templates/run-controller-config.yaml,templates/run-pod-policy.yaml}` | modify | image, resources, vision list, VAP |
| `charts/agent-factory/{values.yaml,values.schema.json}` | modify | pass-through and a pinned default |
| `charts/maku-gateway/tests/probe/{client.py,echo-llm.py,probe.sh}` | modify | P7: image parts survive the presets |
| `.github/workflows/{ci.yaml,release.yaml,image.yaml}` | modify | build, runsc contract, release `maku-browser` |
| `docs/browser-verification.md`, `docs/onboard-a-repository.md`, `docs/opencode-harness.md` | create/modify | operator docs |

---

## Task 1: Offline spike (BQ1, BQ3). No production code.

**Files:**
- Create: `docs/superpowers/plans/2026-11-26-harness-screenshots-ledger.md` (the results, kept as the recovery map)
- Create (scratch, not committed): `$SCRATCH/spike/` holding a capture server and a throwaway Dockerfile

**Interfaces:**
- Produces: the ledger rows **L1** (OpenCode image path), **L2** (OpenHands image path), **L3** (Chromium and Playwright MCP under runsc), **L4** (the exact OpenCode V2 model-entry key for image input), **L5** (the `@playwright/mcp` version and its tool names). Tasks 7–10 read them.

- [ ] **Step 1: Write the capture server.** `capture.py` is an OpenAI-compatible `/v1/chat/completions` server. It writes each request body to `req-N.json`. On request 1 it answers with a tool call `browser_take_screenshot({})`, and after that it answers `stop`. Write a second stub MCP server, `stub_mcp.py`. It is streamable HTTP on `127.0.0.1:9223/mcp` and answers `tools/list` with one tool, `browser_take_screenshot`, and `tools/call` with `[{"type":"text","text":"ok"},{"type":"image","mimeType":"image/png","data":<1x1 png b64>}]`.
- [ ] **Step 2: Run OpenCode 2.4.1** (`ghcr.io/anomalyco/opencode:2.4.1@sha256:10e340e3…`, the pin in `images/maku-opencode/Dockerfile:4`). Use a config generated by `role_config.role_config(...)` plus `mcp.servers.browser = {type: remote, url: http://127.0.0.1:9223/mcp}`, and try it with and without a `modalities` / `attachment` key on the model entry. Inspect `req-2.json`. Record in **L1** where the image landed (the tool message's `content[]`, a user message, a text placeholder, or nowhere), and in **L4** which key turned it on.
- [ ] **Step 3: Run OpenHands.** Build the `maku-harness` test stage and call `openhands_driver.build_request` with `mcp_config.browser`. Point it at the capture server and run one turn. Repeat with `capability_overrides={"supports_vision": True}` (or the SDK's actual field, read from `openhands/sdk/llm/llm.py` in the image). Record the outcome in **L2**.
- [ ] **Step 4: Run Chromium under runsc.** Use a throwaway Dockerfile: `node:22-bookworm-slim` plus `npx playwright install --with-deps --only-shell chromium` plus `@playwright/mcp@<latest>`, started with the flags in Global Constraints. Run it with `docker run --runtime=runsc --read-only --user 10001:10001 --tmpfs /tmp:size=512m --cap-drop ALL --network none`, then navigate to a `python3 -m http.server` bound to `127.0.0.1` in the same container, and take a screenshot. Record in **L3** the cold-start time, peak RSS (`/sys/fs/cgroup/memory.peak`), compressed and unpacked image size, and any syscall warnings in the runsc debug log.
- [ ] **Step 5: Record L5.** Write down the `@playwright/mcp` version and its `tools/list` names, and confirm that `browser_run_code` and `browser_evaluate` exist and must be filtered.
- [ ] **Step 6: Decide and commit.** If L1 or L2 shows the image dropped and no config key turns it on, stop and ask the owner (BQ1). Otherwise commit the ledger.

```bash
git add docs/superpowers/plans/2026-11-26-harness-screenshots-ledger.md
git commit -m "docs(plans): record the screenshots phase A spike results"
```

## Task 2: Gateway probe P7: image parts survive each preset

**Files:**
- Modify: `charts/maku-gateway/tests/probe/client.py` (an `IMAGE` message mode)
- Modify: `charts/maku-gateway/tests/probe/echo-llm.py` (report image parts per message)
- Modify: `charts/maku-gateway/tests/probe/probe.sh` (section P7)

**Interfaces:**
- Consumes: L1 and L2 (which message role carries the image).
- Produces: proof that agentgateway forwards `image_url` parts unchanged on the `zai`, `mistral` and `moonshot` presets. Anthropic's native translation is proven live in the drill (step D2).

- [ ] **Step 1: Add the failing assertions.** In `echo-llm.py`, add to `report`:

```python
"images": [
    {"role": m.get("role"), "n": sum(1 for p in (m.get("content") or []) if isinstance(p, dict) and p.get("type") == "image_url")}
    for m in req.get("messages", []) if isinstance(m.get("content"), list)
],
```

In `client.py`, when `message == "IMAGE"`, send a user message and a tool message, each with `[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]` (the tool message needs a preceding assistant `tool_calls` entry). In `probe.sh`, after P4:

```bash
echo "==> P7: image parts"
for alias in tier-light mistral-light kimi-frontier; do
  r="$(report "$(call "$PUB/m/$alias/v1/chat/completions" /tokens/public-$alias tier-light IMAGE)")"
  jq -e '[.images[] | select(.n == 1)] | length == 2' <<<"$r" >/dev/null \
    && pass "P7 $alias: the user and tool messages each reach the provider with their image part" || fail "P7 $alias: $r"
done
```

- [ ] **Step 2: Run the probe, expect P7 to fail** before `echo-llm.py` reports images: `charts/maku-gateway/tests/probe/probe.sh` (needs Docker and k3d. Docker is off by default on the laptop: start it for this run.)
- [ ] **Step 3: Finish echo-llm and client as above. Rerun and expect P7 to pass.** If a preset's transformation drops list content (the Mistral `reasoning_effort` note in `_presets.tpl` is a precedent), record it in the ledger as **L6** and stop for the owner.
- [ ] **Step 4: Commit**

```bash
git add charts/maku-gateway/tests/probe
git commit -m "test(gateway): probe P7 proves image parts pass every preset"
```

## Task 3: API: `spec.screenshots` on Repository and AgentRun

**Files:**
- Modify: `api/v1alpha1/agentrun_types.go` (after `Search`, line 129)
- Modify: `api/factory/v1alpha1/factoryconfig_types.go:158-191`
- Modify: `api/factory/v1alpha1/task_types.go` (`ProfileStatus`, `RunProvenance`)
- Create: `internal/factory/config/screenshots.go`, `internal/factory/config/screenshots_test.go`
- Modify: `internal/factory/config/config.go:404-428`, `internal/factory/resources/assemble.go:407`
- Test: `api/v1alpha1/agentrun_crd_test.go`, `internal/factory/resources/assemble_test.go`
- Generated: `config/crd/*.yaml`, `charts/agent-factory-crds/files/*`, `zz_generated.deepcopy.go` (`task crd:gen`)

**Interfaces:**
- Produces:
  - `v1alpha1.AgentRunScreenshots{Enabled bool "enabled"; MaxScreenshots int32 "maxScreenshots,omitempty"}`, and `AgentRunSpec.Screenshots *AgentRunScreenshots "screenshots,omitempty"`
  - `config.Screenshots{Enabled bool; Roles []string; MaxScreenshots int32}`, `const DefaultMaxScreenshots = 10`, `const MaxScreenshotsCeiling = 25`
  - `func (s *Screenshots) For(role string) (max int32, ok bool)`
  - `ProfileStatus.Screenshots *config.Screenshots`, `RunProvenance.Screenshots bool "screenshots,omitempty"`

- [ ] **Step 1: Write the failing tests**

```go
// internal/factory/config/screenshots_test.go
func TestScreenshotsFor(t *testing.T) {
	var none *Screenshots
	if _, ok := none.For("implementer"); ok {
		t.Fatal("nil screenshots is off")
	}
	s := &Screenshots{Enabled: true}
	for role, want := range map[string]bool{"implementer": true, "tester": true, "reviewer": false, "triager": false, "scoper": false} {
		if _, ok := s.For(role); ok != want {
			t.Errorf("%s: got %v", role, ok)
		}
	}
	if m, _ := s.For("tester"); m != DefaultMaxScreenshots {
		t.Errorf("default max %d", m)
	}
	s = &Screenshots{Enabled: true, Roles: []string{"tester"}, MaxScreenshots: 99}
	if _, ok := s.For("implementer"); ok {
		t.Error("roles narrows the set")
	}
	if m, _ := s.For("tester"); m != MaxScreenshotsCeiling {
		t.Errorf("ceiling: %d", m)
	}
}
```

In `agentrun_crd_test.go`, add a case asserting the CRD schema has `spec.screenshots.maxScreenshots` with `minimum: 1, maximum: 25`. In `assemble_test.go`, copy `TestAssembleCarriesPrivateCode` (line 394) to `TestAssembleCarriesScreenshots`, asserting `RepositoryFor("Smana/maku").Screenshots.Enabled`.

- [ ] **Step 2: Run, expect FAIL** (`undefined: Screenshots`):
`go test ./internal/factory/config/ -run TestScreenshotsFor ./internal/factory/resources/ -run Screenshots ./api/...`
- [ ] **Step 3: Implement**

```go
// internal/factory/config/screenshots.go
// SPDX-License-Identifier: Apache-2.0

package config

import "slices"

const (
	DefaultMaxScreenshots int32 = 10
	MaxScreenshotsCeiling int32 = 25
)

// screenshotRoles are the roles a browser may serve in phase A (VS5).
var screenshotRoles = []string{"implementer", "tester"}

// Screenshots is a Repository's opt-in to the browser sidecar (v5 Q16, phase A).
type Screenshots struct {
	Enabled bool `json:"enabled,omitempty"`
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:Enum=implementer;tester
	Roles []string `json:"roles,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=25
	MaxScreenshots int32 `json:"maxScreenshots,omitempty"`
}

// For is the run's screenshot cap and whether role gets the browser.
func (s *Screenshots) For(role string) (int32, bool) {
	if s == nil || !s.Enabled || !slices.Contains(screenshotRoles, role) {
		return 0, false
	}
	if len(s.Roles) > 0 && !slices.Contains(s.Roles, role) {
		return 0, false
	}
	n := s.MaxScreenshots
	if n <= 0 {
		n = DefaultMaxScreenshots
	}
	return min(n, MaxScreenshotsCeiling), true
}
```

Add `Screenshots *config.Screenshots \`json:"screenshots,omitempty"\`` to `RepositorySpec` and to `config.Repository`. Append `"screenshots"` to the key list at `assemble.go:407`. Add `AgentRunScreenshots` with `+kubebuilder:validation:Minimum=1` and `Maximum=25` on `MaxScreenshots`. Add `Screenshots *config.Screenshots` to `ProfileStatus`, and `Screenshots bool` to `RunProvenance`.

- [ ] **Step 4: Regenerate and run, expect PASS:** `task crd:gen && go test ./internal/factory/config/ ./internal/factory/resources/ ./api/... && task crd:check`
- [ ] **Step 5: Commit**

```bash
git add api internal/factory/config internal/factory/resources config/crd charts/agent-factory-crds
git commit -m "feat(api): add the screenshots opt-in to Repository and AgentRun"
```

## Task 4: Factory: freeze at triage, stamp implementer and tester runs

**Files:**
- Modify: `internal/factory/reconciler/reconciler.go:1343-1346` (`ProfileStatus`)
- Modify: `internal/factory/reconciler/implement.go:305-316`, `internal/factory/reconciler/tester.go:67-73`
- Modify: `internal/factory/runs/runs.go:138-150` (`Spec.Screenshots`), `:280` (spec map)
- Modify: `internal/factory/reconciler/implement.go:160,362` (provenance)
- Test: `internal/factory/runs/runs_test.go`, `internal/factory/reconciler/screenshots_test.go` (create)

**Interfaces:**
- Consumes: `config.Screenshots.For(role)` (Task 3).
- Produces: `runs.Spec.Screenshots int32`. 0 means off. A value above 0 renders `spec.screenshots: {enabled: true, maxScreenshots: N}`.

- [ ] **Step 1: Write the failing tests.** In `runs_test.go`: `Spec{Screenshots: 10}` renders `spec.screenshots.enabled == true`, `maxScreenshots == 10`, and `Spec{}` renders no `screenshots` key. In `reconciler/screenshots_test.go`, use the existing fake-reconciler helpers from `harness_test.go`:

```go
func TestScreenshotsRideImplementerAndTesterOnly(t *testing.T) {
	cfg := cfgWithRepo(t, func(rc *config.Repository) { rc.Screenshots = &config.Screenshots{Enabled: true, MaxScreenshots: 7} })
	task := triagedTask(t, cfg) // ProfileStatus frozen
	if task.Status.Profile.Screenshots == nil || !task.Status.Profile.Screenshots.Enabled {
		t.Fatal("triage freezes the repository's screenshots")
	}
	r := newTestReconciler(t)
	if s := r.implementerSpec(cfg, task, "x"); s.Screenshots != 7 {
		t.Errorf("implementer: %d", s.Screenshots)
	}
	if s := r.testerSpec(cfg, task, "x"); s.Screenshots != 7 {
		t.Errorf("tester: %d", s.Screenshots)
	}
	rv := r.implementerSpec(cfg, task, "x")
	rv.Role = "reviewer"
	stampScreenshots(task, &rv)
	if rv.Screenshots != 0 {
		t.Error("reviewer never gets the browser")
	}
}

func TestScreenshotsFrozenAtTriage(t *testing.T) {
	cfg := cfgWithRepo(t, func(rc *config.Repository) { rc.Screenshots = &config.Screenshots{Enabled: true} })
	task := triagedTask(t, cfg)
	cfg.RepositoryFor(task.Spec.Repository).Screenshots.Enabled = false // a later config change
	if s := newTestReconciler(t).implementerSpec(cfg, task, "x"); s.Screenshots != 10 {
		t.Error("a run reads the task's frozen value, not today's config")
	}
}
```

(If `cfgWithRepo` and `triagedTask` are not already in the package's test helpers, write them in this file on top of the existing `harness_test.go` fixtures.)

- [ ] **Step 2: Run, expect FAIL:** `go test ./internal/factory/runs/ ./internal/factory/reconciler/ -run Screenshots`
- [ ] **Step 3: Implement.** In `ProfileStatus` construction add `Screenshots: rc.Screenshots.DeepCopy()` (`rc := r.repo(cfg, t)`; it is nil-safe). Add `stampScreenshots`, and call it in `startRun` next to `s.Harness = harnessFor(t, s)` (`implement.go:315`). `startRun` serves both implementer and tester, so a single call site covers both:

```go
// stampScreenshots gives an implementer or tester run the task's frozen browser opt-in (v5 Q16).
func stampScreenshots(t *v1alpha1.Task, s *runs.Spec) {
	s.Screenshots = 0
	if p := t.Status.Profile; p != nil && t.Spec.Kind != "integration" {
		if n, ok := p.Screenshots.For(s.Role); ok {
			s.Screenshots = n
		}
	}
}
```

In `runs.go`: `if s.Screenshots > 0 { spec["screenshots"] = map[string]any{"enabled": true, "maxScreenshots": int64(s.Screenshots)} }`. In provenance: `Screenshots: s.Screenshots > 0`.
- [ ] **Step 4: Run, expect PASS** (same command).
- [ ] **Step 5: Commit**

```bash
git add internal/factory
git commit -m "feat(factory): freeze the screenshots opt-in at triage and stamp implementer and tester runs"
```

## Task 5: Run-controller config: browser image, resources, vision list, refusals

**Files:**
- Modify: `internal/runctl/config.go:174-211` (`Gateway.Vision`), `:340-349` (`Images.Browser`), `Config.Browser`, `Invalid` (`:516-545`), load-time validation
- Test: `internal/runctl/config_test.go`

**Interfaces:**
- Produces:
  - `Images.Browser string "browser,omitempty"`
  - `Config.Browser BrowserConfig{Resources corev1.ResourceRequirements "resources,omitzero"}`, defaulted at load to the Global Constraints values
  - `Gateway.Vision GatewayAliases "vision,omitzero"`
  - `func (c Config) VisionFor(r *v1alpha1.AgentRun, alias string) bool`

- [ ] **Step 1: Write the failing tests**

```go
func TestScreenshotsRefusals(t *testing.T) {
	on := func(r *v1alpha1.AgentRun) { r.Spec.Screenshots = &v1alpha1.AgentRunScreenshots{Enabled: true, MaxScreenshots: 10} }
	c := testConfig(t)
	if why := c.Invalid(run("r1", on)); !strings.Contains(why, "images.browser") {
		t.Errorf("no browser image: %q", why)
	}
	c.Images.Browser = "ghcr.io/smana/maku-browser:v0.60.0@sha256:" + strings.Repeat("a", 64)
	if why := c.Invalid(run("r1", on)); why != "" {
		t.Errorf("implementer: %q", why)
	}
	if why := c.Invalid(run("r1", on, func(r *v1alpha1.AgentRun) { r.Spec.Role = "reviewer" })); !strings.Contains(why, "implementer or tester") {
		t.Errorf("reviewer: %q", why)
	}
}

func TestVisionFor(t *testing.T) {
	c := testConfig(t)
	c.Model.Mode = ModelModeGateway
	c.Model.Gateway.Vision = GatewayAliases{Public: []string{"mistral-standard"}, Internal: []string{"agent-default"}}
	pub := run("r1")
	if !c.VisionFor(pub, "mistral-standard") || c.VisionFor(pub, "agent-default") {
		t.Error("public list")
	}
	in := run("r1", func(r *v1alpha1.AgentRun) { r.Spec.DataClass = "internal" })
	if !c.VisionFor(in, "agent-default") {
		t.Error("internal list")
	}
}

func TestVisionAliasMustBeServed(t *testing.T) {
	_, err := cfgFrom(t, goodGatewayConfig+"\n      vision: {public: [not-served]}\n")
	if err == nil || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("a vision alias the listener does not serve is refused at load: %v", err)
	}
}
```

(`goodGatewayConfig` is the existing gateway fixture in `config_test.go` or `gateway_test.go`. Indent the appended line under `model.gateway`.)
- [ ] **Step 2: Run, expect FAIL:** `go test ./internal/runctl/ -run 'Screenshots|Vision'`
- [ ] **Step 3: Implement.** Add to `Invalid`, before the gateway block:

```go
	case r.Spec.Screenshots != nil && r.Spec.Screenshots.Enabled && r.Spec.Role != "implementer" && r.Spec.Role != "tester":
		return "spec.screenshots is for an implementer or tester run only (v5 Q16, VS5)"
	case r.Spec.Screenshots != nil && r.Spec.Screenshots.Enabled && c.Images.Browser == "":
		return "spec.screenshots needs images.browser configured"
```

`VisionFor` picks `Vision.Public` or `Vision.Internal` by `r.Spec.DataClass` and returns `slices.Contains`. In `model.mode secret` it returns false. At load, every `Vision.<class>` entry must be in `Aliases.<class>`, with the error message `model.gateway.vision.<class>: %q is not an alias the listener serves`.
- [ ] **Step 4: Run, expect PASS** (same command).
- [ ] **Step 5: Commit**

```bash
git add internal/runctl/config.go internal/runctl/config_test.go
git commit -m "feat(runctl): configure the browser image, its resources and the vision aliases"
```

## Task 6: Run pod: the `browser` native sidecar and the harness env

**Files:**
- Create: `internal/runctl/browser.go`
- Modify: `internal/runctl/pod.go:233-347`
- Test: `internal/runctl/render_test.go`, `internal/runctl/testdata/pod-browser.golden.yaml` (create with `-update`)

**Interfaces:**
- Consumes: `Images.Browser`, `Config.Browser.Resources`, `VisionFor` (Task 5).
- Produces: pod contract.
  - The init container `browser` has `restartPolicy: Always`, the image `c.Images.Browser`, and env `MAX_SCREENSHOTS=<n>` and `VISION=0|1` only.
  - Its mounts are `browser-tmp:/tmp` (emptyDir 512Mi) and `browser-cache:/home/browser/.cache` (emptyDir 128Mi) only.
  - Its probes are `startupProbe` and `readinessProbe` `httpGet 127.0.0.1:9223/healthz`, and `livenessProbe` the same with period 20s.
  - The harness gets `BROWSER_MCP_URL=http://127.0.0.1:9223/mcp` and `SCREENSHOTS_VISION=0|1`.
  - The constant is `BrowserGatePort = 9223`.

- [ ] **Step 1: Write the failing tests**

```go
func TestBrowserPodGolden(t *testing.T) {
	c := testConfig(t)
	c.Images.Browser = "ghcr.io/smana/maku-browser:v0.60.0"
	r := goRun(func(r *v1alpha1.AgentRun) { r.Spec.Screenshots = &v1alpha1.AgentRunScreenshots{Enabled: true, MaxScreenshots: 10} })
	golden(t, "pod-browser.golden.yaml", Pod(r, c, testModel, "abcdefgh"))
}

func TestBrowserSidecarHoldsNothing(t *testing.T) {
	c := testConfig(t)
	c.Images.Browser = "ghcr.io/smana/maku-browser:v0.60.0"
	p := Pod(goRun(func(r *v1alpha1.AgentRun) { r.Spec.Screenshots = &v1alpha1.AgentRunScreenshots{Enabled: true, MaxScreenshots: 10} }), c, testModel, "abcdefgh")
	var b *corev1.Container
	for i := range p.Spec.InitContainers {
		if p.Spec.InitContainers[i].Name == "browser" {
			b = &p.Spec.InitContainers[i]
		}
	}
	if b == nil || b.RestartPolicy == nil || *b.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("browser is a native sidecar")
	}
	for _, m := range b.VolumeMounts {
		if m.Name != "browser-tmp" && m.Name != "browser-cache" {
			t.Errorf("browser mounts %s", m.Name)
		}
	}
	for _, e := range b.Env {
		if e.ValueFrom != nil || strings.Contains(e.Name, "PROXY") || strings.Contains(e.Name, "TOKEN") {
			t.Errorf("browser env %s", e.Name)
		}
	}
	if !*b.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("read-only root")
	}
}

func TestNoBrowserWithoutOptIn(t *testing.T) {
	c := testConfig(t)
	c.Images.Browser = "ghcr.io/smana/maku-browser:v0.60.0"
	for _, ic := range Pod(goRun(), c, testModel, "abcdefgh").Spec.InitContainers {
		if ic.Name == "browser" {
			t.Fatal("AC1: no opt-in, no sidecar")
		}
	}
}
```

Also add a case for a run with `RoomRef == ""`: the sidecar is the only init container.
- [ ] **Step 2: Run, expect FAIL:** `go test ./internal/runctl/ -run Browser`
- [ ] **Step 3: Implement `browser.go`**

```go
// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// BrowserGatePort is the browser gate's loopback port: the harness's MCP server named browser.
const BrowserGatePort = 9223

// browserSidecar is the opt-in browser (v5 Q16, VS2): a native sidecar holding no workspace, no
// secret and no token, so the kubelet ends it with the harness and restarts it after an OOM.
func browserSidecar(r *v1alpha1.AgentRun, c Config, vision bool) (corev1.Container, []corev1.Volume) {
	always := corev1.ContainerRestartPolicyAlways
	health := corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Host: "127.0.0.1", Path: "/healthz", Port: intstr.FromInt32(BrowserGatePort)}}
	v := "0"
	if vision {
		v = "1"
	}
	ctr := corev1.Container{
		Name: "browser", Image: c.Images.Browser, RestartPolicy: &always,
		Env: []corev1.EnvVar{{Name: "MAX_SCREENSHOTS", Value: strconv.Itoa(int(r.Spec.Screenshots.MaxScreenshots))}, {Name: "VISION", Value: v}},
		StartupProbe:    &corev1.Probe{ProbeHandler: health, PeriodSeconds: 1, FailureThreshold: 60},
		ReadinessProbe:  &corev1.Probe{ProbeHandler: health, PeriodSeconds: 10},
		LivenessProbe:   &corev1.Probe{ProbeHandler: health, PeriodSeconds: 20, FailureThreshold: 3},
		Resources:       c.Browser.Resources,
		SecurityContext: containerSecurity(),
		VolumeMounts:    []corev1.VolumeMount{{Name: "browser-tmp", MountPath: "/tmp"}, {Name: "browser-cache", MountPath: "/home/browser/.cache"}},
	}
	return ctr, []corev1.Volume{emptyDir("browser-tmp", "512Mi", false), emptyDir("browser-cache", "128Mi", false)}
}

func browserEnv(vision bool) []corev1.EnvVar {
	v := "0"
	if vision {
		v = "1"
	}
	return []corev1.EnvVar{{Name: "BROWSER_MCP_URL", Value: fmt.Sprintf("http://127.0.0.1:%d/mcp", BrowserGatePort)}, {Name: "SCREENSHOTS_VISION", Value: v}}
}
```

In `Pod`, after the room block (`pod.go:327`) and before `om := meta(...)`:

```go
	if sc := s.Screenshots; sc != nil && sc.Enabled {
		vision := c.VisionFor(r, m.Name)
		ctr, vs := browserSidecar(r, c, vision)
		inits = append(inits, ctr)
		vols = append(vols, vs...)
		harness.Env = append(harness.Env, browserEnv(vision)...)
	}
```

The harness keeps its `HTTPS_PROXY` (`pod.go:333-336`). `NO_PROXY` already covers `127.0.0.1` through `proxyEnv`: verify this in the golden output. The sidecar gets no `proxyEnv`. `m.Name` is the alias the harness calls, which is also what `MODEL` carries (`pod.go:140`).
- [ ] **Step 4: Run, expect PASS** after `go test ./internal/runctl/ -run Browser -update` and a review of the golden diff. Then run `go test ./internal/runctl/` for the package.
- [ ] **Step 5: Commit**

```bash
git add internal/runctl
git commit -m "feat(runctl): add the opt-in browser native sidecar to implementer and tester pods"
```

## Task 7: Browser gate: policy, counter and image cap (pure functions)

**Files:**
- Create: `internal/browsergate/policy.go`, `internal/browsergate/policy_test.go`
- Create: `internal/browsergate/image.go`, `internal/browsergate/image_test.go`

**Interfaces:**
- Consumes: L5 (tool names).
- Produces:
  - `var Allowed = []string{"browser_navigate","browser_navigate_back","browser_snapshot","browser_take_screenshot","browser_click","browser_type","browser_select_option","browser_press_key","browser_resize","browser_wait_for","browser_console_messages","browser_network_requests","browser_tabs","browser_close"}`
  - `func FilterTools(tools []map[string]any, vision bool) []map[string]any`
  - `func CheckCall(name string, args map[string]any) error` (errors: `ErrToolNotAllowed`, `ErrURLRefused`, `ErrViewport`)
  - `type Counter struct{ Path string; Max int }` with `func (c *Counter) Take() (int, error)` (`ErrCapReached` reports `screenshot_cap_reached`; the count is persisted in a file under `/tmp` so it survives a sidecar restart)
  - `func CapImage(b []byte, mime string, limit int) ([]byte, string, error)` (re-encodes as JPEG q=70, halving dimensions until it is ≤ limit; `ErrTooLarge` after 3 passes)
  - `const ImageLimit = 512 << 10`

- [ ] **Step 1: Write the failing tests**

```go
func TestCheckCallURLs(t *testing.T) {
	ok := []string{"http://127.0.0.1:1313/", "http://localhost:5173/docs", "http://[::1]:3000"}
	bad := []string{"https://example.com", "http://169.254.169.254/latest", "http://room-broker.maku.svc:8443/",
		"file:///etc/passwd", "data:text/html,x", "chrome://settings", "http://127.0.0.1.nip.io/", "https://127.0.0.1:8443/",
		"http://localhost@evil.com/", "http://0x7f000001/", "http://127.1/"}
	for _, u := range ok {
		if err := CheckCall("browser_navigate", map[string]any{"url": u}); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := CheckCall("browser_navigate", map[string]any{"url": u}); !errors.Is(err, ErrURLRefused) {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestCheckCallTools(t *testing.T) {
	for _, n := range []string{"browser_evaluate", "browser_run_code", "browser_file_upload", "browser_pdf_save", "browser_install", "anything_new"} {
		if err := CheckCall(n, nil); !errors.Is(err, ErrToolNotAllowed) {
			t.Errorf("%s: %v", n, err)
		}
	}
	if err := CheckCall("browser_resize", map[string]any{"width": 1921.0, "height": 1080.0}); !errors.Is(err, ErrViewport) {
		t.Error("viewport ceiling 1920x1080")
	}
}

func TestFilterToolsDropsScreenshotWithoutVision(t *testing.T) {
	in := []map[string]any{{"name": "browser_snapshot"}, {"name": "browser_take_screenshot"}, {"name": "browser_evaluate"}}
	if got := names(FilterTools(in, true)); !slices.Equal(got, []string{"browser_snapshot", "browser_take_screenshot"}) {
		t.Error(got)
	}
	if got := names(FilterTools(in, false)); !slices.Equal(got, []string{"browser_snapshot"}) {
		t.Error(got)
	}
}

func TestCounterSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "count")
	c := &Counter{Path: p, Max: 2}
	c.Take()
	c2 := &Counter{Path: p, Max: 2} // a restarted sidecar
	if n, err := c2.Take(); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := c2.Take(); !errors.Is(err, ErrCapReached) {
		t.Fatal(err)
	}
}

func TestCapImage(t *testing.T) {
	big := noisePNG(t, 1920, 1080) // > 512 KiB of random pixels
	out, mime, err := CapImage(big, "image/png", ImageLimit)
	if err != nil || len(out) > ImageLimit || mime != "image/jpeg" {
		t.Fatal(len(out), mime, err)
	}
	small := noisePNG(t, 8, 8)
	if out, mime, _ := CapImage(small, "image/png", ImageLimit); !bytes.Equal(out, small) || mime != "image/png" {
		t.Error("under the limit: unchanged")
	}
}
```

- [ ] **Step 2: Run, expect FAIL:** `go test ./internal/browsergate/`
- [ ] **Step 3: Implement.**
  - **URL rule:** `url.Parse`. Require scheme `http`, `u.User == nil`, and `u.Hostname()` exactly in `{"127.0.0.1","localhost","::1"}`. That refuses `127.1`, `0x7f000001` and `*.nip.io`, because only literal matches pass.
  - **Counter:** read and write the decimal count in `Path` with `os.WriteFile(..., 0o600)` under a `sync.Mutex`.
  - **CapImage:** use `image.Decode` (register `image/png` and `image/jpeg`), `golang.org/x/image/draw.ApproxBiLinear` to halve, and `jpeg.Encode{Quality: 70}`. First check whether `golang.org/x/image` is already in `go.mod`. If it is not, use the standard library's nearest-neighbour halving written inline (8 lines), so no dependency is added.
- [ ] **Step 4: Run, expect PASS** (same command).
- [ ] **Step 5: Commit**

```bash
git add internal/browsergate
git commit -m "feat(browsergate): the browser's tool allow-list, loopback URL check, screenshot cap and image cap"
```

## Task 8: Browser gate: the MCP proxy and the binary

**Files:**
- Create: `internal/browsergate/proxy.go`, `internal/browsergate/proxy_test.go`
- Create: `cmd/maku-browser-gate/main.go`

**Interfaces:**
- Consumes: Task 7.
- Produces:
  - `func NewProxy(upstream string, counter *Counter, vision bool) http.Handler`, serving `/mcp` (POST, GET, DELETE) and `/healthz`
  - the binary `maku-browser-gate -listen 127.0.0.1:9223 -upstream http://127.0.0.1:9224/mcp -count /tmp/gate/count`, which reads `MAX_SCREENSHOTS` and `VISION`
  - a stdout line per refused or capped call: `maku-browser-gate: refused tool=<n> reason=<code>`, and `maku-browser-gate: screenshot n=<k>/<max> bytes=<b>`

- [ ] **Step 1: Write the failing tests** against an `httptest` fake upstream that answers `tools/list` with seven tools, including `browser_evaluate`. It answers `tools/call browser_take_screenshot` with a 900 KiB PNG, once as `application/json` and once as `text/event-stream` (`event: message\ndata: {...}\n\n`).

```go
func TestProxyFiltersListAndRefusesCalls(t *testing.T) {
	up := fakeUpstream(t)
	h := NewProxy(up.URL+"/mcp", &Counter{Path: filepath.Join(t.TempDir(), "c"), Max: 1}, true)
	list := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if slices.Contains(toolNames(list), "browser_evaluate") {
		t.Error("filtered")
	}
	res := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_evaluate","arguments":{}}}`)
	if !isToolError(res, "tool_not_allowed") || up.calls("browser_evaluate") != 0 {
		t.Error("refused before upstream")
	}
	res = rpc(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"http://169.254.169.254/"}}}`)
	if !isToolError(res, "url_refused") {
		t.Error("metadata IP refused")
	}
}

func TestProxyCapsScreenshots(t *testing.T) {
	for _, sse := range []bool{false, true} {
		up := fakeUpstream(t, withSSE(sse))
		h := NewProxy(up.URL+"/mcp", &Counter{Path: filepath.Join(t.TempDir(), "c"), Max: 1}, true)
		res := rpc(t, h, screenshotCall(1))
		if n := imageBytes(t, res); n == 0 || n > ImageLimit {
			t.Errorf("sse=%v: image %d bytes", sse, n)
		}
		if !isToolError(rpc(t, h, screenshotCall(2)), "screenshot_cap_reached") {
			t.Errorf("sse=%v: cap", sse)
		}
	}
}
```

- [ ] **Step 2: Run, expect FAIL:** `go test ./internal/browsergate/ -run Proxy`
- [ ] **Step 3: Implement.**
  - The proxy reads bodies of at most 4 MiB, the same bound as `images/maku-opencode/mcp_proxy.py:12-13`, and refuses redirects.
  - It copies `Content-Type`, `Accept`, `Mcp-Session-Id` and `Last-Event-ID`.
  - A request is a single JSON-RPC object. A JSON-RPC batch array is refused with HTTP 400.
  - A refused call returns HTTP 200 with `{"jsonrpc":"2.0","id":<id>,"result":{"isError":true,"content":[{"type":"text","text":"<code>: <why>"}]}}`.
  - A response is rewritten per message, either the JSON body or each SSE `data:` line, by matching `id` to the pending method. GET SSE streams pass through untouched.
  - `main.go` uses `flag` and `http.Server{ReadHeaderTimeout: 5s}`, and handles SIGTERM by calling `Shutdown`.
- [ ] **Step 4: Run, expect PASS:** `go test ./internal/browsergate/ && go build ./cmd/maku-browser-gate`
- [ ] **Step 5: Commit**

```bash
git add internal/browsergate cmd/maku-browser-gate
git commit -m "feat(browsergate): the loopback MCP gate in front of Playwright MCP"
```

## Task 9: The `maku-browser` image and its gVisor contract

**Files:**
- Create: `images/maku-browser/Dockerfile`, `package.json`, `package-lock.json`, `config.json`, `entrypoint.sh`, `README.md`
- Create: `images/maku-browser/tests/runsc-contract.sh`, `images/maku-browser/tests/site/index.html`
- Modify: `.github/workflows/ci.yaml` (a `browser-contract` job modelled on `harness-checks`, lines 191-237), `.github/workflows/release.yaml:50,68,310` (image matrix), `.github/workflows/image.yaml`

**Interfaces:**
- Consumes: L3 and L5 (Task 1), and the gate binary (Task 8).
- Produces: `ghcr.io/smana/maku-browser:<v>@sha256:…`, which runs as uid 10001 with `ENTRYPOINT ["tini","--","/opt/browser/entrypoint.sh"]` and serves `127.0.0.1:9223`.

- [ ] **Step 1: Write the failing contract script.** `runsc-contract.sh` takes `$MAKU_BROWSER_IMAGE` and runs, with `--runtime=runsc --read-only --user 10001:10001 --cap-drop ALL --network none --tmpfs /tmp:size=512m --tmpfs /home/browser/.cache:size=128m --memory 2g`. Each numbered check prints `PASS` or `FAIL` and counts failures, in the same style as `charts/maku-gateway/tests/probe/probe.sh`.
  - B1 `/healthz` answers 200 within 30 s.
  - B2 `tools/list` has no `browser_evaluate` or `browser_run_code`.
  - B3 `python3 -m http.server --bind 127.0.0.1 8080` serving `tests/site/` is started inside the same container through `docker exec`, standing in for the harness's dev server. Navigate to it, then check that `browser_snapshot` contains the page's heading.
  - B4 `browser_take_screenshot` returns one image of 512 KiB or less.
  - B5 (AC3) `https://example.com`, `http://169.254.169.254/` and `file:///etc/passwd` are refused by the gate. A page on `127.0.0.1:8080` that does `fetch('http://93.184.216.34/')` and `<img src="http://example.com/x.png">` shows only failed requests in `browser_network_requests`. That proves the Chromium proxy and resolver layer independently of the gate.
  - B6 (AC5) after 10 screenshots and `browser_close`, `ps -o stat= -e | grep -c Z` is 0. Then `docker kill -s TERM` exits within 10 s.
  - B7 the 11th screenshot with `MAX_SCREENSHOTS=10` returns `screenshot_cap_reached`.
  - B8 prints the image size in MiB (compressed and unpacked) and the RSS peak after B4.
- [ ] **Step 2: Run, expect FAIL** (there is no image yet): `DOCKER_RUNTIME=runsc images/maku-browser/tests/runsc-contract.sh`
- [ ] **Step 3: Write the image**

```dockerfile
# syntax=docker/dockerfile:1
# maku-browser (v5 Q16, VS2/VS6): Playwright MCP + Chromium headless shell behind the Go gate, loopback only.
# Built with repository-root context: docker build -f images/maku-browser/Dockerfile .
FROM golang:1.27-bookworm@sha256:<pin> AS gate
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/maku-browser-gate cmd/maku-browser-gate
COPY internal/browsergate internal/browsergate
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /out/maku-browser-gate ./cmd/maku-browser-gate

FROM node:22-bookworm-slim@sha256:<pin>
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/browser/ms-playwright PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 \
    HOME=/home/browser XDG_CONFIG_HOME=/tmp/xdg XDG_CACHE_HOME=/home/browser/.cache NODE_ENV=production
WORKDIR /opt/browser
COPY images/maku-browser/package.json images/maku-browser/package-lock.json ./
RUN set -eux; npm ci --omit=dev --ignore-scripts; \
    PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=0 npx --no-install playwright install --with-deps --only-shell chromium; \
    apt-get install -y --no-install-recommends tini fonts-noto-core fonts-liberation2 fonts-dejavu-core; \
    rm -rf /var/lib/apt/lists/* /root/.npm; \
    groupadd -g 10001 browser && useradd -u 10001 -g 10001 -d /home/browser -s /usr/sbin/nologin browser; \
    mkdir -p /home/browser/.cache && chown -R 10001:10001 /home/browser; \
    find / -xdev -type f -perm /6000 -exec chmod ug-s {} +
COPY --from=gate /out/maku-browser-gate /usr/local/bin/maku-browser-gate
COPY images/maku-browser/config.json /etc/maku-browser/config.json
COPY --chmod=0755 images/maku-browser/entrypoint.sh /opt/browser/entrypoint.sh
USER 10001
ENTRYPOINT ["tini", "--", "/opt/browser/entrypoint.sh"]
```

`config.json` is the Playwright MCP config. Check the key names against L5's version.

```json
{
  "server": {"host": "127.0.0.1", "port": 9224},
  "capabilities": ["core"],
  "imageResponses": "allow",
  "outputDir": "/tmp/out",
  "network": {"allowedOrigins": ["http://127.0.0.1:*", "http://localhost:*", "http://[::1]:*"]},
  "browser": {
    "browserName": "chromium",
    "isolated": true,
    "launchOptions": {
      "headless": true,
      "chromiumSandbox": false,
      "args": ["--no-sandbox", "--disable-setuid-sandbox", "--disable-dev-shm-usage", "--disable-gpu",
               "--disable-software-rasterizer", "--disable-extensions", "--disable-sync", "--mute-audio",
               "--disable-breakpad", "--disable-crash-reporter", "--no-first-run",
               "--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=127.0.0.1;localhost;[::1]",
               "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost"]
    },
    "contextOptions": {"viewport": {"width": 1280, "height": 800}, "serviceWorkers": "block"}
  }
}
```

`entrypoint.sh`:

```sh
#!/bin/sh
set -eu
mkdir -p /tmp/xdg /tmp/out /tmp/gate
[ "${VISION:-1}" = 1 ] || export PLAYWRIGHT_MCP_IMAGE_RESPONSES=omit
node /opt/browser/node_modules/@playwright/mcp/cli.js --config /etc/maku-browser/config.json &
exec maku-browser-gate -listen 127.0.0.1:9223 -upstream http://127.0.0.1:9224/mcp -count /tmp/gate/count
```

`package.json` pins `"@playwright/mcp": "<L5 version>"` exactly, and the `playwright-core` it brings. The lock is committed.
- [ ] **Step 4: Build and run, expect PASS:** `docker build -t maku-browser:contract -f images/maku-browser/Dockerfile . && SKIP_BUILD=1 MAKU_BROWSER_IMAGE=maku-browser:contract images/maku-browser/tests/runsc-contract.sh`. Record B8 in the ledger. Budget: about 300 MB compressed, about 750 MB unpacked. Fail the review if it exceeds 1.2 GB unpacked.
- [ ] **Step 5: Wire CI and release.** Add a `browser-contract` job that runs when `images/maku-browser/**`, `internal/browsergate/**` or `cmd/maku-browser-gate/**` changes, using the same gVisor install step as `harness-checks`. Add `maku-browser` to the release, scan and cosign matrices next to `maku-opencode`, multi-arch.
- [ ] **Step 6: Commit**

```bash
git add images/maku-browser .github/workflows
git commit -m "feat(images): add the maku-browser sidecar image with its gVisor contract"
```

## Task 10: Harness drivers: the browser MCP server, image input and the rules

**Files:**
- Create: `images/maku-harness/browser_tools.py`, `images/maku-harness/tests/test_browser_tools.py`
- Modify: `images/maku-opencode/role_config.py:120-209`, `images/maku-opencode/opencode_driver.py:120-200`, `images/maku-opencode/Dockerfile:58-73` (COPY `browser_tools.py`)
- Modify: `images/maku-harness/openhands_driver.py:129-195`, `images/maku-harness/Dockerfile:79` (COPY list)
- Test: `images/maku-opencode/tests/test_role_config.py`, `images/maku-harness/tests/test_browser_openhands.py` (create; runs in the image's `test` stage)

**Interfaces:**
- Consumes: the pod env `BROWSER_MCP_URL` and `SCREENSHOTS_VISION` (Task 6), and L1, L2 and L4 (Task 1).
- Produces:
  - `browser_tools.browser_url(env) -> str | None`
  - `browser_tools.vision_on(env) -> bool`
  - `browser_tools.browser_rules(env) -> str` (empty when the browser is off)
  - the OpenCode MCP server `browser`
  - the OpenHands `mcp_config["browser"]`

- [ ] **Step 1: Write the failing tests**

```python
# images/maku-harness/tests/test_browser_tools.py
class BrowserTools(unittest.TestCase):
    def test_off_without_url(self):
        self.assertIsNone(bt.browser_url({}))
        self.assertEqual(bt.browser_rules({}), "")

    def test_only_loopback_urls_are_used(self):
        self.assertIsNone(bt.browser_url({"BROWSER_MCP_URL": "http://10.0.0.1:9223/mcp"}))
        self.assertEqual(bt.browser_url({"BROWSER_MCP_URL": "http://127.0.0.1:9223/mcp"}), "http://127.0.0.1:9223/mcp")

    def test_rules_name_loopback_and_snapshot_first(self):
        r = bt.browser_rules({"BROWSER_MCP_URL": "http://127.0.0.1:9223/mcp", "SCREENSHOTS_VISION": "1"})
        for s in ("127.0.0.1", "browser_snapshot", "browser_take_screenshot", "--bind 127.0.0.1"):
            self.assertIn(s, r)
        r = bt.browser_rules({"BROWSER_MCP_URL": "http://127.0.0.1:9223/mcp", "SCREENSHOTS_VISION": "0"})
        self.assertNotIn("browser_take_screenshot", r)
```

```python
# images/maku-opencode/tests/test_role_config.py (added cases)
    def test_browser_server_and_image_input(self):
        env = {**PUBLIC, "BROWSER_MCP_URL": "http://127.0.0.1:9223/mcp", "SCREENSHOTS_VISION": "1"}
        cfg = role_config.role_config(env)
        self.assertEqual(cfg["mcp"]["servers"]["browser"], {"type": "remote", "url": "http://127.0.0.1:9223/mcp", "oauth": False, "codemode": False})
        entry = cfg["providers"]["maku"]["models"]["agent-default"]
        self.assertIn("image", entry["modalities"]["input"])  # key per ledger L4

    def test_no_browser_no_server_no_image(self):
        cfg = role_config.role_config(PUBLIC)
        self.assertNotIn("mcp", cfg)
        self.assertNotIn("modalities", cfg["providers"]["maku"]["models"]["agent-default"])

    def test_platform_and_browser_coexist(self):
        env = {**PUBLIC, "BROWSER_MCP_URL": "http://127.0.0.1:9223/mcp"}
        cfg = role_config.role_config(env, mcp_proxy_url="http://127.0.0.1:41000/mcp")
        self.assertEqual(set(cfg["mcp"]["servers"]), {"platform", "browser"})
```

```python
# images/maku-harness/tests/test_browser_openhands.py (image test stage only)
    def test_browser_mcp_and_vision(self):
        env = {**BASE_ENV, "BROWSER_MCP_URL": "http://127.0.0.1:9223/mcp", "SCREENSHOTS_VISION": "1"}
        req = openhands_driver.build_request(env, "task", "rules")
        self.assertEqual(req["agent"]["mcp_config"]["browser"], {"url": "http://127.0.0.1:9223/mcp", "transport": "http"})
        self.assertTrue(req["agent"]["llm"]["capability_overrides"]["supports_vision"])  # key per ledger L2
        self.assertIn("browser_snapshot", req["agent_launch_additions"]["system_message_suffix_append"])
```

- [ ] **Step 2: Run, expect FAIL:**
`cd images/maku-opencode && python3 -m unittest tests.test_role_config -v` and `cd images/maku-harness && python3 -m unittest tests.test_browser_tools -v`
- [ ] **Step 3: Implement.** `browser_tools.py`:

```python
"""The opt-in browser sidecar's harness side (v5 Q16, phase A): one MCP server, image input, rules."""
from urllib.parse import urlparse

_LOOPBACK = {"127.0.0.1", "localhost", "::1"}


def browser_url(env) -> "str | None":
    url = (env.get("BROWSER_MCP_URL") or "").strip()
    return url if url and urlparse(url).hostname in _LOOPBACK else None


def vision_on(env) -> bool:
    return browser_url(env) is not None and env.get("SCREENSHOTS_VISION") == "1"


def browser_rules(env) -> str:
    if browser_url(env) is None:
        return ""
    lines = [
        "## Browser (loopback only)",
        "A headless browser can open pages served inside this sandbox. Start the dev server bound to loopback,",
        "for example `hugo server --bind 127.0.0.1 --port 1313` or `npm run dev -- --host 127.0.0.1`, then open",
        "http://127.0.0.1:<port>/ with browser_navigate. No other host is reachable.",
        "Use browser_snapshot (the accessibility tree) to check structure and text.",
    ]
    if vision_on(env):
        lines.append("Use browser_take_screenshot only to check layout, colour or styling; you have a small fixed number of them.")
    lines.append("Stop the dev server before you finish.")
    return "\n".join(lines)
```

OpenCode `role_config`: when `browser_tools.browser_url(env)` is set, add `servers["browser"]`. When `vision_on`, add L4's key to the run's model entry (`_model_entry(model)`) only. `opencode_driver.py:194-198` passes `rules=request.rules + "\n\n" + browser_rules(env)`. For OpenHands `build_request`, put `mcp_config` together from `platform` (when `MCP_URL` is set) and `browser`. When `vision_on`, merge `{"supports_vision": True}` (L2's key) into `capability_overrides`, and append `browser_rules(env)` to `rules`.
- [ ] **Step 4: Run, expect PASS.** Run the two host commands above, then the image test stages in CI or with Docker on: `docker build --target test -f images/maku-opencode/Dockerfile .` and `docker build --target test images/maku-harness`. Confirm AC1: `docker image inspect` sizes of both harness images grow by under 10 KiB (one small .py file).
- [ ] **Step 5: Commit**

```bash
git add images/maku-harness images/maku-opencode
git commit -m "feat(harness): wire the browser MCP server, image input and loopback rules into both drivers"
```

## Task 11: Charts: values, controller config, run-pod VAP

**Files:**
- Modify: `charts/maku/values.yaml:280-313`, `charts/maku/values.schema.json`
- Modify: `charts/maku/templates/run-controller-config.yaml:77-79`
- Modify: `charts/maku/templates/run-pod-policy.yaml:5-7` (image list) and its validations
- Modify: `charts/agent-factory/values.yaml`, `values.schema.json` (pass-through and the pinned default `maku-browser` tag, written by the release like `opencode`)
- Test: `charts/maku/tests/admission/policy_test.go`, `charts/maku/tests/render.sh` goldens, `charts/agent-factory/tests/render.sh`

**Interfaces:**
- Consumes: the run-controller config keys from Task 5.
- Produces:
  - `runController.images.browser: {repository: ghcr.io/smana/maku-browser, tag: ""}`
  - `runController.browser.resources` (spec defaults)
  - `runController.model.gateway.vision: {public: [], internal: []}`
  - VAP rule: a container named `browser` exists only when the pod's `agents.ogenki.io/role` is `implementer` or `tester`. It has no `volumeMounts` except `browser-tmp` and `browser-cache`, no `env[].valueFrom`, and no `envFrom`.

- [ ] **Step 1: Write the failing admission tests** (envtest VAP, the existing `policy_test.go` harness):

```go
{name: "browser on a reviewer pod is refused", pod: runPod(withRole("reviewer"), withBrowser()), wantErr: "browser"},
{name: "browser mounting the workspace is refused", pod: runPod(withRole("implementer"), withBrowser(mount("workspace", "/workspace"))), wantErr: "browser"},
{name: "browser reading a secret env is refused", pod: runPod(withRole("tester"), withBrowser(secretEnv("x"))), wantErr: "browser"},
{name: "browser on an implementer pod is admitted", pod: runPod(withRole("implementer"), withBrowser())},
{name: "an unlisted browser image is refused", pod: runPod(withRole("implementer"), withBrowser(image("ghcr.io/evil/browser@sha256:" + sha))), wantErr: "images come from"},
```

- [ ] **Step 2: Run, expect FAIL:** `go test ./charts/maku/tests/admission/ -run Browser` (envtest assets: `KUBEBUILDER_ASSETS=$(go tool setup-envtest use -p path 1.36.x)`)
- [ ] **Step 3: Implement.** In `run-pod-policy.yaml` line 6, add the browser repository the same way as `opencode`:

```yaml
{{- if and $c.images.browser $c.images.browser.tag }}{{ $images = append $images $c.images.browser.repository }}{{ end -}}
```

New validation:

```yaml
    - expression: >-
        variables.containers.all(c, c.name != 'browser' ||
        (has(object.metadata.labels) && object.metadata.labels[?'agents.ogenki.io/role'].orValue('') in ['implementer', 'tester'] &&
        (!has(c.volumeMounts) || c.volumeMounts.all(m, m.name in ['browser-tmp', 'browser-cache'])) &&
        (!has(c.env) || c.env.all(e, !has(e.valueFrom))) && !has(c.envFrom)))
      message: "the browser sidecar runs on implementer and tester pods only, mounting its own /tmp and cache, with no secret, token or env from a source (v5 Q16)"
```

In `run-controller-config.yaml`, render `browser:` the same way as `opencode` (lines 77-78), plus `browser.resources` and `model.gateway.vision`.
- [ ] **Step 4: Run, expect PASS:** `go test ./charts/maku/tests/admission/ && charts/maku/tests/render.sh && charts/agent-factory/tests/render.sh` (update goldens and review the diff).
- [ ] **Step 5: Commit**

```bash
git add charts
git commit -m "feat(charts): ship the browser image, its resources, the vision aliases and the run-pod rule"
```

## Task 12: Docs and the full local gate

**Files:**
- Create: `docs/browser-verification.md`
- Modify: `docs/onboard-a-repository.md` (the `screenshots` block), `docs/opencode-harness.md` (link), `docs/security.md` (residual C1), `docs/gateway.md` (the vision list), `docs/README.md` (index)

- [ ] **Step 1: Write `docs/browser-verification.md`.** It covers what it does; the opt-in YAML (spec §7 minus `memoryLimit`); the vision list; resources and quota sizing (+2Gi limit and +512Mi request per opted-in run); the two network layers and the honest residual (C1); the fallback without vision; and a troubleshooting table covering `browser_unavailable`, `screenshot_cap_reached`, `url_refused` and `tool_not_allowed`. Include one mermaid diagram (spec §3, corrected: gate `:9223` → Playwright MCP `:9224`).
- [ ] **Step 2: Run the docs gates:** `task docs && task names`
- [ ] **Step 3: Run the full gate once,** before the PR is marked ready: `task check`. This is the only full-suite run, owned by the executor.
- [ ] **Step 4: Commit**

```bash
git add docs
git commit -m "docs(browser): document the opt-in browser verification and its boundaries"
```

---

## ANR adoption (ai-native-ref), after a Maku release carrying Tasks 1–12

No cloud-native-ref change is needed. The `gvisor` RuntimeClass (aws-0 `runtimeclass-gvisor`, gcp-0 GKE Sandbox) and native sidecars already exist and are already used by `room-bridge`. A separate ANR plan file is not needed. These tasks run in an ANR worktree, and `task check` is the evidence.

| # | Change | Files | Test |
|---|---|---|---|
| A1 | Bump Maku pins (all images, plus the new `runController.images.browser` at the release's `vX@sha256:`) | `infrastructure/base/room-broker/helmrelease.yaml:73-111` | `task check` (host `validate-manifests.sh` plus `scripts/ci/tests/test-*room-broker*`) |
| A2 | `runController.model.gateway.vision: {public: [mistral-standard, mistral-light], internal: [agent-default, tier-light, tier-standard, tier-frontier]}`. Add `kimi-frontier` only if BQ2's curl says yes. | same HelmRelease (beside `aliases`) | add an assertion to the existing values test that every vision alias is in `model-gateway/helmrelease.yaml`'s `aliases.<class>` |
| A3 | `screenshots: {enabled: true, roles: [implementer, tester], maxScreenshots: 10}` on the `ai-native-ref` Repository only | `tooling/base/agent-factory/resources/repository-ai-native-ref.yaml`, vendored CRD `scripts/ci/flux-schema/vendored-crds/maku-repository.yaml` | flux-schema validates the new field |
| A4 | Quota: `runsQuota.hard.limits.memory` 64Gi already covers 8 × (4Gi + 2Gi + bridge). Leave it unchanged and record the arithmetic in a comment. Add `maku-browser` to aws-0's headroom pre-pull so the first opted-in run does not pay a ~300 MB pull. | HelmRelease comment; `infrastructure/aws-0/karpenter-nodepools-agents/headroom/*` | `task check` |
| A5 | Amend the spec: §5.2 and §8 per C1 and C2, §6 per C4 and C5, §4.3 and §5.3 per C8 and C9, and add "Phase A as built" pointing at the Maku plan | `docs/superpowers/specs/2026-11-23-v5-harness-screenshots-design.md` | `task docs` |
| A6 | Runbook for the drill below | `docs/runbooks/<next>-browser-verification.md` (follow `docs/docs-style.md`) | `task docs` |

---

## Live acceptance drill on gcp-0 (runbook A6)

Batch this into one gcp-0 session, per "offline before platform". Preconditions: gcp-0 is up on the Maku release with A1–A4, and GKE Sandbox nodes are warm.

| Step | Action | Pass when | Covers |
|---|---|---|---|
| D0 | The owner runs one curl per alias, with an `image_url` user message (a 64×64 red square, "what colour?"), through `model-gateway` from a run-like token | answers "red" on each alias listed in A2 | BQ2 |
| D1 | Plant a bug on a branch of ANR: in `website/assets/css/custom.css`, set the hero CTA button's text colour equal to its background in dark mode. File issue "The hero button is invisible in dark mode on the website; fix it and verify visually", labelled `factory/ready` plus `model:mistral-standard` (public) | triage freezes `screenshots`; `kubectl get pod -l agents.ogenki.io/role=implementer -o jsonpath='{.spec.initContainers[*].name}'` lists `browser` | AC1 (reviewer pod has no `browser`), VAP |
| D2 | Watch the room and the harness log | the agent runs `hugo server --bind 127.0.0.1 --port 1313` (mise installs `hugo-extended`, and Go fetches Hextra through the `golang` profile), calls `browser_snapshot`, toggles dark mode, then `browser_take_screenshot`; it states the defect, fixes the CSS and re-screenshots; the gate log shows `screenshot n≤10` | AC2 |
| D3 | Repeat D1 with `harness:opencode` | same as D2 | AC7 |
| D4 | Repeat D1 on `dataClass: internal` (`agent-default` = Claude Opus 5.5) | same; proves Anthropic native translation of image parts | AC8, the Task 2 gap |
| D5 | Use `kubectl exec` into the harness container (allowed: not an ephemeral container) and ask the agent, through a steering message, to open `https://example.com` and `http://169.254.169.254/` | both refused with `url_refused`; the `browser_network_requests` of a local page with an external `<img>` shows a failure | AC3 on GKE Sandbox |
| D6 | During D2, `kubectl exec … -c browser -- sh -c 'kill -9 $(pgrep -f chrome | head -1)'`, then send the run a 3 GiB allocation page (a `tests/site/oom.html` copied into the dev server) | the browser container restarts (`restartCount` 1), the harness continues or records `browser_unavailable`, and the run still ends | AC5 |
| D7 | Read the meter and the run's cache line (`agent-run` prompt-cache log) for D2 against a no-screenshot baseline run of the same issue with screenshots off | cache-read share within 5 points; the vision turns are priced (no unpriced fallback) | AC6, AC8 |
| D8 | Check `git status` on the PR branch and the PR diff | no PNG anywhere; the PR fixes only the CSS | AC4 |

Record each step in `ai-native-ref/docs/runbooks/…` with the run IDs and costs. Close the drill with the spec amendment (A5).

---

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| A harness drops images in tool results (BQ1) | medium | Phase A is a no-op on that harness | Task 1 finds it before any build. Fall back to the one harness that works, and do not fork upstream. |
| The public default (`glm-5.3`) is text-only (BQ2) | high | Public repos get snapshot-only unless their model is overridden | A vision list plus a per-issue `model:` label. Document it. A vision delegate goes to Phase B. |
| Chromium is unstable under GKE Sandbox (systrap) at high churn | medium | Flaky runs | Native sidecar restart, `--no-zygote` documented as a fallback, and the CI runsc contract |
| The sidecar shares the pod's egress (C1) | certain (by design of K8s) | A renderer exploit reaches what the harness reaches | Chromium proxy and resolver flags, the gate, no credentials in the sidecar. The residual is documented. |
| Page JS on loopback can reach harness ports (`:8000` OpenCode server with Basic auth, the `mcp_proxy` random port that attaches the room-tools token, bridge `:8085`) | low | A repository's dev server could call room tools | Same trust class as the dev-server code, which already runs in the harness with that token readable. `--blocked-origins` lists 8000, 8085, 9223 and 9224 as hygiene. Pinning `mcp_proxy` to a fixed port to block it too is filed as a follow-up. |
| Playwright MCP config keys or tool names shift between versions | medium | Gate filter misses a new tool | The gate is allow-list (default deny), the version is pinned in the lock, and the runsc contract B2 fails on drift. |
| Image size slows cold starts | medium | +20–40 s first pull per node | aws-0 headroom pre-pull (A4). gcp-0 image streaming. Startup probe allows 60 s. |
| Quota pressure | low | Fewer parallel runs | +2Gi limit per opted-in run. A4 arithmetic shows 8 runs still fit. |

---

## Cost and size {#cost-and-size}

**Image:** `maku-browser` is about 280–320 MB compressed and 700–800 MB unpacked (node slim ~75 MB, Chromium headless shell and its libs ~350 MB, fonts ~60 MB, Playwright ~30 MB, gate ~8 MB). Task 9 B8 records the real figure. `maku-opencode` and `maku-harness` grow by one small `.py` file each (AC1 is held as "0 bytes of browser").

**Compute per opted-in run:** +250m CPU and +512Mi requested, with a cap of 2 CPU and 2Gi. Headless Chromium on a docs page idles at 200–400 MiB RSS.

**Tokens per run** (cap 10 screenshots at 1280×800, about 15 later turns re-reading each image from cache):

| Model | Tokens per image | First read | Cache writes | Cache re-reads | Total per run |
|---|---|---|---|---|---|
| Claude Opus 5.5 ($4 / $5 write / $0.20 read per MTok) | ~1.4k | 14k → $0.06 | 14k → $0.07 | 210k → $0.04 | **≈ $0.17** |
| Mistral Medium 2604 ($1.50 / $0.15) | ~4k | 40k → $0.06 | n/a | 600k → $0.09 | **≈ $0.15** |
| Kimi k3.1-pro ($2 / $0.20) | ~4k (assumed) | 40k → $0.08 | 40k → $0.08 | 600k → $0.12 | **≈ $0.28** |

Most runs take 2–4 screenshots, so the typical extra cost is about $0.05–0.10 per run. That is why the ephemeral window (C8) is deferred.

**Build effort:** 12 Maku tasks plus 6 ANR tasks. Roughly 5–7 agent-days for Maku (Task 1 is about 0.5 d; Tasks 7–9 are the bulk at about 3 d), 0.5 d for ANR, and one gcp-0 session of 3–4 h for the drill. Live spend for the drill is under $5 in model calls, plus the cluster hours.

---

## Self-review

- **Spec coverage:** VS1 (Phase A only) is all tasks. VS2 is Tasks 6, 9 and 11. VS3 is Tasks 7, 9 and the C1 row. VS4 is Tasks 8 and 9. VS5 is Tasks 3–5 and 11. VS6 is Task 9. VS7 is sequencing, owner's call. §4.2 is Task 11 (C3). §4.3 is C9 (dropped). §5.3 caps are Task 7, and the window is C8 (deferred). §6 is Tasks 2, 5 and 10 plus drill D0/D4 (C4, C5). §7 is Task 3 (C10). AC1–AC8 map to drill D1–D8 and contract B1–B8.
- **Names used across tasks:** `AgentRunScreenshots`, `config.Screenshots.For`, `runs.Spec.Screenshots int32`, `stampScreenshots`, `Images.Browser`, `Config.Browser.Resources`, `Gateway.Vision`, `VisionFor`, `BrowserGatePort`, `browserSidecar`, `browserEnv`, `BROWSER_MCP_URL`, `SCREENSHOTS_VISION`, `MAX_SCREENSHOTS`, `VISION`, `browser_tools.{browser_url,vision_on,browser_rules}`.
- **Ledger-dependent keys** (filled by Task 1, not placeholders): the OpenCode image-input key (L4), the OpenHands vision override key (L2), and the `@playwright/mcp` version and config keys (L5). The tests name the expected shape. The executor updates the shape if the ledger differs.
