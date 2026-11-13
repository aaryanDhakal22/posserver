// Store › Settings: the calm grid of category tiles and the short page each opens (one Save in a bar
// that follows the screen while something is unsaved, Undo, a guard when leaving unsaved). Every
// save is checked against main's own API. Screenshots of each page land in the test's output folder
// (test-results/browser/…/settings-*.png; the gate copies them to artifacts/main-browser/).
import { test, expect } from './fixtures';
import type { Page, TestInfo } from '@playwright/test';

async function shot(page: Page, info: TestInfo, name: string) {
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: info.outputPath(`settings-${name}.png`), fullPage: true, animations: 'disabled' });
}

const bar = (page: Page) => page.getByTestId('settings-savebar');
const save = (page: Page) => bar(page).getByRole('button', { name: 'Save', exact: true });
const field = (page: Page, label: string) => page.getByLabel(label, { exact: false }).first();

const TILES: [string, string][] = [
  ['details', 'Store details'],
  ['hours', 'Hours'],
  ['map', 'Map and delivery area'],
  ['alerts', 'Alerts'],
  ['ordering', 'Ordering'],
  ['tax', 'Tax and deals'],
  ['fees', 'Fees and tips'],
  ['cashier', 'Cashier limits'],
  ['counter', 'Counter orders'],
  ['screen', 'Login and screen'],
  ['card', 'Card payments'],
  ['voids', 'Void and waste reasons'],
  ['end-of-day', 'End of day'],
  ['callcenter', 'Call center']
];

test('the tiles: every category with a one-line summary opens its own page; the old Details & hours link lands here', async ({
  page,
  store,
  open
}, info) => {
  await open('/store/details');
  await expect(page).toHaveURL(/\/app\/store\/settings$/);
  await expect(page.getByRole('heading', { level: 1, name: 'Settings' })).toBeVisible();
  await expect(page.locator('a.nav[aria-current="page"]').first()).toHaveText('Settings'); // the sidebar (hidden on a phone)
  // Summaries come from the store's own settings.
  await expect(page.getByTestId('tile-cashier')).toContainText('Refunds $20.00 · discounts 20%');
  await expect(page.getByTestId('tile-ordering')).toContainText(/pickup/i);
  await expect(page.getByTestId('tile-fees')).toContainText('Delivery $4.00');
  await expect(page.getByTestId('tile-printers')).toContainText('1 printer');
  await expect(page.getByTestId('tile-staff')).toContainText('Nobody yet');
  await expect(page.getByTestId('tile-payments')).toHaveAttribute('href', '/app/store/payments');
  await shot(page, info, 'tiles');
  for (const [slug, title] of TILES) {
    await page.getByTestId(`tile-${slug}`).click();
    await expect(page).toHaveURL(new RegExp(`/store/settings/${slug}$`));
    await expect(page.getByRole('heading', { level: 1, name: title })).toBeVisible();
    // The breadcrumb leads back to the tiles.
    await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Settings' }).click();
    await expect(page.getByTestId('tile-details')).toBeVisible();
  }
  void store;
});

