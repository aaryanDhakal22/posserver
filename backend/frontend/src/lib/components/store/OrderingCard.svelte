<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, type OrderingSettings, type HoursException } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';

  // Which order types the store's channels take, the ready-time estimates
  // they show, and how far ahead customers may order. `resetLabel` is when
  // the store's new day starts (86'd items come back then).
  let { resetLabel = '' }: { resetLabel?: string } = $props();

  let s = $state<OrderingSettings | null>(null);
  let saving = $state(false);
  // The tip limit is edited in dollars (X: default $50).
  let tipMax = $state('50');
  let tipErr = $state('');

  onMount(async () => {
    try {
      s = (await storeApi<{ settings: OrderingSettings; exceptions: HoursException[] }>('/me/ordering')).settings;
      tipMax = String((s.tip_max_cents ?? 5000) / 100);
    } catch (e) {
      toast.error(e);
    }
  });

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!s || saving) return;
    tipErr = '';
    const tip = Number(tipMax.trim());
    if (!tipMax.trim() || !Number.isFinite(tip) || tip < 0 || tip > 1000 || Math.round(tip * 100) !== tip * 100) {
      tipErr = 'Tip limit: a dollar amount from $0 to $1,000.';
      return;
    }
    saving = true;
    try {
      s = await storeApi<OrderingSettings>('/me/ordering', {
        method: 'PUT',
        body: JSON.stringify({
          pickup_enabled: s.pickup_enabled,
          delivery_enabled: s.delivery_enabled,
          pickup_eta_minutes: Number(s.pickup_eta_minutes) || 0,
          delivery_eta_minutes: Number(s.delivery_eta_minutes) || 0,
          schedule_days: Number(s.schedule_days) || 0,
          tip_max_cents: Math.round(tip * 100)
        })
      });
      tipMax = String(s.tip_max_cents / 100);
      toast.success('Ordering saved — channels see it right away');
    } catch (err) {
      toast.error(err);
    } finally {
      saving = false;
    }
  }
</script>

<form onsubmit={save}>
  <Card title="Ordering" subtitle="For the website, call center and counter (not Brygid).">
    {#if !s}
      <Skeleton lines={3} />
    {:else}
      <div class="flex flex-col gap-4">
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="subcard p-3.5 flex flex-col gap-3">
            <label class="flex items-center gap-2.5 font-medium"><Switch bind:checked={s.pickup_enabled} label="Pickup" />Pickup</label>
            <div>
              <label class="lbl" for="or-pickup-eta">Ready in (minutes)</label>
              <input id="or-pickup-eta" type="number" min="0" max="240" class="field mono !w-28" bind:value={s.pickup_eta_minutes} disabled={!s.pickup_enabled} />
            </div>
          </div>
          <div class="subcard p-3.5 flex flex-col gap-3">
            <label class="flex items-center gap-2.5 font-medium"><Switch bind:checked={s.delivery_enabled} label="Delivery" />Delivery</label>
            <div>
              <label class="lbl" for="or-delivery-eta">At the door in (minutes)</label>
              <input id="or-delivery-eta" type="number" min="0" max="240" class="field mono !w-28" bind:value={s.delivery_eta_minutes} disabled={!s.delivery_enabled} />
            </div>
          </div>
        </div>
        <div>
          <label class="lbl" for="or-days">Order ahead up to (days)</label>
          <input id="or-days" type="number" min="0" max="30" class="field mono !w-28" bind:value={s.schedule_days} />
          <p class="help">0 = ASAP orders only. Customers pick a time every 15 minutes, starting the estimate above from now (or from opening).</p>
        </div>
        <div>
          <label class="lbl" for="or-tip-max">Online tips up to ($)</label>
          <input id="or-tip-max" inputmode="decimal" class="field mono !w-28" bind:value={tipMax} aria-invalid={!!tipErr || undefined} />
          {#if tipErr}<p class="field-error" role="alert">{tipErr}</p>{/if}
          <p class="help">A bigger tip on the website (or added after the order) is refused with a message. Default $50.</p>
        </div>
        {#if resetLabel}<p class="help !mt-0">86'd items come back by themselves when the new day starts ({resetLabel}), unless they have their own “until”.</p>{/if}
        <div><Button type="submit" variant="primary" loading={saving}>Save ordering</Button></div>
      </div>
    {/if}
  </Card>
</form>
