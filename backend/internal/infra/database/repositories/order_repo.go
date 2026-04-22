package repositories

import (
	"context"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"
	mapper "quiccpos/main/internal/infra/database/repositories/mapping"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// compile-time interface check
var _ order.OrderRepository = (*OrderRepository)(nil)

type OrderRepository struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewOrderRepository(pool *pgxpool.Pool, lg zerolog.Logger) *OrderRepository {
	return &OrderRepository{
		pool:   pool,
		logger: lg.With().Str("module", "order-repo").Logger(),
	}
}

func (r *OrderRepository) Create(ctx context.Context, o *order.Order) error {
	log := zerolog.Ctx(ctx).With().Str("module", "order-repo").Int("order_id", o.OrderID).Logger()
	log.Debug().Msg("beginning transaction")

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := models.New(tx)

	// 1. Customer
	customerID, err := q.CreateCustomer(ctx, mapper.ToCustomerParams(o.Customer))
	if err != nil {
		log.Error().Err(err).Msg("failed to upsert customer")
		return fmt.Errorf("create customer: %w", err)
	}
	log.Debug().Int32("customer_id", customerID).Msg("customer upserted")

	// 2. Delivery address (optional)
	var addrID int32
	if o.DeliveryAddress != nil {
		addrID, err = q.CreateDeliveryAddress(ctx, mapper.ToDeliveryAddressParams(*o.DeliveryAddress))
		if err != nil {
			log.Error().Err(err).Msg("failed to create delivery address")
			return fmt.Errorf("create delivery address: %w", err)
		}
		log.Debug().Int32("address_id", addrID).Msg("delivery address created")
	} else {
		log.Debug().Msg("no delivery address")
	}

	// 3. Delivery provider (optional)
	var providerID int32
	if o.DeliveryProvider != nil {
		providerID, err = q.CreateDeliveryProvider(ctx, mapper.ToDeliveryProviderParams(*o.DeliveryProvider))
		if err != nil {
			log.Error().Err(err).Msg("failed to upsert delivery provider")
			return fmt.Errorf("create delivery provider: %w", err)
		}
		log.Debug().Int32("provider_id", providerID).Msg("delivery provider upserted")
	} else {
		log.Debug().Msg("no delivery provider")
	}

	// 4. Order row
	_, err = q.CreateOrder(ctx, mapper.ToCreateOrderParams(o, customerID, addrID, providerID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT DO NOTHING returned no row — order already exists (duplicate SQS delivery).
			log.Info().Msg("order already exists, skipping duplicate")
			return nil
		}
		log.Error().Err(err).Msg("failed to insert order row")
		return fmt.Errorf("create order: %w", err)
	}
	log.Debug().Msg("order row inserted")

	// 5. Items + modifiers per item
	log.Debug().Int("item_count", len(o.Items)).Msg("inserting items")
	for _, item := range o.Items {
		itemID, err := q.CreateSingleItem(ctx, mapper.ToSingleItemParams(o.OrderID, item))
		if err != nil {
			log.Error().Err(err).Str("item_name", item.Name).Msg("failed to insert item")
			return fmt.Errorf("create item: %w", err)
		}
		if len(item.Modifiers) > 0 {
			if _, err = q.CreateModifier(ctx, mapper.ToModifierParams(itemID, item.Modifiers)); err != nil {
				log.Error().Err(err).Int32("item_id", itemID).Msg("failed to insert modifiers")
				return fmt.Errorf("create modifiers: %w", err)
			}
		}
	}

	// 6. Bulk collections
	if len(o.Coupons) > 0 {
		if _, err = q.CreateCoupon(ctx, mapper.ToCouponParams(o.OrderID, o.Coupons)); err != nil {
			log.Error().Err(err).Int("count", len(o.Coupons)).Msg("failed to insert coupons")
			return fmt.Errorf("create coupons: %w", err)
		}
		log.Debug().Int("count", len(o.Coupons)).Msg("coupons inserted")
	}
	if len(o.Payments) > 0 {
		if _, err = q.CreatePayment(ctx, mapper.ToPaymentParams(o.OrderID, o.Payments)); err != nil {
			log.Error().Err(err).Int("count", len(o.Payments)).Msg("failed to insert payments")
			return fmt.Errorf("create payments: %w", err)
		}
		log.Debug().Int("count", len(o.Payments)).Msg("payments inserted")
	}
	if len(o.Taxes) > 0 {
		if _, err = q.CreateTax(ctx, mapper.ToTaxParams(o.OrderID, o.Taxes)); err != nil {
			log.Error().Err(err).Int("count", len(o.Taxes)).Msg("failed to insert taxes")
			return fmt.Errorf("create taxes: %w", err)
		}
		log.Debug().Int("count", len(o.Taxes)).Msg("taxes inserted")
	}
	if len(o.MiscCharges) > 0 {
		if _, err = q.CreateMiscCharge(ctx, mapper.ToMiscChargeParams(o.OrderID, o.MiscCharges)); err != nil {
			log.Error().Err(err).Int("count", len(o.MiscCharges)).Msg("failed to insert misc charges")
			return fmt.Errorf("create misc charges: %w", err)
		}
		log.Debug().Int("count", len(o.MiscCharges)).Msg("misc charges inserted")
	}

	if err = tx.Commit(ctx); err != nil {
		log.Error().Err(err).Msg("failed to commit transaction")
		return fmt.Errorf("commit tx: %w", err)
	}
	log.Info().Msg("order persisted")
	return nil
}

func (r *OrderRepository) GetByID(ctx context.Context, id int) (*order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-repo").Int("order_id", id).Logger()
	log.Debug().Msg("GetByID querying")

	q := models.New(r.pool)
	row, err := q.GetOrderByID(ctx, int32(id))
	if err != nil {
		log.Debug().Err(err).Msg("GetByID query failed")
		return nil, fmt.Errorf("get order: %w", err)
	}

	o, err := r.enrich(ctx, q, row)
	if err != nil {
		return nil, err
	}
	log.Debug().Msg("GetByID enriched")
	return &o, nil
}

func (r *OrderRepository) GetLatest(ctx context.Context) (*order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-repo").Logger()
	log.Debug().Msg("GetLatest querying")

	q := models.New(r.pool)
	row, err := q.GetLatest(ctx)
	if err != nil {
		log.Debug().Err(err).Msg("GetLatest query failed")
		return nil, fmt.Errorf("get latest order: %w", err)
	}

	o, err := r.enrich(ctx, q, row)
	if err != nil {
		return nil, err
	}
	log.Debug().Int("order_id", o.OrderID).Msg("GetLatest enriched")
	return &o, nil
}

func (r *OrderRepository) GetOrdersPage(ctx context.Context, offset, num int) ([]order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-repo").Int("offset", offset).Int("num", num).Logger()
	log.Debug().Msg("GetOrdersPage querying")

	q := models.New(r.pool)
	rows, err := q.GetOrdersPage(ctx, models.GetOrdersPageParams{
		Limit:  int32(num),
		Offset: int32(offset),
	})
	if err != nil {
		log.Error().Err(err).Msg("GetOrdersPage query failed")
		return nil, fmt.Errorf("get orders page: %w", err)
	}

	log.Debug().Int("row_count", len(rows)).Msg("GetOrdersPage enriching rows")
	orders := make([]order.Order, 0, len(rows))
	for _, row := range rows {
		o, err := r.enrich(ctx, q, row)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	log.Debug().Int("count", len(orders)).Msg("GetOrdersPage done")
	return orders, nil
}

func (r *OrderRepository) GetAllOrders(ctx context.Context) ([]order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-repo").Logger()
	log.Debug().Msg("GetAllOrders querying")

	q := models.New(r.pool)
	rows, err := q.GetAllOrders(ctx)
	if err != nil {
		log.Error().Err(err).Msg("GetAllOrders query failed")
		return nil, fmt.Errorf("get all orders: %w", err)
	}

	log.Debug().Int("row_count", len(rows)).Msg("GetAllOrders enriching rows")
	orders := make([]order.Order, 0, len(rows))
	for _, row := range rows {
		o, err := r.enrich(ctx, q, row)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	log.Debug().Int("count", len(orders)).Msg("GetAllOrders done")
	return orders, nil
}

// enrich fetches all related records for a flat Order row and assembles a full domain Order.
func (r *OrderRepository) enrich(ctx context.Context, q *models.Queries, row models.Order) (order.Order, error) {
	log := zerolog.Ctx(ctx).With().Str("module", "order-repo").Int32("order_id", row.Orderid).Logger()
	log.Debug().Msg("enrich: fetching customer")

	customer, err := q.GetCustomerByID(ctx, row.Customer.Int32)
	if err != nil {
		log.Error().Err(err).Int32("customer_id", row.Customer.Int32).Msg("enrich: get customer failed")
		return order.Order{}, fmt.Errorf("get customer: %w", err)
	}
	log.Debug().Int32("customer_id", customer.ID).Msg("enrich: customer loaded")

	var addr *models.DeliveryAddress
	if row.Deliveryaddress.Valid {
		a, err := q.GetDeliveryAddressByID(ctx, row.Deliveryaddress.Int32)
		if err != nil {
			log.Error().Err(err).Int32("address_id", row.Deliveryaddress.Int32).Msg("enrich: get delivery address failed")
			return order.Order{}, fmt.Errorf("get delivery address: %w", err)
		}
		addr = &a
		log.Debug().Int32("address_id", a.ID).Msg("enrich: delivery address loaded")
	}

	var provider *models.DeliveryProvider
	if row.Deliveryprovider.Valid {
		p, err := q.GetDeliveryProviderByID(ctx, row.Deliveryprovider.Int32)
		if err != nil {
			log.Error().Err(err).Int32("provider_id", row.Deliveryprovider.Int32).Msg("enrich: get delivery provider failed")
			return order.Order{}, fmt.Errorf("get delivery provider: %w", err)
		}
		provider = &p
		log.Debug().Int32("provider_id", p.ID).Msg("enrich: delivery provider loaded")
	}

	orderIDPg := mapper.Int32ToPg(row.Orderid)

	items, err := q.GetItemsByOrderID(ctx, orderIDPg)
	if err != nil {
		log.Error().Err(err).Msg("enrich: get items failed")
		return order.Order{}, fmt.Errorf("get items: %w", err)
	}
	log.Debug().Int("item_count", len(items)).Msg("enrich: items loaded")

	modifiersByItem := make(map[int32][]models.Modifier, len(items))
	for _, item := range items {
		mods, err := q.GetModifiersByItemID(ctx, mapper.Int32ToPg(item.ID))
		if err != nil {
			log.Error().Err(err).Int32("item_id", item.ID).Msg("enrich: get modifiers failed")
			return order.Order{}, fmt.Errorf("get modifiers for item %d: %w", item.ID, err)
		}
		modifiersByItem[item.ID] = mods
	}

	coupons, err := q.GetCouponsByOrderID(ctx, orderIDPg)
	if err != nil {
		log.Error().Err(err).Msg("enrich: get coupons failed")
		return order.Order{}, fmt.Errorf("get coupons: %w", err)
	}
	payments, err := q.GetPaymentsByOrderID(ctx, orderIDPg)
	if err != nil {
		log.Error().Err(err).Msg("enrich: get payments failed")
		return order.Order{}, fmt.Errorf("get payments: %w", err)
	}
	taxes, err := q.GetTaxesByOrderID(ctx, orderIDPg)
	if err != nil {
		log.Error().Err(err).Msg("enrich: get taxes failed")
		return order.Order{}, fmt.Errorf("get taxes: %w", err)
	}
	miscCharges, err := q.GetMiscChargesByOrderID(ctx, orderIDPg)
	if err != nil {
		log.Error().Err(err).Msg("enrich: get misc charges failed")
		return order.Order{}, fmt.Errorf("get misc charges: %w", err)
	}

	log.Debug().
		Int("coupons", len(coupons)).
		Int("payments", len(payments)).
		Int("taxes", len(taxes)).
		Int("misc_charges", len(miscCharges)).
		Msg("enrich: complete")

	return mapper.ToOrderDomain(row, customer, addr, provider, items, modifiersByItem, coupons, payments, taxes, miscCharges), nil
}
