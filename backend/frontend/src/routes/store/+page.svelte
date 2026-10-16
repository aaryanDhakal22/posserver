<script lang="ts">
  import { base } from '$app/paths';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import {
    storeApi,
    ApiError,
    fmtTime,
    type Whoami,
    type Location,
    type Printer,
    type Device,
    type Minted,
    type StoreOrder,
    type StoreFloor,
    dollars
  } from '$lib/api';
  import { TIMEZONES } from '$lib/timezones';

  let loading = $state(true);
  let who = $state<Whoami | null>(null);
  let loc = $state<Location | null>(null);
  let printers = $state<Printer[]>([]);
  let devices = $state<Device[]>([]);
  let orders = $state<StoreOrder[]>([]);
  let floor = $state<StoreFloor | null>(null);
  let banner = $state<{ msg: string; kind: 'ok' | 'error' | 'info' } | null>(null);
  let minted = $state<Minted | null>(null);

  // new printer / device inputs
  let np = $state({ name: '', ip: '' });
  let nd = $state({ name: '', kind: 'mobile' });

  function toast(msg: string, kind: 'ok' | 'error' | 'info' = 'ok') {
    banner = { msg, kind };
    setTimeout(() => (banner = null), 4000);
  }
  function err(e: unknown) {
    toast(e instanceof ApiError ? e.message : String(e), 'error');
  }

  async function loadAll() {
    loc = await storeApi<Location>('/me/location');
    printers = await storeApi<Printer[]>('/me/printers');
    devices = await storeApi<Device[]>('/me/devices');
    await loadFloor();
  }

  // The floor (status, kitchen #, drivers) is decided by the store's agent and
  // synced here; this view is read-only and refreshes every 15s.
  async function loadFloor() {
    orders = await storeApi<StoreOrder[]>('/me/orders?limit=30');
    floor = await storeApi<StoreFloor>('/me/floor');
  }
  const STATUS_LABEL: Record<string, string> = {
    received: 'waiting', validated: 'waiting', accepted: 'in kitchen', preparing: 'in kitchen',
    ready: 'ready', on_delivery: 'out for delivery', completed: 'delivered',
    rejected: 'rejected', cancelled: 'cancelled'
  };
  const STATUS_CLASS: Record<string, string> = {
    received: 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-200',
    accepted: 'bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-200',
    ready: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200',
    on_delivery: 'bg-violet-100 text-violet-800 dark:bg-violet-900/40 dark:text-violet-200'
  };
  function statusLabel(o: StoreOrder) {
    if (o.status === 'ready' && o.fulfillment.type !== 'delivery') return 'done';
    return STATUS_LABEL[o.status] ?? o.status;
  }
  function hhmm(t?: string | null) {
    return t ? new Date(t).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : '';
  }

  onMount(() => {
    const t = setInterval(() => loadFloor().catch(() => {}), 15000);
    init();
    return () => clearInterval(t);
  });

  async function init() {
    try {
      who = await storeApi<Whoami>('/session');
      await loadAll();
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        await goto(base + '/login');
        return;
      }
      err(e);
    } finally {
      loading = false;
    }
  }

  async function logout() {
    try {
      await storeApi('/session', { method: 'DELETE' });
    } catch {
      /* ignore */
    }
    await goto(base + '/login');
  }

  async function saveLocation() {
    if (!loc) return;
    try {
      loc = await storeApi<Location>('/me/location', {
        method: 'PATCH',
        body: JSON.stringify({
          name: loc.name,
          timezone: loc.timezone,
          daily_reset_hour: loc.daily_reset_hour,
          phone: loc.phone,
          address_line1: loc.address_line1,
          address_line2: loc.address_line2,
          config: loc.config
        })
      });
      toast('location saved');
    } catch (e) {
      err(e);
    }
  }

  async function addPrinter() {
    if (!np.name.trim()) return toast('printer needs a name', 'error');
    try {
      await storeApi('/me/printers', { method: 'POST', body: JSON.stringify(np) });
      np = { name: '', ip: '' };
      printers = await storeApi<Printer[]>('/me/printers');
      toast('printer added');
    } catch (e) {
      err(e);
    }
  }
  async function savePrinter(p: Printer) {
    try {
      await storeApi('/me/printers/' + p.id, {
        method: 'PATCH',
        body: JSON.stringify({ name: p.name, ip: p.ip })
      });
      toast('printer saved');
    } catch (e) {
      err(e);
    }
  }
  async function makeReceiptPrinter(p: Printer) {
    try {
      await storeApi('/me/printers/' + p.id, {
        method: 'PATCH',
        body: JSON.stringify({ name: p.name, ip: p.ip, is_receipt: true })
      });
      printers = await storeApi<Printer[]>('/me/printers');
      toast(`"${p.name}" now prints the customer receipt`);
    } catch (e) {
      err(e);
    }
  }
  async function deletePrinter(p: Printer) {
    if (!confirm(`Delete printer "${p.name}"?`)) return;
    try {
      await storeApi('/me/printers/' + p.id, { method: 'DELETE' });
      printers = await storeApi<Printer[]>('/me/printers');
      toast('printer deleted');
    } catch (e) {
      err(e);
    }
  }

  async function mintDevice() {
    if (!nd.name.trim()) return toast('device needs a name', 'error');
    try {
      minted = await storeApi<Minted>('/me/devices', { method: 'POST', body: JSON.stringify(nd) });
      nd = { name: '', kind: 'mobile' };
      devices = await storeApi<Device[]>('/me/devices');
    } catch (e) {
      err(e);
    }
  }
  async function revokeDevice(d: Device) {
    if (!confirm(`Revoke "${d.name}"? Its key stops working immediately.`)) return;
    try {
      await storeApi('/me/devices/' + d.id + '/revoke', { method: 'POST' });
      devices = await storeApi<Device[]>('/me/devices');
      toast('device revoked');
    } catch (e) {
      err(e);
    }
  }
