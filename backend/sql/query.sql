-- ───────────────────────────────────────────────────────────────────────────
-- Tenants
-- ───────────────────────────────────────────────────────────────────────────

-- name: CreateTenant :one
-- username/password_hash are optional (sqlc.narg → NULL when absent) so admin
-- can still create credential-less tenants. Full-column RETURNING keeps sqlc
-- mapping to the models.Tenant table struct (see toDomainTenant).
INSERT INTO tenants (slug, name, username, password_hash, store_id, test_store)
VALUES ($1, $2, sqlc.narg('username'), sqlc.narg('password_hash'), sqlc.narg('store_id'), sqlc.arg('test_store'))
RETURNING id, slug, name, config, username, password_hash, created_at, store_id, test_store, retired_at;

-- name: GetTenantByID :one
SELECT id, slug, name, config, username, password_hash, created_at, store_id, test_store, retired_at FROM tenants WHERE id = $1;

-- name: GetTenantAuthByUsername :one
-- Login lookup: tenant id + stored password hash for a username.
SELECT id, password_hash FROM tenants WHERE username = $1;

-- name: SetTenantCredentials :exec
-- Set/rotate a tenant's store-manager login (username + already-hashed
-- password). Every manager session issued before it ends (session_epoch, M8;
-- read / rehashed by plain SQL in repositories/tenancy_repo.go).
UPDATE tenants SET username = $2, password_hash = $3, session_epoch = session_epoch + 1 WHERE id = $1;

-- name: ListTenants :many
SELECT id, slug, name, config, username, password_hash, created_at, store_id, test_store, retired_at FROM tenants ORDER BY created_at ASC;

-- name: GetTenantByStoreID :one
-- Order routing: the external vendor's store_id → tenant (see app/tenancy.ResolveStore).
SELECT id, slug, name, config, username, password_hash, created_at, store_id, test_store, retired_at FROM tenants WHERE store_id = $1 AND retired_at IS NULL;

-- name: SetTenantStoreID :one
-- Set/clear (NULL) a tenant's vendor store_id. Unique when present.
UPDATE tenants SET store_id = sqlc.narg('store_id') WHERE id = $1
RETURNING id, slug, name, config, username, password_hash, created_at, store_id, test_store, retired_at;

-- name: UpdateTenantConfig :one
UPDATE tenants SET config = $2 WHERE id = $1
RETURNING id, slug, name, config, username, password_hash, created_at, store_id, test_store, retired_at;

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
          phone, address_line1, address_line2, config, store_type, created_at,
          lat, lng, geo_source, delivery_radius_m, map_url, map_sha256, map_version, map_bbox;

-- name: GetLocationByID :one
SELECT id, tenant_id, slug, name, timezone, daily_reset_hour,
       phone, address_line1, address_line2, config, store_type, created_at,
       lat, lng, geo_source, delivery_radius_m, map_url, map_sha256, map_version, map_bbox
FROM locations WHERE id = $1;

-- name: ListLocationsByTenant :many
SELECT id, tenant_id, slug, name, timezone, daily_reset_hour,
       phone, address_line1, address_line2, config, store_type, created_at,
       lat, lng, geo_source, delivery_radius_m, map_url, map_sha256, map_version, map_bbox
FROM locations WHERE tenant_id = $1 ORDER BY created_at ASC;

-- name: ListAllLocations :many
SELECT id, tenant_id, slug, name, timezone, daily_reset_hour,
       phone, address_line1, address_line2, config, store_type, created_at,
       lat, lng, geo_source, delivery_radius_m, map_url, map_sha256, map_version, map_bbox
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
          phone, address_line1, address_line2, config, store_type, created_at,
          lat, lng, geo_source, delivery_radius_m, map_url, map_sha256, map_version, map_bbox;

-- name: SetLocationPin :one
-- A manager / admin placed the store pin by hand: geocodes never move it.
UPDATE locations SET lat = $2, lng = $3, geo_source = 'manual'
WHERE id = $1
RETURNING id, tenant_id, slug, name, timezone, daily_reset_hour,
          phone, address_line1, address_line2, config, store_type, created_at,
          lat, lng, geo_source, delivery_radius_m, map_url, map_sha256, map_version, map_bbox;

-- name: SetLocationGeocoded :execrows
-- The store address was geocoded. Never overrides a manual pin.
UPDATE locations SET lat = $2, lng = $3, geo_source = 'geocoded'
WHERE id = $1 AND geo_source <> 'manual';

-- name: SetLocationMap :one
UPDATE locations SET map_url = $2, map_sha256 = $3, map_version = $4, map_bbox = $5
WHERE id = $1
RETURNING id, tenant_id, slug, name, timezone, daily_reset_hour,
          phone, address_line1, address_line2, config, store_type, created_at,
          lat, lng, geo_source, delivery_radius_m, map_url, map_sha256, map_version, map_bbox;

-- name: ListLocationsNeedingGeocode :many
-- Startup backfill: stores with an address but no position yet.
SELECT id, tenant_id FROM locations
WHERE address_line1 <> '' AND lat IS NULL AND geo_source <> 'manual';

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
INSERT INTO printers (tenant_id, location_id, name, ip, mac, print_columns)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt, mac, print_columns;

-- name: GetPrinterByID :one
SELECT id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt, mac, print_columns
FROM printers WHERE id = $1;

-- name: ListPrintersByLocation :many
SELECT id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt, mac, print_columns
FROM printers WHERE location_id = $1 ORDER BY name ASC;

-- name: UpdatePrinter :one
UPDATE printers SET name = $2, ip = $3, updated_at = now()
WHERE id = $1
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt, mac, print_columns;

-- name: ClearReceiptPrinter :exec
-- Unflag the location's current receipt printer (one per location).
UPDATE printers SET is_receipt = false, updated_at = now()
WHERE location_id = $1 AND is_receipt;

-- name: SetPrinterReceipt :one
UPDATE printers SET is_receipt = $2, updated_at = now()
WHERE id = $1
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt, mac, print_columns;

-- name: MovePrinterIP :one
-- The Pi followed a printer to a new IP. Applied only while the printer still
-- has the IP the Pi saw (a manager edit in between wins); an empty mac keeps
-- the stored one.
UPDATE printers
SET ip = sqlc.arg(new_ip),
    mac = CASE WHEN sqlc.arg(mac)::text = '' THEN mac ELSE sqlc.arg(mac)::text END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND location_id = sqlc.arg(location_id) AND ip = sqlc.arg(old_ip)
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt, mac, print_columns;

-- name: SetPrinterColumns :one
-- The printer's print width in font-A characters (32 | 42 | 48): Store ›
-- Printers or the Pi's width test (RPC printer.width).
UPDATE printers SET print_columns = $2, updated_at = now()
WHERE id = $1
RETURNING id, tenant_id, location_id, name, ip, created_at, updated_at, is_receipt, mac, print_columns;

-- name: SetPrinterMAC :exec
UPDATE printers SET mac = $2, updated_at = now() WHERE id = $1;

-- name: InsertPrinterIPChange :exec
INSERT INTO printer_ip_changes (tenant_id, location_id, printer_id, old_ip, new_ip, mac, source)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListPrinterIPChanges :many
SELECT c.id, c.printer_id, p.name AS printer_name, c.old_ip, c.new_ip, c.mac, c.source, c.at
FROM printer_ip_changes c JOIN printers p ON p.id = c.printer_id
WHERE c.location_id = $1
ORDER BY c.at DESC
LIMIT sqlc.arg(max_rows)::int;

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
    size, menu_item_id, printer_id, routing, portion, mode, coupon, coupon_no
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
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
       agent_ack_at, agent_status, agent_status_at, agent_batch_id,
       delivery_fee_cents, driver_fee_cents, service_fee_cents, card_fee_cents, discount_cents, money_changed_at
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
       agent_ack_at, agent_status, agent_status_at, agent_batch_id,
       delivery_fee_cents, driver_fee_cents, service_fee_cents, card_fee_cents, discount_cents, money_changed_at
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
       agent_ack_at, agent_status, agent_status_at, agent_batch_id,
       delivery_fee_cents, driver_fee_cents, service_fee_cents, card_fee_cents, discount_cents, money_changed_at
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
       agent_ack_at, agent_status, agent_status_at, agent_batch_id,
       delivery_fee_cents, driver_fee_cents, service_fee_cents, card_fee_cents, discount_cents, money_changed_at
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
       agent_ack_at, agent_status, agent_status_at, agent_batch_id,
       delivery_fee_cents, driver_fee_cents, service_fee_cents, card_fee_cents, discount_cents, money_changed_at
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
-- Idempotency gate for agent floor events: 0 rows = already seen. An event
-- main refused (invalid / not_found) is looked at again when the Pi sends it
-- again (it keeps refused payments, counter orders and customers queued), so
-- a newer main can still apply it.
INSERT INTO floor_events_seen (event_id, tenant_id, location_id, order_id, type, result)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (event_id) DO UPDATE SET result = EXCLUDED.result, order_id = EXCLUDED.order_id, received_at = now()
WHERE floor_events_seen.result IN ('invalid', 'not_found') AND floor_events_seen.location_id = EXCLUDED.location_id;

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
       size, menu_item_id, printer_id, routing, portion, mode, coupon, coupon_no
