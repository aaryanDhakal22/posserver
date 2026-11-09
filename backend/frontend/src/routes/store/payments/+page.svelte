<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { storeApi, dollars, plural, type SquareStatus, type CardHoldRow, type HoldStatus } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import { confirmDialog } from '$lib/confirm.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge, { type Tone } from '$lib/ui/Badge.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import TableWrap from '$lib/ui/TableWrap.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';

  // Payments: the store's Square connection (online card orders) and the
  // card holds — a card order is authorized at checkout and charged at the
  // driver's clock-out, at Close batch, or by main 24 h later. The manager
  // may charge a waiting one now; releasing a hold only happens by
  // cancelling the order on the Pi (owner, 2026-11-09).

  let sq = $state<SquareStatus | null>(null);
  let busy = $state('');
  let filter = $state<HoldStatus | 'all'>('held');
  let holds = $state<CardHoldRow[] | null>(null);

  const returned: Record<string, [string, 'success' | 'error']> = {
    connected: ['Square is connected', 'success'],
    denied: ['Square access was not given — nothing changed', 'error'],
    expired: ['The Square link expired — connect again', 'error'],
    error: ["Square couldn't be connected — try again", 'error']
  };

  onMount(async () => {
    const r = page.url.searchParams.get('square');
    if (r && returned[r]) {
      const [msg, kind] = returned[r];
      if (kind === 'success') toast.success(msg);
      else toast.error(msg);
      history.replaceState(history.state, '', page.url.pathname);
    }
    await Promise.all([loadSquare(), loadHolds()]);
  });

  async function loadSquare() {
    try {
      sq = await storeApi<SquareStatus>('/me/square');
    } catch (e) {
      toast.error(e);
    }
  }

  async function loadHolds() {
    try {
      const q = filter === 'all' ? '' : `?status=${filter}`;
      holds = (await storeApi<{ holds: CardHoldRow[] }>('/me/card-holds' + q)).holds;
    } catch (e) {
      toast.error(e);
    }
  }

  async function connect() {
    busy = 'connect';
    try {
      const { url } = await storeApi<{ url: string }>('/me/square/connect', { method: 'POST' });
      window.location.href = url;
    } catch (e) {
      toast.error(e);
      busy = '';
    }
  }

  async function pickLocation(id: string) {
    if (!id) return;
    busy = 'location';
    try {
      sq = await storeApi<SquareStatus>('/me/square/location', { method: 'PUT', body: JSON.stringify({ square_location_id: id }) });
      toast.success('Card payments go to that Square location now');
    } catch (e) {
      toast.error(e);
    } finally {
      busy = '';
    }
  }

  async function disconnect() {
    const ok = await confirmDialog({
      title: 'Disconnect Square?',
      body: 'Card orders stop until you connect Square again.',
      confirmLabel: 'Disconnect',
      cancelLabel: 'Keep',
      danger: true
    });
    if (!ok) return;
    busy = 'disconnect';
    try {
      await storeApi('/me/square', { method: 'DELETE' });
      toast.success('Square disconnected');
      await loadSquare();
    } catch (e) {
      toast.error(e);
    } finally {
      busy = '';
    }
  }

  async function chargeNow(h: CardHoldRow) {
    const total = h.amount_cents + h.tip_cents + h.added_tip_cents;
    const ok = await confirmDialog({
      title: `Charge ${dollars(total)} now?`,
      body: `${h.order_ref} · ${h.customer || 'customer'}. The card is charged right away, with any tip added so far.`,
      confirmLabel: 'Charge now'
    });
    if (!ok) return;
    busy = h.order_id;
    try {
      const r = await storeApi<CardHoldRow>(`/me/card-holds/${h.order_id}/capture`, { method: 'POST' });
      if (r.status === 'captured') toast.success(`Charged ${dollars(r.captured_cents)}`);
      else if (r.status === 'held') toast.error(r.last_error ? `Not charged yet: ${r.last_error} — main tries again` : 'Not charged yet — main tries again');
      else toast.error(r.last_error || "The card couldn't be charged");
      await Promise.all([loadHolds(), loadSquare()]);
    } catch (e) {
      toast.error(e);
    } finally {
      busy = '';
    }
  }

  const STATUS: Record<HoldStatus, [string, Tone]> = {
    held: ['Waiting', 'warn'],
    captured: ['Charged', 'ok'],
    cancelled: ['Released', 'mute'],
    failed: ['Problem', 'bad']
  };
  const REASON: Record<string, string> = {
    clock_out: "at the driver's clock-out",
    batch_close: 'at Close batch',
    manual: 'with Charge now'
  };

  const time = (s: string | null) =>
    s ? new Date(s).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : '';

  function detail(h: CardHoldRow): string {
    switch (h.status) {
      case 'held':
        if (h.capture_requested_at) return `Charging (${REASON[h.capture_reason] ?? h.capture_reason})${h.last_error ? ' — retrying: ' + h.last_error : ''}`;
        return `Charged by ${time(h.due_at)} at the latest`;
      case 'captured': {
        const tip = h.added_tip_cents && !h.tip_note.includes('NOT charged') ? ` (+${dollars(h.added_tip_cents)} tip after the order)` : '';
        const by = REASON[h.capture_reason] ?? 'after 24 hours';
        return `${dollars(h.captured_cents)}${tip} · ${by} · ${time(h.captured_at)}`;
      }
      case 'cancelled':
        return h.last_error.includes('refunded') ? 'Order cancelled — refunded' : h.last_error.includes('Square') ? 'Voided in Square' : 'Order cancelled — not charged';
      case 'failed':
        return h.last_error || 'Charge it in Square';
    }
  }

  const envNote = $derived(sq?.env === 'sandbox' ? 'Square sandbox: test cards only' : sq?.env === 'fake' ? 'Test mode (no real Square)' : '');
