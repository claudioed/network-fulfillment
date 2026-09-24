# syntax=docker/dockerfile:1.7

# --- build stage ---
# Go 1.27 (this repo's go.mod), unlike most of the fleet's 1.26 — digest
# resolved from the registry, not copied from a sibling repo whose tag
# points at a different minor.
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src

# Cache go.mod/go.sum download separately from source so editing source
# code doesn't bust the module-download layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# BuildKit cache mounts for the module and build caches speed up repeat
# builds in CI without baking the cache into the image layers.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/netfulfil ./cmd/netfulfil

# --- runtime stage ---
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
# apk upgrade picks up any CVE fixes published to the 3.24 branch since the
# base image was last rebuilt (e.g. openssl point releases); ca-certificates
# is still pinned explicitly for a reproducible, auditable base layer.
#
# ca-certificates is not optional here the way it is for a purely in-cluster
# service: this context's whole job is calling an external network over
# TLS (NETWORK_MODE=live), and without a trust store that fails at runtime,
# not at build time.
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates=20260611-r0 && \
    addgroup -g 1000 -S app && adduser -u 1000 -S app -G app
WORKDIR /app
COPY --from=build --chown=app:app /out/netfulfil ./netfulfil
# Migrations ship IN the image and run at startup, matching every
# database-backed sibling. They land at /app/migrations, which is
# cmd/netfulfil's default MIGRATIONS_PATH.
COPY --from=build --chown=app:app /src/migrations ./migrations
USER 1000

EXPOSE 8080
ENTRYPOINT ["./netfulfil"]