FROM line_items WHERE order_id = $1
ORDER BY parent_id NULLS FIRST, position;

-- name: GetPaymentsByOrderID :many
SELECT id, order_id, processor, status, amount_cents, token, last4, captured_at, auth_code, tip_cents
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

-- name: CountPendingDispatchesByTenant :many
-- outbox.events.pending per store: one row per tenant (0 when drained), so a
-- store's series never goes stale while it exists.
SELECT t.id AS tenant_id, COALESCE(p.n, 0)::bigint AS pending
FROM tenants t
LEFT JOIN (
    SELECT e.tenant_id, COUNT(*) AS n
    FROM outbox_dispatches d
    JOIN outbox_events e ON e.id = d.event_id
    WHERE d.handler_name = $1 AND d.dispatched_at IS NULL
    GROUP BY e.tenant_id
) p ON p.tenant_id = t.id;

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
INSERT INTO menu_categories (tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, no_kitchen_ticket)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at, no_kitchen_ticket;

-- name: GetMenuCategoryByID :one
SELECT id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at, no_kitchen_ticket
FROM menu_categories WHERE id = $1 AND tenant_id = $2;

-- name: ListCategoriesByMenu :many
SELECT id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at, no_kitchen_ticket
FROM menu_categories WHERE menu_id = $1 ORDER BY position ASC, created_at ASC;

-- name: ListCategoriesByLocation :many
SELECT id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at, no_kitchen_ticket
FROM menu_categories WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateMenuCategory :one
UPDATE menu_categories SET name = $3, description = $4, available = $5, default_printer_id = $6, no_kitchen_ticket = $7, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, menu_id, name, description, available, position, default_printer_id, created_at, updated_at, no_kitchen_ticket;

-- name: SetCategoryPosition :exec
UPDATE menu_categories SET position = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteMenuCategory :exec
DELETE FROM menu_categories WHERE id = $1 AND tenant_id = $2;

-- name: CreateMenuItem :one
INSERT INTO menu_items (tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at, kitchen_name, sku, out_hold, no_kitchen_ticket;

-- name: GetMenuItemByID :one
SELECT id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at, kitchen_name, sku, out_hold, no_kitchen_ticket
FROM menu_items WHERE id = $1 AND tenant_id = $2;

-- name: ListItemsByCategory :many
SELECT id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at, kitchen_name, sku, out_hold, no_kitchen_ticket
FROM menu_items WHERE category_id = $1 ORDER BY position ASC, created_at ASC;

-- name: ListItemsByLocation :many
SELECT id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at, kitchen_name, sku, out_hold, no_kitchen_ticket
FROM menu_items WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateMenuItem :one
UPDATE menu_items SET name = $3, description = $4, price_cents = $5, tax_rate_bps = $6, in_stock = $7, out_until = $8, tags = $9, attributes = $10, updated_at = now(),
    out_hold = CASE WHEN $7 THEN false ELSE out_hold END
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, category_id, name, description, price_cents, tax_rate_bps, in_stock, out_until, tags, printer_id, position, attributes, created_at, updated_at, kitchen_name, sku, out_hold, no_kitchen_ticket;

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
-- effective printer (the item's own printer, else its category's default;
-- none and no_kitchen when the item, or its category for an item without
-- its own printer, has "no kitchen ticket", 00041), best candidate first — available menus/categories first, then menu,
-- category and item position. The router keeps the first row per name.
SELECT i.id AS item_id,
       i.name AS item_name,
       (CASE WHEN i.no_kitchen_ticket OR (i.printer_id IS NULL AND c.no_kitchen_ticket) THEN NULL
             ELSE COALESCE(i.printer_id, c.default_printer_id) END)::uuid AS printer_id,
       (i.no_kitchen_ticket OR (i.printer_id IS NULL AND c.no_kitchen_ticket))::boolean AS no_kitchen
FROM menu_items i
JOIN menu_categories c ON c.id = i.category_id
JOIN menus m ON m.id = c.menu_id
WHERE i.location_id = $1
ORDER BY (m.available AND c.available) DESC, m.position, c.position, i.position, i.created_at;

-- name: SetItemPrinter :exec
UPDATE menu_items SET printer_id = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: SetItemNoKitchen :exec
-- "No kitchen ticket" on an item (00041).
UPDATE menu_items SET no_kitchen_ticket = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: MoveMenuItem :exec
-- The bank import (bankimport): an item moves to another category of the location.
UPDATE menu_items SET category_id = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: MoveMenuCategory :exec
-- The bank import: a category moves to another menu of the location.
UPDATE menu_categories SET menu_id = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: SetItemPosition :exec
UPDATE menu_items SET position = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteMenuItem :exec
DELETE FROM menu_items WHERE id = $1 AND tenant_id = $2;

-- name: CreateModifierGroup :one
INSERT INTO modifier_groups (tenant_id, location_id, name, required, min_select, max_select, allow_multiple)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at, portions;

-- name: GetModifierGroupByID :one
SELECT id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at, portions
FROM modifier_groups WHERE id = $1 AND tenant_id = $2;

-- name: ListModifierGroupsByLocation :many
SELECT id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at, portions
FROM modifier_groups WHERE location_id = $1 ORDER BY name ASC;

-- name: UpdateModifierGroup :one
UPDATE modifier_groups SET name = $3, required = $4, min_select = $5, max_select = $6, allow_multiple = $7, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, name, required, min_select, max_select, allow_multiple, created_at, updated_at, portions;

-- name: DeleteModifierGroup :exec
DELETE FROM modifier_groups WHERE id = $1 AND tenant_id = $2;

-- name: DetachGroupFromAllItems :exec
DELETE FROM item_modifier_groups WHERE group_id = $1 AND tenant_id = $2;

-- name: CreateModifierOption :one
INSERT INTO modifier_options (tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at, is_default, kitchen_name, out_hold;

-- name: ListOptionsByGroup :many
SELECT id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at, is_default, kitchen_name, out_hold
FROM modifier_options WHERE group_id = $1 ORDER BY position ASC, created_at ASC;

-- name: ListOptionsByLocation :many
SELECT id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at, is_default, kitchen_name, out_hold
FROM modifier_options WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateModifierOption :one
UPDATE modifier_options SET name = $3, price_delta_cents = $4, in_stock = $5, updated_at = now(),
    out_hold = CASE WHEN $5 THEN false ELSE out_hold END
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, location_id, group_id, name, price_delta_cents, in_stock, position, created_at, updated_at, is_default, kitchen_name, out_hold;

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

-- name: MarkOrdersAcked :many
-- Returns received_at of each newly acked order (orders.ack.latency).
UPDATE orders SET agent_ack_at = now()
WHERE tenant_id = $1 AND location_id = $2 AND id = ANY(sqlc.arg(ids)::uuid[]) AND agent_ack_at IS NULL
RETURNING received_at;

-- name: ListUnackedOrders :many
-- Orders the agent hasn't confirmed: re-sent until it does.
SELECT id, tenant_id, location_id, received_at FROM orders
WHERE agent_ack_at IS NULL AND status = 'received'
  AND received_at < sqlc.arg(older_than) AND received_at > sqlc.arg(newer_than)
ORDER BY received_at ASC
LIMIT sqlc.arg(max_rows);

-- name: CountUnackedOrdersByTenant :many
-- orders.unacked per store: one row per tenant (0 when none).
SELECT t.id AS tenant_id, COALESCE(u.n, 0)::bigint AS unacked
FROM tenants t
LEFT JOIN (
    SELECT tenant_id, count(*) AS n FROM orders
    WHERE agent_ack_at IS NULL AND status = 'received'
      AND received_at < sqlc.arg(older_than) AND received_at > sqlc.arg(newer_than)
    GROUP BY tenant_id
) u ON u.tenant_id = t.id;

-- name: GetStoreSlugs :one
-- Metric labels (observability.StoreLabels): tenant + location slug by a
-- tenant id or a location id (pass uuid.Nil for the other one).
SELECT t.slug AS tenant_slug, l.slug AS location_slug
FROM tenants t
JOIN locations l ON l.tenant_id = t.id
WHERE t.id = sqlc.arg(tenant_id) OR l.id = sqlc.arg(location_id)
ORDER BY l.created_at
LIMIT 1;

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
       OR (status = 'received' AND received_at >= sqlc.arg(since))
       -- 2.1.0: a future order the agent scheduled survives batch close.
       OR (status = 'scheduled' AND received_at >= sqlc.arg(since)::timestamptz - interval '14 days'))
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

-- ---------------------------------------------------------------------------
-- 00012: Pi updates (agent_updates)
-- ---------------------------------------------------------------------------

-- name: InsertAgentUpdate :exec
INSERT INTO agent_updates (id, tenant_id, location_id, action, requested_by, force, state)
VALUES ($1, $2, $3, $4, $5, $6, 'requested');

-- name: SettleAgentUpdateRequest :execrows
-- main's own follow-up after asking the agent (its reply, or no agent / no
-- reply). Only while the row is still 'requested' — a status report the agent
-- already sent is newer and wins.
UPDATE agent_updates SET state = $2, error = $3, updated_at = now()
WHERE id = $1 AND state = 'requested';

