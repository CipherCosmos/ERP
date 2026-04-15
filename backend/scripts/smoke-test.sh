#!/usr/bin/env bash
set -euo pipefail

###############################################################################
# GBOS Comprehensive E2E Smoke Test — ~350 tests
# Tests every API endpoint with data correctness verification
###############################################################################

BASE="http://localhost:8080/api/v1"
PASS=0; FAIL=0; SKIP=0
ERRORS=()

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

header() { echo -e "\n${BLUE}═══════════════════════════════════════${NC}"; echo -e "${BLUE}  $1${NC}"; echo -e "${BLUE}═══════════════════════════════════════${NC}"; }

assert_status() {
  local test_name="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo -e "  ${GREEN}PASS${NC} $test_name (HTTP $actual)"
    PASS=$((PASS+1))
  else
    echo -e "  ${RED}FAIL${NC} $test_name (expected $expected, got $actual)"
    echo -e "       ${RED}Body: ${BODY:0:300}${NC}"
    FAIL=$((FAIL+1))
    ERRORS+=("$test_name: expected=$expected got=$actual")
  fi
}

assert_contains() {
  local test_name="$1" body="$2" substring="$3"
  if echo "$body" | grep -q "$substring"; then
    echo -e "  ${GREEN}PASS${NC} $test_name (contains '$substring')"
    PASS=$((PASS+1))
  else
    echo -e "  ${RED}FAIL${NC} $test_name (missing '$substring')"
    echo -e "       ${RED}Body: ${body:0:300}${NC}"
    FAIL=$((FAIL+1))
    ERRORS+=("$test_name: missing '$substring'")
  fi
}

assert_not_contains() {
  local test_name="$1" body="$2" substring="$3"
  if ! echo "$body" | grep -q "$substring"; then
    echo -e "  ${GREEN}PASS${NC} $test_name (does not contain '$substring')"
    PASS=$((PASS+1))
  else
    echo -e "  ${RED}FAIL${NC} $test_name (unexpectedly contains '$substring')"
    FAIL=$((FAIL+1))
    ERRORS+=("$test_name: unexpectedly contains '$substring'")
  fi
}

# Float equality within tolerance 0.05
assert_eq() {
  local test_name="$1" actual="$2" expected="$3"
  local diff
  diff=$(python3 -c "print(abs(float('${actual}') - float('${expected}')) < 0.05)" 2>/dev/null || echo "False")
  if [ "$diff" = "True" ]; then
    echo -e "  ${GREEN}PASS${NC} $test_name ($actual ≈ $expected)"
    PASS=$((PASS+1))
  else
    echo -e "  ${RED}FAIL${NC} $test_name ($actual != $expected)"
    FAIL=$((FAIL+1))
    ERRORS+=("$test_name: $actual != $expected")
  fi
}

# Float greater-than
assert_gt() {
  local test_name="$1" actual="$2" threshold="$3"
  local result
  result=$(python3 -c "print(float('${actual}') > float('${threshold}'))" 2>/dev/null || echo "False")
  if [ "$result" = "True" ]; then
    echo -e "  ${GREEN}PASS${NC} $test_name ($actual > $threshold)"
    PASS=$((PASS+1))
  else
    echo -e "  ${RED}FAIL${NC} $test_name ($actual not > $threshold)"
    FAIL=$((FAIL+1))
    ERRORS+=("$test_name: $actual not > $threshold")
  fi
}

jq_val() { echo "$1" | python3 -c "import sys,json; d=json.load(sys.stdin); print($2)" 2>/dev/null || echo "PARSE_ERROR"; }
jq_raw() { echo "$1" | python3 -c "import sys,json; d=json.load(sys.stdin); $2" 2>/dev/null; }

# Authenticated requests
auth_get()  { curl -s -w "\n%{http_code}" -H "Authorization: Bearer $TOKEN" -H "X-Store-ID: $STORE_ID" "$BASE$1" 2>/dev/null || echo -e "\n000"; }
auth_post() { curl -s -w "\n%{http_code}" -X POST -H "Authorization: Bearer $TOKEN" -H "X-Store-ID: $STORE_ID" -H "Content-Type: application/json" -d "$2" "$BASE$1" 2>/dev/null || echo -e "\n000"; }
auth_put()  { curl -s -w "\n%{http_code}" -X PUT  -H "Authorization: Bearer $TOKEN" -H "X-Store-ID: $STORE_ID" -H "Content-Type: application/json" -d "$2" "$BASE$1" 2>/dev/null || echo -e "\n000"; }

# Role-scoped requests
role_get()  { curl -s -w "\n%{http_code}" -H "Authorization: Bearer $1" -H "X-Store-ID: $2" "$BASE$3" 2>/dev/null || echo -e "\n000"; }
role_post() { curl -s -w "\n%{http_code}" -X POST -H "Authorization: Bearer $1" -H "X-Store-ID: $2" -H "Content-Type: application/json" -d "$4" "$BASE$3" 2>/dev/null || echo -e "\n000"; }

split_response() {
  local resp="$1"
  BODY=$(echo "$resp" | sed '$d')
  STATUS=$(echo "$resp" | tail -1)
}

# Get total stock for a product at a store
get_stock_qty() {
  local pid="$1" sid="$2"
  local r
  r=$(auth_get "/stock/product/$pid?store_id=$sid")
  split_response "$r"
  echo "$BODY" | python3 -c "import sys,json; d=json.load(sys.stdin); print(sum(b.get('quantity',0) for b in (d if isinstance(d,list) else [])))" 2>/dev/null || echo "0"
}

TODAY=$(date +%Y-%m-%d)
TOMORROW=$(date -v+1d +%Y-%m-%d 2>/dev/null || date -d '+1 day' +%Y-%m-%d)
FIVE_DAYS=$(date -v+5d +%Y-%m-%d 2>/dev/null || date -d '+5 days' +%Y-%m-%d)
THREE_DAYS=$(date -v+3d +%Y-%m-%d 2>/dev/null || date -d '+3 days' +%Y-%m-%d)
YESTERDAY=$(date -v-1d +%Y-%m-%d 2>/dev/null || date -d '-1 day' +%Y-%m-%d)
LAST_MONTH=$(date -v-30d +%Y-%m-%d 2>/dev/null || date -d '-30 days' +%Y-%m-%d)
MONTH=$(date +%Y-%m)

###############################################################################
header "PHASE 0: Setup — Fresh DB + Server"
###############################################################################

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BACKEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TEST_DB_DIR=$(mktemp -d)
TEST_DB="$TEST_DB_DIR/test-gbos.db"
SERVER_PID=""

