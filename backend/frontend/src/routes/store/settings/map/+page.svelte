<script lang="ts">
  import { onMount } from 'svelte';
  import { errText, storeApi, type Location } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import StoreMap from '$lib/components/StoreMap.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';

  // Store › Settings › Map and delivery area: the store pin (PUT /me/location/pin, saved by the map's own
  // "Use this spot") and the delivery circle around it.
  let loc = $state<Location | null>(null);
  let err = $state('');
  onMount(() => storeApi<Location>('/me/location').then((l) => (loc = l)).catch((e) => (err = errText(e))));

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

<SettingsPage title="Map and delivery area" subtitle="Where the store is, for driver routes and the delivery area." loading={!loc && !err}>
  {#if err}<p class="field-error" role="alert">{err}</p>{/if}
  {#if loc}<StoreMap {loc} onsave={savePin} idPrefix="sd-geo" />{/if}
</SettingsPage>