-- name: UpsertAgentUpdateStatus :exec
-- The agent's agent.update.status report. Inserted when unknown (an update
-- started on the Pi itself); only the reporting location's row is touched.
INSERT INTO agent_updates (id, tenant_id, location_id, action, requested_by, force, state,
                           from_version, to_version, error, requested_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(location_id), sqlc.arg(action), sqlc.arg(requested_by),
        sqlc.arg(force), sqlc.arg(state), sqlc.arg(from_version), sqlc.arg(to_version), sqlc.arg(error),
        sqlc.arg(requested_at), now())
ON CONFLICT (id) DO UPDATE
SET state        = EXCLUDED.state,
    from_version = COALESCE(NULLIF(EXCLUDED.from_version, ''), agent_updates.from_version),
    to_version   = COALESCE(NULLIF(EXCLUDED.to_version, ''), agent_updates.to_version),
    error        = EXCLUDED.error,
    updated_at   = now()
WHERE agent_updates.location_id = EXCLUDED.location_id;

-- name: ListAgentUpdates :many
SELECT id, tenant_id, location_id, action, requested_by, force, state, from_version, to_version,
       error, requested_at, updated_at
FROM agent_updates
WHERE (sqlc.narg(location_id)::uuid IS NULL OR location_id = sqlc.narg(location_id))
ORDER BY requested_at DESC
LIMIT sqlc.arg(max_rows)::int;

-- ───────────────────────────────────────────────────────────────────────────
-- Geo (2.0.9): geocode cache, per-order geo, driver runs
-- ───────────────────────────────────────────────────────────────────────────

-- name: GetGeoCache :one
SELECT address_key, lat, lng, quality, source, pinned, updated_at
FROM geo_cache WHERE address_key = $1;

-- name: UpsertGeoCacheGeocoded :exec
-- A geocode result. A pinned entry is never overwritten by a geocode.
INSERT INTO geo_cache (address_key, lat, lng, quality, source, pinned, updated_at)
VALUES ($1, $2, $3, $4, $5, false, now())
ON CONFLICT (address_key) DO UPDATE SET
    lat = EXCLUDED.lat, lng = EXCLUDED.lng, quality = EXCLUDED.quality,
    source = EXCLUDED.source, updated_at = now()
WHERE geo_cache.pinned = false;

-- name: UpsertGeoCachePinned :exec
-- A pin a store's staff set. The key is the store's own ('pin|<tenant>|<key>',
-- app/geo PinCacheKey, migration 00007): never the shared address key.
INSERT INTO geo_cache (address_key, lat, lng, quality, source, pinned, updated_at)
VALUES ($1, $2, $3, 'pinned', 'pin', true, now())
ON CONFLICT (address_key) DO UPDATE SET
    lat = EXCLUDED.lat, lng = EXCLUDED.lng, quality = 'pinned',
    source = 'pin', pinned = true, updated_at = now();

-- name: InsertOrderGeo :execrows
INSERT INTO order_geo (order_id, tenant_id, location_id)
VALUES ($1, $2, $3)
ON CONFLICT (order_id) DO NOTHING;

-- name: EnqueueMissingOrderGeo :execrows
-- Safety net for the after-ingest enqueue: recent delivery orders that have
-- no order_geo row yet.
INSERT INTO order_geo (order_id, tenant_id, location_id)
SELECT o.id, o.tenant_id, o.location_id FROM orders o
WHERE o.fulfillment_type = 'delivery' AND o.received_at > @since::timestamptz
  AND NOT EXISTS (SELECT 1 FROM order_geo g WHERE g.order_id = o.id)
ON CONFLICT (order_id) DO NOTHING;

-- name: ListDueOrderGeo :many
SELECT order_id, tenant_id, location_id, state, attempts, created_at, updated_at
FROM order_geo
WHERE pushed_at IS NULL AND next_try_at <= @now::timestamptz
ORDER BY next_try_at ASC
LIMIT @max_rows;

-- name: GetOrderGeo :one
SELECT order_id, tenant_id, location_id, state, lat, lng, quality, distance_m, duration_s,
       polyline, outside_area, store_to_s, to_store_s, attempts, next_try_at, last_error,
       pushed_at, created_at, updated_at, candidates, confirmed_address, confirmed, corrected, note
FROM order_geo WHERE order_id = $1;

-- name: GetOrderForGeo :one
-- What the geo worker needs to locate an order: its delivery address and
-- whether it is still open, plus its store's position + radius.
SELECT o.id, o.tenant_id, o.location_id, o.fulfillment_type, o.fulfillment_address, o.status,
       l.lat AS store_lat, l.lng AS store_lng, l.delivery_radius_m
FROM orders o JOIN locations l ON l.id = o.location_id
WHERE o.id = @order_id AND o.tenant_id = @tenant_id AND o.location_id = @location_id;

-- name: SaveOrderGeoResult :one
-- A final result (located / check / not_found). Clears pushed_at so it is
-- (re)pushed. candidates is set for check only (NULL otherwise).
UPDATE order_geo SET
    state = @state, lat = sqlc.narg('lat'), lng = sqlc.narg('lng'), quality = @quality,
    candidates = sqlc.narg('candidates'),
    distance_m = @distance_m, duration_s = @duration_s, polyline = @polyline,
    outside_area = @outside_area, store_to_s = sqlc.narg('store_to_s'), to_store_s = sqlc.narg('to_store_s'),
    corrected = @corrected, note = '',
    attempts = attempts + 1, next_try_at = now(), last_error = @last_error,
    pushed_at = NULL, updated_at = now()
WHERE order_id = @order_id
RETURNING updated_at;

-- name: SaveOrderGeoRetry :exec
UPDATE order_geo SET attempts = attempts + 1, next_try_at = @next_try_at,
    last_error = @last_error, updated_at = now()
WHERE order_id = @order_id;

-- name: MarkOrderGeoPushed :execrows
-- Only when nothing changed since the pushed result was read. An acked push
-- spends confirmed (only the push right after geo.pin / geo.choose has it).
UPDATE order_geo SET pushed_at = now(), confirmed = false
WHERE order_id = @order_id AND updated_at = @updated_at;

-- name: DeferOrderGeoPush :exec
UPDATE order_geo SET next_try_at = @next_try_at WHERE order_id = @order_id;

-- name: WakeLocationOrderGeo :execrows
-- The location's agent (re)connected: push everything it has not acked now.
UPDATE order_geo SET next_try_at = now()
WHERE tenant_id = @tenant_id AND location_id = @location_id
  AND pushed_at IS NULL AND state <> 'pending';

-- name: RelocateOrderGeo :exec
-- A delivery's address changed after it was placed (the Pi's order.changed,
-- or a takeout moved to a delivery): work it again from scratch, as at
-- intake. created_at restarts so the retry window counts from the change.
INSERT INTO order_geo (order_id, tenant_id, location_id)
VALUES (@order_id, @tenant_id, @location_id)
ON CONFLICT (order_id) DO UPDATE SET state = 'pending', lat = NULL, lng = NULL, quality = '',
    distance_m = 0, duration_s = 0, polyline = '', outside_area = false, store_to_s = NULL, to_store_s = NULL,
    attempts = 0, next_try_at = now(), last_error = '', pushed_at = NULL, created_at = now(), updated_at = now(),
    candidates = NULL, confirmed_address = NULL, confirmed = false, corrected = false, note = '';

