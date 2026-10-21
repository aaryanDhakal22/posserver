-- ───────────────────────────────────────────────────────────────────────────
-- Tenants
-- ───────────────────────────────────────────────────────────────────────────

-- name: CreateTenant :one
-- username/password_hash are optional (sqlc.narg → NULL when absent) so admin
-- can still create credential-less tenants. Full-column RETURNING keeps sqlc
-- mapping to the models.Tenant table struct (see toDomainTenant).
INSERT INTO tenants (slug, name, username, password_hash, store_id)
VALUES ($1, $2, sqlc.narg('username'), sqlc.narg('password_hash'), sqlc.narg('store_id'))
RETURNING id, slug, name, config, username, password_hash, created_at, store_id;

-- name: GetTenantByID :one
SELECT id, slug, name, config, username, password_hash, created_at, store_id FROM tenants WHERE id = $1;

-- name: GetTenantAuthByUsername :one
-- Login lookup: tenant id + stored password hash for a username.
SELECT id, password_hash FROM tenants WHERE username = $1;

-- name: SetTenantCredentials :exec
-- Set/rotate a tenant's store-manager login (username + already-hashed password).
UPDATE tenants SET username = $2, password_hash = $3 WHERE id = $1;

-- name: ListTenants :many
SELECT id, slug, name, config, username, password_hash, created_at, store_id FROM tenants ORDER BY created_at ASC;

-- name: GetTenantByStoreID :one
-- Order routing: the external vendor's store_id → tenant (see app/tenancy.ResolveStore).
SELECT id, slug, name, config, username, password_hash, created_at, store_id FROM tenants WHERE store_id = $1;

-- name: SetTenantStoreID :one
-- Set/clear (NULL) a tenant's vendor store_id. Unique when present.
UPDATE tenants SET store_id = sqlc.narg('store_id') WHERE id = $1
RETURNING id, slug, name, config, username, password_hash, created_at, store_id;

-- name: UpdateTenantConfig :one
UPDATE tenants SET config = $2 WHERE id = $1
RETURNING id, slug, name, config, username, password_hash, created_at, store_id;

-- name: DeleteTenantOutboxEvents :exec
DELETE FROM outbox_events WHERE tenant_id = $1;

-- name: DeleteTenantOrders :exec
DELETE FROM orders WHERE tenant_id = $1;

-- name: DeleteTenantPrinters :exec
DELETE FROM printers WHERE tenant_id = $1;

-- name: DeleteTenantDevices :exec
DELETE FROM devices WHERE tenant_id = $1;

-- name: DeleteTenantLocations :exec
DELETE FROM locations WHERE tenant_id = $1;

-- name: DeleteTenant :exec
DELETE FROM tenants WHERE id = $1;

-- name: DeleteDevice :exec
DELETE FROM devices WHERE id = $1;

-- name: CreateLocation :one
INSERT INTO locations (tenant_id, slug, name, timezone, daily_reset_hour)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, tenant_id, slug, name, timezone, daily_reset_hour,
          phone, address_line1, address_line2, config, store_type, created_at;

-- name: GetLocationByID :one
SELECT id, tenant_id, slug, name, timezone, daily_reset_hour,
       phone, address_line1, address_line2, config, store_type, created_at
FROM locations WHERE id = $1;

-- name: ListLocationsByTenant :many
SELECT id, tenant_id, slug, name, timezone, daily_reset_hour,
       phone, address_line1, address_line2, config, store_type, created_at
FROM locations WHERE tenant_id = $1 ORDER BY created_at ASC;

-- name: ListAllLocations :many
SELECT id, tenant_id, slug, name, timezone, daily_reset_hour,
       phone, address_line1, address_line2, config, store_type, created_at
FROM locations ORDER BY tenant_id, created_at ASC;

-- name: UpdateLocationDetails :one
-- Updates the editable surface of a location in one shot. Pass current values
-- for fields you don't want to change — there's no partial-update semantics.
UPDATE locations SET
    name           = $2,
    timezone       = $3,
    daily_reset_hour = $4,
    phone          = $5,
    address_line1  = $6,
    address_line2  = $7,
    config         = $8
WHERE id = $1
RETURNING id, tenant_id, slug, name, timezone, daily_reset_hour,
          phone, address_line1, address_line2, config, store_type, created_at;

-- ───────────────────────────────────────────────────────────────────────────
-- Devices (API keys)
-- ───────────────────────────────────────────────────────────────────────────

-- name: CreateDevice :one
INSERT INTO devices (tenant_id, location_id, name, kind, key_hash, key_prefix)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, tenant_id, location_id, name, kind, key_hash, key_prefix,
          created_at, last_used_at, revoked_at;

