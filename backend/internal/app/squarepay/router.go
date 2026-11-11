package squarepay

import (
	"context"
	"strings"

	"quiccpos/main/internal/domain/payment"

	"github.com/google/uuid"
)

// CardRouter takes a store's card payments through its Square account when
// it has one connected (with a location picked), else through fallback (the
// test card on dev / staging; nil in prod = card refused).
type CardRouter struct {
	svc      *Service
	square   *Gateway
	fallback payment.Gateway
}

func (s *Service) CardRouter(fallback payment.Gateway) *CardRouter {
	return &CardRouter{svc: s, square: s.Gateway(), fallback: fallback}
}

func (r *CardRouter) Charge(ctx context.Context, c payment.Charge) (payment.Result, error) {
	if r.svc.Connected(ctx, c.LocationID) {
		return r.square.Charge(ctx, c)
	}
	if r.fallback != nil {
		return r.fallback.Charge(ctx, c)
	}
	return payment.Result{}, payment.ErrNotConnected
}

func (r *CardRouter) Refund(ctx context.Context, locationID uuid.UUID, ref string, amountCents int64) error {
	if r.fallback != nil && strings.HasPrefix(ref, "tc_") { // the test card's refs
		return r.fallback.Refund(ctx, locationID, ref, amountCents)
	}
	return r.square.Refund(ctx, locationID, ref, amountCents)
}

// VoidByKey releases whatever a Charge with that key made at Square (or the
// test card) without making anything.
func (r *CardRouter) VoidByKey(ctx context.Context, locationID uuid.UUID, key string) error {
	if r.svc.Connected(ctx, locationID) {
		return r.square.VoidByKey(ctx, locationID, key)
	}
	if v, ok := r.fallback.(payment.KeyVoider); ok {
		return v.VoidByKey(ctx, locationID, key)
	}
	return nil
}
