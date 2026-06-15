# weft-chaos OCI image. Two-stage : Go build → scratch base.
# ~ 12 MB image, no shell, no package manager — the binary IS
# the harness. Consumed by the openweft pull path :
#   weft microvm pull ghcr.io/openweft/weft-chaos:<tag>
#
# Multi-arch : buildx forwards TARGETOS / TARGETARCH to go build.
# Publish covers amd64 + arm64 + riscv64 + loong64.
#
# Build args :
#   - VERSION : git describe output, stamped via -ldflags
#   - COMMIT  : short sha
#   - DATE    : RFC-3339 UTC build timestamp

ARG GO_VERSION=1.26
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/weft-chaos ./cmd/weft-chaos

FROM scratch
COPY --from=build /out/weft-chaos /weft-chaos
COPY scenarios /scenarios
USER 65532:65532
ENTRYPOINT ["/weft-chaos"]
