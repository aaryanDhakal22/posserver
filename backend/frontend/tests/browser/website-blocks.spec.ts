// Store › Website, the Home page card: block order (↑ / ↓ and drag), on/off, Miso's spot per
// block, and the edit panels (hero, picks, about) with their Spanish fields.
import { test, expect } from './fixtures';
import { block, blockOrder, draftSoon, openWebsite } from './website';

const DEFAULT = ['Hero', 'Order again', 'Categories', 'Deals', 'From the menu', 'Hours & contact', 'About us', 'Install the app'];
const ID: Record<string, string> = { Hero: 'hero', 'Order again': 'again', Categories: 'categories', Deals: 'deals', 'From the menu': 'picks', 'Hours & contact': 'hours', 'About us': 'about', 'Install the app': 'install' };
const ids = (names: string[]) => names.map((n) => ID[n]).join(',');

test('blocks start in the default order and switch on or off', async ({ page, store }) => {
  await openWebsite(page);
  expect(await blockOrder(page)).toEqual(DEFAULT);
  for (const [name, on] of [['Hero', true], ['About us', false], ['Install the app', false], ['Deals', true]] as const) {
    await expect(page.getByRole('switch', { name: `Show ${name}` })).toHaveAttribute('aria-checked', String(on));
  }
  await page.getByRole('switch', { name: 'Show About us' }).click();
  await page.getByRole('switch', { name: 'Show Order again' }).click();
  await expect(block(page, 'Order again')).toHaveClass(/\boff\b/);
  await expect(block(page, 'About us')).not.toHaveClass(/\boff\b/);
  await draftSoon(
    store,
    (d) => JSON.stringify(d.blocks.filter((b: any) => b.on).map((b: any) => b.id)) === JSON.stringify(['hero', 'categories', 'deals', 'picks', 'hours', 'about']),
    'about on, order again off'
  );
});