-- name: ResetOrderGeoForPin :exec
UPDATE order_geo SET state = 'pending', attempts = 0, next_try_at = now(),
    last_error = '', pushed_at = NULL, candidates = NULL, confirmed_address = NULL,
    confirmed = true, updated_at = now()
WHERE order_id = @order_id;

-- name: ResetOrderGeoForChoice :exec
-- Staff picked a candidate (geo.choose): the pinned geo_cache entry is
-- already saved; re-work the order and push it with the confirmed address.
UPDATE order_geo SET state = 'pending', attempts = 0, next_try_at = now(),
    last_error = '', pushed_at = NULL, candidates = NULL,
    confirmed_address = @confirmed_address, confirmed = true, updated_at = now()
WHERE order_id = @order_id;

-- name: RequeueLocationOrderGeo :execrows
-- The store moved: re-work the open deliveries (addresses are cached, so
-- this re-routes from the new position).
UPDATE order_geo g SET state = 'pending', attempts = 0, next_try_at = now(), pushed_at = NULL,
    confirmed = false, updated_at = now()
FROM orders o
WHERE o.id = g.order_id AND g.tenant_id = @tenant_id AND g.location_id = @location_id
  AND g.state <> 'pending'
  AND o.status NOT IN ('completed', 'cancelled', 'rejected')
  AND o.received_at > @since::timestamptz;

-- name: ListOpenLocatedDeliveries :many
-- The location's other open (not delivered / cancelled) located deliveries,
-- for the drive-time table. received_at bounds it to the current service.
SELECT g.order_id, g.lat, g.lng FROM order_geo g JOIN orders o ON o.id = g.order_id
WHERE g.tenant_id = @tenant_id AND g.location_id = @location_id
  AND g.state = 'located' AND g.lat IS NOT NULL AND g.lng IS NOT NULL
  AND g.order_id <> @exclude_id
  AND o.status NOT IN ('completed', 'cancelled', 'rejected')
  AND o.received_at > @since::timestamptz
ORDER BY o.placed_at ASC
LIMIT @max_rows;

-- name: ListOrderGeoByIDs :many
SELECT g.order_id, g.state, g.lat, g.lng FROM order_geo g
WHERE g.tenant_id = @tenant_id AND g.location_id = @location_id AND g.order_id = ANY(@ids::uuid[]);

-- name: InsertDeliveryRun :execrows
INSERT INTO delivery_runs (id, tenant_id, location_id, batch_id, driver_id, driver_name,
                           plan_id, suggested_ids, sent_ids, stops, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING;

-- name: ListDeliveryRuns :many
SELECT id, tenant_id, location_id, batch_id, driver_id, driver_name, plan_id,
       suggested_ids, sent_ids, stops, at, created_at
FROM delivery_runs WHERE tenant_id = $1 AND location_id = $2
ORDER BY at DESC LIMIT $3;

-- name: GetLocationGeo :one
-- The store's position + radius.
SELECT lat, lng, delivery_radius_m FROM locations WHERE id = @location_id AND tenant_id = @tenant_id;

-- name: SaveOrderGeoSearch :execrows
-- A staff address search (geo.search, 2.2.2) answered: state check with the
-- matches found near the store, or not_found with a note. Replaces whatever
-- the order had (staff searched because it was wrong) and is pushed again.
UPDATE order_geo SET state = @state, lat = NULL, lng = NULL, quality = '',
    candidates = sqlc.narg('candidates'), confirmed_address = NULL, confirmed = false,
    corrected = false, note = @note, distance_m = 0, duration_s = 0, polyline = '',
    outside_area = false, store_to_s = NULL, to_store_s = NULL,
    next_try_at = now(), last_error = '', pushed_at = NULL, updated_at = now()
WHERE order_id = @order_id AND tenant_id = @tenant_id AND location_id = @location_id;

-- name: SetLocationDeliveryRadius :execrows
-- 2.2.0 location.radius: the Pi (tablet settings) sets the delivery radius.
UPDATE locations SET delivery_radius_m = @radius_m
WHERE id = @location_id AND tenant_id = @tenant_id;

-- name: ListOpenLocatedOrderGeoForRadius :many
-- The location's open located deliveries with their outside_area flag (a
-- new radius re-flags them).
SELECT g.order_id, g.lat, g.lng, g.outside_area FROM order_geo g JOIN orders o ON o.id = g.order_id
WHERE g.tenant_id = @tenant_id AND g.location_id = @location_id
  AND g.state = 'located' AND g.lat IS NOT NULL AND g.lng IS NOT NULL
  AND o.status NOT IN ('completed', 'cancelled', 'rejected')
  AND o.received_at > @since::timestamptz;

-- name: SetOrderGeoOutsideArea :execrows
-- A new radius flipped the flag: re-push the row (the worker sends order.geo).
UPDATE order_geo SET outside_area = @outside_area, pushed_at = NULL, next_try_at = now(), updated_at = now()
WHERE order_id = @order_id AND state = 'located' AND outside_area <> @outside_area;

-- ─── Channels (main 3.0) ──────────────────────────────────────────────────

-- name: CreateChannel :one
INSERT INTO channels (tenant_id, location_id, kind, name, key_hash, key_prefix, permissions, builtin)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetChannel :one
SELECT * FROM channels WHERE id = $1;

-- name: ListChannels :many
SELECT * FROM channels ORDER BY created_at ASC;

-- name: ListChannelsByLocation :many
SELECT * FROM channels WHERE location_id = $1 ORDER BY created_at ASC;

-- name: UpdateChannel :one
UPDATE channels SET name = $2, permissions = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetChannelKey :one
UPDATE channels SET key_hash = $2, key_prefix = $3, revoked_at = NULL, updated_at = now()
WHERE id = $1 AND NOT builtin
RETURNING *;

-- name: RevokeChannel :one
UPDATE channels SET revoked_at = COALESCE(revoked_at, now()), updated_at = now()
WHERE id = $1 AND NOT builtin
RETURNING *;

-- name: DeleteChannel :execrows
DELETE FROM channels WHERE id = $1 AND NOT builtin;

-- name: AuthenticateChannel :one
SELECT * FROM channels WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: TouchChannel :exec
UPDATE channels SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- ─── Menu bank (main 3.0) ─────────────────────────────────────────────────

-- name: CreateMenuSize :one
INSERT INTO menu_sizes (tenant_id, location_id, name, short, position)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListMenuSizes :many
SELECT * FROM menu_sizes WHERE location_id = $1 ORDER BY position ASC, created_at ASC;

-- name: GetMenuSize :one
SELECT * FROM menu_sizes WHERE id = $1 AND tenant_id = $2;

-- name: UpdateMenuSize :one
UPDATE menu_sizes SET name = $3, short = $4, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING *;

-- name: SetMenuSizePosition :exec
UPDATE menu_sizes SET position = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteMenuSize :execrows
DELETE FROM menu_sizes WHERE id = $1 AND tenant_id = $2;

-- name: SetItemBank :execrows
UPDATE menu_items SET kitchen_name = $3, sku = $4, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: DeleteItemSizes :exec
DELETE FROM menu_item_sizes WHERE item_id = $1 AND tenant_id = $2;

-- name: InsertItemSize :exec
INSERT INTO menu_item_sizes (tenant_id, location_id, item_id, size_id, price_cents, kitchen_name, position)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListItemSizesByLocation :many
SELECT * FROM menu_item_sizes WHERE location_id = $1 ORDER BY item_id, position ASC;

-- name: SetGroupPortions :execrows
UPDATE modifier_groups SET portions = $3, updated_at = now() WHERE id = $1 AND tenant_id = $2;

-- name: SetOptionBank :execrows
UPDATE modifier_options SET is_default = $3, kitchen_name = $4, updated_at = now()
WHERE id = $1 AND tenant_id = $2 AND group_id = $5;

-- name: DeleteOptionPrices :exec
DELETE FROM modifier_option_prices WHERE option_id = $1 AND tenant_id = $2;

-- name: InsertOptionPrice :exec
INSERT INTO modifier_option_prices (tenant_id, location_id, option_id, size_id, price_cents)
VALUES ($1, $2, $3, $4, $5);

-- name: ListOptionPricesByLocation :many
SELECT * FROM modifier_option_prices WHERE location_id = $1;

-- name: GetMenuVersion :one
SELECT version, changed_at FROM menu_versions WHERE location_id = $1;

-- ─── Menu views (main 3.0) ────────────────────────────────────────────────

-- name: CreateMenuView :one
INSERT INTO menu_views (tenant_id, location_id, kind, name, rules)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetMenuView :one
SELECT * FROM menu_views WHERE id = $1;

