// Store › Staff (the counter terminal's people + terminal settings), as the owner uses it on a laptop and
// a phone, and the Store › Pricing card's delivery split (store share + driver share). Every step is
// checked against main's own API; what the owner saves is checked on the Pi's own pull
// (GET /store/v1/agent/terminal-config). Screenshots of each state land in the test's output folder
// (test-results/browser/…/staff-*.png; the gate copies them to artifacts/main-browser/).
import { test, expect, MAIN, type TestStore } from './fixtures';
import type { Page, TestInfo } from '@playwright/test';

async function staffList(store: TestStore) {
  return (await store.me('GET', '/staff', undefined, 200)).data as Array<Record<string, any>>;
}

async function shot(page: Page, info: TestInfo, name: string) {
  await page.screenshot({ path: info.outputPath(`staff-${name}.png`), fullPage: true, animations: 'disabled' });
}

async function fillPins(page: Page, pin: string, again = pin) {
  const dlg = page.getByRole('dialog');
  await dlg.getByLabel('PIN', { exact: true }).fill(pin);
  await dlg.getByLabel('PIN again').fill(again);
}

async function addPerson(page: Page, first: string, last: string, role: string, pin: string, again = pin) {
  await page.getByRole('button', { name: 'Add a person' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByLabel('First name').fill(first);
  await dlg.getByLabel('Last name').fill(last);
  await dlg.getByRole('radio', { name: role }).click();
  await fillPins(page, pin, again);
  await dlg.getByRole('button', { name: 'Add person' }).click();
}

const row = (page: Page, name: string) => page.getByTestId('staff-row').filter({ hasText: name });

async function more(page: Page, first: string, item: string) {
  await page.getByRole('button', { name: `More for ${first}` }).click();
  await page.getByRole('menuitem', { name: item }).click();
}

test('staff: add with a confirmed PIN, PIN conflicts in words, own limits, change PIN, deactivate, reactivate, delete', async ({
  page,
  store,
  open
}, info) => {
  await open('/store/staff');
  await expect(page.getByRole('heading', { level: 1, name: 'Staff' })).toBeVisible();
  await expect(page.getByText('No staff yet')).toBeVisible();
  await expect(page.getByTestId('pi-status')).toContainText('No Pi linked');
  await shot(page, info, 'empty');

  // The dialog explains the role with the store's own limits (the new defaults: 20 % discounts).
  await page.getByRole('button', { name: 'Add a person' }).click();
  const dlg = page.getByRole('dialog');
  await expect(dlg.getByLabel('First name')).toBeFocused();
  await expect(dlg.getByTestId('role-blurb')).toContainText('discounts up to 20%');
  await dlg.getByRole('radio', { name: 'Manager' }).click();
  await expect(dlg.getByTestId('role-blurb')).toContainText('no limits');
  await dlg.getByRole('radio', { name: 'Cashier' }).click();
  // A PIN takes digits only, at most 4.
  await dlg.getByLabel('PIN', { exact: true }).pressSequentially('12ab345');
  await expect(dlg.getByLabel('PIN', { exact: true })).toHaveValue('1234');
  await expect(dlg.getByLabel('PIN', { exact: true })).toHaveAttribute('type', 'password');
  await dlg.getByLabel('First name').fill('Cara');
  await dlg.getByLabel('Last name').fill('Smith');
  await dlg.getByLabel('PIN again').fill('1243');
  await shot(page, info, 'add-dialog');
  await dlg.getByRole('button', { name: 'Add person' }).click();
  await expect(dlg.getByRole('alert')).toHaveText('The two PINs don’t match.');
  expect(await staffList(store)).toHaveLength(0);
  await dlg.getByLabel('PIN again').fill('1234');
  await dlg.getByRole('button', { name: 'Add person' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(row(page, 'Cara Smith')).toBeVisible();
  await expect(row(page, 'Cara Smith')).toContainText('Cashier');
  await expect(row(page, 'Cara Smith')).toContainText('Refunds up to $20.00 · discounts up to 20%');
  let list = await staffList(store);
  expect(list).toHaveLength(1);
  expect(list[0]).toMatchObject({ first_name: 'Cara', last_name: 'Smith', role: 'cashier', active: true });
  expect(list[0].effective_limits).toEqual({ refund_cents: 2000, discount_pct: 20 });
  expect(JSON.stringify(list[0])).not.toContain('1234'); // the PIN is never sent back

  // A manager's PIN is exactly 4 digits (the store security round): a shorter one is refused in the page.
  await addPerson(page, 'Dan', 'Lee', 'Manager', '1');
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('A manager’s PIN is exactly 4 digits.');
  await fillPins(page, '987');
  await page.getByRole('dialog').getByRole('button', { name: 'Add person' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('A manager’s PIN is exactly 4 digits.');
  // Same PIN as an active colleague → said in words, on the PIN.
  await fillPins(page, '1234');
  await page.getByRole('dialog').getByRole('button', { name: 'Add person' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('Someone else here already uses that PIN. Pick a different one.');
  await fillPins(page, '9876');
  await page.getByRole('dialog').getByRole('button', { name: 'Add person' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect.poll(async () => (await staffList(store)).length).toBe(2);
  await expect(row(page, 'Dan Lee')).toContainText('No limits');
  await expect(page.getByText('2 active')).toBeVisible();

  // Edit Cara: her own limits ($25 refunds, 15 % discounts); the role's show as placeholders.
  await row(page, 'Cara Smith').getByRole('button', { name: 'Edit Cara' }).click();
  await expect(page.getByRole('dialog').getByLabel('PIN', { exact: true })).toHaveCount(0); // the PIN has its own button
  await page.getByRole('dialog').getByRole('switch', { name: 'Own limits for this person' }).click();
  await expect(page.getByRole('dialog').getByLabel('Discounts up to')).toHaveAttribute('placeholder', '20');
  await page.getByRole('dialog').getByLabel('Refunds up to').fill('25');
  await page.getByRole('dialog').getByLabel('Discounts up to').fill('15');
  await shot(page, info, 'edit-dialog');
  await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(row(page, 'Cara Smith')).toContainText('Refunds up to $25.00 · discounts up to 15%');
  await expect(row(page, 'Cara Smith')).toContainText('own');
  list = await staffList(store);
  const c = list.find((p) => p.first_name === 'Cara')!;
  expect(c.limits).toEqual({ refund_cents: 2500, discount_pct: 15 });
  expect(c.effective_limits).toEqual({ refund_cents: 2500, discount_pct: 15 });

  // Change Cara's PIN: confirmed, not a colleague's; afterwards her old one is free and the new one taken.
  await row(page, 'Cara Smith').getByRole('button', { name: 'Change Cara’s PIN' }).click();
  await expect(page.getByRole('dialog').getByRole('heading', { name: 'New PIN for Cara' })).toBeVisible();
  await fillPins(page, '9876');
  await page.getByRole('dialog').getByRole('button', { name: 'Save PIN' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('Someone else here already uses that PIN. Pick a different one.');
  await fillPins(page, '4321', '4312');
  await page.getByRole('dialog').getByRole('button', { name: 'Save PIN' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('The two PINs don’t match.');
  await fillPins(page, '4321');
  await page.getByRole('dialog').getByRole('button', { name: 'Save PIN' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect((await store.me('POST', '/staff', { first_name: 'Probe', role: 'cashier', pin: '4321' })).status).toBe(409);
  const probe = (await store.me('POST', '/staff', { first_name: 'Probe', role: 'cashier', pin: '1234' }, 201)).data;
  await store.me('DELETE', `/staff/${probe.id}`, undefined, 204);
  await page.reload();
  await expect(page.getByTestId('staff-row')).toHaveCount(2);

  // Deactivate (from the ⋯ menu, confirmed), then reactivate: a new PIN is required.
  await more(page, 'Cara', 'Deactivate');
  await page.getByRole('dialog').getByRole('button', { name: 'Deactivate' }).click();
  await expect(row(page, 'Cara Smith')).toContainText('Inactive');
  await expect(row(page, 'Cara Smith')).toContainText('Can’t log in');
  await expect(page.getByText('1 active · 1 inactive')).toBeVisible();
  expect((await staffList(store)).find((p) => p.first_name === 'Cara')!.active).toBe(false);
  await shot(page, info, 'list');
  await row(page, 'Cara Smith').getByRole('button', { name: 'Reactivate Cara' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Reactivate' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('A PIN is 2 to 4 digits.');
  await fillPins(page, '9876'); // Dan's
  await page.getByRole('dialog').getByRole('button', { name: 'Reactivate' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('Someone else here already uses that PIN. Pick a different one.');
  await fillPins(page, '55');
  await page.getByRole('dialog').getByRole('button', { name: 'Reactivate' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(row(page, 'Cara Smith')).not.toContainText('Inactive');
  expect((await staffList(store)).find((p) => p.first_name === 'Cara')!.active).toBe(true);

  // Delete Dan (⋯ menu, confirmed).
  await more(page, 'Dan', 'Delete');
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(row(page, 'Dan Lee')).toHaveCount(0);
  expect((await staffList(store)).map((p) => p.first_name)).toEqual(['Cara']);
});

test('terminal settings: the defaults, plain labels with units, every field saves, a bad value is marked on its field', async ({
  page,
  store,
  open
}, info) => {
  await open('/store/staff');
  const field = (label: string) => page.getByLabel(label, { exact: false }).first();
  const bar = page.getByTestId('settings-savebar');
  // Every store starts with the owner's defaults (2026-11-12).
  await expect(field('Refunds up to')).toHaveValue('20.00');
  await expect(field('Discounts up to')).toHaveValue('20');
  await expect(field('Lower an item’s price by up to')).toHaveValue('20');
  await expect(field('Lock the terminal after')).toHaveValue('5');
  await expect(field('Employee meal')).toHaveValue('20');
  await expect(field('Pay out of the drawer up to')).toHaveValue('20.00');
  await expect(field('Pickups are ready in')).toHaveValue('15');
  await expect(field('Local area codes')).toHaveValue('410, 443, 667');
  await expect(field('Counter orders on Quibase over')).toHaveValue('5.00'); // the owner's X, 2026-11-13
  await expect(field('Drivers may claim after')).toHaveCount(0); // retired 2026-11-12: past the promised time
  await expect(page.getByLabel('After login, open')).toHaveValue('info');
  await expect(page.getByLabel('Menu button font')).toHaveValue('barlow');
  await expect(bar).toContainText('All changes saved');
  await shot(page, info, 'settings');

  // A change shows as unsaved; Undo puts it back.
  await field('Discounts up to').fill('12');
  await expect(bar).toContainText('Unsaved changes');
  await bar.getByRole('button', { name: 'Undo changes' }).click();
  await expect(field('Discounts up to')).toHaveValue('20');
  await expect(bar).toContainText('All changes saved');

  await field('Refunds up to').fill('25');
  await field('Discounts up to').fill('12');
  await field('Lower an item’s price by up to').fill('25');
  await page.getByLabel('After login, open').selectOption('takeout');
  await page.getByLabel('Menu button font').selectOption('barlow_condensed');
  await field('Lock the terminal after').fill('7');
  await field('Close the receipt screen after').fill('0');
  await page.getByRole('switch', { name: 'Show the History tab' }).click();
  await page.getByRole('switch', { name: 'Prices on menu buttons' }).click();
  await page.getByRole('switch', { name: 'Click sound' }).click();
  await field('Counter numbers start at').fill('100');
  await field('Employee meal').fill('40');
  await field('Most of one topping').fill('4');
  await field('Deliver past the delivery area by up to').fill('1.5');
  await page.getByRole('switch', { name: 'Ask a name for a paid counter order' }).click();
  await expect(page.getByLabel('Tip choices')).toBeDisabled();
  await page.getByRole('switch', { name: 'Ask for a tip on the card reader' }).click();
  await page.getByLabel('Tip choices').fill('10, 15, 20, 25');
  await page.getByLabel('At (store time)').fill('03:30');
  await field('Pay out of the drawer up to').fill('35');
  await field('Pickups are ready in').fill('241');
  await field('Local area codes').fill('410, 443, 410, 202');
  await field('Counter orders on Quibase over').fill('7.50');
  await bar.getByRole('button', { name: 'Save terminal settings' }).click();
  // A bad value never leaves the page: the message says which field, and that field is marked + focused.
  await expect(bar.getByRole('alert')).toContainText('Pickups are ready in: a whole number from 0 to 240.');
  await expect(field('Pickups are ready in')).toHaveAttribute('aria-invalid', 'true');
  await expect(field('Pickups are ready in')).toBeFocused();
  await shot(page, info, 'settings-error');
  await field('Pickups are ready in').fill('20');
  await field('Local area codes').fill('410, 12');
  await bar.getByRole('button', { name: 'Save terminal settings' }).click();
  await expect(bar.getByRole('alert')).toContainText('Area codes');
  await expect(field('Local area codes')).toBeFocused();
  await field('Local area codes').fill('410, 443, 410, 202');
  await field('Lock the terminal after').fill('0');
  await bar.getByRole('button', { name: 'Save terminal settings' }).click();
  await expect(bar.getByRole('alert')).toContainText('Lock the terminal after');
  await field('Lock the terminal after').fill('7');
  await field('A charge sent to a phone expires after').fill('240');
  await bar.getByRole('button', { name: 'Save terminal settings' }).click();
  await expect(bar).toContainText('All changes saved');

  const want = {
    cashier_refund_cents: 2500, cashier_discount_pct: 12, cashier_price_drop_pct: 25, default_tab: 'takeout',
    button_font: 'barlow_condensed', auto_lock_minutes: 7, receipt_auto_done_seconds: 0, show_history: true,
    prices_on_buttons: true, click_sound: true, counter_start: 100, employee_meal_pct: 40, out_of_area_margin_mi: 1.5,
    name_on_paid_counter: true, card_tips: true, card_tip_percents: [10, 15, 20, 25], auto_close_batch_at: '03:30',
    cashier_payout_cents: 3500, default_pickup_minutes: 20, area_codes: ['410', '443', '202'],
    topping_max_count: 4, phone_charge_timeout_seconds: 240, quibase_counter_min_cents: 750
  };
  await expect.poll(async () => (await store.me('GET', '/terminal/settings', undefined, 200)).data).toMatchObject(want);
  await expect(field('Local area codes')).toHaveValue('410, 443, 202'); // as main saved it

  // Auto-close off saves "".
  await page.getByRole('switch', { name: 'Close the batch by itself' }).click();
  await bar.getByRole('button', { name: 'Save terminal settings' }).click();
  await expect.poll(async () => (await store.me('GET', '/terminal/settings', undefined, 200)).data.auto_close_batch_at).toBe('');

  // A reload shows what main has.
  await expect(bar).toContainText('All changes saved');
  await page.reload();
  await expect(page.getByLabel('After login, open')).toHaveValue('takeout');
  await expect(field('Deliver past the delivery area by up to')).toHaveValue('1.5');

  // Fill in defaults (confirmed) → the owner's defaults, saved on Save.
  await page.getByRole('button', { name: 'Fill in defaults' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Fill in defaults' }).click();
  await expect(field('Discounts up to')).toHaveValue('20');
  await expect(bar).toContainText('Unsaved changes');
  await bar.getByRole('button', { name: 'Save terminal settings' }).click();
  await expect
    .poll(async () => (await store.me('GET', '/terminal/settings', undefined, 200)).data)
    .toMatchObject({ cashier_discount_pct: 20, employee_meal_pct: 20, auto_lock_minutes: 5, cashier_price_drop_pct: 20,
      default_tab: 'info', auto_close_batch_at: '04:00', cashier_refund_cents: 2000 });
});

test('what the owner saves reaches the Pi: terminal-config carries the staff and settings, its version moves', async ({
  page,
  store,
  open
}) => {
  const dev = (await store.adminCall('POST', '/devices', { location_id: store.locationId, name: 'test-pi' }, 201)).data;
  const pull = async () => {
    const r = await fetch(`${MAIN}/store/v1/agent/terminal-config`, { headers: { 'X-STORE-API-KEY': dev.api_key } });
    expect(r.status).toBe(200);
    return (await r.json()) as Record<string, any>;
  };
  const before = await pull();
  expect(before.settings).toMatchObject({ cashier_discount_pct: 20, employee_meal_pct: 20, auto_lock_minutes: 5 });

  await open('/store/staff');
  // A Pi is linked but has no stream: the page says the terminals get changes when it's back.
  await expect(page.getByTestId('pi-status')).toContainText('Pi offline');
  await addPerson(page, 'Eve', 'Ray', 'Driver', '2468');
  await expect(row(page, 'Eve Ray')).toBeVisible();
  await page.getByLabel('Employee meal', { exact: false }).first().fill('30');
  await page.getByTestId('settings-savebar').getByRole('button', { name: 'Save terminal settings' }).click();
  await expect(page.getByText('Terminal settings saved — the terminals get them when the Pi is back online')).toBeVisible();

  const after = await pull();
  expect(after.version).toBeGreaterThan(before.version);
  expect(after.settings.employee_meal_pct).toBe(30);
  expect(after.staff).toHaveLength(1);
  expect(after.staff[0]).toMatchObject({ first_name: 'Eve', last_name: 'Ray', role: 'driver' });
  expect(after.staff[0].pin_hash).toMatch(/^\$2a\$/);
});

test('pricing: the delivery fee is a store share + a driver share', async ({ page, store, open }) => {
  await open('/store/details');
  // A new store delivers for $4: $1 the store's + $3 the driver's (the owner's default).
  await expect(page.getByLabel('Delivery: store share ($)').first()).toHaveValue('1.00');
  await expect(page.getByLabel('Delivery: driver share ($)').first()).toHaveValue('3.00');
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
