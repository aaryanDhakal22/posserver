// Store › Website: logo and cover uploads through the in-browser cropper (drag, arrow keys, zoom,
// Cancel / Use this), the SVG logo that skips it, Remove, and the refusals.
import { test, expect, png, type TestStore } from './fixtures';
import { draftSoon, openWebsite, website } from './website';
import type { Page } from '@playwright/test';

/** The stored image's bytes, as the manager's page fetches them. */
async function stored(store: TestStore, id: string): Promise<{ type: string; bytes: Buffer }> {
  const r = await fetch(`${process.env.MAIN_BASE_URL ?? 'http://localhost:1323'}/store/v1/me/site/images/${id}`, { headers: { cookie: store.cookie } });
  expect(r.status).toBe(200);
  return { type: r.headers.get('content-type') ?? '', bytes: Buffer.from(await r.arrayBuffer()) };
}

function pngSize(b: Buffer) {
  return { w: b.readUInt32BE(16), h: b.readUInt32BE(20) };
}

function jpegSize(b: Buffer) {
  let i = 2;
  while (i < b.length) {
    const marker = b[i + 1];
    const len = b.readUInt16BE(i + 2);
    if (marker >= 0xc0 && marker <= 0xc3) return { h: b.readUInt16BE(i + 5), w: b.readUInt16BE(i + 7) };
    i += 2 + len;
  }
  throw new Error('no SOF marker');
}

async function pick(page: Page, row: 'Logo' | 'Cover photo', button: string, file: { name: string; mimeType: string; buffer: Buffer }) {
  const chooser = page.waitForEvent('filechooser');
  await page.locator('.imgrow').filter({ hasText: row }).getByRole('button', { name: button, exact: true }).click();
  await (await chooser).setFiles(file);
}

const uploads = (page: Page) => {
  const seen: string[] = [];
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().includes('/store/v1/me/site/images')) seen.push(r.url());
  });
  return seen;
};

test('logo: the cropper moves (drag, arrows), zooms, Cancel uploads nothing, Use this stores a 512² PNG', async ({ page, store }) => {
  await openWebsite(page);
  const posts = uploads(page);
  const logoRow = page.locator('.imgrow').filter({ hasText: 'Logo' });
  await expect(logoRow.getByText('No logo yet')).toBeVisible();

  const chooser = page.waitForEvent('filechooser');
  await logoRow.getByRole('button', { name: 'Upload' }).click();
  const fc = await chooser;
  expect(await fc.element().getAttribute('accept')).toContain('image/svg+xml');
  await fc.setFiles({ name: 'wide.png', mimeType: 'image/png', buffer: png(400, 200) });

  const dlg = page.getByRole('dialog', { name: 'Frame your logo' });
  await expect(dlg).toBeVisible();
  await expect(dlg.getByText('We keep a 512 × 512 copy')).toBeVisible();
  const frame = dlg.getByRole('slider', { name: 'Move the picture (arrow keys)' });
  const img = frame.locator('img');
  await expect(img).toBeVisible();
  const tf = () => img.evaluate((el) => (el as HTMLImageElement).style.transform);
  await expect.poll(tf).toBe('translate(0px, 0px)');

  // Arrow keys move the picture (5 px, 20 with Shift); it can't leave the frame.
  await frame.focus();
  await page.keyboard.press('ArrowLeft');
  await expect.poll(tf).toBe('translate(5px, 0px)');
  await page.keyboard.press('Shift+ArrowRight');
  await expect.poll(tf).toBe('translate(-15px, 0px)');
  await page.keyboard.press('ArrowUp'); // the picture just covers the frame's height: no room to move
  await expect.poll(tf).toBe('translate(-15px, 0px)');
  for (let i = 0; i < 40; i++) await page.keyboard.press('Shift+ArrowLeft');
  await expect.poll(tf).toBe('translate(150px, 0px)'); // 600 px wide in a 300 px frame: ±150

  // Dragging moves it too.
  const box = (await frame.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 - 100, box.y + box.height / 2, { steps: 5 });
  await page.mouse.up();
  await expect.poll(tf).toBe('translate(50px, 0px)');

  // Zoom makes it bigger.
  const width = () => img.evaluate((el) => (el as HTMLImageElement).getBoundingClientRect().width);
  const w1 = await width();
  await dlg.getByRole('slider', { name: 'Zoom' }).fill('2');
  await expect.poll(width).toBeGreaterThan(w1 * 1.9);
  expect(await frame.getAttribute('aria-valuenow')).toBe('200');

  // Cancel: closed, nothing uploaded, nothing in the draft.
  await dlg.getByRole('button', { name: 'Cancel' }).click();
  await expect(dlg).toBeHidden();
  expect(posts).toEqual([]);

  // Again, and Use this.
  await pick(page, 'Logo', 'Upload', { name: 'logo.png', mimeType: 'image/png', buffer: png(300, 300) });
  await expect(dlg).toBeVisible();
  await dlg.getByRole('button', { name: 'Use this' }).click();
  await expect(dlg).toBeHidden();
  await expect(logoRow.getByRole('img', { name: 'Logo' })).toBeVisible();
  await expect(logoRow.getByText('Shown in the header and as the app icon.')).toBeVisible();
  await expect(logoRow.getByRole('button', { name: 'Change' })).toBeVisible();
  expect(posts).toHaveLength(1);
  expect(posts[0]).toContain('kind=logo');
  await draftSoon(store, (d) => typeof d.logo === 'string' && d.logo.length > 0, 'the logo id');
  const { draft } = await website(store);
  const s = await stored(store, draft.logo);
  expect(s.type).toBe('image/png');
  expect(pngSize(s.bytes)).toEqual({ w: 512, h: 512 });

  // Remove takes it out of the draft (the file stays until the cleanup).
  await logoRow.getByRole('button', { name: 'Remove' }).click();
  await expect(logoRow.getByText('No logo yet')).toBeVisible();
  await draftSoon(store, (d) => d.logo === undefined, 'no logo');
});

