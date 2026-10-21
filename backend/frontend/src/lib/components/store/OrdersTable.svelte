<script lang="ts">
  import { dollars, hhmm, type StoreOrder } from '$lib/api';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';

  // The floor table (read-only — the store's tablet decides status, kitchen #
  // and drivers; the agent syncs them here).
  let { orders, empty = 'No orders yet.' }: { orders: StoreOrder[]; empty?: string } = $props();
  const typeLabel = (t: string) => (t ? t[0].toUpperCase() + t.slice(1).replaceAll('_', ' ') : '—');
</script>

<div class="overflow-x-auto">
  <table class="tbl min-w-[760px]">
    <thead>
      <tr>
        <th>Kitchen #</th><th>Order</th><th>Customer</th><th>Type</th><th>Status</th><th>Ready by</th><th>Driver</th><th class="!text-right">Total</th>
      </tr>
    </thead>
    <tbody>
      {#each orders as o (o.id)}
        <tr>
          <td class="mono font-bold">{o.kitchen_order_number ?? '—'}</td>
          <td class="mono text-text2">#{o.source_event_id}</td>
          <td>{o.customer?.display_name || '—'}</td>
          <td>{typeLabel(o.fulfillment?.type)}</td>
          <td><StatusBadge order={o} /></td>
          <td class="mono">{hhmm(o.estimated_ready_at) || '—'}</td>
          <td>{o.driver_name || '—'}</td>
          <td class="mono text-right">{dollars(o.money?.total_cents)}</td>
        </tr>
      {:else}
        <tr><td colspan="8" class="text-muted text-center !py-8">{empty}</td></tr>
      {/each}
    </tbody>
  </table>
</div>
