# syntax=docker/dockerfile:1
# maku-browser (v5 Q16, VS2/VS6): Playwright MCP and Chromium's headless shell behind the Go gate,
# loopback only. The run-controller adds it to implementer and tester pods as the `browser` native
# sidecar. Built with repository-root context: docker build -f images/maku-browser/Dockerfile .
# Contract: images/maku-browser/tests/runsc-contract.sh (CI's browser-contract job, under gVisor).

# The gate, cross-compiled on the build platform with the Go images' pinned toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS gate
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X github.com/Smana/maku/internal/version.Version=${VERSION}" \
      -o /out/maku-browser-gate ./cmd/maku-browser-gate

FROM node:24-bookworm-slim@sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530
# Playwright never downloads at runtime; the one browser it may launch is baked in below.
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/browser/ms-playwright PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 \
    HOME=/home/browser XDG_CONFIG_HOME=/tmp/xdg XDG_CACHE_HOME=/home/browser/.cache NODE_ENV=production
WORKDIR /opt/browser
COPY images/maku-browser/package.json images/maku-browser/package-lock.json ./
# Chromium's headless shell with only the libraries Playwright lists for it on Debian 12
# (`playwright install-deps --dry-run chromium`, minus the X server and the CJK/Thai font set its
# --with-deps adds): the shell's ldd must resolve, or the build fails. Then the package managers
# leave the image (npm, corepack, yarn), every setuid/setgid bit is stripped, and uid 10001 owns
# only its home (a tmpfs in the pod).
RUN set -eux; \
    npm ci --omit=dev --ignore-scripts; \
    apt-get update; \
    apt-get install -y --no-install-recommends tini \
      libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 libcairo2 libcups2 libdbus-1-3 libdrm2 \
      libgbm1 libglib2.0-0 libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 \
      libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 libfontconfig1 libfreetype6 \
      fonts-liberation fonts-dejavu-core fonts-noto-color-emoji; \
    PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=0 node node_modules/playwright/cli.js install --no-progress --only-shell chromium; \
    shell="$(find "$PLAYWRIGHT_BROWSERS_PATH" -type f -name chrome-headless-shell | head -1)"; \
    test -x "$shell"; \
    if ldd "$shell" | grep 'not found'; then exit 1; fi; \
    rm -rf /var/lib/apt/lists/* /var/cache/apt/* /root/.npm /root/.cache /tmp/* \
      /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack /opt/yarn-* \
      /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack /usr/local/bin/yarn /usr/local/bin/yarnpkg; \
    groupadd -g 10001 browser; \
    useradd -u 10001 -g 10001 -d /home/browser -M -s /usr/sbin/nologin browser; \
    mkdir -p /home/browser/.cache; \
    chown -R 10001:10001 /home/browser; \
    find / -xdev -type f -perm /6000 -exec chmod ug-s {} +
COPY --from=gate /out/maku-browser-gate /usr/local/bin/maku-browser-gate
COPY images/maku-browser/config.json /etc/maku-browser/config.json
COPY --chmod=0755 images/maku-browser/entrypoint.sh /opt/browser/entrypoint.sh
USER 10001:10001
ENTRYPOINT ["/usr/bin/tini", "--", "/opt/browser/entrypoint.sh"]
