// Package callcenter is main's half of the call center's remote terminal
// (CALLCENTER-PLAN.md, contract/main.md §2.5zh): the store login → a device
// token per browser, the agent's PIN session (role callcenter only), the
// relay of the allowed terminal calls to the store's Pi over the duplex
// channel (RPC terminal.relay) and the Pi's terminal events back.
//
// No order logic lives here: a relayed call's body, answer and status are the
// Pi's.
package callcenter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	storestatusApp "quiccpos/main/internal/app/storestatus"
	"quiccpos/main/internal/domain/staff"
	"quiccpos/main/internal/domain/storestatus"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/repositories"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"
	"quiccpos/main/internal/shared/ratelimit"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

const (
	// TokenPrefix marks a call-center device token.
	TokenPrefix = "qccd_"
	// MaxDevices is the store's cap on live devices.
	MaxDevices = 50
	// MaxBody is the largest relayed body.
	MaxBody = 256 << 10

	// MsgRelay / MsgEvents / MsgEventsWant: the duplex types (agent contract §2.10ab).
	MsgRelay      = "terminal.relay"
	MsgEvents     = "terminal.events"
	MsgEventsWant = "terminal.events.want"

	relayTimeout     = 12 * time.Second
	relaySlowTimeout = 35 * time.Second
	wantSeconds      = 120
	wantEvery        = 60 * time.Second
	seenEvery        = 30 * time.Second

	CodeNotAllowed   = "not_allowed_for_callcenter"
	CodeStoreOffline = "store_offline"
	CodeStoreClosed  = "store_closed"
	CodeStoreBusy    = "store_busy"
)

var (
	ErrDeviceRequired = errors.New("device token required")
	ErrLoginRequired  = errors.New("agent session required")
	ErrBadPin         = errors.New("the PIN is 4 digits")
	ErrWrongPin       = errors.New("wrong PIN")
	ErrWrongRole      = errors.New("that PIN isn't a call-center agent's")
	ErrOffline        = errors.New("the store's Pi is offline")
	// ErrBusy: the Pi is there but its relay is full right now (a burst of
	// calls): the same call a second later goes through.
	ErrBusy    = errors.New("the store's Pi is busy")
	ErrTooMany = repositories.ErrCallcenterTooMany
	// ErrDeviceRevoked: too many wrong PINs in a row: the device token is gone.
	ErrDeviceRevoked = errors.New("too many wrong PINs: sign in with the store's login again")
)

// Throttled: too many wrong PINs on the device.
type Throttled struct{ RetryAfter time.Duration }

func (t Throttled) Error() string { return "too many wrong PINs" }

// Repo is the device store.
type Repo interface {
	Create(ctx context.Context, tenantID, locationID uuid.UUID, name, tokenHash string, max int) (repositories.CallcenterDevice, error)
	ByTokenHash(ctx context.Context, tokenHash string) (repositories.CallcenterDevice, error)
	List(ctx context.Context, locationID uuid.UUID) ([]repositories.CallcenterDevice, error)
	Revoke(ctx context.Context, locationID, id uuid.UUID) (bool, error)
	Seen(ctx context.Context, id uuid.UUID, session bool) error
	PinAttempt(ctx context.Context, id uuid.UUID, lockAfter int, first, maxWait, window time.Duration) (n int, ok bool, err error)
	PinState(ctx context.Context, id uuid.UUID) (live bool, lockedUntil *time.Time, err error)
	PinSucceeded(ctx context.Context, id, staffID uuid.UUID, unlock bool) (bool, error)
	LoginByUsername(ctx context.Context, username string) (repositories.CallcenterLogin, error)
	LoginOf(ctx context.Context, locationID uuid.UUID) (repositories.CallcenterLogin, error)
	SetLogin(ctx context.Context, tenantID, locationID uuid.UUID, username, passwordHash string, revoke bool) (repositories.CallcenterLogin, []uuid.UUID, error)
	DeleteLogin(ctx context.Context, locationID uuid.UUID) ([]uuid.UUID, error)
	EndSession(ctx context.Context, id uuid.UUID) (bool, error)
	Sessions(ctx context.Context, locationID uuid.UUID) ([]repositories.CallcenterDevice, error)
	Report(ctx context.Context, locationID uuid.UUID, zone, from, to string) ([]repositories.CallcenterReportRow, error)
}

// Staff reads the store's people and terminal settings.
type Staff interface {
	People(ctx context.Context, locationID uuid.UUID) ([]staff.Person, error)
	Settings(ctx context.Context, locationID uuid.UUID) (staff.Settings, int64, error)
	OnChanged(fn func(ctx context.Context, tenantID, locationID uuid.UUID))
}

// Status reads the store's open / closed state.
type Status interface {
	Status(ctx context.Context, tenantID, locationID uuid.UUID) (*storestatusApp.Full, error)
}

// Presence is the Pi's live stream.
type Presence interface {
	Presence(scope sseBroker.Scope) (streams int, awaySince time.Time)
}

