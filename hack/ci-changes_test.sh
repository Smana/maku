#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# hack/ci-changes.sh on synthetic pull request merges: what each change gates on.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
g() { git -C "$tmp" -c user.email=t@t -c user.name=t "$@"; }
g init -q -b main
mkdir -p "$tmp/hack" && cp "$here/ci-changes.sh" "$tmp/hack/"
g add -A && g commit -qm base

# want PATH... -- KEY=VALUE...: a merge of a branch touching PATHs yields every KEY=VALUE.
want() {
  local paths=() f out
  while [[ $1 != -- ]]; do paths+=("$1"); shift; done; shift
  g checkout -q -b pr main
  for f in "${paths[@]}"; do mkdir -p "$(dirname "$tmp/$f")"; echo x >>"$tmp/$f"; done
  g add -A && g commit -qm pr
  g checkout -q main && g merge -q --no-ff pr -m merge
  out=$(GITHUB_EVENT_NAME=pull_request GITHUB_OUTPUT='' bash "$tmp/hack/ci-changes.sh" 2>/dev/null)
  for kv in "$@"; do
    grep -qxF "$kv" <<<"$out" || { echo "ci-changes: ${paths[*]}: want $kv, got: $out"; exit 1; }
  done
  g reset -q --hard HEAD^1 && g branch -qD pr
}

want docs/a.md README.md LICENSE -- deployable=false harness=false
# Embedded markdown is code: the skill files ship in the maku binary (ANR pins per-PR images).
want internal/maku/skill/factory-handoff/SKILL.md -- deployable=true harness=false
want charts/maku/README.md -- deployable=true
want internal/x.go -- deployable=true harness=false
want images/maku-harness/x.py -- deployable=true harness=true
# The install jobs (demo-e2e.yaml, v4 step 7 PR F): the charts, the wizard, the preflight, their harness.
want charts/maku/values.yaml -- install=true
want internal/install/x.go -- install=true
want internal/preflight/x.go -- install=true
want hack/install-e2e/lib.sh -- install=true
want hack/e2e-stub/github.go -- install=true
want internal/broker/x.go -- install=false
want docs/install.md -- install=false

# Not a merge commit: an error, never "nothing changed".
if GITHUB_EVENT_NAME=pull_request GITHUB_OUTPUT='' bash "$tmp/hack/ci-changes.sh" >/dev/null 2>&1; then
  echo "ci-changes: passed on a non-merge HEAD"; exit 1
fi
# Any other event: everything runs.
out=$(GITHUB_EVENT_NAME=push GITHUB_OUTPUT='' bash "$tmp/hack/ci-changes.sh")
grep -qx deployable=true <<<"$out" && grep -qx harness=true <<<"$out" && grep -qx install=true <<<"$out"
echo "==> ci-changes_test: ok"
