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
	"math"
	"slices"
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
	// RoleCallcenter is a call-center agent (CALLCENTER-PLAN.md): their PIN
	// works only in the call-center app (/callcenter/v1), never on a store
	// terminal; no refunds, no discounts.
	RoleCallcenter = "callcenter"
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
	// AutoCloseBatchAt: the Pi closes a batch nobody closed at this store
	// time ("HH:MM", 24 h; "" = off). Default DefaultAutoCloseBatchAt.
	AutoCloseBatchAt string `json:"auto_close_batch_at"`

	// The terminal app's own settings (TERMINAL-PLAN 2026-11-11; the Pi
	// passes them through untouched).
	DefaultTab          string  `json:"default_tab"`            // the tab a PIN login lands on
	ShowHistory         bool    `json:"show_history"`           // the History tab on the top bar
	ButtonFont          string  `json:"button_font"`            // one of ButtonFonts
	CashierPriceDropPct int     `json:"cashier_price_drop_pct"` // a cashier lowers an item's price by up to this % without a manager
	ClickSound          bool    `json:"click_sound"`            // the soft click on the bubbly buttons
	PricesOnButtons     bool    `json:"prices_on_buttons"`      // menu buttons show prices (off = captions only)
	OutOfAreaMarginMi   float64 `json:"out_of_area_margin_mi"`  // a delivery this far past the store's radius still goes (warned)
	NameOnPaidCounter   bool    `json:"name_on_paid_counter"`   // pressing Counter asks a name first
	CardTips            bool    `json:"card_tips"`              // the Square Reader offers tips
	CardTipPercents     []int   `json:"card_tip_percents"`      // the Reader's tip choices
	EmployeeMealPct     int     `json:"employee_meal_pct"`      // the Employee meal discount
	ToppingMaxCount     int     `json:"topping_max_count"`      // tap again = one more of a topping, up to this many (2×, 3×; the Pi caps a repeated option)

	// Drawers + phone orders (2026-11-11).
	CashierPayoutCents   int64    `json:"cashier_payout_cents"`   // a cashier's drawer pay-out without a manager's PIN (the Pi reads it)
	DefaultPickupMinutes int      `json:"default_pickup_minutes"` // a pickup's quoted minutes on the terminal
	AreaCodes            []string `json:"area_codes"`             // the store's local area codes (the terminal reads them for phone numbers)

	// DrawerStartCents (the owner, 2026-11-12): the cash left in each
	// terminal's drawer at opening (the Pi opens a terminal's drawer with it
	// at its first payment; the till report's starting cash). Same for every
	// terminal.
	DrawerStartCents int64 `json:"drawer_start_cents"`

	// WasteReasons (the owner, 2026-11-12): the reasons a cashier picks for
	// a "void waste" (made already, never picked up …; nothing prints, the
	// reason goes on the report). X: edited on Store › Staff.
	WasteReasons []string `json:"waste_reasons"`

	// VoidReasons (the owner, 2026-11-12, round 2): the reasons a cashier
	// picks to void an order that was sent (the kitchen gets a void ticket).
	// Picked, never typed; the terminal adds no "Other" of its own, so keep
	// one in the list. X: edited on Store › Staff.
	VoidReasons []string `json:"void_reasons"`

	// PhoneChargeTimeoutSeconds (the owner's X, 2026-11-12, "Send to phone",
	// agent contract §2.10z): a card charge sent to a cashier's phone that
	// nobody finishes in Square within this long expires (a late tap is still
	// recorded). 0 in a stored body = the default. Edited on Store › Staff.
	PhoneChargeTimeoutSeconds int `json:"phone_charge_timeout_seconds"`

	// CallcenterIdleLockMinutes: the call-center app asks for the PIN again
	// after this long idle, and main ends the agent's session (the owner's X,
	// default 15).
	CallcenterIdleLockMinutes int `json:"callcenter_idle_lock_minutes"`

	// QuibaseCounterMinCents (the owner's X, 2026-11-13; agent 2.5.4): an
	// order rung at a counter terminal (Centurion; not the call center's)
	// shows on the counter tablet (Quibase) only when its total is over
	// this AND a line of it goes to a kitchen station (not every line "No
	// kitchen ticket"). The Pi applies it to what it hands Quibase; the
	// order still counts everywhere else. 0 = any total. Edited on Store ›
	// Staff (Orders). Default DefaultQuibaseCounterMinCents ($5).
	QuibaseCounterMinCents int64 `json:"quibase_counter_min_cents"`
}

