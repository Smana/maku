# Audit T7: Charts and RBAC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make both charts' RBAC and network policy match what the binaries do: the factory's AgentRun Role follows `config.runs` (H-5), the broker's AgentRun `patch` is bounded to the suspend annotations by an admission policy (S-2, D4), the run controller reads a CA-only Secret (S-3), the broker CNP opens the factory's two flows (S-4), and the chart refuses a run controller under a group it does not reconcile (S-5).

**Architecture:** Chart-only changes, plus one envtest package and two quickstart lines. Each finding is pinned first by a `yq` assertion in a chart's `tests/render.sh` (or a `bad` render case), S-2's CEL additionally by a Go envtest test that applies the chart's own rendered policy to a real API server and patches an AgentRun as the broker's ServiceAccount. One commit per finding (D0).

**Tech Stack:** Helm 4.3.0 + yq 4.54.1 (`render.sh`), bash + shellcheck (`hack/quickstart/check.sh`), Go 1.27 with controller-runtime v0.25.2 envtest (kube-apiserver v1.36.2 from `setup-envtest`), Kubernetes `admissionregistration.k8s.io/v1` ValidatingAdmissionPolicy.

**Spec:** `docs/superpowers/specs/2026-11-01-audit-fixes-design.md` (theme T7, decision D4, the "Open to the owner" rows), findings in `docs/superpowers/specs/2026-11-01-project-audit.md` (H-5, S-2 to S-5).

## Global Constraints

- **D0:** one PR for the theme, branch `fix/audit-t7-charts`; each finding is one commit holding the test that pins it and its fix; `task check` exits 0 on the PR.
- **After T6:** start from `main` once T6 (factory API TLS) has merged. T6 added `api.tls` to the factory chart and `broker.factoryCA.secretName` / `broker.factoryInsecure` to the maku chart, and T6's render assertions (its `Certificate`). Keep every line T6 added; where this plan replaces a block of `render.sh`, carry T6's `--set` flags and assertions into the new block.
- Commit messages: Conventional Commits, English, scope `chart` as in `aaddf11d fix(chart): …`. **No `Co-Authored-By` and no "Generated with" line**, in commits and in the PR body.
- Chart defaults keep today's behaviour for every deployment except the two the findings name: a run controller under `runs.group != maku.ogenki.io` now fails at render (S-5), and the run controller now reads `broker.tls.caSecretName` (default `room-broker-ca`), which an operator must create before upgrading (S-3). Both go in the PR body as upgrade notes.
- `charts/maku` stays factory-free: S-4 adds policy rules keyed on `broker.factoryURL`, never a factory object.
- No secret on argv in the quickstart: the CA Secret reaches `kubectl` on stdin (`yq` builds it from `kubectl get -o json`).
- Never override `HOME`. Never drop `-race`.
- Per-task verification is targeted (`bash charts/<chart>/tests/render.sh`, `task quickstart:check`, one `go test` package). `task check` runs once, in Task 6.

## Spec deviations

