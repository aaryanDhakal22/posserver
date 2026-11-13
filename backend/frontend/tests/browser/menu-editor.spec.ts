// Store › Menu, the owner's 2026-11-13 round: select items and edit them together (prices by $ / % with a
// preview, printer, category, stock — one request each, all or nothing, with Undo), search by kitchen name /
// SKU, the item panel's Discard + Ctrl+S, and the Find problems tab (a count per check, select, fix in bulk,
// open the item or the group). Every step is checked against main's own API. Screenshots land in the test's
// output folder (menu-*.png; the gate copies them to artifacts/main-browser/).
import { test, expect, type TestStore } from './fixtures';
import type { Page, TestInfo } from '@playwright/test';

async function shot(page: Page, info: TestInfo, name: string) {
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: info.outputPath(`menu-${name}.png`), fullPage: true, animations: 'disabled' });
}

type Row = { id: string; name: string; price_cents: number; in_stock: boolean; printer_id: string | null; no_kitchen_ticket?: boolean; category_id: string };
async function item(store: TestStore, catId: string, name: string): Promise<Row | undefined> {
  const rows = (await store.me('GET', `/menu/categories/${catId}/items`, undefined, 200)).data as Row[];
  return rows.find((r) => r.name === name);
}
const select = (page: Page, name: string) => page.getByRole('checkbox', { name: `Select ${name}`, exact: true });
const bar = (page: Page) => page.getByTestId('bulk-bar');