// Requester is the duplex channel to the Pi.
type Requester interface {
	Request(ctx context.Context, scope sseBroker.Scope, msgType string, payload any, timeout time.Duration) (rpc.Envelope, error)
	RegisterInbound(msgType string, h rpc.InboundHandler)
}

// Commander pushes a fire-and-forget command to the Pi.
type Commander interface {
	PublishCommand(ctx context.Context, scope sseBroker.Scope, data []byte) int
}

// WebCard is Square's public card-field config for a store.
type WebCard interface {
	WebCard(ctx context.Context, locationID uuid.UUID) (env, appID, squareLocationID string, ok bool)
}

// Locations reads a store.
type Locations func(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)

type Service struct {
	repo     Repo
	staff    Staff
	status   Status
	presence Presence
	rpc      Requester
	cmd      Commander
	card     WebCard
	locs     Locations
	logger   zerolog.Logger
	now      func() time.Time

	mu       sync.Mutex
	seen     map[uuid.UUID]time.Time // device → its session's last activity (newer than the row)
	seenSave map[uuid.UUID]time.Time // device → when the row was last stamped
	// storePins: wrong PINs per store across its devices (in memory: a
	// ceiling on top of the per-device counters on the rows).
	storePins *ratelimit.Window
	pinMu     map[uuid.UUID]*sync.Mutex // one PIN check per device at a time
	subs      map[sseBroker.Scope]map[*Subscriber]struct{}
	wanted    map[sseBroker.Scope]time.Time // when terminal.events.want was last sent
	menus     map[uuid.UUID]menuCopy        // location → the Pi's last menu answer
}

func NewService(repo Repo, st Staff, status Status, presence Presence, req Requester, cmd Commander, card WebCard, locs Locations, lg zerolog.Logger) *Service {
	s := &Service{repo: repo, staff: st, status: status, presence: presence, rpc: req, cmd: cmd, card: card, locs: locs,
		logger: lg.With().Str("module", "callcenter").Logger(), now: time.Now,
		seen: map[uuid.UUID]time.Time{}, seenSave: map[uuid.UUID]time.Time{}, storePins: ratelimit.New(storePinMax, storePinWindow), pinMu: map[uuid.UUID]*sync.Mutex{},
		menus: map[uuid.UUID]menuCopy{},
		subs:  map[sseBroker.Scope]map[*Subscriber]struct{}{}, wanted: map[sseBroker.Scope]time.Time{}}
	if req != nil {
		req.RegisterInbound(MsgEvents, s.handleEvents)
	}
	if st != nil {
		st.OnChanged(s.staffChanged)
	}
	return s
}

// --- devices ----------------------------------------------------------------

// Device is a signed-in browser as the routes see it.
type Device struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	LocationID uuid.UUID
	Name       string
	CreatedAt  time.Time
	// StaffID: the agent signed in (nil = locked / never signed in / idle too long).
	StaffID        *uuid.UUID
	SessionExpires time.Time
}

func (d Device) Scope() sseBroker.Scope {
	return sseBroker.Scope{TenantID: d.TenantID, LocationID: d.LocationID}
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func cleanName(n string) string {
	n = strings.Join(strings.Fields(n), " ")
	if n == "" {
		n = "Call center"
	}
	if r := []rune(n); len(r) > 60 {
		n = string(r[:60])
	}
	return n
}

// NewDevice mints a device token for a store (the store login was checked by
// the caller). The token is returned once; only its sha256 is stored.
func (s *Service) NewDevice(ctx context.Context, tenantID, locationID uuid.UUID, name string) (string, Device, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", Device{}, err
	}
	tok := TokenPrefix + hex.EncodeToString(buf)
	row, err := s.repo.Create(ctx, tenantID, locationID, cleanName(name), hashToken(tok), MaxDevices)
	if err != nil {
		return "", Device{}, err
	}
	return tok, Device{ID: row.ID, TenantID: row.TenantID, LocationID: row.LocationID, Name: row.Name, CreatedAt: row.CreatedAt}, nil
}

func (s *Service) idle(ctx context.Context, locationID uuid.UUID) time.Duration {
	st, _, err := s.staff.Settings(ctx, locationID)
	m := st.CallcenterIdleLockMinutes
	if err != nil || m <= 0 {
		m = staff.DefaultCallcenterIdleLock
	}
	return time.Duration(m) * time.Minute
}

// Authenticate resolves a device token (ErrDeviceRequired: none, unknown or
// revoked). The agent session on it is dropped once idle past the store's
// limit.
func (s *Service) Authenticate(ctx context.Context, token string) (Device, error) {
	if !strings.HasPrefix(token, TokenPrefix) || len(token) != len(TokenPrefix)+64 {
		return Device{}, ErrDeviceRequired
	}
	row, err := s.repo.ByTokenHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrDeviceRequired
	}
	if err != nil {
		return Device{}, err
	}
	d := Device{ID: row.ID, TenantID: row.TenantID, LocationID: row.LocationID, Name: row.Name, CreatedAt: row.CreatedAt}
	if row.StaffID != nil {
		last := time.Time{}
		if row.SessionSeenAt != nil {
			last = *row.SessionSeenAt
		}
		s.mu.Lock()
		if m, ok := s.seen[row.ID]; ok && m.After(last) {
			last = m
		}
		s.mu.Unlock()
		idle := s.idle(ctx, row.LocationID)
		if s.now().Sub(last) > idle {
			s.end(ctx, d, "idle")
		} else {
			d.StaffID, d.SessionExpires = row.StaffID, last.Add(idle)
		}
	}
	return d, nil
}

