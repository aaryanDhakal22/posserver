// Typed clients for main's two API surfaces. Calls are same-origin absolute
// paths (NOT under SvelteKit's /app base) — in dev Vite proxies them to :1323,
// in prod the Go binary serves both the app and the API.
//
//   adminApi  → Authorization: Bearer <ADMIN_API_KEY from localStorage>
//   storeApi  → credentials:'include' (httpOnly session cookie)
//
// One 401 handler for each: an admin 401 forgets the stored key, a store 401
// means the session is gone — both send the person to /app/login?next=….

import { goto } from '$app/navigation';
import { base } from '$app/paths';

export const ADMIN_KEY_STORAGE = 'quicc_admin_key';

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  config: Record<string, unknown>;
  username: string;
  has_login: boolean;
  /** External vendor store id (e.g. 3282) — online orders route by it; null = store_name fallback only. */
  store_id: number | null;
  created_at: string;
}

/** Parse a store-id form field: '' → null, a positive integer → number, else undefined (invalid). */
export function parseStoreId(raw: string): number | null | undefined {
  const s = raw.trim();
  if (!s) return null;
  if (!/^\d+$/.test(s)) return undefined;
  const n = Number(s);
  return Number.isSafeInteger(n) && n > 0 ? n : undefined;
}

export interface Location {
  id: string;
  tenant_id: string;
  slug: string;
  name: string;
  timezone: string;
  daily_reset_hour: number;
  phone: string;
  address_line1: string;
  address_line2: string;
  config: Record<string, unknown>;
  created_at: string;
}

export interface Device {
  id: string;
  tenant_id: string;
  location_id: string;
  name: string;
  kind: string;
  key_prefix: string;
  created_at: string;
  last_used_at?: string | null;
  revoked_at?: string | null;
}

export interface Printer {
  id: string;
  tenant_id: string;
  location_id: string;
  name: string;
  ip: string;
  /** The location's receipt printer (full customer receipt); others are kitchen stations. */
  is_receipt: boolean;
  created_at: string;
  updated_at: string;
}

export interface Overview {
  tenants: Tenant[];
  locations: Location[];
  devices: Device[];
}

export interface Minted {
  device: Device;
  api_key: string;
}

export interface Whoami {
  tenant: Tenant;
  location: Location;
  /** True when an admin opened this store ("Open as store") — not the manager. */
  impersonated?: boolean;
}

/** POST /admin/v1/tenants/:id/store-session — the cookie rides on the response. */
export interface StoreSessionGrant {
  tenant_id: string;
  location_id: string;
  tenant_name: string;
  expires_in_seconds: number;
}

export class ApiError extends Error {
  status: number;
  /** Seconds from a 429's Retry-After header, when present. */
  retryAfter?: number;
  constructor(status: number, message: string, retryAfter?: number) {
    super(message);
    this.status = status;
    this.retryAfter = retryAfter;
  }
}

/** A person-readable message for any thrown value. */
export function errText(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}

async function parseError(res: Response): Promise<string> {
  const text = await res.text();
  try {
    return (JSON.parse(text).error as string) || text;
  } catch {
    return text || res.statusText;
  }
}

async function toApiError(res: Response): Promise<ApiError> {
  const ra = Number(res.headers.get('Retry-After'));
  return new ApiError(res.status, await parseError(res), Number.isFinite(ra) && ra > 0 ? ra : undefined);
}

export function getAdminKey(): string {
  try {
    return (typeof localStorage !== 'undefined' && localStorage.getItem(ADMIN_KEY_STORAGE)) || '';
  } catch {
    return '';
  }
}
export function setAdminKey(k: string): void {
  localStorage.setItem(ADMIN_KEY_STORAGE, k);
}
function clearAdminKey(): void {
  try {
    localStorage.removeItem(ADMIN_KEY_STORAGE);
  } catch {
    /* ignore */
  }
}

/** Send the person to the login screen, remembering where they were. */
export function goToLogin(): void {
  if (typeof location === 'undefined') return;
  if (location.pathname.startsWith(base + '/login')) return;
  const next = location.pathname + location.search;
  goto(`${base}/login?next=${encodeURIComponent(next)}`, { replaceState: true });
}

