// Store › Menu › Families and the Deals tab's coupons (the owner's Centurion pass, 2026-11-12), on a
// laptop and a phone. Every step is checked against main's own API (GET /store/v1/me/menu/families,
// /me/deals); screenshots land in the test's output folder (families-*.png).
import { test, expect, type TestStore } from './fixtures';
import type { Page, TestInfo } from '@playwright/test';

type Listing = {
  families: Array<{ id: string; kind: string; name: string; class: string; items: number; options: number }>;
  items: Array<{ id: string; name: string; family_id: string | null }>;
  options: Array<{ id: string; name: string; family_id: string | null }>;
};

async function listing(store: TestStore): Promise<Listing> {
  return (await store.me('GET', '/menu/families', undefined, 200)).data as Listing;
}

async function shot(page: Page, info: TestInfo, name: string) {
  await page.screenshot({ path: info.outputPath(`families-${name}.png`), fullPage: true, animations: 'disabled' });
}

/** A Toppings group (not on any item) for the mod families. */
async function toppings(store: TestStore) {
  await store.me('POST', '/menu/bank/apply', {
    format: 1, sizes: [], menus: [], views: [], deals: [],
    groups: [{ name: 'Toppings', max_select: 0, allow_multiple: true, options: [
      { name: 'Pepperoni', price_cents: 150 }, { name: 'Steak', price_cents: 300 }, { name: 'Well Done', price_cents: 0 }] }]
  }, 200);
}

const famRow = (page: Page, name: string) => page.getByTestId('family-row').filter({ hasText: name });

async function openFamilies(page: Page, open: (p: string) => Promise<void>) {
  await open('/store/menu');
  await page.getByRole('tab', { name: 'Families' }).click();
}

async function selectMembers(page: Page, names: string[]) {
  for (const n of names) await page.getByRole('checkbox', { name: new RegExp(`^Select ${n} \\(`) }).check();
}

test('families: add, bulk assign with the no-family filter, rename, mod classes, merge, delete', async ({ page, store, open }, info) => {
  await toppings(store);
  await openFamilies(page, open);
  await expect(page.getByText('No families yet')).toBeVisible();
  await expect(page.getByText('3 with no family yet')).toBeVisible();
  await shot(page, info, 'empty');

  // Two item families; the "no family yet" filter + bulk assign.
  await page.getByLabel('New family name').fill('Sides');
  await page.getByRole('button', { name: 'Add family' }).click();
  await expect(famRow(page, 'Sides')).toBeVisible();
  await page.getByLabel('New family name').fill('Pizza');
  await page.getByRole('button', { name: 'Add family' }).click();
  await expect(famRow(page, 'Pizza')).toBeVisible();
  await page.getByLabel('Show', { exact: true }).selectOption({ label: 'No family yet' });
  await selectMembers(page, ['Wings', 'Fries']);
  await expect(page.getByTestId('bulk-bar')).toContainText('2 selected');
  await page.getByLabel('Family for the selected').selectOption({ label: 'Sides' });
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect(page.getByText('1 with no family yet')).toBeVisible();
  await expect(page.getByTestId('member-row')).toHaveCount(1); // only Cheese Pizza is left in the filter
  await selectMembers(page, ['Cheese Pizza']);
  await page.getByLabel('Family for the selected').selectOption({ label: 'Pizza' });
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect(page.getByText('Nothing matches.')).toBeVisible();
  let got = await listing(store);
  const sides = got.families.find((f) => f.name === 'Sides')!;
  expect(got.items.filter((i) => i.family_id === sides.id).map((i) => i.name).sort()).toEqual(['Fries', 'Wings']);
  expect(sides.items).toBe(2);

  // Rename.
  await page.getByRole('button', { name: 'Rename Sides' }).click();
  await page.getByLabel('New name for Sides').fill('Wings & Sides');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(famRow(page, 'Wings & Sides')).toBeVisible();
  got = await listing(store);
  expect(got.families.find((f) => f.id === sides.id)!.name).toBe('Wings & Sides');
  await shot(page, info, 'items');

  // Mod families: regular + premium, a class change, then assign options.
  await page.getByRole('radio', { name: /Mod families/ }).click();
  await page.getByLabel('New family name').fill('Pizza Topping');
  await page.getByRole('button', { name: 'Add family' }).click();
  await page.getByLabel('New family name').fill('Premium Topping');
  await page.getByLabel('New family class').selectOption('premium');
  await page.getByRole('button', { name: 'Add family' }).click();
  await expect(famRow(page, 'Premium Topping')).toContainText('Premium');
  await selectMembers(page, ['Pepperoni']);
  await page.getByLabel('Family for the selected').selectOption({ label: 'Pizza Topping' });
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect(page.getByText('2 with no family yet')).toBeVisible();
  await selectMembers(page, ['Steak']);
  await page.getByLabel('Family for the selected').selectOption({ label: 'Premium Topping' });
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect(page.getByText('1 with no family yet')).toBeVisible();
  await page.getByLabel('Class of Premium Topping').selectOption('unpriced');
  await expect(famRow(page, 'Premium Topping')).toContainText('Unpriced');
  got = await listing(store);
  const prem = got.families.find((f) => f.name === 'Premium Topping')!;
  expect(prem.class).toBe('unpriced');
  expect(got.options.find((o) => o.name === 'Steak')!.family_id).toBe(prem.id);
  expect(got.families.find((f) => f.name === 'Pizza Topping')!.class).toBe('regular');
  await shot(page, info, 'mods');

  // Merge Premium Topping into Pizza Topping: Steak moves, the family is gone.
  await page.getByRole('button', { name: 'Merge Premium Topping' }).click();
  await page.getByLabel('Merge Premium Topping into').selectOption({ label: 'Pizza Topping' });
  await page.getByRole('button', { name: 'Merge', exact: true }).click();
  await expect(famRow(page, 'Premium Topping')).toHaveCount(0);
  got = await listing(store);
  const top = got.families.find((f) => f.name === 'Pizza Topping')!;
  expect(top.options).toBe(2);

  // Delete it: its options go back to no family.
  await page.getByRole('button', { name: 'Delete Pizza Topping' }).click();
  await page.getByRole('button', { name: 'Delete family' }).click();
  await expect(page.getByText('No families yet')).toBeVisible();
  await expect(page.getByText('3 with no family yet')).toBeVisible();
  got = await listing(store);
  expect(got.families.filter((f) => f.kind === 'mod')).toHaveLength(0);
  expect(got.options.every((o) => o.family_id === null)).toBe(true);
});

