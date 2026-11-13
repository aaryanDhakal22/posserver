<script lang="ts">
  import { onMount } from 'svelte';
  import { centsInput, dollars, errText, storeApi, toCents, type DeliveryBand, type Location, type PricingBody } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import StoreMap from '$lib/components/StoreMap.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';
  import Section from '$lib/components/settings/Section.svelte';
  import UnitField from '$lib/components/settings/UnitField.svelte';
  import Button from '$lib/ui/Button.svelte';

  // Store › Settings › Map and delivery area: the store pin (PUT /me/location/pin, saved by the map's own
  // "Use this spot"), the delivery radius (PUT /me/location/radius — main is the one place to change it; the Pi,
  // its tablets, the terminals, the call center and the website follow) and the customer's delivery fee by
  // distance (fees.delivery_bands in PUT /me/pricing; one row = the flat fee). The driver's pay is separate
  // (Fees and tips › driver share).
  const MI = 1609.344;
  const MIN_MI = 0.4; // 500 m … 50 mi (main's limits)
  const MAX_MI = 50;

  type Row = { upTo: string; fee: string };
  type Form = { radius: string; rows: Row[] };

  let loc = $state<Location | null>(null);
  let pricing = $state<PricingBody | null>(null);
  let form = $state<Form | null>(null);
  let savedForm = $state('');
  let saving = $state(false);
  let err = $state('');
  let errField = $state('');
  const dirty = $derived(!!form && JSON.stringify(form) !== savedForm);

  const mi = (m: number) => (Math.round((m / MI) * 10) / 10).toString();
  const toM = (miles: number) => Math.round(miles * MI);

  function fill(l: Location, p: PricingBody) {
    loc = l;
    pricing = p;
    const f = p.store.fees;
    const bands = f.delivery_bands?.length ? f.delivery_bands : [{ up_to_m: l.delivery_radius_m, fee_cents: f.delivery_fee_cents }];
    form = { radius: mi(l.delivery_radius_m), rows: bands.map((b) => ({ upTo: mi(b.up_to_m), fee: centsInput(b.fee_cents) })) };
    savedForm = JSON.stringify(form);
  }
  const load = () =>
    Promise.all([storeApi<Location>('/me/location'), storeApi<PricingBody>('/me/pricing')])
      .then(([l, p]) => fill(l, p))
      .catch((e) => (err = errText(e)));
  onMount(load);

  const radiusPreview = $derived.by(() => {
    const v = Number(form?.radius.trim());
    return form && Number.isFinite(v) && v >= MIN_MI && v <= MAX_MI ? toM(v) : undefined;
  });
  const ringsPreview = $derived(
    (form?.rows ?? []).slice(0, -1).map((r) => toM(Number(r.upTo))).filter((m) => Number.isFinite(m) && m > 0)
  );

  class Bad extends Error {
    field: string;
    constructor(field: string, msg: string) {
      super(msg);
      this.field = field;
    }
  }

  /** The radius (meters) and the bands as main takes them (null = one row: the flat fee). */
  function parse(f: Form): { radiusM: number; bands: DeliveryBand[] | null; flat: number } {
    const r = Number(f.radius.trim());
    if (!f.radius.trim() || !Number.isFinite(r) || r < MIN_MI || r > MAX_MI || Math.round(r * 10) !== r * 10)
      throw new Bad('delivery_radius', `Delivers up to: ${MIN_MI} to ${MAX_MI} miles (one decimal, like 4.5).`);
    const radiusM = toM(r);
    const rows = f.rows.map((row, i) => {
      const fee = toCents(row.fee);
      if (fee === null || fee < 0 || fee > 10000) throw new Bad(`band-fee-${i}`, 'Delivery fee: a dollar amount from $0 to $100.');
      const last = i === f.rows.length - 1;
      const upTo = last ? radiusM : toM(Number(row.upTo));
      if (!last) {
        const v = Number(row.upTo.trim());
        if (!row.upTo.trim() || !Number.isFinite(v) || v <= 0 || Math.round(v * 10) !== v * 10)
          throw new Bad(`band-upto-${i}`, 'Up to: miles with one decimal, like 2.5.');
        if (upTo >= radiusM) throw new Bad(`band-upto-${i}`, 'Each distance stays inside the delivery area (the last row covers the rest).');
      }
      return { up_to_m: upTo, fee_cents: fee };
    });
    for (let i = 1; i < rows.length; i++)
      if (rows[i].up_to_m <= rows[i - 1].up_to_m) throw new Bad(`band-upto-${i}`, 'Each row reaches farther than the one above it.');
    return { radiusM, bands: rows.length > 1 ? rows : null, flat: rows[0].fee_cents };
  }

  async function save() {
    if (!form || !loc || !pricing || saving) return;
    err = errField = '';
    let want: ReturnType<typeof parse>;
    try {
      want = parse(form);
    } catch (x) {
      err = errText(x);
      if (x instanceof Bad) {
        errField = x.field;
        document.getElementById(x.field === 'delivery_radius' ? 'map-radius' : x.field)?.focus();
      }
      return;
    }
    saving = true;
    const done: string[] = [];
    try {
      if (want.radiusM !== loc.delivery_radius_m) {
        await storeApi<Location>('/me/location/radius', { method: 'PUT', body: JSON.stringify({ radius_m: want.radiusM }) });
        done.push('delivery area');
      }
      const f = pricing.store.fees;
      const same = JSON.stringify(f.delivery_bands?.length ? f.delivery_bands : null) === JSON.stringify(want.bands) &&
        (want.bands !== null || f.delivery_fee_cents === want.flat);
      if (!same) {
        // Only the customer's fee moves; the driver's share (Fees and tips) stays as it is.
        const fees = { ...f, delivery_bands: want.bands, delivery_fee_cents: want.flat };
        await storeApi('/me/pricing', { method: 'PUT', body: JSON.stringify({ ...pricing, store: { ...pricing.store, fees } }) });
        done.push('delivery fee');
      }
      await load();
      toast.success(done.length ? `Saved the ${done.join(' and the ')} — the Pi, terminals, call center and website get it right away` : 'Nothing changed');
    } catch (x) {
      err = errText(x) + (done.length ? ` (the ${done.join(' and the ')} saved)` : '');
    } finally {
      saving = false;
    }
  }

  function addRow() {
    if (!form) return;
    const prev = form.rows.length > 1 ? Number(form.rows[form.rows.length - 2].upTo) || 0 : 0;
    const r = Number(form.radius) || 5;
    const upTo = Math.round(((prev + r) / 2) * 10) / 10;
    form.rows.splice(form.rows.length - 1, 0, { upTo: String(upTo), fee: form.rows[form.rows.length - 1].fee });
  }

  async function savePin(lat: number, lng: number) {
    try {
      const l = await storeApi<Location>('/me/location/pin', { method: 'PUT', body: JSON.stringify({ lat, lng }) });
      if (loc) loc = { ...loc, lat: l.lat, lng: l.lng, geo_source: l.geo_source };
      toast.success('Store pin saved — the Pi gets it right away');
    } catch (x) {
      toast.error(x);
      throw x;
    }
  }
