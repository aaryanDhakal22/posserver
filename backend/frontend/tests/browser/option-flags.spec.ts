// Store › Menu › Modifier groups: each option's "Show on driver ticket" and "Counts for coupons"
// switches (MENU-PLAN, the owner 2026-11-12), on a laptop and a phone. Every step is checked against
// main's own API (GET …/modifier-groups/:id/options, /me/menu/bank, /me/menu/families); screenshots
// land in the test's output folder (option-flags-*.png).
import { test, expect, type TestStore } from './fixtures';
import type { Page, TestInfo } from '@playwright/test';

type Opt = { id: string; name: string; show_on_driver_ticket: boolean; counts_for_coupons: boolean };
type Listing = {
  families: Array<{ id: string; kind: string; name: string; class: string }>;
  options: Array<{ id: string; name: string; family_id: string | null }>;
};

async function shot(page: Page, info: TestInfo, name: string) {
  await page.screenshot({ path: info.outputPath(`option-flags-${name}.png`), fullPage: true, animations: 'disabled' });
}

/** A Toppings group: Pepperoni in the regular "Pizza Topping" family, Steak and Coke without one. */
async function toppings(store: TestStore): Promise<string> {
  await store.me('POST', '/menu/bank/apply', {
    format: 1, sizes: [], menus: [], views: [], deals: [],
    families: [{ name: 'Pizza Topping', kind: 'mod', class: 'regular' }],
    groups: [{ name: 'Toppings', max_select: 0, allow_multiple: true, options: [
      { name: 'Pepperoni', price_cents: 150, family: 'Pizza Topping' },
      { name: 'Steak', price_cents: 300 },
      { name: 'Coke', price_cents: 0 }] }]
  }, 200);
  const groups = (await store.me('GET', '/menu/modifier-groups', undefined, 200)).data as Array<{ id: string; name: string }>;
  return groups.find((g) => g.name === 'Toppings')!.id;
}

async function options(store: TestStore, gid: string): Promise<Record<string, Opt>> {
  const rows = (await store.me('GET', `/menu/modifier-groups/${gid}/options`, undefined, 200)).data as Opt[];
  return Object.fromEntries(rows.map((o) => [o.name, o]));
}

async function familyOf(store: TestStore, option: string) {
  const l = (await store.me('GET', '/menu/families', undefined, 200)).data as Listing;
  const o = l.options.find((x) => x.name === option)!;
  return l.families.find((f) => f.id === o.family_id) ?? null;
}

const sw = (page: Page, name: string) => page.getByRole('switch', { name, exact: true });

test('option switches: show on driver ticket + counts for coupons, saved in main', async ({ page, store, open }, info) => {
  const gid = await toppings(store);
  await open('/store/menu');
  await page.getByRole('tab', { name: 'Modifier groups' }).click();
  await page.getByRole('button', { name: 'Toppings', exact: true }).click();
  await expect(page.getByTestId('option-row')).toHaveCount(3);
  await expect(page.getByText("Notes stay off the driver's copy.")).toBeVisible();
  // What main says is what the switches show.
  await expect(sw(page, 'Pepperoni counts for coupons')).toHaveAttribute('aria-checked', 'true');
  await expect(sw(page, 'Steak counts for coupons')).toHaveAttribute('aria-checked', 'false');
  await expect(sw(page, 'Show Coke on driver ticket')).toHaveAttribute('aria-checked', 'false');
  await shot(page, info, 'start');

  // Show on driver ticket: Coke on.
  await sw(page, 'Show Coke on driver ticket').click();
  await expect(sw(page, 'Show Coke on driver ticket')).toHaveAttribute('aria-checked', 'true');
  await expect.poll(async () => (await options(store, gid)).Coke.show_on_driver_ticket).toBe(true);
  const bank = (await store.me('GET', '/menu/bank', undefined, 200)).data;
  const coke = bank.groups[gid].options.find((o: Opt) => o.name === 'Coke');
  expect(coke.show_on_driver_ticket).toBe(true);
  expect((await options(store, gid)).Steak.show_on_driver_ticket).toBe(false);

  // Counts for coupons on for Steak: it joins the group's regular family.
  await sw(page, 'Steak counts for coupons').click();
  await expect.poll(async () => (await options(store, gid)).Steak.counts_for_coupons).toBe(true);
  expect((await familyOf(store, 'Steak'))!.name).toBe('Pizza Topping');

  // …and off for Pepperoni: a premium family ("Premium Topping") is made for it.
  await sw(page, 'Pepperoni counts for coupons').click();
  await expect.poll(async () => (await options(store, gid)).Pepperoni.counts_for_coupons).toBe(false);
  const prem = (await familyOf(store, 'Pepperoni'))!;
  expect(prem.name).toBe('Premium Topping');
  expect(prem.class).toBe('premium');

  // A new option in a topping group counts for coupons by default.
  await expect(sw(page, 'New option counts for coupons')).toHaveAttribute('aria-checked', 'true');
  await page.getByLabel('New option name').fill('Bacon');
  await page.getByLabel('New option price change ($)').fill('1.25');
  await page.getByRole('button', { name: 'Add option' }).click();
  await expect(page.getByTestId('option-row')).toHaveCount(4);
  const after = await options(store, gid);
  expect(after.Bacon.counts_for_coupons).toBe(true);
  expect((await familyOf(store, 'Bacon'))!.name).toBe('Pizza Topping');
  await shot(page, info, 'changed');

  // A reload reads every switch back from main.
  await page.reload();
  await page.getByRole('tab', { name: 'Modifier groups' }).click();
  await page.getByRole('button', { name: 'Toppings', exact: true }).click();
  await expect(sw(page, 'Show Coke on driver ticket')).toHaveAttribute('aria-checked', 'true');
  await expect(sw(page, 'Steak counts for coupons')).toHaveAttribute('aria-checked', 'true');
  await expect(sw(page, 'Pepperoni counts for coupons')).toHaveAttribute('aria-checked', 'false');
  await expect(sw(page, 'Bacon counts for coupons')).toHaveAttribute('aria-checked', 'true');

  // Driver ticket off again.
  await sw(page, 'Show Coke on driver ticket').click();
  await expect.poll(async () => (await options(store, gid)).Coke.show_on_driver_ticket).toBe(false);
});