cleanup() {
  echo -e "\n${YELLOW}Cleaning up...${NC}"
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null
  rm -rf "$TEST_DB_DIR"
  echo -e "\n${BLUE}═══════════════════════════════════════${NC}"
  echo -e "${BLUE}  TEST RESULTS${NC}"
  echo -e "${BLUE}═══════════════════════════════════════${NC}"
  echo -e "  ${GREEN}PASSED: $PASS${NC}"
  echo -e "  ${RED}FAILED: $FAIL${NC}"
  echo -e "  ${YELLOW}SKIPPED: $SKIP${NC}"
  echo -e "  TOTAL: $((PASS+FAIL+SKIP))"
  if [ ${#ERRORS[@]} -gt 0 ]; then
    echo -e "\n${RED}Failed tests:${NC}"
    for err in "${ERRORS[@]}"; do echo -e "  ${RED}• $err${NC}"; done
  fi
  echo ""
}
trap cleanup EXIT

# Kill any stale server on 8080
lsof -ti:8080 2>/dev/null | xargs kill -9 2>/dev/null || true
sleep 1

echo "Building server..."
cd "$BACKEND_DIR"
go build -o "$TEST_DB_DIR/server" ./cmd/server/

echo "Starting server with fresh DB at $TEST_DB..."
DATABASE_PATH="$TEST_DB" SERVER_PORT=8080 JWT_SECRET=test-secret "$TEST_DB_DIR/server" &
SERVER_PID=$!
sleep 2

# Health check
RESP=$(curl -s -w "\n%{http_code}" "$BASE/health" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Health check" "200" "$STATUS"

###############################################################################
header "PHASE 0b: Admin Login"
###############################################################################

RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Admin login" "200" "$STATUS"

TOKEN=$(jq_val "$BODY" "d.get('token','')")
STORE_ID=$(jq_val "$BODY" "d.get('store_id',d.get('user',{}).get('store_id',''))")

if [ "$TOKEN" = "PARSE_ERROR" ] || [ -z "$TOKEN" ]; then
  echo -e "${RED}FATAL: Could not extract token${NC}"
  echo "Response: $BODY"
  exit 1
fi
echo -e "  Token: ${TOKEN:0:20}..."
echo -e "  Store ID: $STORE_ID"

# Verify login response structure
assert_contains "Login: has user object" "$BODY" "username"
assert_contains "Login: has role" "$BODY" "role"

###############################################################################
header "PHASE 1: Foundation CRUD — Stores & UOMs"
###############################################################################

echo -e "\n${YELLOW}-- Stores --${NC}"

RESP=$(auth_get "/stores")
split_response "$RESP"
assert_status "Stores: list" "200" "$STATUS"
STORE_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Stores: has seed store" "$STORE_COUNT" "0"

RESP=$(auth_get "/stores/$STORE_ID")
split_response "$RESP"
assert_status "Stores: get by ID" "200" "$STATUS"
assert_contains "Stores: has name" "$BODY" "name"

RESP=$(auth_post "/stores" '{"name":"Test Warehouse","address":"456 WH Road","phone":"9876543210","gstin":"29AABCU9603R1ZP","location_type":"warehouse"}')
split_response "$RESP"
assert_status "Stores: create warehouse" "201" "$STATUS"
STORE2_ID=$(jq_val "$BODY" "d.get('id','')")
echo -e "  Store2 ID: $STORE2_ID"

RESP=$(auth_put "/stores/$STORE_ID" '{"name":"Main Retail Store","phone":"1111111111"}')
split_response "$RESP"
assert_status "Stores: update name" "200" "$STATUS"

# Verify update persisted
RESP=$(auth_get "/stores/$STORE_ID")
split_response "$RESP"
assert_status "Stores: verify update" "200" "$STATUS"
assert_contains "Stores: name updated" "$BODY" "Main Retail Store"

echo -e "\n${YELLOW}-- UOMs --${NC}"

RESP=$(auth_get "/uoms")
split_response "$RESP"
assert_status "UOMs: list" "200" "$STATUS"
UOM_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_contains "UOMs: has NOS" "$BODY" "NOS"
assert_contains "UOMs: has KG" "$BODY" "KG"

# Extract UOM IDs
UOM_NOS_ID=$(jq_val "$BODY" "[u['id'] for u in d if u.get('code')=='NOS'][0] if d else ''")
UOM_KG_ID=$(jq_val "$BODY" "[u['id'] for u in d if u.get('code')=='KG'][0] if d else ''")
UOM_L_ID=$(jq_val "$BODY" "[u['id'] for u in d if u.get('code')=='L'][0] if d else ''")

RESP=$(auth_post "/uoms" '{"code":"DZN","name":"Dozen"}')
split_response "$RESP"
assert_status "UOMs: create Dozen" "201" "$STATUS"
UOM_DZN_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/uoms" '{"code":"BOX","name":"Box","decimal_places":0}')
split_response "$RESP"
assert_status "UOMs: create Box" "201" "$STATUS"

# Verify UOM count increased
RESP=$(auth_get "/uoms")
split_response "$RESP"
NEW_UOM_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "UOMs: count increased" "$NEW_UOM_COUNT" "$UOM_COUNT"

###############################################################################
header "PHASE 2: Categories & Products"
###############################################################################

echo -e "\n${YELLOW}-- Categories --${NC}"

RESP=$(auth_get "/categories?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Categories: list" "200" "$STATUS"
CAT_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
CAT_GROCERY_ID=$(jq_val "$BODY" "[c['id'] for c in d if c.get('name')=='Groceries'][0] if d else ''")

RESP=$(auth_post "/categories" '{"name":"Dairy Products","name_hi":"डेयरी उत्पाद","hsn_code":"0402"}')
split_response "$RESP"
assert_status "Categories: create Dairy" "201" "$STATUS"
CAT_DAIRY_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/categories" '{"name":"Frozen Foods","hsn_code":"1602"}')
split_response "$RESP"
assert_status "Categories: create Frozen" "201" "$STATUS"
CAT_FROZEN_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/categories" '{"name":"Baby Care","hsn_code":"3304"}')
split_response "$RESP"
assert_status "Categories: create Baby Care" "201" "$STATUS"

# Verify category count increased
RESP=$(auth_get "/categories?store_id=$STORE_ID")
split_response "$RESP"
NEW_CAT_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Categories: count increased" "$NEW_CAT_COUNT" "$CAT_COUNT"

echo -e "\n${YELLOW}-- Products --${NC}"

RESP=$(auth_get "/products?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Products: list" "200" "$STATUS"

# Get seed product IDs
PROD_WHEAT_ID=$(jq_val "$BODY" "[p['id'] for p in d if 'Wheat' in p.get('name','')][0] if d else ''")
PROD_SUGAR_ID=$(jq_val "$BODY" "[p['id'] for p in d if 'Sugar' in p.get('name','')][0] if d else ''")
PROD_OIL_ID=$(jq_val "$BODY" "[p['id'] for p in d if 'Oil' in p.get('name','')][0] if d else ''")

# Create 5 new products with different GST rates and UOMs
RESP=$(auth_post "/products" "{\"name\":\"Milk 500ml\",\"name_hi\":\"दूध\",\"category_id\":\"$CAT_DAIRY_ID\",\"uom_id\":\"$UOM_L_ID\",\"gst_rate\":5,\"barcode\":\"MILK500\",\"base_qty\":1,\"min_stock\":20}")
split_response "$RESP"
assert_status "Products: create Milk (5% GST)" "201" "$STATUS"
PROD_MILK_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/products" "{\"name\":\"Butter 200g\",\"category_id\":\"$CAT_DAIRY_ID\",\"uom_id\":\"$UOM_NOS_ID\",\"gst_rate\":12,\"base_qty\":1,\"min_stock\":10}")
split_response "$RESP"
assert_status "Products: create Butter (12% GST)" "201" "$STATUS"
PROD_BUTTER_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/products" "{\"name\":\"Ice Cream 1L\",\"category_id\":\"$CAT_FROZEN_ID\",\"uom_id\":\"$UOM_L_ID\",\"gst_rate\":18,\"base_qty\":1,\"min_stock\":5}")
split_response "$RESP"
assert_status "Products: create Ice Cream (18% GST)" "201" "$STATUS"
PROD_ICE_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/products" "{\"name\":\"Rice 5kg\",\"category_id\":\"$CAT_GROCERY_ID\",\"uom_id\":\"$UOM_KG_ID\",\"gst_rate\":5,\"base_qty\":5,\"min_stock\":10}")
split_response "$RESP"
assert_status "Products: create Rice (5% GST, KG)" "201" "$STATUS"
PROD_RICE_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/products" "{\"name\":\"Tea Leaves 250g\",\"category_id\":\"$CAT_GROCERY_ID\",\"uom_id\":\"$UOM_NOS_ID\",\"gst_rate\":5,\"base_qty\":1,\"min_stock\":15}")
split_response "$RESP"
assert_status "Products: create Tea (5% GST)" "201" "$STATUS"
PROD_TEA_ID=$(jq_val "$BODY" "d.get('id','')")

# Product GET — verify fields
RESP=$(auth_get "/products/$PROD_MILK_ID?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Products: get Milk" "200" "$STATUS"
assert_contains "Products: Milk name correct" "$BODY" "Milk 500ml"
MILK_GST=$(jq_val "$BODY" "d.get('gst_rate',0)")
assert_eq "Products: Milk GST = 5" "$MILK_GST" "5"

# Product get nonexistent
RESP=$(auth_get "/products/nonexistent-id-12345?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Products: get nonexistent → 404" "404" "$STATUS"

# Product update
RESP=$(auth_put "/products/$PROD_MILK_ID" '{"min_stock":25}')
split_response "$RESP"
assert_status "Products: update min_stock" "200" "$STATUS"

# Verify update persisted
RESP=$(auth_get "/products/$PROD_MILK_ID?store_id=$STORE_ID")
split_response "$RESP"
MILK_MINSTOCK=$(jq_val "$BODY" "d.get('min_stock',0)")
assert_eq "Products: min_stock updated to 25" "$MILK_MINSTOCK" "25"

echo -e "\n${YELLOW}-- Product Prices --${NC}"

# Set prices for all new products
for pid_name in "$PROD_MILK_ID:30:25:28:26" "$PROD_BUTTER_ID:150:120:140:130" "$PROD_ICE_ID:250:200:230:210" "$PROD_RICE_ID:400:300:350:320" "$PROD_TEA_ID:200:150:180:160"; do
  IFS=: read -r pid mrp cost sell wsell <<< "$pid_name"
  RESP=$(auth_post "/products/price" "{\"product_id\":\"$pid\",\"store_id\":\"$STORE_ID\",\"mrp\":$mrp,\"cost_price\":$cost,\"sell_price\":$sell,\"wholesale_sell_price\":$wsell}")
  split_response "$RESP"
  assert_status "Prices: set for ${pid:0:8}..." "200" "$STATUS"
done

# Verify prices via product GET
RESP=$(auth_get "/products/$PROD_MILK_ID?store_id=$STORE_ID")
split_response "$RESP"
MILK_SELL=$(jq_val "$BODY" "d.get('sell_price',0)")
assert_eq "Prices: Milk sell_price = 28" "$MILK_SELL" "28"
MILK_MRP=$(jq_val "$BODY" "d.get('mrp',0)")
assert_eq "Prices: Milk MRP = 30" "$MILK_MRP" "30"

echo -e "\n${YELLOW}-- Packing Units --${NC}"

RESP=$(auth_post "/products/$PROD_MILK_ID/packing-units" '{"label":"Box of 12","qty_in_parent":12,"barcode":"MILKBOX12"}')
split_response "$RESP"
assert_status "PackingUnits: create Box of 12" "201" "$STATUS"

RESP=$(auth_post "/products/$PROD_MILK_ID/packing-units" '{"label":"Crate of 24","qty_in_parent":24,"barcode":"MILKCRATE24"}')
split_response "$RESP"
assert_status "PackingUnits: create Crate of 24" "201" "$STATUS"

RESP=$(auth_get "/products/$PROD_MILK_ID/packing-units")
split_response "$RESP"
assert_status "PackingUnits: list" "200" "$STATUS"
PU_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_eq "PackingUnits: count = 2" "$PU_COUNT" "2"

echo -e "\n${YELLOW}-- Barcodes --${NC}"

RESP=$(auth_post "/barcodes/generate" '{"product_id":"'"$PROD_BUTTER_ID"'","prefix":"BUT"}')
split_response "$RESP"
assert_status "Barcodes: generate" "200" "$STATUS"
assert_contains "Barcodes: has barcode" "$BODY" "barcode"

# Lookup product barcode
RESP=$(auth_get "/barcodes/lookup/MILK500?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Barcodes: lookup product barcode" "200" "$STATUS"
BARCODE_PID=$(jq_val "$BODY" "d.get('id','')")
assert_contains "Barcodes: matched product" "$BARCODE_PID" "$PROD_MILK_ID"

# Lookup packing unit barcode
RESP=$(auth_get "/barcodes/lookup/MILKBOX12?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Barcodes: lookup packing unit barcode" "200" "$STATUS"
assert_contains "Barcodes: has packing_qty" "$BODY" "packing_qty"

###############################################################################
header "PHASE 3: Customers & Vendors"
###############################################################################

echo -e "\n${YELLOW}-- Customers --${NC}"

RESP=$(auth_get "/customers?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Customers: list" "200" "$STATUS"

# Create 3 customers
RESP=$(auth_post "/customers" "{\"name\":\"Rahul Sharma\",\"phone\":\"9001001001\",\"address\":\"12 MG Road\",\"gstin\":\"29AABCU9603R1ZP\",\"credit_limit\":5000,\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Customers: create regular" "201" "$STATUS"
CUST_REGULAR_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/customers" "{\"name\":\"Wholesale Mart\",\"phone\":\"9002002002\",\"is_wholesale\":true,\"credit_limit\":50000,\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Customers: create wholesale" "201" "$STATUS"
CUST_WHOLESALE_ID=$(jq_val "$BODY" "d.get('id','')")

# Different-state customer for IGST
RESP=$(auth_post "/customers" "{\"name\":\"Delhi Traders\",\"phone\":\"9003003003\",\"gstin\":\"07BBBBB1234B1ZB\",\"credit_limit\":10000,\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Customers: create diff-state (Delhi 07)" "201" "$STATUS"
CUST_INTERSTATE_ID=$(jq_val "$BODY" "d.get('id','')")

# Customer GET — verify fields
RESP=$(auth_get "/customers/$CUST_REGULAR_ID")
split_response "$RESP"
assert_status "Customers: get regular" "200" "$STATUS"
CUST_NAME=$(jq_val "$BODY" "d.get('customer',{}).get('name','')")
assert_contains "Customers: name correct" "$CUST_NAME" "Rahul"
CUST_OUTSTANDING=$(jq_val "$BODY" "d.get('outstanding',0)")
assert_eq "Customers: outstanding = 0" "$CUST_OUTSTANDING" "0"

# Customer Update
RESP=$(auth_put "/customers/$CUST_REGULAR_ID" '{"phone":"9999999999"}')
split_response "$RESP"
assert_status "Customers: update phone" "200" "$STATUS"

RESP=$(auth_get "/customers/$CUST_REGULAR_ID")
split_response "$RESP"
CUST_PHONE=$(jq_val "$BODY" "d.get('customer',{}).get('phone','')")
assert_contains "Customers: phone updated" "$CUST_PHONE" "9999999999"

echo -e "\n${YELLOW}-- Vendors --${NC}"

RESP=$(auth_get "/vendors")
split_response "$RESP"
assert_status "Vendors: list" "200" "$STATUS"

# Get store GSTIN prefix for inter/intra-state determination
STORE_RESP=$(auth_get "/stores/$STORE_ID")
split_response "$STORE_RESP"
STORE_GSTIN=$(jq_val "$BODY" "d.get('gstin','') if isinstance(d,dict) else ''")
echo -e "  Store GSTIN: $STORE_GSTIN"

# Same-state vendor
RESP=$(auth_post "/vendors" '{"name":"Local Dairy Farm","phone":"8001001001","gstin":"29AADCB2230M1ZX","state":"Karnataka","credit_days":30,"credit_limit":100000}')
split_response "$RESP"
assert_status "Vendors: create same-state" "201" "$STATUS"
VENDOR_LOCAL_ID=$(jq_val "$BODY" "d.get('id','')")

# Different-state vendor (prefix 07 = Delhi)
RESP=$(auth_post "/vendors" '{"name":"Delhi Spice House","phone":"8002002002","gstin":"07AABCU1234F1Z5","state":"Delhi","credit_days":15}')
split_response "$RESP"
assert_status "Vendors: create diff-state" "201" "$STATUS"
VENDOR_INTERSTATE_ID=$(jq_val "$BODY" "d.get('id','')")

# Vendor GET
RESP=$(auth_get "/vendors/$VENDOR_LOCAL_ID")
split_response "$RESP"
assert_status "Vendors: get local" "200" "$STATUS"
VENDOR_PURCHASE=$(jq_val "$BODY" "d.get('total_purchase',0)")
assert_eq "Vendors: total_purchase = 0" "$VENDOR_PURCHASE" "0"

# Vendor Update
RESP=$(auth_put "/vendors/$VENDOR_LOCAL_ID" '{"credit_days":45}')
split_response "$RESP"
assert_status "Vendors: update credit_days" "200" "$STATUS"

###############################################################################
header "PHASE 4: Stock Management"
###############################################################################

echo -e "\n${YELLOW}-- Sell Without Stock (MUST FAIL) --${NC}"

RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"$TODAY\",\"sub_total\":28,\"total\":29.40,\"cgst\":0.70,\"sgst\":0.70,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
assert_status "Sell without stock → 400" "400" "$STATUS"
assert_contains "Sell without stock: insufficient msg" "$BODY" "insufficient"

echo -e "\n${YELLOW}-- Stock Adjust: Add --${NC}"

# Milk: 2 batches
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"MILK-A\",\"quantity\":100,\"type\":\"add\",\"cost_price\":25,\"expiry_date\":\"$FIVE_DAYS\"}")
split_response "$RESP"
assert_status "Stock: add Milk batch A (100)" "200" "$STATUS"
MILK_QTY=$(jq_val "$BODY" "d.get('new_quantity',0)")
assert_eq "Stock: Milk-A qty = 100" "$MILK_QTY" "100"

RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"MILK-B\",\"quantity\":50,\"type\":\"add\",\"cost_price\":28,\"expiry_date\":\"$TOMORROW\"}")
split_response "$RESP"
assert_status "Stock: add Milk batch B (50)" "200" "$STATUS"

# Butter: 80
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_BUTTER_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"BUTTER-A\",\"quantity\":80,\"type\":\"add\",\"cost_price\":120,\"expiry_date\":\"$FIVE_DAYS\"}")
split_response "$RESP"
assert_status "Stock: add Butter (80)" "200" "$STATUS"

# Ice Cream: 40
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_ICE_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"ICE-A\",\"quantity\":40,\"type\":\"add\",\"cost_price\":200}")
split_response "$RESP"
assert_status "Stock: add Ice Cream (40)" "200" "$STATUS"

# Rice: 60
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_RICE_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"RICE-A\",\"quantity\":60,\"type\":\"add\",\"cost_price\":300}")
split_response "$RESP"
assert_status "Stock: add Rice (60)" "200" "$STATUS"

# Tea: 90
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_TEA_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"TEA-A\",\"quantity\":90,\"type\":\"add\",\"cost_price\":150}")
split_response "$RESP"
assert_status "Stock: add Tea (90)" "200" "$STATUS"

echo -e "\n${YELLOW}-- Stock Verification --${NC}"

# Verify stock list
RESP=$(auth_get "/stock?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Stock: list" "200" "$STATUS"
assert_contains "Stock: has Milk" "$BODY" "Milk"

# Verify Milk total stock = 150
MILK_TOTAL=$(get_stock_qty "$PROD_MILK_ID" "$STORE_ID")
assert_eq "Stock: Milk total = 150" "$MILK_TOTAL" "150"

# Verify Milk has 2 batches
RESP=$(auth_get "/stock/product/$PROD_MILK_ID?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Stock: get product Milk" "200" "$STATUS"
MILK_BATCHES=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_eq "Stock: Milk has 2 batches" "$MILK_BATCHES" "2"

# Verify Butter qty
BUTTER_TOTAL=$(get_stock_qty "$PROD_BUTTER_ID" "$STORE_ID")
assert_eq "Stock: Butter = 80" "$BUTTER_TOTAL" "80"

echo -e "\n${YELLOW}-- Low Stock --${NC}"

RESP=$(auth_get "/stock/low?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Stock: low stock" "200" "$STATUS"

echo -e "\n${YELLOW}-- Stock Adjust: Remove --${NC}"

RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_BUTTER_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"BUTTER-A\",\"quantity\":10,\"type\":\"remove\",\"reason\":\"damage\"}")
split_response "$RESP"
assert_status "Stock: remove 10 Butter" "200" "$STATUS"

BUTTER_AFTER=$(get_stock_qty "$PROD_BUTTER_ID" "$STORE_ID")
assert_eq "Stock: Butter after remove = 70" "$BUTTER_AFTER" "70"

echo -e "\n${YELLOW}-- Negative Quantity (MUST FAIL) --${NC}"

RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_BUTTER_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"BUTTER-A\",\"quantity\":-5,\"type\":\"add\"}")
split_response "$RESP"
assert_status "Stock: negative qty → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- Remove More Than Available (MUST FAIL) --${NC}"

RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_BUTTER_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"BUTTER-A\",\"quantity\":999,\"type\":\"remove\"}")
split_response "$RESP"
assert_status "Stock: remove 999 → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- Reserve & Release --${NC}"

RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"MILK-A\",\"quantity\":20,\"type\":\"reserve\"}")
split_response "$RESP"
assert_status "Stock: reserve 20 Milk" "200" "$STATUS"

# Reserve more than available
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"MILK-A\",\"quantity\":999,\"type\":\"reserve\"}")
split_response "$RESP"
assert_status "Stock: reserve too much → 400" "400" "$STATUS"

RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"MILK-A\",\"quantity\":10,\"type\":\"release\"}")
split_response "$RESP"
assert_status "Stock: release 10 Milk" "200" "$STATUS"

# Release more than reserved
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"MILK-A\",\"quantity\":999,\"type\":\"release\"}")
split_response "$RESP"
assert_status "Stock: release too much → 400" "400" "$STATUS"

# Release remaining
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"MILK-A\",\"quantity\":10,\"type\":\"release\"}")
split_response "$RESP"
assert_status "Stock: release remaining 10" "200" "$STATUS"

echo -e "\n${YELLOW}-- Stock Transfer --${NC}"

RESP=$(auth_post "/stock/transfer" "{\"product_id\":\"$PROD_MILK_ID\",\"from_store_id\":\"$STORE_ID\",\"to_store_id\":\"$STORE2_ID\",\"quantity\":15,\"batch_no\":\"MILK-A\"}")
split_response "$RESP"
assert_status "Stock: transfer 15 Milk to store2" "200" "$STATUS"

MILK_AFTER_XFER=$(get_stock_qty "$PROD_MILK_ID" "$STORE_ID")
assert_eq "Stock: Milk after transfer = 135" "$MILK_AFTER_XFER" "135"

# Transfer more than available
RESP=$(auth_post "/stock/transfer" "{\"product_id\":\"$PROD_MILK_ID\",\"from_store_id\":\"$STORE_ID\",\"to_store_id\":\"$STORE2_ID\",\"quantity\":9999,\"batch_no\":\"MILK-A\"}")
split_response "$RESP"
assert_status "Stock: transfer too much → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- Stock Adjustments Log --${NC}"

RESP=$(auth_get "/stock/adjustments?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Stock: adjustments log" "200" "$STATUS"
assert_contains "Stock: log has 'add'" "$BODY" "add"
assert_contains "Stock: log has 'remove'" "$BODY" "remove"

###############################################################################
header "PHASE 5: Purchase Flow"
###############################################################################

echo -e "\n${YELLOW}-- PO Create (Intra-State) --${NC}"

# PO with same-state vendor (GSTIN 29...) → expect CGST+SGST
RESP=$(auth_post "/purchases" "{\"store_id\":\"$STORE_ID\",\"vendor_id\":\"$VENDOR_LOCAL_ID\",\"order_date\":\"$TODAY\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":50,\"rate\":25},{\"product_id\":\"$PROD_BUTTER_ID\",\"quantity\":30,\"rate\":120},{\"product_id\":\"$PROD_TEA_ID\",\"quantity\":20,\"rate\":150}]}")
split_response "$RESP"
assert_status "PO: create intra-state" "201" "$STATUS"
PO1_ID=$(jq_val "$BODY" "d.get('id','')")
PO1_NO=$(jq_val "$BODY" "d.get('po_no','')")
assert_contains "PO: has po_no" "$PO1_NO" "PO-"

echo -e "\n${YELLOW}-- PO Create (Inter-State) --${NC}"

RESP=$(auth_post "/purchases" "{\"store_id\":\"$STORE_ID\",\"vendor_id\":\"$VENDOR_INTERSTATE_ID\",\"order_date\":\"$TODAY\",\"items\":[{\"product_id\":\"$PROD_RICE_ID\",\"quantity\":100,\"rate\":300}]}")
split_response "$RESP"
assert_status "PO: create inter-state" "201" "$STATUS"
PO2_ID=$(jq_val "$BODY" "d.get('id','')")

echo -e "\n${YELLOW}-- PO Line Total Mismatch (MUST FAIL) --${NC}"

RESP=$(auth_post "/purchases" "{\"store_id\":\"$STORE_ID\",\"vendor_id\":\"$VENDOR_LOCAL_ID\",\"order_date\":\"$TODAY\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":10,\"rate\":25,\"total\":9999}]}")
split_response "$RESP"
assert_status "PO: line total mismatch → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- PO List --${NC}"

RESP=$(auth_get "/purchases?store_id=$STORE_ID")
split_response "$RESP"
assert_status "PO: list" "200" "$STATUS"
PO_LIST_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "PO: at least 2 POs" "$PO_LIST_COUNT" "1"

echo -e "\n${YELLOW}-- PO Get --${NC}"

RESP=$(auth_get "/purchases/$PO1_ID")
split_response "$RESP"
assert_status "PO: get by ID" "200" "$STATUS"
PO1_ITEMS=$(jq_val "$BODY" "len(d.get('items',[])) if isinstance(d,dict) else 0")
assert_eq "PO: has 3 items" "$PO1_ITEMS" "3"

echo -e "\n${YELLOW}-- PO Receive (Partial) --${NC}"

RESP=$(auth_post "/purchases/receive" "{\"po_id\":\"$PO1_ID\",\"store_id\":\"$STORE_ID\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":50,\"batch_no\":\"PO-MILK-RCV\",\"cost_price\":25}]}")
split_response "$RESP"
assert_status "PO: receive partial" "200" "$STATUS"
assert_contains "PO: status partial" "$BODY" "partial"

# Verify stock increased
MILK_AFTER_RCV=$(get_stock_qty "$PROD_MILK_ID" "$STORE_ID")
assert_eq "PO: Milk stock after receive = 185" "$MILK_AFTER_RCV" "185"

echo -e "\n${YELLOW}-- PO Receive (Full) --${NC}"

RESP=$(auth_post "/purchases/receive" "{\"po_id\":\"$PO1_ID\",\"store_id\":\"$STORE_ID\",\"items\":[{\"product_id\":\"$PROD_BUTTER_ID\",\"quantity\":30,\"batch_no\":\"PO-BUTTER-RCV\",\"cost_price\":120},{\"product_id\":\"$PROD_TEA_ID\",\"quantity\":20,\"batch_no\":\"PO-TEA-RCV\",\"cost_price\":150}]}")
split_response "$RESP"
assert_status "PO: receive remaining" "200" "$STATUS"
assert_contains "PO: status received" "$BODY" "received"

echo -e "\n${YELLOW}-- PO Cancel --${NC}"

# Create a 3rd PO then cancel
RESP=$(auth_post "/purchases" "{\"store_id\":\"$STORE_ID\",\"vendor_id\":\"$VENDOR_LOCAL_ID\",\"order_date\":\"$TODAY\",\"items\":[{\"product_id\":\"$PROD_ICE_ID\",\"quantity\":10,\"rate\":200}]}")
split_response "$RESP"
PO3_ID=$(jq_val "$BODY" "d.get('id','')")
assert_status "PO: create for cancel" "201" "$STATUS"

RESP=$(auth_post "/purchases/cancel/$PO3_ID" '{}')
split_response "$RESP"
assert_status "PO: cancel" "200" "$STATUS"

# Cancel already-received PO (MUST FAIL)
RESP=$(auth_post "/purchases/cancel/$PO1_ID" '{}')
split_response "$RESP"
assert_status "PO: cancel received → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- GRN --${NC}"

RESP=$(auth_post "/grn" "{\"po_id\":\"$PO2_ID\",\"received_date\":\"$TODAY\",\"notes\":\"Full delivery\",\"items\":[{\"product_id\":\"$PROD_RICE_ID\",\"ordered_qty\":100,\"received_qty\":100,\"accepted_qty\":95,\"rejected_qty\":5,\"reason\":\"5 bags damaged\"}]}")
split_response "$RESP"
assert_status "GRN: create" "201" "$STATUS"
GRN_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_get "/grn?store_id=$STORE_ID")
split_response "$RESP"
assert_status "GRN: list" "200" "$STATUS"
GRN_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "GRN: has entries" "$GRN_COUNT" "0"

RESP=$(auth_get "/grn/$GRN_ID")
split_response "$RESP"
assert_status "GRN: get by ID" "200" "$STATUS"
assert_contains "GRN: has items" "$BODY" "items"

###############################################################################
header "PHASE 6: Billing & Invoice Flow"
###############################################################################

echo -e "\n${YELLOW}-- Cash Sale --${NC}"

# Milk: 5 units × 28 = 140 (sub_total)
# GST: 5% → 7.00 (cgst 3.50, sgst 3.50)
# Total = 147.00
MILK_BEFORE=$(get_stock_qty "$PROD_MILK_ID" "$STORE_ID")

RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T10:00:00\",\"sub_total\":140,\"cgst\":3.50,\"sgst\":3.50,\"igst\":0,\"total\":147,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":5,\"rate\":28,\"total\":140}]}")
split_response "$RESP"
assert_status "Invoice: cash sale" "201" "$STATUS"
INV_CASH_ID=$(jq_val "$BODY" "d.get('id','')")
INV_CASH_NO=$(jq_val "$BODY" "d.get('invoice_no','')")
assert_contains "Invoice: has INV- prefix" "$INV_CASH_NO" "INV-"

# Verify stock deducted
MILK_AFTER_SALE=$(get_stock_qty "$PROD_MILK_ID" "$STORE_ID")
EXPECTED_MILK=$(python3 -c "print(float('$MILK_BEFORE') - 5)")
assert_eq "Invoice: Milk stock deducted by 5" "$MILK_AFTER_SALE" "$EXPECTED_MILK"

echo -e "\n${YELLOW}-- UPI Sale --${NC}"

# Butter: 2 × 140 = 280, GST 12% → 33.60
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T11:00:00\",\"sub_total\":280,\"cgst\":16.80,\"sgst\":16.80,\"total\":313.60,\"status\":\"completed\",\"payment_mode\":\"upi\",\"items\":[{\"product_id\":\"$PROD_BUTTER_ID\",\"quantity\":2,\"rate\":140,\"total\":280}]}")
split_response "$RESP"
assert_status "Invoice: UPI sale" "201" "$STATUS"
INV_UPI_ID=$(jq_val "$BODY" "d.get('id','')")

