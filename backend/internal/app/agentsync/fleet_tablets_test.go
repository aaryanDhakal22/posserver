package agentsync

import "testing"

func TestHealthTablets(t *testing.T) {
	for raw, want := range map[string]string{
		`{"version":"2.2.0","tablets":[{"device_id":"d","name":"Counter","app_version":"1.7.0+1","connected":true}]}`: `[{"device_id":"d","name":"Counter","app_version":"1.7.0+1","connected":true}]`,
		`{"version":"2.1.0"}`: `[]`,
		`{"tablets":null}`:    `[]`,
		`not json`:            `[]`,
	} {
		if got := string(healthTablets([]byte(raw))); got != want {
			t.Errorf("%s: got %s", raw, got)
		}
	}
}
