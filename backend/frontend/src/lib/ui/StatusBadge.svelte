<script lang="ts" module>
  import type { StoreOrder } from '$lib/api';
  import type { Tone } from './Badge.svelte';

  // Floor status as staff say it. "ready" on a pickup order means it's done.
  const LABEL: Record<string, string> = {
    received: 'Waiting',
    validated: 'Waiting',
    accepted: 'In kitchen',
    preparing: 'In kitchen',
    ready: 'Ready',
    on_delivery: 'Out for delivery',
    completed: 'Delivered',
    rejected: 'Rejected',
    cancelled: 'Cancelled'
  };
  const TONE: Record<string, Tone> = {
    received: 'warn',
    validated: 'warn',
    accepted: 'info',
    preparing: 'info',
    ready: 'ok',
    on_delivery: 'brand',
    completed: 'mute',
    rejected: 'bad',
    cancelled: 'bad'
  };

  export type FloorBucket = 'waiting' | 'kitchen' | 'ready' | 'out' | 'done';

  export function orderStatusLabel(o: Pick<StoreOrder, 'status' | 'fulfillment'>): string {
    if (o.status === 'ready' && o.fulfillment?.type !== 'delivery') return 'Done';
    return LABEL[o.status] ?? o.status;
  }
  export function orderBucket(o: Pick<StoreOrder, 'status' | 'fulfillment'>): FloorBucket {
    switch (o.status) {
      case 'received':
      case 'validated':
        return 'waiting';
      case 'accepted':
      case 'preparing':
        return 'kitchen';
      case 'ready':
        return o.fulfillment?.type === 'delivery' ? 'ready' : 'done';
      case 'on_delivery':
        return 'out';
      default:
        return 'done';
    }
  }
</script>

<script lang="ts">
  import Badge from './Badge.svelte';

  let { order }: { order: Pick<StoreOrder, 'status' | 'fulfillment'> } = $props();
  const tone = $derived(
    order.status === 'ready' && order.fulfillment?.type !== 'delivery' ? 'mute' : (TONE[order.status] ?? 'mute')
  );
</script>

<Badge {tone} dot>{orderStatusLabel(order)}</Badge>
