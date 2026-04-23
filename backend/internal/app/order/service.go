package orderSvc

import (
	"context"

	"quiccpos/main/internal/domain/order"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/order"

// OrderPublisher is satisfied by the SSE broker. It is nil-safe — the server
// works without an agent connected.
type OrderPublisher interface {
	PublishOrder(ctx context.Context, o order.Order)
}

type Service struct {
	repo      order.OrderRepository
	publisher OrderPublisher
	logger    zerolog.Logger
	tracer    trace.Tracer
}

func NewOrderService(repo order.OrderRepository, publisher OrderPublisher, logger zerolog.Logger) *Service {
	return &Service{
		repo:      repo,
		publisher: publisher,
		logger:    logger.With().Str("module", "order-service").Logger(),
		tracer:    otel.Tracer(tracerName),
	}
}

func (s *Service) Create(ctx context.Context, o *order.Order) error {
	ctx, span := s.tracer.Start(ctx, "order.create",
		trace.WithAttributes(
			attribute.Int("order.id", o.OrderID),
			attribute.String("order.service_type", o.ServiceType),
			attribute.Int("order.item_count", len(o.Items)),
		),
	)
	defer span.End()

	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Ctx(ctx).Int("order_id", o.OrderID).Str("service_type", o.ServiceType).Msg("Create called")

	if err := s.persist(ctx, o); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "persist failed")
		log.Error().Ctx(ctx).Err(err).Int("order_id", o.OrderID).Msg("Create failed")
		return err
	}

	log.Debug().Ctx(ctx).Int("order_id", o.OrderID).Msg("Create succeeded")

	s.broadcast(ctx, *o)
	return nil
}

func (s *Service) persist(ctx context.Context, o *order.Order) error {
	ctx, span := s.tracer.Start(ctx, "order.persist",
		trace.WithAttributes(attribute.Int("order.id", o.OrderID)),
	)
	defer span.End()

	if err := s.repo.Create(ctx, o); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func (s *Service) broadcast(ctx context.Context, o order.Order) {
	if s.publisher == nil {
		return
	}
	ctx, span := s.tracer.Start(ctx, "order.broadcast",
		trace.WithAttributes(attribute.Int("order.id", o.OrderID)),
	)
	defer span.End()
	s.publisher.PublishOrder(ctx, o)
}

func (s *Service) GetByID(ctx context.Context, id int) (*order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.get_by_id",
		trace.WithAttributes(attribute.Int("order.id", id)),
	)
	defer span.End()

	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Ctx(ctx).Int("order_id", id).Msg("GetByID called")

	o, err := s.repo.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
		log.Debug().Ctx(ctx).Err(err).Int("order_id", id).Msg("GetByID failed")
		return nil, err
	}

	log.Debug().Ctx(ctx).Int("order_id", id).Msg("GetByID succeeded")
	return o, nil
}

func (s *Service) GetLatest(ctx context.Context) (*order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.get_latest")
	defer span.End()

	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Ctx(ctx).Msg("GetLatest called")

	o, err := s.repo.GetLatest(ctx)
	if err != nil {
		span.RecordError(err)
		log.Debug().Ctx(ctx).Err(err).Msg("GetLatest failed")
		return nil, err
	}

	log.Debug().Ctx(ctx).Int("order_id", o.OrderID).Msg("GetLatest succeeded")
	return o, nil
}

func (s *Service) GetAllOrders(ctx context.Context) ([]order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.get_all")
	defer span.End()

	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Ctx(ctx).Msg("GetAllOrders called")

	orders, err := s.repo.GetAllOrders(ctx)
	if err != nil {
		span.RecordError(err)
		log.Error().Ctx(ctx).Err(err).Msg("GetAllOrders failed")
		return nil, err
	}

	log.Debug().Ctx(ctx).Int("count", len(orders)).Msg("GetAllOrders succeeded")
	return orders, nil
}

func (s *Service) GetOrdersPage(ctx context.Context, offset, num int) ([]order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.get_page",
		trace.WithAttributes(
			attribute.Int("offset", offset),
			attribute.Int("num", num),
		),
	)
	defer span.End()

	log := zerolog.Ctx(ctx).With().Str("module", "order-service").Logger()
	log.Debug().Ctx(ctx).Int("offset", offset).Int("num", num).Msg("GetOrdersPage called")

	orders, err := s.repo.GetOrdersPage(ctx, offset, num)
	if err != nil {
		span.RecordError(err)
		log.Error().Ctx(ctx).Err(err).Int("offset", offset).Int("num", num).Msg("GetOrdersPage failed")
		return nil, err
	}

	log.Debug().Ctx(ctx).Int("offset", offset).Int("num", num).Int("count", len(orders)).Msg("GetOrdersPage succeeded")
	return orders, nil
}
