// Package storestatus decides whether a store takes orders right now and
// when it can take them later (main 3.0 step 3): the weekly opening hours
// with dated exceptions, the manager's pause, which order types are on, the
// ready-time estimates and how far ahead orders may be scheduled. Pure: the
// app layer loads the inputs and adds the store's identity, sold-out list and
// menu version.
package storestatus

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
)

// Order types.
const (
	Pickup   = "pickup"
	Delivery = "delivery"
)

// States of a store.
const (
	StateOpen   = "open"
	StatePaused = "paused"
	StateClosed = "closed"
)

// SlotMinutes is the spacing of scheduled order times.
const SlotMinutes = 15

// lookAhead bounds the search for the next opening.
const lookAhead = 14

var (
	ErrBadSettings  = errors.New("eta minutes must be 0..240 and schedule days 0..30")
	ErrBadType      = errors.New("type must be pickup or delivery")
	ErrTypeOff      = errors.New("that order type is off")
	ErrScheduleOff  = errors.New("this store takes ASAP orders only")
	ErrBadDate      = errors.New("date must be YYYY-MM-DD within the scheduling window")
	ErrBadPause     = errors.New("pause must be one of: 15m, 30m, 60m, today, indefinite")
	ErrBadException = errors.New("an exception needs a date and, unless closed, valid hours")
)

// Settings are a location's ordering settings, pause included.
type Settings struct {
	PickupEnabled      bool       `json:"pickup_enabled"`
	DeliveryEnabled    bool       `json:"delivery_enabled"`
	PickupETAMinutes   int        `json:"pickup_eta_minutes"`
	DeliveryETAMinutes int        `json:"delivery_eta_minutes"`
	ScheduleDays       int        `json:"schedule_days"`
	PausedAt           *time.Time `json:"paused_at"`
	PausedUntil        *time.Time `json:"paused_until"`
	PauseNote          string     `json:"pause_note"`
}

// Validate checks the editable numbers.
func (s Settings) Validate() error {
	if s.PickupETAMinutes < 0 || s.PickupETAMinutes > 240 || s.DeliveryETAMinutes < 0 || s.DeliveryETAMinutes > 240 ||
		s.ScheduleDays < 0 || s.ScheduleDays > 30 {
		return ErrBadSettings
	}
	return nil
}

// Paused reports whether the pause holds at now.
func (s Settings) Paused(now time.Time) bool {
	return s.PausedAt != nil && (s.PausedUntil == nil || now.Before(*s.PausedUntil))
}

// ETA is an order type's estimate in minutes.
func (s Settings) ETA(kind string) int {
	if kind == Delivery {
		return s.DeliveryETAMinutes
	}
	return s.PickupETAMinutes
}

// Enabled reports whether an order type is on.
func (s Settings) Enabled(kind string) bool {
	if kind == Delivery {
		return s.DeliveryEnabled
	}
	return s.PickupEnabled
}

// Exception is one date that is closed or keeps its own hours.
type Exception struct {
	ID     uuid.UUID            `json:"id"`
	Date   string               `json:"date"` // YYYY-MM-DD, the store's local date
	Closed bool                 `json:"closed"`
	Hours  []tenancy.HoursRange `json:"hours"`
	Note   string               `json:"note"`
}

// Validate checks the date and (unless closed) the hours.
func (e *Exception) Validate() error {
	if _, err := time.Parse(time.DateOnly, e.Date); err != nil {
		return ErrBadException
	}
	if e.Closed {
		e.Hours = []tenancy.HoursRange{}
		return nil
	}
	if len(e.Hours) == 0 {
		return ErrBadException
	}
	if err := (tenancy.OpeningHours{"mon": e.Hours}).Validate(); err != nil {
		return fmt.Errorf("%w: %s", ErrBadException, strings.TrimPrefix(err.Error(), "opening_hours mon: "))
	}
	e.Note = strings.TrimSpace(e.Note)
	return nil
}

// Schedule is a store's hours: weekly (nil = none set: always open) plus
// dated exceptions, in the store's zone.
type Schedule struct {
	Weekly     tenancy.OpeningHours
	Exceptions map[string]Exception // by date
	Loc        *time.Location
}

// Period is one open stretch.
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// HoursSet reports whether the store has any hours (else it is always open).
// (Every day switched off saves as seven empty days: no hours, not closed for good.)
func (s Schedule) HoursSet() bool {
	for _, r := range s.Weekly {
		if len(r) > 0 {
			return true
		}
	}
	return false
}

func (s Schedule) zone() *time.Location {
	if s.Loc == nil {
		return time.UTC
	}
	return s.Loc
}

func hhmm(v string) int {
	t, _ := time.Parse("15:04", strings.TrimSpace(v))
	return t.Hour()*60 + t.Minute()
}

// DayHours are the ranges a local date opens with (its exception's, else
// the weekday's) and the exception, if any.
func (s Schedule) DayHours(day time.Time) ([]tenancy.HoursRange, *Exception) {
	if e, ok := s.Exceptions[day.Format(time.DateOnly)]; ok {
		if e.Closed {
			return nil, &e
		}
		return e.Hours, &e
	}
	return s.Weekly[tenancy.Weekdays[(int(day.Weekday())+6)%7]], nil
}

