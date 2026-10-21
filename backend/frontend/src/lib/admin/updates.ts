// Pi updates (2.0.6) in plain words: the admin asks a store's Pi to update or
// roll back (POST /admin/v1/agent/update|rollback); the Pi reports progress
// back and main keeps the history (GET /admin/v1/agent-updates).
import { adminApi, ApiError, type AgentUpdate, type AgentUpdateResult } from '$lib/api';
import type { Tone } from '$lib/ui/Badge.svelte';

/** Why the Pi said no, in words a person acts on. */
export function refusalText(code: string): string {
  switch (code) {
    case 'not_after_close':
      return 'The Pi said not now: the open batch has orders. It only updates right after Close batch — close the batch on the tablet, or use Force.';
    case 'orders_open':
      return "Orders are still open (waiting, cooking, ready or out for delivery). Try again once they're done.";
    case 'update_in_progress':
      return 'An update is already running on the Pi.';
    case 'not_supported':
      return "This Pi can't update itself (a dev agent, or a card without the update service).";
    case 'bad_reply':
      return "The Pi's answer made no sense — check its version.";
    default:
      return code ? `The Pi said no (${code}).` : 'The Pi said no.';
  }
}

/** What happens next after an accepted request. */
export function acceptedText(action: 'update' | 'rollback', state: string): string {
  if (state === 'waiting_for_idle') return 'The Pi will install as soon as no orders are open.';
  return action === 'rollback'
    ? 'The Pi is rolling back now — printing pauses for about a minute.'
    : 'The Pi is updating now — printing pauses for about a minute.';
}

export interface UpdateOutcome {
  ok: boolean;
  text: string;
  result?: AgentUpdateResult;
}

/** Ask the store's Pi; never throws — the outcome carries the words to show. */
export async function requestPiUpdate(
  action: 'update' | 'rollback',
  tenantId: string,
  locationId: string,
  force = false
): Promise<UpdateOutcome> {
  try {
    const body = action === 'update' ? { tenant_id: tenantId, location_id: locationId, force } : { tenant_id: tenantId, location_id: locationId };
    const result = await adminApi<AgentUpdateResult>(`/agent/${action}`, { method: 'POST', body: JSON.stringify(body) });
    if (!result.accepted) return { ok: false, text: refusalText(result.code), result };
    return { ok: true, text: acceptedText(action, result.state), result };
  } catch (e) {
    if (e instanceof ApiError && e.status === 503) return { ok: false, text: "The Pi isn't connected — nothing was sent." };
    if (e instanceof ApiError && e.status === 504)
      return { ok: false, text: "The Pi didn't answer. It may run an agent older than 2.0.6, which can't update remotely." };
    return { ok: false, text: e instanceof Error ? e.message : String(e) };
  }
}

export async function loadUpdateHistory(locationId: string, limit = 20): Promise<AgentUpdate[]> {
  return adminApi<AgentUpdate[]>(`/agent-updates?location_id=${locationId}&limit=${limit}`);
}

/** A history row's state as a badge. */
export function updateStateBadge(u: Pick<AgentUpdate, 'action' | 'state'>): { label: string; tone: Tone } {
  switch (u.state) {
    case 'requested':
      return { label: 'Sent', tone: 'info' };
    case 'waiting_for_idle':
      return { label: 'Waiting for no open orders', tone: 'info' };
    case 'running':
      return { label: u.action === 'rollback' ? 'Rolling back' : 'Installing', tone: 'info' };
    case 'ok':
      return { label: u.action === 'rollback' ? 'Rolled back' : 'Updated', tone: 'ok' };
    case 'up_to_date':
      return { label: 'Already up to date', tone: 'ok' };
    case 'rolled_back':
      return { label: 'Failed — rolled back', tone: 'bad' };
    case 'failed':
      return { label: 'Failed', tone: 'bad' };
    case 'rejected':
      return { label: 'Refused', tone: 'warn' };
    case 'unreachable':
      return { label: 'Pi unreachable', tone: 'warn' };
    default:
      return { label: u.state.replaceAll('_', ' ') || '—', tone: 'mute' };
  }
}

/** Who asked, in words. */
export function requesterText(by: string): string {
  return ({ admin: 'Admin website', console: 'Console', 'pi-web': 'Pi web page', boot: 'Fresh card (boot)' } as Record<string, string>)[by] ?? (by || '—');
}

/** A history row's error column: a refusal code in words, else the Pi's error. */
export function updateErrorText(u: Pick<AgentUpdate, 'state' | 'error'>): string {
  if (!u.error) return '';
  return u.state === 'rejected' ? refusalText(u.error) : u.error;
}
