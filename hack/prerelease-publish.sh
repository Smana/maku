#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# hack/prerelease-publish.sh DIGESTS VERSION IMAGE...: ci.yaml's prerelease-publish. For each IMAGE
# whose two platform digests image.yaml left in DIGESTS/digest-IMAGE-*/, joins them under
# ghcr.io/smana/IMAGE:VERSION and signs the index by digest (keyless cosign: the caller's identity).
#   - an image with no digest: an error, unless BUILDS=failure (the prerelease builds' result), when
#     it is the image whose build failed: skipped, with a notice;
#   - an image with a single digest is half-pushed: an error.
# Every image is tried; the errors are collected, and the script fails at the end if there was one.
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

# publish IMAGE: join and sign one image; 1 on any error (set -e does not reach into an `if`).
publish() {
  local image=$1 ref="ghcr.io/smana/$1" files f d digest sources=()
  files=("$dir/digest-${image}-"*/*)
  if ((${#files[@]} == 0)); then
    if [[ $builds == failure ]]; then
      echo "::notice title=prerelease-publish::$image has no digest: its build failed; not published"
      return 0
    fi
    echo "prerelease-publish: $image has no digest, yet the builds succeeded" >&2
    return 1
  fi
  ((${#files[@]} == 2)) || { echo "prerelease-publish: want 2 platform digests for $image, got ${#files[@]}" >&2; return 1; }
  for f in "${files[@]}"; do
    d="$(<"$f")"
    [[ $d =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "prerelease-publish: bad digest '$d' in $f" >&2; return 1; }
    sources+=("${ref}@${d}")
  done
  retry "join $image" docker buildx imagetools create -t "${ref}:${version}" "${sources[@]}" || return 1
  digest="$(retry "read ${ref}:${version} back" index_digest "${ref}:${version}")" || return 1
  retry "sign ${ref}@${digest}" cosign sign --yes "${ref}@${digest}" || return 1
  [[ -z ${GITHUB_STEP_SUMMARY:-} ]] || printf '### %s\n%s:%s@%s\n' "$image" "$ref" "$version" "$digest" >>"$GITHUB_STEP_SUMMARY"
  published+=("$image")
}

builds="${BUILDS:-success}"
published=() failed=()
for image in "$@"; do
  publish "$image" || failed+=("$image")
done
echo "prerelease-publish: published at $version: ${published[*]:-none}"
if ((${#failed[@]})); then
  echo "prerelease-publish: not published: ${failed[*]}" >&2
  exit 1
fi
