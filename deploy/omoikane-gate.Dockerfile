# syntax=docker/dockerfile:1.7
#
# omoikane-gate container image (issue #104 slice G3).
#
# SEPARATE Dockerfile from the repo-root one on purpose. The root
# Dockerfile builds kb-server, which needs cgo + sqlite (the
# sqlite_fts5 build tag). The gate binary (cmd/omoikane-gate) has NO
# sqlite dependency — `go list -deps ./cmd/omoikane-gate` pulls in no
# sqlite driver — so it builds with CGO_ENABLED=0 and runs from a
# scratch-thin base with no sqlite runtime package. Do not fold it into
# the root image; that would drag cgo/sqlite into a binary that needs
# neither.
#
# The gate listens on no port. It is an outbound client to (a) the
# opencrab core's Unix socket and (b) the omoikane HTTP API. It needs
# read/write access to the shared socket volume and network reach to
# kb-server. See docs/gateway-runbook.md and
# deploy/omoikane-gate.compose.yml.
#
# Build (from repo root):
#   docker build -f deploy/omoikane-gate.Dockerfile -t omoikane-gate .

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
# CGO_ENABLED=0: the gate has no sqlite/cgo dependency, so a static
# build is correct here (unlike kb-server). No sqlite_fts5 tag needed.
# GIT_SHA is passed by the deploy pipeline so the running binary can
# report which commit it was built from; defaults to "dev".
ARG GIT_SHA=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build \
        -ldflags="-s -w -X github.com/zenryokukikai/omoikane/internal/version.Build=${GIT_SHA}" \
        -trimpath -o /out/omoikane-gate ./cmd/omoikane-gate

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S gate && adduser -S gate -G gate
COPY --from=build /out/omoikane-gate /usr/local/bin/omoikane-gate
# Runs unprivileged. The gate creates no files; it only dials the core
# socket (owned by the core) and talks HTTP. The socket volume must be
# group/other-accessible to this user, or the core and gate must share a
# uid/gid — coordinate at compose-confirmation time.
USER gate
ENTRYPOINT ["/usr/local/bin/omoikane-gate"]
