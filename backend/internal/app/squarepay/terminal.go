package squarepay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Square on the counter terminal (agent contract §2.10r, main contract §2.5e).
// The terminal never holds the store's Square sign-in: the Pi asks main over
// the duplex channel (store-key auth, the scope is the Pi's store) for
//
//   - square.session: the store's current OAuth access token + Square
//     location, for the Square Reader's Mobile Payments SDK (the Pi seals it
//     for the asking terminal; the refresh token never leaves main);
//   - square.keyed_payment: CreatePayment for a card staff typed into Square's
//     card field (the nonce, never a card number; MOTO);
//   - square.refund: RefundPayment on a payment the reader or manual entry
//     took (a manager's refund on the terminal).
//
// Refusals are answers ({ok: false, code}); a network problem or Square's 5xx
// is an error (main answers 500: the outcome is unknown, the Pi says 503 and
// the same idempotency key is safe to send again).

// The agent → main RPCs.
const (
	MsgSquareSession      = "square.session"
	MsgSquareKeyedPayment = "square.keyed_payment"
	MsgSquareRefund       = "square.refund"
)

// Refusal codes.
const (
	CodeNotLinked      = "square_not_linked" // Square isn't connected (or no location picked)
	CodeReconnect      = "square_reconnect"  // connected before the reader's scope: reconnect once
	CodeCardDeclined   = "card_declined"     // the card was refused (square_code says why)
	CodeSquareRefused  = "square_refused"    // Square said no for another reason
	CodeRefundFailed   = "refund_failed"     // the refund was refused
	maxKeyedCents      = 1_000_000           // the Pi's own limit for a payment
	maxKeyedTipCents   = 100_000             //
	maxReferenceID     = 40                  // Square's reference_id limit
	grantCacheFor      = 10 * time.Minute    // a store's OAuth scopes, read again after this
	keyedPaymentNote   = "Counter terminal: card typed in (MOTO)"
	terminalRefundNote = "Counter terminal refund"
)

// grant is a store's OAuth scopes as Square last answered them.
type grant struct {
	scopes []string
	at     time.Time
	token  string // the access token they were read for
}

type grants struct {
	mu sync.Mutex
	m  map[uuid.UUID]grant
}

// Grant reads the scopes of the store's current token (cached for
// grantCacheFor per token). ok = false when Square couldn't say.
func (s *Service) Grant(ctx context.Context, locationID uuid.UUID, token string) ([]string, bool) {
	s.grants.mu.Lock()
	g, hit := s.grants.m[locationID]
	s.grants.mu.Unlock()
	if hit && g.token == token && s.now().Sub(g.at) < grantCacheFor {
		return g.scopes, true
	}
	info, err := s.client.TokenStatus(ctx, token)
	if err != nil {
		s.logger.Warn().Err(err).Str("location_id", locationID.String()).Msg("square token status unavailable")
		return nil, false
	}
	s.grants.mu.Lock()
	if s.grants.m == nil {
		s.grants.m = map[uuid.UUID]grant{}
	}
	s.grants.m[locationID] = grant{scopes: info.Scopes, at: s.now(), token: token}
	s.grants.mu.Unlock()
	return info.Scopes, true
}

func hasScope(scopes []string, want string) bool {
	for _, x := range scopes {
		if x == want {
			return true
		}
	}
	return false
}

// TerminalReply is every terminal RPC's answer: ok + the call's fields, or
// ok: false + code (+ square_code, Square's own).
type TerminalReply struct {
	OK         bool   `json:"ok"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
	SquareCode string `json:"square_code,omitempty"`

	// square.session
	AppID       string     `json:"app_id,omitempty"`
	LocationID  string     `json:"location_id,omitempty"`
	Environment string     `json:"environment,omitempty"`
	AccessToken string     `json:"access_token,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	InPerson    *bool      `json:"in_person,omitempty"`

	// square.keyed_payment
	Payment *KeyedPayment `json:"payment,omitempty"`

	// square.refund
	RefundID string `json:"refund_id,omitempty"`
	Status   string `json:"status,omitempty"`
}

