import { test, expect } from './fixtures';

// Store › Devices › Terminals & tablets (agent 2.5.3): the card lists what the Pi reported (nothing for a
// throwaway store with no Pi) and the newest releases; "Update now" asks first, then calls main, which asks
// the store's Pi (none connected here: the page says so, nothing breaks).
test('terminals & tablets: the list and Update now (no Pi connected)', async ({ page, open }) => {
  await open('/store/devices');
  await expect(page.getByText('Terminals & tablets')).toBeVisible();
  await expect(page.getByTestId('floor-devices-head')).toContainText('Pi:');
  await expect(page.getByText('No terminals or tablets reported yet')).toBeVisible();

  // Asks first; Cancel sends nothing.
  let posted = 0;
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().includes('/me/terminals/update-now')) posted++;
  });
  await page.getByTestId('update-now').click();
  await expect(page.getByRole('dialog')).toContainText('Update the terminals now?');
  await page.getByRole('button', { name: 'Cancel' }).click();
  expect(posted).toBe(0);

  // Update now: the Pi isn't connected → main's answer on the page.
  await page.getByTestId('update-now').click();
  const answer = page.waitForResponse((r) => r.url().includes('/me/terminals/update-now'));
  await page.getByRole('dialog').getByRole('button', { name: 'Update now' }).click();
  const res = await answer;
  expect(res.status()).toBe(503);
  expect((await res.json()).code).toBe('agent_offline');
  await expect(page.getByTestId('update-result')).toContainText('The Pi is not connected');
  expect(posted).toBe(1);
});
