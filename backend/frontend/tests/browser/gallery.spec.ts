// The page gallery: every page of main's app and its key states, captured on every gate run (the
// owner's rule, 2026-11-13: "did we never screenshot-verify everything?"). Each shot opens a page
// as a store manager (or as admin), waits for it to load, checks it rendered without an uncaught
// error, and saves a PNG of the whole page to test-results/gallery/<project>/<slug>.png (the gate
// copies test-results into .dev/gate/<stamp>/artifacts/main-browser/, green runs too).
//
// Two modes:
//   * the gate (default): a throwaway store of its own per test, with a small menu, a staff member
//     and a few orders the test plays the Pi for (counter, call-center delivery, voided);
//   * GALLERY_DIR=<dir> (+ GALLERY_STORE_USER / GALLERY_STORE_PASS, default dev Lombardi's
//     wire creds): read-only captures of a real dev store into <dir>/<project>/ for the owner's
//     review (no writes: the shots that change something are skipped).
import { test as base, expect, MAIN, adminKey, TestStore } from './fixtures';
import type { Page } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { join } from 'node:path';

const GALLERY = process.env.GALLERY_DIR ?? '';
const OUT = GALLERY || new URL('../../test-results/gallery', import.meta.url).pathname;

/** One capture: where it goes, what it shows, how to get there. */
type Shot = {
  slug: string;
  page: string;
  state: string;
  path: string;
  /** Admin shots sign in with the admin key instead of a store session. */
  admin?: boolean;
  /** Signed out (the login page). */
  anon?: boolean;
  /** Steps after the page loaded (open a tab, a panel…). Read-only. */
  act?: (page: Page, ctx: Ctx) => Promise<void>;
  /** The gate only (it changes something, or needs the seeded orders' ids). */
  gateOnly?: boolean;
  /** The owner's review only (needs data a throwaway store hasn't got, e.g. a day close). */
  galleryOnly?: boolean;
  /** Viewport-only (a panel over the page: the whole-page resize would stretch it). */
  viewport?: boolean;
};

type Ctx = { orders: Record<string, string>; tenantId: string };

const tab = (name: string | RegExp) => async (page: Page) => {
  await page.getByRole('tab', { name }).click();
};

/** Opens the first order row of the Orders table (the detail panel). */
const firstOrder = async (page: Page) => {
  await page.locator('table.tbl tbody tr').first().click();
  await expect(page.getByRole('dialog')).toBeVisible();
};

