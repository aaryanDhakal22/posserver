<script lang="ts">
  import { adminApi, ApiError, fmtTime, type Location, type Device, type Minted } from '$lib/api';
  import { TIMEZONES, DEFAULT_TIMEZONE } from '$lib/timezones';

  let {
    location,
    devices,
    reload,
    toast,
    reveal
  }: {
    location: Location;
    devices: Device[];
    reload: () => void;
    toast: (m: string, k?: 'ok' | 'error' | 'info') => void;
    reveal: (m: Minted) => void;
  } = $props();


  let showEdit = $state(false);
  let e = $state({
    name: '',
    timezone: '',
    daily_reset_hour: 0,
    phone: '',
    address_line1: '',
    address_line2: '',
    configText: '{}'
  });
  let showAgent = $state(false);
  let agentName = $state('');

  function err(x: unknown) {
    toast(x instanceof ApiError ? x.message : String(x), 'error');
  }
  function openEdit() {
    e = {
      name: location.name,
      timezone: location.timezone,
      daily_reset_hour: location.daily_reset_hour,
      phone: location.phone,
      address_line1: location.address_line1,
      address_line2: location.address_line2,
      configText: JSON.stringify(location.config ?? {}, null, 2)
    };
    showEdit = !showEdit;
  }
  async function saveEdit() {
    let cfg: Record<string, unknown>;
    try {
      cfg = e.configText.trim() ? JSON.parse(e.configText) : {};
    } catch {
      return toast('config must be valid JSON', 'error');
    }
    try {
      await adminApi('/locations/' + location.id, {
        method: 'PATCH',
        body: JSON.stringify({
          name: e.name,
          timezone: e.timezone || DEFAULT_TIMEZONE,
          daily_reset_hour: Number(e.daily_reset_hour) || 0,
          phone: e.phone,
          address_line1: e.address_line1,
          address_line2: e.address_line2,
          config: cfg
        })
      });
      toast('location updated');
      reload();
    } catch (x) {
      err(x);
    }
  }
  async function mintAgent() {
    if (!agentName.trim()) return toast('device needs a name', 'error');
    try {
      const m = await adminApi<Minted>('/devices', {
        method: 'POST',
        body: JSON.stringify({ location_id: location.id, name: agentName })
      });
      agentName = '';
      showAgent = false;
      reveal(m);
      reload();
    } catch (x) {
      err(x);
    }
  }
  async function revoke(d: Device) {
    if (!confirm("Revoke this device's key?")) return;
    try {
      await adminApi('/devices/' + d.id + '/revoke', { method: 'POST' });
      toast('device revoked');
      reload();
    } catch (x) {
      err(x);
    }
  }
  async function hardDelete(d: Device) {
    if (!confirm('Hard-delete this device row entirely?')) return;
    try {
      await adminApi('/devices/' + d.id, { method: 'DELETE' });
      toast('device deleted');
      reload();
    } catch (x) {
      err(x);
    }
  }
</script>

<div class="subcard px-3 py-2.5">
  <div class="flex items-start justify-between gap-4">
    <div>
      <div class="text-sm font-medium">{location.name}</div>
      <div class="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
        slug <span class="mono">{location.slug}</span> · tz <span class="mono">{location.timezone}</span> ·
        phone <span class="mono">{location.phone || '—'}</span> · id <span class="mono">{location.id}</span>
      </div>
      <div class="text-xs text-slate-500 dark:text-slate-400">
        {[location.address_line1, location.address_line2].filter(Boolean).join(' · ') || '(no address)'}
      </div>
    </div>
    <div class="flex flex-wrap gap-1 justify-end">
      <button class="btn btn-ghost btn-xs" onclick={openEdit}>edit</button>
      <button class="btn btn-ghost btn-xs" onclick={() => (showAgent = !showAgent)}>+ create agent</button>
    </div>
  </div>

  {#if showEdit}
    <div class="card px-3 py-2.5 mt-2 space-y-2">
      <div class="flex flex-wrap gap-2">
        <div class="grow"><label class="lbl">name</label><input class="field w-full" bind:value={e.name} /></div>
        <div><label class="lbl">timezone</label><select class="field w-44" bind:value={e.timezone}>
          {#each TIMEZONES as tz}<option value={tz}>{tz}</option>{/each}
        </select></div>
        <div><label class="lbl">reset hr</label><input type="number" min="0" max="23" class="field w-20" bind:value={e.daily_reset_hour} /></div>
      </div>
      <div><label class="lbl">phone</label><input class="field w-full" bind:value={e.phone} /></div>
      <div><label class="lbl">address line 1</label><input class="field w-full" bind:value={e.address_line1} /></div>
      <div><label class="lbl">address line 2</label><input class="field w-full" bind:value={e.address_line2} /></div>
      <div><label class="lbl">location config (json)</label><textarea class="field json w-full h-20" bind:value={e.configText}></textarea></div>
      <button class="btn btn-primary btn-xs" onclick={saveEdit}>save location</button>
    </div>
  {/if}

  {#if showAgent}
    <div class="card px-3 py-2.5 mt-2 space-y-2">
      <div class="text-xs text-slate-500 dark:text-slate-400">
        Creates an agent for this location and mints its one-time token. Paste the
        token into the agent's setup screen to link the device.
      </div>
      <div class="flex flex-wrap gap-2 items-end">
        <div class="grow"><label class="lbl">agent name</label><input class="field w-full" bind:value={agentName} placeholder="e.g. Kitchen Pi" /></div>
        <button class="btn btn-primary btn-xs" onclick={mintAgent}>create agent</button>
      </div>
    </div>
  {/if}

  <div class="mt-2 space-y-1">
    {#each devices as d (d.id)}
      <div class="flex items-center justify-between gap-3 px-2.5 py-1.5 border-l-2 border-slate-300 dark:border-slate-700 bg-white dark:bg-slate-900 rounded-r">
        <div>
          <span class="text-sm font-medium">{d.name}</span>
          <span class="chip bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200 ml-1">{d.kind}</span>
          {#if d.revoked_at}<span class="chip bg-rose-200 text-rose-900 dark:bg-rose-900/60 dark:text-rose-200 ml-1">revoked</span>{/if}
          <div class="text-xs text-slate-500 dark:text-slate-400">
            prefix <span class="mono">{d.key_prefix}</span>… · id <span class="mono">{d.id}</span> · last-used {fmtTime(d.last_used_at)}
          </div>
        </div>
        <div class="flex gap-1">
          <button class="btn btn-ghost btn-xs" disabled={!!d.revoked_at} onclick={() => revoke(d)}>revoke</button>
          <button class="btn btn-danger btn-xs" onclick={() => hardDelete(d)}>delete</button>
        </div>
      </div>
    {/each}
  </div>
</div>
