<script lang="ts">
  import { onMount } from 'svelte';
  import { errText, storeApi, type Location } from '$lib/api';
  import { hoursFromConfig, hoursToConfig, type WeekHours } from '$lib/hours';
  import { locationForm } from '$lib/components/LocationFields.svelte';
  import { storeSession } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import HoursEditor from '$lib/components/HoursEditor.svelte';
  import SpecialDays from '$lib/components/store/SpecialDays.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';
  import Section from '$lib/components/settings/Section.svelte';

  // Store › Settings › Hours: the weekly opening hours (location config opening_hours; PATCH /me/location is a
  // full replace, so the rest goes back as saved) and special days (each saved on its own as it's added).
  let loc = $state<Location | null>(null);
  let hours = $state<WeekHours>(hoursFromConfig({}));
  let savedHours = $state('');
  let saving = $state(false);
  let err = $state('');
  const dirty = $derived(!!loc && JSON.stringify(hours) !== savedHours);

  function fill(l: Location) {
    loc = l;
    hours = hoursFromConfig(l.config);
    savedHours = JSON.stringify(hours);
  }
  onMount(() => storeApi<Location>('/me/location').then(fill).catch((e) => (err = errText(e))));

  async function save() {
    if (!loc || saving) return;
    err = '';
    saving = true;
    try {
      const l = await storeApi<Location>('/me/location', {
        method: 'PATCH',
        body: JSON.stringify({ ...locationForm(loc), config: { ...(loc.config ?? {}), opening_hours: hoursToConfig(hours) } })
      });
      fill(l);
      if (storeSession.who) storeSession.who.location = l;
      toast.success('Hours saved — the Pi and every channel get them right away');
    } catch (x) {
      err = errText(x);
    } finally {
      saving = false;
    }
  }
</script>

<SettingsPage
  title="Hours"
  subtitle="When the website, call center and counter take orders."
  loading={!loc && !err}
  {dirty}
  {saving}
  error={err}
  saveLabel="Save hours"
  onsave={save}
  onundo={() => loc && fill(loc)}
>
  {#if loc}
    <Section title="Every week">
      <HoursEditor bind:hours idPrefix="sd-hours" />
    </Section>
  {/if}
</SettingsPage>

{#if loc}
  <div class="max-w-[720px] w-full">
    <SpecialDays />
  </div>
{/if}
