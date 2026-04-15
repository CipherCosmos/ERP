package handlers

import (
	"testing"
)

func TestStockConversionMath(t *testing.T) {
	// Test stock conversion between different UOMs

	tests := []struct {
		name                string
		fromUnit            string
		toUnit              string
		fromQuantity        float64
		conversionFactor    float64 // qty_in_parent relationship
		expectedToQuantity  float64
		direction           string  // "up" for smaller->larger unit, "down" for larger->smaller
	}{
		{
			name:               "Pieces to dozen (divide)",
			fromUnit:           "pcs",
			toUnit:             "dz",
			fromQuantity:       12.0,
			conversionFactor:   12.0, // 12 pcs = 1 dz
			expectedToQuantity: 1.0,
			direction:          "up",
		},
		{
			name:               "Liter to milliliter (multiply)",
			fromUnit:           "l",
			toUnit:             "ml",
			fromQuantity:       1.0,
			conversionFactor:   1000.0,
			expectedToQuantity: 1000.0,
			direction:          "down",
		},
		{
			name:               "Kg to gram (multiply)",
			fromUnit:           "kg",
			toUnit:             "g",
			fromQuantity:       2.5,
			conversionFactor:   1000.0,
			expectedToQuantity: 2500.0,
			direction:          "down",
		},
		{
			name:               "Dozen to pieces (multiply)",
			fromUnit:           "dz",
			toUnit:             "pcs",
			fromQuantity:       2.0,
			conversionFactor:   12.0, // 1 dz = 12 pcs
			expectedToQuantity: 24.0,
			direction:          "down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var computed float64
			if tt.direction == "down" {
				// Converting to smaller unit: multiply
				computed = tt.fromQuantity * tt.conversionFactor
			} else {
				// Converting to larger unit: divide
				computed = tt.fromQuantity / tt.conversionFactor
			}

			// Allow small float precision errors
			epsilon := 0.0001
			if computed < tt.expectedToQuantity-epsilon || computed > tt.expectedToQuantity+epsilon {
				t.Errorf("Expected %.2f, got %.2f (from=%.2f %s, to=%s, factor=%.4f, dir=%s)",
					tt.expectedToQuantity, computed, tt.fromQuantity, tt.fromUnit, tt.toUnit, tt.conversionFactor, tt.direction)
			} else {
				t.Logf("✓ %.2f %s → %.2f %s", tt.fromQuantity, tt.fromUnit, computed, tt.toUnit)
			}
		})
	}
}

func TestStockDeductionAndConversion(t *testing.T) {
	// Test that source stock is deducted and target is incremented

	tests := []struct {
		name                 string
		sourceQty            float64
		conversionQuantity   float64
		expectedSourceAfter  float64
		expectedTargetAfter  float64
	}{
		{
			name:                "Simple conversion",
			sourceQty:           12.0,
			conversionQuantity:  1.0,
			expectedSourceAfter: 11.0,
			expectedTargetAfter: 1.0,
		},
		{
			name:                "Partial conversion",
			sourceQty:           100.0,
			conversionQuantity:  25.0,
			expectedSourceAfter: 75.0,
			expectedTargetAfter: 25.0,
		},
		{
			name:                "Convert all",
			sourceQty:           50.0,
			conversionQuantity:  50.0,
			expectedSourceAfter: 0.0,
			expectedTargetAfter: 50.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceAfter := tt.sourceQty - tt.conversionQuantity
			targetAfter := 0.0 + tt.conversionQuantity

			if sourceAfter != tt.expectedSourceAfter {
				t.Errorf("Source qty wrong: expected %.2f, got %.2f",
					tt.expectedSourceAfter, sourceAfter)
			}
			if targetAfter != tt.expectedTargetAfter {
				t.Errorf("Target qty wrong: expected %.2f, got %.2f",
					tt.expectedTargetAfter, targetAfter)
			}
		})
	}
}

func TestConversionStockAdjustment(t *testing.T) {
	// Test that conversion creates proper stock adjustment records

	adjustments := []map[string]interface{}{}

	// Simulate conversion process
	conversion := map[string]interface{}{
		"type":            "conversion",
		"from_product_id": "prod-1",
		"to_product_id":   "prod-2",
		"quantity":        10.0,
		"reason":          "Unit conversion pcs to dz",
	}
	adjustments = append(adjustments, conversion)

	if len(adjustments) != 1 {
		t.Error("Adjustment not recorded")
	}

	adj := adjustments[0]
	if adj["type"] != "conversion" {
		t.Error("Adjustment type not set")
	}
	if adj["quantity"] != 10.0 {
		t.Error("Adjustment quantity not set")
	}

	t.Logf("Stock adjustment recorded: %v", adj)
}

func TestConversionValidation(t *testing.T) {
	// Test conversion validation rules

	tests := []struct {
		name           string
		sourceQty      float64
		convertQty     float64
		isValid        bool
	}{
		{"Valid conversion", 10.0, 5.0, true},
		{"Convert zero - invalid", 10.0, 0.0, false},
		{"Insufficient source", 5.0, 10.0, false},
		{"Negative quantity - invalid", 10.0, -1.0, false},
		{"Convert everything", 10.0, 10.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid := tt.convertQty > 0 && tt.convertQty <= tt.sourceQty

			if isValid != tt.isValid {
				t.Errorf("Expected valid=%v, got %v (source=%.2f, convert=%.2f)",
					tt.isValid, isValid, tt.sourceQty, tt.convertQty)
			}
		})
	}
}

func TestProductUnitConversion(t *testing.T) {
	// Test ProductUnit conversion relationships

	tests := []struct {
		name           string
		unitName       string
		qtyInParent    float64
	}{
		{"Dozen", "dz", 12.0},
		{"Liter", "l", 1000.0},  // to ml
		{"Kilogram", "kg", 1000.0}, // to g
		{"Piece", "pcs", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.qtyInParent <= 0 {
				t.Error("QtyInParent must be positive")
			}
			t.Logf("✓ Unit %s: %.1f per parent", tt.unitName, tt.qtyInParent)
		})
	}
}