// KeyedPayment is the slice of Square's payment the terminal records.
type KeyedPayment struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Brand         string `json:"brand"`
	Last4         string `json:"last4"`
	AuthCode      string `json:"auth_code"`
	ReceiptNumber string `json:"receipt_number"`
	Entry         string `json:"entry"`
	AmountCents   int64  `json:"amount_cents"`
	TipCents      int64  `json:"tip_cents"`
}

func notLinked(msg string) TerminalReply {
	if msg == "" {
		msg = "This store hasn't connected Square: the owner connects it in Store › Payments."
	}
	return TerminalReply{Code: CodeNotLinked, Error: msg}
}

// terminalConn is the store's token, Square location and token expiry, or a
// refusal (not connected, no location picked).
func (s *Service) terminalConn(ctx context.Context, locationID uuid.UUID) (tok, sqLoc string, expires time.Time, refusal *TerminalReply, err error) {
	tok, sqLoc, err = s.conn(ctx, locationID)
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrDisabled) {
		r := notLinked("")
		return "", "", time.Time{}, &r, nil
	}
	if err != nil {
		return "", "", time.Time{}, nil, err
	}
	if sqLoc == "" {
		r := notLinked("Square is connected but no Square location is picked: the owner picks it in Store › Payments.")
		return "", "", time.Time{}, &r, nil
	}
	if c, gerr := s.repo.Get(ctx, locationID); gerr == nil { // after a refresh: the new expiry
		expires = c.ExpiresAt
	}
	return tok, sqLoc, expires, nil, nil
}

// HandleSession is square.session ({} → the store's Square sign-in for the
// reader). The Pi seals the token for the terminal that asked and keeps it in
// memory only.
func (s *Service) HandleSession(ctx context.Context, scope sseBroker.Scope, _ json.RawMessage) (any, error) {
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Logger()
	tok, sqLoc, expires, refusal, err := s.terminalConn(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		log.Info().Str("code", refusal.Code).Msg("terminal square session refused")
		return *refusal, nil
	}
	r := TerminalReply{OK: true, AppID: s.cfg.AppID, LocationID: sqLoc, Environment: s.cfg.Env, AccessToken: tok}
	if !expires.IsZero() {
		e := expires.UTC()
		r.ExpiresAt = &e
	}
	if s.pastedLocation(ctx, scope.LocationID) {
		in := true // a pasted sandbox token has every permission
		r.InPerson = &in
	} else if scopes, ok := s.Grant(ctx, scope.LocationID, tok); ok {
		in := hasScope(scopes, square.ScopeInPerson)
		r.InPerson = &in
		if !in {
			log.Warn().Msg("terminal square session refused: the grant has no PAYMENTS_WRITE_IN_PERSON")
			return TerminalReply{Code: CodeReconnect, InPerson: &in, Error: "Square was connected before the card reader was allowed: " +
				"the owner reconnects Square once in Store › Payments."}, nil
		}
	}
	log.Info().Str("square_location_id", sqLoc).Msg("terminal square session handed to the Pi") // never the token
	return r, nil
}

type keyedBody struct {
	Nonce          string `json:"nonce"`
	AmountCents    int64  `json:"amount_cents"`
	TipCents       int64  `json:"tip_cents"`
	IdempotencyKey string `json:"idempotency_key"`
	ReferenceID    string `json:"reference_id"`
	OrderID        string `json:"order_id"`
}

