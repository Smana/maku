# maku-opencode

Pinned, non-root OpenCode 2.4.1 runner image (`ghcr.io/smana/maku-opencode`) for opt-in `AgentRun.spec.harness: opencode`.

## Pinning

- Base image: `ghcr.io/anomalyco/opencode:2.4.1@sha256:10e340e3e86d7b23e95cd9960f5a934beae8a87593d7398b5629b6112ed1fc63` (Alpine 3.24)
- Shared runner: `agent_run.py`, `harness_driver.py`, `agent_checks.py`, `agent_push.py`, `test_contract.py`, `git_credential_agent.py`, `loop_policy.py`, `pr_footer.py`, `coordination_integration.py` copied from `images/maku-harness/`
- Python dependencies: hash-locked musllinux amd64+arm64 wheels in `requirements.txt` (PyYAML and OTel SDK/exporter only; no OpenHands packages)
- Userland: the harness userland contract (`images/maku-harness/tests/userland-contract.sh`), shared with the OpenHands image: bash, GNU tools, make, xz, curl, jq, system PyYAML, and `gcompat` + `libstdc++` for glibc-linked mise tools ([what the image carries](https://maku.ogenki.io/docs/concepts/opencode-harness/#what-the-runner-image-carries))
- Non-root UID 10001, no setuid/setgid bits, `update: "disable"`, `OPENCODE_DISABLE_CLAUDE_CODE=1`

## Build and test

```bash
docker build --target test -f images/maku-opencode/Dockerfile .
docker build -f images/maku-opencode/Dockerfile .
python3 images/maku-opencode/tests/contract-matrix.py
images/maku-opencode/tests/runsc-contract.sh
```
