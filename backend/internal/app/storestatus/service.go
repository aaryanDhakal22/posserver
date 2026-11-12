// Package storestatusApp is the use-case layer of a store's ordering status
// (main 3.0 step 3): the manager's ordering settings, pause and dated hours
// exceptions; the status every channel reads (open / paused / closed, order
// types + ETAs, the sold-out list, scheduling) and the times an order can be
// promised for; the delivery check; and the loop that brings 86'd items
// back at each location's daily reset hour.
package storestatusApp

import (
	"context"
	"errors"
	"strings"
	"time"

	"quiccpos/main/internal/app/geo"
	"quiccpos/main/internal/domain/storestatus"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

var (
	ErrNotFound    = errors.New("not found in this location")
	ErrNoteTooLong = errors.New("the pause note is limited to 140 characters")
	ErrBadAddress  = errors.New("an address needs a street line and a zip or a city")
)

// Repo is the status store (repositories.StoreStatusRepository).
type Repo interface {
	Settings(ctx context.Context, tenantID, locationID uuid.UUID) (storestatus.Settings, error)
	UpdateSettings(ctx context.Context, locationID uuid.UUID, s storestatus.Settings) (storestatus.Settings, error)
	SetPause(ctx context.Context, locationID uuid.UUID, at time.Time, until *time.Time, note string) (storestatus.Settings, error)
	ClearPause(ctx context.Context, locationID uuid.UUID) (storestatus.Settings, error)
	Exceptions(ctx context.Context, locationID uuid.UUID, from time.Time) ([]storestatus.Exception, error)
	SetException(ctx context.Context, tenantID, locationID uuid.UUID, e storestatus.Exception) (storestatus.Exception, error)
	DeleteException(ctx context.Context, locationID, id uuid.UUID) (bool, error)
	SoldOut(ctx context.Context, locationID uuid.UUID) ([]storestatus.SoldOutItem, error)
	MenuVersion(ctx context.Context, locationID uuid.UUID) (int64, error)
	EnsureAll(ctx context.Context, locs []tenancy.Location) error
	RestockRows(ctx context.Context) ([]repositories.RestockRow, error)
	Restock(ctx context.Context, locationID uuid.UUID, at time.Time) (int64, error)
	ExpireOutUntil(ctx context.Context) (int64, error)
}

// Locations reads locations (the tenancy service).
type Locations interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
	ListLocations(ctx context.Context) ([]tenancy.Location, error)
}

// AddressChecker geocodes an address against a store's delivery radius (geo).
type AddressChecker interface {
	CheckAddress(ctx context.Context, tenantID, locationID uuid.UUID, a geo.Address) (geo.AddressCheck, error)
}

type Service struct {
	repo   Repo
	locs   Locations
	geo    AddressChecker
	logger zerolog.Logger
	now    func() time.Time
}

func NewService(repo Repo, locs Locations, checker AddressChecker, lg zerolog.Logger) *Service {
	return &Service{repo: repo, locs: locs, geo: checker, logger: lg.With().Str("module", "storestatus-service").Logger(), now: time.Now}
}

// --- schedule ----------------------------------------------------------------------

func zone(l *tenancy.Location) *time.Location {
	if z, err := time.LoadLocation(l.Timezone); err == nil {
		return z
	}
	return time.UTC
}

// schedule loads a location's weekly hours + exceptions from yesterday on
// (an overnight period may still be running).
func (s *Service) schedule(ctx context.Context, l *tenancy.Location) (storestatus.Schedule, error) {
	loc := zone(l)
	weekly, _, err := tenancy.HoursFromConfig(l.Config)
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Str("location_id", l.ID.String()).Msg("store status: bad opening hours, treated as none")
		weekly = nil
	}
	from := s.now().In(loc).AddDate(0, 0, -1)
	exc, err := s.repo.Exceptions(ctx, l.ID, time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		return storestatus.Schedule{}, err
	}
	sch := storestatus.Schedule{Weekly: weekly, Loc: loc, Exceptions: map[string]storestatus.Exception{}}
	for _, e := range exc {
		sch.Exceptions[e.Date] = e
	}
	return sch, nil
}

func (s *Service) location(ctx context.Context, tenantID, locationID uuid.UUID) (*tenancy.Location, error) {
	l, err := s.locs.GetLocation(ctx, locationID)
	if err != nil || l == nil || l.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return l, nil
}

// --- the manager's settings ---------------------------------------------------------

func (s *Service) Settings(ctx context.Context, tenantID, locationID uuid.UUID) (storestatus.Settings, error) {
	return s.repo.Settings(ctx, tenantID, locationID)
}

