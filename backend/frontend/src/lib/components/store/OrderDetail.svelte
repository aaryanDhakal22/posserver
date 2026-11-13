<script lang="ts">
  import { dollars, errText, storeApi } from '$lib/api';
  import { copyText } from '$lib/clipboard';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';
  import {
    brandLabel,
    channelLabel,
    childLabel,
    customerName,
    externalRef,
    numberKind,
    orderNumber,
    phoneText,
    printJobLabel,
    printTicketText,
    stations,
    takenBy as takenByText,
    tenderLabel,
    timeline,
    titleCase,
    typeLabel,
    whenText,
    type OrderDetail,
    type OrderLine
  } from '$lib/store/orders';

  // Store › Orders' detail panel: one order, read only (the store's Pi and terminals decide; main keeps the
  // record). Opened from a row or a deep link (?order=<id>).
  let { id = $bindable<string | null>(null), onclose }: { id: string | null; onclose?: () => void } = $props();

  let open = $state(false);
  let d = $state<OrderDetail | null>(null);
  let err = $state('');
  let loadedFor = '';

  $effect(() => {
    const want = id;
    open = !!want;
    if (!want || want === loadedFor) return;
    loadedFor = want;
    d = null;
    err = '';
    storeApi<OrderDetail>(`/me/orders/${encodeURIComponent(want)}`)
      .then((r) => {
        if (loadedFor === want) d = r;
      })
      .catch((e) => {
        if (loadedFor === want) err = errText(e);
      });
  });

  function closed() {
    loadedFor = '';
    id = null;
    onclose?.();
  }

  const o = $derived(d?.order);
  const steps = $derived(d ? timeline(d) : []);
  const tickets = $derived(d ? stations(d) : []);
  const name = $derived(o ? customerName(o) : '');
  const title = $derived(o ? `${numberKind(o)} ${orderNumber(o)}${name ? ' · ' + name : ''}` : 'Order');
  const eyebrow = $derived(o ? [channelLabel(o), typeLabel(o.fulfillment?.type), whenText(o.placed_at)].filter(Boolean).join(' · ') : '');
  const takenBy = $derived.by(() => {
    if (!d || !o) return '';
    const meta = o.source_metadata ?? {};
    if (o.source === 'counter') return takenByText(o, d.staff?.name) || (meta.device_kind === 'callcenter' ? 'Call center' : 'A cashier');
    if (d.channel?.operator) return `${d.channel.operator} (call center)`;
    if (d.channel) return d.channel.kind === 'web' ? `The website (${d.channel.name})` : d.channel.name;
    if (o.source === 'online') return 'The online ordering vendor (Brygid)';
    return channelLabel(o);
  });
  const prints = $derived(d?.prints ?? []);
  const tipTotal = $derived((d?.payments ?? []).reduce((n, p) => n + (p.amount_cents >= 0 ? p.tip_cents : 0), 0));
  const paid = $derived((d?.payments ?? []).filter((p) => p.status === 'captured' || p.status === 'refunded').reduce((n, p) => n + p.amount_cents, 0));
  const refundOwed = $derived(Number(o?.source_metadata?.refund_owed_cents ?? 0));
  const address = $derived.by(() => {
    const a = o?.fulfillment?.address;
    if (!a) return '';
    return [a.line1, a.line2, [a.city, [a.state, a.postal_code].filter(Boolean).join(' ')].filter(Boolean).join(', ')].filter(Boolean).join(', ');
  });

  const FLAG: Record<string, string> = {
    refund_owed: 'Owes the customer a refund',
    overpaid: 'Paid more than the total',
    refund_needs_manager: 'Refund without a manager',
    over_staff_limit: 'Over the person’s limit',
    money_mismatch: 'Totals don’t add up',
    lines_subtotal_mismatch: 'Lines don’t add up to the subtotal',
    order_cancelled: 'Paid after it was cancelled',
    zero_amount: 'A $0 payment',
    payment_conflict: 'Payment id used twice',
    changed_after_z: 'Changed after the day closed',
    void_needs_manager: 'Void without a manager',
    refund_exceeds_paid: 'Refunded more than was paid',
    refund_exceeds_tender: 'Cash back over the order’s cash'
  };

  function lineMoney(l: OrderLine): string {
    return l.line_total_cents ? dollars(l.line_total_cents) : l.unit_price_cents ? dollars(l.unit_price_cents * l.quantity) : '$0.00';
  }
  /** A timeline time: the clock only on the order's own day, else with the day. */
  function stepTime(t: string): string {
    if (o && new Date(t).toDateString() === new Date(o.placed_at).toDateString())
      return new Date(t).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
    return whenText(t);
  }
  function link() {
    copyText(`${location.origin}${location.pathname}?order=${o?.id ?? id}`, 'Link copied');
  }