test('bulk edit: select, change prices with a preview, undo, move, printer, stock', async ({ page, store, open }, info) => {
  const { pz, sd } = store.menu;
  await open('/store/menu');
  await expect(page.getByRole('button', { name: 'Wings', exact: true })).toBeVisible();
  await expect(bar(page)).toHaveCount(0);

  // Two items: +$1.00, rounded up to .99.
  await select(page, 'Wings').check();
  await select(page, 'Fries').check();
  await expect(page.getByTestId('bulk-count')).toHaveText('2 items selected');
  await bar(page).getByRole('button', { name: 'Change prices…' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByLabel('Amount ($, − lowers)').fill('1');
  await expect(page.getByTestId('bulk-preview')).toContainText('$10.99 → $11.99');
  await expect(page.getByTestId('bulk-preview')).toContainText('$3.99 → $4.99');
  await dlg.getByRole('checkbox', { name: 'Round up to .99' }).check();
  await expect(page.getByTestId('bulk-preview')).toContainText('$3.99 → $4.99');
  await shot(page, info, 'bulk-price');
  await dlg.getByRole('button', { name: 'Change 2 items' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect.poll(async () => (await item(store, sd.id, 'Wings'))?.price_cents).toBe(1199);
  expect((await item(store, sd.id, 'Fries'))?.price_cents).toBe(499);
  // The selection clears after a save; Undo puts the old prices back.
  await expect(bar(page)).toHaveCount(0);
  await page.getByRole('button', { name: 'Undo' }).click();
  await expect.poll(async () => (await item(store, sd.id, 'Wings'))?.price_cents).toBe(1099);
  expect((await item(store, sd.id, 'Fries'))?.price_cents).toBe(399);

  // A price below $0 is refused before anything is sent.
  await select(page, 'Fries').check();
  await bar(page).getByRole('button', { name: 'Change prices…' }).click();
  await page.getByRole('dialog').getByRole('radio', { name: 'By %' }).click();
  await page.getByRole('dialog').getByLabel('Percent (− lowers)').fill('-150');
  await expect(page.getByRole('dialog').getByRole('button', { name: 'Change 1 item' })).toBeDisabled();
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();

  // Move Fries to Pizza; then print it nowhere but the receipt; then out of stock.
  await bar(page).getByRole('button', { name: 'Move to…' }).click();
  await page.getByRole('dialog').getByLabel('Category').selectOption({ label: 'Pizza' });
  await page.getByRole('dialog').getByRole('button', { name: 'Move' }).click();
  await expect.poll(async () => (await item(store, pz.id, 'Fries'))?.category_id).toBe(pz.id);
  // A save clears the selection once the page has its answer (the API can show the change a moment sooner).
  await expect(bar(page)).toHaveCount(0);

  await select(page, 'Fries').check();
  await bar(page).getByRole('button', { name: 'Printer…' }).click();
  await page.getByRole('dialog').getByLabel('Prints on').selectOption('__none');
  await page.getByRole('dialog').getByRole('button', { name: 'Save 1 item' }).click();
  await expect.poll(async () => (await item(store, pz.id, 'Fries'))?.no_kitchen_ticket).toBe(true);
  await expect(bar(page)).toHaveCount(0);

  // A whole category at once.
  await page.getByRole('checkbox', { name: 'Select the items of Pizza' }).check();
  await expect(page.getByTestId('bulk-count')).toHaveText('2 items selected');
  await bar(page).getByRole('button', { name: 'Out of stock' }).click();
  await expect.poll(async () => (await item(store, pz.id, 'Cheese Pizza'))?.in_stock).toBe(false);
  expect((await item(store, pz.id, 'Fries'))?.in_stock).toBe(false);
  await expect(page.getByRole('switch', { name: 'Cheese Pizza in stock' })).toHaveAttribute('aria-checked', 'false');
});

test('search finds kitchen names and SKUs; "/" jumps to it; the item panel has Discard and Ctrl+S', async ({ page, store, open }, info) => {
  await store.me('PUT', `/menu/items/${store.menu.fries.id}/bank`, { kitchen_name: 'FF', sku: 'st:4242', sizes: [] }, 204);
  await open('/store/menu');
  await expect(page.getByRole('button', { name: 'Wings', exact: true })).toBeVisible();
  await page.keyboard.press('/');
  await expect(page.getByLabel('Search items')).toBeFocused();
  await page.keyboard.type('st:4242');
  await expect(page.getByText('Showing 1 of 3 items.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Fries', exact: true })).toBeVisible();
  await page.getByLabel('Search items').fill('FF');
  await expect(page.getByText('Showing 1 of 3 items.')).toBeVisible();

  // The panel: a change shows Discard; Discard closes without saving.
  await page.getByRole('button', { name: 'Fries', exact: true }).click();
  const panel = page.getByRole('dialog');
  await expect(panel.getByRole('heading', { name: 'Basics' })).toBeVisible();
  await expect(panel.getByRole('button', { name: 'Discard' })).toHaveCount(0);
  await panel.locator('#ie-name').fill('Fries!');
  await panel.getByRole('button', { name: 'Discard' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect((await item(store, store.menu.sd.id, 'Fries'))?.name).toBe('Fries');

  // Ctrl+S saves.
  await page.getByRole('button', { name: 'Fries', exact: true }).click();
  await page.getByRole('dialog').locator('#ie-name').fill('Crinkle Fries');
  await shot(page, info, 'item-panel');
  await page.getByRole('dialog').locator('#ie-name').press('Control+s');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect.poll(async () => !!(await item(store, store.menu.sd.id, 'Crinkle Fries'))).toBe(true);
});

test('find problems: a count per check, select and fix in bulk, open the item or the group', async ({ page, store, open }, info) => {
  // Planted: a category without a printer (2 items print everywhere, and the store's Quick add item that the
  // menu page makes), a $0 size, an empty group.
  await store.me('POST', '/menu/bank/apply', {
    format: 1, views: [], deals: [],
    sizes: [{ name: 'Small', short: 'S' }, { name: 'Large', short: 'L' }],
    groups: [{ name: 'Dressings', options: [] }],
    menus: [{ name: 'Main', categories: [
      { name: 'Specials', items: [
        { name: 'Calzone', price_cents: 999, groups: ['Dressings'] },
        { name: 'Stromboli', sizes: [{ size: 'Small', price_cents: 899 }, { size: 'Large', price_cents: 0 }] }] }] }]
  }, 200);
  await open('/store/menu');
  await page.getByRole('tab', { name: /Find problems/ }).click();
  await expect(page.getByTestId('count-printer')).toHaveText('3');
  await expect(page.getByTestId('count-price')).toHaveText('1');
  await expect(page.getByTestId('count-mods')).toHaveText('1');

  // No kitchen printer: select both, give them the kitchen's printer.
  await page.getByTestId('check-printer').click();
  await expect(page.getByTestId('problem-row')).toHaveCount(3);
  await shot(page, info, 'problems-printer');
  await page.getByRole('checkbox', { name: 'Select every item shown' }).check();
  await page.getByTestId('bulk-bar').getByRole('button', { name: 'Printer…' }).click();
  await page.getByRole('dialog').getByLabel('Prints on').selectOption({ label: 'Kitchen' });
  await page.getByRole('dialog').getByRole('button', { name: 'Save 3 items' }).click();
  await expect(page.getByTestId('count-printer')).toHaveText('0');
  await expect(page.getByTestId('no-problems')).toContainText('Nothing to fix here.');
  const problems = (await store.me('GET', '/menu/problems', undefined, 200)).data;
  expect(problems.counts.printer).toBe(0);

  // Prices: the $0 Large opens the item's panel.
  await page.getByTestId('check-price').click();
  await expect(page.getByTestId('problem-row').first()).toContainText('Large is $0.00 while Small has a price.');
  await page.getByRole('button', { name: 'Open Stromboli' }).click();
  await expect(page.getByRole('dialog').getByRole('heading', { name: 'Stromboli' })).toBeVisible();
  await page.getByRole('dialog').getByLabel('Price for Large ($)').fill('12.99');
  await page.getByRole('dialog').getByRole('button', { name: 'Save item' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.getByRole('tab', { name: /Find problems/ }).click();
  await page.getByTestId('check-price').click();
  await expect(page.getByTestId('count-price')).toHaveText('0');

  // Modifier problems: the empty group opens on the Modifier groups tab.
  await page.getByTestId('check-mods').click();
  await expect(page.getByTestId('problem-row').first()).toContainText('No options, but 1 item uses it.');
  await page.getByRole('button', { name: 'Open Dressings' }).click();
  await expect(page.getByRole('tab', { name: 'Modifier groups' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#mg-name')).toHaveValue('Dressings');
});