test('↑ / ↓ move a block; the ends are disabled', async ({ page, store }) => {
  await openWebsite(page);
  await expect(page.getByRole('button', { name: 'Move Hero up' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Move Install the app down' })).toBeDisabled();
  await page.getByRole('button', { name: 'Move Hours & contact up' }).click();
  await page.getByRole('button', { name: 'Move Hours & contact up' }).click();
  await page.getByRole('button', { name: 'Move Hero down' }).click();
  const want = ['Order again', 'Hero', 'Categories', 'Hours & contact', 'Deals', 'From the menu', 'About us', 'Install the app'];
  expect(await blockOrder(page)).toEqual(want);
  await expect(page.getByRole('button', { name: 'Move Order again up' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Move Hero up' })).toBeEnabled();
  await draftSoon(store, (d) => d.blocks.map((b: any) => b.id).join(',') === ids(want), 'the new order');
  // The screen reader position follows.
  await expect(block(page, 'Hours & contact').locator('.sr-only')).toHaveText('Position 4 of 8');
});

test('dragging a block reorders the list', async ({ page, store, isMobile }) => {
  // Playwright drives HTML5 drag with a mouse; the phone project emulates touch, where the ↑ / ↓
  // buttons (tested above on both sizes) are the way to reorder.
  test.skip(isMobile, 'HTML5 drag needs a mouse');
  await openWebsite(page);
  await block(page, 'Install the app').dragTo(block(page, 'Categories'));
  const after = await blockOrder(page);
  expect(after.indexOf('Install the app')).toBe(2);
  expect(after.filter((n) => n !== 'Install the app')).toEqual(DEFAULT.filter((n) => n !== 'Install the app'));
  await draftSoon(store, (d) => d.blocks.map((b: any) => b.id).join(',') === ids(after), 'the dragged order');
  await expect(page.locator('li.blk.dragging')).toHaveCount(0);
});

test('the cat button cycles Miso: off → peeks → beside → off (hero peeks by default)', async ({ page, store }) => {
  await openWebsite(page);
  const heroCat = block(page, 'Hero').locator('button.cat');
  await expect(heroCat).toHaveAttribute('title', 'Miso peeks over it');
  await expect(heroCat).toHaveText('peeks');
  const cat = block(page, 'Deals').locator('button.cat');
  await expect(cat).toHaveAttribute('aria-label', 'No Miso — change');
  await expect(cat).not.toHaveClass(/\bon\b/);
  await cat.click();
  await expect(cat).toHaveAttribute('aria-label', 'Miso peeks over it — change');
  await expect(cat).toHaveText('peeks');
  await draftSoon(store, (d) => d.miso_blocks?.deals === 'peek' && d.miso_blocks?.hero === 'peek', 'deals peek');
  await cat.click();
  await expect(cat).toHaveAttribute('aria-label', 'Miso sits beside it — change');
  await expect(cat).toHaveText('beside');
  await expect(cat).toHaveClass(/\bon\b/);
  await draftSoon(store, (d) => d.miso_blocks?.deals === 'side', 'deals side');
  await cat.click();
  await expect(cat).toHaveAttribute('aria-label', 'No Miso — change');
  await draftSoon(store, (d) => d.miso_blocks?.deals === 'off', 'deals off');
  await heroCat.click();
  await heroCat.click();
  await draftSoon(store, (d) => d.miso_blocks?.hero === 'off', 'hero off');
});

test('hero edit panel: headline + intro, Spanish fields only while Spanish is on', async ({ page, store }) => {
  await openWebsite(page);
  const edit = page.getByRole('button', { name: 'Edit Hero' });
  await expect(edit).toHaveAttribute('aria-expanded', 'false');
  await edit.click();
  await expect(edit).toHaveAttribute('aria-expanded', 'true');
  await page.getByLabel('Headline', { exact: true }).fill('Hot slices.');
  await page.getByLabel('Intro', { exact: true }).fill('Since 1999.');
  await page.getByLabel('Headline (Spanish)').fill('Rebanadas calientes.');
  await page.getByLabel('Intro (Spanish)').fill('Desde 1999.');
  await expect(page.getByLabel('Headline', { exact: true })).toHaveAttribute('maxlength', '80');
  await expect(page.getByLabel('Intro', { exact: true })).toHaveAttribute('maxlength', '200');
  await draftSoon(
    store,
    (d) => d.texts?.headline === 'Hot slices.' && d.texts?.lead === 'Since 1999.' && d.texts?.headline_es === 'Rebanadas calientes.' && d.texts?.lead_es === 'Desde 1999.',
    'hero texts'
  );
  await page.getByRole('switch', { name: 'Spanish' }).click();
  await expect(page.getByLabel('Headline (Spanish)')).toHaveCount(0);
  // Emptied texts leave the document.
  await page.getByLabel('Intro', { exact: true }).fill('   ');
  await draftSoon(store, (d) => d.texts?.lead === undefined && d.texts?.headline === 'Hot slices.', 'empty intro dropped');
  // Only one panel open at a time; the button closes it.
  await page.getByRole('button', { name: 'Edit About us' }).click();
  await expect(page.getByLabel('Headline', { exact: true })).toHaveCount(0);
  await expect(page.getByLabel('Story', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Edit About us' }).click();
  await expect(page.getByLabel('Story', { exact: true })).toHaveCount(0);
});

test('about edit panel: title, story, Spanish, and the empty-story hint', async ({ page, store }) => {
  await openWebsite(page);
  await page.getByRole('button', { name: 'Edit About us' }).click();
  await expect(page.getByText('About us shows once it has a story.')).toBeVisible();
  await page.getByLabel('Title', { exact: true }).fill('Our story');
  await page.getByLabel('Story', { exact: true }).fill('First paragraph.\n\nSecond paragraph.');
  await expect(page.getByText('About us shows once it has a story.')).toHaveCount(0);
  await page.getByLabel('Title (Spanish)').fill('Nuestra historia');
  await page.getByLabel('Story (Spanish)').fill('Primero.');
  await draftSoon(
    store,
    (d) =>
      d.texts?.about_title === 'Our story' &&
      d.texts?.about === 'First paragraph.\n\nSecond paragraph.' &&
      d.texts?.about_title_es === 'Nuestra historia' &&
      d.texts?.about_es === 'Primero.',
    'about texts'
  );
});

test('picks: search, add, reorder, remove, at most 8', async ({ page, store }) => {
  // Six more sides so there are nine items to pick from.
  for (const n of ['Garlic Knots', 'Salad', 'Soda', 'Cookie', 'Brownie', 'Calzone']) {
    await store.me('POST', `/menu/categories/${store.menu.sd.id}/items`, { name: n, price_cents: 299 }, 201);
  }
  await openWebsite(page);
  const row = block(page, 'From the menu');
  await expect(row.getByText('None picked: the first item of each category')).toBeVisible();
  await page.getByRole('button', { name: 'Edit From the menu' }).click();
  const search = page.getByPlaceholder('Add an item: type its name');
  await expect(search).toBeVisible();

  // One letter isn't a search yet.
  await search.fill('w');
  await expect(page.locator('button.match')).toHaveCount(0);
  await search.fill('wi');
  await expect(page.locator('button.match')).toHaveText(['Wings · Sides']);
  await page.locator('button.match').click();
  await expect(search).toHaveValue('');
  await search.fill('pizza');
  await page.locator('button.match', { hasText: 'Cheese Pizza' }).click();
  await expect(row.getByText('2 picked (up to 8)')).toBeVisible();
  await expect(page.locator('.pick')).toHaveText([/^Wings · Sides ↑ ×$/, /^Cheese Pizza · Pizza ↑ ×$/]);
  // A picked item isn't offered again.
  await search.fill('wings');
  await expect(page.locator('button.match')).toHaveCount(0);
  await search.fill('');

  // Up moves Cheese Pizza first; the first one's Up is disabled.
  await expect(page.locator('.pick').first().getByRole('button', { name: 'Up' })).toBeDisabled();
  await page.locator('.pick').nth(1).getByRole('button', { name: 'Up' }).click();
  await expect(page.locator('.pick')).toHaveText([/^Cheese Pizza/, /^Wings/]);
  await draftSoon(store, (d) => JSON.stringify(d.picks) === JSON.stringify([store.menu.pizza.id, store.menu.wings.id]), 'picks pizza, wings');
  await page.getByRole('button', { name: 'Remove Wings' }).click();
  await expect(page.locator('.pick')).toHaveCount(1);
  await draftSoon(store, (d) => JSON.stringify(d.picks) === JSON.stringify([store.menu.pizza.id]), 'picks pizza');

  // Fill to 8: the search box goes away.
  for (const n of ['Garlic', 'Salad', 'Soda', 'Cookie', 'Brownie', 'Calzone', 'Fries']) {
    await search.fill(n);
    await page.locator('button.match', { hasText: n }).first().click();
  }
  await expect(page.locator('.pick')).toHaveCount(8);
  await expect(row.getByText('8 picked (up to 8)')).toBeVisible();
  await expect(search).toHaveCount(0);
  await draftSoon(store, (d) => d.picks?.length === 8, 'eight picks');
});
