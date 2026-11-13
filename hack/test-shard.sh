#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# hack/test-shard.sh I N: the I-th (1-based) of N disjoint slices of `go test -race -count=1 ./...`.
# CI runs the N slices on N runners; together they run every test `task test` runs, once.
#
# The slices split test names, not packages: internal/factory/reconciler alone takes over six
# minutes, so a package-level split would leave one runner holding it. Every Test, Fuzz and Example
# that `go test -list` reports (the authoritative list: build tags, generated files) is sorted by
# name and dealt round-robin. A shard runs, in one `go test` (so its packages still run in
# parallel), the packages that hold at least one of its names, with -run anchored on those names.
# A name two packages share falls to one shard, which runs it in both.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

i="${1:-}" n="${2:-}"
if ! [[ $i =~ ^[0-9]+$ && $n =~ ^[0-9]+$ ]] || ((i < 1 || i > n)); then
  echo "usage: hack/test-shard.sh I N (1 <= I <= N)" >&2
  exit 2
fi

# `go test -list` prints a package's matching names, then its "ok" line; TestMain runs (it may log
# to stdout), so only lines that are a test's name are kept. -race: the same build as the run below,
# which then comes from the cache.
listing="$(go test -race -list '.' ./...)"
declare -A pkgs_of=() # name -> space-separated packages that declare it
names=()
while IFS= read -r line; do
  if [[ $line =~ ^ok[[:space:]]+([^[:space:]]+) ]]; then
    for t in "${names[@]}"; do pkgs_of[$t]+="${BASH_REMATCH[1]} "; done
    names=()
  elif [[ $line =~ ^(Test|Fuzz|Example)[[:alnum:]_]*$ ]]; then
    names+=("$line")
  fi
done <<<"$listing"
((${#pkgs_of[@]})) || { echo "test-shard: go test -list found no tests" >&2; exit 1; }

mapfile -t all < <(printf '%s\n' "${!pkgs_of[@]}" | LC_ALL=C sort)
mine=()
declare -A pkgs=()
for ((k = i - 1; k < ${#all[@]}; k += n)); do
  mine+=("${all[k]}")
  for p in ${pkgs_of[${all[k]}]}; do pkgs[$p]=1; done
done
((${#mine[@]})) || { echo "test-shard: shard $i of $n holds no tests (${#all[@]} in all): use fewer shards" >&2; exit 1; }

run="^($(IFS='|'; echo "${mine[*]}"))\$"
# One argument may not exceed 128 KiB on Linux (MAX_ARG_STRLEN): use more shards before it does.
((${#run} < 120000)) || { echo "test-shard: the -run pattern is ${#run} bytes: use more shards" >&2; exit 1; }

mapfile -t plist < <(printf '%s\n' "${!pkgs[@]}" | LC_ALL=C sort)
echo "test-shard $i/$n: ${#mine[@]} of ${#all[@]} test names, in ${#plist[@]} packages"
exec go test -race -count=1 -run "$run" "${plist[@]}"
