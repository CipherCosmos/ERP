# GBOS ERP Testing Utilities

## Quick Start

```bash
# 1. Build and run backend
cd backend
go run cmd/server/main.go

# 2. In another terminal, reset database (optional)
cd backend
bash scripts/reset-db.sh

# 3. Run comprehensive E2E tests
bash scripts/comprehensive-e2e-test.sh
```

## Test Scripts

### 1. comprehensive-e2e-test.sh
**Purpose**: Full end-to-end testing of all system features
**Duration**: ~60-120 seconds
**Test Count**: 100+ individual assertions
**Coverage**:
- Authentication & Authorization
- CRUD operations on all entities
- Business logic validation
- Stock management & inventory
- Billing & invoicing
- Credit & payment management
- Vendor management & purchase returns
- Analytics & reporting
- Error handling & edge cases
- Stress testing & concurrency
- Data integrity verification

**Usage**:
```bash
bash scripts/comprehensive-e2e-test.sh
```

**Expected Output**:
```
✓ All tests passed! System is working correctly.
Success Rate: 100%
```

### 2. reset-db.sh
**Purpose**: Reset database to clean state
**⚠️ WARNING**: Permanently deletes all data
**Creates**: Automatic backup before deletion

**Usage**:
```bash
bash scripts/reset-db.sh
```

**Prompts**: 
- Requires explicit "yes" confirmation

## Smoke Tests (Legacy)
Located at: `backend/scripts/smoke-test.sh`
- Original CRUD test suite
- Basic functionality validation

## Unit Tests
Located at: `backend/internal/` (various `*_test.go` files)

**Run all unit tests**:
```bash
cd backend
go test ./...
```

**Run specific test suite**:
```bash
go test ./internal/handlers -v
```

## Test Data Generated

### Products (4 created)
```
1. Laptop - Dell XPS 13
   - Price: ₹89,999
   - Stock: 4 units
   - Category: Electronics
   - UOM: Pieces (fixed)

2. Premium Rice - 5kg Bag
   - Price: ₹450/kg
   - Stock: 45kg
   - Category: Grocery
   - UOM: Kilogram (decimal allowed)

3. Paracetamol 650mg - Box of 10
   - Price: ₹85/box
   - Stock: 75 boxes
   - Category: Pharmaceuticals
   - UOM: Box (fixed)

4. Sanitizer - 1L Bottle
   - Price: ₹180/L
   - Stock: 30L
   - Category: Grocery
   - UOM: Liter (decimal allowed)
```

### Customers (3 created)
```
1. John Doe (Retail)
   - Type: Retail cash customer
   - Credit Limit: ₹10,000

2. ABC Retail Store (Wholesale)
   - Type: Wholesale buyer
   - Credit Limit: ₹100,000
   - Discount: 5-10%

3. XYZ Corporation (Credit)
   - Type: Corporate with credit terms
   - Credit Limit: ₹50,000
```

### Purchase Orders (3 created)
```
1. TechWorld Supplies
   - Order: 5 Laptops @ ₹75,000 = ₹375,000
   - Status: Received

2. Fresh Foods Ltd
   - Order: 50kg Rice @ ₹400 + 100L Sanitizer @ ₹150
   - Status: Received

3. Pharma Distributors
   - Order: 100 boxes Paracetamol @ ₹70
   - Status: Received
```

### Transactions (8+ invoices)
```
1. Retail Cash Sale
   - 1 Laptop + 5kg Rice
   - Total: ~₹92,200
   - Payment: Cash

2. Wholesale Credit Sale
   - 50L Sanitizer + 25 boxes Paracetamol
   - Discount: ₹500 + line discounts
   - Total: ~₹9,500
   - Payment: Credit

3. Credit Company Sale
   - 1 Laptop with ₹5,000 discount
   - Total: ~₹84,999
   - Payment: Credit

... and 5+ more test invoices with various combinations
```

## Performance Benchmarks

Typical execution times on standard hardware:

| Phase | Duration | Tests |
|-------|----------|-------|
| Authentication | 1-2s | 1 |
| Master Data | 3-5s | 18 |
| Purchase Orders | 5-8s | 15 |
| Stock Management | 2-3s | 8 |
| Invoicing | 8-12s | 20 |
| Payments | 2-3s | 5 |
| Analytics | 3-5s | 10 |
| Inventory Tracking | 2-3s | 5 |
| Stress Tests | 5-8s | 15 |
| Error Handling | 3-5s | 10 |
| Final Verification | 2-3s | 8 |
| **Total** | **45-70s** | **100+** |

## Test Results Example

