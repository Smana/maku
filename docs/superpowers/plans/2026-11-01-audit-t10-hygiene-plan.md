# Audit T10: Hygiene and Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the audit's repository-hygiene findings and the docs drift: the quickstart stops deleting the `maku` it installed (H-6), Dependabot watches `web/` (S-8), image builds stop shipping `.git`, worktrees and the quickstart kubeconfig (S-10), `.claude/` worktrees stop breaking the local gates (S-11), the shadow-merge tally fails on a failed search instead of halving its count (S-12), and the contributor docs name every package, gate step, binary and port that exists (A-3).

**Architecture:** Shell, config and Markdown only; no Go. Each code finding is pinned first by a shell test in the suite its gate step already runs (`hack/quickstart/lib_test.sh`, `scripts/shadow-merge-tally_test.sh`, `hack/names_test.sh`), the config findings by a one-line check, and A-3 by a package-list diff and `hack/docs.sh`. One commit per finding (D0).

**Tech Stack:** bash + shellcheck, yq 4.54.1, jq, Dependabot v2 config, Docker BuildKit (`.dockerignore`), Markdown under `hack/docs.sh`.

**Spec:** `docs/superpowers/specs/2026-11-01-audit-fixes-design.md` (theme T10, D0), findings in `docs/superpowers/specs/2026-11-01-project-audit.md` (H-6, S-8, S-10, S-11, S-12, A-3 and the "Docs drift" section).

## Global Constraints

- **D0:** one PR for the theme, branch `chore/audit-t10-hygiene`; each finding is one commit holding the check that pins it and its fix; `task check` exits 0 on the PR.
- **After T9 for A-3:** Tasks 1-5 depend on nothing and may run first. Task 6 (A-3) starts only once T6 (`internal/tlsreload`) and T9 (`internal/factory/ledger`, `runwatch.Kind` injected) have merged: rebase the branch onto `main` first, and take each new package's contract from its `// Package` comment as merged.
- Commit messages: Conventional Commits, English, in the repository's scopes (`fix(quickstart)`, `chore(deps)`, `fix(scripts)`, `docs`). **No `Co-Authored-By` and no "Generated with" line**, in commits and in the PR body.
- No network in tests: every shell test serves fixtures (`scripts/testdata/tally/gh-stub.sh`, a locally built tarball).
- Reader pages pass `hack/docs.sh`: no per-row status words (`Planned`, `not built`, `in review`, `(phase N`), every relative link and anchor resolves. `hack/names.sh` also reads them: never write `agents.ogenki.io` as an API group (in backticks, before `/v1alpha1`), only as an annotation-key prefix.
- `CLAUDE.md` is a symlink to `AGENTS.md`: edit `AGENTS.md` only.
- Never override `HOME`. `task check` runs once, in Task 7.

## Spec deviations

| # | Spec says | Plan does | Evidence |
|---|---|---|---|
| 1 | S-10: `.dockerignore` lists `.git`, `.claude`, `.quickstart`, `web/node_modules`, `**/__pycache__` | Adds `.superpowers` | `.superpowers/` (the sdd progress reports) sits at the repo root, ignored only through `.git/info/exclude`, so `COPY . .` ships it like `.claude/`. It applies to the five Go images, whose context is `.` (`.github/workflows/image.yaml:51`); the harness builds from `images/maku-harness` and is unaffected |
| 2 | S-10's check: "`docker build` context size" | Measured with a `FROM scratch` + `COPY . /ctx` Dockerfile (no base image pull), after the fix only; the before figure is `du` of what the file excludes | A pre-fix context transfer is about 3.3 GB (`du -sh .git .claude web/node_modules`: 102M, 3.0G, 106M on 2026-11-01) and loads it all into BuildKit for nothing |
| 3 | A-3 names `docs/architecture.md`'s binaries, images and ports | Also adds the missing packages to `docs/architecture.md`'s two package tables | They are the same tables as `AGENTS.md`'s and miss the same packages, plus `cmd/maku-slack`, `internal/slackapp` and `internal/suspend` (diff in Task 6, Step 1) |
| 4 | H-6: "a shell test: a file `maku` in the dir survives a refetch" | The install step moves into `qs_install_gvisor` in `hack/quickstart/lib.sh`, which `lib_test.sh` drives with a locally built tarball | `fetch-runsc.sh` downloads from Google Storage; testing it whole needs the network. `lib.sh` is where the quickstart keeps its tested helpers |

---

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `hack/quickstart/lib.sh`, `lib_test.sh`, `fetch-runsc.sh`, `check.sh` | 1 | `qs_install_gvisor`: replace gVisor's files only |
| `.github/dependabot.yml` | 2 | the `npm` ecosystem for `/web` |
| `.dockerignore` (new) | 3 | the Go images' build context |
| `.gitignore`, `hack/names_test.sh` | 4 | `.claude/` ignored, and pinned |
| `scripts/shadow-merge-tally.sh`, `scripts/shadow-merge-tally_test.sh`, `scripts/testdata/tally/gh-stub.sh` | 5 | a failed search fails the tally |
| `AGENTS.md`, `CONTRIBUTING.md`, `docs/architecture.md`, `README.md` | 6 | package map, seams, gate table, binaries, images, ports, status line |

Tasks 1-5 are independent; Task 6 needs T6 and T9 merged. Execute in numeric order.

