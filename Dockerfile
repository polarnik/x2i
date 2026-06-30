# syntax=docker/dockerfile:1

# ---- build stage ----
# Build on the native platform of the runner and cross-compile with Go.
# This avoids running the whole Go toolchain under QEMU emulation for
# foreign target architectures (much faster multi-arch builds).
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS build

WORKDIR /src

# Cache dependencies first
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the rest of the source and build a static binary
COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o /out/x2i x2i.go

# ---- runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 x2i

COPY --from=build /out/x2i /usr/local/bin/x2i

USER x2i

ENTRYPOINT ["x2i"]
CMD ["--help"]
