# syntax=docker/dockerfile:1
#
# Build from the repository root:
#   docker build -f deployment/docker/gateway.Dockerfile -t centaur-gateway:local .
#
# Keep the build stage in sync with deployment/docker/connector.Dockerfile: the
# two files differ only in the compiled package, the binary name, EXPOSE and
# ENTRYPOINT.

# --- build -------------------------------------------------------------------
# Pinned to the version from go.mod: a newer minor would make the toolchain
# download itself inside the build container.
FROM --platform=$BUILDPLATFORM golang:1.27.2 AS build

WORKDIR /src

# Dependencies first: the layer is reused until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# BuildKit fills these from --platform: Go cross-compiles without QEMU.
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
      -o /out/centaur-gateway ./cmd/gateway

# --- runtime -----------------------------------------------------------------
FROM scratch

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="centaur-gateway" \
      org.opencontainers.image.source="https://github.com/shpak86/centaur" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

# The binary is static (CGO_ENABLED=0) and reads the roots from the standard
# path, which matters if the target or the pool ever moves to HTTPS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/centaur-gateway /usr/local/bin/centaur-gateway

# proxy.address, pool.address, pool.apiAddress, metrics.address
EXPOSE 18180 18181 18182 18183

# The default config path is already /etc/centaur/gateway.json: mount the file
# there, or pass -config /path/inside/container.json.
ENTRYPOINT ["/usr/local/bin/centaur-gateway"]
