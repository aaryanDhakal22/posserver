// Package tracking follows channel orders (main 3.0 step 6): the
// channel-tracking outbox handler turns each change of a keyed channel's
// order into a stream event (GET /channel/v1/events, resumable for 24 h);
// a channel reads one order's status; a customer reads it by the public
// tracking token; and the owner is paged about a channel order the Pi
// hasn't accepted in time (the notification port).
package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// Retention is how long stream events are kept (Last-Event-ID resumes).
const Retention = 24 * time.Hour

// AlertAfter: a channel order not accepted this long after it was placed
// (or after it fell due) pages the owner, once.
const AlertAfter = 10 * time.Minute

// EventOrderStatus is the stream event type.
const EventOrderStatus = "order.status"

var ErrNotFound = errors.New("order not found")

type Repo interface {
	Link(ctx context.Context, orderID uuid.UUID) (repositories.TrackLink, error)
	ByToken(ctx context.Context, token string) (repositories.ChannelOrder, uuid.UUID, uuid.UUID, error)
	InsertEvent(ctx context.Context, channelID, orderID uuid.UUID, typ string, payload []byte) (int64, error)
	EventsAfter(ctx context.Context, channelID uuid.UUID, after int64, limit int) ([]repositories.StreamEvent, error)
	Has(ctx context.Context, channelID uuid.UUID, id int64) (bool, error)
	Latest(ctx context.Context, channelID uuid.UUID) (int64, error)
	Prune(ctx context.Context, before time.Time) (int64, error)
	ToAlert(ctx context.Context, placedBefore, placedAfter, dueBefore time.Time) ([]repositories.AlertRow, error)
	MarkAlerted(ctx context.Context, orderID uuid.UUID) error
}

// Orders reads orders (the order service).
type Orders interface {
	GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error)
}

// Locations reads a store's name + phone (tenancy).
type Locations interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

type Service struct {
	repo     Repo
	orders   Orders
	locs     Locations
	notifier notify.Notifier
	logger   zerolog.Logger
	now      func() time.Time

	mu   sync.Mutex
	subs map[uuid.UUID]map[chan struct{}]struct{} // channel → its live streams
}

// NewService: the outbox handler needs the service before the order service
// and the pager exist (cmd/api), so those come through Wire.
func NewService(repo Repo, lg zerolog.Logger) *Service {
	return &Service{repo: repo, notifier: notify.Fanout{}, logger: lg.With().Str("module", "tracking").Logger(),
		now: time.Now, subs: map[uuid.UUID]map[chan struct{}]struct{}{}}
}

// Wire adds what reads and pages need (once, at startup).
func (s *Service) Wire(orders Orders, locs Locations, notifier notify.Notifier) {
	s.orders, s.locs, s.notifier = orders, locs, notifier
}

// Driver is what a customer may know of the driver.
type Driver struct {
	FirstName string `json:"first_name"`
}

// Event is one change of an order, as a channel's stream carries it.
type Event struct {
	OrderID          string     `json:"order_id"`
	ClientOrderID    string     `json:"client_order_id"`
	Status           string     `json:"status"`
	KitchenNumber    *int       `json:"kitchen_number"`
	EstimatedReadyAt *time.Time `json:"estimated_ready_at"`
	Driver           *Driver    `json:"driver"`
	At               time.Time  `json:"at"`
}

func driverOf(status, name string) *Driver {
	if (status == "on_delivery" || status == "completed") && order.FirstName(name) != "" {
		return &Driver{FirstName: order.FirstName(name)}
	}
	return nil
}

// Record stores a stream event for a keyed channel's order (the outbox
// handler); other orders are ignored. Idempotent enough: a replayed outbox
// row adds the same state again, which a channel treats as no change.
func (s *Service) Record(ctx context.Context, ev order.DomainEvent) error {
	var snap order.Snapshot
	switch ev.Type {
	case order.EventOrderReceived:
		if err := json.Unmarshal(ev.Payload, &snap); err != nil {
			return fmt.Errorf("decode snapshot: %w", err)
		}
	case order.EventOrderStatusChanged:
		var p order.StatusChangedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("decode status change: %w", err)
		}
		snap = p.Snapshot
	default:
		return nil
	}
	id, err := uuid.Parse(snap.ID)
	if err != nil {
		return nil
	}
	link, err := s.repo.Link(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // not a keyed channel's order
	}
	if err != nil {
		return err
	}
	st := order.Status(snap.Status).Public()
	payload, err := json.Marshal(Event{OrderID: snap.ID, ClientOrderID: link.ClientOrderID, Status: st, KitchenNumber: snap.KitchenNumber,
		EstimatedReadyAt: snap.EstimatedReadyAt, Driver: driverOf(st, snap.DriverName), At: ev.OccurredAt.UTC()})
	if err != nil {
		return err
	}
	if _, err := s.repo.InsertEvent(ctx, link.ChannelID, id, EventOrderStatus, payload); err != nil {
		return err
	}
	s.wake(link.ChannelID)
	return nil
}

// Subscribe registers a live stream of a channel: the returned channel gets
// a signal whenever the channel has a new event (call cancel when done).
func (s *Service) Subscribe(channelID uuid.UUID) (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	s.mu.Lock()
	if s.subs[channelID] == nil {
		s.subs[channelID] = map[chan struct{}]struct{}{}
	}
	s.subs[channelID][c] = struct{}{}
	s.mu.Unlock()
	return c, func() {
		s.mu.Lock()
		delete(s.subs[channelID], c)
		if len(s.subs[channelID]) == 0 {
			delete(s.subs, channelID)
		}
		s.mu.Unlock()
	}
}

