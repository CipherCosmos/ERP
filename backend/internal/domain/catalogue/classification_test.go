package catalogue

import (
	"strings"
	"testing"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// TestAProductMustCarryItsClassification pins the rule that a product has the
// code a tax invoice prints for it.
//
// Without it a shop could save a product with no HSN code and then issue
// invoices missing a particular CGST Rule 46(g) names, which cannot be filed. The
// field was optional, trimmed, and stored blank, so nothing said so until the
// return was prepared months later — by which time the invoices are with
// customers.
func TestAProductMustCarryItsClassification(t *testing.T) {
	base := func() NewProductInput {
		return NewProductInput{
			Name:        "Sona Masoori Rice",
			CategoryID:  uuid.New(),
			UOMID:       uuid.New(),
			BaseUnitQty: money.QuantityFromInt(1),
			GSTRate:     money.RateFromInt(5),
		}
	}

	t.Run("neither code is refused", func(t *testing.T) {
		if _, err := NewProduct(base()); err == nil {
			t.Fatal("a product with no HSN and no SAC code was accepted")
		} else if !strings.Contains(err.Error(), "HSN code is required") {
			t.Errorf("the error should say what is missing: %v", err)
		}
	})

	t.Run("both codes are refused", func(t *testing.T) {
		in := base()
		in.HSNCode, in.SACCode = "10063010", "996211"
		if _, err := NewProduct(in); err == nil {
			t.Error("a product carrying both an HSN and a SAC code was accepted")
		}
	})

	t.Run("an HSN code of a real length is accepted", func(t *testing.T) {
		for _, code := range []string{"1006", "100630", "10063010"} {
			in := base()
			in.HSNCode = code
			got, err := NewProduct(in)
			if err != nil {
				t.Errorf("HSN %q: %v", code, err)
				continue
			}
			if got.HSNCode != code {
				t.Errorf("HSN stored as %q, want %q", got.HSNCode, code)
			}
		}
	})

	t.Run("a service carries a SAC code", func(t *testing.T) {
		in := base()
		in.SACCode = "996211"
		got, err := NewProduct(in)
		if err != nil {
			t.Fatalf("SAC 996211: %v", err)
		}
		if got.SACCode != "996211" || got.HSNCode != "" {
			t.Errorf("stored hsn=%q sac=%q", got.HSNCode, got.SACCode)
		}
	})

	// A code of the wrong length is a typo the GST portal rejects when the return
	// is filed, which is the worst moment to find out.
	t.Run("a code of the wrong length is refused", func(t *testing.T) {
		for _, code := range []string{"1", "100", "10063", "1006301", "100630100"} {
			in := base()
			in.HSNCode = code
			if _, err := NewProduct(in); err == nil {
				t.Errorf("HSN %q (%d digits) was accepted", code, len(code))
			}
		}
		in := base()
		in.SACCode = "9962"
		if _, err := NewProduct(in); err == nil {
			t.Error("a 4-digit SAC code was accepted; SAC is six digits")
		}
	})

	t.Run("a code that is not digits is refused", func(t *testing.T) {
		for _, code := range []string{"1006-30", "HSN1006", "1006 30", "10O63010"} {
			in := base()
			in.HSNCode = code
			if _, err := NewProduct(in); err == nil {
				t.Errorf("HSN %q was accepted", code)
			}
		}
	})

	// Exempt goods are classified too: the code says what the goods are, not what
	// they are taxed at.
	t.Run("an exempt product still needs a code", func(t *testing.T) {
		in := base()
		in.GSTRate = money.RateFromInt(0)
		if _, err := NewProduct(in); err == nil {
			t.Error("a zero-rated product was accepted with no classification")
		}
		in.HSNCode = "0713"
		if _, err := NewProduct(in); err != nil {
			t.Errorf("a zero-rated product with an HSN code: %v", err)
		}
	})
}
