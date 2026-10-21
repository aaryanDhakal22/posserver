<script lang="ts">
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import {
    adminApi,
    ApiError,
    dollars,
    errText,
    fmtTime,
    healthInfo,
    isToday,
    openAsStore,
    parseStoreId,
    timeAgo,
    type AdminOrderSummary,
    type AgentHealth,
    type AgentUpdate,
    type Device,
    type DiagSummary,
    type Location,
    type Minted,
    type Overview,
    type TabletToken,
    type Tenant
  } from '$lib/api';
  import { PI_BADGE, piStateFor } from '$lib/admin/stores';
  import { loadUpdateHistory, requestPiUpdate, requesterText, updateErrorText, updateStateBadge, type UpdateOutcome } from '$lib/admin/updates';
  import { copyText, generatePassword } from '$lib/clipboard';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { hoursFromConfig, hoursToConfig, todayHours, type WeekHours } from '$lib/hours';
  import { tzShort, DEFAULT_TIMEZONE } from '$lib/timezones';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Tabs from '$lib/ui/Tabs.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SecretReveal from '$lib/ui/SecretReveal.svelte';
  import LocationFields, { locationForm, type LocationForm } from '$lib/components/LocationFields.svelte';
  import HoursEditor from '$lib/components/HoursEditor.svelte';

  // One store, admin view. Moved here from the old TenantRow/LocationRow:
  // store id, login, location edit (+ raw config JSON), agent tokens,
  // device revoke/delete, diagnostics, delete. No per-tenant GET exists, so
  // everything is filtered out of the flat admin endpoints.

  const TABS = [
    { id: 'overview', label: 'Overview' },
    { id: 'login', label: 'Login' },
    { id: 'location', label: 'Location' },
    { id: 'pi', label: 'Pi & devices' },
    { id: 'diag', label: 'Diagnostics' },
    { id: 'danger', label: 'Danger zone' }
  ];

  const id = $derived(page.params.id ?? '');
  let tab = $state(TABS.some((t) => t.id === page.url.searchParams.get('tab')) ? page.url.searchParams.get('tab')! : 'overview');

  let loading = $state(true);
  let missing = $state(false);
  let ov = $state<Overview | null>(null);
  let fleet = $state<AgentHealth[]>([]);
  let orders = $state<AdminOrderSummary[]>([]);
  let diags = $state<DiagSummary[]>([]);
  let tokens = $state<TabletToken[]>([]);
  let updates = $state<AgentUpdate[]>([]);

  const tenant = $derived<Tenant | null>(ov?.tenants.find((t) => t.id === id) ?? null);
  const loc = $derived<Location | null>(ov?.locations.find((l) => l.tenant_id === id) ?? null);
  const devices = $derived<Device[]>(loc ? (ov?.devices ?? []).filter((d) => d.location_id === loc.id) : []);
  const pi = $derived(piStateFor(loc, fleet, ov?.devices ?? []));
  const info = $derived(healthInfo(pi.health));
  const todays = $derived(orders.filter((o) => isToday(o.received_at)));
  const unresolved = $derived(diags.filter((d) => !d.resolved_at).length);
  const tabs = $derived(TABS.map((t) => (t.id === 'diag' ? { ...t, badge: unresolved || undefined } : t)));

  async function reload() {
    try {
      ov = await adminApi<Overview>('/overview');
      if (!ov.tenants.some((t) => t.id === id)) {
        missing = true;
        return;
      }
      const l = ov.locations.find((x) => x.tenant_id === id);
      const [f, o, d, tk, up] = await Promise.all([
        adminApi<AgentHealth[]>('/agents/health').catch(() => []),
        adminApi<AdminOrderSummary[]>(`/orders?tenant_id=${id}&limit=200`).catch(() => []),
        adminApi<DiagSummary[]>(`/diag-reports?tenant_id=${id}&limit=50`).catch(() => []),
        l ? adminApi<TabletToken[]>(`/tablet-tokens?location_id=${l.id}`).catch(() => []) : Promise.resolve([]),
        l ? loadUpdateHistory(l.id).catch(() => [] as AgentUpdate[]) : Promise.resolve([] as AgentUpdate[])
      ]);
      fleet = f;
      orders = o;
      diags = d;
      tokens = tk;
      updates = up;
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void id;
    loading = true;
    missing = false;
    reload().then(resetForms);
  });

  function setTab(t: string) {
    const url = new URL(page.url);
    url.searchParams.set('tab', t);
    goto(url, { replaceState: true, keepFocus: true, noScroll: true });
  }

  // --- open as store --------------------------------------------------------
  let opening = $state(false);
  async function openStore(path = '') {
    if (!tenant) return;
    opening = true;
    try {
      await openAsStore(tenant.id);
      goto(base + '/store' + path);
    } catch (e) {
      toast.error(e);
    } finally {
      opening = false;
    }
  }

  // --- login + store id -----------------------------------------------------
  let login = $state({ username: '', password: '' });
  let loginErr = $state('');
  let savingLogin = $state(false);
  let storeIdText = $state('');
  let storeIdErr = $state('');
  let locForm = $state<LocationForm>({ name: '', phone: '', address_line1: '', address_line2: '', timezone: DEFAULT_TIMEZONE, daily_reset_hour: 0 });
  let locConfigText = $state('{}');
  let locConfigLoaded = '{}';
  let hours = $state<WeekHours>(hoursFromConfig({}));
  let tenantConfigText = $state('{}');

  function resetForms() {
    if (tenant) {
      login = { username: tenant.username || '', password: '' };
      storeIdText = tenant.store_id == null ? '' : String(tenant.store_id);
      tenantConfigText = JSON.stringify(tenant.config ?? {}, null, 2);
    }
    if (loc) {
      locForm = locationForm(loc);
      locConfigText = locConfigLoaded = JSON.stringify(loc.config ?? {}, null, 2);
      hours = hoursFromConfig(loc.config);
    }
  }

  async function saveLogin() {
    loginErr = '';
    const u = login.username.trim();
    if (!u || !login.password) return (loginErr = 'A login needs a username and a password.');
    if (u.toLowerCase() === 'admin') return (loginErr = '“admin” is reserved — pick another username.');
    savingLogin = true;
    try {
      await adminApi(`/tenants/${id}/credentials`, { method: 'PATCH', body: JSON.stringify({ username: u, password: login.password }) });
      toast.success(`Login saved — the manager signs in as “${u}”`);
      login.password = '';
      await reload();
    } catch (e) {
      loginErr = e instanceof ApiError && e.status === 409 ? 'That username is already taken — pick another.' : errText(e);
    } finally {
      savingLogin = false;
    }
  }

  async function saveStoreId(clear = false) {
    storeIdErr = '';
    const sid = clear ? null : parseStoreId(storeIdText);
    if (sid === undefined) return (storeIdErr = 'A positive whole number, or clear it.');
    try {
      await adminApi(`/tenants/${id}/store-id`, { method: 'PATCH', body: JSON.stringify({ store_id: sid }) });
      toast.success(sid == null ? 'Store ID cleared — orders route by store name' : `Store ID set — vendor orders with store_id ${sid} route here`);
      if (clear) storeIdText = '';
      await reload();
    } catch (e) {
      storeIdErr = e instanceof ApiError && e.status === 409 ? 'Another store already uses this store ID.' : errText(e);
    }
  }

  // --- location -------------------------------------------------------------
  let savingLoc = $state(false);
  async function saveLocation() {
    if (!loc) return;
    let cfg: Record<string, unknown>;
    try {
      cfg = locConfigText.trim() ? JSON.parse(locConfigText) : {};
    } catch {
      return toast.error('Location config must be valid JSON.');
    }
    savingLoc = true;
    try {
      // PATCH is a full replace. Unless the raw JSON was edited here, start
      // from the location's current config (the store may have changed its
      // receipt settings since this page loaded); the hours form always wins
      // for opening_hours.
      if (locConfigText === locConfigLoaded) {
        const fresh = (await adminApi<Location[]>('/locations')).find((l) => l.id === loc!.id);
        if (fresh) cfg = fresh.config ?? {};
      }
      cfg = { ...cfg, opening_hours: hoursToConfig(hours) };
      await adminApi('/locations/' + loc.id, {
        method: 'PATCH',
        body: JSON.stringify({ ...locForm, timezone: locForm.timezone || DEFAULT_TIMEZONE, daily_reset_hour: Number(locForm.daily_reset_hour) || 0, config: cfg })
      });
      toast.success('Location saved — the Pi gets it right away');
      await reload();
      resetForms();
    } catch (e) {
      toast.error(e);
    } finally {
      savingLoc = false;
    }
  }
  async function saveTenantConfig() {
    let cfg: Record<string, unknown>;
    try {
      cfg = tenantConfigText.trim() ? JSON.parse(tenantConfigText) : {};
    } catch {
      return toast.error('Store config must be valid JSON.');
    }
    try {
      await adminApi(`/tenants/${id}/config`, { method: 'PATCH', body: JSON.stringify(cfg) });
      toast.success('Store config saved');
      await reload();
    } catch (e) {
      toast.error(e);
    }
  }

  // --- devices --------------------------------------------------------------
  let mintOpen = $state(false);
  let mintName = $state('');
  let minting = $state(false);
  let minted = $state<Minted | null>(null);
  async function mintAgent() {
    if (!loc) return;
    if (!mintName.trim()) return toast.error('Give the Pi key a name.');
    minting = true;
    try {
      minted = await adminApi<Minted>('/devices', { method: 'POST', body: JSON.stringify({ location_id: loc.id, name: mintName.trim() }) });
      mintName = '';
      mintOpen = false;
      await reload();
    } catch (e) {
      toast.error(e);
    } finally {
      minting = false;
    }
  }
  async function revokeDevice(d: Device) {
    const ok = await confirmDialog({
      title: `Revoke “${d.name}”?`,
      body: 'Its key stops working immediately. The row stays as a revoked record.',
      confirmLabel: 'Revoke key',
      danger: true
    });
    if (!ok) return;
    try {
      await adminApi('/devices/' + d.id + '/revoke', { method: 'POST' });
      toast.success('Key revoked');
      await reload();
    } catch (e) {
      toast.error(e);
    }
  }
  async function deleteDevice(d: Device) {
    const ok = await confirmDialog({
      title: `Delete “${d.name}” entirely?`,
      body: 'Hard-deletes the device row (revoke keeps a record; this does not).',
      confirmLabel: 'Delete device',
      danger: true
    });
    if (!ok) return;
    try {
      await adminApi('/devices/' + d.id, { method: 'DELETE' });
      toast.success('Device deleted');
      await reload();
    } catch (e) {
      toast.error(e);
    }
  }
  async function revokeToken(t: TabletToken) {
    const ok = await confirmDialog({
      title: `Revoke ${t.tablet_name || 'this tablet'}'s report key?`,
      body: "The tablet can't send diagnostics straight to main until the Pi gives it a new key.",
      confirmLabel: 'Revoke key',
      danger: true
    });
    if (!ok) return;
    try {
      await adminApi(`/tablet-tokens/${t.id}/revoke`, { method: 'POST' });
      toast.success('Report key revoked');
      await reload();
    } catch (e) {
      toast.error(e);
    }
  }

  // --- Pi update / roll back --------------------------------------------------
  // The Pi installs whatever its env's signed manifest says. A normal update
  // only goes in right after Close batch (open batch empty) — otherwise the Pi
  // refuses; Force skips that rule but still waits until no order is open.
  let updateOpen = $state(false);
  let forceUpdate = $state(false);
  let updating = $state(false);
  let updateOutcome = $state<UpdateOutcome | null>(null);
  const lastUpdate = $derived(updates[0] ?? null);

  async function runUpdate() {
    if (!tenant || !loc) return;
    updating = true;
    try {
      updateOutcome = await requestPiUpdate('update', tenant.id, loc.id, forceUpdate);
      updateOpen = false;
      if (updateOutcome.ok) toast.success(updateOutcome.text);
      else toast.error(updateOutcome.text);
      updates = await loadUpdateHistory(loc.id).catch(() => updates);
    } finally {
      updating = false;
    }
  }
  async function rollBack() {
    if (!tenant || !loc) return;
    const ok = await confirmDialog({
      title: 'Roll the Pi back?',
      body:
        'The Pi goes back to the agent it ran before its last update (rolling back again goes forward). ' +
        'It only does this when no orders are open, and printing pauses for about a minute while it restarts.',
      confirmLabel: 'Roll back'
    });
    if (!ok) return;
    updateOutcome = await requestPiUpdate('rollback', tenant.id, loc.id);
    if (updateOutcome.ok) toast.success(updateOutcome.text);
    else toast.error(updateOutcome.text);
    updates = await loadUpdateHistory(loc.id).catch(() => updates);
  }

  // --- danger ---------------------------------------------------------------
  let confirmName = $state('');
  let deleting = $state(false);
  async function deleteStore() {
    if (!tenant || confirmName !== tenant.name) return;
    deleting = true;
    try {
      await adminApi(`/tenants/${tenant.id}`, { method: 'DELETE' });
      toast.success(`${tenant.name} deleted`);
      goto(base + '/admin');
    } catch (e) {
      toast.error(e);
    } finally {
      deleting = false;
    }
  }

  // Same username as now → this is a password reset.
  const resetOnly = $derived(!!tenant?.has_login && login.username.trim() === tenant.username);

  const kindLabel = (k: string) => ({ agent: 'Pi', mobile: 'Mobile', kiosk: 'Kiosk', ingest: 'Ingest' })[k] ?? k;
  const outcomeTone = (o: string) =>
    o === 'ok' || o === 'fixed' ? 'ok' : o === 'info' ? 'info' : o === 'abandoned' ? 'warn' : ['needs_part', 'needs_owner', 'not_fixed'].includes(o) ? 'bad' : 'mute';
