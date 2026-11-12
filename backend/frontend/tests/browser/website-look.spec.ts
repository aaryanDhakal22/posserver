// Store › Website, the Look card: presets, brand colour + its readability check, menu layout,
// Miso and Spanish switches — each one checked against what main stored in the draft.
import { test, expect } from './fixtures';
import { draftSoon, openWebsite, saved, website } from './website';

test('a fresh store shows the defaults and has no draft', async ({ page, store }) => {
  await openWebsite(page);
  const w = await website(store);
  expect(w.has_draft).toBe(false);
  await expect(page.getByRole('radio', { name: /Pizzeria, loud/ })).toHaveAttribute('aria-checked', 'true');
  await expect(page.locator('#brand')).toHaveValue('#E2363B');
  await expect(page.getByText(/Readable · white text on it/)).toBeVisible();
  await expect(page.getByRole('radio', { name: 'List' })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('switch', { name: 'Miso on cart, checkout and sign-in' })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('switch', { name: 'Spanish' })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByText('not published yet (the default look shows)')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Publish' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Discard draft' })).toHaveCount(0);
  await expect(page.getByText(/changes? in your draft/)).toHaveCount(0);
  await expect(page.locator('.pv-bar').getByText('Published', { exact: true })).toBeVisible();
});

test('each preset is picked, its own menu layout follows, and the draft stores it', async ({ page, store }) => {
  await openWebsite(page);
  const expectLayout: Record<string, string> = { 'Café, calm': 'Tiles', 'Taquería, bold': 'Printed', 'Classic paper': 'Printed', 'Pizzeria, loud': 'List' };
  const ids: Record<string, string> = { 'Café, calm': 'cafe-calm', 'Taquería, bold': 'taqueria-bold', 'Classic paper': 'classic-paper', 'Pizzeria, loud': 'pizzeria-loud' };
  for (const [name, layout] of Object.entries(expectLayout)) {
    const radio = page.getByRole('radio', { name: new RegExp(name) });
    await radio.click();
    await expect(radio).toHaveAttribute('aria-checked', 'true');
    await expect(page.locator('.presets [aria-checked="true"]')).toHaveCount(1);
    await expect(page.getByRole('radio', { name: layout, exact: true })).toHaveAttribute('aria-checked', 'true');
    await draftSoon(store, (d) => d.preset === ids[name] && d.menu_layout === undefined, `preset ${ids[name]}`);
  }
});

test('the menu layout overrides the preset, and picking the preset’s own clears the override', async ({ page, store }) => {
  await openWebsite(page);
  await page.getByRole('radio', { name: 'Tiles', exact: true }).click();
  await expect(page.getByRole('radio', { name: 'Tiles', exact: true })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('radio', { name: 'List', exact: true })).toHaveAttribute('aria-checked', 'false');
  await draftSoon(store, (d) => d.menu_layout === 'photo', 'menu_layout photo');
  await page.getByRole('radio', { name: 'Printed', exact: true }).click();
  await draftSoon(store, (d) => d.menu_layout === 'printed', 'menu_layout printed');
  // List is the loud preset's own layout: no override stored.
  await page.getByRole('radio', { name: 'List', exact: true }).click();
  await draftSoon(store, (d) => d.menu_layout === undefined && d.preset === 'pizzeria-loud', 'no menu_layout');
});

