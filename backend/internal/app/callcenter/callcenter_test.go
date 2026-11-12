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
		// Fail closed: a body the guards can't read never reaches the Pi.
		{"POST", "journal", `[1,2]`, "", "bad_request"},
		{"POST", "orders/x/payments", `{"tender":"comp","client_key":7}`, "", "bad_request"},
		{"POST", "orders/x/payments", `{"tender":5}`, "", "bad_request"},
		{"POST", "orders", `{"client_key":"k","type":"pickup","payments":"x"}`, "", "bad_request"},
		{"POST", "orders", `{"client_key":"k","type":"pickup","payments":[{"tender":"manual_card","refund":"yes"}]}`, "", "bad_request"},
		{"POST", "orders", `{"client_key":"k","type":"pickup","cart":{"lines":"x"}}`, "", "bad_request"},
		{"POST", "orders", `{"client_key":"k","type":"pickup","cart":{"lines":[{"custom":"yes"}]}}`, "", "bad_request"},
		{"POST", "orders", `{"client_key":"k","type":7}`, "", "bad_request"},
		{"PUT", "orders/x/cart", `{"cart":[]}`, "", "bad_request"},
		{"POST", "orders/x/payments", `{"tender":"manual_card","refund":1}`, "", "bad_request"},
		{"POST", "orders/x/void", `"reason"`, "", "bad_request"},
		// keys match as Go's decoder (and the Pi's) reads them: any case, the last one wins
		{"POST", "orders/x/payments", `{"Tender":"comp"}`, "comp", CodeNotAllowed},
		{"POST", "orders/x/payments", `{"tender":"manual_card","tender":"cash"}`, "tender", CodeNotAllowed},
		{"POST", "orders/x/void", `{"reason":"x","manager_pin":["1"]}`, "manager", CodeNotAllowed},
		{"POST", "orders/x/void", `{"reason":"x","Manager_Pin":"1"}`, "manager", CodeNotAllowed},
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
	dev      map[string]*repositories.CallcenterDevice // by token hash
	revoked  map[uuid.UUID]bool
	failedAt map[uuid.UUID]time.Time
	logins   map[uuid.UUID]repositories.CallcenterLogin
	now      func() time.Time
	seen     int
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

func (f *fakeRepo) PinAttempt(_ context.Context, id uuid.UUID, lockAfter int, first, maxWait, window time.Duration) (int, bool, error) {
	d := f.byID(id)
	now := f.now()
	if d == nil || f.revoked[id] || (d.PinLockedUntil != nil && d.PinLockedUntil.After(now)) {
		return 0, false, nil
	}
	if f.failedAt[id].IsZero() || now.Sub(f.failedAt[id]) > window {
		d.PinFails = 0
	}
	d.PinFails++
	f.failedAt[id] = now
	d.PinLockedUntil = nil
	if d.PinFails >= lockAfter {
		w := first
		for i := lockAfter; i < d.PinFails && w < maxWait; i++ {
			w *= 2
		}
		if w > maxWait {
			w = maxWait
		}
		u := now.Add(w)
		d.PinLockedUntil = &u
	}
	return d.PinFails, true, nil
}

func (f *fakeRepo) PinState(_ context.Context, id uuid.UUID) (bool, *time.Time, error) {
	d := f.byID(id)
	if d == nil || f.revoked[id] {
		return false, nil, nil
	}
	return true, d.PinLockedUntil, nil
}

func (f *fakeRepo) PinSucceeded(_ context.Context, id, staffID uuid.UUID, unlock bool) (bool, error) {
	d := f.byID(id)
	if d == nil || f.revoked[id] {
		return false, nil
	}
	d.StaffID = &staffID
	if d.PinFails > 0 {
		d.PinFails--
	}
	if unlock {
		d.PinLockedUntil = nil
	}
	return true, nil
}

func (f *fakeRepo) LoginByUsername(_ context.Context, u string) (repositories.CallcenterLogin, error) {
	for _, l := range f.logins {
		if l.Username == u {
			return l, nil
		}
	}
	return repositories.CallcenterLogin{}, pgx.ErrNoRows
}

func (f *fakeRepo) LoginOf(_ context.Context, lid uuid.UUID) (repositories.CallcenterLogin, error) {
	l, ok := f.logins[lid]
	if !ok {
		return l, pgx.ErrNoRows
	}
	return l, nil
}

