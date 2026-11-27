---
title: "Contributing notes"
linkTitle: "Contributing"
weight: 80
description: "Maku documentation."
diataxis: how-to
---

**[CONTRIBUTING.md](https://github.com/Smana/maku/blob/main/CONTRIBUTING.md)** covers workflow, checks, commits, migrations and releases; **[AGENTS.md](https://github.com/Smana/maku/blob/main/AGENTS.md)** covers package boundaries, conventions, tests and security rules. The pages here hold contributor-only procedures and harnesses:

| Page | Covers |
|---|---|
| [Development]({{< relref "/docs/contributing/development.md" >}}) | The Postgres store test harness, migration checks, the `web/` build and offline UI preview, and the `demo-e2e.yaml` install jobs |
| [The one-run quickstart]({{< relref "/docs/contributing/one-run-quickstart.md" >}}) | `task quickstart` (one attended gVisor run via `maku run`, no factory), installing the demo chart without the script, and the `quickstart-e2e` CI job |
| [Stubbed spec-intake functional test]({{< relref "/docs/contributing/intake-functional-test.md" >}}) | Running `task chart:intake:k3d` locally against in-cluster stubs |