// Active marks the device's session as active now (a relayed call, the
// keep-alive): the row is stamped at most every 30 s.
func (s *Service) Active(ctx context.Context, d Device) time.Time {
	now := s.now()
	s.mu.Lock()
	s.seen[d.ID] = now
	save := now.Sub(s.seenSave[d.ID]) >= seenEvery
	if save {
		s.seenSave[d.ID] = now
	}
	s.mu.Unlock()
	if save {
		if err := s.repo.Seen(context.WithoutCancel(ctx), d.ID, d.StaffID != nil); err != nil {
			s.logger.Warn().Err(err).Msg("callcenter: device not stamped")
		}
	}
	return now.Add(s.idle(ctx, d.LocationID))
}

// Devices lists the store's devices for Store › Staff.
type DeviceView struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	StaffID    *uuid.UUID `json:"staff_id,omitempty"`
	StaffName  string     `json:"staff_name,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func (s *Service) Devices(ctx context.Context, locationID uuid.UUID) ([]DeviceView, error) {
	rows, err := s.repo.List(ctx, locationID)
	if err != nil {
		return nil, err
	}
	people, err := s.staff.People(ctx, locationID)
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID]string{}
	for _, p := range people {
		names[p.ID] = DisplayName(p)
	}
	out := make([]DeviceView, 0, len(rows))
	for _, r := range rows {
		v := DeviceView{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, RevokedAt: r.RevokedAt}
		if r.StaffID != nil && r.RevokedAt == nil {
			v.StaffID, v.StaffName = r.StaffID, names[*r.StaffID]
		}
		out = append(out, v)
	}
	return out, nil
}

// Revoke ends a device: its token stops working, its stream closes.
func (s *Service) Revoke(ctx context.Context, tenantID, locationID, id uuid.UUID) (bool, error) {
	ok, err := s.repo.Revoke(ctx, locationID, id)
	if err != nil || !ok {
		return ok, err
	}
	s.forget(id)
	s.publishDevice(sseBroker.Scope{TenantID: tenantID, LocationID: locationID}, id, Event{Name: "session.ended", Data: json.RawMessage(`{"reason":"device_revoked"}`), Close: true})
	return true, nil
}

func (s *Service) forget(id uuid.UUID) {
	s.mu.Lock()
	delete(s.seen, id)
	delete(s.seenSave, id)
	delete(s.pinMu, id)
	s.mu.Unlock()
}

// --- the call center's own login -------------------------------------------------------

var (
	ErrInvalidCredentials = errors.New("wrong call-center login")
	ErrBadUsername        = errors.New("the username is 3-40 of a-z, 0-9, dot, dash, underscore, starting with a letter or digit")
	ErrBadPassword        = errors.New("the password is 10-128 characters")
	ErrUsernameTaken      = repositories.ErrCallcenterUsernameTaken
	ErrNoLogin            = errors.New("no call-center login set")
)

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,39}$`)

// MinPassword is the shortest call-center password.
const MinPassword = 10

// NormUsername lowercases and trims a call-center username; ok = it is valid.
func NormUsername(u string) (string, bool) {
	u = strings.ToLower(strings.TrimSpace(u))
	return u, usernameRE.MatchString(u)
}

// dummyHash keeps a login for an unknown username as slow as a known one.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("no such call-center login"), bcrypt.DefaultCost)

// CheckLogin checks the call center's own shared login (never the store
// manager's: that one opens Store pages and links a Pi) and returns its store.
func (s *Service) CheckLogin(ctx context.Context, username, password string) (tenantID, locationID uuid.UUID, err error) {
	u, ok := NormUsername(username)
	if !ok || password == "" || len(password) > 128 {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return uuid.Nil, uuid.Nil, ErrInvalidCredentials
	}
	l, err := s.repo.LoginByUsername(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return uuid.Nil, uuid.Nil, ErrInvalidCredentials
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(l.PasswordHash), []byte(password)) != nil {
		return uuid.Nil, uuid.Nil, ErrInvalidCredentials
	}
	return l.TenantID, l.LocationID, nil
}

