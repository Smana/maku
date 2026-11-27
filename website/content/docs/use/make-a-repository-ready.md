---
title: "Make a repository ready"
weight: 20
description: "A task's agent works in a sandbox. Before it pushes, it runs your repository's own checks there, so the pull request you read has already been built and tested. Three files on."
diataxis: how-to
aliases:
  - /docs/make-a-repository-ready/
---

A task's agent works in a sandbox. Before it pushes, it runs your repository's own checks there, so the
pull request you read has already been built and tested. Three files on the default branch make that
possible. Without them, a run still opens a pull request, but nothing in the sandbox tested it.

`maku repo check owner/name` warns when any of the three is missing.

## What the sandbox gives a run

| It has | It does not have |
|---|---|
| One platform image with `git`, `bash`, `mise` and Python | Your own image (planned: a per-repository runtime) |
| GitHub, and the package registries the platform grants the repository: PyPI, npm, the Go proxy, crates.io, Maven Central and Gradle (`maven`), Node's downloads (`node`) | A private feed, or any other host |
| 2 CPU and 4 GiB by default, 10 GiB of scratch | Docker, Testcontainers, a database or any other service |
| 8 minutes of checks by default, 15 at most | Your CI's secrets |

The platform team grants the registries on the repository's `Repository` object
([`checks`]({{< relref "/docs/reference/config.md" >}})); your files never name a host.

## The three files

### `mise.toml`: the tools

The sandbox installs exactly the versions pinned here before it runs a step. Pin every tool a check runs:

```toml
[tools]
go = "1.27.4"
golangci-lint = "2.14.0"
```

### `.github/agent-checks.yaml`: what an agent runs before it pushes

The fast subset of your CI. The sandbox reads it from the default branch only, so a branch cannot change
what it is held to.

```yaml
version: 1
timeoutMinutes: 12            # the whole run, 15 at most
tools: [go, golangci-lint]    # each pinned in mise.toml
definedBy: [mise.toml, go.mod, .golangci.yaml]
steps:
  - {name: build, run: go build ./..., ci: test, stopOnFailure: true}
  - {name: unit, run: go test -short ./..., ci: test}
  - {name: lint, run: golangci-lint run, ci: lint}
ciOnly:
  - {check: test, part: "integration tests", why: "they need Postgres; the sandbox has no Docker"}
```

| Key | Means |
|---|---|
| `steps` | Each runs with `bash -c` in the repository's root. `ci` names the CI check the step stands for. `stopOnFailure` skips the later steps when this one fails |
| `tools` | The `mise.toml` tools the steps need |
| `definedBy` | Files that change how the checks behave. A branch that touches one is told its result is unverified until CI runs |
| `ciOnly` | What the sandbox cannot run, and why. Each entry has exactly `check`, `part` and `why` |
| `tests` | Where a tester may write tests first (`paths`, `runner`); optional |

**What belongs in `ciOnly`:** anything that needs Docker, a service (a database, a queue), a secret, a host
outside the registries above, or a long time. List it rather than leave it out: the first comment of every
task names these steps, and says that CI is the judge of a merge.

A test that needs a service should skip, not fail, when the service is absent (for example, skip when
`DATABASE_URL` is unset). The sandbox removes `CI` from a step's environment so such tests skip.

### `AGENTS.md`: how to work in this repository

A few lines: how to build, test and lint, where things live, and what a change must come with. Agents read
it before they start.

## Worked files

One ready repository per stack, under [`examples/agent-ready/`](https://github.com/Smana/maku/tree/main/examples/agent-ready):

| Stack | Steps | `ciOnly` | Registry the platform grants |
|---|---|---|---|
| [Go](https://github.com/Smana/maku/tree/main/examples/agent-ready/go) | build, unit, lint | integration tests on Postgres | `golang` |
| [Node](https://github.com/Smana/maku/tree/main/examples/agent-ready/node) | `npm ci`, `tsc`, Vitest, ESLint | Playwright | `npm`, `node` |
| [Python](https://github.com/Smana/maku/tree/main/examples/agent-ready/python) | `uv sync`, pytest, ruff | warehouse tests (DuckDB fixtures run instead) | `pypi` |
| [JVM](https://github.com/Smana/maku/tree/main/examples/agent-ready/jvm) | `./mvnw` resolve, unit tests offline | Testcontainers | `maven` |

## Check it

```bash
maku repo check acme/my-service
```

```text
PASS  readiness      .github/agent-checks.yaml, mise.toml and AGENTS.md are on the default branch
```

A missing file prints `WARN  readiness      missing on the default branch: …` and the fix for the first one.