-- name: GetActiveDeviceByKeyHash :one
SELECT id, tenant_id, location_id, name, kind, key_hash, key_prefix,
       created_at, last_used_at, revoked_at
FROM devices
WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: ListDevicesByLocation :many
SELECT id, tenant_id, location_id, name, kind, key_hash, key_prefix,
       created_at, last_used_at, revoked_at
FROM devices
WHERE location_id = $1
ORDER BY created_at ASC;

-- name: ListAllDevices :many
SELECT id, tenant_id, location_id, name, kind, key_hash, key_prefix,
       created_at, last_used_at, revoked_at
FROM devices
ORDER BY tenant_id, location_id, created_at ASC;

-- name: RevokeDevice :exec
UPDATE devices SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: TouchDevice :exec
UPDATE devices SET last_used_at = now() WHERE id = $1;

-- ───────────────────────────────────────────────────────────────────────────
-- Printers
-- ───────────────────────────────────────────────────────────────────────────

-- name: CreatePrinter :one
INSERT INTO printers (tenant_id, location_id, name, ip)
VALUES ($1, $2, $3, $4)
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt;

-- name: GetPrinterByID :one
SELECT id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt
FROM printers WHERE id = $1;

-- name: ListPrintersByLocation :many
SELECT id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt
FROM printers WHERE location_id = $1 ORDER BY name ASC;

-- name: UpdatePrinter :one
UPDATE printers SET name = $2, ip = $3, updated_at = now()
WHERE id = $1
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt;

-- name: ClearReceiptPrinter :exec
-- Unflag the location's current receipt printer (one per location).
UPDATE printers SET is_receipt = false, updated_at = now()
WHERE location_id = $1 AND is_receipt;

-- name: SetPrinterReceipt :one
UPDATE printers SET is_receipt = $2, updated_at = now()
WHERE id = $1
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt;

-- name: DeletePrinter :exec
DELETE FROM printers WHERE id = $1;

-- name: DeletePrintersByLocation :exec
-- Used by the store-config editor's replace-all reconciliation.
DELETE FROM printers WHERE location_id = $1;

-- ───────────────────────────────────────────────────────────────────────────
-- Orders
-- ───────────────────────────────────────────────────────────────────────────

-- name: CreateOrder :exec
INSERT INTO orders (
    id, tenant_id, location_id, schema_version, source, source_event_id,
    status, source_status, currency,
    subtotal_cents, tax_cents, tip_cents, total_cents, fees, discounts,
    customer_display_name, customer_email, customer_phone, customer_external_id,
    fulfillment_type, fulfillment_address, promised_at, table_id,
    notes, source_metadata, raw_payload,
    placed_at, received_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9,
    $10, $11, $12, $13, $14, $15,
    $16, $17, $18, $19,
    $20, $21, $22, $23,
    $24, $25, $26,
    $27, $28, $29
);

-- name: CreateLineItem :exec
INSERT INTO line_items (
    id, order_id, parent_id, position, sku, name, quantity, unit_price_cents, line_total_cents, notes,
    size, menu_item_id, printer_id, routing
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
);

-- name: CreatePayment :exec
INSERT INTO payments (
    id, order_id, processor, status, amount_cents, token, last4, captured_at, auth_code
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
);

-- name: CreateStatusEvent :exec
INSERT INTO status_events (
    order_id, position, from_status, to_status, at, actor, actor_id, reason
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
);

-- name: DeleteOrderOutbox :exec
-- Remove the outbox rows for one order (aggregate_id = order id). outbox_events
-- has no FK to orders, so this is explicit; dispatches cascade off outbox_events.
DELETE FROM outbox_events WHERE aggregate_id = $1;

-- name: DeleteOrderByID :exec
-- Delete a single order. line_items / payments / status_events cascade via FK.
DELETE FROM orders WHERE id = $1;

-- name: GetOrderInScope :one
-- Tenant/location-scoped lookup by canonical id. Read endpoints always
-- supply the caller's scope so a cross-tenant id leak still 404s.
SELECT id, tenant_id, location_id, schema_version, source, source_event_id,
       kitchen_order_number, status, source_status, currency,
       subtotal_cents, tax_cents, tip_cents, total_cents, fees, discounts,
       customer_display_name, customer_email, customer_phone, customer_external_id,
       fulfillment_type, fulfillment_address, promised_at, table_id,
       notes, source_metadata, raw_payload,
       placed_at, received_at, updated_at, estimated_ready_at, driver_name, floor_at,
       agent_ack_at, agent_status, agent_status_at, agent_batch_id
FROM orders
WHERE id = $1 AND tenant_id = $2 AND location_id = $3;

