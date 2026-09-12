#!/usr/bin/env bash
# A trading day against the built binary, over HTTP, on a database this script
# creates and drops.
#
# What this is for, and what it is deliberately not: the Go suite already covers
# the domain, the services, the repositories and the HTTP surface, against real
# Postgres, with exact assertions. Reimplementing that in bash is how the
# previous version of this file rotted into 199 failures on a healthy system —
# wrong in a way that trains everyone to ignore it.
#
# So this checks only what the Go tests structurally cannot: that a compiled
# binary, started the way the deployment starts it, migrates an empty database,
# bootstraps an owner, and can then run a shop's day end to end with the figures
# reconciling. If this passes, the artefact works. If it fails, the artefact is
# broken in a way no unit test would have caught.
#
# Usage:
#   bash backend/scripts/smoke-test.sh        # needs the dev Postgres: make db-up
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_DIR="$(cd "$BACKEND_DIR/.." && pwd)"
WORK="$(mktemp -d)"

PASS=0
FAIL=0
FAILURES=()

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'

step()  { printf '\n%s== %s ==%s\n' "$BLUE" "$1" "$NC"; }
ok()    { printf '  %sPASS%s  %s\n' "$GREEN" "$NC" "$1"; PASS=$((PASS + 1)); }
bad()   { printf '  %sFAIL%s  %s\n        got: %s\n' "$RED" "$NC" "$1" "$2"; FAIL=$((FAIL + 1)); FAILURES+=("$1"); }

# A throwaway database per run. Never gbos_dev: this script creates and voids
# real documents, and those must not land in the database someone is working in.
SMOKE_DB="gbos_smoke_$$"
DEV_DB_URL="${DEV_DB_URL:-postgres://gbos:gbos_dev_password@localhost:5434/gbos_dev?sslmode=disable}"
SMOKE_DB_URL=$(printf '%s' "$DEV_DB_URL" | sed -E "s#/[^/?]+(\?|$)#/$SMOKE_DB\1#")
case "$SMOKE_DB_URL" in
  *"/$SMOKE_DB"*) : ;;
  *) echo "FATAL: could not derive a throwaway database URL from DEV_DB_URL"; exit 1 ;;
esac

# Satisfies the server's own password policy rather than a constant this script
# can fall behind.
ADMIN_PASSWORD="smoke-owner-$$-only"

# Documents are dated in the shop's zone, which is also the zone the server
# pins. Deriving the date any other way puts a bill in yesterday for the first
# five and a half hours of every day.
TODAY=$(TZ=Asia/Kolkata date +%Y-%m-%d)

# No host psql is assumed; maintenance statements go through the dev compose
# Postgres, the same way `make db-up` issues them.
pg_admin() {
  docker compose -f "$REPO_DIR/docker-compose.dev.yml" exec -T postgres \
    psql -U gbos -d postgres -v ON_ERROR_STOP=1 -tAc "$1"
}