echo -e "\n${YELLOW}-- Card Sale --${NC}"

# Ice Cream: 3 × 230 = 690, GST 18% → 124.20
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T12:00:00\",\"sub_total\":690,\"cgst\":62.10,\"sgst\":62.10,\"total\":814.20,\"status\":\"completed\",\"payment_mode\":\"card\",\"items\":[{\"product_id\":\"$PROD_ICE_ID\",\"quantity\":3,\"rate\":230,\"total\":690}]}")
split_response "$RESP"
assert_status "Invoice: card sale" "201" "$STATUS"

echo -e "\n${YELLOW}-- Credit Sale --${NC}"

# Tea: 10 × 180 = 1800, GST 5% → 90. Total = 1890
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"customer_id\":\"$CUST_REGULAR_ID\",\"invoice_date\":\"${TODAY}T13:00:00\",\"sub_total\":1800,\"cgst\":45,\"sgst\":45,\"total\":1890,\"status\":\"completed\",\"payment_mode\":\"credit\",\"items\":[{\"product_id\":\"$PROD_TEA_ID\",\"quantity\":10,\"rate\":180,\"total\":1800}]}")
split_response "$RESP"
assert_status "Invoice: credit sale" "201" "$STATUS"
INV_CREDIT_ID=$(jq_val "$BODY" "d.get('id','')")

