package sqsconsumer

import (
	"encoding/json"
	"testing"
)

func TestUnwrapV1Envelope(t *testing.T) {
	vendor := `{"order_id": 4417, "store_name": "Lombardi's Pizza", "items": []}`
	v1, _ := json.Marshal(map[string]string{
		"ID": "4417", "Payload": vendor, "DateCreated": "2026-10-16", "CreatedAt": "2026-10-16T18:00:00Z",
	})
	got, ok := unwrapV1Envelope(v1)
	if !ok || string(got) != vendor {
		t.Fatalf("v1 envelope: ok=%v got=%s", ok, got)
	}
	for name, body := range map[string]string{
		"raw vendor body":           vendor,
		"vendor with Payload field": `{"order_id": 1, "Payload": "x"}`,
		"not json":                  `nope`,
		"empty payload":             `{"ID": "1", "Payload": ""}`,
		"non-string payload":        `{"ID": "1", "Payload": {"order_id": 1}}`,
	} {
		if _, ok := unwrapV1Envelope([]byte(body)); ok {
			t.Errorf("%s must not be unwrapped", name)
		}
	}
}
