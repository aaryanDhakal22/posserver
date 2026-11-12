package menu

import (
	"testing"

	"github.com/google/uuid"
)

func fid(n byte) uuid.UUID { return uuid.UUID{n} }

func famPtr(n byte) *uuid.UUID { u := fid(n); return &u }

func TestCouponFamily(t *testing.T) {
	top14 := Family{ID: fid(1), Kind: FamilyMod, Name: "14\" Topping", Class: ClassRegular}
	top12 := Family{ID: fid(2), Kind: FamilyMod, Name: "12\" Topping", Class: ClassRegular}
	prem := Family{ID: fid(3), Kind: FamilyMod, Name: "14\" Premium", Class: ClassPremium}
	unpriced := Family{ID: fid(4), Kind: FamilyMod, Name: "Notes", Class: ClassUnpriced}
	soda := Family{ID: fid(5), Kind: FamilyItem, Name: "Can Soda"}
	fams := []Family{top12, top14, prem, unpriced, soda}
	opt := func(n byte, fam *uuid.UUID) GroupOption { return GroupOption{ID: fid(100 + n), FamilyID: fam} }
	group := []GroupOption{opt(1, famPtr(1)), opt(2, famPtr(1)), opt(3, famPtr(2)), opt(4, famPtr(3)), opt(5, nil), opt(6, famPtr(4))}

	cases := []struct {
		name    string
		fams    []Family
		opts    []GroupOption
		option  byte
		counts  bool
		keep    bool
		family  *uuid.UUID
		newName string
		newCls  string
	}{
		{"already counts", fams, group, 1, true, true, nil, "", ""},
		{"premium already off", fams, group, 4, false, true, nil, "", ""},
		{"no family already off", fams, group, 5, false, true, nil, "", ""},
		{"unpriced already off", fams, group, 6, false, true, nil, "", ""},
		{"on: the regular family most of the group uses", fams, group, 4, true, false, famPtr(1), "", ""},
		{"on from no family", fams, group, 5, true, false, famPtr(1), "", ""},
		{"off: the group's premium family", fams, group, 1, false, false, famPtr(3), "", ""},
		{"off without a premium family in the group: a new Premium Topping", fams,
			[]GroupOption{opt(1, famPtr(1))}, 1, false, false, nil, DefaultPremiumFamily, ClassPremium},
		{"on in a group with no regular family: the store's Topping", append(fams, Family{ID: fid(9), Kind: FamilyMod, Name: "topping", Class: ClassRegular}),
			[]GroupOption{opt(5, nil)}, 5, true, false, famPtr(9), "", ""},
		{"on with no Topping family: a new one", fams, []GroupOption{opt(5, nil)}, 5, true, false, nil, DefaultRegularFamily, ClassRegular},
		{"Topping taken by a premium family", append(fams, Family{ID: fid(9), Kind: FamilyMod, Name: "Topping", Class: ClassPremium}),
			[]GroupOption{opt(5, nil)}, 5, true, false, nil, "Topping (regular)", ClassRegular},
		{"tie: the family listed first", fams, []GroupOption{opt(1, famPtr(1)), opt(3, famPtr(2)), opt(5, nil)}, 5, true, false, famPtr(2), "", ""},
		{"an item family named Topping doesn't count", append(fams, Family{ID: fid(9), Kind: FamilyItem, Name: "Topping"}),
			[]GroupOption{opt(5, nil)}, 5, true, false, nil, DefaultRegularFamily, ClassRegular},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keep, fam, create := CouponFamily(c.opts, c.fams, fid(100+c.option), c.counts)
			if keep != c.keep {
				t.Fatalf("keep = %v", keep)
			}
			if (fam == nil) != (c.family == nil) || (fam != nil && *fam != *c.family) {
				t.Fatalf("family = %v, want %v", fam, c.family)
			}
			if c.newName == "" && create != nil || c.newName != "" && (create == nil || create.Name != c.newName || create.Class != c.newCls || create.Kind != FamilyMod) {
				t.Fatalf("create = %+v, want %q %q", create, c.newName, c.newCls)
			}
		})
	}
	if !ToppingGroup(group, fams) || ToppingGroup([]GroupOption{opt(4, famPtr(3)), opt(5, nil)}, fams) {
		t.Error("topping group")
	}
	if CountsForCoupons(famPtr(5), fams) || !CountsForCoupons(famPtr(2), fams) || CountsForCoupons(nil, fams) {
		t.Error("counts for coupons")
	}
}
