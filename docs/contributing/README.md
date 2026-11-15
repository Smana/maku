<!-- page-type: how-to -->
# Contributing notes

**[CONTRIBUTING.md](../../CONTRIBUTING.md)** covers workflow, checks, commits, migrations and releases; **[AGENTS.md](../../AGENTS.md)** covers package boundaries, conventions, tests and security rules. The pages here hold contributor-only procedures and harnesses:

| Page | Covers |
|---|---|
| [Development](development.md) | The Postgres store test harness, migration checks, the `web/` build and offline UI preview, and the `demo-e2e.yaml` install jobs |
| [The one-run quickstart](one-run-quickstart.md) | `task quickstart` (one attended gVisor run via `maku run`, no factory), installing the demo chart without the script, and the `quickstart-e2e` CI job |
| [Stubbed spec-intake functional test](intake-functional-test.md) | Running `task chart:intake:k3d` locally against in-cluster stubs |
