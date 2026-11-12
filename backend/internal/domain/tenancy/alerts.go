package tenancy

import (
	"fmt"
	"strings"
)

// OwnerDevicesKey is where the store owner's Pushover device names live in a
// location config: a comma list the agent sends floor problems to (it reads
// `location.config.pushover_owner_devices` from store_config).
const OwnerDevicesKey = "pushover_owner_devices"

// maxDeviceName is Pushover's limit for a device name.
const maxDeviceName = 25

// BadDeviceError is a device name Pushover would refuse.
type BadDeviceError struct{ Name string }

func (e *BadDeviceError) Error() string {
	return fmt.Sprintf("%q is not a Pushover device name (1-25 letters, numbers, - or _)", e.Name)
}

// ValidDeviceName reports whether s is 1-25 of [A-Za-z0-9_-] (Pushover's rule).
func ValidDeviceName(s string) bool {
	if len(s) == 0 || len(s) > maxDeviceName {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// SplitDevices splits a comma list into trimmed, de-duplicated names (empty
// items dropped, first spelling kept). It does not validate.
func SplitDevices(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range strings.Split(raw, ",") {
		if d = strings.TrimSpace(d); d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// NormalizeDevices validates a comma list of device names and returns it
// trimmed, de-duplicated and joined with ",". An empty list is "".
func NormalizeDevices(raw string) (string, error) {
	ds := SplitDevices(raw)
	for _, d := range ds {
		if !ValidDeviceName(d) {
			return "", &BadDeviceError{Name: d}
		}
	}
	return strings.Join(ds, ","), nil
}

// NormalizeOwnerDevices validates + normalizes the owner device list in a
// location config in place (absent stays absent; "" clears it).
func NormalizeOwnerDevices(cfg Config) error {
	raw, present := cfg[OwnerDevicesKey]
	if !present || raw == nil {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return &BadDeviceError{Name: fmt.Sprint(raw)}
	}
	norm, err := NormalizeDevices(s)
	if err != nil {
		return err
	}
	cfg[OwnerDevicesKey] = norm
	if raw, present := cfg[AllowedDevicesKey]; present && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return &BadDeviceError{Name: fmt.Sprint(raw)}
		}
		if cfg[AllowedDevicesKey], err = NormalizeDevices(s); err != nil {
			return err
		}
	}
	return nil
}

// OwnerDevices reads the owner device names from a location config.
func OwnerDevices(cfg Config) []string {
	s, _ := cfg[OwnerDevicesKey].(string)
	return SplitDevices(s)
}

// AllowedDevicesKey is an admin-set list in a location config: when it has
// names, the store's manager may only pick the owner's alert phones from it.
// A manager can never change it (PATCH /store/v1/me/location keeps the saved
// value); the admin PATCH can.
const AllowedDevicesKey = "pushover_allowed_devices"

// AllowedDevices reads the admin's allow-list from a location config.
func AllowedDevices(cfg Config) []string {
	s, _ := cfg[AllowedDevicesKey].(string)
	return SplitDevices(s)
}

// DeviceNotAllowedError is a device name this store's manager may not save:
// it is another store's phone, one of QuiccPOS's own, or not on the store's
// allow-list. Every store's alerts go out through one Pushover account, so
// a device name is the only thing that says whose phone rings (security
// review: a manager could type another store's device name and page it).
type DeviceNotAllowedError struct{ Name string }

func (e *DeviceNotAllowedError) Error() string {
	return fmt.Sprintf("%q isn't one of this store's phones. Ask QuiccPOS to add it.", e.Name)
}

// CheckManagerDevices decides whether a manager may save cfg's owner devices
// for a store whose saved config is cur. others = every other store's config,
// reserved = QuiccPOS's own device names. A name already saved for the store
// always stays allowed (so an unrelated save never fails); a new one must be
// on the store's allow-list when it has one, and may never be a reserved name
// or one another store uses or is allowed. Names compare without case, as
// Pushover does. cfg's allow-list is set back to the saved one.
func CheckManagerDevices(cfg, cur Config, others []Config, reserved []string) error {
	if cfg == nil {
		return nil
	}
	if v, ok := cur[AllowedDevicesKey]; ok {
		cfg[AllowedDevicesKey] = v
	} else {
		delete(cfg, AllowedDevicesKey)
	}
	raw, _ := cfg[OwnerDevicesKey].(string) // a non-string is NormalizeOwnerDevices' bad_device
	set := func(names []string) map[string]bool {
		m := make(map[string]bool, len(names))
		for _, n := range names {
			m[strings.ToLower(n)] = true
		}
		return m
	}
	saved, allowed, taken := set(OwnerDevices(cur)), set(AllowedDevices(cur)), set(reserved)
	for _, o := range others {
		for _, n := range append(OwnerDevices(o), AllowedDevices(o)...) {
			taken[strings.ToLower(n)] = true
		}
	}
	for _, d := range SplitDevices(raw) {
		k := strings.ToLower(d)
		if saved[k] {
			continue
		}
		if (len(allowed) > 0 && !allowed[k]) || (taken[k] && !allowed[k]) {
			return &DeviceNotAllowedError{Name: d}
		}
	}
	return nil
}
