package floor

import (
	"context"
	"encoding/json"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
)

// EventOrderPrinted is the Pi's order.printed (agent contract §2.10ag, main
// §2.5zn): what one print job of an order printed, per ticket and printer.
const EventOrderPrinted = "order.printed"

// PrintStore keeps print jobs (repositories.OrderPrintStore): marks the
// event seen and stores the job in one tx — applied, duplicate, or
// not_found (no such order in the store).
type PrintStore interface {
	SaveOrderPrint(ctx context.Context, sc order.Scope, eventID, orderID uuid.UUID, p order.OrderPrint) (string, error)
}

// OnPrints plugs in the print store (nil = order.printed is answered invalid).
func (s *Service) OnPrints(p PrintStore) { s.prints = p }

// applyPrinted: {order_id, mode, tickets[], at} → order_prints. Append
// only: each job is its own row, a replay is a duplicate.
func (s *Service) applyPrinted(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	oid, err := uuid.Parse(ev.OrderID)
	if s.prints == nil || err != nil || len(ev.Tickets) == 0 || ev.At.IsZero() {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	var tickets []order.PrintTicket
	if err := json.Unmarshal(ev.Tickets, &tickets); err != nil {
		return s.seen(ctx, sc, eid, &oid, ev.Type, order.FloorInvalid)
	}
	mode, tickets, err := order.CheckPrintJob(ev.Mode, tickets, ev.At)
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Str("event_id", ev.EventID).Msg("order.printed refused")
		return s.seen(ctx, sc, eid, &oid, ev.Type, order.FloorInvalid)
	}
	return s.prints.SaveOrderPrint(ctx, sc, eid, oid, order.OrderPrint{EventID: eid.String(), Mode: mode, At: ev.At, Tickets: tickets})
}
