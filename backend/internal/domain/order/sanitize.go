package order

import (
	"quiccpos/main/internal/shared/textclean"
)

// Text limits for an order's free text (security review H3). Every order,
// from every door (the vendor feed, /channel/v1, the Pi's counter orders),
// is built by NewOrder, which strips control characters from its text and
// cuts it to these lengths (characters, not bytes): a name can't carry
// printer commands to the kitchen, a note can't forge a line, and nothing
// is long enough to run a slip off the roll.
const (
	// MaxNameLen: a customer's name, a fee / discount label or code, a size,
	// a table, city / state / zip / country.
	MaxNameLen = 80
	// MaxItemNameLen: an item's or modifier's name, its SKU, a coupon's name.
	// Longer than a person's name on purpose: a cut item name is a wrong
	// dish in the kitchen, and the printouts wrap a long one (the Pi's own
	// cap for these, receipt.MaxNoteRunes, is the same).
	MaxItemNameLen = 300
	// MaxNoteLen: the order's note, an item's note, the delivery note.
	MaxNoteLen = 300
	// MaxAddressLineLen: address line 1 and line 2.
	MaxAddressLineLen = 120
	// MaxPhoneLen / MaxEmailLen: the customer's phone and email as sent.
	MaxPhoneLen = 40
	MaxEmailLen = 254
)

// sanitize cleans the free text of a new order in place (in is NewOrder's
// own copy; items and metadata are copied, the caller's slices stay).
//
// A counter order is main's copy of an order the Pi already holds and
// printed: control characters go, but its text keeps the counter's own
// limit (MaxCounterText, checked by NewCounterOrder) so the copy stays true.
func (in *NewOrderInput) sanitize() {
	lim := func(n int) int {
		if in.Source == SourceCounter && n < MaxCounterText {
			return MaxCounterText
		}
		return n
	}
	in.SourceStatus = textclean.Clean(in.SourceStatus, lim(MaxNameLen))
	in.Notes = textclean.Clean(in.Notes, lim(MaxNoteLen))

	in.Customer.DisplayName = textclean.Clean(in.Customer.DisplayName, lim(MaxNameLen))
	in.Customer.Email = textclean.Clean(in.Customer.Email, lim(MaxEmailLen))
	in.Customer.Phone = textclean.Clean(in.Customer.Phone, lim(MaxPhoneLen))
	in.Customer.ExternalID = textclean.Clean(in.Customer.ExternalID, lim(MaxItemNameLen))

	in.Fulfillment.TableID = textclean.Clean(in.Fulfillment.TableID, lim(MaxNameLen))
	if a := in.Fulfillment.Address; a != nil {
		c := *a
		c.Line1 = textclean.Clean(c.Line1, lim(MaxAddressLineLen))
		c.Line2 = textclean.Clean(c.Line2, lim(MaxAddressLineLen))
		c.City = textclean.Clean(c.City, lim(MaxNameLen))
		c.State = textclean.Clean(c.State, lim(MaxNameLen))
		c.PostalCode = textclean.Clean(c.PostalCode, lim(MaxNameLen))
		c.Country = textclean.Clean(c.Country, lim(MaxNameLen))
		c.Notes = textclean.Clean(c.Notes, lim(MaxNoteLen))
		in.Fulfillment.Address = &c
	}

	in.Items = sanitizeItems(in.Items, lim)

	if len(in.Money.Fees) > 0 {
		fees := make([]Fee, len(in.Money.Fees))
		for i, f := range in.Money.Fees {
			f.Type = textclean.Clean(f.Type, lim(MaxNameLen))
			f.Label = textclean.Clean(f.Label, lim(MaxNameLen))
			fees[i] = f
		}
		in.Money.Fees = fees
	}
	if len(in.Money.Discounts) > 0 {
		ds := make([]Discount, len(in.Money.Discounts))
		for i, d := range in.Money.Discounts {
			d.Type = textclean.Clean(d.Type, lim(MaxNameLen))
			d.Code = textclean.Clean(d.Code, lim(MaxNameLen))
			d.Label = textclean.Clean(d.Label, lim(MaxNameLen))
			ds[i] = d
		}
		in.Money.Discounts = ds
	}

	if in.SourceMetadata != nil {
		in.SourceMetadata, _ = textclean.Any(in.SourceMetadata).(map[string]any)
	}
}

func sanitizeItems(items []LineItem, lim func(int) int) []LineItem {
	if items == nil {
		return nil
	}
	out := make([]LineItem, len(items))
	for i, it := range items {
		it.Name = textclean.Clean(it.Name, lim(MaxItemNameLen))
		it.Size = textclean.Clean(it.Size, lim(MaxNameLen))
		it.SKU = textclean.Clean(it.SKU, lim(MaxItemNameLen))
		it.Notes = textclean.Clean(it.Notes, lim(MaxNoteLen))
		it.Coupon = textclean.Clean(it.Coupon, lim(MaxItemNameLen))
		it.Children = sanitizeItems(it.Children, lim)
		out[i] = it
	}
	return out
}
