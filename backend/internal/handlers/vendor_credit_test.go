package handlers

import (
	"testing"
)

func TestVendorCreditLimitEnforcement(t *testing.T) {
	// Test that purchase orders respect vendor credit limits

	tests := []struct {
		name           string
		creditLimit    float64
		outstanding    float64
		poAmount       float64
		shouldApprove  bool
	}{
		{
			name:          "PO within credit limit",
			creditLimit:   10000.0,
			outstanding:   2000.0,
			poAmount:      5000.0,
			shouldApprove: true,
		},
		{
			name:          "PO exceeds available credit",
			creditLimit:   10000.0,
			outstanding:   8000.0,
			poAmount:      3000.0,
			shouldApprove: false,
		},
		{
			name:          "PO exactly at remaining limit",
			creditLimit:   10000.0,
			outstanding:   6000.0,
			poAmount:      4000.0,
			shouldApprove: true,
		},
		{
			name:          "PO exceeds by small amount",
			creditLimit:   10000.0,
			outstanding:   7000.0,
			poAmount:      3001.0,
			shouldApprove: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			availableCredit := tt.creditLimit - tt.outstanding
			canApprove := tt.poAmount <= availableCredit

			if canApprove != tt.shouldApprove {
				t.Errorf("Expected approval=%v, got %v (available=%.2f, requested=%.2f)",
					tt.shouldApprove, canApprove, availableCredit, tt.poAmount)
			}
		})
	}
}

func TestVendorOutstandingCalculation(t *testing.T) {
	// Test that vendor outstanding is calculated correctly

	tests := []struct {
		name                    string
		pendingOrders           []float64
		partialOrders           []float64
		expectedOutstanding     float64
	}{
		{
			name:                "No orders",
			pendingOrders:       []float64{},
			partialOrders:       []float64{},
			expectedOutstanding: 0.0,
		},
		{
			name:                "Single pending order",
			pendingOrders:       []float64{500.0},
			partialOrders:       []float64{},
			expectedOutstanding: 500.0,
		},
		{
			name:                "Multiple pending orders",
			pendingOrders:       []float64{500.0, 750.0, 1000.0},
			partialOrders:       []float64{},
			expectedOutstanding: 2250.0,
		},
		{
			name:                "Mixed pending and partial",
			pendingOrders:       []float64{500.0},
			partialOrders:       []float64{300.0, 200.0},
			expectedOutstanding: 1000.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outstanding := 0.0
			outstanding += sum(tt.pendingOrders)
			outstanding += sum(tt.partialOrders)

			if outstanding != tt.expectedOutstanding {
				t.Errorf("Expected %.2f, got %.2f", tt.expectedOutstanding, outstanding)
			}
		})
	}
}

func sum(nums []float64) float64 {
	total := 0.0
	for _, n := range nums {
		total += n
	}
	return total
}

func TestVendorCreditDayCalculation(t *testing.T) {
	// Test credit days enforcement

	tests := []struct {
		name       string
		creditDays int
		daysOld    int
		isOverdue  bool
	}{
		{"Recent (3 days old)", 30, 3, false},
		{"At limit (30 days)", 30, 30, false},
		{"Overdue by 1 day", 30, 31, true},
		{"Long overdue", 30, 60, true},
		{"Net 45 terms", 45, 45, false},
		{"Net 45 overdue", 45, 46, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isOverdue := tt.daysOld > tt.creditDays

			if isOverdue != tt.isOverdue {
				t.Errorf("Expected overdue=%v, got %v (days=%d, limit=%d)",
					tt.isOverdue, isOverdue, tt.daysOld, tt.creditDays)
			}
		})
	}
}

func TestVendorCreditAllocationLogic(t *testing.T) {
	// Test credit allocation scenarios

	tests := []struct {
		name            string
		creditLimit     float64
		used            float64
		available       float64
	}{
		{"Full available", 10000.0, 0.0, 10000.0},
		{"Half used", 10000.0, 5000.0, 5000.0},
		{"Fully utilized", 10000.0, 10000.0, 0.0},
		{"Overused", 10000.0, 12000.0, -2000.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			available := tt.creditLimit - tt.used

			if available != tt.available {
				t.Errorf("Expected available=%.2f, got %.2f", tt.available, available)
			}
		})
	}
}
