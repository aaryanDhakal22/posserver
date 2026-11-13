// Store › Menu, after the owner deletes items that a website change and counter buttons still name (prod,
// 2026-11-13: seven unnamed "a counter layout button sells an item this view doesn't show" and six "the bank
// item this entry changes is gone"): every line names its thing by what is left (the button's caption, its tab
// and cell, the entry's name and change, else a short item id) and has a fix: Remove the change, Remove the
// button, Remove all. Find problems lists the same under Broken buttons. Every step is checked against main.
import { test, expect } from './fixtures';

type View = { id: string; kind: string; name: string };

test('views and find problems name what a deleted item left behind, and remove it', async ({ page, store, open }, info) => {
  const { pizza, wings, fries } = store.menu;
  const views = (await store.me('GET', '/menu/views', undefined, 200)).data as View[];
  const web = views.find((v) => v.kind === 'web')!;
  const counter = views.find((v) => v.kind === 'counter')!;
  await store.me('POST', `/menu/views/${web.id}/entries`, { kind: 'item', anchor: { item_ids: [wings.id] }, display_name: 'Party Wings', price_override_cents: 1299 }, 201);
  await store.me('POST', `/menu/views/${web.id}/entries`, { kind: 'item', anchor: { item_ids: [fries.id] }, hidden: true }, 201);
  const bar = { rows: 0, cols: 0, buttons: [] };
  await store.me('PUT', `/menu/views/${counter.id}/layout`, {
    tab_strip: { rows: 1, cols: 2 },
    tabs: [{
      name: 'Sides', strip_row: 0, strip_col: 0, grid: { rows: 2, cols: 2 }, pages: [{ name: 'Page 1' }],
      buttons: [
        { page: 0, row: 0, col: 0, caption: 'Large Wings', action: { type: 'item', item_id: wings.id } },
        { page: 0, row: 0, col: 1, action: { type: 'item', item_id: fries.id } },
        { page: 0, row: 1, col: 0, action: { type: 'item', item_id: pizza.id } }
      ]
    }],
    screens: [], flows: {}, functions: { nav: [], employee: [], manager: [] },
    bottom_bar: { none: bar, line: bar, modifier: bar }, quick_notes: []
  }, 200);
  await store.me('DELETE', `/menu/items/${wings.id}`, undefined, 204);
  await store.me('DELETE', `/menu/items/${fries.id}`, undefined, 204);
  const fry = fries.id.slice(0, 8);

  await open('/store/menu');
  await page.getByRole('tab', { name: 'Views' }).click();

  // The website view: each change says which item it was for, even with no name.
  await page.getByRole('button', { name: 'Web', exact: true }).click();
  const rows = page.getByTestId('attention-row');
  await expect(rows.filter({ hasText: 'Website change for “Party Wings” (item deleted)' })).toContainText('$12.99');
  await expect(rows.filter({ hasText: `Website change for a deleted item ${fry} (hidden)` })).toHaveCount(1);
  await expect(page.getByRole('button', { name: 'Remove all 2 changes' })).toBeVisible();
  await page.screenshot({ path: info.outputPath('menu-broken-web.png'), fullPage: true, animations: 'disabled' });
  await page.getByRole('button', { name: /^Remove: Website change for “Party Wings”/ }).click();
  await expect.poll(async () => (await store.me('GET', `/menu/views/${web.id}/entries`, undefined, 200)).data.length).toBe(1);
  await expect(rows.filter({ hasText: 'Party Wings' })).toHaveCount(0);

  // The counter view: each broken button says its caption, tab and cell.
  await page.getByRole('button', { name: 'Counter', exact: true }).click();
  await expect(rows.filter({ hasText: 'Button “Large Wings” on tab “Sides”: its item was deleted' })).toContainText('page “Page 1”, row 1, column 1');
  await expect(rows.filter({ hasText: 'Button on tab “Sides”: its item was deleted' })).toContainText(`row 1, column 2 · item ${fry}`);
  await page.screenshot({ path: info.outputPath('menu-broken-counter.png'), fullPage: true, animations: 'disabled' });
  await page.getByRole('button', { name: 'Remove all 2 buttons' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Remove all' }).click();
  await expect
    .poll(async () => (await store.me('GET', `/menu/views/${counter.id}/layout`, undefined, 200)).data.tabs[0].buttons.map((b: any) => b.action.item_id))
    .toEqual([pizza.id]);
  await expect(rows).toHaveCount(0);

  // Find problems: what is left (the fries change) under Broken buttons, removed from there.
  await page.getByRole('tab', { name: /Find problems/ }).click();
  await expect(page.getByTestId('count-broken')).toHaveText('1');
  await page.getByTestId('check-broken').click();
  const row = page.getByTestId('problem-row');
  await expect(row).toHaveCount(1);
  await expect(row).toContainText('Web view');
  await expect(row).toContainText(`A change (hidden): its item was deleted (item ${fry}).`);
  await row.getByRole('button', { name: /^Remove: / }).click();
  await expect(page.getByTestId('count-broken')).toHaveText('0');
  expect((await store.me('GET', `/menu/views/${web.id}/entries`, undefined, 200)).data).toEqual([]);
});
