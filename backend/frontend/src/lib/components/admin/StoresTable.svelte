<script lang="ts">
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { timeAgo } from '$lib/api';
  import { PI_BADGE, type StoreRow } from '$lib/admin/stores';
  import Badge from '$lib/ui/Badge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Button from '$lib/ui/Button.svelte';

  let { rows, loading = false }: { rows: StoreRow[]; loading?: boolean } = $props();

  let q = $state('');
  const shown = $derived.by(() => {
    const s = q.trim().toLowerCase();
    if (!s) return rows;
    return rows.filter((r) =>
      [r.tenant.name, r.tenant.store_id ?? '', r.tenant.username, r.tenant.slug].join(' ').toLowerCase().includes(s)
    );
  });

  const href = (r: StoreRow) => `${base}/admin/tenants/${r.tenant.id}`;
  function printers(r: StoreRow): string {
    if (r.printersUp === null && r.printersDown === null) return '—';
    const up = r.printersUp ?? 0;
    return `${up} / ${up + (r.printersDown ?? 0)} up`;
  }
  function seen(r: StoreRow): string {
    if (r.pi === 'online') return r.lastSeen ? `seen ${timeAgo(r.lastSeen)}` : 'connected';
    if (r.pi === 'offline') return r.lastSeen ? `last seen ${timeAgo(r.lastSeen)}` : 'never connected';
    return 'set up the Pi next';
  }
</script>

<section class="card overflow-hidden">
  <div class="flex flex-wrap items-center justify-between gap-3 px-[18px] py-3.5">
    <h2 class="m-0 text-[15px] font-semibold">Stores</h2>
    <SearchInput id="store-search" label="Search stores" placeholder="Search name, store ID, login" bind:value={q} class="w-[280px] max-w-full" />
  </div>
  {#if loading}
    <div class="px-[18px] pb-5"><Skeleton lines={4} /></div>
  {:else if rows.length === 0}
    <EmptyState icon="store" title="No stores yet" body="Create the first store — the wizard walks through login, hours, printers and menu.">
      {#snippet action()}<Button variant="primary" icon="plus" href="{base}/admin/tenants/new">New store</Button>{/snippet}
    </EmptyState>
  {:else}
    <!-- phones: one card per store -->
    <ul class="sm:hidden m-0 p-0 list-none border-t border-border">
      {#each shown as r (r.tenant.id)}
        <li class="border-b border-border last:border-b-0">
          <a class="srow" href={href(r)}>
            <span class="flex-1 min-w-0 flex flex-col gap-1">
              <span class="flex items-center gap-2 min-w-0">
                <span class="font-semibold text-text truncate">{r.tenant.name}</span>
                <Badge tone={PI_BADGE[r.pi].tone} dot>{PI_BADGE[r.pi].label}</Badge>
              </span>
              <span class="text-xs text-muted truncate">
                <span class="mono">{r.tenant.store_id ?? 'no store id'}</span> · {r.tenant.has_login ? r.tenant.username : 'no login'} · {r.ordersToday} order{r.ordersToday === 1 ? '' : 's'} today
              </span>
              <span class="text-xs {r.printersDown ? 'text-bad' : 'text-muted'} truncate">{seen(r)}{printers(r) !== '—' ? ` · printers ${printers(r)}` : ''}</span>
            </span>
            <span class="text-muted shrink-0"><Icon name="chevron-right" size={18} /></span>
          </a>
        </li>
      {/each}
    </ul>
    {#if shown.length === 0}
      <p class="sm:hidden m-0 px-[18px] py-6 text-muted">No store matches “{q}”.</p>
    {/if}
    <div class="hidden sm:block overflow-x-auto">
      <table class="tbl min-w-[760px]">
        <thead>
          <tr>
            <th>Store</th><th>Store ID</th><th>Pi</th><th>Printers</th><th>Version</th><th>Manager login</th><th>Orders today</th><th><span class="sr-only">Open</span></th>
          </tr>
        </thead>
        <tbody>
          {#each shown as r (r.tenant.id)}
            <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_noninteractive_element_interactions -->
            <tr class="clickable" onclick={() => goto(href(r))}>
              <td>
                <a class="rowlink" href={href(r)} onclick={(e) => e.stopPropagation()}>{r.tenant.name}</a>
                <div class="text-xs text-muted">{r.location?.name ?? 'no location'}</div>
              </td>
              <td class="mono">{r.tenant.store_id ?? '—'}</td>
              <td>
                <Badge tone={PI_BADGE[r.pi].tone} dot>{PI_BADGE[r.pi].label}</Badge>
                <div class="text-xs text-muted mt-0.5">{seen(r)}</div>
              </td>
              <td class="mono {r.printersDown ? 'text-bad' : ''}">{printers(r)}</td>
              <td class="mono text-text2">{r.version || '—'}</td>
              <td class="mono text-text2">{r.tenant.has_login ? r.tenant.username : '—'}</td>
              <td class="mono">{r.ordersToday}</td>
              <td class="text-right">
                <a class="btn btn-sm" href={href(r)} aria-label="Open {r.tenant.name}" onclick={(e) => e.stopPropagation()}>
                  Open<Icon name="chevron-right" size={14} />
                </a>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if shown.length === 0}
        <p class="m-0 px-[18px] py-6 text-muted">No store matches “{q}”.</p>
      {/if}
    </div>
  {/if}
</section>

<style>
  .srow {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 18px;
    min-height: 64px;
    color: var(--text);
    text-decoration: none;
  }
  .srow:hover {
    background: var(--hover);
    color: var(--text);
  }
</style>
