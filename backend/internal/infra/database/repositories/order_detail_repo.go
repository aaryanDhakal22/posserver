package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OrderDetailRepository reads everything Store › Orders' detail panel shows
// beside the order itself (GET /store/v1/me/orders/:id): the status history,
// every payment with who took it, the Pi's floor status, the staff journal
// lines and money flags about the order, its card hold, its channel and the
// printers its lines route to. Plain SQL, read only, always in the caller's
// store (the order row is checked first).
type OrderDetailRepository struct{ pool *pgxpool.Pool }

func NewOrderDetailRepository(pool *pgxpool.Pool) *OrderDetailRepository {
	return &OrderDetailRepository{pool: pool}
}

// OrderDetailExtras is the detail panel's data besides the order snapshot.
type OrderDetailExtras struct {
	ReceivedAt    time.Time            `json:"received_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	AgentAckAt    *time.Time           `json:"agent_ack_at"`
	AgentStatus   json.RawMessage      `json:"agent_status"`
	BatchID       *int64               `json:"batch_id"`
	StatusHistory []OrderStatusRow     `json:"status_history"`
	Payments      []OrderPaymentRow    `json:"payments"`
	Journal       []OrderJournalRow    `json:"journal"`
	Flags         []OrderFlagRow       `json:"flags"`
	CardHold      *OrderCardHoldRow    `json:"card_hold"`
	Channel       *OrderChannelRow     `json:"channel"`
	Staff         *OrderPersonRow      `json:"staff"`
	Printers      map[string]PrinterNm `json:"printers"`
	// Prints: what each print job of the order printed, oldest first (the
	// Pi's order.printed, agent 2.5.5 §2.10ag; [] from an older Pi).
	Prints []order.OrderPrint `json:"prints"`
}

type OrderStatusRow struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Reason string    `json:"reason,omitempty"`
}

type OrderPaymentRow struct {
	ID          string `json:"id"`
	Method      string `json:"method"`
	Status      string `json:"status"`
	AmountCents int64  `json:"amount_cents"`
	TipCents    int64  `json:"tip_cents"`
	Last4       string `json:"last4,omitempty"`
	// CardBrand: amex | discover | mastercard | visa | other ("" = not sent).
	CardBrand  string     `json:"card_brand,omitempty"`
	AuthCode   string     `json:"auth_code,omitempty"`
	Origin     string     `json:"origin,omitempty"`
	RefundOf   string     `json:"refund_of,omitempty"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Flags      []string   `json:"flags"`
	StaffName  string     `json:"staff_name,omitempty"`
	BatchID    *int64     `json:"batch_id"`
	CapturedAt *time.Time `json:"captured_at"`
	ReceivedAt time.Time  `json:"received_at"`
}

type OrderJournalRow struct {
	At        time.Time       `json:"at"`
	Action    string          `json:"action"`
	StaffName string          `json:"staff_name,omitempty"`
	DeviceID  string          `json:"device_id,omitempty"`
	Detail    json.RawMessage `json:"detail"`
}

type OrderFlagRow struct {
	At         time.Time       `json:"at"`
	Code       string          `json:"code"`
	Subject    string          `json:"subject"`
	Detail     json.RawMessage `json:"detail"`
	ResolvedAt *time.Time      `json:"resolved_at"`
}

type OrderCardHoldRow struct {
	Gateway       string     `json:"gateway"`
	Status        string     `json:"status"`
	AmountCents   int64      `json:"amount_cents"`
	TipCents      int64      `json:"tip_cents"`
	AddedTipCents int64      `json:"added_tip_cents"`
	CapturedCents int64      `json:"captured_cents"`
	RefundedCents int64      `json:"refunded_cents"`
	DueAt         time.Time  `json:"due_at"`
	CapturedAt    *time.Time `json:"captured_at"`
	CancelledAt   *time.Time `json:"cancelled_at"`
	LastError     string     `json:"last_error,omitempty"`
}

type OrderChannelRow struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	ClientOrderID string `json:"client_order_id,omitempty"`
	Operator      string `json:"operator,omitempty"`
}

type OrderPersonRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type PrinterNm struct {
	Name      string `json:"name"`
	IsReceipt bool   `json:"is_receipt"`
}

// ErrOrderNotInStore: no such order in the caller's store.
var ErrOrderNotInStore = fmt.Errorf("order not found")

