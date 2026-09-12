# GBOS operations runbook

For whoever keeps the shop's system running. Assumes you can use SSH and a
terminal; assumes nothing about Docker beyond that.

Every command here is run from the directory holding `docker-compose.yml` —
`/srv/gbos` in the examples. `cd /srv/gbos` first, every time.

---

## What is running

Four containers. Only one of them is reachable from the internet.

| Container | What it is | Reachable from |
|---|---|---|
| `gbos-caddy` | the front door: TLS, certificates, redirects | the internet, ports 80 and 443 |
| `gbos-frontend` | the web pages the till shows | Caddy only |
| `gbos-backend` | the application: bills, stock, reports | Caddy only |
| `gbos-postgres` | the database — **this is the shop's books** | the backend only |

The database lives in a Docker volume called `gbos-pgdata`. Everything the
business has ever sold is in there. It is the only thing on the machine that
cannot be rebuilt from the repository.

> **The one command that must never be typed: `docker compose down -v`.**
> The `-v` deletes the volume. That is the end of the books, with no undo.
> To stop the stack, use `docker compose down` with no flags, or `stop`.

---

## First deploy

### 1. Check nothing else owns the ports first

GBOS publishes ports 80 and 443. If this machine already runs another web
stack, those are probably taken, and `docker compose up` will fail with
`address already in use` — or worse, half start.

```bash
# What is listening on 80 and 443 right now?
sudo ss -lntup '( sport = :80 or sport = :443 )'

# Which containers publish them?
docker ps --format '{{.Names}}\t{{.Ports}}' | grep -E ':(80|443)->'

# What private networks already exist? GBOS wants 10.89.7.0/24.
for n in $(docker network ls -q); do
  docker network inspect "$n" --format '{{.Name}} {{range .IPAM.Config}}{{.Subnet}}{{end}}'
done
```

Three possible answers:

**(a) Nothing holds 80/443, and nothing uses 10.89.7.0/24.** Carry on. This is
the normal case.

**(b) Something in that 10.89.x range is taken.** Pick another free /24 and put
*both* of these in `.env`, with the same value:

```
GBOS_NETWORK_SUBNET=10.89.9.0/24
TRUSTED_PROXIES=10.89.9.0/24
```

They must match. The backend uses `TRUSTED_PROXIES` to decide whose
`X-Forwarded-For` header to believe; if it is wrong, every customer shares one
login-attempt budget and a single wrong password can lock out the shop.

**(c) Another reverse proxy already owns 80 and 443.** Do not move GBOS's
certificate handling onto odd ports — Let's Encrypt connects to 80 and 443 by
name and will not follow you. Instead let the proxy that owns them do the TLS
and hand GBOS plain HTTP behind it. In `.env`:

```
GBOS_DOMAIN=:80                 # serve any hostname, plain HTTP, no certificate
GBOS_BIND_ADDRESS=127.0.0.1     # only the host itself can reach it
GBOS_HTTP_PORT=8081             # any free port
GBOS_HTTPS_PORT=8444            # unused in this mode, just needs to be free
CORS_ORIGIN=https://shop.example.com    # still the public address
```

then add one site to the existing proxy. For a Caddy already on the box:

```
shop.example.com {
    reverse_proxy 127.0.0.1:8081
}
```

`GBOS_TLS_EMAIL` is still required by compose; any address you read is fine —
in this mode no certificate is requested.

### 2. Fill in the settings

```bash
cp .env.example .env
chmod 600 .env
```

Open `.env` and set, at minimum:

```bash
openssl rand -base64 48     # paste as JWT_SECRET
openssl rand -base64 24     # paste as POSTGRES_PASSWORD
```

plus `GBOS_DOMAIN`, `GBOS_TLS_EMAIL` and `CORS_ORIGIN`.

**Put both generated values into a password manager now.** Nothing else stores
them. Losing `POSTGRES_PASSWORD` while the data volume survives means the
database can no longer be opened.

### 3. Point DNS at this machine

