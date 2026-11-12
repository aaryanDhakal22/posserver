// Package dayclose is the day's close as the Pi reports it (terminal Stage 2,
// contract/main.md §2.5zb): cash drawer sessions per terminal + cashier, their
// pay-ins / pay-outs, and the Z report the Pi builds at Close batch. main only
// stores and serves them (the Pi decides every number); this package checks
// what the floor events carry.
package dayclose

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrBad is a floor event main refuses (answered invalid).
var ErrBad = errors.New("bad day-close event")

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBad, fmt.Sprintf(format, a...))
}

// Bounds.
const (
	MaxCents     = 100_000_000 // $1,000,000: any amount on a drawer
	MaxTextLen   = 80          // device / staff names, ids
	MaxReasonLen = 200
	MaxDenoms    = 30
	MaxZBytes    = 512 * 1024
)

// Close reasons.
const (
	CloseCounted    = "counted"     // counted by a cashier / manager
	CloseHandover   = "handover"    // counted at a cashier change on the same terminal
	CloseBatchClose = "batch_close" // closed by Close batch, not counted (flagged)
)

// Movement kinds.
const (
	PayIn  = "pay_in"
	PayOut = "pay_out"
)

// Denomination is one line of a drawer count.
type Denomination struct {
	ValueCents int64 `json:"value_cents"`
	Count      int64 `json:"count"`
}

