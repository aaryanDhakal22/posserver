<script lang="ts">
  import { onMount } from 'svelte';
  import { fmtTime, storeApi, timeAgo, type Device, type Minted } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SecretReveal from '$lib/ui/SecretReveal.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';

  // This location's device keys. Managers mint mobile/kiosk/ingest keys; the
  // Pi's own key arrives when it links with the manager login.
  let devices = $state<Device[] | null>(null);
  let nd = $state({ name: '', kind: 'mobile' });
  let minting = $state(false);
  let minted = $state<Minted | null>(null);
  let nameErr = $state('');

  const KINDS = [
    { value: 'mobile', label: 'Mobile' },
    { value: 'kiosk', label: 'Kiosk' },
    { value: 'ingest', label: 'Ingest' }
  ];
  const kindLabel = (k: string) => ({ agent: 'Pi', mobile: 'Mobile', kiosk: 'Kiosk', ingest: 'Ingest' })[k] ?? k;

  async function load() {
    devices = await storeApi<Device[]>('/me/devices');
  }
  onMount(() => load().catch((e) => toast.error(e)));

  // Terminals & tablets (agent 2.5.3): what the Pi last reported, the newest
  // release main has, and "Update now" (the Pi tells its terminals to install
  // it at their next quiet moment).
  type AppUpdate = {
    version_name: string;
    version_code: number;
    state: string;
    reason?: string;
    target_code?: number;
    at: string;
    android?: string;
    permissions?: Record<string, boolean>;
    received_at: string;
  };
  type FloorDevice = {
    device_id: string;
    name: string;
    app_version: string;
    last_seen_at: string | null;
    connected: boolean;
    update?: AppUpdate;
  };
  type Release = { version_code: number; version_name: string; created_at: string } | null;
  type Floor = {
    connected: boolean;
    reported_at: string | null;
    terminals: FloorDevice[];
    tablets: FloorDevice[];
    releases: { terminal: Release; quibase: Release };
  };
  type UpdateNow = {
    ok: boolean;
    code?: string;
    release: Release;
    terminals: { device_id: string; name: string; told: boolean; up_to_date: boolean; connected: boolean }[];
  };
  let floor = $state<Floor | null>(null);
  let updating = $state(false);
  let updateResult = $state('');

  async function loadFloor() {
    floor = await storeApi<Floor>('/me/terminals');
  }
  onMount(() => loadFloor().catch((e) => toast.error(e)));

  const STATE_LABEL: Record<string, string> = {
    up_to_date: 'Up to date',
    downloading: 'Downloading',
    cached: 'Downloaded, waiting',
    waiting_for_idle: 'Waiting for a quiet moment',
    waiting_for_tap: 'Waiting for a tap on the screen',
    installing: 'Installing',
    failed: 'Failed'
  };
  const permsText = (p?: Record<string, boolean>) =>
    p ? Object.entries(p).map(([k, v]) => `${k.replaceAll('_', ' ')}: ${v ? 'yes' : 'NO'}`).join(', ') : '—';

  async function updateNow() {
    const ok = await confirmDialog({
      title: 'Update the terminals now?',
      body: 'The Pi gets the newest terminal app and each terminal installs it at its next quiet moment (no order on its screen). Staff log in again afterwards.',
      confirmLabel: 'Update now'
    });
    if (!ok) return;
    updating = true;
    updateResult = '';
    try {
      const r = await storeApi<UpdateNow>('/me/terminals/update-now', { method: 'POST' });
      if (!r.ok) {
        updateResult = 'There is no terminal release to install yet.';
      } else {
        const told = r.terminals.filter((t) => t.told).length;
        const current = r.terminals.filter((t) => t.up_to_date).length;
        updateResult = `Version ${r.release?.version_name} sent: ${told} terminal${told === 1 ? '' : 's'} told` +
          (current ? `, ${current} already up to date` : '') + '.';
      }
      toast.success('Update sent to the Pi');
      await loadFloor();
    } catch (err) {
      updateResult = err instanceof Error ? err.message : String(err);
      toast.error(err);
    } finally {
      updating = false;
    }
  }

  async function mint(e: SubmitEvent) {
    e.preventDefault();
    nameErr = '';
    if (!nd.name.trim()) return (nameErr = 'Give the device a name.');
    minting = true;
    try {
      minted = await storeApi<Minted>('/me/devices', { method: 'POST', body: JSON.stringify({ name: nd.name.trim(), kind: nd.kind }) });
      nd = { name: '', kind: 'mobile' };
      await load();
    } catch (err) {
      toast.error(err);
    } finally {
      minting = false;
    }
  }

  async function revoke(d: Device) {
    const ok = await confirmDialog({
      title: `Revoke “${d.name}”?`,
      body:
        d.kind === 'agent'
          ? "This is the Pi's key — orders stop reaching the store until the Pi is linked again."
          : 'Its key stops working immediately.',
      confirmLabel: 'Revoke key',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi('/me/devices/' + d.id + '/revoke', { method: 'POST' });
      await load();
      toast.success('Key revoked');
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<svelte:head><title>Devices — quicc</title></svelte:head>

<PageHeader title="Devices" subtitle="Keys that let devices talk to quicc for this store" />

{#if minted}
  <SecretReveal title="New device key — copy it now, it is never shown again" secret={minted.api_key} ondismiss={() => (minted = null)}>
    For <b>{minted.device.name}</b> ({kindLabel(minted.device.kind)}).
  </SecretReveal>
{/if}

<Card title="Device keys" padded={false}>
  {#if devices === null}
    <div class="px-5 pb-5"><Skeleton lines={3} /></div>
  {:else if devices.length}
    <div class="overflow-x-auto">
      <table class="tbl stack min-w-[600px]">
        <thead><tr><th>Name</th><th>Kind</th><th>Key</th><th>Last used</th><th><span class="sr-only">Actions</span></th></tr></thead>
        <tbody>
          {#each devices as d (d.id)}
            <tr>
              <td class="font-semibold"><span>{d.name}{#if d.revoked_at}<Badge tone="bad" class="ml-2">Revoked</Badge>{/if}</span></td>
              <td data-label="Kind">{kindLabel(d.kind)}</td>
              <td data-label="Key" class="mono">{d.key_prefix}…</td>
              <td data-label="Last used" title={fmtTime(d.last_used_at)}>{d.last_used_at ? timeAgo(d.last_used_at) : 'never'}</td>
              <td class="text-right stack-actions"><Button size="sm" disabled={!!d.revoked_at} onclick={() => revoke(d)}>Revoke</Button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <EmptyState icon="tablet" title="No devices yet" body="The Pi appears here once it's linked. Create a key below for anything else." />
  {/if}
</Card>

<Card title="Terminals & tablets" padded={false}>
  <div class="px-5 pb-3 flex flex-wrap items-center gap-3" data-testid="floor-devices-head">
    {#if floor}
      <span>Pi: {#if floor.connected}<Badge tone="ok">online</Badge>{:else}<Badge tone="bad">offline</Badge>{/if}</span>
      <span class="text-sm">Last report: {floor.reported_at ? timeAgo(floor.reported_at) : 'never'}</span>
      <span class="text-sm">Newest terminal app: {floor.releases.terminal ? `${floor.releases.terminal.version_name} (${floor.releases.terminal.version_code})` : 'none'}</span>
      <span class="text-sm">Newest Quibase: {floor.releases.quibase ? `${floor.releases.quibase.version_name} (${floor.releases.quibase.version_code})` : 'none'}</span>
    {/if}
    <Button variant="primary" icon="refresh" loading={updating} onclick={updateNow} data-testid="update-now">Update now</Button>
  </div>
  {#if updateResult}<p class="px-5 pb-3 text-sm" role="status" data-testid="update-result">{updateResult}</p>{/if}
  {#if floor === null}
    <div class="px-5 pb-5"><Skeleton lines={3} /></div>
  {:else if floor.terminals.length || floor.tablets.length}
    <div class="overflow-x-auto">
      <table class="tbl stack min-w-[760px]" data-testid="floor-devices">
        <thead><tr><th>Name</th><th>Kind</th><th>App version</th><th>Update</th><th>Permissions</th><th>Android</th><th>Last contact</th></tr></thead>
        <tbody>
          {#each floor.terminals as d (d.device_id)}
            <tr>
              <td class="font-semibold"><span>{d.name}{#if d.connected}<Badge tone="ok" class="ml-2">on</Badge>{/if}</span></td>
              <td data-label="Kind">Terminal</td>
              <td data-label="App version" class="mono">{d.update ? `${d.update.version_name} (${d.update.version_code})` : d.app_version || '—'}</td>
              <td data-label="Update" title={d.update?.reason ?? ''}>
                {#if d.update}{STATE_LABEL[d.update.state] ?? d.update.state}{#if d.update.reason} — {d.update.reason}{/if}
                  <span class="text-xs opacity-70">({timeAgo(d.update.at)})</span>{:else}—{/if}
              </td>
              <td data-label="Permissions">{permsText(d.update?.permissions)}</td>
              <td data-label="Android">{d.update?.android ?? '—'}</td>
              <td data-label="Last contact" title={fmtTime(d.update?.received_at ?? d.last_seen_at)}>
                {(d.update?.received_at ?? d.last_seen_at) ? timeAgo((d.update?.received_at ?? d.last_seen_at) as string) : 'never'}
              </td>
            </tr>
          {/each}
          {#each floor.tablets as d (d.device_id)}
            <tr>
              <td class="font-semibold"><span>{d.name}{#if d.connected}<Badge tone="ok" class="ml-2">on</Badge>{/if}</span></td>
              <td data-label="Kind">Quibase tablet</td>
              <td data-label="App version" class="mono">{d.app_version || '—'}</td>
              <td data-label="Update">updates itself after Close batch</td>
              <td data-label="Permissions">—</td>
              <td data-label="Android">—</td>
              <td data-label="Last contact" title={fmtTime(d.last_seen_at)}>{d.last_seen_at ? timeAgo(d.last_seen_at) : 'never'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <EmptyState icon="tablet" title="No terminals or tablets reported yet" body="The Pi lists them here once they are paired (its report comes every few minutes)." />
  {/if}
</Card>

<Card title="New device key">
  <form class="flex flex-wrap items-end gap-3" onsubmit={mint}>
    <div class="flex-1 min-w-[200px]">
      <label class="lbl" for="dev-name">Name</label>
      <input id="dev-name" class="field" bind:value={nd.name} placeholder="Front tablet" aria-invalid={!!nameErr || undefined} />
      {#if nameErr}<p class="field-error" role="alert">{nameErr}</p>{/if}
    </div>
    <div>
      <span class="lbl">Kind</span>
      <SegmentedControl label="Device kind" options={KINDS} bind:value={nd.kind} />
    </div>
    <Button type="submit" variant="primary" icon="key" loading={minting}>Create key</Button>
  </form>
</Card>
