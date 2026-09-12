#!/usr/bin/env bash
#
# Proves a backup can actually be restored.
#
# A backup you have never restored is not a backup — it is a file you hope is
# a backup. This restores the most recent dump into a scratch database beside
# the live one, checks the data is really there, and throws the scratch copy
# away. It never touches the live database.
#
#   bash scripts/restore-drill.sh                     # newest local backup
#   bash scripts/restore-drill.sh --from-remote       # newest offsite backup
#   bash scripts/restore-drill.sh backups/gbos-X.dump # a specific one
#
# --from-remote is the drill that matters: it proves the copy that survives the
# machine burning down is the one that restores. Run that one at least monthly,
# and after any change to the schema or the backup script.
#
# Settings:
#   BACKUP_DIR      where dumps live                default ./backups
#   BACKUP_REMOTE   rclone target, for --from-remote
#   MAX_AGE_HOURS   fail if the dump is older       default 36
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

BACKUP_DIR="${BACKUP_DIR:-./backups}"
COMPOSE_SERVICE="${COMPOSE_SERVICE:-postgres}"
PG_USER="${POSTGRES_USER:-gbos}"
MAX_AGE_HOURS="${MAX_AGE_HOURS:-36}"
DRILL_DB="gbos_restore_drill_$(date -u +%Y%m%d%H%M%S)"

scratch=""
cleanup_scratch() { [ -n "$scratch" ] && rm -rf "$scratch"; }
trap cleanup_scratch EXIT

dump="${1:-}"

if [ "$dump" = "--from-remote" ]; then
    dump=""
    if [ -z "${BACKUP_REMOTE:-}" ]; then
        echo "!! --from-remote needs BACKUP_REMOTE set (see .env.example)" >&2
        exit 1
    fi
    if ! command -v rclone >/dev/null 2>&1; then
        echo "!! --from-remote needs rclone installed" >&2
        exit 1
    fi
    # Dump names are UTC timestamps, so the lexically last name is the newest.
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
    echo "!! no backup found in $BACKUP_DIR — run scripts/backup.sh first" >&2
    exit 1
fi

# A drill that passes against a dump from three months ago proves the restore
# path works and says nothing about whether backups are still running.
#
# The age comes from the name, not the file's mtime: a dump fetched with
# --from-remote was written to disk seconds ago, so mtime would report every
# offsite backup as fresh — and that is the copy this check most needs to
# police. Names are fixed-width UTC timestamps, so a string compare orders them.
stamp="$(basename "$dump")"
stamp="${stamp#gbos-}"
stamp="${stamp%.dump}"

threshold=""
# GNU date and BSD date spell relative times differently; try both.
threshold="$(date -u -d "-${MAX_AGE_HOURS} hours" +%Y%m%dT%H%M%SZ 2>/dev/null)" ||
    threshold="$(date -u -v "-${MAX_AGE_HOURS}H" +%Y%m%dT%H%M%SZ 2>/dev/null)" ||
    threshold=""

if [ -z "$threshold" ]; then
    echo "   (could not work out the age of $stamp on this system — skipping the freshness check)"
elif [[ "$stamp" < "$threshold" ]]; then
    echo "!! $dump is older than $MAX_AGE_HOURS hours — backups have stopped running" >&2
    echo "   check the timer: systemctl status gbos-backup.timer" >&2
    echo "   to drill it anyway: MAX_AGE_HOURS=100000 bash scripts/restore-drill.sh ..." >&2
    exit 1
fi

echo "==> drilling $dump into $DRILL_DB"

psql_admin() {
    docker compose exec -T "$COMPOSE_SERVICE" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 "$@"
}
psql_drill() {
    docker compose exec -T "$COMPOSE_SERVICE" psql -U "$PG_USER" -d "$DRILL_DB" -tAq "$@"
}

cleanup() {
    echo "==> dropping $DRILL_DB"
    psql_admin -c "DROP DATABASE IF EXISTS \"$DRILL_DB\"" >/dev/null 2>&1 || true
    cleanup_scratch
}
trap cleanup EXIT

psql_admin -c "CREATE DATABASE \"$DRILL_DB\"" >/dev/null

echo "==> restoring"
if ! docker compose exec -T "$COMPOSE_SERVICE" \
        pg_restore -U "$PG_USER" -d "$DRILL_DB" --no-owner --no-acl < "$dump"; then
    echo "!! RESTORE FAILED — this backup cannot be used" >&2
    exit 1
fi

echo "==> checking the restored database"
failed=0

tables="$(psql_drill -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")"
if [ "${tables:-0}" -lt 25 ]; then
    echo "!! only $tables tables restored, expected at least 25" >&2
    failed=1
else
    echo "   $tables tables"
fi

# The schema version has to come back too, or the application will try to
# migrate a database it does not understand.
version="$(psql_drill -c "SELECT version FROM schema_migrations LIMIT 1" 2>/dev/null || echo '')"
if [ -z "$version" ]; then
    echo "!! no schema version in the restored database" >&2
    failed=1
else
    echo "   schema version $version"
fi

# The point of the drill is the data, not the empty shell of it.
for table in invoices invoice_items stock products; do
    n="$(psql_drill -c "SELECT count(*) FROM $table" 2>/dev/null || echo 'error')"
    echo "   $table: $n rows"
    if [ "$n" = "error" ]; then
        echo "!! could not read $table" >&2
        failed=1
    fi
done

# Financial totals should survive a round trip exactly. numeric is exact, so
# any difference here means the dump lost something.
revenue="$(psql_drill -c "SELECT COALESCE(SUM(total),0)::text FROM invoices WHERE status='completed'" 2>/dev/null || echo 'error')"
echo "   completed revenue: $revenue"
if [ "$revenue" = "error" ]; then
    failed=1
fi

echo
if [ "$failed" -ne 0 ]; then
    echo "DRILL FAILED — do not rely on this backup"
    exit 1
fi
echo "DRILL PASSED — $dump restores cleanly"
