---
title: "Upkeep routines"
weight: 70
description: "Upkeep routines run recurring repository maintenance — broken links, failing Renovate pull requests, documentation drift, unreachable code, flaky tests, and duplicated helpers."
diataxis: how-to
aliases:
  - /docs/upkeep-routines/
---

Upkeep routines run recurring repository maintenance — broken links, failing Renovate pull requests, documentation drift, unreachable code, flaky tests, and duplicated helpers — inside hard spend, open pull-request, and yield boundaries.

Every routine stays inert until you explicitly bind it on a `Repository` under `spec.routines[]`. Upkeep never auto-merges on its own: automatic merges still require a `live` [merge policy]({{< relref "/docs/use/merge-policies.md" >}}).

## How routines stay bounded

| Guardrail | Behavior |
|---|---|
| **Explicit opt-in** | Unbound `Routine` definitions never schedule or trigger runs |
| **Isolated upkeep budget** | Charged against `budgets.upkeepDailyUSD` (independent of human and interactive budgets) |
| **Open PR cap and cooldown** | Skips a slot when `maxOpenPRs` is reached or inside `cooldown` (`open_cap`, `cooldown`) |
| **Zero-token probes** | `dead-links`, `renovate-red`, and `actions-findings` probes check deterministically before spending model tokens |
| **Progressive delivery modes** | Start in `propose` (read-only issue) or `draft` (draft PR) before graduating to `pr` |
| **Yield breaker** | Trips `half_open` or `paused` when consecutive closes/failures cross thresholds or rolling merge rate drops |
| **Ceilings on `Routine`** | A `Repository` binding can only tighten `maxOpenPRs`, `maxCostUSD`, `cooldown`, `egress`, and breaker bounds — widening is refused |

## Starter catalog

The `agent-factory` Helm chart installs six starter `Routine` objects in the factory's namespace (`routines.catalog.enabled: true`, labelled `maku.ogenki.io/catalog`):

| Routine | Probe | Workflow | Default ceilings | Requires |
|---|---|---|---|---|
| `dead-links` | `dead-links` | `quick` | 1 open PR · $1/run · 24h | `oracleClass` |
| `renovate-red` | `renovate-red` | `standard` | 1 open PR · $2/run · 24h | — |
| `doc-drift` | `none` | `standard` | 1 open PR · $2/run · 24h | — |
| `dead-code` | `actions-findings` | `standard` | 1 open PR · $2/run · 24h | `probeWorkflow` |
| `flaky-tests` | `actions-findings` | `standard` | 1 open PR · $2/run · 24h | `probeWorkflow` |
| `dup-unifier` | `none` | `standard` | 1 open PR · $2/run · 24h | — |

### Adding a custom routine

Apply any `Routine` (`maku.ogenki.io/v1alpha1`) custom resource in the factory's namespace. It appears immediately in `/routines/catalog` labelled `custom`:

```yaml
apiVersion: maku.ogenki.io/v1alpha1
kind: Routine
metadata:
  name: license-headers
  namespace: agent-system
spec:
  description: "Ensure new source files carry the Apache-2.0 SPDX header."
  workflow: quick
  dataClass: public
  defaultPrompt: |
    Add the missing Apache-2.0 SPDX header to tracked source files reported below.
    If no file is missing a header, make no changes.
  probe:
    kind: actions-findings
    requireConsecutive: 2
    maxFindings: 5
  requires: [probeWorkflow]
  maxOpenPRs: 1
  maxCostUSD: 1
  cooldown: 24h
  protectedPaths:
    - '^\.github/agent-factory/'
  breaker:
    window: 10
    minSamples: 5
    minMergeRate: 0.25
    maxConsecutiveClosed: 3
    maxConsecutiveFailures: 3
```

To replace a shipped catalog routine with your own manifest, add its name to `routines.catalog.exclude` in the chart values.

## Managing routines in the Web UI

### Overview (`/routines`)

Monitor every bound routine across repositories you can read on GitHub. Cards sort attention items (`paused`, `half-open`) first and show 14-day outcome sparklines, rolling merge rate, `$/merged`, open PR count, and next UTC run. Maintainers with write access can override the mode and click **Run now**, **Pause**, **Resume**, or **Configure**.

{{< themed-img light="/images/web-ui/routines-overview-1280-light.png" dark="/images/web-ui/routines-overview-1280-dark.png" alt="Routines overview showing three bound routines with 14-day outcome sparklines, merge rate, cost per merged PR, open PR counts, breaker badges, and Run now, Pause, and Configure controls" >}}

### Routine catalog (`/routines/catalog`)

Browse shipped (`catalog`) and cluster-authored (`custom`) `Routine` definitions, pick a target repository, inspect ceilings and active consumers, and click **Enable on repository →** or **Configure on repository →**.

{{< themed-img light="/images/web-ui/routines-catalog-1280-light.png" dark="/images/web-ui/routines-catalog-1280-dark.png" alt="Routine catalog listing the six starter routines with probe type, workflow, ceilings, breaker rules, active consumers, and a target repository selector" >}}

### Guided configurator (`/routines/<owner>/<repo>/<name>/edit`)

Configure delivery mode (`propose`, `draft`, `pr`, `disabled`), pick a UTC cron schedule with live local and UTC previews of the next three runs, adjust bounds inside the `Routine` ceilings, and author a repository prompt (`.github/agent-factory/upkeep/<routine>.md`) with live validation (`<= 4096` bytes, `<= 5` `## ` sections, a `## Verify` checklist, and no control or invisible characters).

Click **Check & Generate YAML** to validate against the factory and copy or download `repository-<name>.yaml` for GitOps. When `routines.uiWrite: true` is enabled on the factory, **Save to cluster** persists UI-managed bindings into ConfigMap `agent-factory-routine-bindings`; GitOps-defined bindings stay read-only in the cluster (`409 defined_twice`).

{{< themed-img light="/images/web-ui/routines-configurator-1280-light.png" dark="/images/web-ui/routines-configurator-1280-dark.png" alt="Guided Routine Configurator showing delivery mode cards, UTC cron schedule with next three runs preview, bounds and parameters, split-view prompt editor, and Check and Generate YAML action" >}}

## CLI, prompts, and Slack

- **CLI:** run `maku routine list`, `maku routine run <owner/name> <routine> [--mode propose|draft|pr] [--dry-run]`, `maku routine pause|resume <owner/name> <routine>`, and `maku config export --routines` to export UI-stored bindings back to GitOps manifests ([CLI reference]({{< relref "/docs/use/cli.md" >}})).
- **Repository prompts and skills:** commit `.github/agent-factory/upkeep/<routine>.md` on the default branch (`repoPrompt: true`) or point `skill:` at `.agents/skills/<skill>/SKILL.md` to tailor instructions without editing cluster manifests.
- **Slack upkeep channel and weekly digest:** configure `upkeepChannel` on [maku-slack]({{< relref "/docs/use/slack.md" >}}) to route routine proposals, breaker trips, and weekly summaries (`GET /api/routines/digest`) to a dedicated channel.

Full field schemas, skip codes, and API contracts live in [The factory]({{< relref "/docs/concepts/factory.md#upkeep-routines" >}}), [Web UI reference]({{< relref "/docs/reference/web-ui-settings.md#routines" >}}), and [API reference]({{< relref "/docs/reference/api.md" >}}).