| # | Spec says | Plan does | Evidence |
|---|---|---|---|
| 1 | D4: the broker may change "only `metadata.annotations` keys under the suspend prefix" | The policy allows **exactly three keys**: `agents.ogenki.io/operating-mode`, `agents.ogenki.io/remaining-seconds`, `agents.ogenki.io/resumed-at` | There is no suspend prefix. The three keys (`internal/runwatch/run.go:22-27`, patched by `internal/suspend/suspend.go:52-57`) share `agents.ogenki.io/` with 22 other keys in `internal/`, among them the meter's `agents.ogenki.io/usage-tokens`, `max-micro-usd`, `charged-to`, `principal` and the factory's `revoked`. A prefix rule would let a compromised broker zero a run's metered spend or un-revoke it, the exact abuse S-2 names |
| 2 | S-2's check: `object.spec == oldObject.spec` and the annotation rule | Also refuses a broker change to `metadata.labels`, `metadata.finalizers` and `metadata.ownerReferences` | The broker's only AgentRun writes are the suspend merge patch and `Delete` (`grep -rn 'Patch(\|\.Delete(' internal/runwatch internal/roomctrl internal/bridgeapi internal/suspend`). A label or finalizer change is never legitimate, and an added finalizer blocks the run's deletion |
| 3 | S-3: "a Secret holding `ca.crt` only" | New value `broker.tls.caSecretName`, default `room-broker-ca`; the render refuses it equal to `broker.tls.secretName`; the quickstart's `up.sh` creates it from the quickstart CA | Without a default and a guard the default render would still name the key pair. The quickstart is the one install the repo runs end to end (`hack/quickstart/e2e.sh`, `migrate-e2e.sh` both call `up.sh`), so it must create the Secret |
| 4 | S-4: ingress and egress "from `broker.factoryPodLabels`" | The template adds `io.kubernetes.pod.namespace: <release namespace>` to those labels unless they set it; the egress port is `factoryURL`'s (443/80 by scheme when absent) | Cilium's `fromEndpoints`/`toEndpoints` select pods in the policy's own namespace unless the namespace label is given; Cilium matches L4 after service translation, so the port must be the pod's (the factory chart's Service maps 8082 to 8082) |
| 5 | Audit locates S-3 at `run-controller-rbac.yaml:180-190`, S-5 at `:139-149` | The file is 80 lines: the CA Role is `run-controller-rbac.yaml:60-70`, the group at `:19-29` | `cat -n charts/maku/templates/run-controller-rbac.yaml` at `e14b7949` |

## Answers to the spec's open rows

| Question | Answer |
|---|---|
| Does the envtest API server enforce `ValidatingAdmissionPolicy` (S-2)? | **Yes.** `task test` uses `setup-envtest use 1.36.x` (`taskfile.yaml:68`), installed as v1.36.2; its `kube-apiserver -h` lists `ValidatingAdmissionPolicy` among the default-enabled admission plugins. Task 5's test was run against it on 2026-11-01 in a scratch copy of the repo: 12 cases, PASS in 5.7 s. So S-2 gets the Go envtest case **and** the render assertions |
| `broker.factoryPodLabels` default (S-4) | `app.kubernetes.io/name: agent-factory`, the factory chart's selector label (`charts/maku-factory/tests/render.sh:31`) |

---

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `charts/maku-factory/templates/rbac.yaml`, `tests/render.sh`, `README.md` | 1 | AgentRun Role and RoleBinding from `config.runs` |
| `charts/maku/templates/_helpers.tpl` (`ap.validate`) | 2, 3 | Render-time refusals: run controller vs `runs.group`; CA Secret vs key pair |
| `charts/maku/tests/render.sh` | 2, 3, 4, 5 | The assertions pinning each finding |
| `charts/maku/values.yaml` | 3, 4 | `broker.tls.caSecretName`, `broker.factoryPodLabels` |
| `charts/maku/templates/run-controller-config.yaml`, `run-controller-rbac.yaml` | 3 | The controller reads and may get the CA-only Secret |
| `hack/quickstart/up.sh`, `hack/quickstart/check.sh` | 3 | The quickstart creates the CA-only Secret; the drift check holds it |
| `charts/maku/templates/broker-cnp.yaml` | 4 | The factory's ingress and egress rules |
| `charts/maku/templates/broker-agentrun-policy.yaml` (new) | 5 | ValidatingAdmissionPolicy + binding |
| `charts/maku/tests/admission/policy_test.go` (new) | 5 | envtest: the rendered policy against a real API server |
| `charts/maku/README.md`, `docs/security.md` | 2-5 | Values, caveats, the broker's RBAC statement |

Dependencies: Task 2 before Task 3 (both rewrite the production render of `charts/maku/tests/render.sh`). Tasks 1, 4, 5 are independent. Execute in numeric order.

---

### Task 1: H-5, the factory's AgentRun Role follows `config.runs`

**Files:**
- Modify: `charts/maku-factory/templates/rbac.yaml:1-62`
- Modify: `charts/maku-factory/tests/render.sh` (after line 70, and before the `for bad` loop at line 101)
- Modify: `charts/maku-factory/README.md:4-5,65`

**Interfaces:**
- Consumes: `.Values.config.runs.{group,namespace}`, rendered verbatim into the factory's config, where `runwatch.Kind.WithDefaults()` fills each empty field from `runwatch.DefaultKind` (`cloud.ogenki.io`, `agents`, `xplane-run-`; `internal/runwatch/kind.go:23`).
- Produces: nothing later tasks use.

- [ ] **Step 1: Create the branch**

```bash
cd /home/smana/Sources/maku
git switch main && git pull --ff-only
git switch -c fix/audit-t7-charts
```

- [ ] **Step 2: Write the failing render assertions**

In `charts/maku-factory/tests/render.sh`, after line 70 (`ykind Role '.rules[] | select(.resources[] == "agentruns") | has("verbs")' "AgentRun RBAC"`), add the default-values assertions:

```bash
# H-5: the AgentRun Role and its binding sit where the binary looks (config.runs, runwatch.Kind);
# unset, runwatch.DefaultKind's: cloud-native-ref's group in agents.
ykind Role 'select(.metadata.namespace == "agents") | .rules[] | select(.resources[] == "agentruns") | .apiGroups[0] == "cloud.ogenki.io"' "default AgentRun Role: cloud.ogenki.io in agents"
ykind RoleBinding 'select(.metadata.namespace == "agents") | (.roleRef.name == "agent-factory" and .subjects[0].namespace == "agent-system")' "default AgentRun RoleBinding in agents"
```

Before the `for bad in "" "v0.0.0" "latest"; do` loop, add the configured render:

```bash
# H-5: config.runs moves the Role and its binding; nothing stays behind in agents.
out=$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set config.runs.group=maku.ogenki.io --set config.runs.namespace=runs2)
ykind Role 'select(.metadata.namespace == "runs2") | .rules[] | select(.resources[] == "agentruns") | .apiGroups[0] == "maku.ogenki.io"' "H-5: the AgentRun Role takes config.runs.group"
ykind RoleBinding 'select(.metadata.namespace == "runs2") | (.roleRef.name == "agent-factory" and .subjects[0].namespace == "agent-system")' "H-5: the AgentRun RoleBinding sits in config.runs.namespace"
ynone 'select(.metadata.namespace == "agents")' "H-5: nothing left in agents once config.runs.namespace moves the runs"
```

- [ ] **Step 3: Run it to verify it fails**

Run: `bash charts/maku-factory/tests/render.sh`
Expected: exit 1 with
```
FAIL: H-5: the AgentRun Role takes config.runs.group
FAIL: H-5: the AgentRun RoleBinding sits in config.runs.namespace
FAIL: H-5: nothing left in agents once config.runs.namespace moves the runs
```
(the two default assertions already pass: they pin today's values).

- [ ] **Step 4: Template the Role from `config.runs`**

In `charts/maku-factory/templates/rbac.yaml`, insert before line 1 (`---`):

```yaml
{{- /* The AgentRun Role follows config.runs, as the binary does (runwatch.Kind); unset fields take runwatch.DefaultKind's. */}}
{{- $runs := .Values.config.runs | default dict }}
{{- $runsNamespace := $runs.namespace | default "agents" }}
{{- $runsGroup := $runs.group | default "cloud.ogenki.io" }}
```

Then in the AgentRun Role (lines 43-52) and its RoleBinding (lines 54-62), replace both `namespace: agents` lines with:

```yaml
  namespace: {{ $runsNamespace }}
```

and replace `  - apiGroups: [cloud.ogenki.io]` with:

```yaml
  - apiGroups: [{{ $runsGroup }}]
```

- [ ] **Step 5: Run it to verify it passes**

Run: `bash charts/maku-factory/tests/render.sh && helm lint --strict charts/maku-factory --set image.tag='v0.0.0@sha256:0000'`
Expected: `PASS`, then `1 chart(s) linted, 0 chart(s) failed`.

- [ ] **Step 6: Say it in the chart README**

In `charts/maku-factory/README.md`, line 5 starts with `` `agents`, `templates/rbac.yaml`), `` (the end of "a Role in the release namespace and a Role in"). Replace that opening `` `agents`, `templates/rbac.yaml`) `` with:

```markdown
`config.runs.namespace`, default `agents`, `templates/rbac.yaml`)
```

and line 65, replace `` In `agents`, a second Role manages `AgentRun`s. `` with:

```markdown
In `config.runs.namespace` (default `agents`), a second Role manages the `AgentRun`s of `config.runs.group` (default `cloud.ogenki.io`): the values the binary reads, so the two cannot disagree.
```

Run: `bash hack/docs.sh charts/maku-factory/README.md`
Expected: exit 0, no output.

- [ ] **Step 7: Commit**

```bash
git add charts/maku-factory/templates/rbac.yaml charts/maku-factory/tests/render.sh charts/maku-factory/README.md
git commit -m "fix(chart): the factory's AgentRun Role follows config.runs

The binary reads the run kind from config.runs (runwatch.Kind); the Role
hardcoded agents and cloud.ogenki.io, so a factory configured for
maku.ogenki.io was forbidden every AgentRun call. Audit H-5."
```

---

### Task 2: S-5, refuse the run controller under a group it does not reconcile

**Files:**
- Modify: `charts/maku/templates/_helpers.tpl` (`ap.validate`, before `{{- $dsn := …`)
- Modify: `charts/maku/tests/render.sh:81-103` (the production render), `:161` (the `rc-production.yaml` line), the `bad` list (after line 153)
- Modify: `charts/maku/README.md` (Values table)

**Interfaces:**
- Consumes: `runctl` reconciles `v1alpha1.AgentRun` only, `GroupVersion = maku.ogenki.io/v1alpha1` (`api/v1alpha1/groupversion_info.go:16`).
- Produces: the `prod` array and the `ctl` render in `render.sh`, which Task 3 extends; the helm error text `runs.group "<g>" with runController.enabled`.

