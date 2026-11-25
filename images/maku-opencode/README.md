# maku-opencode

Pinned, non-root OpenCode 2.3.0 runner image (`ghcr.io/smana/maku-opencode`) for opt-in `AgentRun.spec.harness: opencode`.

## Pinning

- Base image: `ghcr.io/anomalyco/opencode:2.3.0@sha256:998e3e184c650e603d8bacb5cbeaeb090837078262c25521a7136b0375d2b652` (Alpine 3.24)
- Shared runner: `agent_run.py`, `harness_driver.py`, `agent_checks.py`, `agent_push.py`, `test_contract.py`, `git_credential_agent.py`, `loop_policy.py`, `pr_footer.py`, `coordination_integration.py` copied from `images/maku-harness/`
- Python dependencies: hash-locked musllinux amd64+arm64 wheels in `requirements.txt` (PyYAML and OTel SDK/exporter only; no OpenHands packages)
- Non-root UID 10001, no setuid/setgid bits, `update: "disable"`, `OPENCODE_DISABLE_CLAUDE_CODE=1`

## Build and test

```bash
docker build --target test -f images/maku-opencode/Dockerfile .
docker build -f images/maku-opencode/Dockerfile .
python3 images/maku-opencode/tests/contract-matrix.py
images/maku-opencode/tests/runsc-contract.sh
```
