#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# agentgateway v1.6.0, vendored so no install path needs the network, and none trusts a tag that
# could move upstream:
#   config/agentgateway/                       its four CRDs, applied apart from any Helm release with
#                                              `kubectl apply --server-side` (docs/gateway.md): in a
#                                              release they push its Secret to the 1 MiB limit;
#   charts/agent-factory/vendor/agentgateway/  its controller chart, the umbrella's file:// dependency.
# hack/agentgateway.sha256 pins every vendored file, by path from the repository root.
#
#   agentgateway-vendor.sh check               offline: every vendored file matches its pin, and no
#                                              file is vendored without one (task chart:deps)
#   agentgateway-vendor.sh check-copy CHARTS   the same for the controller chart in a copy of charts/
#                                              (the publish jobs, before `helm dependency build`)
#   agentgateway-vendor.sh update              pulls both upstream charts by digest, re-vendors them
#                                              and rewrites the pins: review the diff with the bump
# The checks prove the tree matches the committed pins. What anchors the pins to upstream is
# `update` pulling by the OCI digests below, and the review of its diff.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
VERSION=v1.6.0
CRDS_DIGEST=sha256:dd7f965d43e54dffa9c95af089b6f762397a23c963a159ad6d4992f6c582292a
CHART_DIGEST=sha256:d3edf2b33cbf2e9d59406c207b8d3554597799707b8803b5f62e698fe5376e28
PINS=hack/agentgateway.sha256
CRDS=config/agentgateway
CHART=charts/agent-factory/vendor/agentgateway

# verify ROOT PINFILE DIR...: every pinned file under ROOT matches, and every entry under DIR... is
# pinned. Entries are listed with `! -type d`, so a symlink or any non-regular file is refused, not skipped.
verify() {
  local root=$1 pins=$2 extra; shift 2
  (cd "$root" && sha256sum --quiet --strict -c "$pins") ||
    { echo "FAIL: a vendored agentgateway file under $root differs from $PINS: re-vendor with 'hack/agentgateway-vendor.sh update'" >&2; exit 1; }
  extra="$(cd "$root" && comm -23 <(find "$@" ! -type d | sort) <(awk '{print $2}' "$pins" | sort))"
  [[ -z $extra ]] || { echo "FAIL: vendored but not pinned in $PINS: $extra" >&2; exit 1; }
}

case "${1:-check}" in
  check)
    verify . "$PWD/$PINS" "$CRDS" "$CHART"
    echo "agentgateway $VERSION: the vendored CRDs and controller chart match $PINS"
    ;;
  check-copy)
    copy="${2:?usage: agentgateway-vendor.sh check-copy CHARTS_DIR}"
    tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
    # the controller chart's pins, with their paths taken relative to charts/
    awk -v p="$CHART/" 'index($2, p) == 1 { print $1 "  " substr($2, length("charts/") + 1) }' "$PINS" >"$tmp"
    [[ -s $tmp ]] || { echo "FAIL: $PINS pins no file under $CHART" >&2; exit 1; }
    verify "$copy" "$tmp" "${CHART#charts/}"
    echo "agentgateway $VERSION: the controller chart under $copy matches $PINS"
    ;;
  update)
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    mkdir "$tmp/crds" "$tmp/chart"
    helm pull "oci://cr.agentgateway.dev/charts/agentgateway-crds:$VERSION@$CRDS_DIGEST" -d "$tmp/crds" >/dev/null
    helm pull "oci://cr.agentgateway.dev/charts/agentgateway:$VERSION@$CHART_DIGEST" -d "$tmp/chart" >/dev/null
    tar -xzf "$tmp"/crds/*.tgz -C "$tmp"
    tar -xzf "$tmp"/chart/*.tgz -C "$tmp"
    rm -rf "$CRDS" "$CHART"
    mkdir -p "$CRDS" "$(dirname "$CHART")"
    cp "$tmp"/agentgateway-crds/templates/*.yaml "$CRDS"/
    cp -r "$tmp"/agentgateway "$CHART"
    find "$CRDS" "$CHART" -type f | sort | xargs sha256sum >"$PINS"
    echo "vendored agentgateway $VERSION; review $PINS"
    ;;
  *) echo "usage: agentgateway-vendor.sh check | check-copy CHARTS_DIR | update" >&2; exit 2 ;;
esac
