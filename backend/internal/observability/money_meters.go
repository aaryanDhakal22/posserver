package observability

import (
	"context"
	"strings"
	"sync"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Money instruments (hardening L-3 + R-1a CM-11 / CM-13 / CM-14): card holds,
// the counter's payments and journal, drawers and Z reports, money flags.
// main counts when it applies an event, so a resend counts once. Built on
// first use from the global meter provider (no change to Meters' setup);
// every attribute is bounded, and the store comes from WithLocation.
type moneyInstruments struct {
	cardHolds       metric.Int64Counter
	cardHoldsFailed metric.Int64Counter
	capturedCents   metric.Int64Counter
	refundedCents   metric.Int64Counter
	payments        metric.Int64Counter
	paymentsCents   metric.Int64Counter
	tipsCents       metric.Int64Counter
	journal         metric.Int64Counter
	overShort       metric.Int64Histogram
	zDifference     metric.Int64Gauge
	zStored         metric.Int64Counter
	flags           metric.Int64Counter
}

var (
	moneyOnce sync.Once
	moneyInst moneyInstruments
)

func money() *moneyInstruments {
	moneyOnce.Do(func() { moneyInst = buildMoney(otel.Meter(MeterName)) })
	return &moneyInst
}

func buildMoney(m metric.Meter) moneyInstruments {
	var moneyInst moneyInstruments
	{
		// Errors only come from invalid names; the no-op instrument they
		// return still works, so a bad one can't stop main.
		moneyInst.cardHolds, _ = m.Int64Counter("card_holds.outcomes",
			metric.WithDescription("Card hold work done: result (captured|voided|refunded|failed), op (capture|void|refund|save)"))
		moneyInst.cardHoldsFailed, _ = m.Int64Counter("card_holds.failed",
			metric.WithDescription("Card hold captures / voids / refunds that failed (retried or given up), by op"))
		moneyInst.capturedCents, _ = m.Int64Counter("payments.captured.cents",
			metric.WithDescription("Cents captured on online card holds"))
		moneyInst.refundedCents, _ = m.Int64Counter("payments.refunded.cents",
			metric.WithDescription("Cents refunded on online card holds (cancelled orders)"))
		moneyInst.payments, _ = m.Int64Counter("counter.payments",
			metric.WithDescription("Counter payments main applied, by tender and kind (sale|refund|tip)"))
		moneyInst.paymentsCents, _ = m.Int64Counter("counter.payments.cents",
			metric.WithDescription("Cents of the counter payments main applied (positive per kind)"))
		moneyInst.tipsCents, _ = m.Int64Counter("counter.tips.cents",
			metric.WithDescription("Tip cents on the counter payments main applied"))
		moneyInst.journal, _ = m.Int64Counter("counter.journal.entries",
			metric.WithDescription("Terminal journal entries main stored, by action and role"))
		moneyInst.overShort, _ = m.Int64Histogram("dayclose.drawer.over_short.cents",
			metric.WithDescription("A closed drawer's over (+) / short (-) in cents, by reason and flagged"),
			metric.WithExplicitBucketBoundaries(-10000, -2000, -500, -100, -1, 0, 1, 100, 500, 2000, 10000))
		moneyInst.zDifference, _ = m.Int64Gauge("dayclose.z.difference.cents",
			metric.WithDescription("The last Z report's difference: the Pi's own (source=pi) and main's check (source=main)"))
		moneyInst.zStored, _ = m.Int64Counter("dayclose.z.stored",
			metric.WithDescription("Z reports main stored, by balanced (true|false)"))
		moneyInst.flags, _ = m.Int64Counter("money.flags",
			metric.WithDescription("Money main stored but flagged (or couldn't store under its id), by event type and code"))
	}
	return moneyInst
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// RecordCardHold counts a card hold outcome; cents (> 0) is what moved.
func (Meters) RecordCardHold(ctx context.Context, locationID uuid.UUID, result, op string, cents int64) {
	m := money()
	m.cardHolds.Add(ctx, 1, WithLocation(ctx, locationID, attribute.String("result", result), attribute.String("op", op)))
	switch result {
	case "failed":
		m.cardHoldsFailed.Add(ctx, 1, WithLocation(ctx, locationID, attribute.String("op", op)))
	case "captured":
		m.capturedCents.Add(ctx, abs(cents), WithLocation(ctx, locationID))
	case "refunded":
		m.refundedCents.Add(ctx, abs(cents), WithLocation(ctx, locationID))
	}
}

// RecordCounterPayment counts one applied counter payment (CM-11): kind sale
// (amount > 0), refund (amount < 0) or tip (amount 0, a tip added later).
func (Meters) RecordCounterPayment(ctx context.Context, locationID uuid.UUID, tender string, amount, tip int64) {
	m := money()
	kind := "sale"
	switch {
	case amount < 0:
		kind = "refund"
	case amount == 0:
		kind = "tip"
	}
	attrs := WithLocation(ctx, locationID, attribute.String("tender", BoundedTender(tender)), attribute.String("kind", kind))
	m.payments.Add(ctx, 1, attrs)
	if amount != 0 {
		m.paymentsCents.Add(ctx, abs(amount), attrs)
	}
	if tip > 0 {
		m.tipsCents.Add(ctx, tip, WithLocation(ctx, locationID, attribute.String("tender", BoundedTender(tender))))
	}
}

// BoundedTender keeps the tender attribute to the counter's known methods.
func BoundedTender(t string) string {
	switch t {
	case "cash", "card", "square_app", "manual_card", "comp", "other":
		return t
	}
	return "unknown"
}

// journalActions are the journal actions CM-13 names; anything else is other.
var journalActions = map[string]bool{
	"refund": true, "void": true, "comp": true, "discount": true, "price_change": true, "override": true,
	"employee_meal": true, "drawer.kick": true, "no_sale": true, "login": true, "login_failed": true, "logout": true,
	"payment": true, "order.placed": true, "manager_check": true, "order.picked_up": true, "draft.take_over": true,
	"batch.close": true, "admin_mode": true, "sync.parked": true, "pin_lockout": true, "waste": true,
	"error.printer": true, "error.card": true, "error.pi_unreachable": true,
}

// RecordJournal counts one journal entry main stored (CM-13).
func (Meters) RecordJournal(ctx context.Context, locationID uuid.UUID, action, role string) {
	a := strings.ToLower(action)
	if !journalActions[a] {
		a = "other"
	}
	switch role {
	case "cashier", "manager", "driver":
	case "":
		role = "none"
	default:
		role = "other"
	}
	money().journal.Add(ctx, 1, WithLocation(ctx, locationID, attribute.String("action", a), attribute.String("role", role)))
}

// RecordDrawerClosed records a closed drawer's over / short (CM-14).
func (Meters) RecordDrawerClosed(ctx context.Context, locationID uuid.UUID, reason string, flagged bool, overShort int64) {
	switch reason {
	case "counted", "handover", "batch_close":
	default:
		reason = "other"
	}
	money().overShort.Record(ctx, overShort, WithLocation(ctx, locationID, attribute.String("reason", reason),
		attribute.Bool("flagged", flagged)))
}

// RecordZ records a stored Z (CM-14): the Pi's difference, main's check (nil
// = not checked) and whether both balance.
func (Meters) RecordZ(ctx context.Context, locationID uuid.UUID, piDiff int64, mainDiff *int64) {
	m := money()
	m.zDifference.Record(ctx, piDiff, WithLocation(ctx, locationID, attribute.String("source", "pi")))
	balanced := piDiff == 0
	if mainDiff != nil {
		m.zDifference.Record(ctx, *mainDiff, WithLocation(ctx, locationID, attribute.String("source", "main")))
		balanced = balanced && *mainDiff == 0
	}
	m.zStored.Add(ctx, 1, WithLocation(ctx, locationID, attribute.Bool("balanced", balanced)))
}

// RecordMoneyFlag counts one money flag (M-2 / M-7 / M-12 / M-13).
func (Meters) RecordMoneyFlag(ctx context.Context, locationID uuid.UUID, eventType, code string) {
	money().flags.Add(ctx, 1, WithLocation(ctx, locationID, attribute.String("type", eventType), attribute.String("code", code)))
}
