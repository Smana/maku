#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# SC2015: `test && pass || fail` is the point (pass never fails). SC2329: cleanup runs from the trap.
# SC2054: the commas are inside --tmpfs options, not between array elements.
# shellcheck disable=SC2015,SC2329,SC2054
#
# The maku-browser image's contract under gVisor (plan Task 9): the sidecar as the run-controller
# runs it (read-only root, uid 10001, no capabilities, no network, /tmp and the cache on tmpfs,
# 2 GiB). Each check prints PASS or FAIL <what>; the exit code is the number of failures.
#   B1  the gate's /healthz answers 200 within 30 s (the kubelet's exec probe).
#   B2  tools/list serves the allow-list only: no browser_evaluate, browser_run_code*,
#       browser_file_upload or browser_drop.
#   B3  a dev server on 127.0.0.1:8080 inside the container (the harness's stand-in): navigate,
#       then browser_snapshot holds its heading, and no outputDir link reaches the client.
#   B4  browser_take_screenshot returns one image of 512 KiB or less, and the gate keeps it.
#   B5  (AC3) the gate refuses https://example.com, http://169.254.169.254/ and file:///etc/passwd;
#       a loopback page's fetch('http://93.184.216.34/') and <img src="http://example.com/..."> fail.
#       B5b repeats the page with Playwright's allowedOrigins removed: Chromium's proxy and resolver
#       flags alone fail both requests (allowedOrigins is hygiene, not the boundary).
#   B6  (AC5) after 10 screenshots and browser_close no process is a zombie, and SIGTERM stops the
#       container within 10 s.
#   B7  with MAX_SCREENSHOTS=10 the 11th screenshot is refused with screenshot_cap_reached.
#   B8  the image's size (unpacked; compressed with SIZE_COMPRESSED=1), the memory after B4 and the
#       cold start (healthz, first tools/list, first screenshot), also in $GITHUB_STEP_SUMMARY.
# Usage: MAKU_BROWSER_IMAGE=<image> SKIP_BUILD=1 runsc-contract.sh
#   DOCKER_RUNTIME (default runsc): runc for a local run on a host without gVisor.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$here/../../.." && pwd)"
IMAGE="${MAKU_BROWSER_IMAGE:-maku-browser:contract}"
RUNTIME="${DOCKER_RUNTIME:-runsc}"

if [[ ${SKIP_BUILD:-} != 1 ]]; then
  docker build -t "$IMAGE" -f "$ROOT/images/maku-browser/Dockerfile" "$ROOT"
fi
docker info --format '{{.Runtimes}}' | grep -qw "$RUNTIME" ||
  { echo "error: docker runtime '$RUNTIME' is not configured (DOCKER_RUNTIME=runc for a local run)" >&2; exit 2; }

fails=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; fails=$((fails + 1)); }
ms() { echo $(($(date +%s%N) / 1000000)); }

C="maku-browser-contract-$$"
work="$(mktemp -d)"
cleanup() {
  local rc=$?
  docker rm -f "$C" "$C-b5b" >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$rc"
}
trap cleanup EXIT

# The run-controller's shape (spec §4.1): restricted, read-only, no network, tmpfs scratch.
run_args=(--runtime="$RUNTIME" --read-only --user 10001:10001 --cap-drop ALL
  --security-opt no-new-privileges --network none --memory 2g
  --tmpfs /tmp:rw,nosuid,nodev,size=512m --tmpfs /home/browser/.cache:rw,nosuid,nodev,size=128m
  -e MAX_SCREENSHOTS=10 -e VISION=1)

