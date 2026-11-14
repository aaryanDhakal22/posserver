package squarepay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/rs/zerolog"
)

// A general refund to a card (batch J, the owner 2026-11-14; agent contract
// §2.10x, main contract §2.5e): money back on a card with no order.
//
//   - square.general_refund: Square's **unlinked refund** (RefundPayment with
//     no payment_id, unlinked: true, destination_id = the token Square's card
//     field gave the terminal, the store's Square location). Proven on Square's
//     sandbox 2026-11-14.
//   - square.card_payments: the fallback (a seller Square won't make unlinked
//     refunds for, or no card at hand): the store's Square card payments of the
//     last 30 days with the card's last 4 that still hold the amount; the
//     manager picks one and the Pi refunds it with the ordinary square.refund.
//
// Refusals are answers ({ok: false, code}); no answer / Square's 5xx is an
// error (500: the outcome is unknown, the same idempotency key is the same
// refund). The token is never logged.

// The RPCs.
const (
	MsgSquareGeneralRefund = "square.general_refund"
	MsgSquareCardPayments  = "square.card_payments"
)

const (
	// CodeUnlinkedUnsupported: Square refuses unlinked refunds for this
	// seller: the terminal offers the linked fallback.
	CodeUnlinkedUnsupported = "unlinked_unsupported"
	generalRefundNote       = "Counter terminal general refund"
	cardPaymentsDays        = 30
	cardPaymentsPages       = 5
	cardPaymentsMax         = 20
)

var last4Re = regexp.MustCompile(`^[0-9]{4}$`)

type generalRefundBody struct {
	Nonce          string `json:"nonce"`
	AmountCents    int64  `json:"amount_cents"`
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason"`
}