test('brand colour: typed, picked, checked for readability, refused when malformed', async ({ page, store }) => {
  await openWebsite(page);
  const brand = page.locator('#brand');

  // A strong blue reads fine on the loud preset.
  await brand.fill('#1d4ed8');
  await expect(page.getByText(/Readable · white text on it \d+\.\d:1/)).toBeVisible();
  await draftSoon(store, (d) => d.brand === '#1D4ED8', 'brand #1D4ED8 (upper-cased)');

  // A near-black disappears on the dark loud canvas: the warning and its help line.
  await brand.fill('#1a1414');
  await expect(page.getByText(/Hard to see · \d+\.\d:1 on the page/)).toBeVisible();
  await expect(page.getByText(/nearly disappears on the Pizzeria, loud background/)).toBeVisible();
  // …but on Classic paper (white) the same colour is fine.
  await page.getByRole('radio', { name: /Classic paper/ }).click();
  await expect(page.getByText(/Readable · white text on it/)).toBeVisible();

  // The colour picker writes the field (upper-cased).
  await page.getByLabel('Pick a colour').evaluate((el: HTMLInputElement) => {
    el.value = '#00aa55';
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await expect(brand).toHaveValue('#00AA55');
  await draftSoon(store, (d) => d.brand === '#00AA55', 'brand from the picker');

  // Malformed: the field error, no badge, and main refuses the draft (Publish stays off).
  await brand.fill('#12');
  await expect(page.getByText('A colour like #E2363B')).toBeVisible();
  await expect(brand).toHaveAttribute('aria-invalid', 'true');
  await expect(page.getByText(/Readable ·|Hard to see ·/)).toHaveCount(0);
  await expect(page.getByText(/Not saved:/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Publish' })).toBeDisabled();
  expect((await website(store)).draft.brand).toBe('#00AA55');
  // Fixing it saves again.
  await brand.fill('#E2363B');
  await saved(page);
  await draftSoon(store, (d) => d.brand === '#E2363B', 'brand fixed');
  await expect(page.getByRole('button', { name: 'Publish' })).toBeEnabled();
});

test('Miso and Spanish switches', async ({ page, store }) => {
  await openWebsite(page);
  const miso = page.getByRole('switch', { name: 'Miso on cart, checkout and sign-in' });
  await miso.click();
  await expect(miso).toHaveAttribute('aria-checked', 'false');
  await draftSoon(store, (d) => d.miso_pages === false, 'miso_pages off');
  await miso.click();
  await draftSoon(store, (d) => d.miso_pages === true, 'miso_pages on');

  const es = page.getByRole('switch', { name: 'Spanish' });
  // Spanish on: the announcement's Spanish field shows.
  await expect(page.locator('#ann-es')).toBeVisible();
  await es.click();
  await expect(es).toHaveAttribute('aria-checked', 'false');
  await expect(page.locator('#ann-es')).toHaveCount(0);
  await draftSoon(store, (d) => JSON.stringify(d.languages) === '["en"]', 'languages en only');
  await es.click();
  await draftSoon(store, (d) => JSON.stringify(d.languages) === '["en","es"]', 'languages en + es');
});

test('autosave: one save after a burst of edits, the changes bar, and a reload keeps the draft', async ({ page, store }) => {
  await openWebsite(page);
  const puts: string[] = [];
  page.on('request', (r) => {
    if (r.method() === 'PUT' && r.url().endsWith('/store/v1/me/site/website/draft')) puts.push(r.postData() ?? '');
  });
  await page.locator('#brand').fill('#0A');
  await page.locator('#brand').fill('#0A6');
  await page.locator('#brand').fill('#0A66C2');
  await page.getByRole('radio', { name: /Café, calm/ }).click();
  await expect(page.getByText('Saving…')).toBeVisible();
  await saved(page);
  expect(puts.length, 'the burst was debounced into one save').toBe(1);
  expect(JSON.parse(puts[0])).toMatchObject({ brand: '#0A66C2', preset: 'cafe-calm' });

  const w = await website(store);
  expect(w.has_draft).toBe(true);
  expect(w.changes.length).toBeGreaterThan(0);
  await expect(page.getByText(`${w.changes.length} ${w.changes.length === 1 ? 'change' : 'changes'}`)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Discard draft' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Publish' })).toBeEnabled();
  await expect(page.locator('.pv-bar').getByText('Draft', { exact: true })).toBeVisible();

  await page.reload();
  await expect(page.getByRole('radio', { name: /Café, calm/ })).toHaveAttribute('aria-checked', 'true');
  await expect(page.locator('#brand')).toHaveValue('#0A66C2');
  // Opening the page doesn't save anything by itself.
  const before = puts.length;
  await page.waitForLoadState('networkidle');
  await expect(page.getByText('Saving…')).toHaveCount(0);
  expect(puts.length).toBe(before);
});