- [ ] **Step 1: Write the failing test, and correct the contrary assertion**

`render.sh:82-100` renders `runs.group=cloud.ogenki.io` with the run controller on (the default) and asserts at line 100 that the controller's RBAC follows that group: the broken combination. Replace lines 81-103 (from `# production-shaped render: no migrate, no tokenreview binding` through `ykind Deployment … 'production: the broker runs on the default runtime'`) with the block below. If T6 added `--set` flags to the old production render, append them to `prod`; if T6 added assertions under `out="$p"`, keep them under `out="$p"`.

```bash
# production-shaped render (cloud-native-ref): no migrate, no tokenreview binding, and no run
# controller: there Crossplane's composition runs the cloud.ogenki.io AgentRuns (S-5)
prod=(--set broker.runIssuer.mode=jwks --set broker.runIssuer.issuer=https://oidc --set broker.runIssuer.jwksURL=https://oidc/keys
      --set broker.database.brokerSecret=dsn --set broker.publicURL=https://rooms.example.com
      --set broker.human.issuer=https://idp.example.com --set broker.human.jwksURL=https://idp.example.com/keys
      --set broker.human.clientIDFile=/c --set broker.human.projectIDFile=/p --set broker.human.origin=https://rooms.example.com
      --set broker.human.groups.admin=a --set broker.human.groups.member=m
      --set runs.namePrefix=xplane-run- --set broker.tls.secretName=broker-cert
      --set 'apiServerPorts={6443}' --set clusterDomain=corp.local)
p="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runs.group=cloud.ogenki.io --set runController.enabled=false)"
out="$p"
ynone 'select(.kind=="ClusterRoleBinding")' 'jwks mode binds no auth-delegator'
ynone 'select(.kind=="RuntimeClass")' 'production renders no RuntimeClass: the cluster-scoped gvisor may already exist'
ynone 'select(.kind=="Deployment") | .spec.template.spec.initContainers | select(. != null)' 'no owner Secret, no migrate'
ynone 'select(.metadata.labels."app.kubernetes.io/name" == "run-controller")' 'runController.enabled=false renders no run controller object'
check 'xplane-run-' 'the production prefix flows through'
check 'cloud.ogenki.io' 'the production group flows through'
ykind Service 'select(.metadata.name=="room-broker") | (.spec.ports | map(.port) | sort | join(",")) == "8080,8443,9090"' 'oidc mode exposes :8080'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools.enabled == true' 'the default keeps the :8090 room tools on, as every existing deployment runs it'
ynone 'select(.metadata.labels."app.kubernetes.io/name" == "agent-factory")' 'the production render has no factory object'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.runtimeClassName == null' 'production: the broker runs on the default runtime'

# the run controller under production values: it follows runs.namePrefix, broker.tls, clusterDomain
# and apiServerPorts; its group is maku.ogenki.io, the only one it reconciles
ctl="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}")"
out="$ctl"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.namePrefix == "xplane-run-" and .broker.caSecret == "broker-cert" and .broker.url == "https://room-broker.agent-system.svc.corp.local:8443")' 'controller follows runs, broker.tls.secretName, clusterDomain'
ykind Role 'select(.metadata.name=="run-controller-ca") | (.rules[0].resourceNames | join(",")) == "broker-cert"' 'the CA Role follows broker.tls.secretName'
ykind Role 'select(.metadata.name=="run-controller" and .metadata.namespace=="agents") | .rules[0].apiGroups[0] == "maku.ogenki.io"' 'controller RBAC names the group the controller reconciles'
ykind CiliumNetworkPolicy 'select(.metadata.name=="run-controller") | (.spec.egress[] | select(.toEntities[0]=="kube-apiserver") | (.toPorts[0].ports | map(.port) | join(",")) == "6443")' 'controller CNP follows apiServerPorts'
```

The rendered controller config the loader checks now comes from `$ctl`: in the `# both rendered configs load through the broker's real loader` block, replace

```bash
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$p" >"$tmp/rc-production.yaml"
```

with

```bash
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$ctl" >"$tmp/rc-production.yaml"
```

After the last `bad` line (`bad 'broker.publicURL "http://0.0.0.0:8080" in local mode' …`), add:

```bash
bad 'runs.group "cloud.ogenki.io" with runController.enabled' --set runs.group=cloud.ogenki.io
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bash charts/maku/tests/render.sh`
Expected: exit 1 with exactly `FAIL: --set runs.group=cloud.ogenki.io must not render`.

- [ ] **Step 3: Write the refusal**

In `charts/maku/templates/_helpers.tpl`, inside `ap.validate`, insert before `{{- $dsn := ne ($b.database.brokerSecret | default "") "" -}}`:

