<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, timeAgo, type StoreFloor, type StoreOrder } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import { orderBucket } from '$lib/ui/StatusBadge.svelte';
  import OrdersTable from '$lib/components/store/OrdersTable.svelte';

  // Read-only floor list: status, kitchen numbers and drivers are set on the
  // restaurant tablet and synced here. Refreshes every 15 s.
  let orders = $state<StoreOrder[]>([]);
  let floor = $state<StoreFloor | null>(null);
  let loading = $state(true);
  let status = $state('all');
  let q = $state('');

  async function load() {
    [orders, floor] = await Promise.all([storeApi<StoreOrder[]>('/me/orders?limit=100'), storeApi<StoreFloor>('/me/floor')]);
  }
  onMount(() => {
    load()
      .catch((e) => toast.error(e))
      .finally(() => (loading = false));
    const t = setInterval(() => load().catch(() => {}), 15000);
    return () => clearInterval(t);
  });

  const n = (b: string) => orders.filter((o) => orderBucket(o) === b).length;
  const options = $derived([
    { value: 'all', label: 'All', count: orders.length },
    { value: 'waiting', label: 'Waiting', count: n('waiting') },
    { value: 'kitchen', label: 'In kitchen', count: n('kitchen') },
    { value: 'ready', label: 'Ready', count: n('ready') },
    { value: 'out', label: 'Out', count: n('out') },
    { value: 'done', label: 'Done', count: n('done') }
  ]);
  const shown = $derived.by(() => {
    const s = q.trim().toLowerCase();
    return orders.filter(
      (o) =>
        (status === 'all' || orderBucket(o) === status) &&
        (!s ||
          [o.source_event_id, o.kitchen_order_number ?? '', o.customer?.display_name ?? '', o.driver_name ?? '']
            .join(' ')
            .toLowerCase()
            .includes(s))
    );
  });
</script>

<svelte:head><title>Orders — quicc</title></svelte:head>

<PageHeader
  title="Orders"
  subtitle={floor?.last_sync_at
    ? `Status, kitchen numbers and drivers come from the restaurant tablet · synced ${timeAgo(floor.last_sync_at)}`
    : "Status, kitchen numbers and drivers come from the restaurant tablet · it hasn't synced yet"}
>
  {#snippet actions()}
    <Button icon="refresh" aria-label="Refresh" onclick={() => load().catch((e) => toast.error(e))} />
  {/snippet}
</PageHeader>

<div class="flex flex-wrap items-center gap-2.5">
  <SegmentedControl label="Status" {options} bind:value={status} />
  <SearchInput id="order-search" label="Search orders" placeholder="Order #, kitchen #, customer, driver" bind:value={q} class="flex-1 min-w-[220px] max-w-[360px]" />
</div>

<section class="card overflow-hidden">
  {#if loading}
    <div class="p-5"><Skeleton lines={6} /></div>
  {:else}
    <OrdersTable orders={shown} empty={orders.length ? 'No orders match.' : 'No orders yet.'} />
  {/if}
</section>
<p class="text-xs text-muted -mt-3">Showing the latest {orders.length} orders.</p>
