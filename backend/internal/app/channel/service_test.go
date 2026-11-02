package channelApp

import (
	"testing"

	channelDom "quiccpos/main/internal/domain/channel"
)

func TestDefaultPermissions(t *testing.T) {
	web := channelDom.DefaultPermissions(channelDom.KindWeb)
	if !web.Orders || len(web.Payments) != 1 || web.Payments[0] != channelDom.PayCard {
		t.Errorf("web: %+v (card only)", web)
	}
	v := channelDom.DefaultPermissions(channelDom.KindVendor)
	if v.Menu || v.Quote || !v.Orders {
		t.Errorf("vendor: %+v (intake only)", v)
	}
	if p := channelDom.DefaultPermissions("fax"); p.Orders || p.Payments == nil {
		t.Errorf("unknown kind: %+v", p)
	}
	for _, k := range channelDom.Kinds {
		if !k.Valid() {
			t.Errorf("%s not valid", k)
		}
	}
	if channelDom.Kind("fax").Valid() {
		t.Error("fax is not a kind")
	}
}

func TestValidPermissions(t *testing.T) {
	ok := []channelDom.Permissions{
		{Payments: []string{}},
		{Payments: []string{"card", "cash"}},
	}
	for _, p := range ok {
		if err := validPermissions(p); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	bad := []channelDom.Permissions{{}, {Payments: []string{"bitcoin"}}, {Payments: []string{""}}}
	for _, p := range bad {
		if err := validPermissions(p); err == nil {
			t.Errorf("%+v accepted", p)
		}
	}
}
