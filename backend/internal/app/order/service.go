package orderSvc

import (
	"context"
	"errors"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/order"

type Waker interface {
	Wake()
}

type Service struct {
	repo   order.OrderRepository
	waker  Waker
	logger zerolog.Logger
	tracer trace.Tracer
	// afterCreate hooks run after an order is persisted (not for duplicates).
	// They must not fail ingest — each handles its own errors.
	afterCreate []func(ctx context.Context, o *order.Order)
}

// OnCreated registers a hook that runs after an order is persisted (the geo
// worker enqueues delivery orders here). Wire before ingest starts.
func (s *Service) OnCreated(fn func(ctx context.Context, o *order.Order)) {
	s.afterCreate = append(s.afterCreate, fn)
}

func NewOrderService(repo order.OrderRepository, waker Waker, logger zerolog.Logger) *Service {
	return &Service{
		repo:   repo,
		waker:  waker,
		logger: logger.With().Str("module", "order-service").Logger(),
		tracer: otel.Tracer(tracerName),
	}
}

func (s *Service) Create(ctx context.Context, o *order.Order) error {
	ctx, span := s.tracer.Start(ctx, "order.create",
		trace.WithAttributes(
			attribute.String("order.id", uuid.UUID(o.ID()).String()),
			attribute.String("tenant.id", uuid.UUID(o.TenantID()).String()),
			attribute.String("location.id", uuid.UUID(o.LocationID()).String()),
			attribute.String("order.source", string(o.Source())),
			attribute.String("order.source_event_id", o.SourceEventID()),
			attribute.Int("order.item_count", len(o.Items())),
		),
	)
	defer span.End()

	log := zerolog.Ctx(ctx).With().
		Str("module", "order-service").
		Str("order_id", uuid.UUID(o.ID()).String()).
		Logger()
	log.Debug().Ctx(ctx).Msg("Create called")

	if err := s.repo.Create(ctx, o); err != nil {
		span.RecordError(err)
		if errors.Is(err, order.ErrDuplicate) {
			span.SetStatus(codes.Ok, "duplicate")
			log.Info().Ctx(ctx).Str("source_event_id", o.SourceEventID()).Msg("duplicate order; skipping outbox")
			return err
		}
		span.SetStatus(codes.Error, "persist failed")
		log.Error().Ctx(ctx).Err(err).Msg("Create failed")
		return err
	}

	log.Debug().Ctx(ctx).Msg("Create persisted; waking dispatcher")
	if s.waker != nil {
		s.waker.Wake()
	}
	for _, fn := range s.afterCreate {
		fn(ctx, o)
	}
	return nil
}

func (s *Service) GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.get_by_id",
		trace.WithAttributes(
			attribute.String("order.id", uuid.UUID(id).String()),
			attribute.String("tenant.id", uuid.UUID(scope.TenantID).String()),
			attribute.String("location.id", uuid.UUID(scope.LocationID).String()),
		),
	)
	defer span.End()

	o, err := s.repo.GetByID(ctx, scope, id)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return o, nil
}

func (s *Service) FindBySourceEvent(ctx context.Context, scope order.Scope, src order.Source, sourceEventID string) (*order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.find_by_source_event",
		trace.WithAttributes(
			attribute.String("order.source", string(src)),
			attribute.String("order.source_event_id", sourceEventID),
			attribute.String("tenant.id", uuid.UUID(scope.TenantID).String()),
			attribute.String("location.id", uuid.UUID(scope.LocationID).String()),
		),
	)
	defer span.End()

	o, err := s.repo.FindBySourceEvent(ctx, scope, src, sourceEventID)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return o, nil
}

func (s *Service) GetOrdersPage(ctx context.Context, scope order.Scope, offset, num int) ([]*order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.get_page",
		trace.WithAttributes(
			attribute.Int("offset", offset),
			attribute.Int("num", num),
			attribute.String("tenant.id", uuid.UUID(scope.TenantID).String()),
			attribute.String("location.id", uuid.UUID(scope.LocationID).String()),
		),
	)
	defer span.End()

	orders, err := s.repo.GetOrdersPage(ctx, scope, offset, num)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return orders, nil
}

// DeleteByID removes a single order (admin-only; not scope-checked — the admin
// bearer is god-mode). Used to clean up test/straggler orders.
func (s *Service) DeleteByID(ctx context.Context, id order.ID) error {
	ctx, span := s.tracer.Start(ctx, "order.delete",
		trace.WithAttributes(attribute.String("order.id", uuid.UUID(id).String())),
	)
	defer span.End()
	if err := s.repo.DeleteByID(ctx, id); err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// ListSummaries returns a cross-tenant, lightweight order listing (admin-only).
func (s *Service) ListSummaries(ctx context.Context, f order.ListFilter) ([]order.Summary, error) {
	return s.repo.ListSummaries(ctx, f)
}

// DeleteAll bulk-deletes orders (admin-only). Empty tenantID = every order.
func (s *Service) DeleteAll(ctx context.Context, tenantID string) (int64, error) {
	ctx, span := s.tracer.Start(ctx, "order.delete_all",
		trace.WithAttributes(attribute.String("tenant.id", tenantID)))
	defer span.End()
	n, err := s.repo.DeleteAll(ctx, tenantID)
	if err != nil {
		span.RecordError(err)
	}
	return n, err
}

// SearchOrders returns a filtered/sorted page of orders for the caller's scope.
func (s *Service) SearchOrders(ctx context.Context, scope order.Scope, f order.OrderFilter) ([]*order.Order, error) {
	ctx, span := s.tracer.Start(ctx, "order.search",
		trace.WithAttributes(
			attribute.Int("offset", f.Offset),
			attribute.Int("num", f.Limit),
			attribute.Bool("reverse", f.Reverse),
			attribute.String("tenant.id", uuid.UUID(scope.TenantID).String()),
			attribute.String("location.id", uuid.UUID(scope.LocationID).String()),
		),
	)
	defer span.End()

	orders, err := s.repo.SearchOrders(ctx, scope, f)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return orders, nil
}
