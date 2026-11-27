---
title: "12. Automate repository upkeep"
linkTitle: "12. Upkeep routines"
weight: 120
diataxis: how-to
description: "Browse the routine catalog, configure schedules and prompts, monitor yield, and check versions in Settings."
---

Upkeep routines run recurring repository maintenance — broken links, failing Renovate pull requests, documentation drift, unreachable code, flaky tests, and duplicated helpers — inside hard spend, open-PR, and yield boundaries.

Every `Routine` object stays inert until a maintainer binds it under a repository's `spec.routines[]`. Upkeep routines never auto-merge on their own: automatic merges still require a `live` [merge policy]({{< relref "merge-policies.md" >}}). Full starter catalog tables, custom CRD specs, and CLI commands live in [Use › Upkeep routines]({{< relref "/docs/use/upkeep-routines.md" >}}), [The factory]({{< relref "/docs/concepts/factory.md#upkeep-routines" >}}), and [Web UI reference]({{< relref "/docs/reference/web-ui-settings.md#routines" >}}).

## Browse the catalog and enable a routine

Open **Routines** (`/routines`) in the rooms web UI and choose **Browse catalog** (`/routines/catalog`) to inspect every shipped (`catalog`) and cluster-authored (`custom`) `Routine`.

{{< themed-img light="/images/web-ui/routines-catalog-1280-light.png" dark="/images/web-ui/routines-catalog-1280-dark.png" alt="Routine catalog showing six starter routines with probe type, workflow, open-PR and cost ceilings, breaker rules, active consumers, and a target repository selector." >}}

1. Pick your target repository in the selector.
2. Review each routine's probe, workflow, cost ceiling, and required fill-once parameters (`oracleClass` or `probeWorkflow`).
3. Click **Enable on repository →** (or **Configure on repository →** for an existing binding) to open the guided configurator.

## Configure mode, schedule, and prompt

The configurator (`/routines/<owner>/<repo>/<routine>/edit`) walks through delivery mode, UTC cron schedule (with live local and UTC previews of the next three runs), bounds (which can only tighten the `Routine`'s ceilings), and an optional repository-owned prompt (`.github/agent-factory/upkeep/<routine>.md`, validated live for size, sections, and a `## Verify` checklist).

{{< themed-img light="/images/web-ui/routines-configurator-1280-light.png" dark="/images/web-ui/routines-configurator-1280-dark.png" alt="Guided Routine Configurator showing delivery mode cards, UTC cron schedule with next runs preview, bounds, split-view prompt editor, and Check and Generate YAML button." >}}

1. Start in **Propose** or **Draft PR** until the routine produces consistent patches.
2. Click **Check & Generate YAML** to validate against the factory and copy or download the `Repository` manifest snippet for GitOps.
3. When an operator enables `routines.uiWrite: true`, **Save to cluster** persists UI-managed bindings into ConfigMap `agent-factory-routine-bindings` while keeping GitOps-declared bindings immutable in the cluster.

## Monitor yield and control runs

The **Routines** overview (`/routines`) sorts routines needing attention (`paused` or `half-open`) first, displaying 14-day outcome sparklines, rolling merge rate, cost per merged PR, open PR count, and the next scheduled run.

{{< themed-img light="/images/web-ui/routines-overview-1280-light.png" dark="/images/web-ui/routines-overview-1280-dark.png" alt="Routines overview listing bound routines with 14-day outcome sparklines, merge rate, cost per merged PR, open PR counts, breaker badges, and Run now, Pause, and Configure buttons." >}}

Maintainers with GitHub write access can trigger **Run now** (with an optional one-shot mode override), **Pause**, or **Resume**, or run `maku routine list`, `maku routine run`, and `maku routine pause|resume` from the terminal.

## Check component versions and environment

Open **Settings** (`/settings`) to view the **Environment** panel below the section cards. It prints the broker and factory versions (flagging version skew), factory name, namespace, sign-in mode, default harness, merge policy stage, hardening mode, routines UI write setting, and active install notices.

{{< themed-img light="/images/web-ui/settings-env-1280-light.png" dark="/images/web-ui/settings-env-1280-dark.png" alt="Settings home displaying the Merge policies, Evidence, and Approval memory cards above the Environment panel with broker and factory versions and cluster settings." >}}

**Next:** keep the [cheat sheet]({{< relref "cheat-sheet.md" >}}) handy for every label, comment, Slack command, MCP tool, and `maku` command on one page.