The certificate is issued by Let's Encrypt connecting back to `GBOS_DOMAIN`.
If DNS is not already pointing here, the first start will keep retrying and the
site will stay on plain HTTP. Check:

```bash
dig +short shop.example.com        # should print this machine's public IP
```

### 4. Check the files before starting anything

This parses every deployment file and starts nothing:

```bash
docker compose config >/dev/null && echo "settings are complete"
```

If a required setting is missing, this is where it says so, by name.

### 5. Start

```bash
docker compose up -d --build
docker compose ps
```

Wait for `backend` and `frontend` to say `(healthy)` — up to a minute on the
first run, because the backend builds the database schema before answering.

### 6. Get the first password

```bash
docker compose logs backend | grep -i "owner account"
```

If you left `GBOS_ADMIN_PASSWORD` empty, a random password was generated and
printed **once**, in that line. Copy it now; it is never shown again. Sign in
and change it immediately (see *Rotating the admin password*).

### 7. Prove it works from outside

```bash
curl -I http://shop.example.com     # expect 308, redirecting to https
curl -sS https://shop.example.com/api/v1/health
# {"database":"ok","status":"ok"}
```

If the second command shows a certificate error, see *Certificates* below.

### 8. Turn on backups

Nothing is backed up until you do this.

```bash
sudo bash scripts/install-timers.sh
sudo nano /etc/gbos/backup.env       # set BACKUP_REMOTE
sudo systemctl start gbos-backup.service
bash scripts/restore-drill.sh
```

See *Backups* for what each of those does.

---

## Is it working?

```bash
make ps          # or: docker compose ps
make health      # asks the app whether it can reach the database
```

`make health` prints `{"database":"ok","status":"ok"}`. That answer means the
application actually ran a query against Postgres — not merely that a process
is listening. If the database is down, this reports it.

---

## Reading the logs

```bash
docker compose logs -f --tail 100            # everything, live
docker compose logs --tail 200 backend       # just the application
docker compose logs --tail 200 caddy         # certificates, requests
docker compose logs --since 30m backend      # the last half hour
docker compose logs backend | grep -i error
```

Logs are capped: 10 MB per file, 5 files per container. Older lines are
discarded automatically, so they cannot fill the disk — but it also means a
problem from last month is gone. Copy anything you need to keep:

```bash
docker compose logs --no-color backend > ~/backend-$(date +%F).log
```

`Ctrl-C` leaves `-f` without stopping anything.

---

## "The shop says it's down"

Work down this list. Stop at the first thing that is wrong.

**1. Is it the shop's internet, or the server?**
From your own machine:
```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://shop.example.com/api/v1/health
```
`200` means the server is fine and the problem is at the shop — their router,
their Wi-Fi, their browser. Ask them to open the site on a phone using mobile
data.

**2. Are the containers running?**
```bash
docker compose ps
```
Look at the STATUS column. `Up (healthy)` is good. Anything `Exited`,
`Restarting`, or `Up (unhealthy)` is the problem — read that container's logs.

**3. Can the application reach the database?**
```bash
make health
docker compose logs --tail 50 postgres
```

**4. Is the disk full?**
```bash
df -h
```
Anything at 100% on `/` or `/var` will take the database down. See
*The disk is filling*.

**5. Did something get killed for using too much memory?**
```bash
docker stats --no-stream
sudo dmesg -T | grep -i -E 'killed process|out of memory' | tail
```
Each container has a memory cap (Postgres 1 GB, backend 512 MB, frontend
256 MB, Caddy 128 MB). The cap exists so one runaway process cannot take the
whole machine down — but a container that repeatedly hits its cap is being
killed and restarted, which looks to the shop like intermittent failure. The
caps are set in `docker-compose.yml` under `deploy: resources: limits:`. They
are limits, not reservations: raising one costs nothing until it is used.

**6. Has the certificate expired or failed to renew?**
```bash
docker compose logs caddy | grep -i -E 'acme|certificate|error' | tail -30
echo | openssl s_client -connect shop.example.com:443 2>/dev/null \
  | openssl x509 -noout -dates
```

