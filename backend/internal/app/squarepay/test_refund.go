package squarepay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/rs/zerolog"
)

// The counter terminal's setup test charge (TERMINAL-SETUP-PLAN, owner Q3:
// "$1 test charge, refunded at once by the terminal"): the terminal charges a
// real card on the reader with no order, then asks the Pi to give it back; the
// Pi relays it here as the agent → main RPC square.test_refund. There is no
// order: only a payment of at most MaxTestRefundCents can be refunded this way,
// so the route can never be used to refund a sale.

// MsgSquareTestRefund is the agent's RPC request for the test refund.
const MsgSquareTestRefund = "square.test_refund"

// MaxTestRefundCents caps both the refund and the payment it refunds (the $1
// test charge).
const MaxTestRefundCents = 100

// maxIdempotencyKey is Square's limit for a refund's idempotency_key.
const maxIdempotencyKey = 45

type testRefundBody struct {
	SquarePaymentID string `json:"square_payment_id"`
	AmountCents     int64  `json:"amount_cents"`
	IdempotencyKey  string `json:"idempotency_key"`
}

// TestRefundReply is square.test_refund's answer: {ok, refund_id, status} or
// {ok: false, code, error}. Codes: square_not_linked (the Pi answers 409),
// not_a_test_charge (the payment is over MaxTestRefundCents or another
// location's), square_refused (Square said no; square_code is Square's).
type TestRefundReply struct {
	OK         bool   `json:"ok"`
	RefundID   string `json:"refund_id,omitempty"`
	Status     string `json:"status,omitempty"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
	SquareCode string `json:"square_code,omitempty"`
}

// HandleTestRefund is the rpc.InboundHandler for square.test_refund:
// {square_payment_id, amount_cents (1..100), idempotency_key (1..45)} →
// TestRefundReply. A bad body is rpc.ErrBadPayload (the Pi gets a 400); a
// payment still APPROVED (not captured) is cancelled instead of refunded.
func (s *Service) HandleTestRefund(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b testRefundBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: square.test_refund: %v", rpc.ErrBadPayload, err)
	}
	b.SquarePaymentID = strings.TrimSpace(b.SquarePaymentID)
	b.IdempotencyKey = strings.TrimSpace(b.IdempotencyKey)
	switch {
	case b.SquarePaymentID == "" || len(b.SquarePaymentID) > 192:
		return nil, fmt.Errorf("%w: square.test_refund needs a square_payment_id", rpc.ErrBadPayload)
	case b.AmountCents < 1 || b.AmountCents > MaxTestRefundCents:
		return nil, fmt.Errorf("%w: square.test_refund amount_cents must be 1..%d", rpc.ErrBadPayload, MaxTestRefundCents)
	case b.IdempotencyKey == "" || len(b.IdempotencyKey) > maxIdempotencyKey:
		return nil, fmt.Errorf("%w: square.test_refund idempotency_key must be 1..%d characters", rpc.ErrBadPayload, maxIdempotencyKey)
	}
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Str("square_payment_id", b.SquarePaymentID).
		Int64("amount_cents", b.AmountCents).Logger()

	tok, sqLoc, err := s.conn(ctx, scope.LocationID)
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrDisabled) {
		log.Info().Msg("terminal test refund: Square not linked")
		return TestRefundReply{Code: "square_not_linked", Error: "This store hasn't connected Square."}, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := s.client.GetPayment(ctx, tok, b.SquarePaymentID)
	if err != nil {
		if code := square.StatusOf(err); code >= 400 && code < 500 {
			log.Warn().Str("square_code", square.CodeOf(err)).Msg("terminal test refund: payment not found")
			return TestRefundReply{Code: "not_a_test_charge", Error: "Square doesn't know this payment for this store.", SquareCode: square.CodeOf(err)}, nil
		}
		return nil, err
	}
	total := int64(0)
	if p.TotalMoney != nil {
		total = p.TotalMoney.Amount
	} else if p.AmountMoney != nil {
		total = p.AmountMoney.Amount
	}
	if total > MaxTestRefundCents || (p.LocationID != "" && sqLoc != "" && p.LocationID != sqLoc) {
		log.Warn().Int64("payment_cents", total).Msg("terminal test refund refused: not a test charge")
		return TestRefundReply{Code: "not_a_test_charge", Error: "Only the setup test charge can be refunded this way."}, nil
	}
	switch p.Status {
	case square.StatusApproved: // not captured yet: cancelling gives it all back
		if _, err := s.client.CancelPayment(ctx, tok, p.ID); err != nil {
			return refused(log, err)
		}
		log.Info().Msg("terminal test charge cancelled")
		return TestRefundReply{OK: true, Status: "CANCELED"}, nil
	case square.StatusCanceled, square.StatusFailed:
		return TestRefundReply{OK: true, Status: p.Status}, nil
	}
	ref, err := s.client.RefundPayment(ctx, tok, b.IdempotencyKey, p.ID, square.USD(b.AmountCents), "setup test charge")
	if err != nil {
		return refused(log, err)
	}
	log.Info().Str("refund_id", ref.ID).Str("status", ref.Status).Msg("terminal test charge refunded")
	return TestRefundReply{OK: true, RefundID: ref.ID, Status: ref.Status}, nil
}

// refused: a 4xx from Square is an answer (the Pi shows Square's code); a
// network problem or a 5xx is an error (the Pi answers 502, the terminal
// tries again with the same idempotency key).
func refused(log zerolog.Logger, err error) (any, error) {
	if code := square.StatusOf(err); code >= 400 && code < 500 {
		log.Warn().Str("square_code", square.CodeOf(err)).Msg("terminal test refund refused by Square")
		return TestRefundReply{Code: "square_refused", Error: "Square refused the refund.", SquareCode: square.CodeOf(err)}, nil
	}
	return nil, err
}
