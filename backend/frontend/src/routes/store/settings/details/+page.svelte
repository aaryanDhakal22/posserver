<script lang="ts">
  import { onMount } from 'svelte';
  import { errText, storeApi, type Location } from '$lib/api';
  import { DEFAULT_TIMEZONE } from '$lib/timezones';
  import { storeSession } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import LocationFields, { locationForm, type LocationForm } from '$lib/components/LocationFields.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';
  import Section from '$lib/components/settings/Section.svelte';

  // Store › Settings › Store details: the name, phone, address (printed on receipts), the time zone, when the
  // store's day starts, and the store's email. Two saves, in order: the email (PUT /me/site/contact), then the
  // location (PATCH /me/location is a full replace: the saved config goes back as it is).
  let loc = $state<Location | null>(null);
  let form = $state<LocationForm>({ name: '', phone: '', address_line1: '', address_line2: '', timezone: DEFAULT_TIMEZONE, daily_reset_hour: 0 });
  let email = $state('');
  let savedEmail = $state('');
  let savedForm = $state('');
  let saving = $state(false);
  let err = $state('');

  const emailErr = $derived(email.trim() && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.trim()) ? "That isn't an email address." : '');
  const dirty = $derived(!!loc && (JSON.stringify(form) !== savedForm || email.trim() !== savedEmail));

  function fill(l: Location) {
    loc = l;
    form = locationForm(l);
    savedForm = JSON.stringify(form);
  }

  onMount(async () => {
    try {
      const [l, c] = await Promise.all([
        storeApi<Location>('/me/location'),
        storeApi<{ reply_email: string }>('/me/site/contact').catch(() => ({ reply_email: '' }))
      ]);
      fill(l);
      email = savedEmail = c.reply_email;
    } catch (e) {
      err = errText(e);
    }
  });

  async function save() {
    if (!loc || saving) return;
    err = '';
    if (emailErr) {
      err = 'Store email: ' + emailErr;
      document.getElementById('sd-email')?.focus();
      return;
    }
    saving = true;
    let emailDone = false;
    try {
      if (email.trim() !== savedEmail) {
        const c = await storeApi<{ reply_email: string }>('/me/site/contact', { method: 'PUT', body: JSON.stringify({ reply_email: email.trim() }) });
        email = savedEmail = c.reply_email;
        emailDone = true;
      }
      if (JSON.stringify(form) !== savedForm) {
        const l = await storeApi<Location>('/me/location', {
          method: 'PATCH',
          body: JSON.stringify({ ...form, daily_reset_hour: Number(form.daily_reset_hour) || 0, config: loc.config ?? {} })
        });
        fill(l);
        if (storeSession.who) storeSession.who.location = l;
      }
      toast.success('Store details saved — the Pi gets them right away');
    } catch (x) {
      // Say exactly what was kept: the email may be saved while the details weren't.
      err = emailDone ? `The store email was saved, but the details weren’t: ${errText(x)}` : errText(x);
    } finally {
      saving = false;
    }
  }

  function undo() {
    if (!loc) return;
    form = JSON.parse(savedForm);
    email = savedEmail;
    err = '';
  }
</script>

<SettingsPage
  title="Store details"
  subtitle="Your store’s name, phone and address, as printed on receipts and shown to customers."
  loading={!loc && !err}
  {dirty}
  {saving}
  error={err}
  onsave={save}
  onundo={undo}
>
  {#if loc}
    <Section title="Name, phone and address" description="A new address moves the map pin by itself, unless you placed the pin by hand (Settings › Map and delivery area).">
      <LocationFields bind:value={form} idPrefix="sd" only="contact" />
    </Section>
    <Section title="Store email" description="When customers reply to their emailed receipts, the reply comes here.">
      <div class="max-w-[380px]">
        <label class="lbl" for="sd-email">Store email</label>
        <input id="sd-email" class="field" type="email" bind:value={email} placeholder="owner@example.com" autocomplete="off" aria-invalid={!!emailErr || undefined} />
        {#if emailErr}<p class="field-error" role="alert">{emailErr}</p>{:else}<p class="help">Leave it empty to turn replies off.</p>{/if}
      </div>
    </Section>
    <Section title="The store’s day" description="86’d items come back and day reports roll over when the new day starts.">
      <div class="max-w-[380px]">
        <LocationFields bind:value={form} idPrefix="sd-day" showName={false} only="day" />
      </div>
    </Section>
  {/if}
</SettingsPage>