**If you need it back up right now** and the cause is not obvious:

```bash
docker compose restart backend       # safe, ~10 seconds, keeps all data
```

If that helps, something is wrong that will come back — capture the logs before
the restart wipes the evidence, and look properly afterwards.

---

## Backups

### What is set up

`scripts/install-timers.sh` installs two systemd timers:

| Timer | When | What |
|---|---|---|
| `gbos-backup.timer` | every night, 02:15 | dump the database, verify it, rotate old copies, copy offsite |
| `gbos-restore-drill.timer` | first Sunday, 03:30 | fetch the newest offsite copy and prove it restores |

Settings live in `/etc/gbos/backup.env` — deliberately separate from the
application's `.env`, because the backup job has no business holding
`JWT_SECRET`.

**The recovery point is up to one day.** Everything billed after the last
successful backup is lost in a restore. If that is too much for this shop, add
a second `OnCalendar=` line (say `14:15`) to
`/etc/systemd/system/gbos-backup.timer` and `systemctl daemon-reload`.

### Did last night's backup run?

```bash
systemctl list-timers 'gbos-*'                   # when last, when next
journalctl -u gbos-backup.service -n 40 --no-pager
cat /var/backups/gbos/last-success               # timestamp of the last good run
ls -lh /var/backups/gbos/
```

A one-line staleness check, suitable for any monitoring you already have — it
prints the file only if the last success is more than about 25 hours old:

```bash
find /var/backups/gbos/last-success -mmin +1500
```

### Run one by hand

```bash
sudo systemctl start gbos-backup.service     # exactly as the timer runs it
# or
bash scripts/backup.sh                       # same script, output on screen
```

### Is it going offsite?

A backup on the same disk as the database does not survive the failure it
exists for. Check `BACKUP_REMOTE` is set:

```bash
grep BACKUP_REMOTE /etc/gbos/backup.env

# List what is actually up there, using the same setting the timer uses.
sudo bash -c 'set -a; . /etc/gbos/backup.env; set +a; rclone lsl "$BACKUP_REMOTE"'
```

If `BACKUP_REMOTE` is empty, the backup script says so loudly on every run, and
the shop has no real backup. Fix it:

```bash
sudo rclone config          # create a remote: Backblaze B2, S3, Drive, whatever
sudo rclone lsd myremote:   # prove it works
sudo nano /etc/gbos/backup.env
```

### Prove a backup restores — the drill

```bash
bash scripts/restore-drill.sh                # newest local copy
bash scripts/restore-drill.sh --from-remote  # newest offsite copy  ← the real test
```

It restores into a scratch database beside the live one, counts the tables and
rows, checks the revenue total, and drops the scratch copy. **It never touches
the live database.** It prints `DRILL PASSED` or `DRILL FAILED`.

`--from-remote` is the one that matters: drilling the local copy proves the
restore path works and says nothing about the copy that survives the disk
dying.

A failing drill is as urgent as the shop being down, because it means the shop
cannot be brought back when it does go down.

---

## Restoring from a backup

**Read this whole section before starting.** A restore throws away everything
billed since the backup was taken.

```bash
cd /srv/gbos

# 1. See what would happen. This changes nothing.
bash scripts/restore.sh

# 2. Do it.
bash scripts/restore.sh --yes                    # newest local backup
bash scripts/restore.sh --yes --from-remote      # newest offsite backup
bash scripts/restore.sh --yes /path/to/gbos-20260911T021500Z.dump
```

What the script does, in order:

1. stops the backend, so nothing writes while the database is replaced
2. **dumps the current state first**, to
   `/var/backups/gbos/pre-restore-<timestamp>.dump` — so a restore of the wrong
   file is itself undoable. If this step fails it stops and changes nothing.
3. drops and recreates the database, and restores the dump into it
4. starts the backend, which applies any newer migrations
5. waits until the application reports the database healthy

Afterwards, before anyone bills anything:

- sign in and open today's sales — confirm the figures match the day you
  restored to