// HandleKeyedPayment is square.keyed_payment: CreatePayment for a typed card
// (Square's card field gave the terminal a single-use nonce), completed at
// once, MOTO (customer_details {customer_initiated: false, seller_keyed_in:
// true}). The same idempotency key again is the same payment.
func (s *Service) HandleKeyedPayment(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b keyedBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: square.keyed_payment: %v", rpc.ErrBadPayload, err)
	}
	b.Nonce, b.IdempotencyKey, b.ReferenceID = strings.TrimSpace(b.Nonce), strings.TrimSpace(b.IdempotencyKey), strings.TrimSpace(b.ReferenceID)
	switch {
	case b.Nonce == "" || len(b.Nonce) > 512:
		return nil, fmt.Errorf("%w: square.keyed_payment needs the card field's nonce", rpc.ErrBadPayload)
	case b.AmountCents < 1 || b.AmountCents > maxKeyedCents || b.TipCents < 0 || b.TipCents > maxKeyedTipCents:
		return nil, fmt.Errorf("%w: square.keyed_payment amount_cents 1..%d, tip_cents 0..%d", rpc.ErrBadPayload, maxKeyedCents, maxKeyedTipCents)
	case b.IdempotencyKey == "" || len(b.IdempotencyKey) > maxIdempotencyKey:
		return nil, fmt.Errorf("%w: square.keyed_payment idempotency_key must be 1..%d characters", rpc.ErrBadPayload, maxIdempotencyKey)
	}
	b.ReferenceID = clip(b.ReferenceID, maxReferenceID) // Square's limit: a longer order key still pays
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Int64("amount_cents", b.AmountCents).
		Int64("tip_cents", b.TipCents).Str("reference_id", b.ReferenceID).Str("order_id", clip(b.OrderID, 64)).Logger()
	tok, sqLoc, _, refusal, err := s.terminalConn(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return *refusal, nil
	}
	req := square.CreatePayment{IdempotencyKey: b.IdempotencyKey, SourceID: b.Nonce, AmountMoney: square.USD(b.AmountCents),
		Autocomplete: true, LocationID: sqLoc, ReferenceID: b.ReferenceID, Note: keyedPaymentNote,
		CustomerDetails: &square.CustomerDetails{CustomerInitiated: false, SellerKeyedIn: true}}
	if b.TipCents > 0 {
		req.TipMoney = square.USD(b.TipCents)
	}
	p, err := s.client.CreatePayment(ctx, tok, req)
	if err != nil {
		code := square.CodeOf(err)
		switch {
		case square.Declined(err):
			log.Info().Str("square_code", code).Msg("terminal keyed payment declined")
			return TerminalReply{Code: CodeCardDeclined, SquareCode: code, Error: declineDetail(err)}, nil
		case square.Temporary(err):
			log.Warn().Err(err).Msg("terminal keyed payment: no answer from Square (outcome unknown)")
			return nil, err
		default:
			log.Warn().Str("square_code", code).Msg("terminal keyed payment refused by Square")
			return TerminalReply{Code: CodeSquareRefused, SquareCode: code, Error: declineDetail(err)}, nil
		}
	}
	if p == nil || (p.Status != square.StatusCompleted && p.Status != square.StatusApproved) {
		st := ""
		if p != nil {
			st = p.Status
		}
		log.Info().Str("status", st).Msg("terminal keyed payment not approved")
		return TerminalReply{Code: CodeCardDeclined, SquareCode: st, Error: "Square didn't approve the card."}, nil
	}
	out := &KeyedPayment{ID: p.ID, Status: p.Status, Brand: p.Brand(), Last4: p.Last4(), AuthCode: p.AuthCode(),
		ReceiptNumber: p.ReceiptNumber, Entry: entryOf(p.Entry()), AmountCents: b.AmountCents, TipCents: b.TipCents}
	log.Info().Str("square_payment_id", p.ID).Str("status", p.Status).Msg("terminal keyed payment approved")
	return TerminalReply{OK: true, Payment: out}, nil
}

// entryOf is Square's entry_method in the Pi's words.
func entryOf(sq string) string {
	switch strings.ToUpper(sq) {
	case "KEYED":
		return "keyed"
	case "EMV":
		return "chip"
	case "CONTACTLESS":
		return "contactless"
	case "SWIPED":
		return "swiped"
	case "":
		return "keyed"
	}
	return strings.ToLower(sq)
}

// declineDetail is Square's first error's detail (never card data: Square's
// errors carry none).
func declineDetail(err error) string {
	var ae *square.APIError
	if errors.As(err, &ae) && len(ae.Errors) > 0 && ae.Errors[0].Detail != "" {
		return clip(ae.Errors[0].Detail, 200)
	}
	return "The card was declined."
}