# mcp NAME ARGS...: the MCP client (tests/mcp-client.js) in container NAME; one JSON line.
mcp() { local c=$1; shift; docker exec -i "$c" node - "$@" <"$here/mcp-client.js"; }
# texts: a tool result's text parts, joined.
texts() { jq -r '[.content[]? | select(.type == "text") | .text] | join("\n")'; }
# start NAME: run the image as NAME (extra docker args after it), then wait for /healthz.
start() {
  local c=$1; shift
  docker run -d --name "$c" "${run_args[@]}" "$@" "$IMAGE" >/dev/null
  for _ in $(seq 60); do
    docker exec "$c" /usr/local/bin/maku-browser-gate -probe http://127.0.0.1:9223/healthz >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}
# ready NAME: tools/list answers through the gate (Playwright MCP is up behind it).
ready() {
  for _ in $(seq 60); do
    docker exec "$1" rm -rf /tmp/mcp-client
    mcp "$1" list >"$work/list.json" 2>/dev/null && jq -e '.tools | length > 0' "$work/list.json" >/dev/null && return 0
    sleep 0.5
  done
  return 1
}
# devserver NAME: tests/site/ on 127.0.0.1:8080 inside NAME, served by node (the image has no python3).
devserver() {
  docker exec "$1" mkdir -p /tmp/site
  tar -C "$here/site" -cf - . | docker exec -i "$1" tar -xf - -C /tmp/site
  docker exec -d "$1" node -e "$(cat "$here/serve.js")" /tmp/site 8080
  for _ in $(seq 20); do
    docker exec "$1" node -e 'fetch("http://127.0.0.1:8080/").then(r => process.exit(r.ok ? 0 : 1), () => process.exit(1))' && return 0
    sleep 0.5
  done
  return 1
}
# egress NAME LABEL: the loopback page's two outbound requests fail.
egress() {
  local c=$1 label=$2 net snap line host
  mcp "$c" call browser_navigate '{"url":"http://127.0.0.1:8080/egress.html"}' >/dev/null || true
  mcp "$c" call browser_wait_for '{"text":"fetch-","time":10}' >/dev/null || true
  snap="$(mcp "$c" call browser_snapshot | texts)" || true
  net="$(mcp "$c" call browser_network_requests | texts)" || true
  grep -q 'fetch-failed' <<<"$snap" && ! grep -q 'fetch-ok' <<<"$snap" &&
    pass "$label: the page's fetch('http://93.184.216.34/') failed" ||
    fail "$label: the page's fetch to 93.184.216.34 did not fail: $(grep -o 'fetch-[a-z]*' <<<"$snap" | head -1)"
  for host in 93.184.216.34 example.com; do
    line="$(grep -F "http://$host/" <<<"$net" | head -1)" || true
    [[ -n $line ]] && grep -q 'FAILED' <<<"$line" && pass "$label: $host failed: ${line##*=> }" ||
      fail "$label: the request to $host did not fail: ${line:-not listed}"
  done
}

echo "==> $IMAGE under $RUNTIME"
t0="$(ms)"
if start "$C"; then
  t_health=$(($(ms) - t0))
  pass "B1 /healthz answers 200 (${t_health} ms)"
else
  fail "B1 /healthz did not answer within 30 s"
  docker logs "$C" 2>&1 | tail -40
  exit "$fails"
fi
ready "$C" || { fail "B1 tools/list did not answer through the gate"; docker logs "$C" 2>&1 | tail -40; exit "$fails"; }
t_list=$(($(ms) - t0))

names="$(jq -r '.tools[].name' "$work/list.json")"
bad="$(grep -E '^(browser_evaluate|browser_run_code.*|browser_file_upload|browser_drop)$' <<<"$names" || true)"
[[ -z $bad ]] && pass "B2 tools/list serves $(wc -l <<<"$names") allowed tools, none of the denied ones" ||
  fail "B2 tools/list serves denied tools: $(tr '\n' ' ' <<<"$bad")"
grep -qx browser_take_screenshot <<<"$names" && grep -qx browser_navigate <<<"$names" &&
  pass "B2 browser_navigate and browser_take_screenshot are listed" || fail "B2 a needed tool is missing: $(tr '\n' ' ' <<<"$names")"

devserver "$C" || fail "B3 the dev server on 127.0.0.1:8080 did not start"
nav="$(mcp "$C" call browser_navigate '{"url":"http://127.0.0.1:8080/"}' | texts)" || true
snap="$(mcp "$C" call browser_snapshot | texts)" || true
grep -q 'Contract dev server' <<<"$snap" && pass "B3 browser_snapshot holds the dev server's heading" ||
  fail "B3 browser_snapshot lacks the heading: $(head -c 400 <<<"$snap$nav")"
! grep -qE '\]\((\.\./)+tmp/|\]\(/tmp/' <<<"$nav$snap" && pass "B3 no outputDir link reaches the client" ||
  fail "B3 an outputDir link reached the client: $(grep -oE '\]\([^)]*tmp/[^)]*\)' <<<"$nav$snap" | head -1)"

mcp "$C" call browser_take_screenshot >"$work/shot.json" || true
t_shot=$(($(ms) - t0))
images="$(jq '[.content[]? | select(.type == "image")] | length' "$work/shot.json" 2>/dev/null)" || images=0
bytes="$(jq -r '[.content[]? | select(.type == "image") | .data][0] // ""' "$work/shot.json" 2>/dev/null | base64 -d 2>/dev/null | wc -c)" || true
[[ $images == 1 ]] && ((bytes > 0 && bytes <= 524288)) && pass "B4 one image of $bytes bytes (cap 524288)" ||
  fail "B4 want one image of at most 512 KiB, got $images image(s), $bytes bytes: $(head -c 300 "$work/shot.json")"
kept="$(docker exec "$C" node -e 'fetch("http://127.0.0.1:9223/shots?after=0").then(r => r.json()).then(j => console.log(j.shots.length), () => console.log(-1))')" || true
[[ $kept == 1 ]] && pass "B4 the gate keeps the screenshot for the room (/shots)" || fail "B4 /shots lists $kept screenshots, want 1"
# B8's memory: the sum of every process's RSS now and of their peaks (VmHWM), in MiB.
mem="$(docker exec "$C" sh -c 'cat /proc/[0-9]*/status 2>/dev/null' |
  awk '/^VmRSS:/ {r += $2} /^VmHWM:/ {h += $2} END {printf "%d %d", r / 1024, h / 1024}')" || mem="n/a n/a"