// LoginView is the store's call-center login as Store › Staff shows it (never
// the password).
type LoginView struct {
	Username  string     `json:"username"`
	Set       bool       `json:"set"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// LoginInfo is the store's call-center login (Set false: none yet).
func (s *Service) LoginInfo(ctx context.Context, locationID uuid.UUID) (LoginView, error) {
	l, err := s.repo.LoginOf(ctx, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginView{}, nil
	}
	if err != nil {
		return LoginView{}, err
	}
	return LoginView{Username: l.Username, Set: true, UpdatedAt: &l.UpdatedAt}, nil
}

// SetLogin sets the store's call-center login. Unless keepDevices, every
// browser signed in with the old one is signed out (their streams end). It
// returns how many were.
func (s *Service) SetLogin(ctx context.Context, tenantID, locationID uuid.UUID, username, password string, keepDevices bool) (LoginView, int, error) {
	u, ok := NormUsername(username)
	if !ok {
		return LoginView{}, 0, ErrBadUsername
	}
	if len(password) < MinPassword || len(password) > 128 {
		return LoginView{}, 0, ErrBadPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return LoginView{}, 0, err
	}
	l, ids, err := s.repo.SetLogin(ctx, tenantID, locationID, u, string(hash), !keepDevices)
	if err != nil {
		return LoginView{}, 0, err
	}
	s.revoked(sseBroker.Scope{TenantID: tenantID, LocationID: locationID}, ids)
	return LoginView{Username: l.Username, Set: true, UpdatedAt: &l.UpdatedAt}, len(ids), nil
}

// RemoveLogin removes the store's call-center login and signs every browser out.
func (s *Service) RemoveLogin(ctx context.Context, tenantID, locationID uuid.UUID) (int, error) {
	ids, err := s.repo.DeleteLogin(ctx, locationID)
	if err != nil {
		return 0, err
	}
	s.revoked(sseBroker.Scope{TenantID: tenantID, LocationID: locationID}, ids)
	return len(ids), nil
}

// revoked forgets devices the store just ended and closes their streams.
func (s *Service) revoked(scope sseBroker.Scope, ids []uuid.UUID) {
	for _, id := range ids {
		s.forget(id)
		s.publishDevice(scope, id, Event{Name: "session.ended", Data: json.RawMessage(`{"reason":"device_revoked"}`), Close: true})
	}
}

// --- the agent's session ------------------------------------------------------

// DisplayName is the Pi's display form of a person: "Cara S.".
func DisplayName(p staff.Person) string {
	n := strings.TrimSpace(p.FirstName)
	if l := []rune(strings.TrimSpace(p.LastName)); len(l) > 0 {
		n += " " + string(l[:1]) + "."
	}
	return n
}

// The PIN guard (security review M1). Every attempt is counted on the device
// row BEFORE the PIN is checked, in one UPDATE that also refuses a locked or
// revoked device, so parallel guesses can't outrun the lock; one check runs per
// device at a time in this process. From the 5th attempt in a window the pad
// locks at once (30 s, doubling to 5 min). A right PIN takes back only its own
// attempt: the wrong ones before it stay until 15 min pass without an attempt
// (so "four wrong, then my own PIN" doesn't buy four more guesses). The 20th
// counted attempt in a window that is wrong revokes the device. A store's
// devices don't lock each other: many wrong PINs across a store are logged
// (an alert reads the line), never a store-wide lock.
const (
	pinMaxFails    = 5
	pinFirst       = 30 * time.Second
	pinMaxWait     = 5 * time.Minute
	pinWindow      = 15 * time.Minute
	pinRevokeAfter = 20
	storePinMax    = 30
	storePinWindow = 15 * time.Minute
)

// pinWait is the lock set by the n-th counted attempt in a window (0 = none).
func pinWait(n int) time.Duration {
	if n < pinMaxFails {
		return 0
	}
	w := pinFirst
	for i := pinMaxFails; i < n && w < pinMaxWait; i++ {
		w *= 2
	}
	if w > pinMaxWait {
		w = pinMaxWait
	}
	return w
}

// pinLock is the device's "one PIN check at a time" lock.
func (s *Service) pinLock(id uuid.UUID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.pinMu[id]
	if m == nil {
		m = &sync.Mutex{}
		s.pinMu[id] = m
	}
	return m
}

// pinFailed handles a counted attempt that was wrong; revoked = the device
// was guessing (the 20th in a window): its token is gone.
func (s *Service) pinFailed(ctx context.Context, d Device, n int) (revoked bool) {
	s.storePins.Add(d.LocationID.String())
	if !s.storePins.Allow(d.LocationID.String()) {
		s.logger.Warn().Str("location_id", d.LocationID.String()).Msg("callcenter: many wrong PINs across the store")
	}
	if n >= pinRevokeAfter {
		// The message is matched by an alert rule: keep the words.
		s.logger.Warn().Str("device_id", d.ID.String()).Str("location_id", d.LocationID.String()).Int("wrong_pins", n).
			Msg("callcenter: device revoked (PIN guessing)")
		_, _ = s.Revoke(context.WithoutCancel(ctx), d.TenantID, d.LocationID, d.ID)
		return true
	}
	if w := pinWait(n); w > 0 {
		// The message is matched by an alert rule: keep the words. Never the PIN.
		s.logger.Warn().Str("device_id", d.ID.String()).Str("location_id", d.LocationID.String()).Int("wrong_pins", n).
			Dur("locked_for", w).Msg("callcenter: PIN lockout")
	}
	return false
}

func validPin4(pin string) bool { return len(pin) == 4 && staff.ValidPin(pin) }

// SignIn checks a PIN against the store's active staff and signs a
// call-center agent in on the device. ErrBadPin (not 4 digits), ErrWrongPin,
// ErrWrongRole (a cashier's / manager's / driver's PIN), Throttled.
func (s *Service) SignIn(ctx context.Context, d Device, pin string) (staff.Person, time.Time, error) {
	none := staff.Person{}
	if !validPin4(pin) {
		return none, time.Time{}, ErrBadPin // not a guess: nothing counted
	}
	mu := s.pinLock(d.ID)
	mu.Lock()
	defer mu.Unlock()
	// Count the attempt first (and learn in the same statement whether the
	// pad is locked or the device revoked).
	n, ok, err := s.repo.PinAttempt(ctx, d.ID, pinMaxFails, pinFirst, pinMaxWait, pinWindow)
	if err != nil {
		return none, time.Time{}, err
	}
	if !ok {
		live, until, err := s.repo.PinState(ctx, d.ID)
		if err != nil {
			return none, time.Time{}, err
		}
		if !live {
			return none, time.Time{}, ErrDeviceRequired
		}
		left := time.Second
		if until != nil && until.Sub(s.now()) > left {
			left = until.Sub(s.now())
		}
		return none, time.Time{}, Throttled{RetryAfter: left}
	}
	people, err := s.staff.People(ctx, d.LocationID)
	if err != nil {
		return none, time.Time{}, err
	}
	var hit *staff.Person
	for i := range people {
		p := &people[i]
		if p.Active && p.PinHash != "" && bcrypt.CompareHashAndPassword([]byte(p.PinHash), []byte(pin)) == nil {
			hit = p
			break
		}
	}
	// A cashier's / manager's / driver's PIN is refused exactly like a wrong
	// one (the caller answers both the same: the PIN pad must not tell which
	// store PINs exist), and counts as one.
	if hit == nil || hit.Role != staff.RoleCallcenter {
		err := ErrWrongPin
		if hit != nil {
			err = ErrWrongRole
		}
		if s.pinFailed(ctx, d, n) {
			return none, time.Time{}, ErrDeviceRevoked
		}
		return none, time.Time{}, err
	}
	// The same attempt succeeded: take it back, lift the lock it set (if it
	// did), start the session. A device revoked meanwhile gets no session.
	in, err := s.repo.PinSucceeded(ctx, d.ID, hit.ID, n >= pinMaxFails)
	if err != nil {
		return none, time.Time{}, err
	}
	if !in {
		return none, time.Time{}, ErrDeviceRequired
	}
	now := s.now()
	s.mu.Lock()
	s.seen[d.ID], s.seenSave[d.ID] = now, now
	s.mu.Unlock()
	s.publishDevice(d.Scope(), d.ID, Event{Name: "session.started"}) // this browser's stream may carry the Pi's events now
	return *hit, now.Add(s.idle(ctx, d.LocationID)), nil
}

// Agent is the person signed in on the device (false: none, or they were
// switched off / deleted / are no longer a call-center agent: the session ends).
func (s *Service) Agent(ctx context.Context, d Device) (staff.Person, bool, error) {
	if d.StaffID == nil {
		return staff.Person{}, false, nil
	}
	people, err := s.staff.People(ctx, d.LocationID)
	if err != nil {
		return staff.Person{}, false, err
	}
	for _, p := range people {
		if p.ID == *d.StaffID {
			if p.Active && p.Role == staff.RoleCallcenter {
				return p, true, nil
			}
			break
		}
	}
	s.end(ctx, d, "staff_changed")
	return staff.Person{}, false, nil
}

// Lock ends the device's session (the idle lock, or the agent leaves).
func (s *Service) Lock(ctx context.Context, d Device) {
	s.end(ctx, d, "locked")
}

func (s *Service) end(ctx context.Context, d Device, reason string) {
	ended, err := s.repo.EndSession(context.WithoutCancel(ctx), d.ID)
	if err != nil {
		s.logger.Warn().Err(err).Msg("callcenter: session not ended")
		return
	}
	s.mu.Lock()
	delete(s.seen, d.ID)
	delete(s.seenSave, d.ID)
	s.mu.Unlock()
	if ended {
		data, _ := json.Marshal(map[string]string{"reason": reason})
		s.publishDevice(d.Scope(), d.ID, Event{Name: "session.ended", Data: data})
		s.logger.Info().Str("device_id", d.ID.String()).Str("reason", reason).Msg("callcenter session ended")
	}
}

// staffChanged: a staff write. Sessions of people no longer active call-center
// agents end at once.
func (s *Service) staffChanged(ctx context.Context, tenantID, locationID uuid.UUID) {
	ctx = context.WithoutCancel(ctx)
	rows, err := s.repo.Sessions(ctx, locationID)
	if err != nil || len(rows) == 0 {
		return
	}
	people, err := s.staff.People(ctx, locationID)
	if err != nil {
		return
	}
	ok := map[uuid.UUID]bool{}
	for _, p := range people {
		ok[p.ID] = p.Active && p.Role == staff.RoleCallcenter
	}
	for _, r := range rows {
		if !ok[*r.StaffID] {
			s.end(ctx, Device{ID: r.ID, TenantID: tenantID, LocationID: locationID}, "staff_changed")
		}
	}
}

// --- Me -----------------------------------------------------------------------

type StoreView struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	LocationID uuid.UUID `json:"location_id"`
	Name       string    `json:"name"`
	Timezone   string    `json:"timezone"`
	Phone      string    `json:"phone"`
}

type SquareView struct {
	Environment   string `json:"environment"`
	ApplicationID string `json:"application_id"`
	LocationID    string `json:"location_id"`
}

// State is the store as the call center sees it now.
type State struct {
	PiOnline     bool                `json:"pi_online"`
	PiSince      *time.Time          `json:"-"`
	Open         bool                `json:"open"`
	ClosedReason string              `json:"closed_reason"`
	NextOpenAt   *time.Time          `json:"next_open_at"`
	ClosesAt     *time.Time          `json:"closes_at"`
	Paused       bool                `json:"paused"`
	PauseNote    string              `json:"pause_note"`
	Hours        []storestatus.Today `json:"-"`
	Store        StoreView           `json:"-"`
}

// reconnectGrace: a Pi whose stream dropped this recently is reconnecting
// (its 2 s backoff, main's blue-green swap), not offline.
const (
	reconnectGrace = 6 * time.Second
	reconnectWait  = 4 * time.Second
)

// PiOnline reports the Pi's live stream to main (a stream that left a few
// seconds ago still counts: the call center isn't flipped to "offline" for a
// reconnect).
func (s *Service) PiOnline(scope sseBroker.Scope) bool {
	n, away := s.presence.Presence(scope)
	return n > 0 || (!away.IsZero() && s.now().Sub(away) < reconnectGrace)
}

// streamLostGrace: after the Pi's last stream closes, relayed calls in flight
// get this long for their answer (the Pi posts it over plain HTTP, and may be
// back on a new stream in a moment) before they are failed.
const streamLostGrace = 2 * time.Second

// PiGone is the broker's "last stream closed" hook: when the stream hasn't
// come back after a short grace, the relayed calls still waiting are failed
// at once (503 store_offline, outcome unknown: the keys make the retry safe)
// instead of waiting out their 12 s / 35 s.
func (s *Service) PiGone(scope sseBroker.Scope) {
	time.Sleep(streamLostGrace)
	if n, _ := s.presence.Presence(scope); n > 0 {
		return
	}
	type failer interface {
		FailType(scope sseBroker.Scope, msgType string) int
	}
	if f, ok := s.rpc.(failer); ok {
		if n := f.FailType(scope, MsgRelay); n > 0 {
			s.logger.Warn().Str("location_id", scope.LocationID.String()).Int("calls", n).
				Msg("callcenter relay: the Pi's stream is gone, calls in flight failed (outcome unknown)")
		}
	}
}

// waitStream waits (briefly) for a reconnecting Pi's stream; false = offline.
func (s *Service) waitStream(ctx context.Context, scope sseBroker.Scope) bool {
	deadline := s.now().Add(reconnectWait)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		n, away := s.presence.Presence(scope)
		if n > 0 {
			return true
		}
		if away.IsZero() || s.now().Sub(away) >= reconnectGrace || !s.now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

// StoreState computes the store's state: the Pi's link and open / closed.
// Closed = outside the store's hours (a store with no hours is always open).
// A pause (the website's "we're slammed") does not close the call center: it
// is a phone order like the store's own; the app shows the note.
func (s *Service) StoreState(ctx context.Context, d Device) (State, error) {
	full, err := s.status.Status(ctx, d.TenantID, d.LocationID)
	if err != nil {
		return State{}, err
	}
	st := State{PiOnline: s.PiOnline(d.Scope()), Open: full.OpenNow, ClosesAt: full.ClosesAt, Hours: full.Week,
		Store: StoreView{TenantID: d.TenantID, LocationID: d.LocationID, Name: full.Store.Name, Timezone: full.Store.Timezone, Phone: full.Store.Phone}}
	if !st.Open {
		st.ClosedReason = "hours"
		st.NextOpenAt = full.NextOpenAt
	}
	if full.Pause != nil {
		st.Paused, st.PauseNote = true, full.Pause.Note
	}
	if st.Hours == nil {
		st.Hours = []storestatus.Today{}
	}
	return st, nil
}

// Square is the card field's public config, nil when the store takes no cards.
func (s *Service) Square(ctx context.Context, locationID uuid.UUID) *SquareView {
	if s.card == nil {
		return nil
	}
	env, app, loc, ok := s.card.WebCard(ctx, locationID)
	if !ok {
		return nil
	}
	return &SquareView{Environment: env, ApplicationID: app, LocationID: loc}
}

// Settings are the store's terminal settings (idle lock among them).
func (s *Service) Settings(ctx context.Context, locationID uuid.UUID) (staff.Settings, error) {
	st, _, err := s.staff.Settings(ctx, locationID)
	return st, err
}

// --- the relay ------------------------------------------------------------------

// RelayRequest is terminal.relay's payload.
type RelayRequest struct {
	Device     string            `json:"device"`
	DeviceName string            `json:"device_name"`
	StaffID    string            `json:"staff_id"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query,omitempty"`
	Headers    map[string]string `json:"headers"`
	Body       json.RawMessage   `json:"body"`
}

