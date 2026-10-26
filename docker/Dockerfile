# syntax=docker/dockerfile:1

# Frontend stage: build the React SPA. Vite (web/vite.config.ts) writes into
# ../internal/webui/dist, which is exactly where the Go embed expects it.
FROM node:22-alpine AS web-builder

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Backend stage: compile a static binary. Migrations are embedded via go:embed,
# and the compiled frontend is copied in below so it is embedded too — the image
# ships as a single self-contained server.
FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Dependencies first so source edits do not invalidate the module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

# Overlay the built assets so //go:embed all:dist picks up the real UI instead of
# the committed placeholder.
COPY --from=web-builder /src/internal/webui/dist ./internal/webui/dist

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/drivethrough ./cmd/server

# Runtime stage: distroless-style, non-root, no shell.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S drivethrough \
 && adduser -S -G drivethrough -H -s /sbin/nologin drivethrough

WORKDIR /app

COPY --from=builder /out/drivethrough ./drivethrough

USER drivethrough

EXPOSE 8080

ENV SERVER_PORT=8080 \
    SERVER_ENV=production \
    TZ=Asia/Kolkata

# The binary answers 200 on /ready only when Postgres and Redis both respond,
# so an orchestrator can gate traffic on it.
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/ready || exit 1

ENTRYPOINT ["/app/drivethrough"]
