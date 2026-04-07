package repositories

import (
	"context"
	"fmt"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"
	mapper "quiccpos/main/internal/infra/database/repositories/mapping"

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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := models.New(tx)

	// 1. Customer
	customerID, err := q.CreateCustomer(ctx, mapper.ToCustomerParams(o.Customer))
	if err != nil {
		return fmt.Errorf("create customer: %w", err)
	}

	// 2. Delivery address (optional)
	var addrID int32
	if o.DeliveryAddress != nil {
		addrID, err = q.CreateDeliveryAddress(ctx, mapper.ToDeliveryAddressParams(*o.DeliveryAddress))
		if err != nil {
			return fmt.Errorf("create delivery address: %w", err)
		}
	}

	// 3. Delivery provider (optional)
	var providerID int32
	if o.DeliveryProvider != nil {
		providerID, err = q.CreateDeliveryProvider(ctx, mapper.ToDeliveryProviderParams(*o.DeliveryProvider))
		if err != nil {
			return fmt.Errorf("create delivery provider: %w", err)
		}
	}

	// 4. Order row
	_, err = q.CreateOrder(ctx, mapper.ToCreateOrderParams(o, customerID, addrID, providerID))
	if err != nil {
		return fmt.Errorf("create order: %w", err)
	}

	// 5. Items + modifiers per item
	for _, item := range o.Items {
		itemID, err := q.CreateSingleItem(ctx, mapper.ToSingleItemParams(o.OrderID, item))
		if err != nil {
			return fmt.Errorf("create item: %w", err)
		}
		if len(item.Modifiers) > 0 {
			if _, err = q.CreateModifier(ctx, mapper.ToModifierParams(itemID, item.Modifiers)); err != nil {
				return fmt.Errorf("create modifiers: %w", err)
			}
		}
	}

	// 6. Bulk collections
	if len(o.Coupons) > 0 {
		if _, err = q.CreateCoupon(ctx, mapper.ToCouponParams(o.OrderID, o.Coupons)); err != nil {
			return fmt.Errorf("create coupons: %w", err)
		}
	}
	if len(o.Payments) > 0 {
		if _, err = q.CreatePayment(ctx, mapper.ToPaymentParams(o.OrderID, o.Payments)); err != nil {
			return fmt.Errorf("create payments: %w", err)
		}
	}
	if len(o.Taxes) > 0 {
		if _, err = q.CreateTax(ctx, mapper.ToTaxParams(o.OrderID, o.Taxes)); err != nil {
			return fmt.Errorf("create taxes: %w", err)
		}
	}
	if len(o.MiscCharges) > 0 {
		if _, err = q.CreateMiscCharge(ctx, mapper.ToMiscChargeParams(o.OrderID, o.MiscCharges)); err != nil {
			return fmt.Errorf("create misc charges: %w", err)
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	r.logger.Debug().Int("order_id", o.OrderID).Msg("order created")
	return nil
}

func (r *OrderRepository) GetByID(ctx context.Context, id int) (*order.Order, error) {
	q := models.New(r.pool)
	row, err := q.GetOrderByID(ctx, int32(id))
	if err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	o, err := r.enrich(ctx, q, row)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *OrderRepository) GetLatest(ctx context.Context) (*order.Order, error) {
	q := models.New(r.pool)
	row, err := q.GetLatest(ctx)
	if err != nil {
		return nil, fmt.Errorf("get latest order: %w", err)
	}
	o, err := r.enrich(ctx, q, row)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *OrderRepository) GetOrdersPage(ctx context.Context, offset, num int) ([]order.Order, error) {
	q := models.New(r.pool)
	rows, err := q.GetOrdersPage(ctx, models.GetOrdersPageParams{
		Limit:  int32(num),
		Offset: int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("get orders page: %w", err)
	}
	orders := make([]order.Order, 0, len(rows))
	for _, row := range rows {
		o, err := r.enrich(ctx, q, row)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, nil
}

func (r *OrderRepository) GetAllOrders(ctx context.Context) ([]order.Order, error) {
	q := models.New(r.pool)
	rows, err := q.GetAllOrders(ctx)
	if err != nil {
		return nil, fmt.Errorf("get all orders: %w", err)
	}
	orders := make([]order.Order, 0, len(rows))
	for _, row := range rows {
		o, err := r.enrich(ctx, q, row)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, nil
}

// enrich fetches all related records for a flat Order row and assembles a full domain Order.
func (r *OrderRepository) enrich(ctx context.Context, q *models.Queries, row models.Order) (order.Order, error) {
	customer, err := q.GetCustomerByID(ctx, row.Customer.Int32)
	if err != nil {
		return order.Order{}, fmt.Errorf("get customer: %w", err)
	}

	var addr *models.DeliveryAddress
	if row.Deliveryaddress.Valid {
		a, err := q.GetDeliveryAddressByID(ctx, row.Deliveryaddress.Int32)
		if err != nil {
			return order.Order{}, fmt.Errorf("get delivery address: %w", err)
		}
		addr = &a
	}

	var provider *models.DeliveryProvider
	if row.Deliveryprovider.Valid {
		p, err := q.GetDeliveryProviderByID(ctx, row.Deliveryprovider.Int32)
		if err != nil {
			return order.Order{}, fmt.Errorf("get delivery provider: %w", err)
		}
		provider = &p
	}

	orderIDPg := mapper.Int32ToPg(row.Orderid)

	items, err := q.GetItemsByOrderID(ctx, orderIDPg)
	if err != nil {
		return order.Order{}, fmt.Errorf("get items: %w", err)
	}
	modifiersByItem := make(map[int32][]models.Modifier, len(items))
	for _, item := range items {
		mods, err := q.GetModifiersByItemID(ctx, mapper.Int32ToPg(item.ID))
		if err != nil {
			return order.Order{}, fmt.Errorf("get modifiers for item %d: %w", item.ID, err)
		}
		modifiersByItem[item.ID] = mods
	}

	coupons, err := q.GetCouponsByOrderID(ctx, orderIDPg)
	if err != nil {
		return order.Order{}, fmt.Errorf("get coupons: %w", err)
	}
	payments, err := q.GetPaymentsByOrderID(ctx, orderIDPg)
	if err != nil {
		return order.Order{}, fmt.Errorf("get payments: %w", err)
	}
	taxes, err := q.GetTaxesByOrderID(ctx, orderIDPg)
	if err != nil {
		return order.Order{}, fmt.Errorf("get taxes: %w", err)
	}
	miscCharges, err := q.GetMiscChargesByOrderID(ctx, orderIDPg)
	if err != nil {
		return order.Order{}, fmt.Errorf("get misc charges: %w", err)
	}

	return mapper.ToOrderDomain(row, customer, addr, provider, items, modifiersByItem, coupons, payments, taxes, miscCharges), nil
}
