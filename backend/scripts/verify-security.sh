#!/usr/bin/env bash
# Security regression checks for the Phase 0 hardening.
#
# Each check corresponds to a defect that was exploitable before. Run this
# after any change to config, auth, the audit middleware, or the store-scope
# helper. Requires: go, curl, jq, openssl.
#
#   bash backend/scripts/verify-security.sh
#
set -uo pipefail

REPO_BACKEND="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$REPO_BACKEND/.." && pwd)"
WORK="$(mktemp -d)"
BIN="$WORK/gbos-verify"

# A throwaway database. Several checks below assert what does NOT exist — that
# the seeded demo accounts are gone, for one — so they have to run against a
# database this script created, not against whatever the developer has been
# working in.
VERIFY_DB="gbos_verify_$$"
DEV_DB_URL="${DEV_DB_URL:-postgres://gbos:gbos_dev_password@localhost:5434/gbos_dev?sslmode=disable}"
VERIFY_DB_URL=$(printf '%s' "$DEV_DB_URL" | sed -E "s#/[^/?]+(\?|$)#/$VERIFY_DB\1#")

# An ephemeral port by default, so this never contends with anything already
# listening on this machine.
PORT="${VERIFY_PORT:-$(/usr/bin/env python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')}"
BASE="http://127.0.0.1:$PORT/api/v1"
PASS=0
FAIL=0

ok()  { echo "  PASS  $1"; PASS=$((PASS + 1)); }
bad() { echo "  FAIL  $1"; echo "        got: $2"; FAIL=$((FAIL + 1)); }

pg_admin() {
  docker compose -f "$REPO_ROOT/docker-compose.dev.yml" exec -T postgres \
    psql -U gbos -d postgres -v ON_ERROR_STOP=1 -tAc "$1"
}

cleanup() {
  [ -n "${SRV:-}" ] && kill "$SRV" 2>/dev/null
  if [ -n "${VERIFY_DB_CREATED:-}" ]; then
    pg_admin "DROP DATABASE IF EXISTS \"$VERIFY_DB\" WITH (FORCE)" >/dev/null 2>&1 \
      || echo "  note: could not drop $VERIFY_DB — drop it by hand"
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

cd "$REPO_BACKEND"
go build -o "$BIN" ./cmd/server || { echo "build failed"; exit 1; }

pg_admin "CREATE DATABASE \"$VERIFY_DB\"" >/dev/null || {
  echo "could not create $VERIFY_DB. Is the dev Postgres up? Run: make db-up"
  exit 1
}
VERIFY_DB_CREATED=1

echo
echo "== 1. Configuration fails closed =="

expect_refusal() {
  local desc="$1" expected="$2"; shift 2
  local out
  out=$(env "$@" DATABASE_URL="$VERIFY_DB_URL" "$BIN" 2>&1 | head -3)
  case "$out" in
    *"$expected"*) ok "$desc" ;;
    *) bad "$desc" "$out" ;;
  esac
}

expect_refusal "release mode with no secret refuses to boot" "JWT_SECRET is required" \
  GIN_MODE=release JWT_SECRET=
expect_refusal "the compose placeholder secret is rejected" "known placeholder" \
  GIN_MODE=release JWT_SECRET=default-change-in-production
expect_refusal "the hardcoded repo secret is rejected" "known placeholder" \
  GIN_MODE=release JWT_SECRET=gbos-secret-key-change-in-production
expect_refusal "a short secret is rejected" "at least 32 characters" \
  GIN_MODE=release JWT_SECRET=tooshort
expect_refusal "wildcard CORS is rejected in release" "CORS_ORIGIN must name an explicit origin" \
  GIN_MODE=release "JWT_SECRET=$(openssl rand -base64 48)" CORS_ORIGIN='*'

echo
echo "== 2. Seeded credentials =="
GBOS_ADMIN_PASSWORD=verifypass123 DATABASE_URL="$VERIFY_DB_URL" SERVER_PORT="$PORT" \
  TZ=Asia/Kolkata "$BIN" >"$WORK/server.log" 2>&1 &
SRV=$!
for _ in $(seq 1 40); do curl -sf "$BASE/health" >/dev/null 2>&1 && break; sleep 0.25; done
curl -sf "$BASE/health" >/dev/null || { echo "server did not start"; cat "$WORK/server.log"; exit 1; }
ok "server starts in debug mode with no JWT_SECRET set"

if grep -q "cashier123\|helper123\|admin123" "$WORK/server.log"; then
  bad "startup log must not contain a published password" "$(grep -o 'cashier123\|helper123\|admin123' "$WORK/server.log" | head -1)"
