# Multi-stage build for GBOS backend

# Stage 1: Builder
FROM golang:1.22-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends gcc libsqlite3-dev libc6-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Enable CGO for sqlite3
ENV CGO_ENABLED=1 GOOS=linux

RUN go build -o server ./cmd/server

# Stage 2: Runtime
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends libsqlite3-0 ca-certificates curl wget && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/server .
COPY --from=builder /app/migrations ./migrations

# Create data directory for SQLite database
RUN mkdir -p /app/data

EXPOSE 8080

ENV DATABASE_PATH=/app/data/gbos.db
ENV GIN_MODE=release
ENV JWT_SECRET=default-change-in-production

CMD ["./server"]