func (f *fakeRepo) SetLogin(_ context.Context, tid, lid uuid.UUID, u, hash string, revoke bool) (repositories.CallcenterLogin, []uuid.UUID, error) {
	for id, l := range f.logins {
		if id != lid && l.Username == u {
			return repositories.CallcenterLogin{}, nil, repositories.ErrCallcenterUsernameTaken
		}
	}
	l := repositories.CallcenterLogin{TenantID: tid, LocationID: lid, Username: u, PasswordHash: hash, UpdatedAt: f.now()}
	f.logins[lid] = l
	ids := []uuid.UUID{}
	if revoke {
		ids = f.revokeAll(lid)
	}
	return l, ids, nil
}

func (f *fakeRepo) DeleteLogin(_ context.Context, lid uuid.UUID) ([]uuid.UUID, error) {
	delete(f.logins, lid)
	return f.revokeAll(lid), nil
}

func (f *fakeRepo) revokeAll(lid uuid.UUID) []uuid.UUID {
	ids := []uuid.UUID{}
	for _, d := range f.dev {
		if d.LocationID == lid && !f.revoked[d.ID] {
			f.revoked[d.ID] = true
			ids = append(ids, d.ID)
		}
	}
	return ids
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
	w := &world{repo: &fakeRepo{dev: map[string]*repositories.CallcenterDevice{}, revoked: map[uuid.UUID]bool{},
		failedAt: map[uuid.UUID]time.Time{}, logins: map[uuid.UUID]repositories.CallcenterLogin{}},
		pi: &fakePi{streams: 1}, now: time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)}
	w.repo.now = func() time.Time { return w.now }
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
	fails := func() int { return w.repo.byID(w.dev(t).ID).PinFails }
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "12"); !errors.Is(err, ErrBadPin) || fails() != 0 {
		t.Fatalf("2 digits is not a guess: %v, %d counted", err, fails())
	}
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "9999"); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("wrong: %v", err)
	}
	for _, pin := range []string{"1111", "2222"} { // a cashier's, a manager's
		if _, _, err := w.svc.SignIn(ctx, w.dev(t), pin); !errors.Is(err, ErrWrongRole) {
			t.Fatalf("%s: %v", pin, err)
		}
	}
	if fails() != 3 {
		t.Fatalf("wrong-role PINs count as wrong ones: %d", fails())
	}
	_, _, _ = w.svc.SignIn(ctx, w.dev(t), "0000")
	// Four wrong, then her own PIN: she is in, but the four stay (no fresh
	// set of guesses bought with a right PIN).
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "4321"); err != nil {
		t.Fatalf("4 wrong then the right PIN: %v", err)
	}
	if fails() != 4 || w.repo.byID(w.dev(t).ID).PinLockedUntil != nil {
		t.Fatalf("after the right PIN: %d counted, lock %v; want 4, none", fails(), w.repo.byID(w.dev(t).ID).PinLockedUntil)
	}
	// The next wrong one is the 5th: the pad locks 30 s, for the right PIN too.
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "0000"); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("5th: %v", err)
	}
	var th Throttled
	if _, _, err := w.svc.SignIn(ctx, w.dev(t), "4321"); !errors.As(err, &th) || th.RetryAfter != 30*time.Second {
		t.Fatalf("after 5 wrong the pad waits 30 s, even for the right PIN: %v", err)
	}
	if fails() != 5 {
		t.Fatalf("an attempt at a locked pad isn't counted: %d", fails())
	}
	// Keep guessing after each lock: the 20th counted wrong PIN revokes the device.
	var last error
	for i := 0; i < 15; i++ {
		w.now = w.now.Add(5*time.Minute + time.Second)
		_, _, last = w.svc.SignIn(ctx, w.dev(t), "0000")
	}
	if !errors.Is(last, ErrDeviceRevoked) {
		t.Fatalf("the 20th wrong PIN: %v", last)
	}
	if _, err := w.svc.Authenticate(ctx, w.tok); !errors.Is(err, ErrDeviceRequired) {
		t.Fatalf("a revoked device's token: %v", err)
	}
}

