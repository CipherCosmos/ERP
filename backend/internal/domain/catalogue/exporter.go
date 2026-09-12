package catalogue

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Catalogue export.
//
// The counterpart to the import, and deliberately the same shape: an export is
// written as ImportRow values and read back by ParseImport, so the two formats
// cannot drift. A shop's actual price revision is export, edit in a
// spreadsheet, import — and that loop only works if the file that comes out is
// a file that goes back in.
//
// One row per product, carrying its default unit's prices. A product also sold
// by the sack has other prices, and those are not here, because the import can
// only write one price per row and a sheet that shows figures an import will
// silently ignore is worse than one that does not show them.

// exportColumns is the header, in the order a person reads it: what the thing
// is, then how it is classified, then what it costs, then the details.
var exportColumns = []importField{
	fName, fNameHi, fNameKn, fCategory, fUnit, fBarcode,
	fHSN, fSAC, fGST,
	fMRP, fCost, fSell, fWholesale,
	fBrand, fMinStock, fAllowDecimal, fTracksExpiry, fShelfLife, fDescription,
}

// WriteExport writes rows as a catalogue sheet.
//
// No byte-order mark. Excel wants one to read UTF-8 correctly, and the
// importer strips it, so it would round-trip — but a file with a BOM is a file
// every other tool has to cope with, and the shops this serves open these in
// Excel and in Google Sheets and in LibreOffice. Plain UTF-8 is what all three
// read; only Excel's double-click path mis-guesses the encoding, and only for
// non-ASCII, and the fix there is its own import dialog.
func WriteExport(w io.Writer, rows []ImportRow) error {
	out := csv.NewWriter(w)

	header := make([]string, len(exportColumns))
	for i, field := range exportColumns {
		header[i] = canonical[field]
	}
	if err := out.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	record := make([]string, len(exportColumns))
	for _, row := range rows {
		for i, field := range exportColumns {
			record[i] = exportCell(row, field)
		}
		if err := out.Write(record); err != nil {
			return fmt.Errorf("write %s: %w", row.Name, err)
		}
	}

	out.Flush()
	return out.Error()
}

func exportCell(row ImportRow, field importField) string {
	switch field {
	case fName:
		return row.Name
	case fNameHi:
		return row.NameHi
	case fNameKn:
		return row.NameKn
	case fCategory:
		// The full path, so a re-import rebuilds the tree rather than flattening
		// every sub-category into a root.
		return strings.Join(row.CategoryPath, " > ")
	case fUnit:
		return row.Unit
	case fBarcode:
		return row.Barcode
	case fHSN:
		return row.HSNCode
	case fSAC:
		return row.SACCode
	case fGST:
		return row.GSTRate.String()
	case fBrand:
		return row.Brand
	case fDescription:
		return row.Description
	case fMinStock:
		// Trimmed rather than fixed: a reorder level of 24 reads better as "24"
		// than "24.000", and the reader takes either.
		return row.MinStock.String()
	case fAllowDecimal:
		return yesNo(row.AllowDecimal)
	case fTracksExpiry:
		return yesNo(row.TracksExpiry)
	case fShelfLife:
		if row.ShelfLifeDays == nil {
			return ""
		}
		return strconv.Itoa(*row.ShelfLifeDays)
	// Money at two places always. "24.5" beside "30" in a price column is a
	// number somebody has to look twice at, and a spreadsheet will right-align
	// them differently; "24.50" and "30.00" read as money at a glance.
	case fMRP:
		return row.MRP.StringFixed()
	case fCost:
		return row.CostPrice.StringFixed()
	case fSell:
		return row.SellPrice.StringFixed()
	case fWholesale:
		return row.WholesaleSellPrice.StringFixed()
	}
	return ""
}

// yesNo writes the answer the importer's own reader accepts, and the one a
// person filling in the column will copy on the row below.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
