<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, toCents, centsInput, CHANNEL_KIND_LABELS, type PricingBody, type PricingFees, type PricingTips } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';

  // Tax, fees, tips and how deals combine. The store's fees and tips apply
  // to every channel unless a channel has its own (e.g. the website charges
  // an online fee and takes tips; the counter doesn't).
  // Delivery fee = the store's share + the driver's share (TERMINAL-PLAN "Delivery fee");
  // the customer pays the total (delivery_fee_cents), the split feeds driver reports.
  // main's default driver share, capped at the fee (before the $state initializers below call feesForm).
  const DEFAULT_DRIVER_CENTS = 300;
  type FeesForm = { service: string; servicePct: string; storeShare: string; driverShare: string; freeOver: string; min: string };
  type TipsForm = { enabled: boolean; presets: string; def: string };
  type KindForm = { ownFees: boolean; fees: FeesForm; ownTips: boolean; tips: TipsForm };

  const KINDS = ['web', 'operator', 'counter'];
  let loaded = $state(false);
  let saving = $state(false);
  let tax = $state('6');
  let stacking = $state<'best' | 'stack' | 'single'>('best');
  let fees = $state<FeesForm>(feesForm(null));
  let tips = $state<TipsForm>(tipsForm(null));
  let kinds = $state<Record<string, KindForm>>({});
  let cardFee = $state({ mode: 'none', bps: 0 });
  let halfTopping = $state<'full' | 'half'>('full');
  let halfOff = $state('0');
  let kept: PricingBody['kinds'] = {}; // kinds this card doesn't edit (vendor) stay as they are

  function feesForm(f: PricingFees | null): FeesForm {
    const total = f?.delivery_fee_cents ?? 0;
    const driver = Math.min(f?.delivery_driver_cents ?? DEFAULT_DRIVER_CENTS, total);
    return {
      service: centsInput(f?.service_fee_cents ?? 0), servicePct: String((f?.service_fee_bps ?? 0) / 100),
      storeShare: centsInput(total - driver), driverShare: centsInput(driver),
      freeOver: f?.free_delivery_over_cents == null ? '' : centsInput(f.free_delivery_over_cents), min: centsInput(f?.delivery_min_cents ?? 0)
    };
  }
  function feeTotal(f: FeesForm): string {
    const a = f.storeShare.trim() ? toCents(f.storeShare) : 0;
    const b = f.driverShare.trim() ? toCents(f.driverShare) : 0;
    return a === null || b === null || a < 0 || b < 0 ? '—' : '$' + centsInput(a + b);
  }
  function tipsForm(t: PricingTips | null): TipsForm {
    return { enabled: !!t?.enabled, presets: (t?.presets ?? [15, 18, 20]).join(', '), def: String(t?.default_pct ?? 18) };
  }
  onMount(async () => {
    try {
      const b = await storeApi<PricingBody>('/me/pricing');
      tax = String(b.store.tax_bps / 100);
      stacking = b.store.stacking;
      fees = feesForm(b.store.fees);
      tips = tipsForm(b.store.tips);
      cardFee = b.store.card_fee;
      halfTopping = b.store.half_topping_price === 'half' ? 'half' : 'full';
      halfOff = String(b.store.half_specialty_off_pct ?? 0);
      for (const k of KINDS) {
        const o = b.kinds[k];
        kinds[k] = { ownFees: !!o?.fees, fees: feesForm(o?.fees ?? b.store.fees), ownTips: !!o?.tips, tips: tipsForm(o?.tips ?? b.store.tips) };
      }
      kept = Object.fromEntries(Object.entries(b.kinds).filter(([k]) => !KINDS.includes(k)));
      loaded = true;
    } catch (e) {
      toast.error(e);
    }
  });

  function feesOf(f: FeesForm, where: string): PricingFees {
    const c = (s: string, what: string) => {
      const v = s.trim() ? toCents(s) : 0;
      if (v === null || v < 0) throw new Error(`${where}: “${s}” isn't a valid ${what}.`);
      return v;
    };
    return {
      service_fee_cents: c(f.service, 'online fee'), service_fee_bps: Math.round((parseFloat(f.servicePct) || 0) * 100),
      delivery_fee_cents: c(f.storeShare, 'store share') + c(f.driverShare, 'driver share'),
      delivery_driver_cents: c(f.driverShare, 'driver share'), free_delivery_over_cents: f.freeOver.trim() ? c(f.freeOver, 'amount') : null,
      delivery_min_cents: c(f.min, 'minimum')
    };
  }
  function tipsOf(t: TipsForm): PricingTips {
    return {
      enabled: t.enabled,
      presets: t.presets.split(',').map((x) => parseInt(x.trim(), 10)).filter((x) => !isNaN(x)),
      default_pct: parseInt(t.def, 10) || 0
    };
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (saving) return;
    let body: PricingBody;
    try {
      body = {
        store: { tax_bps: Math.round((parseFloat(tax) || 0) * 100), stacking, fees: feesOf(fees, 'Store'), tips: tipsOf(tips), card_fee: cardFee,
          half_topping_price: halfTopping, half_specialty_off_pct: Math.round(parseFloat(halfOff) || 0) },
        kinds: { ...kept }
      };
      for (const k of KINDS) {
        const f = kinds[k];
        if (!f.ownFees && !f.ownTips) continue;
        body.kinds[k] = { fees: f.ownFees ? feesOf(f.fees, CHANNEL_KIND_LABELS[k]) : null, tips: f.ownTips ? tipsOf(f.tips) : null, card_fee: null };
      }
    } catch (x) {
      toast.error(x);
      return;
    }
    saving = true;
    try {
      await storeApi('/me/pricing', { method: 'PUT', body: JSON.stringify(body) });
      toast.success('Pricing saved — channels price with it right away');
    } catch (x) {
      toast.error(x);
    } finally {
      saving = false;
    }
  }
