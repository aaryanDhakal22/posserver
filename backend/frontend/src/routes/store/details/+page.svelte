<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, type Location } from '$lib/api';
  import { hoursFromConfig, hoursToConfig, type WeekHours } from '$lib/hours';
  import { DEFAULT_TIMEZONE } from '$lib/timezones';
  import { storeSession } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import HoursEditor from '$lib/components/HoursEditor.svelte';
  import LocationFields, { locationForm, type LocationForm } from '$lib/components/LocationFields.svelte';

  // Location details + opening hours. PATCH /me/location is a full replace,
  // so every field goes back with the hours merged into the config.
  let loc = $state<Location | null>(null);
  let form = $state<LocationForm>({ name: '', phone: '', address_line1: '', address_line2: '', timezone: DEFAULT_TIMEZONE, daily_reset_hour: 0 });
  let hours = $state<WeekHours>(hoursFromConfig({}));
  let saving = $state(false);

  function fill(l: Location) {
    loc = l;
    form = locationForm(l);
    hours = hoursFromConfig(l.config);
  }

  onMount(async () => {
    try {
      fill(await storeApi<Location>('/me/location'));
    } catch (e) {
      toast.error(e);
    }
  });

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!loc || saving) return;
    saving = true;
    try {
      const l = await storeApi<Location>('/me/location', {
        method: 'PATCH',
        body: JSON.stringify({
          ...form,
          daily_reset_hour: Number(form.daily_reset_hour) || 0,
          config: { ...(loc.config ?? {}), opening_hours: hoursToConfig(hours) }
        })
      });
      fill(l);
      if (storeSession.who) storeSession.who.location = l;
      toast.success('Details saved — the Pi gets them right away');
    } catch (err) {
      toast.error(err);
    } finally {
      saving = false;
    }
  }
</script>

<svelte:head><title>Details & hours — quicc</title></svelte:head>

<PageHeader title="Details & hours" subtitle="Printed on receipts. Opening hours decide when the Pi-offline alert is on." />

{#if !loc}
  <Card><Skeleton lines={6} /></Card>
{:else}
  <form class="flex flex-col gap-4 max-w-[760px]" onsubmit={save}>
    <Card title="Store details">
      <LocationFields bind:value={form} idPrefix="sd" />
    </Card>
    <Card>
      <HoursEditor bind:hours idPrefix="sd-hours" />
    </Card>
    <div><Button type="submit" variant="primary" loading={saving}>Save details & hours</Button></div>
  </form>
{/if}