echo -e "\n${YELLOW}-- Credit Limit Exceeded (MUST FAIL) --${NC}"

# Regular customer credit_limit=5000, already has 1890 outstanding + 4000 new = 5890 > 5000
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"customer_id\":\"$CUST_REGULAR_ID\",\"invoice_date\":\"${TODAY}T14:00:00\",\"sub_total\":4000,\"cgst\":100,\"sgst\":100,\"total\":4200,\"status\":\"completed\",\"payment_mode\":\"credit\",\"items\":[{\"product_id\":\"$PROD_TEA_ID\",\"quantity\":22,\"rate\":180,\"total\":3960},{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":40}]}")
split_response "$RESP"
assert_status "Invoice: credit limit exceeded → 400" "400" "$STATUS"
assert_contains "Invoice: credit limit msg" "$BODY" "credit limit"

echo -e "\n${YELLOW}-- Wholesale Pricing --${NC}"

# Wholesale customer: should use wholesale_sell_price of Milk = 26
# 10 × 26 = 260, GST 5% → 13. Total = 273
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"customer_id\":\"$CUST_WHOLESALE_ID\",\"invoice_date\":\"${TODAY}T15:00:00\",\"sub_total\":260,\"cgst\":6.50,\"sgst\":6.50,\"total\":273,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":10,\"rate\":26,\"total\":260}]}")
split_response "$RESP"
assert_status "Invoice: wholesale pricing" "201" "$STATUS"

echo -e "\n${YELLOW}-- Multi-item Invoice --${NC}"

# Milk 2×28=56 + Butter 1×140=140 + Tea 3×180=540 = sub_total 736
# Milk GST: 56×0.05=2.80, Butter GST: 140×0.12=16.80, Tea GST: 540×0.05=27
# Total CGST: (2.80+16.80+27)/2=23.30, SGST: 23.30. Total = 736+46.60=782.60
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T16:00:00\",\"sub_total\":736,\"cgst\":23.30,\"sgst\":23.30,\"total\":782.60,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":2,\"rate\":28,\"total\":56},{\"product_id\":\"$PROD_BUTTER_ID\",\"quantity\":1,\"rate\":140,\"total\":140},{\"product_id\":\"$PROD_TEA_ID\",\"quantity\":3,\"rate\":180,\"total\":540}]}")
split_response "$RESP"
assert_status "Invoice: multi-item sale" "201" "$STATUS"
INV_MULTI_ID=$(jq_val "$BODY" "d.get('id','')")

echo -e "\n${YELLOW}-- Invoice List --${NC}"

RESP=$(auth_get "/invoices?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Invoice: list" "200" "$STATUS"
INV_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Invoice: has multiple invoices" "$INV_COUNT" "4"

echo -e "\n${YELLOW}-- Invoice Detail --${NC}"

RESP=$(auth_get "/invoices/$INV_MULTI_ID")
split_response "$RESP"
assert_status "Invoice: get detail" "200" "$STATUS"
assert_contains "Invoice: has items" "$BODY" "items"
INV_DETAIL_TOTAL=$(jq_val "$BODY" "d.get('invoice',{}).get('total',0) if isinstance(d,dict) else 0")
assert_eq "Invoice: detail total = 782.60" "$INV_DETAIL_TOTAL" "782.60"

echo -e "\n${YELLOW}-- Oversell (MUST FAIL) --${NC}"

RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T17:00:00\",\"sub_total\":50000,\"cgst\":1250,\"sgst\":1250,\"total\":52500,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":999,\"rate\":28,\"total\":27972},{\"product_id\":\"$PROD_TEA_ID\",\"quantity\":999,\"rate\":180,\"total\":22028}]}")
split_response "$RESP"
assert_status "Invoice: oversell → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- Split Payment --${NC}"