// RelayReply is the Pi's answer: its handler's status, headers and body.
type RelayReply struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// Relay sends one allowed call to the store's Pi and returns its answer.
// ErrOffline: no live stream, no answer in time, an older Pi, or the Pi's
// relay is full.
func (s *Service) Relay(ctx context.Context, d Device, staffID uuid.UUID, method, path, query string, headers map[string]string, body []byte) (RelayReply, error) {
	// A reconnecting Pi gets a moment. With no stream after it the request
	// still goes to the requester, which answers "offline" at once and counts
	// it (rpc.offline{type="terminal.relay"}: the alert reads that series).
	s.waitStream(ctx, d.Scope())
	// The menu is large (1 MB+): the Pi uploads it once per version per main
	// process. main asks with the ETag it holds and serves its copy on 304.
	menu := method == "GET" && path == "menu"
	var cached menuCopy
	browserTag := headers["If-None-Match"]
	if menu {
		s.mu.Lock()
		cached = s.menus[d.LocationID]
		s.mu.Unlock()
		if cached.etag != "" {
			headers = map[string]string{"If-None-Match": cached.etag}
		}
	}
	rep, err := s.relay(ctx, d, staffID, method, path, query, headers, body)
	if err != nil || !menu {
		return rep, err
	}
	switch {
	case rep.Status == 200 && rep.Headers["ETag"] != "":
		s.mu.Lock()
		s.menus[d.LocationID] = menuCopy{etag: rep.Headers["ETag"], body: rep.Body}
		s.mu.Unlock()
		if browserTag == rep.Headers["ETag"] {
			return RelayReply{Status: 304, Headers: map[string]string{"ETag": browserTag}}, nil
		}
	case rep.Status == 304 && cached.etag != "":
		if browserTag == cached.etag {
			return RelayReply{Status: 304, Headers: map[string]string{"ETag": cached.etag}}, nil
		}
		return RelayReply{Status: 200, Headers: map[string]string{"ETag": cached.etag}, Body: cached.body}, nil
	}
	return rep, nil
}

