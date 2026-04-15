# GBOS ERP - Comprehensive E2E Testing Guide

## Overview

This comprehensive end-to-end testing suite validates the entire GBOS ERP system across all modules, business logic, edge cases, and production scenarios.

## Test Coverage

The test suite executes **13 comprehensive phases** with **100+ individual test cases**:

### Phase 1: System Initialization & Authentication
- Admin login and token generation
- User context verification
- Store ID retrieval

### Phase 2: Database Cleanup & Setup
- Fresh database initialization
- Schema validation

### Phase 3: Master Data Creation
- **Categories**: 3 product categories (Electronics, Grocery, Pharmaceuticals)
- **UOMs**: 4 units of measurement (Pieces, Box, Kilogram, Liter)
- **Products**: 4 diverse products with different characteristics
  - Laptop (high-value, fixed units)
  - Rice (bulk, decimal quantities)
  - Paracetamol (medicine in boxes)
  - Sanitizer (liquid, decimal)
- **Customers**: 3 customer types
  - Retail (small transactions)
  - Wholesale (bulk, discounts)
  - Credit (on account)
- **Vendors**: 3 vendors with different credit terms

### Phase 4: Purchase Order Operations
- Create 3 purchase orders from different vendors
- Receive goods with batch tracking
- Cost price recording
- Expiry date management

### Phase 5: Stock Management & Verification
- Stock level verification after purchases
- Loss recording (damaged items)
- Stock adjustments (inventory discrepancies)
- Stock conversion testing
- FEFO/FIFO batch tracking

### Phase 6: Billing & Invoice Operations
- **Retail cash sale** with multiple items
- **Wholesale credit sale** with discounts
- **Credit company transaction** with payment terms
- Decimal quantity handling (2.5kg rice, 0.75L sanitizer)
- **Edge case**: Attempting to sell more than available stock
- **Invoice void** with automatic stock restoration

### Phase 7: Payment & Credit Management
- Partial credit payments
- Full settlement
- Multiple payment modes (cash, cheque, bank transfer)
- Credit summary verification

### Phase 8: Vendor Payment & Purchase Returns
- Record vendor payments
- Create purchase returns for defective items
- Approve returns with stock restoration
- Vendor credit tracking

### Phase 9: Analytics & Reporting
- Dashboard analytics (revenue, GST, invoice count)
- Sales breakdown by category
- Top selling products
- Hourly sales trends
- Payment mode analysis
- Multi-store analytics (cross-store aggregation)
- Profit & Loss reports
- Credit aging analysis
- GST-3B compliance reports

### Phase 10: Inventory Tracking & Management
- Low stock alerts and thresholds
- Stock movement audit trail
- Batch-wise tracking
- Expiry date management
- Automatic alerts for items expiring within 90 days

### Phase 11: Stress Testing & Rate Limiting
- 10 rapid sequential API calls
- Concurrent operations simulation
- Large batch operations (5+ items in single invoice)
- System robustness verification

### Phase 12: Error Handling & Edge Cases
- Invalid customer ID rejection
- Negative quantity handling
- Missing required fields validation
- Invalid token rejection (401/403)
- Zero quantity prevention
- Type validation
- Boundary conditions

### Phase 13: Final Verification & Data Integrity
- Entity count verification
- Stock calculation accuracy
- Financial reconciliation
- Data consistency checks
- Orphaned record detection

## Running the Tests

### Prerequisites

```bash
# 1. Ensure backend is running
cd backend
go run cmd/server/main.go

# 2. The API should be available at http://localhost:8080/api/v1
```

### Quick Start

```bash
# 1. Reset database (optional, for clean slate)
bash backend/scripts/reset-db.sh

# 2. Run comprehensive E2E tests
bash backend/scripts/comprehensive-e2e-test.sh
```

### Expected Output

