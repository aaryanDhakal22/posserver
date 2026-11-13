package sse

import (
	"encoding/json"
	"strings"
	"testing"
)

// owner_offline_alert_by_main rides store_config only when main pages the
// owner: false is left out, so older frames stay byte-identical (golden).
func TestOwnerOfflineAlertByMainOnlyWhenTrue(t *testing.T) {
	off, _ := json.Marshal(StoreContext{})
	if strings.Contains(string(off), "owner_offline_alert_by_main") {
		t.Fatalf("false must be left out: %s", off)
	}
	on, _ := json.Marshal(StoreContext{OwnerOfflineAlertByMain: true})
	if !strings.HasSuffix(string(on), `"owner_offline_alert_by_main":true}`) {
		t.Fatalf("true = %s", on)
	}
}
