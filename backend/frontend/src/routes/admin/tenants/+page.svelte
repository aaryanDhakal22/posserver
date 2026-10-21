<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { loadAdminData, type StoreRow } from '$lib/admin/stores';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import StoresTable from '$lib/components/admin/StoresTable.svelte';

  let rows = $state<StoreRow[]>([]);
  let loading = $state(true);

  async function reload() {
    try {
      rows = (await loadAdminData()).rows;
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
    }
  }
  onMount(reload);
</script>

<svelte:head><title>Stores — quicc admin</title></svelte:head>

<PageHeader title="Stores" subtitle="Every restaurant, its Pi and its manager login">
  {#snippet actions()}
    <Button icon="refresh" onclick={reload} aria-label="Refresh" />
    <Button variant="primary" icon="plus" href="{base}/admin/tenants/new">New store</Button>
  {/snippet}
</PageHeader>

<StoresTable {rows} {loading} />