const SHOTS: Shot[] = [
  { slug: 'login', page: 'Login', state: 'signed out', path: '/login', anon: true },
  { slug: 'overview', page: 'Overview', state: 'default', path: '/store' },
  { slug: 'orders', page: 'Orders', state: 'all orders', path: '/store/orders' },
  {
    slug: 'orders-search', page: 'Orders', state: 'search', path: '/store/orders',
    act: async (page) => { await page.locator('#order-search').fill('Lopez'); }
  },
  { slug: 'orders-detail', page: 'Orders', state: 'order detail panel', path: '/store/orders', act: firstOrder, viewport: true },
  {
    slug: 'orders-detail-counter', page: 'Orders', state: 'detail: a paid counter order', path: '/store/orders', gateOnly: true, viewport: true,
    act: async (page, ctx) => { await page.goto(`/app/store/orders?order=${ctx.orders.counter}`); await expect(page.getByRole('dialog')).toBeVisible(); }
  },
  {
    slug: 'orders-detail-delivery', page: 'Orders', state: 'detail: a delivered call-center order', path: '/store/orders', gateOnly: true, viewport: true,
    act: async (page, ctx) => { await page.goto(`/app/store/orders?order=${ctx.orders.delivery}`); await expect(page.getByRole('dialog')).toBeVisible(); }
  },
  {
    slug: 'orders-detail-void', page: 'Orders', state: 'detail: a voided order', path: '/store/orders', gateOnly: true, viewport: true,
    act: async (page, ctx) => { await page.goto(`/app/store/orders?order=${ctx.orders.voided}`); await expect(page.getByRole('dialog')).toBeVisible(); }
  },
  { slug: 'reports', page: 'Reports', state: 'default', path: '/store/reports' },
  {
    slug: 'reports-z', page: 'Reports', state: 'a day close opened', path: '/store/reports', galleryOnly: true, viewport: true,
    act: async (page) => { await page.getByRole('button', { name: 'Open' }).first().click(); await expect(page.getByTestId('z-detail')).toBeVisible(); }
  },
  { slug: 'menu-items', page: 'Menu', state: 'Items', path: '/store/menu' },
  {
    slug: 'menu-item-panel', page: 'Menu', state: 'item panel', path: '/store/menu', viewport: true,
    act: async (page) => { await page.locator('.irow.item').first().click(); await expect(page.getByRole('dialog')).toBeVisible(); }
  },
  {
    slug: 'menu-bulk', page: 'Menu', state: 'two items selected (bulk bar)', path: '/store/menu',
    act: async (page) => {
      const boxes = page.locator('.irow.item input.check');
      await boxes.nth(0).check();
      await boxes.nth(1).check();
    }
  },
  { slug: 'menu-modifiers', page: 'Menu', state: 'Modifier groups', path: '/store/menu', act: tab('Modifier groups') },
  { slug: 'menu-categories', page: 'Menu', state: 'Categories', path: '/store/menu', act: tab('Categories') },
  { slug: 'menu-sizes', page: 'Menu', state: 'Sizes', path: '/store/menu', act: tab('Sizes') },
  { slug: 'menu-families', page: 'Menu', state: 'Families', path: '/store/menu', act: tab('Families') },
  { slug: 'menu-views', page: 'Menu', state: 'Views', path: '/store/menu', act: tab('Views') },
  { slug: 'menu-deals', page: 'Menu', state: 'Deals', path: '/store/menu', act: tab('Deals') },
  { slug: 'menu-problems', page: 'Menu', state: 'Find problems', path: '/store/menu', act: tab(/Find problems/) },
  { slug: 'printers', page: 'Printers', state: 'default', path: '/store/printers' },
  { slug: 'devices', page: 'Devices', state: 'default', path: '/store/devices' },
  { slug: 'staff', page: 'Staff', state: 'default', path: '/store/staff' },
  { slug: 'website', page: 'Website', state: 'default', path: '/store/website' },
  { slug: 'payments', page: 'Payments', state: 'default', path: '/store/payments' },
  { slug: 'settings', page: 'Settings', state: 'all tiles', path: '/store/settings' },
  ...(
    [
      ['details', 'Store details'], ['hours', 'Hours'], ['map', 'Map and delivery area'], ['alerts', 'Alerts'],
      ['ordering', 'Ordering'], ['tax', 'Tax and deals'], ['fees', 'Fees and tips'], ['cashier', 'Cashier limits'],
      ['counter', 'Counter orders'], ['screen', 'Login and screen'], ['card', 'Card payments'],
      ['voids', 'Void and waste reasons'], ['end-of-day', 'End of day'], ['callcenter', 'Call center']
    ] as const
  ).map(([s, name]) => ({ slug: `settings-${s}`, page: 'Settings', state: name, path: `/store/settings/${s}` })),
  { slug: 'admin-stores', page: 'Admin', state: 'stores', path: '/admin', admin: true },
  { slug: 'admin-tenants', page: 'Admin', state: 'tenants list', path: '/admin/tenants', admin: true },
  { slug: 'admin-tenant-new', page: 'Admin', state: 'new store', path: '/admin/tenants/new', admin: true },
  ...(['overview', 'login', 'location', 'pi', 'diag', 'danger'] as const).map((t) => ({
    slug: `admin-tenant-${t}`, page: 'Admin', state: `store: ${t}`, path: `/admin/tenants/@TENANT@?tab=${t}`, admin: true
  })),
  { slug: 'admin-operators', page: 'Admin', state: 'operators', path: '/admin/operators', admin: true },
  { slug: 'admin-channels', page: 'Admin', state: 'channels', path: '/admin/channels', admin: true },
  { slug: 'admin-diagnostics', page: 'Admin', state: 'diagnostics', path: '/admin/diagnostics', admin: true }
];