---

### Task 1: H-6, a gVisor refetch keeps the `maku` that up.sh installed

**Files:**
- Modify: `hack/quickstart/lib.sh` (append a function)
- Modify: `hack/quickstart/lib_test.sh` (before `# no script passes a literal secret on a command line`)
- Modify: `hack/quickstart/fetch-runsc.sh:9-11,34-39`
- Modify: `hack/quickstart/check.sh` (after line 79)

**Interfaces:**
- Produces: `qs_install_gvisor TARBALL DIR WANT` in `lib.sh`: removes `DIR/runsc`, `DIR/containerd-shim-runsc-v1`, `DIR/gvisor-bin/` and `DIR/.gvisor-release`, unpacks TARBALL into DIR, marks both binaries executable, refuses a tarball without `gvisor-bin/`, writes WANT to `DIR/.gvisor-release`. Returns non-zero on any failure.

- [ ] **Step 1: Create the branch**

```bash
cd /home/smana/Sources/maku
git switch main && git pull --ff-only
git switch -c chore/audit-t10-hygiene
```

- [ ] **Step 2: Write the failing test**

In `hack/quickstart/lib_test.sh`, insert before `# no script passes a literal secret on a command line`:

```bash
# qs_install_gvisor: a refetch replaces gVisor's files and keeps the rest of .quickstart/bin, maku included (H-6)
gv=$(mktemp -d)
trap 'rm -rf "$gv"' EXIT
mkdir -p "$gv/src/gvisor-bin" "$gv/bin/gvisor-bin"
printf new >"$gv/src/runsc"; printf new >"$gv/src/containerd-shim-runsc-v1"; printf new >"$gv/src/gvisor-bin/helper"
tar -cjf "$gv/gvisor.tar.bz2" -C "$gv/src" runsc containerd-shim-runsc-v1 gvisor-bin
printf maku >"$gv/bin/maku"; printf old >"$gv/bin/runsc"; printf old >"$gv/bin/gvisor-bin/stale"
ok qs_install_gvisor "$gv/gvisor.tar.bz2" "$gv/bin" "release-x86_64"
eq "$(cat "$gv/bin/maku" 2>/dev/null)" maku "a refetch keeps the maku up.sh installed"
eq "$(cat "$gv/bin/runsc")" new "a refetch replaces runsc"
eq "$(cat "$gv/bin/.gvisor-release")" release-x86_64 "the release stamp"
[[ ! -e $gv/bin/gvisor-bin/stale ]] || { echo "FAIL: a refetch leaves no stale gvisor-bin/ file" >&2; fail=1; }
[[ -x $gv/bin/runsc && -x $gv/bin/containerd-shim-runsc-v1 ]] || { echo "FAIL: runsc and its shim are executable" >&2; fail=1; }
rm "$gv/src/gvisor-bin/helper"; rmdir "$gv/src/gvisor-bin"
tar -cjf "$gv/bad.tar.bz2" -C "$gv/src" runsc containerd-shim-runsc-v1
no qs_install_gvisor "$gv/bad.tar.bz2" "$gv/bin" "release-x86_64"
```

In `hack/quickstart/check.sh`, after line 79 (the `for t in sha256sum sha512sum bzip2 tar` check), add:

```bash
# fetch-runsc.sh installs through qs_install_gvisor, never wipes .quickstart/bin: maku lives there (H-6)
# shellcheck disable=SC2016 # the literal $tmp, $dir and $want are fetch-runsc.sh's text
{
grep -qF 'qs_install_gvisor "$tmp/gvisor.tar.bz2" "$dir" "$want"' "$qs/fetch-runsc.sh" || bad "fetch-runsc.sh does not install through qs_install_gvisor"
if grep -qE '^rm -rf "\$dir"$' "$qs/fetch-runsc.sh"; then bad "fetch-runsc.sh wipes .quickstart/bin, the maku up.sh installed included"; fi
}
```

