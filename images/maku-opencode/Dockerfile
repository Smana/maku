# syntax=docker/dockerfile:1
# Maku OpenCode 2.3.0 runner image (OpenCode harness design §2 H15, §4).
# Built with repository-root context: docker build -f images/maku-opencode/Dockerfile .
FROM ghcr.io/anomalyco/opencode:2.3.0@sha256:998e3e184c650e603d8bacb5cbeaeb090837078262c25521a7136b0375d2b652 AS harness

# cli/cli release: move GH_VERSION and both sums together with images/maku-harness/Dockerfile.
ARG GH_VERSION=2.101.0
ARG GH_SHA256_AMD64=9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8
ARG GH_SHA256_ARM64=b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f

# jdx/mise musl release: move MISE_VERSION and both sums together with images/maku-harness/Dockerfile.
ARG MISE_VERSION=2026.10.3
ARG MISE_SHA256_AMD64=23c45c76567f8f1e4cfc40efff3f19e3352d3745e6c25e74891102765d1cdc3c
ARG MISE_SHA256_ARM64=0af3379b7a8060b047416576a746078daee3c9d2bf57fa8f6ba07d3cdf5f47aa
ARG TARGETARCH

USER root
COPY images/maku-harness/fetch_release.py /tmp/fetch_release.py
RUN set -eux; \
    apk add --no-cache python3 py3-pip git rsync ripgrep ca-certificates tini; \
    case "${TARGETARCH}" in \
      amd64) sum="${GH_SHA256_AMD64}" msum="${MISE_SHA256_AMD64}" march=x64 ;; \
      arm64) sum="${GH_SHA256_ARM64}" msum="${MISE_SHA256_ARM64}" march=arm64 ;; \
      *) exit 1 ;; \
    esac; \
    python3 /tmp/fetch_release.py cli/cli "v${GH_VERSION}" "gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" /tmp/gh.tgz; \
    echo "${sum}  /tmp/gh.tgz" | sha256sum -c -; \
    tar -xzf /tmp/gh.tgz -C /tmp; \
    install -m 0755 "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/lib/gh-real; \
    rm -rf /tmp/gh.tgz "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}"; \
    python3 /tmp/fetch_release.py jdx/mise "v${MISE_VERSION}" "mise-v${MISE_VERSION}-linux-${march}-musl.tar.gz" /tmp/mise.tgz; \
    echo "${msum}  /tmp/mise.tgz" | sha256sum -c -; \
    tar -xzf /tmp/mise.tgz -C /tmp; \
    install -D -m 0755 /tmp/mise/bin/mise /opt/agent/bin/mise; \
    rm -rf /tmp/mise.tgz /tmp/mise /tmp/fetch_release.py; \
    addgroup -g 10001 -S agent; \
    adduser -u 10001 -S -G agent -h /workspace/.opencode/home -s /bin/sh agent; \
    mkdir -p /workspace /opt/agent /etc/agent/git-hooks; \
    chown 10001:10001 /workspace; \
    find / -xdev -type f -perm /6000 -exec chmod ug-s {} +

# Locked OTel + PyYAML dependencies only (no OpenHands packages).
# Symlink /agent-server/.venv/bin/python so shared agent_run.py's shebang works unchanged.
COPY images/maku-opencode/requirements.txt /tmp/requirements.txt
RUN python3 -m venv /opt/agent/.venv \
 && /opt/agent/.venv/bin/pip install --no-cache-dir --require-hashes --only-binary=:all: -r /tmp/requirements.txt \
 && rm /tmp/requirements.txt \
 && mkdir -p /agent-server/.venv/bin \
 && ln -s /opt/agent/.venv/bin/python /agent-server/.venv/bin/python

# Shared runner modules and git/gh wrappers from images/maku-harness/.
COPY --chmod=0755 \
    images/maku-harness/agent_run.py \
    images/maku-harness/harness_types.py \
    images/maku-harness/harness_driver.py \
    images/maku-harness/openhands_driver.py \
    images/maku-harness/loop_guard.py \
    images/maku-harness/event_tracing.py \
    images/maku-harness/agent_push.py \
    images/maku-harness/agent_checks.py \
    images/maku-harness/test_contract.py \
    images/maku-harness/git_credential_agent.py \
    images/maku-harness/loop_policy.py \
    images/maku-harness/pr_footer.py \
    images/maku-harness/coordination_integration.py \
    /opt/agent/
COPY --chmod=0755 \
    images/maku-opencode/config.py \
    images/maku-opencode/role_config.py \
    images/maku-opencode/mcp_proxy.py \
    images/maku-opencode/search.py \
    images/maku-opencode/opencode_driver.py \
    /opt/agent/
COPY --chmod=0755 images/maku-harness/gh /usr/local/bin/gh
COPY --chmod=0755 images/maku-harness/git /usr/local/bin/git
COPY --chmod=0755 images/maku-harness/commit-msg /etc/agent/git-hooks/commit-msg
COPY --chmod=0755 images/maku-harness/pre-commit /etc/agent/git-hooks/pre-commit
COPY images/maku-harness/gitconfig /etc/gitconfig
RUN ln -s /opt/agent/agent_run.py /usr/local/bin/agent-run \
 && ln -s /opt/agent/agent_push.py /usr/local/bin/agent-push \
 && ln -s /opt/agent/agent_checks.py /usr/local/bin/agent-checks \
 && ln -s /opt/agent/test_contract.py /usr/local/bin/test-contract \
 && ln -s /opt/agent/git_credential_agent.py /usr/local/bin/git-credential-agent

ENV AGENT_HARNESS=opencode
ENV HOME=/workspace/.opencode/home
ENV OPENCODE_CONFIG_DIR=/workspace/.opencode/config
ENV OPENCODE_DISABLE_CLAUDE_CODE=1
ENV GIT_TERMINAL_PROMPT=0
USER 10001
ENTRYPOINT ["tini", "--", "/usr/local/bin/agent-run"]

FROM harness AS test
COPY --chown=10001:10001 images/maku-opencode/tests /opt/agent/tests
RUN rg --version \
 && /usr/local/lib/gh-real --version \
 && /opt/agent/bin/mise --version \
 && opencode --version \
 && [ "$(command -v git)" = /usr/local/bin/git ] && [ -x /usr/bin/git ] \
 && cd /opt/agent && /opt/agent/.venv/bin/python -m unittest discover -s tests -v

FROM harness
