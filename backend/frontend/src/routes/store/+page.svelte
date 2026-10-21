<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import {
    dollars,
    fmtTime,
    isToday,
    storeApi,
    timeAgo,
    type LivePrinters,
    type StoreFloor,
    type StoreOrder
  } from '$lib/api';
  import { storeSession } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import StatTile from '$lib/ui/StatTile.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { orderBucket } from '$lib/ui/StatusBadge.svelte';
  import OrdersTable from '$lib/components/store/OrdersTable.svelte';
  import { loadPiStatus, PI_NOT_LINKED, type PiLink } from '$lib/store/pi';

  // Store overview: today's floor at a glance. The floor (status, kitchen #,
  // drivers) is decided by the store's tablet and synced here; this view is
  // read-only and refreshes every 15 s.
  let orders = $state<StoreOrder[]>([]);
  let floor = $state<StoreFloor | null>(null);
  let live = $state<LivePrinters | null>(null);
  let pi = $state<PiLink>('unknown');
  let loading = $state(true);

  async function loadFloor() {
    [orders, floor] = await Promise.all([storeApi<StoreOrder[]>('/me/orders?limit=100'), storeApi<StoreFloor>('/me/floor')]);
  }
  async function loadLive() {
    ({ live, pi } = await loadPiStatus());
  }

  onMount(() => {
    Promise.all([loadFloor(), loadLive()])
      .catch((e) => toast.error(e))
      .finally(() => (loading = false));
    const t = setInterval(() => loadFloor().catch(() => {}), 15000);
    return () => clearInterval(t);
  });

  const today = $derived(orders.filter((o) => isToday(o.received_at)));
  const count = (b: string) => today.filter((o) => orderBucket(o) === b).length;
  const sales = $derived(today.reduce((n, o) => n + (o.money?.total_cents || 0), 0));
  const driversOut = $derived(floor?.drivers.filter((d) => d.status === 'out') ?? []);
</script>

<svelte:head><title>Overview — {storeSession.who?.tenant.name ?? 'quicc'}</title></svelte:head>

<PageHeader
  title="Overview"
  subtitle={floor?.last_sync_at ? `Synced from the restaurant tablet ${timeAgo(floor.last_sync_at)} · refreshes every 15 s` : "The restaurant tablet hasn't synced yet"}
/>

<div class="grid gap-3.5" style="grid-template-columns: repeat(auto-fit, minmax(180px, 1fr))">
  <StatTile {loading} label="Orders today" value={today.length} sub={today.length ? `${dollars(sales)} in sales` : 'none yet'} href="{base}/store/orders" />
  <StatTile {loading} label="Waiting" value={count('waiting')} sub="not accepted yet" subTone={count('waiting') ? 'warn' : 'muted'} />
  <StatTile {loading} label="In kitchen" value={count('kitchen')} sub="{count('ready')} ready for a driver" />
  <StatTile {loading} label="Out for delivery" value={count('out')} sub="{driversOut.length} driver{driversOut.length === 1 ? '' : 's'} out" />
</div>

<div class="grid gap-4 lg:grid-cols-2">
  <Card title="Drivers" subtitle="In queue order, as the tablet has them">
    {#if loading}
      <Skeleton lines={2} />
    {:else if floor?.drivers.length}
      <div class="flex flex-wrap gap-2">
        {#each floor.drivers as d (d.id)}
          <Badge tone={d.status === 'out' ? 'brand' : 'mute'} dot={d.status === 'out'}>{d.name}{d.status === 'out' ? ' · out' : ''}</Badge>
        {/each}
      </div>
    {:else}
      <p class="m-0 text-muted">No drivers clocked in.</p>
    {/if}
  </Card>
  <Card title="Printers" subtitle={pi !== 'none' && live?.reported_at ? `Reported by the Pi ${timeAgo(live.reported_at)}` : 'Live status from the Pi'}>
    {#snippet actions()}<Button size="sm" href="{base}/store/printers">Manage</Button>{/snippet}
    {#if loading}
      <Skeleton lines={2} />
    {:else if pi === 'none'}
      <p class="m-0 text-muted">{PI_NOT_LINKED}</p>
    {:else if !live}
      <p class="m-0 text-muted">Couldn't read printer status.</p>
    {:else if !live.agent_online}
      <p class="m-0 text-warn font-medium">The Pi is offline — printer status unknown.</p>
    {:else if live.printers?.length}
      <ul class="flex flex-col gap-2">
        {#each live.printers as p (p.name)}
          <li class="flex items-center gap-2.5" title={p.last_checked ? 'checked ' + fmtTime(p.last_checked) : undefined}>
            <Badge tone={p.up ? 'ok' : 'bad'} dot>{p.up ? 'Ready' : 'Not responding'}</Badge>
            <span class="font-medium">{p.name}</span>
            <span class="mono text-muted text-[13px]">{p.ip}</span>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="m-0 text-muted">The Pi hasn't reported any printers.</p>
    {/if}
  </Card>
</div>

<Card title="Recent orders" padded={false}>
  {#snippet actions()}<Button size="sm" href="{base}/store/orders" iconRight="chevron-right">All orders</Button>{/snippet}
  {#if loading}
    <div class="px-5 pb-5"><Skeleton lines={4} /></div>
  {:else}
    <OrdersTable orders={orders.slice(0, 10)} />
  {/if}
</Card>