// Drawer is one drawer session (a terminal's drawer, one cashier's shift).
type Drawer struct {
	ID               uuid.UUID      `json:"id"`
	DeviceID         string         `json:"device_id"`
	DeviceName       string         `json:"device_name"`
	StaffID          string         `json:"staff_id"`
	StaffName        string         `json:"staff_name"`
	BatchID          *int64         `json:"batch_id"`
	OpenedAt         time.Time      `json:"opened_at"`
	StartCents       int64          `json:"start_cents"`
	HandoverFrom     *uuid.UUID     `json:"handover_from"`
	ClosedAt         *time.Time     `json:"closed_at"`
	ClosedBy         string         `json:"closed_by"`
	CloseReason      string         `json:"close_reason"`
	CountedCents     *int64         `json:"counted_cents"`
	Denominations    []Denomination `json:"denominations"`
	ExpectedCents    *int64         `json:"expected_cents"`
	OverShortCents   *int64         `json:"over_short_cents"`
	Flagged          bool           `json:"flagged"`
	CashSalesCents   int64          `json:"cash_sales_cents"`
	CashRefundsCents int64          `json:"cash_refunds_cents"`
	CashTipsCents    int64          `json:"cash_tips_cents"`
	PayInsCents      int64          `json:"pay_ins_cents"`
	PayOutsCents     int64          `json:"pay_outs_cents"`
	NoSales          int            `json:"no_sales"`
	Payments         int            `json:"payments"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// Movement is a pay-in or a pay-out on a drawer.
type Movement struct {
	ID          uuid.UUID `json:"id"`
	DrawerID    uuid.UUID `json:"drawer_id"`
	Kind        string    `json:"kind"`
	AmountCents int64     `json:"amount_cents"`
	Reason      string    `json:"reason"`
	StaffID     string    `json:"staff_id"`
	ApprovedBy  string    `json:"approved_by"`
	At          time.Time `json:"at"`
}

func cents(name string, v int64) error {
	if v < 0 || v > MaxCents {
		return bad("%s must be 0–%d", name, MaxCents)
	}
	return nil
}

func text(name, v string, max int) error {
	if utf8.RuneCountInString(v) > max {
		return bad("%s is longer than %d", name, max)
	}
	return nil
}

// CheckOpened checks drawer.opened's drawer.
func (d *Drawer) CheckOpened() error {
	if d.ID == uuid.Nil {
		return bad("drawer.id is required")
	}
	if d.OpenedAt.IsZero() {
		return bad("drawer.opened_at is required")
	}
	if d.BatchID != nil && *d.BatchID < 0 {
		return bad("drawer.batch_id must be positive")
	}
	if err := cents("drawer.start_cents", d.StartCents); err != nil {
		return err
	}
	for name, v := range map[string]string{"device_id": d.DeviceID, "device_name": d.DeviceName, "staff_id": d.StaffID, "staff_name": d.StaffName} {
		if err := text("drawer."+name, v, MaxTextLen); err != nil {
			return err
		}
	}
	d.OpenedAt = d.OpenedAt.UTC()
	return nil
}

// CheckClosed checks drawer.closed's drawer (the whole session).
func (d *Drawer) CheckClosed() error {
	if err := d.CheckOpened(); err != nil {
		return err
	}
	if d.ClosedAt == nil || d.ClosedAt.IsZero() {
		return bad("drawer.closed_at is required")
	}
	t := d.ClosedAt.UTC()
	d.ClosedAt = &t
	switch d.CloseReason {
	case CloseCounted, CloseHandover, CloseBatchClose:
	default:
		return bad("drawer.close_reason must be counted, handover or batch_close")
	}
	if err := text("drawer.closed_by", d.ClosedBy, MaxTextLen); err != nil {
		return err
	}
	if d.CountedCents != nil {
		if err := cents("drawer.counted_cents", *d.CountedCents); err != nil {
			return err
		}
	}
	if d.ExpectedCents != nil && (*d.ExpectedCents < -MaxCents || *d.ExpectedCents > MaxCents) {
		return bad("drawer.expected_cents is out of range")
	}
	if d.OverShortCents != nil && (*d.OverShortCents < -MaxCents || *d.OverShortCents > MaxCents) {
		return bad("drawer.over_short_cents is out of range")
	}
	if len(d.Denominations) > MaxDenoms {
		return bad("drawer.denominations: at most %d lines", MaxDenoms)
	}
	for _, x := range d.Denominations {
		if x.ValueCents < 1 || x.ValueCents > 1_000_000 || x.Count < 0 || x.Count > 1_000_000 {
			return bad("drawer.denominations: value_cents 1–1000000, count 0–1000000")
		}
	}
	for name, v := range map[string]int64{"cash_sales_cents": d.CashSalesCents, "cash_refunds_cents": d.CashRefundsCents,
		"cash_tips_cents": d.CashTipsCents, "pay_ins_cents": d.PayInsCents, "pay_outs_cents": d.PayOutsCents} {
		if err := cents("drawer."+name, v); err != nil {
			return err
		}
	}
	if d.NoSales < 0 || d.Payments < 0 {
		return bad("drawer.no_sales / payments must be 0 or more")
	}
	return nil
}

// Check checks drawer.movement's movement.
func (m *Movement) Check() error {
	if m.ID == uuid.Nil || m.DrawerID == uuid.Nil {
		return bad("movement.id and drawer_id are required")
	}
	if m.Kind != PayIn && m.Kind != PayOut {
		return bad("movement.kind must be pay_in or pay_out")
	}
	if m.AmountCents < 1 || m.AmountCents > MaxCents {
		return bad("movement.amount_cents must be 1–%d", MaxCents)
	}
	if m.At.IsZero() {
		return bad("movement.at is required")
	}
	m.At = m.At.UTC()
	m.Reason = strings.TrimSpace(m.Reason)
	if err := text("movement.reason", m.Reason, MaxReasonLen); err != nil {
		return err
	}
	for name, v := range map[string]string{"staff_id": m.StaffID, "approved_by": m.ApprovedBy} {
		if err := text("movement."+name, v, MaxTextLen); err != nil {
			return err
		}
	}
	return nil
}

// Z is the stored Z report: what main indexes it by, plus the report itself
// exactly as the Pi sent it.
type Z struct {
	ID           uuid.UUID
	BatchID      int64
	BusinessDate time.Time // a date (UTC midnight)
	OpenedAt     *time.Time
	ClosedAt     *time.Time
	Report       json.RawMessage
	ReceivedAt   time.Time
	// 00043: the version (a rebuilt Z for the batch counts up), flagged (the
	// Pi's Z or main's check doesn't balance), main's check against the
	// payments it has (M-12).
	Version             int
	Flagged             bool
	MainCheck           json.RawMessage
	MainDifferenceCents *int64
}

// Flag is one money flag (money_flags): main stored the money but doesn't
// agree with it (or couldn't store a payment under its id: a conflict).
type Flag struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	EventID    *uuid.UUID      `json:"event_id"`
	EventType  string          `json:"event_type"`
	Subject    string          `json:"subject"`
	Code       string          `json:"code"`
	Detail     json.RawMessage `json:"detail"`
	ResolvedAt *time.Time      `json:"resolved_at"`
}

// ZVersion is a Z that a rebuilt one for the same batch replaced (M-9).
type ZVersion struct {
	ID           uuid.UUID `json:"id"`
	Version      int       `json:"version"`
	ReceivedAt   time.Time `json:"received_at"`
	SupersededAt time.Time `json:"superseded_at"`
	SupersededBy uuid.UUID `json:"superseded_by"`
}

// Revision is a closed drawer session as it was before a later
// drawer.closed changed it (M-9).
type Revision struct {
	Seq       int64           `json:"seq"`
	Before    json.RawMessage `json:"before"`
	RevisedAt time.Time       `json:"revised_at"`
}

// TenderCheck is one tender of main's Z check: the Pi's net and main's.
type TenderCheck struct {
	Tender    string `json:"tender"`
	PiCents   int64  `json:"pi_cents"`
	MainCents int64  `json:"main_cents"`
}

// Check is main's check of a Z (M-12): the Pi's own difference (reconcile)
// and, per tender, the Pi's net against the counter payments main has for
// the batch. Checked=false when the report lists no tenders.
type Check struct {
	Checked        bool          `json:"checked"`
	Tenders        []TenderCheck `json:"tenders"`
	MainDifference *int64        `json:"main_difference_cents"`
	PiDifference   int64         `json:"pi_difference_cents"`
	Version        int           `json:"-"`
	Flags          []string      `json:"-"`
}

// Matches reports whether main's payments agree with every tender.
func (c Check) Matches() bool {
	for _, t := range c.Tenders {
		if t.PiCents != t.MainCents {
			return false
		}
	}
	return true
}

// Balanced: the Pi's Z balances and main agrees.
func (c Check) Balanced() bool { return c.PiDifference == 0 && c.Matches() }

// NotCounterTenders are the Z's tenders that are no counter payment, so main
// has no payment row to check them against: the cash a driver collected at
// the door (the Pi's "driver_cash": the order's balance, never an
// order.payment) and the money an online order arrived paid with ("online").
// They stay on the Z; main's check leaves them out (TERMINAL-FLOWS PAY-22 day:
// every batch with a cash delivery was flagged z_mismatch by its driver cash).
var NotCounterTenders = map[string]bool{"driver_cash": true, "online": true}

// CheckZ compares a Z report's tenders (net_cents) with main's net per
// method (Σ amount + tip of the batch's counter payments). The tenders in
// NotCounterTenders are not compared.
func CheckZ(report json.RawMessage, mainNet map[string]int64) Check {
	var r struct {
		Tenders *[]struct {
			Tender   string `json:"tender"`
			NetCents int64  `json:"net_cents"`
		} `json:"tenders"`
		Reconcile struct {
			DifferenceCents int64 `json:"difference_cents"`
		} `json:"reconcile"`
	}
	_ = json.Unmarshal(report, &r)
	c := Check{PiDifference: r.Reconcile.DifferenceCents, Tenders: []TenderCheck{}}
	if r.Tenders == nil {
		return c
	}
	c.Checked = true
	seen := map[string]bool{}
	var piSum, mainSum int64
	for _, t := range *r.Tenders {
		if NotCounterTenders[t.Tender] {
			continue
		}
		seen[t.Tender] = true
		c.Tenders = append(c.Tenders, TenderCheck{Tender: t.Tender, PiCents: t.NetCents, MainCents: mainNet[t.Tender]})
		piSum += t.NetCents
	}
	for m, n := range mainNet {
		mainSum += n
		if !seen[m] && n != 0 {
			c.Tenders = append(c.Tenders, TenderCheck{Tender: m, MainCents: n})
		}
	}
	sort.Slice(c.Tenders, func(i, j int) bool { return c.Tenders[i].Tender < c.Tenders[j].Tender })
	d := mainSum - piSum
	c.MainDifference = &d
	return c
}

// ParseZ checks z.report's report: an object with id, batch_id and
// business_date (YYYY-MM-DD); opened_at / closed_at when present.
func ParseZ(raw json.RawMessage) (Z, error) {
	if len(raw) == 0 || len(raw) > MaxZBytes {
		return Z{}, bad("report must be an object up to %d bytes", MaxZBytes)
	}
	var head struct {
		ID           string     `json:"id"`
		BatchID      *int64     `json:"batch_id"`
		BusinessDate string     `json:"business_date"`
		OpenedAt     *time.Time `json:"opened_at"`
		ClosedAt     *time.Time `json:"closed_at"`
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return Z{}, bad("report must be a JSON object")
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Z{}, bad("report: %v", err)
	}
	id, err := uuid.Parse(head.ID)
	if err != nil {
		return Z{}, bad("report.id must be a uuid")
	}
	if head.BatchID == nil || *head.BatchID < 1 {
		return Z{}, bad("report.batch_id is required")
	}
	day, err := ParseDate(head.BusinessDate)
	if err != nil {
		return Z{}, bad("report.business_date must be YYYY-MM-DD")
	}
	return Z{ID: id, BatchID: *head.BatchID, BusinessDate: day, OpenedAt: utc(head.OpenedAt), ClosedAt: utc(head.ClosedAt), Report: raw}, nil
}

func utc(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// ParseDate parses a YYYY-MM-DD date.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil || t.Format(time.DateOnly) != s {
		return time.Time{}, errors.New("not a YYYY-MM-DD date")
	}
	return t, nil
}

// Summary is one Z report in a list (GET /store/v1/me/reports/z).
type Summary struct {
	ID              uuid.UUID  `json:"id"`
	BatchID         int64      `json:"batch_id"`
	BusinessDate    string     `json:"business_date"`
	OpenedAt        *time.Time `json:"opened_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	CloseKind       string     `json:"close_kind"`
	TotalCents      int64      `json:"total_cents"`
	Orders          int64      `json:"orders"`
	OverShortCents  int64      `json:"over_short_cents"`
	DifferenceCents int64      `json:"difference_cents"`
	ReceivedAt      time.Time  `json:"received_at"`
}

// SummaryOf reads a stored Z's list fields (missing ones are 0 / "").
func SummaryOf(z Z) Summary {
	var r struct {
		CloseKind string `json:"close_kind"`
		Counts    struct {
			Orders int64 `json:"orders"`
		} `json:"counts"`
		Sales struct {
			TotalCents int64 `json:"total_cents"`
		} `json:"sales"`
		OverShortCents int64 `json:"over_short_cents"`
		Reconcile      struct {
			DifferenceCents int64 `json:"difference_cents"`
		} `json:"reconcile"`
	}
	_ = json.Unmarshal(z.Report, &r)
	return Summary{ID: z.ID, BatchID: z.BatchID, BusinessDate: z.BusinessDate.Format(time.DateOnly), OpenedAt: z.OpenedAt,
		ClosedAt: z.ClosedAt, CloseKind: r.CloseKind, TotalCents: r.Sales.TotalCents, Orders: r.Counts.Orders,
		OverShortCents: r.OverShortCents, DifferenceCents: r.Reconcile.DifferenceCents, ReceivedAt: z.ReceivedAt}
}
