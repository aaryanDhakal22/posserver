<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { storeApi, timeAgo, type StoreFloor, type StoreOrder } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import { orderBucket } from '$lib/ui/StatusBadge.svelte';
  import OrdersTable from '$lib/components/store/OrdersTable.svelte';
  import OrderDetail from '$lib/components/store/OrderDetail.svelte';
  import { loadPiStatus, type PiLink } from '$lib/store/pi';
  import { channelLabel, customerName, orderNumber } from '$lib/store/orders';

  // Every order of the store, newest placed first. Read only: status, numbers and drivers are decided on the
  // store's Pi (terminals, tablet) and synced here. A row opens the order (?order=<id>, deep-linkable).
  // "Latest" shows the newest 100 and refreshes every 15 s; a day shows that whole day (history).
  type Row = StoreOrder & { source?: string; placed_at?: string };
  let orders = $state<Row[]>([]);
  let floor = $state<StoreFloor | null>(null);
  let pi = $state<PiLink>('unknown');
  let loading = $state(true);
  let status = $state('all');
  let q = $state('');
  let day = $state(page.url.searchParams.get('date') ?? '');
  let openId = $state<string | null>(page.url.searchParams.get('order'));

  const today = () => new Date().toLocaleDateString('en-CA'); // YYYY-MM-DD, the browser's day

  async function load() {
    const path = day ? `/me/orders?sort=placed&limit=200&date=${day}` : '/me/orders?sort=placed&limit=100';
    [orders, floor] = await Promise.all([storeApi<Row[]>(path), storeApi<StoreFloor>('/me/floor')]);
  }
  function reload() {
    loading = true;
    load()
      .catch((e) => toast.error(e))
      .finally(() => (loading = false));
  }
  onMount(() => {
    reload();
    loadPiStatus().then((r) => (pi = r.pi));
    const t = setInterval(() => {
      if (!day || day === today()) load().catch(() => {});
    }, 15000);
    return () => clearInterval(t);
  });

  function setUrl() {
    const u = new URL(page.url);
    for (const [k, v] of [['order', openId], ['date', day]] as const) {
      if (v) u.searchParams.set(k, v);
      else u.searchParams.delete(k);
    }
    goto(u.pathname + u.search, { replaceState: true, keepFocus: true, noScroll: true });
  }
  function openOrder(id: string) {
    openId = id;
    setUrl();
  }
  function pickDay(v: string) {
    day = v;
    setUrl();
    reload();
  }

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
          [orderNumber(o), o.source_event_id, customerName(o), o.customer?.display_name ?? '', (o.customer as { phone?: string })?.phone ?? '', o.driver_name ?? '', channelLabel(o)]
            .join(' ')
            .toLowerCase()
            .includes(s))
    );
  });
  const syncText = $derived(floor?.last_sync_at ? `last update from the store ${timeAgo(floor.last_sync_at)}` : 'no update from the store yet');
</script>

<svelte:head><title>Orders — quicc</title></svelte:head>

<PageHeader title="Orders" subtitle="Every order from the counter, the call center, online and your website. Click one for everything about it.">
  {#snippet actions()}
    <Button icon="refresh" aria-label="Refresh" onclick={reload} />
  {/snippet}
</PageHeader>

<div class="flex flex-wrap items-center gap-2 -mt-2 text-[13px] text-text2" data-testid="sync-line">
  {#if pi === 'online'}<Badge tone="ok" dot>Pi online</Badge>{:else if pi === 'offline'}<Badge tone="warn" dot>Pi offline</Badge>{:else if pi === 'none'}<Badge tone="mute">No Pi linked</Badge>{/if}
  <span title="The newest change the store's Pi sent main (a new order, accept, ready, a payment…). A quiet store sends nothing, so this can be hours old while the Pi is online.">{syncText}</span>
</div>

<div class="flex flex-wrap items-center gap-2.5">
  <SegmentedControl label="Status" {options} bind:value={status} />
  <SearchInput id="order-search" label="Search orders" placeholder="Number, customer, phone, driver" bind:value={q} class="flex-1 min-w-[220px] max-w-[360px]" />
  <label class="flex items-center gap-2 text-[13px] text-text2">
    Day
    <input type="date" class="field !w-auto" aria-label="Show one day" value={day} max={today()} onchange={(e) => pickDay(e.currentTarget.value)} />
  </label>
  {#if day}<Button size="sm" onclick={() => pickDay('')}>Latest</Button>{/if}
</div>

<section class="card overflow-hidden">
  {#if loading}
    <div class="p-5"><Skeleton lines={6} /></div>
  {:else}
    <OrdersTable orders={shown} empty={orders.length ? 'No orders match.' : day ? 'No orders that day.' : 'No orders yet.'} onopen={openOrder} />
  {/if}
</section>
<p class="text-xs text-muted -mt-3">
  Two kinds of numbers: <b>tickets</b> (1, 2, 3… each day) are the counter's and the call center's; <b>kitchen</b> numbers (from 1100) are online and website orders, given when the kitchen accepts them.
  {day ? `All ${orders.length} orders placed that day.` : `The latest ${orders.length} orders. Pick a day for older ones.`}</p>

<OrderDetail
  bind:id={openId}
  onclose={() => {
    openId = null;
    setUrl();
  }}
/>
