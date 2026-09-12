package money

import (
	"github.com/shopspring/decimal"
)

// two is the CGST/SGST divisor, hoisted so the split does not allocate it per
// invoice line.
var two = decimal.NewFromInt(2)

// TaxSplit is the GST breakdown of a single taxable amount.
//
// Exactly one of the two halves of Indian GST applies to any given supply:
// an intra-state supply is taxed as CGST plus SGST, an inter-state supply as
// IGST alone. A TaxSplit therefore always has either IGST zero, or both CGST
// and SGST zero.
type TaxSplit struct {
	CGST Money
	SGST Money
	IGST Money
}

// Total returns the whole tax on the supply. For any split produced by
// SplitTax this is exactly the amount TaxAmount returned - that identity is
// the guarantee this file exists to provide.
func (t TaxSplit) Total() Money { return t.CGST.Add(t.SGST).Add(t.IGST) }

// TaxAmount returns the total GST on a taxable amount at the given rate
// percentage, rounded to Scale.
//
// The multiplication is exact and the division by 100 is a decimal shift, also
// exact, so there is exactly one rounding in the whole computation and it
// happens at the paisa. The legacy expression - total * (rate/100.0) in
// float64 - had rate/100 inexact before the multiplication even began.
func TaxAmount(taxable Money, ratePercent Rate) Money {
	if ratePercent.IsZero() || taxable.IsZero() {
		return Zero()
	}
	return Money{d: taxable.d.Mul(ratePercent.d).Shift(-2).Round(Scale)}
}

// SplitTax computes the GST on a taxable amount and divides it into the
// CGST/SGST/IGST components that belong on the invoice.
//
// An inter-state supply puts the whole tax in IGST. An intra-state supply
// divides it between CGST and SGST.
//
// # The half-paise problem
//
// When the total tax is an odd number of paise, halving it exactly is
// impossible at Scale. A Rs.55 line at 5% carries Rs.2.75 of tax, and half of
// 2.75 is 1.375 - a figure with no representation in paise and no way to pay
// it. The legacy code stored both halves unrounded and printed each with %.2f,
// so the invoice showed 1.38 + 1.38 = 2.76 while the database held 2.75. Every
// such line drifted a paisa, and the drift was invisible until a reconciliation
// report added up the wrong column.
//
// The policy here: round one half to Scale, then derive the other by
// subtracting it from the total. Subtraction of two Scale-place values is
// exact, so CGST + SGST is identically the total tax by construction rather
// than by luck - there is no input for which the invariant can fail, including
// negative amounts on credit notes and returns. The stray paisa goes to CGST,
// deterministically and always, so recomputing an invoice a year later
// reproduces it to the paisa and an auditor comparing two runs sees no
// difference. An alternating or randomised assignment would spread the paisa
// more evenly across the central and state governments over many invoices, but
// it would make a single invoice irreproducible, and reproducibility is worth
// far more here than a half-paisa of fairness between two tax authorities.
func SplitTax(taxable Money, ratePercent Rate, interState bool) TaxSplit {
	total := TaxAmount(taxable, ratePercent)
	if interState {
		return TaxSplit{CGST: Zero(), SGST: Zero(), IGST: total}
	}
	// DivRound performs an exact division and rounds half away from zero at
	// Scale in a single step: 2.75 -> 1.38, and -2.75 -> -1.38.
	cgst := Money{d: total.d.DivRound(two, Scale)}
	return TaxSplit{CGST: cgst, SGST: total.Sub(cgst), IGST: Zero()}
}

// RoundOff rounds an invoice grand total to the nearest rupee and returns the
// rounded total together with the signed adjustment that reaches it, for the
// invoices.round_off column.
//
//	rounded, adj := RoundOff(MustFromString("1234.60"))  // 1235.00, +0.40
//	rounded, adj := RoundOff(MustFromString("1234.40"))  // 1234.00, -0.40
//
// The identity grandTotal + adjustment == rounded holds exactly for every
// input, so the three figures on the invoice reconcile.
//
// Rounding is half away from zero, which is the rule section 170 of the CGST
// Act states directly: fifty paise or more rounds up. The grand total is first
// normalised to Scale, because round_off is by definition derived from the
// figure printed on the invoice, not from an unrounded intermediate.
func RoundOff(grandTotal Money) (rounded Money, adjustment Money) {
	at2 := grandTotal.Round()
	rounded = Money{d: at2.d.Round(0)}
	return rounded, rounded.Sub(at2)
}

// The Rate type and its constructors live in rate.go, next to the JSON and SQL
// behaviour that is the reason it is a named type at all.
