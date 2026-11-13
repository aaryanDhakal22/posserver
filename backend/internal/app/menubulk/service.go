// Package menubulk is Store › Menu's bulk edit: several items changed in one
// transaction (POST /store/v1/me/menu/items/bulk). The page computes each
// item's new values (a price change of +$1 or +10 %, a printer, a category, in
// stock) and sends them; undoing is sending the old values back. All or
// nothing: one item that isn't the store's, or one bad price, changes none.
package menubulk

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// MaxChanges is the most items one request changes.
const MaxChanges = 2000

// MaxPriceCents is the highest price a bulk change sets ($100,000).
const MaxPriceCents = 10_000_000

var (
	ErrBadBody     = errors.New("bad body")
	ErrNotFound    = errors.New("not found")
	ErrBadPrice    = errors.New("bad price")
	ErrUnknownSize = errors.New("unknown size")
	ErrQuickAdd    = errors.New("quick add kept")
)

// Change is one item's new values; nil = keep.
type Change struct {
	ItemID     uuid.UUID  `json:"item_id"`
	CategoryID *uuid.UUID `json:"category_id,omitempty"`
	// PrinterID: nil keeps; "" clears (the category's printer); a printer id sets.
	PrinterID       *string          `json:"printer_id,omitempty"`
	NoKitchenTicket *bool            `json:"no_kitchen_ticket,omitempty"`
	InStock         *bool            `json:"in_stock,omitempty"`
	PriceCents      *int64           `json:"price_cents,omitempty"`
	SizePrices      map[string]int64 `json:"size_prices,omitempty"` // size id → cents (sizes the item has)
	TaxRateBps      *int             `json:"tax_rate_bps,omitempty"`
}

// Resolved is a checked Change with its ids parsed.
type Resolved struct {
	Change
	SetPrinter bool
	Printer    *uuid.UUID
	Sizes      map[uuid.UUID]int64
}

// Repo applies checked changes in one transaction, after checking every id
// against the location (ErrNotFound / ErrUnknownSize / ErrQuickAdd).
type Repo interface {
	ApplyItemChanges(ctx context.Context, tenantID, locationID uuid.UUID, changes []Resolved) error
}

type Service struct{ repo Repo }

func NewService(repo Repo) *Service { return &Service{repo: repo} }

// Check validates a request's changes (pure): at least one, at most
// MaxChanges, each item once, prices 0…MaxPriceCents, tax 0…10000 bps,
// printer ids that parse.
func Check(changes []Change) ([]Resolved, error) {
	if len(changes) == 0 {
		return nil, fmt.Errorf("%w: no changes", ErrBadBody)
	}
	if len(changes) > MaxChanges {
		return nil, fmt.Errorf("%w: at most %d items at once", ErrBadBody, MaxChanges)
	}
	seen := map[uuid.UUID]bool{}
	out := make([]Resolved, 0, len(changes))
	for i, c := range changes {
		if c.ItemID == uuid.Nil {
			return nil, fmt.Errorf("%w: changes[%d]: item_id is required", ErrBadBody, i)
		}
		if seen[c.ItemID] {
			return nil, fmt.Errorf("%w: changes[%d]: the item is in the list twice", ErrBadBody, i)
		}
		seen[c.ItemID] = true
		r := Resolved{Change: c}
		if c.PrinterID != nil {
			r.SetPrinter = true
			if *c.PrinterID != "" {
				id, err := uuid.Parse(*c.PrinterID)
				if err != nil {
					return nil, fmt.Errorf("%w: changes[%d]: printer_id isn't an id", ErrBadBody, i)
				}
				r.Printer = &id
			}
		}
		// One printer choice (menu.NormalizeRouting): "No printer" clears the item's own printer; a printer
		// (not the category's "") clears "No printer".
		if c.NoKitchenTicket != nil && *c.NoKitchenTicket {
			r.SetPrinter, r.Printer = true, nil
			empty := ""
			r.PrinterID = &empty
		} else if r.Printer != nil && c.NoKitchenTicket == nil {
			f := false
			r.NoKitchenTicket = &f
		}
		if c.PriceCents != nil && (*c.PriceCents < 0 || *c.PriceCents > MaxPriceCents) {
			return nil, fmt.Errorf("%w: changes[%d]: a price is $0 to $100,000", ErrBadPrice, i)
		}
		if c.TaxRateBps != nil && (*c.TaxRateBps < 0 || *c.TaxRateBps > 10000) {
			return nil, fmt.Errorf("%w: changes[%d]: a tax rate is 0 to 100%%", ErrBadBody, i)
		}
		if len(c.SizePrices) > 0 {
			r.Sizes = map[uuid.UUID]int64{}
			for k, v := range c.SizePrices {
				id, err := uuid.Parse(k)
				if err != nil {
					return nil, fmt.Errorf("%w: changes[%d]: size_prices keys are size ids", ErrBadBody, i)
				}
				if v < 0 || v > MaxPriceCents {
					return nil, fmt.Errorf("%w: changes[%d]: a price is $0 to $100,000", ErrBadPrice, i)
				}
				r.Sizes[id] = v
			}
		}
		if c.CategoryID == nil && !r.SetPrinter && c.NoKitchenTicket == nil && c.InStock == nil && c.PriceCents == nil &&
			len(r.Sizes) == 0 && c.TaxRateBps == nil {
			return nil, fmt.Errorf("%w: changes[%d]: nothing to change", ErrBadBody, i)
		}
		out = append(out, r)
	}
	return out, nil
}

// Apply checks and applies the changes (all or nothing); it returns how many
// items changed.
func (s *Service) Apply(ctx context.Context, tenantID, locationID uuid.UUID, changes []Change) (int, error) {
	rs, err := Check(changes)
	if err != nil {
		return 0, err
	}
	if err := s.repo.ApplyItemChanges(ctx, tenantID, locationID, rs); err != nil {
		return 0, err
	}
	return len(rs), nil
}
