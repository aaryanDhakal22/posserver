package callcenter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	storestatusApp "quiccpos/main/internal/app/storestatus"
	"quiccpos/main/internal/domain/staff"
	"quiccpos/main/internal/infra/database/repositories"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

func TestAllowlist(t *testing.T) {
	allowed := [][2]string{{"GET", "menu"}, {"GET", "status"}, {"GET", "session"}, {"POST", "quote"}, {"POST", "orders"},
		{"GET", "orders"}, {"GET", "orders/x"}, {"PUT", "orders/x/cart"}, {"POST", "orders/x/payments"},
		{"POST", "square/keyed-payments"}, {"POST", "orders/x/void"}, {"POST", "orders/x/time"}, {"POST", "orders/x/reprint"},
		{"GET", "pickups"}, {"GET", "checks"}, {"GET", "checks/x"}, {"GET", "drivers"}, {"GET", "floor"}, {"GET", "drafts"},
		{"PUT", "drafts/d"}, {"DELETE", "drafts/d"}, {"GET", "customers"}, {"POST", "customers"}, {"PATCH", "customers/c"},
		{"POST", "customers/c/addresses"}, {"GET", "customers/c/orders"}, {"GET", "addresses"}, {"POST", "journal"}}
	for _, a := range allowed {
		if !Allowed(a[0], a[1]) {
			t.Errorf("%s %s should be allowed", a[0], a[1])
		}
	}
	refused := [][2]string{{"POST", "login"}, {"POST", "logout"}, {"POST", "manager-check"}, {"POST", "refunds"},
		{"POST", "orders/x/waste"}, {"GET", "unsettled"}, {"POST", "orders/x/picked-up"}, {"GET", "drawer"},
		{"POST", "drawer/open"}, {"POST", "drawer/pay-in"}, {"POST", "drawer/pay-out"}, {"POST", "drawer/close"},
		{"POST", "drawer/handover"}, {"GET", "z"}, {"GET", "reports/till"}, {"GET", "reports/drivers"}, {"POST", "close"},
		{"POST", "orders/x/payments/p/tip"}, {"POST", "deliveries/send-out"}, {"POST", "drivers/d/returned"},
		{"GET", "setup"}, {"POST", "print"}, {"GET", "square/session"}, {"POST", "square/test-refund"}, {"GET", "app/apk"},
		{"GET", "journal"}, {"GET", "driver/mine"}, {"POST", "driver/claim"}, {"GET", "phones"}, {"POST", "phone-charges"},
		{"DELETE", "orders/x"}, {"POST", "pickups"}, {"POST", "floor"}, {"GET", "orders/x/cart"}, {"GET", "drafts/d"},
		{"GET", "orders/x/y/z"}, {"GET", ""}}
	for _, a := range refused {
		if Allowed(a[0], a[1]) {
			t.Errorf("%s %s should be refused", a[0], a[1])
		}
	}
	for _, bad := range []string{"", "/", "../devices", "orders/../x", "orders/./x", "orders/a b", "orders/a%2Fb", "orders/a?x", "a//b"} {
		if p, ok := CleanPath(bad); ok {
			t.Errorf("CleanPath(%q) = %q, want refused", bad, p)
		}
	}
	if p, ok := CleanPath("/orders/x/"); !ok || p != "orders/x" {
		t.Errorf("CleanPath trims slashes: %q %v", p, ok)
	}
	if !NeedsOpenStore("POST", "orders") || !NeedsOpenStore("PUT", "orders/x/cart") || !NeedsOpenStore("POST", "square/keyed-payments") ||
		NeedsOpenStore("GET", "orders") || NeedsOpenStore("POST", "orders/x/void") || NeedsOpenStore("POST", "quote") {
		t.Error("NeedsOpenStore: placing, changing and charging only")
	}
}

