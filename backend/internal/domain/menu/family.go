package menu

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Family is a named group of a store's bank items (kind item: Can Soda, Hot
// Subs, …) or modifier options (kind mod: 14" Pizza Toppings, …). A mod
// family has a class: regular (a coupon may make it free), premium or
// unpriced (never). Coupons' parts name item families (migration 00045).
type Family struct {
	ID         uuid.UUID `json:"id"`
	LocationID uuid.UUID `json:"location_id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Class      string    `json:"class"`
	Position   int       `json:"position"`
}

// Family kinds and mod classes.
const (
	FamilyItem = "item"
	FamilyMod  = "mod"

	ClassRegular  = "regular"
	ClassPremium  = "premium"
	ClassUnpriced = "unpriced"

	MaxFamilyName = 60
)

// ErrBadFamily: a family's kind, name or class is wrong.
var ErrBadFamily = errors.New("bad family")

// Normalize checks a family and fills its defaults: the name trimmed (1–60
// characters), an item family's class "" and a mod family's class regular
// unless it names premium / unpriced.
func (f *Family) Normalize() error {
	f.Name = strings.Join(strings.Fields(f.Name), " ")
	if f.Name == "" || utf8.RuneCountInString(f.Name) > MaxFamilyName {
		return errors.Join(ErrBadFamily, errors.New("a family's name is 1–60 characters"))
	}
	switch f.Kind {
	case FamilyItem:
		if f.Class != "" {
			return errors.Join(ErrBadFamily, errors.New("an item family has no class"))
		}
	case FamilyMod:
		switch f.Class {
		case "":
			f.Class = ClassRegular
		case ClassRegular, ClassPremium, ClassUnpriced:
		default:
			return errors.Join(ErrBadFamily, errors.New("a mod family's class is regular, premium or unpriced"))
		}
	default:
		return errors.Join(ErrBadFamily, errors.New("a family's kind is item or mod"))
	}
	return nil
}

// ErrFamilyNameTaken: another family of the store and kind has that name.
var ErrFamilyNameTaken = errors.New("family name taken")

// FamilyMember is a bank item (Parent = its category) or option (Parent =
// its group) with its family, for the Families editor.
type FamilyMember struct {
	ID         uuid.UUID
	Name       string
	Parent     string
	ParentID   uuid.UUID
	PriceCents int64
	FamilyID   *uuid.UUID
}
