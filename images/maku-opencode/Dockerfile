# syntax=docker/dockerfile:1
# Maku OpenCode 2.4.1 runner image (OpenCode harness design §2 H15, §4).
# Built with repository-root context: docker build -f images/maku-opencode/Dockerfile .
FROM ghcr.io/anomalyco/opencode:2.4.1@sha256:10e340e3e86d7b23e95cd9960f5a934beae8a87593d7398b5629b6112ed1fc63 AS harness

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
# The harness userland contract (images/maku-harness/tests/userland-contract.sh), which the OpenHands
# image's Debian base meets: busybox has no bash, and its find, grep and sed lack GNU flags that
# repositories' checks use. gcompat and libstdc++ run the glibc-linked tools mise installs for
# agent-checks on musl (hugo extended). py3-yaml: the system python3 the repositories' checks run
# imports yaml, as the OpenHands image's does.
RUN set -eux; \
    apk add --no-cache python3 py3-pip git rsync ripgrep ca-certificates tini \
      bash coreutils findutils grep sed diffutils make tar gzip xz curl jq gcompat libstdc++ py3-yaml; \
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
# The venv lives where shared agent_run.py's shebang names it, /agent-server/.venv; /opt/agent/.venv
# links to the whole directory. A symlink to the interpreter alone is not a venv: Python finds
# pyvenv.cfg next to the path it was started by, so it ran with the system prefix and none of these
# packages (no repository checks, no tracing).
COPY images/maku-opencode/requirements.txt /tmp/requirements.txt
RUN python3 -m venv /agent-server/.venv \
 && /agent-server/.venv/bin/pip install --no-cache-dir --require-hashes --only-binary=:all: -r /tmp/requirements.txt \
 && rm /tmp/requirements.txt \
 && ln -s /agent-server/.venv /opt/agent/.venv

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
    images/maku-harness/browser_tools.py \
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
# No boot fetches the run's egress refuses: OpenCode's model catalog (models.opencode.ai, also set by
# the driver) and LiteLLM's price map, should any Python here load it.
ENV OPENCODE_DISABLE_MODELS_FETCH=true
ENV LITELLM_LOCAL_MODEL_COST_MAP=True
ENV GIT_TERMINAL_PROMPT=0
USER 10001
ENTRYPOINT ["tini", "--", "/usr/local/bin/agent-run"]

FROM harness AS test
COPY --chown=10001:10001 images/maku-opencode/tests /opt/agent/tests
# The harness userland contract, shared with the OpenHands image's test stage.
COPY --chown=10001:10001 images/maku-harness/tests/userland-contract.sh /opt/agent/tests/
RUN sh /opt/agent/tests/userland-contract.sh
RUN rg --version \
 && /usr/local/lib/gh-real --version \
 && /opt/agent/bin/mise --version \
 && opencode --version \
 && [ "$(command -v git)" = /usr/local/bin/git ] && [ -x /usr/bin/git ] \
 && cd /opt/agent && /opt/agent/.venv/bin/python -m unittest discover -s tests -v

FROM harness
