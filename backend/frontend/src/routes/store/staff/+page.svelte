<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
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
    type StaffRole,
    type TerminalSettings
  } from '$lib/api';
  import { loadPiStatus, type PiLink } from '$lib/store/pi';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge, { type Tone } from '$lib/ui/Badge.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import MenuButton from '$lib/ui/MenuButton.svelte';

  // Store › Staff: the people who log in to the counter terminal with a PIN
  // (cashier / manager / driver), their own limits, and the store's terminal
  // settings (contract/main.md §2.5zb). A PIN is never shown back. Every save
  // here bumps the store's terminal version and main pushes terminal.changed
  // to the Pi, which pulls the new terminal-config.
  const ROLES: { value: StaffRole; label: string; tone: Tone }[] = [
    { value: 'cashier', label: 'Cashier', tone: 'info' },
    { value: 'manager', label: 'Manager', tone: 'brand' },
    { value: 'driver', label: 'Driver', tone: 'mute' },
    { value: 'callcenter', label: 'Call center', tone: 'ok' }
  ];
  // A role change to Call center needs a new 4-digit PIN with it, and the edit
  // dialog has no PIN boxes: an existing person keeps the three store roles
  // (add a call-center agent as a new person).
  const rolesFor = (p: Staff | null) => (p && p.role !== 'callcenter' ? ROLES.filter((r) => r.value !== 'callcenter') : ROLES);
  const roleOf = (r: string) => ROLES.find((x) => x.value === r) ?? { value: r, label: r, tone: 'mute' as Tone };
  const TABS = [
    { value: 'info', label: 'Info' },
    { value: 'counter', label: 'Counter' },
    { value: 'takeout', label: 'Takeout' },
    { value: 'delivery', label: 'Delivery' },
    { value: 'pickup', label: 'Pickup list' },
    { value: 'history', label: 'History' },
    { value: 'dispatch', label: 'Dispatch' }
  ];
  const FONTS = [
    { value: 'barlow', label: 'Barlow' },
    { value: 'barlow_condensed', label: 'Barlow Condensed' },
    { value: 'barlow_semi_condensed', label: 'Barlow Semi Condensed' },
    { value: 'system', label: 'System (Roboto)' },
    { value: 'fira_sans_condensed', label: 'Fira Sans Condensed' },
    { value: 'ibm_plex_sans_condensed', label: 'IBM Plex Sans Condensed' },
    { value: 'atkinson_hyperlegible', label: 'Atkinson Hyperlegible' }
  ];

  let staff = $state<Staff[] | null>(null);
  let pi = $state<PiLink>('unknown');
  const activeCount = $derived(staff?.filter((p) => p.active).length ?? 0);
  const inactiveCount = $derived((staff?.length ?? 0) - activeCount);

  async function load() {
    staff = await storeApi<Staff[]>('/me/staff');
  }

  /** Where saved changes go (the Pi pulls them; offline = when it's back). */
  const PI_TEXT: Record<PiLink, { tone: Tone; badge: string; text: string }> = {
    online: { tone: 'ok', badge: 'Pi online', text: 'Changes here reach the terminals right away.' },
    offline: { tone: 'warn', badge: 'Pi offline', text: 'Changes are saved here and reach the terminals when the Pi is back online.' },
    none: { tone: 'mute', badge: 'No Pi linked', text: 'Changes are saved here; the terminals get them once the store’s Pi is set up.' },
    unknown: { tone: 'mute', badge: 'Checking the Pi…', text: 'Changes here are sent to the store’s Pi.' }
  };
  // Store › Settings pages for the terminal settings (they moved there, 2026-11-13).
  const SETTINGS_LINKS = [
    { slug: 'cashier', label: 'Cashier limits' },
    { slug: 'counter', label: 'Counter orders' },
    { slug: 'screen', label: 'Login and screen' },
    { slug: 'card', label: 'Card payments' },
    { slug: 'voids', label: 'Void and waste reasons' },
    { slug: 'end-of-day', label: 'End of day' },
    { slug: 'callcenter', label: 'Call center' }
  ];

  // --- PINs ----------------------------------------------------------------------
  const PIN_RE = /^\d{2,4}$/;
  const digits = (v: string) => v.replace(/\D/g, '').slice(0, 4);
  const pinText = (e: unknown) =>
    e instanceof ApiError && e.code === 'pin_taken'
      ? 'Someone else here already uses that PIN. Pick a different one.'
      : e instanceof ApiError && e.code === 'bad_pin'
        ? 'A PIN is 2 to 4 digits (exactly 4 for a manager or a call-center agent).'
        : e instanceof ApiError && e.code === 'pin_required'
          ? 'Give them a new PIN.'
          : '';
  /** The PIN pair's problem, or '' when it's fine. */
  function checkPin(pin: string, confirm: string, role = ''): string {
    if (role === 'callcenter' && !/^\d{4}$/.test(pin)) return 'A call-center agent’s PIN is exactly 4 digits.';
    if (role === 'manager' && !/^\d{4}$/.test(pin)) return 'A manager’s PIN is exactly 4 digits.';
    if (!PIN_RE.test(pin)) return 'A PIN is 2 to 4 digits.';
    if (pin !== confirm) return 'The two PINs don’t match.';
    return '';
  }

  // --- the person dialog (add / edit) ----------------------------------------------
  type Form = { first: string; last: string; role: string; pin: string; pin2: string; refund: string; discount: string };
  let open = $state(false);
  let editing = $state<Staff | null>(null);
  let form = $state<Form>(blank());
  let formErr = $state('');
  let pinErr = $state('');
  let saving = $state(false);
  let ownLimits = $state(false);

  function blank(): Form {
    return { first: '', last: '', role: 'cashier', pin: '', pin2: '', refund: '', discount: '' };
  }

  function add() {
    editing = null;
    form = blank();
    ownLimits = false;
    formErr = pinErr = '';
    open = true;
  }
  function edit(p: Staff) {
    editing = p;
    form = {
      first: p.first_name,
      last: p.last_name,
      role: p.role,
      pin: '',
      pin2: '',
      refund: p.limits.refund_cents == null ? '' : centsInput(p.limits.refund_cents),
      discount: p.limits.discount_pct == null ? '' : String(p.limits.discount_pct)
    };
    ownLimits = p.limits.refund_cents != null || p.limits.discount_pct != null;
    formErr = pinErr = '';
    open = true;
  }

  /** What a role may do without a manager, under the saved settings. */
  function roleLimits(role: string): { refund: number | null; discount: number | null } {
    const s = saved ?? TERMINAL_DEFAULTS;
    if (role === 'manager') return { refund: null, discount: null };
    if (role === 'cashier') return { refund: s.cashier_refund_cents, discount: s.cashier_discount_pct };
    return { refund: 0, discount: 0 };
  }
  function roleBlurb(role: string): string {
    const d = roleLimits('cashier');
    switch (role) {
      case 'manager':
        return 'Can do everything on the terminal, with no limits. Their PIN approves what a cashier can’t do.';
      case 'driver':
        return 'Gets the driver view: their deliveries, tips and driver report. No refunds or discounts.';
      case 'callcenter':
        return 'Takes phone orders in the call-center app only (their 4-digit PIN doesn’t work on the store’s terminals). No refunds, discounts or price changes; orders show “Call center - Name”.';
      default:
        return `Takes orders and payments. Without a manager: refunds up to ${dollars(d.refund ?? 0)}, discounts up to ${d.discount}% (Settings › Cashier limits).`;
    }
  }

  /** The person's own limits from the form (blank = the role default). */
  function limitsOf(f: Form): StaffLimits {
    const out: StaffLimits = {};
    if (!ownLimits) return out;
    if (f.refund.trim()) {
      const c = toCents(f.refund);
      if (c === null || c < 0 || c > 1_000_000) throw new Error(`Refunds up to: “${f.refund}” isn’t a dollar amount from $0 to $10,000.`);
      out.refund_cents = c;
    }
    if (f.discount.trim()) {
      const d = Number(f.discount);
      if (!Number.isInteger(d) || d < 0 || d > 100) throw new Error('Discounts up to: a whole % from 0 to 100.');
      out.discount_pct = d;
    }
    return out;
  }

  async function savePerson(e: SubmitEvent) {
    e.preventDefault();
    if (saving) return;
    formErr = pinErr = '';
    if (!form.first.trim()) {
      formErr = 'Give their first name.';
      document.getElementById('st-first')?.focus();
      return;
    }
    if (!editing && (pinErr = checkPin(form.pin, form.pin2, form.role))) {
      document.getElementById('st-pin')?.focus();
      return;
    }
    let limits: StaffLimits;
    try {
      limits = limitsOf(form);
    } catch (x) {
      return (formErr = errText(x));
    }
    saving = true;
    const name = form.first.trim();
    try {
      const body: Record<string, unknown> = { first_name: name, last_name: form.last.trim(), role: form.role,
        limits: form.role === 'callcenter' ? {} : limits };
      if (!editing) {
        await storeApi('/me/staff', { method: 'POST', body: JSON.stringify({ ...body, pin: form.pin }) });
        toast.success(`${name} added`);
      } else {
        await storeApi('/me/staff/' + editing.id, { method: 'PATCH', body: JSON.stringify(body) });
        toast.success(`${name} saved`);
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

  // --- the PIN dialog (change PIN / reactivate) -------------------------------------
  let pinOpen = $state(false);
  let pinFor = $state<Staff | null>(null);
  let pinReactivate = $state(false);
  let newPin = $state('');
  let newPin2 = $state('');
  let newPinErr = $state('');
  let pinSaving = $state(false);

  function changePin(p: Staff, reactivate = false) {
    pinFor = p;
    pinReactivate = reactivate;
    newPin = newPin2 = newPinErr = '';
    pinOpen = true;
  }

  async function savePin(e: SubmitEvent) {
    e.preventDefault();
    if (!pinFor || pinSaving) return;
    if ((newPinErr = checkPin(newPin, newPin2, pinFor.role))) return;
    pinSaving = true;
    const p = pinFor;
    try {
      if (pinReactivate) {
        await storeApi('/me/staff/' + p.id, { method: 'PATCH', body: JSON.stringify({ active: true, pin: newPin }) });
        toast.success(`${p.first_name} can log in again with the new PIN`);
      } else {
        await storeApi(`/me/staff/${p.id}/pin`, { method: 'POST', body: JSON.stringify({ pin: newPin }) });
        toast.success(`${p.first_name}’s PIN changed — the old one stops working`);
      }
      pinOpen = false;
      await load();
    } catch (x) {
      newPinErr = pinText(x) || errText(x);
    } finally {
      pinSaving = false;
    }
  }

  async function deactivate(p: Staff) {
    const ok = await confirmDialog({
      title: `Deactivate ${p.first_name}?`,
      body: 'Their PIN stops working on the terminals and is freed for someone else. You can turn them back on later with a new PIN.',
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
      body: 'They are removed for good (the terminal’s action log keeps what they did). Deactivating is usually enough.',
      confirmLabel: 'Delete',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi('/me/staff/' + p.id, { method: 'DELETE' });
      await load();
      toast.success(`${p.first_name} deleted`);
    } catch (x) {
      toast.error(x);
    }
  }

  /** "Refunds up to $0.00 · discounts up to 20%" / "No limits". */
  function limitSummary(p: Staff): string {
    const r = p.effective_limits.refund_cents;
    const d = p.effective_limits.discount_pct;
    if (r === null && d === null) return 'No limits';
    if (r === 0 && d === 0) return 'No refunds or discounts';
    const rt = r === null ? 'any refund' : r === 0 ? 'no refunds' : `refunds up to ${dollars(r)}`;
    const dt = d === null ? 'any discount' : d === 0 ? 'no discounts' : `discounts up to ${d}%`;
    return rt.charAt(0).toUpperCase() + rt.slice(1) + ' · ' + dt;
  }
  const hasOwn = (p: Staff) => p.limits.refund_cents != null || p.limits.discount_pct != null;

  // --- terminal settings (read only here: Store › Settings edits them) --------------
  // The cashier limits a person without their own get, shown in the person dialog.
  let saved = $state<TerminalSettings | null>(null);

  onMount(() => {
    load().catch((e) => toast.error(e));
    storeApi<TerminalSettings>('/me/terminal/settings')
      .then((t) => (saved = { ...TERMINAL_DEFAULTS, ...t }))
      .catch((e) => toast.error(e));
    loadPiStatus().then((r) => (pi = r.pi));
  });

</script>

<svelte:head><title>Staff — quicc</title></svelte:head>

{#snippet pinFields(pin: string, setPin: (v: string) => void, pin2: string, setPin2: (v: string) => void, err: string, idp: string)}
  <div class="grid gap-3 grid-cols-2">
    <div>
      <label class="lbl" for="{idp}-pin">PIN</label>
      <input
        id="{idp}-pin"
        class="field mono pin"
        type="password"
        inputmode="numeric"
        autocomplete="new-password"
        maxlength="4"
        value={pin}
        oninput={(e) => {
          const v = digits(e.currentTarget.value);
          e.currentTarget.value = v;
          setPin(v);
        }}
        aria-invalid={!!err || undefined}
        aria-describedby="{idp}-pin-help"
      />
    </div>
    <div>
      <label class="lbl" for="{idp}-pin2">PIN again</label>
      <input
        id="{idp}-pin2"
        class="field mono pin"
        type="password"
        inputmode="numeric"
        autocomplete="new-password"
        maxlength="4"
        value={pin2}
        oninput={(e) => {
          const v = digits(e.currentTarget.value);
          e.currentTarget.value = v;
          setPin2(v);
        }}
        aria-invalid={!!err || undefined}
      />
    </div>
  </div>
  {#if err}<p class="field-error !mt-0" role="alert">{err}</p>{/if}
  <p class="help !mt-0" id="{idp}-pin-help">
    2 to 4 digits; 4 is harder to guess. Everyone active at this store needs their own PIN: it’s how the terminal knows who is
    logged in. Nobody can see a PIN once it’s saved, not even you. If someone forgets theirs, give them a new one.
  </p>
{/snippet}

<PageHeader title="Staff" subtitle="The people who log in to the counter terminal with a PIN.">
  {#snippet actions()}
    <Button variant="primary" icon="plus" onclick={add}>Add a person</Button>
  {/snippet}
</PageHeader>

<div class="flex flex-col gap-4 max-w-[980px]">
  <div class="pi-strip" data-testid="pi-status">
    <Badge tone={PI_TEXT[pi].tone} dot>{PI_TEXT[pi].badge}</Badge>
    <span class="text-text2 text-[13px]">{PI_TEXT[pi].text}</span>
  </div>

  <Card
    title="People"
    subtitle={staff === null ? 'Loading…' : staff.length ? `${activeCount} active${inactiveCount ? ` · ${inactiveCount} inactive` : ''}` : 'Nobody yet'}
    padded={false}
  >
    {#if staff === null}
      <div class="px-5 pb-5"><Skeleton lines={3} /></div>
    {:else if staff.length}
      <div class="overflow-x-auto">
        <table class="tbl stack min-w-[640px]">
          <thead><tr><th>Name</th><th>Role</th><th>Without a manager</th><th><span class="sr-only">Actions</span></th></tr></thead>
          <tbody>
            {#each staff as p (p.id)}
              {@const r = roleOf(p.role)}
              <tr class={p.active ? '' : 'inactive'} data-testid="staff-row">
                <td class="font-semibold">
                  <span class="flex items-center gap-2.5">
                    <span class="initials" aria-hidden="true">{(p.first_name[0] ?? '') + (p.last_name[0] ?? '')}</span>
                    <span>{p.first_name} {p.last_name}</span>
                    {#if !p.active}<Badge tone="mute">Inactive</Badge>{/if}
                  </span>
                </td>
                <td data-label="Role"><Badge tone={p.active ? r.tone : 'mute'}>{r.label}</Badge></td>
                <td data-label="Without a manager">
                  <span class="text-text2">{p.active ? limitSummary(p) : 'Can’t log in'}</span>{#if p.active && hasOwn(p)}<Badge tone="warn" class="ml-2" title="Their own limits, not the role’s">own</Badge>{/if}
                </td>
                <td class="text-right stack-actions">
                  <div class="flex flex-wrap justify-end gap-2">
                    {#if p.active}
                      <Button size="sm" icon="pencil" onclick={() => edit(p)} aria-label="Edit {p.first_name}">Edit</Button>
                      <Button size="sm" icon="key" onclick={() => changePin(p)} aria-label="Change {p.first_name}’s PIN">PIN</Button>
                      <MenuButton
                        label="More for {p.first_name}"
                        items={[
                          { label: 'Deactivate', icon: 'x', onclick: () => deactivate(p) },
                          { label: 'Delete', icon: 'trash', danger: true, onclick: () => remove(p) }
                        ]}
                      />
                    {:else}
                      <Button size="sm" onclick={() => changePin(p, true)} aria-label="Reactivate {p.first_name}">Reactivate</Button>
                      <MenuButton label="More for {p.first_name}" items={[{ label: 'Delete', icon: 'trash', danger: true, onclick: () => remove(p) }]} />
                    {/if}
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <p class="help px-5 pb-4 !mt-2">
        Managers can do everything; their PIN approves what a cashier can’t. Cashiers work within Settings › Cashier limits. Drivers get the
        driver view.
      </p>
    {:else}
      <EmptyState
        icon="users"
        title="No staff yet"
        body="Add the people who use the counter terminal. Each one gets their own 2–4 digit PIN to log in."
      >
        {#snippet action()}<Button variant="primary" icon="plus" onclick={add}>Add the first person</Button>{/snippet}
      </EmptyState>
    {/if}
  </Card>

  <Card title="How the terminals behave" subtitle="Limits, the screen, orders, card tips and the end of the day are in Settings.">
    <ul class="links">
      {#each SETTINGS_LINKS as l (l.slug)}
        <li><a href="{base}/store/settings/{l.slug}" class="link-row"><span>{l.label}</span><Icon name="chevron-right" size={16} class="text-muted" /></a></li>
      {/each}
    </ul>
  </Card>
</div>

<Dialog bind:open title={!editing ? 'Add a person' : `Edit ${editing.first_name}`} width={500}>
  <form id="staff-form" class="flex flex-col gap-3.5" onsubmit={savePerson} novalidate>
    <div class="grid gap-3 grid-cols-2">
      <div>
        <label class="lbl" for="st-first">First name</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input id="st-first" class="field" bind:value={form.first} maxlength="40" autocomplete="off" autofocus />
      </div>
      <div><label class="lbl" for="st-last">Last name</label><input id="st-last" class="field" bind:value={form.last} maxlength="40" autocomplete="off" /></div>
    </div>
    <div>
      <span class="lbl">Role</span>
      <SegmentedControl label="Role" options={rolesFor(editing)} bind:value={form.role} />
      <p class="help" data-testid="role-blurb">{roleBlurb(form.role)}</p>
    </div>
    {#if !editing}
      {@render pinFields(form.pin, (v) => (form.pin = v), form.pin2, (v) => (form.pin2 = v), pinErr, 'st')}
    {:else}
      <p class="help !mt-0">To give {editing.first_name} a new PIN, use the PIN button in the list.</p>
    {/if}
    <div class="subcard p-3 flex flex-col gap-3" class:hidden={form.role === 'callcenter'}>
      <label class="sw-row">
        <Switch bind:checked={ownLimits} label="Own limits for this person" />Own limits for this person
      </label>
      {#if ownLimits}
        {@const rd = roleLimits(form.role)}
        <div class="grid gap-3 grid-cols-2">
          <div>
            <label class="lbl" for="st-refund">Refunds up to<span class="sr-only"> ($)</span></label>
            <div class="unit-field prefix">
              <input id="st-refund" class="field mono" inputmode="decimal" placeholder={rd.refund === null ? 'no limit' : centsInput(rd.refund)} bind:value={form.refund} />
              <span class="unit" aria-hidden="true">$</span>
            </div>
          </div>
          <div>
            <label class="lbl" for="st-discount">Discounts up to<span class="sr-only"> (%)</span></label>
            <div class="unit-field">
              <input id="st-discount" class="field mono" inputmode="numeric" placeholder={rd.discount === null ? 'no limit' : String(rd.discount)} bind:value={form.discount} />
              <span class="unit" aria-hidden="true">%</span>
            </div>
          </div>
        </div>
        <p class="help !mt-0">A blank box keeps the role’s limit (shown greyed). Above their limit a manager’s PIN is needed.</p>
      {:else}
        <p class="help !mt-0">Off: the role’s limits apply.</p>
      {/if}
    </div>
    {#if formErr}<p class="field-error" role="alert">{formErr}</p>{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)}>Cancel</Button>
    <Button type="submit" variant="primary" loading={saving} form="staff-form">{!editing ? 'Add person' : 'Save'}</Button>
  {/snippet}
</Dialog>

<Dialog
  bind:open={pinOpen}
  title={pinFor ? (pinReactivate ? `Reactivate ${pinFor.first_name}` : `New PIN for ${pinFor.first_name}`) : 'PIN'}
  description={pinReactivate
    ? 'Their old PIN was freed when they were deactivated: give them a new one.'
    : 'Their old PIN stops working as soon as you save.'}
  width={440}
>
  <form id="pin-form" class="flex flex-col gap-3.5" onsubmit={savePin} novalidate>
    {@render pinFields(newPin, (v) => (newPin = v), newPin2, (v) => (newPin2 = v), newPinErr, 'np')}
  </form>
  {#snippet footer()}
    <Button onclick={() => (pinOpen = false)}>Cancel</Button>
    <Button type="submit" variant="primary" loading={pinSaving} form="pin-form">{pinReactivate ? 'Reactivate' : 'Save PIN'}</Button>
  {/snippet}
</Dialog>

<style>
  .pi-strip {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 10px;
  }
  .unit-field {
    position: relative;
    max-width: 200px; /* a number sits next to its label, not across the screen */
  }
  .unit-field:not(:has(.unit)) {
    max-width: none;
  }
  .unit-field .field {
    padding-right: 52px;
    text-align: right;
  }
  .unit-field.prefix .field {
    padding-right: 12px;
    padding-left: 26px;
  }
  .unit-field .unit {
    position: absolute;
    top: 50%;
    right: 12px;
    transform: translateY(-50%);
    color: var(--muted);
    font-size: 13px;
    pointer-events: none;
  }
  .unit-field.prefix .unit {
    right: auto;
    left: 12px;
  }
  .unit-field:not(:has(.unit)) .field {
    padding-right: 12px;
    text-align: left;
  }
  .sw-row {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    font-size: 14px;
    cursor: pointer;
  }
  .pin {
    letter-spacing: 0.3em;
    font-size: 18px;
  }
  .initials {
    width: 30px;
    height: 30px;
    flex: none;
    border-radius: 999px;
    background: var(--elev);
    border: 1px solid var(--border);
    color: var(--text2);
    font-size: 12px;
    font-weight: 700;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    text-transform: uppercase;
  }
  tr.inactive td {
    color: var(--muted);
  }
  .links {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 2px 16px;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 220px), 1fr));
  }
  .link-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 9px 4px;
    border-bottom: 1px solid var(--border);
    color: var(--text);
    text-decoration: none;
    font-weight: 500;
  }
  .link-row:hover {
    color: var(--brand);
  }
</style>
