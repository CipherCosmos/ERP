#!/usr/bin/env bash
# Quick API checks. Start backend first: `make backend` or `cd backend && go run ./cmd/server`
set -euo pipefail
BASE="${GBOS_API:-http://127.0.0.1:8080/api/v1}"

echo "== Health $BASE/health"
curl -sf "$BASE/health" | head -c 300
echo ""

echo "== Login (admin)"
LOGIN=$(curl -sf "$BASE/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}')
TOKEN=$(echo "$LOGIN" | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")
STORE=$(echo "$LOGIN" | python3 -c "import sys,json; print(json.load(sys.stdin)['user']['store_id'])")
echo "store_id=$STORE"

echo "== Products count (expect ≥3 on fresh seed)"
N=$(curl -sf "$BASE/products?store_id=$STORE" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Store-ID: $STORE" \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d) if isinstance(d,list) else -1)")
echo "products=$N"
test "${N:-0}" -ge 1

echo "== Barcode lookup"
curl -sf "$BASE/barcodes/lookup/8901001100001?store_id=$STORE" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Store-ID: $STORE" | head -c 200
echo ""

echo "OK smoke tests passed."