# Create a small invoice first for split payment
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T18:00:00\",\"sub_total\":28,\"cgst\":0.70,\"sgst\":0.70,\"total\":29.40,\"status\":\"completed\",\"payment_mode\":\"split\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
assert_status "Invoice: for split payment" "201" "$STATUS"
INV_SPLIT_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/payments/split" "{\"invoice_id\":\"$INV_SPLIT_ID\",\"payments\":[{\"payment_mode\":\"cash\",\"amount\":15},{\"payment_mode\":\"upi\",\"amount\":14.40}]}")
split_response "$RESP"
assert_status "Payment: split create" "201" "$STATUS"

# Split payment idempotency
RESP=$(auth_post "/payments/split" "{\"invoice_id\":\"$INV_SPLIT_ID\",\"payments\":[{\"payment_mode\":\"cash\",\"amount\":15},{\"payment_mode\":\"upi\",\"amount\":14.40}]}")
split_response "$RESP"
assert_status "Payment: split idempotent → 409" "409" "$STATUS"

# Get invoice payments
RESP=$(auth_get "/payments/invoice/$INV_SPLIT_ID")
split_response "$RESP"
assert_status "Payment: get invoice payments" "200" "$STATUS"
PAY_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_eq "Payment: has 2 entries" "$PAY_COUNT" "2"

echo -e "\n${YELLOW}-- Settlement Report --${NC}"

RESP=$(auth_get "/payments/settlement?store_id=$STORE_ID&date=$TODAY")
split_response "$RESP"
assert_status "Payment: settlement report" "200" "$STATUS"
SETTLE_TOTAL=$(jq_val "$BODY" "d.get('total_sales',0)")
assert_gt "Payment: total_sales > 0" "$SETTLE_TOTAL" "0"

###############################################################################
header "PHASE 7: Credit Management"
###############################################################################

echo -e "\n${YELLOW}-- Credit List --${NC}"

RESP=$(auth_get "/credit?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Credit: list" "200" "$STATUS"
CREDIT_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Credit: has entries" "$CREDIT_COUNT" "0"

# Extract credit entry ID from credit sale
CREDIT_ENTRY_ID=$(jq_val "$BODY" "[e['id'] for e in d if e.get('type')=='issue'][0] if d else ''")

echo -e "\n${YELLOW}-- Credit Get --${NC}"

if [ "$CREDIT_ENTRY_ID" != "PARSE_ERROR" ] && [ -n "$CREDIT_ENTRY_ID" ]; then
  RESP=$(auth_get "/credit/$CREDIT_ENTRY_ID")
  split_response "$RESP"
  assert_status "Credit: get by ID" "200" "$STATUS"
  assert_contains "Credit: has entry" "$BODY" "entry"
fi

echo -e "\n${YELLOW}-- Credit Payment: Partial --${NC}"

RESP=$(auth_post "/credit/payment" "{\"customer_id\":\"$CUST_REGULAR_ID\",\"amount\":500,\"method\":\"cash\"}")
split_response "$RESP"
assert_status "Credit: partial payment" "200" "$STATUS"

echo -e "\n${YELLOW}-- Credit Payment: Full --${NC}"

RESP=$(auth_post "/credit/payment" "{\"customer_id\":\"$CUST_REGULAR_ID\",\"amount\":1390,\"method\":\"upi\"}")
split_response "$RESP"
assert_status "Credit: full payment" "200" "$STATUS"

echo -e "\n${YELLOW}-- Credit Overpayment → Advance --${NC}"

# Create another credit sale for overpayment test
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"customer_id\":\"$CUST_REGULAR_ID\",\"invoice_date\":\"${TODAY}T19:00:00\",\"sub_total\":180,\"cgst\":4.50,\"sgst\":4.50,\"total\":189,\"status\":\"completed\",\"payment_mode\":\"credit\",\"items\":[{\"product_id\":\"$PROD_TEA_ID\",\"quantity\":1,\"rate\":180,\"total\":180}]}")
split_response "$RESP"
assert_status "Credit: new credit sale for overpay" "201" "$STATUS"

RESP=$(auth_post "/credit/payment" "{\"customer_id\":\"$CUST_REGULAR_ID\",\"amount\":300,\"method\":\"cash\"}")
split_response "$RESP"
assert_status "Credit: overpayment" "200" "$STATUS"
assert_contains "Credit: advance note" "$BODY" "advance"

echo -e "\n${YELLOW}-- Credit Overdue --${NC}"