-- name: ListMenuViews :many
SELECT * FROM menu_views WHERE location_id = $1 ORDER BY created_at ASC;

-- name: FirstMenuViewOfKind :one
SELECT * FROM menu_views WHERE location_id = $1 AND kind = $2 ORDER BY created_at ASC LIMIT 1;

-- name: UpdateMenuView :one
UPDATE menu_views SET name = $2, rules = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteMenuView :execrows
DELETE FROM menu_views WHERE id = $1;

-- name: GetMenuViewLayout :one
SELECT layout FROM menu_views WHERE id = $1;

-- name: SetMenuViewLayout :execrows
UPDATE menu_views SET layout = $2, updated_at = now()
WHERE id = $1 AND kind = 'counter';

-- name: CreateMenuViewEntry :one
INSERT INTO menu_view_entries (tenant_id, location_id, view_id, kind, anchor, display_name, description, photo, category,
    position, hidden, price_override_cents, extra)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetMenuViewEntry :one
SELECT * FROM menu_view_entries WHERE id = $1;

-- name: ListMenuViewEntries :many
SELECT * FROM menu_view_entries WHERE view_id = $1 ORDER BY position ASC, created_at ASC;

-- name: UpdateMenuViewEntry :one
UPDATE menu_view_entries SET anchor = $2, display_name = $3, description = $4, photo = $5, category = $6, position = $7,
    hidden = $8, price_override_cents = $9, extra = $10, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteMenuViewEntry :execrows
DELETE FROM menu_view_entries WHERE id = $1;

-- name: GetMenuViewBuild :one
SELECT * FROM menu_view_builds WHERE view_id = $1;

-- name: SaveMenuViewBuild :exec
INSERT INTO menu_view_builds (view_id, menu_version, build, built_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (view_id) DO UPDATE SET menu_version = EXCLUDED.menu_version, build = EXCLUDED.build, built_at = now();

-- name: SetChannelView :one
UPDATE channels SET view_id = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- ─── Store status (main 3.0 step 3) ─────────────────────────────────────────

-- name: EnsureStoreOrdering :exec
-- restocked_at starts at now: 86'd items come back at the next reset hour,
-- never right when the row is first made.
INSERT INTO store_ordering (location_id, tenant_id, restocked_at) VALUES ($1, $2, now())
ON CONFLICT (location_id) DO NOTHING;

-- name: GetStoreOrdering :one
SELECT * FROM store_ordering WHERE location_id = $1;

-- name: UpdateStoreOrdering :one
UPDATE store_ordering SET pickup_enabled = $2, delivery_enabled = $3, pickup_eta_minutes = $4,
       delivery_eta_minutes = $5, schedule_days = $6, tip_max_cents = $7, updated_at = now()
WHERE location_id = $1
RETURNING *;

-- name: SetStorePause :one
UPDATE store_ordering SET paused_at = $2, paused_until = $3, pause_note = $4, updated_at = now()
WHERE location_id = $1
RETURNING *;

-- name: ClearStorePause :one
UPDATE store_ordering SET paused_at = NULL, paused_until = NULL, pause_note = '', updated_at = now()
WHERE location_id = $1
RETURNING *;

-- name: ListStoreOrderingForRestock :many
-- Every ordering row with what the restock loop needs to find its reset time.
SELECT o.location_id, o.restocked_at, l.timezone, l.daily_reset_hour
FROM store_ordering o JOIN locations l ON l.id = o.location_id;

-- name: SetStoreRestocked :exec
UPDATE store_ordering SET restocked_at = $2 WHERE location_id = $1;

-- name: RestockItems :execrows
-- The daily restock: every 86'd item of the location without a later
-- out_until comes back.
UPDATE menu_items SET in_stock = true, out_until = NULL, updated_at = now()
WHERE location_id = $1 AND NOT in_stock AND NOT out_hold AND (out_until IS NULL OR out_until <= now());

-- name: RestockOptions :execrows
UPDATE modifier_options SET in_stock = true, updated_at = now()
WHERE location_id = $1 AND NOT in_stock AND NOT out_hold;

-- name: ExpireOutUntil :execrows
-- An item 86'd until a time comes back at that time (any location).
UPDATE menu_items SET in_stock = true, out_until = NULL, updated_at = now()
WHERE NOT in_stock AND out_until IS NOT NULL AND out_until <= now();

-- name: ListSoldOutItems :many
SELECT id, name, out_until FROM menu_items
WHERE location_id = $1 AND NOT in_stock
ORDER BY name;

-- name: ListStoreHoursExceptions :many
SELECT * FROM store_hours_exceptions WHERE location_id = $1 AND day >= $2 ORDER BY day;

-- name: UpsertStoreHoursException :one
INSERT INTO store_hours_exceptions (tenant_id, location_id, day, closed, hours, note)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (location_id, day) DO UPDATE SET closed = EXCLUDED.closed, hours = EXCLUDED.hours, note = EXCLUDED.note
RETURNING *;

-- name: DeleteStoreHoursException :execrows
DELETE FROM store_hours_exceptions WHERE id = $1 AND location_id = $2;

-- ─── Pricing + deals (main 3.0 step 4) ──────────────────────────────────────

-- name: ListPricingSettings :many
SELECT * FROM pricing_settings WHERE location_id = $1 ORDER BY kind;

-- name: UpsertPricingSettings :exec
INSERT INTO pricing_settings (location_id, tenant_id, kind, settings) VALUES ($1, $2, $3, $4)
ON CONFLICT (location_id, kind) DO UPDATE SET settings = EXCLUDED.settings, updated_at = now();

-- name: DeletePricingSettings :exec
DELETE FROM pricing_settings WHERE location_id = $1 AND kind = $2;

-- name: ListDeals :many
SELECT * FROM deals WHERE location_id = $1 ORDER BY position, created_at;

-- name: GetDeal :one
SELECT * FROM deals WHERE id = $1 AND location_id = $2;

-- name: CreateDeal :one
INSERT INTO deals (tenant_id, location_id, kind, name, description, active, channel_kinds, def, code, position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, (SELECT COALESCE(MAX(position) + 1, 0) FROM deals WHERE location_id = $2))
RETURNING *;

-- name: UpdateDeal :one
UPDATE deals SET kind = $3, name = $4, description = $5, active = $6, channel_kinds = $7, def = $8, code = $9, updated_at = now()
WHERE id = $1 AND location_id = $2
RETURNING *;

-- name: DeleteDeal :execrows
DELETE FROM deals WHERE id = $1 AND location_id = $2;

-- ─── Channel orders (main 3.0 step 5) ───────────────────────────────────────

-- name: InsertChannelOrder :exec
INSERT INTO channel_orders (order_id, channel_id, tenant_id, location_id, client_order_id, payment, track_token, operator_id, customer_id)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg(track_token), sqlc.narg(operator_id), sqlc.narg(customer_id));

-- name: GetChannelOrderByClient :one
SELECT * FROM channel_orders WHERE channel_id = $1 AND client_order_id = $2;

-- name: GetChannelOrder :one
SELECT * FROM channel_orders WHERE order_id = $1;

-- name: ListChannelOrders :many
SELECT * FROM channel_orders
WHERE channel_id = $1 AND created_at < $2
ORDER BY created_at DESC
LIMIT $3;

-- name: BuiltinChannel :one
SELECT * FROM channels WHERE location_id = $1 AND builtin AND kind = $2 ORDER BY created_at LIMIT 1;

-- ─── Channel tracking (main 3.0 step 6) ─────────────────────────────────────

