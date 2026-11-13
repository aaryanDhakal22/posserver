// Store › Menu › Quick add (owner 2026-11-13, main 00008): the store's "Quick add" item decides where the
// terminal's typed (Quicc Add) lines print. Opening Store › Menu makes it; the strip opens it; its printer is
// saved like any item's (checked against main's API: GET /me/menu/quick-add + the item's own row); it can't be
// deleted. Screenshots land in the test's output folder (quick-add-*.png).
import { test, expect } from './fixtures';
import type { Page, TestInfo } from '@playwright/test';

async function shot(page: Page, info: TestInfo, name: string) {
  await page.screenshot({ path: info.outputPath(`quick-add-${name}.png`), fullPage: true, animations: 'disabled' });
}

type QuickAdd = { item_id: string; category_id: string; menu_id: string };
type Row = { id: string; printer_id: string | null; no_kitchen_ticket?: boolean; quick_add?: boolean };

test('quick add: the strip opens the item, its printer is saved, it cannot be deleted', async ({ page, store, open }, info) => {
  await open('/store/menu');
  const strip = page.getByTestId('quick-add-strip');
  await expect(strip).toBeVisible();
  const qa = (await store.me('GET', '/menu/quick-add', undefined, 200)).data as QuickAdd;
  await shot(page, info, 'strip');

  await strip.getByRole('button', { name: 'Set where it prints' }).click();
  await expect(page.getByTestId('quick-add-note')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Delete', exact: true })).toHaveCount(0);
  await expect(page.getByTestId('quick-add-row')).toBeVisible();
  const kitchen = ((await store.me('GET', '/printers', undefined, 200)).data as Array<{ id: string }>)[0].id;
  await page.getByLabel('Prints on').selectOption(kitchen);
  await shot(page, info, 'editor');
  await page.getByRole('button', { name: 'Save item' }).click();

  const row = async () => {
    const rows = (await store.me('GET', `/menu/categories/${qa.category_id}/items`, undefined, 200)).data as Row[];
    return rows.find((r) => r.id === qa.item_id)!;
  };
  await expect.poll(async () => (await row()).printer_id).toBe(kitchen);
  expect((await row()).quick_add).toBe(true);
  // main refuses the delete (the button is gone; the API says why).
  const del = await store.me('DELETE', `/menu/items/${qa.item_id}`, undefined, 409);
  expect((del.data as { code: string }).code).toBe('quick_add_kept');
});