test('an SVG logo skips the cropper', async ({ page, store }) => {
  await openWebsite(page);
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="#E2363B"/></svg>';
  await pick(page, 'Logo', 'Upload', { name: 'logo.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(svg) });
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.locator('.imgrow').filter({ hasText: 'Logo' }).getByRole('img', { name: 'Logo' })).toBeVisible();
  await draftSoon(store, (d) => !!d.logo, 'the svg logo');
  const s = await stored(store, (await website(store)).draft.logo);
  expect(s.type).toBe('image/svg+xml');
  expect(s.bytes.toString()).toBe(svg);
});

test('cover: 16:9 cropper, stored as a 1600 × 900 JPEG, Change and Remove', async ({ page, store }) => {
  await openWebsite(page);
  const coverRow = page.locator('.imgrow').filter({ hasText: 'Cover photo' });
  const chooser = page.waitForEvent('filechooser');
  await coverRow.getByRole('button', { name: 'Upload' }).click();
  const fc = await chooser;
  expect(await fc.element().getAttribute('accept')).not.toContain('svg');
  await fc.setFiles({ name: 'cover.png', mimeType: 'image/png', buffer: png(320, 320) });
  const dlg = page.getByRole('dialog', { name: 'Frame your cover photo' });
  await expect(dlg.getByText('We keep a 16 : 9 copy')).toBeVisible();
  const frame = dlg.getByRole('slider', { name: 'Move the picture (arrow keys)' });
  const fb = (await frame.boundingBox())!;
  expect(Math.round(fb.width)).toBe(448);
  expect(Math.round(fb.height)).toBe(252);
  // A square picture covers a wide frame by its width: it moves up and down only.
  await frame.focus();
  await page.keyboard.press('ArrowUp');
  await expect.poll(() => frame.locator('img').evaluate((el) => (el as HTMLImageElement).style.transform)).toBe('translate(0px, 5px)');
  await dlg.getByRole('button', { name: 'Use this' }).click();
  await expect(coverRow.getByRole('img', { name: 'Cover' })).toBeVisible();
  await draftSoon(store, (d) => !!d.cover, 'the cover id');
  const first = (await website(store)).draft.cover;
  const s = await stored(store, first);
  expect(s.type).toBe('image/jpeg');
  expect(jpegSize(s.bytes)).toEqual({ w: 1600, h: 900 });

  // Change replaces it with a new image.
  await pick(page, 'Cover photo', 'Change', { name: 'c2.png', mimeType: 'image/png', buffer: png(64, 36) });
  await dlg.getByRole('button', { name: 'Use this' }).click();
  await draftSoon(store, (d) => !!d.cover && d.cover !== first, 'the new cover');
  await coverRow.getByRole('button', { name: 'Remove' }).click();
  await expect(coverRow.getByText('16 : 9')).toBeVisible();
  await draftSoon(store, (d) => d.cover === undefined, 'no cover');
});

test('refusals: a GIF is turned away before upload; a too-big SVG gets main’s message', async ({ page, store }) => {
  await openWebsite(page);
  const posts = uploads(page);
  await pick(page, 'Logo', 'Upload', { name: 'a.gif', mimeType: 'image/gif', buffer: Buffer.from('GIF89a') });
  await expect(page.getByRole('alert').filter({ hasText: 'Pick a PNG, JPEG or WebP picture (or an SVG logo)' })).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(posts).toEqual([]);

  // The cover never offers SVG.
  const coverRow = page.locator('.imgrow').filter({ hasText: 'Cover photo' });
  const chooser = page.waitForEvent('filechooser');
  await coverRow.getByRole('button', { name: 'Upload' }).click();
  await (await chooser).setFiles({ name: 'c.svg', mimeType: 'image/svg+xml', buffer: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"/>') });
  await expect(page.getByRole('alert').getByText('Pick a PNG, JPEG or WebP picture', { exact: true })).toBeVisible();
  expect(posts).toEqual([]);

  // Over 2 MB: main refuses it, the page says why, the draft has no logo.
  const big = '<svg xmlns="http://www.w3.org/2000/svg"><!--' + 'x'.repeat(2 * 1024 * 1024 + 10) + '--></svg>';
  await pick(page, 'Logo', 'Upload', { name: 'big.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(big) });
  await expect(page.getByRole('alert').filter({ hasText: /up to 2 MB/ })).toBeVisible();
  expect(posts).toHaveLength(1);
  await expect(page.locator('.imgrow').filter({ hasText: 'Logo' }).getByText('No logo yet')).toBeVisible();
  expect((await website(store)).has_draft).toBe(false);
});