// DefaultQuibaseCounterMinCents: a terminal order shows on Quibase over $5
// (the owner, 2026-11-13). MaxQuibaseCounterMinCents bounds the setting.
const (
	DefaultQuibaseCounterMinCents = 500
	MaxQuibaseCounterMinCents     = 100_000
)

// DefaultCallcenterIdleLock is the call center's idle lock (minutes).
const DefaultCallcenterIdleLock = 15

// Send to phone's timeout: default and bounds (seconds).
const (
	DefaultPhoneChargeTimeoutS = 180
	MinPhoneChargeTimeoutS     = 30
	MaxPhoneChargeTimeoutS     = 1800
)

// DefaultVoidReasons are the owner's defaults (2026-11-12).
func DefaultVoidReasons() []string {
	return []string{"Customer cancelled", "Wrong order", "Duplicate", "Took too long", "Other"}
}

// DefaultWasteReasons are the owner's (2026-11-12).
func DefaultWasteReasons() []string {
	return []string{"Never picked up", "Made wrong / remade", "Customer complaint", "Burnt / dropped"}
}

// Bounds of the waste reasons.
const (
	MaxWasteReasons   = 20
	MaxWasteReasonLen = 40
)

// DefaultTabs are the tabs a login may land on (the terminal's names;
// Drawer is a bottom-bar button, not a landing tab).
var DefaultTabs = []string{"info", "counter", "takeout", "delivery", "pickup", "history", "dispatch"}

// ButtonFonts are the menu buttons' font choices (the terminal maps each
// name to a bundled font; "system" = the platform font, Roboto).
var ButtonFonts = []string{"barlow", "barlow_condensed", "barlow_semi_condensed", "system",
	"fira_sans_condensed", "ibm_plex_sans_condensed", "atkinson_hyperlegible"}

// Ranges of the terminal app's settings.
const (
	MaxOutOfAreaMarginMi = 10
	MaxToppingCount      = 10
	MaxCardTipPercents   = 6
	MaxPayoutCents       = 100_000
	MaxDrawerStartCents  = 1_000_000
	MaxPickupMinutes     = 240
	MaxAreaCodes         = 20
)

// DefaultAreaCodes are Lombardi's (Baltimore County): 410, 443, 667.
func DefaultAreaCodes() []string { return []string{"410", "443", "667"} }

// ValidAreaCode: three digits, the first 2–9 (a NANP area code).
func ValidAreaCode(c string) bool {
	return len(c) == 3 && c[0] >= '2' && c[0] <= '9' && c[1] >= '0' && c[1] <= '9' && c[2] >= '0' && c[2] <= '9'
}

// The defaults the owner set on 2026-11-12 (they were 10 % and 50 %;
// migration 00039 moved stores that still held those). Auto-lock 5 min and
// the cashier price drop 20 % were already the defaults.
const (
	DefaultCashierDiscountPct = 20
	DefaultEmployeeMealPct    = 20
	// DefaultCashierRefundCents: a cashier gives back up to $20 in cash per
	// order without a manager (the owner, 2026-11-12; it was 0 = every
	// refund a manager's; migration 00042 moved stores still on 0). Card
	// refunds always need a manager (the Pi's rule).
	DefaultCashierRefundCents = 2000
)

// DefaultAutoCloseBatchAt is the batch auto-close time (the owner: on by
// default at 4:00 am).
const DefaultAutoCloseBatchAt = "04:00"

