package stock_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gbos/internal/domain/money"
	"gbos/internal/domain/stock"
)

// How much to order is the one number on a reorder list that a buyer cannot
// work out at a glance, and the rounding is where it goes wrong.
func TestSuggestOrderRoundsUpToWholeBuyingUnits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                         string
		minStock, current, qtyInBase string
		want                         string
	}{
		{
			// 50 kg minimum, 12 on hand, bought in 25 kg sacks. Twice the
			// minimum is 100, so 88 kg are needed, which is 3.52 sacks.
			name:     "part of a sack rounds up to a whole one",
			minStock: "50", current: "12", qtyInBase: "25", want: "4",
		},
		{
			// Rounding down would leave 87.5 kg against a 50 kg reorder level
			// — above it, but the next delivery is a week away and the point of
			// ordering to twice the level is not to be back here on Tuesday.
			name:     "exactly on a boundary does not round up further",
			minStock: "50", current: "0", qtyInBase: "25", want: "4",
		},
		{
			name:     "bought loose, so the suggestion is the shortfall itself",
			minStock: "50", current: "12", qtyInBase: "1", want: "88",
		},
		{
			// At twice the level already: nothing to order, even though the
			// product is on the list because it is at its reorder level.
			name:     "already at the target",
			minStock: "10", current: "20", qtyInBase: "5", want: "0",
		},
		{
			// A product with no purchasable unit cannot be ordered at all, and
			// dividing by zero is not the way to say so.
			name:     "no buying unit",
			minStock: "50", current: "0", qtyInBase: "0", want: "0",
		},
		{
			// The commonest real case: a 250 g pouch, where a naive division
			// gives 352 and rounding matters not at all — but a fractional
			// answer here would be an order for part of a pouch.
			name:     "small packs",
			minStock: "50", current: "12", qtyInBase: "0.25", want: "352",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stock.SuggestOrder(
				money.MustQuantityFromString(tc.minStock),
				money.MustQuantityFromString(tc.current),
				money.MustQuantityFromString(tc.qtyInBase))
			if !got.Equal(money.MustQuantityFromString(tc.want)) {
				t.Errorf("SuggestOrder(%s, %s, %s) = %s, want %s",
					tc.minStock, tc.current, tc.qtyInBase, got, tc.want)
			}
		})
	}
}

// An order for a whole number of buying units must never leave the shop below
// its reorder level once the delivery is put away. Rounding down is the way
// that happens, and it is the one outcome the list exists to prevent.
func TestASuggestedOrderAlwaysClearsTheReorderLevel(t *testing.T) {
	t.Parallel()
	for _, pack := range []string{"0.25", "1", "5", "10", "25", "50"} {
		for _, current := range []string{"0", "1", "7.5", "12", "49.999"} {
			minStock := money.MustQuantityFromString("50")
			onHand := money.MustQuantityFromString(current)
			qtyInBase := money.MustQuantityFromString(pack)

			units := stock.SuggestOrder(minStock, onHand, qtyInBase)
			after := onHand.Add(units.Mul(qtyInBase.Decimal()))
			if !after.GreaterThan(minStock) {
				t.Errorf("%s on hand plus %s packs of %s = %s, which is not above the %s reorder level",
					current, units, pack, after, minStock)
			}
		}
	}
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// The rule two product fields were added for and neither had.
func TestExpiryForBatch(t *testing.T) {
	t.Parallel()
	today := day(2026, time.September, 12)
	shelfLife := 270

	stated := day(2027, time.March, 1)
	made := day(2026, time.June, 15)

	for _, tc := range []struct {
		name        string
		tracks      bool
		shelfLife   *int
		stated, mfg *time.Time
		want        *time.Time
		wantErr     string
	}{
		{
			name:   "a stated date wins",
			tracks: true, shelfLife: &shelfLife, stated: &stated,
			want: &stated,
		},
		{
			// A clerk reading a date off a packet is recording a fact, and the
			// report is better for having it.
			name:   "a stated date is kept even on a product that does not expire",
			tracks: false, stated: &stated,
			want: &stated,
		},
		{
			// What the shelf life is for: a packing date and "best before 270
			// days", which is what most Indian packaging carries.
			name:   "counted from manufacture when the shelf life is known",
			tracks: true, shelfLife: &shelfLife, mfg: &made,
			want: func() *time.Time { d := made.AddDate(0, 0, shelfLife); return &d }(),
		},
		{
			name:   "nothing to compute from is refused",
			tracks: true, shelfLife: &shelfLife,
			wantErr: "270 day shelf life",
		},
		{
			name:    "no shelf life either, so the date has to be given",
			tracks:  true,
			wantErr: "needs an expiry date",
		},
		{
			name:   "a product that does not expire needs nothing",
			tracks: false,
			want:   nil,
		},
		{
			name:   "a manufacture date in the future is a typing mistake",
			tracks: true, shelfLife: &shelfLife,
			mfg:     func() *time.Time { d := day(2026, time.December, 1); return &d }(),
			wantErr: "in the future",
		},
		{
			name:   "an expiry before manufacture is a typing mistake",
			tracks: true, mfg: &made,
			stated:  func() *time.Time { d := day(2026, time.January, 1); return &d }(),
			wantErr: "before it was made",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := stock.ExpiryForBatch("Toned Milk", tc.tracks, tc.shelfLife, tc.stated, tc.mfg, today)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("got %v, want a refusal mentioning %q", got, tc.wantErr)
				}
				if !errors.Is(err, stock.ErrValidation) {
					t.Errorf("err = %v, want a validation error", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("message = %q, want it to mention %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "Toned Milk") {
					t.Errorf("message = %q, want it to name the product — a receipt has many lines", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("got %s, want no expiry", got.Format("2006-01-02"))
			case tc.want != nil && got == nil:
				t.Errorf("got no expiry, want %s", tc.want.Format("2006-01-02"))
			case tc.want != nil && !got.Equal(*tc.want):
				t.Errorf("got %s, want %s", got.Format("2006-01-02"), tc.want.Format("2006-01-02"))
			}
		})
	}
}

// Goods do arrive expired. A shop that cannot record that cannot send them back
// or write them off, and the expiry report is where it finds them.
func TestAnAlreadyExpiredDeliveryCanBeRecorded(t *testing.T) {
	t.Parallel()
	today := day(2026, time.September, 12)
	gone := day(2026, time.August, 1)

	got, err := stock.ExpiryForBatch("Toned Milk", true, nil, &gone, nil, today)
	if err != nil {
		t.Fatalf("recording an expired delivery: %v", err)
	}
	if got == nil || !got.Equal(gone) {
		t.Errorf("got %v, want the stated date kept", got)
	}
}
