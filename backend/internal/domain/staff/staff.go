// Package staff models the counter terminal's staff (TERMINAL-PLAN.md Stage
// 0d): people who log in to a terminal with a 2–4 digit PIN, their role
// (cashier, manager, driver) and limits, and the store's terminal settings
// (the owner's X values: role limits, auto-lock, the counter sequence start,
// the receipt screen's auto-Done).
package staff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Roles.
const (
	RoleCashier = "cashier"
	RoleManager = "manager"
	RoleDriver  = "driver"
)

// Limits are refund / discount ceilings. A nil field = not set (an override
// that falls back to the role default, or — effective — unlimited).
type Limits struct {
	RefundCents *int64 `json:"refund_cents,omitempty"`
	DiscountPct *int   `json:"discount_pct,omitempty"`
}

// Effective are the limits that apply to a person: null = unlimited.
type Effective struct {
	RefundCents *int64 `json:"refund_cents"`
	DiscountPct *int   `json:"discount_pct"`
}

// Person is one member of staff (the PIN hash never leaves main except to the
// store's own Pi, through PinHash on the terminal config).
type Person struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"-"`
	LocationID uuid.UUID `json:"-"`
	FirstName  string    `json:"first_name"`
	LastName   string    `json:"last_name"`
	Role       string    `json:"role"`
	Limits     Limits    `json:"limits"`
	Active     bool      `json:"active"`
	PinHash    string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Settings are a store's terminal settings.
type Settings struct {
	CashierRefundCents     int64 `json:"cashier_refund_cents"`
	CashierDiscountPct     int   `json:"cashier_discount_pct"`
	AutoLockMinutes        int   `json:"auto_lock_minutes"`
	CounterStart           int   `json:"counter_start"`
	ReceiptAutoDoneSeconds int   `json:"receipt_auto_done_seconds"`
}

// DefaultSettings: refunds are a manager's (TERMINAL-PLAN "Refunds: manager
// only"), a cashier discounts up to 10 %, the terminal locks after 5 idle
// minutes, counter numbers start at 1, the receipt screen closes after 15 s.
func DefaultSettings() Settings {
	return Settings{CashierRefundCents: 0, CashierDiscountPct: 10, AutoLockMinutes: 5, CounterStart: 1, ReceiptAutoDoneSeconds: 15}
}

// Ranges.
const (
	MaxRefundCents   = 1_000_000
	MaxNameLen       = 40
	MaxStaff         = 200
	MaxAutoLock      = 240
	MaxCounterStart  = 999_999
	MaxReceiptDoneS  = 600
	MaxReasonLen     = 200
	MaxJournalBatch  = 200
	MaxJournalAction = 40
	MaxJournalDetail = 4096
	MaxDeviceIDLen   = 64
)

var (
	ErrBadPin      = errors.New("the PIN must be 2–4 digits")
	ErrBadName     = errors.New("first name 1–40 characters, last name up to 40")
	ErrBadRole     = errors.New("role must be cashier, manager or driver")
	ErrBadLimits   = errors.New("refund 0–1,000,000 cents, discount 0–100 %")
	ErrBadSettings = errors.New("bad terminal settings")
	ErrPinTaken    = errors.New("another active person already has that PIN")
	ErrTooMany     = errors.New("too many staff (200 per store)")
	ErrNotFound    = errors.New("staff not found in this store")
	ErrPinRequired = errors.New("turning someone back on needs a new PIN")
)

// ValidPin: 2–4 ASCII digits.
func ValidPin(pin string) bool {
	if len(pin) < 2 || len(pin) > 4 {
		return false
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ValidRole reports whether r is a role.
func ValidRole(r string) bool { return r == RoleCashier || r == RoleManager || r == RoleDriver }

// NormName trims a name and checks its length (required: the first name).
func NormName(s string, required bool) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if (required && s == "") || utf8.RuneCountInString(s) > MaxNameLen {
		return "", ErrBadName
	}
	return s, nil
}

// Validate checks a person's own limits.
func (l Limits) Validate() error {
	if l.RefundCents != nil && (*l.RefundCents < 0 || *l.RefundCents > MaxRefundCents) {
		return ErrBadLimits
	}
	if l.DiscountPct != nil && (*l.DiscountPct < 0 || *l.DiscountPct > 100) {
		return ErrBadLimits
	}
	return nil
}

// Validate checks the settings.
func (s Settings) Validate() error {
	switch {
	case s.CashierRefundCents < 0 || s.CashierRefundCents > MaxRefundCents:
		return fmt.Errorf("%w: cashier_refund_cents must be 0–%d", ErrBadSettings, MaxRefundCents)
	case s.CashierDiscountPct < 0 || s.CashierDiscountPct > 100:
		return fmt.Errorf("%w: cashier_discount_pct must be 0–100", ErrBadSettings)
	case s.AutoLockMinutes < 1 || s.AutoLockMinutes > MaxAutoLock:
		return fmt.Errorf("%w: auto_lock_minutes must be 1–%d", ErrBadSettings, MaxAutoLock)
	case s.CounterStart < 1 || s.CounterStart > MaxCounterStart:
		return fmt.Errorf("%w: counter_start must be 1–%d", ErrBadSettings, MaxCounterStart)
	case s.ReceiptAutoDoneSeconds < 0 || s.ReceiptAutoDoneSeconds > MaxReceiptDoneS:
		return fmt.Errorf("%w: receipt_auto_done_seconds must be 0–%d", ErrBadSettings, MaxReceiptDoneS)
	}
	return nil
}

// RoleDefaults are a role's limits under the store's settings: managers are
// unlimited, drivers can't refund or discount.
func RoleDefaults(role string, s Settings) Effective {
	zero64, zero := int64(0), 0
	switch role {
	case RoleManager:
		return Effective{}
	case RoleCashier:
		r, d := s.CashierRefundCents, s.CashierDiscountPct
		return Effective{RefundCents: &r, DiscountPct: &d}
	}
	return Effective{RefundCents: &zero64, DiscountPct: &zero}
}

// EffectiveLimits are what applies to p: their own limits over their role's.
func EffectiveLimits(p Person, s Settings) Effective {
	e := RoleDefaults(p.Role, s)
	if p.Limits.RefundCents != nil {
		v := *p.Limits.RefundCents
		e.RefundCents = &v
	}
	if p.Limits.DiscountPct != nil {
		v := *p.Limits.DiscountPct
		e.DiscountPct = &v
	}
	return e
}

// JournalEntry is one terminal action the Pi recorded.
type JournalEntry struct {
	ID       uuid.UUID
	At       time.Time
	StaffID  *uuid.UUID
	DeviceID string
	Action   string
	OrderID  *uuid.UUID
	Detail   []byte // a JSON object
}

// JournalRow is one stored journal entry (Store › Staff's journal).
type JournalRow struct {
	ID       uuid.UUID       `json:"id"`
	At       time.Time       `json:"at"`
	StaffID  *uuid.UUID      `json:"staff_id"`
	DeviceID string          `json:"device_id"`
	Action   string          `json:"action"`
	OrderID  *uuid.UUID      `json:"order_id"`
	Detail   json.RawMessage `json:"detail"`
}

// Tx is a staff write inside the store's lock (the repository's Write).
type Tx interface {
	List(ctx context.Context) ([]Person, error)
	Insert(ctx context.Context, p Person) (Person, error)
	Update(ctx context.Context, p Person) (Person, error)
	Delete(ctx context.Context, id uuid.UUID) error
	SaveSettings(ctx context.Context, s Settings) error
}
