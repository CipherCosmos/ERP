.PHONY: backend frontend smoke verify-security clean-testdb migrate-down-all backup restore-drill \
        db-up db-down db-reset db-psql test test-unit migrate-up migrate-down migrate-status \
        ops-check caddy-check restore install-timers logs ps health

# Local Postgres for development and tests. Port 5434 keeps clear of the other
# Postgres containers already running on this machine (5432 and 5433).
DEV_DB_URL ?= postgres://gbos:gbos_dev_password@localhost:5434/gbos_dev?sslmode=disable
TEST_DB_URL ?= postgres://gbos:gbos_dev_password@localhost:5434/gbos_test?sslmode=disable
MIGRATIONS_DIR := backend/migrations/postgres

# --- application ------------------------------------------------------------

# API (creates ./backend/data/ if needed; seeds on first run)
backend:
	cd backend && mkdir -p data && go run ./cmd/server

frontend:
	cd frontend && npm start

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

# Requires backend on 8080 (or set GBOS_API, e.g. http://127.0.0.1:18081/api/v1)
smoke:
	bash scripts/smoke-test.sh

# Security regressions from the Phase 0 hardening. Starts its own server.
verify-security:
	bash backend/scripts/verify-security.sh

clean-testdb:
	rm -f backend/data/test-seed.db
