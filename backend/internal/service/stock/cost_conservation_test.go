package stock_test

import (
	"context"
	"testing"

	"gbos/internal/domain/money"
	domain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/stock"
)

// TestWhatWasPaidIsWhatLeaves is the conservation rule for inventory value:
// money that went in as the cost of a delivery must come out as the cost of the
// goods sold, to the paisa, when the whole delivery is sold.
//
// It is a property, not a figure. A 30 kg sack at Rs.940 is Rs.31.3333... a
// kilo, and no per-kilo figure rounded to paise can be multiplied back up to
// Rs.940 — 30 x 31.33 is Rs.939.90. Storing only the per-unit cost therefore
// loses ten paise per sack, always in the same direction, and a gross-profit
// report can no longer be tied back to what was bought.
//
// The pack sizes below are the ones a grain or oil trade actually uses, chosen
// so the division does not come out even.
func TestWhatWasPaidIsWhatLeaves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name      string
		qtyInBase string
		packs     string
		packPrice string
		paid      string
	}{
		{"a 30 kg sack at 940", "30", "1", "940.00", "940.00"},
		{"three 30 kg sacks", "30", "3", "940.00", "2820.00"},
		{"a 14 kg tin of oil at 1850", "14", "1", "1850.00", "1850.00"},
		{"a 7 kg bag at 505", "7", "1", "505.00", "505.00"},
		{"a 25 kg sack at 940, which divides evenly", "25", "4", "940.00", "3760.00"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			pack := f.addUnit(t, "pack", tc.qtyInBase)

			booked, err := f.svc.Adjust(ctx, service.AdjustInput{
				ProductID: f.product, StoreID: f.store, UserID: f.user,
				BatchNo: "DELIVERY", ProductUnitID: pack, UnitQty: qty(tc.packs),
				Type: domain.AdjustAdd, CostPrice: money.MustFromString(tc.packPrice),
			})
			if err != nil {
				t.Fatalf("book in the delivery: %v", err)
			}

			// Sell the whole delivery. Cost of goods sold must be what was paid.
			var deduction domain.Deduction
			if err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
				var err error
				deduction, err = f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
					ProductID: f.product, StoreID: f.store,
					Quantity: booked.Batch.Quantity,
				})
				return err
			}); err != nil {
				t.Fatalf("sell the whole delivery: %v", err)
			}

			paid := money.MustFromString(tc.paid)
			if !deduction.TotalCost.Equal(paid) {
				t.Errorf("cost of goods sold = %s, want %s — %s went missing between buying and selling",
					deduction.TotalCost, paid, paid.Sub(deduction.TotalCost))
			}
		})
	}
}

// TestASecondDeliveryIntoABatchAddsToItsValue pins a behaviour change that came
// with the batch holding value.
//
// The upsert used to SET the per-unit cost from whatever arrived, so a second,
// cheaper delivery into the same batch number silently repriced the stock
// already on the shelf — twenty kilos bought at Rs.40 became twenty kilos worth
// Rs.30 because ten more arrived at Rs.30. Value now adds, which is what a
// weighted average means.
func TestASecondDeliveryIntoABatchAddsToItsValue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	first, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "MIXED", Quantity: qty("20"),
		Type: domain.AdjustAdd, CostPrice: money.MustFromString("40.00"),
	})
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if want := money.MustFromString("800.00"); !first.Batch.TotalCost.Equal(want) {
		t.Fatalf("after the first delivery the batch is worth %s, want %s", first.Batch.TotalCost, want)
	}

	second, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "MIXED", Quantity: qty("10"),
		Type: domain.AdjustAdd, CostPrice: money.MustFromString("30.00"),
	})
	if err != nil {
		t.Fatalf("second delivery: %v", err)
	}

	// 20 at 40 plus 10 at 30 is Rs.1,100 for 30 units: Rs.36.666... each, which
	// is why the value and not the rate is what gets stored.
	if want := money.MustFromString("1100.00"); !second.Batch.TotalCost.Equal(want) {
		t.Errorf("batch value = %s, want %s — the second delivery must add, not reprice",
			second.Batch.TotalCost, want)
	}
	assertQty(t, "quantity", second.Batch.Quantity, "30")

	// Selling the lot must still come to Rs.1,100.
	var deduction domain.Deduction
	if err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		deduction, err = f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, Quantity: qty("30"),
		})
		return err
	}); err != nil {
		t.Fatalf("sell the lot: %v", err)
	}
	if want := money.MustFromString("1100.00"); !deduction.TotalCost.Equal(want) {
		t.Errorf("cost of goods sold = %s, want %s", deduction.TotalCost, want)
	}
}
