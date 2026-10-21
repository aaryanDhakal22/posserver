package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// compile-time interface check
var _ order.OrderRepository = (*OrderRepository)(nil)

const pgUniqueViolation = "23505"

type OrderRepository struct {
	pool         *pgxpool.Pool
	logger       zerolog.Logger
	handlerNames []string
}

func NewOrderRepository(pool *pgxpool.Pool, lg zerolog.Logger, handlerNames []string) *OrderRepository {
	seen := make(map[string]struct{}, len(handlerNames))
	deduped := make([]string, 0, len(handlerNames))
	for _, n := range handlerNames {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		deduped = append(deduped, n)
	}
	return &OrderRepository{
		pool:         pool,
		logger:       lg.With().Str("module", "order-repo").Logger(),
		handlerNames: deduped,
	}
}

// Create persists the aggregate and drains its DomainEvents into the outbox in
// a single transaction. (tenant_id, source, source_event_id) uniqueness is
// enforced at the DB level; collisions surface as order.ErrDuplicate so the
// caller can ack-and-skip.
func (r *OrderRepository) Create(ctx context.Context, o *order.Order) error {
	log := zerolog.Ctx(ctx).With().Str("module", "order-repo").Str("order_id", uuid.UUID(o.ID()).String()).Logger()
	log.Debug().Msg("Create: beginning transaction")

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := models.New(tx)

	if err := q.CreateOrder(ctx, toOrderRow(o)); err != nil {
		if isUniqueViolation(err) {
			log.Info().Msg("duplicate order; skipping")
			return order.ErrDuplicate
		}
		return fmt.Errorf("create order: %w", err)
	}

	for _, item := range o.Items() {
		if err := insertLineItemTree(ctx, q, o.ID(), nil, item); err != nil {
			return fmt.Errorf("create line items: %w", err)
		}
	}

	for _, p := range o.Payments() {
		if err := q.CreatePayment(ctx, toPaymentRow(o.ID(), p)); err != nil {
			return fmt.Errorf("create payment: %w", err)
		}
	}

	for i, se := range o.StatusHistory() {
		if err := q.CreateStatusEvent(ctx, toStatusEventRow(o.ID(), i, se)); err != nil {
			return fmt.Errorf("create status event: %w", err)
		}
	}

	for _, ev := range o.PullEvents() {
		if err := insertOutboxEvent(ctx, q, o.TenantID(), o.LocationID(), ev, r.handlerNames); err != nil {
			return fmt.Errorf("emit domain event %s: %w", ev.Type, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	log.Info().
		Str("source", string(o.Source())).
		Str("source_event_id", o.SourceEventID()).
		Msg("order persisted")
	return nil
}

func insertOutboxEvent(ctx context.Context, q *models.Queries, tenant order.TenantID, location order.LocationID, ev order.DomainEvent, handlerNames []string) error {
	payload := []byte(ev.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if err := q.InsertOutboxEvent(ctx, models.InsertOutboxEventParams{
		ID:            toPgUUID(ev.ID),
		AggregateID:   toPgUUID(uuid.UUID(ev.AggregateID)),
		TenantID:      toPgUUID(uuid.UUID(tenant)),
		LocationID:    toPgUUID(uuid.UUID(location)),
		Type:          string(ev.Type),
		Payload:       payload,
		OccurredAt:    pgtype.Timestamptz{Time: ev.OccurredAt, Valid: true},
		SchemaVersion: int32(ev.SchemaVersion),
	}); err != nil {
		return fmt.Errorf("insert outbox_events: %w", err)
	}
	for _, name := range handlerNames {
		if err := q.InsertOutboxDispatch(ctx, models.InsertOutboxDispatchParams{
			EventID:     toPgUUID(ev.ID),
			HandlerName: name,
		}); err != nil {
			return fmt.Errorf("insert outbox_dispatches[%s]: %w", name, err)
		}
	}
	return nil
}

func (r *OrderRepository) GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error) {
	q := models.New(r.pool)
	row, err := q.GetOrderInScope(ctx, models.GetOrderInScopeParams{
		ID:         toPgUUID(uuid.UUID(id)),
		TenantID:   toPgUUID(uuid.UUID(scope.TenantID)),
		LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
	})
	if err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	return r.assemble(ctx, q, row)
}

func (r *OrderRepository) FindBySourceEvent(ctx context.Context, scope order.Scope, src order.Source, sourceEventID string) (*order.Order, error) {
	q := models.New(r.pool)
	row, err := q.GetOrderBySourceEventInScope(ctx, models.GetOrderBySourceEventInScopeParams{
		TenantID:      toPgUUID(uuid.UUID(scope.TenantID)),
		LocationID:    toPgUUID(uuid.UUID(scope.LocationID)),
		Source:        string(src),
		SourceEventID: sourceEventID,
	})
	if err != nil {
		return nil, err
	}
	return r.assemble(ctx, q, row)
}

func (r *OrderRepository) GetOrdersPage(ctx context.Context, scope order.Scope, offset, limit int) ([]*order.Order, error) {
	q := models.New(r.pool)
	rows, err := q.GetOrdersPageInScope(ctx, models.GetOrdersPageInScopeParams{
		TenantID:   toPgUUID(uuid.UUID(scope.TenantID)),
		LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
		Limit:      int32(limit),
		Offset:     int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("get orders page: %w", err)
	}
	out := make([]*order.Order, 0, len(rows))
	for _, row := range rows {
		agg, err := r.assemble(ctx, q, row)
		if err != nil {
			return nil, err
		}
		out = append(out, agg)
	}
	return out, nil
}

// SearchOrders applies optional filters + sort direction. SearchOrdersInScope
// returns the same []models.Order shape as the page query, so assemble() is
// reused unchanged.
func (r *OrderRepository) SearchOrders(ctx context.Context, scope order.Scope, f order.OrderFilter) ([]*order.Order, error) {
	q := models.New(r.pool)
	params := models.SearchOrdersInScopeParams{
		TenantID:   toPgUUID(uuid.UUID(scope.TenantID)),
		LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
		Q:          f.Query,
		Status:     f.Status,
		Reverse:    f.Reverse,
		Off:        int32(f.Offset),
		Lim:        int32(f.Limit),
	}
	if f.From != nil {
		params.FromTime = pgtype.Timestamptz{Time: *f.From, Valid: true}
	}
	if f.To != nil {
		params.ToTime = pgtype.Timestamptz{Time: *f.To, Valid: true}
	}
	rows, err := q.SearchOrdersInScope(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search orders: %w", err)
	}
	out := make([]*order.Order, 0, len(rows))
	for _, row := range rows {
		agg, err := r.assemble(ctx, q, row)
		if err != nil {
			return nil, err
		}
		out = append(out, agg)
	}
	return out, nil
}

// DeleteByID removes one order + its outbox rows in a single transaction. Child
// rows (line_items, payments, status_events) cascade via FK; outbox_events has
// no FK to orders so it's deleted explicitly (dispatches cascade off it).
func (r *OrderRepository) DeleteByID(ctx context.Context, id order.ID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := models.New(tx)
	pid := toPgUUID(uuid.UUID(id))
	if err := q.DeleteOrderOutbox(ctx, pid); err != nil {
		return fmt.Errorf("delete order outbox: %w", err)
	}
	if err := q.DeleteOrderByID(ctx, pid); err != nil {
		return fmt.Errorf("delete order: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// ListSummaries returns a cross-tenant, lightweight order listing for the admin
// surface (no children assembled). UUIDs are cast to text so we avoid pgx type
// registration for the raw query. Admin-only — no scope filter.
func (r *OrderRepository) ListSummaries(ctx context.Context, f order.ListFilter) ([]order.Summary, error) {
	sql := `SELECT id::text, tenant_id::text, location_id::text, source, source_event_id,
	               status, customer_display_name, total_cents, received_at
	        FROM orders WHERE 1=1`
	args := []any{}
	if f.TenantID != "" {
		args = append(args, f.TenantID)
		sql += fmt.Sprintf(" AND tenant_id = $%d::uuid", len(args))
	}
	if f.SourceEventID != "" {
		args = append(args, f.SourceEventID)
		sql += fmt.Sprintf(" AND source_event_id = $%d", len(args))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" ORDER BY received_at DESC LIMIT $%d", len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list order summaries: %w", err)
	}
	defer rows.Close()

	out := make([]order.Summary, 0)
	for rows.Next() {
		var s order.Summary
		if err := rows.Scan(&s.ID, &s.TenantID, &s.LocationID, &s.Source, &s.SourceEventID,
			&s.Status, &s.CustomerName, &s.TotalCents, &s.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan order summary: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteAll bulk-deletes orders + their outbox rows in one transaction. Empty
// tenantID wipes every order; a non-empty tenantID scopes to one tenant. Order
// children (line_items, payments, status_events) cascade via FK; outbox_events
// has no FK so it's deleted explicitly (it carries tenant_id directly).
func (r *OrderRepository) DeleteAll(ctx context.Context, tenantID string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var tag pgconn.CommandTag
	if tenantID != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM outbox_events WHERE tenant_id = $1::uuid`, tenantID); err != nil {
			return 0, fmt.Errorf("delete outbox by tenant: %w", err)
		}
		if tag, err = tx.Exec(ctx, `DELETE FROM orders WHERE tenant_id = $1::uuid`, tenantID); err != nil {
			return 0, fmt.Errorf("delete orders by tenant: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `DELETE FROM outbox_events`); err != nil {
			return 0, fmt.Errorf("delete all outbox: %w", err)
		}
		if tag, err = tx.Exec(ctx, `DELETE FROM orders`); err != nil {
			return 0, fmt.Errorf("delete all orders: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Aggregate ↔ row mapping
// ---------------------------------------------------------------------------

func insertLineItemTree(ctx context.Context, q *models.Queries, orderID order.ID, parentID *uuid.UUID, item order.LineItem) error {
	var parentPg pgtype.UUID
	if parentID != nil {
		parentPg = toPgUUID(*parentID)
	}
	if err := q.CreateLineItem(ctx, models.CreateLineItemParams{
		ID:             toPgUUID(item.ID),
		OrderID:        toPgUUID(uuid.UUID(orderID)),
		ParentID:       parentPg,
		Position:       int32(item.Position),
		Sku:            item.SKU,
		Name:           item.Name,
		Quantity:       int32(item.Quantity),
		UnitPriceCents: int64(item.UnitPrice),
		LineTotalCents: int64(item.LineTotal),
		Notes:          item.Notes,
		Size:           item.Size,
		MenuItemID:     optPgUUID(item.MenuItemID),
		PrinterID:      optPgUUID(item.PrinterID),
		Routing:        string(item.Routing),
	}); err != nil {
		return err
	}
	for _, child := range item.Children {
		thisID := item.ID
		if err := insertLineItemTree(ctx, q, orderID, &thisID, child); err != nil {
			return err
		}
	}
	return nil
}

func toOrderRow(o *order.Order) models.CreateOrderParams {
	feesJSON, _ := json.Marshal(o.Money().Fees)
	discountsJSON, _ := json.Marshal(o.Money().Discounts)
	metaJSON, _ := json.Marshal(o.SourceMetadata())

	var addrJSON []byte
	if a := o.Fulfillment().Address; a != nil {
		addrJSON, _ = json.Marshal(a)
	}

	var promisedAt pgtype.Timestamptz
	if t := o.Fulfillment().PromisedAt; t != nil {
		promisedAt = pgtype.Timestamptz{Time: *t, Valid: true}
	}

	return models.CreateOrderParams{
		ID:                  toPgUUID(uuid.UUID(o.ID())),
		TenantID:            toPgUUID(uuid.UUID(o.TenantID())),
		LocationID:          toPgUUID(uuid.UUID(o.LocationID())),
		SchemaVersion:       int32(o.SchemaVersion()),
		Source:              string(o.Source()),
		SourceEventID:       o.SourceEventID(),
		Status:              string(o.Status()),
		SourceStatus:        o.SourceStatus(),
		Currency:            string(o.Currency()),
		SubtotalCents:       int64(o.Money().Subtotal),
		TaxCents:            int64(o.Money().Tax),
		TipCents:            int64(o.Money().Tip),
		TotalCents:          int64(o.Money().Total),
		Fees:                feesJSON,
		Discounts:           discountsJSON,
		CustomerDisplayName: o.Customer().DisplayName,
		CustomerEmail:       o.Customer().Email,
		CustomerPhone:       o.Customer().Phone,
		CustomerExternalID:  o.Customer().ExternalID,
		FulfillmentType:     string(o.Fulfillment().Type),
		FulfillmentAddress:  addrJSON,
		PromisedAt:          promisedAt,
		TableID:             o.Fulfillment().TableID,
		Notes:               o.Notes(),
		SourceMetadata:      metaJSON,
		RawPayload:          o.RawPayload(),
		PlacedAt:            pgtype.Timestamptz{Time: o.PlacedAt(), Valid: true},
		ReceivedAt:          pgtype.Timestamptz{Time: o.ReceivedAt(), Valid: true},
		UpdatedAt:           pgtype.Timestamptz{Time: o.UpdatedAt(), Valid: true},
	}
}

func toPaymentRow(orderID order.ID, p order.Payment) models.CreatePaymentParams {
	var capturedAt pgtype.Timestamptz
	if p.CapturedAt != nil {
		capturedAt = pgtype.Timestamptz{Time: *p.CapturedAt, Valid: true}
	}
	return models.CreatePaymentParams{
		ID:          toPgUUID(p.ID),
		OrderID:     toPgUUID(uuid.UUID(orderID)),
		Processor:   p.Processor,
		Status:      string(p.Status),
		AmountCents: int64(p.Amount),
		Token:       p.Token,
		Last4:       p.Last4,
		CapturedAt:  capturedAt,
	}
}

func toStatusEventRow(orderID order.ID, position int, se order.StatusEvent) models.CreateStatusEventParams {
	var actorID pgtype.UUID
	if se.ActorID != (uuid.UUID{}) {
		actorID = toPgUUID(se.ActorID)
	}
	return models.CreateStatusEventParams{
		OrderID:    toPgUUID(uuid.UUID(orderID)),
		Position:   int32(position),
		FromStatus: string(se.From),
		ToStatus:   string(se.To),
		At:         pgtype.Timestamptz{Time: se.At, Valid: true},
		Actor:      se.Actor,
		ActorID:    actorID,
		Reason:     se.Reason,
	}
}

func (r *OrderRepository) assemble(ctx context.Context, q *models.Queries, row models.Order) (*order.Order, error) {
	orderID := uuid.UUID(row.ID.Bytes)

	itemRows, err := q.GetLineItemsByOrderID(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("get line items: %w", err)
	}
	items := buildItemTree(itemRows)

	paymentRows, err := q.GetPaymentsByOrderID(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("get payments: %w", err)
	}
	payments := make([]order.Payment, 0, len(paymentRows))
	for _, p := range paymentRows {
		var captured *time.Time
		if p.CapturedAt.Valid {
			t := p.CapturedAt.Time
			captured = &t
		}
		payments = append(payments, order.Payment{
			ID:         uuid.UUID(p.ID.Bytes),
			Processor:  p.Processor,
			Status:     order.PaymentStatus(p.Status),
			Amount:     order.Money(p.AmountCents),
			Token:      p.Token,
			Last4:      p.Last4,
			CapturedAt: captured,
		})
	}

	historyRows, err := q.GetStatusEventsByOrderID(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("get status events: %w", err)
	}
	history := make([]order.StatusEvent, 0, len(historyRows))
	for _, h := range historyRows {
		var aid uuid.UUID
		if h.ActorID.Valid {
			aid = uuid.UUID(h.ActorID.Bytes)
		}
		history = append(history, order.StatusEvent{
			From:    order.Status(h.FromStatus),
			To:      order.Status(h.ToStatus),
			At:      h.At.Time,
			ActorID: aid,
			Actor:   h.Actor,
			Reason:  h.Reason,
		})
	}

	var fees []order.Fee
	if len(row.Fees) > 0 {
		_ = json.Unmarshal(row.Fees, &fees)
	}
	var discounts []order.Discount
	if len(row.Discounts) > 0 {
		_ = json.Unmarshal(row.Discounts, &discounts)
	}
	var meta map[string]any
	if len(row.SourceMetadata) > 0 {
		_ = json.Unmarshal(row.SourceMetadata, &meta)
	}

	var addr *order.Address
	if len(row.FulfillmentAddress) > 0 {
		var a order.Address
		if err := json.Unmarshal(row.FulfillmentAddress, &a); err == nil {
			addr = &a
		}
	}
	var promisedAt *time.Time
	if row.PromisedAt.Valid {
		t := row.PromisedAt.Time
		promisedAt = &t
	}

	in := order.RehydrateInput{
		ID:            order.ID(orderID),
		TenantID:      order.TenantID(row.TenantID.Bytes),
		LocationID:    order.LocationID(row.LocationID.Bytes),
		SchemaVersion: int(row.SchemaVersion),
		Source:        order.Source(row.Source),
		SourceEventID: row.SourceEventID,
		Status:        order.Status(row.Status),
		SourceStatus:  row.SourceStatus,
		StatusHistory: history,
		Customer: order.Customer{
			DisplayName: row.CustomerDisplayName,
			Email:       row.CustomerEmail,
			Phone:       row.CustomerPhone,
			ExternalID:  row.CustomerExternalID,
		},
		Fulfillment: order.Fulfillment{
			Type:       order.FulfillmentType(row.FulfillmentType),
			Address:    addr,
			PromisedAt: promisedAt,
			TableID:    row.TableID,
		},
		Items: items,
		Money: order.MoneyBreakdown{
			Subtotal:  order.Money(row.SubtotalCents),
			Tax:       order.Money(row.TaxCents),
			Tip:       order.Money(row.TipCents),
			Fees:      fees,
			Discounts: discounts,
			Total:     order.Money(row.TotalCents),
		},
		Currency:       order.Currency(row.Currency),
		PlacedAt:       row.PlacedAt.Time,
		ReceivedAt:     row.ReceivedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
		Payments:       payments,
		Notes:          row.Notes,
		SourceMetadata: meta,
		RawPayload:     row.RawPayload,
		DriverName:     row.DriverName,
	}
	if row.KitchenOrderNumber.Valid {
		k := int(row.KitchenOrderNumber.Int32)
		in.KitchenNumber = &k
	}
	if row.EstimatedReadyAt.Valid {
		t := row.EstimatedReadyAt.Time
		in.EstimatedReadyAt = &t
	}
	if row.FloorAt.Valid {
		t := row.FloorAt.Time
		in.FloorAt = &t
	}
	return order.Rehydrate(in), nil
}

func buildItemTree(rows []models.LineItem) []order.LineItem {
	if len(rows) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*order.LineItem, len(rows))
	tops := make([]*order.LineItem, 0)

	for i := range rows {
		row := rows[i]
		var parentID *uuid.UUID
		if row.ParentID.Valid {
			pid := uuid.UUID(row.ParentID.Bytes)
			parentID = &pid
		}
		li := order.LineItem{
			ID:        uuid.UUID(row.ID.Bytes),
			ParentID:  parentID,
			Position:  int(row.Position),
			SKU:       row.Sku,
			Name:      row.Name,
			Quantity:  int(row.Quantity),
			UnitPrice: order.Money(row.UnitPriceCents),
			LineTotal: order.Money(row.LineTotalCents),
			Notes:     row.Notes,
			Size:      row.Size,
			Routing:   order.Routing(row.Routing),
		}
		if row.MenuItemID.Valid {
			id := uuid.UUID(row.MenuItemID.Bytes)
			li.MenuItemID = &id
		}
		if row.PrinterID.Valid {
			id := uuid.UUID(row.PrinterID.Bytes)
			li.PrinterID = &id
		}
		byID[li.ID] = &li
		if parentID == nil {
			tops = append(tops, &li)
		} else if p, ok := byID[*parentID]; ok {
			p.Children = append(p.Children, li)
		}
	}

	out := make([]order.LineItem, 0, len(tops))
	for _, t := range tops {
		out = append(out, *t)
	}
	return out
}

func toPgUUID(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	return false
}

var ErrNoRows = pgx.ErrNoRows

// optPgUUID maps an optional uuid to a nullable pg uuid.
func optPgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}
