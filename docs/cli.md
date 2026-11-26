# maku

`maku` is a room in a terminal: read where a task stands, follow the log, post a note, fork a room. It never steers a run, interrupts it, moves the driver token or approves anything (`status` may print an approve command, for display only); those stay in the [web UI](web-ui.md). On a local install (the quickstart) `run` also starts one.

It calls the broker through the platform's sign-in proxy with your own access token; the broker filters what it returns, so it sees only the rooms your GitHub identity may read.

## Install and verify

Download the binary for your system from the latest GitHub release, with `maku.sha256`:

| Asset | System |
|---|---|
| `maku-linux-amd64`, `maku-linux-arm64` | Linux |
| `maku-darwin-amd64`, `maku-darwin-arm64` | macOS |
| `maku.sha256` | the checksums of all four |

```bash
sha256sum -c --ignore-missing maku.sha256
sudo install maku-linux-amd64 /usr/local/bin/maku
```

From a checkout: `go build -o maku ./cmd/maku`. The release workflow is `.github/workflows/release.yaml`.

## Configure and sign in

For a coding agent, one command runs the whole setup — configuring, signing in, registering the
`maku` MCP server and skill, and running the checks:

```bash
maku setup claude                             # or codex, copilot, cursor, gemini, opencode
```

To set up the terminal CLI on its own:

```bash
maku configure --url https://rooms.<domain>   # the broker serves the other three values
maku login                                    # device flow: open the printed URL, enter the code
maku doctor                                   # check the setup
```