// menuCopy is the Pi's last menu answer for a store.
type menuCopy struct {
	etag string
	body json.RawMessage
}

func (s *Service) relay(ctx context.Context, d Device, staffID uuid.UUID, method, path, query string, headers map[string]string, body []byte) (RelayReply, error) {
	req := RelayRequest{Device: d.ID.String(), DeviceName: d.Name, StaffID: staffID.String(), Method: method, Path: path,
		Query: query, Headers: headers}
	if len(body) > 0 {
		req.Body = json.RawMessage(body)
	} else {
		req.Body = json.RawMessage("null")
	}
	timeout := relayTimeout
	if slowCall(path) {
		timeout = relaySlowTimeout
	}
	env, err := s.rpc.Request(ctx, d.Scope(), MsgRelay, req, timeout)
	if err != nil {
		if errors.Is(err, rpc.ErrAgentOffline) || errors.Is(err, rpc.ErrTimeout) || errors.Is(err, rpc.ErrStreamLost) {
			s.logger.Warn().Err(err).Str("method", method).Str("path", path).Str("location_id", d.LocationID.String()).Msg("callcenter relay: no answer from the Pi")
			return RelayReply{}, ErrOffline
		}
		return RelayReply{}, err
	}
	var rep RelayReply
	if err := json.Unmarshal(env.Payload, &rep); err != nil || rep.Status < 100 || rep.Status > 599 {
		// An older Pi answers an unknown type with something else: offline for us.
		s.logger.Warn().Str("type", env.Type).Str("path", path).Msg("callcenter relay: the Pi's reply isn't a relay answer (an older Pi?)")
		return RelayReply{}, ErrOffline
	}
	if rep.Status == 503 {
		var b struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(rep.Body, &b) == nil {
			switch b.Code {
			case "busy": // no slot within the Pi's wait: nothing was done
				return RelayReply{}, ErrBusy
			case "timeout": // the Pi's handler ran past its deadline: outcome unknown
				return RelayReply{}, ErrOffline
			}
		}
	}
	return rep, nil
}

