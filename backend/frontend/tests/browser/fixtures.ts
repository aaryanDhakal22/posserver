// Playwright fixtures: a throwaway store on main per test (tenant + manager login + a small menu),
// signed in through the SPA's own origin so the session cookie lands where the app expects it.
// Deleted after the test, so tests share no rows and run in parallel.
import { test as base, expect, type Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { deflateSync } from 'node:zlib';

export { expect };

export const MAIN = (process.env.MAIN_BASE_URL ?? 'http://localhost:1323').replace(/\/+$/, '');

/** The admin key: ADMIN_API_KEY, else dev main's Doppler token file (like the pytest suites). */
export function adminKey(): string {
  if (process.env.ADMIN_API_KEY) return process.env.ADMIN_API_KEY;
  const tokenFile = new URL('../../../../.doppler-dev-main', import.meta.url).pathname;
  if (!existsSync(tokenFile)) return '';
  try {
    return execFileSync('doppler', ['secrets', 'get', 'ADMIN_API_KEY', '--plain'], {
      env: { ...process.env, DOPPLER_TOKEN: readFileSync(tokenFile, 'utf8').trim() }
    })
      .toString()
      .trim();
  } catch {
    return '';
  }
}

type Json = Record<string, any>;

async function req(method: string, url: string, headers: Record<string, string>, body?: unknown, expected?: number) {
  const r = await fetch(url, {
    method,
    headers: { ...headers, ...(body === undefined ? {} : { 'content-type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  const text = await r.text();
  const data = text ? JSON.parse(text) : null;
  if (expected !== undefined && r.status !== expected) throw new Error(`${method} ${url}: ${r.status} ${text.slice(0, 300)}`);
  return { status: r.status, data, headers: r.headers };
}

export class TestStore {
  tag = randomBytes(4).toString('hex');
  host = `t-${this.tag}.localhost`;
  tenantId = '';
  locationId = '';
  username = '';
  password = '';
  cookie = '';
  menu: Json = {};
  constructor(public admin: string) {}

  adminCall(method: string, path: string, body?: unknown, expected?: number) {
    return req(method, `${MAIN}/admin/v1${path}`, { authorization: `Bearer ${this.admin}` }, body, expected);
  }
  me(method: string, path: string, body?: unknown, expected?: number) {
    return req(method, `${MAIN}/store/v1/me${path}`, { cookie: this.cookie }, body, expected);
  }

  async create(name = 'Test Pizza') {
    const t = (await this.adminCall('POST', '/tenants', { slug: `t-${this.tag}`, name }, 201)).data;
    this.tenantId = t.id;
    this.locationId = t.location.id;
    this.username = `mgr${this.tag}`;
    this.password = randomBytes(9).toString('base64url');
    await this.adminCall('PATCH', `/tenants/${this.tenantId}/credentials`, { username: this.username, password: this.password }, 204);
    const s = await req('POST', `${MAIN}/store/v1/session`, {}, { username: this.username, password: this.password }, 200);
    this.cookie = (s.headers.get('set-cookie') ?? '').split(';')[0];
    await this.adminCall('POST', `/locations/${this.locationId}/site-domains`, { host: this.host }, 201);
    await this.adminCall('PATCH', `/site-domains/${this.host}`, { verified: true }, 200);
    await this.buildMenu();
    return this;
  }

  /** Pizza (two sizes), Wings and Fries: enough for the picks list. */
  private async buildMenu() {
    const M = (m: string, p: string, b?: unknown, e?: number) => this.me(m, `/menu${p}`, b, e).then((r) => r.data);
    const printer = (await this.me('POST', '/printers', { name: 'Kitchen', ip: '10.0.0.2' }, 201)).data;
    const menu = await M('POST', '/menus', { name: 'Main' }, 201);
    const pz = await M('POST', `/menus/${menu.id}/categories`, { name: 'Pizza', default_printer_id: printer.id }, 201);
    const sd = await M('POST', `/menus/${menu.id}/categories`, { name: 'Sides', default_printer_id: printer.id }, 201);
    const pizza = await M('POST', `/categories/${pz.id}/items`, { name: 'Cheese Pizza', price_cents: 1199 }, 201);
    const wings = await M('POST', `/categories/${sd.id}/items`, { name: 'Wings', price_cents: 1099 }, 201);
    const fries = await M('POST', `/categories/${sd.id}/items`, { name: 'Fries', price_cents: 399 }, 201);
    this.menu = { pizza, wings, fries, pz, sd };
  }

  async destroy() {
    if (!this.tenantId) return;
    let r = await this.adminCall('DELETE', `/tenants/${this.tenantId}`).catch(() => null);
    if (r && r.status === 400) {
      // A deadlock with a request that was still running (main answers the database's error as a
      // 400): once more, now that nothing else touches the store.
      r = await this.adminCall('DELETE', `/tenants/${this.tenantId}`).catch(() => null);
    }
    if (r && ![200, 204, 404].includes(r.status)) {
      throw new Error(`deleting test store ${this.tag}: ${r.status} ${JSON.stringify(r.data ?? '')}`);
    }
  }
}

const admin = adminKey();

async function mainUp() {
  try {
    return (await fetch(`${MAIN}/health`, { signal: AbortSignal.timeout(2000) })).ok;
  } catch {
    return false;
  }
}

type Fixtures = {
  /** The test's own store, signed in as its manager in the page's browser context. */
  store: TestStore;
  /** Opens an SPA path (under /app) as the store's manager. */
  open: (path: string) => Promise<void>;
};

export const test = base.extend<Fixtures>({
  store: async ({ page }, use) => {
    test.skip(!admin || !(await mainUp()), `main isn't reachable at ${MAIN} (or no admin key)`);
    const s = await new TestStore(admin).create();
    // Sign in through the SPA's origin (the preview server proxies /store/v1), like the login form.
    const r = await page.request.post('/store/v1/session', { data: { username: s.username, password: s.password } });
    expect(r.status()).toBe(200);
    await use(s);
    // Stop the page first: a request it still had in flight (a screen polling or saving) could
    // take locks the store's delete needs, and Postgres ends such a deadlock after 1 s (the delete
    // answered 400 once on grace, 2026-11-12).
    await page.goto('about:blank').catch(() => null);
    await s.destroy();
  },
  page: async ({ page }, use) => {
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    await use(page);
    expect(errors, 'uncaught errors in the page').toEqual([]);
  },
  open: async ({ page, store }, use) => {
    void store;
    await use(async (path: string) => {
      await page.goto(`/app${path}`);
    });
  }
});

/** A tiny valid PNG (w × h, one colour) for upload tests. */
export function png(w = 64, h = 64): Buffer {
  // Built on the fly so tests carry no binary fixtures.
  const row = Buffer.concat([Buffer.from([0]), Buffer.alloc(w * 3, 0xc0)]);
  const raw = Buffer.concat(Array.from({ length: h }, () => row));
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (b: Buffer) => {
    let c = 0xffffffff;
    for (const x of b) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type: string, data: Buffer) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    const c = Buffer.alloc(4);
    c.writeUInt32BE(crc(td));
    return Buffer.concat([len, td, c]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // RGB
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0))
  ]);
}
