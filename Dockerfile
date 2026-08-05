# syntax=docker/dockerfile:1.7
# crons: read-only HTML dashboard for OpenClaw cron_jobs.
#
# Build:  docker build -t crons:dev .
# Run:    docker run --rm -p 8080:8080 \
#             -v /srv/openclaw/state/state:/data/openclaw-state:ro \
#             crons:dev

# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Cache module downloads so source-only edits don't re-fetch the world.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Pure-Go build: CGO_ENABLED=0 keeps the image minimal and lets us use a
# static distroless runtime. -trimpath strips the build host's paths;
# -s -w drops the symbol table and DWARF info.
RUN CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w" \
        -o /out/crons ./cmd/crons

# ---- runtime stage ----
# distroless/static has no shell, no package manager. The binary is the
# only thing on the filesystem.
#
# Runtime identity: the OpenClaw state directory this app reads
# (host_source in deploy.json) is owned by UID/GID 1000:1000 with mode
# 0600 on the platform. Running as the distroless default `nonroot`
# (UID 65532) would deny every read. The image therefore declares an
# explicit numeric UID so it can read the mounted database without
# making the mount writable. Docker accepts a numeric USER even when
# the uid is not present in /etc/passwd inside the image.
#
# This is a platform-wide convention for apps that read OpenClaw
# state; other apps that need the same access use the same UID.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/crons /usr/local/bin/crons

EXPOSE 8080
USER 1000:1000

ENTRYPOINT ["/usr/local/bin/crons"]