// --- events -----------------------------------------------------------------------

// Event is one SSE frame for a call-center browser.
type Event struct {
	Name  string
	Data  json.RawMessage
	Close bool // the stream ends after it (device revoked)
}

// Subscriber is one browser's event stream.
type Subscriber struct {
	DeviceID uuid.UUID
	C        chan Event
}

// Subscribe opens a device's stream and asks the Pi for its terminal events.
func (s *Service) Subscribe(ctx context.Context, d Device) (*Subscriber, func()) {
	sub := &Subscriber{DeviceID: d.ID, C: make(chan Event, 256)}
	scope := d.Scope()
	s.mu.Lock()
	if s.subs[scope] == nil {
		s.subs[scope] = map[*Subscriber]struct{}{}
	}
	s.subs[scope][sub] = struct{}{}
	s.mu.Unlock()
	s.Want(ctx, scope, true)
	return sub, func() {
		s.mu.Lock()
		delete(s.subs[scope], sub)
		if len(s.subs[scope]) == 0 {
			delete(s.subs, scope)
			delete(s.wanted, scope)
		}
		s.mu.Unlock()
	}
}

// Want tells the Pi to forward its terminal events for a while (re-sent every
// minute while a browser listens; force = now, e.g. the Pi just reconnected).
func (s *Service) Want(ctx context.Context, scope sseBroker.Scope, force bool) {
	now := s.now()
	s.mu.Lock()
	if !force && now.Sub(s.wanted[scope]) < wantEvery {
		s.mu.Unlock()
		return
	}
	s.wanted[scope] = now
	s.mu.Unlock()
	payload, _ := json.Marshal(map[string]int{"seconds": wantSeconds})
	data, _ := json.Marshal(rpc.Envelope{Type: MsgEventsWant, ID: uuid.NewString(), Payload: payload})
	s.cmd.PublishCommand(ctx, scope, data)
}