func TestGuard(t *testing.T) {
	cases := []struct {
		method, path, body string
		what, code         string
	}{
		{"POST", "orders", `{"client_key":"k","type":"pickup","cart":{"lines":[{"item_id":"i"}]}}`, "", ""},
		{"POST", "orders", `{"client_key":"k","type":"delivery","cart":{"lines":[],"code":"SAVE"},"payments":[{"tender":"manual_card"}]}`, "", ""},
		{"POST", "orders", `{"client_key":"k","type":"counter","cart":{}}`, "type", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","cart":{}}`, "type", CodeNotAllowed},
		{"POST", "orders", `{"type":"pickup","cart":{}}`, "", "client_key_required"},
		{"POST", "orders", `{"client_key":"k","type":"pickup","payments":[{"tender":"cash"}]}`, "tender", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","type":"pickup","payments":[{"tender":"comp"}]}`, "comp", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","type":"pickup","payments":[{"tender":"manual_card","refund":true}]}`, "refund", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","type":"pickup","manager_pin":"1234"}`, "manager", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","type":"pickup","manager_pin":""}`, "", ""},
		{"POST", "orders", `{"client_key":"k","type":"pickup","approvals":["a"]}`, "manager", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","type":"pickup","cart":{"discount":{"pct":5}}}`, "discount", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","type":"pickup","cart":{"discount":null}}`, "", ""},
		{"POST", "orders", `{"client_key":"k","type":"pickup","cart":{"lines":[{"custom":true,"price_cents":5}]}}`, "open_price", CodeNotAllowed},
		{"POST", "orders", `{"client_key":"k","type":"pickup","cart":{"lines":[{"item_id":"i","price_cents":5}]}}`, "price_change", CodeNotAllowed},
		{"POST", "quote", `{"cart":{"lines":[{"custom":true,"price_cents":5}]}}`, "", ""},
		{"POST", "quote", `{"cart":{"discount":{"amount_cents":5}}}`, "discount", CodeNotAllowed},
		{"PUT", "orders/x/cart", `{"cart":{"lines":[{"custom":true,"price_cents":5},{"item_id":"i","price_cents":9}]}}`, "", ""},
		{"PUT", "orders/x/cart", `{"type":"counter","cart":{}}`, "type", CodeNotAllowed},
		{"PUT", "orders/x/cart", `{"cart":{"employee_meal":true}}`, "discount", CodeNotAllowed},
		{"POST", "orders/x/payments", `{"tender":"manual_card","amount_cents":5}`, "", ""},
		{"POST", "orders/x/payments", `{"tender":"cash"}`, "tender", CodeNotAllowed},
		{"POST", "orders/x/payments", `{"tender":"square_app"}`, "tender", CodeNotAllowed},
		{"POST", "orders/x/payments", `{"tender":"manual_card","refund":true}`, "refund", CodeNotAllowed},
		{"POST", "square/keyed-payments", `{"nonce":"n","amount_cents":5}`, "", "idempotency_key_required"},
		{"POST", "square/keyed-payments", `{"nonce":"n","amount_cents":5,"idempotency_key":"k"}`, "", ""},
		{"POST", "orders/x/void", `{"reason":"x"}`, "", ""},
		{"POST", "orders/x/void", `{"reason":"x","manager_pin":"9"}`, "manager", CodeNotAllowed},
		{"GET", "orders", ``, "", ""},
		{"POST", "journal", `[1,2]`, "", ""}, // not an object: the Pi answers
	}
	for _, c := range cases {
		r := Guard(c.method, c.path, []byte(c.body))
		switch {
		case c.code == "" && r != nil:
			t.Errorf("%s %s %s: refused %+v, want allowed", c.method, c.path, c.body, *r)
		case c.code != "" && (r == nil || r.Code != c.code || r.What != c.what):
			t.Errorf("%s %s %s: got %+v, want %s/%s", c.method, c.path, c.body, r, c.code, c.what)
		}
	}
}

func TestPinWait(t *testing.T) {
	want := map[int]time.Duration{1: 0, 4: 0, 5: 30 * time.Second, 6: time.Minute, 7: 2 * time.Minute, 8: 4 * time.Minute,
		9: 5 * time.Minute, 19: 5 * time.Minute}
	for n, w := range want {
		if got := pinWait(n); got != w {
			t.Errorf("pinWait(%d) = %v, want %v", n, got, w)
		}
	}
}

// --- fakes --------------------------------------------------------------------

type fakeRepo struct {
	dev     map[string]*repositories.CallcenterDevice // by token hash
	revoked map[uuid.UUID]bool
	seen    int
}

func (f *fakeRepo) byID(id uuid.UUID) *repositories.CallcenterDevice {
	for _, d := range f.dev {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (f *fakeRepo) Create(_ context.Context, tid, lid uuid.UUID, name, hash string, max int) (repositories.CallcenterDevice, error) {
	if len(f.dev) >= max {
		return repositories.CallcenterDevice{}, repositories.ErrCallcenterTooMany
	}
	d := &repositories.CallcenterDevice{ID: uuid.New(), TenantID: tid, LocationID: lid, Name: name}
	f.dev[hash] = d
	return *d, nil
}

func (f *fakeRepo) ByTokenHash(_ context.Context, hash string) (repositories.CallcenterDevice, error) {
	d, ok := f.dev[hash]
	if !ok || f.revoked[d.ID] {
		return repositories.CallcenterDevice{}, pgx.ErrNoRows
	}
	return *d, nil
}

func (f *fakeRepo) List(context.Context, uuid.UUID) ([]repositories.CallcenterDevice, error) {
	return nil, nil
}

func (f *fakeRepo) Revoke(_ context.Context, _, id uuid.UUID) (bool, error) {
	if f.revoked[id] {
		return false, nil
	}
	f.revoked[id] = true
	return true, nil
}

func (f *fakeRepo) Seen(context.Context, uuid.UUID, bool) error { f.seen++; return nil }

func (f *fakeRepo) StartSession(_ context.Context, id, staffID uuid.UUID) error {
	d := f.byID(id)
	d.StaffID, d.PinFails, d.PinLockedUntil = &staffID, 0, nil
	return nil
}

func (f *fakeRepo) PinFailed(_ context.Context, id uuid.UUID, until *time.Time) (int, error) {
	d := f.byID(id)
	d.PinFails++
	if until != nil {
		d.PinLockedUntil = until
	}
	return d.PinFails, nil
}

func (f *fakeRepo) EndSession(_ context.Context, id uuid.UUID) (bool, error) {
	d := f.byID(id)
	had := d.StaffID != nil
	d.StaffID = nil
	return had, nil
}

func (f *fakeRepo) Sessions(context.Context, uuid.UUID) ([]repositories.CallcenterDevice, error) {
	out := []repositories.CallcenterDevice{}
	for _, d := range f.dev {
		if d.StaffID != nil && !f.revoked[d.ID] {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (f *fakeRepo) Report(context.Context, uuid.UUID, string, string, string) ([]repositories.CallcenterReportRow, error) {
	return nil, nil
}

type fakeStaff struct {
	people   []staff.Person
	settings staff.Settings
	changed  func(context.Context, uuid.UUID, uuid.UUID)
}

func (f *fakeStaff) People(context.Context, uuid.UUID) ([]staff.Person, error) { return f.people, nil }
func (f *fakeStaff) Settings(context.Context, uuid.UUID) (staff.Settings, int64, error) {
	return f.settings, 1, nil
}
func (f *fakeStaff) OnChanged(fn func(context.Context, uuid.UUID, uuid.UUID)) { f.changed = fn }

type fakePi struct {
	streams int
	away    time.Time
	calls   []RelayRequest
	answer  func(RelayRequest) (any, error)
	cmds    []string
}

func (f *fakePi) Presence(sseBroker.Scope) (int, time.Time)  { return f.streams, f.away }
func (f *fakePi) RegisterInbound(string, rpc.InboundHandler) {}
func (f *fakePi) PublishCommand(_ context.Context, _ sseBroker.Scope, data []byte) int {
	var env rpc.Envelope
	_ = json.Unmarshal(data, &env)
	f.cmds = append(f.cmds, env.Type)
	return f.streams
}
func (f *fakePi) Request(_ context.Context, _ sseBroker.Scope, typ string, payload any, _ time.Duration) (rpc.Envelope, error) {
	if f.streams == 0 {
		return rpc.Envelope{}, rpc.ErrAgentOffline // the requester's own answer (it counts it)
	}
	req := payload.(RelayRequest)
	f.calls = append(f.calls, req)
	out, err := f.answer(req)
	if err != nil {
		return rpc.Envelope{}, err
	}
	raw, _ := json.Marshal(out)
	return rpc.Envelope{Type: typ + ".reply", Payload: raw}, nil
}

type fakeStatus struct{}

func (fakeStatus) Status(context.Context, uuid.UUID, uuid.UUID) (*storestatusApp.Full, error) {
	f := &storestatusApp.Full{}
	f.OpenNow = true
	return f, nil
}

func person(role, pin string, active bool) staff.Person {
	h, _ := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.MinCost)
	return staff.Person{ID: uuid.New(), FirstName: "Cara", LastName: "Smith", Role: role, Active: active, PinHash: string(h)}
}

type world struct {
	svc   *Service
	repo  *fakeRepo
	staff *fakeStaff
	pi    *fakePi
	now   time.Time
	tok   string
	agent staff.Person
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{repo: &fakeRepo{dev: map[string]*repositories.CallcenterDevice{}, revoked: map[uuid.UUID]bool{}},
		pi: &fakePi{streams: 1}, now: time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)}
	w.agent = person(staff.RoleCallcenter, "4321", true)
	w.staff = &fakeStaff{settings: staff.DefaultSettings(),
		people: []staff.Person{w.agent, person(staff.RoleCashier, "1111", true), person(staff.RoleManager, "2222", true)}}
	w.svc = NewService(w.repo, w.staff, fakeStatus{}, w.pi, w.pi, w.pi, nil, nil, zerolog.Nop())
	w.svc.now = func() time.Time { return w.now }
	w.svc.storePins.SetClock(w.svc.now)
	tok, _, err := w.svc.NewDevice(context.Background(), uuid.New(), uuid.New(), "  Desk   1 ")
	if err != nil {
		t.Fatal(err)
	}
	w.tok = tok
	return w
}

func (w *world) dev(t *testing.T) Device {
	t.Helper()
	d, err := w.svc.Authenticate(context.Background(), w.tok)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return d
}

func TestSessionIdleExpiry(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if d := w.dev(t); d.Name != "Desk 1" || d.StaffID != nil {
		t.Fatalf("new device: %+v", d)
	}
	if _, err := w.svc.Authenticate(ctx, "qccd_nope"); !errors.Is(err, ErrDeviceRequired) {
		t.Fatalf("a bad token: %v", err)
	}
	p, exp, err := w.svc.SignIn(ctx, w.dev(t), "4321")
	if err != nil || p.ID != w.agent.ID || !exp.Equal(w.now.Add(15*time.Minute)) {
		t.Fatalf("sign in: %v %v %v", p.ID, exp, err)
	}
	// 14 minutes idle: still in. A call keeps it alive (the row stamped at most every 30 s).
	w.now = w.now.Add(14 * time.Minute)
	d := w.dev(t)
	if d.StaffID == nil {
		t.Fatal("session ended early")
	}
	w.svc.Active(ctx, d)
	w.svc.Active(ctx, d)
	if w.repo.seen != 1 {
		t.Fatalf("row stamped %d times, want 1", w.repo.seen)
	}
	w.now = w.now.Add(14 * time.Minute)
	if w.dev(t).StaffID == nil {
		t.Fatal("the keep-alive didn't count")
	}
	// The store's setting is the limit.
	w.staff.settings.CallcenterIdleLockMinutes = 5
	w.now = w.now.Add(6 * time.Minute)
	if w.dev(t).StaffID != nil {
		t.Fatal("idle past the store's limit: the session should be gone")
	}
	if w.repo.byID(d.ID).StaffID != nil {
		t.Fatal("the row still holds the session")
	}
}

func TestSignInRolesAndGuessing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "12"); !errors.Is(err, ErrBadPin) {
		t.Fatalf("2 digits: %v", err)
	}
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "9999"); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("wrong: %v", err)
	}
	for _, pin := range []string{"1111", "2222"} { // a cashier's, a manager's
		if _, _, err := w.svc.SignIn(ctx, w.dev(t), pin); !errors.Is(err, ErrWrongRole) {
			t.Fatalf("%s: %v", pin, err)
		}
	}
	if n := w.repo.byID(w.dev(t).ID).PinFails; n != 3 {
		t.Fatalf("wrong-role PINs count as wrong ones: %d", n)
	}
	for i := 0; i < 2; i++ {
		_, _, _ = w.svc.SignIn(ctx, w.dev(t), "0000")
	}
	var th Throttled
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "4321"); !errors.As(err, &th) || th.RetryAfter != 30*time.Second {
		t.Fatalf("after 5 wrong the pad waits 30 s, even for the right PIN: %v", err)
	}
	// Keep guessing after each wait: at 20 in a row the device is revoked.
	var last error
	for i := 0; i < 15; i++ {
		w.now = w.now.Add(6 * time.Minute)
		_, _, last = w.svc.SignIn(ctx, w.dev(t), "0000")
	}
	if !errors.Is(last, ErrDeviceRevoked) {
		t.Fatalf("the 20th wrong PIN: %v", last)
	}
	if _, err := w.svc.Authenticate(ctx, w.tok); !errors.Is(err, ErrDeviceRequired) {
		t.Fatalf("a revoked device's token: %v", err)
	}
}

func TestStorePinCeiling(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	d := w.dev(t)
	// 30 wrong PINs across the store's browsers in 15 min: every pad waits.
	for i := 0; i < storePinMax; i++ {
		w.svc.storePins.Add(d.LocationID.String())
	}
	var th Throttled
	if _, _, err := w.svc.SignIn(ctx, d, "4321"); !errors.As(err, &th) {
		t.Fatalf("store ceiling: %v", err)
	}
	w.now = w.now.Add(16 * time.Minute)
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "4321"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestStaffChangeEndsSession(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "4321"); err != nil {
		t.Fatal(err)
	}
	d := w.dev(t)
	sub, cancel := w.svc.Subscribe(ctx, d)
	defer cancel()
	if len(w.pi.cmds) != 1 || w.pi.cmds[0] != MsgEventsWant {
		t.Fatalf("subscribing asks the Pi for its events: %v", w.pi.cmds)
	}
	w.staff.people[0].Active = false
	w.staff.changed(ctx, d.TenantID, d.LocationID)
	select {
	case ev := <-sub.C:
		if ev.Name != "session.ended" || string(ev.Data) != `{"reason":"staff_changed"}` {
			t.Fatalf("event: %+v", ev)
		}
	default:
		t.Fatal("no session.ended")
	}
	if _, ok, _ := w.svc.Agent(ctx, w.dev(t)); ok {
		t.Fatal("a switched-off agent still signed in")
	}
}

func TestRelay(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	d := w.dev(t)
	uploads := 0
	w.pi.answer = func(r RelayRequest) (any, error) {
		switch r.Path {
		case "menu":
			if r.Headers["If-None-Match"] == `"m1"` {
				return RelayReply{Status: 304, Headers: map[string]string{"ETag": `"m1"`}}, nil
			}
			uploads++
			return RelayReply{Status: 200, Headers: map[string]string{"ETag": `"m1"`}, Body: json.RawMessage(`{"menu":1}`)}, nil
		case "busy":
			return RelayReply{Status: 503, Body: json.RawMessage(`{"code":"busy"}`)}, nil
		case "printers":
			return RelayReply{Status: 503, Body: json.RawMessage(`{"code":"printing_unavailable"}`)}, nil
		case "old":
			return map[string]any{"ok": false}, nil
		case "slow":
			return nil, rpc.ErrTimeout
		}
		return RelayReply{Status: 201, Body: json.RawMessage(`{"ok":true}`)}, nil
	}
	rep, err := w.svc.Relay(ctx, d, w.agent.ID, "POST", "orders", "a=1", map[string]string{}, []byte(`{"client_key":"k"}`))
	if err != nil || rep.Status != 201 {
		t.Fatalf("relay: %+v %v", rep, err)
	}
	got := w.pi.calls[0]
	if got.Device != d.ID.String() || got.DeviceName != "Desk 1" || got.StaffID != w.agent.ID.String() || got.Method != "POST" ||
		got.Path != "orders" || got.Query != "a=1" || string(got.Body) != `{"client_key":"k"}` {
		t.Fatalf("payload: %+v", got)
	}
	if rep, _ := w.svc.Relay(ctx, d, w.agent.ID, "GET", "pickups", "", map[string]string{}, nil); rep.Status != 201 || string(w.pi.calls[1].Body) != "null" {
		t.Fatalf("no body rides as null: %s", w.pi.calls[1].Body)
	}
	for _, path := range []string{"busy", "old", "slow"} {
		if _, err := w.svc.Relay(ctx, d, w.agent.ID, "GET", path, "", map[string]string{}, nil); !errors.Is(err, ErrOffline) {
			t.Errorf("%s: %v, want offline", path, err)
		}
	}
	if rep, err := w.svc.Relay(ctx, d, w.agent.ID, "GET", "printers", "", map[string]string{}, nil); err != nil || rep.Status != 503 {
		t.Errorf("the Pi's own 503 passes through: %+v %v", rep, err)
	}

	// The menu: one upload per version; the copy serves the Pi's 304.
	for i := 0; i < 3; i++ {
		rep, err := w.svc.Relay(ctx, d, w.agent.ID, "GET", "menu", "", map[string]string{}, nil)
		if err != nil || rep.Status != 200 || string(rep.Body) != `{"menu":1}` || rep.Headers["ETag"] != `"m1"` {
			t.Fatalf("menu %d: %+v %v", i, rep, err)
		}
	}
	if uploads != 1 {
		t.Fatalf("the Pi uploaded the menu %d times", uploads)
	}
	rep, _ = w.svc.Relay(ctx, d, w.agent.ID, "GET", "menu", "", map[string]string{"If-None-Match": `"m1"`}, nil)
	if rep.Status != 304 || len(rep.Body) != 0 {
		t.Fatalf("a browser that has it: %+v", rep)
	}

	// No stream: offline at once when the Pi has been gone a while; a Pi
	// that left seconds ago still counts as online for Me.
	w.pi.streams, w.pi.away = 0, w.now.Add(-time.Minute)
	n := len(w.pi.calls)
	if _, err := w.svc.Relay(ctx, d, w.agent.ID, "GET", "pickups", "", map[string]string{}, nil); !errors.Is(err, ErrOffline) || len(w.pi.calls) != n {
		t.Fatalf("offline: %v", err)
	}
	if w.svc.PiOnline(d.Scope()) {
		t.Fatal("gone a minute: offline")
	}
	w.pi.away = w.now.Add(-2 * time.Second)
	if !w.svc.PiOnline(d.Scope()) {
		t.Fatal("a 2 s reconnect isn't offline")
	}
}

func TestEventsFanOut(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	d := w.dev(t)
	sub, cancel := w.svc.Subscribe(ctx, d)
	other, cancel2 := w.svc.Subscribe(ctx, Device{ID: uuid.New(), TenantID: uuid.New(), LocationID: uuid.New()})
	defer cancel2()
	payload := `{"events":[{"event":"terminal.order.placed","data":{"order":{"id":"o1"}}},{"event":"bad\nname","data":{}},{"event":"order.updated"}]}`
	if _, err := w.svc.handleEvents(ctx, d.Scope(), json.RawMessage(payload)); err != nil {
		t.Fatal(err)
	}
	if ev := <-sub.C; ev.Name != "terminal.order.placed" || string(ev.Data) != `{"order":{"id":"o1"}}` {
		t.Fatalf("first: %+v", ev)
	}
	if ev := <-sub.C; ev.Name != "order.updated" || string(ev.Data) != `{}` {
		t.Fatalf("second: %+v", ev)
	}
	if len(other.C) != 0 {
		t.Fatal("another store's browser got the event")
	}
	if _, err := w.svc.handleEvents(ctx, d.Scope(), json.RawMessage(`nope`)); !errors.Is(err, rpc.ErrBadPayload) {
		t.Fatalf("bad payload: %v", err)
	}
	// Want is re-sent at most once a minute unless forced.
	n := len(w.pi.cmds)
	w.svc.Want(ctx, d.Scope(), false)
	if len(w.pi.cmds) != n {
		t.Fatal("want re-sent too soon")
	}
	w.now = w.now.Add(61 * time.Second)
	w.svc.Want(ctx, d.Scope(), false)
	if len(w.pi.cmds) != n+1 {
		t.Fatal("want not re-sent after a minute")
	}
	cancel()
	if _, err := w.svc.handleEvents(ctx, d.Scope(), json.RawMessage(payload)); err != nil {
		t.Fatal(err)
	}
}
