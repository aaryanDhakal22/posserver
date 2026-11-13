<script lang="ts">
  import { onMount } from 'svelte';
  import { errText, storeApi, type PricingBody } from '$lib/api';
  import { STACKING } from '$lib/store/pricingForm';
  import { toast } from '$lib/toast.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';
  import Section from '$lib/components/settings/Section.svelte';
  import UnitField from '$lib/components/settings/UnitField.svelte';

  // Store › Settings › Tax and deals: the store's sales tax, how deals combine and half-and-half pricing
  // (GET/PUT /me/pricing; the fees, tips and channels go back as they were loaded).
  type Form = { tax: string; stacking: 'best' | 'stack' | 'single'; halfTopping: 'full' | 'half'; halfOff: string };
  let body = $state<PricingBody | null>(null);
  let form = $state<Form | null>(null);
  let savedForm = $state('');
  let saving = $state(false);
  let err = $state('');
  const dirty = $derived(!!form && JSON.stringify(form) !== savedForm);

  function fill(b: PricingBody) {
    body = b;
    form = {
      tax: String(b.store.tax_bps / 100),
      stacking: b.store.stacking,
      halfTopping: b.store.half_topping_price === 'half' ? 'half' : 'full',
      halfOff: String(b.store.half_specialty_off_pct ?? 0)
    };
    savedForm = JSON.stringify(form);
  }
  onMount(() => storeApi<PricingBody>('/me/pricing').then(fill).catch((e) => (err = errText(e))));

  async function save() {
    if (!body || !form || saving) return;
    err = '';
    const tax = parseFloat(form.tax);
    if (!form.tax.trim() || !Number.isFinite(tax) || tax < 0 || tax > 30) {
      err = 'Sales tax: a percentage from 0 to 30.';
      return;
    }
    const off = Number(form.halfOff.trim() || '0');
    if (!Number.isInteger(off) || off < 0 || off > 100) {
      err = 'Two specialties: a whole percentage from 0 to 100.';
      return;
    }
    saving = true;
    try {
      const next: PricingBody = {
        ...body,
        store: { ...body.store, tax_bps: Math.round(tax * 100), stacking: form.stacking, half_topping_price: form.halfTopping, half_specialty_off_pct: off }
      };
      await storeApi('/me/pricing', { method: 'PUT', body: JSON.stringify(next) });
      fill(await storeApi<PricingBody>('/me/pricing'));
      toast.success('Tax and deals saved — channels price with them right away');
    } catch (x) {
      err = errText(x);
    } finally {
      saving = false;
    }
  }
</script>

<SettingsPage
  title="Tax and deals"
  subtitle="For the website, call center and counter."
  loading={!form && !err}
  {dirty}
  {saving}
  error={err}
  onsave={save}
  onundo={() => form && (form = JSON.parse(savedForm))}
>
  {#if form}
    <Section title="Sales tax" description="Items use their own tax rate when they have one (Menu › item), else this. Delivery fees aren’t taxed; the online fee is.">
      <UnitField id="pr-tax" label="Sales tax" unit="%" decimal bind:value={form.tax} />
    </Section>
    <Section title="Deals in one order" description="What happens when more than one deal or code fits an order.">
      <div class="max-w-[380px]">
        <label class="lbl" for="pr-stack">Deals in one order</label>
        <select id="pr-stack" class="field" bind:value={form.stacking}>
          {#each Object.entries(STACKING) as [v, label] (v)}<option value={v}>{label}</option>{/each}
        </select>
      </div>
    </Section>
    <Section
      title="Half and half"
      description="One specialty on a half costs that specialty’s price; two cost the dearer one plus the cheaper one’s extra over its plain pizza, at the % off below."
    >
      <div class="max-w-[380px]">
        <label class="lbl" for="pr-half">A topping on half a pizza</label>
        <select id="pr-half" class="field" bind:value={form.halfTopping}>
          <option value="full">Full price</option>
          <option value="half">Half price (rounded up)</option>
        </select>
      </div>
      <UnitField id="pr-halfoff" label="2 specialties: cheaper half’s extra, % off" unit="%" bind:value={form.halfOff} />
    </Section>
  {/if}
</SettingsPage>
