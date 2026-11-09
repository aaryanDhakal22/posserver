package squarepay

import (
	"context"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/payment"
	"quiccpos/main/internal/infra/square"

	"github.com/google/uuid"
)

// GatewayName is how Square payments are labelled (payment refs, holds).
const GatewayName = "square"

// Gateway takes a store's online card payments through its Square account
// (payment.Holder): a charge is a hold, captured later.
type Gateway struct{ s *Service }

func (s *Service) Gateway() *Gateway { return &Gateway{s: s} }

var _ payment.Holder = (*Gateway)(nil)

func (g *Gateway) client() *square.Client { return g.s.client }

// connErr maps a connection problem to the payment port's errors.
func connErr(err error) error {
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrDisabled) {
		return payment.ErrNotConnected
	}
	return err
}

// Charge authorizes the order's total on the card (a hold, captured later).
func (g *Gateway) Charge(ctx context.Context, c payment.Charge) (payment.Result, error) {
	tok, sqLoc, err := g.s.conn(ctx, c.LocationID)
	if err != nil {
		return payment.Result{}, connErr(err)
	}
	if sqLoc == "" {
		return payment.Result{}, payment.ErrNotConnected
	}
	if c.Token == "" {
		return payment.Result{}, payment.ErrBadToken
	}
	idem := key("hold", c.IdempotencyKey)
	req := square.CreatePayment{IdempotencyKey: idem, SourceID: c.Token, AmountMoney: square.USD(c.AmountCents - c.TipCents),
		Autocomplete: false, LocationID: sqLoc, ReferenceID: clip(c.ReferenceID, 40), Note: clip(c.Note, 500),
		BuyerEmailAddress: clip(c.BuyerEmail, 255), VerificationToken: c.VerificationToken}
	if c.TipCents > 0 {
		req.TipMoney = square.USD(c.TipCents)
	}
	p, err := g.client().CreatePayment(ctx, tok, req)
	switch {
	case err == nil:
	case square.CodeOf(err) == "CARD_DECLINED_VERIFICATION_REQUIRED":
		return payment.Result{}, payment.ErrVerify
	case square.Declined(err):
		return payment.Result{}, payment.ErrDeclined
	case square.CodeOf(err) == "INVALID_CARD_DATA" || square.CodeOf(err) == "CARD_TOKEN_USED" || square.CodeOf(err) == "CARD_TOKEN_EXPIRED":
		return payment.Result{}, payment.ErrBadToken
	case square.Temporary(err):
		// We don't know whether Square made the hold: void whatever this key made.
		if cerr := g.client().CancelByIdempotencyKey(context.WithoutCancel(ctx), tok, idem); cerr != nil {
			g.s.logger.Error().Err(cerr).Str("location_id", c.LocationID.String()).Msg("square: couldn't void an unknown hold")
		}
		return payment.Result{}, err
	default:
		return payment.Result{}, err
	}
	status := payment.StatusAuthorized
	if p.Status == square.StatusCompleted {
		status = payment.StatusCaptured
	}
	return payment.Result{Gateway: GatewayName, Ref: p.ID, Status: status, Last4: p.Last4(), Brand: p.Brand(), AuthCode: p.AuthCode()}, nil
}

// Refund gives money back: a hold is voided, a completed payment refunded.
func (g *Gateway) Refund(ctx context.Context, locationID uuid.UUID, ref string, amountCents int64) error {
	tok, _, err := g.s.conn(ctx, locationID)
	if err != nil {
		return connErr(err)
	}
	p, err := g.client().GetPayment(ctx, tok, ref)
	if err != nil {
		return err
	}
	switch p.Status {
	case square.StatusApproved:
		_, err = g.client().CancelPayment(ctx, tok, ref)
		return err
	case square.StatusCanceled, square.StatusFailed:
		return nil
	}
	_, err = g.client().RefundPayment(ctx, tok, key("refund", ref, fmt.Sprint(amountCents)), ref, square.USD(amountCents), "order refunded")
	return err
}

