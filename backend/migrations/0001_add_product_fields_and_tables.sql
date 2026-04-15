-- Add brand, description, sac_code to products
ALTER TABLE products ADD COLUMN brand TEXT DEFAULT '';
ALTER TABLE products ADD COLUMN description TEXT DEFAULT '';
ALTER TABLE products ADD COLUMN sac_code TEXT DEFAULT '';

-- Vendor payments table (accounts payable)
CREATE TABLE IF NOT EXISTS vendor_payments (
    id TEXT PRIMARY KEY,
    vendor_id TEXT NOT NULL,
    po_id TEXT,
    amount REAL NOT NULL,
    payment_mode TEXT NOT NULL DEFAULT 'bank',
    reference TEXT,
    payment_date TEXT NOT NULL,
    notes TEXT,
    user_id TEXT NOT NULL,
    store_id TEXT NOT NULL,
    created_at TEXT DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (vendor_id) REFERENCES vendors(id),
    FOREIGN KEY (po_id) REFERENCES purchase_orders(id),
    FOREIGN KEY (user_id) REFERENCES users(id),
    FOREIGN KEY (store_id) REFERENCES stores(id)
);

-- Purchase returns
CREATE TABLE IF NOT EXISTS purchase_returns (
    id TEXT PRIMARY KEY,
    po_id TEXT,
    vendor_id TEXT NOT NULL,
    store_id TEXT NOT NULL,
    return_date TEXT NOT NULL,
    total REAL DEFAULT 0,
    status TEXT DEFAULT 'pending',
    reason TEXT,
    user_id TEXT NOT NULL,
    created_at TEXT DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (po_id) REFERENCES purchase_orders(id),
    FOREIGN KEY (vendor_id) REFERENCES vendors(id),
    FOREIGN KEY (store_id) REFERENCES stores(id),
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS purchase_return_items (
    id TEXT PRIMARY KEY,
    return_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    quantity REAL NOT NULL,
    rate REAL DEFAULT 0,
    total REAL DEFAULT 0,
    created_at TEXT DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (return_id) REFERENCES purchase_returns(id),
    FOREIGN KEY (product_id) REFERENCES products(id)
)
