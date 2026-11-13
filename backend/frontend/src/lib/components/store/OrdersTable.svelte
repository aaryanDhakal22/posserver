<script lang="ts">
  import { base } from '$app/paths';
  import { dollars, type StoreOrder } from '$lib/api';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';
  import { channelLabel, customerName, externalRef, numberKind, orderNumber, titleCase, typeLabel, whenText } from '$lib/store/orders';

  // The orders table (read only: the store's Pi decides status, numbers and drivers; main mirrors them).
  // A row opens the order's detail panel: `onopen` on the Orders page, else a link to it (?order=<id>).
  let {
    orders,
    empty = 'No orders yet.',
    onopen
  }: { orders: (StoreOrder & { source?: string; placed_at?: string })[]; empty?: string; onopen?: (id: string) => void } = $props();
  // Tips ride on the payments (a card pre-tip, a card tip added afterwards), never in the total.
  const tipsOf = (o: StoreOrder) => (o.payments ?? []).reduce((n, p) => n + (p.tip_cents ?? 0), 0);
  const href = (id: string) => `${base}/store/orders?order=${id}`;
  function open(e: MouseEvent, id: string) {
    if (!onopen || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    onopen(id);
  }
</script>

<div class="overflow-x-auto">
  <table class="tbl min-w-[820px]">
    <thead>
      <tr>
        <th>Placed</th><th>No.</th><th>Customer</th><th>Type</th><th>Status</th><th>Driver</th><th class="!text-right">Total</th>
      </tr>
    </thead>
    <tbody>
      {#each orders as o (o.id)}
        <tr class="row" data-order={o.id}>
          <td class="whitespace-nowrap text-text2">
            <a class="cover" href={href(o.id)} onclick={(e) => open(e, o.id)} aria-label="Open {numberKind(o)} {orderNumber(o)}">{whenText(o.placed_at ?? o.received_at)}</a>
          </td>
          <td class="whitespace-nowrap">
            <span class="mono font-bold">{orderNumber(o)}</span>
            <span class="block text-[11.5px] text-muted">{numberKind(o) === 'Ticket' ? 'Ticket' : numberKind(o) === 'Kitchen' ? 'Kitchen' : 'Not accepted'}{externalRef(o) ? ' · ' + externalRef(o) : ''}</span>
          </td>
          <td>{customerName(o)}</td>
          <td class="whitespace-nowrap">{typeLabel(o.fulfillment?.type)}<span class="block text-[11.5px] text-muted">{channelLabel(o)}</span></td>
          <td><StatusBadge order={o} /></td>
          <td>{o.driver_name ? titleCase(o.driver_name) : ''}</td>
          <td class="mono text-right">
            {dollars(o.money?.total_cents)}
            {#if tipsOf(o) > 0}<div class="text-xs text-text2">+ {dollars(tipsOf(o))} tip</div>{/if}
            {#if (o.source_metadata?.refund_owed_cents ?? 0) > 0}<div class="text-xs text-warn" title="Changed after it was paid online: refund the customer in the vendor's site / Square">{dollars(o.source_metadata?.refund_owed_cents ?? 0)} refund owed</div>{/if}
          </td>
        </tr>
      {:else}
        <tr><td colspan="7" class="text-muted text-center !py-8">{empty}</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  /* The whole row is the link (the first cell's link covers it), so a click anywhere opens the order. */
  .row {
    position: relative;
    cursor: pointer;
  }
  .row:hover {
    background: var(--hover);
  }
  .cover {
    color: inherit;
    text-decoration: none;
  }
  .cover::after {
    content: '';
    position: absolute;
    inset: 0;
  }
  .cover:focus-visible {
    outline: none;
  }
  .row:has(.cover:focus-visible) {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
</style>