(The `{ }` block carries the one `disable` to both lines, as check.sh's local-mode block does; shellcheck flags SC2016 otherwise and fails the gate.)

- [ ] **Step 3: Run it to verify it fails**

Run: `bash hack/quickstart/lib_test.sh; bash hack/quickstart/check.sh`
Expected: lib_test exits 1 with
```
FAIL: expected success: qs_install_gvisor …/gvisor.tar.bz2 …/bin release-x86_64
FAIL: a refetch replaces runsc: got 'old', want 'new'
FAIL: the release stamp: got '', want 'release-x86_64'
FAIL: a refetch leaves no stale gvisor-bin/ file
FAIL: runsc and its shim are executable
```
and check.sh prints `FAIL: fetch-runsc.sh does not install through qs_install_gvisor` and `FAIL: fetch-runsc.sh wipes .quickstart/bin, the maku up.sh installed included`.

- [ ] **Step 4: Write `qs_install_gvisor` and use it**

Append to `hack/quickstart/lib.sh`:

```bash

# qs_install_gvisor TARBALL DIR WANT: unpacks a verified gvisor.tar.bz2 into DIR and stamps WANT,
# replacing only what gVisor owns there: runsc, containerd-shim-runsc-v1, gvisor-bin/ and the
# stamp. DIR is .quickstart/bin, which also holds the maku up.sh installed a step earlier; wiping
# the directory deleted it on every first run and every GVISOR_RELEASE bump (audit H-6).
qs_install_gvisor() {
  local tarball=$1 dir=$2 want=$3
  rm -rf "$dir/runsc" "$dir/containerd-shim-runsc-v1" "$dir/gvisor-bin" "$dir/.gvisor-release"
  mkdir -p "$dir"
  tar -xjf "$tarball" -C "$dir" || return 1
  chmod +x "$dir/runsc" "$dir/containerd-shim-runsc-v1" || return 1
  [[ -d $dir/gvisor-bin ]] || { echo "gvisor-bin/ missing from the tarball" >&2; return 1; }
  echo "$want" >"$dir/.gvisor-release"
}
```

In `hack/quickstart/fetch-runsc.sh`, after line 11 (`. hack/quickstart/versions.env`), add:

```bash
# shellcheck source=hack/quickstart/lib.sh
. hack/quickstart/lib.sh
```

and replace lines 34-39:

```bash
rm -rf "$dir"
mkdir -p "$dir"
tar -xjf "$tmp/gvisor.tar.bz2" -C "$dir"
chmod +x "$dir/runsc" "$dir/containerd-shim-runsc-v1"
[[ -d $dir/gvisor-bin ]] || { echo "gvisor-bin/ missing from the tarball" >&2; exit 1; }
echo "$want" >"$stamp"
```

with:

```bash
qs_install_gvisor "$tmp/gvisor.tar.bz2" "$dir" "$want"
```

(`stamp` stays: the idempotency check at line 22 still reads it.)

- [ ] **Step 5: Run it to verify it passes**

Run: `task quickstart:check`
Expected: `lib_test: ok` and `quickstart check: ok` (shellcheck over `hack/quickstart/*.sh` included).

- [ ] **Step 6: Commit**

```bash
git add hack/quickstart/lib.sh hack/quickstart/lib_test.sh hack/quickstart/fetch-runsc.sh hack/quickstart/check.sh
git commit -m "fix(quickstart): a gVisor refetch keeps the maku binary up.sh installed

up.sh installs .quickstart/bin/maku, then fetch-runsc.sh ran rm -rf on
.quickstart/bin: every first run and every GVISOR_RELEASE bump deleted
the maku it told the user to put on PATH. qs_install_gvisor replaces
gVisor's own files only. Audit H-6."
```

---

### Task 2: S-8, Dependabot watches `web/`'s npm dependencies

**Files:**
- Modify: `.github/dependabot.yml` (append)

**Interfaces:** none.

- [ ] **Step 1: Write the failing check**

Run: `yq '[.updates[] | select(.package-ecosystem == "npm" and .directory == "/web")] | length' .github/dependabot.yml`
Expected now: `0` (the check passes when it prints `1`).

- [ ] **Step 2: Add the ecosystem**

Append to `.github/dependabot.yml`:

```yaml

  # web/'s dependencies, markdown-it among them: the parser of untrusted room text. A bump changes
  # the committed bundle, so its PR fails ui:check until `task ui:build` is committed on its branch.
  - package-ecosystem: npm
    directory: /web
    schedule:
      interval: weekly
    cooldown:
      default-days: 7
    groups:
      npm:
        patterns: ["*"]
    ignore:
      - dependency-name: "*"
        update-types: ["version-update:semver-major"]
```

- [ ] **Step 3: Run the check to verify it passes**

Run: `yq '[.updates[] | select(.package-ecosystem == "npm" and .directory == "/web")] | length' .github/dependabot.yml && yq '[.updates[] | select(.package-ecosystem == "npm") | (.cooldown.default-days == 7 and .ignore[0].update-types[0] == "version-update:semver-major")] | all' .github/dependabot.yml`
Expected: `1`, then `true`.

- [ ] **Step 4: Commit**

```bash
git add .github/dependabot.yml
git commit -m "chore(deps): Dependabot watches web/'s npm dependencies

No ecosystem covered web/, so markdown-it, the parser of untrusted room
text, was never bumped. Same weekly cadence, 7-day cooldown and major
ignore as the others. Audit S-8."
```

---

### Task 3: S-10, a `.dockerignore` for the Go images' build context

**Files:**
- Create: `.dockerignore`

**Interfaces:** none. The five Go images build with context `.` (`.github/workflows/image.yaml:51`, `hack/quickstart/migrate-e2e.sh:76`) and `COPY . .`; their build needs `go.mod`, `go.sum`, `api/`, `cmd/`, `internal/` (its `go:embed` targets are `internal/humanapi/ui/dist`, `internal/maku/skill/…`, `internal/store/migrations/*.sql`). The harness's context is `images/maku-harness`, which this file does not reach.

- [ ] **Step 1: Write the failing check**

Run: `test -f .dockerignore && grep -qx '.claude' .dockerignore && grep -qx '.quickstart' .dockerignore; echo "exit=$?"`
Expected now: `exit=1`.

- [ ] **Step 2: Write the file**

Create `.dockerignore`:

```
# The Go images build from the repository root (docker build -f images/<bin>/Dockerfile .) and
# COPY . . for go build, which needs the module only. Never send the history, local agent
# worktrees and reports, the quickstart's admin kubeconfig, or web/'s dependencies.
.git
.claude
.superpowers
.quickstart
web/node_modules
**/__pycache__
```

- [ ] **Step 3: Run the check, then measure the context and prove a build**

```bash
test -f .dockerignore && grep -qx '.claude' .dockerignore && grep -qx '.quickstart' .dockerignore; echo "exit=$?"
du -sh .git .claude .superpowers .quickstart web/node_modules 2>/dev/null   # what no longer ships
printf 'FROM scratch\nCOPY . /ctx\n' >"$SCRATCH/ctx.Dockerfile"            # $SCRATCH: the session scratchpad
docker build --no-cache --progress=plain -f "$SCRATCH/ctx.Dockerfile" --output type=tar,dest=/dev/null . 2>&1 | grep 'transferring context'
docker build -f images/maku-broker/Dockerfile -t maku-broker:dockerignore .
docker rmi maku-broker:dockerignore
```
Expected: `exit=0`; the `du` lines (about 3.3 GB on a laptop with worktrees); a context transfer in the tens of MB; the broker image builds. Record both figures for the PR body.

- [ ] **Step 4: Commit**

```bash
git add .dockerignore
git commit -m "chore: a .dockerignore keeps history, worktrees and the quickstart kubeconfig out of image builds

The Go images COPY . . from the repository root, so a local build sent
.git, .claude/ worktrees (3 GB), web/node_modules and .quickstart/,
whose kubeconfig is the kind cluster's admin, into the build stage.
Audit S-10."
```

---

### Task 4: S-11, ignore `.claude/` so local worktrees stay out of the gates

**Files:**
- Modify: `.gitignore` (append)
- Modify: `hack/names_test.sh` (before the last line)

**Interfaces:** `hack/names.sh` and `hack/spdx.sh` list files with `git grep --untracked` / `git ls-files --others --exclude-standard`, which honour `.gitignore`.

- [ ] **Step 1: Write the failing test**

In `hack/names_test.sh`, insert before the last line (`echo "==> names_test: ok"`):

```bash
# Local agent worktrees under .claude/ are copies of the tree: the gates must never read them (S-11).
git -C "$here/.." check-ignore -q .claude/worktrees/any/AGENTS.md || { echo "names_test: .claude/ is not ignored, so names.sh and spdx.sh read every local worktree"; exit 1; }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bash hack/names_test.sh`
Expected: exit 1, `names_test: .claude/ is not ignored, so names.sh and spdx.sh read every local worktree`.

- [ ] **Step 3: Ignore the directory**

Append to `.gitignore`:

```
# Claude Code's local state and its agent worktrees: full copies of the tree the gates would read.
.claude/
```

- [ ] **Step 4: Run it to verify it passes**

Run: `git ls-files .claude | wc -l && bash hack/names_test.sh && bash hack/names.sh && bash hack/spdx.sh && echo spdx-ok`
Expected: `0` (nothing under `.claude/` is tracked), `==> names_test: ok`, `==> names: no old artifact name outside the historical docs` even with `.claude/worktrees/` present, `spdx-ok`.

- [ ] **Step 5: Commit**

```bash
git add .gitignore hack/names_test.sh
git commit -m "chore: ignore .claude/ so local worktrees stay out of the gates

names.sh and spdx.sh read untracked files; with 42 agent worktrees
under .claude/ the names gate failed locally on 25k copies. Audit S-11."
```

---

### Task 5: S-12, a failed search fails the shadow-merge tally

**Files:**
- Modify: `scripts/testdata/tally/gh-stub.sh:12`
- Modify: `scripts/shadow-merge-tally_test.sh` (before `# --window is whole hours`)
- Modify: `scripts/shadow-merge-tally.sh:89`

**Interfaces:** the stub's new fixture `TALLY_FIXTURE=searchfail`: the `factory/auto-merged` search exits 1 with gh's 502 message, the `factory/merged` search serves `prs.json`.

- [ ] **Step 1: Write the failing test**

In `scripts/testdata/tally/gh-stub.sh`, replace line 12:

```bash
  *'label:"factory/merged:'*) cat "$d/prs${TALLY_FIXTURE:+-$TALLY_FIXTURE}.json" ;;
```

with:

```bash
  # Fixture "searchfail": the auto-merged search fails as gh does on a 502; the merged one answers.
  *'label:"factory/auto-merged:'*) [ "${TALLY_FIXTURE:-}" = searchfail ] && { echo 'HTTP 502: Bad Gateway' >&2; exit 1; }; echo '[]' ;;
  *'label:"factory/merged:'*) [ "${TALLY_FIXTURE:-}" = searchfail ] && TALLY_FIXTURE=; cat "$d/prs${TALLY_FIXTURE:+-$TALLY_FIXTURE}.json" ;;
```

In `scripts/shadow-merge-tally_test.sh`, insert before `# --window is whole hours: anything else is refused, not miscomputed.`:

```bash
# A failed search stops the tally (S-12): read through a process substitution, its failure was lost
# and the tally counted the other search's merges alone, rc 0 and widen=yes.
if out="$(TALLY_FIXTURE=searchfail tally 2>&1)"; then
  echo "FAIL: a failed gh search must fail the tally, got rc 0: $out" >&2; exit 1
fi
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bash scripts/shadow-merge-tally_test.sh; echo "exit=$?"`
Expected: `FAIL: a failed gh search must fail the tally, got rc 0: HTTP 502: Bad Gateway`, the 31 merge lines, a totals line ending `widen=yes`, and `exit=1`: the audit's reproduction.

- [ ] **Step 3: Capture each search before `jq -s`**

In `scripts/shadow-merge-tally.sh`, replace line 89:

```bash
  prs=$(jq -s 'add | unique_by(.number)' <(merged_with factory/merged) <(merged_with factory/auto-merged))
```

with:

```bash
  # Each search into a variable first: under set -e a failed gh then stops the tally. Read through
  # a process substitution, its failure was lost and the tally counted half the merges (S-12).
  merged=$(merged_with factory/merged)
  auto=$(merged_with factory/auto-merged)
  prs=$(printf '%s\n%s\n' "$merged" "$auto" | jq -s 'add | unique_by(.number)')
```

- [ ] **Step 4: Run it to verify it passes**

Run: `task scripts && shellcheck scripts/shadow-merge-tally.sh scripts/shadow-merge-tally_test.sh scripts/testdata/tally/gh-stub.sh && echo sc-ok`
Expected: both script tests pass (the tally test prints `ok` last), then `sc-ok`.

- [ ] **Step 5: Commit**

```bash
git add scripts/shadow-merge-tally.sh scripts/shadow-merge-tally_test.sh scripts/testdata/tally/gh-stub.sh
git commit -m "fix(scripts): a failed search fails the shadow-merge tally instead of halving it

--outcomes read its two gh searches through process substitutions, whose
exit status set -e never sees: a failed search left the other's merges
alone, rc 0 and possibly widen=yes. Each search is now captured first.
Audit S-12."
```

---

### Task 6: A-3, the contributor docs match the code

**Files:**
- Modify: `AGENTS.md` (gate table lines 15-28, package map lines 149-199, seams lines 212-222)
- Modify: `CONTRIBUTING.md:24-25`
- Modify: `docs/architecture.md` (intro lines 3-10, Components table, the two package tables, Ports, Binaries and images)
- Modify: `README.md:66`

**Interfaces:**
- Consumes: T6's `internal/tlsreload` and T9's `internal/factory/ledger` and injected `runwatch.Kind`, as merged on `main`.

- [ ] **Step 1: Rebase, then write the failing check**

```bash
git fetch origin && git rebase origin/main
go list ./... | grep -E 'internal/(tlsreload|factory/ledger)$'   # both must print; stop if not: T6 or T9 has not merged
grep -n 'func init\|^var current' internal/runwatch/*.go           # must print nothing (T9's D9)
```

The check: every package `go list` reports is named in both maps, test tooling and embed-only subpackages aside.

```bash
pkgmap() {
  for p in $(go list ./... | sed 's#github.com/Smana/maku/##' |
             grep -vE '^(charts/maku/tests/|hack/quickstart/stub-model$|internal/store/migrations$|internal/humanapi/ui$)'); do
    grep -qF "\`$p\`" AGENTS.md || echo "AGENTS.md: $p"
    grep -qF "\`$p\`" docs/architecture.md || echo "docs/architecture.md: $p"
  done
}
pkgmap
```
Expected now: lines for `cmd/maku-run-controller` (AGENTS.md), `cmd/maku-slack` (architecture), the eight `internal/factory/{decision,fence,filefence,finding,fluxwatch,mdlinks,semverdiff,steering}`, `internal/factory/ledger`, `internal/{migrategroup,roomview,runctl,sponsored,tlsreload,webhook}`, and `internal/{slackapp,suspend}` (architecture). The check passes when `pkgmap` prints nothing.

Also run `sed -n 1,12p internal/tlsreload/*.go internal/factory/ledger/*.go | grep -A4 '^// Package'`: if a contract below disagrees with the merged `// Package` comment, the comment wins.

- [ ] **Step 2: Rewrite AGENTS.md's gate table**

Replace `AGENTS.md` lines 15-28 (the table under `task check`) with the fourteen steps, in `taskfile.yaml`'s order:

```markdown
| Step | Runs | Fails on |
|---|---|---|
| `spdx` | `hack/spdx.sh` | a `.go` file, tracked or new, whose first line is not `// SPDX-License-Identifier: Apache-2.0` |
| `tidy` | `go mod tidy -diff` | `go.mod` or `go.sum` drift |
| `lint` | `hack/lint.sh` | any golangci-lint issue under [`.golangci.yaml`](.golangci.yaml), gofmt and goimports included |
| `vuln` | `go run …/govulncheck@v1.8.0 ./...` | a known vulnerability reachable from our code, stdlib included |
| `test` | `go test -race -count=1 ./...`, envtest's API server from `setup-envtest` 1.36.x | a failing test or a data race; never cached |
| `crd:check` | `crd:gen`, then `git diff --exit-code -- config/crd api charts` | a committed CRD, deepcopy or the chart's CRD copy that differs from what the types generate |
| `chart:check` | `helm lint --strict` and `tests/render.sh` for `charts/maku-factory` and `charts/maku` | either chart losing a property the platform relies on, or the maku chart rendering a config its component's loader refuses |
| `quickstart:check` | `hack/quickstart/check.sh`, offline: `bash -n`, shellcheck, `lib_test.sh` | the quickstart scripts, their manifests and the maku chart disagreeing on a name they share |
| `migrations` | `atlas migrate validate --dir file://internal/store/migrations`, offline | an `atlas.sum` that no longer matches the migrations: re-hash with `atlas migrate hash` |
| `docs` | `hack/docs_test.sh`, `hack/docs.sh` | a reader page with a per-row status tag, or a dead relative link or anchor |
| `names` | `hack/names_test.sh`, `hack/names.sh` | an artifact, API group or metric name from before the maku rebrand outside the historical docs |
| `scripts` | `scripts/*_test.sh` | an operator script, such as `shadow-merge-tally.sh`, no longer reading GitHub's shapes as its test fakes them |
| `ui:test` | `npm ci`, `tsc --noEmit`, `vitest run` in `web/` | a type error or a failing UI test |
| `ui:check` | `web/`'s build into `internal/humanapi/ui/dist/` | a committed bundle that differs from what `web/` builds, or a built file not committed |
```

- [ ] **Step 3: Complete AGENTS.md's package map**

In the rooms table, replace the `cmd/maku-broker` row with:

```markdown
| Entry | `cmd/maku-broker` | `serve`, `retention`, `migrate` (the schema, the chart's init container) and `migrate-group` (the maku rebrand's group move) subcommands |
```

After the `internal/version` row, add:

```markdown
| Entry | `cmd/maku-run-controller` | the run controller binary, on clusters without cloud-native-ref's Crossplane runtime; its wiring is `app.RunController` |
```

After the `internal/authn` row, add:

```markdown
| Ingress | `internal/sponsored` | what the broker and the factory must agree on for a sponsored principal: the names neither may give one, the shape of the git ref it may send |
```

After the `internal/suspend` row, add:

```markdown
| Runs | `internal/runctl` | the run controller: `maku.ogenki.io` AgentRuns reconciled into the pod, ServiceAccount, ConfigMaps and CiliumNetworkPolicy that cloud-native-ref's composition builds, minus the gateway, octo-sts, traces, Kueue and suspend |
```

After the `internal/prpattern` row, add:

```markdown
| API | `internal/tlsreload` | the reloading certificate loader both TLS servers import (D7): `:8443` and the factory's run-request API re-read their pair when it is renewed |
| API | `internal/webhook` | the broker's outbound events (F6): id-only payloads, signed as Standard Webhooks sign them; the broker and the Slack App use it |
```

After the `internal/summary` row, add:

```markdown
| Viewers | `internal/roomview` | the factory's four room views as `maku` renders them; the one place that makes untrusted text inert |
```

After the `internal/metrics` row, add:

```markdown
| Ops | `internal/migrategroup` | `maku-broker migrate-group`: copies Rooms and Tasks from one API group to another, behind a guard annotation, the maku rebrand's one-shot |
```

In the factory table, after the `internal/factory/sanitize`, `internal/factory/taskid` row, add:

```markdown
| Intake | `internal/factory/fence` | the single-owner fence: a repository is acted on only by the factory its default branch's `.github/agent-factory.yaml` names |
| Intake | `internal/factory/finding` | what both RunLore intakes write the same way: the task text, the public stub issue, the label charset that keeps attacker-influenced identifiers inert |
```

After the `internal/factory/runs`, `internal/factory/killswitch` row, add:

```markdown
| Tasks | `internal/factory/steering` | a task class's steering note, read on the default branch at triage; an agent pull request that changes one is refused |
| Tasks | `internal/factory/ledger` | the budget ledger (D8): the day ledgers' names and layout, a day's use, a run's release, the settled columns; `api`, `intake`, `meter` and `reconciler` import it |
| Gate | `internal/factory/filefence`, `internal/factory/mdlinks`, `internal/factory/semverdiff` | a class's file conditions on a diff, one implementation for the gate and the adopt poller; Markdown link targets as the links-only shape reads them; whether a diff is a dependency bump of one kind |
| Gate | `internal/factory/decision` | the per-decision record: the values each declared row read, so the tally can replay a cap |
| Gate | `internal/factory/fluxwatch` | Flux's objects for the post-merge monitor (MC7): uncached, read-only, `unstructured` |
```

- [ ] **Step 4: Update AGENTS.md's seams**

Replace the `internal/runwatch` seam row with (match the names T9 merged: `grep -n 'runwatch.Kind' internal/app/*.go`):

```markdown
| `internal/runwatch` | the run kind (`runwatch.Kind`: group, namespace, claim-name prefix) is config, the broker's and the factory's `runs:`, built in `internal/app` and passed to each consumer; unset, `runwatch.DefaultKind`, cloud-native-ref's `cloud.ogenki.io`, `agents`, `xplane-run-`. The `agents.ogenki.io/revoked` annotation and the three suspend annotations are constants; a `RunSource` interface would replace them in a spin-out |
```

After the `internal/runrequest` row, add:

```markdown
| `internal/runctl` | the `maku.ogenki.io` `AgentRun` CRD (typed, `api/v1alpha1`), Cilium's `CiliumNetworkPolicy` and the run pod's layout (the bridge as a native sidecar, the broker CA at `/etc/room-broker-ca`); the runs namespace, name prefix, RuntimeClass, images, Secrets, broker URL, labels and CA Secret are config, and the egress profiles have built-in defaults (github, pypi, npm, golang, crates) |
| `internal/migrategroup` | the kinds it moves (`Room`, `Task`), the version `v1alpha1` and the guard annotation `v1alpha1.AnnotationMigrating`; the two groups come from `--from` and `--to`. Removed once every cluster has migrated |
```

- [ ] **Step 5: CONTRIBUTING.md's step count**

Replace `CONTRIBUTING.md` lines 24-25 (`It runs eleven steps (SPDX headers, tidy,` … `UI test, UI check);`) with:

```markdown
`task check` must exit 0 before every commit and push. It runs fourteen steps (SPDX headers, tidy,
lint, vuln, test, CRD check, chart check, quickstart check, migrations, docs lint, old-name check,
operator scripts, UI test, UI check); CI's required `check`
```

(the next line, `job runs the same command. …`, stays.)

- [ ] **Step 6: docs/architecture.md's binaries, images, ports and package tables**

Replace the intro paragraph (lines 3-10, from `Four binaries and one image.` to `every connection from` … `the pod opens.`) with:

```markdown
Five services, one CLI and one run image. **`maku-factory`** turns a labelled GitHub issue into a
narrated run and merges the result through its gate. **`maku-broker`** is the log of record: it owns
the `Room` CRD, authenticates every writer, redacts and sequences events, and stores each room's log
in Postgres. **`maku-bridge`** is a native sidecar in every `AgentRun` pod that names a room: it polls
the harness on loopback and pushes its events to the broker over TLS on `:8443`.
**`maku-run-controller`**, on a cluster without cloud-native-ref's Crossplane runtime, turns each
`maku.ogenki.io` `AgentRun` into a gVisor pod. **`maku-slack`** is the factory's Slack App: a thread
becomes a task, and the broker's webhooks post the room's progress back to it. **`maku`** is the
terminal client for people. **`maku-harness`** is the image the run executes in (OpenHands
agent-server, `gh`, the pull-request footer). Nothing dials into a sandbox: every connection from
the `agents` namespace is one the pod opens.
```

In the Components table, after the `maku-harness` row, add:

```markdown
| `maku-run-controller` | Deployment `run-controller`, chart `charts/maku`, on clusters without cloud-native-ref's Crossplane runtime | Reconciles `maku.ogenki.io` `AgentRun`s into a run's pod, ServiceAccount, ConfigMaps and CiliumNetworkPolicy |
| `maku-slack` | A Deployment beside the factory ([Slack App](slack.md)) | Slack events on `:8080` start factory tasks from a thread; the broker's webhooks on `:8081` post each room's progress back to it |
```

In the "Packages split by domain" table, replace the `` `cmd/<bin>` stays thin `` row's second cell with:

```markdown
`cmd/maku-broker`, `cmd/maku-bridge`, `cmd/maku`, `cmd/maku-factory`, `cmd/maku-run-controller` and `cmd/maku-slack` parse arguments and dispatch; the wiring lives in `internal/app`
```

In the "Broker, bridge and CLI, by pipeline stage" table, replace the `cmd/maku-broker` row and add the missing rows (two-column `| Package | Stage |` form):

```markdown
| `cmd/maku-broker` | `serve`, `retention`, `migrate` and `migrate-group` subcommands |
| `cmd/maku-slack`, `internal/slackapp` | The factory's Slack App: intake from a thread, webhooks to the thread ([Slack App](slack.md)) |
| `internal/runctl` | The run controller: `maku.ogenki.io` AgentRuns into pod, ServiceAccount, ConfigMaps and CNP |
| `internal/sponsored` | What the broker and the factory must agree on for a sponsored principal |
| `internal/suspend` | The suspend trigger: a suspendable run waiting on an approval in an idle attended room is suspended, and resumed once nothing waits |
| `internal/tlsreload` | The reloading certificate loader of `:8443` and the factory's run-request API |
| `internal/webhook` | The broker's outbound events: id-only payloads, signed as Standard Webhooks sign them |
| `internal/roomview` | The factory's four room views as `maku` renders them; the one place that makes untrusted text inert |
| `internal/migrategroup` | `maku-broker migrate-group`: Rooms and Tasks copied from one API group to another |
```

In the factory table (`| Stage | Package | Contract |`), add the same seven factory rows as Step 3 (`fence`, `finding`, `steering`, `ledger`, the `filefence`/`mdlinks`/`semverdiff` row, `decision`, `fluxwatch`), capitalising each contract's first word as that table does.

In the Ports table, add after the `:9091` row:

```markdown
| `:8085` | maku-bridge | The kubelet's probes of the native sidecar (`HEALTH_ADDR`) |
| `:8080` | maku-slack | Slack's events, `POST /slack/events` |
| `:8081` | maku-slack | The broker's webhooks, `POST /webhooks`, cluster only |
| `:9090` | maku-slack | Metrics and probes |
```

Leave the `:8082` row as T6 left it.

In the Binaries and images table, replace the `maku-broker` row's second cell with `Stateless service in `agent-system`; owns the `Room` CRD (`maku.ogenki.io/v1alpha1`); subcommands `serve`, `retention`, `migrate`, `migrate-group``, and after the `maku-run-controller` row add:

```markdown
| `maku-slack` | The factory's Slack App, a Deployment beside the factory; its config is `--config` or `SLACK_APP_CONFIG` ([Slack App](slack.md)) | `ghcr.io/smana/maku-slack` |
```

- [ ] **Step 7: README.md's status line**

Replace `README.md:66` (`v0.15.2, in daily use on one cloud for one public repository, with the merge gate in shadow. It`) with:

```markdown
In daily use on one cloud for one public repository, with the merge gate in shadow; the version and its notes are on the [latest release](https://github.com/Smana/maku/releases/latest). It
```

(No version number in prose: the release page cannot drift. Confirm "with the merge gate in shadow" with the owner before merging; the audit did not re-check it.)

- [ ] **Step 8: Run the checks to verify they pass**

Run: `pkgmap; bash hack/docs.sh && bash hack/names.sh`
Expected: `pkgmap` prints nothing; `hack/docs.sh` exits 0; `==> names: no old artifact name outside the historical docs`.

- [ ] **Step 9: Commit**

```bash
git add AGENTS.md CONTRIBUTING.md docs/architecture.md README.md
git commit -m "docs: the package map, gate table, binaries, ports and status line match the code

AGENTS.md and docs/architecture.md missed 16 packages and two binaries,
the gate table listed 12 of 14 steps and one chart, the runwatch seam
predated the injected Kind, runctl and migrategroup had no seam row,
CONTRIBUTING counted eleven steps, architecture.md said four binaries
and missed maku-slack and the bridge's health port, and the README
said v0.15.2. Audit A-3."
```

---

### Task 7: The gate, and the pull request

**Files:** none changed.

- [ ] **Step 1: Run the whole gate once**

Run: `task check; echo "task check exit=$?"` (Docker running: the store tests use testcontainers)
Expected: `task check exit=0`. A failure is fixed in the task that owns the file, folded into that task's commit (`git commit --fixup <sha>`, then `git rebase --autosquash origin/main`), and the gate re-run.

- [ ] **Step 2: Review the branch**

Run: `git log --oneline origin/main..HEAD && git log origin/main..HEAD --format=%B | grep -c 'Co-Authored-By\|Generated with'`
Expected: six commits, Task 1 to Task 6 in order; then `0`.

- [ ] **Step 3: Open the pull request (only when the owner says to push)**

```bash
git push -u origin chore/audit-t10-hygiene
gh pr create --base main --title "chore: audit T10, hygiene and docs (H-6, S-8, S-10, S-11, S-12, A-3)" --body "$(cat <<'EOF'
Theme T10 of docs/superpowers/specs/2026-11-01-audit-fixes-design.md, one commit per finding.

| Finding | Fix | Pinned by |
|---|---|---|
| H-6 | `fetch-runsc.sh` replaces gVisor's own files through `qs_install_gvisor`; the `maku` up.sh installed survives | `hack/quickstart/lib_test.sh`, `check.sh` |
| S-8 | Dependabot's `npm` ecosystem for `/web`, same cooldown and major ignore | yq check |
| S-10 | A root `.dockerignore` for the Go images' context | build context: <BEFORE> excluded, <AFTER> sent (Task 3, Step 3) |
| S-11 | `.gitignore` gains `.claude/` | `hack/names_test.sh` |
| S-12 | `shadow-merge-tally.sh --outcomes` captures each search before `jq -s` | `scripts/shadow-merge-tally_test.sh`, fixture `searchfail` |
| A-3 | AGENTS.md, CONTRIBUTING.md, docs/architecture.md, README.md match the packages, gate steps, binaries and ports | package-list diff, `hack/docs.sh` |

A Dependabot npm PR changes the committed UI bundle, so it fails `ui:check` until `task ui:build` is committed on its branch.

`task check` exit 0.
EOF
)"
```

Replace `<BEFORE>` and `<AFTER>` with Task 3's figures before running it.

---

## Self-review

| Spec item | Task |
|---|---|
| H-6: remove only `runsc`, `containerd-shim-runsc-v1`, `gvisor-bin/` and the stamp; a `maku` survives a refetch | 1 |
| S-8: `npm` for `/web`, same cooldown and major-ignore | 2 |
| S-10: root `.dockerignore` with `.git`, `.claude`, `.quickstart`, `web/node_modules`, `**/__pycache__`; context size in the PR | 3 (+ `.superpowers`: deviation 1) |
| S-11: `.gitignore` gains `.claude/`; `names.sh` passes with worktrees present | 4 |
| S-12: each search into a variable under `set -e` before `jq -s`; a failing-search test expecting non-zero | 5 |
| A-3: 13 packages + `cmd/maku-run-controller` in the map, runwatch seam (configurable kind), runctl and migrategroup seam rows, 14-step gate table with both charts under `chart:check`, CONTRIBUTING's count, architecture's binaries, images and ports, README's status line; T9's `internal/factory/ledger` and T6's `internal/tlsreload` included | 6 |
| D0: one PR, one commit per finding with its check, `task check` exit 0 | Global Constraints, 7 |

Left in the audit, outside T10's rows: the other "Docs drift" items (`ci.yaml:163`'s comment, `metrics.go:136`, the chart READMEs' missing keys, the factory and broker sharing a namespace).

Tasks 1 and 5 were run on 2026-11-01 in a scratch export of `e14b7949`: the new `lib_test.sh` and `check.sh` lines pass and shellcheck is clean; the tally test fails before the fix with the audit's exact reproduction (rc 0, `widen=yes`) and passes after it.