</script>

{#snippet feeFields(f: FeesForm, p: string)}
  <div class="grid gap-3 grid-cols-2 sm:grid-cols-3">
    <div><label class="lbl" for="{p}-svc">Online fee ($)</label><input id="{p}-svc" class="field mono text-right" inputmode="decimal" bind:value={f.service} /></div>
    <div><label class="lbl" for="{p}-svcp">+ % of food</label><input id="{p}-svcp" class="field mono text-right" inputmode="decimal" bind:value={f.servicePct} /></div>
    <div><label class="lbl" for="{p}-dels">Delivery: store share ($)</label><input id="{p}-dels" class="field mono text-right" inputmode="decimal" bind:value={f.storeShare} /></div>
    <div><label class="lbl" for="{p}-deld">Delivery: driver share ($)</label><input id="{p}-deld" class="field mono text-right" inputmode="decimal" bind:value={f.driverShare} /></div>
    <div class="flex flex-col justify-end pb-2"><span class="lbl">Delivery fee total</span><span class="mono font-semibold" data-testid="{p}-del-total">{feeTotal(f)}</span></div>
    <div><label class="lbl" for="{p}-free">Free delivery over ($)</label><input id="{p}-free" class="field mono text-right" inputmode="decimal" placeholder="never" bind:value={f.freeOver} /></div>
    <div><label class="lbl" for="{p}-min">Delivery minimum ($)</label><input id="{p}-min" class="field mono text-right" inputmode="decimal" bind:value={f.min} /></div>
  </div>
{/snippet}
{#snippet tipFields(t: TipsForm, p: string)}
  <div class="flex flex-wrap items-end gap-3">
    <label class="flex items-center gap-2.5 pb-2"><Switch bind:checked={t.enabled} label="Ask for a tip" />Ask for a tip</label>
    {#if t.enabled}
      <div><label class="lbl" for="{p}-pre">Choices (%)</label><input id="{p}-pre" class="field mono !w-36" bind:value={t.presets} /></div>
      <div><label class="lbl" for="{p}-def">Pre-picked (%)</label><input id="{p}-def" class="field mono !w-20" inputmode="numeric" bind:value={t.def} /></div>
    {/if}
  </div>
{/snippet}

<form onsubmit={save}>
  <Card title="Pricing" subtitle="Tax, fees, tips and how deals combine, for the website, call center and counter.">
    {#if !loaded}
      <Skeleton lines={4} />
    {:else}
      <div class="flex flex-col gap-4">
        <div class="flex flex-wrap gap-4 items-end">
          <div>
            <label class="lbl" for="pr-tax">Sales tax (%)</label>
            <input id="pr-tax" class="field mono text-right !w-24" inputmode="decimal" bind:value={tax} />
          </div>
          <div class="flex-1 min-w-[220px]">
            <label class="lbl" for="pr-stack">Deals in one order</label>
            <select id="pr-stack" class="field" bind:value={stacking}>
              <option value="best">Combos + the one best deal or code</option>
              <option value="stack">Everything that matches stacks</option>
              <option value="single">One deal per order</option>
            </select>
          </div>
        </div>
        <p class="help !mt-0">Items use their own tax rate when they have one (item panel), else this. Delivery fees aren't taxed; the online fee is. The customer pays the delivery fee total; the driver share shows on the driver reports.</p>
        <div class="flex flex-wrap gap-4 items-end">
          <div class="flex-1 min-w-[220px]">
            <label class="lbl" for="pr-half">A topping on half a pizza</label>
            <select id="pr-half" class="field" bind:value={halfTopping}>
              <option value="full">Full price</option>
              <option value="half">Half price (rounded up)</option>
            </select>
          </div>
          <div>
            <label class="lbl" for="pr-halfoff">2 specialties: cheaper half's extra, % off</label>
            <input id="pr-halfoff" class="field mono text-right !w-24" inputmode="numeric" bind:value={halfOff} />
          </div>
        </div>
        <p class="help !mt-0">Half and half: one specialty on a half costs that specialty's price; two cost the dearer one plus the cheaper one's extra over its plain pizza, at this % off.</p>
        <div class="subcard p-3.5 flex flex-col gap-3">
          <span class="lbl !mb-0">Store fees and tips</span>
          {@render feeFields(fees, 'pr-st')}
          {@render tipFields(tips, 'pr-st')}
        </div>
        {#each KINDS as k (k)}
          {#if kinds[k]}
            <div class="subcard p-3.5 flex flex-col gap-3">
              <span class="font-semibold">{CHANNEL_KIND_LABELS[k]}</span>
              <label class="flex items-center gap-2.5"><Switch bind:checked={kinds[k].ownFees} label="{CHANNEL_KIND_LABELS[k]} has its own fees" />Its own fees</label>
              {#if kinds[k].ownFees}{@render feeFields(kinds[k].fees, `pr-${k}`)}{/if}
              <label class="flex items-center gap-2.5"><Switch bind:checked={kinds[k].ownTips} label="{CHANNEL_KIND_LABELS[k]} has its own tips" />Its own tips</label>
              {#if kinds[k].ownTips}{@render tipFields(kinds[k].tips, `pr-${k}t`)}{/if}
            </div>
          {/if}
        {/each}
        <div><Button type="submit" variant="primary" loading={saving}>Save pricing</Button></div>
      </div>
    {/if}
  </Card>
</form>