- `maku setup <client>` runs in order, skipping what is already done: `configure` (asking for the rooms URL when there is no config), `login`, registering `maku mcp` as the `maku` MCP server (rewriting an existing `factory` entry to `maku` and printing a note), installing the `maku-handoff` skill and `/maku` command, and `doctor --client <client>`.
- `configure` reads the issuer, client id and project id from the broker's unauthenticated `/.well-known/maku`. Pass `--issuer`, `--client-id` or `--project-id` to override one. `--factory-login LOGIN` saves the factory App's GitHub login (for example `my-factory[bot]`): `answer` trusts public bounce comments from that login alone when no room question is readable. If it is empty, including in an older config, the public fallback refuses securely while authorized room questions remain available. The URL must be `https`, except for a local install (`humanMode: local`), whose listener is the `kubectl port-forward`: `http://localhost:<port>` is accepted on `localhost` or a loopback address only, and anything else in clear is refused before any request.
- `configure --from <rooms URL>` replaces the whole config with what the broker serves now (a room page's URL works: only its origin is used). Use it after a platform rebuild: a stale client id makes the identity provider answer `invalid_client`, and `maku login` then prints this fix. It refuses a non-`https` URL or issuer — a local install configures with `--url http://localhost:<port>` instead — and cannot be combined with the other broker-value flags. `--factory-login` is the one override: it sets the public-comment trust identity alongside the discovered values. It prints the four broker values it will write (and the old ones when they change) and, on a terminal, asks `y/N`; `--yes` skips the question. An issuer outside the URL's registrable domain (`rooms.ogenki.io` and `auth.ogenki.io` share one) is refused unless `--yes` says you checked it.
- A local broker needs no `login` and takes no token: `login` says so, `token` says so, and no request carries an `Authorization` header.
- The config and token files sit in `<user config dir>/maku/` (`config.json`, `token.json`), owner-only. The directory a CLI before v0.30.0 wrote (formerly `roomctl/`, beside it) is copied there once, the first time `maku` runs without one; the old directory stays.
- `login` prints `Open <verification URL> and enter the code …` (the URL is the one your identity provider returns), then `logged in; …`.
- `doctor` prints one line per check, `ok` or the failure with its fix: `dns`, `tls`, `login`, `github`, `skill`, and `factory capabilities`. When your account is not yet linked to GitHub, `github` prints `<rooms URL>/link/github` so you can link it directly (or ask an admin to link it at your identity provider); when the room list cannot prove the link on its own, `github` probes up to three of your rooms' repositories and prints `warn`, never claiming "linked" unproven. `skill` checks the installed `maku-handoff` skill. `factory capabilities` reports what a coding client declared when it opened `maku mcp`, from `capabilities.json` beside the config: whether it answers with forms, with URL mode, with its roots and with prompts, and the protocol revision it asked for, with the day maku measured it. `--client <name>` names one client; a name no session ever gave reads `not measured yet — run a session and ask again`, and nothing else is printed about it. Without the flag, the five clients measured most recently are listed, the last one first. It reads a file, so it answers with the VPN down or the URL wrong, and it never fills a client maku has not met from a guess.

## Shell completion

`maku completion <bash|zsh|fish>` prints a completion script for the commands, subcommands and flags `maku --help` lists, and the values it spells: the `setup` clients, `--format md|text`, `--output json`. It is generated from the help text, so a new command completes with no second list to keep. Room and task ids are not completed: the only source is the broker, and a network call on every Tab would be too slow.

```bash
# bash, in ~/.bashrc
source <(maku completion bash)
# zsh, in ~/.zshrc after compinit; or save it as _maku in a directory on $fpath
source <(maku completion zsh)
# fish
maku completion fish > ~/.config/fish/completions/maku.fish
```

## Commands

`maku help` (or `maku --help`) prints the everyday commands; `maku help admin` prints the platform team's (`install`, `db`, `preflight`, `repo`, `demo`, `config`, `policy`, `drill`, `fork`, `run`, `stages`, `workflow`, `routine`); `maku help <command>` and `maku <command> --help` print one command. Together they are the list below. A `<room>` is a room id, the last part of a room page's `…/r/<room>` URL. The MCP tools and prompts also take `#<issue>`, `PR #<n>`, a bare number or a title fragment ([below](#the-mcp-server)); the CLI takes ids only, since a shell drops an unquoted `#42`.

| Command | What it does |
|---|---|
| `configure --url URL [--issuer URL --client-id ID --project-id ID --factory-login LOGIN]` | Saves the config. The broker's URL is enough; the factory login enables the trusted public-comment fallback |
| `configure --from URL [--yes] [--factory-login LOGIN]` | Replaces the config with the values the broker serves now, whatever is stored, with an optional factory-login override |
| `login` | Signs in with the device flow |
| `setup <claude\|codex\|copilot\|cursor\|gemini\|opencode> [--advanced] [--project] [--print] [--restart-service]` | Runs the whole setup in order, skipping what is already done: `configure` (asking for the rooms URL when there is no config), `login`, registering `maku mcp` (or `maku mcp --advanced` with `--advanced`) as the `maku` MCP server (rewriting an existing `factory` entry to `maku` and printing a note), writing the `maku-handoff` skill and, for `claude` and `opencode`, the short `/maku` command ([below](#short-commands)), and running `doctor --client`. `--project` writes under the repository's top instead of your home; `--print` shows what it would do and writes nothing |
| `completion <bash\|zsh\|fish>` | Prints a shell completion script ([above](#shell-completion)) |
| `install --print --out DIR [--answers FILE] [--force] [--no-wait]` | Writes a production install of the umbrella chart and applies nothing. It asks the seam questions one per line (`[default]` on Enter), or reads them all from `--answers`, a YAML map keyed by question id, decoded strictly: an unknown key, a value of the wrong kind, or an answer the other answers leave out is refused. Every answer is checked against the charts' own `values.schema.json` (embedded, kept equal by `task chart:check`), so the wizard refuses what `helm template` would. It asks for what the factory needs to start outside the demo: a meter URL and at least one repository, and offers `database.mode` (`external`, `bundled` for Tier-1 Pilot single-replica Postgres with TLS 1.3, or `cnpg` to also write `database/cnpg-cluster.yaml`). With `apps.create`, it creates the agents App and the factory App (the merger folded in, the GitHub account link's callback) through one manifest flow and waits until you install both (`--no-wait` skips the wait). DIR gets `values.yaml` (the production defaults, images by digest, no secret value), `crds/values.yaml`, `factory/` (the `Factory`, its `spec.mergeDefaults` the merge settings every repository shares: `mergerLogin` the factory App's bot, `verifyFor: 30m`, `revertWindow: 168h`, `breaker` 1 revert in 10; a `Repository` each, the classes' `MergePolicy` objects, every class in shadow), `github/` (the merge gate's ruleset and the `gh` commands, never run), `gitops/flux.yaml` and `gitops/argocd.yaml`, a `README.md` with the commands in order, and `secrets/` (mode 0700, files 0600, its own `.gitignore`): the Apps' keys and OAuth client, a generated cookie secret and webhook key, and templates with empty keys for what it never asks (the DSNs unless `database.mode` is `bundled`, the provider key, the IdP's client secret, Slack's). It refuses a non-empty DIR without `--force`, and never overwrites `secrets/`, before it asks anything or creates an App. The factory's API runs on TLS from `tls.issuer` (a cert-manager ClusterIssuer); without one, only an explicit `factory.insecure: true` accepts plaintext. `--out -` prints `values.yaml` alone, generates no key and names the Secrets you create instead; it is refused when Apps would be created. Bare `maku install` is refused |
| `db bootstrap --out FILE [--namespace NS] [--admin-dsn-env MAKU_DB_ADMIN_URL] [--host HOST] [--port PORT] [--database rooms] [--sslmode verify-full] [--ca-secret NAME] [--rotate-passwords] [--force]` | Bootstraps an external PostgreSQL 16 or later server for Maku: reads the admin DSN from the environment (never argv), creates the three fixed roles (`rooms_owner`, `rooms_broker`, `rooms_retention`) with generated passwords, creates the `rooms` database owned by `rooms_owner` (granting `rooms_owner` to `CURRENT_USER` automatically for non-superuser cloud admins on RDS, Cloud SQL and Azure), grants `CREATE, USAGE` on schema `public`, verifies that none of the roles bypasses row-level security, and writes the three DSN `Secret` manifests to `FILE` (mode 0600; `-` for stdout). Refuses existing roles unless `--rotate-passwords` is passed, and refuses to overwrite `FILE` without `--force` ([the database](database.md#bootstrap)) |
| `preflight [--namespace NS] [--release NAME] [--kubeconfig FILE] [--context NAME] [--strict] [--json] [--keep] [--no-outside] [--timeout D]` | Runs a release's install preflight from your machine: it creates a Job, `maku-preflight-<rand>`, from the release's `<release>-preflight` CronJob (rendered by `maku.preflight.enabled`; without it, it says so and exits 2), owned by that CronJob, waits for it, reads its pod's JSON lines and deletes the Job unless `--keep`. It adds the outside checks, run from where you stand: each hostname of the release's `rooms-web` and `factory-slack-events` routes (HTTPRoute or Ingress) resolves, the web host answers oauth2-proxy's `/ping` and serves `/.well-known/maku` through it, and the Slack host answers an unsigned `POST /slack/events` with 401 and a `GET` with no 2xx (`--no-outside` skips them). It prints one table by seam and the summary, or with `--json` the pod's lines verbatim then the outside ones. Exit 1 on a fail (`--strict`: on a warning too), 2 when no report could be read. Your kubeconfig needs, in the namespace: `get`/`list` on CronJobs, `create`/`get`/`delete` on Jobs, `list` on Pods and `get` on `pods/log`, and `get` on HTTPRoutes and Ingresses |
| `repo add <owner/name> --out DIR --factory-app SLUG --agents-app SLUG [--factory-app-id ID] [--factory NAME] [--namespace NS] [--maintainer LOGIN]... [--check NAME]... [--leak-scan-check NAME] [--acknowledge-private-code] [--merge-defaults=false] [--force]` | Writes one repository's onboarding files and applies nothing: it opens no pull request and needs no administration right. With your `gh` login it reads the repository's default branch, its visibility, its required checks (the branch's rulesets and its classic protection, `maku/merge-gate` left out) and the two Apps (`GET apps/<slug>`, which answers only public Apps). For private Apps pass `--factory-app-id ID` (the factory App's settings page shows it): no App is looked up and both install links are `https://github.com/apps/<slug>/installations/new`; without it a private App's 404 fails, naming the flag. A protection it cannot read (classic protection needs admin rights) fails, naming `--check`, rather than write a `Repository` that merges on no check; so does a branch that requires none. `--check` names the checks and skips that read. DIR gets `factory/repository-<name>.yaml` (a `Repository` with no binding: the chart's catalog is evaluated in shadow without one), `repo/.github/agent-factory.yaml` (the fence, one line), `github/ruleset-merge-gate.json` and `github/apply-ruleset.sh` (the merge gate's ruleset and its `gh` command, as `install --print` writes them, never run) and a `README.md` with the steps in order; it prints both Apps' install links. The `Repository` is 12 lines: the merge settings every repository shares (`mergerLogin`, `verifyFor`, `revertWindow`, `breaker`) are left to the `Factory`'s `spec.mergeDefaults`, which `install --print` writes. For a `Factory` without them, `--merge-defaults=false` writes them into the `Repository` (`mergerLogin` the factory App's bot), since the factory refuses a `Repository` that lacks them. A private repository needs `--acknowledge-private-code`. `--maintainer` defaults to your `gh` login, `--leak-scan-check` to the first required check. It refuses a non-empty DIR without `--force` |
| `repo check <owner/name> [--namespace NS] [--factory NAME] [--kubeconfig FILE] [--context NAME]` | Checks one repository's onboarding, one line per check, then the fix under each failure: `fence` (the default branch's `.github/agent-factory.yaml`, read with your `gh` login, names the factory), `readiness` (`.github/agent-checks.yaml`, `mise.toml` and `AGENTS.md` are on the default branch; a missing one warns, see [Make a repository ready](make-a-repository-ready.md)), `repository` (a `Repository` in the namespace serves it, `Accepted` is true and `BindingsValid` is not false), and the factory's own `apps`, `merge-gate` and `private-code` checks for that repository alone, from its `GET /preflight?repository=owner/name` through a port-forward to a Ready factory pod's ops port. A factory too old to filter fails undecided rather than answer for every repository. Exit 0 when no line fails or warns, 1 otherwise, 2 on a usage error or an unreachable cluster. Your kubeconfig needs, in the namespace: `list` on Repositories and Pods, and `create` on `pods/portforward` |
| `demo app --dir DIR [--owner ORG] [--name NAME]` | The laptop demo's GitHub App, through GitHub's App manifest flow. Serves a page on `127.0.0.1` that posts the manifest (seven permissions, `actions: write` among them so a cancelled `ci` can be re-run, no webhook events, private) to GitHub, takes the callback once its `state` matches, converts the code and writes `app_id`, `private_key` (mode 0600), `slug` and `html_url` into DIR (mode 0700), each file is written through a temporary file and renamed into place (a symlink there is replaced, not followed), `html_url` last: its presence means the set is complete. It then prints the link that installs the App on your repository. The callback works once and only for the `127.0.0.1` host. It never prints the key and gives up after five minutes. `--owner` creates the App in an organization |
| `doctor [--client <name>]` | Checks the VPN, the CA, your login, your GitHub link and the installed skill's version (`skill`), with the fix of each, then `factory capabilities`: what that coding client declared when it opened `maku mcp` — forms, URL mode, roots, prompts, the protocol revision it asked for and the day it was measured — or `not measured yet` for a client no session gave. Without `--client`, the five clients measured most recently |
| `drill plant --repo owner/name --factory URL --stranger-token-file F --maintainer-token-file F --factory-token-file F [--id ID] [--wait D] [--runlore-hook X --tool-result-hook X --room-hook X]` | Plants an [injection drill](drills.md) in the drill repository, each scenario with a fresh marker, then posts its manifest to the factory as `system:drill`, retrying for `--wait` while the tasks are not made yet. Needs no maku config. Tokens are read from files and never printed. A scenario planted through a hook, without its hook, exits 2 before anything is planted |
| `token` | Prints a valid access token, for scripts. Treat it as a secret |
| `run --repo owner/name --task TEXT [--role implementer\|triager]` | Quickstart only, on a local broker ([one-run quickstart](contributing/one-run-quickstart.md)): creates a room, queues TEXT for the run's brief, starts the run, applies the claim the broker rendered with your own kubectl and kubeconfig, and follows the room until the run ends. Exit 0 only when the run ended Succeeded. It stops at the first failing step and names it: room, task, start_run (with the broker's reason), claim (kubectl's own error), run. On any other broker it refuses: the factory starts runs there |
| `status <room> [--json] [--after seq:N]` | Where a room stands. It leads with the [brief](reference/api.md#get-apiroomsidbrief)'s head — the `Resume:` line, what was asked, the plan, what changed in the last 24 hours, what blocks it and the one `Next:` step — then the room card (below): the title, the state, the facts (run, spend, PR and last verdict, next, why), what needs you, the runs line, the agents' notes and what you can do, then a `cursor: seq:N` line. The head is a read of its own and takes no cursor: it is always the brief's default window, the last day, whatever `--after` asks of the notes. `--json` prints the broker's summary as is; `--after` shows only the notes after a cursor (the output prints it as `seq:N`) |
| `rooms [--repo owner/name] [--mine] [--needs-me] [--all] [--json]` | The rooms you can read, as a table that leads with the task and ends with the room id: TASK (`#issue title`), REPO (when several), STATE, WHERE IT IS, PR, UPDATED, ROOM. `--mine`: tasks of issues you filed or labelled, PRs you authored or review. `--needs-me`: an approval you could decide, a task held on your room, a PR waiting for your merge, or an Escalated task waiting for its issue's author when you own the room. `--all`: the rooms that ended over a week ago too. `--json`: the broker's rows as is |
| `needs [--since RFC3339] [--room id] [--repo owner/name] [--format md\|text] [--json]` | What needs you across every room you can read, grouped by the five inbox types (`question`, `plan`, `review`, `blocked`, `failed`) with one entry per task (its most urgent type); stale entries for ended tasks are dropped. Each item shows its kind, what it asks, the one action and where it happens, and decides nothing. `--since`: only what moved after that instant, RFC 3339 as the answer's own cursor is; anything else is refused before any request, since it is neither everything nor nothing. Without it, the cursor the last call stored in `cursors.json`, which each call moves on. `--room` and `--repo` narrow the list; `--json` is the broker's answer as is (keeping `kind` and adding `type`). An empty list says so in one line, and a room whose state could not be read is named rather than passed off as quiet. Needs a broker whose `human.needs.enabled` serves `GET /api/needs` |
| `changes <room> [--cursor seq:N] [--format md\|text] [--json]` | What changed in a room since the cursor of your last call (the first call: 24 hours). The cursor is kept in `cursors.json` beside the config. Reads the broker's `task` tool |
| `checkout <room>` | Checks the room's branch, `agent/<room>`, out into a new git worktree beside the repository you are in, `<repo>.<room>`, and prints its path. It never touches your current checkout, so a dirty tree or your branch is safe, and a second call prints the same path. It refuses outside a checkout of the room's repository, and prints the `gh repo clone` command. When the branch changes files that run code on open or on commit, it lists them and asks `y/N`, and refuses without a terminal ([below](#checking-out-an-agents-branch)) |
| `coordination checkout <parent> [--workspace owner/name=path]...` | Checks every child of a coordination parent out, each in a detached worktree at the exact commit the parent names (an open PR's head, or a landed merge on the default branch), under `../<repo>.<parent>/<child>`, and writes `maku-coordination.json` there last. The current directory is the primary's clone; `--workspace` names each other repository's clone, and a missing one refuses with its `gh repo clone` command. It never clones, creates or moves a branch, or touches a clone's checkout. A moved head, a merge no longer on the default branch or a changed plan refuses as `checkout_stale`; a second identical call reuses the worktrees; a root left incomplete is reported, never repaired. It asks `y/N` once for every child's files that run code |
| `watch <room> [--tail N]` | Follows a room, one line per event: its `seq`, who (`role (id)` for a participant with a room role), what, and its text. Shows the last `N` events first (default 50). It reconnects after a drop and picks up where it stopped |
| `post <room> [--queue] <text>` | Posts a chat message and prints its `seq`. With `--queue` the text is kept for the next run's brief instead: in a factory task's room, the task's very next planner, triager or implementer run quotes it, whatever started that run (see [queued notes](factory.md#queued-notes)). The broker stores it labelled as [machine-sent](reference/api.md#machine-sent-text), as any text from maku |
| `start [--repository owner/name] [--workflow <name>] [--plan-first] "<request>"` | Plans a task through the broker's `/api/handoffs` routes, prints the workflow card and drafted issue, asks `File and start? [y/N]`, and on yes files and labels the issue with your `gh`. `--workflow` is a maintainer's; anyone else sees the refusal's sentence and the default |
| `cancel <task> [--discard]` | Stops the task now: its runs end and its room reads `Stopped — cancelled` (`Ended: cancelled` in Slack), even when its issue was deleted. `--discard` also closes its pull request, deletes its `agent/<task>` branch and closes its issue as not planned (a GitHub App cannot delete an issue). The task's owner or a maintainer of its repository only, with write on it; the room page's **cancel task** and **discard task** are the same act |
| `stages --repository owner/name [--class c] [--by class\|model] [--last N] [--json]` | Prints the per-class, per-stage cost and effect report for ended tasks of the repository, including `typical` (median) and `p90` merged cost once a class has at least 30 merged tasks. `--by model` prints the same tasks by model instead: each task under its implementer's model with its tasks, merges, USD and USD per merged task, and each stage under the model of the role that ran it (a run's served model, else a `model:` label or `evaluation.treatmentModel`, else the tier's alias). `--json` prints the broker's response verbatim, `models` included |
| `workflow dry-run <name> [--file definition.yaml] [--repository owner/name] [--last 20] [--json]` | Simulates a workflow definition (or an unstored local YAML file validated and compiled by the factory) over the newest ended tasks (default 20), printing what stages each task ran vs would have run and estimating cost from shared stages' recorded spend plus class medians (with at least 5 recorded runs in the class; `?` and left out of the total otherwise). `--json` prints the broker's response verbatim |
| `policy list --repository owner/name [--since DATE]` | Lists each policy of the repository first (mode, `CHECKED BY`: the [correctness signal](merge-policies.md#holds-agreed-signal-checked) or `nothing`, progress in agreed decisions or quiet merges, wrong calls), then the recent per-policy decisions through the broker: pull request, outcome, policy, measured files and lines, and when decided. `--output json` returns the factory's structured records, with `policies` beside `decisions` |
| `policy explain owner/name#PR` | Explains one pull request's decision timeline: head, matched policy, outcome, whether each policy holds, what checked the meaning (and how to add a signal when nothing does), every stage and its measured detail. `--output json` returns the same timeline, with each policy's sentence under `meanings` |
| `policy explain owner/name POLICY` | Prints what the policy merges in that repository and what checks its meaning (the sentence of its settings page), its progress, and each blocker of its promotion with the next step |
| `policy backtest FILE --repository owner/name [--since 30d]` | Re-decides recorded pull-request diffs under an edited policy file. Prints only changed and unknown rows, each linked to its real pull request with before and after, then changed, unchanged and unknown counts and a warning when the edited rule matches none. A missing immutable diff or original oracle findings is `unknown`, never `unchanged`. `--since` is days or an explicit date; `--output json` returns the same result |
| `policy promote POLICY --repository owner/name` | Re-reads eligibility and opens the one-line promotion pull request through the factory; prints its URL. Without `gitops`, prints the `gh pr create` command instead. `--output json` returns the proposal and PR fields |
| `policy drill owner/name#PR` | Opens a revert of that merged pull request labelled `factory/drill`; a maintainer still merges it. `--output json` returns the revert number and URL |
| `policy validate\|export\|check\|simulate\|diff --config FILE [--manifests FILE]... --repository owner/name [files]` | A repository's merge policy file, offline against a factory config and the manifests served over it: `validate` checks equivalence and accepts `--output json`; `export` writes a bound repository's bindings as the file's `mergePolicies` (a repository whose classes are the config file's is refused: `maku config migrate --from` first), `check FILE` proves it equivalent to them (not merely valid), `diff OLD NEW` validates both and names each class's tightening or relaxation, `simulate FILE FIXTURE...` replays the conformance fixtures under both. `diff` and `simulate` need `policyManagement.simulation` in that config. See [merge policies](merge-policies.md#workflows) |
| `policy pause --repository owner/name --reason TEXT` | Prints the `merge.paused` manifest edit. With `--config` naming a `Factory` whose `spec.gitops` is set, updates only that field in the named GitOps manifest and opens a pull request; Maku never merges it. A repository already paused with the same reason is refused before anything is written. `--output json` returns the edit or PR URL |
| `policy resume --repository owner/name` | Prints `merge.paused: null`, or opens the corresponding GitOps pull request when `--config` names a GitOps-enabled Factory; a repository whose manifest is not paused is refused with `is not paused`, and no pull request is opened |
| `config migrate --from FILE [--residual FILE [--force]] [--namespace NS]` | Converts a factory config file's `repositories[]` into the binding shape: the Factory, each Repository with its `merge` and `mergePolicies`, and one `MergePolicy` per class. `config migrate export` and `config migrate check` are the new home of `policy export` and `policy check`; the old names remain aliases and print the new one. `--manifests`, which converted per-repository `MergePolicy` objects, is removed and refused. It assembles both and writes nothing, the residual included, unless every resolved class and merge setting is unchanged, printing a `changed:` line for each difference. The residual never replaces the `--from` file, nor an existing file without `--force` |
| `config validate --config FILE [--base-ref REF \| --base-config FILE --base FILE...] [--expect-unchanged] [--resolved] [--allow-refused-bindings] MANIFEST...` | Checks manifests the way the factory would. A binding that does not resolve drops its class without refusing its `Repository`, so it is one `binding: Repository/<name> <policy>: <reason>: <message>` line and a failure; `--allow-refused-bindings` prints it as a warning instead. A base, either `--base-ref` (the config file and each manifest as committed at that git ref, such as `origin/main`) or `--base-config` with `--base`, prints each policy whose definition or consumers changed, with each consumer's change, live ones first and a live relaxation marked `LIVE`. `--expect-unchanged` fails when a resolved class differs from the base's. `--resolved` prints every repository's resolved classes as JSON, alone on stdout, with every other line on stderr. A binding over a guardrail is one `guardrail:` line and a failure. A `MergePolicy` in the removed per-repository shape is refused: `the per-repository MergePolicy was removed in this release: maku config migrate --manifests (from the previous release)` |
| `skill install [--dir .agents/skills]` | Writes the `maku-handoff` skill. A `factory-handoff` skill an older maku wrote stays where it is: delete it. Needs no config or login |
| `mcp [--advanced]` | Serves Maku's six MCP tools on stdio for a local coding agent, plus the rare and deprecated tools with `--advanced` ([below](#the-mcp-server)) |
| `fork <room> --at SEQ [--role R] [--pr URL] [--egress pypi,npm] [--note TEXT] [--approvals attended\|unattended]` | A room of your own from events 1 to `SEQ`, with an optional run. `--role`: `implementer`, `reviewer` or `triager`; `--pr`: the reviewer's pull request; `--egress`: extra egress allow-lists (`pypi`, `npm`, `golang`, `crates`, `maven`, `node`); `--note`: why you fork, under 1 KiB once labelled as machine-sent (about 900 bytes); `--approvals`: `attended` by default, so the fork asks before risky actions even when the source is an unattended factory room, `unattended` keeps an unattended source's policy (an attended source stays attended). Prints the new room's URL |

Flags may come before or after the positional arguments. Source: `internal/app/maku.go`.

`status` leads with the brief's head, prints the room card, then the cursor. For a task's room:

```text
**Resume: 26zfnuxm · #42 docs: chart version is wrong**
Asked: #42, chart version is wrong
Plan: scope ready · budget $1.50 ceiling
Changed since 2026-11-04 18:20 UTC (16 h):
Blocked on: `approval` — "git push to agent/26zfnuxm" waits for you
Next: approve it on the room page → https://rooms.example/r/26zfnuxm#01M4A

#42 docs: chart version is wrong · 26zfnuxm
needs you · an agent is coding · Smana/cloud-native-ref · open the room (https://rooms.example/r/26zfnuxm)

Workflow   standard · this repository's default
Will run   implementer → reviewer → CI → merge gate
Cost       up to $5.10
Path       implementer ● · reviewer ○
Run        implementer cf4ato2x (human review) · started 19:11 UTC, 18 min ago
Spend      $0.31 of $1.50 (21 %) · 189 k tokens
PR         #2239 (https://github.com/Smana/cloud-native-ref/pull/2239) · reviewer approved at 18:49 UTC

Needs you
1. approve "git push to agent/26zfnuxm" by 19:45 UTC → https://rooms.example/r/26zfnuxm#01M4A

Runs implementer (first run) succeeded · 40 min · $0.18 → reviewer approved · 10 min · $0.03 → implementer (human review) running · 18 min · ↪ steered ×2 · 1 chat · $0.10

Agents' notes, their claims, not checked
- 19:12 UTC cf4ato2x "found both versions on line 12-13, fixing"

You can cancel it with maku cancel 26zfnuxm
cursor: seq:7
```

Each line is left out when its data is absent: a room with no task has no facts table, a sealed
room no `Needs you` or `You can`. The spend line reads dollars when the factory priced the task
and tokens (`182 k of 1.5 M tokens (12 %)`) when it did not; token counts read like the room
page's, under 1,000 as is, then `k`, then `M`. Times are UTC, dated when not today. A run that
ended reads `succeeded`, `failed` and so on, and a stopped task's unfinished run reads `ended`,
never `running`. A run in the runs line counts the humans' messages sent while it was open, as the room page's rail does: `↪ steered ×N` and `N chat`, each left out at zero. Durations read the two largest units (`40 s`, `12 min`, `5 h 47 min`, `37 d 17 h`) and a past time its largest unit (`18 min ago`, `just now` under a minute), as on the room page. An approval past its deadline is not listed. On a terminal, with `NO_COLOR` unset, the state word is bold when it needs you.
The agents' text is quoted; an approval is a link to its card on the room page, never a command.

Room links print only under the broker's own address: the card takes it from the summary's URL
(the broker's `PublicURL`), and falls back to the URL `maku` is configured with when the summary
carries none. A broker reached by another name (a port-forward) therefore still shows its links.

### Acting on a task from the terminal

Each command sends its room act over the room's connection, prints one line saying what happens
next, and prints the refusal's sentence on a refusal (such as `act_not_enabled` when the broker's
`human.roomActs` does not include the act):

- `maku answer <task> "<one line>"` — answers a task's open question. Multi-line answers are refused locally before sending; the factory appends the decision record to `## Decisions` on the issue (an answer to the intake acceptance question also becomes the issue's `## Acceptance check` section, so the restarted task does not ask it again) and restarts the task for a maintainer (or waits for a maintainer for an issue author).
- `maku split accept|decline <task>` — accepts a proposed split (parts open one at a time, charged to you) or declines it to run the task whole (maintainers with write only).
- `maku plan approve|split <task>` — approves a proposed plan to start implementation, or splits it into parts (issue author or maintainer with write).
- `maku plan revise <task> "<one comment>"` — asks the planner to revise a proposed plan once with a single-line comment (issue author or maintainer with write).
- `maku retry <task> [--deeper]` — queues a fresh run of an escalated task; with `--deeper`, steps an escalated or no-op task up once on its workflow ladder (within its cap) on the same room, branch and pull request (maintainers with write only).
- `maku cancel <task> [--discard]` — stops the task now; `--discard` also closes the pull request, deletes the `agent/<task>` branch and closes the issue as not planned.

Source: `internal/roomview/card.go`.

The head above the card is the first six section lines of the room's
[brief](reference/api.md#get-apiroomsidbrief), taken as they are: the changed block's items, the acceptance
checks and the links stay in the brief an agent reads in full, since the card already carries what
needs you. A broker that serves no brief prints the card alone, as before; a brief that fails
for another reason says so in one line on stderr, because the card is still what you asked for.
Source: `internal/maku/status.go`.

## The MCP server

`maku mcp` is an MCP server on stdio. `maku setup <client>` registers it as `maku` (or register it
directly; no token is pasted):

```bash
claude mcp add --scope user maku -- maku mcp    # Claude Code; any agent that runs a stdio MCP server works
```

- The broker's operator turns `/mcp` on (`human.mcp.enabled`). Off, every call answers `mcp_off`. `/mcp` accepts `maku`'s own token only (`mcp_needs_maku`; an older broker's `mcp_needs_roomctl` is translated).
- **Six tools by default**, always listed in this order regardless of broker switches:

| Tool | Where | Input | What it does |
|---|---|---|---|
| `handoff` | broker | `{text, repository?, candidates?, context?, evidence?}` to begin; `{id, answer}` or `{id, acceptRest: true}` to continue | Plans the work you described, holds the plan on the broker for 24 hours bound to you, and returns the plan text as it is plus at most one question with its recommendation and why ([below](#the-plan)). With planning off, returns `Planning is not available here; file on GitHub.` |
| `start` | local | `{id, workflow?}` | Re-validates the stored plan on the broker, then shows a confirmation form with the workflow card (`Workflow`, `Will run`, `Cost`), described workflow options and the issue. An optional `workflow` preselects the form field when offered; only your answer files the issue and starts the work |
| `inbox` | broker | `{repo?}` | What needs you across your tasks (or your recent tasks when nothing is waiting or `needs` is off) |
| `task` | broker | `{task, since?}` | Where one task stands (the workflow card and `Path` when set, `State`, `Needs you`, `Budget`, `Merge` once the merge gate has evaluated a pull request), what was asked, `Next` and the milestones since `since`, with one cursor |
| `wait` | local | `{task, timeout?}` | Polls from your machine until a task needs you, opens a pull request or ends (300 s by default, at most 1800 s), and returns the task view. Run it in a background task |
| `answer` | local | `{task, text?}` | Replies to an open question on the task's issue (and restarts the task when you may start work there) or leaves a note for its next run, behind a confirmation form |
| `cancel` | local | `{task, discard?}` | Stops the task, or discards it (its pull request, its `agent/<task>` branch and its issue closed as not planned), behind a confirmation form; without a form it stops nothing. Listed after the six |

- **`start {id, workflow?}` and its form.** `start` asks the broker to re-validate the stored plan, then builds a confirmation form (MCP elicitation). The form leads with the workflow card (`Workflow`, `Will run`, `Cost`), lists the other offered workflows with their descriptions and ceilings, and shows the issue — no policy or merge line is shown before a pull request exists. The optional `workflow` tool argument preselects the form's `workflow` field when the name is offered (an unoffered name leaves the default and returns `workflow_ignored`); nothing is pinned without the form's answer. When several workflows are offered, changing `workflow` in the form re-validates the plan on the broker. `"File this issue"` (`file`) defaults to no. `"Start now"` (`start`) appears and defaults to yes when `human.mcp.start {default, except}` allows starting work on that repository (`human.mcp.sessionStart` was removed: the broker refuses a config that sets it) and your linked GitHub account has write access. On yes, your own `gh` creates the issue and applies any pinned `workflow:<name>` label (when you maintain the repository; skipped and noted otherwise) and `factory/ready`. A failed workflow label starts nothing. A client without forms or an unanswered form writes nothing and returns a `gh issue create` fallback command for you to run. It is a form field, never a tool argument: your agent can call `start` but cannot answer the form.
- **`answer {task, text?}` and its form.** `answer` reads the task first (`task`, falling back on `needs_author` to the newest public bounce comment from the configured `factoryLogin`).
  - **When a question is open**, the form shows the question, the recommendation and the full body diff (`## Acceptance check` plus `D<n>: Acceptance check: <answer>` for the acceptance question, or `D<n>` under `## Decisions` otherwise), takes a one-line `answer` (blank accepts the recommendation), and defaults `send` to no. When `canStart` is true, `"Restart the task"` (`restart`) appears and defaults to yes: after verifying the issue's body, labels and `updatedAt` are unchanged and writing the body edit with `gh issue edit --body-file=-`, yes re-applies `factory/ready` with your `gh` so the task is scoped again with the answer. Without `canStart`, the answer is written and `next` says a maintainer must restart the task.
  - **When no question is open**, `answer {task, text}` asks to confirm (`send` default no) and queues `text` as a [machine-sent](reference/api.md#machine-sent-text) note for the task's next run, which quotes it whatever started it ([queued notes](factory.md#queued-notes)).
- **Task references.** Every tool and prompt that takes a task or room (`task`, `wait`, `answer`, the `task` prompt, and under `--advanced` the `room_*` tools) accepts the id, `#<issue>` (the issue only), `PR #<n>` (the PR only), a bare number (an issue or a PR), or a fragment of the task's title (3 characters or more, case-insensitive; not an id or a number). `maku mcp` resolves it through the room list, open tasks first and then ended ones. When several match, the tool lists them with their ids and asks, calling nothing. Text that matches nothing goes to the broker as typed, which refuses it.
- **`--advanced`.** `maku setup <client> --advanced` registers `maku mcp --advanced`. Beyond the six tools, `--advanced` lists `room_tail` (which also runs as an MCP Task that `maku mcp` polls), `room_fork`, `room_checkout`, `coordination_checkout`, `coordination_open` and `coordination_plan`.
  - `room_checkout {room}` is `maku checkout` for your agent, rooted at your client's first root (else where `maku mcp` runs).
  - `coordination_checkout {parent, workspaces?}` is `maku coordination checkout` for your agent, asking one form for every child's files that run code.
- **Prompts.** Three prompts are listed: `handoff` (`what`), `task` (`task`, optional) and `inbox` (see [Short commands](#short-commands)). Any other prompt name is an unknown prompt.
- No tool approves or steers, and none stops or labels on the agent's say: only your answer to `start`'s, `answer`'s or `cancel`'s form does. Approvals stay on the room page.
- Every request reads `token.json` and refreshes the token when it has expired, one refresh at a time. When the refresh is refused, a call answers `not_logged_in`: run `maku login` in a terminal, and the running server uses the new token, with no restart.

The [maku-handoff skill](#the-maku-handoff-skill) does the same jobs through the shell for an
agent with no MCP configuration.

### The plan

`handoff` is the broker's governed planner (`human.intake.plan.enabled`). Your own sentence about
what is wrong becomes a plan stored on the broker for 24 hours under a random `id` bound to your
identity: your coding agent passes `{text}` (and optional `repository`, `candidates`, `context` or
`evidence`) to begin, then `{id, answer}` to answer the current question or `{id, acceptRest: true}`
to record the remaining recommendations as assumptions. The client never holds or edits a plan
envelope. An expired or foreign `id` is refused in one line: start a new handoff. With the planner
off, `handoff` stays listed and returns `Planning is not available here; file on GitHub.` Candidate
repositories come from the factory's served `Repository` objects through its catalog (cached for
60 s); `human.intake.plan.repositories` is optional and narrows that list when set, logging a
warning for any entry the factory does not serve.

`maku mcp` makes the loop the same whatever model drives the client:

| It does | So that |
|---|---|
| lists `handoff` with a description and argument descriptions of its own | a model that read no skill calls it at once with your words, and reads no file and runs no git first |
| fills `repository` from the `origin` of your workspace's checkout when the agent names none | the plan lands in the repository you are in, without a shell call |
| adds `next` to every `handoff` and `start` result, in `structuredContent` and as a last text block | each result names the one call to make, with the id in it: ask the question, `start`, `wait`, or stop |
| turns an answer that only says "accept the rest" into `acceptRest: true`, and drops `text`, `repository` and `context` sent again beside an `id` | a continuation is never refused for its shape |
| tells a planner refusal a second try can pass (`planner_invalid`, `invalid_draft`, `planner_timeout`) from the others | the agent retries once, then stops in one line; it never rewords your request to get past a refusal |
| ends `unavailable` with `maku doctor` | a broker that cannot be reached is one step for you, not a retry loop |

Source: `internal/maku/handoffnext.go`, `internal/maku/workspacerepo.go`.

The broker still computes and checks the plan's hash across the stored plan and the one `start`
re-validates, including your chosen workflow. When you change `workflow` in
`start`'s form, the relay re-validates that workflow on the broker before anything is filed: for a
maintainer, the broker records `D<n>: workflow: <w>, pinned by the developer` in `## Decisions`
(replacing a `workflow: factory's choice` Assumption), sets the issue's roles to that workflow, and
checks the choice against the repository's catalog with no model call and no daily plan slot. A
choice the catalog refuses comes back as one closed reason and sentence (`profile_unknown`,
`profile_conflict`, `not_maintainer`, `profile_no_tests_paths`; see [the factory's docs](factory.md#pinning-a-workflow))
and files nothing. Keeping the default pins nothing (`A<n>: workflow: factory's choice; suggested <s>`
when the planner suggested one).

What a plan shows, as `handoff` answers and your agent shows it back:

````text
**Plan** · Smana/maku · `h_9f3k2m7q`
Workflow   standard · this repository's default
Will run   implementer → reviewer → CI → merge gate
Cost       up to $2.00

1. `docs: the install link in the coding-agent guide 404s`

Issue 1
```
## What is wrong
`docs/cli.md` links to
`https://github.com/Smana/maku/releases/latest/download/maku-linux`, which 404s since the
assets were renamed.

## What it should be
The link points at the release asset the page describes, `maku-linux-amd64`.

## Acceptance check
The page's link resolves, and `hack/docs.sh` passes.
```

Question `Should the link point at the latest release, or at a pinned version?`
Recommended `the latest release, as the rest of the page does` · `the page already reads as a live guide`

Why this repository (unverified: model claim)
- `docs/cli.md is the page that carries the install section`
- `nothing else in the repository names that asset`
````

Its sections, in the order they are read:

- **The repository, handoff id, workflow and budget.** The head names the repository, the handoff `id`, the default `Workflow` (its roles come from the workflow config, never from the model) and the `Budget` (from the broker's config). No merge policy is named before a pull request exists. `Why this repository` is the planner's own reading — one quoted item per line and never a link (`kind: model_claim`, marked unverified). The head also names the model the model gateway reported answering (`served by …`) when it differs from the alias. A repository outside the ones this request may use refuses the plan whole, and is not named back.
- **The issue.** One numbered line per issue (one to three), then `Issue 1`, `Issue 2` … with each draft as it will be filed (`## What is wrong`, `## What it should be`, `## Acceptance check`, plus optional `## Decisions` with `D<n>` answers and `A<n>` assumptions). One `start` call files the **first**: with a longer plan, say the others are not filed.
- **At most one question.** The broker keeps one gap and assumes the rest (template gaps, the acceptance check and the observable behaviour, are never asked); `Question` shows it with its recommendation and why. Reply with `{id, answer}` or accept the recommendation with `{id, acceptRest: true}`; a re-plan after an answer asks nothing more. Once no gaps remain, `handoff` marks the plan `ready: true` for `start {id}`. A plan marked `degraded` came from the broker's deterministic composer (`human.intake.plan.fallback: composer`) rather than the model, and says so on its first line.

The initial plan and every `handoff {id, answer}` round below the cap cost one of your day's plans
(`human.intake.plan.dailyPlans` per requester, reset at 00:00 UTC); `{id, acceptRest: true}` and
`start`'s re-validation cost none. `human.intake.plan.maxRounds` (default 10) caps clarification rounds in
one handoff: reaching the cap records the remaining recommendations as assumptions without another
model call.

The refusals you can meet, with the next step (the full list is [the planner on `/mcp`](reference/api.md#the-planner-on-mcp)):

| Reason | Next step |
|---|---|
| `private_repository_unsupported` | The planner's internal route (`human.intake.plan.internal`) is not configured, so a private repository is refused before any model call. Ask an operator to enable `human.intake.plan.internal` with an `internal`-class gateway provider (and `spec.privateCode.acknowledged: true` on the `Repository`), or file on GitHub |
| `repository_not_allowed` | Name the repository as `owner/name`, or pick one of the ones the refusal lists: those are the repositories you may file into. A candidate that is private is left out of that list without a word |
| `no_write` | Your GitHub access to that repository is read-only, and the planner plans only what you may push to. Ask for one you may write in, or show the plan to someone who can file it |
| `planner_timeout` | The one call outlived its deadline (20 s, under `/mcp`'s 25 s). Ask again, with the same words or a shorter context |
| `planner_unavailable` | The plan service is down or refused the call. Try again later: nothing was filed, and the refusal names no repository. The cause (the model gateway's status, a missing or empty key file, a DNS or connection error) is in the broker's `intake plan` log line, as `cause=`; ask the operator |
| `catalogue_unavailable` | The factory's catalog could not be read, and no fresh cached list remains (60 s TTL). Try again in a minute |
| `plan_changed` | The stored plan and hash no longer hold together. Run `handoff` again and confirm the new plan |
| `unresolved_marker` | The plan still has an open question. Answer it with `handoff {id, answer}` or pass `{id, acceptRest: true}` |
| `invalid_decisions` | Fix the Decisions records: one line each, with ascending unique IDs and valid gap references |

### OpenCode's background service

OpenCode v2 runs a background service (`opencode service start|restart|status|stop`) that keeps the
config it started with. A service started before `maku setup opencode` gives its attached sessions
no Maku tools, while `opencode mcp list` still says `maku` is connected. After it registers the
server, `setup opencode` runs `opencode service status`: when the service runs it says so and asks
`y/N` on a terminal before running `opencode service restart`; `--restart-service` restarts without
asking. It never restarts silently, since other sessions may be attached; without a terminal or the
flag it prints the command. When the binary or its `service` subcommand is missing it prints the hint
alone. `maku doctor` warns (`warn opencode`) when the service started before the OpenCode config
file's last change.

### OpenCode and Code Mode

OpenCode v2 exposes MCP tools through Code Mode by default (`tools.<server>.<tool>` through its
`execute` tool). A config with the permission `{"action":"execute","resource":"*","effect":"deny"}`
turns Code Mode off and hides every Code Mode server: `maku` is "connected" in
`opencode mcp list`, yet a session has no Maku tools. `"codemode": false` on
`mcp.servers.<name>` keeps that server's tools on the native tool list (OpenCode's
"Disable Code Mode for an MCP Server"), which also suits the confirmation forms and `wait`.
After `opencode mcp add`, `maku setup opencode` sets `"codemode": false` on `mcp.servers.maku`
(rewriting an older `factory` entry to `maku` when present) and keeps the rest of the file (it is
rewritten as indented JSON; a file with comments is left alone and setup says what to add). The
pasted snippet carries it too. `maku doctor` warns (`warn opencode`) when the `maku` entry (or a
legacy `factory` entry) lacks it and an `execute` deny rule exists.

It also sets `"timeout": {"execution": 900000}` (ms) there: OpenCode cuts a tool at its execution
timeout, which cut `start`'s confirmation form ("context deadline exceeded"). `maku doctor` warns
when the entry has none. When a form cannot be shown or is not answered, `start` answers with a
`next:` hint holding one `gh` command that only files the issue, for the developer to run. It never
labels: starting the work stays the developer's act (the label, or choosing `"Start now"` in the
form).

### Short commands

Claude Code lists the MCP prompts as `/maku:handoff`, but typing that is rejected as an unknown
command: only `/mcp__maku__handoff` runs. So `maku setup claude` and `maku setup opencode`
also write `maku.md`, calling the same
tools:

| Type | Does |
|---|---|
| `/maku handoff [what]` | calls `handoff` at once with your words and nothing else, then does what each result's `next` says: asks the one question (or accepts the rest with `acceptRest: true`), then calls `start` without asking, so its form is where you confirm and start the work |
| `/maku task [#issue]` | calls `task` (or `inbox` first when no task is named, then `task` on the first item waiting on you) and shows the result |
| `/maku inbox` | calls `inbox` once and shows what needs you across your tasks |

Where: `~/.claude/commands/maku.md` (`.claude/commands/` with `--project`) for Claude Code,
`~/.config/opencode/commands/maku.md` (`.opencode/commands/`) for OpenCode, which reads the same
markdown file with `$ARGUMENTS`. A second `setup` rewrites it. The MCP prompts stay as they are.
Source: `internal/maku/skill/commands/maku.md`.

### Checking out an agent's branch

An agent's branch is code a sandboxed agent wrote, and some of its files run on your machine without
you running anything: agent and editor configuration, `.mcp.json`, hooks, `mise.toml`, `.envrc`.
So the checkout works like this:

- It fetches only `agent/<room>` from your `origin`, after checking that `origin` is the room's
  repository (`git@github.com:o/n` or `https://github.com/o/n`, with or without `.git`, in any case).
- It runs git with an argument list, never a shell, with `core.hooksPath=/dev/null` and no Git LFS
  smudge filter, so no hook
  of your repository or of the branch runs during the fetch or the checkout. It runs nothing in the
  new worktree.
- The worktree path is `<repository>.<room>` in the repository's own parent directory. A path that
  already exists is refused, never overwritten.
- It fetches `agent/<room>`, resolves it once to a commit, and checks, asks about and creates only
  that commit: a fetch that moves the branch while a form is open changes nothing. It verifies the
  new worktree is at that commit, and removes it otherwise. A killed or failed checkout is removed,
  never left to be reused.
- A local branch `agent/<room>` that is not that commit is refused, with the command to drop or move
  it. Every git call is bounded (2 minutes for the fetch, 30 seconds for the rest), and recursion into
  submodules is off.
- It lists what the branch changed against its merge base with your `HEAD`, and against `HEAD`
  itself. If a changed file is one of the paths below, or is under your repository's in-tree
  `core.hooksPath`, it asks first: a form whose `create` field defaults to no (`room_checkout`),
  or `y/N` on a terminal (`maku checkout`). The file names are shown in code spans, cleaned.
  `core.hooksPath` is read from your effective git config, and an `origin` with several URLs is refused.
  A client with no forms, or a command with no terminal, is refused and told the files.

The paths: the folders `.claude/`, `.cursor/`, `.opencode/`, `.gemini/`, `.codex/`, `.vscode/`,
`.agents/`, `.devcontainer/`, `.kiro/`, `.amazonq/`, `.roo/`, `.continue/`, `.zed/`, `.qwen/`,
`.windsurf/`, `.github/skills/`, `.github/hooks/`, `.husky/`, `.githooks/`, `.mise/`, `mise/` and
`.config/mise/`, whole (settings, hooks, tools, plugins, skills and scripts all live there), and the
files `.github/mcp.json`, `.mcp.json`, `opencode.json`, `opencode.jsonc`, `crush.json`,
`.crush.json`, `.devcontainer.json`, `.envrc`, `.tool-versions`, `mise.toml`, `.mise.toml`,
`mise.local.toml`, `.mise.local.toml`, `mise.*.toml`, `.mise.*.toml`, `.pre-commit-config.yaml`,
`lefthook.*`, `lefthook-local.*`, `.lefthook.*`, `.lefthook-local.*`, `.config/lefthook.*`, `.config/lefthook-local.*`, `.gitmodules` and
`.lfsconfig` (a `*` stays within one name), in the repository root or any folder of it, in ASCII
case. A changed path with a byte past ASCII anywhere in its name is flagged too, since a filesystem
may fold it into one of these names. A changed path that is a folder of one of them counts (a file
named `.claude`), and so does every symlink the branch adds or changes, whatever its name: a link
`.claude -> x` with `x/settings.json` would otherwise hide the file. A symlink the branch's tree
already has at one of these places, in any folder, is asked about as well, since the branch can
change its target. Flagging more is safe, since a flag only means "ask first". The form lists every
match of the paths above first (up to 200), and then the symlinks and non-ASCII names, at most 20
of them, and a count of the rest. The list is `flagged` in `internal/maku/checkout.go`; the tests
hold a fixture per entry.

What the check cannot see: it follows names, not what runs them. A file that one of your own git
hooks or build steps runs is code, and the check does not know it: lint-staged or commitlint
configuration, a Makefile target that a hook calls, `package.json` scripts, a workflow you run with
`act`. Read the diff of those yourself. A repository with more than about 150,000 files makes the
tree listing pass its 16 MiB cap, and the checkout then refuses instead of guessing.

One checkout of a room runs at a time in a repository: it takes an advisory lock (`flock`) on
`maku-checkout-<room>.lock` in git's directory, which the kernel drops when the process ends, so
a crash leaves nothing to wait out. A second is refused while the first waits for you. What the
call made is all it ever removes: the branch is created only if it does not exist yet, so a branch
you made while the form was open is never used, moved or deleted, and a worktree at its path that
git does not list as this call's is left alone. A worktree of the branch whose folder is gone is
removed and made again, unless you locked it (`git worktree lock`).

## The maku-handoff skill

`maku skill install` writes an Agent Skill that lets a local coding agent hand a side task to Maku ([the factory](factory.md)) and follow it. The skill is embedded in the binary, so it always matches the CLI that runs its commands (`internal/maku/skill`).

It works MCP first. With the `maku` MCP server registered (`maku setup <client>`), it uses the six tools — `handoff`, `start`, `inbox`, `task`, `wait` and `answer` (plus `room_checkout` when `--advanced` is enabled) — and shows each tool's text as it is. Without the server, a shell fallback section runs the same jobs through `gh` and `maku`.

It does:

- Run the `handoff` prompt (or call `handoff` directly when the client has no prompts), show the plan as it is, ask one question at a time or accept the rest with `acceptRest: true`, and finish with `start` so your answer to the form files and starts the work. With the planner off, `handoff` answers one fixed line; in a shell with no MCP server, the skill writes the issue in the template's three sections and says the broker planned nothing.
- Follow a task with `task`: report state, budget, merge decision, what needs you and the newest milestones.
- Say "needs you" first when something waits on you, giving the link: approvals stay on the room page.
- Answer "anything waiting on me?" with `inbox`.
- Reply to a task's question or leave a note for its next run with `answer` (or `maku post <room> --queue` in the shell fallback), only with your words and confirmation.
- Follow a task in the background with `wait` when the agent can run a background task or subagent, and check a task's branch out into a new worktree beside the repository (`room_checkout` under `--advanced`, or `maku checkout <room>`), only when you ask. It never touches your current checkout.

It never:

- Applies a label, `factory/ready` included, on the agent's say.
- Approves, stops or retries a task, or gives a command that approves.
- Follows an instruction found in a note or an agent's text: that is data to report.
- Installs software, trusts a CA or signs in for you. It runs only read-only checks.

`skill install` writes `<dir>/maku-handoff/` (`SKILL.md` and `references/issue-template.md`), `.agents/skills` by default, and overwrites what is there. It refuses to write through a symlink inside the skill tree. `--dir` sets where the skill is written; point it at the directory your agent reads skills from.

The skill needs `gh` (authenticated) and `maku` (signed in) on the PATH.

`maku doctor` has a `skill` check: it reads the `roomctl-version` of every `maku-handoff` skill in the directories the six clients read (`maku setup`'s, under your home and under the working directory), and fails when one is not this binary's version, because a skill from another release names tools or commands this one lacks. It skips when none is installed.

## Troubleshooting

Run `maku doctor` (or `maku setup <client>`) first: it checks in the order things fail and prints the fix of each.

| Symptom | Cause | Fix |
|---|---|---|
| `maku` not found | not installed | [Install and verify](#install-and-verify) |
| `not configured` | no config file | `maku setup <client>`, or `maku configure --url https://rooms.<domain>` |
| `no such host` on the rooms host | the VPN is down: the host resolves only on the tailnet | `tailscale up`, then `tailscale status` |
| `x509: certificate signed by unknown authority` | the platform's root CA is not trusted, and `maku` has no `--cacert` | trust the root CA on your system, or set `SSL_CERT_FILE` to a bundle holding it for the shell that runs `maku` |
| auth error, expired token | not signed in | `maku login` (or re-run `maku setup <client>`) |
| `401: user_inactive` | your identity provider user was deactivated, locked or deleted | ask an admin to reactivate it; `maku login` cannot help |
| `rooms` lists nothing | your identity provider user has no linked GitHub identity, so no repository is readable | open `<rooms URL>/link/github` (printed on `maku doctor`'s `github` line), or ask an admin to link your GitHub account at your identity provider |
| `FAIL skill` on `maku doctor` | a `maku-handoff` skill from another maku release, in your home or in the repository | delete the stale copy, or rewrite it: a stale copy in your home needs `maku setup <client>` without `--project`, and `--project` only rewrites the repository's copy. A `factory-handoff` copy is not read: delete it |
| `warn skill` after `maku setup <client>` | another client's `maku-handoff` copy is from another release; setup checked its own copy and does not fail on this one | run the `maku setup <other client>` the line names |
| `warn factory capabilities (cli 2.0.x …)` | OpenCode before 2.3.0 declares forms it never shows | update OpenCode to 2.3.2 or later |
