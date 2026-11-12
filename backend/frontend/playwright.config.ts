// Browser tests for the store manager's pages (Store › Website, Store › Payments) against a running
// main (dev by default; MAIN_BASE_URL + ADMIN_API_KEY for another). The built SPA is served by
// `vite preview` on a free port, proxying /store/v1 and /admin/v1 to main, so the session cookie is
// same-origin like in production. Each test signs in to a throwaway store of its own
// (tests/browser/fixtures.ts). Run: npm run test:browser (builds first).
// Browsers: `npx playwright install chromium` once.
import { defineConfig, devices } from '@playwright/test';
import { createServer } from 'node:net';

// One free port for the whole run: workers inherit it through the environment.
if (!process.env.PW_SPA_PORT) {
  process.env.PW_SPA_PORT = String(
    await new Promise<number>((resolve) => {
      const s = createServer().listen(0, () => {
        const p = (s.address() as { port: number }).port;
        s.close(() => resolve(p));
      });
    })
  );
}
const port = Number(process.env.PW_SPA_PORT);

export default defineConfig({
  testDir: 'tests/browser',
  outputDir: 'test-results/browser',
  snapshotPathTemplate: '{testDir}/__screenshots__/{projectName}/{testFilePath}/{arg}{ext}',
  fullyParallel: true,
  workers: process.env.CI ? 2 : 4,
  retries: 0,
  timeout: 45_000,
  expect: {
    timeout: 8_000,
    toHaveScreenshot: { maxDiffPixelRatio: 0.002, animations: 'disabled', caret: 'hide', scale: 'css' }
  },
  reporter: process.env.CI ? 'line' : [['list'], ['html', { outputFolder: 'test-results/browser-html', open: 'never' }]],
  webServer: {
    command: `npx vite preview --port ${port} --strictPort`,
    url: `http://localhost:${port}/app/`,
    reuseExistingServer: false,
    timeout: 60_000
  },
  use: {
    baseURL: `http://localhost:${port}`,
    reducedMotion: 'reduce',
    locale: 'en-US',
    timezoneId: 'America/New_York',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure'
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1360, height: 900 } } },
    { name: 'phone', use: { ...devices['Pixel 7'], browserName: 'chromium' } }
  ]
});
