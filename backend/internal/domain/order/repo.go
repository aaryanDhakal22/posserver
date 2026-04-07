package order

import "context"

type OrderRepository interface {
	Create(ctx context.Context, order *Order) error
	GetLatest(ctx context.Context) (*Order, error)
	GetByID(ctx context.Context, id int) (*Order, error)
	GetAllOrders(ctx context.Context) ([]Order, error)
	GetOrdersPage(ctx context.Context, offset, num int) ([]Order, error)
}
