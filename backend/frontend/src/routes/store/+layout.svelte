<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { ApiError, errText, storeApi, storeSignOut, type Whoami } from '$lib/api';
  import { storeSession } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import AppShell, { type NavItem } from '$lib/ui/AppShell.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';

  // Store area: needs a manager session (whoami; a 401 → login?next=…). An
  // admin's "Open as store" session shows the lavender banner with Exit.
  let { children } = $props();

  let failed = $state('');
  const who = $derived(storeSession.who);

  const nav: NavItem[] = [
    { href: base + '/store', label: 'Overview', icon: 'dashboard' },
    { href: base + '/store/orders', label: 'Orders', icon: 'receipt', match: base + '/store/orders' },
    { href: base + '/store/menu', label: 'Menu', icon: 'utensils', match: base + '/store/menu' },
    { href: base + '/store/printers', label: 'Printers', icon: 'printer', match: base + '/store/printers' },
    { href: base + '/store/devices', label: 'Devices', icon: 'tablet', match: base + '/store/devices' },
    { href: base + '/store/details', label: 'Details & hours', icon: 'clock', match: base + '/store/details' }
  ];

  onMount(async () => {
    // Never show a previous session's store while the new whoami loads.
    storeSession.who = null;
    try {
      storeSession.who = await storeApi<Whoami>('/session');
    } catch (e) {
      // 401 already redirected to login
      if (!(e instanceof ApiError && e.status === 401)) failed = errText(e);
    }
  });

  async function exitImpersonation() {
    const tid = who?.tenant.id;
    // Leave first: the store pages unmount and stop polling, so nothing is
    // still asking /me/* when the session goes (those would all 401).
    await goto(base + (tid ? `/admin/tenants/${tid}` : '/admin'));
    storeSession.who = null;
    await storeSignOut();
  }

  async function signOut() {
    if (who?.impersonated) return exitImpersonation();
    await goto(base + '/login', { replaceState: true });
    storeSession.who = null;
    await storeSignOut();
    toast.success('Signed out');
  }
</script>

{#if who}
  <AppShell title={who.tenant.name} subtitle={who.location.name} {nav} onsignout={signOut}>
    {#snippet banner()}
      {#if who.impersonated}
        <div class="flex flex-wrap items-center justify-center gap-2.5 px-4 py-2 text-[13px] font-semibold" style="background:#AC94F4;color:#241B47">
          <Icon name="eye" size={16} />
          Viewing as {who.tenant.name} — you're signed in as admin
          <button type="button" class="underline font-semibold" style="color:#241B47" onclick={exitImpersonation}>Exit</button>
        </div>
      {/if}
    {/snippet}
    {@render children()}
  </AppShell>
{:else if failed}
  <div class="max-w-md mx-auto mt-24 card p-6 flex flex-col gap-3">
    <h1 class="text-lg font-semibold m-0">Couldn't load your store</h1>
    <p class="text-text2">{failed}</p>
    <div><button class="btn btn-p" onclick={() => location.reload()}>Try again</button></div>
  </div>
{:else}
  <div class="max-w-3xl mx-auto mt-16 px-4"><Skeleton lines={5} /></div>
{/if}
