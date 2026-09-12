// Package store holds the shop and its billing terminals.
//
// A store is the unit of tenancy: it owns its GSTIN, its prices, its stock and
// its document series. A terminal is a counter within a store, and matters
// because each one runs its own invoice series.
package store

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value.
var ErrValidation = errors.New("validation failed")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// LocationType distinguishes a shop that sells from a godown that only holds
// stock. Analytics and the POS both care which they are looking at.
type LocationType string

const (
	LocationStore     LocationType = "store"
	LocationWarehouse LocationType = "warehouse"
)

// gstinPattern is the statutory GSTIN format: two state-code digits, a ten
// character PAN, an entity digit, a fixed 'Z', and a checksum character.
var gstinPattern = regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][0-9A-Z]Z[0-9A-Z]$`)

// Store is one shop or godown.
type Store struct {
	ID           uuid.UUID    `json:"id"`
	Name         string       `json:"name"`
	Address      string       `json:"address"`
	Phone        string       `json:"phone"`
	GSTIN        string       `json:"gstin"`
	LocationType LocationType `json:"location_type"`
	IsActive     bool         `json:"is_active"`
}

// StateCode is the first two digits of the GSTIN, which decide whether a
// supply is intra-state (CGST + SGST) or inter-state (IGST). An empty GSTIN
// yields an empty code, and the caller treats that as intra-state.
func (s Store) StateCode() string {
	if len(s.GSTIN) < 2 {
		return ""
	}
	return s.GSTIN[:2]
}

// NewStore validates and returns a store ready to persist.
func NewStore(name, address, phone, gstin string, locationType LocationType) (Store, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Store{}, invalid("store name is required")
	}
	gstin = strings.ToUpper(strings.TrimSpace(gstin))
	if err := ValidateGSTIN(gstin); err != nil {
		return Store{}, err
	}
	if locationType == "" {
		locationType = LocationStore
	}
	if locationType != LocationStore && locationType != LocationWarehouse {
		return Store{}, invalid("location type must be %q or %q", LocationStore, LocationWarehouse)
	}
	return Store{
		ID:           uuid.New(),
		Name:         name,
		Address:      strings.TrimSpace(address),
		Phone:        strings.TrimSpace(phone),
		GSTIN:        gstin,
		LocationType: locationType,
		IsActive:     true,
	}, nil
}

// ValidateGSTIN accepts a well-formed GSTIN or an empty one. A shop below the
// registration threshold legitimately has none, and rejecting that would stop
// it using the system at all.
func ValidateGSTIN(gstin string) error {
	if gstin == "" {
		return nil
	}
	if !gstinPattern.MatchString(gstin) {
		return invalid("GSTIN %q is not in the statutory format", gstin)
	}
	return nil
}

// Terminal is a billing counter. Its Code is the segment that appears in the
// invoice series it owns, so it is short and stable.
type Terminal struct {
	ID       uuid.UUID `json:"id"`
	StoreID  uuid.UUID `json:"store_id"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	IsActive bool      `json:"is_active"`
}

// terminalCodePattern keeps a code usable inside an invoice number: no
// separators, no spaces, nothing that would make a series ambiguous to read.
var terminalCodePattern = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)

// NewTerminal validates and returns a terminal ready to persist.
func NewTerminal(storeID uuid.UUID, code, name string) (Terminal, error) {
	if storeID == uuid.Nil {
		return Terminal{}, invalid("terminal must belong to a store")
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if !terminalCodePattern.MatchString(code) {
		return Terminal{}, invalid("terminal code must be 1-8 letters or digits, got %q", code)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Terminal{}, invalid("terminal name is required")
	}
	return Terminal{
		ID:       uuid.New(),
		StoreID:  storeID,
		Code:     code,
		Name:     name,
		IsActive: true,
	}, nil
}
