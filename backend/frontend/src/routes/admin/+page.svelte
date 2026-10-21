<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { timeAgo, hhmm, durationSince } from '$lib/api';
  import { loadAdminData, type AdminData } from '$lib/admin/stores';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import StatTile from '$lib/ui/StatTile.svelte';
  import Badge, { type Tone } from '$lib/ui/Badge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import StoresTable from '$lib/components/admin/StoresTable.svelte';

  let data = $state<AdminData | null>(null);
  let loading = $state(true);
  let loadedAt = $state<string | null>(null);

  async function reload() {
    try {
      data = await loadAdminData();
      loadedAt = new Date().toISOString();
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    reload();
    const t = setInterval(reload, 30000);
    return () => clearInterval(t);
  });

  const rows = $derived(data?.rows ?? []);
  const withPi = $derived(rows.filter((r) => r.pi !== 'none'));
  const online = $derived(rows.filter((r) => r.pi === 'online'));
  const offline = $derived(rows.filter((r) => r.pi === 'offline'));
  const ordersToday = $derived(rows.reduce((n, r) => n + r.ordersToday, 0));
  const busiest = $derived(
    rows
      .filter((r) => r.ordersToday > 0)
      .sort((a, b) => b.ordersToday - a.ordersToday)
      .slice(0, 2)
      .map((r) => `${r.tenant.name} ${r.ordersToday}`)
      .join(' · ')
  );

  interface Attention {
    key: string;
    tone: Tone;
    tag: string;
    store: string;
    text: string;
    href: string;
    cta: string;
  }
  const attention = $derived.by<Attention[]>(() => {
    if (!data) return [];
    const out: Attention[] = [];
    for (const r of offline) {
      out.push({
        key: 'off-' + r.tenant.id,
        tone: 'bad',
        tag: 'Offline',
        store: r.tenant.name,
        text: r.lastSeen ? `Pi hasn't checked in for ${durationSince(r.lastSeen)}` : "Pi has a key but hasn't connected yet",
        href: `${base}/admin/tenants/${r.tenant.id}`,
        cta: 'Open store'
      });
    }
    for (const r of rows) {
      if (r.pi !== 'online') continue;
      if (r.printersDown)
        out.push({
          key: 'pr-' + r.tenant.id,
          tone: 'warn',
          tag: 'Printer',
          store: r.tenant.name,
          text: `${r.printersDown} printer${r.printersDown === 1 ? '' : 's'} not responding (as of ${hhmm(r.health?.at)})`,
          href: `${base}/admin/tenants/${r.tenant.id}`,
          cta: 'Open store'
        });
      for (const p of r.problems)
        out.push({
          key: 'pb-' + r.tenant.id + p,
          tone: 'warn',
          tag: 'Pi',
          store: r.tenant.name,
          text: p,
          href: `${base}/admin/tenants/${r.tenant.id}`,
          cta: 'Open store'
        });
    }
    for (const d of data.unresolved.slice(0, 5)) {
      out.push({
        key: 'dg-' + d.id,
        tone: 'info',
        tag: 'Diagnostics',
        store: d.store_name,
        text: `tablet sent “${d.headline || d.scenario.replaceAll('_', ' ') || d.kind}”`,
        href: `${base}/admin/diagnostics?id=${d.id.slice(0, 8)}`,
        cta: 'View report'
      });
    }
    return out;
  });
</script>

<svelte:head><title>Overview — quicc admin</title></svelte:head>

<PageHeader title="Overview" subtitle="Every store at a glance · {loadedAt ? 'updated ' + timeAgo(loadedAt) : 'loading…'}">
  {#snippet actions()}
    <Button icon="refresh" onclick={reload} aria-label="Refresh" />
    <Button variant="primary" icon="plus" href="{base}/admin/tenants/new">New store</Button>
  {/snippet}
</PageHeader>

<div class="grid gap-3.5" style="grid-template-columns: repeat(auto-fit, minmax(200px, 1fr))">
  <StatTile {loading} label="Stores" value={rows.length} sub="{withPi.length} with a Pi linked" />
  <StatTile
    {loading}
    label="Pis online"
    value={online.length}
    suffix="/ {withPi.length}"
    sub={offline.length ? `${offline.map((r) => r.tenant.name).slice(0, 2).join(', ')} offline` : 'all connected'}
    subTone={offline.length ? 'bad' : 'muted'}
  />
  <StatTile {loading} label="Orders today" value={ordersToday} sub={busiest || 'no orders yet today'} />
  <StatTile
    {loading}
    label="Diagnostics"
    value={data?.unresolved.length ?? 0}
    sub="unresolved reports"
    subTone={data?.unresolved.length ? 'warn' : 'muted'}
    href="{base}/admin/diagnostics"
  />
</div>

<section class="card py-1" aria-labelledby="attn">
  <div class="flex items-center gap-2.5 px-[18px] py-3">
    <span class={attention.length ? 'text-warn' : 'text-ok'}><Icon name={attention.length ? 'alert' : 'check'} size={18} /></span>
    <h2 id="attn" class="m-0 text-[15px] font-semibold">Needs attention</h2>
  </div>
  {#if loading}
    <div class="px-[18px] pb-4"><Skeleton lines={2} /></div>
  {:else if attention.length === 0}
    <p class="m-0 px-[18px] py-3 border-t border-border text-muted">Nothing right now — every Pi is online and no reports are open.</p>
  {:else}
    {#each attention as a (a.key)}
      <div class="flex flex-wrap items-center gap-3 px-[18px] py-3 border-t border-border">
        <Badge tone={a.tone} dot>{a.tag}</Badge>
        <span class="flex-1 min-w-[200px]"><b>{a.store}</b> — {a.text}</span>
        <Button size="sm" href={a.href}>{a.cta}</Button>
      </div>
    {/each}
  {/if}
</section>

<StoresTable {rows} {loading} />
