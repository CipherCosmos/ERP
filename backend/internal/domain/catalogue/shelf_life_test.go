package catalogue

import (
	"strings"
	"testing"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

func perishableBase() NewProductInput {
	return NewProductInput{
		Name:        "Toned Milk 500 ml",
		CategoryID:  uuid.New(),
		UOMID:       uuid.New(),
		HSNCode:     "0401",
		BaseUnitQty: money.QuantityFromInt(1),
		GSTRate:     money.RateFromInt(5),
	}
}

func days(n int) *int { return &n }

// TestAShelfLifeBelongsToSomethingThatExpires pins the pair that has to be read
// together.
//
// A batch has always carried an expiry date and nothing said which products need
// one, so the expiry report was only as complete as whoever was typing at the
// receiving bay remembered. Now the product says. The shelf life is how long it
// lasts from manufacture, which lets a receipt work the date out — and stating
// one for something that does not expire is a contradiction, not a default.
func TestAShelfLifeBelongsToSomethingThatExpires(t *testing.T) {
	t.Run("a shelf life without expiry tracking is refused", func(t *testing.T) {
		in := perishableBase()
		in.ShelfLifeDays = days(90)
		_, err := NewProduct(in)
		if err == nil {
			t.Fatal("90 days of shelf life was accepted for something not marked as expiring")
		}
		if !strings.Contains(err.Error(), "Tick that it expires") {
			t.Errorf("the error should say what to do about it: %v", err)
		}
	})

	t.Run("expiry tracking without a shelf life is fine", func(t *testing.T) {
		// The common case: the shop reads the date off the packet.
		in := perishableBase()
		in.TracksExpiry = true
		got, err := NewProduct(in)
		if err != nil {
			t.Fatalf("a perishable with no stated shelf life: %v", err)
		}
		if !got.TracksExpiry || got.ShelfLifeDays != nil {
			t.Errorf("tracks=%v shelfLife=%v, want tracked with no stated life", got.TracksExpiry, got.ShelfLifeDays)
		}
	})

	t.Run("both together are stored", func(t *testing.T) {
		in := perishableBase()
		in.TracksExpiry = true
		in.ShelfLifeDays = days(7)
		got, err := NewProduct(in)
		if err != nil {
			t.Fatalf("milk with a seven day life: %v", err)
		}
		if got.ShelfLifeDays == nil || *got.ShelfLifeDays != 7 {
			t.Errorf("shelf life = %v, want 7", got.ShelfLifeDays)
		}
	})

	t.Run("a negative shelf life is refused", func(t *testing.T) {
		in := perishableBase()
		in.TracksExpiry = true
		in.ShelfLifeDays = days(-1)
		if _, err := NewProduct(in); err == nil {
			t.Error("a negative shelf life was accepted; every receipt of it would expire on arrival")
		}
	})

	t.Run("neither is the ordinary case", func(t *testing.T) {
		got, err := NewProduct(perishableBase())
		if err != nil {
			t.Fatalf("salt: %v", err)
		}
		if got.TracksExpiry || got.ShelfLifeDays != nil {
			t.Error("a product with nothing said about expiry should track none")
		}
	})
}
