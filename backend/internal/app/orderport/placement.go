package orderport

// Placements (DURABILITY-PLAN.md, steps 1–3): a channel order is recorded
// under its client_order_id before any money moves; the work runs detached
// from the request (a caller hanging up never cancels it); the order row is
// created only once its card is held (or it is cash), in the same transaction
// that marks the placement placed; a sweeper finishes or undoes placements
// whose worker went away.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/domain/payment"
	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Placements stores placements (repositories.PlacementRepository).
type Placements interface {
	Insert(ctx context.Context, p repositories.Placement) (repositories.Placement, bool, error)
	Get(ctx context.Context, channelID uuid.UUID, key string) (repositories.Placement, error)
	Claim(ctx context.Context, p repositories.Placement, worker, cardToken, verificationToken string) (bool, error)
	StartCharge(ctx context.Context, channelID uuid.UUID, key string, cents int64) error
	Finish(ctx context.Context, channelID uuid.UUID, key, state string, result []byte) (bool, error)
	Park(ctx context.Context, channelID uuid.UUID, key, waiting string, result []byte) error
	Stale(ctx context.Context, worker string, before time.Time) ([]repositories.Placement, error)
	ParkedExpired(ctx context.Context) ([]repositories.Placement, error)
	DueAlerts(ctx context.Context) ([]repositories.Placement, error)
	SetDueAlerted(ctx context.Context, channelID uuid.UUID, key string) error
	Prune(ctx context.Context, t time.Time) (int64, error)
}

// Kitchen says whether a store's kitchen (its Pi) can get an order now.
type Kitchen interface {
	Offline(ctx context.Context, tenantID, locationID uuid.UUID) (bool, time.Time, error)
}

// Channels reads a channel (the sweeper resumes placements without a request).
type Channels interface {
	Get(ctx context.Context, id uuid.UUID) (*channelDom.Channel, error)
}

// Customers reads a signed-in customer back (the sweeper).
type Customers interface {
	ByID(ctx context.Context, id uuid.UUID) (customer.Customer, error)
}

// NotPlacedMailer tells a customer their order wasn't placed (and that they
// weren't charged).
type NotPlacedMailer interface {
	NotPlaced(ctx context.Context, locationID uuid.UUID, to, name, why string)
}

var (
	// PlaceWait: how long POST /orders waits for its placement before
	// answering 202 pending (the work goes on).
	PlaceWait = 25 * time.Second
	// WorkFor: a placement's own deadline (it never depends on the caller).
	WorkFor = 2 * time.Minute
	// StaleAfter: a pending placement nobody touched this long is the
	// sweeper's, even when its worker was this main.
	StaleAfter = 2 * time.Minute
	// SweepEvery: the sweeper's tick.
	SweepEvery = 15 * time.Second
	// KeepFor: final placements are kept this long (replays, by-key).
	KeepFor = 30 * 24 * time.Hour
	// ScheduledLead: a scheduled order due at least this far ahead is
	// accepted while the Pi is offline (the owner is paged before it's due).
	ScheduledLead = time.Hour
	// DueCheckBefore: when the Pi is checked for a placed scheduled order.
	DueCheckBefore = 30 * time.Minute
	// KitchenPageEvery: one "kitchen offline" page per store per this long.
	KitchenPageEvery = 10 * time.Minute
)

// Pending: main is still working on the order; ask GET /orders/by-key later.
type Pending struct{ ClientOrderID string }

func (p *Pending) Error() string { return "pending: " + p.ClientOrderID }

var (
	errExpired   = refuse(http.StatusConflict, "expired", "Not placed — you weren't charged.")
	errVoided    = refuse(http.StatusConflict, "voided", "We couldn't finish your order — the card hold was released, you weren't charged.")
	errKeyReused = refuse(http.StatusConflict, "key_reused", "That client_order_id was already used for a different order.")
	errKitchen   = refuse(http.StatusConflict, "kitchen_offline", "The kitchen can't take online orders right now — please call the store.")
)