// ---------------------------------------------------------------------------------------------------
// The store the shots look at.

/** Plays the Pi: floor events over the duplex channel (contract/main.md §2.5zb). */
async function floorEvents(key: string, events: unknown[]) {
  const r = await fetch(`${MAIN}/store/v1/agent/rpc`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'X-STORE-API-KEY': key },
    body: JSON.stringify({ type: 'floor.events', id: randomUUID(), payload: { events } })
  });
  const text = await r.text();
  if (r.status !== 200) throw new Error(`floor.events: ${r.status} ${text.slice(0, 300)}`);
  const results = (JSON.parse(text).payload?.results ?? []) as { event_id: string; result: string }[];
  const bad = results.filter((x) => x.result !== 'applied');
  if (bad.length) throw new Error(`floor.events not applied: ${JSON.stringify(bad)}`);
}

/** A few orders with everything the detail panel shows. */
async function seedOrders(s: TestStore): Promise<Record<string, string>> {
  const dev = (await s.adminCall('POST', '/devices', { location_id: s.locationId, name: 'gallery-pi' }, 201)).data;
  const key = dev.api_key as string;
  const staff = (await s.me('POST', '/staff', { first_name: 'Dana', last_name: 'Cashier', role: 'cashier', pin: '4821' }, 201)).data;
  const staffId = staff?.id ?? staff?.staff?.id ?? randomUUID();
  const now = Date.now();
  const at = (minAgo: number) => new Date(now - minAgo * 60_000).toISOString();
  const printers = (await s.me('GET', '/printers', undefined, 200)).data as { id: string }[];
  const line = (name: string, cents: number, extra: Record<string, unknown> = {}) => ({
    id: randomUUID(), position: 0, name, quantity: 1, unit_price_cents: cents, line_total_cents: cents, routing: 'matched',
    printer_id: printers[0]?.id, ...extra
  });
  const placed = (snap: Record<string, any>, batch = 1) => ({
    event_id: randomUUID(), type: 'order.placed', order_id: snap.id, at: snap.placed_at, batch_id: batch,
    agent_status: { state: snap.status, kitchen_number: snap.kitchen_order_number }, snapshot: snap
  });
  const status = (id: string, state: string, minAgo: number, extra: Record<string, unknown> = {}) => ({
    event_id: randomUUID(), type: 'order.status', order_id: id, status: state, at: at(minAgo), batch_id: 1,
    agent_status: { state, kitchen_number: 4, accepted_at: at(35), printed_date: at(35), prep_minutes: 30, ...stamps[state], ...extra }, ...extra
  });
  const stamps: Record<string, Record<string, string>> = {
    ready: { ready_at: at(20) },
    on_delivery: { ready_at: at(20), out_at: at(15) },
    delivered: { ready_at: at(20), out_at: at(15), delivered_at: at(5) }
  };

  const counter = randomUUID();
  const delivery = randomUUID();
  const voided = randomUUID();
  await floorEvents(key, [
    placed({
      id: counter, source: 'counter', source_event_id: `ck-${counter.slice(0, 8)}`, status: 'accepted', currency: 'USD',
      money: { subtotal_cents: 2598, tax_cents: 156, tip_cents: 0, fees: [], discounts: [{ type: 'manual', label: 'Manager 10%', amount_cents: 260 }], total_cents: 2494 },
      customer: { display_name: 'Sam Lee', phone: '4105550100' },
      fulfillment: { type: 'counter' },
      items: [
        line('Cheese Pizza', 1199, {
          size: '16" X-Large',
          children: [
            { id: randomUUID(), position: 0, name: 'Pepperoni', quantity: 1, unit_price_cents: 150, line_total_cents: 150, portion: 'first', mode: 'add' },
            { id: randomUUID(), position: 1, name: 'Mushrooms', quantity: 1, unit_price_cents: 150, line_total_cents: 150, portion: 'second', mode: 'add' }
          ],
          notes: 'Well done'
        }),
        line('Wings', 1099, { position: 1 }),
        line('Fries', 0, { position: 2, routing: 'none', printer_id: undefined, children: [{ id: randomUUID(), position: 0, name: 'No salt', quantity: 1, unit_price_cents: 0, line_total_cents: 0, mode: 'no' }] })
      ],
      payments: [{ id: randomUUID(), method: 'card', amount_cents: 2494, tip_cents: 400, last4: '4242', auth_code: 'A1B2C3', brand: 'visa', at: at(40), staff_id: staffId }],
      notes: '', placed_at: at(42), kitchen_order_number: 3, staff_id: staffId, device_id: 'terminal-1', device_name: 'Counter 1'
    }),
    placed({
      id: delivery, source: 'counter', source_event_id: `cc-${delivery.slice(0, 8)}`, status: 'accepted', currency: 'USD',
      money: { subtotal_cents: 2398, tax_cents: 144, tip_cents: 0, fees: [{ type: 'delivery', label: 'Delivery fee', amount_cents: 300, driver_cents: 200 }], discounts: [], total_cents: 2842 },
      customer: { display_name: 'PAT JONES', phone: '4105550111' },
      fulfillment: { type: 'delivery', address: { line1: '931 Taylor Ave', city: 'Towson', state: 'MD', postal_code: '21286', notes: 'Side door' } },
      items: [line('Cheese Pizza', 1199), line('Wings', 1199, { position: 1 })],
      payments: [{ id: randomUUID(), method: 'manual_card', amount_cents: 2842, tip_cents: 500, last4: '1111', at: at(35) }],
      notes: 'Call when outside', placed_at: at(36), kitchen_order_number: 4, staff_id: staffId, device_id: 'callcenter',
      device_kind: 'callcenter', taken_by: 'Call center - Cara S.'
    }),
    placed({
      id: voided, source: 'counter', source_event_id: `ck-${voided.slice(0, 8)}`, status: 'accepted', currency: 'USD',
      money: { subtotal_cents: 399, tax_cents: 24, tip_cents: 0, fees: [], discounts: [], total_cents: 423 },
      customer: {}, fulfillment: { type: 'pickup' },
      items: [line('Fries', 399)], payments: [], notes: '', placed_at: at(20), kitchen_order_number: 5, staff_id: staffId, device_id: 'terminal-1'
    })
  ]);
  await floorEvents(key, [
    status(delivery, 'ready', 20),
    status(delivery, 'on_delivery', 15, { driver_name: 'Mike' }),
    status(delivery, 'delivered', 5, { driver_name: 'Mike' }),
    { event_id: randomUUID(), type: 'order.void', order_id: voided, at: at(18), batch_id: 1, reason: 'Customer left', staff_id: staffId },
    // What the Pi printed (agent 2.5.5): the panel's print results.
    { event_id: randomUUID(), type: 'order.printed', order_id: counter, mode: 'kitchen', at: at(41), tickets: [
      { kind: 'station', printer: 'Kitchen', result: 'printed', printed_on: 'Kitchen', attempts: 1, at: at(41) },
      { kind: 'receipt', printer: 'Front', result: 'rerouted', printed_on: 'Kitchen', by: 'auto', attempts: 2, at: at(41) }
    ] }
  ]);
  return { counter, delivery, voided };
}