// DefaultSettings are every store's defaults (the owner, 2026-11-12: "everything
// here is default, not just for one store"): a cashier refunds cash up to
// $20 per order (card refunds are a manager's), discounts up to 20 %,
// the terminal locks after 5 idle minutes, counter numbers start at 1, the
// receipt screen closes after 15 s; the app lands on Info with History
// hidden, Barlow buttons without prices or clicks, a cashier lowers a price
// up to 20 %, deliveries up to 1 mile past the radius go, no name on a paid
// counter order, no card tips (15 / 18 / 20 % when on), the employee meal is
// 20 % off, a topping goes on up to 3 times (tap again = 2×, 3×), a cashier
// pays out up
// to $20 from the drawer, a pickup is quoted 15 minutes, the area codes are
// 410 / 443 / 667. (Drivers claim a delivery nobody took once it is past
// the order's promised time on the Pi: no setting since 2026-11-12.)
func DefaultSettings() Settings {
	return Settings{CashierRefundCents: DefaultCashierRefundCents, CashierDiscountPct: DefaultCashierDiscountPct, AutoLockMinutes: 5, CounterStart: 1, ReceiptAutoDoneSeconds: 15,
		AutoCloseBatchAt: DefaultAutoCloseBatchAt,
		DefaultTab:       "info", ButtonFont: "barlow", CashierPriceDropPct: 20, OutOfAreaMarginMi: 1,
		CardTipPercents: []int{15, 18, 20}, EmployeeMealPct: DefaultEmployeeMealPct, ToppingMaxCount: 3,
		CashierPayoutCents: 2000, DefaultPickupMinutes: 15, AreaCodes: DefaultAreaCodes(), DrawerStartCents: 10000,
		WasteReasons: DefaultWasteReasons(), VoidReasons: DefaultVoidReasons(), PhoneChargeTimeoutSeconds: DefaultPhoneChargeTimeoutS,
		CallcenterIdleLockMinutes: DefaultCallcenterIdleLock, QuibaseCounterMinCents: DefaultQuibaseCounterMinCents}
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
	ErrBadPin  = errors.New("the PIN must be 2–4 digits")
	ErrBadName = errors.New("first name 1–40 characters, last name up to 40")
	ErrBadRole = errors.New("role must be cashier, manager, driver or callcenter")
	// ErrBadCallcenterPin wraps ErrBadPin: a call-center agent's PIN is 4 digits.
	ErrBadCallcenterPin = fmt.Errorf("%w (a call-center agent's PIN is exactly 4 digits)", ErrBadPin)
	// ErrBadManagerPin wraps ErrBadPin: a manager's PIN is exactly 4 digits
	// (the store security round, 2026-11-12: the Pi refuses a shorter one).
	ErrBadManagerPin = fmt.Errorf("%w (a manager's PIN is exactly 4 digits)", ErrBadPin)
	ErrBadLimits     = errors.New("refund 0–1,000,000 cents, discount 0–100 %")
	ErrBadSettings   = errors.New("bad terminal settings")
	ErrPinTaken      = errors.New("another active person already has that PIN")
	ErrTooMany       = errors.New("too many staff (200 per store)")
	ErrNotFound      = errors.New("staff not found in this store")
	ErrPinRequired   = errors.New("turning someone back on needs a new PIN")
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
func ValidRole(r string) bool {
	return r == RoleCashier || r == RoleManager || r == RoleDriver || r == RoleCallcenter
}

// ValidPinFor: a manager's and a call-center agent's PIN is exactly 4
// digits, a cashier's or a driver's 2-4.
func ValidPinFor(role, pin string) bool {
	if role == RoleCallcenter || role == RoleManager {
		return len(pin) == 4 && ValidPin(pin)
	}
	return ValidPin(pin)
}

// PinError is what a PIN that ValidPinFor refused for role answers.
func PinError(role string) error {
	if role == RoleManager {
		return ErrBadManagerPin
	}
	return ErrBadCallcenterPin
}

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
	case s.CallcenterIdleLockMinutes != 0 && (s.CallcenterIdleLockMinutes < 1 || s.CallcenterIdleLockMinutes > MaxAutoLock):
		return fmt.Errorf("%w: callcenter_idle_lock_minutes must be 1–%d", ErrBadSettings, MaxAutoLock)
	case s.PhoneChargeTimeoutSeconds != 0 && (s.PhoneChargeTimeoutSeconds < MinPhoneChargeTimeoutS || s.PhoneChargeTimeoutSeconds > MaxPhoneChargeTimeoutS):
		return fmt.Errorf("%w: phone_charge_timeout_seconds must be %d–%d", ErrBadSettings, MinPhoneChargeTimeoutS, MaxPhoneChargeTimeoutS)
	case s.ReceiptAutoDoneSeconds < 0 || s.ReceiptAutoDoneSeconds > MaxReceiptDoneS:
		return fmt.Errorf("%w: receipt_auto_done_seconds must be 0–%d", ErrBadSettings, MaxReceiptDoneS)
	case !ValidHHMM(s.AutoCloseBatchAt):
		return fmt.Errorf("%w: auto_close_batch_at must be HH:MM (24 h) or empty (off)", ErrBadSettings)
	case !slices.Contains(DefaultTabs, s.DefaultTab):
		return fmt.Errorf("%w: default_tab must be one of %s", ErrBadSettings, strings.Join(DefaultTabs, ", "))
	case !slices.Contains(ButtonFonts, s.ButtonFont):
		return fmt.Errorf("%w: button_font must be one of %s", ErrBadSettings, strings.Join(ButtonFonts, ", "))
	case s.CashierPriceDropPct < 0 || s.CashierPriceDropPct > 100:
		return fmt.Errorf("%w: cashier_price_drop_pct must be 0–100", ErrBadSettings)
	case math.IsNaN(s.OutOfAreaMarginMi) || s.OutOfAreaMarginMi < 0 || s.OutOfAreaMarginMi > MaxOutOfAreaMarginMi:
		return fmt.Errorf("%w: out_of_area_margin_mi must be 0–%d", ErrBadSettings, MaxOutOfAreaMarginMi)
	case len(s.CardTipPercents) < 1 || len(s.CardTipPercents) > MaxCardTipPercents:
		return fmt.Errorf("%w: card_tip_percents needs 1–%d values", ErrBadSettings, MaxCardTipPercents)
	case s.EmployeeMealPct < 0 || s.EmployeeMealPct > 100:
		return fmt.Errorf("%w: employee_meal_pct must be 0–100", ErrBadSettings)
	case s.ToppingMaxCount < 1 || s.ToppingMaxCount > MaxToppingCount:
		return fmt.Errorf("%w: topping_max_count must be 1–%d", ErrBadSettings, MaxToppingCount)
	case s.CashierPayoutCents < 0 || s.CashierPayoutCents > MaxPayoutCents:
		return fmt.Errorf("%w: cashier_payout_cents must be 0–%d", ErrBadSettings, MaxPayoutCents)
	case s.QuibaseCounterMinCents < 0 || s.QuibaseCounterMinCents > MaxQuibaseCounterMinCents:
		return fmt.Errorf("%w: quibase_counter_min_cents must be 0–%d", ErrBadSettings, MaxQuibaseCounterMinCents)
	case s.DrawerStartCents < 0 || s.DrawerStartCents > MaxDrawerStartCents:
		return fmt.Errorf("%w: drawer_start_cents must be 0–%d", ErrBadSettings, MaxDrawerStartCents)
	case s.DefaultPickupMinutes < 0 || s.DefaultPickupMinutes > MaxPickupMinutes:
		return fmt.Errorf("%w: default_pickup_minutes must be 0–%d", ErrBadSettings, MaxPickupMinutes)
	case len(s.AreaCodes) < 1 || len(s.AreaCodes) > MaxAreaCodes:
		return fmt.Errorf("%w: area_codes needs 1–%d codes", ErrBadSettings, MaxAreaCodes)
	case len(s.WasteReasons) < 1 || len(s.WasteReasons) > MaxWasteReasons:
		return fmt.Errorf("%w: waste_reasons needs 1–%d reasons", ErrBadSettings, MaxWasteReasons)
	case len(s.VoidReasons) < 1 || len(s.VoidReasons) > MaxWasteReasons:
		return fmt.Errorf("%w: void_reasons needs 1–%d reasons", ErrBadSettings, MaxWasteReasons)
	}
	for _, r := range s.WasteReasons {
		if r == "" || utf8.RuneCountInString(r) > MaxWasteReasonLen {
			return fmt.Errorf("%w: a waste reason is 1–%d characters", ErrBadSettings, MaxWasteReasonLen)
		}
	}
	for _, r := range s.VoidReasons {
		if r == "" || utf8.RuneCountInString(r) > MaxWasteReasonLen {
			return fmt.Errorf("%w: a void reason is 1–%d characters", ErrBadSettings, MaxWasteReasonLen)
		}
	}
	for _, c := range s.AreaCodes {
		if !ValidAreaCode(c) {
			return fmt.Errorf("%w: area_codes are 3 digits, the first 2–9 (%q)", ErrBadSettings, c)
		}
	}
	for _, p := range s.CardTipPercents {
		if p < 0 || p > 100 {
			return fmt.Errorf("%w: card_tip_percents are 0–100", ErrBadSettings)
		}
	}
	return nil
}

