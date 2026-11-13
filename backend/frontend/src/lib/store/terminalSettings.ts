// The store's counter-terminal settings (contract/main.md §2.5zb) as Store › Settings edits them.
// PUT /me/terminal/settings takes the whole object, so every settings page loads it all, edits its own
// group of fields and sends the whole thing back. Every save bumps the store's terminal version and
// main pushes terminal.changed to the Pi, which pulls the new terminal-config.
import { centsInput, dollars, toCents, TERMINAL_DEFAULTS, type TerminalSettings } from '$lib/api';
import type { PiLink } from '$lib/store/pi';
import type { Tone } from '$lib/ui/Badge.svelte';

export type SettingsForm = {
  refund: string; discount: string; lock: string; counterStart: string; receiptDone: string;
  autoClose: boolean; autoCloseAt: string; defaultTab: string; showHistory: boolean; font: string;
  priceDrop: string; clickSound: boolean; pricesOnButtons: boolean; margin: string; nameOnPaid: boolean; toppingMax: string;
  cardTips: boolean; tipPercents: string; meal: string; payout: string; pickupMinutes: string; areaCodes: string; drawerStart: string;
  wasteReasons: string; voidReasons: string; phoneTimeout: string; quibaseMin: string;
};
export type FormKey = keyof SettingsForm;
export type TextKey = { [K in FormKey]: SettingsForm[K] extends string ? K : never }[FormKey];

/** The settings pages that edit the terminal settings, one group of fields each. */
export type TerminalGroup = 'cashier' | 'screen' | 'counter' | 'voids' | 'card' | 'endofday';
export const GROUP_KEYS: Record<TerminalGroup, FormKey[]> = {
  cashier: ['refund', 'discount', 'priceDrop', 'payout'],
  screen: ['lock', 'defaultTab', 'receiptDone', 'font', 'showHistory', 'pricesOnButtons', 'clickSound'],
  counter: ['meal', 'pickupMinutes', 'counterStart', 'toppingMax', 'quibaseMin', 'areaCodes', 'nameOnPaid', 'margin'],
  voids: ['wasteReasons', 'voidReasons'],
  card: ['cardTips', 'tipPercents', 'phoneTimeout'],
  endofday: ['autoClose', 'autoCloseAt', 'drawerStart']
};

export const TABS = [
  { value: 'info', label: 'Info' },
  { value: 'counter', label: 'Counter' },
  { value: 'takeout', label: 'Takeout' },
  { value: 'delivery', label: 'Delivery' },
  { value: 'pickup', label: 'Pickup list' },
  { value: 'history', label: 'History' },
  { value: 'dispatch', label: 'Dispatch' }
];
export const FONTS = [
  { value: 'barlow', label: 'Barlow' },
  { value: 'barlow_condensed', label: 'Barlow Condensed' },
  { value: 'barlow_semi_condensed', label: 'Barlow Semi Condensed' },
  { value: 'system', label: 'System (Roboto)' },
  { value: 'fira_sans_condensed', label: 'Fira Sans Condensed' },
  { value: 'ibm_plex_sans_condensed', label: 'IBM Plex Sans Condensed' },
  { value: 'atkinson_hyperlegible', label: 'Atkinson Hyperlegible' }
];

/** Where saved changes go (the Pi pulls them; offline = when it's back). */
export const PI_TEXT: Record<PiLink, { tone: Tone; badge: string; text: string }> = {
  online: { tone: 'ok', badge: 'Pi online', text: 'Changes here reach the terminals right away.' },
  offline: { tone: 'warn', badge: 'Pi offline', text: 'Changes are saved here and reach the terminals when the Pi is back online.' },
  none: { tone: 'mute', badge: 'No Pi linked', text: 'Changes are saved here; the terminals get them once the store’s Pi is set up.' },
  unknown: { tone: 'mute', badge: 'Checking the Pi…', text: 'Changes here are sent to the store’s Pi.' }
};
export const SAVED_TO: Record<PiLink, string> = {
  online: 'sent to the terminals',
  offline: 'the terminals get them when the Pi is back online',
  none: 'the terminals get them once a Pi is linked',
  unknown: 'sent to the store’s Pi'
};

