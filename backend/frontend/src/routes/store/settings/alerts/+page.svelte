<script lang="ts">
  import { onMount } from 'svelte';
  import { errText, storeApi, type Location } from '$lib/api';
  import { OWNER_DEVICES_KEY, badDevice, devicesFromConfig, splitDevices } from '$lib/alerts';
  import { locationForm } from '$lib/components/LocationFields.svelte';
  import { storeSession } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';
  import Section from '$lib/components/settings/Section.svelte';

  // Store › Settings › Alerts: the owner's Pushover device names (location config pushover_owner_devices;
  // PATCH /me/location is a full replace, so the rest goes back as saved) + a test push.
  let loc = $state<Location | null>(null);
  let devices = $state('');
  let saving = $state(false);
  let testing = $state(false);
  let err = $state('');
  const deviceErr = $derived.by(() => {
    const bad = badDevice(devices);
    return bad ? `“${bad}” isn't a Pushover device name — use 1–25 letters, numbers, - or _.` : '';
  });
  const dirty = $derived(!!loc && splitDevices(devices).join(',') !== splitDevices(devicesFromConfig(loc.config)).join(','));

  function fill(l: Location) {
    loc = l;
    devices = devicesFromConfig(l.config);
  }
  onMount(() => storeApi<Location>('/me/location').then(fill).catch((e) => (err = errText(e))));

  async function saveDevices(): Promise<boolean> {
    if (!loc) return false;
    if (deviceErr) {
      err = deviceErr;
      return false;
    }
    const l = await storeApi<Location>('/me/location', {
      method: 'PATCH',
      body: JSON.stringify({ ...locationForm(loc), config: { ...(loc.config ?? {}), [OWNER_DEVICES_KEY]: splitDevices(devices).join(',') } })
    });
    fill(l);
    if (storeSession.who) storeSession.who.location = l;
    return true;
  }

  async function save() {
    if (saving) return;
    err = '';
    saving = true;
    try {
      if (await saveDevices()) toast.success('Phones saved — the Pi gets them right away');
    } catch (x) {
      err = errText(x);
    } finally {
      saving = false;
    }
  }

  async function sendTest() {
    if (testing || deviceErr) return;
    err = '';
    testing = true;
    try {
      if (dirty && !(await saveDevices())) return;
      await storeApi('/me/alerts/test', { method: 'POST' });
      toast.success('Test alert sent — check the phones');
    } catch (x) {
      err = errText(x);
    } finally {
      testing = false;
    }
  }
</script>

<SettingsPage
  title="Alerts"
  subtitle="Who gets a push when something goes wrong on the floor."
  loading={!loc && !err}
  {dirty}
  {saving}
  error={err}
  saveLabel="Save phones"
  onsave={save}
  onundo={() => loc && fill(loc)}
>
  {#if loc}
    <Section title="Your phones in Pushover" description="Floor problems (an order nobody accepted, nothing printed, a printer down) and the store going offline go to these phones.">
      <div class="flex flex-col gap-4">
        <div class="max-w-[420px]">
          <label class="lbl" for="sd-alert-devices">Pushover device names</label>
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
          {#if deviceErr}<p class="field-error" role="alert">{deviceErr}</p>{:else}<p class="help">Separated by commas, as they are named in the Pushover app.</p>{/if}
        </div>
        <div>
          <Button icon="bell" loading={testing} disabled={!!deviceErr || saving || !splitDevices(devices).length} onclick={sendTest}>Send test</Button>
        </div>
      </div>
    </Section>
  {/if}
</SettingsPage>
