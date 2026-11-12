// Store › Payments against dev main's fake Square (/__fake/square): connect (OAuth round trip),
// the return messages, the location picker, disconnect, and the card holds list (filters, Refresh,
// Charge now). Every step is checked against main's own state.
import { test, expect, MAIN, type TestStore } from './fixtures';
import { createHmac, randomBytes } from 'node:crypto';
import type { Page } from '@playwright/test';

const FAKE = `${MAIN}/__fake/square`;

test.beforeEach(async () => {
  const r = await fetch(`${FAKE}/_fake/payments`).catch(() => null);
  test.skip(!r || r.status !== 200, "main isn't running the fake Square (SQUARE_ENV=fake)");
});

const dollars = (c: number) => `$${(c / 100).toFixed(2)}`;

async function square(store: TestStore) {
  return (await store.me('GET', '/square', undefined, 200)).data;
}

/**
 * The Connect button sends the browser to Square's authorize page; Square → main's callback → main's
 * own /app/store/payments?square=…. Playwright can't route the redirected hops, so the first hop
 * is answered here: the chain is followed server-side (exactly as the browser would; main's callback
 * needs no cookie, the state names the store) and the browser lands on this run's copy of the app.
 * `extra` is appended to the authorize URL (e.g. "&deny=1": the owner says no).
 */
async function backToThisApp(page: Page, baseURL: string, extra = '') {
  await page.route(/\/__fake\/square\/oauth2\/authorize\?/, async (route) => {
    let url = route.request().url() + extra;
    for (let hop = 0; hop < 5; hop++) {
      const r = await fetch(url, { redirect: 'manual' });
      const loc = r.headers.get('location');
      if (!loc) throw new Error(`the OAuth chain stopped at ${url}: ${r.status}`);
      const next = new URL(loc, url);
      if (next.pathname.startsWith('/app/')) return route.fulfill({ status: 302, headers: { location: baseURL + next.pathname + next.search } });
      url = next.toString();
    }
    throw new Error('the OAuth chain never came back to /app');
  });
}

