// Store › Staff (the counter terminal's people), as the owner uses it on a laptop and a phone (the
// terminal settings moved to Store › Settings: settings.spec.ts). Every step is
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
  // The terminal settings live in Store › Settings now (one page per group).
  await open('/store/settings/counter');
  await expect(page.getByTestId('pi-status')).toContainText('Pi offline');
  await page.getByLabel('Employee meal', { exact: false }).first().fill('30');
  await page.getByTestId('settings-savebar').getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Counter orders saved — the terminals get them when the Pi is back online')).toBeVisible();

  const after = await pull();
  expect(after.version).toBeGreaterThan(before.version);
  expect(after.settings.employee_meal_pct).toBe(30);
  expect(after.staff).toHaveLength(1);
  expect(after.staff[0]).toMatchObject({ first_name: 'Eve', last_name: 'Ray', role: 'driver' });
  expect(after.staff[0].pin_hash).toMatch(/^\$2a\$/);
});
