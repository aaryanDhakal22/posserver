// Package dayclose stores the day's close as the Pi reports it (terminal
// Stage 2): the floor events drawer.opened / drawer.movement / drawer.closed
// and z.report, and serves them to the store's manager
// (/store/v1/me/drawers*, /store/v1/me/reports/z*). main never computes a
// drawer or a Z itself: the Pi decides every number.
package dayclose

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"quiccpos/main/internal/domain/dayclose"
	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Floor event types.
const (
	EventDrawerOpened   = "drawer.opened"
	EventDrawerMovement = "drawer.movement"
	EventDrawerClosed   = "drawer.closed"
	EventZReport        = "z.report"
)

// Repo is the storage (repositories.DayCloseRepository).
type Repo interface {
	OpenDrawer(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, d dayclose.Drawer) (string, error)
	CloseDrawer(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, d dayclose.Drawer) (string, error)
	AddMovement(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, m dayclose.Movement) (string, error)
	SaveZ(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, z dayclose.Z) (string, error)
	ListZ(ctx context.Context, locationID uuid.UUID, date *time.Time, limit int) ([]dayclose.Z, error)
	GetZ(ctx context.Context, locationID, id uuid.UUID) (dayclose.Z, error)
	ListDrawers(ctx context.Context, locationID uuid.UUID, date *time.Time, limit int) ([]dayclose.Drawer, error)
	GetDrawer(ctx context.Context, locationID, id uuid.UUID) (dayclose.Drawer, []dayclose.Movement, error)
}

// ErrNotFound: the store has no such drawer session / Z report.
var ErrNotFound = errors.New("not found in this store")

// List sizes.
const (
	ZListDefault      = 30
	DrawerListDefault = 50
)

type Service struct {
	repo     Repo
	notFound func(error) bool
	logger   zerolog.Logger
}

// NewService: notFound tells the repository's "no row" error apart.
func NewService(repo Repo, notFound func(error) bool, lg zerolog.Logger) *Service {
	return &Service{repo: repo, notFound: notFound, logger: lg.With().Str("module", "dayclose").Logger()}
}

// Event is the part of a floor event this package reads.
type Event struct {
	Type     string
	Drawer   json.RawMessage
	Movement json.RawMessage
	Report   json.RawMessage
}

// Refused is a day-close event main refuses (answer it invalid): the
// caller marks it seen. Any other error is infrastructure.
type Refused struct{ Err error }

func (r *Refused) Error() string { return r.Err.Error() }
func (r *Refused) Unwrap() error { return r.Err }

func refused(err error) (string, error) { return order.FloorInvalid, &Refused{Err: err} }

// Apply applies one day-close floor event: its result, a *Refused (answer
// invalid) or an infrastructure error.
func (s *Service) Apply(ctx context.Context, sc order.Scope, eventID uuid.UUID, ev Event) (string, error) {
	switch ev.Type {
	case EventDrawerOpened, EventDrawerClosed:
		var d dayclose.Drawer
		if len(ev.Drawer) == 0 {
			return refused(errors.New("drawer is required"))
		}
		if err := json.Unmarshal(ev.Drawer, &d); err != nil {
			return refused(err)
		}
		if ev.Type == EventDrawerOpened {
			if err := d.CheckOpened(); err != nil {
				return refused(err)
			}
			return s.repo.OpenDrawer(ctx, sc, eventID, ev.Type, d)
		}
		if err := d.CheckClosed(); err != nil {
			return refused(err)
		}
		res, err := s.repo.CloseDrawer(ctx, sc, eventID, ev.Type, d)
		if err == nil && res == order.FloorApplied {
			e := s.logger.Info().Ctx(ctx).Str("drawer_id", d.ID.String()).Str("reason", d.CloseReason).Bool("flagged", d.Flagged)
			if d.OverShortCents != nil {
				e = e.Int64("over_short_cents", *d.OverShortCents)
			}
			e.Msg("drawer closed")
		}
		return res, err
	case EventDrawerMovement:
		var m dayclose.Movement
		if len(ev.Movement) == 0 {
			return refused(errors.New("movement is required"))
		}
		if err := json.Unmarshal(ev.Movement, &m); err != nil {
			return refused(err)
		}
		if err := m.Check(); err != nil {
			return refused(err)
		}
		return s.repo.AddMovement(ctx, sc, eventID, ev.Type, m)
	case EventZReport:
		z, err := dayclose.ParseZ(ev.Report)
		if err != nil {
			return refused(err)
		}
		res, err := s.repo.SaveZ(ctx, sc, eventID, ev.Type, z)
		if err == nil && res == order.FloorApplied {
			sum := dayclose.SummaryOf(z)
			s.logger.Info().Ctx(ctx).Int64("batch_id", z.BatchID).Str("business_date", sum.BusinessDate).
				Int64("total_cents", sum.TotalCents).Int64("difference_cents", sum.DifferenceCents).Msg("Z report stored")
		}
		return res, err
	}
	return refused(errors.New("not a day-close event"))
}

// Handles reports whether the type is one of this package's events.
func Handles(typ string) bool {
	switch typ {
	case EventDrawerOpened, EventDrawerMovement, EventDrawerClosed, EventZReport:
		return true
	}
	return false
}

// ZReports lists the store's Z reports for a business date (nil = the
// newest ZListDefault).
func (s *Service) ZReports(ctx context.Context, locationID uuid.UUID, date *time.Time) ([]dayclose.Summary, error) {
	limit := ZListDefault
	if date != nil {
		limit = 100
	}
	zs, err := s.repo.ListZ(ctx, locationID, date, limit)
	if err != nil {
		return nil, err
	}
	out := make([]dayclose.Summary, 0, len(zs))
	for _, z := range zs {
		out = append(out, dayclose.SummaryOf(z))
	}
	return out, nil
}

// ZReport is one of the store's Z reports.
func (s *Service) ZReport(ctx context.Context, locationID, id uuid.UUID) (dayclose.Z, error) {
	z, err := s.repo.GetZ(ctx, locationID, id)
	if err != nil && s.notFound(err) {
		return z, ErrNotFound
	}
	return z, err
}

// Drawers lists the store's drawer sessions opened on a store-local date
// (nil = the newest DrawerListDefault).
func (s *Service) Drawers(ctx context.Context, locationID uuid.UUID, date *time.Time) ([]dayclose.Drawer, error) {
	limit := DrawerListDefault
	if date != nil {
		limit = 200
	}
	return s.repo.ListDrawers(ctx, locationID, date, limit)
}

// Drawer is one drawer session with its pay-ins / pay-outs.
func (s *Service) Drawer(ctx context.Context, locationID, id uuid.UUID) (dayclose.Drawer, []dayclose.Movement, error) {
	d, m, err := s.repo.GetDrawer(ctx, locationID, id)
	if err != nil && s.notFound(err) {
		return d, nil, ErrNotFound
	}
	return d, m, err
}