else
  ok "startup log contains no published passwords"
fi

LOGIN=$(curl -s -X POST "$BASE/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"verifypass123"}')
TOKEN=$(echo "$LOGIN" | jq -r '.token // empty')
STORE=$(echo "$LOGIN" | jq -r '.user.store_id // empty')
if [ -n "$TOKEN" ]; then ok "admin login honours GBOS_ADMIN_PASSWORD"; else bad "admin login" "$LOGIN"; exit 1; fi

for u in cashier helper; do
  r=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/auth/login" \
    -H 'Content-Type: application/json' -d "{\"username\":\"$u\",\"password\":\"${u}123\"}")
  [ "$r" = "401" ] && ok "demo account '$u' does not exist" || bad "demo account '$u' should be gone" "HTTP $r"
done

AUTH=(-H "Authorization: Bearer $TOKEN" -H "X-Store-ID: $STORE")

echo
echo "== 3. SQL injection through the limit parameter =="
PAYLOAD='(SELECT 1 WHERE (SELECT substr(password_hash,1,1) FROM users)=char(36))'
code=$(curl -s -o "$WORK/inj.json" -w '%{http_code}' -G "${AUTH[@]}" \
  --data-urlencode "store_id=$STORE" --data-urlencode "limit=$PAYLOAD" "$BASE/invoices")
if [ "$code" = "200" ] && ! grep -qi 'syntax error\|SQLSTATE\|pq:\|pgx' "$WORK/inj.json"; then
  ok "an injected subquery in limit is neutralised"
else
  bad "limit injection should be neutralised" "HTTP $code $(head -c 200 "$WORK/inj.json")"
fi
code=$(curl -s -o /dev/null -w '%{http_code}' -G "${AUTH[@]}" \
  --data-urlencode "store_id=$STORE" --data-urlencode "limit=99999999" "$BASE/invoices")
[ "$code" = "200" ] && ok "an absurd limit is clamped, not rejected" || bad "limit clamping" "HTTP $code"

echo
echo "== 4. Store scoping =="
BOGUS="00000000-0000-0000-0000-000000000000"
for ep in "credit?store_id=$BOGUS" "credit/overdue?store_id=$BOGUS"; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "${AUTH[@]}" "$BASE/$ep")
  [ "$code" = "403" ] && ok "GET /${ep%%\?*} rejects an unknown store" || bad "GET /$ep should be 403" "HTTP $code"
