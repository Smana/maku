#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# task chart:deps: builds the dependencies of the charts that pull an upstream chart (the umbrella's
# agentgateway, agent-factory-crds' agentgateway-crds) and checks each upstream archive against the
# chart's dependencies.sha256. Chart.yaml pins the exact version; the checksum pins the content, so a
# tag moved upstream fails here rather than shipping. After a deliberate bump, rewrite the file with
# `sha256sum` over the new archive and review it with the version.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
fail=0
for chart in charts/agent-factory-crds charts/agent-factory; do
  helm dependency build "$chart" >/dev/null
  pins="$chart/dependencies.sha256"
  [[ -s $pins ]] || { echo "FAIL: $pins is missing" >&2; fail=1; continue; }
  # every oci:// dependency has its archive pinned, and every pin matches
  while read -r name version; do
    grep -qE "  $name-$version\.tgz$" "$pins" || { echo "FAIL: $pins pins no $name-$version.tgz (Chart.yaml's $name $version)" >&2; fail=1; }
  done < <(yq '.dependencies[] | select(.repository | test("^oci://")) | .name + " " + .version' "$chart/Chart.yaml")
  (cd "$chart/charts" && sha256sum --quiet -c "../dependencies.sha256") || { echo "FAIL: an upstream archive of $chart differs from $pins" >&2; fail=1; }
done
[[ $fail == 0 ]] || exit 1
echo "chart dependencies match their pinned checksums"