/** A same-app path to return to after login, or '' if `raw` isn't one (no open redirects). */
export function safeNext(raw: string | null, prefix: string): string {
  if (!raw || !raw.startsWith(base + prefix) || raw.startsWith('//')) return '';
  return raw;
}

// --- admin (Bearer ADMIN_API_KEY) ---------------------------------------
export async function adminApi<T = unknown>(path: string, opts: RequestInit = {}): Promise<T> {
  const key = getAdminKey();
  if (!key) {
    goToLogin();
    throw new ApiError(401, 'Sign in as admin first.');
  }
  const res = await fetch('/admin/v1' + path, {
    ...opts,
    headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json', ...(opts.headers || {}) }
  });
  if (res.status === 401) {
    clearAdminKey();
    goToLogin();
    throw new ApiError(401, 'Your admin sign-in expired — sign in again.');
  }
  if (!res.ok) throw await toApiError(res);
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

/** Check an ADMIN_API_KEY against main and remember it. Throws ApiError(401) for a wrong key. */
export async function adminSignIn(key: string): Promise<void> {
  const k = key.trim();
  const res = await fetch('/admin/v1/tenants', { headers: { Authorization: 'Bearer ' + k } });
  if (res.status === 401) throw new ApiError(401, 'Wrong admin key');
  if (!res.ok) throw await toApiError(res);
  setAdminKey(k);
}

/** Forget the admin key (and drop any store session opened with "Open as store"). */
export async function signOutAdmin(): Promise<void> {
  clearAdminKey();
  await storeSignOut();
}

/** Open a store as its manager: main sets the (impersonated) session cookie. */
export async function openAsStore(tenantId: string): Promise<StoreSessionGrant> {
  try {
    return await adminApi<StoreSessionGrant>(`/tenants/${tenantId}/store-session`, {
      method: 'POST',
      credentials: 'include'
    });
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) throw new ApiError(404, 'That store no longer exists.');
    if (e instanceof ApiError && e.status === 409)
      throw new ApiError(409, "This store has no location yet, so it can't be opened.");
    throw e;
  }
}

// --- store (session cookie) ---------------------------------------------
export interface StoreApiOptions {
  /** false: a 401 is just thrown (login form, session probes) instead of redirecting. */
  redirectOn401?: boolean;
}

export async function storeApi<T = unknown>(
  path: string,
  opts: RequestInit = {},
  { redirectOn401 = true }: StoreApiOptions = {}
): Promise<T> {
  const res = await fetch('/store/v1' + path, {
    ...opts,
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) }
  });
  if (res.status === 401 && redirectOn401) {
    goToLogin();
    throw new ApiError(401, 'Your session ended — sign in again.');
  }
  if (!res.ok) throw await toApiError(res);
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

/** Manager sign-in. A 429 carries a friendly "try again in …" message. */
export async function storeSignIn(username: string, password: string): Promise<void> {
  try {
    await storeApi('/session', { method: 'POST', body: JSON.stringify({ username, password }) }, { redirectOn401: false });
  } catch (e) {
    if (e instanceof ApiError && e.status === 429) {
      const wait = e.retryAfter ? ` Try again in ${retryText(e.retryAfter)}.` : ' Try again in a few minutes.';
      throw new ApiError(429, 'Too many sign-in attempts.' + wait, e.retryAfter);
    }
    if (e instanceof ApiError && e.status === 401) throw new ApiError(401, 'Wrong username or password');
    throw e;
  }
}

/** End the store session (manager logout / leave "Open as store"). Never throws. */
export async function storeSignOut(): Promise<void> {
  try {
    await storeApi('/session', { method: 'DELETE' }, { redirectOn401: false });
  } catch {
    /* already gone */
  }
}

/** Whoami without the 401 redirect — null when there's no session. */
export async function probeStoreSession(): Promise<Whoami | null> {
  try {
    return await storeApi<Whoami>('/session', {}, { redirectOn401: false });
  } catch {
    return null;
  }
}