// Normalize: a null tip list / area-code list = the default one; the
// margin keeps one decimal; area codes are trimmed and deduped. (The repository decodes a stored row over DefaultSettings, so
// fields saved before they existed read as their defaults.)
func (s *Settings) Normalize() {
	if s.CardTipPercents == nil {
		s.CardTipPercents = []int{15, 18, 20}
	}
	if s.AreaCodes == nil {
		s.AreaCodes = DefaultAreaCodes()
	} else {
		// Trimmed, repeats dropped (the first one kept), order kept.
		seen := make(map[string]bool, len(s.AreaCodes))
		codes := make([]string, 0, len(s.AreaCodes))
		for _, c := range s.AreaCodes {
			c = strings.TrimSpace(c)
			if !seen[c] {
				seen[c] = true
				codes = append(codes, c)
			}
		}
		s.AreaCodes = codes
	}
	s.OutOfAreaMarginMi = math.Round(s.OutOfAreaMarginMi*10) / 10
	s.WasteReasons = normalizeReasons(s.WasteReasons, DefaultWasteReasons)
	s.VoidReasons = normalizeReasons(s.VoidReasons, DefaultVoidReasons)
	if s.PhoneChargeTimeoutSeconds == 0 {
		s.PhoneChargeTimeoutSeconds = DefaultPhoneChargeTimeoutS
	}
	if s.CallcenterIdleLockMinutes == 0 {
		s.CallcenterIdleLockMinutes = DefaultCallcenterIdleLock
	}
}

// normalizeReasons: nil = the defaults; else trimmed, blanks and repeats (any
// case) dropped, order kept (the waste and the void reasons).
func normalizeReasons(list []string, defaults func() []string) []string {
	if list == nil {
		return defaults()
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, r := range list {
		r = strings.Join(strings.Fields(r), " ")
		if r == "" || seen[strings.ToLower(r)] {
			continue
		}
		seen[strings.ToLower(r)] = true
		out = append(out, r)
	}
	return out
}

// ValidHHMM: "" or a 24-hour "HH:MM".
func ValidHHMM(v string) bool {
	if v == "" {
		return true
	}
	t, err := time.Parse("15:04", v)
	return err == nil && len(v) == 5 && t.Format("15:04") == v
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
// A manager is always unlimited: their own limits are ignored (the Pi's rule,
// contract/money-rules.json E07: it never applied them).
func EffectiveLimits(p Person, s Settings) Effective {
	e := RoleDefaults(p.Role, s)
	if p.Role == RoleManager || p.Role == RoleCallcenter {
		return e // a call-center agent: always 0 / 0, their own limits ignored
	}
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