- check stock on a few fast-moving items against the shelf
- tell the shop exactly which period of billing was lost, so they can re-enter
  it from the paper copies

Keep `pre-restore-*.dump` until you are certain. Then delete it — it contains
the whole database.

### Restoring onto a different machine

Same procedure, from the top of *First deploy*, with one change: after step 5
(`docker compose up -d --build`), copy the dump across and restore it **before
anyone signs in**. The new machine will have seeded its own fresh admin
account; the restore replaces it with the real one, so use the old machine's
admin password afterwards, not the one printed in the new machine's logs.

---

## Rotating the admin password

### Normal case — you know the current password

In the app: sign in, open the password change screen. Or directly:

```bash
curl -sS -X POST https://shop.example.com/api/v1/auth/change-password \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"old_password":"...","new_password":"..."}'
```

Rules: at least 10 characters, at most 72, and it must contain letters plus at
least one digit or symbol.

Changing a password ends every session that user has, including the one that
made the request. Everyone signs in again. That is intended.

### The admin password is lost

There is no reset link. Set the stored hash directly.

```bash
# 1. Make a bcrypt hash of the new password. The Caddy image is already on this
#    machine and can do it. Mind the quotes: single, so the shell leaves the
#    password alone.
docker run --rm caddy:2-alpine caddy hash-password --plaintext 'NewPassword-2026'
# -> $2a$14$....................................................

#    If that is not available, apache2-utils does the same job:
#      htpasswd -bnBC 12 "" 'NewPassword-2026' | tr -d ':\n'
#    Either way the hash must start with "$2" — that is bcrypt, which is what
#    the application reads. Anything else will not let anyone in.

# 2. Write it in, and end every session that user has. The \$ are escaped so
#    the shell passes the dollars through to psql unchanged.
docker compose exec postgres psql -U gbos -d gbos -c \
  "UPDATE users
      SET password_hash = '\$2a\$14\$....',
          sessions_invalidated_at = now(),
          updated_at = now()
    WHERE username = 'admin'"
```

`UPDATE 1` is the answer you want. `UPDATE 0` means the username is not
`admin` — list them with
`SELECT username, role, is_active FROM users;`.

Check it took effect:

```bash
docker compose exec postgres psql -U gbos -d gbos -c \
  "SELECT username, sessions_invalidated_at FROM users WHERE username = 'admin'"
```

Then sign in with the new password and change it again through the app, so the
plaintext is not sitting in your shell history:

```bash
history -d $(history 1)      # or clear the whole file
```

### Ending everyone's session at once

Change `JWT_SECRET` in `.env` and `docker compose up -d backend`. Every issued
token becomes invalid immediately. Nobody loses data; everybody signs in again.

---

## The disk is filling

```bash
df -h
sudo du -h -d1 /var/lib/docker | sort -h | tail
sudo du -sh /var/backups/gbos
docker system df
```

In rough order of likelihood:

**1. Backups.** They are kept for `RETAIN_DAYS` (30 by default) in
`/etc/gbos/backup.env`, and each one is the whole database. Lower it, or move
`BACKUP_DIR` to a bigger disk:

```bash
sudo nano /etc/gbos/backup.env      # RETAIN_DAYS=14
```

Old dumps are rotated on the next run, locally and on the remote.

**2. Old images and build cache.** Every `up -d --build` leaves the previous
image behind.

```bash
docker system df                 # see how much is reclaimable first
docker image prune               # untagged images only — safe
docker builder prune             # build cache — safe, slows the next build
```

Be careful with `docker image prune -a` and `docker system prune` on a machine
that runs other projects: they remove anything not currently in use by a
*running* container, including images another stopped stack needs.

**3. Container logs.** Capped at 10 MB × 5 files per container — about 200 MB
across the stack, and they cannot grow past it. If this is your problem, the
cap has been removed from `docker-compose.yml`; put it back.

**4. The database itself.** Genuine growth, on a shop with years of history:

```bash
docker compose exec postgres psql -U gbos -d gbos -c "\l+"
docker compose exec postgres psql -U gbos -d gbos -c \
  "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS size
     FROM pg_catalog.pg_statio_user_tables
    ORDER BY pg_total_relation_size(relid) DESC LIMIT 10"
```

If the disk is *already* full, Postgres will refuse writes and the till will
stop. Free space before anything else — deleting the oldest backup dumps is the
fastest safe move, as long as a recent one survives offsite:

```bash
ls -t /var/backups/gbos/gbos-*.dump | tail -n +5 | xargs -r rm
```

Never delete anything under `/var/lib/docker/volumes/` to free space.

---

## Certificates

Caddy gets and renews them by itself, about 30 days before expiry. There is
nothing to schedule and nothing to remember.

```bash
docker compose logs caddy | grep -i -E 'acme|obtain|renew|certificate'
echo | openssl s_client -connect shop.example.com:443 2>/dev/null \
  | openssl x509 -noout -dates -subject
```

When issuing fails, it is almost always one of:

- **DNS does not point here.** `dig +short shop.example.com` must show this
  machine's public IP.
- **Port 80 or 443 is not reachable from the internet** — a firewall or the VPS
  provider's security group. Let's Encrypt has to connect in.
- **Rate limited.** Five certificates per domain per week. This happens when
  the certificate storage is lost repeatedly — which is why `gbos-caddy-data`
  is a volume. Do not delete that volume.

`GBOS_TLS_EMAIL` is where Let's Encrypt writes when renewal has been failing
for weeks. Make sure it is an address somebody reads.

---

## Changing the domain

1. Point the new DNS name at this machine and wait for it to resolve.
2. Edit `.env`: `GBOS_DOMAIN` and `CORS_ORIGIN` (both — they must agree).
3. `docker compose up -d caddy backend`

Caddy fetches a certificate for the new name within a few seconds. Leave the
old DNS record in place for a day so anyone with the old address still lands.

---

## Deploying a new version

```bash
cd /srv/gbos
bash scripts/backup.sh          # before, not after
git pull
docker compose build
docker compose up -d
docker compose ps
make health
```

The backend applies any new database migrations on start; there is no separate
step. Watch it come up:

```bash
docker compose logs -f backend
```

If it fails to start, the previous image is still on the machine —
`docker images gbos-backend` lists them — but the cleanest way back is to check
out the previous commit and rebuild, then restore the backup you took first if
a migration had already changed the schema.

Do this when the shop is closed.

---

## Reboots

Everything is set to `restart: unless-stopped`, so the whole stack comes back
by itself after a power cut or a reboot. Nothing needs to be typed.

Two things worth knowing:

- The frontend deliberately waits for the backend to report the database
  healthy before it starts. On a slow boot, or while Postgres is replaying its
  write-ahead log, the site can be unreachable for a minute or two longer than
  you expect. That is correct behaviour, not a fault.
- `unless-stopped` means exactly that: if you ran `docker compose stop`, the
  reboot will *not* start it again. Use `docker compose start`.

Test it deliberately, once, when the shop is closed:

```bash
sudo reboot
# wait, then from your own machine:
curl -sS https://shop.example.com/api/v1/health
```

---

## Checking a change before it goes near the shop

The deployment files are the ones nobody compiles, so nothing catches a mistake
in them until it is on this machine.

```bash
make ops-check      # parses compose, checks the shell scripts — starts nothing
make caddy-check    # asks Caddy to validate its own config
```

CI runs both on every push, plus the Go test suite against a real Postgres, the
migrations up and down, and the frontend build.

---

## Quick reference

```bash
cd /srv/gbos

make ps                     # what is running
make health                 # can the app reach the database?
make logs                   # follow everything
make backup                 # back up now
make restore-drill          # prove a backup restores (safe)
make restore ARGS="--yes"   # DESTRUCTIVE: replace the live database
make ops-check              # validate the deployment files

docker compose restart backend      # safe, ~10s
docker compose up -d                # apply a change to .env or compose
docker compose down                 # stop everything, KEEP the data
docker compose down -v              # NEVER. Deletes the books.
```
