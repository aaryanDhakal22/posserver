// Typed clients for main's two API surfaces. Calls are same-origin absolute
// paths (NOT under SvelteKit's /app base) — in dev Vite proxies them to :1323,
// in prod the Go binary serves both the app and the API.
//
//   adminApi  → Authorization: Bearer <ADMIN_API_KEY from localStorage>
//   storeApi  → credentials:'include' (httpOnly session cookie)

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
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function parseError(res: Response): Promise<string> {
  const text = await res.text();
  try {
    return (JSON.parse(text).error as string) || text;
  } catch {
    return text || res.statusText;
  }
}

export function getAdminKey(): string {
  return (typeof localStorage !== 'undefined' && localStorage.getItem(ADMIN_KEY_STORAGE)) || '';
}
export function setAdminKey(k: string): void {
  localStorage.setItem(ADMIN_KEY_STORAGE, k);
}

// --- admin (Bearer ADMIN_API_KEY) ---------------------------------------
export async function adminApi<T = unknown>(path: string, opts: RequestInit = {}): Promise<T> {
  const key = getAdminKey();
  if (!key) throw new ApiError(0, 'Set the ADMIN_API_KEY first.');
  const res = await fetch('/admin/v1' + path, {
    ...opts,
    headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json', ...(opts.headers || {}) }
  });
  if (!res.ok) throw new ApiError(res.status, await parseError(res));
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// --- store (session cookie) ---------------------------------------------
export async function storeApi<T = unknown>(path: string, opts: RequestInit = {}): Promise<T> {
  const res = await fetch('/store/v1' + path, {
    ...opts,
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) }
  });
  if (!res.ok) throw new ApiError(res.status, await parseError(res));
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
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

export function dollars(cents: number): string {
  return '$' + ((cents || 0) / 100).toFixed(2);
}
export function toCents(dollarsStr: string): number {
  const n = parseFloat(dollarsStr);
  return isNaN(n) ? 0 : Math.round(n * 100);
}
