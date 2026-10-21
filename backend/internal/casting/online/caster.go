package online

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName = "quiccpos/main/casting/online"

	// currencyUSD is the only currency this caster emits today. The vendor
	// payload doesn't carry a currency code; the deployment is single-tenant
	// USD. Lift to config when a second tenant lands.
	currencyUSD order.Currency = "USD"
)

// Sentinel errors returned by Cast. Callers (the SQS consumer) use these to
// decide whether to redrive (transient) or DLQ (permanent). Today every Cast
// failure is permanent — a malformed vendor payload won't fix itself on
// redelivery.
var (
	ErrPayloadInvalid    = errors.New("online: payload is not valid JSON")
	ErrPayloadIncomplete = errors.New("online: payload is missing required fields")
	ErrTimestampInvalid  = errors.New("online: payload timestamp could not be parsed")
)

// StoreResolver maps a vendor order to the owning (tenant, location). The
// online caster uses it to route each order to the right restaurant.
//
// The concrete resolver (app/tenancy ResolveStore) matches the vendor's numeric
// store_id against tenants.store_id first; when store_id is absent (0) or not
// mapped it falls back to a fuzzy store_name match (TECH-DEBT). method reports
// which key matched ("store_id" | "name_fallback") for tracing.
type StoreResolver interface {
	ResolveStore(ctx context.Context, storeID int64, storeName string) (order.TenantID, order.LocationID, string, error)
}

// ItemRouter maps an order's top-level item names to kitchen printers for one
// location (one ItemRoute per name, same order). The concrete router is the
// menu service (app/menu RouteItems): exact name match, case/space-insensitive.
type ItemRouter interface {
	RouteItems(ctx context.Context, locationID uuid.UUID, names []string) ([]order.ItemRoute, error)
}

// Caster maps a vendor JSON Payload to the canonical order.Order aggregate.
// One Caster instance per process; safe for concurrent use.
type Caster struct {
	logger   zerolog.Logger
	tracer   trace.Tracer
	resolver StoreResolver
	router   ItemRouter // nil = no kitchen routing (items print as one receipt)
}

// New builds a Caster. The tenant/location for each order are resolved per-call
// from the payload's store_id (then store_name) via the injected StoreResolver — the SQS
// tenant_id/location_id MessageAttributes are ignored for this source (they
// still arrive from online but no longer drive routing).
//
// router stamps each item's kitchen printer. It runs inside Cast, BEFORE
// order.NewOrder, because NewOrder captures the `order.received` snapshot that
// the outbox ships to the agent — routing added afterwards would never reach it.
func New(lg zerolog.Logger, resolver StoreResolver, router ItemRouter) *Caster {
	return &Caster{
		logger:   lg.With().Str("module", "online-caster").Logger(),
		tracer:   otel.Tracer(tracerName),
		resolver: resolver,
		router:   router,
	}
}

// Source returns the order.Source this caster produces. Lets the consumer
// route by source without hard-coding the constant.
func (c *Caster) Source() order.Source { return order.SourceOnline }

