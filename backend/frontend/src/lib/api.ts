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
  /** Store pin (null until geocoded from the address or placed by hand). */
  lat: number | null;
  lng: number | null;
  /** '' (not placed) | geocoded | manual (a hand-placed pin; geocoding never moves it). */
  geo_source: '' | 'geocoded' | 'manual';
  /** Orders farther than this from the store are tagged "outside delivery area". */
  delivery_radius_m: number;
  /** The offline map file the Pi downloads; null = none published. */
  map: StoreMapFile | null;
}

export interface StoreMapFile {
  url: string;
  sha256: string;
  version: string;
  /** [minLng, minLat, maxLng, maxLat] */
  bbox: number[];
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
  /** Hardware address the Pi learned ('' = not known yet) — how it follows the printer to a new IP. */
  mac?: string;
  created_at: string;
  updated_at: string;
}

/** A printer's name as the menu shows it: items on the receipt printer print
 * only on the receipt (agent ≥ 2.0.5) unless it is the store's only printer. */
export function printerLabel(p: Printer, all: Printer[]): string {
  return p.is_receipt && all.length > 1 ? `${p.name} (receipt only)` : p.name;
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
  /** The body's machine-readable `code` (e.g. "pin_taken"), when present. */
  code?: string;
  constructor(status: number, message: string, retryAfter?: number, code?: string) {
    super(message);
    this.status = status;
    this.retryAfter = retryAfter;
    this.code = code;
  }
}

/** A person-readable message for any thrown value. */
export function errText(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}

async function parseError(res: Response): Promise<{ message: string; code?: string }> {
  const text = await res.text();
  try {
    const j = JSON.parse(text);
    return { message: (j.error as string) || text, code: typeof j.code === 'string' ? j.code : undefined };
  } catch {
    return { message: text || res.statusText };
  }
}

async function toApiError(res: Response): Promise<ApiError> {
  const ra = Number(res.headers.get('Retry-After'));
  const { message, code } = await parseError(res);
  return new ApiError(res.status, message, Number.isFinite(ra) && ra > 0 ? ra : undefined, code);
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
  /** "No kitchen ticket" (00041): its items without their own printer print on the receipt only. */
  no_kitchen_ticket?: boolean;
}
export interface Item {
  id: string; tenant_id: string; location_id: string; category_id: string;
  name: string; description: string; price_cents: number; tax_rate_bps: number;
  in_stock: boolean; out_until: string | null; tags: string[];
  printer_id: string | null; position: number; attributes: Record<string, unknown>;
  /** "No kitchen ticket" (00041): no station ticket, not in "Also on this order"; receipt only. */
  no_kitchen_ticket?: boolean;
}
export interface ModifierGroup {
  id: string; tenant_id: string; location_id: string; name: string;
  required: boolean; min_select: number; max_select: number; allow_multiple: boolean;
}
export interface ModifierOption {
  id: string; tenant_id: string; location_id: string; group_id: string;
  name: string; price_delta_cents: number; in_stock: boolean; position: number;
  /** Print on the delivery (driver) ticket even at $0 (00046). */
  show_on_driver_ticket?: boolean;
  /** Its mod family is regular: a coupon's free toppings can include it. */
  counts_for_coupons?: boolean;
}
/** PUT …/options/:id/coupons — an option's switches after the change. */
export interface OptionFlags {
  option_id: string; show_on_driver_ticket: boolean; counts_for_coupons: boolean;
  family_id: string | null; family_name?: string;
}
export interface ItemGroupLink { group: ModifierGroup; attach_position: number; }

// --- the menu bank + views (main 3.0) --------------------------------------
/** One of the store-wide sizes ("12 Medium"). */
export interface MenuSize { id: string; location_id: string; name: string; short: string; position: number; }
export interface ItemSize { size_id: string; price_cents: number; kitchen_name?: string; position?: number; }
export interface OptionPrice { size_id: string; price_cents: number; }
/** GET /me/menu/bank — the bank as views compile from it. */
export interface BankItem extends Item { kitchen_name: string; sku: string; sizes: ItemSize[]; group_ids: string[]; family_id?: string | null; }
export interface BankOption extends ModifierOption { is_default: boolean; kitchen_name: string; prices: OptionPrice[]; family_id?: string | null; }
export interface BankGroup extends ModifierGroup { portions: boolean; options: BankOption[]; }
export interface Bank {
  location_id: string; version: number; sizes: MenuSize[];
  menus: (Menu & { categories: (Category & { items: BankItem[] })[] })[];
  groups: Record<string, BankGroup>;
  families?: MenuFamily[];
}