-- name: TrackingLink :one
-- An order's channel link, unless its channel is the built-in vendor feed
-- (Brygid's orders are followed on the Pi, not through a channel). The
-- built-in Call center follows its orders like a keyed channel.
SELECT co.order_id, co.channel_id, co.client_order_id, co.track_token
FROM channel_orders co JOIN channels c ON c.id = co.channel_id
WHERE co.order_id = $1 AND NOT (c.builtin AND c.kind = 'vendor');

-- name: InsertChannelEvent :one
INSERT INTO channel_events (channel_id, order_id, type, payload) VALUES ($1, $2, $3, $4) RETURNING id;

-- name: ChannelEventsAfter :many
SELECT * FROM channel_events WHERE channel_id = $1 AND id > $2 ORDER BY id LIMIT $3;

-- name: PruneChannelEvents :execrows
DELETE FROM channel_events WHERE created_at < $1;

-- name: GetChannelOrderByToken :one
SELECT * FROM channel_orders WHERE track_token = $1;

-- name: ChannelOrdersToAlert :many
-- Channel orders (not the vendor feed's) the Pi hasn't accepted N minutes
-- after they were placed (ASAP, or due within the window), not paged yet.
SELECT co.order_id, co.channel_id, co.location_id, c.name AS channel_name, o.customer_display_name
FROM channel_orders co
JOIN channels c ON c.id = co.channel_id
JOIN orders o ON o.id = co.order_id
WHERE NOT (c.builtin AND c.kind = 'vendor') AND co.alerted_at IS NULL
  AND co.created_at < @placed_before::timestamptz AND co.created_at > @placed_after::timestamptz
  AND o.status IN ('received', 'validated')
  AND (o.promised_at IS NULL OR o.promised_at < @due_before::timestamptz);

-- name: MarkChannelOrderAlerted :exec
UPDATE channel_orders SET alerted_at = now() WHERE order_id = $1;

-- name: ChannelEventExists :one
SELECT EXISTS (SELECT 1 FROM channel_events WHERE channel_id = $1 AND id = $2);

-- name: LatestChannelEvent :one
SELECT COALESCE(MAX(id), 0)::bigint FROM channel_events WHERE channel_id = $1;

-- name: RevokeOperatorToken :execrows
UPDATE operator_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL;

-- weborder stage 2: storefront platform keys, hosts, site settings.

-- name: CreatePlatformKey :one
INSERT INTO platform_keys (name, key_hash, key_prefix) VALUES ($1, $2, $3) RETURNING *;

-- name: ListPlatformKeys :many
SELECT * FROM platform_keys ORDER BY created_at;

-- name: RevokePlatformKey :one
UPDATE platform_keys SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1 RETURNING *;

-- name: AuthenticatePlatformKey :one
SELECT * FROM platform_keys WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: TouchPlatformKey :exec
UPDATE platform_keys SET last_used_at = now() WHERE id = $1;

-- name: InsertSiteDomain :one
INSERT INTO site_domains (host, tenant_id, location_id, is_primary, verified)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListSiteDomains :many
SELECT * FROM site_domains WHERE location_id = $1 ORDER BY is_primary DESC, host;

-- name: GetSiteDomain :one
SELECT * FROM site_domains WHERE host = $1;

-- name: DeleteSiteDomain :execrows
DELETE FROM site_domains WHERE host = $1 AND location_id = $2;

-- name: ClearSitePrimary :exec
UPDATE site_domains SET is_primary = false WHERE location_id = $1 AND is_primary;

-- name: SetSitePrimary :execrows
UPDATE site_domains SET is_primary = true WHERE host = $1 AND location_id = $2;

-- name: SetSiteDomainVerified :one
UPDATE site_domains SET verified = $2 WHERE host = $1 RETURNING *;

-- name: GetSiteSettings :one
SELECT * FROM site_settings WHERE location_id = $1;

-- The Website page (00029).

-- name: SaveSiteDraft :exec
INSERT INTO site_settings (location_id, tenant_id, draft, draft_at) VALUES ($1, $2, $3, now())
ON CONFLICT (location_id) DO UPDATE SET draft = EXCLUDED.draft, draft_at = now(), updated_at = now();

-- name: DiscardSiteDraft :exec
UPDATE site_settings SET draft = '{}'::jsonb, draft_at = NULL, updated_at = now() WHERE location_id = $1;

-- name: PublishSiteDraft :one
UPDATE site_settings SET published = draft, draft = '{}'::jsonb, draft_at = NULL, version = version + 1,
    published_at = now(), published_by = $2, updated_at = now()
WHERE location_id = $1 AND draft_at IS NOT NULL
RETURNING version, published_at;

-- name: EnsureSitePreviewToken :one
INSERT INTO site_settings (location_id, tenant_id, preview_token) VALUES ($1, $2, $3)
ON CONFLICT (location_id) DO UPDATE SET preview_token =
    CASE WHEN site_settings.preview_token = '' THEN EXCLUDED.preview_token ELSE site_settings.preview_token END
RETURNING preview_token;

-- name: InsertSiteImage :one
INSERT INTO site_images (location_id, tenant_id, kind, content_type, bytes) VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at;

-- name: GetSiteImage :one
SELECT id, kind, content_type, bytes, created_at FROM site_images WHERE id = $1 AND location_id = $2;

-- name: DeleteUnusedSiteImages :execrows
DELETE FROM site_images WHERE location_id = $1 AND created_at < now() - interval '1 day' AND NOT (id = ANY(@keep::uuid[]));

-- weborder stage 2: Square connections + card holds.

-- name: UpsertSquareConnection :one
INSERT INTO square_connections (location_id, tenant_id, merchant_id, square_location_id, access_token_enc, refresh_token_enc, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (location_id) DO UPDATE SET merchant_id = EXCLUDED.merchant_id, square_location_id = EXCLUDED.square_location_id,
    access_token_enc = EXCLUDED.access_token_enc, refresh_token_enc = EXCLUDED.refresh_token_enc, expires_at = EXCLUDED.expires_at,
    refreshed_at = now(), status = 'connected', last_error = '', connected_at = now()
RETURNING *;

-- name: GetSquareConnection :one
SELECT * FROM square_connections WHERE location_id = $1;

-- name: SquareConnectionsByMerchant :many
SELECT * FROM square_connections WHERE merchant_id = $1;

-- name: SetSquareLocation :execrows
UPDATE square_connections SET square_location_id = $2 WHERE location_id = $1;

-- name: UpdateSquareTokens :exec
UPDATE square_connections SET access_token_enc = $2, refresh_token_enc = $3, expires_at = $4, refreshed_at = now(),
    status = 'connected', last_error = '' WHERE location_id = $1;

-- name: SetSquareConnectionStatus :exec
UPDATE square_connections SET status = $2, last_error = $3 WHERE location_id = $1;

-- name: DeleteSquareConnection :execrows
DELETE FROM square_connections WHERE location_id = $1;

-- name: SquareConnectionsToRefresh :many
SELECT * FROM square_connections WHERE status <> 'revoked' AND refreshed_at < $1 ORDER BY refreshed_at LIMIT 50;

-- name: InsertCardHold :exec
INSERT INTO card_holds (order_id, tenant_id, location_id, gateway, payment_ref, amount_cents, tip_cents, due_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetCardHold :one
SELECT * FROM card_holds WHERE order_id = $1;

-- name: ListCardHolds :many
SELECT * FROM card_holds WHERE location_id = $1 ORDER BY created_at DESC LIMIT $2;

-- name: ListCardHoldsWithOrders :many
-- The Payments page: a store's holds with who ordered, newest first (?status=).
SELECT h.*, o.customer_display_name, o.fulfillment_type, o.placed_at, o.status AS order_status
FROM card_holds h JOIN orders o ON o.id = h.order_id
WHERE h.location_id = $1 AND (sqlc.narg(status)::text IS NULL OR h.status = sqlc.narg(status))
ORDER BY h.created_at DESC LIMIT $2;

-- name: ClaimCardHolds :many
-- Holds to capture now (asked for, or past their safety time), leased for
-- two minutes so a slow capture isn't picked up twice.
UPDATE card_holds SET next_try_at = now() + interval '2 minutes', updated_at = now()
WHERE order_id IN (
    SELECT h.order_id FROM card_holds h
    WHERE h.status = 'held' AND h.next_try_at <= now() AND (h.due_at <= now() OR h.capture_requested_at IS NOT NULL)
      -- M-3: never capture a hold whose order was cancelled / rejected or
      -- whose void is owed (the cancel worker releases those).
      AND h.cancel_requested_at IS NULL
      AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.id = h.order_id AND o.status IN ('cancelled', 'rejected'))
    ORDER BY h.next_try_at LIMIT $1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: RequestCardHoldCapture :execrows
UPDATE card_holds SET capture_requested_at = COALESCE(capture_requested_at, now()),
    capture_reason = CASE WHEN capture_reason = '' THEN $3 ELSE capture_reason END, next_try_at = LEAST(next_try_at, now()), updated_at = now()
WHERE location_id = $1 AND order_id = ANY($2::uuid[]) AND status = 'held' AND cancel_requested_at IS NULL;

-- name: RequestCardHoldCaptureForBatch :execrows
UPDATE card_holds h SET capture_requested_at = COALESCE(h.capture_requested_at, now()),
    capture_reason = CASE WHEN h.capture_reason = '' THEN 'batch_close' ELSE h.capture_reason END,
    next_try_at = LEAST(h.next_try_at, now()), updated_at = now()
FROM orders o
WHERE o.id = h.order_id AND h.location_id = $1 AND h.status = 'held' AND h.cancel_requested_at IS NULL AND o.agent_batch_id = $2;

-- name: RequestCardHoldCaptureForDriver :execrows
-- A driver clocked out: their deliveries sent between clock-in and clock-out.
UPDATE card_holds h SET capture_requested_at = COALESCE(h.capture_requested_at, now()),
    capture_reason = CASE WHEN h.capture_reason = '' THEN 'clock_out' ELSE h.capture_reason END,
    next_try_at = LEAST(h.next_try_at, now()), updated_at = now()
WHERE h.location_id = $1 AND h.status = 'held' AND h.cancel_requested_at IS NULL AND h.order_id::text IN (
    SELECT jsonb_array_elements_text(r.sent_ids) FROM delivery_runs r
    WHERE r.location_id = $1 AND r.driver_id = $2 AND r.at >= $3 AND r.at <= $4);

-- name: SetCardHoldAddedTip :execrows
UPDATE card_holds SET added_tip_cents = $3, updated_at = now()
WHERE location_id = $1 AND order_id = $2 AND status = 'held' AND cancel_requested_at IS NULL;

-- name: CardHoldCaptured :execrows
-- M-14: only the hold the worker read (still held, the added tip it saw): a
-- tip set meanwhile makes this 0 rows and the capture is looked at again.
UPDATE card_holds SET status = 'captured', captured_cents = $2, tip_payment_ref = $3, tip_note = $4, captured_at = now(),
    last_error = '', updated_at = now() WHERE order_id = $1 AND status = 'held' AND added_tip_cents = $5;

-- name: CardHoldCancelled :exec
UPDATE card_holds SET status = 'cancelled', cancelled_at = now(), last_error = $2, updated_at = now() WHERE order_id = $1;

-- name: CardHoldRetry :exec
UPDATE card_holds SET attempts = attempts + 1, next_try_at = $2, last_error = $3, updated_at = now() WHERE order_id = $1;

-- name: CardHoldFailed :exec
UPDATE card_holds SET status = 'failed', attempts = attempts + 1, last_error = $2, updated_at = now() WHERE order_id = $1;

-- name: SetOrderCardPayment :exec
-- The hold's own payment row follows it (authorized → captured / voided);
-- M-4: by the payment's id, never every card row of the order.
UPDATE payments SET status = $2, amount_cents = $3, tip_cents = $4, captured_at = $5 WHERE id = $1;

-- name: LinkCardHoldPayment :exec
-- The hold's own payment row (M-4), written in the order's transaction.
UPDATE card_holds h SET payment_id = (
    SELECT p.id FROM payments p WHERE p.order_id = h.order_id AND p.processor = 'card' ORDER BY p.id LIMIT 1)
WHERE h.order_id = $1;

-- name: GetCardHoldByRef :one
SELECT * FROM card_holds WHERE location_id = $1 AND payment_ref = $2;

-- weborder stage 2: customer accounts + email receipts.

-- name: UpsertCustomerByPhone :one
-- Website sign-in: a name typed there fills an empty one (with its first +
-- last split, name_source web); an existing name is kept.
INSERT INTO customers (tenant_id, location_id, phone, name, first_name, last_name, name_source, source)
VALUES (sqlc.arg(tenant_id), sqlc.arg(location_id), sqlc.arg(phone), sqlc.arg(name), sqlc.arg(first_name), sqlc.arg(last_name),
        CASE WHEN sqlc.arg(name)::text = '' THEN '' ELSE 'web' END, 'web')
ON CONFLICT (location_id, phone) DO UPDATE SET last_seen_at = now(),
    name        = CASE WHEN customers.name = '' THEN EXCLUDED.name ELSE customers.name END,
    first_name  = CASE WHEN customers.name = '' THEN EXCLUDED.first_name ELSE customers.first_name END,
    last_name   = CASE WHEN customers.name = '' THEN EXCLUDED.last_name ELSE customers.last_name END,
    name_source = CASE WHEN customers.name = '' AND EXCLUDED.name <> '' THEN 'web' ELSE customers.name_source END
RETURNING *, (xmax = 0) AS created;

-- name: GetCustomer :one
SELECT * FROM customers WHERE id = $1;

-- name: UpdateCustomerProfile :one
-- The website's profile: a name it sends is the customer's own (name_source web).
-- When it didn't change the name, first + last stay as they are.
UPDATE customers SET name = sqlc.arg(name), email = sqlc.arg(email),
    first_name  = CASE WHEN sqlc.arg(name_set)::bool THEN sqlc.arg(first_name) ELSE first_name END,
    last_name   = CASE WHEN sqlc.arg(name_set)::bool THEN sqlc.arg(last_name) ELSE last_name END,
    name_source = CASE WHEN sqlc.arg(name_set)::bool THEN 'web' ELSE name_source END, updated_at = now()
WHERE id = sqlc.arg(id) RETURNING *;

-- name: SetCustomerSquareID :exec
UPDATE customers SET square_customer_id = $2, updated_at = now() WHERE id = $1;

-- name: DeleteCustomer :execrows
DELETE FROM customers WHERE id = $1;

-- name: InsertCustomerSession :exec
INSERT INTO customer_sessions (token_hash, customer_id, location_id, expires_at) VALUES ($1, $2, $3, $4);

-- name: CustomerBySession :one
-- A live session's customer; the session is renewed (90 days from now) as it is read.
UPDATE customer_sessions s SET last_used_at = now(), expires_at = GREATEST(s.expires_at, $3::timestamptz)
FROM customers c
WHERE s.token_hash = $1 AND s.location_id = $2 AND s.expires_at > now() AND c.id = s.customer_id
RETURNING c.*;

-- name: DeleteCustomerSession :exec
DELETE FROM customer_sessions WHERE token_hash = $1;

-- name: PruneCustomerSessions :execrows
DELETE FROM customer_sessions WHERE expires_at < now();

-- name: ListCustomerAddresses :many
SELECT * FROM customer_addresses WHERE customer_id = $1 ORDER BY is_default DESC, created_at;

-- name: InsertCustomerAddress :one
INSERT INTO customer_addresses (customer_id, label, line1, line2, city, state, zip, notes, is_default)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *;

-- name: UpdateCustomerAddress :one
UPDATE customer_addresses SET label = $3, line1 = $4, line2 = $5, city = $6, state = $7, zip = $8, notes = $9, is_default = $10
WHERE id = $1 AND customer_id = $2 RETURNING *;

-- name: ClearCustomerDefaultAddress :exec
UPDATE customer_addresses SET is_default = false WHERE customer_id = $1 AND is_default;

-- name: DeleteCustomerAddress :execrows
DELETE FROM customer_addresses WHERE id = $1 AND customer_id = $2;

-- name: CountCustomerAddresses :one
SELECT count(*) FROM customer_addresses WHERE customer_id = $1;

-- name: ListCustomerCards :many
SELECT * FROM customer_cards WHERE customer_id = $1 ORDER BY created_at DESC;

-- name: InsertCustomerCard :one
INSERT INTO customer_cards (customer_id, gateway, card_ref, brand, last4, exp_month, exp_year)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (customer_id, card_ref) DO UPDATE SET brand = EXCLUDED.brand
RETURNING *;

-- name: GetCustomerCard :one
SELECT * FROM customer_cards WHERE id = $1 AND customer_id = $2;

-- name: DeleteCustomerCard :execrows
DELETE FROM customer_cards WHERE id = $1 AND customer_id = $2;

-- name: JoinGuestOrders :execrows
-- A verified phone takes over that number's guest web orders at the store.
UPDATE channel_orders co SET customer_id = $3
FROM orders o, channels ch
WHERE co.order_id = o.id AND ch.id = co.channel_id AND co.location_id = $1 AND co.customer_id IS NULL
  AND ch.kind = 'web' AND o.customer_phone = $2;

-- name: ListCustomerOrders :many
SELECT * FROM channel_orders WHERE customer_id = $1 AND created_at < $2 ORDER BY created_at DESC LIMIT $3;

-- name: GetChannelOrderForCustomer :one
SELECT * FROM channel_orders WHERE order_id = $1 AND customer_id = $2;

-- Terminal stage 0c: the bulk import + the Pi's change feed.

-- name: ListCustomersByPhones :many
SELECT * FROM customers WHERE location_id = $1 AND phone = ANY(sqlc.arg(phones)::text[]);

-- name: ListAddressesOfCustomers :many
SELECT * FROM customer_addresses WHERE customer_id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY customer_id, is_default DESC, created_at;

-- name: InsertImportedCustomer :one
-- No row (pgx.ErrNoRows) = the phone is taken: another writer (the Pi's
-- customer.upsert, a website sign-in) created it after the import read.
INSERT INTO customers (tenant_id, location_id, phone, name, first_name, last_name, notes, source, name_source, notes_source)
VALUES (sqlc.arg(tenant_id), sqlc.arg(location_id), sqlc.arg(phone), sqlc.arg(name), sqlc.arg(first_name), sqlc.arg(last_name),
        sqlc.arg(notes), 'import',
        CASE WHEN sqlc.arg(name)::text = '' THEN '' ELSE 'import' END,
        CASE WHEN sqlc.arg(notes)::text = '' THEN '' ELSE 'import' END)
ON CONFLICT (location_id, phone) DO NOTHING
RETURNING *;

-- name: UpdateImportedCustomer :exec
UPDATE customers SET name = $2, first_name = $3, last_name = $4, name_source = $5, notes = $6, notes_source = $7
WHERE id = $1;

-- name: FillCustomerAddress :exec
-- The import fills an existing address's blank fields (never overwrites).
UPDATE customer_addresses SET line2 = $2, city = $3, state = $4, zip = $5, notes = $6, label = $7 WHERE id = $1;

-- name: CustomerChanges :many
-- The Pi's change feed: customers changed and customers deleted at the store
-- after the cursor (ts, id), oldest first.
SELECT * FROM (
    SELECT c.id, c.updated_at AS at, false AS deleted FROM customers c
    WHERE c.location_id = sqlc.arg(location_id) AND (c.updated_at, c.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
    UNION ALL
    SELECT t.customer_id, t.deleted_at, true FROM customer_tombstones t
    WHERE t.location_id = sqlc.arg(location_id) AND (t.deleted_at, t.customer_id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
) ch ORDER BY at, id LIMIT sqlc.arg(max_rows);

-- name: ListCustomersByIDs :many
SELECT * FROM customers WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: PruneCustomerTombstones :execrows
DELETE FROM customer_tombstones WHERE deleted_at < $1;

-- name: GetSiteReplyEmail :one
SELECT reply_email FROM site_settings WHERE location_id = $1;

-- name: SetSiteReplyEmail :exec
INSERT INTO site_settings (location_id, tenant_id, reply_email) VALUES ($1, $2, $3)
ON CONFLICT (location_id) DO UPDATE SET reply_email = EXCLUDED.reply_email, updated_at = now();

-- name: InsertEmailReceipt :exec
INSERT INTO email_receipts (order_id, kind, location_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: ClaimEmailReceipts :many
UPDATE email_receipts SET next_try_at = now() + interval '2 minutes'
WHERE (order_id, kind) IN (
    SELECT r.order_id, r.kind FROM email_receipts r WHERE r.status = 'pending' AND r.next_try_at <= now()
    ORDER BY r.next_try_at LIMIT $1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: EmailReceiptDone :exec
UPDATE email_receipts SET status = $3, message_id = $4, last_error = $5, attempts = attempts + 1,
    sent_at = CASE WHEN $3 = 'sent' THEN now() ELSE sent_at END
WHERE order_id = $1 AND kind = $2;

-- name: EmailReceiptRetry :exec
UPDATE email_receipts SET attempts = attempts + 1, next_try_at = $3, last_error = $4 WHERE order_id = $1 AND kind = $2;

-- name: ListEmailReceipts :many
SELECT * FROM email_receipts WHERE order_id = $1 ORDER BY created_at;

-- name: QueueFinalReceipt :execrows
-- A final receipt only for an order that got a receipt when it was placed.
INSERT INTO email_receipts (order_id, kind, location_id)
SELECT r.order_id, 'final', r.location_id FROM email_receipts r WHERE r.order_id = $1 AND r.kind = 'placed'
ON CONFLICT DO NOTHING;


-- weborder stage 3: sold out from Quibase (through the Pi).

-- name: ListStockItems :many
SELECT i.id, i.name, c.name AS category, i.in_stock, i.out_until, i.out_hold
FROM menu_items i JOIN menu_categories c ON c.id = i.category_id
WHERE i.location_id = $1 AND NOT i.quick_add ORDER BY c.position, c.name, i.position, i.name;

-- name: ListStockOptions :many
SELECT g.id AS group_id, g.name AS group_name, o.id, o.name, o.in_stock, o.out_hold
FROM modifier_groups g JOIN modifier_options o ON o.group_id = g.id
WHERE g.location_id = $1 ORDER BY g.name, o.position, o.name;

-- name: SetItemStock :one
UPDATE menu_items SET in_stock = $3, out_hold = (NOT $3::boolean AND $4::boolean), out_until = NULL, updated_at = now()
WHERE id = $1 AND location_id = $2
RETURNING id, name, in_stock, out_until, out_hold;

-- name: SetOptionStock :one
UPDATE modifier_options SET in_stock = $3, out_hold = (NOT $3::boolean AND $4::boolean), updated_at = now()
WHERE id = $1 AND location_id = $2
RETURNING id, name, in_stock, out_hold;

-- ───────────────────────────────────────────────────────────────────────────
-- Order placements (DURABILITY-PLAN.md: a channel order is a placement first)
-- ───────────────────────────────────────────────────────────────────────────

-- name: InsertPlacement :one
-- A new placement, or nothing when the key exists (then GetPlacement).
INSERT INTO order_placements (channel_id, client_order_id, tenant_id, location_id, body_hash, request,
    card_token, verification_token, customer_id, operator, expires_at, state, result, worker)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (channel_id, client_order_id) DO NOTHING
RETURNING *;

-- name: GetPlacement :one
SELECT * FROM order_placements WHERE channel_id = $1 AND client_order_id = $2;

-- name: ClaimPlacement :execrows
-- Take over a pending placement nobody works on (or a stale worker's), as long
-- as nobody else took it first (worker + attempts unchanged). New tokens (a
-- retry after 3-D Secure) replace the stored ones.
UPDATE order_placements
SET worker = @worker, attempts = attempts + 1, waiting = '',
    card_token = CASE WHEN @card_token::text <> '' THEN @card_token::text ELSE card_token END,
    verification_token = CASE WHEN @verification_token::text <> '' THEN @verification_token::text ELSE verification_token END,
    updated_at = now()
WHERE channel_id = @channel_id AND client_order_id = @client_order_id AND state = 'pending'
  AND worker = @old_worker AND attempts = @old_attempts;

-- name: StartPlacementCharge :exec
UPDATE order_placements SET charge_started = true, charge_cents = $3, updated_at = now()
WHERE channel_id = $1 AND client_order_id = $2;

-- name: FinishPlacement :execrows
-- A final state other than placed (refused / expired / voided): the tokens go.
UPDATE order_placements
SET state = $3, result = $4, worker = '', waiting = '', card_token = '', verification_token = '', updated_at = now()
WHERE channel_id = $1 AND client_order_id = $2 AND state = 'pending';

-- name: ParkPlacement :exec
-- Still pending, but nobody works on it until the caller comes back (3-D
-- Secure, or a temporary failure the caller may retry).
UPDATE order_placements SET waiting = $3, result = $4, worker = '', updated_at = now()
WHERE channel_id = $1 AND client_order_id = $2 AND state = 'pending';

-- name: MarkPlacementPlaced :execrows
-- In the order's own transaction: the key and its result together.
UPDATE order_placements
SET state = 'placed', order_id = $3, due_alert_at = $4, worker = '', waiting = '', result = NULL,
    card_token = '', verification_token = '', updated_at = now()
WHERE channel_id = $1 AND client_order_id = $2 AND state = 'pending';

-- name: ListStalePlacements :many
-- Pending placements whose worker is another main (restarted) or silent too long.
SELECT * FROM order_placements
WHERE state = 'pending' AND waiting = '' AND (worker <> @worker OR updated_at < @before)
ORDER BY updated_at
LIMIT 20;

-- name: ListParkedExpired :many
-- Parked placements nobody came back for (their deadline, else 30 minutes).
SELECT * FROM order_placements
WHERE state = 'pending' AND waiting <> '' AND COALESCE(expires_at, created_at + interval '30 minutes') < now()
LIMIT 50;

-- name: ListPlacementDueAlerts :many
SELECT * FROM order_placements
WHERE state = 'placed' AND NOT due_alerted AND due_alert_at <= now()
LIMIT 50;

-- name: SetPlacementDueAlerted :exec
UPDATE order_placements SET due_alerted = true WHERE channel_id = $1 AND client_order_id = $2;

-- name: PrunePlacements :execrows
DELETE FROM order_placements WHERE state <> 'pending' AND created_at < $1;

-- name: CountActiveAgents :one
-- The store has a Pi (an agent key not revoked): only then is "kitchen offline" checked.
SELECT count(*)::bigint FROM devices WHERE location_id = $1 AND kind = 'agent' AND revoked_at IS NULL;