test('coupons: add one by family on the Deals tab, the test cart fits it, saved for the counter', async ({ page, store, open }, info) => {
  const pizza = (await store.me('POST', '/menu/families', { kind: 'item', name: 'Pizza' }, 201)).data;
  const sides = (await store.me('POST', '/menu/families', { kind: 'item', name: 'Sides' }, 201)).data;
  await store.me('POST', '/menu/families/assign', { family_id: pizza.id, items: [store.menu.pizza.id] }, 200);
  await store.me('POST', '/menu/families/assign', { family_id: sides.id, items: [store.menu.wings.id, store.menu.fries.id] }, 200);

  await open('/store/menu');
  await page.getByRole('tab', { name: 'Deals' }).click();
  await page.getByRole('button', { name: 'Add deal' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByRole('radio', { name: 'Coupon' }).click();
  await dlg.getByLabel('Name', { exact: true }).fill('Pizza + Side');
  await dlg.getByLabel('Coupon price ($)').fill('10');
  await dlg.getByLabel('Card lines (one per line, up to 6)').fill('Any pizza, 1 topping\nWings or fries');
  await dlg.getByLabel('Part 1', { exact: true }).fill('Pizza');
  await dlg.getByLabel('Regular toppings free').fill('1');
  await dlg.getByTestId('coupon-part').nth(0).getByRole('button', { name: 'Pizza', exact: true }).click();
  await dlg.getByRole('button', { name: 'Add a part' }).click();
  await dlg.getByLabel('Part 2', { exact: true }).fill('Side');
  await dlg.getByTestId('coupon-part').nth(1).getByRole('button', { name: 'Sides', exact: true }).click();
  // The test cart: a pizza alone doesn't fit; + fries it does (11.99 + 3.99 − 10.00 = 5.98).
  await dlg.getByLabel('Add an item to the test cart').selectOption({ label: 'Cheese Pizza' });
  await dlg.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(dlg.getByTestId('coupon-fit')).toHaveText('The cart doesn’t fit this coupon yet.');
  await dlg.getByLabel('Add an item to the test cart').selectOption({ label: 'Fries' });
  await dlg.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(dlg.getByTestId('coupon-fit')).toHaveText('The cart fits: saves $5.98');
  await shot(page, info, 'coupon-dialog');
  await dlg.getByRole('button', { name: 'Save deal' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.getByText('Pizza (1 topping) + Side for $10.00')).toBeVisible();
  await shot(page, info, 'deals');

  const deals = (await store.me('GET', '/deals', undefined, 200)).data as Array<Record<string, any>>;
  expect(deals).toHaveLength(1);
  expect(deals[0]).toMatchObject({
    kind: 'coupon', name: 'Pizza + Side', price_cents: 1000, channel_kinds: ['counter'], card: ['Any pizza, 1 topping', 'Wings or fries'],
    parts: [{ label: 'Pizza', families: [pizza.id], qty: 1, regular_mods_included: 1 }, { label: 'Side', families: [sides.id], qty: 1, regular_mods_included: 0 }]
  });
});