// Read-only mode: sign in to an existing dev store (Lombardi's by default) instead of a throwaway one.
async function galleryStore(page: Page): Promise<Ctx> {
  const user = process.env.GALLERY_STORE_USER ?? 'lombardispizza';
  const pass = process.env.GALLERY_STORE_PASS ?? 'khawar';
  const r = await page.request.post('/store/v1/session', { data: { username: user, password: pass } });
  expect(r.status(), 'gallery store login').toBe(200);
  const who = await (await page.request.get('/store/v1/session')).json();
  return { orders: {}, tenantId: who?.tenant?.id ?? '' };
}

/** Fonts in, nothing loading. */
async function settle(page: Page) {
  await page.evaluate(() => document.fonts.ready);
  await expect(page.locator('[aria-busy=true]')).toHaveCount(0, { timeout: 15_000 });
  await expect(page.locator('.toaster [role]')).toHaveCount(0, { timeout: 10_000 });
}

/** The whole page in one frame (the viewport grows to the page's height, capped), or the viewport. */
async function capture(page: Page, shot: Shot): Promise<Buffer> {
  if (shot.viewport) {
    // A side panel: grow the window to the panel's content, so the shot shows all of it.
    const vp = page.viewportSize()!;
    const extra = await page.evaluate(() => {
      const body = document.querySelector('dialog[open] .overflow-y-auto');
      return body ? body.scrollHeight - body.clientHeight : 0;
    });
    if (extra > 0) await page.setViewportSize({ width: vp.width, height: Math.min(vp.height + extra, 4000) });
  } else {
    const vp = page.viewportSize()!;
    const h = await page.evaluate(() => document.documentElement.scrollHeight);
    await page.setViewportSize({ width: vp.width, height: Math.min(Math.max(h, vp.height), 6000) });
  }
  return page.screenshot({ animations: 'disabled', caret: 'hide' });
}

