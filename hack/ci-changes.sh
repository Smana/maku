#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# What a pull request touches, for ci.yaml's path-gated jobs: key=value lines on stdout (and in
# $GITHUB_OUTPUT when set).
#   harness     images/maku-harness/ (the harness image, its suites and its checks), or this gating
#   deployable  anything but documentation (docs/, LICENSE, the root's *.md): what the prereleases build
# On any event but pull_request every key is true: main and tags run everything.
#
# It reads the merge commit a pull_request checks out (fetch-depth 2): HEAD^1 is the base, so
# HEAD^1..HEAD is the pull request's change against the base it merges into. A missing parent is an
# error, never "nothing changed": the gated jobs fail closed.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

emit() {
  printf '%s=%s\n' "$1" "$2"
  [[ -z ${GITHUB_OUTPUT:-} ]] || printf '%s=%s\n' "$1" "$2" >>"$GITHUB_OUTPUT"
}

if [[ ${GITHUB_EVENT_NAME:-} != pull_request ]]; then
  emit harness true
  emit deployable true
  exit 0
fi

git rev-parse -q --verify 'HEAD^2' >/dev/null ||
  { echo "ci-changes: HEAD is not a merge commit: check out the pull request's merge with fetch-depth 2" >&2; exit 1; }
files="$(git diff --name-only 'HEAD^1' HEAD)"
printf 'changed:\n%s\n' "$files" >&2

harness=false deployable=false
while IFS= read -r f; do
  [[ -n $f ]] || continue
  case "$f" in
    images/maku-harness/* | .github/workflows/ci.yaml | hack/ci-changes.sh) harness=true ;;
  esac
  # Documentation is docs/, LICENSE and the root's *.md only: markdown deeper down can be code
  # (internal/maku/skill/ embeds its files in the binary) or ship in a chart.
  case "$f" in
    docs/* | LICENSE) ;;
    */*) deployable=true ;;
    *.md) ;;
    *) deployable=true ;;
  esac
done <<<"$files"
emit harness "$harness"
emit deployable "$deployable"
