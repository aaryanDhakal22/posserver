// Store › Orders' record of how an order went (the owner, 2026-11-13; contract/main.md §2.5zn, agent §2.10ag):
// the test plays the Pi (floor events over POST /store/v1/agent/rpc) and checks what the manager sees:
//   * the Orders table's "Taken by": "Carlos M. on Counter 1", "Cara S. (call center)", and an order from an
//     older Pi without names shows the terminal's id;
//   * the order panel: the print results per job ("Pizza ✓ · Receipt → rerouted to Online · Wings ✗ failed"),
//     the card's brand on the payment, who took it;
//   * Reports › a day close lists the day's card payments with their brand.
import { test, expect, MAIN, TestStore } from './fixtures';
import { randomUUID } from 'node:crypto';

async function floorEvents(key: string, events: unknown[]) {
  const r = await fetch(`${MAIN}/store/v1/agent/rpc`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'X-STORE-API-KEY': key },
    body: JSON.stringify({ type: 'floor.events', id: randomUUID(), payload: { events } })
  });
  const text = await r.text();
  expect(r.status, text).toBe(200);
  const results = (JSON.parse(text).payload?.results ?? []) as { event_id: string; result: string }[];
  expect(results.filter((x) => x.result !== 'applied'), 'every event applied').toEqual([]);
}

async function seed(s: TestStore) {
  const key = (await s.adminCall('POST', '/devices', { location_id: s.locationId, name: 'record-pi' }, 201)).data.api_key as string;
  const now = Date.now();
  const at = (minAgo: number) => new Date(now - minAgo * 60_000).toISOString();
  const line = (name: string, cents: number) => ({
    id: randomUUID(), position: 0, name, quantity: 1, unit_price_cents: cents, line_total_cents: cents, routing: 'matched'
  });
  const snap = (id: string, minAgo: number, extra: Record<string, unknown>) => ({
    id, source: 'counter', source_event_id: `ck-${id.slice(0, 8)}`, status: 'accepted', currency: 'USD',
    money: { subtotal_cents: 1199, tax_cents: 72, tip_cents: 0, fees: [], discounts: [], total_cents: 1271 },
    customer: { display_name: 'Sam Lee' }, fulfillment: { type: 'counter' }, items: [line('Cheese Pizza', 1199)],
    payments: [], notes: '', placed_at: at(minAgo), staff_id: randomUUID(), device_id: 'terminal-1', ...extra
  });
  const placed = (sn: Record<string, any>) => ({
    event_id: randomUUID(), type: 'order.placed', order_id: sn.id, at: sn.placed_at, batch_id: 9,
    agent_status: { state: 'accepted' }, snapshot: sn
  });
  const counter = randomUUID();
  const call = randomUUID();
  const old = randomUUID();
  const card = randomUUID();
  await floorEvents(key, [
    placed(snap(counter, 30, {
      kitchen_order_number: 21, device_name: 'Counter 1', staff_name: 'Carlos M.',
      payments: [{ id: card, method: 'card', amount_cents: 1271, tip_cents: 200, last4: '4242', brand: 'VISA', at: at(30) }]
    })),
    placed(snap(call, 20, {
      kitchen_order_number: 22, device_id: 'callcenter', device_kind: 'callcenter', taken_by: 'Call center - Cara S.',
      payments: [{ id: randomUUID(), method: 'manual_card', amount_cents: 1271, last4: '0005', brand: 'amex', at: at(20) }]
    })),
    placed(snap(old, 10, { kitchen_order_number: 23, payments: [{ id: randomUUID(), method: 'cash', amount_cents: 1271, at: at(10) }] }))
  ]);
  await floorEvents(key, [
    {
      event_id: randomUUID(), type: 'order.printed', order_id: counter, mode: 'kitchen', at: at(29),
      tickets: [
        { kind: 'station', printer: 'Pizza', result: 'printed', printed_on: 'Pizza', attempts: 1, at: at(29) },
        { kind: 'station', printer: 'Wings', result: 'failed', attempts: 3, error: 'printer unreachable', at: at(29) },
        { kind: 'receipt', printer: 'Counter', result: 'rerouted', printed_on: 'Online', by: 'auto', attempts: 2, at: at(29) }
      ]
    },
    {
      event_id: randomUUID(), type: 'order.printed', order_id: counter, mode: 'receipt', at: at(25),
      tickets: [{ kind: 'reprint', printer: 'Counter', result: 'printed', printed_on: 'Counter', attempts: 1, at: at(25) }]
    },
    {
      event_id: randomUUID(), type: 'z.report', at: at(1),
      report: {
        id: randomUUID(), version: 1, batch_id: 9, business_date: new Date(now).toISOString().slice(0, 10), timezone: 'America/New_York',
        opened_at: at(60), closed_at: at(1), close_kind: 'manual', forced: false, counts: { orders: 3 }, sales: { total_cents: 3813 },
        tenders: [], reconcile: { difference_cents: 0 }
      }
    }
  ]);
  return { counter, call, old, card };
}

test('the order record: taken by, print results, card brand', async ({ page, store, open }) => {
  const o = await seed(store);
  await open('/store/orders');
  const row = (id: string) => page.locator(`tr[data-order="${id}"]`);
  await expect(row(o.counter).getByTestId('taken-by')).toHaveText('Carlos M. on Counter 1');
  await expect(row(o.call).getByTestId('taken-by')).toHaveText('Cara S. (call center)');
  await expect(row(o.old).getByTestId('taken-by')).toHaveText('On terminal-1'); // an older Pi: the id

  await row(o.counter).click();
  const panel = page.getByTestId('order-detail');
  await expect(panel).toBeVisible();
  await expect(panel).toContainText('Carlos M. on Counter 1');
  await expect(panel.getByTestId('card-brand')).toHaveText(' · Visa');
  await expect(panel).toContainText('•••• 4242');
  const prints = panel.getByTestId('print-results');
  await expect(prints.locator('li')).toHaveCount(2);
  await expect(prints.locator('li').first()).toContainText('Kitchen tickets');
  await expect(prints.locator('li').first()).toContainText('Pizza ✓');
  await expect(prints.locator('li').first()).toContainText('Wings ✗ failed (3 tries)');
  await expect(prints.locator('li').first()).toContainText('Receipt → rerouted to Online');
  await expect(prints.locator('li').nth(1)).toContainText('Receipt reprinted');
  await expect(panel).toContainText('What printed where, as the Pi reported it.');

  // The call-center order: its brand, no print results (the older-Pi note).
  await page.goto(`/app/store/orders?order=${o.call}`);
  await expect(panel).toContainText('Cara S. (call center)');
  await expect(panel.getByTestId('card-brand')).toHaveText(' · Amex');
  await expect(panel.getByTestId('print-results')).toHaveCount(0);

  // Reports: the day close lists the card payments with their brand.
  await open('/store/reports');
  await page.getByRole('button', { name: 'Open', exact: true }).first().click();
  const cards = page.getByTestId('z-cards');
  await expect(cards).toBeVisible();
  await expect(cards).toContainText('Visa · 1');
  await expect(cards).toContainText('Amex · 1');
  await expect(cards.locator('tbody tr')).toHaveCount(2);
  await expect(cards).toContainText('Card (reader) · Visa');
});
