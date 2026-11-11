import { test, expect } from './fixtures';

test('the store manager lands on Store › Website', async ({ page, open }) => {
  await open('/store/website');
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
});
