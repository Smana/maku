#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# hack/prerelease-publish.sh DIGESTS VERSION IMAGE...: ci.yaml's prerelease-publish. For each IMAGE
# whose two platform digests image.yaml left in DIGESTS/digest-IMAGE-*/, joins them under
# ghcr.io/smana/IMAGE:VERSION and signs the index by digest (keyless cosign: the caller's identity).
#   - an image with no digest was not built: skipped, with a notice;
#   - one with a single digest is half-pushed: an error;
#   - no image built at all: nothing to publish, and success.
# ghcr can answer "not found" for a tag it has just accepted, and Sigstore can answer a 5xx: both
# are retried (RETRY_SLEEP seconds times the attempt, 5 by default).
set -euo pipefail
shopt -s nullglob

dir="${1:?usage: prerelease-publish.sh DIGESTS VERSION IMAGE...}"
version="${2:?usage: prerelease-publish.sh DIGESTS VERSION IMAGE...}"
shift 2
(($#)) || { echo "prerelease-publish: no image named" >&2; exit 2; }
pause="${RETRY_SLEEP:-5}"

# retry WHAT CMD...: CMD, up to five times.
retry() {
  local what=$1 attempt
  shift
  for attempt in 1 2 3 4 5; do
    "$@" && return 0
    ((attempt < 5)) || break
    echo "prerelease-publish: $what, attempt $attempt failed; retrying" >&2
    sleep $((attempt * pause))
  done
  echo "prerelease-publish: $what failed" >&2
  return 1
}

index_digest() {
  local d
  d="$(docker buildx imagetools inspect "$1" --format '{{ json .Manifest }}' | jq -r .digest)" || return 1
  [[ $d =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  printf '%s\n' "$d"
}

published=0
for image in "$@"; do
  ref="ghcr.io/smana/${image}"
  files=("$dir/digest-${image}-"*/*)
  if ((${#files[@]} == 0)); then
    echo "::notice title=prerelease-publish::$image was not built: nothing to publish"
    continue
  fi
  ((${#files[@]} == 2)) || { echo "prerelease-publish: want 2 platform digests for $image, got ${#files[@]}" >&2; exit 1; }
  sources=()
  for f in "${files[@]}"; do
    d="$(<"$f")"
    [[ $d =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "prerelease-publish: bad digest '$d' in $f" >&2; exit 1; }
    sources+=("${ref}@${d}")
  done
  retry "join $image" docker buildx imagetools create -t "${ref}:${version}" "${sources[@]}"
  digest="$(retry "read ${ref}:${version} back" index_digest "${ref}:${version}")"
  retry "sign ${ref}@${digest}" cosign sign --yes "${ref}@${digest}"
  [[ -z ${GITHUB_STEP_SUMMARY:-} ]] || printf '### %s\n%s:%s@%s\n' "$image" "$ref" "$version" "$digest" >>"$GITHUB_STEP_SUMMARY"
  published=$((published + 1))
done
((published)) || echo "::notice title=prerelease-publish::no image was built: nothing published"
echo "prerelease-publish: $published image(s) published at $version"
