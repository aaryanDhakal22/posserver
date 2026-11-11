// Everything the admin Overview / Stores pages show about each store, joined
// client-side from the flat admin endpoints (there is no per-tenant GET).
import {
  adminApi,
  healthInfo,
  isToday,
  type AdminOrderSummary,
  type AgentHealth,
  type Device,
  type DiagSummary,
  type Location,
  type Overview,
  type Tenant
} from '$lib/api';
import type { Tone } from '$lib/ui/Badge.svelte';

export type PiState = 'online' | 'offline' | 'none';

export interface StoreRow {
  tenant: Tenant;
  location: Location | null;
  health: AgentHealth | null;
  pi: PiState;
  /** Last sign of life from the Pi (heartbeat or key use). */
  lastSeen: string | null;
  version: string;
  /** The Pi says a newer agent is published for its env (agent ≥ 2.0.6). */
  updateAvailable: boolean;
  /** That newer version ('' when unknown). */
  latestVersion: string;
  printersUp: number | null;
  printersDown: number | null;
  problems: string[];
  ordersToday: number;
  /** main is refusing this store's ASAP channel orders (409 kitchen_offline): it has a live
   *  agent key but no Pi stream — e.g. a retired Pi whose key was never revoked. */
  kitchenOffline: boolean;
  /** Since when its Pi has been away (null when unknown or online). */
  kitchenAwaySince: string | null;
}

/** One row of GET /admin/v1/kitchen (the liveness check behind 409 kitchen_offline). */
export interface KitchenStatus {
  tenant_id: string;
  location_id: string;
  kitchen_offline: boolean;
  away_since: string | null;
}

export interface AdminData {
  overview: Overview;
  fleet: AgentHealth[];
  orders: AdminOrderSummary[];
  unresolved: DiagSummary[];
  kitchen: KitchenStatus[];
  rows: StoreRow[];
}

const latest = (a: string | null | undefined, b: string | null | undefined) =>
  !a ? (b ?? null) : !b ? a : new Date(a) > new Date(b) ? a : b;

export function piStateFor(loc: Location | null, fleet: AgentHealth[], devices: Device[]) {
  if (!loc) return { pi: 'none' as PiState, health: null, lastSeen: null };
  const health = fleet.find((f) => f.location_id === loc.id) ?? null;
  const agents = devices.filter((d) => d.location_id === loc.id && d.kind === 'agent' && !d.revoked_at);
  let lastSeen: string | null = health?.at ?? null;
  for (const d of agents) lastSeen = latest(lastSeen, d.last_used_at);
  const pi: PiState = health?.connected ? 'online' : health || agents.length ? 'offline' : 'none';
  return { pi, health, lastSeen };
}

export function buildRows(
  overview: Overview,
  fleet: AgentHealth[],
  orders: AdminOrderSummary[],
  kitchen: KitchenStatus[] = []
): StoreRow[] {
  const today: Record<string, number> = {};
  for (const o of orders) if (isToday(o.received_at)) today[o.tenant_id] = (today[o.tenant_id] ?? 0) + 1;
  return overview.tenants.map((t) => {
    const location = overview.locations.find((l) => l.tenant_id === t.id) ?? null;
    const { pi, health, lastSeen } = piStateFor(location, fleet, overview.devices);
    const info = healthInfo(health);
    const k = location ? kitchen.find((x) => x.location_id === location.id) : undefined;
    return {
      tenant: t,
      location,
      health,
      pi,
      lastSeen,
      version: info.version,
      updateAvailable: !!info.update?.updateAvailable,
      latestVersion: info.update?.latestVersion ?? '',
      printersUp: info.printersUp,
      printersDown: info.printersDown,
      problems: info.problems,
      ordersToday: today[t.id] ?? 0,
      kitchenOffline: !!k?.kitchen_offline,
      kitchenAwaySince: k?.away_since ?? null
    };
  });
}

/** Load everything at once; the optional sources degrade to empty instead of failing the page. */
export async function loadAdminData(): Promise<AdminData> {
  const [overview, fleet, orders, unresolved, kitchen] = await Promise.all([
    adminApi<Overview>('/overview'),
    adminApi<AgentHealth[]>('/agents/health').catch(() => [] as AgentHealth[]),
    adminApi<AdminOrderSummary[]>('/orders?limit=200').catch(() => [] as AdminOrderSummary[]),
    adminApi<DiagSummary[]>('/diag-reports?unresolved=true&limit=200').catch(() => [] as DiagSummary[]),
    adminApi<KitchenStatus[]>('/kitchen').catch(() => [] as KitchenStatus[])
  ]);
  return { overview, fleet, orders, unresolved, kitchen, rows: buildRows(overview, fleet, orders, kitchen) };
}

export const PI_BADGE: Record<PiState, { label: string; tone: Tone }> = {
  online: { label: 'Online', tone: 'ok' },
  offline: { label: 'Offline', tone: 'bad' },
  none: { label: 'No Pi yet', tone: 'mute' }
};

/** Hover text for the "Kitchen offline" badge. */
export const KITCHEN_OFFLINE_TITLE =
  'main is refusing this store’s online ASAP orders (409 kitchen_offline): it has an active Pi key but no Pi ' +
  'connection. If the Pi was retired or replaced, revoke its old key (store page → Pi & device keys).';
