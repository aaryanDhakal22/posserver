<script lang="ts">
  import { onMount } from 'svelte';
  import { adminApi, getAdminKey, ApiError, type OperatorAdmin, type Overview, type Location } from '$lib/api';
  import OperatorRow from '$lib/components/OperatorRow.svelte';

  let operators = $state<OperatorAdmin[]>([]);
  let locations = $state<Location[]>([]);
  let banner = $state<{ msg: string; kind: 'ok' | 'error' | 'info' } | null>(null);
  let showNew = $state(false);
  let nw = $state({ username: '', password: '', display_name: '' });

  // Errors stay until dismissed or replaced; other messages auto-hide. The
  // previous timer is cleared so a new message isn't cut short by an old one.
  let bannerTimer: ReturnType<typeof setTimeout> | undefined;
  function toast(msg: string, kind: 'ok' | 'error' | 'info' = 'ok') {
    clearTimeout(bannerTimer);
    banner = { msg, kind };
    bannerTimer = kind === 'error' ? undefined : setTimeout(() => (banner = null), 4000);
  }
  function dismissBanner() {
    clearTimeout(bannerTimer);
    banner = null;
  }
  function err(e: unknown) { toast(e instanceof ApiError ? e.message : String(e), 'error'); }

  async function reload() {
    if (!getAdminKey()) { operators = []; return; }
    try {
      operators = await adminApi<OperatorAdmin[]>('/operators');
      const ov = await adminApi<Overview>('/overview');
      locations = ov.locations;
    } catch (e) { err(e); }
  }

  async function create() {
    if (!nw.username.trim() || !nw.password || !nw.display_name.trim())
      return toast('username, password and display name are all required', 'error');
    try {
      await adminApi('/operators', { method: 'POST', body: JSON.stringify(nw) });
      nw = { username: '', password: '', display_name: '' };
      showNew = false;
      toast('operator created — assign locations next');
      reload();
    } catch (e) { err(e); }
  }

  onMount(reload);
</script>

<div class="flex flex-wrap items-center justify-between gap-3 mb-4">
  <div>
    <div class="text-lg font-semibold">Operators</div>
    <div class="text-xs text-slate-500 dark:text-slate-400">
      call-center order-takers · username + password → read-only menu token, scoped to allowed locations
    </div>
  </div>
  <button class="btn btn-dark btn-xs" onclick={() => (showNew = !showNew)}>+ new operator</button>
</div>

{#if banner}
  <div
    class="rounded-md border px-4 py-3 text-sm mb-4 flex items-start gap-3 {banner.kind === 'error'
      ? 'border-rose-300 bg-rose-50 text-rose-900 dark:border-rose-900/60 dark:bg-rose-950/60 dark:text-rose-200'
      : 'border-emerald-300 bg-emerald-50 text-emerald-900 dark:border-emerald-900/60 dark:bg-emerald-950/50 dark:text-emerald-200'}"
  >
    <span class="flex-1 min-w-0 break-words">{banner.msg}</span>
    <button type="button" class="shrink-0 opacity-60 hover:opacity-100 leading-none text-base" aria-label="dismiss" title="dismiss" onclick={dismissBanner}>×</button>
  </div>
{/if}

<section class="card">
  {#if showNew}
    <div class="px-4 py-3 border-b border-slate-200 dark:border-slate-800 bg-slate-50 dark:bg-slate-950/40">
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div><label class="lbl">display name</label><input class="field w-full" bind:value={nw.display_name} placeholder="Jane Doe" /></div>
        <div><label class="lbl">username</label><input class="field w-full" bind:value={nw.username} autocomplete="off" placeholder="jane" /></div>
        <div><label class="lbl">password</label><input class="field w-full" type="password" bind:value={nw.password} autocomplete="new-password" /></div>
      </div>
      <div class="flex items-center gap-3 mt-3">
        <button class="btn btn-primary" onclick={create}>create operator</button>
        <span class="text-xs text-slate-500 dark:text-slate-400">After creating, use “locations” to grant menu access.</span>
      </div>
    </div>
  {/if}

  <div class="divide-y divide-slate-200 dark:divide-slate-800">
    {#if operators.length}
      {#each operators as op (op.id)}
        <OperatorRow operator={op} {locations} {reload} {toast} />
      {/each}
    {:else}
      <div class="px-4 py-6 text-sm text-slate-500 dark:text-slate-400">
        {getAdminKey() ? 'No operators yet. Create one above.' : 'Set your ADMIN_API_KEY on the Admin tab first.'}
      </div>
    {/if}
  </div>
</section>
