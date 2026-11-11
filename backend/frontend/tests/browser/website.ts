// Helpers for the Store › Website specs: main's view of the draft (what autosave really stored),
// a stub for the preview frame (no storefront runs in these tests) and the page's landmarks.
import { expect, type Page } from '@playwright/test';
import type { TestStore } from './fixtures';

type Json = Record<string, any>;

/** main's Website view for the store (GET /store/v1/me/site/website). */
export async function website(store: TestStore): Promise<Json> {
  return (await store.me('GET', '/site/website', undefined, 200)).data;
}

/** Polls main until the saved draft matches (autosave is debounced ~600 ms). */
export async function draftSoon(store: TestStore, check: (draft: Json, w: Json) => boolean, what = 'the draft') {
  await expect
    .poll(
      async () => {
        const w = await website(store);
        try {
          return check(w.draft ?? {}, w);
        } catch {
          return false; // the field isn't there yet
        }
      },
      { message: `main never stored ${what}`, timeout: 10_000 }
    )
    .toBe(true);
}

/** The preview iframe points at the storefront (<host>:5173 on dev); answer it with a stub page. */
export async function stubPreview(page: Page) {
  const seen: string[] = [];
  await page.route(/^https?:\/\/[^/]+\/p\/qpv_[0-9a-f]+/, (route) => {
    seen.push(route.request().url());
    return route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>preview</title><body style="margin:0;background:#eee">preview</body>' });
  });
  return seen;
}

/** Opens Store › Website and waits until the editor (not the skeleton) shows. */
export async function openWebsite(page: Page) {
  await stubPreview(page);
  await page.goto('/app/store/website');
  await expect(page.getByRole('radiogroup', { name: 'Preset' })).toBeVisible();
}

/** The "Draft saved" status after an edit (the save landed). */
export async function saved(page: Page) {
  await expect(page.getByText('Draft saved')).toBeVisible();
  await expect(page.getByText('Saving…')).toHaveCount(0);
}

export function block(page: Page, name: string) {
  return page.locator('li.blk').filter({ has: page.locator('b', { hasText: new RegExp(`^${name}$`) }) });
}

export async function blockOrder(page: Page): Promise<string[]> {
  return page.locator('li.blk .blk-row b').allTextContents();
}
