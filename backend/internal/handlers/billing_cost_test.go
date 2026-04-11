package handlers

import (
	"path/filepath"
	"testing"

	"gbos/internal/auth"
	"gbos/internal/db"
)

func TestDeductStockFromSale_ReturnsBatchCost(t *testing.T) {
	auth.Configure("test-secret-phase-a", 0)
	tmp := filepath.Join(t.TempDir(), "cost.db")
	if err := db.Initialize(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	var pid, sid string
	if err := db.DB.QueryRow(`SELECT id FROM products LIMIT 1`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT id FROM stores LIMIT 1`).Scan(&sid); err != nil {
		t.Fatal(err)
	}

	_, err := db.DB.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price) VALUES (lower(hex(randomblob(16))), ?, ?, 'UNIT-TEST-B2', 50, 99.5)`,
		pid, sid)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	batch, cost, err := deductStockFromSale(tx, pid, sid, "UNIT-TEST-B2", 2)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if batch != "UNIT-TEST-B2" {
		t.Fatalf("batch %q", batch)
	}
	if cost != 99.5 {
		t.Fatalf("cost %v", cost)
	}
	_ = tx.Commit()

	var q float64
	_ = db.DB.QueryRow(`SELECT quantity FROM stock WHERE product_id = ? AND store_id = ? AND batch_no = 'UNIT-TEST-B2'`, pid, sid).Scan(&q)
	if q != 48 {
		t.Fatalf("qty after deduct want 48 got %v", q)
	}
}