// Periods are the open stretches that start on a local date (a close at or
// before the open runs past midnight). Times are wall-clock times, so a
// daylight-saving day keeps its hours. Without weekly hours a date is open
// all day unless its exception says otherwise.
func (s Schedule) Periods(day time.Time) []Period {
	loc := s.zone()
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	ranges, exc := s.DayHours(d)
	if !s.HoursSet() && exc == nil {
		return []Period{{Start: d, End: d.AddDate(0, 0, 1)}}
	}
	out := make([]Period, 0, len(ranges))
	for _, r := range ranges {
		o, c := hhmm(r.Open), hhmm(r.Close)
		start := time.Date(d.Year(), d.Month(), d.Day(), o/60, o%60, 0, 0, loc)
		end := time.Date(d.Year(), d.Month(), d.Day(), c/60, c%60, 0, 0, loc)
		if c <= o {
			end = time.Date(d.Year(), d.Month(), d.Day()+1, c/60, c%60, 0, 0, loc)
		}
		out = append(out, Period{Start: start, End: end})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// periodsAround lists the periods starting from the day before t's date to
// `days` days after, in order.
func (s Schedule) periodsAround(t time.Time, days int) []Period {
	local := t.In(s.zone())
	var out []Period
	for i := -1; i <= days; i++ {
		out = append(out, s.Periods(local.AddDate(0, 0, i))...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// Current is the open period holding t (open = at or after its start, before its end).
func (s Schedule) Current(t time.Time) (Period, bool) {
	for _, p := range s.periodsAround(t, 0) {
		if !t.Before(p.Start) && t.Before(p.End) {
			return p, true
		}
	}
	return Period{}, false
}

// OpenAt reports whether the store is open at t.
func (s Schedule) OpenAt(t time.Time) bool {
	_, ok := s.Current(t)
	return ok
}

// NextOpen is t when open then, else the next period's start (nil when none
// in the next two weeks).
func (s Schedule) NextOpen(t time.Time) *time.Time {
	if s.OpenAt(t) {
		return &t
	}
	for _, p := range s.periodsAround(t, lookAhead) {
		if p.Start.After(t) {
			start := p.Start
			return &start
		}
	}
	return nil
}

// ClosesAt is when the open stretch holding t ends (back-to-back periods
// count as one), nil when closed or open all day without hours.
func (s Schedule) ClosesAt(t time.Time) *time.Time {
	p, ok := s.Current(t)
	if !ok || !s.HoursSet() {
		return nil
	}
	end := p.End
	for _, q := range s.periodsAround(end, 1) {
		if !q.Start.After(end) && q.End.After(end) {
			end = q.End
		}
	}
	return &end
}

// PauseUntil is when a pause preset ends (nil = until someone unpauses):
// 15m / 30m / 60m from now; "today" = the end of the last open stretch that
// starts today (overnight hours included), else the local midnight.
func (s Schedule) PauseUntil(now time.Time, preset string) (*time.Time, error) {
	var d time.Duration
	switch preset {
	case "15m":
		d = 15 * time.Minute
	case "30m":
		d = 30 * time.Minute
	case "60m":
		d = time.Hour
	case "indefinite":
		return nil, nil
	case "today":
		local := now.In(s.zone())
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.zone())
		// After midnight inside last night's stretch, "today" is that shift:
		// it ends when the stretch does, not after tonight's service.
		if cur, ok := s.Current(now); ok && cur.Start.Before(midnight) && s.HoursSet() {
			return s.ClosesAt(now), nil
		}
		end := midnight.AddDate(0, 0, 1)
		for _, p := range s.Periods(local) { // a period past midnight stretches "today"
			if p.End.After(end) {
				end = p.End
			}
		}
		return &end, nil
	default:
		return nil, ErrBadPause
	}
	t := now.Add(d)
	return &t, nil
}

// Status is a store's ordering status at one moment.
type Status struct {
	State      string     `json:"state"`     // open | paused | closed
	Accepting  bool       `json:"accepting"` // ASAP orders now (open, not paused, a type on)
	OpenNow    bool       `json:"open_now"`  // inside the hours (pause aside)
	HoursSet   bool       `json:"hours_set"`
	Pause      *PauseInfo `json:"pause"`
	NextOpenAt *time.Time `json:"next_open_at"` // when ASAP orders are next taken (null = open now, or unknown)
	ClosesAt   *time.Time `json:"closes_at"`
	Today      Today      `json:"today"`
	OrderTypes OrderTypes `json:"order_types"`
	Scheduling Scheduling `json:"scheduling"`
}

type PauseInfo struct {
	Since time.Time  `json:"since"`
	Until *time.Time `json:"until"` // null = until the store unpauses
	Note  string     `json:"note"`
}

type Today struct {
	Date   string               `json:"date"`
	Closed bool                 `json:"closed"`
	Hours  []tenancy.HoursRange `json:"hours"`
	Note   string               `json:"note"` // the date exception's note
}

type OrderTypes struct {
	Pickup   TypeStatus `json:"pickup"`
	Delivery TypeStatus `json:"delivery"`
}

type TypeStatus struct {
	Enabled    bool `json:"enabled"`
	ETAMinutes int  `json:"eta_minutes"`
	RadiusM    int  `json:"radius_m,omitempty"` // delivery only
}

type Scheduling struct {
	DaysAhead   int `json:"days_ahead"` // 0 = ASAP only
	SlotMinutes int `json:"slot_minutes"`
}

// Compute is the status at now.
func Compute(now time.Time, sch Schedule, st Settings, radiusM int) Status {
	local := now.In(sch.zone())
	out := Status{
		OpenNow:  sch.OpenAt(now),
		HoursSet: sch.HoursSet(),
		OrderTypes: OrderTypes{
			Pickup:   TypeStatus{Enabled: st.PickupEnabled, ETAMinutes: st.PickupETAMinutes},
			Delivery: TypeStatus{Enabled: st.DeliveryEnabled, ETAMinutes: st.DeliveryETAMinutes, RadiusM: radiusM},
		},
		Scheduling: Scheduling{DaysAhead: st.ScheduleDays, SlotMinutes: SlotMinutes},
	}
	hours, exc := sch.DayHours(local)
	out.Today = Today{Date: local.Format(time.DateOnly), Hours: hours, Closed: sch.HoursSet() && len(hours) == 0}
	if exc != nil {
		out.Today.Note, out.Today.Closed = exc.Note, exc.Closed || len(hours) == 0
	}
	if out.Today.Hours == nil {
		out.Today.Hours = []tenancy.HoursRange{}
	}
	paused := st.Paused(now)
	if paused {
		out.Pause = &PauseInfo{Since: *st.PausedAt, Until: st.PausedUntil, Note: st.PauseNote}
	}
	switch {
	case paused:
		out.State = StatePaused
	case !out.OpenNow:
		out.State = StateClosed
	default:
		out.State = StateOpen
	}
	out.Accepting = out.State == StateOpen && (st.PickupEnabled || st.DeliveryEnabled)
	if out.OpenNow {
		out.ClosesAt = sch.ClosesAt(now)
	}
	if !out.Accepting {
		switch {
		case paused && st.PausedUntil == nil:
			// until someone unpauses: unknown
		case paused:
			out.NextOpenAt = sch.NextOpen(*st.PausedUntil)
		case out.OpenNow: // open but no order type on
		default:
			out.NextOpenAt = sch.NextOpen(now)
		}
	}
	return out
}

// Times are the times an order of a type can be promised for on a local date
// (ready for pickup / at the door for delivery), every SlotMinutes: the
// kitchen needs the type's ETA after the later of the period's opening, now
// and the pause's end, and the time must fall before the period closes.
func Times(now time.Time, sch Schedule, st Settings, kind, date string) ([]time.Time, error) {
	if kind != Pickup && kind != Delivery {
		return nil, ErrBadType
	}
	if !st.Enabled(kind) {
		return nil, ErrTypeOff
	}
	if st.ScheduleDays == 0 {
		return nil, ErrScheduleOff
	}
	loc := sch.zone()
	day, err := time.ParseInLocation(time.DateOnly, date, loc)
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if err != nil || day.Before(today) || day.After(today.AddDate(0, 0, st.ScheduleDays)) {
		return nil, ErrBadDate
	}
	out := []time.Time{}
	if st.Paused(now) && st.PausedUntil == nil {
		return out, nil
	}
	eta := time.Duration(st.ETA(kind)) * time.Minute
	for _, p := range sch.Periods(day) {
		from := p.Start
		if now.After(from) {
			from = now
		}
		if st.Paused(now) && st.PausedUntil.After(from) {
			from = *st.PausedUntil
		}
		t := ceilSlot(from.Add(eta), loc)
		for !t.After(p.End) {
			out = append(out, t)
			t = t.Add(SlotMinutes * time.Minute)
		}
	}
	return out, nil
}

// ceilSlot rounds up to the next SlotMinutes boundary of the local clock.
func ceilSlot(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	base := time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, loc)
	step := SlotMinutes * time.Minute
	for base.Before(l) {
		base = base.Add(step)
	}
	return base
}

// LastReset is the most recent daily reset instant at or before now (the
// location's daily_reset_hour, local time).
func LastReset(now time.Time, loc *time.Location, hour int) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	l := now.In(loc)
	r := time.Date(l.Year(), l.Month(), l.Day(), hour, 0, 0, 0, loc)
	if r.After(l) {
		r = r.AddDate(0, 0, -1)
	}
	return r
}

// SoldOutItem is an 86'd bank item (Until = when it comes back by itself;
// null = at the next daily reset).
type SoldOutItem struct {
	ItemID uuid.UUID  `json:"item_id"`
	Name   string     `json:"name"`
	Until  *time.Time `json:"until"`
}
