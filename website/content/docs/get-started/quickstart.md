---
title: "Quickstart"
weight: 10
description: "task demo reads each setting from the environment, .env, or .demo/answers.env, and prompts in a terminal only for what is missing (without a terminal it stops and names the."
diataxis: tutorial
aliases:
  - /docs/quickstart/
---

**`task demo` runs the factory loop on your laptop in about 15 minutes: label an issue, watch a room of agents open a pull request on your repository, and read the merge gate's verdict.** It is a local demo, not production — see [what production adds](#what-production-adds). Only point it at a public repository you own on purpose.

```bash
git clone https://github.com/Smana/maku && cd maku
task demo          # or ./hack/demo/up.sh if Task is not installed
```

## Before you start

| Need | Detail |
|---|---|
| Tools | Docker with `buildx`, Helm, kubectl, `curl`, `jq`, and k3d or kind (k3d wins when both exist). [Task](https://taskfile.dev) is optional (`./hack/demo/up.sh` runs directly). `gh` (logged in) is optional and automates repository setup |
| System | Linux or macOS (`amd64` and `arm64`, with Docker, Docker Desktop, OrbStack, or Colima). Runs use the default runtime (`runc`), not a sandbox |
| GitHub | An account and a **public** repository: the agent can read the model key and the App's token, so the demo refuses private repositories |
| Model | An OpenAI-compatible base URL, model name and API key (one model serves every role) |
| Network | Access to `ghcr.io/smana` and the GitHub API (60 unauthenticated requests/hour is enough) |

## Run it

`task demo` reads each setting from the environment, `.env`, or `.demo/answers.env`, and prompts in a terminal only for what is missing (without a terminal it stops and names the variable). Re-running it resumes from `.demo/` and upgrades in place when `MAKU_VERSION` changes.

| Step | What happens |
|---|---|
| preflight | Checks tools, settles `MAKU_VERSION`, and picks k3d or kind |
| model | Prompts for base URL, model name and API key (not echoed) |
| GitHub App | Opens a local browser flow (`127.0.0.1`) to create a private, webhook-free GitHub App (actions, contents, issues, pull requests and statuses write; checks and metadata read) and receives its key. Skipped if you supply an existing App |
| repository | With `gh` admin access, creates `DEMO_REPO` from [`Smana/maku-demo`](https://github.com/Smana/maku-demo), seeds labels (`factory/ready`, `class:docs-links`, `documentation`), issues, and the `maku-demo` ruleset (PRs + `ci` required); without `gh`, prints the template link and manual steps. Prompts you to install the App on `DEMO_REPO` |
| cluster | Creates `maku-demo` (kubeconfig in `.demo/`) or uses `CLUSTER=current` |
| install | Installs `charts/agent-factory-crds` and `charts/agent-factory -f values-demo.yaml` (images pinned by digest), including the model gateway (`agentgateway`, +128 MiB request per component; anonymous apiserver auth on k3d), and waits for rollout |
| room page | Port-forwards the broker to `http://localhost:8080` in the background and prints the room URL and issue to label |

The repository's default branch must contain `.github/agent-factory.yaml` with `factory: demo` (included in `Smana/maku-demo`), or the factory ignores it.

| Variable | Default | Meaning |
|---|---|---|
| `MODEL_BASE_URL`, `MODEL_NAME`, `MODEL_API_KEY` | asked | OpenAI-compatible endpoint, model name, and key (saved mode `0600` in `.demo/`, readable by the run pod) |
| `DEMO_REPO` | asked | Public `owner/name`, created from `Smana/maku-demo` when absent |
| `DEMO_LOGIN` | `gh` login, else asked | GitHub login allowed to trigger tasks |
| `GITHUB_APP_ID`, `GITHUB_APP_KEY_FILE`, `DEMO_SLUG` | unset | Existing App id, PEM path and URL slug; skips browser creation (permissions in [the factory page]({{< relref "/docs/reference/config.md#demo" >}})) |
| `CLUSTER` | `k3d`, else `kind` | `k3d`, `kind`, or `current` (confirmed interactively or via `DEMO_CONFIRM_CONTEXT=<context>`) |
| `MAKU_VERSION` | latest release (or saved) | Image, CLI, and chart release tag; auto-pulls published OCI charts if local charts changed since that release, or set to a PR pre-release tag to test local chart edits |
| `DEMO_LIVE` | saved, else `0` | `1` turns the `docs-links` [class]({{< relref "/docs/concepts/glossary.md#glossary" >}}) live so the gate merges (`DEMO_LIVE=0 task demo` reverts it) |
| `DEMO_NO_GH` | unset | Never call `gh` or write to GitHub |
| `K3S_IMAGE`, `KIND_NODE_IMAGE` | pinned | Cluster node image override |

## Watch the loop

1. Open the seeded `class:docs-links` issue and add **`factory/ready`**. The factory polls once a minute.
2. Open **http://localhost:8080**. A room appears with its steps in order: the [scoper]({{< relref "/docs/concepts/glossary.md#glossary" >}}), the implementer, the [judge]({{< relref "/docs/concepts/glossary.md#glossary" >}}), and the reviewer (`room_verdict`). The implementer shows a **Not sandboxed** banner.
3. `<slug>[bot]` opens a pull request on your repository and `ci` runs.
4. The gate posts **`maku/merge-gate`** with the class and reason. On the first run `docs-links` is in [shadow]({{< relref "/docs/concepts/glossary.md#glossary" >}}): the status stays `pending` (`shadow: would merge; a maintainer merges`) and only a factory merge posts `success`. Merge it yourself if you wish.
5. The second seeded issue carries `documentation` rather than `class:docs-links`, so the gate classes it `review` for a human merge (approve-to-merge is off in the demo). On an escalated task, a maintainer can comment `/factory retry` on the issue (polled every 5 minutes).
6. For an unattended merge, run `DEMO_LIVE=1 task demo`, then file a **new** `class:docs-links` issue (such as fixing another Markdown link) and add `factory/ready`. Once `ci` passes, the gate merges it.

## What production adds

The demo installs the production chart with `-f values-demo.yaml` (`demoValues: true`). It exercises intake, roles, rooms and the merge gate while omitting production isolation and identity seams:

| In the demo | In production |
|---|---|
| One GitHub App shared by the factory, merger and runs; runs can read its token and `maku/merge-gate` is advisory | Separate read-only factory App and merger App, per-role run tokens (reviewers cannot push), and a [required merge-gate ruleset]({{< relref "/docs/concepts/factory.md#branch-protection-for-the-merge-gate" >}}) |
| Provider API key reachable from runs; no meter or USD caps | Model gateway holds provider keys outside the sandbox, pins the judge's model via the identity proxy, meters tokens, and enforces per-run, fleet and per-person budgets |
| Default `runc` runtime ("Not sandboxed") and open run egress | gVisor sandbox and default-deny egress with per-run FQDN allow-lists |
| Room tools on `:8090` via `tokenreview` (`MCP_BARE_SERVER`, `MCP_READ_ONLY_TOOLS`) | Agent gateway authenticating `:8090` and injecting the MCP key |
| One local user, generated CA (plain in-cluster DB hop), public repositories only | OIDC login with GitHub-backed room access, your CA end-to-end (database included), and private repositories with data-class routing |

For a production cluster, follow [Install on your cluster]({{< relref "/docs/install/_index.md" >}}) (`maku install --print --out DIR` or `values-production.example.yaml`), [Install Secrets]({{< relref "/docs/install/install-secrets.md" >}}), and [From the demo to production]({{< relref "/docs/operate/upgrading.md#from-the-demo-to-production" >}}). To test multi-workflow selection on the demo cluster, see [Team evaluation]({{< relref "/docs/get-started/team-evaluation.md" >}}).

## Clean up

```bash
task demo:down           # stops port-forward, deletes cluster, clears answers/values; keeps .demo/app
task demo:down -- --all  # also deletes .demo/app and prints https://github.com/settings/apps/<slug>/advanced
```

Delete the GitHub repository and App yourself on GitHub. Under `CLUSTER=current`, run `helm uninstall af -n maku` and `kubectl delete ns maku agents`.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| "could not resolve a Maku release version ..." | Shallow clone or tarball without GitHub access: set `MAKU_VERSION=vX.Y.Z` to a published release tag or check connectivity to `api.github.com` |
| `mise ERROR ... mise.toml are not trusted` | Only affects shells using `mise`: run `mise trust` in the cloned directory |
| `curl: (22) ... 404` / "could not fetch maku <version>" on the App step | Expected until a release ships `maku demo app`: the script falls back to `go run ./cmd/maku demo app` (needs Go on `PATH`), or pass `GITHUB_APP_ID`, `GITHUB_APP_KEY_FILE` and `DEMO_SLUG` |
| "the repository is private" or GitHub API rate-limit error | Use a public `DEMO_REPO`; authenticate with `gh` or `GITHUB_TOKEN` if rate-limited |
| "<deployment> did not roll out" | Inspect the printed pod logs; commonly `MAKU_VERSION` is older than the chart |
| Missing `.github/agent-factory.yaml` or `gh` lacks admin rights | Commit `factory: demo` in `.github/agent-factory.yaml` on the default branch; create labels, issues and the ruleset manually if not admin |
| Factory log shows `a repository is not wired yet` (401/404) | Install the GitHub App on `DEMO_REPO`; the factory connects within a minute |
| "a previous run left a half-made App" or no terminal for browser flow | Delete the unfinished App and retry in a terminal, or pass `GITHUB_APP_ID`, `GITHUB_APP_KEY_FILE` and `DEMO_SLUG` |
| Context mismatch or missing `DEMO_CONFIRM_CONTEXT` (`CLUSTER=current`) | Re-confirm the active kubeconfig context, or set `DEMO_CONFIRM_CONTEXT=<context>` |
| Cluster `maku-demo` already exists, port `8080` busy, or nodes not Ready | Free port `8080`, set another `CLUSTER`, or run `task demo:down` and retry |
| `docker buildx` missing or image not found | Install `buildx`, or set `MAKU_VERSION` to a published tag |
| Nothing happens after labelling | Ensure `DEMO_LOGIN` added `factory/ready`; wait up to 1 minute for polling |