func (s *Service) publish(scope sseBroker.Scope, ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[scope] {
		select {
		case sub.C <- ev:
		default: // a stuck browser: it refetches on its next reconnect
		}
	}
}

func (s *Service) publishDevice(scope sseBroker.Scope, id uuid.UUID, ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[scope] {
		if sub.DeviceID != id {
			continue
		}
		select {
		case sub.C <- ev:
		default:
		}
	}
}

// handleEvents is the Pi's terminal.events report: fan each event out to the
// store's call-center browsers under the Pi's own event name.
func (s *Service) handleEvents(_ context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var in struct {
		Events []struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		} `json:"events"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, rpc.ErrBadPayload
	}
	for _, e := range in.Events {
		if e.Event == "" || strings.ContainsAny(e.Event, "\r\n") || len(e.Data) > 1<<20 {
			continue
		}
		if len(e.Data) == 0 {
			e.Data = json.RawMessage("{}")
		}
		s.publish(scope, Event{Name: e.Event, Data: e.Data})
	}
	return nil, nil
}

// --- report -------------------------------------------------------------------------

type ReportTotals struct {
	Orders     int   `json:"orders"`
	Voided     int   `json:"voided"`
	SalesCents int64 `json:"sales_cents"`
	CardCents  int64 `json:"card_cents"`
	TipsCents  int64 `json:"tips_cents"`
}

type Report struct {
	From     string                             `json:"from"`
	To       string                             `json:"to"`
	Timezone string                             `json:"timezone"`
	Rows     []repositories.CallcenterReportRow `json:"rows"`
	Totals   ReportTotals                       `json:"totals"`
}

// ErrBadRange: from / to aren't dates, are reversed or span more than 92 days.
var ErrBadRange = errors.New("from / to must be YYYY-MM-DD, at most 92 days apart")

// MaxReportDays caps a report's range.
const MaxReportDays = 92

// Report is orders + sales per call-center agent per store-local day.
func (s *Service) Report(ctx context.Context, locationID uuid.UUID, from, to string) (Report, error) {
	l, err := s.locs(ctx, locationID)
	if err != nil {
		return Report{}, err
	}
	zone := l.Timezone
	loc, lerr := time.LoadLocation(zone)
	if lerr != nil || zone == "" {
		zone, loc = "UTC", time.UTC
	}
	today := s.now().In(loc).Format(time.DateOnly)
	if from == "" && to == "" {
		from, to = today, today
	} else if from == "" {
		from = to
	} else if to == "" {
		to = from
	}
	f, err1 := time.Parse(time.DateOnly, from)
	t, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > MaxReportDays*24*time.Hour {
		return Report{}, ErrBadRange
	}
	rows, err := s.repo.Report(ctx, locationID, zone, from, to)
	if err != nil {
		return Report{}, err
	}
	rep := Report{From: from, To: to, Timezone: zone, Rows: rows}
	for _, r := range rows {
		rep.Totals.Orders += r.Orders
		rep.Totals.Voided += r.Voided
		rep.Totals.SalesCents += r.SalesCents
		rep.Totals.CardCents += r.CardCents
		rep.Totals.TipsCents += r.TipsCents
	}
	return rep, nil
}
