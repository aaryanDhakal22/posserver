package diag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
)

// Reports from the counter terminal (Centurion's setup wizard / check
// everything / guides), relayed by the Pi with device_kind "terminal".

func TestParseReportDeviceObjectAndKind(t *testing.T) {
	r := mustParse(t, map[string]any{"tablet": nil, "kind": "setup", "outcome": "ok",
		"device": map[string]any{"name": "Counter 1", "app_version": "1.4.0+1762900000", "kind": "terminal"}})
	if r.TabletName != "Counter 1" || r.AppVersion != "1.4.0+1762900000" || r.DeviceKind != DeviceTerminal {
		t.Fatalf("device object: %+v", r)
	}
	if b, _ := r.JSON(); !strings.Contains(string(b), `"device_kind":"terminal"`) {
		t.Fatalf("device_kind not kept in the report: %s", b)
	}
	if q := mustParse(t, nil); q.DeviceKind != DeviceTablet {
		t.Fatalf("a Quibase report is a tablet's, got %q", q.DeviceKind)
	}
	if q := mustParse(t, map[string]any{"device_kind": "Something"}); q.DeviceKind != DeviceTablet {
		t.Fatalf("an unknown kind reads as a tablet, got %q", q.DeviceKind)
	}
}

func TestFormatPageTerminal(t *testing.T) {
	r := &Report{ID: uuid.New(), Kind: "setup", Outcome: "not_fixed", Headline: "Counter 1 set up: drawer not confirmed",
		AppVersion: "1.4.0", AgentVersion: "2.6.0", DeviceKind: DeviceTerminal}
	_, msg := FormatPage("Lombardi's", r, ViaPi, "Counter 1")
	if !strings.Contains(msg, "Terminal: Counter 1 · Centurion 1.4.0 · Pi 2.6.0 · sent via Pi") {
		t.Fatalf("terminal page:\n%s", msg)
	}
}

func TestShouldPageTerminalChecks(t *testing.T) {
	for _, c := range []struct {
		kind, outcome string
		want          bool
	}{
		{"setup", "ok", false},
		{"setup", "not_fixed", true},
		{"full_check", "ok", false},
		{"full_check", "not_fixed", true},
	} {
		if got := ShouldPage(&Report{Kind: c.kind, Outcome: c.outcome, DeviceKind: DeviceTerminal}); got != c.want {
			t.Errorf("terminal %s/%s pages = %v, want %v", c.kind, c.outcome, got, c.want)
		}
	}
}

func TestHandleRPCTerminalKindFromThePi(t *testing.T) {
	clock := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	s, db, pager := testService(&clock)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	rep := reportJSON(t, map[string]any{"kind": "setup", "outcome": "not_fixed", "scenario": "setup", "headline": "Counter 1 set up: reader skipped"})
	body, _ := json.Marshal(map[string]any{"tablet_id": "term-1", "tablet_name": "Counter 1", "device_kind": "terminal",
		"report": json.RawMessage(rep), "pi": map[string]any{"version": "2.6.0"}})
	if _, err := s.HandleRPC(context.Background(), scope, body); err != nil {
		t.Fatal(err)
	}
	if len(db.reports) != 1 {
		t.Fatalf("%d reports stored", len(db.reports))
	}
	for _, p := range db.reports {
		if p.TabletID != "term-1" || p.TabletName != "Counter 1" || !strings.Contains(string(p.Report), `"device_kind":"terminal"`) {
			t.Fatalf("stored %+v / %s", p, p.Report)
		}
	}
	if len(pager.sent) != 1 || !strings.Contains(pager.sent[0].msg, "Terminal: Counter 1") {
		t.Fatalf("pages: %+v", pager.sent)
	}
}
