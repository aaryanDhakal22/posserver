<script lang="ts">
  import { onMount } from 'svelte';
  import {
    dollars,
    errText,
    storeApi,
    timeAgo,
    TERMINAL_DEFAULTS,
    ApiError,
    type CallcenterDevice,
    type CallcenterLogin,
    type CallcenterReport,
    type TerminalSettings
  } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';

  // Store › Staff → Call center (contract/main.md §2.5zh): the idle lock (a
  // store setting), the browsers the store's login opened the call-center app
  // on (revocable), and orders + sales per call-center agent per day.
  let { agents = 0 }: { agents?: number } = $props();

  let devices = $state<CallcenterDevice[] | null>(null);
  let report = $state<CallcenterReport | null>(null);
  let err = $state('');
  let idle = $state('');
  const IDLE_DEFAULT = TERMINAL_DEFAULTS.callcenter_idle_lock_minutes ?? 15;
  let idleSaved = $state(IDLE_DEFAULT);
  let idleErr = $state('');
  let idleSaving = $state(false);
  let from = $state('');
  let to = $state('');
  let reportErr = $state('');
  let reportLoading = $state(false);

  const live = $derived(devices?.filter((d) => !d.revoked_at) ?? []);

  // The call center's own login: a username + password only the call-center
  // app takes (never the store's own login, which opens these pages).
  let login = $state<CallcenterLogin | null>(null);
  let lUser = $state('');
  let lPass = $state('');
  let lErr = $state('');
  let lSaving = $state(false);

  function loginText(x: unknown): string {
    if (x instanceof ApiError) {
      switch (x.code) {
        case 'bad_username':
          return 'A username is 3 to 40 letters, digits, dots, dashes or underscores.';
        case 'bad_password':
          return 'A password is at least 10 characters.';
        case 'same_as_manager':
          return 'Use a different username and password from the store’s own login: this one is for the call center only.';
        case 'username_taken':
          return 'That username is taken. Pick another.';
      }
    }
    return errText(x);
  }

  async function saveLogin(e: SubmitEvent) {
    e.preventDefault();
    lErr = '';
    if (lUser.trim().length < 3) return (lErr = 'A username is 3 to 40 letters, digits, dots, dashes or underscores.');
    if (lPass.length < 10) return (lErr = 'A password is at least 10 characters.');
    if (login?.set && live.length) {
      const ok = await confirmDialog({
        title: 'Change the call-center login?',
        body: `The ${live.length === 1 ? 'computer' : `${live.length} computers`} signed in now ${live.length === 1 ? 'is' : 'are'} signed out at once and need the new login.`,
        confirmLabel: 'Change login',
        danger: true
      });
      if (!ok) return;
    }
    lSaving = true;
    try {
      login = await storeApi<CallcenterLogin>('/me/callcenter/login', {
        method: 'PUT',
        body: JSON.stringify({ username: lUser.trim(), password: lPass })
      });
      lUser = login.username;
      lPass = '';
      toast.success('Call-center login saved');
      await loadDevices();
    } catch (x) {
      lErr = loginText(x);
    } finally {
      lSaving = false;
    }
  }

  async function removeLogin() {
    const ok = await confirmDialog({
      title: 'Remove the call-center login?',
      body: 'Nobody can open the call-center app for this store until you set a new login. Every computer signed in now is signed out.',
      confirmLabel: 'Remove login',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi('/me/callcenter/login', { method: 'DELETE' });
      login = { username: '', set: false, updated_at: null };
      lUser = lPass = '';
      toast.success('Call-center login removed');
      await loadDevices();
    } catch (x) {
      toast.error(errText(x));
    }
  }

  async function loadDevices() {
    devices = await storeApi<CallcenterDevice[]>('/me/callcenter/devices');
  }
  async function loadReport() {
    reportErr = '';
    reportLoading = true;
    try {
      const q = new URLSearchParams();
      if (from) q.set('from', from);
      if (to) q.set('to', to);
      report = await storeApi<CallcenterReport>('/me/reports/callcenter' + (q.size ? '?' + q : ''));
      from = report.from;
      to = report.to;
    } catch (x) {
      reportErr = errText(x);
    } finally {
      reportLoading = false;
    }
  }

  onMount(async () => {
    try {
      const s = await storeApi<TerminalSettings>('/me/terminal/settings');
      idleSaved = s.callcenter_idle_lock_minutes ?? IDLE_DEFAULT;
      idle = String(idleSaved);
      login = await storeApi<CallcenterLogin>('/me/callcenter/login');
      lUser = login.username;
      await Promise.all([loadDevices(), loadReport()]);
    } catch (x) {
      err = errText(x);
    }
  });

  async function saveIdle(e: SubmitEvent) {
    e.preventDefault();
    idleErr = '';
    const n = Number(idle);
    if (!Number.isInteger(n) || n < 1 || n > 240) return (idleErr = 'A whole number of minutes from 1 to 240.');
    idleSaving = true;
    try {
      // Only this field: main keeps every other terminal setting as saved.
      const s = await storeApi<TerminalSettings>('/me/terminal/settings', {
        method: 'PUT',
        body: JSON.stringify({ callcenter_idle_lock_minutes: n })
      });
      idleSaved = s.callcenter_idle_lock_minutes ?? n;
      idle = String(idleSaved);
      toast.success('Call-center lock saved');
    } catch (x) {
      idleErr = errText(x);
    } finally {
      idleSaving = false;
    }
  }

  async function revoke(d: CallcenterDevice) {
    const ok = await confirmDialog({
      title: `Remove “${d.name}”?`,
      body: 'The call-center app on that computer is signed out at once. It needs the store’s login again to come back.',
      confirmLabel: 'Remove',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi('/me/callcenter/devices/' + d.id, { method: 'DELETE' });
      toast.success(`${d.name} removed`);
      await loadDevices();
    } catch (x) {
      toast.error(errText(x));
    }
  }
</script>

<Card
  title="Call center"
  subtitle={`${agents} agent${agents === 1 ? '' : 's'} · ${live.length} computer${live.length === 1 ? '' : 's'} signed in`}
>
  <div class="px-5 pb-5 flex flex-col gap-5" data-testid="callcenter-card">
    {#if err}<p class="field-error !mt-0" role="alert">{err}</p>{/if}
    <p class="help !mt-0">
      The call center takes phone orders in its own app (callcenter.quiccpos.com). A computer there is opened once with the
      call-center login below; each agent then uses their own 4-digit PIN (add them above with the role Call center).
    </p>

    <section class="flex flex-col gap-2" aria-labelledby="cc-login-title">
      <h3 class="sec-title" id="cc-login-title">Call-center login</h3>
      <form class="flex flex-wrap items-end gap-3" onsubmit={saveLogin} data-testid="callcenter-login">
        <div>
          <label class="lbl" for="cc-user">Username</label>
          <input id="cc-user" class="field" bind:value={lUser} maxlength="40" autocomplete="off" autocapitalize="none" spellcheck="false" />
        </div>
        <div>
          <label class="lbl" for="cc-pass">{login?.set ? 'New password' : 'Password'}</label>
          <input id="cc-pass" class="field" type="password" bind:value={lPass} maxlength="128" autocomplete="new-password" />
        </div>
        <Button type="submit" loading={lSaving}>{login?.set ? 'Change login' : 'Set login'}</Button>
        {#if login?.set}<Button variant="ghost" onclick={removeLogin}>Remove</Button>{/if}
        <p class="help !mt-0 basis-full">
          {#if lErr}<span class="field-error" role="alert">{lErr}</span>
          {:else if login === null}Loading…
          {:else if login.set}Set (username <span class="mono">{login.username}</span>). Give it to the call center only: it opens the call-center app and nothing else. Changing it signs every computer out.
          {:else}Not set: nobody can open the call-center app for this store yet. Never use the store’s own login here.{/if}
        </p>
      </form>
    </section>

    <form class="flex flex-wrap items-end gap-3" onsubmit={saveIdle}>
      <div>
        <label class="lbl" for="cc-idle">Ask for the PIN again after<span class="sr-only"> (minutes idle)</span></label>
        <div class="unit-field w-[160px]">
          <input id="cc-idle" class="field mono" inputmode="numeric" bind:value={idle} aria-invalid={idleErr ? true : undefined} />
          <span class="unit" aria-hidden="true">min</span>
        </div>
      </div>
      <Button type="submit" loading={idleSaving} disabled={idle === String(idleSaved)}>Save</Button>
      <p class="help !mt-0 basis-full">
        {#if idleErr}<span class="field-error" role="alert">{idleErr}</span>{:else}Minutes without a click or a key. Default {IDLE_DEFAULT}.{/if}
      </p>
    </form>

    <section class="flex flex-col gap-2" aria-labelledby="cc-dev-title">
      <h3 class="sec-title" id="cc-dev-title">Computers</h3>
      {#if devices === null}
        <Skeleton lines={2} />
      {:else if !devices.length}
        <p class="help !mt-0">None yet. A computer shows here after someone signs in to the call-center app with the store’s login.</p>
      {:else}
        <div class="overflow-x-auto">
          <table class="tbl stack min-w-[560px]" data-testid="callcenter-devices">
            <thead><tr><th>Computer</th><th>Agent now</th><th>Last used</th><th><span class="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {#each devices as d (d.id)}
                <tr class:opacity-60={!!d.revoked_at}>
                  <td data-label="Computer">{d.name}</td>
                  <td data-label="Agent now">
                    {#if d.revoked_at}<Badge tone="mute">Removed</Badge>{:else if d.staff_name}{d.staff_name}{:else}<span class="text-muted">Locked</span>{/if}
                  </td>
                  <td data-label="Last used">{d.last_seen_at ? timeAgo(d.last_seen_at) : 'never'}</td>
                  <td>
                    {#if !d.revoked_at}
                      <Button size="sm" variant="ghost" onclick={() => revoke(d)} aria-label="Remove {d.name}">Remove</Button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <section class="flex flex-col gap-2" aria-labelledby="cc-rep-title">
      <h3 class="sec-title" id="cc-rep-title">Orders by agent</h3>
      <form class="flex flex-wrap items-end gap-3" onsubmit={(e) => { e.preventDefault(); loadReport(); }}>
        <div><label class="lbl" for="cc-from">From</label><input id="cc-from" class="field" type="date" bind:value={from} /></div>
        <div><label class="lbl" for="cc-to">To</label><input id="cc-to" class="field" type="date" bind:value={to} /></div>
        <Button type="submit" loading={reportLoading}>Show</Button>
      </form>
      {#if reportErr}<p class="field-error !mt-0" role="alert">{reportErr}</p>{/if}
      {#if report === null}
        {#if !reportErr}<Skeleton lines={2} />{/if}
      {:else if !report.rows.length}
        <p class="help !mt-0">No call-center orders in these days.</p>
      {:else}
        <div class="overflow-x-auto">
          <table class="tbl stack min-w-[640px]" data-testid="callcenter-report">
            <thead>
              <tr><th>Day</th><th>Agent</th><th class="text-right">Orders</th><th class="text-right">Pickup</th><th class="text-right">Delivery</th>
                <th class="text-right">Cancelled</th><th class="text-right">Sales</th><th class="text-right">Cards</th><th class="text-right">Tips</th></tr>
            </thead>
            <tbody>
              {#each report.rows as r (r.day + r.staff_id)}
                <tr>
                  <td data-label="Day">{r.day}</td>
                  <td data-label="Agent">{r.staff_name || 'Unknown'}</td>
                  <td data-label="Orders" class="text-right mono">{r.orders}</td>
                  <td data-label="Pickup" class="text-right mono">{r.pickup}</td>
                  <td data-label="Delivery" class="text-right mono">{r.delivery}</td>
                  <td data-label="Cancelled" class="text-right mono">{r.voided}</td>
                  <td data-label="Sales" class="text-right mono">{dollars(r.sales_cents)}</td>
                  <td data-label="Cards" class="text-right mono">{dollars(r.card_cents)}</td>
                  <td data-label="Tips" class="text-right mono">{dollars(r.tips_cents)}</td>
                </tr>
              {/each}
              <tr class="font-semibold">
                <td data-label="Day">Total</td><td></td>
                <td data-label="Orders" class="text-right mono">{report.totals.orders}</td><td></td><td></td>
                <td data-label="Cancelled" class="text-right mono">{report.totals.voided}</td>
                <td data-label="Sales" class="text-right mono">{dollars(report.totals.sales_cents)}</td>
                <td data-label="Cards" class="text-right mono">{dollars(report.totals.card_cents)}</td>
                <td data-label="Tips" class="text-right mono">{dollars(report.totals.tips_cents)}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="help !mt-0">An order counts for the agent who placed it, on the day it was placed (store time). Sales leave cancelled orders out.</p>
      {/if}
    </section>
  </div>
</Card>
