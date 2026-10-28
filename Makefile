.PHONY: backend backend-watch frontend frontend-https smoke verify-security migrate-down-all backup restore-drill \
        db-up db-down db-reset db-psql test test-unit migrate-up migrate-down migrate-status \
        ops-check caddy-check restore install-timers logs ps health

# Local Postgres for development and tests. Port 5434 keeps clear of the other
# Postgres containers already running on this machine (5432 and 5433).
DEV_DB_URL ?= postgres://gbos:gbos_dev_password@localhost:5434/gbos_dev?sslmode=disable
TEST_DB_URL ?= postgres://gbos:gbos_dev_password@localhost:5434/gbos_test?sslmode=disable
MIGRATIONS_DIR := backend/migrations/postgres

# --- application ------------------------------------------------------------

# API. Migrates the dev database on start and seeds the owner account on first
# run, printing its generated password once.
backend:
	cd backend && go run ./cmd/server

# Same server, rebuilt and restarted on every save. Config in backend/.air.toml.
#
# air is installed as a standalone binary rather than a go.mod tool dependency
# on purpose: `go get -tool` pulls air's own dependency tree (Hugo among them)
# into this module's graph and upgrades production dependencies with it.
backend-watch: AIR := $(shell go env GOPATH)/bin/air
backend-watch:
	@test -x "$(AIR)" || { \
		echo "air is not installed. Install it with:"; \
		echo "  go install github.com/air-verse/air@v1.67.4"; \
		exit 1; \
	}
	cd backend && "$(AIR)"

frontend:
	cd frontend && npm start

# Serves the dev frontend over HTTPS with a self-signed certificate, which is
# what the camera barcode scanner needs when the page is opened from a phone or
# tablet on the LAN: browsers expose getUserMedia only in a secure context, and
# a plain http:// LAN address is not one. The device must be told to trust the
# certificate on first visit. Plain `make frontend` is fine on this machine,
# where localhost already counts as secure.
frontend-https:
	cd frontend && HTTPS=true HOST=0.0.0.0 npm start

# --- database ---------------------------------------------------------------

db-up:
	docker compose -f docker-compose.dev.yml up -d
	@echo "waiting for postgres..."
	@until docker compose -f docker-compose.dev.yml exec -T postgres pg_isready -U gbos -d gbos_dev >/dev/null 2>&1; do sleep 1; done
	@docker compose -f docker-compose.dev.yml exec -T postgres \
		psql -U gbos -d gbos_dev -c "SELECT 1" >/dev/null
	@docker compose -f docker-compose.dev.yml exec -T postgres \
		sh -c "psql -U gbos -d postgres -tc \"SELECT 1 FROM pg_database WHERE datname='gbos_test'\" | grep -q 1 || createdb -U gbos gbos_test"
	@echo "postgres ready on localhost:5434 (databases: gbos_dev, gbos_test)"

db-down:
	docker compose -f docker-compose.dev.yml down

# Destroys all local database data.
db-reset:
	docker compose -f docker-compose.dev.yml down -v
	$(MAKE) db-up

db-psql:
	docker compose -f docker-compose.dev.yml exec postgres psql -U gbos -d gbos_dev

migrate-up:
	cd backend && go run ./cmd/migrate -database "$(DEV_DB_URL)" up

# Reverts one migration. Use `migrate-down-all` to revert everything.
migrate-down:
	cd backend && go run ./cmd/migrate -database "$(DEV_DB_URL)" down

migrate-down-all:
	cd backend && go run ./cmd/migrate -database "$(DEV_DB_URL)" down-all -yes

migrate-status:
	cd backend && go run ./cmd/migrate -database "$(DEV_DB_URL)" version

# --- backups ----------------------------------------------------------------

# Backs up the production database. Set BACKUP_REMOTE to an rclone target to
# copy it off the machine — a backup on the same disk as the database does not
# survive the failure it exists for.
backup:
	bash scripts/backup.sh

# Restores the newest backup into a scratch database, checks it, throws it
# away. Never touches the live database. Run it monthly: a backup you have
# never restored is not a backup.
restore-drill:
	bash scripts/restore-drill.sh

# DESTRUCTIVE. Replaces the live database with a backup. Prints what it would
# do and stops; pass ARGS="--yes" to go through with it.
#   make restore                        # show the plan
#   make restore ARGS="--yes"           # newest local backup
#   make restore ARGS="--yes --from-remote"
restore:
	bash scripts/restore.sh $(ARGS)

# Installs the nightly backup and monthly restore drill as systemd timers.
install-timers:
	sudo bash scripts/install-timers.sh

# --- operations -------------------------------------------------------------

# Validates every deployment file without starting anything. This is what CI
# runs; run it before pushing a change to compose, the Caddyfile or a script.
ops-check:
	@printf '%s\n' \
	  'JWT_SECRET=local-check-placeholder-not-a-real-secret-0123' \
	  'POSTGRES_PASSWORD=local-check-placeholder' \
	  'GBOS_DOMAIN=shop.example.com' \
	  'GBOS_TLS_EMAIL=ops@example.com' \
	  'CORS_ORIGIN=https://shop.example.com' > .ops-check.env
	docker compose --env-file .ops-check.env config >/dev/null
	docker compose -f docker-compose.dev.yml config >/dev/null
	@rm -f .ops-check.env
	@command -v shellcheck >/dev/null 2>&1 \
	  && shellcheck -x scripts/*.sh \
	  || echo "shellcheck not installed - skipped (CI still runs it)"
	@echo "ops files parse"

# Caddy validates its own config; this asks it to, without serving anything.
caddy-check:
	docker run --rm \
	  -e GBOS_SITE_ADDRESS=shop.example.com -e GBOS_TLS_EMAIL=ops@example.com \
	  -v "$(PWD)/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" \
	  caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile

ps:
	docker compose ps

logs:
	docker compose logs -f --tail 100

# Asks the running application whether it can reach the database.
health:
	@docker compose exec -T backend \
	  wget -q -O - http://127.0.0.1:8080/api/v1/health && echo \
	  || echo "backend is not answering - see docs/RUNBOOK.md"

# --- tests ------------------------------------------------------------------

# Full suite, including tests that need a real Postgres.
# -p 2 caps how many package binaries run at once: each opens its own pool
# against the one server, and the default exhausts its max_connections.
test: db-up
	cd backend && TEST_DATABASE_URL="$(TEST_DB_URL)" go test ./... -race -p 2

# Only the tests that need no database.
test-unit:
	cd backend && go test ./internal/domain/... ./internal/rbac/... -race

# Starts its own server on an ephemeral port against a throwaway database, and
# drops it afterwards. Needs the dev Postgres: make db-up.
smoke: db-up
	bash backend/scripts/smoke-test.sh


# Security regressions from the hardening pass. Starts its own server on an
# ephemeral port against a throwaway database.
verify-security: db-up
	bash backend/scripts/verify-security.sh