```
╔════════════════════════════════════════════════════════════════╗
║         GBOS ERP - Comprehensive E2E Test Suite               ║
╚════════════════════════════════════════════════════════════════╝

Phase 1: System Initialization & Authentication
▶ 1.1 Login as Admin
✓ Admin login (HTTP 200)

Phase 3: Master Data Creation
▶ 3.1 Create Product Categories
✓ Create category - Electronics (HTTP 201)
✓ Create category - Grocery (HTTP 201)
✓ Create category - Pharmaceuticals (HTTP 201)

▶ 3.2 Create Unit of Measurements
✓ Create UOM - Pieces (HTTP 201)
✓ Create UOM - Box (HTTP 201)
✓ Create UOM - Kilogram (HTTP 201)
✓ Create UOM - Liter (HTTP 201)

▶ 3.3 Create Products
✓ Create product - Laptop (HTTP 201)
✓ Create product - Rice (HTTP 201)
✓ Create product - Paracetamol (HTTP 201)
✓ Create product - Sanitizer (HTTP 201)

... [more sections] ...

Phase 13: Final Verification & Data Integrity
▶ 13.1 Verify All Created Entities
ℹ Products created: 4
ℹ Invoices created: 8
ℹ Customers created: 3

╔════════════════════════════════════════════════════════════════╗
║                     TEST SUMMARY REPORT                        ║
╠════════════════════════════════════════════════════════════════╣
║ Total Tests:      118
║ Passed:          118
║ Failed:            0
║ Success Rate:    100%
╚════════════════════════════════════════════════════════════════╝

✓ All tests passed! System is working correctly.
```

## Troubleshooting

### Backend connection fails
```bash
# Check if backend is running
lsof -i :8080

# Check API health
curl http://localhost:8080/api/v1/health
```

### Tests hang on specific phase
```bash
# Check backend logs for errors
tail -50 backend.log

# Manually test the endpoint
curl -X GET "http://localhost:8080/api/v1/products" \
  -H "Authorization: Bearer <your-token>"
```

### Database issues
```bash
# Check database file exists
ls -lah ./data/gbos.db

# Create directory if missing
mkdir -p ./data

# Reset database
bash scripts/reset-db.sh
```

## CI/CD Integration

### GitHub Actions
Add to `.github/workflows/test.yml`:
```yaml
- name: Run E2E Tests
  run: |
    cd backend
    go run cmd/server/main.go &
    sleep 5
    bash scripts/comprehensive-e2e-test.sh
```

### GitLab CI
Add to `.gitlab-ci.yml`:
```yaml
e2e_test:
  script:
    - cd backend
    - go run cmd/server/main.go &
    - sleep 5
    - bash scripts/comprehensive-e2e-test.sh
```

## Test Coverage Map

```
✓ Authentication (Login, Token)
  ├─ Valid credentials → 200 OK
  ├─ Invalid credentials → 401
  └─ Token refresh

✓ Product Management
  ├─ Create → 201
  ├─ Read → 200
  ├─ Update → 200
  ├─ Delete → 200
  └─ List with pagination → 200

✓ Inventory
  ├─ Stock receipt
  ├─ Stock deduction
  ├─ Loss recording
  ├─ Adjustment
  ├─ Batch tracking
  └─ Expiry management

✓ Sales
  ├─ Invoice creation
  ├─ Multiple items
  ├─ Discounts
  ├─ Tax calculation (CGST+SGST vs IGST)
  ├─ Payment modes
  ├─ Void with reversal
  └─ Partial deliveries

✓ Purchases
  ├─ PO creation
  ├─ Receipt
  ├─ Returns
  └─ Payment to vendor

✓ Financial
  ├─ Credit tracking
  ├─ Payment recording
  ├─ GST calculation
  ├─ P&L reports
  └─ Credit aging

✓ Analytics
  ├─ Dashboard
  ├─ Sales by category
  ├─ Top products
  ├─ Hourly trends
  ├─ Payment breakdown
  └─ Multi-store aggregation

✓ Error Handling
  ├─ Invalid inputs
  ├─ Missing fields
  ├─ Insufficient stock
  ├─ Unauthorized access
  ├─ Invalid tokens
  └─ Concurrent operations
```

## Recommended Test Schedule

**Development**: After every code change
```bash
bash scripts/comprehensive-e2e-test.sh
```

**Pre-deployment**: Before releasing to production
```bash
# 1. Reset database
bash scripts/reset-db.sh

# 2. Run full test suite multiple times
bash scripts/comprehensive-e2e-test.sh
bash scripts/comprehensive-e2e-test.sh  # Run again
bash scripts/comprehensive-e2e-test.sh  # Third time for stress
```

**Nightly**: Automated via CI/CD
- Full test suite
- Performance benchmarks
- Report generation

**Monthly**: Load testing
```bash
# Run tests with concurrent clients
for i in {1..5}; do bash scripts/comprehensive-e2e-test.sh & done
wait
```

## Success Criteria

System is production-ready when:

✓ All 100+ tests pass consistently
✓ 0 errors in error handling phase
✓ Stock calculations 100% accurate
✓ Financial totals reconcile perfectly
✓ No data integrity violations
✓ Response times within benchmarks
✓ All payment modes work
✓ Analytics queries complete
✓ Concurrent operations succeed
✓ Stress test completes without errors

## Support

For issues or questions:
1. Check test output for specific failures
2. Review E2E_TESTING_GUIDE.md for detailed scenarios
3. Examine backend logs
4. Verify database state manually
5. Check API endpoints individually

## Next Steps

1. ✓ Run `comprehensive-e2e-test.sh` to validate system
2. ✓ Review test results and fix any failures
3. ✓ Deploy backend to production
4. ✓ Run tests in production environment
5. ✓ Set up automated test scheduling
6. ✓ Monitor system performance

---

**Happy Testing! 🎉**