test('terminal settings: one group per page, the defaults, every field saves, a bad value is marked on its field', async ({
  page,
  store,
  open
}, info) => {
  const saved = async () => (await store.me('GET', '/terminal/settings', undefined, 200)).data;

  // Cashier limits.
  await open('/store/settings/cashier');
  await expect(field(page, 'Refunds up to')).toHaveValue('20.00');
  await expect(field(page, 'Discounts up to')).toHaveValue('20');
  await expect(field(page, 'Lower an item’s price by up to')).toHaveValue('20');
  await expect(field(page, 'Pay out of the drawer up to')).toHaveValue('20.00');
  await expect(bar(page)).toContainText('All changes saved');
  await expect(save(page)).toBeDisabled();
  await shot(page, info, 'cashier');
  // A change shows as unsaved; Undo puts it back.
  await field(page, 'Discounts up to').fill('12');
  await expect(bar(page)).toContainText('Unsaved changes');
  await bar(page).getByRole('button', { name: 'Undo changes' }).click();
  await expect(field(page, 'Discounts up to')).toHaveValue('20');
  // Leaving with unsaved changes asks; Stay keeps them.
  await field(page, 'Discounts up to').fill('12');
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Settings' }).click();
  await expect(page.getByRole('dialog')).toContainText('Leave without saving?');
  await page.getByRole('dialog').getByRole('button', { name: 'Stay' }).click();
  await expect(field(page, 'Discounts up to')).toHaveValue('12');
  await field(page, 'Refunds up to').fill('25');
  await field(page, 'Lower an item’s price by up to').fill('25');
  await field(page, 'Pay out of the drawer up to').fill('35');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(saved).toMatchObject({ cashier_refund_cents: 2500, cashier_discount_pct: 12, cashier_price_drop_pct: 25, cashier_payout_cents: 3500 });

  // Login and screen.
  await open('/store/settings/screen');
  await expect(page.getByLabel('After login, open')).toHaveValue('info');
  await expect(page.getByLabel('Menu button font')).toHaveValue('barlow');
  await field(page, 'Lock the terminal after').fill('0');
  await save(page).click();
  // A bad value never leaves the page: the message says which field, and that field is marked + focused.
  await expect(bar(page).getByRole('alert')).toContainText('Lock the terminal after: a whole number from 1 to 240.');
  await expect(field(page, 'Lock the terminal after')).toHaveAttribute('aria-invalid', 'true');
  await expect(field(page, 'Lock the terminal after')).toBeFocused();
  await shot(page, info, 'screen-error');
  await field(page, 'Lock the terminal after').fill('7');
  await page.getByLabel('After login, open').selectOption('takeout');
  await page.getByLabel('Menu button font').selectOption('barlow_condensed');
  await field(page, 'Close the receipt screen after').fill('0');
  await page.getByRole('switch', { name: 'Show the History tab' }).click();
  await page.getByRole('switch', { name: 'Prices on menu buttons' }).click();
  await page.getByRole('switch', { name: 'Click sound' }).click();
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(saved).toMatchObject({ auto_lock_minutes: 7, default_tab: 'takeout', button_font: 'barlow_condensed',
    receipt_auto_done_seconds: 0, show_history: true, prices_on_buttons: true, click_sound: true });
  // What the cashier page saved stayed (each page sends the whole settings back).
  expect(await saved()).toMatchObject({ cashier_discount_pct: 12, cashier_refund_cents: 2500 });

  // Counter orders.
  await open('/store/settings/counter');
  await expect(field(page, 'Employee meal')).toHaveValue('20');
  await expect(field(page, 'Pickups are ready in')).toHaveValue('15');
  await expect(field(page, 'Local area codes')).toHaveValue('410, 443, 667');
  await expect(field(page, 'Counter orders on Quibase over')).toHaveValue('5.00'); // the owner's X, 2026-11-13
  await field(page, 'Pickups are ready in').fill('241');
  await save(page).click();
  await expect(bar(page).getByRole('alert')).toContainText('Pickups are ready in: a whole number from 0 to 240.');
  await expect(field(page, 'Pickups are ready in')).toBeFocused();
  await field(page, 'Pickups are ready in').fill('20');
  await field(page, 'Local area codes').fill('410, 12');
  await save(page).click();
  await expect(bar(page).getByRole('alert')).toContainText('Area codes');
  await expect(field(page, 'Local area codes')).toBeFocused();
  await field(page, 'Local area codes').fill('410, 443, 410, 202');
  await field(page, 'Counter numbers start at').fill('100');
  await field(page, 'Employee meal').fill('40');
  await field(page, 'Most of one topping').fill('4');
  await field(page, 'Counter orders on Quibase over').fill('7.50');
  await field(page, 'Deliver past the delivery area by up to').fill('1.5');
  await page.getByRole('switch', { name: 'Ask a name for a paid counter order' }).click();
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(saved).toMatchObject({ default_pickup_minutes: 20, area_codes: ['410', '443', '202'], counter_start: 100,
    employee_meal_pct: 40, topping_max_count: 4, quibase_counter_min_cents: 750, out_of_area_margin_mi: 1.5, name_on_paid_counter: true });
  await expect(field(page, 'Local area codes')).toHaveValue('410, 443, 202'); // as main saved it
  await shot(page, info, 'counter');

  // Card payments.
  await open('/store/settings/card');
  await expect(page.getByLabel('Tip choices')).toBeDisabled();
  await page.getByRole('switch', { name: 'Ask for a tip on the card reader' }).click();
  await page.getByLabel('Tip choices').fill('10, 15, 20, 25');
  await field(page, 'A charge sent to a phone expires after').fill('240');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(saved).toMatchObject({ card_tips: true, card_tip_percents: [10, 15, 20, 25], phone_charge_timeout_seconds: 240 });

  // Void and waste reasons.
  await open('/store/settings/voids');
  await field(page, 'Void reasons').fill('Customer cancelled\nWrong order\ncustomer cancelled\nOther');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(async () => (await saved()).void_reasons).toEqual(['Customer cancelled', 'Wrong order', 'Other']);

  // End of day: auto-close at 03:30, then off ("").
  await open('/store/settings/end-of-day');
  await page.getByLabel('At (store time)').fill('03:30');
  await field(page, 'Cash left in each drawer at opening').fill('150');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(saved).toMatchObject({ auto_close_batch_at: '03:30', drawer_start_cents: 15000 });
  await page.getByRole('switch', { name: 'Close the batch by itself' }).click();
  await save(page).click();
  await expect.poll(async () => (await saved()).auto_close_batch_at).toBe('');
  await shot(page, info, 'end-of-day');

  // Fill in defaults touches only this page's fields (saved on Save).
  await open('/store/settings/cashier');
  await page.getByRole('button', { name: 'Fill in defaults' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Fill in defaults' }).click();
  await expect(field(page, 'Discounts up to')).toHaveValue('20');
  await expect(bar(page)).toContainText('Unsaved changes');
  await save(page).click();
  await expect.poll(saved).toMatchObject({ cashier_discount_pct: 20, cashier_refund_cents: 2000, employee_meal_pct: 40, auto_lock_minutes: 7 });
});

test('store details, hours, alerts: each saves its own part of the location; a bad value is said in words', async ({ page, store, open }, info) => {
  const loc = async () => (await store.me('GET', '/location', undefined, 200)).data;

  await open('/store/settings/details');
  await field(page, 'Phone').fill('410-555-0100');
  await field(page, 'Store email').fill('not-an-email');
  await expect(page.getByRole('alert').first()).toContainText("That isn't an email address.");
  await save(page).click();
  await expect(bar(page).getByRole('alert')).toContainText('Store email');
  await field(page, 'Store email').fill('owner@example.com');
  await page.getByLabel('New day starts at').selectOption('4');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(loc).toMatchObject({ phone: '410-555-0100', daily_reset_hour: 4 });
  expect((await store.me('GET', '/site/contact', undefined, 200)).data.reply_email).toBe('owner@example.com');
  await shot(page, info, 'details');

  await open('/store/settings/hours');
  await page.getByRole('checkbox', { name: 'Monday' }).check();
  await page.getByLabel('Monday opens').fill('10:30');
  await page.getByLabel('Monday closes').fill('22:00');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(async () => (await loc()).config.opening_hours?.mon).toEqual([{ open: '10:30', close: '22:00' }]);
  // The phone and day saved above stayed (the PATCH is a full replace).
  expect(await loc()).toMatchObject({ phone: '410-555-0100', daily_reset_hour: 4 });
  // A special day saves on its own.
  await page.getByLabel('Date').fill('2026-12-25');
  await page.getByLabel('Note').fill('Christmas');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.getByText('Christmas')).toBeVisible();
  await shot(page, info, 'hours');

  await open('/store/settings/alerts');
  await field(page, 'Pushover device names').fill('Owner Phone');
  await expect(page.getByRole('alert').first()).toContainText('isn\'t a Pushover device name');
  await field(page, 'Pushover device names').fill('OwnerPhone, Tablet2');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(async () => (await loc()).config.pushover_owner_devices).toBe('OwnerPhone,Tablet2');
  expect((await loc()).config.opening_hours?.mon).toEqual([{ open: '10:30', close: '22:00' }]);
  await shot(page, info, 'alerts');

  // The tiles say what was saved.
  await open('/store/settings');
  await expect(page.getByTestId('tile-alerts')).toContainText('2 phones');
  await expect(page.getByTestId('tile-details')).toContainText('410-555-0100');
});

test('ordering, tax and deals, fees and tips: each page sends the rest of the pricing back as it was', async ({ page, store, open }, info) => {
  const pricing = async () => (await store.me('GET', '/pricing', undefined, 200)).data;

  await open('/store/settings/ordering');
  await field(page, 'Ready in').fill('25');
  await field(page, 'Online tips up to').fill('2000');
  await save(page).click();
  await expect(bar(page).getByRole('alert')).toContainText('Tip limit: a dollar amount from $0 to $1,000.');
  await field(page, 'Online tips up to').fill('60');
  await page.getByRole('switch', { name: 'Delivery' }).click();
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect
    .poll(async () => (await store.me('GET', '/ordering', undefined, 200)).data.settings)
    .toMatchObject({ pickup_eta_minutes: 25, tip_max_cents: 6000, delivery_enabled: false });
  await shot(page, info, 'ordering');

  await open('/store/settings/tax');
  await field(page, 'Sales tax').fill('7.25');
  await page.getByLabel('Deals in one order').selectOption('single');
  await page.getByLabel('A topping on half a pizza').selectOption('half');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  await expect.poll(async () => (await pricing()).store).toMatchObject({ tax_bps: 725, stacking: 'single', half_topping_price: 'half' });
  await shot(page, info, 'tax');

  // A new store delivers for $4: $1 the store's + $3 the driver's (the owner's default).
  await open('/store/settings/fees');
  await expect(page.getByLabel('Delivery: store share ($)').first()).toHaveValue('1.00');
  await expect(page.getByLabel('Delivery: driver share ($)').first()).toHaveValue('3.00');
  await page.getByLabel('Delivery: driver share ($)').first().fill('3.50');
  await expect(page.getByTestId('pr-st-del-total')).toHaveText('$4.50');
  // A channel that differs opens to its own fees.
  await page.locator('details.kind summary').filter({ hasText: 'Website' }).click();
  await page.getByRole('switch', { name: 'Website has its own fees' }).click();
  await page.locator('#pr-web-svc').fill('1.25');
  await save(page).click();
  await expect(bar(page)).toContainText('All changes saved');
  const p = await pricing();
  expect(p.store.fees).toMatchObject({ delivery_fee_cents: 450, delivery_driver_cents: 350 });
  expect(p.kinds.web.fees).toMatchObject({ service_fee_cents: 125 });
  expect(p.store).toMatchObject({ tax_bps: 725, stacking: 'single', half_topping_price: 'half' }); // tax page's save kept
  await shot(page, info, 'fees');

  await page.reload();
  await expect(page.getByLabel('Delivery: driver share ($)').first()).toHaveValue('3.50');
});
