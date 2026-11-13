#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# hack/prerelease-publish.sh against stub docker and cosign: what it joins, signs and refuses.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
# docker: logs each call; `imagetools inspect` answers "not found" while $tmp/notfound counts down.
cat >"$tmp/bin/docker" <<EOF
#!/usr/bin/env bash
echo "docker \$*" >>"$tmp/log"
if [[ \$3 == inspect ]]; then
  n=\$(cat "$tmp/notfound" 2>/dev/null || echo 0)
  if ((n > 0)); then echo \$((n - 1)) >"$tmp/notfound"; echo "ERROR: \$4: not found" >&2; exit 1; fi
  printf '{"digest":"sha256:%064d"}\n' 9
fi
EOF
printf '#!/usr/bin/env bash\necho "cosign $*" >>"%s/log"\n' "$tmp" >"$tmp/bin/cosign"
chmod +x "$tmp/bin/docker" "$tmp/bin/cosign"
export PATH="$tmp/bin:$PATH" RETRY_SLEEP=0 GITHUB_STEP_SUMMARY=''
d1="sha256:$(printf '%064d' 1)" d2="sha256:$(printf '%064d' 2)"
digests() {
  mkdir -p "$tmp/d/digest-$1-0" "$tmp/d/digest-$1-1"
  echo "$d1" >"$tmp/d/digest-$1-0/linux-0"
  echo "$d2" >"$tmp/d/digest-$1-1/linux-1"
}
run() { rm -f "$tmp/log"; touch "$tmp/log"; bash "$here/prerelease-publish.sh" "$tmp/d" v1.0.1-pr1.abcdef12 "$@"; }
fail() { echo "prerelease-publish_test: $*"; cat "$tmp/log"; exit 1; }

# Nothing built while the builds failed: success, nothing joined or signed.
mkdir -p "$tmp/d"
BUILDS=failure run maku-broker maku-bridge >/dev/null || fail "no digests, builds failed: want success"
[[ ! -s $tmp/log ]] || fail "no digests: want no docker or cosign call"
# No digest although the builds succeeded: an error.
if run maku-broker >/dev/null 2>&1; then fail "no digests, builds succeeded: want a failure"; fi

# Some built (another build failed): only those are joined and signed.
digests maku-broker
BUILDS=failure run maku-broker maku-bridge >/dev/null || fail "one image built, the other's build failed: want success"
grep -q "imagetools create -t ghcr.io/smana/maku-broker:v1.0.1-pr1.abcdef12 ghcr.io/smana/maku-broker@$d1 ghcr.io/smana/maku-broker@$d2" "$tmp/log" || fail "maku-broker not joined"
grep -q "cosign sign --yes ghcr.io/smana/maku-broker@sha256:0*9$" "$tmp/log" || fail "maku-broker's index not signed by digest"
! grep -q maku-bridge "$tmp/log" || fail "maku-bridge was not built, yet touched"

# ghcr's read-after-write "not found" (PR #540): retried, then signed.
echo 2 >"$tmp/notfound"
run maku-broker >/dev/null 2>&1 || fail "inspect not found twice: want a retry and success"
[[ $(grep -c "imagetools inspect" "$tmp/log") == 3 ]] || fail "want three inspects"
grep -q "cosign sign" "$tmp/log" || fail "not signed after the retry"

# A half-pushed image (one platform digest) fails the run, after the images behind it are published.
mkdir -p "$tmp/d/digest-maku-slack-0" && echo "$d1" >"$tmp/d/digest-maku-slack-0/linux-0"
if run maku-slack maku-broker >/dev/null 2>&1; then fail "one platform digest: want a failure"; fi
grep -q "cosign sign --yes ghcr.io/smana/maku-broker@" "$tmp/log" || fail "maku-broker, after the half-pushed maku-slack, not published"
! grep -q maku-slack "$tmp/log" || fail "the half-pushed maku-slack was joined"
echo "==> prerelease-publish_test: ok"
