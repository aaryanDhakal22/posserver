<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { dollars, errText, storeApi } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { tenderLabel, whenText } from '$lib/store/orders';

  // Store › Reports: the day closes (the Pi's Z report, one per Close batch), the cash drawers and the money
  // notes main keeps (contract/main.md §2.5zc–§2.5zd). Read only: the Pi builds every number; main checks
  // the Z's tenders against the payments it has (the "main's check" column).
  type ZRow = {
    id: string; batch_id: number; business_date: string; opened_at: string; closed_at: string; close_kind: string;
    total_cents: number; orders: number; over_short_cents: number | null; difference_cents: number; received_at: string;
  };
  type Drawer = {
    id: string; device_name: string; staff_name: string; batch_id: number | null; opened_at: string; start_cents: number;
    closed_at: string | null; close_reason: string; counted_cents: number | null; expected_cents: number | null;
    over_short_cents: number | null; flagged: boolean; cash_sales_cents: number; pay_ins_cents: number; pay_outs_cents: number; no_sales: number;
  };
  type Flag = { id: number; at: string; event_type: string; subject: string; code: string; detail: Record<string, unknown>; resolved_at: string | null };
  type ZFull = {
    report: Record<string, any>; received_at: string; version: number; flagged: boolean;
    main_check?: { tender: string; pi_cents: number; main_cents: number }[]; main_difference_cents?: number;
  };

  let day = $state('');
  let zs = $state<ZRow[]>([]);
  let drawers = $state<Drawer[]>([]);
  let flags = $state<Flag[]>([]);
  let loading = $state(true);
  let open = $state(false);
  let z = $state<ZFull | null>(null);
  let zErr = $state('');

  async function load() {
    loading = true;
    const q = day ? `?date=${day}` : '';
    try {
      const [a, b, c] = await Promise.all([
        storeApi<{ reports: ZRow[] }>(`/me/reports/z${q}`),
        storeApi<{ drawers: Drawer[] }>(`/me/drawers${q}`),
        storeApi<{ flags: Flag[] }>('/me/reports/flags')
      ]);
      zs = a.reports ?? [];
      drawers = b.drawers ?? [];
      flags = c.flags ?? [];
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
    }
  }
  onMount(load);

  async function openZ(id: string) {
    open = true;
    z = null;
    zErr = '';
    try {
      z = await storeApi<ZFull>(`/me/reports/z/${id}`);
    } catch (e) {
      zErr = errText(e);
    }
  }

  const FLAG: Record<string, string> = {
    refund_owed: 'Owes the customer a refund',
    overpaid: 'Paid more than the total',
    refund_exceeds_paid: 'Refunded more than was paid',
    refund_exceeds_tender: 'Cash back over the order’s cash',
    refund_needs_manager: 'Refund without a manager',
    over_staff_limit: 'Over the person’s limit',
    money_mismatch: 'Totals don’t add up',
    lines_subtotal_mismatch: 'Lines don’t add up to the subtotal',
    order_cancelled: 'Paid after it was cancelled',
    zero_amount: 'A $0 payment',
    payment_conflict: 'Payment id used twice',
    z_mismatch: 'The day close and main’s payments differ',
    z_unbalanced: 'The day close doesn’t balance',
    changed_after_z: 'Changed after the day closed',
    drawer_recount: 'A drawer was counted again',
    driver_share_over_fee: 'Driver share over the fee',
    void_needs_manager: 'Void without a manager'
  };
  const signed = (c: number | null | undefined) => (c == null ? '—' : (c < 0 ? '−' : c > 0 ? '+' : '') + dollars(Math.abs(c)));
  function subjectLink(s: string): string | null {
    const [kind, id] = s.split(':');
    return kind === 'order' && id ? `${base}/store/orders?order=${id}` : null;
  }
  const unresolved = $derived(flags.filter((f) => !f.resolved_at));
</script>

<svelte:head><title>Reports — quicc</title></svelte:head>

