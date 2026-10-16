<script lang="ts">
  import { adminApi, ApiError, parseStoreId, type Tenant, type Location, type Device, type Minted } from '$lib/api';
  import LocationRow from './LocationRow.svelte';

  let {
    tenant,
    locations,
    devsByLoc,
    reload,
    toast,
    reveal
  }: {
    tenant: Tenant;
    locations: Location[];
    devsByLoc: Record<string, Device[]>;
    reload: () => void;
    toast: (m: string, k?: 'ok' | 'error' | 'info') => void;
    reveal: (m: Minted) => void;
  } = $props();

  let showLogin = $state(false);
  let login = $state({ username: '', password: '' });
  let showStoreId = $state(false);
  let storeIdText = $state('');
  let showConfig = $state(false);
  let configText = $state('{}');

  function err(x: unknown) {
    toast(x instanceof ApiError ? x.message : String(x), 'error');
  }
  function openLogin() {
    login = { username: tenant.username || '', password: '' };
    showLogin = !showLogin;
  }
  function openStoreId() {
    storeIdText = tenant.store_id == null ? '' : String(tenant.store_id);
    showStoreId = !showStoreId;
  }
  async function saveStoreId(clear = false) {
    const storeId = clear ? null : parseStoreId(storeIdText);
    if (storeId === undefined) return toast('store id must be a positive whole number', 'error');
    try {
      await adminApi(`/tenants/${tenant.id}/store-id`, { method: 'PATCH', body: JSON.stringify({ store_id: storeId }) });
      toast(storeId == null ? 'store id cleared — orders route by store name' : `store id set — vendor orders with store_id ${storeId} route here`);
      showStoreId = false;
      reload();
    } catch (x) {
      err(x);
    }
  }
  function openConfig() {
    configText = JSON.stringify(tenant.config ?? {}, null, 2);
    showConfig = !showConfig;
  }
  async function saveLogin() {
    if (!login.username.trim() || !login.password)
      return toast('login needs a username and password', 'error');
    try {
      await adminApi(`/tenants/${tenant.id}/credentials`, { method: 'PATCH', body: JSON.stringify(login) });
      toast('login set — the manager can now log in as “' + login.username + '”');
      reload();
    } catch (x) {
      err(x);
    }
  }
  async function saveConfig() {
    let cfg: Record<string, unknown>;
    try {
      cfg = configText.trim() ? JSON.parse(configText) : {};
    } catch {
      return toast('config must be valid JSON', 'error');
    }
    try {
      await adminApi(`/tenants/${tenant.id}/config`, { method: 'PATCH', body: JSON.stringify(cfg) });
      toast('tenant config updated');
      reload();
    } catch (x) {
      err(x);
    }
  }
  async function del() {
    if (!confirm(`Delete restaurant "${tenant.name}" and its location, devices, printers, menu and orders? This cannot be undone.`)) return;
    try {
      await adminApi(`/tenants/${tenant.id}`, { method: 'DELETE' });
      toast('tenant deleted');
      reload();
    } catch (x) {
      err(x);
    }
  }
</script>

<div class="px-4 py-3">
  <div class="flex items-start justify-between gap-4">
    <div>
      <div class="flex items-center gap-2">
        <span class="font-medium">{tenant.name}</span>
        {#if tenant.has_login}
          <span class="chip bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-200">login: {tenant.username}</span>
        {:else}
          <span class="chip bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200">no login</span>
        {/if}
        {#if tenant.store_id != null}
          <span
            class="chip bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-200"
            title="Vendor orders carrying this store_id route to this tenant."
          >store #{tenant.store_id}</span>
        {:else}
          <span
            class="chip bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300"
            title="No vendor store id — online orders reach this tenant only via the store_name fallback."
          >no store id</span>
        {/if}
        {#if locations.length === 0}
          <span
            class="chip bg-rose-100 text-rose-800 dark:bg-rose-900/40 dark:text-rose-200"
            title="Every store gets its location when it's created — this one predates that; re-run migrations."
          >no location</span>
        {/if}
      </div>
      <div class="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
        slug <span class="mono">{tenant.slug}</span> · id <span class="mono">{tenant.id}</span>
      </div>
    </div>
    <div class="flex flex-wrap gap-1 justify-end">
      <button class="btn btn-ghost btn-xs" onclick={openLogin}>set login</button>
      <button class="btn btn-ghost btn-xs" onclick={openStoreId}>store id</button>
      <button class="btn btn-ghost btn-xs" onclick={openConfig}>edit config</button>
      <button class="btn btn-danger btn-xs" onclick={del}>delete</button>
    </div>
  </div>

  {#if showLogin}
    <div class="subcard px-3 py-2.5 mt-3">
      <div class="text-xs text-slate-500 dark:text-slate-400 mb-2">
        Store-manager login — the manager signs in at /app/store to edit the store, printers and menu.
      </div>
      <div class="flex flex-wrap gap-2 items-end">
        <div><label class="lbl">username</label><input class="field" bind:value={login.username} autocomplete="off" /></div>
        <div><label class="lbl">password</label><input class="field" type="password" bind:value={login.password} autocomplete="new-password" /></div>
        <button class="btn btn-primary btn-xs" onclick={saveLogin}>save login</button>
      </div>
    </div>
  {/if}

  {#if showStoreId}
    <div class="subcard px-3 py-2.5 mt-3">
      <div class="text-xs text-slate-500 dark:text-slate-400 mb-2">
        Vendor store id — online orders whose <span class="mono">store_id</span> matches route to this tenant
        (orders without a matching id fall back to the store-name match).
      </div>
      <div class="flex flex-wrap gap-2 items-end">
        <div><label class="lbl">store id</label><input class="field mono w-32" inputmode="numeric" bind:value={storeIdText} placeholder="3282" autocomplete="off" /></div>
        <button class="btn btn-primary btn-xs" onclick={() => saveStoreId()}>save store id</button>
        {#if tenant.store_id != null}
          <button class="btn btn-ghost btn-xs" onclick={() => saveStoreId(true)}>clear</button>
        {/if}
      </div>
    </div>
  {/if}

  {#if showConfig}
    <div class="subcard px-3 py-2.5 mt-3">
      <div class="text-xs text-slate-500 dark:text-slate-400 mb-1">tenant config (json)</div>
      <textarea class="field json w-full h-24" bind:value={configText}></textarea>
      <button class="btn btn-primary btn-xs mt-2" onclick={saveConfig}>save config</button>
    </div>
  {/if}

  <div class="mt-3 space-y-2">
    {#each locations as l (l.id)}
      <LocationRow location={l} devices={devsByLoc[l.id] ?? []} {reload} {toast} {reveal} />
    {/each}
  </div>
</div>