// Extras loads the detail panel's data for one order of the store.
func (r *OrderDetailRepository) Extras(ctx context.Context, tenantID, locationID, orderID uuid.UUID) (*OrderDetailExtras, error) {
	out := &OrderDetailExtras{StatusHistory: []OrderStatusRow{}, Payments: []OrderPaymentRow{}, Journal: []OrderJournalRow{},
		Flags: []OrderFlagRow{}, Printers: map[string]PrinterNm{}, Prints: []order.OrderPrint{}}
	var staffID string
	err := r.pool.QueryRow(ctx, `SELECT received_at, updated_at, agent_ack_at, agent_status, agent_batch_id,
		       COALESCE(source_metadata->>'staff_id', '')
		FROM orders WHERE id = $1 AND tenant_id = $2 AND location_id = $3`, orderID, tenantID, locationID).
		Scan(&out.ReceivedAt, &out.UpdatedAt, &out.AgentAckAt, &out.AgentStatus, &out.BatchID, &staffID)
	if NoRows(err) {
		return nil, ErrOrderNotInStore
	}
	if err != nil {
		return nil, fmt.Errorf("order: %w", err)
	}

	rows, err := r.pool.Query(ctx, `SELECT from_status, to_status, at, actor, reason FROM status_events
		WHERE order_id = $1 ORDER BY position`, orderID)
	if err != nil {
		return nil, fmt.Errorf("status history: %w", err)
	}
	for rows.Next() {
		var s OrderStatusRow
		if err := rows.Scan(&s.From, &s.To, &s.At, &s.Actor, &s.Reason); err != nil {
			rows.Close()
			return nil, err
		}
		out.StatusHistory = append(out.StatusHistory, s)
	}
	rows.Close()

	// The staff of the store by id (payments, journal, approvals and who
	// took the order name them by id).
	names := map[string]OrderPersonRow{}
	rows, err = r.pool.Query(ctx, `SELECT id::text, trim(first_name || ' ' || last_name), role FROM staff
		WHERE tenant_id = $1 AND location_id = $2`, tenantID, locationID)
	if err != nil {
		return nil, fmt.Errorf("staff: %w", err)
	}
	for rows.Next() {
		var p OrderPersonRow
		if err := rows.Scan(&p.ID, &p.Name, &p.Role); err != nil {
			rows.Close()
			return nil, err
		}
		names[p.ID] = p
	}
	rows.Close()
	name := func(id string) string {
		if p, ok := names[strings.ToLower(id)]; ok {
			return p.Name
		}
		return ""
	}
	if p, ok := names[strings.ToLower(staffID)]; ok {
		out.Staff = &p
	}

	rows, err = r.pool.Query(ctx, `SELECT id::text, processor, status, amount_cents, tip_cents, last4, card_brand, auth_code, origin,
		       COALESCE(refund_of::text, ''), approved_by, reason, flags, COALESCE(staff_id::text, ''), batch_id,
		       captured_at, received_at
		FROM payments WHERE order_id = $1 ORDER BY COALESCE(captured_at, received_at), received_at`, orderID)
	if err != nil {
		return nil, fmt.Errorf("payments: %w", err)
	}
	for rows.Next() {
		var p OrderPaymentRow
		var sid string
		if err := rows.Scan(&p.ID, &p.Method, &p.Status, &p.AmountCents, &p.TipCents, &p.Last4, &p.CardBrand, &p.AuthCode, &p.Origin,
			&p.RefundOf, &p.ApprovedBy, &p.Reason, &p.Flags, &sid, &p.BatchID, &p.CapturedAt, &p.ReceivedAt); err != nil {
			rows.Close()
			return nil, err
		}
		p.StaffName = name(sid)
		if n := name(p.ApprovedBy); n != "" {
			p.ApprovedBy = n
		}
		if p.Flags == nil {
			p.Flags = []string{}
		}
		out.Payments = append(out.Payments, p)
	}
	rows.Close()

	rows, err = r.pool.Query(ctx, `SELECT at, action, COALESCE(staff_id::text, ''), device_id, detail FROM staff_journal
		WHERE order_id = $1 AND tenant_id = $2 AND location_id = $3 ORDER BY at LIMIT 200`, orderID, tenantID, locationID)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	for rows.Next() {
		var j OrderJournalRow
		var sid string
		if err := rows.Scan(&j.At, &j.Action, &sid, &j.DeviceID, &j.Detail); err != nil {
			rows.Close()
			return nil, err
		}
		j.StaffName = name(sid)
		out.Journal = append(out.Journal, j)
	}
	rows.Close()

	subjects := []string{"order:" + orderID.String()}
	for _, p := range out.Payments {
		subjects = append(subjects, "payment:"+p.ID)
	}
	rows, err = r.pool.Query(ctx, `SELECT at, code, subject, detail, resolved_at FROM money_flags
		WHERE tenant_id = $1 AND location_id = $2 AND subject = ANY($3) ORDER BY at`, tenantID, locationID, subjects)
	if err != nil {
		return nil, fmt.Errorf("flags: %w", err)
	}
	for rows.Next() {
		var f OrderFlagRow
		if err := rows.Scan(&f.At, &f.Code, &f.Subject, &f.Detail, &f.ResolvedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out.Flags = append(out.Flags, f)
	}
	rows.Close()

	var h OrderCardHoldRow
	err = r.pool.QueryRow(ctx, `SELECT gateway, status, amount_cents, tip_cents, added_tip_cents, captured_cents, refunded_cents,
		       due_at, captured_at, cancelled_at, last_error
		FROM card_holds WHERE order_id = $1 AND tenant_id = $2 AND location_id = $3`, orderID, tenantID, locationID).
		Scan(&h.Gateway, &h.Status, &h.AmountCents, &h.TipCents, &h.AddedTipCents, &h.CapturedCents, &h.RefundedCents,
			&h.DueAt, &h.CapturedAt, &h.CancelledAt, &h.LastError)
	if err == nil {
		out.CardHold = &h
	} else if !NoRows(err) {
		return nil, fmt.Errorf("card hold: %w", err)
	}

	var ch OrderChannelRow
	err = r.pool.QueryRow(ctx, `SELECT COALESCE(c.kind, ''), COALESCE(c.name, ''), co.client_order_id,
		       COALESCE(NULLIF(o.display_name, ''), o.username, '')
		FROM channel_orders co
		LEFT JOIN channels c ON c.id = co.channel_id
		LEFT JOIN operators o ON o.id = co.operator_id
		WHERE co.order_id = $1 AND co.tenant_id = $2 AND co.location_id = $3`, orderID, tenantID, locationID).
		Scan(&ch.Kind, &ch.Name, &ch.ClientOrderID, &ch.Operator)
	if err == nil {
		out.Channel = &ch
	} else if !NoRows(err) {
		return nil, fmt.Errorf("channel: %w", err)
	}

	rows, err = r.pool.Query(ctx, `SELECT id::text, name, is_receipt FROM printers WHERE tenant_id = $1 AND location_id = $2`,
		tenantID, locationID)
	if err != nil {
		return nil, fmt.Errorf("printers: %w", err)
	}
	for rows.Next() {
		var id string
		var p PrinterNm
		if err := rows.Scan(&id, &p.Name, &p.IsReceipt); err != nil {
			rows.Close()
			return nil, err
		}
		out.Printers[id] = p
	}
	rows.Close()

	rows, err = r.pool.Query(ctx, `SELECT event_id::text, mode, at, tickets FROM order_prints
		WHERE order_id = $1 AND tenant_id = $2 AND location_id = $3 ORDER BY at, received_at LIMIT 200`, orderID, tenantID, locationID)
	if err != nil {
		return nil, fmt.Errorf("prints: %w", err)
	}
	for rows.Next() {
		var p order.OrderPrint
		var raw []byte
		if err := rows.Scan(&p.EventID, &p.Mode, &p.At, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &p.Tickets); err != nil || p.Tickets == nil {
			p.Tickets = []order.PrintTicket{}
		}
		out.Prints = append(out.Prints, p)
	}
	rows.Close()
	return out, rows.Err()
}

// ListIDs answers the store's orders for Store › Orders, newest placed first
// (the time the order was taken, not when main stored it: a Pi that syncs
// late or an order accepted out of turn keeps its place). day (YYYY-MM-DD)
// narrows it to one store-local day; q matches the order number, kitchen /
// check number, customer name or phone, and driver.
func (r *OrderDetailRepository) ListIDs(ctx context.Context, tenantID, locationID uuid.UUID, day string, q string, limit int) ([]uuid.UUID, error) {
	var dayArg any // NULL = every day
	if day != "" {
		dayArg = day
	}
	rows, err := r.pool.Query(ctx, `SELECT o.id FROM orders o
		WHERE o.tenant_id = $1 AND o.location_id = $2
		  AND ($3::date IS NULL OR (o.placed_at AT TIME ZONE COALESCE(NULLIF((SELECT l.timezone FROM locations l WHERE l.id = o.location_id), ''), 'America/New_York'))::date = $3::date)
		  AND ($4 = '' OR o.source_event_id ILIKE '%' || $4 || '%' OR o.customer_display_name ILIKE '%' || $4 || '%'
		       OR o.customer_phone ILIKE '%' || $4 || '%' OR o.driver_name ILIKE '%' || $4 || '%'
		       OR o.kitchen_order_number::text = $4 OR o.id::text = $4)
		ORDER BY o.placed_at DESC, o.received_at DESC
		LIMIT $5`, tenantID, locationID, dayArg, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