// RaiseTip sets the hold's tip when Square lets it grow.
func (g *Gateway) RaiseTip(ctx context.Context, h payment.Hold, tipCents int64) error {
	tok, _, err := g.s.conn(ctx, h.LocationID)
	if err != nil {
		return connErr(err)
	}
	p, err := g.client().GetPayment(ctx, tok, h.Ref)
	if err != nil {
		return err
	}
	switch {
	case currentTip(p) == tipCents && p.Status != square.StatusCanceled && p.Status != square.StatusFailed:
		return nil // already there (a capture retried after a crash)
	case p.Status == square.StatusCanceled || p.Status == square.StatusFailed:
		return payment.ErrHoldGone
	case p.Status != square.StatusApproved:
		return payment.Permanent{Err: fmt.Errorf("square payment %s is %s", h.Ref, p.Status)}
	case tipCents > currentTip(p) && !p.Can(square.CapTipUp):
		return payment.ErrCantRaise
	}
	_, err = g.client().UpdatePayment(ctx, tok, h.Ref, key("tip", h.Ref, fmt.Sprint(tipCents)), nil, square.USD(tipCents))
	if square.CodeOf(err) == "AMOUNT_TOO_HIGH" || square.CodeOf(err) == "PAYMENT_LIMIT_EXCEEDED" {
		return payment.ErrCantRaise
	}
	return err
}

func currentTip(p *square.Payment) int64 {
	if p.TipMoney == nil {
		return 0
	}
	return p.TipMoney.Amount
}

// Complete captures the hold and returns the total charged.
func (g *Gateway) Complete(ctx context.Context, h payment.Hold) (int64, error) {
	tok, _, err := g.s.conn(ctx, h.LocationID)
	if err != nil {
		return 0, connErr(err)
	}
	p, err := g.client().CompletePayment(ctx, tok, h.Ref)
	if err != nil && !square.Temporary(err) {
		// Already captured, or voided / expired at Square?
		if cur, gerr := g.client().GetPayment(ctx, tok, h.Ref); gerr == nil {
			switch cur.Status {
			case square.StatusCompleted:
				p, err = cur, nil
			case square.StatusCanceled, square.StatusFailed:
				return 0, payment.ErrHoldGone
			}
		}
	}
	if err != nil {
		return 0, err
	}
	if p.TotalMoney != nil {
		return p.TotalMoney.Amount, nil
	}
	return h.AmountCents + h.TipCents, nil
}

// Void cancels a hold.
func (g *Gateway) Void(ctx context.Context, h payment.Hold) error {
	tok, _, err := g.s.conn(ctx, h.LocationID)
	if err != nil {
		return connErr(err)
	}
	_, err = g.client().CancelPayment(ctx, tok, h.Ref)
	if err != nil && !square.Temporary(err) {
		if cur, gerr := g.client().GetPayment(ctx, tok, h.Ref); gerr == nil {
			switch cur.Status {
			case square.StatusCanceled, square.StatusFailed:
				return nil
			case square.StatusCompleted:
				return payment.Permanent{Err: fmt.Errorf("square payment %s was already captured — refund it instead", h.Ref)}
			}
		}
	}
	return err
}

// ChargeTip saves the card behind the completed hold for this order, charges
// the tip on it and turns the card off again.
func (g *Gateway) ChargeTip(ctx context.Context, h payment.Hold, cents int64, b payment.Buyer) (string, error) {
	tok, sqLoc, err := g.s.conn(ctx, h.LocationID)
	if err != nil {
		return "", connErr(err)
	}
	oid := h.OrderID.String()
	cu, err := g.client().CreateCustomer(ctx, tok, key("customer", oid), square.Customer{GivenName: clip(b.Name, 100),
		PhoneNumber: b.Phone, EmailAddress: b.Email, ReferenceID: clip(oid, 40), Note: "Saved for one order's tip"})
	if err != nil {
		if square.StatusOf(err) == 400 && b.Phone != "" { // Square refuses some phone formats: try without
			cu, err = g.client().CreateCustomer(ctx, tok, key("customer", oid, "nophone"), square.Customer{GivenName: clip(b.Name, 100),
				EmailAddress: b.Email, ReferenceID: clip(oid, 40), Note: "Saved for one order's tip"})
		}
		if err != nil {
			return "", err
		}
	}
	card, err := g.client().CreateCard(ctx, tok, key("card", oid), h.Ref, cu.ID)
	if err != nil {
		if !square.Temporary(err) {
			return "", payment.Permanent{Err: fmt.Errorf("couldn't save the card: %w", err)}
		}
		return "", err
	}
	defer func() {
		if derr := g.client().DisableCard(context.WithoutCancel(ctx), tok, card.ID); derr != nil {
			g.s.logger.Warn().Err(derr).Str("order_id", oid).Msg("square: couldn't turn the saved card off")
		}
	}()
	p, err := g.client().CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: key("tipcharge", oid), SourceID: card.ID,
		CustomerID: cu.ID, AmountMoney: square.USD(cents), Autocomplete: true, LocationID: sqLoc,
		ReferenceID: clip(oid, 40), Note: "Tip added after the order"})
	if err != nil {
		if square.Declined(err) {
			return "", payment.Permanent{Err: payment.ErrDeclined}
		}
		return "", err
	}
	return p.ID, nil
}