func (s *Service) wake(channelID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.subs[channelID] {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// EventsAfter reads a channel's events after an id. reset = that id is no
// longer kept (older than Retention, or not the channel's): the channel
// should re-read its orders, then follow the stream from here.
func (s *Service) EventsAfter(ctx context.Context, channelID uuid.UUID, after int64, limit int) ([]repositories.StreamEvent, bool, error) {
	if after > 0 {
		ok, err := s.repo.Has(ctx, channelID, after)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, true, nil
		}
	}
	evs, err := s.repo.EventsAfter(ctx, channelID, after, limit)
	return evs, false, err
}

// Latest is the channel's newest event id: where a fresh stream starts.
func (s *Service) Latest(ctx context.Context, channelID uuid.UUID) (int64, error) {
	return s.repo.Latest(ctx, channelID)
}

// Step is one status an order went through.
type Step struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

// Status is an order's tracking state.
type Status struct {
	OrderID          string     `json:"order_id"`
	ClientOrderID    string     `json:"client_order_id,omitempty"`
	Status           string     `json:"status"`
	Type             string     `json:"type"`
	PlacedAt         time.Time  `json:"placed_at"`
	PromisedAt       *time.Time `json:"promised_at"`
	KitchenNumber    *int       `json:"kitchen_number"`
	EstimatedReadyAt *time.Time `json:"estimated_ready_at"`
	Driver           *Driver    `json:"driver"`
	Steps            []Step     `json:"steps"`
}

func statusOf(o *order.Order) Status {
	st := o.Status().Public()
	out := Status{OrderID: uuid.UUID(o.ID()).String(), Status: st, Type: string(o.Fulfillment().Type), PlacedAt: o.PlacedAt(),
		PromisedAt: o.Fulfillment().PromisedAt, KitchenNumber: o.KitchenNumber(), EstimatedReadyAt: o.EstimatedReadyAt(),
		Driver: driverOf(st, o.DriverName()), Steps: []Step{}}
	for _, h := range o.StatusHistory() {
		p := h.To.Public()
		if n := len(out.Steps); n > 0 && out.Steps[n-1].Status == p {
			continue
		}
		out.Steps = append(out.Steps, Step{Status: p, At: h.At})
	}
	return out
}

// ChannelStatus is one of a channel's orders' tracking state.
func (s *Service) ChannelStatus(ctx context.Context, channelID, tenantID, locationID, orderID uuid.UUID) (*Status, error) {
	link, err := s.repo.Link(ctx, orderID)
	if err != nil || link.ChannelID != channelID {
		return nil, ErrNotFound
	}
	o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(tenantID), LocationID: order.LocationID(locationID)}, order.ID(orderID))
	if err != nil {
		return nil, ErrNotFound
	}
	st := statusOf(o)
	st.ClientOrderID = link.ClientOrderID
	return &st, nil
}

// Public is what a customer's tracking link shows.
type Public struct {
	Status
	Store struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	} `json:"store"`
}

// ByToken is an order's public tracking state.
func (s *Service) ByToken(ctx context.Context, token string) (*Public, error) {
	if len(token) < 20 || len(token) > 64 {
		return nil, ErrNotFound
	}
	link, tenantID, locationID, err := s.repo.ByToken(ctx, token)
	if err != nil {
		return nil, ErrNotFound
	}
	o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(tenantID), LocationID: order.LocationID(locationID)}, order.ID(link.OrderID))
	if err != nil {
		return nil, ErrNotFound
	}
	out := &Public{Status: statusOf(o)}
	if l, err := s.locs.GetLocation(ctx, locationID); err == nil && l != nil {
		out.Store.Name, out.Store.Phone = l.Name, l.Phone
	}
	return out, nil
}

// RunMaintenance prunes old stream events and pages the owner about channel
// orders the Pi hasn't accepted. Every minute.
func (s *Service) RunMaintenance(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) maintain(ctx context.Context) {
	now := s.now()
	if n, err := s.repo.Prune(ctx, now.Add(-Retention)); err != nil {
		s.logger.Warn().Err(err).Msg("prune channel events")
	} else if n > 0 {
		s.logger.Debug().Int64("pruned", n).Msg("channel events pruned")
	}
	s.alert(ctx, now)
}

// alert pages once per channel order still not accepted AlertAfter after it
// was placed (an ASAP order) or after it fell due (a later one), within 6 h.
func (s *Service) alert(ctx context.Context, now time.Time) {
	rows, err := s.repo.ToAlert(ctx, now.Add(-AlertAfter), now.Add(-6*time.Hour), now.Add(-AlertAfter))
	if err != nil {
		s.logger.Warn().Err(err).Msg("channel orders to alert")
		return
	}
	for _, r := range rows {
		msg := fmt.Sprintf("A %s order for %s hasn't been accepted on the tablet for %d minutes. Check the tablet and the Pi.",
			r.ChannelName, order.FirstName(r.Customer), int(AlertAfter.Minutes()))
		if err := s.notifier.Notify(ctx, notify.Notice{LocationID: r.LocationID, Kind: notify.KindOrderNotAccepted,
			Title: "Order waiting", Message: msg}); err != nil && !errors.Is(err, notify.ErrNotConfigured) {
			s.logger.Warn().Err(err).Str("order_id", r.OrderID.String()).Msg("page: order not accepted")
			continue // try again next minute
		}
		if err := s.repo.MarkAlerted(ctx, r.OrderID); err != nil {
			s.logger.Warn().Err(err).Msg("mark channel order alerted")
		}
	}
}
