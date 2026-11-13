#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# agentgateway v1.6.0, vendored so no install path needs the network when the gateway is off, and
# none trusts a tag that could move upstream:
#   charts/agent-factory-crds/files/agentgateway/  its four CRDs, rendered with the keep annotation
#                                                  when agentgateway-crds.enabled is true;
#   charts/agent-factory/vendor/agentgateway/      its controller chart, the umbrella's file:// dependency.
# charts/agentgateway.sha256 pins every vendored file.
#
#   agentgateway-vendor.sh check [DIR]    offline: every vendored file under DIR (default charts) matches its pins
#                                         (task chart:deps; the publish jobs run it on their copy)
#   agentgateway-vendor.sh update         pulls both upstream charts by digest, re-vendors them and
#                                         rewrites the pins: review the diff with the version bump
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
VERSION=v1.6.0
CRDS_DIGEST=sha256:dd7f965d43e54dffa9c95af089b6f762397a23c963a159ad6d4992f6c582292a
CHART_DIGEST=sha256:d3edf2b33cbf2e9d59406c207b8d3554597799707b8803b5f62e698fe5376e28
PINS=charts/agentgateway.sha256
mode="${1:-check}"
case "$mode" in
  check)
    # DIR is the repository's charts/ or a copy of it (the publish jobs'); the pins are always this
    # checkout's. The check only proves the tree matches the committed pins: what anchors them to
    # upstream is `update` pulling by the OCI digests above, and the review of its diff.
    dir="${2:-charts}"
    pins="$PWD/$PINS"
    (cd "$dir" && sha256sum --quiet --strict -c "$pins") ||
      { echo "FAIL: a vendored agentgateway file under $dir differs from $PINS: re-vendor with 'hack/agentgateway-vendor.sh update'" >&2; exit 1; }
    # nothing vendored that the pins do not name: every entry but a directory, so a symlink or any
    # other non-regular file is listed (and refused) rather than skipped
    extra="$(cd "$dir" && comm -23 <(find agent-factory-crds/files/agentgateway agent-factory/vendor/agentgateway ! -type d | sort) <(awk '{print $2}' "$pins" | sort))"
    [[ -z $extra ]] || { echo "FAIL: vendored files missing from $PINS: $extra" >&2; exit 1; }
    echo "agentgateway $VERSION: the vendored CRDs and controller chart under $dir match $PINS"
    ;;
  update)
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    mkdir "$tmp/crds" "$tmp/chart"
    helm pull "oci://cr.agentgateway.dev/charts/agentgateway-crds:$VERSION@$CRDS_DIGEST" -d "$tmp/crds" >/dev/null
    helm pull "oci://cr.agentgateway.dev/charts/agentgateway:$VERSION@$CHART_DIGEST" -d "$tmp/chart" >/dev/null
    tar -xzf "$tmp"/crds/*.tgz -C "$tmp"
    tar -xzf "$tmp"/chart/*.tgz -C "$tmp"
    rm -rf charts/agent-factory-crds/files/agentgateway charts/agent-factory/vendor/agentgateway
    mkdir -p charts/agent-factory-crds/files/agentgateway charts/agent-factory/vendor
    cp "$tmp"/agentgateway-crds/templates/*.yaml charts/agent-factory-crds/files/agentgateway/
    cp -r "$tmp"/agentgateway charts/agent-factory/vendor/agentgateway
    (cd charts && find agent-factory-crds/files/agentgateway agent-factory/vendor/agentgateway -type f | sort | xargs sha256sum) >"$PINS"
    echo "vendored agentgateway $VERSION; review $PINS"
    ;;
  *) echo "usage: agentgateway-vendor.sh check [DIR] | update" >&2; exit 2 ;;
esac
