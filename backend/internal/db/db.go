package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

var DB *sql.DB

func Initialize(dbPath string) error {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
	}
	var err error
	DB, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on&cache=shared")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	DB.SetMaxOpenConns(25)
	DB.SetMaxIdleConns(5)
	DB.SetConnMaxLifetime(5 * time.Minute)

	if err = createTables(); err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	if err = seedDefaults(); err != nil {
		return fmt.Errorf("failed to seed defaults: %w", err)
	}

	if err = migrateSchema(); err != nil {
		return fmt.Errorf("failed to migrate schema: %w", err)
	}

	return nil
}

func columnExists(table, col string) (bool, error) {
	rows, err := DB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func migrateSchema() error {
	ok, err := columnExists("customers", "store_id")
	if err != nil {
		return err
	}
	if !ok {
		if _, err := DB.Exec(`ALTER TABLE customers ADD COLUMN store_id TEXT REFERENCES stores(id)`); err != nil {
			return fmt.Errorf("customers.store_id: %w", err)
		}
	}
	if _, err := DB.Exec(`UPDATE customers SET store_id = (SELECT id FROM stores ORDER BY datetime(created_at) LIMIT 1) WHERE store_id IS NULL OR store_id = ''`); err != nil {
		return err
	}

	ok, err = columnExists("product_prices", "wholesale_sell_price")
	if err != nil {
		return err
	}
	if !ok {
		if _, err := DB.Exec(`ALTER TABLE product_prices ADD COLUMN wholesale_sell_price REAL DEFAULT 0`); err != nil {
			return fmt.Errorf("product_prices.wholesale_sell_price: %w", err)
		}
	}

	if _, err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_stock_product_store_batch ON stock(product_id, store_id, batch_no)`); err != nil {
		log.Printf("gbos migrate: could not create unique stock index (remove duplicate batches if needed): %v", err)
	}
	if _, err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_product_prices_product_store ON product_prices(product_id, store_id)`); err != nil {
		log.Printf("gbos migrate: could not create unique product_prices index: %v", err)
	}

	if _, err := DB.Exec(`
		CREATE TABLE IF NOT EXISTS product_units (
			id TEXT PRIMARY KEY,
			product_id TEXT NOT NULL,
			parent_unit_id TEXT,
			label TEXT NOT NULL,
			qty_in_parent REAL NOT NULL DEFAULT 1,
			barcode TEXT,
			sort_order INTEGER DEFAULT 0,
			created_at TEXT DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE CASCADE,
			FOREIGN KEY (parent_unit_id) REFERENCES product_units(id)
		)`); err != nil {
		return fmt.Errorf("product_units table: %w", err)
	}
	if _, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_product_units_product ON product_units(product_id)`); err != nil {
		log.Printf("gbos migrate: product_units index: %v", err)
	}

	if ok, err := columnExists("stores", "location_type"); err != nil {
		return err
	} else if !ok {
		if _, err := DB.Exec(`ALTER TABLE stores ADD COLUMN location_type TEXT DEFAULT 'store'`); err != nil {
			return fmt.Errorf("stores.location_type: %w", err)
		}
	}

	if ok, err := columnExists("customers", "is_wholesale"); err != nil {
		return err
	} else if !ok {
		if _, err := DB.Exec(`ALTER TABLE customers ADD COLUMN is_wholesale INTEGER DEFAULT 0`); err != nil {
			return fmt.Errorf("customers.is_wholesale: %w", err)
		}
	}

	if ok, err := columnExists("vendors", "credit_days"); err != nil {
		return err
	} else if !ok {
		if _, err := DB.Exec(`ALTER TABLE vendors ADD COLUMN credit_days INTEGER DEFAULT 0`); err != nil {
			return fmt.Errorf("vendors.credit_days: %w", err)
		}
	}
	if ok, err := columnExists("vendors", "credit_limit"); err != nil {
		return err
	} else if !ok {
		if _, err := DB.Exec(`ALTER TABLE vendors ADD COLUMN credit_limit REAL DEFAULT 0`); err != nil {
			return fmt.Errorf("vendors.credit_limit: %w", err)
		}
	}

	if ok, err := columnExists("invoice_items", "unit_cost_at_sale"); err != nil {
		return err
	} else if !ok {
		if _, err := DB.Exec(`ALTER TABLE invoice_items ADD COLUMN unit_cost_at_sale REAL NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("invoice_items.unit_cost_at_sale: %w", err)
		}
		// Best-effort backfill for legacy rows (ambiguous batch; uses any matching stock row).
		if _, err := DB.Exec(`
			UPDATE invoice_items SET unit_cost_at_sale = COALESCE((
				SELECT s.cost_price FROM invoices i
				JOIN stock s ON s.product_id = invoice_items.product_id AND s.store_id = i.store_id
				WHERE i.id = invoice_items.invoice_id
				LIMIT 1
			), 0)
			WHERE unit_cost_at_sale = 0`); err != nil {
			log.Printf("gbos migrate: invoice_items cost backfill: %v", err)
		}
	}

	return nil
}

func createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS stores (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		address TEXT,
		phone TEXT,
		gstin TEXT,
		location_type TEXT DEFAULT 'store',
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL,
		store_id TEXT,
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (store_id) REFERENCES stores(id)
	);

	CREATE TABLE IF NOT EXISTS categories (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		name_hi TEXT,
		name_kn TEXT,
		parent_id TEXT,
		hsn_code TEXT,
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (parent_id) REFERENCES categories(id)
	);

	CREATE TABLE IF NOT EXISTS units_of_measure (
		id TEXT PRIMARY KEY,
		code TEXT NOT NULL,
		name TEXT NOT NULL,
		decimal_places INTEGER DEFAULT 0,
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS products (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		name_hi TEXT,
		name_kn TEXT,
		category_id TEXT,
		hsn_code TEXT,
		barcode TEXT,
		uom_id TEXT,
		base_unit_qty REAL DEFAULT 1,
		gst_rate REAL DEFAULT 0,
		is_free INTEGER DEFAULT 0,
		is_active INTEGER DEFAULT 1,
		allow_decimal INTEGER DEFAULT 0,
		image_url TEXT,
		min_stock REAL DEFAULT 0,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (category_id) REFERENCES categories(id),
		FOREIGN KEY (uom_id) REFERENCES units_of_measure(id)
	);

	CREATE TABLE IF NOT EXISTS product_units (
		id TEXT PRIMARY KEY,
		product_id TEXT NOT NULL,
		parent_unit_id TEXT,
		label TEXT NOT NULL,
		qty_in_parent REAL NOT NULL DEFAULT 1,
		barcode TEXT,
		sort_order INTEGER DEFAULT 0,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE CASCADE,
		FOREIGN KEY (parent_unit_id) REFERENCES product_units(id)
	);

	CREATE INDEX IF NOT EXISTS idx_product_units_product ON product_units(product_id);

	CREATE TABLE IF NOT EXISTS product_prices (
		id TEXT PRIMARY KEY,
		product_id TEXT NOT NULL,
		store_id TEXT NOT NULL,
		mrp REAL DEFAULT 0,
		cost_price REAL DEFAULT 0,
		sell_price REAL DEFAULT 0,
		wholesale_sell_price REAL DEFAULT 0,
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (product_id) REFERENCES products(id),
		FOREIGN KEY (store_id) REFERENCES stores(id)
	);

	CREATE TABLE IF NOT EXISTS stock (
		id TEXT PRIMARY KEY,
		product_id TEXT NOT NULL,
		store_id TEXT NOT NULL,
		batch_no TEXT,
		quantity REAL DEFAULT 0,
		reserved_qty REAL DEFAULT 0,
		expiry_date TEXT,
		mfg_date TEXT,
		cost_price REAL DEFAULT 0,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (product_id) REFERENCES products(id),
		FOREIGN KEY (store_id) REFERENCES stores(id)
	);

	CREATE TABLE IF NOT EXISTS customers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		name_hi TEXT,
		phone TEXT,
		address TEXT,
		gstin TEXT,
		credit_limit REAL DEFAULT 0,
		is_wholesale INTEGER DEFAULT 0,
		store_id TEXT,
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (store_id) REFERENCES stores(id)
	);

	CREATE TABLE IF NOT EXISTS vendors (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		phone TEXT,
		address TEXT,
		gstin TEXT,
		state TEXT,
		credit_days INTEGER DEFAULT 0,
		credit_limit REAL DEFAULT 0,
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS invoices (
		id TEXT PRIMARY KEY,
		invoice_no TEXT UNIQUE NOT NULL,
		store_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		customer_id TEXT,
		invoice_date TEXT NOT NULL,
		sub_total REAL DEFAULT 0,
		cgst REAL DEFAULT 0,
		sgst REAL DEFAULT 0,
		igst REAL DEFAULT 0,
		discount REAL DEFAULT 0,
		round_off REAL DEFAULT 0,
		total REAL DEFAULT 0,
		status TEXT DEFAULT 'completed',
		payment_mode TEXT DEFAULT 'cash',
		is_gstr1_filed INTEGER DEFAULT 0,
		gstr1_month TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (store_id) REFERENCES stores(id),
		FOREIGN KEY (user_id) REFERENCES users(id),
		FOREIGN KEY (customer_id) REFERENCES customers(id)
	);

	CREATE TABLE IF NOT EXISTS invoice_items (
		id TEXT PRIMARY KEY,
		invoice_id TEXT NOT NULL,
		product_id TEXT NOT NULL,
		batch_no TEXT,
		quantity REAL DEFAULT 0,
		rate REAL DEFAULT 0,
		discount REAL DEFAULT 0,
		cgst REAL DEFAULT 0,
		sgst REAL DEFAULT 0,
		igst REAL DEFAULT 0,
		total REAL DEFAULT 0,
		unit_cost_at_sale REAL NOT NULL DEFAULT 0,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (invoice_id) REFERENCES invoices(id),
		FOREIGN KEY (product_id) REFERENCES products(id)
	);

	CREATE TABLE IF NOT EXISTS purchase_orders (
		id TEXT PRIMARY KEY,
		po_no TEXT UNIQUE NOT NULL,
		store_id TEXT NOT NULL,
		vendor_id TEXT NOT NULL,
		order_date TEXT NOT NULL,
		expected_date TEXT,
		sub_total REAL DEFAULT 0,
		cgst REAL DEFAULT 0,
		sgst REAL DEFAULT 0,
		igst REAL DEFAULT 0,
		discount REAL DEFAULT 0,
		total REAL DEFAULT 0,
		status TEXT DEFAULT 'pending',
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (store_id) REFERENCES stores(id),
		FOREIGN KEY (vendor_id) REFERENCES vendors(id)
	);

	CREATE TABLE IF NOT EXISTS purchase_items (
		id TEXT PRIMARY KEY,
		po_id TEXT NOT NULL,
		product_id TEXT NOT NULL,
		quantity REAL DEFAULT 0,
		uom_id TEXT,
		received_qty REAL DEFAULT 0,
		rate REAL DEFAULT 0,
		discount REAL DEFAULT 0,
		cgst REAL DEFAULT 0,
		sgst REAL DEFAULT 0,
		igst REAL DEFAULT 0,
		total REAL DEFAULT 0,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (po_id) REFERENCES purchase_orders(id),
		FOREIGN KEY (product_id) REFERENCES products(id),
		FOREIGN KEY (uom_id) REFERENCES units_of_measure(id)
	);

	CREATE TABLE IF NOT EXISTS credit_entries (
		id TEXT PRIMARY KEY,
		customer_id TEXT NOT NULL,
		invoice_id TEXT,
		amount REAL DEFAULT 0,
		type TEXT NOT NULL,
		due_date TEXT,
		paid_amount REAL DEFAULT 0,
		status TEXT DEFAULT 'pending',
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (customer_id) REFERENCES customers(id),
		FOREIGN KEY (invoice_id) REFERENCES invoices(id)
	);

	CREATE TABLE IF NOT EXISTS sync_records (
		id TEXT PRIMARY KEY,
		table_name TEXT NOT NULL,
		record_id TEXT NOT NULL,
		operation TEXT NOT NULL,
		store_id TEXT NOT NULL,
		status TEXT DEFAULT 'pending',
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		synced_at TEXT,
		FOREIGN KEY (store_id) REFERENCES stores(id)
	);

	CREATE TABLE IF NOT EXISTS audit_logs (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		store_id TEXT NOT NULL,
		action TEXT NOT NULL,
		table_name TEXT,
		record_id TEXT,
		old_value TEXT,
		new_value TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id),
		FOREIGN KEY (store_id) REFERENCES stores(id)
	);

	CREATE INDEX IF NOT EXISTS idx_products_barcode ON products(barcode);
	CREATE INDEX IF NOT EXISTS idx_products_category ON products(category_id);
	CREATE INDEX IF NOT EXISTS idx_stock_product ON stock(product_id, store_id);
	CREATE INDEX IF NOT EXISTS idx_invoices_date ON invoices(invoice_date);
	CREATE INDEX IF NOT EXISTS idx_credit_customer ON credit_entries(customer_id);
	CREATE INDEX IF NOT EXISTS idx_sync_pending ON sync_records(status, created_at);

	CREATE TABLE IF NOT EXISTS invoice_payments (
		id TEXT PRIMARY KEY,
		invoice_id TEXT NOT NULL,
		payment_mode TEXT NOT NULL,
		amount REAL NOT NULL,
		reference TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (invoice_id) REFERENCES invoices(id)
	);

	CREATE TABLE IF NOT EXISTS returns (
		id TEXT PRIMARY KEY,
		invoice_id TEXT,
		product_id TEXT NOT NULL,
		store_id TEXT NOT NULL,
		quantity REAL NOT NULL,
		amount REAL NOT NULL,
		reason TEXT NOT NULL,
		refund_mode TEXT,
		status TEXT DEFAULT 'pending',
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (invoice_id) REFERENCES invoices(id),
		FOREIGN KEY (product_id) REFERENCES products(id),
		FOREIGN KEY (store_id) REFERENCES stores(id)
	);

	CREATE TABLE IF NOT EXISTS inventory_losses (
		id TEXT PRIMARY KEY,
		product_id TEXT NOT NULL,
		store_id TEXT NOT NULL,
		batch_no TEXT,
		quantity REAL NOT NULL,
		loss_type TEXT NOT NULL,
		reason TEXT,
		user_id TEXT NOT NULL,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (product_id) REFERENCES products(id),
		FOREIGN KEY (store_id) REFERENCES stores(id),
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS goods_receipts (
		id TEXT PRIMARY KEY,
		po_id TEXT NOT NULL,
		received_date TEXT NOT NULL,
		received_by TEXT NOT NULL,
		notes TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (po_id) REFERENCES purchase_orders(id)
	);

	CREATE TABLE IF NOT EXISTS goods_receipt_items (
		id TEXT PRIMARY KEY,
		gr_id TEXT NOT NULL,
		product_id TEXT NOT NULL,
		ordered_qty REAL,
		received_qty REAL NOT NULL,
		accepted_qty REAL,
		rejected_qty REAL,
		reason TEXT,
		FOREIGN KEY (gr_id) REFERENCES goods_receipts(id),
		FOREIGN KEY (product_id) REFERENCES products(id)
	);

	CREATE TABLE IF NOT EXISTS expense_categories (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		is_active INTEGER DEFAULT 1,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS expenses (
		id TEXT PRIMARY KEY,
		store_id TEXT NOT NULL,
		category_id TEXT NOT NULL,
		amount REAL NOT NULL,
		description TEXT,
		expense_date TEXT NOT NULL,
		user_id TEXT NOT NULL,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (store_id) REFERENCES stores(id),
		FOREIGN KEY (category_id) REFERENCES expense_categories(id),
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS stock_adjustments (
		id TEXT PRIMARY KEY,
		product_id TEXT NOT NULL,
		store_id TEXT NOT NULL,
		batch_no TEXT,
		quantity REAL NOT NULL,
		adjustment_type TEXT NOT NULL,
		reason TEXT,
		user_id TEXT NOT NULL,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (product_id) REFERENCES products(id),
		FOREIGN KEY (store_id) REFERENCES stores(id),
		FOREIGN KEY (user_id) REFERENCES users(id)
	);
	`
	_, err := DB.Exec(schema)
	return err
}

func seedDefaults() error {
	var count int
	err := DB.QueryRow("SELECT COUNT(*) FROM stores").Scan(&count)
	if err != nil || count > 0 {
		return err
	}

	storeID := uuid.New().String()
	_, err = DB.Exec(`INSERT INTO stores (id, name, address, phone, gstin, location_type) VALUES (?, 'Main Store', 'Address', '1234567890', '29ABCDE1234F1Z5', 'store')`,
		storeID)
	if err != nil {
		return err
	}

	passwordHash, _ := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	adminID := uuid.New().String()
	_, err = DB.Exec(`INSERT INTO users (id, username, password_hash, role, store_id) VALUES (?, 'admin', ?, 'owner', ?)`,
		adminID, string(passwordHash), storeID)
	if err != nil {
		return err
	}

	_, err = DB.Exec(`INSERT INTO units_of_measure (id, code, name, decimal_places) VALUES 
		(?, 'NOS', 'Numbers', 0),
		(?, 'KG', 'Kilogram', 3),
		(?, 'G', 'Gram', 3),
		(?, 'L', 'Liter', 3),
		(?, 'ML', 'Milliliter', 3),
		(?, 'PKT', 'Packet', 0),
		(?, 'BOX', 'Box', 0)`,
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String())
	if err != nil {
		return err
	}

	catID := uuid.New().String()
	_, err = DB.Exec(`INSERT INTO categories (id, name, name_hi, name_kn, hsn_code) VALUES 
		(?, 'Groceries', 'किराना', 'ತರಕಾರಾ', '1001'),
		(?, 'Dals & Pulses', 'दालें', 'ಬೆಳ್ಚಿನಹಿಂದಿ', '1008'),
		(?, 'Oil & Ghee', 'तेल', 'ಎಣ್ಣೆ', '1501'),
		(?, 'Spices', 'मसाले', 'ಮಸಾಲಾ', '0910'),
		(?, 'Snacks', 'स्नैक्स', 'ತಿಂತಾರ', '1901'),
		(?, 'Beverages', 'पेय', 'ಪಾನಾಯಗಳು', '2201'),
		(?, 'Household', 'घरेलू', 'ಮನೆಕಾರ್ಯ', '3403'),
		(?, 'Personal Care', 'व्यक्तिगत देखभाल', 'ವೈಯಕ್ತಿಕ ಕಾಳಜಿ', '3401')`,
		catID,
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String())
	if err != nil {
		return err
	}

	_, err = DB.Exec(`INSERT INTO expense_categories (id, name) VALUES 
		(?, 'Rent'),
		(?, 'Electricity'),
		(?, 'Water'),
		(?, 'Salaries'),
		(?, 'Transportation'),
		(?, 'Packaging'),
		(?, 'Maintenance'),
		(?, 'Marketing'),
		(?, 'Bank Charges'),
		(?, 'Insurance'),
		(?, 'Taxes'),
		(?, 'Miscellaneous')`,
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String())
	if err != nil {
		return err
	}

	var uomNos string
	if err = DB.QueryRow(`SELECT id FROM units_of_measure WHERE code = 'NOS' LIMIT 1`).Scan(&uomNos); err != nil {
		return err
	}

	demo := []struct {
		name    string
		barcode string
		gst     float64
		sell    float64
	}{
		{"Demo Atta 1kg", "8901001100001", 5, 55},
		{"Demo Sugar 1kg", "8901001100002", 5, 45},
		{"Demo Sunflower Oil 1L", "8901001100003", 5, 180},
	}
	for _, dp := range demo {
		pid := uuid.New().String()
		_, err = DB.Exec(`INSERT INTO products (id, name, category_id, barcode, uom_id, base_unit_qty, gst_rate, allow_decimal, min_stock) VALUES (?,?,?,?,?,?,?,?,?)`,
			pid, dp.name, catID, dp.barcode, uomNos, 1, dp.gst, 1, 2)
		if err != nil {
			return err
		}
		priceID := uuid.New().String()
		_, err = DB.Exec(`INSERT INTO product_prices (id, product_id, store_id, mrp, cost_price, sell_price, wholesale_sell_price) VALUES (?,?,?,?,?,?,?)`,
			priceID, pid, storeID, dp.sell*1.1, dp.sell*0.75, dp.sell, dp.sell*0.92)
		if err != nil {
			return err
		}
		stockID := uuid.New().String()
		_, err = DB.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price) VALUES (?,?,?,?,?,?)`,
			stockID, pid, storeID, "DEMO-BATCH-1", 500, dp.sell*0.75)
		if err != nil {
			return err
		}
	}

	cashHash, _ := bcrypt.GenerateFromPassword([]byte("cashier123"), bcrypt.DefaultCost)
	cashID := uuid.New().String()
	_, err = DB.Exec(`INSERT INTO users (id, username, password_hash, role, store_id) VALUES (?, 'cashier', ?, 'cashier', ?)`,
		cashID, string(cashHash), storeID)
	if err != nil {
		return err
	}

	helperHash, _ := bcrypt.GenerateFromPassword([]byte("helper123"), bcrypt.DefaultCost)
	helperID := uuid.New().String()
	_, err = DB.Exec(`INSERT INTO users (id, username, password_hash, role, store_id) VALUES (?, 'helper', ?, 'helper', ?)`,
		helperID, string(helperHash), storeID)
	if err != nil {
		return err
	}

	custID := uuid.New().String()
	_, err = DB.Exec(`INSERT INTO customers (id, name, phone, credit_limit, is_wholesale, store_id) VALUES (?, 'Demo Wholesale Mart', '9990001111', 50000, 1, ?)`,
		custID, storeID)
	if err != nil {
		return err
	}

	vendID := uuid.New().String()
	_, err = DB.Exec(`INSERT INTO vendors (id, name, phone, gstin, state, credit_days, credit_limit) VALUES (?, 'Demo Supplier Co', '9888877776', '27AAAAA0000A1Z5', 'KA', 15, 100000)`,
		vendID)
	if err != nil {
		return err
	}

	log.Println("gbos: seeded demo data — admin/admin123, cashier/cashier123, helper/helper123; barcodes 8901001100001–003; Demo Wholesale Mart (credit/wholesale)")
	return nil
}

func Close() {
	if DB != nil {
		DB.Close()
	}
}