SERVER_PID=""
DB_CREATED=""
cleanup() {
  # Only the server this script started, by the pid it captured. Never a search
  # for whatever holds a port.
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [ -n "$DB_CREATED" ]; then
    pg_admin "DROP DATABASE IF EXISTS \"$SMOKE_DB\" WITH (FORCE)" >/dev/null 2>&1 \
      || printf '  %snote: could not drop %s — drop it by hand%s\n' "$YELLOW" "$SMOKE_DB" "$NC"
  fi
  rm -rf "$WORK"

  printf '\n%s== result ==%s\n' "$BLUE" "$NC"
  printf '  passed %d, failed %d\n' "$PASS" "$FAIL"
  if [ ${#FAILURES[@]} -gt 0 ]; then
    printf '\n%sfailed:%s\n' "$RED" "$NC"
    for f in "${FAILURES[@]}"; do printf '  - %s\n' "$f"; done
  fi
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# HTTP helpers. Each sets STATUS and BODY.
# ---------------------------------------------------------------------------

TOKEN=""
STORE=""

req() {
  local method="$1" path="$2" body="${3:-}"
  local url="$BASE$path"
  case "$path" in
    *\?*) [ -n "$STORE" ] && url="$url&store_id=$STORE" ;;
    *)    [ -n "$STORE" ] && url="$url?store_id=$STORE" ;;
  esac
  local args=(-sS -o "$WORK/body" -w '%{http_code}' -X "$method" "$url" -H 'Content-Type: application/json')
  [ -n "$TOKEN" ] && args+=(-H "Authorization: Bearer $TOKEN")
  [ -n "$body" ] && args+=(-d "$body")
  # A fresh idempotency key per call: these are distinct documents, not retries.
  args+=(-H "Idempotency-Key: smoke-$$-$RANDOM-$RANDOM")
  STATUS=$(curl "${args[@]}" 2>/dev/null || echo 000)
  BODY=$(cat "$WORK/body" 2>/dev/null)
}

# expect NAME METHOD PATH EXPECTED_STATUS [BODY]
expect() {
  local name="$1" method="$2" path="$3" want="$4" body="${5:-}"
  req "$method" "$path" "$body"
  if [ "$STATUS" = "$want" ]; then
    ok "$name"
  else
    bad "$name" "HTTP $STATUS ${BODY:0:200}"
  fi
}

# field JQ_EXPR — reads from the last response
field() { printf '%s' "$BODY" | jq -r "$1 // empty" 2>/dev/null; }

# equals NAME ACTUAL EXPECTED
equals() {
  if [ "$2" = "$3" ]; then ok "$1 = $3"; else bad "$1" "$2, want $3"; fi
}

# ---------------------------------------------------------------------------
step "start the built binary against an empty database"
# ---------------------------------------------------------------------------

PORT=$(/usr/bin/env python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
BASE="http://127.0.0.1:$PORT/api/v1"
echo "  port $PORT, database $SMOKE_DB"

pg_admin "CREATE DATABASE \"$SMOKE_DB\"" >/dev/null || {
  echo "FATAL: could not create $SMOKE_DB. Is the dev Postgres up? Run: make db-up"
  exit 1
}
DB_CREATED=1

( cd "$BACKEND_DIR" && go build -o "$WORK/server" ./cmd/server ) || { echo "FATAL: build failed"; exit 1; }

DATABASE_URL="$SMOKE_DB_URL" SERVER_PORT="$PORT" TZ=Asia/Kolkata \
  GBOS_ADMIN_PASSWORD="$ADMIN_PASSWORD" "$WORK/server" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 60); do
  curl -fsS "$BASE/health" >/dev/null 2>&1 && break
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "FATAL: server exited during startup"; tail -20 "$WORK/server.log"; exit 1
  fi
  sleep 0.25
done

req GET /health
if [ "$STATUS" = "200" ] && [ "$(field .database)" = "ok" ]; then
  ok "the binary migrated an empty database and is serving"
else
  bad "health" "HTTP $STATUS ${BODY:0:200}"
  tail -20 "$WORK/server.log"
  exit 1
fi

# ---------------------------------------------------------------------------
step "the owner account exists and nothing else does"
# ---------------------------------------------------------------------------

req POST /auth/login "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}"
TOKEN=$(field .token)
if [ -n "$TOKEN" ]; then ok "owner signs in with GBOS_ADMIN_PASSWORD"; else bad "owner login" "${BODY:0:200}"; exit 1; fi

# The old seed planted demo logins. A shop inherits them if they come back.
for u in cashier helper manager; do
  req POST /auth/login "{\"username\":\"$u\",\"password\":\"${u}123\"}"
  [ "$STATUS" = "401" ] && ok "no seeded '$u' account" || bad "seeded '$u' account exists" "HTTP $STATUS"
done

if grep -q "$ADMIN_PASSWORD" "$WORK/server.log"; then
  # A supplied password must not be echoed; only a generated one is printed, once.
  bad "startup log leaks the supplied owner password" "found it in the log"
else
  ok "startup log does not echo the supplied password"
fi

# ---------------------------------------------------------------------------
step "set the shop up"
# ---------------------------------------------------------------------------

req POST /stores '{"name":"Smoke Kirana","address":"1 Market Rd","phone":"9000000001","gstin":"29ABCDE1234F1Z5","location_type":"store"}'
STORE=$(field .id)
[ -n "$STORE" ] && ok "store created" || { bad "store create" "${BODY:0:200}"; exit 1; }

req POST "/stores/$STORE/terminals" '{"code":"T1","name":"Counter 1"}'
TERMINAL=$(field .id)
[ -n "$TERMINAL" ] && ok "terminal T1 registered" || { bad "terminal register" "${BODY:0:200}"; exit 1; }

req POST /categories '{"name":"Staples","hsn_code":"1006"}'
CATEGORY=$(field .id)
[ -n "$CATEGORY" ] && ok "category created" || { bad "category create" "${BODY:0:200}"; exit 1; }

# The units migration seeds the ordinary Indian retail vocabulary, so KG is
# already on file with its family and conversion factor.
req GET /uoms
KG=$(field '.units[] | select(.code=="KG") | .id')
[ -n "$KG" ] && ok "KG is seeded with a family" || bad "KG missing from seeded units" "${BODY:0:200}"

# ---------------------------------------------------------------------------
step "a product sold both loose and by the sack"
# ---------------------------------------------------------------------------

req POST /products "{\"name\":\"Sona Masoori Rice\",\"category_id\":\"$CATEGORY\",\"uom_id\":\"$KG\",\"hsn_code\":\"10063010\",\"gst_rate\":5,\"allow_decimal\":true,\"min_stock\":10}"
RICE=$(field .id)
[ -n "$RICE" ] && ok "product created" || { bad "product create" "${BODY:0:200}"; exit 1; }

req POST "/products/$RICE/units" "{\"label\":\"Loose kg\",\"qty_in_base\":1,\"uom_id\":\"$KG\",\"is_default\":true,\"is_sellable\":true,\"is_purchasable\":true}"
LOOSE=$(field .id)
[ -n "$LOOSE" ] && ok "loose kilo unit added" || bad "loose unit" "${BODY:0:200}"

req POST "/products/$RICE/units" "{\"label\":\"25 kg sack\",\"qty_in_base\":25,\"uom_id\":\"$KG\",\"is_sellable\":true,\"is_purchasable\":true}"
SACK=$(field .id)
[ -n "$SACK" ] && ok "25 kg sack unit added" || bad "sack unit" "${BODY:0:200}"

# Each unit carries its own price. Bulk being cheaper per kilo is the entire
# point of selling it, so 1050 for 25 kg must coexist with 45 a kilo.
expect "loose priced at 45/kg"  POST /products/price 200 \
  "{\"product_id\":\"$RICE\",\"product_unit_id\":\"$LOOSE\",\"mrp\":50,\"cost_price\":38,\"sell_price\":45}"
expect "sack priced at 1050"    POST /products/price 200 \
  "{\"product_id\":\"$RICE\",\"product_unit_id\":\"$SACK\",\"mrp\":1200,\"cost_price\":940,\"sell_price\":1050}"

# ---------------------------------------------------------------------------
step "receive stock through a purchase order"
# ---------------------------------------------------------------------------

req POST /vendors '{"name":"Metro Wholesale","phone":"9000000002","gstin":"29ZZZZZ1234F1Z5","state":"Karnataka","credit_days":30}'
VENDOR=$(field .id)
[ -n "$VENDOR" ] && ok "vendor created" || { bad "vendor create" "${BODY:0:200}"; exit 1; }

# Four sacks, priced per sack. Stock is one pool in the base unit, so this has
# to land as 100 kg costed at 37.60 a kilo, not 4 at 940.
req POST /purchases "{\"vendor_id\":\"$VENDOR\",\"terminal_id\":\"$TERMINAL\",\"order_date\":\"$TODAY\",\"items\":[{\"product_id\":\"$RICE\",\"product_unit_id\":\"$SACK\",\"unit_qty\":4,\"rate\":940}]}"
PO=$(field '.purchase_order.id // .id')
[ -n "$PO" ] && ok "purchase order raised" || { bad "purchase order" "${BODY:0:200}"; exit 1; }

expect "goods received" POST /purchases/receive 201 \
  "{\"po_id\":\"$PO\",\"received_date\":\"$TODAY\",\"items\":[{\"product_id\":\"$RICE\",\"product_unit_id\":\"$SACK\",\"unit_qty\":4,\"accepted_qty\":4,\"batch_no\":\"SMK-1\",\"cost_price\":940}]}"

req GET "/stock/product/$RICE"
ON_HAND=$(printf '%s' "$BODY" | jq -r '[.batches[]?.quantity // 0] | add // 0')
equals "4 sacks land as kilos" "$ON_HAND" "100.000"
# The batch holds what the delivery cost, and the per-kilo figure is derived
# from it by the database. Four sacks at Rs.940 is Rs.3,760.00 exactly.
BATCH_VALUE=$(printf '%s' "$BODY" | jq -r '[.batches[]?.total_cost // 0] | add // 0')
equals "four sacks at 940 are worth" "$BATCH_VALUE" "3760.00"
UNIT_COST=$(printf '%s' "$BODY" | jq -r '.batches[0].cost_price // 0')
equals "which is per kilo" "$UNIT_COST" "37.6"

# ---------------------------------------------------------------------------
step "sell it, loose and in bulk, on one bill"
# ---------------------------------------------------------------------------

# 2 kg loose at 45 is 90.00; one sack at 1050 is 1050.00. Sub-total 1140.00,
# GST at 5% is 57.00, total 1197.00. The sack is one multiplication, never a
# per-kilo rate multiplied back up.
req POST /invoices "{\"terminal_id\":\"$TERMINAL\",\"payment_mode\":\"cash\",\"items\":[
  {\"product_id\":\"$RICE\",\"product_unit_id\":\"$LOOSE\",\"unit_qty\":2},
  {\"product_id\":\"$RICE\",\"product_unit_id\":\"$SACK\",\"unit_qty\":1}]}"
if [ "$STATUS" != "201" ]; then bad "cash sale" "HTTP $STATUS ${BODY:0:300}"; else
  ok "cash sale raised"
  equals "sub total"      "$(field '.invoice.sub_total')" "1140.00"
  equals "cgst"           "$(field '.invoice.cgst')"      "28.50"
  equals "sgst"           "$(field '.invoice.sgst')"      "28.50"
  equals "total"          "$(field '.invoice.total')"     "1197.00"
  equals "invoice number" "$(field '.invoice.invoice_no' | cut -d/ -f1-2)" "INV/T1"
  equals "line carries its HSN" "$(field '.items[0].hsn_code')" "10063010"
  equals "line carries its rate" "$(field '.items[0].gst_rate')" "5"
  INVOICE=$(field '.invoice.id')
fi

req GET "/stock/product/$RICE"
ON_HAND=$(printf '%s' "$BODY" | jq -r '[.batches[]?.quantity // 0] | add // 0')
equals "2 kg and a sack leave 73 kg" "$ON_HAND" "73.000"

# ---------------------------------------------------------------------------
step "a credit sale, then a payment against it"
# ---------------------------------------------------------------------------

req POST /customers '{"name":"Rahul Traders","phone":"9000000003","credit_limit":5000,"is_wholesale":false}'
CUSTOMER=$(field .id)
[ -n "$CUSTOMER" ] && ok "customer created" || bad "customer create" "${BODY:0:200}"

req POST /invoices "{\"terminal_id\":\"$TERMINAL\",\"customer_id\":\"$CUSTOMER\",\"payment_mode\":\"credit\",\"items\":[
  {\"product_id\":\"$RICE\",\"product_unit_id\":\"$LOOSE\",\"unit_qty\":10}]}"
if [ "$STATUS" != "201" ]; then bad "credit sale" "HTTP $STATUS ${BODY:0:300}"; else
  ok "credit sale raised"
  equals "credit sale total" "$(field '.invoice.total')" "472.50"
fi

req GET "/credit?customer_id=$CUSTOMER"
OUTSTANDING=$(printf '%s' "$BODY" | jq -r '[.entries[]? | (.amount - .paid_amount)] | add // 0')
equals "outstanding after the credit sale" "$OUTSTANDING" "472.5"

# Paying must reduce the balance. The implementation this replaced inserted the
# payment as a fresh unpaid entry, so settling a bill *raised* the debt.
expect "payment recorded" POST /credit/payment 201 "{\"customer_id\":\"$CUSTOMER\",\"amount\":472.50}"
req GET "/credit?customer_id=$CUSTOMER"
OUTSTANDING=$(printf '%s' "$BODY" | jq -r '[.entries[]? | (.amount - .paid_amount)] | add // 0')
equals "outstanding after paying in full" "$OUTSTANDING" "0"

# ---------------------------------------------------------------------------
step "void a bill and check the shelf and the books both move back"
# ---------------------------------------------------------------------------

if [ -n "${INVOICE:-}" ]; then
  expect "void accepted"      POST "/invoices/$INVOICE/void" 200 '{}'
  expect "a second void is refused" POST "/invoices/$INVOICE/void" 409 '{}'
  req GET "/stock/product/$RICE"
  ON_HAND=$(printf '%s' "$BODY" | jq -r '[.batches[]?.quantity // 0] | add // 0')
  equals "voiding restores 27 kg" "$ON_HAND" "90.000"
  # And the value with it. A void must not change what the shop's inventory is
  # worth: 90 of the original 100 kg remain, at Rs.37.60, so Rs.3,384.00.
  VALUE=$(printf '%s' "$BODY" | jq -r '[.batches[]?.total_cost // 0] | add // 0')
  equals "and the value that went out with them" "$VALUE" "3384.00"
fi

# ---------------------------------------------------------------------------
step "the reports agree with the day"
# ---------------------------------------------------------------------------

# One cash bill, voided. One credit bill of 472.50, still a sale. Revenue must
# exclude the void: counting it was an eight-aggregate defect, GSTR-1 included.
req GET /analytics/dashboard
equals "dashboard revenue excludes the void" "$(field '.revenue')" "472.50"
equals "dashboard counts one invoice"        "$(field '.invoice_count')" "1"

req GET /payments/settlement
equals "settlement agrees with the dashboard" "$(field '.total_sales')" "472.50"

MONTH=$(TZ=Asia/Kolkata date +%Y-%m)
req GET "/invoices/gstr1?month=$MONTH"
GSTR1=$(printf '%s' "$BODY" | jq -r '(.invoices | length) // 0')
equals "gstr-1 files the one live invoice" "$GSTR1" "1"

req GET "/reports/profit-loss?start_date=$TODAY&end_date=$TODAY"
if [ "$STATUS" = "200" ]; then ok "profit and loss renders"; else bad "profit and loss" "HTTP $STATUS ${BODY:0:200}"; fi

# ---------------------------------------------------------------------------
step "the things a caller must not be able to do"
# ---------------------------------------------------------------------------

SAVED="$TOKEN"; TOKEN=""
expect "no token is refused" GET /products 401
TOKEN="$SAVED"

SAVED="$STORE"; STORE="00000000-0000-0000-0000-000000000000"
req GET /products
case "$STATUS" in
  403|404) ok "another store's data is refused (HTTP $STATUS)" ;;
  *)       bad "unscoped store_id should be refused" "HTTP $STATUS" ;;
esac
STORE="$SAVED"

# An injected subquery in a clamped integer parameter must be neutralised, and
# an absurd value clamped rather than rejected.
req GET "/invoices?limit=(SELECT%201)"
[ "$STATUS" = "200" ] && ok "an injected limit is neutralised" || bad "limit injection" "HTTP $STATUS ${BODY:0:200}"
req GET "/invoices?limit=99999999"
[ "$STATUS" = "200" ] && ok "an absurd limit is clamped" || bad "limit clamping" "HTTP $STATUS"

# Selling more than is on the shelf must fail whole, not partly.
req POST /invoices "{\"terminal_id\":\"$TERMINAL\",\"payment_mode\":\"cash\",\"items\":[
  {\"product_id\":\"$RICE\",\"product_unit_id\":\"$LOOSE\",\"unit_qty\":100000}]}"
[ "$STATUS" != "201" ] && ok "overselling is refused (HTTP $STATUS)" || bad "overselling was allowed" "HTTP $STATUS"
req GET "/stock/product/$RICE"
ON_HAND=$(printf '%s' "$BODY" | jq -r '[.batches[]?.quantity // 0] | add // 0')
equals "the refused sale moved no stock" "$ON_HAND" "90.000"

[ "$FAIL" -eq 0 ]
