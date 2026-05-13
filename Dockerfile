# syntax=docker/dockerfile:1.7
#
# omoikane container image.
#
# Multi-stage: build with the official Go alpine image (cgo + sqlite
# dev headers needed for the sqlite_fts5 build tag), then drop into a
# small alpine runtime. Final image ~25-30MB.
#
# Configuration is via env vars (see internal/config/config.go for the
# full list). The /data volume is the only stateful surface — bind
# mount or named-volume it in docker-compose.
#
# Permissions model: an entrypoint script runs as root just long
# enough to chown /data (so a bind-mounted host directory becomes
# writable regardless of host UID), then drops privileges to `kb`
# (UID 100) via su-exec. This avoids the "readonly database" failure
# that happens when the host bind-mount is owned by a different UID
# than the in-container user.
#
# Build:
#   docker build -t omoikane .
# Run (example):
#   docker run --rm -p 8080:8080 -v $PWD/data:/data \
#     -e KB_OAUTH_GOOGLE_CLIENT_ID=... \
#     -e KB_OAUTH_GOOGLE_CLIENT_SECRET=... \
#     -e KB_OAUTH_REDIRECT_BASE=https://kb.example.com \
#     -e KB_AUTH_ALLOW_EMAILS=you@example.com \
#     omoikane

FROM golang:1.26-alpine AS build
RUN apk add --no-cache build-base sqlite-dev
WORKDIR /src
# Cache module downloads in a separate layer so source-only changes
# don't bust the dep cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
# sqlite_fts5 tag is mandatory — without it, FTS index creation in
# migration 002 errors at startup ("no such module: fts5").
#
# BuildKit cache mounts persist Go's compiled-package cache + module
# cache across docker builds. Without them every `docker build`
# recompiles the dependency tree from scratch (~70s); with them the
# incremental build after a source-only change is ~5-10s. The cache
# survives until you `docker builder prune`.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 go build -tags sqlite_fts5 -ldflags='-s -w' \
        -trimpath -o /out/kb-server ./cmd/kb-server

FROM alpine:3.20
RUN apk add --no-cache sqlite ca-certificates tzdata wget su-exec && \
    addgroup -S kb && adduser -S kb -G kb && \
    mkdir -p /data && chown kb:kb /data
COPY --from=build /out/kb-server /usr/local/bin/kb-server
# Entrypoint chowns /data on startup (handles host bind-mount UID
# mismatch) then drops to the kb user.
COPY <<'EOF' /usr/local/bin/entrypoint.sh
#!/bin/sh
# Run as root only long enough to fix /data ownership for whatever
# host UID owns the bind-mounted volume. Then drop to kb (UID 100)
# via su-exec — same effect as USER kb in the Dockerfile, but
# applied AFTER the chown.
set -e
if [ "$(id -u)" = "0" ]; then
    chown -R kb:kb /data 2>/dev/null || true
    exec su-exec kb:kb /usr/local/bin/kb-server "$@"
else
    # Already non-root (e.g. user ran with --user) — just exec.
    exec /usr/local/bin/kb-server "$@"
fi
EOF
RUN chmod +x /usr/local/bin/entrypoint.sh
# Don't set USER — we need root briefly for the chown. The entrypoint
# drops to kb itself.
VOLUME ["/data"]
ENV KB_DB_PATH=/data/kb.db
ENV KB_HTTP_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