// TipCap is the store's website tip cap (M-20).
func (s *Service) TipCap(ctx context.Context, tenantID, locationID uuid.UUID) (int64, error) {
	st, err := s.repo.Settings(ctx, tenantID, locationID)
	if err != nil {
		return 0, err
	}
	return st.TipMaxCents, nil
}

// UpdateSettings saves the order types, ETAs and scheduling days (the pause
// is its own call).
func (s *Service) UpdateSettings(ctx context.Context, tenantID, locationID uuid.UUID, in storestatus.Settings) (storestatus.Settings, error) {
	if err := in.Validate(); err != nil {
		return storestatus.Settings{}, err
	}
	if _, err := s.repo.Settings(ctx, tenantID, locationID); err != nil {
		return storestatus.Settings{}, err
	}
	return s.repo.UpdateSettings(ctx, locationID, in)
}

// Pause stops ASAP orders through every channel until the preset's end (15m,
// 30m, 60m, today, indefinite). A note (≤ 140) is shown to customers.
func (s *Service) Pause(ctx context.Context, tenantID, locationID uuid.UUID, preset, note string) (storestatus.Settings, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 140 {
		return storestatus.Settings{}, ErrNoteTooLong
	}
	l, err := s.location(ctx, tenantID, locationID)
	if err != nil {
		return storestatus.Settings{}, err
	}
	sch, err := s.schedule(ctx, l)
	if err != nil {
		return storestatus.Settings{}, err
	}
	now := s.now()
	until, err := sch.PauseUntil(now, preset)
	if err != nil {
		return storestatus.Settings{}, err
	}
	if _, err := s.repo.Settings(ctx, tenantID, locationID); err != nil {
		return storestatus.Settings{}, err
	}
	s.logger.Info().Ctx(ctx).Str("location_id", locationID.String()).Str("preset", preset).Msg("store paused")
	return s.repo.SetPause(ctx, locationID, now, until, note)
}

func (s *Service) Resume(ctx context.Context, tenantID, locationID uuid.UUID) (storestatus.Settings, error) {
	if _, err := s.repo.Settings(ctx, tenantID, locationID); err != nil {
		return storestatus.Settings{}, err
	}
	s.logger.Info().Ctx(ctx).Str("location_id", locationID.String()).Msg("store unpaused")
	return s.repo.ClearPause(ctx, locationID)
}

// Exceptions lists the exceptions from today (store time) on.
func (s *Service) Exceptions(ctx context.Context, tenantID, locationID uuid.UUID) ([]storestatus.Exception, error) {
	l, err := s.location(ctx, tenantID, locationID)
	if err != nil {
		return nil, err
	}
	today := s.now().In(zone(l))
	return s.repo.Exceptions(ctx, locationID, time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC))
}

func (s *Service) SetException(ctx context.Context, tenantID, locationID uuid.UUID, e storestatus.Exception) (storestatus.Exception, error) {
	if err := e.Validate(); err != nil {
		return e, err
	}
	if _, err := s.location(ctx, tenantID, locationID); err != nil {
		return e, err
	}
	return s.repo.SetException(ctx, tenantID, locationID, e)
}