// Cast consumes the raw vendor JSON and produces a canonical Order aggregate.
// The raw bytes are preserved on the aggregate's RawPayload for replay/debug.
func (c *Caster) Cast(ctx context.Context, in order.CastInput) (*order.Order, error) {
	ctx, span := c.tracer.Start(ctx, "online.cast",
		trace.WithAttributes(attribute.Int("payload.bytes", len(in.Raw))),
	)
	defer span.End()

	raw := in.Raw
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "json_unmarshal")
		return nil, fmt.Errorf("%w: %v", ErrPayloadInvalid, err)
	}

	if p.OrderID == 0 {
		span.SetStatus(codes.Error, "missing_order_id")
		return nil, fmt.Errorf("%w: missing order_id", ErrPayloadIncomplete)
	}

	// Route by store_id (falling back to store_name) → (tenant, location). An
	// unresolved store is a permanent failure: returning the error leaves the
	// message for redrive/DLQ (see consumer) rather than persisting it to a
	// wrong scope.
	span.SetAttributes(
		attribute.Int64("order.store_id", p.StoreID),
		attribute.String("order.store_name", p.StoreName),
	)
	tenantID, locationID, method, err := c.resolver.ResolveStore(ctx, p.StoreID, p.StoreName)
	span.SetAttributes(attribute.String("resolve.method", method))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "store_unresolved")
		return nil, fmt.Errorf("online cast: resolve store id=%d name=%q: %w", p.StoreID, p.StoreName, err)
	}

	span.SetAttributes(
		attribute.Int("order.source.id", p.OrderID),
		attribute.String("order.service_type", p.ServiceType),
		attribute.Int("order.item_count", len(p.Items)),
		attribute.String("tenant.id", uuid.UUID(tenantID).String()),
		attribute.String("location.id", uuid.UUID(locationID).String()),
	)

	placedAt, err := parseVendorTime(p.SubmittedDate)
	if err != nil {
		span.SetStatus(codes.Error, "placed_at_parse")
		return nil, fmt.Errorf("%w: submitted_date %q: %v", ErrTimestampInvalid, p.SubmittedDate, err)
	}

	var promisedAt *time.Time
	if p.DeferredDate != "" {
		t, err := parseVendorTime(p.DeferredDate)
		if err == nil {
			promisedAt = &t
		} else {
			// Non-fatal: a deferred order with an unparseable promise time
			// becomes an ASAP order rather than a rejection.
			c.logger.Warn().Err(err).Str("deferred_date", p.DeferredDate).Msg("ignoring unparseable deferred_date")
		}
	}

	items := buildItems(p.Items)
	if err := c.routeItems(ctx, uuid.UUID(locationID), items); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "routing_failed")
		// Transient (menu lookup) — return the error so SQS redelivers.
		return nil, fmt.Errorf("online cast: route items: %w", err)
	}

	agg, err := order.NewOrder(order.NewOrderInput{
		TenantID:       tenantID,
		LocationID:     locationID,
		Source:         order.SourceOnline,
		SourceEventID:  strconv.Itoa(p.OrderID),
		SourceStatus:   "", // vendor does not send a status
		Customer:       buildCustomer(p.Customer),
		Fulfillment:    buildFulfillment(p.ServiceType, p.DeliveryAddress, promisedAt),
		Items:          items,
		Money:          buildMoney(p),
		Currency:       currencyUSD,
		Payments:       buildPayments(p.Payments),
		PlacedAt:       placedAt,
		Notes:          p.Notes,
		SourceMetadata: buildSourceMetadata(p),
		RawPayload:     raw,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "aggregate_invalid")
		return nil, fmt.Errorf("online cast: build aggregate: %w", err)
	}
	return agg, nil
}

// routeItems stamps kitchen routing (menu item + station printer) onto the
// top-level items, in place. Modifiers inherit their parent's printer at print
// time. A nil router leaves items unrouted (they print as one full receipt).
func (c *Caster) routeItems(ctx context.Context, locationID uuid.UUID, items []order.LineItem) error {
	if c.router == nil || len(items) == 0 {
		return nil
	}
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Name
	}
	routes, err := c.router.RouteItems(ctx, locationID, names)
	if err != nil {
		return err
	}
	if len(routes) != len(items) {
		return fmt.Errorf("router returned %d routes for %d items", len(routes), len(items))
	}
	var unrouted []string
	for i := range items {
		items[i].MenuItemID = routes[i].MenuItemID
		items[i].PrinterID = routes[i].PrinterID
		items[i].Routing = routes[i].Routing
		if routes[i].Routing != order.RoutingMatched {
			unrouted = append(unrouted, items[i].Name)
		}
	}
	if len(unrouted) > 0 {
		c.logger.Warn().Ctx(ctx).Strs("items", unrouted).Str("location_id", locationID.String()).
			Msg("order items not routed to a printer (unmatched name or no printer) — agent prints them on every station")
	}
	return nil
}

// dollarsToCents converts a float dollar amount to integer cents using
// banker's rounding to absorb the float drift that vendors inevitably emit.
// Negative inputs clamp to zero — vendor payloads should never carry
// negative amounts; a negative tip or order_total is a data bug we'd rather
// surface as $0 than silently propagate.
func dollarsToCents(d float64) order.Money {
	if d <= 0 {
		return 0
	}
	return order.Money(math.RoundToEven(d * 100))
}

func buildCustomer(c Customer) order.Customer {
	display := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if display == "" {
		display = c.Company
	}
	return order.Customer{
		DisplayName: display,
		Email:       c.Email,
		Phone:       c.Phone,
	}
}

