<script lang="ts">
  import { onMount } from 'svelte';
  import { CHANNEL_KIND_LABELS, errText, storeApi, type PricingBody } from '$lib/api';
  import { base } from '$app/paths';
  import { KINDS, bandsRange, feeTotal, feesForm, feesOf, kindsForm, tipsForm, tipsOf, type FeesForm, type KindForm, type TipsForm } from '$lib/store/pricingForm';
  import { toast } from '$lib/toast.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';
  import Section from '$lib/components/settings/Section.svelte';

  // Store › Settings › Fees and tips: the store's delivery fee (store share + driver share), online fee,
  // delivery minimum and tips, and a channel's own fees / tips when it differs (each channel folded shut
  // until opened). GET/PUT /me/pricing: tax, deals and half-and-half go back as they were loaded.
  type Form = { fees: FeesForm; tips: TipsForm; kinds: Record<string, KindForm> };
  let body = $state<PricingBody | null>(null);
  let form = $state<Form | null>(null);
  let savedForm = $state('');
  let saving = $state(false);
  let err = $state('');
  const dirty = $derived(!!form && JSON.stringify(form) !== savedForm);

  function fill(b: PricingBody) {
    body = b;
    form = { fees: feesForm(b.store.fees), tips: tipsForm(b.store.tips), kinds: kindsForm(b) };
    savedForm = JSON.stringify(form);
  }
  onMount(() => storeApi<PricingBody>('/me/pricing').then(fill).catch((e) => (err = errText(e))));

  async function save() {
    if (!body || !form || saving) return;
    err = '';
    let next: PricingBody;
    try {
      const kept = Object.fromEntries(Object.entries(body.kinds).filter(([k]) => !KINDS.includes(k)));
      next = { store: { ...body.store, fees: feesOf(form.fees, 'Store'), tips: tipsOf(form.tips) }, kinds: { ...kept } };
      for (const k of KINDS) {
        const f = form.kinds[k];
        if (!f.ownFees && !f.ownTips) continue;
        next.kinds[k] = {
          fees: f.ownFees ? feesOf(f.fees, CHANNEL_KIND_LABELS[k]) : null,
          tips: f.ownTips ? tipsOf(f.tips) : null,
          card_fee: body.kinds[k]?.card_fee ?? null
        };
      }
    } catch (x) {
      err = errText(x);
      return;
    }
    saving = true;
    try {
      await storeApi('/me/pricing', { method: 'PUT', body: JSON.stringify(next) });
      fill(await storeApi<PricingBody>('/me/pricing'));
      toast.success('Fees and tips saved — channels price with them right away');
    } catch (x) {
      err = errText(x);
    } finally {
      saving = false;
    }
  }
</script>

