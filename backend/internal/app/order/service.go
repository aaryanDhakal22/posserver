package orderSvc

import (
	"context"

	"quiccpos/main/internal/domain/order"

	"github.com/rs/zerolog"
)

type Service struct {
	repo   order.OrderRepository
	logger zerolog.Logger
}

func NewOrderService(repo order.OrderRepository, logger zerolog.Logger) *Service {
	return &Service{
		repo:   repo,
		logger: logger.With().Str("module", "order-service").Logger(),
	}
}

func (s *Service) Create(ctx context.Context, o *order.Order) error {
	s.logger.Debug().Int("order_id", o.OrderID).Msg("creating order")
	return s.repo.Create(ctx, o)
}

func (s *Service) GetByID(ctx context.Context, id int) (*order.Order, error) {
	s.logger.Debug().Int("order_id", id).Msg("getting order by id")
	return s.repo.GetByID(ctx, id)
}

func (s *Service) GetLatest(ctx context.Context) (*order.Order, error) {
	s.logger.Debug().Msg("getting latest order")
	return s.repo.GetLatest(ctx)
}

func (s *Service) GetAllOrders(ctx context.Context) ([]order.Order, error) {
	s.logger.Debug().Msg("getting all orders")
	return s.repo.GetAllOrders(ctx)
}

func (s *Service) GetOrdersPage(ctx context.Context, offset, num int) ([]order.Order, error) {
	s.logger.Debug().Int("offset", offset).Int("num", num).Msg("getting orders page")
	return s.repo.GetOrdersPage(ctx, offset, num)
}
