package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PlacementRepository keeps order placements (DURABILITY-PLAN.md): a channel
// order's idempotency key, its request and its outcome, written before any
// money moves. MarkPlacementPlaced runs inside OrderRepository.Create.
type PlacementRepository struct {
	pool *pgxpool.Pool
	box  TextBox // nil: card tokens are stored as given
}

// TextBox seals the card token a placement keeps while it is in flight
// (squarepay.Service: SQUARE_TOKEN_KEY, the stored OAuth tokens' key). Open
// must return a value it never sealed unchanged (plaintext rows from before).
type TextBox interface {
	SealText(plain string) (string, error)
	OpenText(sealed string) (string, error)
}

// SetTokenBox seals card tokens at rest from now on.
func (r *PlacementRepository) SetTokenBox(b TextBox) { r.box = b }

func (r *PlacementRepository) sealToken(t string) (string, error) {
	if r.box == nil || t == "" {
		return t, nil
	}
	return r.box.SealText(t)
}

// opened is p with its card token opened.
func (r *PlacementRepository) opened(p Placement) (Placement, error) {
	if r.box == nil || p.CardToken == "" {
		return p, nil
	}
	t, err := r.box.OpenText(p.CardToken)
	if err != nil {
		return Placement{}, fmt.Errorf("placement %s: card token: %w", p.ClientOrderID, err)
	}
	p.CardToken = t
	return p, nil
}

// openedAll opens every row's card token. A token that can't be opened (a
// changed key) is blanked rather than failing the list: the sweeper must not
// stall on one row; that placement's card call then fails and it ends.
func (r *PlacementRepository) openedAll(ps []Placement, err error) ([]Placement, error) {
	if err != nil {
		return ps, err
	}
	for i := range ps {
		if o, oerr := r.opened(ps[i]); oerr == nil {
			ps[i] = o
		} else {
			ps[i].CardToken = ""
		}
	}
	return ps, nil
}

func NewPlacementRepository(pool *pgxpool.Pool) *PlacementRepository {
	return &PlacementRepository{pool: pool}
}

// Placement states.
const (
	PlacementPending = "pending"
	PlacementPlaced  = "placed"
	PlacementRefused = "refused"
	PlacementExpired = "expired"
	PlacementVoided  = "voided"
)

