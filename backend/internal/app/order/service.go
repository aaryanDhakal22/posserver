package orderSvc

import (
	"context"

	"quiccpos/main/internal/domain/order"

	"github.com/rs/zerolog"
)

// OrderPublisher is satisfied by the SSE broker. It is nil-safe — the server
// works without an agent connected.
type OrderPublisher interface {
	PublishOrder(o order.Order)
}

type Service struct {
	repo      order.OrderRepository
	publisher OrderPublisher
	logger    zerolog.Logger
}

func NewOrderService(repo order.OrderRepository, publisher OrderPublisher, logger zerolog.Logger) *Service {
	return &Service{
		repo:      repo,
		publisher: publisher,
		logger:    logger.With().Str("module", "order-service").Logger(),
	}
}

func (s *Service) Create(ctx context.Context, o *order.Order) error {
	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Int("order_id", o.OrderID).Str("service_type", o.ServiceType).Msg("Create called")

	if err := s.repo.Create(ctx, o); err != nil {
		log.Error().Err(err).Int("order_id", o.OrderID).Msg("Create failed")
		return err
	}

	log.Debug().Int("order_id", o.OrderID).Msg("Create succeeded")

	if s.publisher != nil {
		s.publisher.PublishOrder(*o)
	}

	return nil
}

func (s *Service) GetByID(ctx context.Context, id int) (*order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Int("order_id", id).Msg("GetByID called")

	o, err := s.repo.GetByID(ctx, id)
	if err != nil {
		log.Debug().Err(err).Int("order_id", id).Msg("GetByID failed")
		return nil, err
	}

	log.Debug().Int("order_id", id).Msg("GetByID succeeded")
	return o, nil
}

func (s *Service) GetLatest(ctx context.Context) (*order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Msg("GetLatest called")

	o, err := s.repo.GetLatest(ctx)
	if err != nil {
		log.Debug().Err(err).Msg("GetLatest failed")
		return nil, err
	}

	log.Debug().Int("order_id", o.OrderID).Msg("GetLatest succeeded")
	return o, nil
}

func (s *Service) GetAllOrders(ctx context.Context) ([]order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Msg("GetAllOrders called")

	orders, err := s.repo.GetAllOrders(ctx)
	if err != nil {
		log.Error().Err(err).Msg("GetAllOrders failed")
		return nil, err
	}

	log.Debug().Int("count", len(orders)).Msg("GetAllOrders succeeded")
	return orders, nil
}

func (s *Service) GetOrdersPage(ctx context.Context, offset, num int) ([]order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Int("offset", offset).Int("num", num).Msg("GetOrdersPage called")

	orders, err := s.repo.GetOrdersPage(ctx, offset, num)
	if err != nil {
		log.Error().Err(err).Int("offset", offset).Int("num", num).Msg("GetOrdersPage failed")
		return nil, err
	}

	log.Debug().Int("offset", offset).Int("num", num).Int("count", len(orders)).Msg("GetOrdersPage succeeded")
	return orders, nil
}
