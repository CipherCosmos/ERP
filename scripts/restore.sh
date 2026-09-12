#!/usr/bin/env bash
#
# Restores the LIVE GBOS database from a backup. Destructive.
#
# This is the one you run at 2am when the books are gone. Everything currently
# in the database is replaced by the contents of the dump. Anything billed
# since that dump was taken is lost — that is what a restore is.
#
#   bash scripts/restore.sh --yes                      # newest local backup
#   bash scripts/restore.sh --yes --from-remote        # newest offsite backup
#   bash scripts/restore.sh --yes backups/gbos-X.dump  # a specific one
#
# Without --yes it prints what it would do and stops.
#
# It refuses to start unless it can first take a dump of the current state, so
# a restore of the wrong file is itself undoable. That safety dump is written
# to $BACKUP_DIR/pre-restore-<timestamp>.dump.
#
# To test a backup WITHOUT touching live data, use scripts/restore-drill.sh.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

BACKUP_DIR="${BACKUP_DIR:-./backups}"
COMPOSE_SERVICE="${COMPOSE_SERVICE:-postgres}"
APP_SERVICES="${APP_SERVICES:-backend}"
PG_USER="${POSTGRES_USER:-gbos}"
PG_DB="${POSTGRES_DB:-gbos}"

confirmed=0
from_remote=0
dump=""
scratch=""

for arg in "$@"; do
    case "$arg" in
        --yes) confirmed=1 ;;
        --from-remote) from_remote=1 ;;
        -*) echo "!! unknown option: $arg" >&2; exit 2 ;;
        *) dump="$arg" ;;
    esac
done

# shellcheck disable=SC2329  # invoked by the trap on the next line
cleanup() { [ -n "$scratch" ] && rm -rf "$scratch"; }
trap cleanup EXIT

if [ "$from_remote" -eq 1 ]; then
    if [ -z "${BACKUP_REMOTE:-}" ] || ! command -v rclone >/dev/null 2>&1; then
        echo "!! --from-remote needs BACKUP_REMOTE set and rclone installed" >&2
        exit 1
    fi
    newest="$(rclone lsf "$BACKUP_REMOTE" --include 'gbos-*.dump' | sort | tail -1)"
    if [ -z "$newest" ]; then
        echo "!! no gbos-*.dump found at $BACKUP_REMOTE" >&2
        exit 1
    fi
    scratch="$(mktemp -d)"
    echo "==> fetching $newest from $BACKUP_REMOTE"
    rclone copy "$BACKUP_REMOTE/$newest" "$scratch"
    dump="$scratch/$newest"
fi

if [ -z "$dump" ]; then
    # Names are UTC timestamps, so the lexically last one is the newest.
    # Sorting by name beats `ls -t`: copying a file changes its mtime.
    dump="$(printf '%s\n' "$BACKUP_DIR"/gbos-*.dump 2>/dev/null | sort | tail -1)"
fi
if [ -z "$dump" ] || [ ! -f "$dump" ]; then
    echo "!! no backup found — pass one explicitly, or use --from-remote" >&2
    exit 1
fi

echo
echo "  restore source : $dump"
echo "  taken          : $(date -r "$dump" 2>/dev/null || stat -c %y "$dump" 2>/dev/null || echo unknown)"
echo "  target database: $PG_DB on compose service $COMPOSE_SERVICE"
echo "  will stop      : $APP_SERVICES"
echo
echo "  EVERYTHING currently in $PG_DB will be replaced."
echo "  Sales billed since this dump was taken will be gone."
echo

if [ "$confirmed" -ne 1 ]; then
    echo "Not confirmed. Re-run with --yes to actually do this."
    exit 1
fi

if ! docker compose ps --status running --format '{{.Service}}' 2>/dev/null | grep -qx "$COMPOSE_SERVICE"; then
    echo "!! compose service '$COMPOSE_SERVICE' is not running — start the stack first" >&2
    exit 1
fi

psql_admin() {
    docker compose exec -T "$COMPOSE_SERVICE" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 "$@"
}

# 1. Stop whatever writes, so the database is not moving while it is replaced.
echo "==> stopping $APP_SERVICES"
# shellcheck disable=SC2086
docker compose stop $APP_SERVICES

# 2. Safety dump. If this fails, stop — a restore you cannot undo is a much
#    worse position than the one you are already in.
mkdir -p "$BACKUP_DIR"
safety="$BACKUP_DIR/pre-restore-$(date -u +%Y%m%dT%H%M%SZ).dump"
echo "==> saving current state to $safety"
if ! docker compose exec -T "$COMPOSE_SERVICE" \
        pg_dump -U "$PG_USER" -d "$PG_DB" -Fc --no-owner --no-acl > "$safety"; then
    echo "!! could not dump the current database — refusing to overwrite it" >&2
    echo "   restart the app with: docker compose start $APP_SERVICES" >&2
    rm -f "$safety"
    exit 1
fi
chmod 600 "$safety"
echo "==> safety dump written ($(du -h "$safety" | cut -f1))"

# 3. Replace the database. DROP needs no other sessions attached; the app is
#    stopped, but a forgotten psql would block it.
echo "==> recreating $PG_DB"
psql_admin -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$PG_DB' AND pid <> pg_backend_pid()" >/dev/null
psql_admin -c "DROP DATABASE IF EXISTS \"$PG_DB\"" >/dev/null
psql_admin -c "CREATE DATABASE \"$PG_DB\" OWNER \"$PG_USER\"" >/dev/null

echo "==> restoring $dump"
if ! docker compose exec -T "$COMPOSE_SERVICE" \
        pg_restore -U "$PG_USER" -d "$PG_DB" --no-owner --no-acl < "$dump"; then
    echo "!! RESTORE FAILED." >&2
    echo "   The database is now empty or partial. Put it back with:" >&2
    echo "     bash scripts/restore.sh --yes $safety" >&2
    exit 1
fi

# 4. Back up. The backend applies any migrations the dump predates on start.
echo "==> starting $APP_SERVICES"
# shellcheck disable=SC2086
docker compose start $APP_SERVICES

echo "==> waiting for the application to report the database healthy"
for _ in $(seq 1 60); do
    if docker compose exec -T backend \
            wget -q -O - http://127.0.0.1:8080/api/v1/health 2>/dev/null | grep -q '"database":"ok"'; then
        echo
        echo "RESTORE COMPLETE — $dump is live."
        echo "Previous state kept at $safety (delete it once you are sure)."
        echo "Now log in and check today's figures before billing anything."
        exit 0
    fi
    sleep 2
done

echo "!! the backend did not come back healthy within two minutes" >&2
echo "   docker compose logs --tail 100 backend" >&2
exit 1
