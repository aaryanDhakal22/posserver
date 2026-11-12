<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { beforeNavigate, goto } from '$app/navigation';
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
    { value: 'driver', label: 'Driver', tone: 'mute' }
  ];
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
  const SAVED_TO: Record<PiLink, string> = {
    online: 'sent to the terminals',
    offline: 'the terminals get them when the Pi is back online',
    none: 'the terminals get them once a Pi is linked',
    unknown: 'sent to the store’s Pi'
  };

  // --- PINs ----------------------------------------------------------------------
  const PIN_RE = /^\d{2,4}$/;
  const digits = (v: string) => v.replace(/\D/g, '').slice(0, 4);
  const pinText = (e: unknown) =>
    e instanceof ApiError && e.code === 'pin_taken'
      ? 'Someone else here already uses that PIN. Pick a different one.'
      : e instanceof ApiError && e.code === 'bad_pin'
        ? 'A PIN is 2 to 4 digits.'
        : e instanceof ApiError && e.code === 'pin_required'
          ? 'Give them a new PIN.'
          : '';
  /** The PIN pair's problem, or '' when it's fine. */
  function checkPin(pin: string, confirm: string): string {
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
      default:
        return `Takes orders and payments. Without a manager: refunds up to ${dollars(d.refund ?? 0)}, discounts up to ${d.discount}% (Cashier limits below).`;
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
    if (!editing && (pinErr = checkPin(form.pin, form.pin2))) {
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
      const body: Record<string, unknown> = { first_name: name, last_name: form.last.trim(), role: form.role, limits };
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
    if ((newPinErr = checkPin(newPin, newPin2))) return;
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

  // --- terminal settings -------------------------------------------------------
  type SettingsForm = {
    refund: string; discount: string; lock: string; counterStart: string; receiptDone: string;
    autoClose: boolean; autoCloseAt: string; defaultTab: string; showHistory: boolean; font: string;
    priceDrop: string; clickSound: boolean; pricesOnButtons: boolean; margin: string; nameOnPaid: boolean; toppingMax: string;
    cardTips: boolean; tipPercents: string; meal: string; payout: string; pickupMinutes: string; areaCodes: string; drawerStart: string;
    wasteReasons: string; voidReasons: string;
  };
  type TextKey = { [K in keyof SettingsForm]: SettingsForm[K] extends string ? K : never }[keyof SettingsForm];
  let saved = $state<TerminalSettings | null>(null);
  let sf = $state<SettingsForm | null>(null);
  let savedForm = $state('');
  let sErr = $state('');
  let sErrField = $state<TextKey | ''>('');
  let sSaving = $state(false);
  const dirty = $derived(!!sf && JSON.stringify(sf) !== savedForm);

  function settingsForm(raw: Partial<TerminalSettings>): SettingsForm {
    const s = { ...TERMINAL_DEFAULTS, ...raw };
    return {
      refund: centsInput(s.cashier_refund_cents), discount: String(s.cashier_discount_pct), lock: String(s.auto_lock_minutes),
      counterStart: String(s.counter_start), receiptDone: String(s.receipt_auto_done_seconds),
      autoClose: s.auto_close_batch_at !== '', autoCloseAt: s.auto_close_batch_at || '04:00',
      defaultTab: s.default_tab, showHistory: s.show_history, font: s.button_font, priceDrop: String(s.cashier_price_drop_pct),
      clickSound: s.click_sound, pricesOnButtons: s.prices_on_buttons, margin: String(s.out_of_area_margin_mi),
      toppingMax: String(s.topping_max_count),
      nameOnPaid: s.name_on_paid_counter, cardTips: s.card_tips, tipPercents: (s.card_tip_percents ?? []).join(', '),
      meal: String(s.employee_meal_pct), payout: centsInput(s.cashier_payout_cents),
      pickupMinutes: String(s.default_pickup_minutes), areaCodes: (s.area_codes ?? []).join(', '),
      drawerStart: centsInput(s.drawer_start_cents),
      wasteReasons: (s.waste_reasons ?? []).join('\n'),
      voidReasons: (s.void_reasons ?? []).join('\n')
    };
  }
  function useSaved(s: TerminalSettings) {
    saved = { ...TERMINAL_DEFAULTS, ...s };
    sf = settingsForm(s);
    savedForm = JSON.stringify(sf);
  }

  /** A settings problem tied to one field (focused + marked in the form). */
  class FieldError extends Error {
    field: TextKey;
    constructor(field: TextKey, message: string) {
      super(message);
      this.field = field;
    }
  }
  function int(f: SettingsForm, key: TextKey, what: string, lo: number, hi: number): number {
    const v = f[key];
    const n = Number(v.trim());
    if (!v.trim() || !Number.isInteger(n) || n < lo || n > hi) throw new FieldError(key, `${what}: a whole number from ${lo} to ${hi}.`);
    return n;
  }
  function money(f: SettingsForm, key: TextKey, what: string, hiCents: number): number {
    const c = toCents(f[key]);
    if (c === null || c < 0 || c > hiCents)
      throw new FieldError(key, `${what}: a dollar amount from $0 to $${(hiCents / 100).toLocaleString('en-US')}.`);
    return c;
  }

  function settingsOf(f: SettingsForm): TerminalSettings {
    const margin = Number(f.margin.trim());
    if (!f.margin.trim() || !Number.isFinite(margin) || margin < 0 || margin > 10 || Math.round(margin * 10) !== margin * 10)
      throw new FieldError('margin', 'Past the delivery area: 0 to 10 miles (one decimal, like 1.5).');
    const tips = f.tipPercents.split(',').map((x) => x.trim()).filter(Boolean);
    if (tips.length < 1 || tips.length > 6 || tips.some((x) => !/^\d+$/.test(x) || Number(x) > 100))
      throw new FieldError('tipPercents', 'Tip choices: 1 to 6 percentages from 0 to 100, separated by commas.');
    if (f.autoClose && !/^([01]\d|2[0-3]):[0-5]\d$/.test(f.autoCloseAt)) throw new FieldError('autoCloseAt', 'Close the batch at: a time like 04:00.');
    const codes = [...new Set(f.areaCodes.split(',').map((x) => x.trim()).filter(Boolean))];
    if (codes.length < 1 || codes.length > 20 || codes.some((c) => !/^[2-9]\d\d$/.test(c)))
      throw new FieldError('areaCodes', 'Area codes: 1 to 20 three-digit codes, separated by commas.');
    const reasons = (text: string) => {
      const seen = new Set<string>();
      return text
        .split('\n')
        .map((x) => x.trim().replace(/\s+/g, ' '))
        .filter((x) => x && !seen.has(x.toLowerCase()) && seen.add(x.toLowerCase()));
    };
    const waste = reasons(f.wasteReasons);
    if (waste.length < 1 || waste.length > 20 || waste.some((x) => x.length > 40))
      throw new FieldError('wasteReasons', 'Waste reasons: 1 to 20 reasons, one per line, up to 40 characters each.');
    const voids = reasons(f.voidReasons);
    if (voids.length < 1 || voids.length > 20 || voids.some((x) => x.length > 40))
      throw new FieldError('voidReasons', 'Void reasons: 1 to 20 reasons, one per line, up to 40 characters each.');
    return {
      cashier_refund_cents: money(f, 'refund', 'Refunds up to', 1_000_000),
      cashier_discount_pct: int(f, 'discount', 'Discounts up to', 0, 100),
      auto_lock_minutes: int(f, 'lock', 'Lock the terminal after', 1, 240),
      counter_start: int(f, 'counterStart', 'Counter numbers start at', 1, 999_999),
      receipt_auto_done_seconds: int(f, 'receiptDone', 'Close the receipt screen after', 0, 600),
      auto_close_batch_at: f.autoClose ? f.autoCloseAt : '',
      default_tab: f.defaultTab as TerminalSettings['default_tab'],
      show_history: f.showHistory,
      button_font: f.font as TerminalSettings['button_font'],
      cashier_price_drop_pct: int(f, 'priceDrop', 'Lower a price by up to', 0, 100),
      click_sound: f.clickSound,
      prices_on_buttons: f.pricesOnButtons,
      out_of_area_margin_mi: margin,
      name_on_paid_counter: f.nameOnPaid,
      card_tips: f.cardTips,
      card_tip_percents: tips.map(Number),
      employee_meal_pct: int(f, 'meal', 'Employee meal', 0, 100),
      topping_max_count: int(f, 'toppingMax', 'Most of one topping', 1, 10),
      cashier_payout_cents: money(f, 'payout', 'Pay out of the drawer up to', 100_000),
      drawer_start_cents: money(f, 'drawerStart', 'Cash left in each drawer at opening', 1_000_000),
      default_pickup_minutes: int(f, 'pickupMinutes', 'Pickups are ready in', 0, 240),
      area_codes: codes,
      waste_reasons: waste,
      void_reasons: voids
    };
  }

  async function saveSettings(e: SubmitEvent) {
    e.preventDefault();
    if (!sf || sSaving) return;
    sErr = sErrField = '';
    let body: TerminalSettings;
    try {
      body = settingsOf(sf);
    } catch (x) {
      sErr = errText(x);
      if (x instanceof FieldError) {
        sErrField = x.field;
        await tick();
        document.getElementById('ts-' + x.field)?.focus();
      }
      return;
    }
    sSaving = true;
    try {
      useSaved(await storeApi<TerminalSettings>('/me/terminal/settings', { method: 'PUT', body: JSON.stringify(body) }));
      toast.success(`Terminal settings saved — ${SAVED_TO[pi]}`);
      if (staff) await load(); // the cashiers' limits follow the settings
    } catch (x) {
      sErr = errText(x);
    } finally {
      sSaving = false;
    }
  }

  function undoSettings() {
    sf = JSON.parse(savedForm);
    sErr = sErrField = '';
  }
  async function fillDefaults() {
    const ok = await confirmDialog({
      title: 'Fill in the defaults?',
      body: 'Every terminal setting below goes back to the QuiccPOS default. Nothing is saved until you press Save.',
      confirmLabel: 'Fill in defaults'
    });
    if (!ok || !sf) return;
    sf = settingsForm(TERMINAL_DEFAULTS);
    sErr = sErrField = '';
  }

  // Leaving with unsaved settings asks first.
  let leaving = false;
  beforeNavigate((nav) => {
    if (!dirty || leaving) return;
    if (nav.type === 'leave') {
      nav.cancel(); // the browser asks
      return;
    }
    nav.cancel();
    const to = nav.to?.url;
    confirmDialog({
      title: 'Leave without saving?',
      body: 'Your terminal settings changes aren’t saved yet.',
      confirmLabel: 'Leave',
      cancelLabel: 'Stay',
      danger: true
    }).then((ok) => {
      if (ok && to) {
        leaving = true;
        goto(to);
      }
    });
  });

  onMount(() => {
    load().catch((e) => toast.error(e));
    storeApi<TerminalSettings>('/me/terminal/settings')
      .then(useSaved)
      .catch((e) => toast.error(e));
    loadPiStatus().then((r) => (pi = r.pi));
  });

  // The settings' number fields: label, unit, help (with the default).
  type NumField = { key: TextKey; label: string; unit: string; prefix?: boolean; help: string; decimal?: boolean };
  const D = TERMINAL_DEFAULTS;
  const CASHIER: NumField[] = [
    { key: 'refund', label: 'Refunds up to', unit: '$', prefix: true, decimal: true,
      help: `Cash given back on one order, in all, without a manager (card refunds and older days’ orders always need one). 0 = every refund needs a manager. Default ${dollars(D.cashier_refund_cents)}.` },
    { key: 'discount', label: 'Discounts up to', unit: '%', help: `A discount the cashier types on an order. Default ${D.cashier_discount_pct}%.` },
    { key: 'priceDrop', label: 'Lower an item’s price by up to', unit: '%', help: `Raising a price is always allowed. Default ${D.cashier_price_drop_pct}%.` },
    { key: 'payout', label: 'Pay out of the drawer up to', unit: '$', prefix: true, decimal: true,
      help: `Paying cash in is always allowed. Default ${dollars(D.cashier_payout_cents)}.` },
    { key: 'drawerStart', label: 'Cash left in each drawer at opening', unit: '$', prefix: true, decimal: true,
      help: `Every terminal's drawer starts the day with this; the drawer report's Expected in Drawer counts from it. Default ${dollars(D.drawer_start_cents)}.` }
  ];
  const SCREEN: NumField[] = [
    { key: 'lock', label: 'Lock the terminal after', unit: 'min', help: `Idle this long, the terminal goes back to the PIN pad. Default ${D.auto_lock_minutes} min.` },
    { key: 'receiptDone', label: 'Close the receipt screen after', unit: 'sec', help: `0 = it stays until Done is pressed. Default ${D.receipt_auto_done_seconds} sec.` }
  ];
  const ORDERS: NumField[] = [
    { key: 'meal', label: 'Employee meal', unit: '% off', help: `The Employee meal button. A cashier can give it without a manager. Default ${D.employee_meal_pct}%.` },
    { key: 'pickupMinutes', label: 'Pickups are ready in', unit: 'min', help: `The time a pickup is quoted. Default ${D.default_pickup_minutes} min.` },
    { key: 'counterStart', label: 'Counter numbers start at', unit: '#', help: `Counter orders count up from here, again after Close batch. Default ${D.counter_start}.` },
    { key: 'toppingMax', label: 'Most of one topping', unit: '×', help: `Tapping a topping again adds one more (2×, 3×). 1 = no doubles. Default ${D.topping_max_count}.` }
  ];
  const DELIVERY: NumField[] = [
    { key: 'margin', label: 'Deliver past the delivery area by up to', unit: 'mi', decimal: true,
      help: `Up to this far past your delivery area the cashier just sees a warning; farther needs a manager’s PIN. Default ${D.out_of_area_margin_mi} mi.` }
  ];
</script>

<svelte:head><title>Staff — quicc</title></svelte:head>

{#snippet numField(f: NumField)}
  {@const bad = sErrField === f.key}
  <div class="nf">
    <label class="lbl" for="ts-{f.key}">{f.label}<span class="sr-only"> ({f.unit})</span></label>
    <div class="unit-field" class:prefix={f.prefix}>
      <input
        id="ts-{f.key}"
        class="field mono"
        inputmode={f.decimal ? 'decimal' : 'numeric'}
        autocomplete="off"
        bind:value={() => sf![f.key], (v) => (sf![f.key] = v)}
        aria-invalid={bad || undefined}
        aria-describedby="ts-{f.key}-help"
      />
      <span class="unit" aria-hidden="true">{f.unit}</span>
    </div>
    <p class="help" id="ts-{f.key}-help">{f.help}</p>
  </div>
{/snippet}

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

<PageHeader title="Staff" subtitle="The people who log in to the counter terminal with a PIN, and how the terminal behaves.">
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
        Managers can do everything; their PIN approves what a cashier can’t. Cashiers work within the Cashier limits below. Drivers get the
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

  <form onsubmit={saveSettings} novalidate>
    <Card title="Terminal settings" subtitle="How the counter terminals behave. Every store starts with the defaults shown under each field.">
      {#snippet actions()}
        <Button size="sm" variant="ghost" icon="refresh" onclick={fillDefaults} disabled={!sf}>Fill in defaults</Button>
      {/snippet}
      {#if !sf}
        <Skeleton lines={4} />
      {:else}
        <div class="flex flex-col gap-4">
          <section class="subcard sec" aria-labelledby="ts-sec-1">
            <h3 class="sec-title" id="ts-sec-1">Cashier limits</h3>
            <p class="help !mt-0">What a cashier can do without a manager’s PIN. Managers have no limits.</p>
            <div class="grid-f">
              {#each CASHIER as f (f.key)}{@render numField(f)}{/each}
            </div>
          </section>

          <section class="subcard sec" aria-labelledby="ts-sec-2">
            <h3 class="sec-title" id="ts-sec-2">Login and screen</h3>
            <div class="grid-f">
              {@render numField(SCREEN[0])}
              <div class="nf">
                <label class="lbl" for="ts-tab">After login, open</label>
                <select id="ts-tab" class="field" bind:value={sf.defaultTab}>
                  {#each TABS as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
                </select>
                <p class="help">The tab a cashier lands on. Default Info.</p>
              </div>
              {@render numField(SCREEN[1])}
              <div class="nf">
                <label class="lbl" for="ts-font">Menu button font</label>
                <select id="ts-font" class="field" bind:value={sf.font}>
                  {#each FONTS as f (f.value)}<option value={f.value}>{f.label}</option>{/each}
                </select>
                <p class="help">Default Barlow.</p>
              </div>
            </div>
            <div class="switches">
              <label class="sw-row"><Switch bind:checked={sf.showHistory} label="Show the History tab" />Show the History tab</label>
              <label class="sw-row"><Switch bind:checked={sf.pricesOnButtons} label="Prices on menu buttons" />Prices on menu buttons</label>
              <label class="sw-row"><Switch bind:checked={sf.clickSound} label="Click sound" />Click sound on buttons</label>
            </div>
          </section>

          <section class="subcard sec" aria-labelledby="ts-sec-3">
            <h3 class="sec-title" id="ts-sec-3">Orders</h3>
            <div class="grid-f">
              {#each ORDERS as f (f.key)}{@render numField(f)}{/each}
              <div class="nf wide">
                <label class="lbl" for="ts-areaCodes">Local area codes</label>
                <input
                  id="ts-areaCodes"
                  class="field mono"
                  inputmode="numeric"
                  autocomplete="off"
                  bind:value={sf.areaCodes}
                  aria-invalid={sErrField === 'areaCodes' || undefined}
                />
                <p class="help">Separated by commas. The terminal uses them to fill in phone numbers. Default {D.area_codes.join(', ')}.</p>
              </div>
              <div class="nf wide">
                <label class="lbl" for="ts-wasteReasons">Waste reasons</label>
                <textarea
                  id="ts-wasteReasons"
                  class="field"
                  rows="4"
                  autocomplete="off"
                  bind:value={sf.wasteReasons}
                  aria-invalid={sErrField === 'wasteReasons' || undefined}
                ></textarea>
                <p class="help">One per line. A cashier picks one to void an order that was already made (it goes on the report; nothing prints). Default: {D.waste_reasons.join(', ')}.</p>
              </div>
              <div class="nf wide">
                <label class="lbl" for="ts-voidReasons">Void reasons</label>
                <textarea
                  id="ts-voidReasons"
                  class="field"
                  rows="4"
                  autocomplete="off"
                  bind:value={sf.voidReasons}
                  aria-invalid={sErrField === 'voidReasons' || undefined}
                ></textarea>
                <p class="help">One per line. A cashier picks one to void a sent order (the kitchen gets a void ticket); nobody types a reason, so keep an "Other". Default: {D.void_reasons.join(', ')}.</p>
              </div>
            </div>
            <div class="switches">
              <label class="sw-row"
                ><Switch bind:checked={sf.nameOnPaid} label="Ask a name for a paid counter order" />Ask a name for a paid counter order</label
              >
            </div>
          </section>

          <section class="subcard sec" aria-labelledby="ts-sec-4">
            <h3 class="sec-title" id="ts-sec-4">Deliveries and drivers</h3>
            <div class="grid-f">
              {#each DELIVERY as f (f.key)}{@render numField(f)}{/each}
            </div>
            <p class="help !mt-0">A delivery nobody took shows in the drivers’ Dispatch list once it is past the time promised to the customer; the driver who claims it gets it as delivered.</p>
            <p class="help !mt-0">The delivery fee (store share + driver share) is on Store › Details, with the website’s prices.</p>
          </section>

          <section class="subcard sec" aria-labelledby="ts-sec-5">
            <h3 class="sec-title" id="ts-sec-5">Card tips at the counter</h3>
            <div class="flex flex-wrap items-start gap-x-6 gap-y-3">
              <label class="sw-row"><Switch bind:checked={sf.cardTips} label="Ask for a tip on the card reader" />Ask for a tip on the card reader</label>
              <div class="nf">
                <label class="lbl" for="ts-tipPercents">Tip choices<span class="sr-only"> (%)</span></label>
                <div class="unit-field">
                  <input
                    id="ts-tipPercents"
                    class="field mono !w-48"
                    autocomplete="off"
                    bind:value={sf.tipPercents}
                    disabled={!sf.cardTips}
                    aria-invalid={sErrField === 'tipPercents' || undefined}
                  />
                </div>
                <p class="help">Percentages, separated by commas. Default {D.card_tip_percents.join(', ')}.</p>
              </div>
            </div>
          </section>

          <section class="subcard sec" aria-labelledby="ts-sec-6">
            <h3 class="sec-title" id="ts-sec-6">Close the batch by itself</h3>
            <div class="flex flex-wrap items-start gap-x-6 gap-y-3">
              <label class="sw-row"><Switch bind:checked={sf.autoClose} label="Close the batch by itself" />Close a batch nobody closed</label>
              {#if sf.autoClose}
                <div class="nf">
                  <label class="lbl" for="ts-autoCloseAt">At (store time)</label>
                  <input
                    id="ts-autoCloseAt"
                    class="field mono !w-36"
                    type="time"
                    bind:value={sf.autoCloseAt}
                    aria-invalid={sErrField === 'autoCloseAt' || undefined}
                  />
                </div>
              {/if}
            </div>
            <p class="help !mt-0">If nobody pressed Close batch by then, the Pi closes it and prints the driver reports. Default on, at 4:00 am.</p>
          </section>
        </div>
      {/if}
    </Card>

    {#if sf}
      <div class="savebar" class:is-dirty={dirty} data-testid="settings-savebar">
        <div class="min-w-0 flex-1">
          {#if sErr}
            <p class="field-error !m-0" role="alert">{sErr}</p>
          {:else if dirty}
            <span class="text-warn font-medium text-[13px]">Unsaved changes</span>
          {:else}
            <span class="text-muted text-[13px]">All changes saved</span>
          {/if}
        </div>
        {#if dirty}<Button variant="ghost" onclick={undoSettings}>Undo changes</Button>{/if}
        <Button type="submit" variant="primary" loading={sSaving}>Save terminal settings</Button>
      </div>
    {/if}
  </form>
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
      <SegmentedControl label="Role" options={ROLES} bind:value={form.role} />
      <p class="help" data-testid="role-blurb">{roleBlurb(form.role)}</p>
    </div>
    {#if !editing}
      {@render pinFields(form.pin, (v) => (form.pin = v), form.pin2, (v) => (form.pin2 = v), pinErr, 'st')}
    {:else}
      <p class="help !mt-0">To give {editing.first_name} a new PIN, use the PIN button in the list.</p>
    {/if}
    <div class="subcard p-3 flex flex-col gap-3">
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
  .sec {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 14px;
    min-width: 0;
  }
  .sec-title {
    font-size: 13px;
    font-weight: 700;
    color: var(--text);
    margin: 0;
  }
  .grid-f {
    display: grid;
    gap: 14px 16px;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 220px), 1fr));
  }
  .nf {
    min-width: 0;
  }
  .nf.wide {
    grid-column: span 2;
  }
  @media (max-width: 520px) {
    .nf.wide {
      grid-column: auto;
    }
  }
  .nf .help {
    margin-top: 4px;
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
  .switches {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 24px;
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
  /* At the end of the form; while something is unsaved it follows the screen's bottom edge. */
  .savebar {
    position: static;
    z-index: 5;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 12px;
    padding: 10px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 14px;
    box-shadow: var(--shadow);
  }
  .savebar.is-dirty {
    position: sticky;
    bottom: 12px;
    border-color: var(--warn);
  }
</style>