func buildFulfillment(serviceType string, addr *DeliveryAddress, promisedAt *time.Time) order.Fulfillment {
	f := order.Fulfillment{
		PromisedAt: promisedAt,
	}
	switch strings.ToLower(serviceType) {
	case "delivery":
		f.Type = order.FulfillmentDelivery
	case "dine_in", "dinein":
		f.Type = order.FulfillmentDineIn
	default:
		// "takeout", "pickup", and anything unknown collapse to pickup —
		// the most common vendor case and a safe over-the-counter default.
		f.Type = order.FulfillmentPickup
	}
	if addr != nil && f.Type == order.FulfillmentDelivery {
		f.Address = &order.Address{
			Line1:      strings.TrimSpace(addr.Street),
			Line2:      strings.TrimSpace(addr.Suite),
			City:       addr.City,
			State:      addr.State,
			PostalCode: addr.Zip,
			Country:    "US",
			Notes:      strings.TrimSpace(addr.CrossStreets),
		}
	}
	return f
}

func buildItems(src []Item) []order.LineItem {
	if len(src) == 0 {
		return nil
	}
	items := make([]order.LineItem, 0, len(src))
	for i, it := range src {
		parentID := uuid.Must(uuid.NewV7())
		unit := dollarsToCents(it.Price)
		qty := max(it.Quantity, 1)

		children := make([]order.LineItem, 0, len(it.Modifiers))
		var childTotal order.Money
		for j, m := range it.Modifiers {
			cid := uuid.Must(uuid.NewV7())
			pid := parentID
			modUnit := dollarsToCents(m.Price)
			modQty := max(m.Quantity, 1)
			childLine := modUnit * order.Money(modQty)
			childTotal += childLine
			children = append(children, order.LineItem{
				ID:        cid,
				ParentID:  &pid,
				Position:  j,
				Name:      strings.TrimSpace(m.Name),
				Quantity:  modQty,
				UnitPrice: modUnit,
				LineTotal: childLine,
				Notes:     m.Action,
			})
		}

		lineTotal := unit*order.Money(qty) + childTotal
		items = append(items, order.LineItem{
			ID:        parentID,
			Position:  i,
			SKU:       coalesce(it.PLU, strconv.Itoa(it.ItemID)),
			Name:      strings.TrimSpace(it.Name),
			Size:      strings.TrimSpace(it.SizeName),
			Quantity:  qty,
			UnitPrice: unit,
			LineTotal: lineTotal,
			Notes:     it.Notes,
			Children:  children,
		})
	}
	return items
}

func buildMoney(p Payload) order.MoneyBreakdown {
	var taxCents order.Money
	for _, t := range p.Taxes {
		taxCents += dollarsToCents(t.TaxAmount)
	}

	fees := make([]order.Fee, 0, len(p.MiscCharges))
	for _, mc := range p.MiscCharges {
		fees = append(fees, order.Fee{
			Type:   feeType(mc.MiscChargeName),
			Label:  mc.MiscChargeDesc,
			Amount: dollarsToCents(mc.MiscChargeAmount),
		})
	}

	discounts := make([]order.Discount, 0, len(p.Coupons))
	for _, c := range p.Coupons {
		discounts = append(discounts, order.Discount{
			Type:   "promo",
			Code:   c.Serial,
			Label:  c.Name,
			Amount: dollarsToCents(c.Value),
		})
	}

	total := dollarsToCents(p.OrderTotal)
	tip := dollarsToCents(p.Tip)

	// Subtotal = Total - Tax - Tip - Fees + Discounts. The vendor never sends
	// an explicit subtotal; we derive it so reconcile passes.
	var feeSum order.Money
	for _, f := range fees {
		feeSum += f.Amount
	}
	var discSum order.Money
	for _, d := range discounts {
		discSum += d.Amount
	}
	subtotal := total - taxCents - tip - feeSum + discSum

	return order.MoneyBreakdown{
		Subtotal:  subtotal,
		Tax:       taxCents,
		Tip:       tip,
		Fees:      fees,
		Discounts: discounts,
		Total:     total,
	}
}