async function connect(page: Page, baseURL: string) {
  await backToThisApp(page, baseURL);
  await page.goto('/app/store/payments');
  await page.getByRole('button', { name: 'Connect Square' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Square is connected' })).toBeVisible();
  await expect(page).toHaveURL(/\/app\/store\/payments$/); // the ?square= note is gone
}

/** A card order on the store's website channel (a hold on the fake Square). */
async function cardOrder(store: TestStore): Promise<string> {
  const ch = (await store.adminCall('POST', '/channels', { location_id: store.locationId, kind: 'web', name: 'Web test' }, 201)).data;
  const r = await fetch(`${MAIN}/channel/v1/orders`, {
    method: 'POST',
    headers: { authorization: `Bearer ${ch.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({
      client_order_id: `pw-${randomBytes(5).toString('hex')}`,
      type: 'pickup',
      lines: [{ item_id: store.menu.fries.id, qty: 2 }],
      payment: { method: 'card', token: 'cnon:card-nonce-ok' },
      customer: { name: 'Ada Lovelace', phone: '(410) 555-0101', email: 'ada@example.com' }
    })
  });
  const body = await r.json();
  expect(r.status, JSON.stringify(body)).toBe(201);
  return body.id;
}

async function webhook(body: object) {
  const raw = JSON.stringify(body);
  const sig = createHmac('sha256', 'fake-webhook-key').update(`${MAIN}/square/webhook` + raw).digest('base64');
  const r = await fetch(`${MAIN}/square/webhook`, { method: 'POST', headers: { 'content-type': 'application/json', 'x-square-hmacsha256-signature': sig }, body: raw });
  expect(r.status).toBe(200);
}

test('not connected: the Connect button and the test-mode note', async ({ page, store }) => {
  await page.goto('/app/store/payments');
  await expect(page.getByRole('button', { name: 'Connect Square' })).toBeVisible();
  await expect(page.getByText('Test mode (no real Square)')).toBeVisible();
  await expect(page.getByText('Nothing waiting')).toBeVisible();
  const sq = await square(store);
  expect(sq.connected).toBe(false);
  expect(sq.state).toBe('');
});

test('connect through Square and back: connected, the location, nothing waiting', async ({ page, store, baseURL }) => {
  await connect(page, baseURL!);
  const sq = await square(store);
  expect(sq.connected).toBe(true);
  await expect(page.getByText('Connected', { exact: true })).toBeVisible();
  await expect(page.getByText(sq.merchant_id, { exact: true })).toBeVisible();
  const loc = page.getByLabel('Payments go to');
  await expect(loc).toHaveValue(sq.square_location_id);
  await expect(loc.locator('option')).toHaveCount(sq.locations.length);
  await expect(page.getByText('No card orders waiting to be charged')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Disconnect' })).toBeEnabled();

  // Choosing the location saves it.
  const put = page.waitForResponse((r) => r.url().endsWith('/store/v1/me/square/location') && r.request().method() === 'PUT');
  await loc.selectOption(sq.square_location_id);
  expect((await put).status()).toBe(200);
  await expect(page.getByRole('status').filter({ hasText: 'Card payments go to that Square location now' })).toBeVisible();
});

test('the owner says no on Square’s page: nothing changes', async ({ page, store, baseURL }) => {
  await backToThisApp(page, baseURL!, '&deny=1');
  await page.goto('/app/store/payments');
  await page.getByRole('button', { name: 'Connect Square' }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'Square access was not given — nothing changed' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Connect Square' })).toBeVisible();
  expect((await square(store)).connected).toBe(false);
});

test('the return notes for an expired link and an error', async ({ page, store }) => {
  void store;
  await page.goto('/app/store/payments?square=expired');
  await expect(page.getByRole('alert').filter({ hasText: 'The Square link expired — connect again' })).toBeVisible();
  await expect(page).toHaveURL(/\/app\/store\/payments$/);
  await page.goto('/app/store/payments?square=error');
  await expect(page.getByRole('alert').filter({ hasText: "Square couldn't be connected — try again" })).toBeVisible();
  // An unknown value says nothing.
  await page.goto('/app/store/payments?square=whatever');
  await expect(page.getByRole('button', { name: 'Connect Square' })).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('disconnect asks first; Keep keeps it, Disconnect drops it and offers Reconnect', async ({ page, store, baseURL }) => {
  await connect(page, baseURL!);
  await page.getByRole('button', { name: 'Disconnect' }).click();
  const dlg = page.getByRole('dialog', { name: 'Disconnect Square?' });
  await expect(dlg.getByText('Card orders stop until you connect Square again.')).toBeVisible();
  await dlg.getByRole('button', { name: 'Keep' }).click();
  await expect(dlg).toBeHidden();
  expect((await square(store)).connected).toBe(true);

  await page.getByRole('button', { name: 'Disconnect' }).click();
  await page.getByRole('dialog', { name: 'Disconnect Square?' }).getByRole('button', { name: 'Disconnect' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Square disconnected' })).toBeVisible();
  // The connection is forgotten: back to the first screen.
  const sq = await square(store);
  expect(sq.connected).toBe(false);
  expect(sq.state).toBe('');
  await expect(page.getByRole('button', { name: 'Connect Square' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Disconnect' })).toHaveCount(0);
});

test('card holds: waiting list, Charge now (Cancel / confirm), filters, Refresh, disconnect blocked while waiting', async ({ page, store, baseURL }) => {
  await connect(page, baseURL!);
  const a = await cardOrder(store);
  const b = await cardOrder(store);
  await page.reload();
  const rows = page.locator('tbody tr');
  await expect(rows).toHaveCount(2);
  const holds = (await store.me('GET', '/card-holds?status=held', undefined, 200)).data.holds as any[];
  const ha = holds.find((h) => h.order_id === a)!;
  const rowA = rows.filter({ hasText: ha.order_ref });
  await expect(rowA.getByText('Ada Lovelace')).toBeVisible();
  await expect(rowA.getByText(dollars(ha.amount_cents + ha.tip_cents))).toBeVisible();
  await expect(rowA.getByText('Waiting')).toBeVisible();
  await expect(rowA.getByText(/^Pickup · /)).toBeVisible();
  await expect(rowA.getByText(/^Charged by .+ at the latest$/)).toBeVisible();
  await expect(page.getByText('2 card orders waiting to be charged')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Disconnect' })).toBeDisabled();
  await expect(page.getByText('You can disconnect once nothing is waiting (after Close batch).')).toBeVisible();

  // Charge now: Cancel does nothing, Charge now charges.
  const total = ha.amount_cents + ha.tip_cents + ha.added_tip_cents;
  await rowA.getByRole('button', { name: 'Charge now' }).click();
  const dlg = page.getByRole('dialog', { name: `Charge ${dollars(total)} now?` });
  await expect(dlg.getByText(`${ha.order_ref} · Ada Lovelace.`)).toBeVisible();
  await dlg.getByRole('button', { name: 'Cancel' }).click();
  await expect(dlg).toBeHidden();
  expect((await store.me('GET', '/card-holds?status=held', undefined, 200)).data.holds).toHaveLength(2);
  await rowA.getByRole('button', { name: 'Charge now' }).click();
  await page.getByRole('dialog', { name: `Charge ${dollars(total)} now?` }).getByRole('button', { name: 'Charge now' }).click();
  await expect(page.getByRole('status').filter({ hasText: `Charged ${dollars(total)}` })).toBeVisible();
  await expect(rows).toHaveCount(1);
  await expect(page.getByText('1 card order waiting to be charged')).toBeVisible();
  const charged = (await store.me('GET', '/card-holds?status=captured', undefined, 200)).data.holds as any[];
  expect(charged.map((h) => h.order_id)).toEqual([a]);
  expect(charged[0].capture_reason).toBe('manual');

  // Filters.
  const show = page.getByRole('radiogroup', { name: 'Show' });
  await show.getByRole('radio', { name: 'Charged' }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows.first().getByText('Charged', { exact: true })).toBeVisible();
  await expect(rows.first().getByText(new RegExp(`^${dollars(total).replace('$', '\\$')} · with Charge now · `))).toBeVisible();
  await expect(rows.first().getByRole('button', { name: 'Charge now' })).toHaveCount(0);
  await show.getByRole('radio', { name: 'All' }).click();
  await expect(rows).toHaveCount(2);
  await show.getByRole('radio', { name: 'Problems' }).click();
  await expect(page.getByText('No card orders here')).toBeVisible();

  // Released: the other order voided at Square (webhook).
  const hb = (await store.me('GET', '/card-holds?status=held', undefined, 200)).data.holds.find((h: any) => h.order_id === b);
  const sq = await square(store);
  await webhook({ merchant_id: sq.merchant_id, type: 'payment.updated', event_id: randomBytes(8).toString('hex'), data: { type: 'payment', id: hb.payment_ref, object: { payment: { id: hb.payment_ref, status: 'CANCELED' } } } });
  await show.getByRole('radio', { name: 'Released' }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows.first().getByText('Released')).toBeVisible();
  await expect(rows.first().getByText('Voided in Square')).toBeVisible();

  // Refresh picks up an order placed since.
  await show.getByRole('radio', { name: 'Waiting' }).click();
  await expect(page.getByText('Nothing waiting')).toBeVisible();
  await cardOrder(store);
  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect(rows).toHaveCount(1);
});

test('access removed in Square: the store sees it and can reconnect', async ({ page, store, baseURL }) => {
  await connect(page, baseURL!);
  const sq = await square(store);
  await webhook({ merchant_id: sq.merchant_id, type: 'oauth.authorization.revoked', event_id: randomBytes(8).toString('hex'), data: { type: 'revocation', id: 'x', object: {} } });
  await page.reload();
  await expect(page.getByText('Access removed in Square')).toBeVisible();
  await expect(page.getByText('Card orders are off until Square is connected again.')).toBeVisible();
  await page.getByRole('button', { name: 'Reconnect Square' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Square is connected' })).toBeVisible();
  await expect(page.getByText('Connected', { exact: true })).toBeVisible();
  expect((await square(store)).connected).toBe(true);
});

/** A token the fake Square issued (stands in for the Developer Console's sandbox access token) + its active location. */
async function fakeSandboxToken(): Promise<{ token: string; location: string }> {
  const auth = await fetch(`${FAKE}/oauth2/authorize?client_id=sq0idp-fake&redirect_uri=${encodeURIComponent('http://paste.invalid/cb')}&state=s&scope=PAYMENTS_WRITE`, { redirect: 'manual' });
  const code = new URL(auth.headers.get('location')!).searchParams.get('code')!;
  const tok = await (await fetch(`${FAKE}/oauth2/token`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ client_id: 'sq0idp-fake', client_secret: 'sq0csp-fake', grant_type: 'authorization_code', code })
  })).json();
  const locs = await (await fetch(`${FAKE}/v2/locations`, { headers: { authorization: `Bearer ${tok.access_token}` } })).json();
  return { token: tok.access_token, location: locs.locations.find((l: any) => l.status === 'ACTIVE').id };
}

test('paste a Square sandbox token (staging only): shown only when main runs the sandbox, saved like a connection', async ({ page, store }) => {
  // Dev main runs the fake Square, so it never offers the form: none here.
  await page.goto('/app/store/payments');
  await expect(page.getByRole('button', { name: 'Connect Square' })).toBeVisible();
  await expect(page.getByTestId('square-paste')).toHaveCount(0);
  // Staging's main (SQUARE_ENV=sandbox) says sandbox_paste: true; the save goes to the real route.
  await page.route(/\/store\/v1\/me\/square$/, async (route) => {
    const r = await route.fetch();
    const body = await r.json();
    await route.fulfill({ response: r, json: { ...body, sandbox_paste: true } });
  });
  await page.reload();
  const { token, location } = await fakeSandboxToken();
  await expect(page.getByTestId('square-paste')).toBeVisible();
  await page.getByLabel('Sandbox access token').fill('EAAA-not-a-token');
  await page.getByLabel('Sandbox location ID').fill(location);
  await page.getByRole('button', { name: 'Save sandbox token' }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'refused by Square' })).toBeVisible();
  expect((await square(store)).connected).toBe(false);
  await page.getByLabel('Sandbox access token').fill(token);
  await page.getByRole('button', { name: 'Save sandbox token' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'sandbox, pasted token' })).toBeVisible();
  await expect(page.getByText('Connected (sandbox, pasted token)', { exact: true })).toBeVisible();
  const st = await square(store);
  expect([st.connected, st.pasted, st.square_location_id]).toEqual([true, true, location]);
  expect(JSON.stringify(st)).not.toContain(token);
});