function retryText(sec: number): string {
  if (sec < 90) return `${Math.ceil(sec)} seconds`;
  return `${Math.ceil(sec / 60)} minutes`;
}

export function fmtTime(t?: string | null): string {
  if (!t) return '—';
  try {
    return new Date(t).toLocaleString();
  } catch {
    return t;
  }
}

// --- menu module types ---------------------------------------------------
export interface Menu {
  id: string; tenant_id: string; location_id: string;
  name: string; description: string; available: boolean; position: number;
}
export interface Category {
  id: string; tenant_id: string; location_id: string; menu_id: string;
  name: string; description: string; available: boolean; position: number;
  default_printer_id: string | null;
}
export interface Item {
  id: string; tenant_id: string; location_id: string; category_id: string;
  name: string; description: string; price_cents: number; tax_rate_bps: number;
  in_stock: boolean; out_until: string | null; tags: string[];
  printer_id: string | null; position: number; attributes: Record<string, unknown>;
}
export interface ModifierGroup {
  id: string; tenant_id: string; location_id: string; name: string;
  required: boolean; min_select: number; max_select: number; allow_multiple: boolean;
}
export interface ModifierOption {
  id: string; tenant_id: string; location_id: string; group_id: string;
  name: string; price_delta_cents: number; in_stock: boolean; position: number;
}
export interface ItemGroupLink { group: ModifierGroup; attach_position: number; }

/** An order as main stores it, with the floor fields the agent synced. */
export interface StoreOrder {
  id: string;
  source_event_id: string;
  status: string; // received | accepted | ready | on_delivery | completed | …
  customer: { display_name?: string };
  fulfillment: { type: string };
  money: { total_cents: number };
  received_at: string;
  kitchen_order_number?: number;
  estimated_ready_at?: string;
  driver_name?: string;
}
/** A recent vendor item name that doesn't reach a kitchen printer. */
export interface MenuImportResult {
  dry_run: boolean; replace: boolean; rows: number; menus_deleted: number; menus: number;
  categories: number; category_printers: number; items: number; items_updated: number;
  item_printers: number; changes: string[];
  /** Where the file's items will print (own printer, else category default). */
  prints_on?: { printer: string; items: number }[];
  /** File items left with no printer — they print on every station. */
  items_no_printer?: number;
}
export interface UnmatchedName { name: string; routing: 'unmatched' | 'unassigned'; orders: number; last_seen: string; }
export interface FloorDriver { id: string; name: string; position: number; status: string; updated_at: string; }
export interface StoreFloor { drivers: FloorDriver[]; last_sync_at: string | null; }

export interface LivePrinter {
  name: string; ip: string; up: boolean; last_checked?: string | null; last_error?: string;
}
export interface LivePrinters {
  agent_online: boolean; printers: LivePrinter[]; reported_at?: string; age_seconds?: number;
}

// --- operators (admin) ---------------------------------------------------
export interface OperatorAdmin {
  id: string; username: string; display_name: string; location_ids: string[]; created_at: string;
}

// --- tablet diagnostics (admin) -------------------------------------------
/** One Quibase troubleshooting report as the admin list shows it. */
export interface DiagSummary {
  id: string;
  tenant_id: string;
  location_id: string;
  store_name: string;
  tablet_id: string;
  tablet_name: string;
  via: 'pi' | 'direct';
  kind: string; // setup | guide | manual | full_check
  scenario: string; // pi_unreachable | printer | no_orders | pi_swap | setup | '' (none)
  outcome: string; // ok | fixed | needs_part | needs_owner | not_fixed | abandoned | info
  headline: string;
  cause: string;
  fixed_by: string;
  app_version: string;
  agent_version: string;
  created_at: string;
  received_at: string;
  paged_at: string | null;
  resolved_at: string | null;
}
export interface DiagStep { id?: string; at?: string; answer?: string; result?: string; [k: string]: unknown }
export interface DiagCheck { status?: string; plain?: string; details?: string; [k: string]: unknown }
/** The stored report v1 (fields may be missing; newer tablets may add more). */
export interface DiagReportDoc {
  v?: number;
  id?: string;
  created_at?: string;
  kind?: string;
  scenario?: string | null;
  outcome?: string;
  headline?: string;
  cause?: string;
  fixed_by?: string;
  note?: string;
  steps?: DiagStep[];
  checks?: Record<string, DiagCheck>;
  tablet?: Record<string, unknown> & { checks?: Record<string, unknown> };
  pi?: Record<string, unknown>;
  log?: string[];
  [k: string]: unknown;
}
export interface DiagDetail extends DiagSummary { report: DiagReportDoc; }
export interface TabletToken {
  id: string; tenant_id: string; location_id: string; location_name: string;
  tablet_id: string; tablet_name: string; key_prefix: string;
  created_at: string; last_used_at: string | null; revoked_at: string | null;
}