func feeType(vendorName string) string {
	switch strings.ToLower(vendorName) {
	case "delivery_charge", "delivery":
		return "delivery"
	case "service_charge", "service":
		return "service"
	default:
		return "other"
	}
}

// buildPayments projects the vendor's raw Payment array onto the canonical
// order.Payment slice. Empty / nil input → nil output, which downstream
// consumers (agent receipt builder, Pushover) interpret as a cash order.
//
// Method classification: any vendor `type` starting with "cash" (case-
// insensitive) maps to processor="cash". Everything else with a card_number
// present maps to "card". Anything else maps to "other" (e.g. gift cards,
// store credit).
func buildPayments(src []Payment) []order.Payment {
	if len(src) == 0 {
		return nil
	}
	out := make([]order.Payment, 0, len(src))
	for _, p := range src {
		processor := "other"
		switch {
		case strings.HasPrefix(strings.ToLower(p.Type), "cash"):
			processor = "cash"
		case strings.TrimSpace(p.CardNumber) != "":
			processor = "card"
		}
		op := order.Payment{
			ID:        uuid.Must(uuid.NewV7()),
			Processor: processor,
			Status:    order.PaymentCaptured,
			Amount:    dollarsToCents(p.Amount),
			Token:     p.Token,
			AuthCode:  p.AuthCode,
		}
		if processor == "card" {
			op.Last4 = lastFour(p.CardNumber)
		}
		out = append(out, op)
	}
	return out
}

// lastFour returns the last 4 digits of a card number. Vendor strings are
// usually pre-masked like "xxxxxxxxxxxxx303" — we keep the trailing digits
// without re-formatting.
func lastFour(card string) string {
	digits := make([]byte, 0, 4)
	for i := len(card) - 1; i >= 0 && len(digits) < 4; i-- {
		c := card[i]
		if c >= '0' && c <= '9' {
			digits = append([]byte{c}, digits...)
		}
	}
	return string(digits)
}

func buildSourceMetadata(p Payload) map[string]any {
	md := map[string]any{
		"tver":            p.TVer,
		"store_id":        p.StoreID,
		"vendor_store_id": p.VendorStoreID,
		"store_name":      p.StoreName,
		"is_tax_exempt":   p.IsTaxExempt,
		"balance_owing":   p.BalanceOwing,
	}
	if p.PrintDate != "" {
		md["print_date"] = p.PrintDate
	}
	if p.DeliveryProvider != nil {
		md["delivery_provider"] = map[string]any{
			"provider_name": p.DeliveryProvider.ProviderName,
			"status":        p.DeliveryProvider.Status,
			"delivery_id":   p.DeliveryProvider.DeliveryID,
			"tracking_url":  p.DeliveryProvider.TrackingURL,
			"pickup_date":   p.DeliveryProvider.PickupDate,
		}
	}
	if len(p.Payments) > 0 {
		// We don't promote vendor payments to first-class order.Payment yet —
		// they carry raw card data the aggregate refuses to model. Keep a
		// summary in metadata so reports can reconcile.
		summary := make([]map[string]any, 0, len(p.Payments))
		for _, pay := range p.Payments {
			summary = append(summary, map[string]any{
				"type":           pay.Type,
				"amount":         pay.Amount,
				"auth_code":      pay.AuthCode,
				"transaction_id": pay.TransactionID,
			})
		}
		md["payments"] = summary
	}
	return md
}

// vendorTimeFormats lists every timestamp shape we have ever seen in a
// real vendor payload, plus the strict RFC3339 forms for resilience. Order
// matters: more-specific formats (with milliseconds, with seconds) come
// first so they win over coarser parses on ambiguous input.
var vendorTimeFormats = []string{
	// vendor empirical forms — numeric offset without a colon
	"2006-01-02T15:04:05.000000-0700",
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05-0700",
	// RFC3339 family — colon offset and/or "Z" UTC marker
	time.RFC3339Nano,
	time.RFC3339,
	// occasional seen-in-the-wild space separator
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
}

// parseVendorTime turns a vendor timestamp string into a time.Time. The
// vendor sends a few different shapes; we try each in turn. Empty strings
// are an explicit error — callers should check before calling for fields
// that can be optional (e.g. deferred_date).
func parseVendorTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	for _, f := range vendorTimeFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unrecognised timestamp format")
}

func coalesce(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