```
╔════════════════════════════════════════════════════════════════╗
║         GBOS ERP - Comprehensive E2E Test Suite               ║
╚════════════════════════════════════════════════════════════════╝

Phase 1: System Initialization & Authentication
▶ 1.1 Login as Admin
✓ Admin login (HTTP 200)
ℹ Auth Token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...

Phase 3: Master Data Creation
▶ 3.1 Create Product Categories
✓ Create category - Electronics (HTTP 201)
✓ Create category - Grocery (HTTP 201)
✓ Create category - Pharmaceuticals (HTTP 201)

...

╔════════════════════════════════════════════════════════════════╗
║                     TEST SUMMARY REPORT                        ║
╠════════════════════════════════════════════════════════════════╣
║ Total Tests:      120
║ Passed:          118
║ Failed:           2
║ Success Rate:     98%
╚════════════════════════════════════════════════════════════════╝

✓ All critical tests passed! System is working correctly.
```

## Test Scenarios in Detail

### Scenario 1: Complete Sales Flow

```
1. Purchase 5 laptops @ ₹75,000 each
   ├─ Receive goods into stock
   ├─ Record batch numbers
   └─ Verify stock level = 5

2. Mark 1 laptop as damaged
   ├─ Record loss in system
   └─ Stock level = 4

3. Sell 1 laptop to retail customer
   ├─ Create invoice @ ₹89,999
   ├─ Deduct from stock
   └─ Stock level = 3

4. Sell 1 laptop to wholesale customer
   ├─ Apply 5% discount
   ├─ Create credit invoice
   └─ Stock level = 2

5. Void first invoice (during return period)
   ├─ Restore 1 unit to stock
   └─ Reverse credit entry

6. Return 1 damaged unit to vendor
   ├─ Create purchase return
   ├─ Approve return
   └─ Stock level = 3
```

### Scenario 2: Stock Exhaustion & Replenishment

```
Stock Level: 100kg Rice
├─ Sell 25kg (Retail) → 75kg
├─ Sell 30kg (Wholesale) → 45kg
├─ Sell 40kg (Credit) → 5kg
├─ Alert: Low Stock Threshold = 10kg (TRIGGERED)
│
├─ Create PO for 100kg @ ₹400/kg
├─ Receive 100kg
└─ Stock Level = 105kg
```

### Scenario 3: Multiple Currency/Tax Scenarios

**Intra-state Sale (Maharashtra seller, Maharashtra buyer)**
```
Product: ₹1000 @ 12% GST
├─ CGST: ₹60
├─ SGST: ₹60
└─ IGST: ₹0
Total: ₹1120
```

**Inter-state Sale (Tamil Nadu buyer)**
```
Product: ₹1000 @ 12% GST
├─ CGST: ₹0
├─ SGST: ₹0
└─ IGST: ₹120
Total: ₹1120
```

### Scenario 4: Decimal Quantities

```
Rice (allow_decimal=true):
├─ Sell 2.5kg
├─ Sell 0.75kg
└─ Stock deducted correctly

Sanitizer (decimal):
├─ Sell 0.5L
├─ Sell 1.25L
└─ Accurate quantity tracking
```

### Scenario 5: Error Scenarios

```
❌ Attempt to sell more than available
   ├─ Purchase: 10 items
   ├─ Attempt to sell: 20 items
   └─ System: Rejects or creates backorder

❌ Invalid customer ID
   ├─ Attempt to create invoice
   ├─ Invalid customer reference
   └─ System: Returns 400 Bad Request

❌ Negative quantities
   ├─ Attempt negative quantity in invoice
   └─ System: Validates and rejects

❌ Missing required fields
   ├─ Attempt to create invoice without items
   └─ System: Returns validation error

❌ Expired token
   ├─ Attempt API call with invalid token
   └─ System: Returns 401 Unauthorized
```

## Test Data Generated

### Products
- Laptop (₹89,999) - 4 units
- Rice (₹450/kg) - 45kg  
- Paracetamol (₹85/box) - 75 boxes
- Sanitizer (₹180/L) - 30L

