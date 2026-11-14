package squarepay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"
)

// square.tip_adjust (batch F, 2026-11-14; agent contract §2.10am, main
// contract §2.5e): at the press of Close batch the Pi asks main to put a
// driver's card tip, typed before the close, on the counter's Square
// payment. tip_cents is the payment's WHOLE tip. Square changes a tip only
// while the payment is APPROVED (a delayed capture): a completed payment is
// answered not_adjustable and the Pi pages the floor.

// MsgSquareTipAdjust is the RPC's type.
const MsgSquareTipAdjust = "square.tip_adjust"

// CodeNotAdjustable: Square takes no tip change on this payment.
const CodeNotAdjustable = "not_adjustable"

type tipAdjustBody struct {
	SquarePaymentID string `json:"square_payment_id"`
	TipCents        int64  `json:"tip_cents"`
	IdempotencyKey  string `json:"idempotency_key"`
	OrderID         string `json:"order_id"`
}

// TipAdjustReply is square.tip_adjust's answer.
type TipAdjustReply struct {
	TerminalReply
	TipCents int64 `json:"tip_cents,omitempty"`
}

// HandleTipAdjust sets a store's Square payment's whole tip when Square lets it.
func (s *Service) HandleTipAdjust(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b tipAdjustBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: square.tip_adjust: %v", rpc.ErrBadPayload, err)
	}
	b.SquarePaymentID, b.IdempotencyKey = strings.TrimSpace(b.SquarePaymentID), strings.TrimSpace(b.IdempotencyKey)
	switch {
	case b.SquarePaymentID == "" || len(b.SquarePaymentID) > 192:
		return nil, fmt.Errorf("%w: square.tip_adjust needs a square_payment_id", rpc.ErrBadPayload)
	case b.TipCents < 0 || b.TipCents > maxKeyedTipCents:
		return nil, fmt.Errorf("%w: square.tip_adjust tip_cents 0..%d", rpc.ErrBadPayload, maxKeyedTipCents)
	case b.IdempotencyKey == "" || len(b.IdempotencyKey) > maxIdempotencyKey:
		return nil, fmt.Errorf("%w: square.tip_adjust idempotency_key must be 1..%d characters", rpc.ErrBadPayload, maxIdempotencyKey)
	}
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Str("square_payment_id", b.SquarePaymentID).
		Str("order_id", b.OrderID).Int64("tip_cents", b.TipCents).Logger()
	tok, sqLoc, _, refusal, err := s.terminalConn(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return TipAdjustReply{TerminalReply: *refusal}, nil
	}
	refused := func(code, sqCode, msg string) (any, error) {
		log.Warn().Str("code", code).Str("square_code", sqCode).Msg("driver tip not put on the Square payment")
		return TipAdjustReply{TerminalReply: TerminalReply{Code: code, SquareCode: sqCode, Error: msg}}, nil
	}
	p, err := s.client.GetPayment(ctx, tok, b.SquarePaymentID)
	if err != nil {
		if code := square.StatusOf(err); code >= 400 && code < 500 && !square.Temporary(err) {
			return refused("square_refused", square.CodeOf(err), "Square doesn't know this payment for this store.")
		}
		return nil, err
	}
	if p.LocationID != "" && p.LocationID != sqLoc {
		return refused("square_refused", "", "That payment belongs to another Square location.")
	}
	if currentTip(p) == b.TipCents {
		return TipAdjustReply{TerminalReply: TerminalReply{OK: true, Status: p.Status}, TipCents: b.TipCents}, nil
	}
	if p.Status != square.StatusApproved {
		return refused(CodeNotAdjustable, p.Status, "Square takes no tip change on a payment that is "+strings.ToLower(p.Status)+".")
	}
	if b.TipCents > currentTip(p) && !p.Can(square.CapTipUp) {
		return refused(CodeNotAdjustable, "EDIT_TIP_AMOUNT_UP", "Square won't raise the tip on this payment.")
	}
	got, err := s.client.UpdatePayment(ctx, tok, p.ID, b.IdempotencyKey, nil, square.USD(b.TipCents))
	if err != nil {
		if code := square.StatusOf(err); code >= 400 && code < 500 && !square.Temporary(err) {
			return refused(CodeNotAdjustable, square.CodeOf(err), declineDetail(err))
		}
		return nil, err
	}
	st := p.Status
	if got != nil {
		st = got.Status
	}
	log.Info().Msg("driver tip put on the Square payment")
	return TipAdjustReply{TerminalReply: TerminalReply{OK: true, Status: st}, TipCents: b.TipCents}, nil
}