RESP=$(auth_get "/credit/overdue?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Credit: overdue" "200" "$STATUS"

echo -e "\n${YELLOW}-- Credit Aging --${NC}"

RESP=$(auth_get "/reports/credit-aging?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Credit: aging report" "200" "$STATUS"
assert_contains "Credit: has total_outstanding" "$BODY" "total_outstanding"

###############################################################################
header "PHASE 8: Returns & Losses"
###############################################################################

echo -e "\n${YELLOW}-- Returns --${NC}"

TEA_BEFORE_RETURN=$(get_stock_qty "$PROD_TEA_ID" "$STORE_ID")

RESP=$(auth_post "/returns" "{\"invoice_id\":\"$INV_CREDIT_ID\",\"product_id\":\"$PROD_TEA_ID\",\"quantity\":3,\"amount\":540,\"reason\":\"defective\",\"refund_mode\":\"cash\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Returns: create" "201" "$STATUS"
RETURN_ID=$(jq_val "$BODY" "d.get('id','')")
assert_contains "Returns: status pending" "$BODY" "pending"

RESP=$(auth_get "/returns?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Returns: list" "200" "$STATUS"
RETURN_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Returns: has entries" "$RETURN_COUNT" "0"

RESP=$(auth_post "/returns/$RETURN_ID/approve?store_id=$STORE_ID" '{}')
split_response "$RESP"
assert_status "Returns: approve" "200" "$STATUS"

# Verify stock restored
TEA_AFTER_RETURN=$(get_stock_qty "$PROD_TEA_ID" "$STORE_ID")
EXPECTED_TEA=$(python3 -c "print(float('$TEA_BEFORE_RETURN') + 3)")
assert_eq "Returns: Tea stock restored +3" "$TEA_AFTER_RETURN" "$EXPECTED_TEA"

echo -e "\n${YELLOW}-- Losses --${NC}"

BUTTER_BEFORE_LOSS=$(get_stock_qty "$PROD_BUTTER_ID" "$STORE_ID")

RESP=$(auth_post "/losses" "{\"product_id\":\"$PROD_BUTTER_ID\",\"store_id\":\"$STORE_ID\",\"quantity\":2,\"loss_type\":\"damaged\",\"reason\":\"dropped\"}")
split_response "$RESP"
assert_status "Losses: create damaged" "201" "$STATUS"

RESP=$(auth_post "/losses" "{\"product_id\":\"$PROD_ICE_ID\",\"store_id\":\"$STORE_ID\",\"quantity\":1,\"loss_type\":\"expired\",\"reason\":\"past expiry date\"}")
split_response "$RESP"
assert_status "Losses: create expired" "201" "$STATUS"

RESP=$(auth_post "/losses" "{\"product_id\":\"$PROD_RICE_ID\",\"store_id\":\"$STORE_ID\",\"quantity\":1,\"loss_type\":\"stolen\",\"reason\":\"theft\"}")
split_response "$RESP"
assert_status "Losses: create stolen" "201" "$STATUS"

RESP=$(auth_get "/losses?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Losses: list" "200" "$STATUS"
LOSS_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_eq "Losses: has 3 entries" "$LOSS_COUNT" "3"

# Filter by loss_type
RESP=$(auth_get "/losses?store_id=$STORE_ID&loss_type=damaged")
split_response "$RESP"
assert_status "Losses: filter damaged" "200" "$STATUS"
DAMAGED_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_eq "Losses: 1 damaged" "$DAMAGED_COUNT" "1"

###############################################################################
header "PHASE 9: Expiry Tracking"
###############################################################################

# Add near-expiry stock
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_ICE_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"ICE-EXPIRY\",\"quantity\":5,\"type\":\"add\",\"cost_price\":200,\"expiry_date\":\"$THREE_DAYS\"}")
split_response "$RESP"
assert_status "Expiry: add near-expiry stock" "200" "$STATUS"

# Add expired stock
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_BUTTER_ID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"BUTTER-EXPIRED\",\"quantity\":3,\"type\":\"add\",\"cost_price\":120,\"expiry_date\":\"$YESTERDAY\"}")
split_response "$RESP"
assert_status "Expiry: add expired stock" "200" "$STATUS"

RESP=$(auth_get "/expiry/expiring?store_id=$STORE_ID&days=7")
split_response "$RESP"
assert_status "Expiry: expiring soon" "200" "$STATUS"
EXPIRING_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Expiry: has expiring items" "$EXPIRING_COUNT" "0"

RESP=$(auth_get "/expiry/expired?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Expiry: expired list" "200" "$STATUS"
EXPIRED_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Expiry: has expired items" "$EXPIRED_COUNT" "0"

###############################################################################
header "PHASE 10: Analytics & Reports"
###############################################################################

echo -e "\n${YELLOW}-- Dashboard --${NC}"

RESP=$(auth_get "/analytics/dashboard?store_id=$STORE_ID&range=today")
split_response "$RESP"
assert_status "Analytics: dashboard" "200" "$STATUS"
DASH_REVENUE=$(jq_val "$BODY" "d.get('revenue',0)")
assert_gt "Analytics: revenue > 0" "$DASH_REVENUE" "0"
DASH_INV_COUNT=$(jq_val "$BODY" "d.get('invoice_count',0)")
assert_gt "Analytics: invoice_count > 0" "$DASH_INV_COUNT" "0"
DASH_GST=$(jq_val "$BODY" "d.get('gst_collected',0)")
assert_gt "Analytics: gst_collected > 0" "$DASH_GST" "0"

echo -e "\n${YELLOW}-- Sales by Category --${NC}"

RESP=$(auth_get "/analytics/sales/category?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Analytics: sales by category" "200" "$STATUS"
CAT_SALES_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Analytics: has category sales" "$CAT_SALES_COUNT" "0"

echo -e "\n${YELLOW}-- Top Products --${NC}"

RESP=$(auth_get "/analytics/sales/top?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Analytics: top products" "200" "$STATUS"
TOP_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Analytics: has top products" "$TOP_COUNT" "0"

echo -e "\n${YELLOW}-- Hourly Sales --${NC}"

RESP=$(auth_get "/analytics/sales/hourly?store_id=$STORE_ID&date=$TODAY")
split_response "$RESP"
assert_status "Analytics: hourly sales" "200" "$STATUS"
HOURLY_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Analytics: has hourly data" "$HOURLY_COUNT" "0"

echo -e "\n${YELLOW}-- Payment Mode Breakup --${NC}"

RESP=$(auth_get "/analytics/sales/payment?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Analytics: payment breakup" "200" "$STATUS"
assert_contains "Analytics: has cash" "$BODY" "cash"

echo -e "\n${YELLOW}-- Profit & Loss --${NC}"

RESP=$(auth_get "/reports/profit-loss?store_id=$STORE_ID&start_date=$LAST_MONTH&end_date=$TODAY")
split_response "$RESP"
assert_status "Reports: profit-loss" "200" "$STATUS"
PL_REVENUE=$(jq_val "$BODY" "d.get('total_revenue',0)")
assert_gt "Reports: revenue > 0" "$PL_REVENUE" "0"
PL_COGS=$(jq_val "$BODY" "d.get('total_cogs',0)")
assert_gt "Reports: COGS > 0" "$PL_COGS" "0"

echo -e "\n${YELLOW}-- GSTR-1 --${NC}"

RESP=$(auth_get "/invoices/gstr1?store_id=$STORE_ID&month=$MONTH")
split_response "$RESP"
assert_status "Reports: GSTR-1" "200" "$STATUS"
GSTR1_COUNT=$(jq_val "$BODY" "len(d.get('invoices',[])) if isinstance(d,dict) else 0")
assert_gt "Reports: GSTR-1 has invoices" "$GSTR1_COUNT" "0"

echo -e "\n${YELLOW}-- GSTR-3B --${NC}"

RESP=$(auth_get "/reports/gstr-3b?store_id=$STORE_ID&month=$MONTH")
split_response "$RESP"
assert_status "Reports: GSTR-3B" "200" "$STATUS"
assert_contains "Reports: GSTR-3B has outward" "$BODY" "outward_supplies"

echo -e "\n${YELLOW}-- GSTR-1 Mark Filed --${NC}"

RESP=$(auth_post "/invoices/gstr1/mark-filed" "{\"store_id\":\"$STORE_ID\",\"month\":\"$MONTH\"}")
split_response "$RESP"
assert_status "Reports: GSTR-1 mark filed" "200" "$STATUS"
assert_contains "Reports: invoices marked" "$BODY" "invoices_marked"

# Re-check GSTR-1 — should now be empty (all filed)
RESP=$(auth_get "/invoices/gstr1?store_id=$STORE_ID&month=$MONTH")
split_response "$RESP"
GSTR1_AFTER=$(jq_val "$BODY" "len(d.get('invoices',[])) if isinstance(d,dict) else 0")
assert_eq "Reports: GSTR-1 empty after filing" "$GSTR1_AFTER" "0"

###############################################################################
header "PHASE 11: Expenses"
###############################################################################

RESP=$(auth_get "/expenses/categories")
split_response "$RESP"
assert_status "Expenses: categories" "200" "$STATUS"
EXPENSE_CAT_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Expenses: has seeded categories" "$EXPENSE_CAT_COUNT" "5"

# Get first expense category ID
EXP_CAT1_ID=$(jq_val "$BODY" "d[0]['id'] if d else ''")
EXP_CAT2_ID=$(jq_val "$BODY" "d[1]['id'] if d else ''")
EXP_CAT3_ID=$(jq_val "$BODY" "d[2]['id'] if d else ''")

RESP=$(auth_post "/expenses" "{\"store_id\":\"$STORE_ID\",\"category_id\":\"$EXP_CAT1_ID\",\"amount\":5000,\"description\":\"Monthly rent\",\"expense_date\":\"$TODAY\"}")
split_response "$RESP"
assert_status "Expenses: create rent" "201" "$STATUS"

RESP=$(auth_post "/expenses" "{\"store_id\":\"$STORE_ID\",\"category_id\":\"$EXP_CAT2_ID\",\"amount\":2000,\"description\":\"Electricity bill\",\"expense_date\":\"$TODAY\"}")
split_response "$RESP"
assert_status "Expenses: create electricity" "201" "$STATUS"

RESP=$(auth_post "/expenses" "{\"store_id\":\"$STORE_ID\",\"category_id\":\"$EXP_CAT3_ID\",\"amount\":15000,\"description\":\"Staff salaries\",\"expense_date\":\"$TODAY\"}")
split_response "$RESP"
assert_status "Expenses: create salaries" "201" "$STATUS"

RESP=$(auth_get "/expenses?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Expenses: list" "200" "$STATUS"
EXP_LIST_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_eq "Expenses: has 3 entries" "$EXP_LIST_COUNT" "3"

RESP=$(auth_get "/expenses/summary?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Expenses: summary" "200" "$STATUS"
EXP_GRAND=$(jq_val "$BODY" "d.get('grand_total',0)")
assert_eq "Expenses: grand_total = 22000" "$EXP_GRAND" "22000"

###############################################################################
header "PHASE 12: User Management & RBAC"
###############################################################################

echo -e "\n${YELLOW}-- Register Users --${NC}"

RESP=$(auth_post "/auth/register" "{\"username\":\"cashier1\",\"password\":\"cash123\",\"role\":\"cashier\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Auth: register cashier" "201" "$STATUS"
CASHIER_UID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/auth/register" "{\"username\":\"warehouse1\",\"password\":\"ware123\",\"role\":\"warehouse\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Auth: register warehouse" "201" "$STATUS"
WAREHOUSE_UID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/auth/register" "{\"username\":\"helper1\",\"password\":\"help123\",\"role\":\"helper\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Auth: register helper" "201" "$STATUS"
HELPER_UID=$(jq_val "$BODY" "d.get('id','')")

echo -e "\n${YELLOW}-- List Users --${NC}"

RESP=$(auth_get "/auth/users?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Auth: list users" "200" "$STATUS"
USER_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Auth: multiple users" "$USER_COUNT" "3"

echo -e "\n${YELLOW}-- Login as Each Role --${NC}"

# Cashier login
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"cashier1","password":"cash123"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: cashier login" "200" "$STATUS"
CASHIER_TOKEN=$(jq_val "$BODY" "d.get('token','')")

# Warehouse login
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"warehouse1","password":"ware123"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: warehouse login" "200" "$STATUS"
WAREHOUSE_TOKEN=$(jq_val "$BODY" "d.get('token','')")

# Helper login
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"helper1","password":"help123"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: helper login" "200" "$STATUS"
HELPER_TOKEN=$(jq_val "$BODY" "d.get('token','')")

echo -e "\n${YELLOW}-- RBAC: Cashier Permissions --${NC}"

# Cashier CAN read catalog
RESP=$(role_get "$CASHIER_TOKEN" "$STORE_ID" "/products?store_id=$STORE_ID")
split_response "$RESP"
assert_status "RBAC: cashier can read products" "200" "$STATUS"

# Cashier CAN create invoice
RESP=$(role_post "$CASHIER_TOKEN" "$STORE_ID" "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T20:00:00\",\"sub_total\":28,\"cgst\":0.70,\"sgst\":0.70,\"total\":29.40,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
assert_status "RBAC: cashier can create invoice" "201" "$STATUS"

# Cashier CANNOT create category (403)
RESP=$(role_post "$CASHIER_TOKEN" "$STORE_ID" "/categories" '{"name":"Should Fail"}')
split_response "$RESP"
assert_status "RBAC: cashier cannot create category" "403" "$STATUS"

# Cashier CANNOT adjust stock (403)
RESP=$(role_post "$CASHIER_TOKEN" "$STORE_ID" "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"quantity\":1,\"type\":\"add\"}")
split_response "$RESP"
assert_status "RBAC: cashier cannot adjust stock" "403" "$STATUS"

# Cashier CANNOT view credit (403)
RESP=$(role_get "$CASHIER_TOKEN" "$STORE_ID" "/credit?store_id=$STORE_ID")
split_response "$RESP"
assert_status "RBAC: cashier cannot view credit" "403" "$STATUS"

# Cashier CANNOT view analytics (403)
RESP=$(role_get "$CASHIER_TOKEN" "$STORE_ID" "/analytics/dashboard?store_id=$STORE_ID")
split_response "$RESP"
assert_status "RBAC: cashier cannot view analytics" "403" "$STATUS"

echo -e "\n${YELLOW}-- RBAC: Warehouse Permissions --${NC}"

# Warehouse CAN read catalog
RESP=$(role_get "$WAREHOUSE_TOKEN" "$STORE_ID" "/products?store_id=$STORE_ID")
split_response "$RESP"
assert_status "RBAC: warehouse can read products" "200" "$STATUS"

# Warehouse CAN adjust stock
RESP=$(role_post "$WAREHOUSE_TOKEN" "$STORE_ID" "/stock/adjust" "{\"product_id\":\"$PROD_RICE_ID\",\"store_id\":\"$STORE_ID\",\"quantity\":5,\"type\":\"add\",\"cost_price\":300}")
split_response "$RESP"
assert_status "RBAC: warehouse can adjust stock" "200" "$STATUS"

# Warehouse CANNOT create invoice (403)
RESP=$(role_post "$WAREHOUSE_TOKEN" "$STORE_ID" "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"$TODAY\",\"sub_total\":28,\"total\":29,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
assert_status "RBAC: warehouse cannot create invoice" "403" "$STATUS"

# Warehouse CANNOT view credit (403)
RESP=$(role_get "$WAREHOUSE_TOKEN" "$STORE_ID" "/credit?store_id=$STORE_ID")
split_response "$RESP"
assert_status "RBAC: warehouse cannot view credit" "403" "$STATUS"

echo -e "\n${YELLOW}-- RBAC: Helper Permissions --${NC}"

# Helper CAN read catalog
RESP=$(role_get "$HELPER_TOKEN" "$STORE_ID" "/products?store_id=$STORE_ID")
split_response "$RESP"
assert_status "RBAC: helper can read products" "200" "$STATUS"

# Helper CAN view analytics
RESP=$(role_get "$HELPER_TOKEN" "$STORE_ID" "/analytics/dashboard?store_id=$STORE_ID&range=today")
split_response "$RESP"
assert_status "RBAC: helper can view analytics" "200" "$STATUS"

# Helper CAN read invoices
RESP=$(role_get "$HELPER_TOKEN" "$STORE_ID" "/invoices?store_id=$STORE_ID")
split_response "$RESP"
assert_status "RBAC: helper can read invoices" "200" "$STATUS"

# Helper CANNOT create invoice (403)
RESP=$(role_post "$HELPER_TOKEN" "$STORE_ID" "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"$TODAY\",\"sub_total\":28,\"total\":29,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
assert_status "RBAC: helper cannot create invoice" "403" "$STATUS"

# Helper CANNOT adjust stock (403)
RESP=$(role_post "$HELPER_TOKEN" "$STORE_ID" "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE_ID\",\"quantity\":1,\"type\":\"add\"}")
split_response "$RESP"
assert_status "RBAC: helper cannot adjust stock" "403" "$STATUS"

# Helper CANNOT create customer (403)
RESP=$(role_post "$HELPER_TOKEN" "$STORE_ID" "/customers" "{\"name\":\"Should Fail\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "RBAC: helper cannot create customer" "403" "$STATUS"

echo -e "\n${YELLOW}-- Change Password --${NC}"

# Change cashier password
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Authorization: Bearer $CASHIER_TOKEN" -H "X-Store-ID: $STORE_ID" -H "Content-Type: application/json" \
  -d '{"old_password":"cash123","new_password":"newcash456"}' "$BASE/auth/change-password" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: change password" "200" "$STATUS"

# Login with new password
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"cashier1","password":"newcash456"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: login with new password" "200" "$STATUS"
CASHIER_TOKEN=$(jq_val "$BODY" "d.get('token','')")

# Old password should fail
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"cashier1","password":"cash123"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: old password fails" "401" "$STATUS"

echo -e "\n${YELLOW}-- Deactivate / Activate User --${NC}"

RESP=$(auth_post "/auth/users/$HELPER_UID/deactivate" '{}')
split_response "$RESP"
assert_status "Auth: deactivate helper" "200" "$STATUS"

# Verify can't login
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"helper1","password":"help123"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: deactivated can't login" "401" "$STATUS"

# Re-activate
RESP=$(auth_post "/auth/users/$HELPER_UID/activate" '{}')
split_response "$RESP"
assert_status "Auth: activate helper" "200" "$STATUS"

# Verify can login again
RESP=$(curl -s -w "\n%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '{"username":"helper1","password":"help123"}' "$BASE/auth/login" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Auth: reactivated can login" "200" "$STATUS"

###############################################################################
header "PHASE 13: Audit & Sync"
###############################################################################

echo -e "\n${YELLOW}-- Audit --${NC}"

RESP=$(auth_get "/audit?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Audit: list" "200" "$STATUS"
AUDIT_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Audit: has entries" "$AUDIT_COUNT" "0"

# Filter by table
RESP=$(auth_get "/audit?store_id=$STORE_ID&table=invoices")
split_response "$RESP"
assert_status "Audit: filter by invoices" "200" "$STATUS"

# Post manual audit entry
RESP=$(auth_post "/audit" '{"action":"manual_test","table_name":"test","record_id":"test-123","old_value":"before","new_value":"after"}')
split_response "$RESP"
assert_status "Audit: POST manual entry" "200" "$STATUS"

echo -e "\n${YELLOW}-- Sync --${NC}"

RESP=$(auth_post "/sync/push" "{\"table_name\":\"products\",\"record_id\":\"$PROD_MILK_ID\",\"operation\":\"update\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "Sync: push record" "200" "$STATUS"
SYNC_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_get "/sync/pending?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Sync: pending list" "200" "$STATUS"
SYNC_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "Sync: has pending" "$SYNC_COUNT" "0"

# Mark synced
RESP=$(auth_post "/sync/mark" "{\"record_ids\":[\"$SYNC_ID\"]}")
split_response "$RESP"
assert_status "Sync: mark synced" "200" "$STATUS"

# Verify gone from pending
RESP=$(auth_get "/sync/pending?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Sync: verify no longer pending" "200" "$STATUS"

# Sync conflict resolution
RESP=$(auth_post "/sync/push" "{\"table_name\":\"products\",\"record_id\":\"conflict-test\",\"operation\":\"update\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
CONFLICT_SYNC_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/sync/conflict" '{"table_name":"products","record_id":"conflict-test","resolution":"remote_wins"}')
split_response "$RESP"
assert_status "Sync: conflict resolution remote_wins" "200" "$STATUS"

###############################################################################
header "PHASE 14: Multi-Store Isolation"
###############################################################################

# Store 2 already exists. We need to set prices and add stock for store 2.

# Set Milk prices for store 2
RESP=$(auth_post "/products/price" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE2_ID\",\"mrp\":35,\"cost_price\":27,\"sell_price\":32,\"wholesale_sell_price\":30}")
split_response "$RESP"
assert_status "Store2: set Milk price" "200" "$STATUS"

# Add stock to store 2
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$PROD_MILK_ID\",\"store_id\":\"$STORE2_ID\",\"batch_no\":\"S2-MILK\",\"quantity\":50,\"type\":\"add\",\"cost_price\":27}")
split_response "$RESP"
assert_status "Store2: add Milk stock" "200" "$STATUS"

# Verify Store 2 stock
S2_MILK=$(get_stock_qty "$PROD_MILK_ID" "$STORE2_ID")
assert_eq "Store2: Milk qty = 50+15(transfer) = 65" "$S2_MILK" "65"

# Store 1 stock list should not contain Store 2 stock batches
RESP=$(auth_get "/stock?store_id=$STORE_ID")
split_response "$RESP"
assert_not_contains "Isolation: Store1 stock no S2-MILK batch" "$BODY" "S2-MILK"

# Store 2 customers should be isolated
RESP=$(auth_get "/customers?store_id=$STORE2_ID")
split_response "$RESP"
assert_status "Isolation: Store2 customers" "200" "$STATUS"
S2_CUST_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_eq "Isolation: Store2 has 0 customers" "$S2_CUST_COUNT" "0"

###############################################################################
header "PHASE 15: Business Cycle — Exhaustion → Purchase → Resell"
###############################################################################

# Use a known product: create a fresh one with controlled stock
RESP=$(auth_post "/products" "{\"name\":\"Cycle Test Product\",\"category_id\":\"$CAT_DAIRY_ID\",\"uom_id\":\"$UOM_NOS_ID\",\"gst_rate\":5,\"base_qty\":1,\"min_stock\":5}")
split_response "$RESP"
CYCLE_PID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/products/price" "{\"product_id\":\"$CYCLE_PID\",\"store_id\":\"$STORE_ID\",\"mrp\":100,\"cost_price\":60,\"sell_price\":80,\"wholesale_sell_price\":70}")
split_response "$RESP"

# Step 1: Add exactly 20 units
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$CYCLE_PID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"CYCLE-BATCH\",\"quantity\":20,\"type\":\"add\",\"cost_price\":60}")
split_response "$RESP"
assert_status "Cycle: add 20 units" "200" "$STATUS"

# Step 2: Sell 18 units. sub_total=18×80=1440, GST 5%→72, total=1512
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T21:00:00\",\"sub_total\":1440,\"cgst\":36,\"sgst\":36,\"total\":1512,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$CYCLE_PID\",\"quantity\":18,\"rate\":80,\"total\":1440}]}")
split_response "$RESP"
assert_status "Cycle: sell 18 units" "201" "$STATUS"

# Verify stock = 2
CYCLE_STOCK=$(get_stock_qty "$CYCLE_PID" "$STORE_ID")
assert_eq "Cycle: stock after sell = 2" "$CYCLE_STOCK" "2"

# Step 3: Try sell 5 more → FAIL
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T21:05:00\",\"sub_total\":400,\"cgst\":10,\"sgst\":10,\"total\":420,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$CYCLE_PID\",\"quantity\":5,\"rate\":80,\"total\":400}]}")
split_response "$RESP"
assert_status "Cycle: sell 5 when only 2 → 400" "400" "$STATUS"

# Step 4: Purchase 50 more units via PO
RESP=$(auth_post "/purchases" "{\"store_id\":\"$STORE_ID\",\"vendor_id\":\"$VENDOR_LOCAL_ID\",\"order_date\":\"$TODAY\",\"items\":[{\"product_id\":\"$CYCLE_PID\",\"quantity\":50,\"rate\":60}]}")
split_response "$RESP"
CYCLE_PO_ID=$(jq_val "$BODY" "d.get('id','')")
assert_status "Cycle: create PO for 50" "201" "$STATUS"

# Step 5: Receive PO
RESP=$(auth_post "/purchases/receive" "{\"po_id\":\"$CYCLE_PO_ID\",\"store_id\":\"$STORE_ID\",\"items\":[{\"product_id\":\"$CYCLE_PID\",\"quantity\":50,\"batch_no\":\"CYCLE-PO-RCV\",\"cost_price\":60}]}")
split_response "$RESP"
assert_status "Cycle: receive PO" "200" "$STATUS"

# Verify stock = 52 (2 remaining + 50 received)
CYCLE_STOCK2=$(get_stock_qty "$CYCLE_PID" "$STORE_ID")
assert_eq "Cycle: stock after receive = 52" "$CYCLE_STOCK2" "52"

# Step 6: Sell 30 units. sub_total=30×80=2400, GST 5%→120, total=2520
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T22:00:00\",\"sub_total\":2400,\"cgst\":60,\"sgst\":60,\"total\":2520,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$CYCLE_PID\",\"quantity\":30,\"rate\":80,\"total\":2400}]}")
split_response "$RESP"
assert_status "Cycle: sell 30 units after restock" "201" "$STATUS"

# Verify stock = 22
CYCLE_STOCK3=$(get_stock_qty "$CYCLE_PID" "$STORE_ID")
assert_eq "Cycle: stock after 2nd sell = 22" "$CYCLE_STOCK3" "22"

# Step 7: Verify analytics reflect
RESP=$(auth_get "/analytics/dashboard?store_id=$STORE_ID&range=today")
split_response "$RESP"
CYCLE_REVENUE=$(jq_val "$BODY" "d.get('revenue',0)")
assert_gt "Cycle: dashboard revenue includes sales" "$CYCLE_REVENUE" "3000"

###############################################################################
header "PHASE 16: Edge Cases & Stress"
###############################################################################

echo -e "\n${YELLOW}-- Auth Edge Cases --${NC}"

# No token
RESP=$(curl -s -w "\n%{http_code}" "$BASE/products?store_id=$STORE_ID" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Edge: no token → 401" "401" "$STATUS"

# Invalid token
RESP=$(curl -s -w "\n%{http_code}" -H "Authorization: Bearer invalidtoken123" -H "X-Store-ID: $STORE_ID" "$BASE/products?store_id=$STORE_ID" 2>/dev/null || echo -e "\n000")
split_response "$RESP"
assert_status "Edge: invalid token → 401" "401" "$STATUS"

echo -e "\n${YELLOW}-- Invalid Input Edge Cases --${NC}"

# Invalid JSON body
RESP=$(auth_post "/categories" 'this is not json')
split_response "$RESP"
assert_status "Edge: invalid JSON → 400" "400" "$STATUS"

# Missing required fields
RESP=$(auth_post "/categories" '{}')
split_response "$RESP"
assert_status "Edge: missing required field → 400" "400" "$STATUS"

# Invoice with nonexistent product
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"$TODAY\",\"sub_total\":100,\"total\":100,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"nonexistent-product\",\"quantity\":1,\"rate\":100,\"total\":100}]}")
split_response "$RESP"
assert_status "Edge: invoice with nonexistent product → 400" "400" "$STATUS"

# Invoice with nonexistent customer
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"customer_id\":\"nonexistent-cust\",\"invoice_date\":\"$TODAY\",\"sub_total\":28,\"cgst\":0.70,\"sgst\":0.70,\"total\":29.40,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
assert_status "Edge: invoice with nonexistent customer → 400" "400" "$STATUS"

# Sell exact stock amount (→ stock = 0)
RESP=$(auth_post "/products" "{\"name\":\"ExactTest\",\"category_id\":\"$CAT_DAIRY_ID\",\"uom_id\":\"$UOM_NOS_ID\",\"gst_rate\":5,\"base_qty\":1}")
split_response "$RESP"
EXACT_PID=$(jq_val "$BODY" "d.get('id','')")
RESP=$(auth_post "/products/price" "{\"product_id\":\"$EXACT_PID\",\"store_id\":\"$STORE_ID\",\"mrp\":100,\"sell_price\":80,\"cost_price\":60}")
split_response "$RESP"
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$EXACT_PID\",\"store_id\":\"$STORE_ID\",\"quantity\":5,\"type\":\"add\",\"cost_price\":60}")
split_response "$RESP"

# Sell exactly 5
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T23:00:00\",\"sub_total\":400,\"cgst\":10,\"sgst\":10,\"total\":420,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$EXACT_PID\",\"quantity\":5,\"rate\":80,\"total\":400}]}")
split_response "$RESP"
assert_status "Edge: sell exact stock (5 of 5)" "201" "$STATUS"

EXACT_STOCK=$(get_stock_qty "$EXACT_PID" "$STORE_ID")
assert_eq "Edge: stock = 0 after exact sell" "$EXACT_STOCK" "0"

# Try sell 1 more → FAIL
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T23:05:00\",\"sub_total\":80,\"cgst\":2,\"sgst\":2,\"total\":84,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$EXACT_PID\",\"quantity\":1,\"rate\":80,\"total\":80}]}")
split_response "$RESP"
assert_status "Edge: sell 1 when 0 stock → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- Concurrent Invoice Numbers --${NC}"

# Create 2 invoices back-to-back rapidly
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T23:10:00\",\"sub_total\":28,\"cgst\":0.70,\"sgst\":0.70,\"total\":29.40,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
RAPID_INV1=$(jq_val "$BODY" "d.get('invoice_no','')")

RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T23:10:01\",\"sub_total\":28,\"cgst\":0.70,\"sgst\":0.70,\"total\":29.40,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
RAPID_INV2=$(jq_val "$BODY" "d.get('invoice_no','')")

if [ "$RAPID_INV1" != "$RAPID_INV2" ] && [ "$RAPID_INV1" != "PARSE_ERROR" ] && [ "$RAPID_INV2" != "PARSE_ERROR" ]; then
  echo -e "  ${GREEN}PASS${NC} Edge: unique invoice numbers ($RAPID_INV1 vs $RAPID_INV2)"
  PASS=$((PASS+1))
else
  echo -e "  ${RED}FAIL${NC} Edge: duplicate invoice numbers"
  FAIL=$((FAIL+1))
  ERRORS+=("Edge: duplicate invoice numbers")
fi

echo -e "\n${YELLOW}-- Barcode Lookup Not Found --${NC}"

RESP=$(auth_get "/barcodes/lookup/NONEXISTENT999?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Edge: barcode not found → 404" "404" "$STATUS"

echo -e "\n${YELLOW}-- SubTotal Mismatch --${NC}"

RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"$TODAY\",\"sub_total\":9999,\"total\":9999,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":1,\"rate\":28,\"total\":28}]}")
split_response "$RESP"
assert_status "Edge: sub_total mismatch → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- Self Deactivation --${NC}"

# Admin tries to deactivate self
ADMIN_UID=$(jq_val "$(auth_get "/auth/users?store_id=$STORE_ID" | sed '$d')" "[u['id'] for u in d if u.get('username')=='admin'][0] if d else ''")
if [ "$ADMIN_UID" != "PARSE_ERROR" ] && [ -n "$ADMIN_UID" ]; then
  RESP=$(auth_post "/auth/users/$ADMIN_UID/deactivate" '{}')
  split_response "$RESP"
  assert_status "Edge: cannot deactivate self → 400" "400" "$STATUS"
fi

###############################################################################
header "PHASE 17: Invoice Void & New Features"
###############################################################################

echo -e "\n${YELLOW}-- Void Invoice --${NC}"

# Create a simple invoice to void
RESP=$(auth_post "/invoices" "{\"store_id\":\"$STORE_ID\",\"invoice_date\":\"${TODAY}T23:20:00\",\"sub_total\":280,\"cgst\":16.80,\"sgst\":16.80,\"total\":313.60,\"status\":\"completed\",\"payment_mode\":\"cash\",\"items\":[{\"product_id\":\"$PROD_BUTTER_ID\",\"quantity\":2,\"rate\":140,\"total\":280}]}")
split_response "$RESP"
assert_status "Invoice: create for void test" "201" "$STATUS"
VOID_INV_ID=$(jq_val "$BODY" "d.get('id','')")

# Get initial stock
BUTTER_BEFORE_VOID=$(get_stock_qty "$PROD_BUTTER_ID" "$STORE_ID")

# Void the invoice
RESP=$(auth_post "/invoices/$VOID_INV_ID/void" '{}')
split_response "$RESP"
assert_status "Invoice: void" "200" "$STATUS"

# Verify stock restored
BUTTER_AFTER_VOID=$(get_stock_qty "$PROD_BUTTER_ID" "$STORE_ID")
EXPECTED_BUTTER=$(python3 -c "print(float('$BUTTER_BEFORE_VOID') + 2)")
assert_eq "Invoice: void restores stock +2" "$BUTTER_AFTER_VOID" "$EXPECTED_BUTTER"

# Try to void already-voided → FAIL
RESP=$(auth_post "/invoices/$VOID_INV_ID/void" '{}')
split_response "$RESP"
assert_status "Invoice: double void → 400" "400" "$STATUS"

echo -e "\n${YELLOW}-- Vendor Payments --${NC}"

RESP=$(auth_post "/vendor-payments" "{\"vendor_id\":\"$VENDOR_LOCAL_ID\",\"po_id\":\"$PO1_ID\",\"amount\":5000,\"payment_mode\":\"bank_transfer\",\"reference\":\"TXN12345\",\"payment_date\":\"$TODAY\",\"notes\":\"PO1 payment\",\"store_id\":\"$STORE_ID\"}")
split_response "$RESP"
assert_status "VendorPayments: create" "201" "$STATUS"

RESP=$(auth_get "/vendor-payments?store_id=$STORE_ID")
split_response "$RESP"
assert_status "VendorPayments: list" "200" "$STATUS"
VP_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "VendorPayments: has entries" "$VP_COUNT" "0"

RESP=$(auth_get "/vendor-payments/summary?store_id=$STORE_ID")
split_response "$RESP"
assert_status "VendorPayments: summary" "200" "$STATUS"

echo -e "\n${YELLOW}-- Purchase Returns --${NC}"

RESP=$(auth_post "/purchase-returns" "{\"po_id\":\"$PO1_ID\",\"vendor_id\":\"$VENDOR_LOCAL_ID\",\"store_id\":\"$STORE_ID\",\"return_date\":\"$TODAY\",\"reason\":\"defective items\",\"items\":[{\"product_id\":\"$PROD_MILK_ID\",\"quantity\":10,\"rate\":25}]}")
split_response "$RESP"
assert_status "PurchaseReturns: create" "201" "$STATUS"
PR_ID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_get "/purchase-returns?store_id=$STORE_ID")
split_response "$RESP"
assert_status "PurchaseReturns: list" "200" "$STATUS"
PR_COUNT=$(jq_val "$BODY" "len(d) if isinstance(d,list) else 0")
assert_gt "PurchaseReturns: has entries" "$PR_COUNT" "0"

RESP=$(auth_post "/purchase-returns/$PR_ID/approve?store_id=$STORE_ID" '{}')
split_response "$RESP"
assert_status "PurchaseReturns: approve" "200" "$STATUS"

echo -e "\n${YELLOW}-- Stock Conversion --${NC}"

# Create a product with packing units
RESP=$(auth_post "/products" "{\"name\":\"Beverage Can\",\"category_id\":\"$CAT_DAIRY_ID\",\"uom_id\":\"$UOM_NOS_ID\",\"gst_rate\":5,\"base_qty\":1,\"min_stock\":10}")
split_response "$RESP"
BEV_PID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_post "/products/price" "{\"product_id\":\"$BEV_PID\",\"store_id\":\"$STORE_ID\",\"mrp\":50,\"cost_price\":30,\"sell_price\":45}")
split_response "$RESP"

# Add stock in individual units
RESP=$(auth_post "/stock/adjust" "{\"product_id\":\"$BEV_PID\",\"store_id\":\"$STORE_ID\",\"batch_no\":\"BEV-IND\",\"quantity\":100,\"type\":\"add\",\"cost_price\":30}")
split_response "$RESP"
assert_status "Stock: add individual beverage units" "200" "$STATUS"

# Create packing unit (dozen = 12 cans)
RESP=$(auth_post "/products/$BEV_PID/packing-units" '{"label":"Pack of 12","qty_in_parent":12,"barcode":"BEV12PACK"}')
split_response "$RESP"
PU_ID=$(jq_val "$BODY" "d.get('id','')")
assert_status "PackingUnits: create dozen" "201" "$STATUS"

echo -e "\n${YELLOW}-- Multi-Store Analytics --${NC}"

RESP=$(auth_get "/analytics/multi-store")
split_response "$RESP"
assert_status "Analytics: multi-store (all)" "200" "$STATUS"
MS_TOTAL_REV=$(jq_val "$BODY" "d.get('total_revenue',0)")
assert_gt "Analytics: multi-store revenue > 0" "$MS_TOTAL_REV" "1000"

RESP=$(auth_get "/analytics/multi-store?store_ids=$STORE_ID,$STORE2_ID&range=today")
split_response "$RESP"
assert_status "Analytics: multi-store (filtered)" "200" "$STATUS"

echo -e "\n${YELLOW}-- Product Fields (brand, sac, image) --${NC}"

# Create product with new fields
RESP=$(auth_post "/products" "{\"name\":\"Premium Ghee\",\"category_id\":\"$CAT_DAIRY_ID\",\"uom_id\":\"$UOM_L_ID\",\"gst_rate\":5,\"base_qty\":1,\"brand\":\"DesiBrand\",\"sac_code\":\"02101200\",\"description\":\"Pure Desi Ghee\",\"image_url\":\"https://example.com/ghee.jpg\"}")
split_response "$RESP"
assert_status "Products: create with brand/sac/image" "201" "$STATUS"
GHEE_PID=$(jq_val "$BODY" "d.get('id','')")

RESP=$(auth_get "/products/$GHEE_PID?store_id=$STORE_ID")
split_response "$RESP"
assert_status "Products: get with fields" "200" "$STATUS"
assert_contains "Products: has brand" "$BODY" "DesiBrand"
assert_contains "Products: has sac_code" "$BODY" "02101200"
assert_contains "Products: has description" "$BODY" "Pure Desi"

echo ""
echo -e "${BLUE}═══════════════════════════════════════${NC}"
echo -e "${BLUE}  ALL PHASES COMPLETE${NC}"
echo -e "${BLUE}═══════════════════════════════════════${NC}"