rss="${mem% *}" hwm="${mem#* }"
stats="$(docker stats --no-stream --format '{{.MemUsage}}' "$C" 2>/dev/null || echo n/a)"

for url in https://example.com http://169.254.169.254/ file:///etc/passwd; do
  out="$(mcp "$C" call browser_navigate "{\"url\":\"$url\"}")" || true
  jq -e '.isError == true' <<<"$out" >/dev/null && grep -q url_refused <<<"$(texts <<<"$out")" &&
    pass "B5 the gate refuses $url" || fail "B5 $url was not refused: $(head -c 300 <<<"$out")"
done
egress "$C" B5

ok=0
for _ in $(seq 2 10); do
  n="$(mcp "$C" call browser_take_screenshot | jq '[.content[]? | select(.type == "image")] | length')" || true
  [[ $n == 1 ]] && ok=$((ok + 1))
done
[[ $ok == 9 ]] && pass "B6 screenshots 2 to 10 each returned an image" || fail "B6 only $ok of screenshots 2 to 10 returned an image"
out="$(mcp "$C" call browser_take_screenshot)" || true
jq -e '.isError == true' <<<"$out" >/dev/null && grep -q screenshot_cap_reached <<<"$(texts <<<"$out")" &&
  pass "B7 the 11th screenshot is refused with screenshot_cap_reached" || fail "B7 the 11th screenshot was not refused: $(head -c 300 <<<"$out")"

mcp "$C" call browser_close >/dev/null || fail "B6 browser_close failed"
sleep 2
states="$(docker exec "$C" sh -c 'cat /proc/[0-9]*/stat 2>/dev/null' | sed 's/.*) //' | cut -d' ' -f1)" || true
zombies="$(grep -c Z <<<"$states" || true)"
[[ $zombies == 0 ]] && pass "B6 no zombie after browser_close ($(wc -l <<<"$states") processes)" || fail "B6 $zombies zombie process(es) after browser_close"
t1="$(ms)"
docker kill -s TERM "$C" >/dev/null
code="$(timeout 15 docker wait "$C" || echo timeout)"
stop_ms=$(($(ms) - t1))
[[ $code != timeout ]] && ((stop_ms <= 10000)) && pass "B6 SIGTERM stopped the container in ${stop_ms} ms (exit $code)" ||
  fail "B6 SIGTERM did not stop the container within 10 s (${stop_ms} ms, $code)"

# B5b: the same page with allowedOrigins removed, so only Chromium's flags stand between it and the network.
jq 'del(.network)' <(docker run --rm --entrypoint cat "$IMAGE" /etc/maku-browser/config.json) >"$work/config.json"
chmod 0644 "$work/config.json"
if start "$C-b5b" -v "$work/config.json:/etc/maku-browser/config.json:ro" && ready "$C-b5b" && devserver "$C-b5b"; then
  egress "$C-b5b" "B5b (no allowedOrigins)"
else
  fail "B5b the container without allowedOrigins did not start"
  docker logs "$C-b5b" 2>&1 | tail -20
fi

unpacked="$(docker image inspect "$IMAGE" --format '{{.Size}}')"
compressed=n/a
if [[ ${SIZE_COMPRESSED:-} == 1 ]]; then
  compressed="$(($(docker save "$IMAGE" | gzip -6 | wc -c) / 1048576)) MiB"
fi
b8="B8 image ${IMAGE} under ${RUNTIME}: unpacked $((unpacked / 1048576)) MiB, compressed (gzip) ${compressed}; memory after B4: RSS ${rss} MiB, peak (sum of VmHWM) ${hwm} MiB, docker stats ${stats}; cold start: healthz ${t_health} ms, tools/list ${t_list} ms, first screenshot ${t_shot} ms"
echo "$b8"
((unpacked <= 1200 * 1000 * 1000)) && pass "B8 the image is within the 1.2 GB review limit" || fail "B8 the image is over 1.2 GB unpacked"
[[ -z ${GITHUB_STEP_SUMMARY:-} ]] || printf '### maku-browser contract\n\n%s\n\nfailures: %d\n' "$b8" "$fails" >>"$GITHUB_STEP_SUMMARY"

echo "==> $fails failure(s)"
exit "$fails"
