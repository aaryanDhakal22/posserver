// Store › Staff → Call center (contract/main.md §2.5zh): the role Call center with its 4-digit PIN, the
// idle lock (a store setting), the computers the store's login opened the call-center app on (removable)
// and orders by agent. Every step is checked against main's own API.
import { test, expect, MAIN, type TestStore } from './fixtures';
import type { Page, TestInfo } from '@playwright/test';

async function shot(page: Page, info: TestInfo, name: string) {
  await page.screenshot({ path: info.outputPath(`callcenter-${name}.png`), fullPage: true, animations: 'disabled' });
}

async function cc(method: string, path: string, token = '', body?: unknown) {
  const r = await fetch(`${MAIN}/callcenter/v1${path}`, {
    method,
    headers: { 'content-type': 'application/json', ...(token ? { authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  return { status: r.status, data: r.status === 204 ? null : await r.json() };
}

const ccUser = (store: TestStore) => `cc-${store.tag}`;
const CC_PASS = 'a-Long-enough-pw';

async function login(store: TestStore, name: string): Promise<string> {
  const r = await cc('POST', '/login', '', { username: ccUser(store), password: CC_PASS, device_name: name });
  expect(r.status).toBe(200);
  return r.data.device_token as string;
}

test('call center: an agent with a 4-digit PIN, the idle lock, computers signed in and removed, orders by agent', async ({
  page,
  store,
  open
}, info) => {
  // The agents are people on Store › Staff; the call center's own card is Store › Settings › Call center.
  await open('/store/staff');
  const card = page.getByTestId('callcenter-card');

  // The role: explained, a 4-digit PIN only, no own limits.
  await page.getByRole('button', { name: 'Add a person' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByLabel('First name').fill('Cara');
  await dlg.getByLabel('Last name').fill('Smith');
  await dlg.getByRole('radio', { name: 'Call center' }).click();
  await expect(dlg.getByTestId('role-blurb')).toContainText('call-center app only');
  await expect(dlg.getByText('Own limits for this person')).toBeHidden();
  await dlg.getByLabel('PIN', { exact: true }).fill('123');
  await dlg.getByLabel('PIN again').fill('123');
  await dlg.getByRole('button', { name: 'Add person' }).click();
  await expect(dlg.getByRole('alert')).toHaveText('A call-center agent’s PIN is exactly 4 digits.');
  await dlg.getByLabel('PIN', { exact: true }).fill('4321');
  await dlg.getByLabel('PIN again').fill('4321');
  await shot(page, info, 'add-agent');
  await dlg.getByRole('button', { name: 'Add person' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const row = page.getByTestId('staff-row').filter({ hasText: 'Cara Smith' });
  await expect(row).toContainText('Call center');
  await expect(row).toContainText('No refunds or discounts');
  const people = (await store.me('GET', '/staff', undefined, 200)).data as Array<Record<string, any>>;
  expect(people.map((p) => [p.first_name, p.role])).toEqual([['Cara', 'callcenter']]);
  // An existing cashier can't be switched to Call center in the edit dialog (it needs a new 4-digit PIN).
  await store.me('POST', '/staff', { first_name: 'Sam', role: 'cashier', pin: '77' }, 201);
  await page.reload();
  await page.getByRole('button', { name: 'Edit Sam' }).click();
  await expect(page.getByRole('dialog').getByRole('radio', { name: 'Cashier' })).toBeVisible();
  await expect(page.getByRole('dialog').getByRole('radio', { name: 'Call center' })).toHaveCount(0);
  await page.keyboard.press('Escape');

  await open('/store/settings/callcenter');
  await expect(card).toBeVisible();
  await expect(page.getByText(/^1 agent ·/)).toBeVisible();
  await expect(card.getByText('None yet.')).toBeVisible();
  await expect(card.getByText('No call-center orders in these days.')).toBeVisible();
  await expect(card.getByLabel('Ask for the PIN again after')).toHaveValue('15');

  // The idle lock: a bad value in words, a good one saved on main (the other terminal settings untouched).
  const idle = card.getByLabel('Ask for the PIN again after');
  await idle.fill('0');
  await card.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(card.getByRole('alert')).toHaveText('A whole number of minutes from 1 to 240.');
  await idle.fill('20');
  await card.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.getByText('Call-center lock saved')).toBeVisible();
  const saved = (await store.me('GET', '/terminal/settings', undefined, 200)).data as Record<string, any>;
  expect([saved.callcenter_idle_lock_minutes, saved.auto_lock_minutes, saved.cashier_discount_pct]).toEqual([20, 5, 20]);

  // The call center's own login: never the store's own, set in words, the password never shown back.
  const loginForm = card.getByTestId('callcenter-login');
  await expect(loginForm).toContainText('Not set');
  expect((await cc('POST', '/login', '', { username: store.username, password: store.password })).status).toBe(401);
  await loginForm.getByLabel('Username').fill(store.username);
  await loginForm.getByLabel('Password').fill(CC_PASS);
  await loginForm.getByRole('button', { name: 'Set login' }).click();
  await expect(loginForm.getByRole('alert')).toContainText('different username and password from the store’s own login');
  await loginForm.getByLabel('Username').fill(ccUser(store));
  await loginForm.getByLabel('Password').fill('short');
  await loginForm.getByRole('button', { name: 'Set login' }).click();
  await expect(loginForm.getByRole('alert')).toHaveText('A password is at least 10 characters.');
  await loginForm.getByLabel('Password').fill(CC_PASS);
  await loginForm.getByRole('button', { name: 'Set login' }).click();
  await expect(page.getByText('Call-center login saved')).toBeVisible();
  await expect(loginForm).toContainText(`Set (username ${ccUser(store)})`);
  await expect(loginForm.getByLabel('New password')).toHaveValue('');
  expect((await store.me('GET', '/callcenter/login', undefined, 200)).data).toMatchObject({ username: ccUser(store), set: true });
  // The store's own login still opens nothing at the call center.
  expect((await cc('POST', '/login', '', { username: store.username, password: store.password })).status).toBe(401);

  // Two computers sign in with the call-center login; an agent signs in on one.
  const desk1 = await login(store, 'Desk 1');
  const desk2 = await login(store, 'Desk 2');
  expect((await cc('POST', '/session', desk1, { pin: '4321' })).status).toBe(200);
  expect((await cc('GET', '/me', desk1)).data.settings).toEqual({ idle_lock_minutes: 20 });
  await page.reload();
  const devices = page.getByTestId('callcenter-devices');
  await expect(devices.getByRole('row').filter({ hasText: 'Desk 1' })).toContainText('Cara S.');
  await expect(devices.getByRole('row').filter({ hasText: 'Desk 2' })).toContainText('Locked');
  await expect(page.getByText('1 agent · 2 computers signed in')).toBeVisible();
  await shot(page, info, 'computers');

  // Remove one: its token stops working at once; the other is untouched.
  await devices.getByRole('button', { name: 'Remove Desk 1' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click();
  await expect(devices.getByRole('row').filter({ hasText: 'Desk 1' })).toContainText('Removed');
  expect((await cc('GET', '/me', desk1)).status).toBe(401);
  expect((await cc('GET', '/me', desk2)).status).toBe(200);

  // The report asks main for the days picked; a reversed range is refused in words.
  await card.getByLabel('From').fill('2026-11-10');
  await card.getByLabel('To').fill('2026-11-01');
  await card.getByRole('button', { name: 'Show' }).click();
  await expect(card.getByRole('alert')).toContainText('YYYY-MM-DD');
  await card.getByLabel('To').fill('2026-11-12');
  await card.getByRole('button', { name: 'Show' }).click();
  await expect(card.getByText('No call-center orders in these days.')).toBeVisible();

  // Changing the login signs the remaining computer out (asked first).
  await loginForm.getByLabel('New password').fill(CC_PASS + '-2');
  await loginForm.getByRole('button', { name: 'Change login' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Change login' }).click();
  await expect(page.getByText('Call-center login saved')).toBeVisible();
  expect((await cc('GET', '/me', desk2)).status).toBe(401);
  await shot(page, info, 'login-changed');
});
