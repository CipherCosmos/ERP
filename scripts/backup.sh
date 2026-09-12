#!/usr/bin/env bash
#
# Backs up the GBOS database.
#
# This volume is the shop's entire financial history. Before this script there
# was no backup of any kind: a stray `docker compose down -v`, a failed disk or
# a bad migration ended the business's books permanently.
#
#   bash scripts/backup.sh                    # back up, verify, rotate, copy off
#   BACKUP_DIR=/mnt/backups bash scripts/backup.sh
#
# Settings (all optional, normally set in .env):
#   BACKUP_DIR     where dumps are written          default ./backups
#   RETAIN_DAYS    days to keep, here and remote    default 30
#   BACKUP_REMOTE  an rclone target to copy to      default unset (warns)
#
# Put it on a timer — scripts/install-timers.sh does that for you, or by hand:
#   15 2 * * *  cd /srv/gbos && bash scripts/backup.sh >> /var/log/gbos-backup.log 2>&1
#
# A backup you have never restored is not a backup. scripts/restore-drill.sh
# proves this one works; run it monthly.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

BACKUP_DIR="${BACKUP_DIR:-./backups}"
RETAIN_DAYS="${RETAIN_DAYS:-30}"
COMPOSE_SERVICE="${COMPOSE_SERVICE:-postgres}"
PG_USER="${POSTGRES_USER:-gbos}"
PG_DB="${POSTGRES_DB:-gbos}"

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
target="$BACKUP_DIR/gbos-$timestamp.dump"

mkdir -p "$BACKUP_DIR"

# Two backups running at once would fight over rotation and could delete a file
# the other is still writing. mkdir is atomic on every filesystem that matters,
# which flock is not (and flock is absent on macOS).
lock="$BACKUP_DIR/.backup.lock"
if ! mkdir "$lock" 2>/dev/null; then
    echo "!! another backup is already running ($lock exists)" >&2
    echo "   if you are sure none is, remove it: rmdir $lock" >&2
    exit 1
fi
trap 'rmdir "$lock" 2>/dev/null || true' EXIT

# Fail early and legibly rather than emitting a confusing pg_dump error.
if ! docker compose ps --status running --format '{{.Service}}' 2>/dev/null | grep -qx "$COMPOSE_SERVICE"; then
    echo "!! compose service '$COMPOSE_SERVICE' is not running — nothing to back up" >&2
    echo "   check with: docker compose ps" >&2
    exit 1
fi

echo "==> backing up $PG_DB to $target"

# Custom format (-Fc): compressed, and pg_restore can read it selectively.
# Streaming straight to a file on the host means the dump never has to fit
# inside the container.
if ! docker compose exec -T "$COMPOSE_SERVICE" \
        pg_dump -U "$PG_USER" -d "$PG_DB" -Fc --no-owner --no-acl > "$target.partial"; then
    echo "!! pg_dump failed; leaving no partial file behind" >&2
    rm -f "$target.partial"
    exit 1
fi

# An empty or near-empty file means pg_dump wrote nothing useful even though it
# exited 0 — a full disk does exactly this.
bytes="$(wc -c < "$target.partial" | tr -d '[:space:]')"
if [ "${bytes:-0}" -lt 1024 ]; then
    echo "!! the dump is only ${bytes} bytes — treating this run as failed" >&2
    echo "   check free space: df -h $BACKUP_DIR" >&2
    rm -f "$target.partial"
    exit 1
fi

# Only name it as a real backup once it is complete, so a half-written file
# from an interrupted run can never be mistaken for one.
mv "$target.partial" "$target"
chmod 600 "$target"

size="$(du -h "$target" | cut -f1)"
echo "==> wrote $target ($size)"

# A dump that pg_restore cannot read is not a backup either. Listing its table
# of contents is cheap and catches a truncated or corrupt file now, rather than
# on the day it is needed.
#
# pg_restore reads the archive from stdin when given no filename. Do not pass
# /dev/stdin instead: a custom-format archive has to seek, and a pipe cannot.
entries="$(docker compose exec -T "$COMPOSE_SERVICE" pg_restore --list < "$target" 2>/dev/null | grep -c '^[0-9]' || true)"
if [ "${entries:-0}" -lt 1 ]; then
    echo "!! the dump is unreadable by pg_restore — treating this run as failed" >&2
    mv "$target" "$target.corrupt"
    exit 1
fi
echo "==> verified readable ($entries archive entries)"

echo "==> removing backups older than $RETAIN_DAYS days"
find "$BACKUP_DIR" -name 'gbos-*.dump' -type f -mtime "+$RETAIN_DAYS" -print -delete || true

# Copying off the machine is the part that matters. A backup sitting on the
# same disk as the database does not survive the failure it exists for.
if [ -n "${BACKUP_REMOTE:-}" ]; then
    if ! command -v rclone >/dev/null 2>&1; then
        echo "!! BACKUP_REMOTE is set but rclone is not installed — the backup is still only on this machine" >&2
        exit 1
    fi

    echo "==> copying to $BACKUP_REMOTE"
    # The whole directory, not just tonight's file: if an earlier night's copy
    # failed because the network was down, this catches it up. rclone skips
    # files that already match, so the usual case transfers one dump.
    rclone copy "$BACKUP_DIR" "$BACKUP_REMOTE" --include 'gbos-*.dump'
    echo "==> copied off host"

    # Same retention on the remote, or it grows until the bill does.
    # The --include is not cosmetic: without it this would delete anything else
    # that happens to live under BACKUP_REMOTE.
    echo "==> pruning $BACKUP_REMOTE of dumps older than $RETAIN_DAYS days"
    rclone delete "$BACKUP_REMOTE" --include 'gbos-*.dump' --min-age "${RETAIN_DAYS}d"
else
    echo "!! BACKUP_REMOTE is not set: this backup exists only on this machine," >&2
    echo "   which is the machine it protects against. Set BACKUP_REMOTE to an" >&2
    echo "   rclone target to copy it off." >&2
fi

# A monitoring hook that needs no monitoring system: if this file is older than
# a day, last night's backup did not happen.
#   find "$BACKUP_DIR/last-success" -mmin +1500 && echo "backup is stale"
date -u +%Y-%m-%dT%H:%M:%SZ > "$BACKUP_DIR/last-success"
echo "$target" >> "$BACKUP_DIR/last-success"

echo "==> done"
