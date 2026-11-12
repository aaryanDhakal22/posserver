package notify

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// Test-store notice modes (env NOTIFY_TEST_STORES).
const (
	// TestStoresLog: a test store's notices are logged (and kept by dev's
	// Recorder) but never reach a phone. The default.
	TestStoresLog = "log"
	// TestStoresPushover: a test store pages like a real one.
	TestStoresPushover = "pushover"
	// TestStoresDevicePrefix + a device name: Pushover to that one device of
	// the owner's account only (e.g. a muted "quicc-tests" device).
	TestStoresDevicePrefix = "device:"
)

// TestStores routes the notices of test stores (tenants.test_store: the
// throwaway stores the flows and pytest make on dev and staging) away from
// the owner's phones (2026-11-12: a staging flow that flags money on purpose
// paged him). Inner gets every real store's notice and a test store's under
// TestStoresPushover; Device (a pager set to the one device) gets a test
// store's under "device:<name>"; otherwise nothing is sent. When IsTest
// fails the notice goes to Inner (a real store must never lose a page).
type TestStores struct {
	Inner  Notifier
	IsTest func(ctx context.Context, locationID uuid.UUID) (bool, error)
	Mode   string
	Device Pager
}

// ParseTestStoresMode normalises the env value ("" = log); ok=false for an
// unknown one (then log).
func ParseTestStoresMode(v string) (string, bool) {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || v == TestStoresLog:
		return TestStoresLog, true
	case v == TestStoresPushover:
		return v, true
	case strings.HasPrefix(v, TestStoresDevicePrefix) && strings.TrimPrefix(v, TestStoresDevicePrefix) != "":
		return v, true
	}
	return TestStoresLog, false
}

// DeviceOf is the device a "device:<name>" mode names ("" otherwise).
func DeviceOf(mode string) string {
	if d, ok := strings.CutPrefix(mode, TestStoresDevicePrefix); ok {
		return d
	}
	return ""
}

func (t TestStores) Notify(ctx context.Context, n Notice) error {
	test := false
	if t.IsTest != nil {
		if v, err := t.IsTest(ctx, n.LocationID); err == nil {
			test = v
		}
	}
	switch {
	case !test || t.Mode == TestStoresPushover:
		return t.Inner.Notify(ctx, n)
	case DeviceOf(t.Mode) != "" && t.Device != nil && t.Device.Enabled():
		return t.Device.SendAlso(ctx, nil, "[test store] "+n.Title, n.Message, "none")
	}
	return nil
}