<PageHeader title="Reports" subtitle="Each day's close from the Pi, the cash drawers and anything about the money that needs a look.">
  {#snippet actions()}
    <label class="flex items-center gap-2 text-[13px] text-text2">
      Day
      <input type="date" class="field !w-auto" aria-label="Business day" bind:value={day} onchange={load} />
    </label>
    {#if day}<Button size="sm" onclick={() => { day = ''; load(); }}>Latest</Button>{/if}
  {/snippet}
</PageHeader>

<Card title="Day closes" subtitle={day ? `Closed for ${day}` : 'The newest 30. Each Close batch on the Pi sends one.'} padded={false}>
  {#if loading}
    <div class="px-5 pb-5"><Skeleton lines={3} /></div>
  {:else if zs.length === 0}
    <EmptyState icon="receipt" title="No day closes {day ? 'that day' : 'yet'}" body="The Pi sends its Z report when the day is closed (Close batch, or the automatic close)." />
  {:else}
    <div class="overflow-x-auto">
      <table class="tbl min-w-[720px]">
        <thead><tr><th>Day</th><th>Closed</th><th class="!text-right">Orders</th><th class="!text-right">Sales</th><th class="!text-right">Drawers over / short</th><th>Balance</th><th></th></tr></thead>
        <tbody>
          {#each zs as r (r.id)}
            <tr>
              <td>{r.business_date}</td>
              <td class="text-text2">{new Date(r.closed_at).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })}{r.close_kind === 'auto' ? ' (automatic)' : ''}</td>
              <td class="mono text-right">{r.orders}</td>
              <td class="mono text-right">{dollars(r.total_cents)}</td>
              <td class="mono text-right {r.over_short_cents && r.over_short_cents < 0 ? 'text-bad' : ''}">{signed(r.over_short_cents)}</td>
              <td>{#if r.difference_cents === 0}<Badge tone="ok">Balanced</Badge>{:else}<Badge tone="warn">Off by {dollars(Math.abs(r.difference_cents))}</Badge>{/if}</td>
              <td class="text-right"><Button size="sm" onclick={() => openZ(r.id)}>Open</Button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>

<Card title="Cash drawers" subtitle={day ? `Opened on ${day}` : 'The newest 50'} padded={false}>
  {#if loading}
    <div class="px-5 pb-5"><Skeleton lines={3} /></div>
  {:else if drawers.length === 0}
    <EmptyState icon="card" title="No drawers {day ? 'that day' : 'yet'}" body="A drawer shows here once a terminal opens one." />
  {:else}
    <div class="overflow-x-auto">
      <table class="tbl min-w-[760px]">
        <thead><tr><th>Terminal</th><th>Who</th><th>Opened</th><th>Closed</th><th class="!text-right">Start</th><th class="!text-right">Cash sales</th><th class="!text-right">Expected</th><th class="!text-right">Counted</th><th class="!text-right">Over / short</th></tr></thead>
        <tbody>
          {#each drawers as d (d.id)}
            <tr>
              <td>{d.device_name || '—'}</td>
              <td>{d.staff_name || '—'}</td>
              <td class="text-text2">{whenText(d.opened_at)}</td>
              <td class="text-text2">{d.closed_at ? whenText(d.closed_at) : 'Open'}{d.close_reason === 'batch_close' ? ' (at Close batch, not counted)' : ''}</td>
              <td class="mono text-right">{dollars(d.start_cents)}</td>
              <td class="mono text-right">{dollars(d.cash_sales_cents)}</td>
              <td class="mono text-right">{d.expected_cents == null ? '—' : dollars(d.expected_cents)}</td>
              <td class="mono text-right">{d.counted_cents == null ? '—' : dollars(d.counted_cents)}</td>
              <td class="mono text-right {d.over_short_cents && d.over_short_cents < 0 ? 'text-bad' : ''}">{signed(d.over_short_cents)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>

<Card title="Money notes" subtitle={unresolved.length ? `${unresolved.length} to look at. Main stored the money and noted what looked wrong.` : 'Nothing to look at. Main notes money that looks wrong here (it never refuses it).'} padded={false}>
  {#if loading}
    <div class="px-5 pb-5"><Skeleton lines={3} /></div>
  {:else if flags.length === 0}
    <EmptyState icon="check" title="No money notes" body="A refund without a manager, a day close that doesn't balance or an order paid twice would show here." />
  {:else}
    <div class="overflow-x-auto">
      <table class="tbl min-w-[640px]">
        <thead><tr><th>When</th><th>What</th><th>About</th><th></th></tr></thead>
        <tbody>
          {#each flags as f (f.id)}
            <tr>
              <td class="text-text2 whitespace-nowrap">{whenText(f.at)}</td>
              <td>{FLAG[f.code] ?? f.code.replaceAll('_', ' ')}{#if f.resolved_at}<Badge tone="mute" class="ml-1">resolved</Badge>{/if}</td>
              <td class="text-text2">{f.subject.split(':')[0]}{f.detail?.order_owed_cents ? ` · ${dollars(Number(f.detail.order_owed_cents))} owed` : ''}</td>
              <td class="text-right">{#if subjectLink(f.subject)}<Button size="sm" href={subjectLink(f.subject) ?? ''}>Open order</Button>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>

<p class="text-xs text-muted -mt-3">Orders per call-center agent are on <a class="text-accent" href="{base}/store/settings/callcenter">Settings › Call center</a>.</p>

<Drawer bind:open title={z ? `Day close · ${z.report.business_date ?? ''}` : 'Day close'} eyebrow={z ? `Batch ${z.report.batch_id} · closed ${whenText(z.report.closed_at)}` : ''} width={620}>
  {#if zErr}
    <p class="text-bad">{zErr}</p>
  {:else if !z}
    <Skeleton lines={10} />
  {:else}
    {@const r = z.report}
    <div class="flex flex-col gap-5 text-[14px]" data-testid="z-detail">
      <section class="kv">
        <span>Orders kept</span><span class="mono">{r.counts?.orders ?? 0}</span>
        <span>Counter · pickup · delivery</span><span class="mono">{r.counts?.counter ?? 0} · {r.counts?.pickup ?? 0} · {r.counts?.delivery ?? 0}</span>
        <span>Online (pickup · delivery)</span><span class="mono">{r.counts?.online ?? 0} ({r.counts?.online_pickup ?? 0} · {r.counts?.online_delivery ?? 0})</span>
        <span>Voided · cancelled</span><span class="mono">{r.counts?.voided ?? 0} · {r.counts?.cancelled ?? 0}</span>
      </section>
      <section class="kv">
        <span>Items</span><span class="mono">{dollars(r.sales?.items_cents ?? 0)}</span>
        <span>Discounts</span><span class="mono">−{dollars(r.sales?.discounts_cents ?? 0)}</span>
        <span>Fees</span><span class="mono">{dollars(r.sales?.fees_cents ?? 0)}</span>
        <span>Tax</span><span class="mono">{dollars(r.sales?.tax_cents ?? 0)}</span>
        <span class="font-semibold">Total sales</span><span class="mono font-semibold">{dollars(r.sales?.total_cents ?? 0)}</span>
      </section>
      <section>
        <h3 class="sec">Tenders</h3>
        <table class="tbl">
          <thead><tr><th>Tender</th><th class="!text-right">Count</th><th class="!text-right">Net</th><th class="!text-right">Main's check</th></tr></thead>
          <tbody>
            {#if !(r.tenders ?? []).length}<tr><td colspan="4" class="text-muted">No money taken that day.</td></tr>{/if}
            {#each r.tenders ?? [] as t (t.tender)}
              {@const m = z.main_check?.find((x) => x.tender === t.tender)}
              <tr>
                <td>{t.tender === 'online' ? 'Online (arrived paid)' : t.tender === 'driver_cash' ? 'Drivers’ cash' : tenderLabel(t.tender)}</td>
                <td class="mono text-right">{t.count}</td>
                <td class="mono text-right">{dollars(t.net_cents)}</td>
                <td class="mono text-right {m && m.main_cents !== m.pi_cents ? 'text-warn' : 'text-text2'}">{m ? dollars(m.main_cents) : '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </section>
      <section class="kv">
        <span>Card tips</span><span class="mono">{dollars(r.tips?.card_cents ?? 0)}</span>
        <span>Online tips</span><span class="mono">{dollars(r.tips?.online_cents ?? 0)}</span>
        <span>Owed to drivers</span><span class="mono">{dollars(r.tips?.drivers_owed_cents ?? 0)}</span>
        <span>Refunds</span><span class="mono">{r.refunds?.count ?? 0} · {dollars(r.refunds?.cents ?? 0)}</span>
        <span>Voids</span><span class="mono">{r.voids?.count ?? 0} · {dollars(r.voids?.cents ?? 0)}</span>
        <span>Unpaid at close</span><span class="mono">{r.unpaid?.count ?? 0} · {dollars(r.unpaid?.cents ?? 0)}</span>
        <span>No sales</span><span class="mono">{r.no_sales ?? 0}</span>
        <span class="font-semibold">Difference</span><span class="mono font-semibold {r.reconcile?.difference_cents ? 'text-warn' : ''}">{signed(r.reconcile?.difference_cents ?? 0)}</span>
      </section>
      {#if (r.drivers ?? []).length}
        <section>
          <h3 class="sec">Drivers</h3>
          <table class="tbl">
            <thead><tr><th>Driver</th><th class="!text-right">Deliveries</th><th class="!text-right">Cash</th><th class="!text-right">Tips</th><th class="!text-right">Owed</th></tr></thead>
            <tbody>
              {#each r.drivers as dr (dr.driver_id)}
                <tr><td>{dr.name}</td><td class="mono text-right">{dr.deliveries}</td><td class="mono text-right">{dollars(dr.cash_cents)}</td><td class="mono text-right">{dollars(dr.tip_cents)}</td><td class="mono text-right">{dollars(dr.owed_cents)}</td></tr>
              {/each}
            </tbody>
          </table>
        </section>
      {/if}
      <p class="text-[12.5px] text-muted m-0">Version {z.version} · received {whenText(z.received_at)}{z.flagged ? ' · main noted a difference with its payments' : ''}</p>
    </div>
  {/if}
</Drawer>

<style>
  .kv {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 4px 12px;
  }
  .kv > span:nth-child(odd) {
    color: var(--text2);
  }
  .kv > span:nth-child(even) {
    text-align: right;
  }
  .sec {
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    font-weight: 700;
    color: var(--muted);
    margin: 0 0 6px;
  }
</style>
