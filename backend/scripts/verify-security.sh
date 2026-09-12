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
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${AUTH[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"Scope Probe\",\"store_id\":\"$BOGUS\"}" "$BASE/customers")
[ "$code" = "403" ] && ok "POST /customers rejects an unknown store" || bad "customer create scoping" "HTTP $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "${AUTH[@]}" \
  -H 'Content-Type: application/json' -d '{"name":"Hijacked"}' "$BASE/stores/$BOGUS")
[ "$code" = "403" ] && ok "PUT /stores/:id rejects an unknown store" || bad "store update scoping" "HTTP $code"

echo
echo "== 5. Invoice status is not client-controlled =="
PROD=$(curl -s "${AUTH[@]}" -G --data-urlencode "store_id=$STORE" "$BASE/products" \
  | jq -r 'if type=="array" then .[0].id elif has("products") then .products[0].id else empty end // empty')
if [ -n "$PROD" ]; then
  INV=$(curl -s -X POST "${AUTH[@]}" -H 'Content-Type: application/json' -d "{
    \"store_id\":\"$STORE\",\"invoice_date\":\"$(date +%F)\",\"sub_total\":55,\"cgst\":1.375,
    \"sgst\":1.375,\"igst\":0,\"discount\":0,\"round_off\":0,\"total\":57.75,
    \"status\":\"voided\",\"payment_mode\":\"cash\",
    \"items\":[{\"product_id\":\"$PROD\",\"quantity\":1,\"rate\":55,\"total\":55}]}" "$BASE/invoices")
  IID=$(echo "$INV" | jq -r '.id // empty')
  if [ -n "$IID" ]; then
    ST=$(curl -s "${AUTH[@]}" "$BASE/invoices/$IID" | jq -r '.invoice.status // .status // empty')
    [ "$ST" = "completed" ] && ok "an invoice posted as 'voided' is stored as 'completed'" \
      || bad "client-supplied status must be ignored" "stored status = $ST"
  else
    bad "could not create the probe invoice" "$(head -c 200 <<<"$INV")"
  fi
else
  bad "could not find a seeded product" "products response empty"
fi

echo
echo "== 6. Audit log redaction =="
curl -s -o /dev/null -X POST "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d '{"old_password":"verifypass123","new_password":"NewVerifyPass456"}' "$BASE/auth/change-password"
AUD=$(curl -s "${AUTH[@]}" -G --data-urlencode "store_id=$STORE" --data-urlencode "limit=50" "$BASE/audit")
if grep -q 'NewVerifyPass456\|verifypass123' <<<"$AUD"; then
  bad "a password must never reach the audit log" "found a password in the audit response"
else
  ok "no password appears in the audit log"
fi
grep -q 'redacted' <<<"$AUD" && ok "the audited entry records redacted fields" \
  || echo "  NOTE  no '[redacted]' marker present — change-password may not have been audited"

echo
echo "=================================="
echo "  passed: $PASS   failed: $FAIL"
echo "=================================="
[ "$FAIL" -eq 0 ]
