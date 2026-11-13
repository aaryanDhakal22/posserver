// main's settings map (src/lib/settings/settings-map.json, contract/main.md §2.5zm): the Settings search
// finds a setting by a word people use and opens it lit up; a deep link (path#anchor) lands on its field;
// and every page + field in the map is really there on its page (the map can't drift from the pages).
import { readFileSync } from 'node:fs';
import { test, expect } from './fixtures';

type Field = { id: string; anchor: string; label: string };
type Page = { id: string; path: string; title: string; fields: Field[] };
const MAP: { pages: Page[] } = JSON.parse(readFileSync(new URL('../../src/lib/settings/settings-map.json', import.meta.url), 'utf8'));

test('search: a word finds the setting, Enter opens its page with the field lit up and focused', async ({ page, open }) => {
  await open('/store/settings');
  const box = page.getByTestId('settings-search');
  await box.fill('ticket');
  const hits = page.getByTestId('settings-search-hits');
  await expect(hits.getByRole('option').first()).toContainText('Counter numbers start at');
  await expect(hits).toContainText('Online and kitchen numbers start at');
  await box.press('Enter');
  await expect(page).toHaveURL(/\/app\/store\/settings\/counter#counter_start$/);
  await expect(page.locator('.setting-hit', { hasText: 'Counter numbers start at' })).toBeVisible();
  await expect(page.locator('input').and(page.getByLabel('Counter numbers start at', { exact: false }))).toBeFocused();

  // Arrow keys pick another hit; synonyms count ("radius" → the delivery area).
  await open('/store/settings');
  await box.fill('radius');
  await expect(hits).toContainText('Delivers up to (miles)');
  await box.fill('zzzz-nothing');
  await expect(hits).toContainText('Nothing called that');
  await box.fill('printer');
  await box.press('ArrowDown');
  await expect(hits.getByRole('option', { selected: true })).toBeVisible();
  await box.press('Escape');
  await expect(box).toHaveValue('');
});

test('a deep link lands on its field: on load, and when only the hash changes', async ({ page, open }) => {
  await open('/store/settings/end-of-day#close_unpaid_counter_action');
  await expect(page.locator('.setting-hit', { hasText: 'Unpaid counter checks at close' })).toBeVisible();
  await page.evaluate(() => (location.hash = '#drawer_start_cents'));
  await expect(page.locator('.setting-hit', { hasText: 'Cash left in each drawer at opening' })).toBeVisible();
  // A tab: the link opens it.
  await open('/store/menu#deals');
  await expect(page.locator('#mt-deals')).toHaveAttribute('aria-selected', 'true');
});

test('every page and field in the map is on its page', async ({ page, open }) => {
  test.setTimeout(180_000);
  const missing: string[] = [];
  for (const p of MAP.pages) {
    await open(p.path.replace(/^\/app/, ''));
    await expect(page.getByRole('heading', { level: 1 }).first()).toBeVisible();
    for (const f of p.fields) {
      // Each field by its hash: the page's own lander finds it (by id, else by its label) and lights it up.
      await page.evaluate((a) => {
        document.querySelectorAll('.setting-hit').forEach((e) => e.classList.remove('setting-hit'));
        location.hash = '#' + a;
      }, f.anchor);
      const found = await page
        .locator('.setting-hit')
        .first()
        .waitFor({ timeout: 4000 })
        .then(() => true)
        .catch(() => false);
      if (!found) missing.push(`${f.id} ("${f.label}" on ${p.path})`);
      await page.evaluate(() => history.replaceState(null, '', location.pathname));
    }
  }
  expect(missing, 'fields of the map not found on their page').toEqual([]);
});