-- name: GetOrderInScopeForUpdate :one
-- Same as GetOrderInScope, row-locked: floor updates for one order apply one
-- at a time (load + apply + write in a single transaction).
SELECT id, tenant_id, location_id, schema_version, source, source_event_id,
       kitchen_order_number, status, source_status, currency,
       subtotal_cents, tax_cents, tip_cents, total_cents, fees, discounts,
       customer_display_name, customer_email, customer_phone, customer_external_id,
       fulfillment_type, fulfillment_address, promised_at, table_id,
       notes, source_metadata, raw_payload,
       placed_at, received_at, updated_at, estimated_ready_at, driver_name, floor_at,
       agent_ack_at, agent_status, agent_status_at, agent_batch_id
FROM orders
WHERE id = $1 AND tenant_id = $2 AND location_id = $3
FOR UPDATE;

-- name: GetOrderBySourceEventInScope :one
SELECT id, tenant_id, location_id, schema_version, source, source_event_id,
       kitchen_order_number, status, source_status, currency,
       subtotal_cents, tax_cents, tip_cents, total_cents, fees, discounts,
       customer_display_name, customer_email, customer_phone, customer_external_id,
       fulfillment_type, fulfillment_address, promised_at, table_id,
       notes, source_metadata, raw_payload,
       placed_at, received_at, updated_at, estimated_ready_at, driver_name, floor_at,
       agent_ack_at, agent_status, agent_status_at, agent_batch_id
FROM orders
WHERE tenant_id = $1 AND location_id = $2 AND source = $3 AND source_event_id = $4;

-- name: GetOrdersPageInScope :many
SELECT id, tenant_id, location_id, schema_version, source, source_event_id,
       kitchen_order_number, status, source_status, currency,
       subtotal_cents, tax_cents, tip_cents, total_cents, fees, discounts,
       customer_display_name, customer_email, customer_phone, customer_external_id,
       fulfillment_type, fulfillment_address, promised_at, table_id,
       notes, source_metadata, raw_payload,
       placed_at, received_at, updated_at, estimated_ready_at, driver_name, floor_at,
       agent_ack_at, agent_status, agent_status_at, agent_batch_id
FROM orders
WHERE tenant_id = $1 AND location_id = $2
ORDER BY received_at DESC
LIMIT $3 OFFSET $4;

-- name: SearchOrdersInScope :many
-- Filtered/sorted order history for the agent's store-manager web UI. Every
-- filter is optional: empty text params and NULL time bounds match everything.
-- `reverse` flips newest-first (default) to oldest-first without a second query.
-- Column list is identical to GetOrdersPageInScope so sqlc returns []Order and
-- the repo's assemble() is reused unchanged.
SELECT id, tenant_id, location_id, schema_version, source, source_event_id,
       kitchen_order_number, status, source_status, currency,
       subtotal_cents, tax_cents, tip_cents, total_cents, fees, discounts,
       customer_display_name, customer_email, customer_phone, customer_external_id,
       fulfillment_type, fulfillment_address, promised_at, table_id,
       notes, source_metadata, raw_payload,
       placed_at, received_at, updated_at, estimated_ready_at, driver_name, floor_at,
       agent_ack_at, agent_status, agent_status_at, agent_batch_id
FROM orders
WHERE tenant_id = sqlc.arg('tenant_id') AND location_id = sqlc.arg('location_id')
  AND (sqlc.arg('q')::text = '' OR customer_display_name ILIKE '%' || sqlc.arg('q') || '%'
                                OR source_event_id ILIKE '%' || sqlc.arg('q') || '%')
  AND (sqlc.arg('status')::text = '' OR status = sqlc.arg('status'))
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR received_at >= sqlc.narg('from_time'))
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR received_at <= sqlc.narg('to_time'))
ORDER BY
  CASE WHEN sqlc.arg('reverse')::boolean THEN received_at END ASC,
  received_at DESC
LIMIT sqlc.arg('lim') OFFSET sqlc.arg('off');

-- name: MarkFloorEventSeen :execrows
-- Idempotency gate for agent floor events: 0 rows = already applied.
INSERT INTO floor_events_seen (event_id, tenant_id, location_id, order_id, type, result)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (event_id) DO NOTHING;

-- name: UpdateOrderFloor :exec
-- Applies an agent floor update (status + mirrored floor fields).
UPDATE orders
SET status = $2, kitchen_order_number = $3, estimated_ready_at = $4,
    driver_name = $5, updated_at = $6, floor_at = $7
WHERE id = $1;

-- name: DeleteLocationDrivers :exec
DELETE FROM drivers WHERE tenant_id = $1 AND location_id = $2;

