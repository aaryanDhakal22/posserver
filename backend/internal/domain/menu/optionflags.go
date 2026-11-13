package menu

import (
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Option flags (MENU-PLAN, the owner 2026-11-12, migration 00046).
//
//   - show_on_driver_ticket: the choice prints on the delivery (driver)
//     ticket even when it costs $0 (SoftTouch rang drinks / desserts / sides
//     as 1¢ choices only so the driver saw them). Notes stay off it.
//   - counts for coupons: the option's mod family class is regular, so a
//     coupon's free toppings can be this option (premium / unpriced / no
//     family: never). It is not a column: the editor's toggle moves the
//     option between the group's regular and premium families.
//   - never_included (00009, the owner 2026-11-13): always charged, even on
//     an item whose price includes N toppings (item attribute
//     included_toppings).

// GroupOption is one option of a modifier group with its family and flag.
type GroupOption struct {
	ID                 uuid.UUID
	Name               string
	FamilyID           *uuid.UUID
	ShowOnDriverTicket bool
	// NeverIncluded (00009): always charged, even on an item whose price
	// includes N toppings (Shrimp on a build-your-own pizza).
	NeverIncluded bool
}

// Default family names a counts-for-coupons toggle creates when the group
// has no family of the class yet.
const (
	DefaultRegularFamily = "Topping"
	DefaultPremiumFamily = "Premium Topping"
)

// CountsForCoupons reports whether an option in family fam counts for
// coupons (its mod family's class is regular).
func CountsForCoupons(fam *uuid.UUID, fams []Family) bool {
	if fam == nil {
		return false
	}
	for _, f := range fams {
		if f.ID == *fam {
			return f.Kind == FamilyMod && f.Class == ClassRegular
		}
	}
	return false
}

// ToppingGroup: the group already has an option in a regular-class family
// (a new option there counts for coupons unless told otherwise).
func ToppingGroup(opts []GroupOption, fams []Family) bool {
	for _, o := range opts {
		if CountsForCoupons(o.FamilyID, fams) {
			return true
		}
	}
	return false
}

// CouponFamily decides the family an option moves to when its "counts for
// coupons" toggle is set to counts:
//
//   - nothing changes when the option already is where counts says (on: a
//     regular family; off: anything else — premium, unpriced or no family);
//   - on: the regular-class mod family most of the group's options use, else
//     the store's mod family named "Topping" when it is regular, else a new
//     "Topping" (regular);
//   - off: the same with premium and "Premium Topping".
//
// Ties go to the family listed first (fams' order: position, name). A
// family name taken by a family of another class gets the class in brackets
// ("Topping (regular)").
func CouponFamily(opts []GroupOption, fams []Family, optionID uuid.UUID, counts bool) (keep bool, family *uuid.UUID, create *Family) {
	var cur *uuid.UUID
	for _, o := range opts {
		if o.ID == optionID {
			cur = o.FamilyID
		}
	}
	if CountsForCoupons(cur, fams) == counts {
		return true, nil, nil
	}
	family, create = CouponTarget(opts, fams, counts)
	return false, family, create
}

// CouponTarget is the family an option of the group joins to count for
// coupons (counts) or to be a premium topping (!counts): see CouponFamily.
func CouponTarget(opts []GroupOption, fams []Family, counts bool) (family *uuid.UUID, create *Family) {
	class, name := ClassPremium, DefaultPremiumFamily
	if counts {
		class, name = ClassRegular, DefaultRegularFamily
	}
	byID := map[uuid.UUID]Family{}
	order := map[uuid.UUID]int{}
	for i, f := range fams {
		byID[f.ID] = f
		order[f.ID] = i
	}
	uses := map[uuid.UUID]int{}
	for _, o := range opts {
		if o.FamilyID == nil {
			continue
		}
		if f, ok := byID[*o.FamilyID]; ok && f.Kind == FamilyMod && f.Class == class {
			uses[f.ID]++
		}
	}
	if len(uses) > 0 {
		ids := make([]uuid.UUID, 0, len(uses))
		for id := range uses {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if uses[ids[i]] != uses[ids[j]] {
				return uses[ids[i]] > uses[ids[j]]
			}
			return order[ids[i]] < order[ids[j]]
		})
		id := ids[0]
		return &id, nil
	}
	taken := false
	for _, f := range fams {
		if f.Kind == FamilyMod && strings.EqualFold(f.Name, name) {
			if f.Class == class {
				id := f.ID
				return &id, nil
			}
			taken = true
		}
	}
	if taken {
		name += " (" + class + ")"
		for _, f := range fams {
			if f.Kind == FamilyMod && strings.EqualFold(f.Name, name) && f.Class == class {
				id := f.ID
				return &id, nil
			}
		}
	}
	return nil, &Family{Kind: FamilyMod, Name: name, Class: class}
}