// --- Families (00045): item / mod families; a mod family's class decides what a coupon may make free ---
export type FamilyKind = 'item' | 'mod';
export type FamilyClass = '' | 'regular' | 'premium' | 'unpriced';
export interface MenuFamily { id: string; location_id: string; kind: FamilyKind; name: string; class: FamilyClass; position: number }
export interface FamilyRow extends MenuFamily { items: number; options: number }
export interface FamiliesListing {
  families: FamilyRow[];
  items: { id: string; name: string; category: string; family_id: string | null }[];
  options: { id: string; name: string; group: string; group_id: string; price_cents: number; family_id: string | null }[];
}
export const FAMILY_CLASSES: { value: FamilyClass; label: string; help: string }[] = [
  { value: 'regular', label: 'Regular', help: 'A coupon may make these free (e.g. pizza toppings at the normal price)' },
  { value: 'premium', label: 'Premium', help: 'Never free in a coupon (steak, grilled chicken, add-ons)' },
  { value: 'unpriced', label: 'Unpriced', help: 'No price: halves, choices, notes, "no X"' }
];
export type ViewKind = 'web' | 'operator' | 'counter' | 'vendor';
export interface ViewRules { markup_bps: number; round: 'none' | 'ceil_99'; hidden_categories: string[]; require_photo: boolean; }
export interface MenuView {
  id: string; tenant_id: string; location_id: string; kind: ViewKind; name: string; rules: ViewRules;
  created_at: string; updated_at: string;
}
export type EntryKind = 'item' | 'merge' | 'augment' | 'map';
export interface ViewEntry {
  id: string; view_id: string; kind: EntryKind;
  anchor: { item_ids: string[]; size_id?: string };
  display_name: string; description: string; photo: string; category: string; position: number; hidden: boolean;
  price_override_cents: number | null;
  extra: { size_prices?: Record<string, number>; labels?: Record<string, string>; vendor_name?: string; vendor_size?: string };
}
export interface NeedsAttention { code: string; entry_id?: string; item_id?: string; name?: string; message: string; orders?: number; }
export interface BuildItem {
  id: string; name: string; description: string; photo: string; anchor: { item_ids: string[] };
  price_cents: number; sizes: { size_id: string; name: string; price_cents: number }[]; sold_out: boolean;
  choices?: { item_id: string; name: string; price_cents: number; sold_out: boolean }[];
}
/** GET /me/menu/views/:id/build — a view as its channels see it. */
export interface ViewBuild {
  view: { id: string; kind: ViewKind; name: string }; version: number;
  categories: { key: string; name: string; items: BuildItem[] }[];
  maps?: { entry_id: string; vendor_name: string; vendor_size: string; item_id: string }[];
  needs_attention: NeedsAttention[];
}
export const VIEW_KINDS: { kind: ViewKind; label: string; help: string }[] = [
  { kind: 'web', label: 'Website', help: 'What customers see on the ordering site' },
  { kind: 'operator', label: 'Call center', help: 'What the call-center operators see' },
  { kind: 'counter', label: 'Counter', help: 'What a counter POS terminal sees' },
  { kind: 'vendor', label: 'Brygid', help: 'Matches the vendor’s item names to your items' }
];
/** "12 Medium $11.99 · 16 X-Large $15.99" for an item's sizes. */
export function sizesLine(sizes: ItemSize[] | undefined, all: MenuSize[]): string {
  return (sizes || [])
    .map((s) => `${all.find((x) => x.id === s.size_id)?.name ?? '?'} ${dollars(s.price_cents)}`)
    .join(' · ');
}

