// Store › Staff (the counter terminal's people + terminal settings) and the Store › Pricing card's
// delivery split (store share + driver share). Every step is checked against main's own API.
import { test, expect, type TestStore } from './fixtures';
import type { Page } from '@playwright/test';

async function staffList(store: TestStore) {
  return (await store.me('GET', '/staff', undefined, 200)).data as Array<Record<string, any>>;
}

async function addPerson(page: Page, first: string, last: string, role: string, pin: string) {
  await page.getByRole('button', { name: 'Add a person' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByLabel('First name').fill(first);
  await dlg.getByLabel('Last name').fill(last);
  await dlg.getByRole('radio', { name: role }).click();
  await dlg.getByLabel(/PIN/).fill(pin);
  await dlg.getByRole('button', { name: 'Add', exact: true }).click();
}

test('staff: add, PIN conflict, edit limits, deactivate and reactivate with a new PIN, delete', async ({ page, store, open }) => {
  await open('/store/staff');
  await expect(page.getByRole('heading', { level: 1, name: 'Staff' })).toBeVisible();

  await addPerson(page, 'Cara', 'Smith', 'Cashier', '1234');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.getByTestId('staff-row').filter({ hasText: 'Cara Smith' })).toBeVisible();
  let list = await staffList(store);
  expect(list).toHaveLength(1);
  expect(list[0]).toMatchObject({ first_name: 'Cara', last_name: 'Smith', role: 'cashier', active: true });
  expect(JSON.stringify(list[0])).not.toContain('1234'); // the PIN is never sent back

  // Same PIN for a second active person → pin_taken, shown on the PIN field.
  await addPerson(page, 'Dan', 'Lee', 'Manager', '1234');
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('Another active person has that PIN.');
  await page.getByRole('dialog').getByLabel(/PIN/).fill('9876');
  await page.getByRole('dialog').getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect.poll(async () => (await staffList(store)).length).toBe(2);

  // Edit Cara: her own limits ($25 refund, 15 % discount).
  const cara = page.getByTestId('staff-row').filter({ hasText: 'Cara Smith' });
  await cara.getByRole('button', { name: 'Edit' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByLabel('Refund limit ($)').fill('25');
  await dlg.getByLabel('Discount limit (%)').fill('15');
  await dlg.getByRole('button', { name: 'Save' }).click();
  await expect(dlg).toHaveCount(0);
  await expect(cara).toContainText('$25.00');
  list = await staffList(store);
  const c = list.find((p) => p.first_name === 'Cara')!;
  expect(c.limits).toEqual({ refund_cents: 2500, discount_pct: 15 });
  expect(c.effective_limits).toEqual({ refund_cents: 2500, discount_pct: 15 });

  // Deactivate (confirm), then reactivate: a new PIN is required.
  await cara.getByRole('button', { name: 'Deactivate' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Deactivate' }).click();
  await expect(cara).toContainText('Inactive');
  expect((await staffList(store)).find((p) => p.first_name === 'Cara')!.active).toBe(false);
  await cara.getByRole('button', { name: 'Reactivate' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Reactivate' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('The PIN must be 2–4 digits.');
  await page.getByRole('dialog').getByLabel(/PIN/).fill('9876'); // Dan's
  await page.getByRole('dialog').getByRole('button', { name: 'Reactivate' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('Another active person has that PIN.');
  await page.getByRole('dialog').getByLabel(/PIN/).fill('55');
  await page.getByRole('dialog').getByRole('button', { name: 'Reactivate' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(cara).not.toContainText('Inactive');
  expect((await staffList(store)).find((p) => p.first_name === 'Cara')!.active).toBe(true);

  // Delete Dan.
  const dan = page.getByTestId('staff-row').filter({ hasText: 'Dan Lee' });
  await dan.getByRole('button', { name: 'Delete Dan' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(dan).toHaveCount(0);
  expect((await staffList(store)).map((p) => p.first_name)).toEqual(['Cara']);
});

test('terminal settings: every field saves, a bad value is refused in the page', async ({ page, store, open }) => {
  await open('/store/staff');
  await expect(page.getByLabel('Refund up to ($)')).toHaveValue('0.00');
  await expect(page.getByLabel('After login, open')).toHaveValue('info');
  await expect(page.getByLabel('Button font')).toHaveValue('barlow');

  await page.getByLabel('Refund up to ($)').fill('20');
  await page.getByLabel('Discount up to (%)').fill('12');
  await page.getByLabel('Lower a price by up to (%)').fill('25');
  await page.getByLabel('After login, open').selectOption('takeout');
  await page.getByLabel('Button font').selectOption('barlow_condensed');
  await page.getByLabel('Lock after idle (min)').fill('7');
  await page.getByLabel('Receipt screen closes after (s)').fill('0');
  await page.getByRole('switch', { name: 'Show the History tab' }).click();
  await page.getByRole('switch', { name: 'Prices on menu buttons' }).click();
  await page.getByRole('switch', { name: 'Click sound' }).click();
  await page.getByLabel('Counter numbers start at').fill('100');
  await page.getByLabel('Employee meal (% off)').fill('40');
  await page.getByLabel('Out-of-area margin (mi)').fill('1.5');
  await page.getByRole('switch', { name: 'Ask a name for a paid counter order' }).click();
  await page.getByRole('switch', { name: 'Ask for a tip on the card reader' }).click();
  await page.getByLabel('Choices (%)').fill('10, 15, 20, 25');
  await page.getByLabel('At (store time)').fill('03:30');

  // A bad value never leaves the page.
  await page.getByLabel('Lock after idle (min)').fill('0');
  await page.getByRole('button', { name: 'Save terminal settings' }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'Auto-lock' })).toBeVisible();
  await page.getByLabel('Lock after idle (min)').fill('7');
  await page.getByRole('button', { name: 'Save terminal settings' }).click();

  await expect
    .poll(async () => (await store.me('GET', '/terminal/settings', undefined, 200)).data)
    .toMatchObject({
      cashier_refund_cents: 2000, cashier_discount_pct: 12, cashier_price_drop_pct: 25, default_tab: 'takeout',
      button_font: 'barlow_condensed', auto_lock_minutes: 7, receipt_auto_done_seconds: 0, show_history: true,
      prices_on_buttons: true, click_sound: true, counter_start: 100, employee_meal_pct: 40, out_of_area_margin_mi: 1.5,
      name_on_paid_counter: true, card_tips: true, card_tip_percents: [10, 15, 20, 25], auto_close_batch_at: '03:30'
    });

  // Auto-close off saves "".
  await page.getByRole('switch', { name: 'Close the batch by itself' }).click();
  await page.getByRole('button', { name: 'Save terminal settings' }).click();
  await expect.poll(async () => (await store.me('GET', '/terminal/settings', undefined, 200)).data.auto_close_batch_at).toBe('');

  // A reload shows what main has.
  await page.reload();
  await expect(page.getByLabel('After login, open')).toHaveValue('takeout');
  await expect(page.getByLabel('Out-of-area margin (mi)')).toHaveValue('1.5');
});

test('pricing: the delivery fee is a store share + a driver share', async ({ page, store, open }) => {
  await open('/store/details');
  const st = page.getByLabel('Delivery: store share ($)').first();
  const dr = page.getByLabel('Delivery: driver share ($)').first();
  await st.fill('1.00');
  await dr.fill('3.50');
  await expect(page.getByTestId('pr-st-del-total')).toHaveText('$4.50');
  await page.getByRole('button', { name: 'Save pricing' }).click();
  await expect
    .poll(async () => (await store.me('GET', '/pricing', undefined, 200)).data.store.fees)
    .toMatchObject({ delivery_fee_cents: 450, delivery_driver_cents: 350 });

  await page.reload();
  await expect(page.getByLabel('Delivery: store share ($)').first()).toHaveValue('1.00');
  await expect(page.getByLabel('Delivery: driver share ($)').first()).toHaveValue('3.50');
});
