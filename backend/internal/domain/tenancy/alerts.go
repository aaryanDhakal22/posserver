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
	return nil
}

// OwnerDevices reads the owner device names from a location config.
func OwnerDevices(cfg Config) []string {
	s, _ := cfg[OwnerDevicesKey].(string)
	return SplitDevices(s)
}
