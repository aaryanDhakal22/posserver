package menu

import "github.com/google/uuid"

// NormalizeRouting is the one rule for where an item or category prints
// (the owner, 2026-11-13: one printer dropdown, "No printer" = the customer
// receipt only). noKitchen true ("No printer") clears the printer; a printer
// with the flag left out clears the flag. Absent stays absent otherwise (a
// PATCH that names neither keeps both). A stored row with both a printer and
// the flag keeps printing as before (the flag wins in ListRoutingByLocation).
func NormalizeRouting(printer *uuid.UUID, noKitchen *bool) (*uuid.UUID, *bool) {
	if noKitchen != nil && *noKitchen {
		return nil, noKitchen
	}
	if printer != nil && noKitchen == nil {
		f := false
		return printer, &f
	}
	return printer, noKitchen
}
