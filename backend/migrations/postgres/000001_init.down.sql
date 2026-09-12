-- Reverses 000001_init.up.sql.
--
-- Tables are dropped in reverse dependency order (children before the parents
-- they reference), then the enum types. DROP TABLE is used without CASCADE
-- deliberately: if the order were wrong, Postgres raises a dependency error
-- rather than silently taking unrelated objects with it. Indexes and
-- constraints are owned by their tables and go with them.

BEGIN;

-- Numbering and idempotency
DROP TABLE IF EXISTS processed_requests;
DROP TABLE IF EXISTS document_sequences;

-- Audit and sync
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS sync_records;

-- Expenses
DROP TABLE IF EXISTS expenses;
DROP TABLE IF EXISTS expense_categories;

-- Returns, losses, adjustments
DROP TABLE IF EXISTS stock_adjustments;
DROP TABLE IF EXISTS inventory_losses;
DROP TABLE IF EXISTS returns;

-- Customer credit ledger
DROP TABLE IF EXISTS credit_entries;

-- Purchasing
DROP TABLE IF EXISTS vendor_payments;
DROP TABLE IF EXISTS purchase_return_items;
DROP TABLE IF EXISTS purchase_returns;
DROP TABLE IF EXISTS goods_receipt_items;
DROP TABLE IF EXISTS goods_receipts;
DROP TABLE IF EXISTS purchase_items;
DROP TABLE IF EXISTS purchase_orders;

-- Sales
DROP TABLE IF EXISTS invoice_payments;
DROP TABLE IF EXISTS invoice_items;
DROP TABLE IF EXISTS invoices;

-- Parties
DROP TABLE IF EXISTS vendors;
DROP TABLE IF EXISTS customers;

-- Stock and catalogue
DROP TABLE IF EXISTS stock;
DROP TABLE IF EXISTS product_prices;
DROP TABLE IF EXISTS product_units;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS units_of_measure;
DROP TABLE IF EXISTS categories;

-- Stores, users, terminals
DROP TABLE IF EXISTS terminals;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS stores;

-- Enum types last: nothing may still reference them at this point.
DROP TYPE IF EXISTS document_type;
DROP TYPE IF EXISTS sync_status;
DROP TYPE IF EXISTS purchase_return_status;
DROP TYPE IF EXISTS return_status;
DROP TYPE IF EXISTS purchase_order_status;
DROP TYPE IF EXISTS credit_entry_type;
DROP TYPE IF EXISTS credit_entry_status;
DROP TYPE IF EXISTS invoice_status;

COMMIT;
