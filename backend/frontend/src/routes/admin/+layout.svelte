<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { adminApi, getAdminKey, goToLogin, signOutAdmin, type DiagSummary } from '$lib/api';
  import AppShell, { type NavItem } from '$lib/ui/AppShell.svelte';

  // Admin area: needs the stored ADMIN_API_KEY (else → login?next=…). The new-
  // store wizard renders full-screen without the sidebar.
  let { children } = $props();

  let ready = $state(false);
  let unresolved = $state(0);

  const bare = $derived(page.url.pathname.replace(/\/$/, '') === base + '/admin/tenants/new');

  const nav = $derived<NavItem[]>([
    { href: base + '/admin', label: 'Overview', icon: 'dashboard' },
    { href: base + '/admin/tenants', label: 'Stores', icon: 'store', match: base + '/admin/tenants' },
    { href: base + '/admin/operators', label: 'Operators', icon: 'users', match: base + '/admin/operators' },
    { href: base + '/admin/diagnostics', label: 'Diagnostics', icon: 'activity', match: base + '/admin/diagnostics', badge: unresolved || undefined }
  ]);

  async function countDiag() {
    try {
      unresolved = (await adminApi<DiagSummary[]>('/diag-reports?unresolved=true&limit=200')).length;
    } catch {
      /* the badge is a nicety */
    }
  }

  onMount(() => {
    if (!getAdminKey()) {
      goToLogin();
      return;
    }
    ready = true;
    countDiag();
    const t = setInterval(countDiag, 60000);
    return () => clearInterval(t);
  });

  async function signOut() {
    await signOutAdmin();
    goto(base + '/login', { replaceState: true });
  }
</script>

{#if ready}
  {#if bare}
    {@render children()}
  {:else}
    <AppShell title="quicc" subtitle="Admin" {nav} onsignout={signOut}>
      {@render children()}
    </AppShell>
  {/if}
{/if}
