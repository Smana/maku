#!/usr/bin/env bash
# Measure OpenCode image size, cold startup readiness distribution, and run the offline recovery +
# contract matrix under gVisor (runsc) or a configured Docker runtime.
# Set DOCKER_RUNTIME='' on a host without gVisor to run against ordinary Docker (runc).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
IMAGE="${MAKU_OPENCODE_IMAGE:-maku-opencode:runsc-contract}"
RUNTIME="${DOCKER_RUNTIME-runsc}"
SAMPLES="${READINESS_SAMPLES:-5}"

docker build --target harness -t "${IMAGE}" -f "${ROOT}/images/maku-opencode/Dockerfile" "${ROOT}"

SIZE_BYTES="$(docker image inspect "${IMAGE}" --format '{{.Size}}')"
echo "==> Image ${IMAGE}: ${SIZE_BYTES} bytes ($((SIZE_BYTES / 1024 / 1024)) MiB)"

RUN_ARGS=(
  --rm
  --network=none
  --read-only
  --user 10001:10001
  --tmpfs /tmp:rw,nosuid,nodev,size=128m
)

if [[ -n "${RUNTIME}" ]]; then
  if ! docker info --format '{{.Runtimes}}' 2>/dev/null | grep -qw "${RUNTIME}"; then
    echo "error: docker runtime '${RUNTIME}' is not configured on this host (set DOCKER_RUNTIME='' for ordinary Docker)" >&2
    exit 1
  fi
  RUN_ARGS+=(--runtime="${RUNTIME}")
fi

echo "==> Measuring cold readiness across ${SAMPLES} samples (runtime=${RUNTIME:-runc})..."
docker run "${RUN_ARGS[@]}" \
  --entrypoint /opt/agent/.venv/bin/python \
  "${IMAGE}" -c "
import os, socket, statistics, sys, tempfile, time
sys.path.insert(0, '/opt/agent')
import opencode_driver
samples = []
for i in range(${SAMPLES}):
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    tmp = tempfile.mkdtemp(prefix=f'oc-ready-{i}-')
    d = opencode_driver.OpenCodeDriver({
        'OPENCODE_PORT': str(port),
        'OPENCODE_WORKSPACE_ROOT': os.path.join(tmp, '.opencode'),
        'OPENCODE_SESSION_ID_FILE': os.path.join(tmp, '.sid'),
        'OPENCODE_PASSWORD_FILE': os.path.join(tmp, '.pw'),
        'OPENCODE_TERMINAL_FILE': os.path.join(tmp, '.term'),
        'OPENCODE_WORKDIR': tmp,
    })
    t0 = time.monotonic()
    d.start()
    try:
        d.wait_ready(30)
        samples.append((time.monotonic() - t0) * 1000.0)
    finally:
        d.stop(5)
samples.sort()
p50 = statistics.median(samples)
p95 = samples[min(len(samples)-1, int(0.95 * len(samples)))]
print(f'readiness_ms: samples={[round(x, 1) for x in samples]} p50={p50:.1f}ms p95={p95:.1f}ms max={samples[-1]:.1f}ms')
"

echo "==> Running recovery suite (runtime=${RUNTIME:-runc})..."
MAKU_OPENCODE_IMAGE="${IMAGE}" DOCKER_RUNTIME="${RUNTIME}" "${ROOT}/images/maku-opencode/tests/recovery-runsc.sh"

echo "==> Running full offline parity matrix (runtime=${RUNTIME:-runc})..."
MAKU_OPENCODE_IMAGE="${IMAGE}" DOCKER_RUNTIME="${RUNTIME}" python3 "${ROOT}/images/maku-opencode/tests/contract-matrix.py"