// Placement is one channel order's placement.
type Placement struct {
	ChannelID         uuid.UUID
	ClientOrderID     string
	TenantID          uuid.UUID
	LocationID        uuid.UUID
	BodyHash          string
	Request           []byte
	CardToken         string
	VerificationToken string
	CustomerID        *uuid.UUID
	Operator          []byte
	ExpiresAt         *time.Time
	State             string
	Waiting           string // pending but parked: verification | retry
	Result            []byte // the last refusal
	OrderID           *uuid.UUID
	Worker            string
	Attempts          int32
	ChargeStarted     bool
	ChargeCents       int64
	DueAlertAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func pgTS(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func placementOf(r models.OrderPlacement) Placement {
	return Placement{ChannelID: idOf(r.ChannelID), ClientOrderID: r.ClientOrderID, TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID),
		BodyHash: r.BodyHash, Request: r.Request, CardToken: r.CardToken, VerificationToken: r.VerificationToken,
		CustomerID: idPtrOf(r.CustomerID), Operator: r.Operator, ExpiresAt: tsPtr(r.ExpiresAt), State: r.State, Waiting: r.Waiting,
		Result: r.Result, OrderID: idPtrOf(r.OrderID), Worker: r.Worker, Attempts: r.Attempts, ChargeStarted: r.ChargeStarted,
		ChargeCents: r.ChargeCents, DueAlertAt: tsPtr(r.DueAlertAt), CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
}

func placementsOf(rs []models.OrderPlacement) []Placement {
	out := make([]Placement, len(rs))
	for i, r := range rs {
		out[i] = placementOf(r)
	}
	return out
}

// Insert writes a new placement; inserted = false when the key exists (p is
// then the stored one).
func (r *PlacementRepository) Insert(ctx context.Context, p Placement) (Placement, bool, error) {
	q := models.New(r.pool)
	token, err := r.sealToken(p.CardToken)
	if err != nil {
		return Placement{}, false, err
	}
	row, err := q.InsertPlacement(ctx, models.InsertPlacementParams{ChannelID: toPgUUID(p.ChannelID), ClientOrderID: p.ClientOrderID,
		TenantID: toPgUUID(p.TenantID), LocationID: toPgUUID(p.LocationID), BodyHash: p.BodyHash, Request: p.Request,
		CardToken: token, VerificationToken: p.VerificationToken, CustomerID: pgIDPtr(p.CustomerID), Operator: p.Operator,
		ExpiresAt: pgTS(p.ExpiresAt), State: p.State, Result: p.Result, Worker: p.Worker})
	if err == nil {
		got, err := r.opened(placementOf(row))
		return got, err == nil, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Placement{}, false, err
	}
	got, err := r.Get(ctx, p.ChannelID, p.ClientOrderID)
	return got, false, err
}

// Get is one placement (pgx.ErrNoRows = none).
func (r *PlacementRepository) Get(ctx context.Context, channelID uuid.UUID, key string) (Placement, error) {
	row, err := models.New(r.pool).GetPlacement(ctx, models.GetPlacementParams{ChannelID: toPgUUID(channelID), ClientOrderID: key})
	if err != nil {
		return Placement{}, err
	}
	return r.opened(placementOf(row))
}

// Claim takes a pending placement over for worker; false = someone else did.
func (r *PlacementRepository) Claim(ctx context.Context, p Placement, worker, cardToken, verificationToken string) (bool, error) {
	cardToken, err := r.sealToken(cardToken)
	if err != nil {
		return false, err
	}
	n, err := models.New(r.pool).ClaimPlacement(ctx, models.ClaimPlacementParams{Worker: worker, CardToken: cardToken,
		VerificationToken: verificationToken, ChannelID: toPgUUID(p.ChannelID), ClientOrderID: p.ClientOrderID,
		OldWorker: p.Worker, OldAttempts: p.Attempts})
	return n == 1, err
}

// StartCharge records that the card call is about to be made, for cents.
func (r *PlacementRepository) StartCharge(ctx context.Context, channelID uuid.UUID, key string, cents int64) error {
	return models.New(r.pool).StartPlacementCharge(ctx, models.StartPlacementChargeParams{ChannelID: toPgUUID(channelID), ClientOrderID: key, ChargeCents: cents})
}

// Finish ends a pending placement in state (refused / expired / voided) with
// result; false = it wasn't pending any more.
func (r *PlacementRepository) Finish(ctx context.Context, channelID uuid.UUID, key, state string, result []byte) (bool, error) {
	n, err := models.New(r.pool).FinishPlacement(ctx, models.FinishPlacementParams{ChannelID: toPgUUID(channelID), ClientOrderID: key,
		State: state, Result: result})
	return n == 1, err
}

// Park leaves a pending placement for its caller to come back to.
func (r *PlacementRepository) Park(ctx context.Context, channelID uuid.UUID, key, waiting string, result []byte) error {
	return models.New(r.pool).ParkPlacement(ctx, models.ParkPlacementParams{ChannelID: toPgUUID(channelID), ClientOrderID: key,
		Waiting: waiting, Result: result})
}

// Stale lists pending placements another main left behind, or older than before.
func (r *PlacementRepository) Stale(ctx context.Context, worker string, before time.Time) ([]Placement, error) {
	rs, err := models.New(r.pool).ListStalePlacements(ctx, models.ListStalePlacementsParams{Worker: worker,
		Before: pgtype.Timestamptz{Time: before, Valid: true}})
	return r.openedAll(placementsOf(rs), err)
}

// ParkedExpired lists parked placements nobody came back for.
func (r *PlacementRepository) ParkedExpired(ctx context.Context) ([]Placement, error) {
	rs, err := models.New(r.pool).ListParkedExpired(ctx)
	return r.openedAll(placementsOf(rs), err)
}

// DueAlerts lists placed scheduled orders whose Pi check is due.
func (r *PlacementRepository) DueAlerts(ctx context.Context) ([]Placement, error) {
	rs, err := models.New(r.pool).ListPlacementDueAlerts(ctx)
	return r.openedAll(placementsOf(rs), err)
}

func (r *PlacementRepository) SetDueAlerted(ctx context.Context, channelID uuid.UUID, key string) error {
	return models.New(r.pool).SetPlacementDueAlerted(ctx, models.SetPlacementDueAlertedParams{ChannelID: toPgUUID(channelID), ClientOrderID: key})
}

// Prune drops final placements created before t.
func (r *PlacementRepository) Prune(ctx context.Context, t time.Time) (int64, error) {
	return models.New(r.pool).PrunePlacements(ctx, pgtype.Timestamptz{Time: t, Valid: true})
}

// ActiveAgents counts a location's agent keys that aren't revoked (its Pi).
func (r *PlacementRepository) ActiveAgents(ctx context.Context, locationID uuid.UUID) (int64, error) {
	return models.New(r.pool).CountActiveAgents(ctx, toPgUUID(locationID))
}
