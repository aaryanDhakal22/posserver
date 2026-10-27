package tenancy

import (
	"errors"
	"strings"
	"testing"
)

func TestValidDeviceName(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"AaryanPhone", true},
		{"owner_phone-2", true},
		{strings.Repeat("a", 25), true},
		{strings.Repeat("a", 26), false},
		{"", false},
		{"my phone", false},
		{"phone!", false},
		{"téléphone", false},
	} {
		if got := ValidDeviceName(c.name); got != c.ok {
			t.Errorf("ValidDeviceName(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
}

func TestNormalizeDevices(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"a", "a"},
		{" a , b,a,, c ", "a,b,c"},
		{"OwnerPhone,ownerphone", "OwnerPhone,ownerphone"},
	} {
		got, err := NormalizeDevices(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeDevices(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	_, err := NormalizeDevices("ok, not ok")
	var bad *BadDeviceError
	if !errors.As(err, &bad) || bad.Name != "not ok" {
		t.Fatalf("want BadDeviceError for %q, got %v", "not ok", err)
	}
}

func TestNormalizeOwnerDevices(t *testing.T) {
	cfg := Config{OwnerDevicesKey: " b, a ,b", "other": 1}
	if err := NormalizeOwnerDevices(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg[OwnerDevicesKey] != "b,a" || cfg["other"] != 1 {
		t.Fatalf("got %v", cfg)
	}
	if got := OwnerDevices(cfg); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("OwnerDevices = %v", got)
	}

	empty := Config{}
	if err := NormalizeOwnerDevices(empty); err != nil || len(empty) != 0 {
		t.Fatalf("absent key must stay absent: %v %v", empty, err)
	}
	if len(OwnerDevices(empty)) != 0 {
		t.Fatal("no devices expected")
	}

	var bad *BadDeviceError
	if err := NormalizeOwnerDevices(Config{OwnerDevicesKey: 42}); !errors.As(err, &bad) {
		t.Fatalf("a non-string value must be refused, got %v", err)
	}
	if err := NormalizeOwnerDevices(Config{OwnerDevicesKey: "x y"}); !errors.As(err, &bad) {
		t.Fatalf("want BadDeviceError, got %v", err)
	}
}
