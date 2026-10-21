<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { getAdminKey, probeStoreSession } from '$lib/api';
  import Brand from '$lib/ui/Brand.svelte';

  // /app → the right home: admin key → admin, store session → store, else login.
  onMount(async () => {
    if (getAdminKey()) return goto(base + '/admin', { replaceState: true });
    const who = await probeStoreSession();
    goto(base + (who ? '/store' : '/login'), { replaceState: true });
  });
</script>

<div class="min-h-screen flex items-center justify-center" aria-busy="true">
  <div class="animate-pulse"><Brand size={44} /></div>
</div>
