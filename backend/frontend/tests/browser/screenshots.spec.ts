// Pixel baselines of Store › Website and Store › Payments in their main states, at both sizes, so
// a restructure can't move the look unnoticed. Random bits (hosts with the store's tag, tokens,
// merchant ids, order refs, times) are masked. Re-approve on purpose with --update-snapshots.
import { test, expect, png, MAIN, type TestStore } from './fixtures';
import { openWebsite, saved } from './website';
import { randomBytes } from 'node:crypto';
import type { Locator, Page } from '@playwright/test';

/** Fonts in, nothing saving, no toast; toHaveScreenshot then waits for two identical frames.
 * (Not 'networkidle': the preview frame is re-created on every save and Playwright's idle
 * signal doesn't come back after that.) */
async function settle(page: Page) {
  await page.evaluate(() => document.fonts.ready);
  await expect(page.getByText('Saving…')).toHaveCount(0);
  await expect(page.locator('.toaster [role]')).toHaveCount(0, { timeout: 10_000 });
}

/** The whole page in one frame: the viewport grows to the page's height (a fullPage capture
 * stitches sticky elements — the sidebar, the preview — into the middle of the page). */
async function whole(page: Page, name: string, mask: Locator[]) {
  const vp = page.viewportSize()!;
  const h = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: vp.width, height: h });
  await expect(page).toHaveScreenshot(name, { mask });
}

function websiteMasks(page: Page): Locator[] {
  return [
    page.locator('.status a'), // the live host carries the store's tag
    page.locator('.status .text-muted').filter({ hasText: /published / }),
    page.locator('.host .mono'),
    page.locator('.pending .mono'),
    page.locator('.dns'),
    page.locator('.pane'), // the preview frame (a stub here)
    page.locator('.toaster')
  ];
}

test('Store › Website, a fresh store', async ({ page, store }) => {
  void store;
  await openWebsite(page);
  await settle(page);
  await whole(page, 'website-fresh.png', websiteMasks(page));
});

test('Store › Website, after edits (draft, open panels, own domain, own privacy)', async ({ page, store }) => {
  await store.me('POST', '/site/domains', { host: `order.${store.tag}-pizza.com` }, 201);
  await openWebsite(page);
  await page.getByRole('radio', { name: /Café, calm/ }).click();
  await page.locator('#brand').fill('#0A66C2');
  await page.getByRole('radio', { name: 'Printed', exact: true }).click();
  await page.getByRole('switch', { name: 'Show About us' }).click();
  await page.getByRole('button', { name: 'Move Hours & contact up' }).click();
  await page.locator('li.blk').filter({ hasText: 'Deals' }).locator('button.cat').click();
  await page.getByRole('button', { name: 'Edit Hero' }).click();
  await page.getByLabel('Headline', { exact: true }).fill('Hot slices since 1999.');
  await page.getByLabel('Headline (Spanish)').fill('Rebanadas calientes.');
  await page.getByRole('switch', { name: 'Show the announcement bar' }).click();
  await page.locator('#ann').fill('Closed Thanksgiving Day');
  await page.locator('#ann-from').fill('2026-11-25');
  await page.locator('#ann-to').fill('2026-11-27');
  const privacy = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'Privacy policy' }) });
  await privacy.getByRole('button', { name: 'Edit' }).click();
  await privacy.locator('textarea').fill('Our own policy.\n\nSecond part.');
  await saved(page);
  await settle(page);
  await whole(page, 'website-edited.png', websiteMasks(page));
});

test('Store › Website, the logo cropper open', async ({ page, store }) => {
  void store;
  await openWebsite(page);
  const chooser = page.waitForEvent('filechooser');
  await page.locator('.imgrow').filter({ hasText: 'Logo' }).getByRole('button', { name: 'Upload' }).click();
  await (await chooser).setFiles({ name: 'logo.png', mimeType: 'image/png', buffer: png(400, 200) });
  await expect(page.getByRole('dialog', { name: 'Frame your logo' }).locator('img')).toBeVisible();
  await settle(page);
  await expect(page).toHaveScreenshot('website-cropper.png', { mask: websiteMasks(page) });
});

async function cardOrder(store: TestStore) {
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
  expect(r.status).toBe(201);
}

function paymentMasks(page: Page): Locator[] {
  return [page.locator('span.mono.text-muted'), page.locator('tbody td:first-child'), page.locator('tbody td:nth-child(4) .text-muted'), page.locator('.toaster')];
}

test('Store › Payments, not connected', async ({ page, store }) => {
  void store;
  const fake = await fetch(`${MAIN}/__fake/square/_fake/payments`).catch(() => null);
  test.skip(!fake || fake.status !== 200, 'no fake Square');
  await page.goto('/app/store/payments');
  await expect(page.getByRole('button', { name: 'Connect Square' })).toBeVisible();
  await expect(page.getByText('Nothing waiting')).toBeVisible();
  await settle(page);
  await whole(page, 'payments-not-connected.png', paymentMasks(page));
});

test('Store › Payments, connected with a card order waiting', async ({ page, store }) => {
  const fake = await fetch(`${MAIN}/__fake/square/_fake/payments`).catch(() => null);
  test.skip(!fake || fake.status !== 200, 'no fake Square');
  // Connect without the browser (the payments spec covers the clicks).
  const { url } = (await store.me('POST', '/square/connect', undefined, 200)).data;
  const r = await fetch(url);
  expect(r.url).toContain('square=connected');
  await cardOrder(store);
  await page.goto('/app/store/payments');
  await expect(page.locator('tbody tr')).toHaveCount(1);
  await expect(page.getByText('Connected', { exact: true })).toBeVisible();
  // The order ref is random and its width moves the other columns: give the masked column a
  // fixed width so the customer / amount columns sit in the same place every run.
  await page.addStyleTag({ content: 'thead th:first-child, tbody td:first-child { width: 10rem; min-width: 10rem; max-width: 10rem; overflow: hidden; white-space: nowrap; }' });
  await settle(page);
  await whole(page, 'payments-connected.png', paymentMasks(page));
});