/** "1 menu", "2 menus", "3 categories" (pass `many` for irregular plurals). */
export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`;
}

export function dollars(cents: number): string {
  return '$' + ((cents || 0) / 100).toFixed(2);
}
/**
 * Parse a dollar amount typed by a person ("5.99", "$5.99", " 5 ", "-1.50",
 * "-$1.50") into cents. Returns null for anything else ("", "abc", "5,99") so
 * callers can show an error instead of silently saving $0.
 */
export function toCents(dollarsStr: string): number | null {
  const m = /^(-?)\s*\$?\s*(\d+(?:\.\d*)?|\.\d+)$/.exec(dollarsStr.trim());
  if (!m) return null;
  const cents = Math.round(parseFloat(m[2]) * 100);
  if (!Number.isSafeInteger(cents)) return null;
  return m[1] ? -cents : cents;
}

// --- fleet / admin order list ----------------------------------------------
/** GET /admin/v1/agents/health — one row per location that has reported. */
export interface AgentHealth {
  location_id: string;
  tenant_id: string;
  location_name: string;
  connected: boolean;
  at: string;
  /** The agent's raw heartbeat JSON — read defensively (older agents send less). */
  health: Record<string, unknown> | null;
}
/** Version + printer counts out of an agent heartbeat, when present. */
export function healthInfo(h: AgentHealth | undefined | null): {
  version: string;
  printersUp: number | null;
  printersDown: number | null;
  problems: string[];
} {
  const raw = (h?.health && typeof h.health === 'object' ? h.health : {}) as Record<string, unknown>;
  const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
  return {
    version: typeof raw.version === 'string' ? raw.version : '',
    printersUp: num(raw.printers_up),
    printersDown: num(raw.printers_down),
    problems: Array.isArray(raw.problems) ? raw.problems.filter((p): p is string => typeof p === 'string') : []
  };
}

/** GET /admin/v1/orders — the cross-tenant summary rows. */
export interface AdminOrderSummary {
  id: string;
  tenant_id: string;
  location_id: string;
  source: string;
  source_event_id: string;
  status: string;
  customer_name: string;
  total_cents: number;
  received_at: string;
}

/** True when `t` falls on today's local date. */
export function isToday(t: string | null | undefined): boolean {
  if (!t) return false;
  const d = new Date(t);
  const n = new Date();
  return d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
}

/** "4 s ago", "12 min ago", "2 h 14 min ago", "Oct 19". */
export function timeAgo(t: string | null | undefined): string {
  if (!t) return '—';
  const ms = Date.now() - new Date(t).getTime();
  if (!Number.isFinite(ms)) return '—';
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s} s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h} h ${m % 60} min ago` : `${h} h ago`;
  return new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' });
}

/** Clock time, "11:02 AM". */
export function hhmm(t?: string | null): string {
  return t ? new Date(t).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : '';
}

/** Cents → "12.50" (no $), for editable price fields. */
export function centsInput(cents: number): string {
  return ((cents || 0) / 100).toFixed(2);
}

/** "45 s", "12 min", "2 h 14 min", "3 days" since `t`. */
export function durationSince(t: string | null | undefined): string {
  if (!t) return '—';
  const s = Math.max(0, Math.round((Date.now() - new Date(t).getTime()) / 1000));
  if (!Number.isFinite(s)) return '—';
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
  return `${Math.floor(h / 24)} days`;
}