func TestPinWindowDecaysAndRevokedDeviceGetsNoSession(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	d := w.dev(t)
	for i := 0; i < 4; i++ {
		_, _, _ = w.svc.SignIn(ctx, d, "0000")
	}
	// 15 minutes with no attempt: the window starts again.
	w.now = w.now.Add(16 * time.Minute)
	if _, _, err := w.svc.SignIn(ctx, d, "0000"); !errors.Is(err, ErrWrongPin) || w.repo.byID(d.ID).PinFails != 1 {
		t.Fatalf("after the window: %v, %d counted", err, w.repo.byID(d.ID).PinFails)
	}
	// One device's wrong PINs never lock another device of the store.
	tok2, d2, err := w.svc.NewDevice(ctx, d.TenantID, d.LocationID, "Desk 2")
	if err != nil || tok2 == "" {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		w.now = w.now.Add(6 * time.Minute)
		_, _, _ = w.svc.SignIn(ctx, d, "0000")
	}
	if _, _, err := w.svc.SignIn(ctx, d2, "4321"); err != nil {
		t.Fatalf("another device of the store: %v", err)
	}
	// The first device was revoked for guessing: the right PIN gets no session there.
	if _, _, err := w.svc.SignIn(ctx, d, "4321"); !errors.Is(err, ErrDeviceRequired) {
		t.Fatalf("a revoked device with the right PIN: %v", err)
	}
	if w.repo.byID(d.ID).StaffID != nil {
		t.Fatal("a revoked device got a session")
	}
}

func TestOwnLogin(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	tid, lid := uuid.New(), uuid.New()
	if _, _, err := w.svc.CheckLogin(ctx, "lombardis-cc", "a long password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("no login set: %v", err)
	}
	for _, bad := range []string{"", "ab", "has space", "-lead", "UPPER!", "x" + string(make([]byte, 50))} {
		if _, _, err := w.svc.SetLogin(ctx, tid, lid, bad, "a long password", false); !errors.Is(err, ErrBadUsername) {
			t.Errorf("username %q: %v", bad, err)
		}
	}
	if _, _, err := w.svc.SetLogin(ctx, tid, lid, "lombardis-cc", "short", false); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("short password: %v", err)
	}
	v, n, err := w.svc.SetLogin(ctx, tid, lid, " Lombardis-CC ", "a long password", false)
	if err != nil || v.Username != "lombardis-cc" || !v.Set || n != 0 {
		t.Fatalf("set: %+v %d %v", v, n, err)
	}
	gt, gl, err := w.svc.CheckLogin(ctx, "LOMBARDIS-cc", "a long password")
	if err != nil || gt != tid || gl != lid {
		t.Fatalf("check: %v", err)
	}
	if _, _, err := w.svc.CheckLogin(ctx, "lombardis-cc", "another password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, _, err := w.svc.SetLogin(ctx, uuid.New(), uuid.New(), "lombardis-cc", "a long password", false); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("another store, the same username: %v", err)
	}
	// A new password signs every browser out (unless kept).
	tok, d, _ := w.svc.NewDevice(ctx, tid, lid, "Desk")
	sub, cancel := w.svc.Subscribe(ctx, d)
	defer cancel()
	if _, n, _ := w.svc.SetLogin(ctx, tid, lid, "lombardis-cc", "a newer password", true); n != 0 {
		t.Fatalf("keep_devices: %d revoked", n)
	}
	if _, err := w.svc.Authenticate(ctx, tok); err != nil {
		t.Fatalf("kept device: %v", err)
	}
	if _, n, _ := w.svc.SetLogin(ctx, tid, lid, "lombardis-cc", "the newest password", false); n != 1 {
		t.Fatalf("changing the password revokes the devices: %d", n)
	}
	if _, err := w.svc.Authenticate(ctx, tok); !errors.Is(err, ErrDeviceRequired) {
		t.Fatalf("a device of the old password: %v", err)
	}
	if ev := <-sub.C; ev.Name != "session.ended" || !ev.Close {
		t.Fatalf("its stream ends: %+v", ev)
	}
	if _, _, err := w.svc.CheckLogin(ctx, "lombardis-cc", "a newer password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the old password: %v", err)
	}
	if _, err := w.svc.RemoveLogin(ctx, tid, lid); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.svc.LoginInfo(ctx, lid); v.Set {
		t.Fatal("removed login still set")
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
	if _, err := w.svc.Relay(ctx, d, w.agent.ID, "GET", "busy", "", map[string]string{}, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("busy: %v, want ErrBusy", err)
	}
	for _, path := range []string{"old", "slow"} {
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
