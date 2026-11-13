# Stdlib-only stub for spec-intake k3d; built with the repo root as context.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY hack/e2e-stub ./hack/e2e-stub
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/e2e-stub ./hack/e2e-stub

FROM --platform=$TARGETPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf
RUN apk add --no-cache git git-daemon ca-certificates
COPY --from=build /out/e2e-stub /usr/local/bin/e2e-stub
RUN git init --bare /srv/git/owner/repo.git \
  && work="$(mktemp -d)" \
  && git -C "$work" init -q \
  && mkdir -p "$work/.github" \
  && printf 'factory: demo\n' > "$work/.github/agent-factory.yaml" \
  && printf '# e2e-stub repository\n' > "$work/README.md" \
  && git -C "$work" add . \
  && git -C "$work" -c user.name=e2e -c user.email=e2e@example.invalid commit -q -m init \
  && git -C "$work" push -q /srv/git/owner/repo.git HEAD:main \
  && git --git-dir=/srv/git/owner/repo.git symbolic-ref HEAD refs/heads/main \
  && rm -rf "$work" \
  && chown -R 65532:65532 /srv/git
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/e2e-stub"]