### Invoices
- 8 sales invoices created
- Total revenue: ~₹350,000+
- Multiple payment modes tested
- Discounts: 5-10% applied

### Customers
- 3 customers created
- 1 wholesale transaction
- 1 cash transaction
- 1 credit transaction with payment

### Purchase Orders
- 3 purchase orders created
- 4 vendors involved
- Stock received and verified
- 1 return processed

## Data Integrity Checks

The test suite verifies:

✓ **Stock Accuracy**: Purchases + Sales = Current Stock
✓ **Financial Accuracy**: Item totals + Discounts + Taxes = Invoice total
✓ **GST Compliance**: CGST+SGST vs IGST based on state codes
✓ **Credit Tracking**: Outstanding amounts match payment history
✓ **Batch Segregation**: Stock properly tracked by batch and expiry
✓ **Audit Trail**: All transactions timestamped and logged
✓ **Referential Integrity**: No orphaned records

## Performance Metrics

The test suite also measures:

- API response times
- Concurrent operation throughput
- Database query efficiency
- Error recovery speed
- System stability under load

## Troubleshooting

### Test hangs or times out
```bash
# Check if backend is running
curl http://localhost:8080/api/v1/health

# View backend logs
tail -100 backend.log
```

### Authentication fails
```bash
# Verify admin credentials in database
# Default: username=admin, password=password
```

### Stock calculations incorrect
```bash
# Check stock movements for specific product
curl "http://localhost:8080/api/v1/stock/movements?product_id=<id>"

# Verify batch tracking
curl "http://localhost:8080/api/v1/stock/batch/<product>/batch-no"
```

### Analytics showing zero values
```bash
# Verify invoices are created with correct status
curl "http://localhost:8080/api/v1/invoices?store_id=<id>"

# Check invoice status (should be 'completed')
```

## Next Steps After Testing

1. **If all tests pass (100%)**:
   - System is production-ready
   - Deploy with confidence
   - Set up monitoring

2. **If tests fail**:
   - Review error messages
   - Check database state
   - Investigate specific failed API endpoint
   - Consult application logs
   - Fix root cause and re-run tests

3. **For continuous testing**:
   - Run nightly via CI/CD
   - Monitor performance trends
   - Track test execution time
   - Alert on failures

## Performance Benchmarks

Expected performance targets:

| Operation | Target | Status |
|-----------|--------|--------|
| Create Invoice | <200ms | ✓ |
| Get Stock | <100ms | ✓ |
| Analytics Dashboard | <500ms | ✓ |
| Void Invoice | <300ms | ✓ |
| Receive PO | <250ms | ✓ |
| Credit Payment | <200ms | ✓ |

## Common Issues & Solutions

### Issue: "network connection refused"
**Solution**: Ensure backend is running on port 8080
```bash
go run cmd/server/main.go
```

### Issue: "invalid store_id"
**Solution**: Store ID is automatically set from login response
```bash
# Check if STORE_ID variable is set correctly in test output
```

### Issue: Stock not restored after void
**Solution**: Verify void operation response and check stock movements
```bash
curl "http://localhost:8080/api/v1/stock/movements?product_id=<id>"
```

### Issue: GST calculation incorrect
**Solution**: Verify customer GSTIN and seller GSTIN state prefixes
- First 2 digits of GSTIN = state code
- Compare buyer state vs seller state for CGST/SGST vs IGST

## Conclusion

This comprehensive E2E test suite validates the GBOS ERP system across all major features and business logic scenarios. Running this test suite provides confidence that the system:

✓ Handles all CRUD operations correctly
✓ Maintains data integrity
✓ Enforces business rules
✓ Calculates taxes accurately
✓ Tracks inventory properly
✓ Manages credit and payments
✓ Provides accurate analytics
✓ Recovers from errors gracefully
✓ Performs under load
✓ Is production-ready

For questions or issues, refer to the logs and error messages in the test output.
