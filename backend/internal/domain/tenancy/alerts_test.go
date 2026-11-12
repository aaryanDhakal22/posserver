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

// A manager may keep the store's phones and add a new one nobody uses; not
// another store's, not one of ours, and only allow-listed ones when the admin
// set a list. The allow-list itself is the admin's.
func TestCheckManagerDevices(t *testing.T) {
	cur := Config{OwnerDevicesKey: "OwnerPhone"}
	others := []Config{{OwnerDevicesKey: "RivalPhone,Shared"}, {AllowedDevicesKey: "RivalSpare"}, nil}
	reserved := []string{"AdminPhone"}
	try := func(cur Config, devices string) (Config, error) {
		cfg := Config{OwnerDevicesKey: devices, AllowedDevicesKey: "Sneaky"}
		return cfg, CheckManagerDevices(cfg, cur, others, reserved)
	}
	var notAllowed *DeviceNotAllowedError

	for _, ok := range []string{"OwnerPhone", "", "OwnerPhone,NewTablet", "ownerphone"} {
		if _, err := try(cur, ok); err != nil {
			t.Errorf("%q: %v, want allowed", ok, err)
		}
	}
	for _, bad := range []string{"RivalPhone", "OwnerPhone,rivalphone", "RivalSpare", "AdminPhone", "adminphone"} {
		if _, err := try(cur, bad); !errors.As(err, &notAllowed) {
			t.Errorf("%q: err = %v, want DeviceNotAllowedError", bad, err)
		}
	}
	// The manager can't write the allow-list: absent stays absent, set stays as saved.
	if cfg, _ := try(cur, "OwnerPhone"); cfg[AllowedDevicesKey] != nil {
		t.Errorf("the manager set the allow-list: %v", cfg[AllowedDevicesKey])
	}
	listed := Config{OwnerDevicesKey: "OwnerPhone", AllowedDevicesKey: "Tablet2,Shared"}
	if cfg, _ := try(listed, "OwnerPhone"); cfg[AllowedDevicesKey] != "Tablet2,Shared" {
		t.Errorf("the allow-list changed: %v", cfg[AllowedDevicesKey])
	}
	// With a list: only its names (and the saved ones); a name the admin
	// allowed is fine even when another store uses it (one owner, two stores).
	for _, ok := range []string{"OwnerPhone,Tablet2", "Shared", "tablet2"} {
		if _, err := try(listed, ok); err != nil {
			t.Errorf("allow-listed %q: %v", ok, err)
		}
	}
	if _, err := try(listed, "NewTablet"); !errors.As(err, &notAllowed) || notAllowed.Name != "NewTablet" {
		t.Errorf("a name off the allow-list: err = %v", err)
	}
}
