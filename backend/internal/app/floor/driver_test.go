package floor

import (
	"encoding/json"
	"testing"
)

// The wire's driver_name is omitempty, so only agent_status can say "no
// driver" (an order moved back to Unassigned, 4.0.2).
func TestEventDriver(t *testing.T) {
	for _, c := range []struct {
		name      string
		ev        event
		want      string
		wantKnown bool
	}{
		{"agent_status names the driver", event{DriverName: "Juma D.", AgentStatus: json.RawMessage(`{"state":"delivered","driver_name":"Juma D."}`)}, "Juma D.", true},
		{"agent_status without a driver = none", event{AgentStatus: json.RawMessage(`{"state":"delivered","batch_id":7}`)}, "", true},
		{"agent_status wins over the field", event{DriverName: "Old", AgentStatus: json.RawMessage(`{"driver_name":"New"}`)}, "New", true},
		{"older agent names the driver", event{DriverName: "Juma D."}, "Juma D.", true},
		{"older agent says nothing", event{}, "", false},
		{"null agent_status", event{AgentStatus: json.RawMessage(`null`)}, "", false},
		{"unreadable agent_status falls back", event{DriverName: "Abdul", AgentStatus: json.RawMessage(`"x"`)}, "Abdul", true},
	} {
		got, known := eventDriver(c.ev)
		if got != c.want || known != c.wantKnown {
			t.Errorf("%s: got %q %v, want %q %v", c.name, got, known, c.want, c.wantKnown)
		}
	}
}
