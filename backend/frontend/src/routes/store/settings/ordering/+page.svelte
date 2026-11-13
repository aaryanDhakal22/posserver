<script lang="ts">
  import { onMount } from 'svelte';
  import { errText, storeApi, type HoursException, type Location, type OrderingSettings } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';
  import Section from '$lib/components/settings/Section.svelte';
  import UnitField from '$lib/components/settings/UnitField.svelte';

  // Store › Settings › Ordering (GET/PUT /me/ordering): which order types the website, call center and counter
  // take, the ready-time estimates they show, how far ahead customers may order and the online tip limit
  // (the owner's X, default $50).
  type Form = { pickup: boolean; delivery: boolean; pickupEta: string; deliveryEta: string; days: string; tipMax: string };
  let form = $state<Form | null>(null);
  let savedForm = $state('');
  let saving = $state(false);
  let err = $state('');
  let errField = $state('');
  let resetLabel = $state('');
  const dirty = $derived(!!form && JSON.stringify(form) !== savedForm);

  function fill(s: OrderingSettings) {
    form = {
      pickup: s.pickup_enabled, delivery: s.delivery_enabled, pickupEta: String(s.pickup_eta_minutes),
      deliveryEta: String(s.delivery_eta_minutes), days: String(s.schedule_days), tipMax: String((s.tip_max_cents ?? 5000) / 100)
    };
    savedForm = JSON.stringify(form);
  }
  onMount(async () => {
    try {
      const [o, l] = await Promise.all([
        storeApi<{ settings: OrderingSettings; exceptions: HoursException[] }>('/me/ordering'),
        storeApi<Location>('/me/location').catch(() => null)
      ]);
      fill(o.settings);
      if (l) resetLabel = new Date(2000, 0, 1, l.daily_reset_hour).toLocaleTimeString('en-US', { hour: 'numeric' });
    } catch (e) {
      err = errText(e);
    }
  });

  function whole(v: string, what: string, field: string, hi: number): number {
    const n = Number(v.trim());
    if (!v.trim() || !Number.isInteger(n) || n < 0 || n > hi) {
      errField = field;
      throw new Error(`${what}: a whole number from 0 to ${hi}.`);
    }
    return n;
  }

  async function save() {
    if (!form || saving) return;
    err = errField = '';
    let body;
    try {
      const tip = Number(form.tipMax.trim());
      const pickupEta = whole(form.pickupEta, 'Pickup ready in', 'or-pickup-eta', 240);
      const deliveryEta = whole(form.deliveryEta, 'Delivery at the door in', 'or-delivery-eta', 240);
      const days = whole(form.days, 'Order ahead', 'or-days', 30);
      if (!form.tipMax.trim() || !Number.isFinite(tip) || tip < 0 || tip > 1000 || Math.round(tip * 100) !== tip * 100) {
        errField = 'or-tip-max';
        throw new Error('Tip limit: a dollar amount from $0 to $1,000.');
      }
      body = {
        pickup_enabled: form.pickup, delivery_enabled: form.delivery, pickup_eta_minutes: pickupEta,
        delivery_eta_minutes: deliveryEta, schedule_days: days, tip_max_cents: Math.round(tip * 100)
      };
    } catch (x) {
      err = errText(x);
      if (errField) document.getElementById(errField)?.focus();
      return;
    }
    saving = true;
    try {
      fill(await storeApi<OrderingSettings>('/me/ordering', { method: 'PUT', body: JSON.stringify(body) }));
      toast.success('Ordering saved — channels see it right away');
    } catch (x) {
      err = errText(x);
    } finally {
      saving = false;
    }
  }
</script>

<SettingsPage
  title="Ordering"
  subtitle="For the website, call center and counter (not Brygid)."
  loading={!form && !err}
  {dirty}
  {saving}
  error={err}
  onsave={save}
  onundo={() => form && (form = JSON.parse(savedForm))}
>
  {#if form}
    <Section title="Pickup" description="Customers come to the store for their order.">
      <label class="sw-row"><Switch bind:checked={form.pickup} label="Pickup" />Take pickup orders</label>
      <UnitField id="or-pickup-eta" label="Ready in" unit="min" bind:value={form.pickupEta} disabled={!form.pickup} invalid={errField === 'or-pickup-eta'} help="The estimate customers see." />
    </Section>
    <Section title="Delivery" description="Drivers take the order to the customer’s door.">
      <label class="sw-row"><Switch bind:checked={form.delivery} label="Delivery" />Take delivery orders</label>
      <UnitField id="or-delivery-eta" label="At the door in" unit="min" bind:value={form.deliveryEta} disabled={!form.delivery} invalid={errField === 'or-delivery-eta'} help="The estimate customers see." />
    </Section>
    <Section title="Ordering ahead">
      <UnitField
        id="or-days"
        label="Order ahead up to"
        unit="days"
        bind:value={form.days}
        invalid={errField === 'or-days'}
        help="0 = ASAP orders only. Customers pick a time every 15 minutes, starting the estimate above from now (or from opening)."
      />
      {#if resetLabel}<p class="help !mt-0">86’d items come back by themselves when the new day starts ({resetLabel}), unless they have their own “until”.</p>{/if}
    </Section>
    <Section title="Online tips">
      <UnitField
        id="or-tip-max"
        label="Online tips up to"
        unit="$"
        prefix
        decimal
        bind:value={form.tipMax}
        invalid={errField === 'or-tip-max'}
        help="A bigger tip on the website (or added after the order) is refused with a message. Default $50."
      />
    </Section>
  {/if}
</SettingsPage>

<style>
  .sw-row {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    font-size: 14px;
    font-weight: 500;
    cursor: pointer;
    width: fit-content;
  }
</style>
