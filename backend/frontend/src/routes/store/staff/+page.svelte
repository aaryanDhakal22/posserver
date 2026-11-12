<script lang="ts">
  import { onMount } from 'svelte';
  import {
    ApiError,
    centsInput,
    dollars,
    errText,
    storeApi,
    toCents,
    TERMINAL_DEFAULTS,
    type Staff,
    type StaffLimits,
    type TerminalSettings
  } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';

  // Store › Staff: the people who log in to the counter terminal with a PIN
  // (cashier / manager / driver), their own limits, and the store's terminal
  // settings (contract/main.md §2.5zb). A PIN is never shown back.
  const ROLES = [
    { value: 'cashier', label: 'Cashier' },
    { value: 'manager', label: 'Manager' },
    { value: 'driver', label: 'Driver' }
  ];
  const roleLabel = (r: string) => ROLES.find((x) => x.value === r)?.label ?? r;
  const TABS = [
    { value: 'info', label: 'Info' },
    { value: 'counter', label: 'Counter' },
    { value: 'takeout', label: 'Takeout' },
    { value: 'delivery', label: 'Delivery' },
    { value: 'pickup', label: 'Pickup' },
    { value: 'history', label: 'History' },
    { value: 'dispatch', label: 'Dispatch' }
  ];
  const FONTS = [
    { value: 'barlow', label: 'Barlow' },
    { value: 'barlow_condensed', label: 'Barlow Condensed' },
    { value: 'barlow_semi_condensed', label: 'Barlow Semi Condensed' },
    { value: 'system', label: 'System (Roboto)' }
  ];

  let staff = $state<Staff[] | null>(null);

  // --- the person dialog (add / edit / reactivate) ----------------------------
  type Form = { first: string; last: string; role: string; pin: string; refund: string; discount: string };
  let open = $state(false);
  let editing = $state<Staff | null>(null);
  let reactivating = $state(false);
  let form = $state<Form>(blank());
  let formErr = $state('');
  let pinErr = $state('');
  let saving = $state(false);

  function blank(): Form {
    return { first: '', last: '', role: 'cashier', pin: '', refund: '', discount: '' };
  }

  const PIN_RE = /^\d{2,4}$/;
  const pinText = (e: unknown) =>
    e instanceof ApiError && e.code === 'pin_taken'
      ? 'Another active person has that PIN.'
      : e instanceof ApiError && e.code === 'bad_pin'
        ? 'The PIN must be 2–4 digits.'
        : e instanceof ApiError && e.code === 'pin_required'
          ? 'Turning someone back on needs a new PIN.'
          : '';

  async function load() {
    staff = await storeApi<Staff[]>('/me/staff');
  }

  function add() {
    editing = null;
    reactivating = false;
    form = blank();
    formErr = pinErr = '';
    open = true;
  }
  function edit(p: Staff) {
    editing = p;
    reactivating = false;
    form = {
      first: p.first_name,
      last: p.last_name,
      role: p.role,
      pin: '',
      refund: p.limits.refund_cents == null ? '' : centsInput(p.limits.refund_cents),
      discount: p.limits.discount_pct == null ? '' : String(p.limits.discount_pct)
    };
    formErr = pinErr = '';
    open = true;
  }
  function reactivate(p: Staff) {
    edit(p);
    reactivating = true;
  }

  /** The person's own limits from the form (blank = the role default). */
  function limitsOf(f: Form): StaffLimits {
    const out: StaffLimits = {};
    if (f.refund.trim()) {
      const c = toCents(f.refund);
      if (c === null || c < 0) throw new Error(`“${f.refund}” isn't a dollar amount.`);
      out.refund_cents = c;
    }
    if (f.discount.trim()) {
      const d = Number(f.discount);
      if (!Number.isInteger(d) || d < 0 || d > 100) throw new Error('The discount limit is a whole % from 0 to 100.');
      out.discount_pct = d;
    }
    return out;
  }

  async function savePerson(e: SubmitEvent) {
    e.preventDefault();
    if (saving) return;
    formErr = pinErr = '';
    if (!form.first.trim()) return (formErr = 'Give the first name.');
    const needPin = !editing || reactivating;
    if ((needPin || form.pin) && !PIN_RE.test(form.pin)) return (pinErr = 'The PIN must be 2–4 digits.');
    let limits: StaffLimits;
    try {
      limits = limitsOf(form);
    } catch (x) {
      return (formErr = errText(x));
    }
    saving = true;
    try {
      const body: Record<string, unknown> = { first_name: form.first.trim(), last_name: form.last.trim(), role: form.role, limits };
      if (!editing) {
        await storeApi('/me/staff', { method: 'POST', body: JSON.stringify({ ...body, pin: form.pin }) });
        toast.success(`${form.first.trim()} added`);
      } else {
        if (reactivating) {
          body.active = true;
          body.pin = form.pin;
        } else if (form.pin) {
          body.pin = form.pin;
        }
        await storeApi('/me/staff/' + editing.id, { method: 'PATCH', body: JSON.stringify(body) });
        toast.success(reactivating ? `${form.first.trim()} is active again` : 'Saved');
      }
      open = false;
      await load();
    } catch (x) {
      const p = pinText(x);
      if (p) pinErr = p;
      else formErr = errText(x);
    } finally {
      saving = false;
    }
  }

  async function deactivate(p: Staff) {
    const ok = await confirmDialog({
      title: `Deactivate ${p.first_name}?`,
      body: 'Their PIN stops working on the terminals and is freed for someone else. Turning them back on needs a new PIN.',
      confirmLabel: 'Deactivate',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi('/me/staff/' + p.id, { method: 'PATCH', body: JSON.stringify({ active: false }) });
      await load();
      toast.success(`${p.first_name} deactivated`);
    } catch (x) {
      toast.error(x);
    }
  }

  async function remove(p: Staff) {
    const ok = await confirmDialog({
      title: `Delete ${p.first_name} ${p.last_name}`.trim() + '?',
      body: 'They are removed for good (the action log keeps their past actions). Deactivating is usually enough.',
      confirmLabel: 'Delete',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi('/me/staff/' + p.id, { method: 'DELETE' });
      await load();
      toast.success('Deleted');
    } catch (x) {
      toast.error(x);
    }
  }

  const limitText = (v: number | null, money: boolean) => (v === null ? 'unlimited' : money ? dollars(v) : `${v}%`);

  // --- terminal settings -------------------------------------------------------
  type SettingsForm = {
    refund: string; discount: string; lock: string; counterStart: string; receiptDone: string;
    autoClose: boolean; autoCloseAt: string; defaultTab: string; showHistory: boolean; font: string;
    priceDrop: string; clickSound: boolean; pricesOnButtons: boolean; margin: string; nameOnPaid: boolean;
    cardTips: boolean; tipPercents: string; meal: string;
  };
  let sf = $state<SettingsForm | null>(null);
  let sErr = $state('');
  let sSaving = $state(false);

  function settingsForm(raw: Partial<TerminalSettings>): SettingsForm {
    const s = { ...TERMINAL_DEFAULTS, ...raw };
    return {
      refund: centsInput(s.cashier_refund_cents), discount: String(s.cashier_discount_pct), lock: String(s.auto_lock_minutes),
      counterStart: String(s.counter_start), receiptDone: String(s.receipt_auto_done_seconds),
      autoClose: s.auto_close_batch_at !== '', autoCloseAt: s.auto_close_batch_at || '04:00',
      defaultTab: s.default_tab, showHistory: s.show_history, font: s.button_font, priceDrop: String(s.cashier_price_drop_pct),
      clickSound: s.click_sound, pricesOnButtons: s.prices_on_buttons, margin: String(s.out_of_area_margin_mi),
      nameOnPaid: s.name_on_paid_counter, cardTips: s.card_tips, tipPercents: (s.card_tip_percents ?? []).join(', '),
      meal: String(s.employee_meal_pct)
    };
  }

  function int(v: string, what: string, lo: number, hi: number): number {
    const n = Number(v.trim());
    if (!v.trim() || !Number.isInteger(n) || n < lo || n > hi) throw new Error(`${what} must be a whole number from ${lo} to ${hi}.`);
    return n;
  }

  function settingsOf(f: SettingsForm): TerminalSettings {
    const refund = toCents(f.refund);
    if (refund === null || refund < 0 || refund > 1_000_000) throw new Error('The cashier refund limit must be $0–$10,000.');
    const margin = Number(f.margin.trim());
    if (!f.margin.trim() || !Number.isFinite(margin) || margin < 0 || margin > 10 || Math.round(margin * 10) !== margin * 10)
      throw new Error('The out-of-area margin is 0–10 miles (one decimal).');
    const tips = f.tipPercents.split(',').map((x) => x.trim()).filter(Boolean);
    if (tips.length < 1 || tips.length > 6) throw new Error('Card tips need 1–6 percentages.');
    const percents = tips.map((x) => int(x, 'A card tip choice', 0, 100));
    if (f.autoClose && !/^([01]\d|2[0-3]):[0-5]\d$/.test(f.autoCloseAt)) throw new Error('The auto-close time must be HH:MM.');
    return {
      cashier_refund_cents: refund,
      cashier_discount_pct: int(f.discount, 'The cashier discount limit', 0, 100),
      auto_lock_minutes: int(f.lock, 'Auto-lock', 1, 240),
      counter_start: int(f.counterStart, 'The counter start', 1, 999_999),
      receipt_auto_done_seconds: int(f.receiptDone, 'The receipt screen time', 0, 600),
      auto_close_batch_at: f.autoClose ? f.autoCloseAt : '',
      default_tab: f.defaultTab as TerminalSettings['default_tab'],
      show_history: f.showHistory,
      button_font: f.font as TerminalSettings['button_font'],
      cashier_price_drop_pct: int(f.priceDrop, 'The cashier price drop limit', 0, 100),
      click_sound: f.clickSound,
      prices_on_buttons: f.pricesOnButtons,
      out_of_area_margin_mi: margin,
      name_on_paid_counter: f.nameOnPaid,
      card_tips: f.cardTips,
      card_tip_percents: percents,
      employee_meal_pct: int(f.meal, 'The employee meal discount', 0, 100)
    };
  }

  async function saveSettings(e: SubmitEvent) {
    e.preventDefault();
    if (!sf || sSaving) return;
    sErr = '';
    let body: TerminalSettings;
    try {
      body = settingsOf(sf);
    } catch (x) {
      sErr = errText(x);
      return;
    }
    sSaving = true;
    try {
      sf = settingsForm(await storeApi<TerminalSettings>('/me/terminal/settings', { method: 'PUT', body: JSON.stringify(body) }));
      toast.success('Terminal settings saved — the terminals get them right away');
    } catch (x) {
      sErr = errText(x);
    } finally {
      sSaving = false;
    }
  }

  onMount(() => {
    load().catch((e) => toast.error(e));
    storeApi<Partial<TerminalSettings>>('/me/terminal/settings')
      .then((s) => (sf = settingsForm(s)))
      .catch((e) => toast.error(e));
  });
</script>

<svelte:head><title>Staff — quicc</title></svelte:head>

<PageHeader title="Staff" subtitle="Who logs in to the counter terminal, and how the terminal behaves">
  {#snippet actions()}
    <Button variant="primary" icon="plus" onclick={add}>Add a person</Button>
  {/snippet}
</PageHeader>

<Card title="People" padded={false}>
  {#if staff === null}
    <div class="px-5 pb-5"><Skeleton lines={3} /></div>
  {:else if staff.length}
    <div class="overflow-x-auto">
      <table class="tbl stack min-w-[640px]">
        <thead><tr><th>Name</th><th>Role</th><th>Refund limit</th><th>Discount limit</th><th><span class="sr-only">Actions</span></th></tr></thead>
        <tbody>
          {#each staff as p (p.id)}
            <tr class={p.active ? '' : 'opacity-55'} data-testid="staff-row">
              <td class="font-semibold">
                <span>{p.first_name} {p.last_name}{#if !p.active}<Badge tone="mute" class="ml-2">Inactive</Badge>{/if}</span>
              </td>
              <td data-label="Role">{roleLabel(p.role)}</td>
              <td data-label="Refund limit">
                {limitText(p.effective_limits.refund_cents, true)}{#if p.limits.refund_cents != null}<span class="text-text3"> (own)</span>{/if}
              </td>
              <td data-label="Discount limit">
                {limitText(p.effective_limits.discount_pct, false)}{#if p.limits.discount_pct != null}<span class="text-text3"> (own)</span>{/if}
              </td>
              <td class="text-right stack-actions">
                <div class="flex flex-wrap justify-end gap-2">
                  {#if p.active}
                    <Button size="sm" icon="pencil" onclick={() => edit(p)}>Edit</Button>
                    <Button size="sm" onclick={() => deactivate(p)}>Deactivate</Button>
                  {:else}
                    <Button size="sm" onclick={() => reactivate(p)}>Reactivate</Button>
                  {/if}
                  <Button size="sm" icon="trash" onclick={() => remove(p)} aria-label="Delete {p.first_name}">Delete</Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <EmptyState icon="users" title="No staff yet" body="Add the people who use the counter terminal. Each one gets a 2–4 digit PIN." />
  {/if}
</Card>

<Dialog
  bind:open
  title={!editing ? 'Add a person' : reactivating ? `Reactivate ${editing.first_name}` : `Edit ${editing.first_name}`}
  description={reactivating ? 'Their old PIN was freed when they were deactivated: give them a new one.' : undefined}
  width={480}
>
  <form id="staff-form" class="flex flex-col gap-3" onsubmit={savePerson}>
    <div class="grid gap-3 grid-cols-2">
      <div><label class="lbl" for="st-first">First name</label><input id="st-first" class="field" bind:value={form.first} maxlength="40" /></div>
      <div><label class="lbl" for="st-last">Last name</label><input id="st-last" class="field" bind:value={form.last} maxlength="40" /></div>
    </div>
    <div>
      <span class="lbl">Role</span>
      <SegmentedControl label="Role" options={ROLES} bind:value={form.role} />
    </div>
    <div>
      <label class="lbl" for="st-pin">{editing && !reactivating ? 'New PIN (blank = keep)' : 'PIN (2–4 digits)'}</label>
      <input
        id="st-pin"
        class="field mono !w-32"
        type="password"
        inputmode="numeric"
        autocomplete="new-password"
        maxlength="4"
        bind:value={form.pin}
        aria-invalid={!!pinErr || undefined}
      />
      {#if pinErr}<p class="field-error" role="alert">{pinErr}</p>{/if}
    </div>
    <div class="grid gap-3 grid-cols-2">
      <div>
        <label class="lbl" for="st-refund">Refund limit ($)</label>
        <input id="st-refund" class="field mono text-right" inputmode="decimal" placeholder="role default" bind:value={form.refund} />
      </div>
      <div>
        <label class="lbl" for="st-discount">Discount limit (%)</label>
        <input id="st-discount" class="field mono text-right" inputmode="numeric" placeholder="role default" bind:value={form.discount} />
      </div>
    </div>
    <p class="help !mt-0">Blank = the role's default: managers are unlimited, cashiers use the terminal settings below, drivers can't refund or discount.</p>
    {#if formErr}<p class="field-error" role="alert">{formErr}</p>{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)}>Cancel</Button>
    <Button type="submit" variant="primary" loading={saving} form="staff-form">{!editing ? 'Add' : reactivating ? 'Reactivate' : 'Save'}</Button>
  {/snippet}
</Dialog>

<form onsubmit={saveSettings}>
  <Card title="Terminal settings" subtitle="How the counter terminals behave. Saved settings reach the terminals right away.">
    {#if !sf}
      <Skeleton lines={4} />
    {:else}
      <div class="flex flex-col gap-4">
        <div class="subcard p-3.5 flex flex-col gap-3">
          <span class="lbl !mb-0">Cashier limits</span>
          <div class="grid gap-3 grid-cols-2 sm:grid-cols-3">
            <div><label class="lbl" for="ts-refund">Refund up to ($)</label><input id="ts-refund" class="field mono text-right" inputmode="decimal" bind:value={sf.refund} /></div>
            <div><label class="lbl" for="ts-discount">Discount up to (%)</label><input id="ts-discount" class="field mono text-right" inputmode="numeric" bind:value={sf.discount} /></div>
            <div><label class="lbl" for="ts-drop">Lower a price by up to (%)</label><input id="ts-drop" class="field mono text-right" inputmode="numeric" bind:value={sf.priceDrop} /></div>
          </div>
          <p class="help !mt-0">Above these a manager's PIN is needed. A cashier may always raise an item's price.</p>
        </div>

        <div class="subcard p-3.5 flex flex-col gap-3">
          <span class="lbl !mb-0">Screen</span>
          <div class="grid gap-3 grid-cols-2 sm:grid-cols-3">
            <div>
              <label class="lbl" for="ts-tab">After login, open</label>
              <select id="ts-tab" class="field" bind:value={sf.defaultTab}>
                {#each TABS as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
              </select>
            </div>
            <div>
              <label class="lbl" for="ts-font">Button font</label>
              <select id="ts-font" class="field" bind:value={sf.font}>
                {#each FONTS as f (f.value)}<option value={f.value}>{f.label}</option>{/each}
              </select>
            </div>
            <div><label class="lbl" for="ts-lock">Lock after idle (min)</label><input id="ts-lock" class="field mono text-right" inputmode="numeric" bind:value={sf.lock} /></div>
            <div><label class="lbl" for="ts-done">Receipt screen closes after (s)</label><input id="ts-done" class="field mono text-right" inputmode="numeric" bind:value={sf.receiptDone} /></div>
          </div>
          <div class="flex flex-wrap gap-x-6 gap-y-2">
            <label class="flex items-center gap-2.5"><Switch bind:checked={sf.showHistory} label="Show the History tab" />Show the History tab</label>
            <label class="flex items-center gap-2.5"><Switch bind:checked={sf.pricesOnButtons} label="Prices on menu buttons" />Prices on menu buttons</label>
            <label class="flex items-center gap-2.5"><Switch bind:checked={sf.clickSound} label="Click sound" />Click sound</label>
          </div>
          <p class="help !mt-0">“Receipt screen closes after” 0 = never.</p>
        </div>

        <div class="subcard p-3.5 flex flex-col gap-3">
          <span class="lbl !mb-0">Orders</span>
          <div class="grid gap-3 grid-cols-2 sm:grid-cols-3">
            <div><label class="lbl" for="ts-start">Counter numbers start at</label><input id="ts-start" class="field mono text-right" inputmode="numeric" bind:value={sf.counterStart} /></div>
            <div><label class="lbl" for="ts-meal">Employee meal (% off)</label><input id="ts-meal" class="field mono text-right" inputmode="numeric" bind:value={sf.meal} /></div>
            <div><label class="lbl" for="ts-margin">Out-of-area margin (mi)</label><input id="ts-margin" class="field mono text-right" inputmode="decimal" bind:value={sf.margin} /></div>
          </div>
          <label class="flex items-center gap-2.5"><Switch bind:checked={sf.nameOnPaid} label="Ask a name for a paid counter order" />Ask a name for a paid counter order</label>
          <p class="help !mt-0">The margin: a delivery up to this far past the delivery area is taken without a warning.</p>
        </div>

        <div class="subcard p-3.5 flex flex-col gap-3">
          <span class="lbl !mb-0">Card tips at the counter</span>
          <div class="flex flex-wrap items-end gap-3">
            <label class="flex items-center gap-2.5 pb-2"><Switch bind:checked={sf.cardTips} label="Ask for a tip on the card reader" />Ask for a tip on the card reader</label>
            <div>
              <label class="lbl" for="ts-tips">Choices (%)</label>
              <input id="ts-tips" class="field mono !w-36" bind:value={sf.tipPercents} disabled={!sf.cardTips} />
            </div>
          </div>
        </div>

        <div class="subcard p-3.5 flex flex-col gap-3">
          <span class="lbl !mb-0">Close batch by itself</span>
          <div class="flex flex-wrap items-end gap-3">
            <label class="flex items-center gap-2.5 pb-2"><Switch bind:checked={sf.autoClose} label="Close the batch by itself" />Close a batch nobody closed</label>
            {#if sf.autoClose}
              <div><label class="lbl" for="ts-close">At (store time)</label><input id="ts-close" class="field mono !w-32" type="time" bind:value={sf.autoCloseAt} /></div>
            {/if}
          </div>
        </div>

        {#if sErr}<p class="field-error" role="alert">{sErr}</p>{/if}
        <div><Button type="submit" variant="primary" loading={sSaving}>Save terminal settings</Button></div>
      </div>
    {/if}
  </Card>
</form>