-- name: InsertDriver :exec
INSERT INTO drivers (id, tenant_id, location_id, name, position, status, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (location_id, id) DO UPDATE SET
    name = excluded.name, position = excluded.position, status = excluded.status,
    updated_at = excluded.updated_at;

-- name: ListLocationDrivers :many
SELECT id, tenant_id, location_id, name, position, status, updated_at
FROM drivers WHERE tenant_id = $1 AND location_id = $2
ORDER BY position, name;

-- name: UpsertDriverShift :exec
-- A driver.shift event (clock-in, then the clock-out for the same shift id),
-- keyed per location.
INSERT INTO driver_shifts (id, tenant_id, location_id, driver_id, driver_name, clock_in, clock_out, auto_out, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
ON CONFLICT (location_id, id) DO UPDATE SET
    driver_name = excluded.driver_name, clock_out = excluded.clock_out,
    auto_out = excluded.auto_out, updated_at = now();

-- name: ListDriverShifts :many
SELECT id, tenant_id, location_id, driver_id, driver_name, clock_in, clock_out, auto_out, updated_at
FROM driver_shifts
WHERE tenant_id = $1 AND location_id = $2 AND clock_in >= $3
ORDER BY clock_in DESC;

-- name: LastFloorEventAt :one
SELECT max(received_at)::timestamptz AS last_at FROM floor_events_seen
WHERE tenant_id = $1 AND location_id = $2;

-- name: GetLineItemsByOrderID :many
SELECT id, order_id, parent_id, position, sku, name, quantity, unit_price_cents, line_total_cents, notes,
       size, menu_item_id, printer_id, routing
FROM line_items WHERE order_id = $1
ORDER BY parent_id NULLS FIRST, position;

-- name: GetPaymentsByOrderID :many
SELECT id, order_id, processor, status, amount_cents, token, last4, captured_at, auth_code
FROM payments WHERE order_id = $1;

-- name: GetStatusEventsByOrderID :many
SELECT order_id, position, from_status, to_status, at, actor, actor_id, reason
FROM status_events WHERE order_id = $1 ORDER BY position;

-- ───────────────────────────────────────────────────────────────────────────
-- Outbox
-- ───────────────────────────────────────────────────────────────────────────

-- name: InsertOutboxEvent :exec
INSERT INTO outbox_events (id, aggregate_id, tenant_id, location_id, type, payload, occurred_at, schema_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertOutboxDispatch :exec
INSERT INTO outbox_dispatches (event_id, handler_name, dispatched_at, attempts, last_error, last_attempt_at, next_attempt_at)
VALUES ($1, $2, NULL, 0, '', NULL, NULL)
ON CONFLICT (event_id, handler_name) DO NOTHING;

-- name: PendingEventsForHandler :many
SELECT e.id, e.aggregate_id, e.tenant_id, e.location_id, e.type, e.payload,
       e.occurred_at, e.schema_version, d.attempts
FROM outbox_events e
JOIN outbox_dispatches d ON d.event_id = e.id
WHERE d.handler_name = $1
  AND d.dispatched_at IS NULL
  AND (d.next_attempt_at IS NULL OR d.next_attempt_at <= NOW())
ORDER BY e.occurred_at ASC, e.id ASC
LIMIT $2;

-- name: ResetDispatchBackoffForScope :exec
-- An agent for this (tenant, location) just (re)subscribed: make every still-
-- pending, backed-off dispatch for the scope due now so its backlog drains
-- within one dispatcher tick instead of waiting out the exponential backoff.
UPDATE outbox_dispatches d
SET next_attempt_at = NULL
FROM outbox_events e
WHERE d.event_id = e.id
  AND d.dispatched_at IS NULL
  AND d.next_attempt_at IS NOT NULL
  AND e.tenant_id = $1
  AND e.location_id = $2;

-- name: MarkDispatchSucceeded :exec
UPDATE outbox_dispatches
SET dispatched_at  = NOW(),
    attempts       = attempts + 1,
    last_error     = '',
    last_attempt_at = NOW(),
    next_attempt_at = NULL
WHERE event_id = $1 AND handler_name = $2;

-- name: MarkDispatchFailed :exec
UPDATE outbox_dispatches
SET attempts        = attempts + 1,
    last_error      = $3,
    last_attempt_at = NOW(),
    next_attempt_at = $4
WHERE event_id = $1 AND handler_name = $2;

-- name: CountPendingDispatches :one
SELECT COUNT(*) FROM outbox_dispatches
WHERE handler_name = $1 AND dispatched_at IS NULL;

-- ═══════════════════════════════════════════════════════════════════════════
-- Menu module
-- ═══════════════════════════════════════════════════════════════════════════

-- name: CreateMenu :one
INSERT INTO menus (tenant_id, location_id, name, description, available, position)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, tenant_id, location_id, name, description, available, position, created_at, updated_at;

-- name: GetMenuByID :one
SELECT id, tenant_id, location_id, name, description, available, position, created_at, updated_at
FROM menus WHERE id = $1 AND tenant_id = $2;

-- name: ListMenusByLocation :many
SELECT id, tenant_id, location_id, name, description, available, position, created_at, updated_at
FROM menus WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateMenu :one
UPDATE menus SET name = $3, description = $4, available = $5, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, name, description, available, position, created_at, updated_at;

-- name: SetMenuPosition :exec
UPDATE menus SET position = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteMenu :exec
DELETE FROM menus WHERE id = $1 AND tenant_id = $2;

-- name: CreateMenuCategory :one
INSERT INTO menu_categories (tenant_id, location_id, menu_id, name, description, available, position, default_printer_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at;

-- name: GetMenuCategoryByID :one
SELECT id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at
FROM menu_categories WHERE id = $1 AND tenant_id = $2;

-- name: ListCategoriesByMenu :many
SELECT id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at
FROM menu_categories WHERE menu_id = $1 ORDER BY position ASC, created_at ASC;

-- name: ListCategoriesByLocation :many
SELECT id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at
FROM menu_categories WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateMenuCategory :one
UPDATE menu_categories SET name = $3, description = $4, available = $5, default_printer_id = $6, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at;

-- name: SetCategoryPosition :exec
UPDATE menu_categories SET position = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteMenuCategory :exec
DELETE FROM menu_categories WHERE id = $1 AND tenant_id = $2;

-- name: CreateMenuItem :one
INSERT INTO menu_items (tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at;

-- name: GetMenuItemByID :one
SELECT id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at
FROM menu_items WHERE id = $1 AND tenant_id = $2;

-- name: ListItemsByCategory :many
SELECT id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at
FROM menu_items WHERE category_id = $1 ORDER BY position ASC, created_at ASC;

-- name: ListItemsByLocation :many
SELECT id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at
FROM menu_items WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateMenuItem :one
UPDATE menu_items SET name = $3, description = $4, price_cents = $5, tax_rate_bps = $6, in_stock = $7, out_until = $8, tags = $9, attributes = $10, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at;

-- name: ListRecentItemNames :many
-- Top-level item names ordered at a location since a time, with counts — the
-- input to the unmatched-names report.
SELECT li.name, count(*)::bigint AS orders, max(o.received_at)::timestamptz AS last_seen
FROM line_items li
JOIN orders o ON o.id = li.order_id
WHERE o.tenant_id = $1 AND o.location_id = $2 AND li.parent_id IS NULL AND o.received_at >= $3
GROUP BY li.name;

-- name: ListRoutingByLocation :many
-- Kitchen routing table for one location: every menu item's name with its
-- effective printer (the item's own printer, else its category's default),
-- best candidate first — available menus/categories first, then menu,
-- category and item position. The router keeps the first row per name.
SELECT i.id AS item_id,
       i.name AS item_name,
       COALESCE(i.printer_id, c.default_printer_id)::uuid AS printer_id
FROM menu_items i
JOIN menu_categories c ON c.id = i.category_id
JOIN menus m ON m.id = c.menu_id
WHERE i.location_id = $1
ORDER BY (m.available AND c.available) DESC, m.position, c.position, i.position, i.created_at;

-- name: SetItemPrinter :exec
UPDATE menu_items SET printer_id = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: SetItemPosition :exec
UPDATE menu_items SET position = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteMenuItem :exec
DELETE FROM menu_items WHERE id = $1 AND tenant_id = $2;

-- name: CreateModifierGroup :one
INSERT INTO modifier_groups (tenant_id, location_id, name, required, min_select, max_select, allow_multiple)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at;

-- name: GetModifierGroupByID :one
SELECT id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at
FROM modifier_groups WHERE id = $1 AND tenant_id = $2;

-- name: ListModifierGroupsByLocation :many
SELECT id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at
FROM modifier_groups WHERE location_id = $1 ORDER BY name ASC;

-- name: UpdateModifierGroup :one
UPDATE modifier_groups SET name = $3, required = $4, min_select = $5, max_select = $6, allow_multiple = $7, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at;

-- name: DeleteModifierGroup :exec
DELETE FROM modifier_groups WHERE id = $1 AND tenant_id = $2;

-- name: DetachGroupFromAllItems :exec
DELETE FROM item_modifier_groups WHERE group_id = $1 AND tenant_id = $2;

-- name: CreateModifierOption :one
INSERT INTO modifier_options (tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at;

-- name: ListOptionsByGroup :many
SELECT id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at
FROM modifier_options WHERE group_id = $1 ORDER BY position ASC, created_at ASC;

-- name: ListOptionsByLocation :many
SELECT id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at
FROM modifier_options WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateModifierOption :one
UPDATE modifier_options SET name = $3, price_delta_cents = $4, in_stock = $5, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at;

-- name: SetOptionPosition :exec
UPDATE modifier_options SET position = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteModifierOption :exec
DELETE FROM modifier_options WHERE id = $1 AND tenant_id = $2;

-- name: AttachModifierGroup :exec
INSERT INTO item_modifier_groups (tenant_id, item_id, group_id, position)
VALUES ($1, $2, $3, $4)
ON CONFLICT (item_id, group_id) DO UPDATE SET position = EXCLUDED.position;

-- name: DetachModifierGroup :exec
DELETE FROM item_modifier_groups WHERE tenant_id = $1 AND item_id = $2 AND group_id = $3;

-- name: ListGroupsForItem :many
SELECT g.id, g.tenant_id, g.location_id, g.name, g.required, g.min_select, g.max_select, g.allow_multiple, g.created_at, g.updated_at, img.position AS attach_position
FROM item_modifier_groups img
JOIN modifier_groups g ON g.id = img.group_id
WHERE img.item_id = $1 AND img.tenant_id = $2
ORDER BY img.position ASC;

-- name: ListItemModifierGroupsByLocation :many
SELECT img.item_id, img.group_id, img.position
FROM item_modifier_groups img
JOIN menu_items i ON i.id = img.item_id
WHERE i.location_id = $1
ORDER BY img.position ASC;

-- name: CountItemsForGroup :one
SELECT count(*) FROM item_modifier_groups WHERE group_id = $1 AND tenant_id = $2;

-- ═══════════════════════════════════════════════════════════════════════════
-- Operators (call-center order-takers)
-- ═══════════════════════════════════════════════════════════════════════════

-- name: CreateOperator :one
INSERT INTO operators (username, password_hash, display_name)
VALUES ($1, $2, $3)
RETURNING id, username, display_name, created_at, updated_at;

-- name: GetOperatorByID :one
SELECT id, username, display_name, created_at, updated_at FROM operators WHERE id = $1;

-- name: GetOperatorAuthByUsername :one
SELECT id, password_hash FROM operators WHERE username = $1;

-- name: ListOperators :many
SELECT id, username, display_name, created_at, updated_at FROM operators ORDER BY created_at ASC;

-- name: UpdateOperatorProfile :one
UPDATE operators SET display_name = $2, updated_at = now() WHERE id = $1
RETURNING id, username, display_name, created_at, updated_at;

-- name: SetOperatorPassword :exec
UPDATE operators SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: DeleteOperator :exec
DELETE FROM operators WHERE id = $1;

-- name: DeleteOperatorLocations :exec
DELETE FROM operator_locations WHERE operator_id = $1;

-- name: AddOperatorLocation :exec
INSERT INTO operator_locations (operator_id, tenant_id, location_id)
VALUES ($1, $2, $3)
ON CONFLICT (operator_id, location_id) DO NOTHING;

-- name: ListOperatorLocations :many
SELECT operator_id, tenant_id, location_id FROM operator_locations WHERE operator_id = $1;

-- name: CreateOperatorToken :one
INSERT INTO operator_tokens (operator_id, token_hash, token_prefix, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id, operator_id, token_hash, token_prefix, created_at, last_used_at, expires_at, revoked_at;

-- name: GetActiveOperatorTokenByHash :one
SELECT t.id AS token_id, o.id AS operator_id, o.username, o.display_name
FROM operator_tokens t
JOIN operators o ON o.id = t.operator_id
WHERE t.token_hash = $1
  AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > now());

-- name: TouchOperatorToken :exec
UPDATE operator_tokens SET last_used_at = now() WHERE id = $1;

-- name: RevokeAllOperatorTokens :exec
UPDATE operator_tokens SET revoked_at = now() WHERE operator_id = $1 AND revoked_at IS NULL;

-- App releases (Quibase APKs) ----------------------------------------------------

-- name: CreateAppRelease :one
INSERT INTO app_releases (app, version_code, version_name, sha256, size, apk)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, app, version_code, version_name, sha256, size, created_at;

-- name: ListAppReleases :many
SELECT id, app, version_code, version_name, sha256, size, created_at
FROM app_releases WHERE app = $1 ORDER BY version_code DESC;

-- name: GetLatestAppRelease :one
SELECT id, app, version_code, version_name, sha256, size, created_at
FROM app_releases WHERE app = $1 ORDER BY version_code DESC LIMIT 1;

-- name: GetAppReleaseAPK :one
SELECT apk FROM app_releases WHERE app = $1 AND version_code = $2;

-- name: PruneAppReleases :exec
DELETE FROM app_releases a
WHERE a.app = $1 AND a.version_code NOT IN (
    SELECT b.version_code FROM app_releases b WHERE b.app = $1 ORDER BY b.version_code DESC LIMIT $2
);

-- name: DeleteAppRelease :execrows
DELETE FROM app_releases WHERE app = $1 AND version_code = $2;

-- Agent reliability + recovery (00009) -------------------------------------------

-- name: MarkOrdersAcked :execrows
UPDATE orders SET agent_ack_at = now()
WHERE tenant_id = $1 AND location_id = $2 AND id = ANY(sqlc.arg(ids)::uuid[]) AND agent_ack_at IS NULL;

-- name: ListUnackedOrders :many
-- Orders the agent hasn't confirmed: re-sent until it does.
SELECT id, tenant_id, location_id, received_at FROM orders
WHERE agent_ack_at IS NULL AND status = 'received'
  AND received_at < sqlc.arg(older_than) AND received_at > sqlc.arg(newer_than)
ORDER BY received_at ASC
LIMIT sqlc.arg(max_rows);

-- name: CountUnackedOrders :one
SELECT count(*) FROM orders
WHERE agent_ack_at IS NULL AND status = 'received'
  AND received_at < sqlc.arg(older_than) AND received_at > sqlc.arg(newer_than);

-- name: SetOrderAgentStatus :execrows
UPDATE orders
SET agent_status = sqlc.arg(agent_status), agent_status_at = sqlc.arg(at),
    agent_batch_id = COALESCE(sqlc.narg(batch_id), agent_batch_id),
    agent_ack_at = COALESCE(agent_ack_at, now())
WHERE id = sqlc.arg(id) AND tenant_id = sqlc.arg(tenant_id) AND location_id = sqlc.arg(location_id)
  AND (agent_status_at IS NULL OR agent_status_at <= sqlc.arg(at));

-- name: UpsertAgentState :exec
INSERT INTO agent_state (location_id, tenant_id, state, at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (location_id) DO UPDATE
SET state = EXCLUDED.state, at = EXCLUDED.at, updated_at = now()
WHERE agent_state.at <= EXCLUDED.at;

-- name: GetAgentState :one
SELECT location_id, tenant_id, state, at, updated_at FROM agent_state WHERE location_id = $1;

-- name: ListRestoreOrderIDs :many
-- A replacement agent's open batch: everything the old agent filed under that
-- batch, plus orders nobody accepted yet since the batch opened.
SELECT id FROM orders
WHERE tenant_id = sqlc.arg(tenant_id) AND location_id = sqlc.arg(location_id)
  AND (agent_batch_id = sqlc.narg(batch_id)
       OR (status = 'received' AND received_at >= sqlc.arg(since)))
ORDER BY received_at ASC
LIMIT 2000;

-- name: GetOrderAgentFields :one
SELECT agent_status, agent_batch_id, agent_ack_at FROM orders
WHERE id = $1 AND tenant_id = $2 AND location_id = $3;

-- name: UpsertAgentHealth :exec
INSERT INTO agent_health (location_id, tenant_id, device_id, health, at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (location_id) DO UPDATE
SET device_id = EXCLUDED.device_id, health = EXCLUDED.health, at = EXCLUDED.at;

-- name: ListAgentHealth :many
SELECT h.location_id, h.tenant_id, h.device_id, h.health, h.at, l.name AS location_name
FROM agent_health h JOIN locations l ON l.id = h.location_id
ORDER BY l.name;

-- name: InsertAgentBackup :one
INSERT INTO agent_backups (tenant_id, location_id, device_id, size, sha256, data)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, tenant_id, location_id, device_id, size, sha256, created_at;

-- name: PruneAgentBackups :exec
DELETE FROM agent_backups a
WHERE a.location_id = $1 AND a.id NOT IN (
    SELECT b.id FROM agent_backups b WHERE b.location_id = $1 ORDER BY b.created_at DESC LIMIT 2
);

-- name: ListAgentBackups :many
SELECT id, tenant_id, location_id, device_id, size, sha256, created_at FROM agent_backups
WHERE (sqlc.narg(location_id)::uuid IS NULL OR location_id = sqlc.narg(location_id))
ORDER BY created_at DESC LIMIT 100;

-- name: GetAgentBackupData :one
SELECT data, location_id, created_at FROM agent_backups WHERE id = $1;

-- ---------------------------------------------------------------------------
-- 00010_diagnostics: tablet report tokens + troubleshooting reports
-- ---------------------------------------------------------------------------

-- name: RevokeTabletTokensForTablet :execrows
UPDATE tablet_report_tokens SET revoked_at = now()
WHERE location_id = $1 AND tablet_id = $2 AND revoked_at IS NULL;

-- name: InsertTabletReportToken :one
INSERT INTO tablet_report_tokens (tenant_id, location_id, tablet_id, tablet_name, token_hash, key_prefix)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: RevokeExcessTabletTokens :execrows
-- Keep the newest `keep` active tokens of a location; revoke the rest.
UPDATE tablet_report_tokens t SET revoked_at = now()
WHERE t.location_id = sqlc.arg(location_id)::uuid AND t.revoked_at IS NULL AND t.id NOT IN (
    SELECT k.id FROM tablet_report_tokens k
    WHERE k.location_id = sqlc.arg(location_id)::uuid AND k.revoked_at IS NULL
    ORDER BY k.created_at DESC, k.id DESC
    LIMIT sqlc.arg(keep)::int
);

-- name: GetActiveTabletTokenByHash :one
SELECT id, tenant_id, location_id, tablet_id, tablet_name, last_used_at
FROM tablet_report_tokens
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: TouchTabletToken :exec
UPDATE tablet_report_tokens SET last_used_at = now() WHERE id = $1;

-- name: ListTabletTokens :many
SELECT k.id, k.tenant_id, k.location_id, k.tablet_id, k.tablet_name, k.key_prefix,
       k.created_at, k.last_used_at, k.revoked_at, l.name AS location_name
FROM tablet_report_tokens k JOIN locations l ON l.id = k.location_id
WHERE (sqlc.narg(location_id)::uuid IS NULL OR k.location_id = sqlc.narg(location_id))
ORDER BY k.created_at DESC
LIMIT 500;

-- name: RevokeTabletTokenByID :execrows
UPDATE tablet_report_tokens SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1;

-- name: DiagReportExists :one
SELECT EXISTS (SELECT 1 FROM diag_reports WHERE id = $1);

-- name: InsertDiagReport :execrows
INSERT INTO diag_reports (id, tenant_id, location_id, tablet_id, tablet_name, via, kind, scenario,
                          outcome, headline, cause, fixed_by, app_version, agent_version, report, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (id) DO NOTHING;

-- name: LastDiagPage :one
-- When this (location, scenario, outcome) last paged the owner (throttle).
SELECT COALESCE(max(paged_at), 'epoch'::timestamptz)::timestamptz AS last_paged
FROM diag_reports
WHERE location_id = $1 AND scenario = $2 AND outcome = $3 AND paged_at IS NOT NULL;

-- name: MarkDiagReportPaged :exec
UPDATE diag_reports SET paged_at = now() WHERE id = $1;

-- name: ListDiagReports :many
SELECT r.id, r.tenant_id, r.location_id, r.tablet_id, r.tablet_name, r.via, r.kind, r.scenario,
       r.outcome, r.headline, r.cause, r.fixed_by, r.app_version, r.agent_version,
       r.created_at, r.received_at, r.paged_at, r.resolved_at, t.name AS store_name
FROM diag_reports r JOIN tenants t ON t.id = r.tenant_id
WHERE (sqlc.narg(tenant_id)::uuid IS NULL OR r.tenant_id = sqlc.narg(tenant_id))
  AND (sqlc.narg(kind)::text IS NULL OR r.kind = sqlc.narg(kind))
  AND (sqlc.narg(outcome)::text IS NULL OR r.outcome = sqlc.narg(outcome))
  AND (NOT sqlc.arg(unresolved)::bool OR r.resolved_at IS NULL)
ORDER BY r.received_at DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: GetDiagReport :one
SELECT r.id, r.tenant_id, r.location_id, r.tablet_id, r.tablet_name, r.via, r.kind, r.scenario,
       r.outcome, r.headline, r.cause, r.fixed_by, r.app_version, r.agent_version,
       r.created_at, r.received_at, r.paged_at, r.resolved_at, t.name AS store_name, r.report
FROM diag_reports r JOIN tenants t ON t.id = r.tenant_id
WHERE r.id = $1;

-- name: FindDiagReportIDsByPrefix :many
-- The page links carry only the first 8 hex chars of a report id.
SELECT id FROM diag_reports WHERE id::text LIKE sqlc.arg(prefix)::text || '%' ORDER BY received_at DESC LIMIT 2;

-- name: SetDiagReportResolved :execrows
UPDATE diag_reports
SET resolved_at = CASE WHEN sqlc.arg(resolved)::bool THEN COALESCE(resolved_at, now()) ELSE NULL END
WHERE id = sqlc.arg(id);

-- name: PruneDiagReports :execrows
DELETE FROM diag_reports WHERE received_at < $1;
