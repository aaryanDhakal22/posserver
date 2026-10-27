<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, type Location } from '$lib/api';
  import { hoursFromConfig, hoursToConfig, type WeekHours } from '$lib/hours';
  import { OWNER_DEVICES_KEY, badDevice, devicesFromConfig, splitDevices } from '$lib/alerts';
  import { DEFAULT_TIMEZONE } from '$lib/timezones';
  import { storeSession } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import HoursEditor from '$lib/components/HoursEditor.svelte';
  import LocationFields, { locationForm, type LocationForm } from '$lib/components/LocationFields.svelte';
  import StoreMap from '$lib/components/StoreMap.svelte';

  // Location details + opening hours. PATCH /me/location is a full replace,
  // so every field goes back with the hours merged into the config.
  let loc = $state<Location | null>(null);
  let form = $state<LocationForm>({ name: '', phone: '', address_line1: '', address_line2: '', timezone: DEFAULT_TIMEZONE, daily_reset_hour: 0 });
  let hours = $state<WeekHours>(hoursFromConfig({}));
  let saving = $state(false);
  // Alerts: the owner's Pushover device names (config.pushover_owner_devices),
  // saved on their own so unsaved detail edits are left alone.
  let devices = $state('');
  let savingDevices = $state(false);
  let testing = $state(false);
  const deviceErr = $derived.by(() => {
    const bad = badDevice(devices);
    return bad ? `“${bad}” isn't a Pushover device name — use 1–25 letters, numbers, - or _.` : '';
  });
  const devicesDirty = $derived(!!loc && splitDevices(devices).join(',') !== splitDevices(devicesFromConfig(loc.config)).join(','));

  function fill(l: Location) {
    loc = l;
    form = locationForm(l);
    hours = hoursFromConfig(l.config);
    devices = devicesFromConfig(l.config);
  }

  onMount(async () => {
    try {
      fill(await storeApi<Location>('/me/location'));
    } catch (e) {
      toast.error(e);
    }
  });

  // Only the position fields: a reload must not clobber unsaved form edits.
  async function refreshPin() {
    try {
      const l = await storeApi<Location>('/me/location');
      if (loc) loc = { ...loc, lat: l.lat, lng: l.lng, geo_source: l.geo_source, map: l.map, delivery_radius_m: l.delivery_radius_m };
    } catch {
      /* best effort */
    }
  }

  async function savePin(lat: number, lng: number) {
    try {
      const l = await storeApi<Location>('/me/location/pin', { method: 'PUT', body: JSON.stringify({ lat, lng }) });
      if (loc) loc = { ...loc, lat: l.lat, lng: l.lng, geo_source: l.geo_source };
      toast.success('Store pin saved — the Pi gets it right away');
    } catch (err) {
      toast.error(err);
      throw err;
    }
  }

  // PATCH is a full replace: the saved fields go back with only the device
  // list changed in the config.
  async function saveDevices(): Promise<boolean> {
    if (!loc || deviceErr) return false;
    const l = await storeApi<Location>('/me/location', {
      method: 'PATCH',
      body: JSON.stringify({
        ...locationForm(loc),
        config: { ...(loc.config ?? {}), [OWNER_DEVICES_KEY]: splitDevices(devices).join(',') }
      })
    });
    loc = l;
    devices = devicesFromConfig(l.config);
    if (storeSession.who) storeSession.who.location = l;
    return true;
  }

  async function onSaveDevices(e: SubmitEvent) {
    e.preventDefault();
    if (savingDevices) return;
    savingDevices = true;
    try {
      if (await saveDevices()) toast.success('Phones saved — the Pi gets them right away');
    } catch (err) {
      toast.error(err);
    } finally {
      savingDevices = false;
    }
  }

  async function sendTest() {
    if (testing || deviceErr) return;
    testing = true;
    try {
      if (devicesDirty && !(await saveDevices())) return;
      await storeApi('/me/alerts/test', { method: 'POST' });
      toast.success('Test alert sent — check the phones');
    } catch (err) {
      toast.error(err);
    } finally {
      testing = false;
    }
  }

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
      const typed = devices; // the Alerts card saves on its own
      fill(l);
      devices = typed;
      if (storeSession.who) storeSession.who.location = l;
      toast.success('Details saved — the Pi gets them right away');
      // The address is geocoded in the background; pick up the new pin.
      if (l.geo_source !== 'manual') setTimeout(refreshPin, 2500);
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
  <form class="max-w-[760px] mt-4" onsubmit={onSaveDevices}>
    <Card title="Alerts">
      <div class="flex flex-col gap-4">
        <div>
          <label class="lbl" for="sd-alert-devices">Your phones in Pushover</label>
          <input
            id="sd-alert-devices"
            class="field mono"
            bind:value={devices}
            placeholder="OwnerPhone, OwnerTablet"
            aria-invalid={!!deviceErr || undefined}
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
          />
          {#if deviceErr}<p class="field-error" role="alert">{deviceErr}</p>{:else}<p class="help">Floor problems (order not accepted, nothing printed, printer down) go to these phones.</p>{/if}
        </div>
        <div class="flex flex-wrap gap-2.5">
          <Button type="submit" variant="primary" loading={savingDevices} disabled={!!deviceErr || testing}>Save phones</Button>
          <Button loading={testing} disabled={!!deviceErr || savingDevices || !splitDevices(devices).length} onclick={sendTest}>Send test</Button>
        </div>
      </div>
    </Card>
  </form>
  <div class="max-w-[760px] mt-4">
    <StoreMap {loc} onsave={savePin} idPrefix="sd-geo" />
  </div>
{/if}
