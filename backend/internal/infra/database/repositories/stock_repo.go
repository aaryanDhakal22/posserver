package repositories

import (
	"context"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StockRepository marks a store's items and toppings sold out (weborder
// stage 3: Quibase through the Pi).
type StockRepository struct {
	pool *pgxpool.Pool
}

func NewStockRepository(pool *pgxpool.Pool) *StockRepository { return &StockRepository{pool: pool} }

// StockItem is a menu item's stock.
type StockItem struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	Category string     `json:"category"`
	InStock  bool       `json:"in_stock"`
	OutUntil *time.Time `json:"out_until"`
	Hold     bool       `json:"hold"` // sold out until turned back on
}

// StockOption is a topping's stock.
type StockOption struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	InStock bool      `json:"in_stock"`
	Hold    bool      `json:"hold"`
}

// StockGroup is a modifier group with its options.
type StockGroup struct {
	ID      uuid.UUID     `json:"id"`
	Name    string        `json:"name"`
	Options []StockOption `json:"options"`
}

func (r *StockRepository) List(ctx context.Context, locationID uuid.UUID) ([]StockItem, []StockGroup, error) {
	q := models.New(r.pool)
	irows, err := q.ListStockItems(ctx, pgID(locationID))
	if err != nil {
		return nil, nil, err
	}
	items := make([]StockItem, 0, len(irows))
	for _, x := range irows {
		items = append(items, StockItem{ID: idOf(x.ID), Name: x.Name, Category: x.Category, InStock: x.InStock,
			OutUntil: tsPtr(x.OutUntil), Hold: x.OutHold})
	}
	orows, err := q.ListStockOptions(ctx, pgID(locationID))
	if err != nil {
		return nil, nil, err
	}
	groups := []StockGroup{}
	for _, x := range orows {
		gid := idOf(x.GroupID)
		if len(groups) == 0 || groups[len(groups)-1].ID != gid {
			groups = append(groups, StockGroup{ID: gid, Name: x.GroupName, Options: []StockOption{}})
		}
		g := &groups[len(groups)-1]
		g.Options = append(g.Options, StockOption{ID: idOf(x.ID), Name: x.Name, InStock: x.InStock, Hold: x.OutHold})
	}
	return items, groups, nil
}

// SetItem marks an item in stock or sold out (hold = until turned back on).
// pgx.ErrNoRows = not the store's item.
func (r *StockRepository) SetItem(ctx context.Context, locationID, id uuid.UUID, inStock, hold bool) (StockItem, error) {
	x, err := models.New(r.pool).SetItemStock(ctx, models.SetItemStockParams{ID: pgID(id), LocationID: pgID(locationID), InStock: inStock, Column4: hold})
	if err != nil {
		return StockItem{}, err
	}
	return StockItem{ID: idOf(x.ID), Name: x.Name, InStock: x.InStock, OutUntil: tsPtr(x.OutUntil), Hold: x.OutHold}, nil
}

// SetOption marks a topping in stock or sold out.
func (r *StockRepository) SetOption(ctx context.Context, locationID, id uuid.UUID, inStock, hold bool) (StockOption, error) {
	x, err := models.New(r.pool).SetOptionStock(ctx, models.SetOptionStockParams{ID: pgID(id), LocationID: pgID(locationID), InStock: inStock, Column4: hold})
	if err != nil {
		return StockOption{}, err
	}
	return StockOption{ID: idOf(x.ID), Name: x.Name, InStock: x.InStock, Hold: x.OutHold}, nil
}
