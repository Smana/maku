# Static binary on distroless: no shell, no package manager, uid 65532.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X github.com/Smana/maku/internal/version.Version=${VERSION}" \
      -o /out/maku-run-controller ./cmd/maku-run-controller

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/maku-run-controller /maku-run-controller
USER nonroot:nonroot
ENTRYPOINT ["/maku-run-controller"]
