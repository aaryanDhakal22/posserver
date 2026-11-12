// Store › Website: announcement, address (own domain request), privacy, the preview pane, and the
// draft's life: publish, discard, and an admin who opened the store publishing for it.
import { test, expect, adminKey } from './fixtures';
import { draftSoon, openWebsite, saved, stubPreview, website } from './website';

test('announcement bar: switch, required text, Spanish text, dates', async ({ page, store }) => {
  await openWebsite(page);
  const on = page.getByRole('switch', { name: 'Show the announcement bar' });
  await on.click();
  await expect(page.getByText('The bar needs its text')).toBeVisible();
  await expect(page.locator('#ann')).toHaveAttribute('aria-invalid', 'true');
  await page.locator('#ann').fill('Closed Thanksgiving Day');
  await expect(page.getByText('The bar needs its text')).toHaveCount(0);
  await page.locator('#ann-es').fill('Cerrado el Día de Acción de Gracias');
  await page.locator('#ann-from').fill('2026-11-25');
  await page.locator('#ann-to').fill('2026-11-27');
  await draftSoon(
    store,
    (d) =>
      d.announcement?.on === true &&
      d.announcement.text === 'Closed Thanksgiving Day' &&
      d.announcement.text_es === 'Cerrado el Día de Acción de Gracias' &&
      d.announcement.from === '2026-11-25' &&
      d.announcement.to === '2026-11-27',
    'the announcement'
  );
  // Off with text kept: stored as off.
  await on.click();
  await draftSoon(store, (d) => d.announcement?.on === false && d.announcement.text === 'Closed Thanksgiving Day', 'announcement off');
  // Off and empty: just {on: false}.
  await page.locator('#ann').fill('');
  await draftSoon(store, (d) => JSON.stringify(d.announcement) === '{"on":false}', 'announcement cleared');
});

test('address: the live host, Copy, requesting an own domain shows its DNS record, Remove asks first', async ({ page, store, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await openWebsite(page);
  const card = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'Address' }) });
  await expect(card.getByText(store.host, { exact: true })).toBeVisible();
  await expect(page.locator('.status').getByText('Live')).toBeVisible();
  await expect(page.locator('.status').getByRole('link', { name: store.host })).toHaveAttribute('href', `https://${store.host}`);
  await card.getByRole('button', { name: 'Copy' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Copied' })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`https://${store.host}`);

  const request = card.getByRole('button', { name: 'Request' });
  await expect(request).toBeDisabled();
  const own = `order.${store.tag}-pizza.com`;
  await page.locator('#own').fill(own);
  await request.click();
  const pending = card.locator('.pending');
  await expect(pending.getByText(own, { exact: true })).toBeVisible();
  await expect(pending.getByText('Waiting for check')).toBeVisible();
  const { base_domain } = (await store.me('GET', '/site/domains', undefined, 200)).data;
  await expect(pending.locator('.dns')).toHaveText(`TypeCNAMENameorderValuestores.${base_domain}`);
  await expect(page.locator('#own')).toHaveCount(0); // one request at a time
  expect((await store.me('GET', '/site/domains', undefined, 200)).data.domains.some((d: any) => d.host === own && !d.verified)).toBe(true);

  // Remove: Cancel keeps it, Remove drops it.
  await pending.getByRole('button', { name: 'Remove' }).click();
  const dlg = page.getByRole('dialog', { name: `Remove ${own}?` });
  await dlg.getByRole('button', { name: 'Cancel' }).click();
  await expect(dlg).toBeHidden();
  await expect(pending).toBeVisible();
  await pending.getByRole('button', { name: 'Remove' }).click();
  await page.getByRole('dialog', { name: `Remove ${own}?` }).getByRole('button', { name: 'Remove' }).click();
  await expect(card.locator('.pending')).toHaveCount(0);
  await expect(page.locator('#own')).toBeVisible();
  expect((await store.me('GET', '/site/domains', undefined, 200)).data.domains.some((d: any) => d.host === own)).toBe(false);
});

test('privacy: the default from the store details, Edit starts from it, Back to the default', async ({ page, store }) => {
  await openWebsite(page);
  const card = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'Privacy policy' }) });
  await expect(card.getByText('Using the default')).toBeVisible();
  await expect(card.locator('.policy p').first()).toContainText('Test Pizza');
  await expect(card.locator('.policy')).toContainText('card numbers go straight to Square');
  await card.getByRole('button', { name: 'Edit' }).click();
  const box = card.locator('textarea');
  await expect(box).toHaveValue(/^Test Pizza[\s\S]*We don't sell or share your information\./);
  await expect(card.getByText('Your own text')).toBeVisible();
  await box.fill('Our own policy.\n\nSecond part.');
  await draftSoon(store, (d) => d.privacy === 'Our own policy.\n\nSecond part.', 'the own policy');
  await card.getByRole('button', { name: 'Back to the default' }).click();
  await expect(card.getByText('Using the default')).toBeVisible();
  await expect(card.locator('textarea')).toHaveCount(0);
  await expect(card.getByRole('button', { name: 'Edit' })).toBeVisible();
  await draftSoon(store, (d) => d.privacy === undefined && d.preset === 'pizzeria-loud', 'no own policy');
});

