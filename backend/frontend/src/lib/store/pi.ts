import { storeApi, type Device, type LivePrinters } from '$lib/api';

/**
 * Whether the store's Pi is reporting printer status:
 *   online   — a Pi is connected right now
 *   offline  — a Pi is linked (unrevoked agent key) but not connected
 *   none     — no Pi was ever linked (or its key was revoked)
 *   unknown  — couldn't tell (the requests failed)
 */
export type PiLink = 'online' | 'offline' | 'none' | 'unknown';

export const PI_NOT_LINKED = 'No Pi linked yet — printer status shows once the Pi is set up.';

export function piLink(live: LivePrinters | null, devices: Device[] | null): PiLink {
  if (live?.agent_online) return 'online';
  if (devices && !devices.some((d) => d.kind === 'agent' && !d.revoked_at)) return 'none';
  return live ? 'offline' : 'unknown';
}

/** The Pi's live printer roster plus whether a Pi is linked at all. Never throws. */
export async function loadPiStatus(): Promise<{ live: LivePrinters | null; pi: PiLink }> {
  const [live, devices] = await Promise.all([
    storeApi<LivePrinters>('/me/printers/live').catch(() => null),
    storeApi<Device[]>('/me/devices').catch(() => null)
  ]);
  return { live, pi: piLink(live, devices) };
}