// GeneralRefundReply is square.general_refund's answer.
type GeneralRefundReply struct {
	OK         bool   `json:"ok"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
	SquareCode string `json:"square_code,omitempty"`
	RefundID   string `json:"refund_id,omitempty"`
	Status     string `json:"status,omitempty"`
	Brand      string `json:"brand,omitempty"`
	Last4      string `json:"last4,omitempty"`
	Unlinked   bool   `json:"unlinked,omitempty"`
}

// unlinkedRefused: Square's answer means it won't make unlinked refunds for
// this seller (not a problem with this card or amount).
func unlinkedRefused(err error) bool {
	var ae *square.APIError
	if !errors.As(err, &ae) {
		return false
	}
	for _, e := range ae.Errors {
		c := strings.ToUpper(e.Code)
		if strings.HasPrefix(c, "UNSUPPORTED") || c == "NOT_SUPPORTED" || c == "FORBIDDEN" {
			return true
		}
		if strings.Contains(strings.ToLower(e.Field+" "+e.Detail), "unlinked") {
			return true
		}
	}
	return ae.Status == 403
}

// HandleGeneralRefund is square.general_refund (see the file comment).
func (s *Service) HandleGeneralRefund(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b generalRefundBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: square.general_refund: %v", rpc.ErrBadPayload, err)
	}
	b.Nonce, b.IdempotencyKey, b.Reason = strings.TrimSpace(b.Nonce), strings.TrimSpace(b.IdempotencyKey), strings.TrimSpace(b.Reason)
	switch {
	case b.Nonce == "" || len(b.Nonce) > 512:
		return nil, fmt.Errorf("%w: square.general_refund needs the card field's nonce", rpc.ErrBadPayload)
	case b.AmountCents < 1 || b.AmountCents > maxKeyedCents:
		return nil, fmt.Errorf("%w: square.general_refund amount_cents 1..%d", rpc.ErrBadPayload, maxKeyedCents)
	case b.IdempotencyKey == "" || len(b.IdempotencyKey) > maxIdempotencyKey:
		return nil, fmt.Errorf("%w: square.general_refund idempotency_key must be 1..%d characters", rpc.ErrBadPayload, maxIdempotencyKey)
	}
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Int64("amount_cents", b.AmountCents).
		Str("idempotency_key", b.IdempotencyKey).Logger() // never the nonce
	tok, sqLoc, _, refusal, err := s.terminalConn(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return GeneralRefundReply{Code: refusal.Code, Error: refusal.Error}, nil
	}
	reason := b.Reason
	if reason == "" {
		reason = generalRefundNote
	}
	ref, err := s.client.RefundUnlinked(ctx, tok, b.IdempotencyKey, sqLoc, b.Nonce, square.USD(b.AmountCents), clip(reason, 192))
	if err != nil {
		return generalRefundRefused(log, err)
	}
	if ref == nil || ref.ID == "" || ref.Status == "REJECTED" || ref.Status == "FAILED" {
		st := ""
		if ref != nil {
			st = ref.Status
		}
		log.Warn().Str("status", st).Msg("general card refund not accepted by Square")
		return GeneralRefundReply{Code: CodeRefundFailed, SquareCode: st, Error: "Square didn't accept the refund."}, nil
	}
	out := GeneralRefundReply{OK: true, RefundID: ref.ID, Status: ref.Status, Unlinked: true}
	if c := ref.Card(); c != nil {
		out.Brand, out.Last4 = c.Card.CardBrand, c.Card.Last4
	}
	log.Info().Str("refund_id", ref.ID).Str("status", ref.Status).Msg("general card refund given through Square (unlinked)")
	return out, nil
}

// generalRefundRefused: Square's 4xx is an answer (unlinked_unsupported or
// refund_failed + Square's code); a network problem / 5xx / 429 is an error.
func generalRefundRefused(log zerolog.Logger, err error) (any, error) {
	code := square.StatusOf(err)
	if code < 400 || code >= 500 || square.Temporary(err) {
		log.Warn().Err(err).Msg("general card refund: no answer from Square (outcome unknown)")
		return nil, err
	}
	if unlinkedRefused(err) {
		log.Warn().Str("square_code", square.CodeOf(err)).Msg("general card refund: Square makes no unlinked refunds for this store")
		return GeneralRefundReply{Code: CodeUnlinkedUnsupported, SquareCode: square.CodeOf(err),
			Error: "Square doesn't give money back to a card without its payment for this store: find the card's payment instead."}, nil
	}
	log.Warn().Str("square_code", square.CodeOf(err)).Msg("general card refund refused by Square")
	return GeneralRefundReply{Code: CodeRefundFailed, SquareCode: square.CodeOf(err), Error: declineDetail(err)}, nil
}

type cardPaymentsBody struct {
	Last4       string `json:"last4"`
	AmountCents int64  `json:"amount_cents"`
}

// CardPayment is one listed payment (square.card_payments).
type CardPayment struct {
	SquarePaymentID string    `json:"square_payment_id"`
	At              time.Time `json:"at"`
	AmountCents     int64     `json:"amount_cents"`
	RefundableCents int64     `json:"refundable_cents"`
	Brand           string    `json:"brand"`
	Last4           string    `json:"last4"`
	Entry           string    `json:"entry"`
	ReceiptNumber   string    `json:"receipt_number,omitempty"`
}

// CardPaymentsReply is square.card_payments's answer.
type CardPaymentsReply struct {
	OK         bool          `json:"ok"`
	Code       string        `json:"code,omitempty"`
	Error      string        `json:"error,omitempty"`
	SquareCode string        `json:"square_code,omitempty"`
	Payments   []CardPayment `json:"payments"`
}

func total(p square.ListedPayment) int64 {
	switch {
	case p.TotalMoney != nil:
		return p.TotalMoney.Amount
	case p.AmountMoney != nil:
		t := p.AmountMoney.Amount
		if p.TipMoney != nil {
			t += p.TipMoney.Amount
		}
		return t
	}
	return 0
}

// HandleCardPayments is square.card_payments (see the file comment).
func (s *Service) HandleCardPayments(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b cardPaymentsBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: square.card_payments: %v", rpc.ErrBadPayload, err)
	}
	b.Last4 = strings.TrimSpace(b.Last4)
	switch {
	case !last4Re.MatchString(b.Last4):
		return nil, fmt.Errorf("%w: square.card_payments last4 is 4 digits", rpc.ErrBadPayload)
	case b.AmountCents < 1 || b.AmountCents > maxKeyedCents+maxKeyedTipCents:
		return nil, fmt.Errorf("%w: square.card_payments amount_cents 1..%d", rpc.ErrBadPayload, maxKeyedCents+maxKeyedTipCents)
	}
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Int64("amount_cents", b.AmountCents).Logger()
	tok, sqLoc, _, refusal, err := s.terminalConn(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return CardPaymentsReply{Code: refusal.Code, Error: refusal.Error, Payments: []CardPayment{}}, nil
	}
	end := s.now()
	begin := end.Add(-cardPaymentsDays * 24 * time.Hour)
	out := []CardPayment{}
	cursor := ""
	for page := 0; page < cardPaymentsPages && len(out) < cardPaymentsMax; page++ {
		list, next, err := s.client.ListCardPayments(ctx, tok, sqLoc, b.Last4, begin, end.Add(time.Minute), cursor)
		if err != nil {
			if c := square.StatusOf(err); c >= 400 && c < 500 && !square.Temporary(err) {
				log.Warn().Str("square_code", square.CodeOf(err)).Msg("card payments: Square refused the list")
				return CardPaymentsReply{Code: CodeSquareRefused, SquareCode: square.CodeOf(err), Error: declineDetail(err), Payments: []CardPayment{}}, nil
			}
			log.Warn().Err(err).Msg("card payments: no answer from Square")
			return nil, err
		}
		for _, p := range list {
			if p.Status != square.StatusCompleted || p.CardDetails == nil || p.CardDetails.Card.Last4 != b.Last4 ||
				(p.LocationID != "" && p.LocationID != sqLoc) {
				continue
			}
			t := total(p)
			left := t
			if p.RefundedMoney != nil {
				left -= p.RefundedMoney.Amount
			}
			if left < b.AmountCents {
				continue
			}
			at, _ := time.Parse(time.RFC3339Nano, p.CreatedAt)
			out = append(out, CardPayment{SquarePaymentID: p.ID, At: at.UTC(), AmountCents: t, RefundableCents: left,
				Brand: p.CardDetails.Card.CardBrand, Last4: p.CardDetails.Card.Last4, Entry: entryOf(p.CardDetails.EntryMethod),
				ReceiptNumber: p.ReceiptNumber})
			if len(out) >= cardPaymentsMax {
				break
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	log.Info().Int("found", len(out)).Msg("card payments listed for a general card refund")
	return CardPaymentsReply{OK: true, Payments: out}, nil
}
