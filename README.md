# GBOS

A point of sale and back office for an Indian retail or distribution business.
One to three shops, a counter that has to keep billing when the internet does
not, goods that sell both loose and in bulk, and GST returns that have to tie to
the cash drawer.

**Status: not yet live anywhere.** The books are correct and the operations are
in place; offline billing and the pilot are not done. See
[What is not finished](#what-is-not-finished).

## The problem this is shaped around

A sack of rice is not twenty-five kilos of rice, commercially. It costs less per
kilo, it scans differently, and a customer buys one or the other. Most retail
schemas cannot say that: a product has one price and one unit, so bulk must cost
exactly twenty-five times loose, which forbids the trade that bulk exists for.

So in GBOS a unit is a row, not an enum:

- **Families** group units that convert to one another by a stored factor. A
  kilo is a thousand grams everywhere, so that factor belongs to the unit.
- **A product's units** list what *that* product may be transacted as: loose by
  the kilo, a 5 kg bag, a 25 kg sack. Each carries `qty_in_base` — how many of
  the product's own stocking unit one of them amounts to — its own price per
  store, and its own barcode.
- **Stock is one pool in the base unit.** Receiving ten 25 kg sacks adds 250 kg;
  a 400 g sale takes 0.4 off the same pile. A shop can never be out of loose
  rice while sacks sit unopened.
- **A line total is `rate x unit_qty`** — a multiplication. Deriving a per-kilo
  rate from a sack's price gives 41.999… and a bill built on it is short.

Adding a unit, or a whole new family, is an INSERT. No deploy.

## Running it locally

```bash
make db-up        # PostgreSQL 16 in docker, on 5434
make backend      # migrates, seeds an owner, prints its password once
make frontend     # http://localhost:3000
```

The backend needs no configuration in development. In release mode it refuses to
start without `JWT_SECRET`, `DATABASE_URL` and an explicit `CORS_ORIGIN`, and it
rejects known placeholder secrets by name.

The owner password comes from `GBOS_ADMIN_PASSWORD` if set; otherwise one is
generated and printed once at startup, because nothing stores it in the clear.

## How it is put together

```
backend/
  cmd/server            wiring only
  cmd/migrate           schema up, down, version
  migrations/postgres   the schema, embedded in both binaries
  internal/
    platform/           config, pgx pool and Tx runner, error mapping, logging,
                        the HTTP error envelope, an in-process cache
    domain/             entities and invariants. No SQL, no HTTP.
      money/            Money (2dp), Quantity (3dp), Rate — one rounding policy each
    repo/postgres/      all SQL. One file per aggregate.
    service/            transactions, invariants, idempotency
    http/               thin handlers, middleware, the router
frontend/src/
  pages/                27 screens
  components/           DataTable, form kit, modal, charts
  theme.ts              colour, spacing, type, radius, shadow
  services/api.ts       the single HTTP choke point
```

Three rules hold the shape: SQL only in `repo/`, transactions only in
`service/`, and handlers never touch the database. The previous implementation
had all 219 queries inside HTTP handlers, and the cost was not style — it was
that every rule was a hand-written repetition nobody owned, so the same defect
recurred wherever someone forgot it.

### Money

Three decimal types, each with its own scale and its own rounding policy stated
once. `NUMERIC` in Postgres, `shopspring/decimal` in Go, never a float64 — `Scan`
refuses one rather than rounding it, because a rupee value arriving as binary
floating point is the defect these types exist to prevent.

There are no tolerance comparisons anywhere. The seven `math.Abs(x) > 0.05`
checks that used to reconcile client and server float drift are gone; equality
is exact.

### What the database enforces itself

Status columns are enums. Stock and reserved quantities cannot go negative. A
credit entry cannot be overpaid. An invoice series is unique per store. Money
columns are exact numeric. A defect the database rejects cannot be reintroduced
by the next handler someone writes.

### Invoice numbering

Per terminal: `INV/T1/2026-27/00001`, allocated gap-free from a sequence table
inside the sale's own transaction. CGST Rule 46(b) wants each series consecutive
and unique, and a terminal owning its own series never has to coordinate — which
is also what makes offline billing possible later.

The financial year turns at Indian midnight. Every date boundary in the system —
the year, a GSTR-1 month, a trading day — derives from one fixed +05:30, so two
of them can never disagree.

### Tax invoice particulars

Description, HSN code and rate of tax are snapshotted onto each invoice line
when it is written, not joined from the catalogue. Renaming a product must not
rewrite the bills it was already sold on, and a rate moved by notification must
not make an old invoice reprint at a rate the customer was never charged.

## Testing

```bash
make test            # 41 packages against real Postgres
make test-unit       # just the ones that need no database
make smoke           # a trading day against the built binary, over HTTP
make verify-security # the hardening checks, each tied to a past defect
```

Repository and service tests run against a real PostgreSQL, never a mock.
Mocked SQL would have hidden most of what was wrong here — drained cursors,
lock ordering, `ILIKE` versus `LIKE`, exact numeric.

| | Statement coverage |
|---|---|
| `internal/domain` | 90% |
| `internal/platform` | 84% |
| `internal/repo` | 78% |
| `internal/service` | 72% |
| `internal/http` | 38% |
| **overall** | **62%** |

Measured with `-coverpkg=./...`, so a service test credits the repository code it
drives. The HTTP layer is the thin one and also the weakest covered; that is the
gap to close next, not a claim that it does not matter.

Concurrency is tested, not assumed: twenty tills billing at once, crossed
baskets that used to deadlock, concurrent voids, and an idempotency key retried
while the original is still in flight.

## 🚀 Deployment

The target is **one VPS the owner administers themselves**, running the stack
under docker compose. **[docs/RUNBOOK.md](./docs/RUNBOOK.md)** is the operating
manual: first deploy, backups, restore, reading logs, and what to check when
the shop says it is down. Read that, not this summary.

### The stack

| Container | What it is | Reachable from |
|---|---|---|
| `gbos-caddy` | TLS terminator and front door; gets and renews its own certificate | the internet, ports 80 and 443 |
| `gbos-frontend` | nginx serving the built React bundle | Caddy only |
| `gbos-backend` | the Go application | Caddy only |
| `gbos-postgres` | PostgreSQL 16 — the shop's books | the backend only |

Only Caddy publishes a host port. The database is not exposed at all.

### Settings

Copy `.env.example` to `.env` and fill it in. Compose refuses to start if any
required value is missing, by name, rather than defaulting to something wrong:

| Required | What |
|---|---|
| `JWT_SECRET` | `openssl rand -base64 48`. Placeholders are rejected at start. |
| `POSTGRES_PASSWORD` | `openssl rand -base64 24` |
| `GBOS_DOMAIN` | the hostname the shop uses; a bare hostname triggers automatic TLS |
| `GBOS_TLS_EMAIL` | where Let's Encrypt reports renewal problems |
| `CORS_ORIGIN` | the exact public origin, e.g. `https://shop.example.com` |

`.env.example` documents the optional ones (ports, network, timezone, backups).

### Running it

```bash
cp .env.example .env && chmod 600 .env   # then edit it
docker compose config >/dev/null         # parses, starts nothing
docker compose up -d --build
docker compose ps                        # wait for (healthy)
```

Before the first `up` on a machine that already runs other stacks, work through
the port and network collision check in the runbook — the stack wants 80, 443
and the `10.89.7.0/24` private network.

### Backups

There is no backup until the timers are installed:

```bash
sudo bash scripts/install-timers.sh      # nightly backup + monthly restore drill
sudo nano /etc/gbos/backup.env           # set BACKUP_REMOTE, or it stays on this disk
bash scripts/restore-drill.sh            # prove a backup restores. Safe; never touches live data.
```

---


## What is not finished

| | |
|---|---|
| **Offline billing** | Not built. The counter needs the server. The old service-worker sync is removed: it read IndexedDB while the app wrote `localStorage`, its token getter always returned an empty string, and the offline path printed no receipt. Replacing it properly means terminal-scoped series (done), an outbox, idempotent sync against `processed_requests` (done), and cash-only rules offline. |
| **Pilot** | Not run. The gate is fourteen consecutive days in one shop reconciling against the cash drawer and a physical stock count with no manual correction. |
| **Mobile app** | Cut. The `mobile/` tree was seven Dart files, never built, read-only, with its API URL hardcoded to an emulator loopback. The web app is a PWA and serves the same purpose. |
| **HSN enforcement** | A product can be saved with no HSN code, and then its invoices go out without one. The invoice view flags it; nothing yet prevents it. |

## Known judgement calls

Three decisions deliberately left open rather than made quietly:

1. **Idempotency keys are scoped by check, not by key.** A mismatched store now
   gets a 409 rather than another shop's invoice, but the primary key is still
   the raw client string. The clean fix is a composite `(store_id, key)`, which
   is a migration that has to decide what happens to retries in flight across
   the deploy.
2. **A unit size with a fourth decimal is refused**, not rounded. `qty_in_base`
   is `numeric(14,4)` while quantities are `numeric(14,3)`. Refusing is
   consistent with the rule against silent tolerances, but it makes such a pack
   unsellable rather than slightly wrong. The alternative is widening quantities.
3. **Rejected goods close a purchase order.** The goods came, they were refused,
   nothing more is coming — which is the documented convention and is pinned by
   a test. Some trades expect a short receipt to leave the order open.

## Licence

MIT.