test('preview: page and device pickers drive the frame, Open preview matches, a save reloads it', async ({ page, store }) => {
  const frames = await stubPreview(page);
  await page.goto('/app/store/website');
  await expect(page.getByRole('radiogroup', { name: 'Preset' })).toBeVisible();
  const w = await website(store);
  expect(w.preview_url).toMatch(/\/p\/qpv_[0-9a-f]+$/);
  const frame = page.locator('iframe[title="Preview of your website"]');
  const open = page.getByRole('link', { name: 'Open preview' });
  await expect(frame).toHaveAttribute('src', w.preview_url);
  await expect(open).toHaveAttribute('href', w.preview_url);
  await expect(open).toHaveAttribute('target', '_blank');

  const pages: [string, string][] = [['Menu', '/menu'], ['An item', '/menu/cheese-pizza'], ['Deals', '/deals'], ['Cart', '/cart'], ['Checkout', '/checkout'], ['Privacy', '/privacy'], ['Home', '']];
  for (const [label, path] of pages) {
    await page.getByRole('combobox', { name: 'Page' }).selectOption({ label });
    await expect(frame).toHaveAttribute('src', w.preview_url + path);
    await expect(open).toHaveAttribute('href', w.preview_url + path);
  }
  for (const [label, width] of [['Tablet', 834], ['Desktop', 1280], ['Phone', 390]] as const) {
    const radio = page.getByRole('radiogroup', { name: 'Device' }).getByRole('radio', { name: label });
    await radio.click();
    await expect(radio).toHaveAttribute('aria-checked', 'true');
    await expect(frame).toHaveCSS('width', `${width}px`);
    // Scaled to fit the pane, never wider than it.
    const pane = (await page.locator('.pane').boundingBox())!;
    const dev = (await page.locator('.device').boundingBox())!;
    expect(dev.width).toBeLessThanOrEqual(pane.width + 1);
  }
  await expect.poll(() => frames.length).toBeGreaterThan(0);
  const before = frames.length;
  await page.getByRole('radio', { name: /Café, calm/ }).click();
  await saved(page);
  await expect.poll(() => frames.length, { message: 'the frame reloads after a save' }).toBeGreaterThan(before);
});

test('publish: the draft goes live, the version moves, the buttons settle', async ({ page, store }) => {
  await openWebsite(page);
  const v0 = (await website(store)).version;
  await page.getByRole('radio', { name: /Taquería, bold/ }).click();
  await page.locator('#brand').fill('#FF4D8D');
  // Publish right away: a save still pending goes first.
  await expect(page.getByRole('button', { name: 'Publish' })).toBeEnabled();
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Published · your website shows it within a minute' })).toBeVisible();
  const w = await website(store);
  expect(w.has_draft).toBe(false);
  expect(w.version).toBeGreaterThan(v0);
  expect(w.published).toMatchObject({ preset: 'taqueria-bold', brand: '#FF4D8D' });
  expect(w.published_by).not.toBe('admin');
  await expect(page.getByRole('button', { name: 'Publish' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Discard draft' })).toHaveCount(0);
  await expect(page.getByText(/changes? in your draft/)).toHaveCount(0);
  await expect(page.locator('.status').getByText(/· published /)).toBeVisible();
  await expect(page.locator('.status').getByText('by an admin')).toHaveCount(0);
  await expect(page.locator('.pv-bar').getByText('Published', { exact: true })).toBeVisible();
  // A reload shows the published look.
  await page.reload();
  await expect(page.getByRole('radio', { name: /Taquería, bold/ })).toHaveAttribute('aria-checked', 'true');
});

test('discard: Cancel keeps the draft, Discard goes back to the published look', async ({ page, store }) => {
  await store.me('PUT', '/site/website/draft', { preset: 'classic-paper' }, 200);
  await store.me('POST', '/site/website/publish', undefined, 200);
  await openWebsite(page);
  await page.getByRole('radio', { name: /Café, calm/ }).click();
  await page.getByRole('switch', { name: 'Show About us' }).click();
  await saved(page);
  await page.getByRole('button', { name: 'Discard draft' }).click();
  const dlg = page.getByRole('dialog', { name: 'Discard your draft?' });
  await expect(dlg.getByText('Your website stays as it is now.')).toBeVisible();
  await dlg.getByRole('button', { name: 'Cancel' }).click();
  await expect(dlg).toBeHidden();
  expect((await website(store)).has_draft).toBe(true);

  await page.getByRole('button', { name: 'Discard draft' }).click();
  await page.getByRole('dialog', { name: 'Discard your draft?' }).getByRole('button', { name: 'Discard' }).click();
  await expect(page.getByRole('button', { name: 'Discard draft' })).toHaveCount(0);
  await expect(page.getByRole('radio', { name: /Classic paper/ })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('switch', { name: 'Show About us' })).toHaveAttribute('aria-checked', 'false');
  const w = await website(store);
  expect(w.has_draft).toBe(false);
  expect(w.draft.preset).toBe('classic-paper');
  // Nothing saves by itself after the discard.
  await page.waitForLoadState('networkidle');
  expect((await website(store)).has_draft).toBe(false);
});

test('an admin who opened the store edits and publishes it', async ({ page, store }) => {
  await page.context().clearCookies();
  const r = await page.request.post(`/admin/v1/tenants/${store.tenantId}/store-session`, { headers: { authorization: `Bearer ${adminKey()}` } });
  expect(r.status()).toBeLessThan(300);
  await openWebsite(page);
  await page.getByRole('radio', { name: /Classic paper/ }).click();
  await saved(page);
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.locator('.status').getByText(/by an admin/)).toBeVisible();
  const w = await website(store);
  expect(w.published_by).toBe('admin');
  expect(w.published.preset).toBe('classic-paper');
});