</script>

<SettingsPage
  title="Map and delivery area"
  subtitle="Where the store is, how far it delivers and what a delivery costs the customer."
  loading={!form && !err}
  {dirty}
  {saving}
  error={err}
  onsave={save}
  onundo={() => form && ((form = JSON.parse(savedForm)), (err = errField = ''))}
>
  {#if loc}<StoreMap {loc} onsave={savePin} idPrefix="sd-geo" radiusM={radiusPreview} ringsM={ringsPreview} />{/if}
  {#if form}
    <Section title="Delivery area" description="The circle on the map. Past it the call center, the counter terminals and the website warn (the website refuses); Counter orders › “Deliver past the delivery area by up to” sets how far past it still goes with a warning.">
      <div id="delivery_radius" class="max-w-[260px]">
        <UnitField
          id="map-radius"
          label="Delivers up to"
          unit="mi"
          decimal
          invalid={errField === 'delivery_radius'}
          help="Straight line from the store pin, 0.4 to 50 miles."
          bind:value={form.radius}
        />
      </div>
    </Section>
    <Section
      title="Delivery fee by distance"
      description="What the customer pays for a delivery, by how far it goes. One row = the same fee everywhere. An address that isn’t located yet pays the first row. The driver’s pay is separate (Fees and tips › driver share)."
    >
      <div id="delivery_fee_bands" class="bands" data-testid="delivery-bands">
        {#each form.rows as row, i (i)}
          {@const last = i === form.rows.length - 1}
          <div class="band">
            <span class="band-from">
              {#if form.rows.length === 1}Every delivery{:else if i === 0}From the store{:else}From {form.rows[i - 1].upTo || '…'} mi{/if}
            </span>
            {#if last}
              <span class="band-to">{form.rows.length === 1 ? '' : 'to the edge and past it'}</span>
            {:else}
              <label class="band-to">
                <span class="sr-only">Row {i + 1}: up to (miles)</span>
                up to
                <input id="band-upto-{i}" class="field mono !w-20 text-right" inputmode="decimal" bind:value={row.upTo} aria-invalid={errField === `band-upto-${i}` || undefined} />
                mi
              </label>
            {/if}
            <label class="band-fee">
              <span class="sr-only">Row {i + 1}: fee ($)</span>
              $<input id="band-fee-{i}" class="field mono !w-24 text-right" inputmode="decimal" bind:value={row.fee} aria-invalid={errField === `band-fee-${i}` || undefined} />
            </label>
            {#if !last}
              <Button size="sm" variant="ghost" icon="x" onclick={() => form!.rows.splice(i, 1)}>Remove</Button>
            {/if}
          </div>
        {/each}
        <div>
          <Button size="sm" variant="ghost" icon="plus" onclick={addRow} disabled={form.rows.length >= 10}>Add a distance</Button>
        </div>
        {#if pricing}
          <p class="help max-w-[60ch]">
            Today: {form.rows.length === 1 ? `${dollars(toCents(form.rows[0].fee) ?? 0)} for every delivery` : 'priced by distance'}. A channel with its
            own fees (Fees and tips) keeps its own delivery fee.
          </p>
        {/if}
      </div>
    </Section>
  {/if}
</SettingsPage>

<style>
  .bands {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .band {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 14px;
    font-size: 14px;
  }
  .band-from {
    min-width: 110px;
    color: var(--text2);
  }
  .band-to,
  .band-fee {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .band-to {
    min-width: 170px;
  }
</style>
