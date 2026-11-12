package squarepay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"
)

// "Send to phone" (agent contract §2.10z, main §2.5zf): a cashier's phone
// charges a counter order with Square's Point of Sale app; the Pi asks main
// for the app's set-up (square.pos_config), to find the payment the phone's
// transaction id names (square.pos_lookup, for the card's brand / last 4 and
// any tip the customer added on Square's screen), and in dev for a fake
// transaction (square.pos_fake_charge). main stores nothing here: the Pi
// records the payment and sends it as order.payment as usual.

const (
	MsgPosConfig     = "square.pos_config"
	MsgPosLookup     = "square.pos_lookup"
	MsgPosFakeCharge = "square.pos_fake_charge"
)

// PosCallbackEnv is the registered HTTPS bounce page Square's app calls back
// (deploy/paybounce in quicc-ops); empty = Send to phone isn't set up.
const PosCallbackEnv = "SQUARE_POS_CALLBACK_URL"

// PosConfigReply is square.pos_config's answer.
type PosConfigReply struct {
	AppID       string `json:"app_id"`
	Environment string `json:"environment"` // production | sandbox | fake | off
	CallbackURL string `json:"callback_url"`
	Linked      bool   `json:"linked"`
	LocationID  string `json:"location_id,omitempty"`
}

// HandlePosConfig is the rpc.InboundHandler for square.pos_config.
func (s *Service) HandlePosConfig(ctx context.Context, scope sseBroker.Scope, _ json.RawMessage) (any, error) {
	out := PosConfigReply{AppID: s.cfg.AppID, Environment: s.cfg.Env, CallbackURL: strings.TrimSpace(os.Getenv(PosCallbackEnv))}
	if out.Environment == "" {
		out.Environment = "off"
	}
	_, sqLoc, err := s.conn(ctx, scope.LocationID)
	switch {
	case err == nil:
		out.Linked, out.LocationID = true, sqLoc
	case errors.Is(err, ErrNotConnected), errors.Is(err, ErrDisabled):
	default:
		return nil, err
	}
	return out, nil
}