export function settingsForm(raw: Partial<TerminalSettings>): SettingsForm {
  const s = { ...TERMINAL_DEFAULTS, ...raw };
  return {
    refund: centsInput(s.cashier_refund_cents), discount: String(s.cashier_discount_pct), lock: String(s.auto_lock_minutes),
    counterStart: String(s.counter_start), receiptDone: String(s.receipt_auto_done_seconds),
    phoneTimeout: String(s.phone_charge_timeout_seconds || TERMINAL_DEFAULTS.phone_charge_timeout_seconds),
    autoClose: s.auto_close_batch_at !== '', autoCloseAt: s.auto_close_batch_at || '04:00',
    defaultTab: s.default_tab, showHistory: s.show_history, font: s.button_font, priceDrop: String(s.cashier_price_drop_pct),
    clickSound: s.click_sound, pricesOnButtons: s.prices_on_buttons, margin: String(s.out_of_area_margin_mi),
    toppingMax: String(s.topping_max_count),
    nameOnPaid: s.name_on_paid_counter, cardTips: s.card_tips, tipPercents: (s.card_tip_percents ?? []).join(', '),
    meal: String(s.employee_meal_pct), payout: centsInput(s.cashier_payout_cents),
    pickupMinutes: String(s.default_pickup_minutes), areaCodes: (s.area_codes ?? []).join(', '),
    drawerStart: centsInput(s.drawer_start_cents),
    quibaseMin: centsInput(s.quibase_counter_min_cents),
    wasteReasons: (s.waste_reasons ?? []).join('\n'),
    voidReasons: (s.void_reasons ?? []).join('\n')
  };
}

/** A settings problem tied to one field (focused + marked in the form). */
export class FieldError extends Error {
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

/** The form as main takes it; a bad value throws a FieldError naming its field. */
export function settingsOf(f: SettingsForm, keep: Partial<TerminalSettings> = {}): TerminalSettings {
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
    ...TERMINAL_DEFAULTS,
    ...keep, // fields these pages don't edit (the call center's idle lock) go back as they were
    cashier_refund_cents: money(f, 'refund', 'Refunds up to', 1_000_000),
    cashier_discount_pct: int(f, 'discount', 'Discounts up to', 0, 100),
    auto_lock_minutes: int(f, 'lock', 'Lock the terminal after', 1, 240),
    counter_start: int(f, 'counterStart', 'Counter numbers start at', 1, 999_999),
    receipt_auto_done_seconds: int(f, 'receiptDone', 'Close the receipt screen after', 0, 600),
    phone_charge_timeout_seconds: int(f, 'phoneTimeout', 'A charge sent to a phone expires after', 30, 1800),
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
    quibase_counter_min_cents: money(f, 'quibaseMin', 'Counter orders on Quibase over', 100_000),
    area_codes: codes,
    waste_reasons: waste,
    void_reasons: voids
  };
}