</script>

<svelte:head><title>{tenant?.name ?? 'Store'} — quicc admin</title></svelte:head>

{#if loading && !ov}
  <Skeleton lines={6} />
{:else if missing || !tenant}
  <EmptyState icon="store" title="Store not found" body="It may have been deleted.">
    {#snippet action()}<Button href="{base}/admin/tenants">All stores</Button>{/snippet}
  </EmptyState>
{:else}
  <PageHeader title={tenant.name} crumbs={[{ label: 'Stores', href: base + '/admin/tenants' }, { label: tenant.name }]}>
    {#snippet badges()}
      <Badge tone={PI_BADGE[pi.pi].tone} dot>{pi.pi === 'online' ? 'Pi online' : pi.pi === 'offline' ? 'Pi offline' : 'No Pi yet'}</Badge>
      <Badge mono title="Vendor orders with this store_id route here">{tenant.store_id != null ? `store ${tenant.store_id}` : 'no store id'}</Badge>
      <Badge mono tone={tenant.has_login ? 'mute' : 'warn'}>{tenant.has_login ? tenant.username : 'no login'}</Badge>
      {#if !loc}<Badge tone="bad">no location</Badge>{/if}
    {/snippet}
    {#snippet actions()}
      <Button variant="primary" icon="eye" loading={opening} disabled={!loc} onclick={() => openStore()}>Open as store</Button>
    {/snippet}
  </PageHeader>

  <Tabs {tabs} bind:value={tab} label="Store sections" idPrefix="td" onchange={setTab} />

  <div id="td-panel" role="tabpanel" aria-labelledby="td-{tab}" class="flex flex-col gap-4">
    {#if tab === 'overview'}
      <div class="grid gap-4" style="grid-template-columns: repeat(auto-fit, minmax(280px, 1fr))">
        <div class="card px-5 py-[18px]">
          <div class="eyebrow">Pi</div>
          <div class="mt-1.5">
            <div class="kv"><span>Status</span><span><Badge tone={PI_BADGE[pi.pi].tone} dot>{pi.pi === 'online' ? 'Connected' : PI_BADGE[pi.pi].label}</Badge></span></div>
            <div class="kv"><span>{pi.pi === 'online' ? 'Last heartbeat' : 'Last seen'}</span><span>{pi.lastSeen ? timeAgo(pi.lastSeen) : '—'}</span></div>
            <div class="kv">
              <span>Version</span>
              <span class="mono">
                {info.version || '—'}
                {#if info.update?.updateAvailable}
                  <span class="text-warn"> · update available{info.update.latestVersion ? ` → ${info.update.latestVersion}` : ''}</span>
                {/if}
              </span>
            </div>
            {#if lastUpdate || info.update?.last}
              {@const lu = lastUpdate ?? { action: (info.update?.last?.action === 'rollback' ? 'rollback' : 'update') as 'update' | 'rollback', state: info.update?.last?.state ?? '' }}
              {@const b = updateStateBadge(lu)}
              <div class="kv">
                <span>Last update</span>
                <span class="text-right">
                  <Badge tone={b.tone}>{lu.action === 'rollback' ? 'Roll back' : 'Update'}: {b.label}</Badge>
                  {#if lastUpdate}<span class="block text-xs text-muted mt-0.5">{timeAgo(lastUpdate.updated_at)}{lastUpdate.to_version ? ` · ${lastUpdate.to_version}` : ''}</span>{/if}
                </span>
              </div>
            {/if}
            <div class="kv">
              <span>Printers</span>
              <span class="mono {info.printersDown ? 'text-bad' : ''}">
                {info.printersUp === null ? '—' : `${info.printersUp} / ${info.printersUp + (info.printersDown ?? 0)} up`}
              </span>
            </div>
            {#if info.problems.length}
              <div class="kv"><span>Problems</span><span class="text-warn">{info.problems.join(' · ')}</span></div>
            {/if}
          </div>
          <div class="flex flex-wrap gap-2 mt-3">
            <Button size="sm" icon="download" disabled={!loc} onclick={() => ((forceUpdate = false), (updateOpen = true))}>Update Pi</Button>
            <Button size="sm" variant="ghost" disabled={!loc} onclick={rollBack}>Roll back</Button>
          </div>
          {#if updateOutcome}
            <p class="m-0 mt-2 text-[13px] {updateOutcome.ok ? 'text-ok' : 'text-warn'}" role="status">{updateOutcome.text}</p>
          {/if}
        </div>
        <div class="card px-5 py-[18px]">
          <div class="eyebrow">Today</div>
          <div class="mt-1.5">
            <div class="kv"><span>Orders</span><span class="mono">{todays.length}</span></div>
            <div class="kv"><span>Waiting</span><span class="mono">{todays.filter((o) => o.status === 'received' || o.status === 'validated').length}</span></div>
            <div class="kv"><span>Sales</span><span class="mono">{dollars(todays.reduce((n, o) => n + (o.total_cents || 0), 0))}</span></div>
            <div class="kv"><span>Last order</span><span>{orders[0] ? timeAgo(orders[0].received_at) : '—'}</span></div>
          </div>
        </div>
        <div class="card px-5 py-[18px]">
          <div class="eyebrow">Store</div>
          <div class="mt-1.5">
            <div class="kv"><span>Manager login</span><span class="mono">{tenant.has_login ? tenant.username : '—'}</span></div>
            <div class="kv"><span>Time zone</span><span>{loc ? tzShort(loc.timezone) : '—'}</span></div>
            <div class="kv"><span>Open today</span><span class="mono">{loc ? todayHours(loc.config) : '—'}</span></div>
          </div>
        </div>
        <div class="card px-5 py-[18px]">
          <form
            class="flex flex-col gap-2.5"
            onsubmit={(e) => {
              e.preventDefault();
              saveStoreId();
            }}
          >
            <label class="eyebrow" for="sid">Online ordering store ID</label>
            <div class="flex flex-wrap gap-2">
              <input
                id="sid"
                class="field mono flex-1 min-w-[120px]"
                inputmode="numeric"
                bind:value={storeIdText}
                placeholder="e.g. 9003"
                autocomplete="off"
                aria-invalid={!!storeIdErr || undefined}
                aria-describedby="sid-help"
              />
              <Button type="submit" variant="primary" disabled={storeIdText.trim() === (tenant.store_id == null ? '' : String(tenant.store_id))}>Save</Button>
              {#if tenant.store_id != null}<Button onclick={() => saveStoreId(true)}>Clear</Button>{/if}
            </div>
            {#if storeIdErr}<p class="field-error !m-0" role="alert">{storeIdErr}</p>{/if}
            <p id="sid-help" class="help !m-0">
              Online orders whose <span class="mono">store_id</span> matches route here. Without one, orders fall back to the store-name match.
            </p>
          </form>
        </div>
      </div>
      <p class="text-xs text-muted mono break-all">tenant {tenant.id} · slug {tenant.slug}{loc ? ` · location ${loc.id} (${loc.slug})` : ''}</p>
    {:else if tab === 'login'}
      <Card class="max-w-[560px]">
        <form
          class="flex flex-col gap-4"
          onsubmit={(e) => {
            e.preventDefault();
            saveLogin();
          }}
        >
          <div>
            <h2 class="m-0 text-base font-semibold">Manager login</h2>
            <p class="mt-1 text-muted">
              {tenant.has_login
                ? 'To reset the password, generate a new one and save it. Used on main and to sign in on the Pi; the tablet stays signed in.'
                : 'Used on main and to sign in on the Pi.'}
            </p>
          </div>
          <div>
            <label class="lbl" for="lu">Username</label>
            <input id="lu" class="field mono" bind:value={login.username} placeholder="storename" autocomplete="off" autocapitalize="off" spellcheck="false" />
          </div>
          <div>
            <label class="lbl" for="lp">New password</label>
            <div class="flex flex-wrap gap-2">
              <input id="lp" class="field mono flex-1 min-w-[200px]" bind:value={login.password} autocomplete="new-password" />
              <Button onclick={() => (login.password = generatePassword())}>Generate new</Button>
              <Button icon="copy" aria-label="Copy password" disabled={!login.password} onclick={() => copyText(login.password, 'Password copied')} />
            </div>
          </div>
          {#if loginErr}<p class="field-error !m-0" role="alert">{loginErr}</p>{/if}
          <div><Button type="submit" variant="primary" loading={savingLogin}>{resetOnly ? 'Reset password' : 'Save login'}</Button></div>
        </form>
      </Card>
    {:else if tab === 'location'}
      {#if loc}
        <Card class="max-w-[720px]">
          <div class="flex flex-col gap-4">
            <LocationFields bind:value={locForm} idPrefix="td-loc" />
            <div class="pt-4 border-t border-border">
              <HoursEditor bind:hours idPrefix="td-hours" />
            </div>
            <details class="subcard px-4 py-3">
              <summary class="cursor-pointer font-medium text-text2">Advanced — raw config JSON</summary>
              <div class="flex flex-col gap-4 mt-3">
                <div>
                  <label class="lbl" for="loc-cfg">Location config (saved with the location)</label>
                  <textarea id="loc-cfg" class="field mono h-40" bind:value={locConfigText} spellcheck="false"></textarea>
                  <p class="help">Holds receipt settings and <span class="mono">opening_hours</span> — the hours form above replaces <span class="mono">opening_hours</span> on save.</p>
                </div>
                <div>
                  <label class="lbl" for="t-cfg">Store (tenant) config</label>
                  <textarea id="t-cfg" class="field mono h-28" bind:value={tenantConfigText} spellcheck="false"></textarea>
                  <div class="mt-2"><Button size="sm" onclick={saveTenantConfig}>Save store config</Button></div>
                </div>
              </div>
            </details>
            <div class="flex flex-wrap gap-2.5">
              <Button variant="primary" loading={savingLoc} onclick={saveLocation}>Save details & hours</Button>
            </div>
          </div>
        </Card>
      {:else}
        <EmptyState icon="alert" title="This store has no location" body="Every store gets its location when it's created — this one predates that. Re-run the migrations." />
      {/if}
    {:else if tab === 'pi'}
      {#if minted}
        <SecretReveal title="Pi token — copy it now, it is never shown again" secret={minted.api_key} ondismiss={() => (minted = null)}>
          Device <span class="mono">{minted.device.name}</span>. Paste this token into the Pi's setup screen to link it (the usual way is signing in on the Pi with the manager login).
        </SecretReveal>
      {/if}
      <Card title="Pi & device keys" padded={false}>
        {#snippet actions()}
          <Button icon="key" disabled={!loc} onclick={() => (mintOpen = true)}>Create a Pi key by hand</Button>
        {/snippet}
        {#if devices.length}
          <div class="overflow-x-auto">
            <table class="tbl stack min-w-[680px]">
              <thead><tr><th>Name</th><th>Kind</th><th>Key</th><th>Last used</th><th><span class="sr-only">Actions</span></th></tr></thead>
              <tbody>
                {#each devices as d (d.id)}
                  <tr>
                    <td class="font-semibold"><span>{d.name}{#if d.revoked_at}<Badge tone="bad" class="ml-2">Revoked</Badge>{/if}</span></td>
                    <td data-label="Kind">{kindLabel(d.kind)}</td>
                    <td data-label="Key" class="mono">{d.key_prefix}…</td>
                    <td data-label="Last used" title={fmtTime(d.last_used_at)}>{d.last_used_at ? timeAgo(d.last_used_at) : 'never'}</td>
                    <td class="text-right whitespace-nowrap stack-actions">
                      <Button size="sm" disabled={!!d.revoked_at} onclick={() => revokeDevice(d)}>Revoke</Button>
                      <Button size="sm" variant="danger-ghost" onclick={() => deleteDevice(d)}>Delete</Button>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else}
          <EmptyState icon="tablet" title="No device keys yet" body="The Pi gets its key when someone signs in on it with the manager login." />
        {/if}
      </Card>
      <Card title="Tablet report keys" subtitle="Write-only keys a tablet uses to send diagnostics straight to main" padded={false}>
        {#if tokens.length}
          <div class="overflow-x-auto">
            <table class="tbl stack min-w-[640px]">
              <thead><tr><th>Tablet</th><th>Key</th><th>Created</th><th>Last used</th><th><span class="sr-only">Actions</span></th></tr></thead>
              <tbody>
                {#each tokens as t (t.id)}
                  <tr>
                    <td class="font-semibold"><span>{t.tablet_name || t.tablet_id}{#if t.revoked_at}<Badge tone="bad" class="ml-2">Revoked</Badge>{/if}</span></td>
                    <td data-label="Key" class="mono">{t.key_prefix}…</td>
                    <td data-label="Created">{fmtTime(t.created_at)}</td>
                    <td data-label="Last used">{t.last_used_at ? timeAgo(t.last_used_at) : 'never'}</td>
                    <td class="text-right stack-actions"><Button size="sm" disabled={!!t.revoked_at} onclick={() => revokeToken(t)}>Revoke</Button></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else}
          <p class="m-0 px-5 pb-5 text-muted">No tablet has a report key yet.</p>
        {/if}
      </Card>
      <Card title="Update history" subtitle="Pi updates and roll backs — from here, the console, the Pi's own page or a fresh card" padded={false}>
        {#if updates.length}
          <div class="overflow-x-auto">
            <table class="tbl stack min-w-[720px]">
              <thead><tr><th>Action</th><th>Who</th><th>State</th><th>Versions</th><th>Error</th><th>When</th></tr></thead>
              <tbody>
                {#each updates as u (u.id)}
                  {@const b = updateStateBadge(u)}
                  <tr>
                    <td class="font-semibold"><span>{u.action === 'rollback' ? 'Roll back' : 'Update'}{#if u.force}<Badge tone="warn" class="ml-2">Force</Badge>{/if}</span></td>
                    <td data-label="Who">{requesterText(u.requested_by)}</td>
                    <td data-label="State"><Badge tone={b.tone}>{b.label}</Badge></td>
                    <td data-label="Versions" class="mono">{u.from_version || u.to_version ? `${u.from_version || '?'} → ${u.to_version || '?'}` : '—'}</td>
                    <td data-label="Error" class="text-[13px] {u.error ? 'text-bad' : 'text-muted'}">{updateErrorText(u) || '—'}</td>
                    <td data-label="When" title={fmtTime(u.requested_at)}>{timeAgo(u.updated_at)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else}
          <p class="m-0 px-5 pb-5 text-muted">No updates yet. Use “Update Pi” on the Overview tab.</p>
        {/if}
      </Card>
      <Dialog bind:open={mintOpen} title="Create a Pi key by hand" description="Mints an agent token for this store. Paste it into the Pi's setup screen — only needed when the manager-login link isn't an option.">
        <form
          id="mint-form"
          onsubmit={(e) => {
            e.preventDefault();
            mintAgent();
          }}
        >
          <label class="lbl" for="mint-name">Name</label>
          <input id="mint-name" class="field" bind:value={mintName} placeholder="e.g. Kitchen Pi" />
        </form>
        {#snippet footer()}
          <Button onclick={() => (mintOpen = false)}>Cancel</Button>
          <Button variant="primary" type="submit" form="mint-form" loading={minting}>Create key</Button>
        {/snippet}
      </Dialog>
    {:else if tab === 'diag'}
      <section class="card py-1">
        {#each diags as d, i (d.id)}
          <div class="flex flex-wrap items-center gap-3 px-[18px] py-3.5 {i ? 'border-t border-border' : ''}">
            {#if d.resolved_at}<Badge tone="ok">Resolved</Badge>{:else}<Badge tone="warn">Open</Badge>{/if}
            <Badge tone={outcomeTone(d.outcome)}>{d.outcome.replaceAll('_', ' ')}</Badge>
            <span class="flex-1 min-w-[220px]">
              <b>{d.headline || d.scenario.replaceAll('_', ' ') || '(no headline)'}</b>
              — {d.tablet_name || 'tablet'}{d.app_version ? ` · Quibase ${d.app_version}` : ''}
            </span>
            <span class="text-muted">{fmtTime(d.received_at)}</span>
            <Button size="sm" href="{base}/admin/diagnostics?id={d.id.slice(0, 8)}">View</Button>
          </div>
        {:else}
          <EmptyState icon="activity" title="No diagnostics reports" body="Reports appear here when the store's tablet runs a troubleshooting guide." />
        {/each}
      </section>
    {:else if tab === 'danger'}
      <section class="card p-[22px] max-w-[620px] flex flex-col gap-3.5 !border-bad">
        <div>
          <h2 class="m-0 text-base font-semibold text-bad">Delete this store</h2>
          <p class="mt-1.5 text-text2">Removes the store, its location, menu, printers, device keys and every order. The Pi unlinks itself. This can't be undone.</p>
        </div>
        <div>
          <label class="lbl" for="cf">Type <b>{tenant.name}</b> to confirm</label>
          <input id="cf" class="field" bind:value={confirmName} autocomplete="off" />
        </div>
        <div><Button variant="danger" loading={deleting} disabled={confirmName !== tenant.name} onclick={deleteStore}>Delete store</Button></div>
      </section>
    {/if}
  </div>

  <Dialog bind:open={updateOpen} title="Update the Pi?" description="It installs the newest agent published for this Pi (the signed release, checked like a fresh card)." width={480}>
    <div class="flex flex-col gap-3 text-[14px]">
      <p class="m-0">
        <b>Normally</b> the Pi only updates right after <b>Close batch</b> — when the open batch has no orders. Otherwise it says no and nothing
        changes.
      </p>
      <p class="m-0">Printing pauses for about a minute while the new agent starts. If it isn't healthy within 2 minutes, the Pi rolls back by itself.</p>
      <label class="flex items-start gap-2 subcard px-3 py-2.5">
        <input type="checkbox" class="check mt-0.5" bind:checked={forceUpdate} />
        <span>
          <b>Force</b> — install as soon as no orders are open
          <span class="block text-[13px] text-muted">Skips the after-close rule; the Pi still waits until nothing is waiting, cooking, ready or out for delivery.</span>
        </span>
      </label>
    </div>
    {#snippet footer()}
      <Button onclick={() => (updateOpen = false)}>Cancel</Button>
      <Button variant={forceUpdate ? 'danger' : 'primary'} loading={updating} onclick={runUpdate}>{forceUpdate ? 'Force update' : 'Update Pi'}</Button>
    {/snippet}
  </Dialog>
{/if}
