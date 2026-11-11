package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
)

// ErrNotCounterView: only a counter view holds a layout (the 00031 check).
var ErrNotCounterView = errors.New("only a counter view has a layout")

// Layout reads a view's counter layout (nil = none).
func (r *MenuBankRepository) Layout(ctx context.Context, viewID uuid.UUID) (*menuview.Layout, error) {
	return layoutOf(ctx, models.New(r.pool), viewID)
}

// SetLayout saves a counter view's layout (nil clears it). The view row's
// update bumps the menu version (00018's trigger).
func (r *MenuBankRepository) SetLayout(ctx context.Context, viewID uuid.UUID, l *menuview.Layout) error {
	return setLayout(ctx, models.New(r.pool), viewID, l)
}

// DealNames are a location's deals, id → name (what a layout's deal
// buttons may point at).
func (r *MenuBankRepository) DealNames(ctx context.Context, locationID uuid.UUID) (map[string]string, error) {
	rows, err := models.New(r.pool).ListDeals(ctx, pgID(locationID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		d, err := dealOf(row)
		if err != nil {
			return nil, err
		}
		out[d.ID] = d.Name
	}
	return out, nil
}

func layoutOf(ctx context.Context, q *models.Queries, viewID uuid.UUID) (*menuview.Layout, error) {
	raw, err := q.GetMenuViewLayout(ctx, pgID(viewID))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var l menuview.Layout
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("view %s layout: %w", viewID, err)
	}
	menuview.NormalizeLayout(&l)
	return &l, nil
}

func setLayout(ctx context.Context, q *models.Queries, viewID uuid.UUID, l *menuview.Layout) error {
	var raw []byte
	if l != nil {
		var err error
		if raw, err = json.Marshal(l); err != nil {
			return err
		}
	}
	n, err := q.SetMenuViewLayout(ctx, models.SetMenuViewLayoutParams{ID: pgID(viewID), Layout: raw})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotCounterView
	}
	return nil
}

// Layout / SetLayout inside the bank import's transaction.
func (t *bankTx) Layout(ctx context.Context, viewID uuid.UUID) (*menuview.Layout, error) {
	return layoutOf(ctx, t.q, viewID)
}

func (t *bankTx) SetLayout(ctx context.Context, viewID uuid.UUID, l *menuview.Layout) error {
	return setLayout(ctx, t.q, viewID, l)
}
