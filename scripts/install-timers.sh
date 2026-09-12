#!/usr/bin/env bash
#
# Installs the nightly backup and the monthly restore drill as systemd timers.
#
#   sudo bash scripts/install-timers.sh
#
# Safe to run again: it overwrites the units with the current versions and
# reloads. It starts no containers and touches no data.
#
# What it does:
#   1. copies scripts/systemd/*.{service,timer} into /etc/systemd/system,
#      pointing them at wherever this repository actually lives
#   2. creates /etc/gbos/backup.env on first run, for the backup settings
#   3. enables both timers
#
# Afterwards, fill in /etc/gbos/backup.env — in particular BACKUP_REMOTE, or
# every backup stays on the disk it is supposed to protect against.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UNIT_DIR=/etc/systemd/system
CONF_DIR=/etc/gbos
CONF="$CONF_DIR/backup.env"

if ! command -v systemctl >/dev/null 2>&1; then
    echo "!! no systemd on this machine." >&2
    echo "   Use cron instead — add this line to root's crontab (crontab -e):" >&2
    echo "     15 2 * * * cd $REPO && bash scripts/backup.sh >> /var/log/gbos-backup.log 2>&1" >&2
    echo "     30 3 1 * * cd $REPO && bash scripts/restore-drill.sh --from-remote >> /var/log/gbos-drill.log 2>&1" >&2
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "!! run this with sudo — it writes to $UNIT_DIR" >&2
    exit 1
fi

echo "==> repository is at $REPO"

install -d -m 755 "$CONF_DIR"

if [ ! -f "$CONF" ]; then
    echo "==> creating $CONF"
    cat > "$CONF" <<EOF
# Settings for the GBOS backup timer. Read by scripts/backup.sh.
# Deliberately separate from the application's .env: this job has no business
# holding JWT_SECRET.

# Where dumps are written on this machine.
BACKUP_DIR=/var/backups/gbos

# Days to keep, here and on the remote.
RETAIN_DAYS=30

# An rclone remote to copy each dump to. WITHOUT THIS, every backup lives on
# the same disk as the database and none of them survive that disk failing.
#   rclone config                     # as root, once
#   rclone lsd <remote>:              # check it works
# BACKUP_REMOTE=b2:my-shop-backups/gbos
BACKUP_REMOTE=

# Only needed if you changed them in .env.
# POSTGRES_USER=gbos
# POSTGRES_DB=gbos
EOF
    chmod 600 "$CONF"
else
    echo "==> $CONF already exists, leaving it alone"
fi

install -d -m 700 /var/backups/gbos

for unit in gbos-backup.service gbos-backup.timer \
            gbos-restore-drill.service gbos-restore-drill.timer; do
    src="$REPO/scripts/systemd/$unit"
    if [ ! -f "$src" ]; then
        echo "!! missing $src" >&2
        exit 1
    fi
    # The units ship with /srv/gbos as a stand-in; point them at the real path.
    sed "s#/srv/gbos#$REPO#g" "$src" > "$UNIT_DIR/$unit"
    chmod 644 "$UNIT_DIR/$unit"
    echo "==> installed $UNIT_DIR/$unit"
done

systemctl daemon-reload

# Enabling a timer arms a schedule. It starts no containers and runs nothing
# now — the first backup happens at 02:15.
systemctl enable --now gbos-backup.timer
systemctl enable --now gbos-restore-drill.timer

echo
systemctl list-timers --no-pager 'gbos-*' || true

cat <<EOF

Installed.

Next:
  1. Edit $CONF and set BACKUP_REMOTE. Until you do, the backups are on the
     same disk as the database and protect against almost nothing.
  2. Prove it works now rather than in a month:
       systemctl start gbos-backup.service
       journalctl -u gbos-backup.service -n 40 --no-pager
       bash $REPO/scripts/restore-drill.sh
  3. Read docs/RUNBOOK.md, "Backups".
EOF