```
{{- if and .Values.runController.enabled (ne .Values.runs.group "maku.ogenki.io") -}}
{{- fail (printf "runs.group %q with runController.enabled: the run controller reconciles maku.ogenki.io AgentRuns only; set runController.enabled=false where another controller runs %s AgentRuns (cloud-native-ref's Crossplane composition)" .Values.runs.group .Values.runs.group) -}}
{{- end -}}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `bash charts/maku/tests/render.sh && task quickstart:check`
Expected: `PASS`, then `lib_test: ok` and `quickstart check: ok`.

- [ ] **Step 5: Document the refusal**

In `charts/maku/README.md`'s Values table, after the `runs.{group,namespace,namePrefix}` row, add:

```markdown
| `runController.enabled` | On by default. Refused at render with any `runs.group` but `maku.ogenki.io`, the only group the controller reconciles: turn it off where another controller runs the AgentRuns (cloud-native-ref's `cloud.ogenki.io` composition) |
```

Run: `bash hack/docs.sh charts/maku/README.md`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add charts/maku/templates/_helpers.tpl charts/maku/tests/render.sh charts/maku/README.md
git commit -m "fix(chart): refuse the run controller under a runs.group it does not reconcile

The controller watches maku.ogenki.io AgentRuns only, while its Role
followed runs.group: under cloud.ogenki.io it would start and see no run.
The render now fails, and render.sh's production render, which asserted
that combination, runs without the controller. Audit S-5."
```

---

### Task 3: S-3, the run controller reads a CA-only Secret

**Files:**
- Modify: `charts/maku/values.yaml:55-59` (`broker.tls`)
- Modify: `charts/maku/templates/run-controller-config.yaml:27`
- Modify: `charts/maku/templates/run-controller-rbac.yaml:60-70`
- Modify: `charts/maku/templates/_helpers.tpl` (`ap.validate`)
- Modify: `charts/maku/tests/render.sh` (lines 61, 71, the `prod` array and `ctl` block from Task 2, the `bad` list)
- Modify: `hack/quickstart/up.sh:100-101`, `hack/quickstart/check.sh` (after line 63)
- Modify: `charts/maku/README.md` (Values and Caveats)

**Interfaces:**
- Consumes: `runctl.(*Reconciler).inputs` reads key `ca.crt` of Secret `Broker.CASecret` in `Broker.Namespace` (`internal/runctl/reconciler.go:214`), and reports `Secret <ns>/<name> has no ca.crt` while it is missing. No Go change.
- Produces: value `broker.tls.caSecretName` (default `room-broker-ca`); helm error `broker.tls.caSecretName must not be broker.tls.secretName`.

- [ ] **Step 1: Write the failing assertions**

In `charts/maku/tests/render.sh`:

Line 61 (quickstart controller config): replace `.broker.caSecret == "room-broker-tls"` with `.broker.caSecret == "room-broker-ca"`, and the message `'controller config: runs, FQDN broker URL, broker labels, CA from broker.tls.secretName'` with `'controller config: runs, FQDN broker URL, broker labels, CA from broker.tls.caSecretName'`.

Line 71 (quickstart CA Role): replace the whole line with

```bash
ykind Role 'select(.metadata.name=="run-controller-ca") | (.metadata.namespace == null and (.rules[0].resourceNames | join(",")) == "room-broker-ca" and (.rules[0].verbs | join(",")) == "get")' 'get on the CA-only Secret by name, in the release namespace'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="secrets") | .resourceNames[] | select(. == "room-broker-tls")' 'no Role reads the broker key pair (S-3)'
```

In the `prod` array from Task 2, replace `--set runs.namePrefix=xplane-run- --set broker.tls.secretName=broker-cert` with

```bash
      --set runs.namePrefix=xplane-run- --set broker.tls.secretName=broker-cert --set broker.tls.caSecretName=broker-ca
```

In the `ctl` block from Task 2, replace its first two assertions with

```bash
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.namePrefix == "xplane-run-" and .broker.caSecret == "broker-ca" and .broker.url == "https://room-broker.agent-system.svc.corp.local:8443")' 'controller follows runs, broker.tls.caSecretName, clusterDomain'
ykind Role 'select(.metadata.name=="run-controller-ca") | (.rules[0].resourceNames | join(",")) == "broker-ca"' 'the CA Role follows broker.tls.caSecretName'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="secrets") | .resourceNames[] | select(. == "broker-cert")' 'no Role reads the key pair broker.tls.secretName names'
```

After the `bad 'runs.group …` line from Task 2, add

```bash
bad 'broker.tls.caSecretName must not be broker.tls.secretName' --set broker.tls.caSecretName=room-broker-tls
```

In `hack/quickstart/check.sh`, after line 63 (the `done` of the `modelSecret gitSecret` loop), add

```bash
# The run controller's CA Secret: up.sh creates the one the chart names, holding ca.crt only (S-3)
ca_secret=$(yq '.broker.tls.caSecretName' "$vals")
grep -qF "\"metadata\": {\"name\": \"$ca_secret\"}" "$qs/up.sh" || bad "up.sh does not create the CA Secret $ca_secret"
[[ $(yq '.broker.caSecret' - <<<"$cfg") == "$ca_secret" ]] || bad "the run controller does not read the CA from $ca_secret"
```

- [ ] **Step 2: Run them to verify they fail**

Run: `bash charts/maku/tests/render.sh; task quickstart:check`
Expected: render.sh exits 1 naming `controller config: … CA from broker.tls.caSecretName`, `get on the CA-only Secret by name …`, `no Role reads the broker key pair (S-3)`, the two `ctl` CA assertions and `no Role reads the key pair broker.tls.secretName names`, and `FAIL: --set broker.tls.caSecretName=room-broker-tls must not render`; check.sh fails with `FAIL: up.sh does not create the CA Secret null` (the value does not exist yet).

- [ ] **Step 3: Add the value and point the controller at it**

In `charts/maku/values.yaml`, under `broker.tls`, after `    secretName: room-broker-tls`, add:

```yaml
    # The run controller's copy of the broker's CA: a Secret in the release namespace holding
    # ca.crt only (an ExternalSecret, a trust-manager Bundle, or the quickstart's up.sh). Never
    # secretName: that Secret also holds the broker's tls.key.
    caSecretName: room-broker-ca
```

In `charts/maku/templates/run-controller-config.yaml:27`, replace the `caSecret:` line with:

```yaml
      caSecret: {{ required "broker.tls.caSecretName is required" .Values.broker.tls.caSecretName | quote }}
```

In `charts/maku/templates/run-controller-rbac.yaml`, replace line 60 (`# The broker's CA, copied into each run: the one Secret it reads outside the runs namespace.`) with

```yaml
# The broker's CA, copied into each run: the one Secret it reads outside the runs namespace, and
# ca.crt only: never broker.tls.secretName, whose tls.key would let the controller impersonate the broker.
```

and line 69 with

```yaml
    resourceNames: [{{ .Values.broker.tls.caSecretName | quote }}]
```

In `charts/maku/templates/_helpers.tpl`, inside `ap.validate`, after the Task 2 block, add:

```
{{- if and .Values.runController.enabled (eq ($b.tls.caSecretName | default "") $b.tls.secretName) -}}
{{- fail "broker.tls.caSecretName must not be broker.tls.secretName: the run controller needs ca.crt only, never the broker's tls.key" -}}
{{- end -}}
```

- [ ] **Step 4: Create the CA-only Secret in the quickstart**

In `hack/quickstart/up.sh`, after line 101 (`kubectl -n "$NS" wait --for=condition=Ready issuer/quickstart-ca --timeout=120s`), add:

```bash
# The run controller's copy of the CA (the chart's broker.tls.caSecretName): ca.crt only, never a key.
kubectl -n "$NS" get secret quickstart-ca -o json |
  yq -p=json -o=yaml '{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": {"name": "room-broker-ca"}, "data": {"ca.crt": .data."tls.crt"}}' |
  kubectl -n "$NS" apply -f -
```

(`quickstart-ca`'s `tls.crt` is the CA certificate the broker's `Certificate` is issued from; the CA Issuer is Ready only once that Secret exists.)

- [ ] **Step 5: Run them to verify they pass**

Run: `bash charts/maku/tests/render.sh && task quickstart:check`
Expected: `PASS`, `lib_test: ok`, `quickstart check: ok` (shellcheck included).

- [ ] **Step 6: Document the value and the upgrade step**

In `charts/maku/README.md`'s Values table, after the `broker.tls.issuerRef.name` row, add:

```markdown
| `broker.tls.caSecretName` | `room-broker-ca`: a Secret in the release namespace holding `ca.crt` only, which the run controller copies into each run. Bring it: an ExternalSecret, a trust-manager Bundle targeting a Secret, or the quickstart's `up.sh`. Never `broker.tls.secretName`, which also holds the broker's `tls.key`: the render refuses it |
```

In its Caveats table, add:

```markdown
| Upgrading with the run controller | Create the `broker.tls.caSecretName` Secret before upgrading: until it exists the controller starts no run and says `Secret <namespace>/room-broker-ca has no ca.crt` |
```

Run: `bash hack/docs.sh charts/maku/README.md`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add charts/maku/values.yaml charts/maku/templates/run-controller-config.yaml charts/maku/templates/run-controller-rbac.yaml \
  charts/maku/templates/_helpers.tpl charts/maku/tests/render.sh charts/maku/README.md hack/quickstart/up.sh hack/quickstart/check.sh
git commit -m "fix(chart): the run controller reads a CA-only Secret, never the broker's key pair

It needs ca.crt; its Role could get the broker's TLS Secret, tls.key
included. broker.tls.caSecretName (default room-broker-ca) names a
Secret with ca.crt only, the render refuses the key pair's name, and the
quickstart creates it from its CA. Audit S-3."
```

---

### Task 4: S-4, the broker CNP opens the factory's two flows

**Files:**
- Modify: `charts/maku/templates/broker-cnp.yaml` (head, ingress after line 23, egress after line 51)
- Modify: `charts/maku/values.yaml` (after `factoryURL`, and after T6's `factoryCA`/`factoryInsecure`)
- Modify: `charts/maku/tests/render.sh` (new block before `# a misconfiguration is a helm error`)
- Modify: `charts/maku/README.md` (Values)

**Interfaces:**
- Consumes: `broker.factoryURL` (`https://…` after T6); the factory pods' label `app.kubernetes.io/name: agent-factory`.
- Produces: value `broker.factoryPodLabels`.

- [ ] **Step 1: Write the failing assertions**

In `charts/maku/tests/render.sh`, insert before `# a misconfiguration is a helm error, for the intended reason: each case names its message`:

```bash
# the factory's two flows (S-4) open only with factoryURL: ingress to :8443 from its pods, egress to
# factoryURL's port on them, in the release namespace unless broker.factoryPodLabels names one
out="$q"
ynone 'select(.kind=="CiliumNetworkPolicy" and .metadata.name=="room-broker") | .spec.ingress[] | select(.fromEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory")' 'no factory ingress without factoryURL'
ynone 'select(.kind=="CiliumNetworkPolicy" and .metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory")' 'no factory egress without factoryURL'
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.factoryURL=https://agent-factory.x.svc.cluster.local:8082)"
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.ingress[] | select(.fromEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory") | (.fromEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "x" and .toPorts[0].ports[0].port == "8443")' 'the factory reaches :8443 from its pods in the release namespace'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory") | (.toEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "x" and .toPorts[0].ports[0].port == "8082")' "the broker reaches factoryURL's port on the factory pods"
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.factoryURL=https://factory.example \
  --set 'broker.factoryPodLabels.io\.kubernetes\.pod\.namespace=agent-system')"
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory") | (.toEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "agent-system" and .toPorts[0].ports[0].port == "443")' 'an https factoryURL without a port is 443; factoryPodLabels may name the namespace'
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bash charts/maku/tests/render.sh`
Expected: exit 1 with the three `ykind` messages above (`the factory reaches :8443 …`, `the broker reaches factoryURL's port …`, `an https factoryURL without a port is 443 …`); the two `ynone` pass.

- [ ] **Step 3: Add the value**

In `charts/maku/values.yaml`, after `broker.factoryURL` and the T6 keys beside it (`factoryCA`, `factoryInsecure`), add:

```yaml
  # The factory's pods, for the broker CNP's two factory flows (only when factoryURL is set):
  # ingress to :8443 from them, egress to factoryURL's port on them. Their namespace is the
  # release's unless these labels set io.kubernetes.pod.namespace.
  factoryPodLabels:
    app.kubernetes.io/name: agent-factory
```

- [ ] **Step 4: Write the two rules**

In `charts/maku/templates/broker-cnp.yaml`, insert before line 1 (`# Default deny; one allow per flow.`):

```yaml
{{- /* S-4: the factory's flows, keyed on factoryURL. Cilium matches L4 after service translation,
so the port is factoryURL's own, which must be the factory pod's (its chart maps 8082 to 8082). */}}
{{- $factory := dict -}}
{{- with .Values.broker.factoryURL -}}
{{- $u := urlParse . -}}
{{- $port := ternary "443" "80" (eq $u.scheme "https") -}}
{{- if regexMatch ":[0-9]+$" $u.host }}{{ $port = regexFind "[0-9]+$" $u.host }}{{ end -}}
{{- $labels := merge (deepCopy $.Values.broker.factoryPodLabels) (dict "io.kubernetes.pod.namespace" $.Release.Namespace) -}}
{{- $factory = dict "port" $port "labels" $labels -}}
{{- end -}}
```

After the kubelet probe ingress rule (`        - ports: [{port: "9090", protocol: TCP}]`, line 23), before `{{- with .Values.broker.networkPolicy.extraIngress }}`, insert:

```yaml
    {{- with $factory }}
    # The factory's system API calls (its rooms-system token).
    - fromEndpoints:
        - matchLabels: {{- toYaml .labels | nindent 12 }}
      toPorts:
        - ports: [{port: "8443", protocol: TCP}]
    {{- end }}
```

After the PostgreSQL egress rule (`        - ports: [{port: "5432", protocol: TCP}]`, line 51), before `{{- with .Values.broker.networkPolicy.extraEgress }}`, insert:

```yaml
    {{- with $factory }}
    # start_run: POST /v1/runs to factoryURL.
    - toEndpoints:
        - matchLabels: {{- toYaml .labels | nindent 12 }}
      toPorts:
        - ports: [{port: {{ .port | quote }}, protocol: TCP}]
    {{- end }}
```

(`merge` keeps the first dict's keys, so a namespace set in `factoryPodLabels` wins over the release's.)

- [ ] **Step 5: Run it to verify it passes**

Run: `bash charts/maku/tests/render.sh`
Expected: `PASS`.

- [ ] **Step 6: Document the value**

In `charts/maku/README.md`'s Values table, add:

```markdown
| `broker.factoryURL`, `broker.factoryPodLabels` | Set `factoryURL`, and the broker CNP lets the factory's pods (`factoryPodLabels`, default `app.kubernetes.io/name: agent-factory`, in the release namespace unless the labels set `io.kubernetes.pod.namespace`) reach `:8443`, and the broker reach them on `factoryURL`'s port. Cilium matches after service translation, so that port must be the factory pod's: 8082 with the factory chart |
```

Run: `bash hack/docs.sh charts/maku/README.md`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add charts/maku/templates/broker-cnp.yaml charts/maku/values.yaml charts/maku/tests/render.sh charts/maku/README.md
git commit -m "fix(chart): the broker CNP opens the factory's two flows when factoryURL is set

Default deny left no ingress on :8443 from the factory (its system API
calls) and no egress to factoryURL (start_run). Both now render from
factoryURL and broker.factoryPodLabels. Audit S-4."
```

---

### Task 5: S-2, an admission policy bounds the broker's AgentRun patch

**Files:**
- Create: `charts/maku/templates/broker-agentrun-policy.yaml`
- Create: `charts/maku/tests/admission/policy_test.go`
- Modify: `charts/maku/tests/render.sh` (new block before `# a misconfiguration is a helm error`)
- Modify: `charts/maku/templates/broker-rbac.yaml:40-41` (comment)
- Modify: `docs/security.md:100-101`, `charts/maku/README.md` (Caveats)

**Interfaces:**
- Consumes: `runwatch.OperatingModeAnnotation`, `runwatch.RemainingSecondsAnnotation`, `runwatch.ResumedAtAnnotation`, `runwatch.RevokedAnnotation` (string constants, `internal/runwatch/run.go:18-27`); the AgentRun CRD in `charts/maku/crds/`.
- Produces: cluster-scoped `ValidatingAdmissionPolicy` and `ValidatingAdmissionPolicyBinding`, both named `room-broker-<release namespace>-agentruns`.

- [ ] **Step 1: Write the failing envtest**

Create `charts/maku/tests/admission/policy_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

// Package admission proves the maku chart's AgentRun admission policy against a real API server
// (envtest): the broker's ServiceAccount may change the suspend annotations and nothing else
// (audit S-2, design D4). The policy is the chart's own render, never a copy.
package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Smana/maku/internal/runwatch"
)

const (
	release    = "maku"
	runsNS     = "agents"
	brokerUser = "system:serviceaccount:" + release + ":room-broker"
	runName    = "run-abcdefgh"
	usageKey   = "agents.ogenki.io/usage-tokens" // the meter's: the broker must never write it
	digest     = "v0.0.0@sha256:0000"
)

func TestTheBrokerMayChangeOnlyTheSuspendAnnotations(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	policy := render(t)
	ctrl.SetLogger(logr.Discard())
	e := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	admin, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// system:masters passes RBAC, never admission: the policy alone decides.
	u, err := e.AddUser(envtest.User{Name: brokerUser, Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := client.New(u.Config(), client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runsNS}}); err != nil {
		t.Fatal(err)
	}
	for _, o := range policy {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := admin.Create(ctx, agentRun()); err != nil {
		t.Fatal(err)
	}
	waitEnforced(t, broker)

	for _, tc := range []struct {
		name    string
		as      client.Client
		patch   map[string]any
		allowed bool
	}{
		{"the broker suspends a run", broker, ann(map[string]any{runwatch.OperatingModeAnnotation: "Suspended", runwatch.RemainingSecondsAnnotation: "600"}), true},
		{"the broker resumes a run", broker, ann(map[string]any{runwatch.OperatingModeAnnotation: "Running", runwatch.ResumedAtAnnotation: "2026-11-01T00:00:00Z"}), true},
		{"the broker drops a suspend annotation", broker, ann(map[string]any{runwatch.RemainingSecondsAnnotation: nil}), true},
		{"the broker changes the spec", broker, map[string]any{"spec": map[string]any{"budget": map[string]any{"maxTokens": 1}}}, false},
		{"the broker rewrites the meter's annotation", broker, ann(map[string]any{usageKey: "0"}), false},
		{"the broker drops the meter's annotation", broker, ann(map[string]any{usageKey: nil}), false},
		{"the broker revokes the run", broker, ann(map[string]any{runwatch.RevokedAnnotation: "broker"}), false},
		{"the broker adds an annotation of its own", broker, ann(map[string]any{"example.com/x": "y"}), false},
		{"the broker relabels the run", broker, map[string]any{"metadata": map[string]any{"labels": map[string]any{"agents.ogenki.io/task": "other"}}}, false},
		{"the broker adds a finalizer", broker, map[string]any{"metadata": map[string]any{"finalizers": []string{"example.com/hold"}}}, false},
		{"another writer changes the spec", admin, map[string]any{"spec": map[string]any{"budget": map[string]any{"maxTokens": 2}}}, true},
		{"another writer rewrites the meter's annotation", admin, ann(map[string]any{usageKey: "20"}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := patch(t, tc.as, tc.patch)
			switch {
			case tc.allowed && err != nil:
				t.Fatalf("refused: %v", err)
			case !tc.allowed && !deniedByPolicy(err):
				t.Fatalf("want the policy's refusal, got %v", err)
			}
		})
	}
}

// render is the chart's policy, as `helm template` renders it for release "maku".
func render(t *testing.T) []client.Object {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm is not on PATH: mise install")
	}
	chart := filepath.Join("..", "..")
	out, err := exec.CommandContext(t.Context(), helm, "template", "ap", chart, "-n", release,
		"-f", filepath.Join(chart, "values-quickstart.yaml"),
		"--set", "broker.image.tag="+digest, "--set", "runController.image.tag="+digest,
		"--set", "runController.images.harness.tag="+digest, "--set", "runController.images.bridge.tag="+digest,
		"--show-only", "templates/broker-agentrun-policy.yaml").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("helm template: %v: %s", err, ee.Stderr)
		}
		t.Fatal(err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	var objs []client.Object
	for {
		u := &unstructured.Unstructured{}
		if err := dec.Decode(&u.Object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(u.Object) > 0 {
			objs = append(objs, u)
		}
	}
	if len(objs) != 2 {
		t.Fatalf("want the policy and its binding, rendered %d objects", len(objs))
	}
	return objs
}

func agentRun() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "maku.ogenki.io/v1alpha1", "kind": "AgentRun",
		"metadata": map[string]any{"name": runName, "namespace": runsNS,
			"labels":      map[string]any{"agents.ogenki.io/task": "t1"},
			"annotations": map[string]any{usageKey: "10", runwatch.RemainingSecondsAnnotation: "900"}},
		"spec": map[string]any{"role": "implementer", "repository": "o/r", "baseRef": "main", "principal": "human:a",
			"dataClass": "public", "size": "small", "task": map[string]any{"text": "fix it"}},
	}}
}

func ann(kv map[string]any) map[string]any {
	return map[string]any{"metadata": map[string]any{"annotations": kv}}
}

// patch merge-patches the run, as internal/suspend does.
func patch(t *testing.T, c client.Client, body map[string]any) error {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("maku.ogenki.io/v1alpha1")
	u.SetKind("AgentRun")
	u.SetNamespace(runsNS)
	u.SetName(runName)
	return c.Patch(t.Context(), u, client.RawPatch(types.MergePatchType, b))
}

// deniedByPolicy is the policy's refusal, not the CRD's own validation.
func deniedByPolicy(err error) bool {
	return (apierrors.IsInvalid(err) || apierrors.IsForbidden(err)) && strings.Contains(err.Error(), "ValidatingAdmissionPolicy")
}

// waitEnforced returns once the API server enforces the policy: it loads policies asynchronously.
func waitEnforced(t *testing.T, broker client.Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := patch(t, broker, ann(map[string]any{"example.com/probe": time.Now().Format(time.RFC3339Nano)}))
		if deniedByPolicy(err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the policy was not enforced within 30 s: does this envtest API server run the ValidatingAdmissionPolicy plugin?")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
```

Notes for the implementer: `budget.maxTokens` is the one spec field the CRD leaves mutable, so a refusal of the broker's change can only be the policy's (`deniedByPolicy` also checks the message names `ValidatingAdmissionPolicy`). `time.Sleep` and `os/exec` are allowed in tests (`.golangci.yaml` exclusions).

- [ ] **Step 2: Write the failing render assertions**

In `charts/maku/tests/render.sh`, insert before `# a misconfiguration is a helm error, for the intended reason: each case names its message`:

```bash
# the AgentRun admission policy (S-2, D4): the broker's updates may touch the suspend annotations only.
# CEL is proven in charts/maku/tests/admission (envtest); here, its shape and binding.
out="$q"
ykind ValidatingAdmissionPolicy '(.spec.failurePolicy == "Fail" and (.spec.matchConstraints.resourceRules[0] | (.apiGroups[0] == "maku.ogenki.io" and (.operations | join(",")) == "UPDATE" and (.resources | join(",")) == "agentruns")))' 'the policy holds every AgentRun update, failing closed'
ykind ValidatingAdmissionPolicy '.spec.matchConditions[0].expression | test("^request.userInfo.username == .system:serviceaccount:maku:room-broker.$")' "the policy applies to the broker's ServiceAccount only"
KEYS="['agents.ogenki.io/operating-mode', 'agents.ogenki.io/remaining-seconds', 'agents.ogenki.io/resumed-at']" \
  ykind ValidatingAdmissionPolicy '.spec.variables[] | select(.name == "suspendKeys") | .expression == strenv(KEYS)' 'exactly the three suspend keys, never the agents.ogenki.io/ prefix the meter shares'
ykind ValidatingAdmissionPolicyBinding '(.spec.policyName == "room-broker-maku-agentruns" and (.spec.validationActions | join(",")) == "Deny" and .spec.matchResources.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "agents")' 'the binding denies, in the runs namespace'
out="$p"
ykind ValidatingAdmissionPolicy '(.metadata.name == "room-broker-agent-system-agentruns" and .spec.matchConstraints.resourceRules[0].apiGroups[0] == "cloud.ogenki.io")' "the policy follows runs.group and the release namespace"
```

- [ ] **Step 3: Run both to verify they fail**

Run:
```bash
export KUBEBUILDER_ASSETS="$(go tool setup-envtest use -p path 1.36.x)"
go test -race -count=1 ./charts/maku/tests/admission/
bash charts/maku/tests/render.sh
```
Expected: the Go test fails in `render` with `helm template: exit status 1: Error: could not find template templates/broker-agentrun-policy.yaml in chart`; render.sh exits 1 with the five policy messages above.

- [ ] **Step 4: Write the policy**

Create `charts/maku/templates/broker-agentrun-policy.yaml`:

```yaml
{{- $name := printf "room-broker-%s-agentruns" .Release.Namespace -}}
# The broker writes an AgentRun for one reason: the suspend trigger's merge patch of three
# annotations (internal/suspend; runwatch's OperatingMode, RemainingSeconds and ResumedAt keys).
# RBAC cannot bound a patch to fields, so this in-tree policy (Kubernetes 1.30+) does: an UPDATE
# by the broker's ServiceAccount may set, change or drop those three keys and nothing else. Exact
# keys, not the agents.ogenki.io/ prefix: the meter's, the factory's and the revoke annotations
# share it. Every other writer passes untouched.
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ $name }}
  labels: {{- include "ap.labels" . | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [{{ .Values.runs.group | quote }}]
        apiVersions: ["*"]
        operations: [UPDATE]
        resources: [agentruns]
  matchConditions:
    - name: the-broker
      expression: {{ printf "request.userInfo.username == 'system:serviceaccount:%s:room-broker'" .Release.Namespace | quote }}
  variables:
    - name: suspendKeys
      expression: "['agents.ogenki.io/operating-mode', 'agents.ogenki.io/remaining-seconds', 'agents.ogenki.io/resumed-at']"
  validations:
    - expression: >-
        has(object.spec) == has(oldObject.spec) && (!has(object.spec) || object.spec == oldObject.spec)
      message: the broker may not change an AgentRun's spec
    - expression: >-
        has(object.metadata.labels) == has(oldObject.metadata.labels) &&
        (!has(object.metadata.labels) || object.metadata.labels == oldObject.metadata.labels)
      message: the broker may not change an AgentRun's labels
    - expression: >-
        has(object.metadata.finalizers) == has(oldObject.metadata.finalizers) &&
        (!has(object.metadata.finalizers) || object.metadata.finalizers == oldObject.metadata.finalizers)
      message: the broker may not change an AgentRun's finalizers
    - expression: >-
        has(object.metadata.ownerReferences) == has(oldObject.metadata.ownerReferences) &&
        (!has(object.metadata.ownerReferences) || object.metadata.ownerReferences == oldObject.metadata.ownerReferences)
      message: the broker may not change an AgentRun's owners
    - expression: >-
        (!has(object.metadata.annotations) || object.metadata.annotations.all(k, k in variables.suspendKeys ||
        (has(oldObject.metadata.annotations) && k in oldObject.metadata.annotations && oldObject.metadata.annotations[k] == object.metadata.annotations[k]))) &&
        (!has(oldObject.metadata.annotations) || oldObject.metadata.annotations.all(k, k in variables.suspendKeys ||
        (has(object.metadata.annotations) && k in object.metadata.annotations)))
      message: the broker may change only the suspend annotations (operating-mode, remaining-seconds, resumed-at)
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ $name }}
  labels: {{- include "ap.labels" . | nindent 4 }}
spec:
  policyName: {{ $name }}
  validationActions: [Deny]
  matchResources:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: {{ .Values.runs.namespace }}
```

In `charts/maku/templates/broker-rbac.yaml`, replace lines 40-41 with:

```yaml
# AgentRuns: watched, deleted when their Room is, and merge-patched by the suspend trigger, whose
# patch broker-agentrun-policy.yaml bounds to three annotations. NEVER created: only the run
# requester creates runs.
```

- [ ] **Step 5: Run both to verify they pass**

Run:
```bash
go test -race -count=1 -v ./charts/maku/tests/admission/ 2>&1 | tail -15
bash charts/maku/tests/render.sh
helm lint --strict charts/maku -f charts/maku/values-quickstart.yaml --set broker.image.tag='v0.0.0@sha256:0000' --set runController.image.tag='v0.0.0@sha256:0000' --set runController.images.harness.tag='v0.0.0@sha256:0000' --set runController.images.bridge.tag='v0.0.0@sha256:0000'
```
Expected: `--- PASS: TestTheBrokerMayChangeOnlyTheSuspendAnnotations` with 12 passing subtests and `ok  github.com/Smana/maku/charts/maku/tests/admission`; `PASS`; `1 chart(s) linted, 0 chart(s) failed`.
If `waitEnforced` times out, the envtest API server does not enforce VAP: stop and report; the spec's fallback is the render assertions alone plus a manual check on the quickstart's kind cluster (`kubectl --as=system:serviceaccount:maku:room-broker -n agents annotate agentrun <name> example.com/x=y` must be refused).

- [ ] **Step 6: Correct the security doc and the chart caveats**

In `docs/security.md`, replace lines 100-101 (`- **The broker never creates `AgentRun`s**: its RBAC is …`) with:

```markdown
- **The broker never creates `AgentRun`s**: its RBAC is `get`, `list`, `watch`, `delete` and `patch`
  on `agentruns` in `agents`; CRUD on `rooms` in `agent-system`; leases; no cluster-admin. The `patch`
  is the suspend trigger's, and RBAC cannot bound it to fields: the maku chart's
  `ValidatingAdmissionPolicy` `room-broker-<namespace>-agentruns` refuses any update by the broker's
  ServiceAccount that changes more than the three suspend annotations
  (`agents.ogenki.io/operating-mode`, `agents.ogenki.io/remaining-seconds`,
  `agents.ogenki.io/resumed-at`): no spec, label, finalizer, owner or other annotation, the meter's
  included. An install without the chart needs the same policy for the same bound.
```

In `charts/maku/README.md`'s Caveats table, add:

```markdown
| `AgentRun` admission | A cluster-scoped `ValidatingAdmissionPolicy` and binding, `room-broker-<release namespace>-agentruns` (Kubernetes 1.30+): an update by the broker's ServiceAccount may change the three suspend annotations of an AgentRun and nothing else |
```

Run: `bash hack/docs.sh docs/security.md charts/maku/README.md`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add charts/maku/templates/broker-agentrun-policy.yaml charts/maku/templates/broker-rbac.yaml charts/maku/tests/admission/policy_test.go \
  charts/maku/tests/render.sh docs/security.md charts/maku/README.md
git commit -m "fix(chart): an admission policy bounds the broker's AgentRun patch to the suspend annotations

The broker holds patch on AgentRuns for the suspend trigger, and RBAC
cannot bound a patch to fields: a compromised broker could rewrite a
run's spec or its meter annotations. A ValidatingAdmissionPolicy lets
its ServiceAccount change the three suspend keys only; an envtest case
applies the rendered policy to a real API server. docs/security.md now
lists patch and its bound. Audit S-2, design D4."
```

---

### Task 6: The gate, and the pull request

**Files:** none changed.

- [ ] **Step 1: Run the whole gate once**

Run: `task check; echo "task check exit=$?"` (Docker running: the store tests use testcontainers)
Expected: `task check exit=0`. A failure is fixed in the task that owns the file, amended into that task's commit (`git commit --fixup <sha>` then `git rebase --autosquash main`), and the gate re-run.

- [ ] **Step 2: Review the branch**

Run: `git log --oneline main..HEAD`
Expected: exactly five commits, in order: `fix(chart): the factory's AgentRun Role follows config.runs`, `… refuse the run controller under a runs.group …`, `… CA-only Secret …`, `… the factory's two flows …`, `… an admission policy bounds …`; none carries a `Co-Authored-By` trailer (`git log main..HEAD --format=%B | grep -c Co-Authored-By` prints `0`).

- [ ] **Step 3: Open the pull request (only when the owner says to push)**

```bash
git push -u origin fix/audit-t7-charts
gh pr create --base main --title "fix(chart): audit T7, charts and RBAC (H-5, S-2 to S-5)" --body "$(cat <<'EOF'
Theme T7 of docs/superpowers/specs/2026-11-01-audit-fixes-design.md, one commit per finding.

| Finding | Fix | Pinned by |
|---|---|---|
| H-5 | The factory's AgentRun Role and binding follow `config.runs` | `charts/maku-factory/tests/render.sh` |
| S-5 | The maku chart refuses `runController.enabled` with `runs.group != maku.ogenki.io`; the production render that asserted that combination runs without the controller | `render.sh` `bad` case |
| S-3 | The run controller reads `broker.tls.caSecretName` (`ca.crt` only), never the key pair | `render.sh`, `hack/quickstart/check.sh` |
| S-4 | The broker CNP opens ingress `:8443` from and egress to the factory when `factoryURL` is set | `render.sh` |
| S-2 | A `ValidatingAdmissionPolicy` bounds the broker's AgentRun updates to the three suspend annotations (D4) | envtest `charts/maku/tests/admission`, `render.sh` |

Upgrade notes:
- With the run controller on, create the Secret `broker.tls.caSecretName` (default `room-broker-ca`, key `ca.crt` only) in the release namespace before upgrading; until then the controller starts no run.
- `runController.enabled` with a `runs.group` other than `maku.ogenki.io` now fails at render: turn the controller off there.
- The chart now installs a cluster-scoped ValidatingAdmissionPolicy and binding (Kubernetes 1.30+).

Spec deviations (exact keys rather than a prefix, the extra metadata fields, the CA Secret default) are in the plan, `docs/superpowers/plans/2026-11-01-audit-t7-charts-plan.md`.

`task check` exit 0.
EOF
)"
```

---

## Self-review

| Spec item | Task |
|---|---|
| H-5: Role `namespace`/`apiGroups` from `.Values.config.runs`, defaults today's; assertion with `group=maku.ogenki.io,namespace=runs2` | 1 |
| S-2 / D4: VAP + binding on the broker SA, `UPDATE` on `agentruns`, spec unchanged, annotations bounded; `docs/security.md` lists `patch` and its bound; envtest case since envtest 1.36.2 enforces VAP | 5 (keys exact: deviation 1) |
| S-3: controller reads `broker.tls.caSecretName`; Role's `resourceNames` never the key pair's | 3 |
| S-4: CNP ingress `:8443` from `broker.factoryPodLabels` and egress to the factory when `factoryURL` is set; default `app.kubernetes.io/name: agent-factory` | 4 |
| S-5: `fail` when `runController.enabled` and `runs.group != maku.ogenki.io`; `render.sh:82-100` corrected | 2 |
| D0: one PR, one commit per finding with its test, `task check` exit 0 | Global Constraints, 6 |
| T6 values referenced, not planned | Global Constraints, 2 (carry T6's flags), 4 (insert after T6's keys) |

The render.sh assertions, the four template changes, the quickstart CA step and the envtest file were each run on 2026-11-01 in a scratch export of `e14b7949` (`git archive`), not in this worktree: `render.sh` PASS, `quickstart check` clean but for the scratch copy's missing `mise.toml`, envtest PASS (12 subtests), and the `suspendKeys` assertion was seen to fail on a changed key.