/** An order as main stores it, with the floor fields the agent synced. */
export interface StoreOrder {
  id: string;
  source_event_id: string;
  status: string; // received | scheduled | accepted | ready | on_delivery | completed | …
  customer: { display_name?: string };
  fulfillment: { type: string };
  money: { total_cents: number };
  /** The order's payments; tip_cents = the tip taken with one (never in the total). */
  payments?: { method: string; amount_cents: number; tip_cents?: number }[];
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
  /** File items with "no kitchen ticket" (receipt only). */
  items_no_kitchen?: number;
  item_no_kitchen_changed?: number;
}
export interface UnmatchedName { name: string; routing: 'unmatched' | 'unassigned'; orders: number; last_seen: string; }
export interface FloorDriver { id: string; name: string; position: number; status: string; updated_at: string; }
export interface StoreFloor { drivers: FloorDriver[]; last_sync_at: string | null; }

export interface LivePrinter {
  name: string; ip: string; mac?: string; up: boolean; last_checked?: string | null; last_error?: string;
}
/** A printer the Pi found on its network (TCP 9100) that isn't set up yet (agent ≥ 2.0.6). */
export interface FoundPrinter { ip: string; mac: string; }
export interface LivePrinters {
  agent_online: boolean; printers: LivePrinter[]; found?: FoundPrinter[]; reported_at?: string; age_seconds?: number;
}
/** GET /store/v1/me/printers/ip-changes — the Pi following a printer, or a manager edit. */
export interface PrinterIPChange {
  id: string; printer_id: string; printer_name: string;
  old_ip: string; new_ip: string; mac: string; source: 'agent' | 'manager'; at: string;
}

// --- operators (admin) ---------------------------------------------------
export interface OperatorAdmin {
  id: string; username: string; display_name: string; location_ids: string[]; created_at: string;
}

