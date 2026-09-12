package stock_test

import (
	"testing"

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