</script>

<svelte:head><title>Payments — quicc</title></svelte:head>

<PageHeader title="Payments" subtitle="Card orders from the website are held on the card at checkout and charged when they're done." />

<div class="flex flex-col gap-4 max-w-[900px]">
  <Card title="Square" subtitle="Online card payments go to your Square account.">
    {#if !sq}
      <Skeleton lines={3} />
    {:else if !sq.enabled}
      <p class="text-muted">Card payments aren't set up on this server yet.</p>
    {:else if !sq.state}
      <div class="flex flex-col gap-3 items-start">
        <p>Connect your Square account to take card payments on your website. You'll sign in to Square and allow quicc to take payments for you.</p>
        <Button variant="primary" icon="card" loading={busy === 'connect'} onclick={connect}>Connect Square</Button>
        {#if envNote}<Badge tone="info">{envNote}</Badge>{/if}
      </div>
    {:else}
      <div class="flex flex-col gap-4">
        <div class="flex flex-wrap items-center gap-2.5">
          {#if sq.connected}
            <Badge tone="ok" dot>Connected</Badge>
          {:else if sq.state === 'revoked'}
            <Badge tone="bad" dot>Access removed in Square</Badge>
          {:else}
            <Badge tone="bad" dot>Needs reconnecting</Badge>
          {/if}
          <span class="mono text-muted text-[13px]">{sq.merchant_id}</span>
          {#if envNote}<Badge tone="info">{envNote}</Badge>{/if}
        </div>
        {#if !sq.connected}
          <p class="text-text2">Card orders are off until Square is connected again.{#if sq.last_error} <span class="text-muted">({sq.last_error})</span>{/if}</p>
          <div><Button variant="primary" icon="card" loading={busy === 'connect'} onclick={connect}>Reconnect Square</Button></div>
        {:else}
          <div>
            <label class="lbl" for="pay-loc">Payments go to</label>
            <select
              id="pay-loc"
              class="field !w-auto min-w-[260px]"
              value={sq.square_location_id}
              disabled={busy === 'location'}
              onchange={(e) => pickLocation((e.currentTarget as HTMLSelectElement).value)}
            >
              {#if !sq.square_location_id}<option value="">Pick a Square location…</option>{/if}
              {#each sq.locations as l (l.id)}<option value={l.id}>{l.name}</option>{/each}
            </select>
            {#if !sq.square_location_id}
              <p class="field-error">Card orders are off until you pick where payments go.</p>
            {:else if sq.last_error}
              <p class="help">Square didn't answer just now ({sq.last_error}).</p>
            {/if}
          </div>
          <div class="flex flex-wrap items-center justify-between gap-3">
            <p class="text-text2 m-0">
              {sq.holds_open ? `${plural(sq.holds_open, 'card order')} waiting to be charged` : 'No card orders waiting to be charged'}
            </p>
            <Button variant="danger-ghost" loading={busy === 'disconnect'} disabled={sq.holds_open > 0} onclick={disconnect}
              >Disconnect</Button
            >
          </div>
          {#if sq.holds_open > 0}<p class="help !mt-0">You can disconnect once nothing is waiting (after Close batch).</p>{/if}
        {/if}
      </div>
    {/if}
  </Card>

  <Card title="Card orders" subtitle="Charged at the driver's clock-out, at Close batch, or 24 hours after the order.">
    {#snippet actions()}
      <SegmentedControl
        label="Show"
        bind:value={filter}
        onchange={() => {
          holds = null;
          loadHolds();
        }}
        options={[
          { value: 'held', label: 'Waiting' },
          { value: 'captured', label: 'Charged' },
          { value: 'cancelled', label: 'Released' },
          { value: 'failed', label: 'Problems' },
          { value: 'all', label: 'All' }
        ]}
      />
      <Button variant="ghost" icon="refresh" aria-label="Refresh" onclick={() => loadHolds()} />
    {/snippet}
    {#if !holds}
      <div class="px-5 pb-5"><Skeleton lines={4} /></div>
    {:else if !holds.length}
      <EmptyState icon="card" title={filter === 'held' ? 'Nothing waiting' : 'No card orders here'} body="Website card orders show up here once they're placed." />
    {:else}
      <TableWrap minWidth={720}>
        <thead>
          <tr><th>Order</th><th>Customer</th><th class="text-right">Amount</th><th>Status</th><th></th></tr>
        </thead>
        <tbody>
          {#each holds as h (h.order_id)}
            <tr>
              <td>
                <div class="mono">{h.order_ref}</div>
                <div class="text-muted text-[12px] whitespace-nowrap">{h.type === 'delivery' ? 'Delivery' : 'Pickup'} · {time(h.placed_at)}</div>
              </td>
              <td>{h.customer || '—'}</td>
              <td class="text-right mono">
                {dollars(h.amount_cents + h.tip_cents)}
                {#if h.added_tip_cents}<div class="text-muted text-[12px]">+{dollars(h.added_tip_cents)} tip</div>{/if}
              </td>
              <td>
                <Badge tone={STATUS[h.status][1]}>{STATUS[h.status][0]}</Badge>
                <div class="text-muted text-[12px] mt-1">{detail(h)}</div>
              </td>
              <td class="text-right">
                {#if h.status === 'held'}
                  <Button size="sm" loading={busy === h.order_id} disabled={!!busy && busy !== h.order_id} onclick={() => chargeNow(h)}>Charge now</Button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </TableWrap>
    {/if}
  </Card>
</div>