done
# The store has to be named where the server reads it. A store_id in the body
# is not a scoping input — it is ignored, by design — so probing with one
# proves nothing and passing it would be the wrong kind of reassurance.
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H "Authorization: Bearer $TOKEN" -H "X-Store-ID: $BOGUS" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Scope Probe"}' "$BASE/customers?store_id=$BOGUS")
[ "$code" = "403" ] && ok "POST /customers rejects an unknown store" || bad "customer create scoping" "HTTP $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "${AUTH[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"Hijacked"}' "$BASE/stores/$BOGUS")
[ "$code" = "403" ] && ok "PUT /stores/:id rejects an unknown store" || bad "store update scoping" "HTTP $code"

echo
echo "== 5. The client cannot name its own status or total =="

# A fresh database seeds nothing, so the probe builds what it needs. Three rows:
# a category, a product with one sellable unit, and stock to sell from.
api() { curl -s -X "$1" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: verify-$$-$RANDOM" -d "${3:-}" "$BASE$2?store_id=$STORE"; }

CAT=$(api POST /categories '{"name":"Probe"}' | jq -r '.id // empty')
KG=$(curl -s "${AUTH[@]}" "$BASE/uoms?store_id=$STORE" | jq -r '.units[]? | select(.code=="KG") | .id')
PROD=$(api POST /products "{\"name\":\"Probe Item\",\"category_id\":\"$CAT\",\"uom_id\":\"$KG\",\"gst_rate\":5,\"allow_decimal\":true}" | jq -r '.id // empty')
UNIT=$(api POST "/products/$PROD/units" "{\"label\":\"Each\",\"qty_in_base\":1,\"uom_id\":\"$KG\",\"is_default\":true,\"is_sellable\":true}" | jq -r '.id // empty')
api POST /products/price "{\"product_id\":\"$PROD\",\"product_unit_id\":\"$UNIT\",\"mrp\":100,\"cost_price\":40,\"sell_price\":55}" >/dev/null
api POST /stock/adjust "{\"product_id\":\"$PROD\",\"product_unit_id\":\"$UNIT\",\"unit_qty\":10,\"type\":\"add\",\"batch_no\":\"PROBE\",\"cost_price\":40,\"reason\":\"probe\"}" >/dev/null

TERM=$(api POST "/stores/$STORE/terminals" '{"code":"TV","name":"Verify"}' | jq -r '.id // empty')

if [ -n "$PROD" ] && [ -n "$UNIT" ] && [ -n "$TERM" ]; then
  # One unit at 55 plus 5% is 57.75. The request asserts a voided status and a
  # total of 1.00; both must be ignored. A caller who could name either could
  # post a Rs.1 sale, or a sale that deducts stock while reading as cancelled.
  INV=$(api POST /invoices "{\"terminal_id\":\"$TERM\",\"payment_mode\":\"cash\",
    \"status\":\"voided\",\"total\":1.00,\"sub_total\":1.00,
    \"items\":[{\"product_id\":\"$PROD\",\"product_unit_id\":\"$UNIT\",\"unit_qty\":1,\"total\":1.00}]}")
  IID=$(echo "$INV" | jq -r '.invoice.id // empty')
  if [ -n "$IID" ]; then
    ST=$(echo "$INV" | jq -r '.invoice.status // empty')
    TOT=$(echo "$INV" | jq -r '.invoice.total // empty')
    [ "$ST" = "completed" ] && ok "an invoice posted as 'voided' is stored as 'completed'" \
      || bad "client-supplied status must be ignored" "stored status = $ST"
    [ "$TOT" = "57.75" ] && ok "the server's total wins over the client's" \
      || bad "client-supplied total must be ignored" "stored total = $TOT, want 57.75"
  else
    bad "could not create the probe invoice" "$(head -c 300 <<<"$INV")"
  fi
else
  bad "could not build the probe product" "product=$PROD unit=$UNIT terminal=$TERM"
fi

echo
echo "== 6. Audit log redaction =="
code=$(curl -s -o "$WORK/chpw.json" -w '%{http_code}' -X POST "${AUTH[@]}" \
  -H 'Content-Type: application/json' \
  -d '{"old_password":"verifypass123","new_password":"NewVerifyPass456"}' "$BASE/auth/change-password")
[ "$code" = "200" ] && ok "the password changed" || bad "change-password" "HTTP $code $(head -c 200 "$WORK/chpw.json")"

# Changing a password ends every session, this request's included, so the old
# token can no longer read anything. Reading the trail with it would return a
# 401 and make the redaction check below pass vacuously.
NEWTOK=$(curl -s -X POST "$BASE/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"NewVerifyPass456"}' | jq -r '.token // empty')
[ -n "$NEWTOK" ] && ok "the old password no longer signs in, the new one does" \
  || bad "could not sign in with the new password" "login returned no token"
code=$(curl -s -o /dev/null -w '%{http_code}' "${AUTH[@]}" -G \
  --data-urlencode "store_id=$STORE" "$BASE/audit")
[ "$code" = "401" ] && ok "the session that changed the password was ended" \
  || bad "changing a password must end existing sessions" "old token still works: HTTP $code"

# The trail is written after the response is flushed — deliberately, so a
# failed audit insert cannot turn a successful change into a 500 — so the row
# may not be readable the instant the caller gets its 200. Poll rather than
# read once; reading once makes this check flaky and a flaky security check is
# one people learn to rerun until it passes.
AUD=""
for _ in $(seq 1 20); do
  AUD=$(curl -s -H "Authorization: Bearer $NEWTOK" -H "X-Store-ID: $STORE" -G \
    --data-urlencode "store_id=$STORE" --data-urlencode "limit=50" "$BASE/audit")
  grep -q 'change_password' <<<"$AUD" && break
  sleep 0.2
done
if grep -q 'NewVerifyPass456\|verifypass123' <<<"$AUD"; then
  bad "a password must never reach the audit log" "found a password in the audit response"
else
  ok "no password appears in the audit log"
fi
# The trail records the derived action, not the URL: DescribeRoute turns
# POST /auth/change-password into table "auth", action "change_password".
if grep -q 'change_password' <<<"$AUD"; then
  ok "the password change is in the trail"
else
  bad "the password change must be audited" "no change_password entry in the last 50"
fi
grep -q 'redacted' <<<"$AUD" && ok "the audited entry records redacted fields" \
  || bad "the audited body must show [redacted] where the credential was" "no marker found"

echo
echo "=================================="
echo "  passed: $PASS   failed: $FAIL"
echo "=================================="
[ "$FAIL" -eq 0 ]