/** A number field: label, unit, help (with the default). */
export type NumField = { key: TextKey; label: string; unit: string; prefix?: boolean; help: string; decimal?: boolean };
const D = TERMINAL_DEFAULTS;
export const NUM_FIELDS: Partial<Record<TextKey, NumField>> = Object.fromEntries(
  ([
    { key: 'refund', label: 'Refunds up to', unit: '$', prefix: true, decimal: true,
      help: `Cash given back on one order, in all, without a manager (card refunds and older days’ orders always need one). 0 = every refund needs a manager. Default ${dollars(D.cashier_refund_cents)}.` },
    { key: 'discount', label: 'Discounts up to', unit: '%', help: `A discount the cashier types on an order. Default ${D.cashier_discount_pct}%.` },
    { key: 'priceDrop', label: 'Lower an item’s price by up to', unit: '%', help: `Raising a price is always allowed. Default ${D.cashier_price_drop_pct}%.` },
    { key: 'payout', label: 'Pay out of the drawer up to', unit: '$', prefix: true, decimal: true,
      help: `Paying cash in is always allowed. Default ${dollars(D.cashier_payout_cents)}.` },
    { key: 'drawerStart', label: 'Cash left in each drawer at opening', unit: '$', prefix: true, decimal: true,
      help: `Every terminal's drawer starts the day with this; the drawer report's Expected in Drawer counts from it. Default ${dollars(D.drawer_start_cents)}.` },
    { key: 'lock', label: 'Lock the terminal after', unit: 'min', help: `Idle this long, the terminal goes back to the PIN pad. Default ${D.auto_lock_minutes} min.` },
    { key: 'receiptDone', label: 'Close the receipt screen after', unit: 'sec', help: `0 = it stays until Done is pressed. Default ${D.receipt_auto_done_seconds} sec.` },
    { key: 'phoneTimeout', label: 'A charge sent to a phone expires after', unit: 'sec',
      help: `Send to phone: nobody finished it in Square by then, it expires (a late tap is still recorded). 30–1800. Default ${D.phone_charge_timeout_seconds} sec.` },
    { key: 'meal', label: 'Employee meal', unit: '% off', help: `The Employee meal button. A cashier can give it without a manager. Default ${D.employee_meal_pct}%.` },
    { key: 'pickupMinutes', label: 'Pickups are ready in', unit: 'min', help: `The time a pickup is quoted. Default ${D.default_pickup_minutes} min.` },
    { key: 'counterStart', label: 'Counter numbers start at', unit: '#', help: `Counter orders count up from here, again after Close batch. Default ${D.counter_start}.` },
    { key: 'toppingMax', label: 'Most of one topping', unit: '×', help: `Tapping a topping again adds one more (2×, 3×). 1 = no doubles. Default ${D.topping_max_count}.` },
    { key: 'quibaseMin', label: 'Counter orders on Quibase over', unit: '$', prefix: true, decimal: true,
      help: `An order rung at a terminal shows on the counter tablet only when its total is over this and something on it goes to a kitchen station (a drink-only sale stays off). It still counts on every report. Call-center, online and website orders always show. 0 = any total. Default ${dollars(D.quibase_counter_min_cents)}.` },
    { key: 'margin', label: 'Deliver past the delivery area by up to', unit: 'mi', decimal: true,
      help: `Up to this far past your delivery area the cashier just sees a warning; farther needs a manager’s PIN. Default ${D.out_of_area_margin_mi} mi.` }
  ] as NumField[]).map((f) => [f.key, f])
);

const tabLabel = (v: string) => TABS.find((t) => t.value === v)?.label ?? v;
const clock = (hhmm: string) => {
  const [h, m] = hhmm.split(':').map(Number);
  return new Date(2000, 0, 1, h, m).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' });
};

/** One line per group for the Settings tiles. */
export function terminalSummary(group: TerminalGroup, raw: Partial<TerminalSettings>): string {
  const s = { ...D, ...raw };
  switch (group) {
    case 'cashier':
      return `Refunds ${dollars(s.cashier_refund_cents)} · discounts ${s.cashier_discount_pct}% · payouts ${dollars(s.cashier_payout_cents)}`;
    case 'screen':
      return `Locks after ${s.auto_lock_minutes} min · opens ${tabLabel(s.default_tab)}`;
    case 'counter':
      return `Pickups in ${s.default_pickup_minutes} min · employee meal ${s.employee_meal_pct}% · Quibase over ${dollars(s.quibase_counter_min_cents)}`;
    case 'voids':
      return `${s.waste_reasons.length} waste reasons · ${s.void_reasons.length} void reasons`;
    case 'card':
      return s.card_tips ? `Asks for a tip: ${s.card_tip_percents.join(', ')}%` : 'No tip on the card reader';
    case 'endofday':
      return `${s.auto_close_batch_at ? `Closes the batch at ${clock(s.auto_close_batch_at)}` : 'Batch closed by hand'} · drawers start ${dollars(s.drawer_start_cents)}`;
  }
}