function save(project: string, shot: Shot, png: Buffer) {
  const dir = join(OUT, project);
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, `${shot.slug}.png`), png);
  writeFileSync(join(dir, `${shot.slug}.json`), JSON.stringify({ slug: shot.slug, page: shot.page, state: shot.state, path: shot.path }));
}

/** On a phone only one shot per page (a sanity check); the desktop gets every state at 1440 wide. */
const PHONE = new Set(['login', 'overview', 'orders', 'orders-detail', 'reports', 'menu-items', 'menu-item-panel', 'printers', 'devices', 'staff', 'website', 'payments', 'settings', 'settings-details', 'admin-stores', 'admin-tenant-overview']);

async function run(page: Page, shot: Shot, ctx: Ctx, project: string) {
  if (project === 'desktop') await page.setViewportSize({ width: 1440, height: 900 });
  if (shot.admin) {
    const key = adminKey();
    await page.addInitScript((k) => {
      try {
        localStorage.setItem('quicc_admin_key', k);
      } catch {
        /* about:blank has no storage */
      }
    }, key);
  }
  const steps = async () => {
    await page.goto('/app' + shot.path.replace('@TENANT@', ctx.tenantId));
    await expect(page.locator('main, form').first()).toBeVisible();
    await settle(page);
    if (shot.act) {
      await shot.act(page, ctx);
      await settle(page);
    }
  };
  if (!GALLERY) {
    await steps();
    save(project, shot, await capture(page, shot));
    return;
  }
  // The owner's review: a state the page can't reach (yet) is captured as it is and noted.
  let missing = '';
  await steps().catch((e) => (missing = String(e).split('\n')[0]));
  save(project, shot, await capture(page, shot));
  if (missing) writeFileSync(join(OUT, project, `${shot.slug}.missing`), missing);
}

if (GALLERY) {
  // Read-only: no throwaway store, no seeded orders.
  for (const shot of SHOTS.filter((s) => !s.gateOnly)) {
    base(`gallery: ${shot.page} — ${shot.state}`, async ({ page }, info) => {
      base.skip(info.project.name === 'phone' && !PHONE.has(shot.slug), 'desktop only');
      const ctx = shot.anon ? { orders: {}, tenantId: '' } : shot.admin ? { orders: {}, tenantId: process.env.GALLERY_TENANT ?? (await galleryStore(page)).tenantId } : await galleryStore(page);
      await run(page, shot, ctx, info.project.name);
    });
  }
} else {
  const test = base.extend<{ ctx: Ctx }>({
    ctx: async ({ store }, use) => use({ orders: await seedOrders(store), tenantId: store.tenantId })
  });
  for (const shot of SHOTS.filter((s) => !s.galleryOnly)) {
    test.describe(() => {
      // Decided before the fixtures run: a skipped phone shot makes no store.
      test.skip(({ isMobile }) => isMobile && !PHONE.has(shot.slug), 'desktop only');
      test(`gallery: ${shot.page} — ${shot.state}`, async ({ page, ctx }, info) => {
        if (shot.anon) await page.context().clearCookies();
        await run(page, shot, ctx, info.project.name);
      });
    });
  }
}
