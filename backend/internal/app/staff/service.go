// Package staffApp is the use-case layer of the counter terminal's staff
// (TERMINAL-PLAN.md 0d): Store › Staff's CRUD + the store's terminal
// settings, the Pi's terminal config, the journal the Pi syncs up, and the
// `terminal.changed` command to the store's Pi after every change.
package staffApp

import (
	"context"
	"encoding/json"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/staff"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

// PinCost is the bcrypt cost of a PIN hash. A 2–4 digit PIN has at most
// 11,100 values, so no cost makes a leaked hash safe; the hash only keeps the
// PIN out of plain sight. The Pi checks a PIN against every active hash at
// login, so a low cost keeps that fast on a Raspberry Pi.
const PinCost = 8

// MsgTerminalChanged is the command main pushes when staff or terminal
// settings change: {version}.
const MsgTerminalChanged = "terminal.changed"

// Repo is the staff store (repositories.StaffRepository).
type Repo interface {
	List(ctx context.Context, locationID uuid.UUID) ([]staff.Person, error)
	Settings(ctx context.Context, locationID uuid.UUID) (*staff.Settings, int64, error)
	Write(ctx context.Context, tenantID, locationID uuid.UUID, fn func(ctx context.Context, tx staff.Tx) error) (int64, error)
	InsertJournal(ctx context.Context, tenantID, locationID uuid.UUID, entries []staff.JournalEntry) (int, error)
	Journal(ctx context.Context, locationID uuid.UUID, before *time.Time, limit int) ([]staff.JournalRow, error)
}

// Pusher sends a command frame to a store's connected agents (*sse.Broker).
type Pusher interface {
	PublishCommand(ctx context.Context, scope sseBroker.Scope, data []byte) int
}

type Service struct {
	repo   Repo
	push   Pusher
	logger zerolog.Logger
	// onChanged hears every staff / settings write (the call center ends the
	// sessions of people switched off, deleted or changed).
	onChanged []func(ctx context.Context, tenantID, locationID uuid.UUID)
}

// OnChanged registers a listener for staff / terminal-settings writes.
func (s *Service) OnChanged(fn func(ctx context.Context, tenantID, locationID uuid.UUID)) {
	s.onChanged = append(s.onChanged, fn)
}

// People is the store's staff as stored (with PIN hashes): main's own PIN
// checks (the call center's sign-in).
func (s *Service) People(ctx context.Context, locationID uuid.UUID) ([]staff.Person, error) {
	return s.repo.List(ctx, locationID)
}

func NewService(repo Repo, push Pusher, lg zerolog.Logger) *Service {
	return &Service{repo: repo, push: push, logger: lg.With().Str("module", "staff").Logger()}
}

// View is a person as Store › Staff shows them (never the PIN).
type View struct {
	staff.Person
	EffectiveLimits staff.Effective `json:"effective_limits"`
}

// ConfigPerson is a person as the Pi gets them (with the PIN hash).
type ConfigPerson struct {
	ID              uuid.UUID       `json:"id"`
	FirstName       string          `json:"first_name"`
	LastName        string          `json:"last_name"`
	Role            string          `json:"role"`
	PinHash         string          `json:"pin_hash"`
	Limits          staff.Limits    `json:"limits"`
	EffectiveLimits staff.Effective `json:"effective_limits"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// Active false = switched off in main (contract §2.5zb): listed with no
	// PIN hash so the Pi ends their session with reason `deactivated`; a
	// person deleted is simply absent (`removed`).
	Active bool `json:"active"`
}

// Config is GET /store/v1/agent/terminal-config.
type Config struct {
	Version  int64          `json:"version"`
	Settings staff.Settings `json:"settings"`
	Staff    []ConfigPerson `json:"staff"`
}

// Settings are the store's terminal settings (the defaults when never saved)
// and its terminal version.
func (s *Service) Settings(ctx context.Context, locationID uuid.UUID) (staff.Settings, int64, error) {
	st, v, err := s.repo.Settings(ctx, locationID)
	if err != nil {
		return staff.Settings{}, 0, err
	}
	if st == nil {
		return staff.DefaultSettings(), v, nil
	}
	return *st, v, nil
}

// List is the store's staff as Store › Staff shows them.
func (s *Service) List(ctx context.Context, locationID uuid.UUID) ([]View, error) {
	people, err := s.repo.List(ctx, locationID)
	if err != nil {
		return nil, err
	}
	st, _, err := s.Settings(ctx, locationID)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(people))
	for _, p := range people {
		out = append(out, View{Person: p, EffectiveLimits: staff.EffectiveLimits(p, st)})
	}
	return out, nil
}

// Refunders are the store's people as the money rules see them (role +
// effective refund limit; a manager unlimited), keyed by id, switched-off
// people too (their earlier refunds still have a role). Floor events read
// it once per money event (contract/money-rules.json).
func (s *Service) Refunders(ctx context.Context, locationID uuid.UUID) (map[uuid.UUID]order.Refunder, error) {
	people, err := s.repo.List(ctx, locationID)
	if err != nil {
		return nil, err
	}
	st, _, err := s.Settings(ctx, locationID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]order.Refunder, len(people))
	for _, p := range people {
		out[p.ID] = order.RefunderOf(p.Role, staff.EffectiveLimits(p, st).RefundCents)
	}
	return out, nil
}

// RefundLimit is a person's role and refund limit at the store (nil =
// unlimited, a manager); found=false when the store has no such person.
func (s *Service) RefundLimit(ctx context.Context, locationID, staffID uuid.UUID) (string, *int64, bool, error) {
	people, err := s.repo.List(ctx, locationID)
	if err != nil {
		return "", nil, false, err
	}
	st, _, err := s.Settings(ctx, locationID)
	if err != nil {
		return "", nil, false, err
	}
	for _, p := range people {
		if p.ID == staffID {
			return p.Role, staff.EffectiveLimits(p, st).RefundCents, true, nil
		}
	}
	return "", nil, false, nil
}

// Config is what the store's Pi pulls: the settings and the staff, the
// active ones with their PIN hashes, the switched-off ones with active false
// and no hash (the Pi tells `deactivated` from `removed`, agent §2.10u).
func (s *Service) Config(ctx context.Context, locationID uuid.UUID) (Config, error) {
	people, err := s.repo.List(ctx, locationID)
	if err != nil {
		return Config{}, err
	}
	st, v, err := s.Settings(ctx, locationID)
	if err != nil {
		return Config{}, err
	}
	c := Config{Version: v, Settings: st, Staff: []ConfigPerson{}}
	for _, p := range people {
		cp := ConfigPerson{ID: p.ID, FirstName: p.FirstName, LastName: p.LastName, Role: p.Role,
			PinHash: p.PinHash, Limits: p.Limits, EffectiveLimits: staff.EffectiveLimits(p, st), UpdatedAt: p.UpdatedAt, Active: p.Active}
		if !p.Active || p.Role == staff.RoleCallcenter {
			// An inactive person's hash never leaves main; nor does a
			// call-center agent's: main checks that PIN itself, the Pi never
			// does (so no Pi, old or new, can log one in at a store terminal).
			cp.PinHash = ""
		}
		c.Staff = append(c.Staff, cp)
	}
	return c, nil
}

// NewPerson is a POST /me/staff body.
type NewPerson struct {
	FirstName string        `json:"first_name"`
	LastName  string        `json:"last_name"`
	Role      string        `json:"role"`
	Pin       string        `json:"pin"`
	Limits    *staff.Limits `json:"limits"`
}

// Change is a PATCH /me/staff/:id body (nil = keep).
type Change struct {
	FirstName *string       `json:"first_name"`
	LastName  *string       `json:"last_name"`
	Role      *string       `json:"role"`
	Limits    *staff.Limits `json:"limits"`
	Active    *bool         `json:"active"`
	// Pin sets a new PIN (needed to turn an inactive person back on).
	Pin *string `json:"pin"`
}

// pinTaken: does another active person (not except) have this PIN?
func pinTaken(people []staff.Person, pin string, except uuid.UUID) bool {
	for _, p := range people {
		if p.Active && p.ID != except && bcrypt.CompareHashAndPassword([]byte(p.PinHash), []byte(pin)) == nil {
			return true
		}
	}
	return false
}

func hashPin(pin string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pin), PinCost)
	return string(h), err
}

// Create adds a person.
func (s *Service) Create(ctx context.Context, tenantID, locationID uuid.UUID, in NewPerson) (View, error) {
	first, err := staff.NormName(in.FirstName, true)
	if err != nil {
		return View{}, err
	}
	last, err := staff.NormName(in.LastName, false)
	if err != nil {
		return View{}, err
	}
	if !staff.ValidRole(in.Role) {
		return View{}, staff.ErrBadRole
	}
	if !staff.ValidPin(in.Pin) {
		return View{}, staff.ErrBadPin
	}
	if !staff.ValidPinFor(in.Role, in.Pin) {
		return View{}, staff.PinError(in.Role)
	}
	limits := staff.Limits{}
	if in.Limits != nil {
		limits = *in.Limits
	}
	if err := limits.Validate(); err != nil {
		return View{}, err
	}
	hash, err := hashPin(in.Pin)
	if err != nil {
		return View{}, err
	}
	var saved staff.Person
	v, err := s.repo.Write(ctx, tenantID, locationID, func(ctx context.Context, tx staff.Tx) error {
		people, err := tx.List(ctx)
		if err != nil {
			return err
		}
		if len(people) >= staff.MaxStaff {
			return staff.ErrTooMany
		}
		if pinTaken(people, in.Pin, uuid.Nil) {
			return staff.ErrPinTaken
		}
		saved, err = tx.Insert(ctx, staff.Person{FirstName: first, LastName: last, Role: in.Role, Limits: limits, Active: true, PinHash: hash})
		return err
	})
	if err != nil {
		return View{}, err
	}
	s.changed(ctx, tenantID, locationID, v)
	return s.view(ctx, locationID, saved)
}

func (s *Service) view(ctx context.Context, locationID uuid.UUID, p staff.Person) (View, error) {
	st, _, err := s.Settings(ctx, locationID)
	if err != nil {
		return View{}, err
	}
	return View{Person: p, EffectiveLimits: staff.EffectiveLimits(p, st)}, nil
}

func find(people []staff.Person, id uuid.UUID) (staff.Person, bool) {
	for _, p := range people {
		if p.ID == id {
			return p, true
		}
	}
	return staff.Person{}, false
}

// Update changes a person. An inactive person's PIN is released (a colleague
// may take it meanwhile, and a hash can't be compared with another hash), so
// turning someone back on needs a PIN (ErrPinRequired); a PIN given at any
// other time is a reset.
func (s *Service) Update(ctx context.Context, tenantID, locationID, id uuid.UUID, ch Change) (View, error) {
	hash := ""
	if ch.Pin != nil {
		if !staff.ValidPin(*ch.Pin) {
			return View{}, staff.ErrBadPin
		}
		var err error
		if hash, err = hashPin(*ch.Pin); err != nil {
			return View{}, err
		}
	}
	var saved staff.Person
	v, err := s.repo.Write(ctx, tenantID, locationID, func(ctx context.Context, tx staff.Tx) error {
		people, err := tx.List(ctx)
		if err != nil {
			return err
		}
		p, ok := find(people, id)
		if !ok {
			return staff.ErrNotFound
		}
		if ch.FirstName != nil {
			if p.FirstName, err = staff.NormName(*ch.FirstName, true); err != nil {
				return err
			}
		}
		if ch.LastName != nil {
			if p.LastName, err = staff.NormName(*ch.LastName, false); err != nil {
				return err
			}
		}
		if ch.Role != nil {
			if !staff.ValidRole(*ch.Role) {
				return staff.ErrBadRole
			}
			// Becoming a call-center agent needs a (4-digit) PIN with it: the
			// saved hash can't say how long the old one was.
			if *ch.Role == staff.RoleCallcenter && p.Role != staff.RoleCallcenter && ch.Pin == nil {
				return staff.ErrPinRequired
			}
			// Becoming a manager keeps the saved PIN (the Staff page's edit
			// dialog has no PIN boxes): the hash can't say how long it is,
			// so the Pi refuses a manager whose PIN isn't 4 digits at the
			// PIN pad, with the words to set a new one here.
			p.Role = *ch.Role
		}
		if ch.Pin != nil && !staff.ValidPinFor(p.Role, *ch.Pin) {
			return staff.PinError(p.Role)
		}
		if ch.Limits != nil {
			if err := ch.Limits.Validate(); err != nil {
				return err
			}
			p.Limits = *ch.Limits
		}
		if ch.Active != nil {
			if *ch.Active && !p.Active && ch.Pin == nil {
				return staff.ErrPinRequired
			}
			p.Active = *ch.Active
		}
		if ch.Pin != nil {
			if p.Active && pinTaken(people, *ch.Pin, p.ID) {
				return staff.ErrPinTaken
			}
			p.PinHash = hash
		}
		saved, err = tx.Update(ctx, p)
		return err
	})
	if err != nil {
		return View{}, err
	}
	s.changed(ctx, tenantID, locationID, v)
	return s.view(ctx, locationID, saved)
}

// ResetPin sets a person's new PIN.
func (s *Service) ResetPin(ctx context.Context, tenantID, locationID, id uuid.UUID, pin string) error {
	if !staff.ValidPin(pin) {
		return staff.ErrBadPin
	}
	hash, err := hashPin(pin)
	if err != nil {
		return err
	}
	v, err := s.repo.Write(ctx, tenantID, locationID, func(ctx context.Context, tx staff.Tx) error {
		people, err := tx.List(ctx)
		if err != nil {
			return err
		}
		p, ok := find(people, id)
		if !ok {
			return staff.ErrNotFound
		}
		if !staff.ValidPinFor(p.Role, pin) {
			return staff.PinError(p.Role)
		}
		if p.Active && pinTaken(people, pin, p.ID) {
			return staff.ErrPinTaken
		}
		p.PinHash = hash
		_, err = tx.Update(ctx, p)
		return err
	})
	if err != nil {
		return err
	}
	s.changed(ctx, tenantID, locationID, v)
	return nil
}

// Delete removes a person.
func (s *Service) Delete(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	v, err := s.repo.Write(ctx, tenantID, locationID, func(ctx context.Context, tx staff.Tx) error {
		return tx.Delete(ctx, id)
	})
	if err != nil {
		return err
	}
	s.changed(ctx, tenantID, locationID, v)
	return nil
}

// SaveSettings replaces the store's terminal settings.
func (s *Service) SaveSettings(ctx context.Context, tenantID, locationID uuid.UUID, st staff.Settings) (staff.Settings, error) {
	st.Normalize()
	if err := st.Validate(); err != nil {
		return staff.Settings{}, err
	}
	v, err := s.repo.Write(ctx, tenantID, locationID, func(ctx context.Context, tx staff.Tx) error {
		return tx.SaveSettings(ctx, st)
	})
	if err != nil {
		return staff.Settings{}, err
	}
	s.changed(ctx, tenantID, locationID, v)
	return st, nil
}

// changed tells the store's Pi (fire-and-forget; it also pulls at connect).
func (s *Service) changed(ctx context.Context, tenantID, locationID uuid.UUID, version int64) {
	for _, fn := range s.onChanged {
		fn(ctx, tenantID, locationID)
	}
	if s.push == nil {
		return
	}
	payload, _ := json.Marshal(map[string]int64{"version": version})
	data, _ := json.Marshal(map[string]any{"type": MsgTerminalChanged, "id": uuid.NewString(), "payload": json.RawMessage(payload)})
	n := s.push.PublishCommand(ctx, sseBroker.Scope{TenantID: tenantID, LocationID: locationID}, data)
	s.logger.Debug().Ctx(ctx).Str("location_id", locationID.String()).Int64("version", version).Int("delivered", n).Msg("terminal.changed pushed")
}

// AddJournal stores the Pi's journal entries (idempotent per entry id).
func (s *Service) AddJournal(ctx context.Context, tenantID, locationID uuid.UUID, entries []staff.JournalEntry) (int, error) {
	return s.repo.InsertJournal(ctx, tenantID, locationID, entries)
}

// Journal lists the store's newest journal entries.
func (s *Service) Journal(ctx context.Context, locationID uuid.UUID, before *time.Time, limit int) ([]staff.JournalRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.repo.Journal(ctx, locationID, before, limit)
}