</script>

{#if loading}
  <div class="text-slate-500 dark:text-slate-400">Loading…</div>
{:else if who}
  <div class="flex items-center justify-between mb-4">
    <div>
      <div class="text-lg font-semibold">{who.tenant.name}</div>
      <div class="text-xs text-slate-500 dark:text-slate-400">
        location <span class="mono">{who.location.name}</span>
      </div>
    </div>
    <button class="btn btn-ghost btn-xs" onclick={logout}>log out</button>
  </div>

  {#if banner}
    <div
      class="rounded-md border px-4 py-3 text-sm mb-4 {banner.kind === 'error'
        ? 'border-rose-300 bg-rose-50 text-rose-900 dark:border-rose-900/60 dark:bg-rose-950/60 dark:text-rose-200'
        : 'border-emerald-300 bg-emerald-50 text-emerald-900 dark:border-emerald-900/60 dark:bg-emerald-950/50 dark:text-emerald-200'}"
    >
      {banner.msg}
    </div>
  {/if}

  {#if minted}
    <div
      class="rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 mb-4 dark:border-amber-500/40 dark:bg-amber-500/10"
    >
      <div class="text-sm font-semibold text-amber-900 dark:text-amber-200">
        New device key — copy it now, it is never shown again
      </div>
      <div
        class="mono mt-2 break-all bg-white dark:bg-slate-900 border border-amber-200 dark:border-amber-500/30 rounded px-2 py-1"
      >
        {minted.api_key}
      </div>
      <button class="btn btn-ghost btn-xs mt-3" onclick={() => (minted = null)}>dismiss</button>
    </div>
  {/if}

  <!-- Floor (read-only, synced from the store's agent) -->
  <section class="card px-4 py-4 mb-4">
    <div class="flex items-baseline justify-between mb-1">
      <h2 class="font-semibold">Orders</h2>
      <span class="text-xs text-slate-500 dark:text-slate-400">
        {#if floor?.last_sync_at}agent last synced {fmtTime(floor.last_sync_at)}{:else}agent hasn't synced yet{/if}
      </span>
    </div>
    <p class="text-xs text-slate-500 dark:text-slate-400 mb-3">
      Status, kitchen numbers and drivers are set on the restaurant tablet and synced here.
    </p>
    <div class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="text-xs text-slate-500 dark:text-slate-400 text-left">
          <tr><th class="py-1 pr-2">kitchen #</th><th class="pr-2">order</th><th class="pr-2">customer</th><th class="pr-2">type</th><th class="pr-2">status</th><th class="pr-2">ready by</th><th class="pr-2">driver</th><th class="text-right">total</th></tr>
        </thead>
        <tbody>
          {#each orders as o (o.id)}
            <tr class="border-t border-slate-200 dark:border-slate-800">
              <td class="py-1 pr-2 mono font-semibold">{o.kitchen_order_number ?? '—'}</td>
              <td class="pr-2 mono">#{o.source_event_id}</td>
              <td class="pr-2">{o.customer.display_name ?? ''}</td>
              <td class="pr-2">{o.fulfillment.type}</td>
              <td class="pr-2"><span class="chip {STATUS_CLASS[o.status] ?? 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200'}">{statusLabel(o)}</span></td>
              <td class="pr-2">{hhmm(o.estimated_ready_at)}</td>
              <td class="pr-2">{o.driver_name ?? ''}</td>
              <td class="text-right mono">{dollars(o.money.total_cents)}</td>
            </tr>
          {:else}
            <tr><td colspan="8" class="py-2 text-slate-500 dark:text-slate-400">No orders yet.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if floor && floor.drivers.length}
      <div class="mt-3 pt-3 border-t border-slate-200 dark:border-slate-800 text-sm">
        <span class="text-xs text-slate-500 dark:text-slate-400 mr-2">drivers (queue order)</span>
        {#each floor.drivers as d (d.id)}
          <span class="chip mr-1 {d.status === 'out' ? 'bg-violet-100 text-violet-800 dark:bg-violet-900/40 dark:text-violet-200' : 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200'}">{d.name}{d.status === 'out' ? ' · out' : ''}</span>
        {/each}
      </div>
    {/if}
  </section>

  <!-- Location -->
  {#if loc}
    <section class="card px-4 py-4 mb-4">
      <h2 class="font-semibold mb-3">Location</h2>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div><label class="lbl">Name</label><input class="field w-full" bind:value={loc.name} /></div>
        <div><label class="lbl">Phone</label><input class="field w-full" bind:value={loc.phone} /></div>
        <div>
          <label class="lbl">Address line 1</label><input
            class="field w-full"
            bind:value={loc.address_line1}
          />
        </div>
        <div>
          <label class="lbl">Address line 2</label><input
            class="field w-full"
            bind:value={loc.address_line2}
          />
        </div>
        <div>
          <label class="lbl">Timezone</label><select class="field w-full" bind:value={loc.timezone}>
            {#each TIMEZONES as tz}<option value={tz}>{tz}</option>{/each}
          </select>
        </div>
        <div>
          <label class="lbl">Daily reset hour</label><input
            type="number"
            min="0"
            max="23"
            class="field w-full"
            bind:value={loc.daily_reset_hour}
          />
        </div>
      </div>
      <button class="btn btn-primary btn-xs mt-3" onclick={saveLocation}>save location</button>
    </section>
  {/if}

  <!-- Printers -->
  <section class="card px-4 py-4 mb-4">
    <h2 class="font-semibold mb-1">Printers</h2>
    <p class="text-xs text-slate-500 dark:text-slate-400 mb-3">
      Kitchen stations print only their own items (set per item / category in the Menu). The
      <b>receipt printer</b> prints the full customer receipt. Changes reach the store's agent immediately.
    </p>
    <div class="space-y-2">
      {#each printers as p (p.id)}
        <div class="flex flex-wrap gap-2 items-center">
          <input class="field grow" bind:value={p.name} placeholder="name" />
          <input class="field w-40 mono" bind:value={p.ip} placeholder="ip" />
          {#if p.is_receipt}
            <span class="chip bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-200" title="Prints the full customer receipt">receipt printer</span>
          {:else}
            <button class="btn btn-ghost btn-xs" onclick={() => makeReceiptPrinter(p)} title="Print the full customer receipt here">make receipt printer</button>
          {/if}
          <button class="btn btn-primary btn-xs" onclick={() => savePrinter(p)}>save</button>
          <button class="btn btn-danger btn-xs" onclick={() => deletePrinter(p)}>delete</button>
        </div>
      {:else}
        <div class="text-sm text-slate-500 dark:text-slate-400">No printers yet.</div>
      {/each}
    </div>
    <div class="flex flex-wrap gap-2 items-end mt-3 pt-3 border-t border-slate-200 dark:border-slate-800">
      <div><label class="lbl">name</label><input class="field" bind:value={np.name} placeholder="Kitchen" /></div>
      <div>
        <label class="lbl">ip</label><input class="field w-40 mono" bind:value={np.ip} placeholder="192.168.1.50" />
      </div>
      <button class="btn btn-primary btn-xs" onclick={addPrinter}>add printer</button>
    </div>
  </section>

  <!-- Devices -->
  <section class="card px-4 py-4">
    <h2 class="font-semibold mb-3">Devices</h2>
    <div class="space-y-1">
      {#each devices as d (d.id)}
        <div
          class="flex items-center justify-between gap-3 px-2.5 py-1.5 border-l-2 border-slate-300 dark:border-slate-700 bg-white dark:bg-slate-900 rounded-r"
        >
          <div>
            <span class="text-sm font-medium">{d.name}</span>
            <span class="chip bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200 ml-1">{d.kind}</span>
            {#if d.revoked_at}<span class="chip bg-rose-200 text-rose-900 dark:bg-rose-900/60 dark:text-rose-200 ml-1">revoked</span>{/if}
            <div class="text-xs text-slate-500 dark:text-slate-400">
              prefix <span class="mono">{d.key_prefix}</span>… · last-used {fmtTime(d.last_used_at)}
            </div>
          </div>
          <button class="btn btn-ghost btn-xs" disabled={!!d.revoked_at} onclick={() => revokeDevice(d)}>revoke</button>
        </div>
      {:else}
        <div class="text-sm text-slate-500 dark:text-slate-400">No devices yet.</div>
      {/each}
    </div>
    <div class="flex flex-wrap gap-2 items-end mt-3 pt-3 border-t border-slate-200 dark:border-slate-800">
      <div class="grow"><label class="lbl">name</label><input class="field w-full" bind:value={nd.name} placeholder="Front tablet" /></div>
      <div>
        <label class="lbl">kind</label>
        <select class="field" bind:value={nd.kind}>
          <option value="mobile">mobile</option>
          <option value="kiosk">kiosk</option>
          <option value="ingest">ingest</option>
        </select>
      </div>
      <button class="btn btn-primary btn-xs" onclick={mintDevice}>mint key</button>
    </div>
  </section>
{/if}
