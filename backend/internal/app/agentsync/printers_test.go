package agentsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/tenancy"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// fakeTenancy is an in-memory tenancy repository for the printer handlers;
// any other method panics (nil embedded interface).
type fakeTenancy struct {
	tenancy.Repository
	loc      tenancy.Location
	printers []*tenancy.Printer
	changes  int
}

func (f *fakeTenancy) GetLocationByID(_ context.Context, id uuid.UUID) (*tenancy.Location, error) {
	if id != f.loc.ID {
		return nil, fmt.Errorf("get location: %w", pgx.ErrNoRows)
	}
	l := f.loc
	return &l, nil
}

func (f *fakeTenancy) ListPrintersByLocation(_ context.Context, lid uuid.UUID) ([]tenancy.Printer, error) {
	var out []tenancy.Printer
	for _, p := range f.printers {
		if p.LocationID == lid {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakeTenancy) CreatePrinter(_ context.Context, tid, lid uuid.UUID, name, ip, mac string) (*tenancy.Printer, error) {
	for _, p := range f.printers {
		if p.LocationID == lid && p.Name == name {
			return nil, tenancy.ErrPrinterNameTaken
		}
	}
	p := &tenancy.Printer{ID: uuid.New(), TenantID: tid, LocationID: lid, Name: name, IP: ip, MAC: mac}
	f.printers = append(f.printers, p)
	return p, nil
}

func (f *fakeTenancy) GetPrinterByID(_ context.Context, id uuid.UUID) (*tenancy.Printer, error) {
	for _, p := range f.printers {
		if p.ID == id {
			cp := *p
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("get printer: %w", pgx.ErrNoRows)
}

func (f *fakeTenancy) SetReceiptPrinter(_ context.Context, _, id uuid.UUID, on bool) (*tenancy.Printer, error) {
	for _, p := range f.printers {
		p.IsReceipt = on && p.ID == id
	}
	return f.GetPrinterByID(context.Background(), id)
}

func (f *fakeTenancy) MovePrinterIP(_ context.Context, lid, id uuid.UUID, oldIP, newIP, mac, _ string) (*tenancy.Printer, error) {
	for _, p := range f.printers {
		if p.ID == id && p.LocationID == lid && p.IP == oldIP {
			p.IP, p.MAC = newIP, mac
			f.changes++
			cp := *p
			return &cp, nil
		}
	}
	return nil, tenancy.ErrPrinterIPChanged
}

type pushRecorder struct{ scopes []sseBroker.Scope }

func (r *pushRecorder) push(_ context.Context, tid, lid uuid.UUID) {
	r.scopes = append(r.scopes, sseBroker.Scope{TenantID: tid, LocationID: lid})
}

func newPrinterService() (*Service, *fakeTenancy, *pushRecorder, sseBroker.Scope) {
	loc := tenancy.Location{ID: uuid.New(), TenantID: uuid.New()}
	ft := &fakeTenancy{loc: loc}
	s := &Service{tenancy: tenancyApp.NewService(ft, zerolog.Nop()), logger: zerolog.Nop()}
	rec := &pushRecorder{}
	s.OnPrintersChanged(rec.push)
	return s, ft, rec, sseBroker.Scope{TenantID: loc.TenantID, LocationID: loc.ID}
}

func call(t *testing.T, h rpc.InboundHandler, scope sseBroker.Scope, body string) map[string]any {
	t.Helper()
	reply, err := h(context.Background(), scope, json.RawMessage(body))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	raw, _ := json.Marshal(reply)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestHandlePrinterAdd(t *testing.T) {
	s, ft, rec, scope := newPrinterService()

	got := call(t, s.HandlePrinterAdd, scope, `{"name":" Kitchen ","ip":"10.0.0.50","mac":"AA:BB:CC:DD:EE:01"}`)
	if got["ok"] != true || got["name"] != "Kitchen" || got["ip"] != "10.0.0.50" || got["id"] == "" {
		t.Fatalf("reply = %v", got)
	}
	if len(ft.printers) != 1 || !ft.printers[0].IsReceipt || ft.printers[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("printer = %+v, want the first printer as receipt with its mac", ft.printers[0])
	}
	if len(rec.scopes) != 1 || rec.scopes[0] != scope {
		t.Fatalf("store_config pushes = %v, want one to the scope", rec.scopes)
	}

	for body, code := range map[string]string{
		`{"name":"kitchen","ip":"10.0.0.51"}`: "name_taken", // case-insensitive
		`{"name":"","ip":"10.0.0.51"}`:        "bad_request",
		`{"name":"Bar","ip":" "}`:             "bad_request",
		`not json`:                            "bad_request",
	} {
		if got := call(t, s.HandlePrinterAdd, scope, body); got["ok"] != false || got["code"] != code {
			t.Errorf("%s → %v, want code %s", body, got, code)
		}
	}
	if len(rec.scopes) != 1 {
		t.Fatalf("a refused add must not push store_config (%d pushes)", len(rec.scopes))
	}
}

func TestHandlePrinterMoved(t *testing.T) {
	s, ft, rec, scope := newPrinterService()
	p := &tenancy.Printer{ID: uuid.New(), TenantID: scope.TenantID, LocationID: scope.LocationID, Name: "Pizza", IP: "10.0.0.20"}
	ft.printers = append(ft.printers, p)

	body := fmt.Sprintf(`{"printer_id":%q,"old_ip":"10.0.0.20","new_ip":"10.0.0.33","mac":"aa:bb:cc:dd:ee:02"}`, p.ID)
	if got := call(t, s.HandlePrinterMoved, scope, body); got["applied"] != true || got["reason"] != "" {
		t.Fatalf("reply = %v, want applied", got)
	}
	if p.IP != "10.0.0.33" || ft.changes != 1 || len(rec.scopes) != 1 {
		t.Fatalf("ip=%s changes=%d pushes=%d", p.IP, ft.changes, len(rec.scopes))
	}

	// The same report again (the Pi retried): applied, nothing changes.
	if got := call(t, s.HandlePrinterMoved, scope, body); got["applied"] != true {
		t.Fatalf("repeat reply = %v, want applied", got)
	}
	if ft.changes != 1 || len(rec.scopes) != 1 {
		t.Fatal("a repeated move must not log or push again")
	}

	stale := fmt.Sprintf(`{"printer_id":%q,"old_ip":"10.0.0.20","new_ip":"10.0.0.44","mac":""}`, p.ID)
	if got := call(t, s.HandlePrinterMoved, scope, stale); got["applied"] != false || got["reason"] != "stale_old_ip" {
		t.Fatalf("stale reply = %v", got)
	}

	other := sseBroker.Scope{TenantID: scope.TenantID, LocationID: uuid.New()}
	if got := call(t, s.HandlePrinterMoved, other, body); got["applied"] != false || got["reason"] != "not_found" {
		t.Fatalf("other location reply = %v, want not_found", got)
	}
	if got := call(t, s.HandlePrinterMoved, scope, `{"printer_id":"nope","old_ip":"a","new_ip":"b"}`); got["reason"] != "not_found" {
		t.Fatalf("bad id reply = %v, want not_found", got)
	}
	if _, err := s.HandlePrinterMoved(context.Background(), scope, json.RawMessage(`{"printer_id":"x","old_ip":"","new_ip":""}`)); !errors.Is(err, rpc.ErrBadPayload) {
		t.Fatalf("missing ips: err = %v, want ErrBadPayload", err)
	}
}

func TestHandleUpdateStatusRejectsBadPayloads(t *testing.T) {
	s := &Service{logger: zerolog.Nop()}
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	for _, body := range []string{
		`not json`,
		`{"state":"running"}`,                     // no id
		`{"id":"abc","state":""}`,                 // no state
		`{"id":"has spaces","state":"running"}`,   // id outside [A-Za-z0-9-]
		`{"id":"abc","state":"Running; DROP it"}`, // state outside [a-z_]
	} {
		if _, err := s.HandleUpdateStatus(context.Background(), scope, json.RawMessage(body)); !errors.Is(err, rpc.ErrBadPayload) {
			t.Errorf("%s: err = %v, want ErrBadPayload", body, err)
		}
	}
}

func TestRequestUpdateRefusesUnknownLocation(t *testing.T) {
	s, _, _, scope := newPrinterService()
	_, err := s.RequestUpdate(context.Background(), nil, UpdateRequest{
		TenantID: uuid.New(), LocationID: scope.LocationID, Action: UpdateActionUpdate, RequestedBy: "admin",
	})
	if !errors.Is(err, ErrUnknownLocation) {
		t.Fatalf("another tenant's location: err = %v, want ErrUnknownLocation", err)
	}
	if _, err := s.RequestUpdate(context.Background(), nil, UpdateRequest{
		TenantID: scope.TenantID, LocationID: scope.LocationID, Action: "reboot",
	}); err == nil {
		t.Fatal("an unknown action must be refused")
	}
}

func TestUpdateStatusAcceptsEmptyTimes(t *testing.T) {
	// The Pi sends every field; while waiting/running the times are "".
	var r updateStatus
	body := `{"id":"abc","action":"update","state":"requested","started_at":"","finished_at":"","from_version":"2.0.6","to_version":""}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("an in-progress status must decode: %v", err)
	}
}
