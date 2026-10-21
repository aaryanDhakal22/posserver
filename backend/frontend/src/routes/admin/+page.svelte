<script lang="ts">
  import { onMount } from 'svelte';
  import {
    adminApi,
    getAdminKey,
    setAdminKey,
    ApiError,
    parseStoreId,
    type Overview,
    type Location,
    type Device,
    type Minted
  } from '$lib/api';
  import TenantRow from '$lib/components/TenantRow.svelte';

  let key = $state(getAdminKey());
  let ov = $state<Overview | null>(null);
  let banner = $state<{ msg: string; kind: 'ok' | 'error' | 'info' } | null>(null);
  let minted = $state<Minted | null>(null);
  let showNew = $state(false);
  let nt = $state({ slug: '', name: '', username: '', password: '', store_id: '' });
  // Default the login username to the store name (lowercased, punctuation/spaces
  // stripped) until the operator edits it by hand — matches the console's dev
  // seed-logins convention.
  let usernameTouched = $state(false);
  const storeUsername = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '');
  function onNameInput() {
    if (!usernameTouched) nt.username = storeUsername(nt.name);
  }

  function toast(msg: string, kind: 'ok' | 'error' | 'info' = 'ok') {
    banner = { msg, kind };
    setTimeout(() => (banner = null), 4000);
  }
  function err(e: unknown) {
    toast(e instanceof ApiError ? e.message : String(e), 'error');
  }
  function reveal(m: Minted) {
    minted = m;
  }

  const locsByTenant = $derived.by(() => {
    const m: Record<string, Location[]> = {};
    ov?.locations.forEach((l) => (m[l.tenant_id] ??= []).push(l));
    return m;
  });
  const devsByLoc = $derived.by(() => {
    const m: Record<string, Device[]> = {};
    ov?.devices.forEach((d) => (m[d.location_id] ??= []).push(d));
    return m;
  });

  async function reload() {
    if (!getAdminKey()) {
      ov = null;
      return;
    }
    try {
      ov = await adminApi<Overview>('/overview');
    } catch (e) {
      err(e);
    }
  }
  function saveKey() {
    setAdminKey(key.trim());
    toast('admin key saved');
    reload();
  }

  async function createTenant() {
    if (!nt.slug.trim() || !nt.name.trim()) return toast('tenant needs slug + name', 'error');
    if ((nt.username && !nt.password) || (!nt.username && nt.password))
      return toast('set both username and password, or neither', 'error');
    const storeId = parseStoreId(nt.store_id);
    if (storeId === undefined) return toast('store id must be a positive whole number (or blank)', 'error');
    try {
      await adminApi('/tenants', { method: 'POST', body: JSON.stringify({ ...nt, store_id: storeId }) });
      const hadLogin = !!nt.username;
      nt = { slug: '', name: '', username: '', password: '', store_id: '' };
      usernameTouched = false;
      toast(hadLogin ? 'tenant created' : 'tenant created (no login yet)');
      reload();
    } catch (e) {
      err(e);
    }
  }

  onMount(reload);
</script>

<div class="flex flex-wrap items-center justify-between gap-3 mb-4">
  <div>
    <div class="text-lg font-semibold">Admin</div>
    <div class="text-xs text-slate-500 dark:text-slate-400">
      god mode · tenants, locations, logins, agent devices
    </div>
  </div>
  <div class="flex items-center gap-2">
    <input class="field w-64" type="password" placeholder="ADMIN_API_KEY" bind:value={key} />
    <button class="btn btn-dark btn-xs" onclick={saveKey}>save</button>
    <button class="btn btn-ghost btn-xs" onclick={reload}>reload</button>
  </div>
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
      Agent token — copy it now, it is never shown again
    </div>
    <div
      class="mono mt-2 break-all bg-white dark:bg-slate-900 border border-amber-200 dark:border-amber-500/30 rounded px-2 py-1"
    >
      {minted.api_key}
    </div>
    <div class="text-xs text-slate-600 dark:text-slate-400 mt-1">
      device id: <span class="mono">{minted.device.id}</span> · paste this token into the agent's
      setup screen to link the device.
    </div>
    <div class="flex gap-1 mt-3">
      <button
        class="btn btn-primary btn-xs"
        onclick={() => {
          navigator.clipboard?.writeText(minted?.api_key ?? '');
          toast('token copied');
        }}>copy token</button
      >
      <button class="btn btn-ghost btn-xs" onclick={() => (minted = null)}>dismiss</button>
    </div>
  </div>
{/if}

<section class="card">
  <header class="px-4 py-3 border-b border-slate-200 dark:border-slate-800 flex items-center justify-between">
    <h2 class="font-semibold">Tenants</h2>
    <button class="btn btn-dark btn-xs" onclick={() => (showNew = !showNew)}>+ new tenant</button>
  </header>

  {#if showNew}
    <div class="px-4 py-3 border-b border-slate-200 dark:border-slate-800 bg-slate-50 dark:bg-slate-950/40">
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div><label class="lbl">slug</label><input class="field w-full" bind:value={nt.slug} placeholder="acme-pizza" /></div>
        <div><label class="lbl">display name</label><input class="field w-full" bind:value={nt.name} oninput={onNameInput} placeholder="Acme Pizza" /></div>
        <div>
          <label class="lbl">login username <span class="normal-case text-slate-400">(defaults to store name)</span></label>
          <input class="field w-full" bind:value={nt.username} oninput={() => (usernameTouched = true)} autocomplete="off" />
        </div>
        <div>
          <label class="lbl">login password <span class="normal-case text-slate-400">(optional)</span></label>
          <input class="field w-full" type="password" bind:value={nt.password} autocomplete="new-password" />
        </div>
        <div>
          <label class="lbl">vendor store id <span class="normal-case text-slate-400">(optional — online orders route by it)</span></label>
          <input class="field w-full mono" inputmode="numeric" bind:value={nt.store_id} placeholder="3282" autocomplete="off" />
        </div>
      </div>
      <div class="flex items-center gap-3 mt-3">
        <button class="btn btn-primary" onclick={createTenant}>create tenant</button>
        <span class="text-xs text-slate-500 dark:text-slate-400">
          Creates the restaurant and its location. Set a username + password so the manager can sign in at /app/store.
        </span>
      </div>
    </div>
  {/if}

  <div class="divide-y divide-slate-200 dark:divide-slate-800">
    {#if ov && ov.tenants.length}
      {#each ov.tenants as t (t.id)}
        <TenantRow
          tenant={t}
          locations={locsByTenant[t.id] ?? []}
          {devsByLoc}
          {reload}
          {toast}
          {reveal}
        />
      {/each}
    {:else}
      <div class="px-4 py-6 text-sm text-slate-500 dark:text-slate-400">
        {getAdminKey() ? 'No tenants yet. Create one above.' : 'Paste your ADMIN_API_KEY to get started.'}
      </div>
    {/if}
  </div>
</section>