</script>

<Drawer bind:open {title} {eyebrow} width={560} onclose={closed}>
  {#if err}
    <p class="text-bad">{err}</p>
  {:else if !d || !o}
    <Skeleton lines={10} />
  {:else}
    <div class="flex flex-col gap-5 text-[14px]" data-testid="order-detail">
      <div class="flex flex-wrap items-center gap-2">
        <StatusBadge order={o} />
        {#if externalRef(o)}<Badge tone="mute" title="The vendor's order number">Vendor {externalRef(o)}</Badge>{/if}
        {#if o.fulfillment?.promised_at}<Badge tone="info">For {whenText(o.fulfillment.promised_at)}</Badge>{/if}
        {#if o.estimated_ready_at && o.status !== 'cancelled'}<span class="text-text2 text-[13px]">Ready by {whenText(o.estimated_ready_at)}</span>{/if}
      </div>

      <section class="grid grid-cols-[120px_1fr] gap-x-3 gap-y-1.5">
        <span class="text-muted">Customer</span><span>{name || '—'}</span>
        {#if o.customer?.phone}<span class="text-muted">Phone</span><a class="mono" href="tel:{o.customer.phone}">{phoneText(o.customer.phone)}</a>{/if}
        {#if address}
          <span class="text-muted">Address</span>
          <span>{address}{#if o.fulfillment.address?.notes}<span class="block text-text2 text-[13px]">{o.fulfillment.address.notes}</span>{/if}</span>
        {/if}
        <span class="text-muted">Taken by</span><span>{takenBy}</span>
        {#if o.driver_name}<span class="text-muted">Driver</span><span>{titleCase(o.driver_name)}</span>{/if}
        {#if o.kitchen_order_number != null}<span class="text-muted">{numberKind(o)} #</span><span class="mono">{o.kitchen_order_number}</span>{/if}
        {#if d.batch_id != null}<span class="text-muted">Day (batch)</span><span class="mono">{d.batch_id}</span>{/if}
        {#if o.notes}<span class="text-muted">Order note</span><span>{o.notes}</span>{/if}
      </section>

      <section>
        <h3 class="sec">Items</h3>
        <ul class="list-none m-0 p-0 flex flex-col">
          {#each o.items ?? [] as l (l.id)}
            <li class="py-2 border-b border-border last:border-0">
              <div class="flex gap-3">
                <span class="mono w-7 shrink-0 text-text2">{l.quantity}×</span>
                <span class="flex-1 min-w-0">
                  <span class="font-semibold">{l.name}</span>{#if l.size}<span class="text-text2">{` · ${l.size}`}</span>{/if}
                  {#if l.coupon}{" "}<Badge tone="brand" class="ml-1">{l.coupon}</Badge>{/if}
                  {#each l.children ?? [] as c (c.id)}
                    <span class="block text-[13px] text-text2">{childLabel(c)}{#if c.line_total_cents}<span class="mono">{` +${dollars(c.line_total_cents)}`}</span>{/if}</span>
                  {/each}
                  {#if l.notes}<span class="block text-[13px] italic text-text2">“{l.notes}”</span>{/if}
                </span>
                <span class="mono text-right">{lineMoney(l)}</span>
              </div>
            </li>
          {/each}
        </ul>
      </section>

      <section class="tot bg-elev">
        <span>Subtotal</span><span class="mono">{dollars(o.money.subtotal_cents)}</span>
        {#each o.money.discounts ?? [] as x, i (i)}
          <span>{x.label || x.code || 'Discount'}</span><span class="mono">−{dollars(x.amount_cents)}</span>
        {/each}
        {#each o.money.fees ?? [] as f, i (i)}
          <span>{f.label || typeLabel(f.type) + ' fee'}{#if f.driver_cents}<span class="text-muted text-[12px]">{` (${dollars(f.driver_cents)} to the driver)`}</span>{/if}</span><span class="mono">{dollars(f.amount_cents)}</span>
        {/each}
        <span>Tax</span><span class="mono">{dollars(o.money.tax_cents)}</span>
        {#if o.money.tip_cents}<span>Tip</span><span class="mono">{dollars(o.money.tip_cents)}</span>{/if}
        <span class="font-semibold">Total</span><span class="mono font-semibold">{dollars(o.money.total_cents)}</span>
        {#if tipTotal}<span class="text-text2">Card tips (on top)</span><span class="mono text-text2">{dollars(tipTotal)}</span>{/if}
        {#if refundOwed > 0}<span class="text-warn">Refund owed</span><span class="mono text-warn">{dollars(refundOwed)}</span>{/if}
      </section>

      <section>
        <h3 class="sec">Payments</h3>
        {#if d.payments.length === 0 && !d.card_hold}
          <p class="text-text2 m-0">No payment recorded{o.status === 'cancelled' ? '' : ' — paid at the door, at pickup, or not yet'}.</p>
        {/if}
        {#each d.payments as p (p.id)}
          <div class="pay border-b border-border">
            <div class="flex-1 min-w-0">
              <div class="font-medium">
                {p.amount_cents < 0 ? 'Refund · ' : ''}{tenderLabel(p.method)}{#if brandLabel(p.card_brand)}<span data-testid="card-brand">{` · ${brandLabel(p.card_brand)}`}</span>{/if}{#if p.last4}<span class="mono text-text2">{` •••• ${p.last4}`}</span>{/if}
                {#if p.status !== 'captured' && p.status !== 'refunded'}<Badge tone="warn" class="ml-1">{p.status}</Badge>{/if}
              </div>
              <div class="text-[12.5px] text-text2">
                {[whenText(p.captured_at || p.received_at), p.staff_name, p.approved_by ? `approved by ${p.approved_by}` : '', p.reason, p.auth_code ? `auth ${p.auth_code}` : ''].filter(Boolean).join(' · ')}
              </div>
              {#each p.flags as f (f)}<Badge tone="warn" class="mt-1 mr-1">{FLAG[f] ?? f.replaceAll('_', ' ')}</Badge>{/each}
            </div>
            <div class="text-right">
              <div class="mono {p.amount_cents < 0 ? 'text-bad' : ''}">{p.amount_cents < 0 ? '−' : ''}{dollars(Math.abs(p.amount_cents))}</div>
              {#if p.tip_cents}<div class="mono text-[12.5px] text-text2">+ {dollars(p.tip_cents)} tip</div>{/if}
            </div>
          </div>
        {/each}
        {#if d.card_hold}
          {@const h = d.card_hold}
          <div class="pay border-b border-border">
            <div class="flex-1 min-w-0">
              <div class="font-medium">Card on the website ({h.gateway === 'square' ? 'Square' : h.gateway}) · {h.status === 'held' ? 'held, not charged yet' : h.status}</div>
              <div class="text-[12.5px] text-text2">
                {h.status === 'held' ? `Charged at the driver's clock-out, Close batch, or by ${whenText(h.due_at)}` : h.captured_at ? `Charged ${whenText(h.captured_at)}` : h.cancelled_at ? `Released ${whenText(h.cancelled_at)}` : ''}
                {#if h.last_error} · <span class="text-warn">{h.last_error}</span>{/if}
              </div>
            </div>
            <div class="text-right">
              <div class="mono">{dollars(h.captured_cents || h.amount_cents)}</div>
              {#if h.tip_cents + h.added_tip_cents}<div class="mono text-[12.5px] text-text2">+ {dollars(h.tip_cents + h.added_tip_cents)} tip</div>{/if}
              {#if h.refunded_cents}<div class="mono text-[12.5px] text-bad">−{dollars(h.refunded_cents)} refunded</div>{/if}
            </div>
          </div>
        {/if}
        {#if d.payments.length}
          <div class="flex justify-between pt-2 text-text2 text-[13px]"><span>Paid in all</span><span class="mono">{dollars(paid)}</span></div>
        {/if}
      </section>

      <section>
        <h3 class="sec">Timeline</h3>
        <ol class="tl">
          {#each steps as s, i (i)}
            <li class={s.tone ?? ''}>
              <span class="mono text-[12.5px] text-text2 shrink-0 whitespace-nowrap min-w-[64px]">{stepTime(s.at)}</span>
              <span class="flex-1 min-w-0"><span class="font-medium">{s.label}</span>{#if s.detail}<span class="text-text2">{' · '}{s.detail}</span>{/if}</span>
            </li>
          {/each}
        </ol>
      </section>

      <section>
        <h3 class="sec">Kitchen tickets</h3>
        <ul class="list-none m-0 p-0 flex flex-col gap-1.5">
          {#each tickets as t (t.name)}
            <li><span class="font-medium">{t.name}</span><span class="text-text2">{` · ${t.lines.join(', ')}`}</span></li>
          {/each}
        </ul>
        {#if prints.length}
          <ul class="list-none m-0 mt-3 p-0 flex flex-col gap-1.5" data-testid="print-results">
            {#each prints as j (j.event_id)}
              <li class="text-[13px]">
                <span class="mono text-text2 mr-1">{stepTime(j.at)}</span><span class="font-medium">{printJobLabel(j.mode)}</span>
                {#each j.tickets as t, i (i)}
                  <span class="text-text2">{' · '}</span><span
                    class={t.result === 'failed' || t.result === 'skipped' ? 'text-bad' : t.result === 'rerouted' ? 'text-warn' : ''}
                    title={[`${t.attempts} ${t.attempts === 1 ? 'try' : 'tries'}`, t.error, t.at ? new Date(t.at).toLocaleTimeString() : ''].filter(Boolean).join(' · ')}
                    >{printTicketText(t)}</span>
                {/each}
              </li>
            {/each}
          </ul>
        {/if}
        <p class="text-[12.5px] text-muted mt-2 mb-0">
          {d.agent_status?.printed_date ? `The Pi printed the tickets ${whenText(d.agent_status.printed_date)}.` : o.status === 'received' ? 'Not printed yet: the kitchen hasn’t accepted it.' : ''}
          {prints.length ? 'What printed where, as the Pi reported it.' : 'Where each line prints comes from the menu; this Pi doesn’t report what printed (older Pi).'}
        </p>
      </section>

      {#if d.flags.length}
        <section>
          <h3 class="sec">Money notes</h3>
          {#each d.flags as f, i (i)}
            <div class="text-[13px]"><Badge tone={f.resolved_at ? 'mute' : 'warn'}>{FLAG[f.code] ?? f.code.replaceAll('_', ' ')}</Badge> <span class="text-text2">{whenText(f.at)}</span></div>
          {/each}
        </section>
      {/if}
    </div>
  {/if}
  {#snippet footer()}
    <Button icon="copy" onclick={link}>Copy link</Button>
    <span class="text-[12.5px] text-muted self-center flex-1 min-w-[200px]">Reprint a receipt on the store's terminal or tablet.</span>
  {/snippet}
</Drawer>

<style>
  .sec {
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    font-weight: 700;
    color: var(--muted);
    margin: 0 0 6px;
  }
  .tot {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 4px 12px;
    padding: 10px 12px;
    border-radius: 12px;
  }
  .tot :global(.mono) {
    text-align: right;
  }
  .pay {
    display: flex;
    gap: 12px;
    padding: 8px 0;
  }
  .tl {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .tl li {
    display: flex;
    gap: 10px;
    padding-left: 12px;
    border-left: 2px solid var(--border);
  }
  .tl li.ok {
    border-left-color: var(--ok);
  }
  .tl li.bad {
    border-left-color: var(--bad);
  }
  .tl li.mute {
    opacity: 0.85;
  }
</style>
