# GBOS backend image.
#
# Build from the backend/ directory:
#   docker build -t gbos-backend:local backend/
# or let compose do it:
#   docker compose build backend

# Keep this in step with the `go` directive in go.mod. If it falls behind, the
# build still works — the toolchain downloads the newer Go itself — but it
# downloads it on every build, which is slow and needs network at build time.
ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# No cgo. Every dependency is pure Go — the server talks to Postgres through
# pgx — so a static binary is possible, and it means the runtime image has no
# libc to keep patched and nothing to mismatch at start.
ENV CGO_ENABLED=0 GOOS=linux

# -trimpath keeps build paths out of the binary; -s -w drops the symbol table.
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# --- runtime ---------------------------------------------------------------

FROM alpine:3.20

# ca-certificates  outbound TLS.
# tzdata           without it TZ is ignored and everything is UTC, which moves
#                  a shop's late-evening sales into the next day's takings.
# wget is busybox's, already present, and is what the compose healthcheck runs.
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/server /app/server
COPY --from=builder /out/migrate /app/migrate

# Runs unprivileged. There is no writable state in the image — the schema is
# compiled into the binary and all data lives in Postgres — so nothing here
# needs to be writable at run time.
RUN addgroup -S gbos && adduser -S -G gbos gbos && chown -R gbos:gbos /app
USER gbos

EXPOSE 8080

ENV GIN_MODE=release
ENV TZ=Asia/Kolkata

# JWT_SECRET, DATABASE_URL and CORS_ORIGIN are deliberately not set here. In
# release mode the server refuses to start without them, so they must be
# supplied at run time and can never be baked into a published image.

CMD ["/app/server"]
