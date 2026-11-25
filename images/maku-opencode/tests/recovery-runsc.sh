#!/usr/bin/env bash
# Verify OpenCode PVC persistence, owned job cleanup, and graceful shutdown inside a container.
# Set DOCKER_RUNTIME=runsc on a gVisor-enabled host to exercise the exact production runtime.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
IMAGE="${MAKU_OPENCODE_IMAGE:-maku-opencode:recovery-test}"
RUNTIME="${DOCKER_RUNTIME-runsc}"

if [[ "${SKIP_BUILD:-}" != "1" ]]; then
  docker build --load --target harness -t "${IMAGE}" -f "${ROOT}/images/maku-opencode/Dockerfile" "${ROOT}"
fi

RUN_ARGS=(
  --rm
  --network=none
  --read-only
  --user 10001:10001
  --tmpfs /tmp:rw,nosuid,nodev,size=128m
  -v "${ROOT}/images/maku-opencode/tests:/opt/agent/tests:ro"
)

if [[ -n "${RUNTIME}" ]]; then
  if ! docker info --format '{{.Runtimes}}' 2>/dev/null | grep -qw "${RUNTIME}"; then
    echo "error: docker runtime '${RUNTIME}' is not configured on this host (set DOCKER_RUNTIME='' for default runc)" >&2
    exit 1
  fi
  RUN_ARGS+=(--runtime="${RUNTIME}")
fi

exec docker run "${RUN_ARGS[@]}" \
  -w /opt/agent/tests \
  --entrypoint /opt/agent/.venv/bin/python \
  "${IMAGE}" \
  -m unittest -v test_recovery test_control test_tracing