{#snippet feeFields(f: FeesForm, p: string)}
  <div class="fgrid">
    {#if f.bands?.length}
      <!-- A fee by distance: the customer's fee is edited on Map and delivery area; the driver's pay stays here. -->
      <div class="total"><span class="lbl">Delivery fee</span><span class="mono font-semibold text-[15px]" data-testid="{p}-del-total">{bandsRange(f.bands)} by distance</span>
        <a class="text-[13px]" href="{base}/store/settings/map#delivery_fee_bands">Change it on Map and delivery area</a></div>
      <div><label class="lbl" for="{p}-deld">Delivery: driver share ($)</label><input id="{p}-deld" class="field mono text-right" inputmode="decimal" bind:value={f.driverShare} /></div>
      <div></div>
    {:else}
      <div><label class="lbl" for="{p}-dels">Delivery: store share ($)</label><input id="{p}-dels" class="field mono text-right" inputmode="decimal" bind:value={f.storeShare} /></div>
      <div><label class="lbl" for="{p}-deld">Delivery: driver share ($)</label><input id="{p}-deld" class="field mono text-right" inputmode="decimal" bind:value={f.driverShare} /></div>
      <div class="total"><span class="lbl">Delivery fee total</span><span class="mono font-semibold text-[15px]" data-testid="{p}-del-total">{feeTotal(f)}</span></div>
    {/if}
    <div><label class="lbl" for="{p}-free">Free delivery over ($)</label><input id="{p}-free" class="field mono text-right" inputmode="decimal" placeholder="never" bind:value={f.freeOver} /></div>
    <div><label class="lbl" for="{p}-min">Delivery minimum ($)</label><input id="{p}-min" class="field mono text-right" inputmode="decimal" bind:value={f.min} /></div>
    <div></div>
    <div><label class="lbl" for="{p}-svc">Online fee ($)</label><input id="{p}-svc" class="field mono text-right" inputmode="decimal" bind:value={f.service} /></div>
    <div><label class="lbl" for="{p}-svcp">+ % of food</label><input id="{p}-svcp" class="field mono text-right" inputmode="decimal" bind:value={f.servicePct} /></div>
  </div>
{/snippet}
{#snippet tipFields(t: TipsForm, p: string)}
  <div class="flex flex-wrap items-end gap-4">
    <label class="sw-row pb-2"><Switch bind:checked={t.enabled} label="Ask for a tip" />Ask for a tip</label>
    {#if t.enabled}
      <div><label class="lbl" for="{p}-pre">Choices (%)</label><input id="{p}-pre" class="field mono !w-40" bind:value={t.presets} /></div>
      <div><label class="lbl" for="{p}-def">Pre-picked (%)</label><input id="{p}-def" class="field mono !w-24" inputmode="numeric" bind:value={t.def} /></div>
    {/if}
  </div>
{/snippet}

<SettingsPage
  title="Fees and tips"
  subtitle="What customers pay on top of the food, for the website, call center and counter."
  loading={!form && !err}
  {dirty}
  {saving}
  error={err}
  onsave={save}
  onundo={() => form && (form = JSON.parse(savedForm))}
>
  {#if form}
    <Section title="Fees" description="The customer pays the delivery fee total. The driver share is the driver’s pay for each delivery: paid weekly, or taken off the cash he hands in at close when Store › Settings › End of day says so.">
      {@render feeFields(form.fees, 'pr-st')}
    </Section>
    <Section title="Tips" description="Asked for on the website and the call center (the counter’s card reader has its own, in Card payments).">
      {@render tipFields(form.tips, 'pr-st')}
    </Section>
    <Section title="A channel that differs" description="Every channel uses the store’s fees and tips unless you give it its own.">
      <div class="flex flex-col gap-2.5">
        {#each KINDS as k (k)}
          {@const f = form.kinds[k]}
          <details class="kind" open={f.ownFees || f.ownTips}>
            <summary>
              <span class="font-semibold">{CHANNEL_KIND_LABELS[k]}</span>
              <span class="text-muted text-[13px]">{f.ownFees || f.ownTips ? [f.ownFees && 'own fees', f.ownTips && 'own tips'].filter(Boolean).join(' · ') : 'same as the store'}</span>
              <Icon name="chevron-down" size={16} class="chev ml-auto" />
            </summary>
            <div class="flex flex-col gap-4 pt-4">
              <label class="sw-row"><Switch bind:checked={f.ownFees} label="{CHANNEL_KIND_LABELS[k]} has its own fees" />Its own fees</label>
              {#if f.ownFees}{@render feeFields(f.fees, `pr-${k}`)}{/if}
              <label class="sw-row"><Switch bind:checked={f.ownTips} label="{CHANNEL_KIND_LABELS[k]} has its own tips" />Its own tips</label>
              {#if f.ownTips}{@render tipFields(f.tips, `pr-${k}t`)}{/if}
            </div>
          </details>
        {/each}
      </div>
    </Section>
  {/if}
</SettingsPage>

<style>
  .fgrid {
    display: grid;
    gap: 16px;
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
  @media (max-width: 620px) {
    .fgrid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .fgrid > div:empty {
      display: none;
    }
  }
  .total {
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    padding-bottom: 8px;
  }
  .sw-row {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    font-size: 14px;
    cursor: pointer;
    width: fit-content;
  }
  .kind {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 12px 14px;
    background: var(--elev);
  }
  .kind summary {
    display: flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    list-style: none;
    min-height: 28px;
  }
  .kind summary::-webkit-details-marker {
    display: none;
  }
  .kind[open] :global(.chev) {
    transform: rotate(180deg);
  }
</style>