// PlacementView is GET /channel/v1/orders/by-key/{key}.
type PlacementView struct {
	State     string     `json:"state"` // pending | placed | refused | expired | voided
	Order     *View      `json:"order,omitempty"`
	Error     *ErrorView `json:"error,omitempty"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type ErrorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// stored is a refusal as kept in the placement row.
type stored struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Issues  any    `json:"issues,omitempty"`
}

func resultOf(e *Error) []byte {
	var issues any
	if e.Issues != nil {
		issues = e.Issues
	}
	b, _ := json.Marshal(stored{Status: e.Status, Code: e.Code, Message: e.Message, Issues: issues})
	return b
}

// errorOf is a final placement's answer.
func errorOf(p repositories.Placement) *Error {
	var r stored
	if len(p.Result) > 0 && json.Unmarshal(p.Result, &r) == nil && r.Code != "" {
		e := &Error{Status: r.Status, Code: r.Code, Message: r.Message}
		if r.Issues != nil {
			b, _ := json.Marshal(r.Issues)
			_ = json.Unmarshal(b, &e.Issues)
		}
		return e
	}
	switch p.State {
	case repositories.PlacementExpired:
		return errExpired
	case repositories.PlacementVoided:
		return errVoided
	}
	return refuse(http.StatusConflict, "not_placed", "The order wasn't placed.")
}

// bodyHash fingerprints a request without its card tokens (a retry after 3-D
// Secure carries a new verification token, a re-sent body the same one).
func bodyHash(req Request) string {
	r := req
	r.Payment.Token, r.Payment.VerificationToken = "", ""
	b, _ := json.Marshal(r)
	h := sha256.New()
	h.Write(b)
	if req.Account != nil {
		h.Write([]byte("\x00account:" + req.Account.ID.String()))
	}
	if req.TakenBy != nil {
		h.Write([]byte("\x00operator:" + req.TakenBy.ID.String()))
	}
	return hex.EncodeToString(h.Sum(nil))
}

type outcome struct {
	view    *View
	created bool
	err     error
}

type run struct {
	done chan struct{}
	out  outcome
}

// placementWiring is the durability part of the Service.
type placementWiring struct {
	placements Placements
	kitchen    Kitchen
	channels   Channels
	customers  Customers
	mailer     NotPlacedMailer
	meters     *observability.Meters
	boot       string

	mu        sync.Mutex
	live      map[string]*run
	kitchenAt map[uuid.UUID]kitchenPage
}

type kitchenPage struct {
	at    time.Time
	count int
}

// SetPlacements turns placements on (boot = this process's id, so the sweeper
// can tell another main's work from its own).
func (s *Service) SetPlacements(p Placements, boot string) {
	s.pl.placements, s.pl.boot = p, boot
	s.pl.live = map[string]*run{}
	s.pl.kitchenAt = map[uuid.UUID]kitchenPage{}
}

// SetKitchen plugs in the Pi liveness check (kitchen_offline).
func (s *Service) SetKitchen(k Kitchen) { s.pl.kitchen = k }

// SetSweeper gives the sweeper what it needs to resume a placement.
func (s *Service) SetSweeper(c Channels, cu Customers) { s.pl.channels, s.pl.customers = c, cu }

// SetMailer tells customers about orders that weren't placed.
func (s *Service) SetMailer(m NotPlacedMailer) { s.pl.mailer = m }

// SetMeters counts placements, refusals and sweeps.
func (s *Service) SetMeters(m *observability.Meters) { s.pl.meters = m }

func liveKey(channelID uuid.UUID, key string) string { return channelID.String() + "\x00" + key }

// count adds one to c, labelled with the store (tenant + location slugs, or
// "unrouted" for uuid.Nil) so alerts and dashboards can say which store.
func (s *Service) count(c metric.Int64Counter, tenantID uuid.UUID, attrs ...attribute.KeyValue) {
	if s.pl.meters == nil || c == nil {
		return
	}
	ctx := context.Background()
	c.Add(ctx, 1, observability.WithStore(ctx, tenantID, attrs...))
}

func (s *Service) countPlacement(ch channelDom.Channel, result string) {
	if s.pl.meters != nil {
		s.count(s.pl.meters.OrderPlacements, ch.TenantID, attribute.String("result", result), attribute.String("channel", string(ch.Kind)))
	}
}

func (s *Service) countRefusal(ch channelDom.Channel, code string) {
	if s.pl.meters != nil {
		s.count(s.pl.meters.OrderRefusals, ch.TenantID, attribute.String("reason", code), attribute.String("channel", string(ch.Kind)))
	}
}

// countSweep counts one sweeper action (tenantID uuid.Nil: not one store's, e.g. pruned).
func (s *Service) countSweep(tenantID uuid.UUID, action string) {
	if s.pl.meters != nil {
		s.count(s.pl.meters.PlacementSweeps, tenantID, attribute.String("action", action))
	}
}

// --- the request side -------------------------------------------------------------------

// placeDurable is Place with placements on.
func (s *Service) placeDurable(ctx context.Context, ch channelDom.Channel, req Request) (*View, bool, error) {
	key := req.ClientOrderID
	p := repositories.Placement{ChannelID: ch.ID, ClientOrderID: key, TenantID: ch.TenantID, LocationID: ch.LocationID,
		BodyHash: bodyHash(req), CardToken: req.Payment.Token, VerificationToken: req.Payment.VerificationToken,
		ExpiresAt: req.ExpiresAt, State: repositories.PlacementPending, Worker: s.pl.boot}
	raw := req
	raw.Payment.Token, raw.Payment.VerificationToken = "", ""
	p.Request, _ = json.Marshal(raw)
	if req.Account != nil {
		id := req.Account.ID
		p.CustomerID = &id
	}
	if req.TakenBy != nil {
		p.Operator, _ = json.Marshal(req.TakenBy)
	}
	// An order placed before placements existed answers from its link.
	if _, err := s.pl.placements.Get(ctx, ch.ID, key); errors.Is(err, pgx.ErrNoRows) {
		if v, err := s.byClient(ctx, ch, key); err == nil {
			return v, false, nil
		}
	}
	expired := req.ExpiresAt != nil && s.now().After(*req.ExpiresAt)
	if expired { // too late: recorded, never charged
		p.State, p.Worker, p.CardToken, p.VerificationToken, p.Result = repositories.PlacementExpired, "", "", "", resultOf(errExpired)
	}
	got, inserted, err := s.pl.placements.Insert(ctx, p)
	if err != nil {
		return nil, false, err
	}
	if inserted {
		if expired {
			s.countPlacement(ch, "expired")
			s.notNotify(ctx, ch, req, "it reached the restaurant after its deadline", true)
			return nil, false, errExpired
		}
		return s.wait(ctx, ch, key, s.start(ctx, ch, req, got, false))
	}

	// The key exists.
	if got.BodyHash != p.BodyHash {
		s.countPlacement(ch, "key_reused")
		return nil, false, errKeyReused
	}
	switch got.State {
	case repositories.PlacementPlaced:
		s.countPlacement(ch, "replayed")
		v, err := s.byClient(ctx, ch, key)
		return v, false, err
	case repositories.PlacementPending:
	default:
		s.countPlacement(ch, "replayed")
		return nil, false, errorOf(got)
	}
	if r := s.liveRun(ch.ID, key); r != nil {
		return s.wait(ctx, ch, key, r) // the same request again while the first is still at work
	}
	if got.Waiting != "" { // the caller came back (3-D Secure answered, or a retry)
		if expired {
			if ok, _ := s.pl.placements.Finish(ctx, ch.ID, key, repositories.PlacementExpired, resultOf(errExpired)); ok {
				s.voidIfCharged(ctx, ch, got)
			}
			s.countPlacement(ch, "expired")
			return nil, false, errExpired
		}
		ok, err := s.pl.placements.Claim(ctx, got, s.pl.boot, req.Payment.Token, req.Payment.VerificationToken)
		if err != nil {
			return nil, false, err
		}
		if ok {
			got.Worker, got.Attempts = s.pl.boot, got.Attempts+1
			return s.wait(ctx, ch, key, s.start(ctx, ch, req, got, false))
		}
	}
	// Another worker (the sweeper, another main) has it.
	s.countPlacement(ch, "pending")
	return nil, false, &Pending{ClientOrderID: key}
}

func (s *Service) liveRun(channelID uuid.UUID, key string) *run {
	s.pl.mu.Lock()
	defer s.pl.mu.Unlock()
	return s.pl.live[liveKey(channelID, key)]
}

// start runs a placement's work detached from the caller (its own deadline).
func (s *Service) start(ctx context.Context, ch channelDom.Channel, req Request, p repositories.Placement, resume bool) *run {
	r := &run{done: make(chan struct{})}
	lk := liveKey(ch.ID, req.ClientOrderID)
	s.pl.mu.Lock()
	s.pl.live[lk] = r
	s.pl.mu.Unlock()
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), WorkFor)
	go func() {
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error().Interface("panic", rec).Str("client_order_id", req.ClientOrderID).Msg("placement worker panicked; the sweeper takes it")
				r.out = outcome{err: fmt.Errorf("placement worker: %v", rec)}
			}
			s.pl.mu.Lock()
			delete(s.pl.live, lk)
			s.pl.mu.Unlock()
			close(r.done)
		}()
		r.out = s.work(dctx, ch, req, p, resume)
		if r.out.err == nil {
			s.countPlacement(ch, "placed")
		}
	}()
	return r
}

type waitKey struct{}

// WithPlaceWait shortens how long Place waits before answering pending (dev
// tests only; the handler sets it from ?wait_ms= on dev).
func WithPlaceWait(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, waitKey{}, d)
}

// wait waits for a run, at most PlaceWait (then 202 pending).
func (s *Service) wait(ctx context.Context, ch channelDom.Channel, key string, r *run) (*View, bool, error) {
	d := PlaceWait
	if w, ok := ctx.Value(waitKey{}).(time.Duration); ok && w > 0 && w < d {
		d = w
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.done:
		return r.out.view, r.out.created, r.out.err
	case <-t.C:
		s.countPlacement(ch, "pending")
		return nil, false, &Pending{ClientOrderID: key}
	case <-ctx.Done():
		return nil, false, ctx.Err() // the caller left; the work goes on
	}
}

// ByKey is GET /channel/v1/orders/by-key/{key}: the placement's state.
func (s *Service) ByKey(ctx context.Context, ch channelDom.Channel, key string) (*PlacementView, error) {
	if s.pl.placements == nil {
		return nil, ErrNotFound
	}
	p, err := s.pl.placements.Get(ctx, ch.ID, key)
	if err != nil {
		// An order placed before placements existed answers from its link.
		if v, verr := s.byClient(ctx, ch, key); verr == nil {
			return &PlacementView{State: repositories.PlacementPlaced, Order: v}, nil
		}
		return nil, ErrNotFound
	}
	out := &PlacementView{State: p.State, ExpiresAt: p.ExpiresAt}
	switch p.State {
	case repositories.PlacementPlaced:
		v, err := s.byClient(ctx, ch, key)
		if err != nil {
			return nil, err
		}
		out.Order = v
	case repositories.PlacementPending:
		if p.Waiting != "" && len(p.Result) > 0 { // parked: what the caller must do (verify the card / retry)
			e := errorOf(p)
			out.Error = &ErrorView{Code: e.Code, Message: e.Message}
		}
	default:
		e := errorOf(p)
		out.Error = &ErrorView{Code: e.Code, Message: e.Message}
	}
	return out, nil
}

// --- the worker's helpers ---------------------------------------------------------------

func (s *Service) finish(ctx context.Context, ch channelDom.Channel, key, state string, e *Error) {
	if s.pl.placements == nil {
		return
	}
	if _, err := s.pl.placements.Finish(context.WithoutCancel(ctx), ch.ID, key, state, resultOf(e)); err != nil {
		s.logger.Error().Err(err).Str("client_order_id", key).Msg("placement not finished (the sweeper retries)")
	}
}

func (s *Service) park(ctx context.Context, ch channelDom.Channel, key, waiting string, e *Error) {
	if s.pl.placements == nil {
		return
	}
	if err := s.pl.placements.Park(context.WithoutCancel(ctx), ch.ID, key, waiting, resultOf(e)); err != nil {
		s.logger.Error().Err(err).Str("client_order_id", key).Msg("placement not parked")
	}
}

// voidKey releases whatever a card call with this order's key made.
func (s *Service) voidKey(ctx context.Context, ch channelDom.Channel, key string) error {
	gw, ok := s.gateways[payment.MethodCard]
	if !ok {
		return nil
	}
	v, ok := gw.(payment.KeyVoider)
	if !ok {
		return nil
	}
	return v.VoidByKey(context.WithoutCancel(ctx), ch.LocationID, ch.ID.String()+":"+key)
}

func (s *Service) voidIfCharged(ctx context.Context, ch channelDom.Channel, p repositories.Placement) {
	if !p.ChargeStarted {
		return
	}
	if err := s.voidKey(ctx, ch, p.ClientOrderID); err != nil {
		s.logger.Error().Err(err).Str("client_order_id", p.ClientOrderID).Msg("VOID FAILED for an unplaced order — void it by hand")
		s.page(ctx, ch.LocationID, notify.KindRefundFailed, "Card hold not released",
			fmt.Sprintf("An online order (%s) wasn't placed, but its card hold couldn't be released. Void it in Square's dashboard.", p.ClientOrderID))
	}
}

func (s *Service) page(ctx context.Context, locationID uuid.UUID, kind, title, msg string) {
	if s.notifier == nil {
		return
	}
	_ = s.notifier.Notify(context.WithoutCancel(ctx), notify.Notice{LocationID: locationID, Kind: kind, Title: title, Message: msg})
}

// notNotify tells the customer (email) and the owner (a page) that an order
// wasn't placed. charged=false: nothing was ever held.
func (s *Service) notNotify(ctx context.Context, ch channelDom.Channel, req Request, why string, _ bool) {
	// No customer name in a page (L-4): the order's key finds it.
	s.page(ctx, ch.LocationID, notify.KindOrderNotPlaced, "Online order NOT placed",
		fmt.Sprintf("A %s order (%s) wasn't placed: %s. The customer wasn't charged and was told.", ch.Name, req.ClientOrderID, why))
	if s.pl.mailer != nil && req.Customer.Email != "" {
		s.pl.mailer.NotPlaced(context.WithoutCancel(ctx), ch.LocationID, req.Customer.Email, req.Customer.Name, why)
	}
}

// kitchenOffline pages the owner (at most once per KitchenPageEvery per
// store, with how many were refused) and returns the refusal.
func (s *Service) kitchenOffline(ctx context.Context, ch channelDom.Channel, since time.Time) *Error {
	now := s.now()
	s.pl.mu.Lock()
	kp := s.pl.kitchenAt[ch.LocationID]
	kp.count++
	send := kp.at.IsZero() || now.Sub(kp.at) >= KitchenPageEvery
	n := kp.count
	if send {
		kp.at, kp.count = now, 0
	}
	s.pl.kitchenAt[ch.LocationID] = kp
	s.pl.mu.Unlock()
	if send {
		away := "a while"
		if !since.IsZero() {
			away = now.Sub(since).Round(time.Second).String()
		}
		s.page(ctx, ch.LocationID, notify.KindKitchenOffline, "Online orders refused — Pi offline",
			fmt.Sprintf("The store's Pi has been offline for %s, so %d online order(s) were refused (customers were told to call). Check the Pi and its internet.", away, n))
	}
	return errKitchen
}

// dueCheck is when a scheduled order's Pi is checked (due − 30 min).
func dueCheck(promised *time.Time) *time.Time {
	if promised == nil {
		return nil
	}
	t := promised.Add(-DueCheckBefore)
	return &t
}

// --- the sweeper ------------------------------------------------------------------------

// SweepResult counts one sweep's actions.
type SweepResult struct {
	Resumed, Completed, Voided, Expired, ParkedExpired, DueAlerts int
	Pruned                                                        int64
}

// Run sweeps every SweepEvery until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if s.pl.placements == nil {
		return
	}
	t := time.NewTicker(SweepEvery)
	defer t.Stop()
	for {
		s.Sweep(ctx, StaleAfter)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep finishes or undoes stale pending placements (another main's, or
// untouched for staleAfter), expires parked ones nobody came back for, checks
// the Pi for scheduled orders coming due, and prunes old final placements.
func (s *Service) Sweep(ctx context.Context, staleAfter time.Duration) SweepResult {
	var res SweepResult
	if s.pl.placements == nil {
		return res
	}
	stale, err := s.pl.placements.Stale(ctx, s.pl.boot, s.now().Add(-staleAfter))
	if err != nil {
		s.logger.Error().Err(err).Msg("sweeper: list stale placements")
	}
	for _, p := range stale {
		if s.liveRun(p.ChannelID, p.ClientOrderID) != nil {
			continue // still at work here
		}
		switch s.resume(ctx, p) {
		case "completed":
			res.Completed++
		case "voided":
			res.Voided++
		case "expired":
			res.Expired++
		case "resumed":
			res.Resumed++
		}
	}
	if parked, err := s.pl.placements.ParkedExpired(ctx); err == nil {
		for _, p := range parked {
			if ok, _ := s.pl.placements.Finish(ctx, p.ChannelID, p.ClientOrderID, repositories.PlacementExpired, resultOf(errExpired)); ok {
				if ch, err := s.pl.channels.Get(ctx, p.ChannelID); err == nil {
					s.voidIfCharged(ctx, *ch, p)
					// A temporary failure the customer may not have seen through: tell
					// them (a 3-D Secure wait was in front of them, so no message).
					var req Request
					if p.Waiting == "retry" && json.Unmarshal(p.Request, &req) == nil {
						s.notNotify(ctx, *ch, req, "main couldn't finish it in time", p.ChargeStarted)
					}
				}
				res.ParkedExpired++
				s.countSweep(p.TenantID, "parked_expired")
			}
		}
	}
	if due, err := s.pl.placements.DueAlerts(ctx); err == nil {
		for _, p := range due {
			s.dueAlert(ctx, p)
			res.DueAlerts++
		}
	}
	if n, err := s.pl.placements.Prune(ctx, s.now().Add(-KeepFor)); err == nil && n > 0 {
		res.Pruned = n
		s.countSweep(uuid.Nil, "pruned")
	}
	return res
}

// resume takes a stale placement over and finishes it: "completed",
// "voided", "expired", "resumed" (a fresh run that ended otherwise), "" (left).
func (s *Service) resume(ctx context.Context, p repositories.Placement) string {
	if s.pl.channels == nil {
		return ""
	}
	ok, err := s.pl.placements.Claim(ctx, p, s.pl.boot, "", "")
	if err != nil || !ok {
		return ""
	}
	p.Worker, p.Attempts = s.pl.boot, p.Attempts+1
	s.countSweep(p.TenantID, "resumed")
	chp, err := s.pl.channels.Get(ctx, p.ChannelID)
	if err != nil {
		s.logger.Error().Err(err).Str("client_order_id", p.ClientOrderID).Msg("sweeper: channel gone")
		return ""
	}
	ch := *chp
	var req Request
	if err := json.Unmarshal(p.Request, &req); err != nil {
		s.finish(ctx, ch, p.ClientOrderID, repositories.PlacementVoided, errVoided)
		s.voidIfCharged(ctx, ch, p)
		return "voided"
	}
	req.Payment.Token, req.Payment.VerificationToken = p.CardToken, p.VerificationToken
	if p.CustomerID != nil && s.pl.customers != nil {
		if cu, err := s.pl.customers.ByID(ctx, *p.CustomerID); err == nil {
			req.Account = &cu
		}
	}
	if len(p.Operator) > 0 {
		var op channelDom.Operator
		if json.Unmarshal(p.Operator, &op) == nil {
			req.TakenBy = &op
		}
	}
	if p.ExpiresAt != nil && s.now().After(*p.ExpiresAt) {
		state := repositories.PlacementExpired
		e := errExpired
		if p.ChargeStarted {
			state, e = repositories.PlacementVoided, errVoided
		}
		if ok, _ := s.pl.placements.Finish(ctx, ch.ID, p.ClientOrderID, state, resultOf(e)); ok {
			s.voidIfCharged(ctx, ch, p)
			s.countSweep(p.TenantID, state)
			s.notNotify(ctx, ch, req, "main couldn't finish it before its deadline", p.ChargeStarted)
			return state
		}
		return ""
	}
	r := s.start(ctx, ch, req, p, p.ChargeStarted)
	<-r.done
	switch {
	case r.out.err == nil:
		s.countSweep(p.TenantID, "completed")
		return "completed"
	case errors.Is(r.out.err, errVoided):
		s.countSweep(p.TenantID, "voided")
		return "voided"
	}
	// Refused on the re-run (closed meanwhile, …): nobody may be watching.
	if q, err := s.pl.placements.Get(ctx, ch.ID, p.ClientOrderID); err == nil && q.State == repositories.PlacementRefused {
		s.notNotify(ctx, ch, req, errorOf(q).Message, false)
	}
	return "resumed"
}

// dueAlert pages the owner when a scheduled order is coming due and the Pi
// is offline.
func (s *Service) dueAlert(ctx context.Context, p repositories.Placement) {
	defer func() { _ = s.pl.placements.SetDueAlerted(ctx, p.ChannelID, p.ClientOrderID) }()
	s.countSweep(p.TenantID, "due_alert")
	if s.pl.kitchen == nil {
		return
	}
	off, since, err := s.pl.kitchen.Offline(ctx, p.TenantID, p.LocationID)
	if err != nil || !off {
		return
	}
	var req Request
	_ = json.Unmarshal(p.Request, &req)
	due := "soon"
	if req.PromisedAt != nil {
		due = req.PromisedAt.Format(time.Kitchen)
	}
	away := ""
	if !since.IsZero() {
		away = fmt.Sprintf(" (offline for %s)", s.now().Sub(since).Round(time.Minute))
	}
	s.page(ctx, p.LocationID, notify.KindKitchenOffline, "Scheduled order due — Pi offline",
		fmt.Sprintf("%s's online order is due at %s, but the store's Pi is offline%s. It can't print until the Pi is back.", req.Customer.Name, due, away))
}

// --- dev only ---------------------------------------------------------------------------

// DevOrphan (dev only, tests) leaves a pending placement as a main that died
// mid-work would: worker "dev-gone", the card call recorded for chargeCents
// (0 = not made), and with hold the card hold actually made (the same call
// the worker makes). It returns the hold's reference ("" without one).
func (s *Service) DevOrphan(ctx context.Context, channelID uuid.UUID, req Request, chargeCents int64, hold bool) (string, error) {
	if s.pl.placements == nil || s.pl.channels == nil {
		return "", errors.New("placements are off")
	}
	chp, err := s.pl.channels.Get(ctx, channelID)
	if err != nil {
		return "", err
	}
	ch := *chp
	if e := req.validate(); e != nil {
		return "", e
	}
	p := repositories.Placement{ChannelID: ch.ID, ClientOrderID: req.ClientOrderID, TenantID: ch.TenantID, LocationID: ch.LocationID,
		BodyHash: bodyHash(req), CardToken: req.Payment.Token, VerificationToken: req.Payment.VerificationToken,
		ExpiresAt: req.ExpiresAt, State: repositories.PlacementPending, Worker: "dev-gone"}
	raw := req
	raw.Payment.Token, raw.Payment.VerificationToken = "", ""
	p.Request, _ = json.Marshal(raw)
	if _, inserted, err := s.pl.placements.Insert(ctx, p); err != nil {
		return "", err
	} else if !inserted {
		return "", errors.New("that key exists")
	}
	if chargeCents <= 0 {
		return "", nil
	}
	if err := s.pl.placements.StartCharge(ctx, ch.ID, req.ClientOrderID, chargeCents); err != nil {
		return "", err
	}
	if !hold {
		return "", nil
	}
	pctx, _, err := s.pricing.Context(ctx, ch.TenantID, ch.LocationID, string(ch.Kind), ch.ViewID)
	if err != nil {
		return "", err
	}
	q := pricing.Price(*pctx, pricing.Cart{Type: req.Type, Payment: req.Payment.Method, Lines: req.Lines, Code: req.Code,
		TipCents: req.TipCents, TipPct: req.TipPct, FirstOrder: req.FirstOrder}, s.now())
	charge, cerr := s.chargeFor(ctx, ch, req, q)
	if cerr != nil {
		return "", cerr
	}
	res, err := s.gateways[payment.MethodCard].Charge(ctx, charge)
	return res.Ref, err
}