func (s *Service) DeleteException(ctx context.Context, locationID, id uuid.UUID) error {
	ok, err := s.repo.DeleteException(ctx, locationID, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// --- what channels read --------------------------------------------------------------

// StoreInfo is who the store is.
type StoreInfo struct {
	Name         string `json:"name"`
	Timezone     string `json:"timezone"`
	Phone        string `json:"phone"`
	AddressLine1 string `json:"address_line1"`
	AddressLine2 string `json:"address_line2"`
}

// Full is the store status a channel (or the manager's overview) reads.
type Full struct {
	Store StoreInfo `json:"store"`
	storestatus.Status
	SoldOut     []storestatus.SoldOutItem `json:"sold_out"`
	MenuVersion int64                     `json:"menu_version"`
}

func (s *Service) Status(ctx context.Context, tenantID, locationID uuid.UUID) (*Full, error) {
	l, err := s.location(ctx, tenantID, locationID)
	if err != nil {
		return nil, err
	}
	st, err := s.repo.Settings(ctx, tenantID, locationID)
	if err != nil {
		return nil, err
	}
	sch, err := s.schedule(ctx, l)
	if err != nil {
		return nil, err
	}
	sold, err := s.repo.SoldOut(ctx, locationID)
	if err != nil {
		return nil, err
	}
	ver, err := s.repo.MenuVersion(ctx, locationID)
	if err != nil {
		return nil, err
	}
	return &Full{
		Store:  StoreInfo{Name: l.Name, Timezone: l.Timezone, Phone: l.Phone, AddressLine1: l.AddressLine1, AddressLine2: l.AddressLine2},
		Status: storestatus.Compute(s.now(), sch, st, l.DeliveryRadiusM), SoldOut: sold, MenuVersion: ver,
	}, nil
}

// Times lists the times an order of a type can be promised for on a date.
func (s *Service) Times(ctx context.Context, tenantID, locationID uuid.UUID, kind, date string) ([]time.Time, error) {
	l, err := s.location(ctx, tenantID, locationID)
	if err != nil {
		return nil, err
	}
	st, err := s.repo.Settings(ctx, tenantID, locationID)
	if err != nil {
		return nil, err
	}
	sch, err := s.schedule(ctx, l)
	if err != nil {
		return nil, err
	}
	return storestatus.Times(s.now(), sch, st, kind, date)
}

// DeliveryCheck answers whether the store delivers to an address now
// (delivery switched on, and inside the radius).
type DeliveryCheck struct {
	Deliverable bool             `json:"deliverable"`
	Result      string           `json:"result"` // inside | outside | check | not_found | no_store_location | delivery_off
	DistanceM   *int             `json:"distance_m"`
	RadiusM     int              `json:"radius_m"`
	Candidates  []CheckCandidate `json:"candidates"`
}

// CheckCandidate is one of several places an address could be (result check).
type CheckCandidate struct {
	Label      string `json:"label"`
	Line1      string `json:"line1"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
}

func (s *Service) DeliveryCheck(ctx context.Context, tenantID, locationID uuid.UUID, a geo.Address) (*DeliveryCheck, error) {
	a.Line1, a.City, a.State, a.Zip = strings.TrimSpace(a.Line1), strings.TrimSpace(a.City), strings.TrimSpace(a.State), strings.TrimSpace(a.Zip)
	if a.Line1 == "" || (a.Zip == "" && a.City == "") || len(a.Line1)+len(a.City)+len(a.State)+len(a.Zip) > 300 {
		return nil, ErrBadAddress
	}
	st, err := s.repo.Settings(ctx, tenantID, locationID)
	if err != nil {
		return nil, err
	}
	out := &DeliveryCheck{Candidates: []CheckCandidate{}}
	if !st.DeliveryEnabled {
		if l, err := s.location(ctx, tenantID, locationID); err == nil {
			out.RadiusM = l.DeliveryRadiusM
		}
		out.Result = "delivery_off"
		return out, nil
	}
	c, err := s.geo.CheckAddress(ctx, tenantID, locationID, a)
	if err != nil {
		return nil, err
	}
	out.Result, out.RadiusM = c.Result, c.RadiusM
	if c.Point != nil {
		d := c.DistanceM
		out.DistanceM = &d
	}
	for _, k := range c.Candidates {
		out.Candidates = append(out.Candidates, CheckCandidate{Label: k.Label, Line1: k.Line1, City: k.City, State: k.State, PostalCode: k.PostalCode})
	}
	out.Deliverable = c.Result == geo.CheckInside
	return out, nil
}

// --- the restock loop ------------------------------------------------------------------

// RunRestock brings 86'd items back: an item 86'd until a time at that time,
// everything else at the location's daily reset hour. Every minute.
func (s *Service) RunRestock(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.restock(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) restock(ctx context.Context) {
	if n, err := s.repo.ExpireOutUntil(ctx); err != nil {
		s.logger.Warn().Err(err).Msg("restock: out_until")
	} else if n > 0 {
		s.logger.Info().Int64("items", n).Msg("restock: items back at their out-until time")
	}
	locs, err := s.locs.ListLocations(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("restock: locations")
		return
	}
	// A new row starts restocked now: nothing comes back before the next reset.
	if err := s.repo.EnsureAll(ctx, locs); err != nil {
		s.logger.Warn().Err(err).Msg("restock: ordering rows")
		return
	}
	rows, err := s.repo.RestockRows(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("restock: rows")
		return
	}
	now := s.now()
	for _, r := range rows {
		z, err := time.LoadLocation(r.Timezone)
		if err != nil {
			z = time.UTC
		}
		reset := storestatus.LastReset(now, z, r.DailyResetHour)
		if r.RestockedAt != nil && !r.RestockedAt.Before(reset) {
			continue
		}
		n, err := s.repo.Restock(ctx, r.LocationID, now)
		if err != nil {
			s.logger.Warn().Err(err).Str("location_id", r.LocationID.String()).Msg("restock")
			continue
		}
		s.logger.Info().Int64("back", n).Str("location_id", r.LocationID.String()).Msg("restock: daily reset")
	}
}

// RestockNow runs a location's restock at once (dev/tests: the reset hour
// and out-until expiry without waiting).
func (s *Service) RestockNow(ctx context.Context, locationID uuid.UUID) (int64, error) {
	n, err := s.repo.ExpireOutUntil(ctx)
	if err != nil {
		return 0, err
	}
	m, err := s.repo.Restock(ctx, locationID, s.now())
	return n + m, err
}
