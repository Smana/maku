# Static binary on Chainguard's Wolfi git image (hardening H3, HR24): the push applier shells out to
# git to inspect and push a run's bundle, so the final stage carries git and nothing it does not
# need; nonroot, uid 65532. The factory's pod securityContext (restricted PSS) overrides the uid
# anyway. The image job checks git --version is at least 2.48, the first git that fscks the objects
# fetched from a bundle (fetch.fsckObjects).
# The build stage runs on the runner's own arch and cross-compiles: compiling under QEMU
# arm64 emulation outran the prerelease job's 30 minute timeout.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X github.com/Smana/maku/internal/version.Version=${VERSION}" \
      -o /out/maku-factory ./cmd/maku-factory

FROM cgr.dev/chainguard/git:latest@sha256:f5f91faa60ed1b69deab0546d2154d2150f5f9163843793e72d45c51ff27153e
COPY --from=build /out/maku-factory /maku-factory
USER 65532:65532
ENTRYPOINT ["/maku-factory"]
