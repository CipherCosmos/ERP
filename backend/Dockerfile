# Multi-stage build for GBOS backend

# Stage 1: Builder
FROM golang:1.21-alpine AS builder

RUN apk add --no-cache gcc sqlite-dev musl-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Enable CGO for sqlite3
ENV CGO_ENABLED=1 GOOS=linux

RUN go build -o server ./cmd/server

# Stage 2: Runtime
FROM alpine:latest

RUN apk add --no-cache sqlite-libs ca-certificates

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