// PosPayment is a payment the lookup found.
type PosPayment struct {
	ID            string `json:"id"`
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	Brand         string `json:"brand"`
	Last4         string `json:"last4"`
	Entry         string `json:"entry"`
	AmountCents   int64  `json:"amount_cents"` // the tender's amount (Square's tip included)
	TipCents      int64  `json:"tip_cents"`    // the tip the customer added on Square's screen
	TotalCents    int64  `json:"total_cents"`
	ReceiptNumber string `json:"receipt_number,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

// PosLookupReply is square.pos_lookup's answer.
type PosLookupReply struct {
	Found   bool        `json:"found"`
	Payment *PosPayment `json:"payment,omitempty"`
	Code    string      `json:"code,omitempty"` // not_found | square_not_linked | square_refused | ambiguous
	Error   string      `json:"error,omitempty"`
}

type posLookupBody struct {
	TransactionID string    `json:"transaction_id"`
	Code          string    `json:"code"`
	TotalCents    int64     `json:"total_cents"`
	Since         time.Time `json:"since"`
}

func money(m *square.Money) int64 {
	if m == nil {
		return 0
	}
	return m.Amount
}

// HandlePosLookup is the rpc.InboundHandler for square.pos_lookup: by the
// transaction id (= the order id: RetrieveOrder → its card tender), else by
// the charge's code in the note (or, failing that, the one card payment of
// that total) among the location's payments since `since`.
func (s *Service) HandlePosLookup(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b posLookupBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: square.pos_lookup: %v", rpc.ErrBadPayload, err)
	}
	b.TransactionID, b.Code = strings.TrimSpace(b.TransactionID), strings.TrimSpace(b.Code)
	if (b.TransactionID == "" && b.Code == "") || len(b.TransactionID) > 192 || len(b.Code) > 64 || b.TotalCents < 0 {
		return nil, fmt.Errorf("%w: square.pos_lookup needs a transaction_id or a code", rpc.ErrBadPayload)
	}
	log := s.logger.With().Str("location_id", scope.LocationID.String()).Str("transaction_id", b.TransactionID).Str("code", b.Code).Logger()
	tok, sqLoc, err := s.conn(ctx, scope.LocationID)
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrDisabled) {
		return PosLookupReply{Code: "square_not_linked", Error: "This store hasn't connected Square."}, nil
	}
	if err != nil {
		return nil, err
	}
	if b.TransactionID != "" {
		o, err := s.client.GetOrder(ctx, tok, b.TransactionID)
		if err != nil {
			return lookupRefused(err)
		}
		if o == nil || (o.LocationID != "" && sqLoc != "" && o.LocationID != sqLoc) {
			return PosLookupReply{Code: "not_found"}, nil
		}
		for _, t := range o.Tenders {
			if t.Type != "CARD" && t.CardDetails == nil {
				continue
			}
			p := &PosPayment{ID: t.PaymentID, TransactionID: o.ID, Status: o.State, AmountCents: money(t.AmountMoney),
				TipCents: money(t.TipMoney), TotalCents: money(t.AmountMoney), CreatedAt: o.CreatedAt}
			if p.ID == "" {
				p.ID = t.ID
			}
			if t.CardDetails != nil {
				p.Brand, p.Last4, p.Entry = t.CardDetails.Card.CardBrand, t.CardDetails.Card.Last4, t.CardDetails.EntryMethod
			}
			log.Info().Str("square_payment_id", p.ID).Msg("phone charge found in Square")
			return PosLookupReply{Found: true, Payment: p}, nil
		}
		return PosLookupReply{Code: "not_found"}, nil
	}
	since := b.Since
	if since.IsZero() || s.now().Sub(since) > 24*time.Hour {
		since = s.now().Add(-time.Hour)
	}
	list, err := s.client.ListPayments(ctx, tok, sqLoc, since, s.now().Add(time.Minute))
	if err != nil {
		return lookupRefused(err)
	}
	var byCode, byTotal []square.ListedPayment
	for _, p := range list {
		if p.Status != square.StatusCompleted && p.Status != square.StatusApproved || p.CardDetails == nil {
			continue
		}
		if strings.Contains(p.Note, b.Code) {
			byCode = append(byCode, p)
		} else if b.TotalCents > 0 && (money(p.AmountMoney) == b.TotalCents || money(p.TotalMoney) == b.TotalCents) && p.OrderID != "" {
			byTotal = append(byTotal, p)
		}
	}
	pick := byCode
	if len(pick) == 0 {
		pick = byTotal
	}
	switch len(pick) {
	case 0:
		return PosLookupReply{Code: "not_found"}, nil
	case 1:
	default:
		log.Warn().Int("candidates", len(pick)).Msg("phone charge: several Square payments match")
		return PosLookupReply{Code: "ambiguous", Error: "Several Square payments match: record it by hand."}, nil
	}
	p := pick[0]
	out := &PosPayment{ID: p.ID, TransactionID: p.OrderID, Status: p.Status, Brand: p.CardDetails.Card.CardBrand, Last4: p.CardDetails.Card.Last4,
		Entry: p.CardDetails.EntryMethod, AmountCents: money(p.AmountMoney), TipCents: money(p.TipMoney), TotalCents: money(p.TotalMoney),
		ReceiptNumber: p.ReceiptNumber, CreatedAt: p.CreatedAt}
	log.Info().Str("square_payment_id", p.ID).Msg("phone charge found in Square by its note / total")
	return PosLookupReply{Found: true, Payment: out}, nil
}

func lookupRefused(err error) (any, error) {
	if code := square.StatusOf(err); code == 404 {
		return PosLookupReply{Code: "not_found"}, nil
	} else if code >= 400 && code < 500 {
		return PosLookupReply{Code: "square_refused", Error: square.CodeOf(err)}, nil
	}
	return nil, err
}

// HandlePosFakeCharge is the rpc.InboundHandler for square.pos_fake_charge
// (SQUARE_ENV=fake only): the Pi's fake Square POS page makes its
// transaction here so dev's lookups find it.
func (s *Service) HandlePosFakeCharge(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	if s.cfg.Env != "fake" {
		return map[string]any{"ok": false, "code": "not_fake"}, nil
	}
	var b struct {
		TotalCents int64  `json:"total_cents"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal(payload, &b); err != nil || b.TotalCents < 1 || b.TotalCents > 10_000_000 || len(b.Note) > 500 {
		return nil, fmt.Errorf("%w: square.pos_fake_charge needs total_cents", rpc.ErrBadPayload)
	}
	tok, sqLoc, err := s.conn(ctx, scope.LocationID)
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrDisabled) {
		return map[string]any{"ok": false, "code": "square_not_linked"}, nil
	}
	if err != nil {
		return nil, err
	}
	tx, client, err := s.client.FakePOSCharge(ctx, tok, sqLoc, b.TotalCents, b.Note)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "transaction_id": tx, "client_transaction_id": client}, nil
}
