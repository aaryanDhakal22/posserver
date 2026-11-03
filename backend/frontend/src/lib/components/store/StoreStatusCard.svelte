<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { storeApi, storeTime, PAUSE_PRESETS, type StoreStatus } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';

  // Whether the store takes orders through its channels (website, call
  // center, counter) right now, and the pause. Brygid orders aren't paused.
  let status = $state<StoreStatus | null>(null);
  let pauseOpen = $state(false);
  let preset = $state('30m');
  let note = $state('');
  let busy = $state(false);

  async function load() {
    status = await storeApi<StoreStatus>('/me/status');
  }
  onMount(() => {
    load().catch((e) => toast.error(e));
    const t = setInterval(() => load().catch(() => {}), 30000);
    return () => clearInterval(t);
  });

  const tz = $derived(status?.store.timezone ?? '');
  const tone = $derived(status?.state === 'open' ? (status.accepting ? 'ok' : 'warn') : status?.state === 'paused' ? 'warn' : 'mute');
  const label = $derived(
    !status ? '' : status.state === 'paused' ? 'Paused' : status.state === 'closed' ? 'Closed' : status.accepting ? 'Taking orders' : 'Open — every order type is off'
  );
  const detail = $derived.by(() => {
    if (!status) return '';
    if (status.state === 'paused') {
      return status.pause?.until ? `Back at ${storeTime(status.pause.until, tz)}` : 'Until you resume';
    }
    if (status.state === 'closed') return status.next_open_at ? `Opens ${storeTime(status.next_open_at, tz)}` : 'No opening in the next two weeks';
    if (status.closes_at) return `Closes ${storeTime(status.closes_at, tz)}`;
    return status.hours_set ? '' : 'No opening hours set — always open';
  });
  const types = $derived(
    !status
      ? ''
      : [
          status.order_types.pickup.enabled ? `Pickup ${status.order_types.pickup.eta_minutes} min` : 'Pickup off',
          status.order_types.delivery.enabled ? `Delivery ${status.order_types.delivery.eta_minutes} min` : 'Delivery off'
        ].join(' · ')
  );

  async function pause(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    try {
      await storeApi('/me/pause', { method: 'POST', body: JSON.stringify({ preset, note }) });
      pauseOpen = false;
      await load();
      toast.success('Orders paused on the website, call center and counter');
    } catch (err) {
      toast.error(err);
    } finally {
      busy = false;
    }
  }
  async function resume() {
    busy = true;
    try {
      await storeApi('/me/pause', { method: 'DELETE' });
      await load();
      toast.success('Taking orders again');
    } catch (err) {
      toast.error(err);
    } finally {
      busy = false;
    }
  }
</script>

<Card title="Ordering" subtitle="Website, call center and counter. Brygid orders always come in.">
  {#snippet actions()}
    {#if status?.state === 'paused'}
      <Button size="sm" variant="primary" loading={busy} onclick={resume}>Resume orders</Button>
    {:else if status}
      <Button
        size="sm"
        onclick={() => {
          note = '';
          pauseOpen = true;
        }}>Pause orders</Button
      >
    {/if}
    <Button size="sm" variant="ghost" href="{base}/store/details">Settings</Button>
  {/snippet}
  {#if !status}
    <Skeleton lines={2} />
  {:else}
    <div class="flex flex-col gap-2.5">
      <div class="flex flex-wrap items-center gap-2.5">
        <Badge {tone} dot>{label}</Badge>
        {#if detail}<span class="text-text2">{detail}</span>{/if}
      </div>
      {#if status.pause?.note}<p class="m-0 text-[13px] text-text2">Customers see: “{status.pause.note}”</p>{/if}
      {#if status.today.note}<p class="m-0 text-[13px] text-text2">Today: {status.today.note}</p>{/if}
      <p class="m-0 text-[13px] text-muted">{types}{status.scheduling.days_ahead ? ` · orders up to ${status.scheduling.days_ahead} day${status.scheduling.days_ahead === 1 ? '' : 's'} ahead` : ' · ASAP only'}</p>
      {#if status.sold_out.length}
        <div class="flex flex-wrap items-center gap-1.5">
          <span class="text-[13px] font-medium text-warn">86'd:</span>
          {#each status.sold_out as s (s.item_id)}
            <Badge tone="warn" title={s.until ? `back ${storeTime(s.until, tz)}` : 'back when the new day starts'}>{s.name}</Badge>
          {/each}
          <Button size="xs" variant="ghost" href="{base}/store/menu">Menu</Button>
        </div>
      {/if}
    </div>
  {/if}
</Card>

<Dialog bind:open={pauseOpen} title="Pause orders" description="The website, call center and counter stop taking ASAP orders. Brygid orders still come in.">
  <form id="pause-form" class="flex flex-col gap-4" onsubmit={pause}>
    <SegmentedControl options={PAUSE_PRESETS} bind:value={preset} label="How long" />
    <div>
      <label class="lbl" for="pause-note">Message for customers (optional)</label>
      <input id="pause-note" class="field" maxlength="140" bind:value={note} placeholder="Kitchen is backed up — back soon" />
      <p class="help">Shown with the time orders open again.</p>
    </div>
  </form>
  {#snippet footer()}
    <Button onclick={() => (pauseOpen = false)}>Cancel</Button>
    <Button type="submit" form="pause-form" variant="primary" loading={busy}>Pause orders</Button>
  {/snippet}
</Dialog>