// --- sales channels (admin, main 3.0) -------------------------------------
export type ChannelKind = 'web' | 'operator' | 'counter' | 'vendor';
export interface ChannelPermissions {
  menu: boolean; quote: boolean; orders: boolean; track: boolean; payments: ('card' | 'cash')[];
}
/** A store's sales channel; `key` only on create / rotate (shown once). */
export interface ChannelAdmin {
  id: string; tenant_id: string; location_id: string; kind: ChannelKind; name: string;
  key_prefix: string; permissions: ChannelPermissions; builtin: boolean; active: boolean;
  revoked_at: string | null; last_used_at: string | null; created_at: string; updated_at: string;
  key?: string;
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
/** The Pi's own update view out of its heartbeat (agent ≥ 2.0.6; older agents send none). */
export interface HealthUpdate {
  currentVersion: string;
  latestVersion: string;
  updateAvailable: boolean;
  /** idle | waiting_for_idle | requested | running | ok | up_to_date | failed | rolled_back */
  state: string;
  /** The Pi's last finished update / roll back, when it has one. */
  last: { action: string; state: string; to_version: string; error: string; finished_at: string } | null;
}

/** Version + printer counts out of an agent heartbeat, when present. */
export function healthInfo(h: AgentHealth | undefined | null): {
  version: string;
  printersUp: number | null;
  printersDown: number | null;
  problems: string[];
  update: HealthUpdate | null;
} {
  const raw = (h?.health && typeof h.health === 'object' ? h.health : {}) as Record<string, unknown>;
  const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
  const str = (v: unknown) => (typeof v === 'string' ? v : '');
  const obj = (v: unknown) => (v && typeof v === 'object' ? (v as Record<string, unknown>) : null);
  const up = obj(raw.update);
  const last = obj(up?.last);
  return {
    version: str(raw.version),
    printersUp: num(raw.printers_up),
    printersDown: num(raw.printers_down),
    problems: Array.isArray(raw.problems) ? raw.problems.filter((p): p is string => typeof p === 'string') : [],
    update: up
      ? {
          currentVersion: str(up.current_version),
          latestVersion: str(up.latest_version),
          updateAvailable: up.update_available === true,
          state: str(up.state),
          last: last
            ? { action: str(last.action), state: str(last.state), to_version: str(last.to_version), error: str(last.error), finished_at: str(last.finished_at) }
            : null
        }
      : null
  };
}

/** GET /admin/v1/agent-updates — one Pi update / roll back. */
export interface AgentUpdate {
  id: string;
  tenant_id: string;
  location_id: string;
  action: 'update' | 'rollback';
  /** admin | console | pi-web | boot */
  requested_by: string;
  force: boolean;
  /** requested | waiting_for_idle | running | ok | up_to_date | failed | rolled_back | rejected | unreachable */
  state: string;
  from_version: string;
  to_version: string;
  error: string;
  requested_at: string;
  updated_at: string;
}
/** POST /admin/v1/agent/update|rollback → the Pi's answer. */
export interface AgentUpdateResult {
  id: string;
  accepted: boolean;
  /** requested | waiting_for_idle (accepted) · rejected (see code) · unreachable */
  state: string;
  /** '' | not_after_close | orders_open | update_in_progress | not_supported */
  code: string;
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

// --- store status (main 3.0) ------------------------------------------------
export interface HoursRange { open: string; close: string; }
export interface OrderingSettings {
  pickup_enabled: boolean; delivery_enabled: boolean;
  pickup_eta_minutes: number; delivery_eta_minutes: number; schedule_days: number;
  paused_at: string | null; paused_until: string | null; pause_note: string;
  /** The website's tip limit (and a tip added after the order), cents; default $50. */
  tip_max_cents: number;
}
export interface HoursException { id: string; date: string; closed: boolean; hours: HoursRange[]; note: string; }
/** GET /me/status — what every channel sees. */
export interface StoreStatus {
  store: { name: string; timezone: string; phone: string; address_line1: string; address_line2: string };
  state: 'open' | 'paused' | 'closed';
  accepting: boolean; open_now: boolean; hours_set: boolean;
  pause: { since: string; until: string | null; note: string } | null;
  next_open_at: string | null; closes_at: string | null;
  today: { date: string; closed: boolean; hours: HoursRange[]; note: string };
  order_types: { pickup: { enabled: boolean; eta_minutes: number }; delivery: { enabled: boolean; eta_minutes: number; radius_m?: number } };
  scheduling: { days_ahead: number; slot_minutes: number };
  sold_out: { item_id: string; name: string; until: string | null }[];
  menu_version: number;
}
export const PAUSE_PRESETS = [
  { value: '15m', label: '15 min' },
  { value: '30m', label: '30 min' },
  { value: '60m', label: '1 hour' },
  { value: 'today', label: 'Rest of today' },
  { value: 'indefinite', label: 'Until I resume' }
];
/** "7:30 PM" today, else "Tue 7:30 PM", in the store's zone. */
export function storeTime(iso: string | null | undefined, tz: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  const opts: Intl.DateTimeFormatOptions = { hour: 'numeric', minute: '2-digit', timeZone: tz || undefined };
  const sameDay = new Date().toLocaleDateString('en-US', { timeZone: tz || undefined }) === d.toLocaleDateString('en-US', { timeZone: tz || undefined });
  if (!sameDay) opts.weekday = 'short';
  try {
    return d.toLocaleTimeString('en-US', opts);
  } catch {
    return d.toLocaleTimeString();
  }
}

// --- pricing + deals (main 3.0) ---------------------------------------------
export interface PricingFees {
  service_fee_cents: number; service_fee_bps: number; delivery_fee_cents: number;
  free_delivery_over_cents: number | null; delivery_min_cents: number;
  /** The driver's share of delivery_fee_cents (the rest is the store's); null/absent = $3, capped at the fee. */
  delivery_driver_cents?: number | null;
}
export interface PricingTips { enabled: boolean; presets: number[]; default_pct: number }
export interface PricingStore {
  tax_bps: number; stacking: 'best' | 'stack' | 'single'; fees: PricingFees; tips: PricingTips; card_fee: { mode: string; bps: number };
  /** Half and half: a topping on a half at full or half price; the cheaper of two half-specialties' extra over plain at this % off. */
  half_topping_price: 'full' | 'half'; half_specialty_off_pct: number;
}
export interface PricingKind { fees: PricingFees | null; tips: PricingTips | null; card_fee: { mode: string; bps: number } | null }
export interface PricingBody { store: PricingStore; kinds: Record<string, PricingKind> }
export type DealKind = 'bundle' | 'mix' | 'nth' | 'code' | 'free_item' | 'coupon';
/** A coupon part: qty items of one of these item families (at one of these sizes, when any), the N cheapest regular mods free on each;
 *  upcharge_over: its base families (a unit of another of its families pays its price over the cheapest base item at its size). */
export interface CouponPart { label: string; families: string[]; sizes?: string[]; qty: number; regular_mods_included: number; upcharge_over?: string[] }
export interface DealEligible { items: string[]; categories: string[]; sizes: string[] }
export interface StoreDeal {
  id: string; kind: DealKind; name: string; description: string; active: boolean; fulfil: string;
  schedule: { days: string[]; start: string; end: string } | null;
  price_cents?: number; slots?: { label: string; eligible: DealEligible; included_options: number }[];
  eligible?: DealEligible | null; qty?: number; each_cents?: number; n?: number; pct?: number;
  code?: string; amount_cents?: number; first_order_only?: boolean; min_subtotal_cents?: number;
  card?: string[]; parts?: CouponPart[];
  channel_kinds: string[]; position?: number;
}
export const DEAL_KINDS: { kind: DealKind; label: string; help: string }[] = [
  { kind: 'bundle', label: 'Combo', help: 'Pick items for each step at one price (e.g. a 16" pizza + wings for $19.99)' },
  { kind: 'mix', label: 'Mix & match', help: 'Any N of these for $X each (e.g. any 2 pizzas $10 each)' },
  { kind: 'nth', label: 'Every Nth', help: 'Every Nth one is % off (e.g. 2nd wings half off)' },
  { kind: 'code', label: 'Promo code', help: 'A code customers type for % or $ off' },
  { kind: 'free_item', label: 'Free item', help: 'One of these free once the order reaches $Y' },
  { kind: 'coupon', label: 'Coupon', help: 'The cashier applies it when the order has every part (by family): the priciest item carries the price, the rest $0; N regular toppings free' }
];
export const CHANNEL_KIND_LABELS: Record<string, string> = { web: 'Website', operator: 'Call center', counter: 'Counter', vendor: 'Vendor' };

// --- Payments (weborder stage 2): Square + online card holds ----------------------

export interface SquareLocation {
  id: string;
  name: string;
  status: string;
  currency: string;
}

export interface SquareStatus {
  enabled: boolean;
  env: 'production' | 'sandbox' | 'fake' | 'off';
  app_id: string;
  connected: boolean;
  state: '' | 'connected' | 'revoked' | 'error';
  merchant_id: string;
  square_location_id: string;
  locations: SquareLocation[];
  last_error: string;
  connected_at: string | null;
  holds_open: number;
  /** The grant includes the counter card reader's scope (PAYMENTS_WRITE_IN_PERSON); false = reconnect once; null = unknown. */
  in_person?: boolean | null;
  scopes?: string[];
  /** This server runs Square's sandbox: "Paste a Square sandbox token" is offered (never in production). */
  sandbox_paste?: boolean;
  /** The connection is a pasted sandbox token. */
  pasted?: boolean;
}

export type HoldStatus = 'held' | 'captured' | 'cancelled' | 'failed';

export interface CardHoldRow {
  order_id: string;
  order_ref: string;
  customer: string;
  type: string;
  placed_at: string;
  order_status: string;
  gateway: string;
  payment_ref: string;
  amount_cents: number;
  tip_cents: number;
  added_tip_cents: number;
  status: HoldStatus;
  capture_reason: string;
  due_at: string;
  capture_requested_at: string | null;
  attempts: number;
  last_error: string;
  captured_cents: number;
  tip_payment_ref: string;
  tip_note: string;
  captured_at: string | null;
  cancelled_at: string | null;
  created_at: string;
}

// --- Website (Store › Website, weborder) ----------------------------------

export type BlockId = 'hero' | 'again' | 'categories' | 'deals' | 'picks' | 'hours' | 'about' | 'install';
export type MisoSpot = 'peek' | 'side' | 'off';

/** The website's settings document (main's site.Look; the storefront's defaults fill what's missing). */
export interface SiteLook {
  preset?: string;
  brand?: string;
  menu_layout?: '' | 'compact' | 'photo' | 'printed';
  languages?: string[];
  logo?: string;
  cover?: string;
  texts?: Partial<Record<'eyebrow' | 'headline' | 'lead' | 'about_title' | 'about' | 'eyebrow_es' | 'headline_es' | 'lead_es' | 'about_title_es' | 'about_es', string>>;
  blocks?: { id: BlockId; on: boolean }[];
  picks?: string[];
  announcement?: { on: boolean; text?: string; text_es?: string; from?: string; to?: string };
  miso_pages?: boolean;
  miso_blocks?: Partial<Record<BlockId, MisoSpot>>;
  privacy?: string;
}

export interface Website {
  draft: SiteLook;
  published: SiteLook;
  has_draft: boolean;
  changes: string[];
  version: number;
  draft_at: string | null;
  published_at: string | null;
  published_by: string;
  /** <origin>/p/<token>, + "/menu" etc. for another page ("" = no host yet) */
  preview_url: string;
  preview_host: string;
}

export interface SiteImage {
  id: string;
  kind: 'logo' | 'cover';
  content_type: string;
  size: number;
}

export interface SiteDomain {
  host: string;
  primary: boolean;
  verified: boolean;
  created_at: string;
}

// --- Store › Staff (counter terminal staff + terminal settings, contract §2.5zb) ---
export type StaffRole = 'cashier' | 'manager' | 'driver';
export interface StaffLimits { refund_cents?: number; discount_pct?: number }
export interface StaffEffective { refund_cents: number | null; discount_pct: number | null }
export interface Staff {
  id: string; first_name: string; last_name: string; role: StaffRole;
  limits: StaffLimits; effective_limits: StaffEffective; active: boolean; created_at: string; updated_at: string;
}
export type TerminalTabName = 'info' | 'counter' | 'takeout' | 'delivery' | 'pickup' | 'history' | 'dispatch';
export type ButtonFont =
  | 'barlow' | 'barlow_condensed' | 'barlow_semi_condensed' | 'system'
  | 'fira_sans_condensed' | 'ibm_plex_sans_condensed' | 'atkinson_hyperlegible';
export interface TerminalSettings {
  cashier_refund_cents: number; cashier_discount_pct: number; auto_lock_minutes: number; counter_start: number;
  receipt_auto_done_seconds: number; auto_close_batch_at: string;
  default_tab: TerminalTabName; show_history: boolean; button_font: ButtonFont; cashier_price_drop_pct: number;
  click_sound: boolean; prices_on_buttons: boolean; out_of_area_margin_mi: number; name_on_paid_counter: boolean;
  card_tips: boolean; card_tip_percents: number[]; employee_meal_pct: number; topping_max_count: number;
  cashier_payout_cents: number; default_pickup_minutes: number; area_codes: string[]; drawer_start_cents: number;
  waste_reasons: string[];
  void_reasons: string[];
}
/** The owner's defaults (main's staff.DefaultSettings): a field main doesn't send yet shows these. */
export const TERMINAL_DEFAULTS: TerminalSettings = {
  cashier_refund_cents: 2000, cashier_discount_pct: 20, auto_lock_minutes: 5, counter_start: 1, receipt_auto_done_seconds: 15,
  auto_close_batch_at: '04:00', default_tab: 'info', show_history: false, button_font: 'barlow', cashier_price_drop_pct: 20,
  click_sound: false, prices_on_buttons: false, out_of_area_margin_mi: 1, name_on_paid_counter: false, card_tips: false,
  card_tip_percents: [15, 18, 20], employee_meal_pct: 20, topping_max_count: 3,
  cashier_payout_cents: 2000, default_pickup_minutes: 15, area_codes: ['410', '443', '667'], drawer_start_cents: 10000,
  waste_reasons: ['Never picked up', 'Made wrong / remade', 'Customer complaint', 'Burnt / dropped'],
  void_reasons: ['Customer cancelled', 'Wrong order', 'Duplicate', 'Took too long', 'Other']
};