type refundBody struct {
	PaymentID      string `json:"payment_id"`
	AmountCents    int64  `json:"amount_cents"`
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason"`
}

// HandleRefund is square.refund: RefundPayment on a payment the store's
// reader or manual entry took. The payment is read first with the store's own
// token: it must belong to the store's Square location. The Pi records the
// refund only on an ok answer (PENDING / COMPLETED).
func (s *Service) HandleRefund(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b refundBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: square.refund: %v", rpc.ErrBadPayload, err)
	}
	b.PaymentID, b.IdempotencyKey, b.Reason = strings.TrimSpace(b.PaymentID), strings.TrimSpace(b.IdempotencyKey), strings.TrimSpace(b.Reason)
	switch {
	case b.PaymentID == "" || len(b.PaymentID) > 192:
		return nil, fmt.Errorf("%w: square.refund needs a payment_id", rpc.ErrBadPayload)
	case b.AmountCents < 1 || b.AmountCents > maxKeyedCents+maxKeyedTipCents:
		return nil, fmt.Errorf("%w: square.refund amount_cents 1..%d", rpc.ErrBadPayload, maxKeyedCents+maxKeyedTipCents)
	case b.IdempotencyKey == "" || len(b.IdempotencyKey) > maxIdempotencyKey:
		return nil, fmt.Errorf("%w: square.refund idempotency_key must be 1..%d characters", rpc.ErrBadPayload, maxIdempotencyKey)
	}
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Str("square_payment_id", b.PaymentID).
		Int64("amount_cents", b.AmountCents).Logger()
	tok, sqLoc, _, refusal, err := s.terminalConn(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return *refusal, nil
	}
	p, err := s.client.GetPayment(ctx, tok, b.PaymentID)
	if err != nil {
		return refundRefused(log, err, "Square doesn't know this payment for this store.")
	}
	if p.LocationID != "" && p.LocationID != sqLoc {
		log.Warn().Str("payment_location", p.LocationID).Msg("terminal refund refused: another Square location's payment")
		return TerminalReply{Code: CodeRefundFailed, Error: "That payment belongs to another Square location."}, nil
	}
	if p.Status != square.StatusCompleted {
		log.Warn().Str("status", p.Status).Msg("terminal refund refused: the payment isn't completed")
		return TerminalReply{Code: CodeRefundFailed, SquareCode: p.Status, Error: "Square can't refund a payment that is " + strings.ToLower(p.Status) + "."}, nil
	}
	reason := b.Reason
	if reason == "" {
		reason = terminalRefundNote
	}
	ref, err := s.client.RefundPayment(ctx, tok, b.IdempotencyKey, p.ID, square.USD(b.AmountCents), clip(reason, 192))
	if err != nil {
		return refundRefused(log, err, "")
	}
	if ref == nil || ref.Status == "REJECTED" || ref.Status == "FAILED" {
		st := ""
		if ref != nil {
			st = ref.Status
		}
		log.Warn().Str("status", st).Msg("terminal refund not accepted by Square")
		return TerminalReply{Code: CodeRefundFailed, SquareCode: st, Error: "Square didn't accept the refund."}, nil
	}
	log.Info().Str("refund_id", ref.ID).Str("status", ref.Status).Msg("terminal refund given through Square")
	return TerminalReply{OK: true, RefundID: ref.ID, Status: ref.Status}, nil
}

// refundRefused: Square's 4xx is an answer (refund_failed + Square's code); a
// network problem or 5xx is an error (not recorded, the manager tries again
// with the same key).
func refundRefused(log zerolog.Logger, err error, msg string) (any, error) {
	if code := square.StatusOf(err); code >= 400 && code < 500 && !square.Temporary(err) {
		if msg == "" {
			msg = declineDetail(err)
		}
		log.Warn().Str("square_code", square.CodeOf(err)).Msg("terminal refund refused by Square")
		return TerminalReply{Code: CodeRefundFailed, SquareCode: square.CodeOf(err), Error: msg}, nil
	}
	return nil, err
}
