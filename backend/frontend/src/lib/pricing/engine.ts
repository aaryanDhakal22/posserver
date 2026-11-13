// The pricing engine's TypeScript copy (main 3.0 step 4). main's Go engine
// (internal/domain/pricing) is the authority; this copy previews prices in
// the browser (the deals editor today, the storefront later). Both run the
// same JSON cases (internal/domain/pricing/testdata/cases, `node --test`):
// keep them in step, line for line. Pure, integer cents, no imports.

export interface BuildItemSize { size_id: string; name: string; price_cents: number }
export interface BuildChoice { item_id: string; name: string; price_cents: number; sizes: BuildItemSize[]; sold_out: boolean }
export interface BuildItem {
  id: string; name: string; anchor: { item_ids: string[] }; price_cents: number; sizes: BuildItemSize[];
  sold_out: boolean; modifier_group_ids: string[]; choices?: BuildChoice[];
  [k: string]: unknown;
}
export interface BuildOption {
  id: string; name: string; price_cents: number; prices: { size_id: string; price_cents: number }[]; is_default: boolean; sold_out: boolean;
}
export interface BuildGroup {
  id: string; name: string; required: boolean; min_select: number; max_select: number; allow_multiple: boolean; portions: boolean; options: BuildOption[];
}
export interface Menu { categories: { items: BuildItem[] }[]; modifier_groups: BuildGroup[] }
/** no_tax: the item isn't taxed (tax_bps 0 = the store rate); open_price: the line carries its own price_cents (the counter only). */
/** included_toppings: this many of the line's charged options are in the item's price, the cheapest first (included_toppings_sizes: per size id). */
export interface ItemFacts {
  category_id: string; tax_bps: number; half_base?: string; no_tax?: boolean; open_price?: boolean; family?: string;
  included_toppings?: number; included_toppings_sizes?: Record<string, number> | null;
}
/** A modifier option's family and its class (regular: a coupon may make it free; premium / unpriced: never); never_included: always charged,
 *  even on an item with toppings included. */
export interface OptionFacts { family?: string; class?: string; never_included?: boolean }
export interface Settings {
  tax_bps: number; service_fee_cents: number; service_fee_bps: number;
  delivery_fee_cents: number; free_delivery_over_cents: number | null; delivery_min_cents: number;
  /** The delivery fee by distance (absent = the flat delivery_fee_cents); deliveryFeeFor picks one. */
  delivery_bands?: DeliveryBand[] | null;
  tips: { enabled: boolean; presets: number[]; default_pct: number };
  card_fee: { mode: 'none' | 'surcharge' | 'cash_discount' | string; bps: number };
  stacking: 'best' | 'stack' | 'single' | string;
  /** Half and half: a topping on a half at full ('' too) or half price; the cheaper half-specialty's extra over plain at this % off. */
  half_topping_price?: 'full' | 'half' | string; half_specialty_off_pct?: number;
}
export interface Eligible { items: string[] | null; categories: string[] | null; sizes: string[] | null }
export interface Slot { label: string; eligible: Eligible; included_options: number }
/** A coupon part: qty items of one of these item families (at one of these sizes, when any), each with N cheapest regular mods free.
 *  upcharge_over: some of its families, the base: a unit of another of its families pays its own price over the cheapest base item at its size
 *  (none at its size: the unit doesn't fit). */
export interface Part {
  label: string; families: string[] | null; sizes?: string[] | null; qty: number; regular_mods_included: number; upcharge_over?: string[] | null;
}
export interface Schedule { days: string[] | null; start: string; end: string }
export interface Deal {
  id: string; kind: 'bundle' | 'mix' | 'nth' | 'code' | 'free_item' | string; name: string; description?: string; active: boolean;
  fulfil: string; schedule: Schedule | null;
  price_cents?: number; slots?: Slot[]; eligible?: Eligible | null; qty?: number; each_cents?: number; n?: number; pct?: number;
  code?: string; amount_cents?: number; first_order_only?: boolean; min_subtotal_cents?: number;
  /** coupon (+ price_cents): the card's lines and its parts. */
  card?: string[] | null; parts?: Part[] | null;
}
export interface Context {
  menu: Menu; items: Record<string, ItemFacts>; settings: Settings; deals: Deal[] | null; timezone: string;
  types: { pickup: boolean; delivery: boolean };
  /** This channel may sell open-price items at the line's own price (the counter). */
  open_prices?: boolean;
  /** A line's price_cents may also replace a set price (an item price change; only the Pi's counter sets it). */
  price_overrides?: boolean;
  /** The counter terminal: a missed required group is a warning (the line still sells) and a cart may carry custom lines. */
  counter?: boolean;
  /** An allow_multiple group's option at most this many times on the same portion + mode (0 = no cap; the Pi's topping_max_count). */
  max_repeat?: number;
  /** Modifier option id → its family + class (coupons); an option not here is in no family (never free). */
  options?: Record<string, OptionFacts> | null;
}
/** portion: whole ('' too) | first | second (older carts: left = first, right = second); mode: add ('' too) | only | no. */
/** price_cents: a kept option price (price_overrides only, the Pi's kept line): the pick as it was rung (0–MAX_OPEN_PRICE_CENTS; a NO stays free). */
export interface Pick { group_id: string; option_id: string; portion?: string; mode?: string; price_cents?: number | null }
/** A specialty pizza (a menu item) on half 1 or 2, priced at the line's size. */
export interface Half { item_id: string; half: number }
/** price_cents: an open-price item's price (0–MAX_OPEN_PRICE_CENTS); on any other item a price change, taken only with price_overrides (it replaces the item's price at its size, half and half: the pizza's; options still add). */
export interface Selection { item_id: string; choice_id?: string; size_id?: string; options?: Pick[] | null; halves?: Half[] | null; price_cents?: number | null }
/** custom (the counter only): a free line, name (1–60 characters, blank = "Open item") at price_cents (0–999,999), the store's tax rate, no deals. */
/** keep_price: price_cents is the Pi's kept price (price_overrides only), not a price change: the line stays in coupons and the automatic deals. */
export interface Line extends Selection {
  qty?: number; note?: string; deal_id?: string; picks?: Selection[] | null; custom?: boolean; name?: string; keep_price?: boolean;
}
export interface Cart {
  type: string; payment?: string; lines: Line[] | null; code?: string;
  tip_cents?: number | null; tip_pct?: number | null; first_order?: boolean | null;
  /** Applied coupons: a coupon deal and the cart line units it takes (one index per unit). */
  coupons?: CartCoupon[] | null;
  /** A delivery's straight-line distance from the store in meters (null / absent = unknown): the fee bands read it. */
  distance_m?: number | null;
}
/** One row of the delivery fee by distance: up to up_to_m meters costs fee_cents (ascending; the last also covers farther). */
export interface DeliveryBand { up_to_m: number; fee_cents: number }
/** The customer's delivery fee: the first band the distance is within, the last past them all, the FIRST when the
 *  distance is unknown; no bands = the flat fee (Go DeliveryFeeFor). */
export function deliveryFeeFor(flat: number, bands: DeliveryBand[] | null | undefined, distanceM: number | null | undefined): number {
  if (!bands || bands.length === 0) return flat;
  if (distanceM === null || distanceM === undefined) return bands[0].fee_cents;
  for (const b of bands) if (distanceM <= b.up_to_m) return b.fee_cents;
  return bands[bands.length - 1].fee_cents;
}
export interface CartCoupon { id: string; lines: number[] | null }
/** unit_cents: the item's own price (with its options); total_cents: the line as sold (a coupon's units at their coupon prices). */
export interface QuoteLine {
  index: number; name: string; mods: string[]; qty: number; unit_cents: number; total_cents: number; regular_cents: number | null; deal_id: string;
  /** A price change: the item's own price the line's price_cents replaced. */
  list_cents?: number;
  /** A custom line. */
  custom?: boolean;
}
/** item_cents: the coupon price on the carrier (0 on the others) + upcharge_cents (its part's upcharge_over, absent when none). */
export interface CouponUnit {
  line: number; part: number; carrier: boolean; item_cents: number; upcharge_cents?: number; mods_cents: number; free_mods: string[];
  unit_cents: number; regular_cents: number;
}
/** A coupon re-prices its lines: never a discount (regular_cents is never shown as a saving). */
export interface AppliedCoupon {
  index: number; id: string; name: string; card: string[]; price_cents: number; lines: number[];
  regular_cents: number; total_cents: number; units: CouponUnit[];
}
export interface CouponOffer {
  id: string; name: string; card: string[]; price_cents: number; lines: number[]; regular_cents: number; total_cents: number;
}
export interface Discount { type: string; deal_id: string; label: string; amount_cents: number }
export interface Issue { code: string; message: string; line: number | null }
export interface Quote {
  lines: QuoteLine[]; subtotal_cents: number; discounts: Discount[]; delivery_fee_cents: number; service_fee_cents: number;
  tax_cents: number; tip_cents: number; card_fee_cents: number; total_cents: number; saved_cents: number;
  errors: Issue[]; warnings: string[]; nudges: string[];
  /** Warnings about a line, with its index (the counter's missed required group); each message is in warnings too. */
  line_warnings?: Issue[];
  /** The cart's coupons that apply (they re-price their lines); the live coupons the units not in a coupon fully fit, the most off the units' own prices first (both absent when none). */
  coupons?: AppliedCoupon[];
  coupon_offers?: CouponOffer[];
}

/** amount × bps / 10000, half up (half away from zero below zero). */
export function pct(amount: number, bps: number): number {
  if (amount < 0) return -pct(-amount, bps);
  return Math.floor((amount * bps + 5000) / 10000);
}

/** "$12.50" ("−$1.00" below zero). */
export function money(c: number): string {
  const sign = c < 0 ? '−' : '';
  const a = Math.abs(c);
  return `${sign}$${Math.floor(a / 100)}.${String(a % 100).padStart(2, '0')}`;
}

const WEEKDAYS: Record<string, string> = { Sun: 'sun', Mon: 'mon', Tue: 'tue', Wed: 'wed', Thu: 'thu', Fri: 'fri', Sat: 'sat' };

function hhmm(s: string | undefined): number | null {
  const m = /^\s*(\d{1,2}):(\d{2})\s*$/.exec(s ?? '');
  if (!m) return null;
  const h = Number(m[1]), mi = Number(m[2]);
  return h < 24 && mi < 60 ? h * 60 + mi : null;
}

function localClock(at: Date, tz: string): { day: string; minutes: number } {
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat('en-US', { timeZone: tz || 'UTC', weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(at);
  } catch {
    parts = new Intl.DateTimeFormat('en-US', { timeZone: 'UTC', weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(at);
  }
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? '';
  return { day: WEEKDAYS[get('weekday')] ?? '', minutes: (Number(get('hour')) % 24) * 60 + Number(get('minute')) };
}

/** Whether a deal runs at `at` for an order type ("" = any). */
export function live(d: Deal, tz: string, at: Date, orderType: string): boolean {
  if (!d.active) return false;
  if (d.fulfil && d.fulfil !== 'both' && orderType && d.fulfil !== orderType) return false;
  const s = d.schedule;
  if (!s) return true;
  const now = localClock(at, tz);
  if (s.days?.length && !s.days.includes(now.day)) return false;
  const start = hhmm(s.start), end = hhmm(s.end);
  if (start !== null && end !== null && end <= start) return now.minutes >= start || now.minutes < end;
  if (start !== null && now.minutes < start) return false;
  if (end !== null && now.minutes >= end) return false;
  return true;
}

interface Unit { price: number; item: string; category: string; size: string }

function matches(el: Eligible, u: Unit): boolean {
  if (!u.item) return false;
  if (!(el.items ?? []).includes(u.item) && !(u.category && (el.categories ?? []).includes(u.category))) return false;
  if (el.sizes?.length) return !!u.size && el.sizes.includes(u.size);
  return true;
}

interface Priced {
  name: string; mods: string[]; base: number; halfUp: number; extras: number; unit: number; bankItem: string; rateItem: string; size: string; issues: Issue[];
  /** The counter's warnings (a missed required group). */
  soft?: Issue[];
  /** A price change: the price the line's own replaced. */
  list?: number;
  /** Charged picks (price > 0, not an included topping) for coupons, in pick order. */
  cmods?: { price: number; option: string; label: string }[];
}

/** An open price's ceiling ($10,000). */
export const MAX_OPEN_PRICE_CENTS = 1_000_000;
/** A custom line's price ceiling ($9,999.99), name length and blank name. */
export const MAX_CUSTOM_CENTS = 999_999;
export const MAX_CUSTOM_NAME = 60;
export const CUSTOM_LINE_NAME = 'Open item';

const issueOf = (code: string, message: string): Issue => ({ code, message, line: null });

/** A pick's portion: first | second | '' (whole); left / right are older carts' first / second. */
function portionOf(s: string | undefined): string {
  if (s === 'first' || s === 'left') return 'first';
  if (s === 'second' || s === 'right') return 'second';
  return '';
}

const modeOf = (s: string | undefined): string => (s === 'only' || s === 'no' ? s : 'add');

/** How an option reads: "Pepperoni (1st half)", "ONLY Ham (2nd half)", "NO Onion". */
export function optionLabel(name: string, portion: string, mode: string): string {
  if (mode === 'only') name = 'ONLY ' + name;
  else if (mode === 'no') name = 'NO ' + name;
  if (portion === 'first') return name + ' (1st half)';
  if (portion === 'second') return name + ' (2nd half)';
  return name;
}

/** A menu item's price at a size ('' = an item without sizes); null = not in that size. */
function sizePrice(it: BuildItem, size: string): number | null {
  if (!(it.sizes ?? []).length) return size === '' ? it.price_cents : null;
  for (const z of it.sizes) if (z.size_id === size) return z.price_cents;
  return null;
}

interface Specialty { name: string; key: string; bank: string; price: number }
interface HalfPizza { key: string; price: number; plain: number }

class Engine {
  items = new Map<string, BuildItem>();
  groups = new Map<string, BuildGroup>();
  cheap = new Map<string, number | null>(); // coupons' base prices (family set + size)
  ctx: Context;
  constructor(ctx: Context) {
    this.ctx = ctx;
    for (const c of ctx.menu.categories ?? []) for (const it of c.items ?? []) if (!this.items.has(it.id)) this.items.set(it.id, it);
    for (const g of ctx.menu.modifier_groups ?? []) this.groups.set(g.id, g);
  }
  category(bankItem: string): string {
    return this.ctx.items?.[bankItem]?.category_id ?? '';
  }
  rate(bankItem: string): number {
    if (this.ctx.items?.[bankItem]?.no_tax) return 0;
    const r = this.ctx.items?.[bankItem]?.tax_bps ?? 0;
    return r > 0 ? r : this.ctx.settings.tax_bps;
  }
  /** A bank item's plain pizza (its half_base, as this menu shows it) at a size, with its id; null = none. */
  plainOf(bankItem: string, size: string): { price: number; id: string } | null {
    const hb = this.ctx.items?.[bankItem]?.half_base ?? '';
    if (bankItem === '' || hb === '') return null;
    const it = this.items.get(hb);
    if (!it) return null;
    const pr = sizePrice(it, size);
    return pr === null ? null : { price: pr, id: hb };
  }
  /** A split pizza's price: one pizza on both halves costs its price; two cost the dearer + the cheaper's extra over plain at X % off. */
  halves(p: Priced, base: number, spec: (Specialty | null)[], only: boolean[]): number {
    const lineKey = p.bankItem === '' ? 'line' : p.bankItem;
    let linePlain = base, plainKey = lineKey;
    const lp = this.plainOf(p.bankItem, p.size);
    if (lp) {
      linePlain = lp.price;
      plainKey = lp.id;
    }
    const half = (k: number): HalfPizza => {
      const s = spec[k];
      if (s) {
        const sp = this.plainOf(s.bank, p.size);
        return { key: s.key, price: s.price, plain: sp ? sp.price : linePlain };
      }
      if (only[k]) return { key: plainKey, price: linePlain, plain: linePlain };
      return { key: lineKey, price: base, plain: linePlain };
    };
    const h1 = half(1), h2 = half(2);
    if (h1.key === h2.key) return h1.price;
    let dear = h1, cheap = h2;
    if (h2.price > h1.price) {
      dear = h2;
      cheap = h1;
    }
    const extra = Math.max(cheap.price - cheap.plain, 0);
    const off = Math.min(Math.max(Math.trunc(this.ctx.settings.half_specialty_off_pct ?? 0), 0), 100);
    return dear.price + pct(extra, (100 - off) * 100);
  }
  price(sel: Selection, free: number): Priced {
    const it = this.items.get(sel.item_id);
    if (!it) return { name: 'Unknown item', mods: [], base: 0, halfUp: 0, extras: 0, unit: 0, bankItem: '', rateItem: '', size: '', issues: [issueOf('unknown_item', 'An item in your order is no longer on the menu.')] };
    const p: Priced = { name: it.name, mods: [], base: 0, halfUp: 0, extras: 0, unit: 0, bankItem: '', rateItem: '', size: '', issues: [] };
    let sizes = it.sizes ?? [], base = it.price_cents, soldOut = it.sold_out;
    const anchor = it.anchor?.item_ids ?? [];
    if (it.choices?.length) {
      let ch: BuildChoice | undefined;
      for (const c of it.choices) if (c.item_id === (sel.choice_id ?? '')) ch = c;
      if (!ch) {
        p.issues.push(issueOf('option_rules', `${it.name}: choose one.`));
        return p;
      }
      p.mods.push(ch.name);
      sizes = ch.sizes ?? [];
      base = ch.price_cents;
      soldOut = ch.sold_out;
      p.bankItem = p.rateItem = ch.item_id;
    } else if (anchor.length && anchor[0] === it.id) {
      p.bankItem = p.rateItem = it.id;
    } else if (anchor.length) {
      p.rateItem = anchor[0];
    }
    const sizeId = sel.size_id ?? '';
    if (sizes.length) {
      let sz: BuildItemSize | undefined = sizeId === '' ? sizes[0] : undefined;
      for (const s of sizes) if (s.size_id === sizeId) sz = s;
      if (!sz) {
        p.issues.push(issueOf('unknown_size', `${it.name}: that size is gone.`));
        return p;
      }
      base = sz.price_cents;
      p.size = sz.size_id;
      p.mods.push(sz.name);
    } else if (sizeId !== '') {
      p.issues.push(issueOf('unknown_size', `${it.name}: that size is gone.`));
      return p;
    }
    if (soldOut) p.issues.push(issueOf('sold_out', `${p.name} is sold out right now.`));
    // An open-price item (the counter's "Advance Payment"): the line carries the cashier's price.
    // A price change (price_overrides, the Pi's counter): the line's own price replaces the item's, once the pizza is priced.
    const open = p.bankItem !== '' && !!this.ctx.items?.[p.bankItem]?.open_price;
    const own = sel.price_cents;
    let override = false;
    if (open && !this.ctx.open_prices) p.issues.push(issueOf('open_price', `${p.name} can't be ordered here.`));
    else if (open && (own == null || !Number.isInteger(own) || own < 0 || own > MAX_OPEN_PRICE_CENTS)) p.issues.push(issueOf('open_price', `${p.name}: enter a price.`));
    else if (open) base = own as number;
    else if (own != null && this.ctx.price_overrides && (!Number.isInteger(own) || own < 0 || own > MAX_OPEN_PRICE_CENTS))
      p.issues.push(issueOf('open_price', `${p.name}: a price is $0.00 to ${money(MAX_OPEN_PRICE_CENTS)}.`));
    else if (own != null && this.ctx.price_overrides) override = true;
    else if (own != null) p.issues.push(issueOf('open_price', `${p.name} has a set price.`));
    const groupIds = it.modifier_group_ids ?? [];
    // Half-specialties: a specialty pizza on a half, at the line's size.
    const spec: (Specialty | null)[] = [null, null, null];
    const halves = sel.halves ?? [];
    if (halves.length) {
      let split = false;
      for (const gid of groupIds) if (this.groups.get(gid)?.portions) split = true;
      if (!split) p.issues.push(issueOf('option_rules', `${it.name} can't be split in halves.`));
      for (const h of halves) {
        if (!split) break;
        if (h.half !== 1 && h.half !== 2) {
          p.issues.push(issueOf('option_rules', `${it.name}: a half is 1 or 2.`));
          continue;
        }
        if (spec[h.half]) {
          p.issues.push(issueOf('option_rules', `${it.name}: one specialty per half.`));
          continue;
        }
        const s = this.items.get(h.item_id);
        if (!s) {
          p.issues.push(issueOf('unknown_item', `${it.name}: a half's pizza is no longer on the menu.`));
          continue;
        }
        if (s.choices?.length) {
          p.issues.push(issueOf('option_rules', `${s.name} can't be a half.`));
          continue;
        }
        const pr = sizePrice(s, p.size);
        if (pr === null) {
          p.issues.push(issueOf('unknown_size', `${s.name} doesn't come in that size.`));
          continue;
        }
        if (s.sold_out) p.issues.push(issueOf('sold_out', `${s.name} is sold out right now.`));
        const anchor = s.anchor?.item_ids ?? [];
        const bank = anchor.length ? anchor[0] : '';
        spec[h.half] = { name: s.name, key: bank !== '' ? bank : 'menu:' + s.id, bank, price: pr };
      }
      for (const k of [1, 2]) {
        const s = spec[k];
        if (s) p.mods.push(optionLabel(s.name, k === 1 ? 'first' : 'second', 'add'));
      }
    }
    const halfPrice = this.ctx.settings.half_topping_price === 'half';
    const only = [false, false, false];
    const charged: { price: number; option: string; label: string; half: boolean; included: boolean }[] = []; // priced picks, in option order
    const opts = sel.options ?? [];
    for (const gid of groupIds) {
      const g = this.groups.get(gid);
      if (!g) continue;
      const picks = opts.filter((pk) => pk.group_id === g.id);
      const seen = new Set<string>(); // options picked (any portion or mode)
      const twice = new Set<string>(); // option + portion + mode
      const reps = new Map<string, number>(); // option + portion + mode → picks so far
      const modAt = new Map<string, number>(); // option + portion + mode → its p.mods index
      let count = 0; // picks that put something on (NO doesn't count)
      for (const pk of picks) {
        let o: BuildOption | undefined;
        for (const x of g.options ?? []) if (x.id === pk.option_id) o = x;
        if (!o) {
          p.issues.push(issueOf('unknown_option', `${it.name}: an option is no longer available.`));
          continue;
        }
        const por = g.portions ? portionOf(pk.portion) : '';
        const mode = modeOf(pk.mode);
        const key = `${pk.option_id}/${por}/${mode}`;
        if (twice.has(key) && !g.allow_multiple) p.issues.push(issueOf('option_rules', `${it.name}: ${o.name} only once.`));
        twice.add(key);
        const n = (reps.get(key) ?? 0) + 1;
        reps.set(key, n);
        const maxRepeat = Math.trunc(this.ctx.max_repeat ?? 0);
        if (g.allow_multiple && maxRepeat > 0 && n === maxRepeat + 1) p.issues.push(issueOf('option_rules', `${it.name}: up to ${maxRepeat}× ${o.name}.`));
        seen.add(pk.option_id);
        if (mode !== 'no') {
          count++;
          if (o.sold_out) p.issues.push(issueOf('sold_out', `${o.name} is sold out right now.`));
        }
        const label = optionLabel(o.name, por, mode);
        // A repeat (allow_multiple: a double / triple) reads once, "2× Pepperoni".
        const at = modAt.get(key);
        if (at !== undefined && g.allow_multiple) p.mods[at] = `${n}× ${label}`;
        else if (n > 1 && g.allow_multiple) {
          modAt.set(key, p.mods.length);
          p.mods.push(`${n}× ${label}`);
        } else if (!o.is_default || por !== '' || mode !== 'add') {
          modAt.set(key, p.mods.length);
          p.mods.push(label);
        }
        let price = o.price_cents;
        for (const op of o.prices ?? []) if (p.size !== '' && op.size_id === p.size) price = op.price_cents;
        if (mode === 'no') price = 0;
        else if (por !== '' && halfPrice) price = Math.floor((price + 1) / 2); // half the whole-pie price, rounded up
        // A kept option price (the Pi's kept line): the pick as it was rung.
        const kp = pk.price_cents;
        if (kp != null && this.ctx.price_overrides && mode !== 'no' && Number.isInteger(kp) && kp >= 0 && kp <= MAX_OPEN_PRICE_CENTS) price = kp;
        if (price > 0) charged.push({ price, option: pk.option_id, label, half: por !== '', included: false });
        if (mode === 'only') {
          if (por === '') only[1] = only[2] = true;
          else only[por === 'second' ? 2 : 1] = true;
        }
      }
      // "NO onions": a default left off in a pick-many group.
      if (g.max_select !== 1) for (const o of g.options ?? []) if (o.is_default && !seen.has(o.id)) p.mods.push('NO ' + o.name);
      let min = g.min_select;
      if (g.required && min < 1) min = 1;
      if (count < min) {
        const is = issueOf('option_rules', `${it.name}: choose ${min === 1 ? 'a' : String(min)} ${g.name.toLowerCase()}.`);
        // The counter: the cashier's screen asked; the line still sells.
        if (this.ctx.counter) (p.soft ??= []).push(is);
        else p.issues.push(is);
      }
      if (g.max_select > 0 && count > g.max_select) p.issues.push(issueOf('option_rules', `${it.name}: up to ${g.max_select} ${g.name.toLowerCase()}.`));
    }
    for (const pk of opts) {
      if (!groupIds.includes(pk.group_id)) {
        p.issues.push(issueOf('unknown_option', `${it.name}: an option doesn't belong to this item.`));
        break;
      }
    }
    for (const k of [1, 2]) {
      if (spec[k] && only[k]) {
        p.issues.push(issueOf('option_rules', `${it.name}: a half with a specialty can't be ONLY.`));
        break;
      }
    }
    // The item's included toppings first (the cheapest charged picks, a tie: option order; a half takes half a place when halves cost half,
    // a whole place otherwise; a whole topping never takes a lone half place; never_included options are always charged), then a bundle
    // slot's `free` priciest of the options left.
    const n = this.includedAt(p.bankItem, p.size);
    if (n > 0) {
      const cand = charged.filter((c) => !this.ctx.options?.[c.option]?.never_included);
      cand.sort((a, b) => a.price - b.price); // stable
      let places = 2 * n;
      for (const c of cand) {
        const need = halfPrice && c.half ? 1 : 2;
        if (need > places) continue;
        places -= need;
        c.included = true;
        if (places === 0) break;
      }
    }
    const extras: number[] = [];
    for (const c of charged) {
      if (c.included) continue;
      extras.push(c.price);
      (p.cmods ??= []).push({ price: c.price, option: c.option, label: c.label });
    }
    extras.sort((a, b) => b - a);
    extras.forEach((x, i) => {
      if (i >= free) p.extras += x;
    });
    if (spec[1] || spec[2] || only[1] || only[2]) p.halfUp = this.halves(p, base, spec, only) - base;
    if (override) {
      // The pizza's price (with its halves) becomes the line's own.
      p.list = base + p.halfUp;
      base = own as number;
      p.halfUp = 0;
    }
    p.base = base;
    p.extras += p.halfUp;
    p.unit = base + p.extras;
    return p;
  }

  /** How many toppings a bank item includes at a size (0 = none; an added item: none). */
  includedAt(bankItem: string, size: string): number {
    if (!bankItem) return 0;
    const f = this.ctx.items?.[bankItem];
    if (!f) return 0;
    const per = f.included_toppings_sizes ?? null;
    const n = per && Object.prototype.hasOwnProperty.call(per, size) ? per[size] : f.included_toppings ?? 0;
    return Math.max(Math.trunc(n ?? 0), 0);
  }

  couponLine(p: Priced, qty: number): CLine {
    const c: CLine = {
      ...noLine(), ok: true, qty, unit: p.unit, base: p.base, halfUp: p.halfUp, item: p.bankItem, category: this.category(p.bankItem),
      size: p.size, family: this.ctx.items?.[p.bankItem]?.family ?? ''
    };
    for (const m of p.cmods ?? []) c.mods.push({ price: m.price, cls: this.ctx.options?.[m.option]?.class ?? '', label: m.label });
    return c;
  }
  /** The lowest price, at a size, of an item of one of the families on this menu (a merged item's choices count; sold out ones too); null = none has that size. */
  cheapest(fams: string[], size: string): number | null {
    const key = size + '|' + fams.join(',');
    if (this.cheap.has(key)) return this.cheap.get(key) as number | null;
    let best: number | null = null;
    const take = (bank: string, pr: number | null) => {
      const f = this.ctx.items?.[bank];
      if (pr == null || !f || !f.family || !fams.includes(f.family)) return;
      if (best == null || pr < best) best = pr;
    };
    for (const c of this.ctx.menu.categories ?? []) {
      for (const it of c.items ?? []) {
        take(it.id, sizePrice(it, size));
        for (const ch of it.choices ?? []) take(ch.item_id, sizePrice({ ...it, price_cents: ch.price_cents, sizes: ch.sizes ?? [] }, size));
      }
    }
    this.cheap.set(key, best);
    return best;
  }
  /** What a unit pays over the coupon on a part: its own price over the cheapest of the part's base families at its size (never below 0); 0 without a base or in it; null = no base item has its size (it doesn't fit). */
  upcharge(c: CLine, pt: Part): number | null {
    const base = pt.upcharge_over ?? [];
    if (base.length === 0 || base.includes(c.family)) return 0;
    const low = this.cheapest(base, c.size);
    if (low == null) return null;
    return Math.max(c.base - low, 0);
  }
  fitsPart(c: CLine, pt: Part): boolean {
    const sizes = pt.sizes ?? [];
    if (!c.ok || c.family === '' || !(pt.families ?? []).includes(c.family) || (sizes.length !== 0 && (c.size === '' || !sizes.includes(c.size)))) return false;
    return this.upcharge(c, pt) != null;
  }
  /** Matches a coupon's parts to units (cand: one line index per unit, preferred first) so every part is filled (Kuhn's augmenting paths, slots in part order), then prices it. */
  fit(d: Deal, cand: number[], clines: CLine[]): CouponFit | null {
    const parts = d.parts ?? [];
    const slots: number[] = [];
    parts.forEach((pt, k) => {
      for (let j = 0; j < Math.trunc(pt.qty ?? 0); j++) slots.push(k);
    });
    if (slots.length === 0 || slots.length > MAX_COUPON_SLOTS || cand.length < slots.length) return null;
    const owner: number[] = cand.map(() => -1);
    const tryAt = (s: number, seen: boolean[]): boolean => {
      // A free unit first: keep the natural order.
      for (let u = 0; u < cand.length; u++) {
        if (owner[u] < 0 && this.fitsPart(clines[cand[u]], parts[slots[s]])) {
          owner[u] = s;
          return true;
        }
      }
      for (let u = 0; u < cand.length; u++) {
        if (seen[u] || !this.fitsPart(clines[cand[u]], parts[slots[s]])) continue;
        seen[u] = true;
        if (owner[u] < 0 || tryAt(owner[u], seen)) {
          owner[u] = s;
          return true;
        }
      }
      return false;
    };
    for (let s = 0; s < slots.length; s++) if (!tryAt(s, cand.map(() => false))) return null;
    const bySlot: number[] = slots.map(() => 0);
    owner.forEach((s, u) => {
      if (s >= 0) bySlot[s] = cand[u];
    });
    // The carrier: the highest-priced item (a tie: the first line, then the first slot).
    let carrier = 0;
    for (let s = 1; s < slots.length; s++) {
      const a = clines[bySlot[s]], b = clines[bySlot[carrier]];
      if (a.base > b.base || (a.base === b.base && bySlot[s] < bySlot[carrier])) carrier = s;
    }
    const f: CouponFit = { units: [], lines: [], regular: 0, total: 0, saved: 0 };
    slots.forEach((k, s) => {
      const li = bySlot[s];
      const { mods, free } = unitIn(clines[li], Math.trunc(parts[k].regular_mods_included ?? 0));
      const up = this.upcharge(clines[li], parts[k]) ?? 0;
      const item = up + (s === carrier ? (d.price_cents ?? 0) : 0);
      const u: CouponUnit = {
        line: li, part: k, carrier: s === carrier, item_cents: item, mods_cents: mods, free_mods: free, unit_cents: item + mods, regular_cents: clines[li].unit
      };
      if (up !== 0) u.upcharge_cents = up;
      f.units.push(u);
      f.lines.push(li);
      f.regular += u.regular_cents;
      f.total += u.unit_cents;
    });
    f.lines.sort((a, b) => a - b);
    f.saved = f.regular - f.total;
    return f;
  }
  /** Applies the cart's coupons (one that is ended, not on now or doesn't fit as asked is left out: a warning on the counter, an error elsewhere), re-prices each touched line (total_cents: the units left at the line's own price + its coupon units' prices: never a discount), lists the offers. */
  coupons(q: Quote, cart: Cart, clines: CLine[], at: Date, issue: (code: string, message: string, line: number | null) => void): void {
    const ctx = this.ctx;
    const deals = ctx.deals ?? [];
    const type = cart.type ?? '';
    const problem = (code: string, message: string) => {
      if (ctx.counter) {
        if (!q.warnings.includes(message)) q.warnings.push(message);
        return;
      }
      issue(code, message, null);
    };
    (cart.coupons ?? []).forEach((cc, ci) => {
      let d: Deal | undefined;
      for (const x of deals) if (x.id === cc.id && x.kind === 'coupon') d = x;
      if (!d || !d.active) {
        problem('coupon_ended', 'A coupon in the order has ended.');
        return;
      }
      if (!live(d, ctx.timezone, at, type)) {
        problem('coupon_unavailable', `${d.name} isn't available right now.`);
        return;
      }
      const lines = cc.lines ?? [];
      const taken = new Map<number, number>();
      let bad = false, busy = false;
      for (const li of lines) {
        if (!Number.isInteger(li) || li < 0 || li >= clines.length || !clines[li].ok || clines[li].family === '') {
          bad = true;
          break;
        }
        const n = (taken.get(li) ?? 0) + 1;
        taken.set(li, n);
        if (clines[li].used + n > clines[li].qty) busy = true;
      }
      if (bad) {
        problem('coupon_fit', `${d.name}: a line in it can't be in a coupon.`);
        return;
      }
      if (busy) {
        problem('coupon_fit', `${d.name}: a line in it is already in another coupon.`);
        return;
      }
      const cand = [...lines].sort((a, b) => clines[b].unit - clines[a].unit || a - b);
      const f = this.fit(d, cand, clines);
      if (!f || cand.length !== f.units.length) {
        problem('coupon_fit', `${d.name} doesn't fit the order.`);
        return;
      }
      if (f.saved <= 0) {
        problem('coupon_fit', `${d.name} costs more than its items on this order.`);
        return;
      }
      for (const u of f.units) {
        const c = clines[u.line];
        c.used++;
        c.taxCut += u.regular_cents - u.unit_cents;
        c.couponCents += u.unit_cents;
      }
      (q.coupons ??= []).push({
        index: ci, id: d.id, name: d.name, card: cardOf(d), price_cents: d.price_cents ?? 0, lines: f.lines,
        regular_cents: f.regular, total_cents: f.total, units: f.units
      });
    });
    clines.forEach((c, i) => {
      if (c.used > 0 && i < q.lines.length) q.lines[i].total_cents = (c.qty - c.used) * c.unit + c.couponCents;
    });
    // Offers: the units not in a coupon, priciest first (a tie: line order).
    const free: number[] = [];
    clines.forEach((c, li) => {
      if (!c.ok || c.family === '') return;
      for (let k = c.used; k < c.qty && k < c.used + MAX_COUPON_SLOTS; k++) free.push(li);
    });
    free.sort((a, b) => clines[b].unit - clines[a].unit);
    const offers: CouponOffer[] = [];
    for (const d of deals) {
      if (d.kind !== 'coupon' || !live(d, ctx.timezone, at, type)) continue;
      const f = this.fit(d, free, clines);
      if (!f || f.saved <= 0) continue;
      offers.push({ id: d.id, name: d.name, card: cardOf(d), price_cents: d.price_cents ?? 0, lines: f.lines, regular_cents: f.regular, total_cents: f.total });
    }
    // The cheapest order first (the most off the units' own prices; a tie: the deal order).
    offers.sort((a, b) => b.regular_cents - b.total_cents - (a.regular_cents - a.total_cents));
    if (offers.length) q.coupon_offers = offers;
  }
}

// Coupons (the owner's Centurion pass, 2026-11-12; contract/main.md "Families + coupons"): parts of families × qty
// at one price; applies only when its units fill every part; the highest-priced item carries the price, the others
// $0.00; on each unit the part's N cheapest regular mods are free; a part's upcharge_over base: a unit of another of
// its families pays its price over the cheapest base item at its size. A coupon re-prices its lines (total_cents):
// never a discount, no saving shown (the owner, 2026-11-12). Keep in step with coupons.go.

/** The units one coupon takes at most (8 parts × 10). */
export const MAX_COUPON_SLOTS = 80;

/** A cart item line as coupons and the automatic deals see it. */
interface CLine {
  ok: boolean; qty: number; used: number; unit: number; base: number; halfUp: number;
  item: string; category: string; size: string; family: string;
  mods: { price: number; cls: string; label: string }[];
  taxCut: number; couponCents: number;
}

const noLine = (): CLine => ({
  ok: false, qty: 0, used: 0, unit: 0, base: 0, halfUp: 0, item: '', category: '', size: '', family: '', mods: [], taxCut: 0, couponCents: 0
});

/** One unit of the line on a part with n free regular mods: the n cheapest regular ones free (ties: the first picked), the rest charged, a half and half upcharge on top. */
function unitIn(c: CLine, n: number): { mods: number; free: string[] } {
  const regular: number[] = [];
  c.mods.forEach((m, k) => {
    if (m.cls === 'regular') regular.push(k);
  });
  regular.sort((a, b) => c.mods[a].price - c.mods[b].price);
  const free = new Set<number>();
  for (let k = 0; k < n && k < regular.length; k++) free.add(regular[k]);
  let mods = 0;
  const labels: string[] = [];
  c.mods.forEach((m, k) => {
    if (free.has(k)) labels.push(m.label);
    else mods += m.price;
  });
  if (c.halfUp > 0) mods += c.halfUp;
  return { mods, free: labels };
}

interface CouponFit { units: CouponUnit[]; lines: number[]; regular: number; total: number; saved: number }

const cardOf = (d: Deal): string[] => [...(d.card ?? [])];

/** Prices a cart at `at`. */
export function price(ctx: Context, cart: Cart, at: Date): Quote {
  const e = new Engine(ctx);
  const st = ctx.settings;
  const deals = ctx.deals ?? [];
  const type = cart.type ?? '';
  const q: Quote = {
    lines: [], subtotal_cents: 0, discounts: [], delivery_fee_cents: 0, service_fee_cents: 0, tax_cents: 0, tip_cents: 0,
    card_fee_cents: 0, total_cents: 0, saved_cents: 0, errors: [], warnings: [], nudges: []
  };
  const issue = (code: string, message: string, line: number | null) => {
    if (q.errors.some((x) => x.code === code && x.message === message)) return;
    q.errors.push({ code, message, line });
  };
  const nudge = (m: string) => {
    if (!q.nudges.includes(m)) q.nudges.push(m);
  };
  const warn = (is: Issue, line: number) => {
    if (!q.warnings.includes(is.message)) q.warnings.push(is.message);
    const lw = (q.line_warnings ??= []);
    if (lw.some((x) => x.code === is.code && x.message === is.message && x.line === line)) return;
    lw.push({ code: is.code, message: is.message, line });
  };
  const lineTax: { total: number; rate: number }[] = [];
  let hasBundle = false;
  // Item lines a coupon may take (by cart line index) and where each line's entry in lineTax is.
  const nLines = (cart.lines ?? []).length;
  const clines: CLine[] = Array.from({ length: nLines }, () => noLine());
  const taxAt: number[] = Array.from({ length: nLines }, () => -1);

  (cart.lines ?? []).forEach((line, i) => {
    let qty = Math.trunc(line.qty ?? 0);
    if (qty < 1) qty = 1;
    if (qty > 50) qty = 50;
    if (line.custom) {
      // A custom line (Quicc Add): the cashier's name and price, the store's tax rate, no deals.
      const name = (line.name ?? '').trim() || CUSTOM_LINE_NAME;
      const own = line.price_cents;
      let unit = 0;
      if (!ctx.counter) issue('custom_line', "A custom item can't be ordered here.", i);
      else if ([...name].length > MAX_CUSTOM_NAME) issue('custom_line', `A custom item's name is at most ${MAX_CUSTOM_NAME} characters.`, i);
      else if (own == null || !Number.isInteger(own) || own < 0 || own > MAX_CUSTOM_CENTS) issue('custom_line', `${name}: a price is $0.00 to ${money(MAX_CUSTOM_CENTS)}.`, i);
      else unit = own;
      const mods: string[] = [];
      const note = (line.note ?? '').trim();
      if (note) mods.push(`“${note}”`);
      q.lines.push({ index: i, name, mods, qty, unit_cents: unit, total_cents: unit * qty, regular_cents: null, deal_id: '', custom: true });
      lineTax.push({ total: unit * qty, rate: st.tax_bps });
      return;
    }
    if (!line.deal_id) {
      const p = e.price(line, 0);
      for (const is of p.issues) issue(is.code, is.message, i);
      for (const is of p.soft ?? []) warn(is, i);
      const mods = [...p.mods];
      const note = (line.note ?? '').trim();
      if (note) mods.push(`“${note}”`);
      const ql: QuoteLine = { index: i, name: p.name, mods, qty, unit_cents: p.unit, total_cents: p.unit * qty, regular_cents: null, deal_id: '' };
      if (p.list != null) ql.list_cents = p.list;
      q.lines.push(ql);
      taxAt[i] = lineTax.length;
      lineTax.push({ total: p.unit * qty, rate: e.rate(p.rateItem) });
      // A price-changed line is left out of the automatic deals and coupons: the cashier's price is the line's price. A kept price (the Pi's) stays in them.
      if (p.bankItem && p.issues.length === 0 && (p.list == null || (line.keep_price && ctx.price_overrides))) clines[i] = e.couponLine(p, qty);
      return;
    }
    hasBundle = true;
    let d: Deal | undefined;
    for (const x of deals) if (x.id === line.deal_id && x.kind === 'bundle') d = x;
    if (!d || !d.active) {
      issue('deal_ended', 'A deal in your order has ended.', i);
      q.lines.push({ index: i, name: 'Ended deal', mods: [], qty, unit_cents: 0, total_cents: 0, regular_cents: null, deal_id: line.deal_id });
      return;
    }
    if (!live(d, ctx.timezone, at, type)) issue('deal_unavailable', `${d.name} isn't available right now.`, i);
    let extras = 0, regular = 0, rateItem = '';
    const mods: string[] = [];
    const picks = line.picks ?? [];
    (d.slots ?? []).forEach((slot, k) => {
      if (k >= picks.length) {
        issue('deal_slot', `${d!.name}: finish every step.`, i);
        return;
      }
      const p = e.price(picks[k], slot.included_options ?? 0);
      for (const is of p.issues) issue(is.code, is.message, i);
      for (const is of p.soft ?? []) warn(is, i);
      if (p.list != null) issue('open_price', `${d!.name}: a combo's items keep their prices.`, i);
      if (p.issues.length === 0 && !matches(slot.eligible, { price: 0, item: p.bankItem, category: e.category(p.bankItem), size: p.size }))
        issue('deal_slot', `${d!.name}: ${p.name} doesn't fit “${slot.label}”.`, i);
      if (rateItem === '') rateItem = p.rateItem;
      extras += p.extras;
      regular += e.price(picks[k], 0).unit;
      mods.push([p.name, ...p.mods].join(' · '));
    });
    const u = (d.price_cents ?? 0) + extras;
    if (regular > u) q.saved_cents += (regular - u) * qty;
    q.lines.push({ index: i, name: d.name, mods, qty, unit_cents: u, total_cents: u * qty, regular_cents: regular, deal_id: d.id });
    lineTax.push({ total: u * qty, rate: e.rate(rateItem) });
  });
  if (q.lines.length === 0) issue('empty', 'Your order is empty.', null);

  // Coupons: the cashier's (they re-price their lines: not a discount), then the offers on the units left.
  e.coupons(q, cart, clines, at, issue);
  clines.forEach((c, i) => {
    if (c.taxCut !== 0 && taxAt[i] >= 0) lineTax[taxAt[i]].total -= c.taxCut;
  });
  for (const l of q.lines) q.subtotal_cents += l.total_cents;
  const sub = q.subtotal_cents;
  const food = sub;
  // The units left for the automatic deals (a coupon's units take none).
  const units: Unit[] = [];
  for (const c of clines) for (let k = c.used; c.ok && k < c.qty; k++) units.push({ price: c.unit, item: c.item, category: c.category, size: c.size });

  // Automatic deals.
  const autos: Discount[] = [];
  for (const d of deals) {
    if (!live(d, ctx.timezone, at, type) || !d.eligible) continue;
    const el = units.filter((u) => matches(d.eligible!, u));
    let amount = 0;
    if (d.kind === 'mix') {
      const qn = d.qty ?? 0, each = d.each_cents ?? 0;
      if (qn > 0 && el.length >= qn) {
        for (const u of el) if (u.price > each) amount += u.price - each;
      } else if (qn > 0 && el.length === qn - 1) nudge(`Add 1 more for ${d.name}: ${money(each)} each.`);
    } else if (d.kind === 'nth') {
      const n = d.n ?? 0;
      if (n < 1) continue;
      const sorted = [...el].sort((a, b) => b.price - a.price);
      for (let k = n - 1; k < sorted.length; k += n) amount += pct(sorted[k].price, (d.pct ?? 0) * 100);
      if (n > 1 && sorted.length % n === n - 1) nudge(`Add 1 more for ${d.name}.`);
    } else if (d.kind === 'free_item') {
      if (el.length === 0) continue;
      const min = d.min_subtotal_cents ?? 0;
      if (food >= min) amount = Math.min(...el.map((u) => u.price));
      else nudge(`Add ${money(min - food)} more for ${d.name}.`);
    }
    if (amount > 0) autos.push({ type: 'deal', deal_id: d.id, label: d.name, amount_cents: amount });
  }

  // A promo code.
  let code: Discount | null = null;
  const c = (cart.code ?? '').trim().toUpperCase();
  if (c) {
    let d: Deal | undefined;
    for (const x of deals) if (x.kind === 'code' && (x.code ?? '').trim().toUpperCase() === c) d = x;
    if (!d || !live(d, ctx.timezone, at, type)) issue('code_invalid', `Code ${c} isn't valid right now.`, null);
    else if (food < (d.min_subtotal_cents ?? 0)) issue('code_minimum', `${c} needs ${money(d.min_subtotal_cents ?? 0)} or more.`, null);
    else if (d.first_order_only && cart.first_order === false) issue('code_first_order', `${c} is for a first order only.`, null);
    else {
      let amt = d.amount_cents ?? 0;
      if ((d.pct ?? 0) > 0) amt = pct(food, (d.pct ?? 0) * 100);
      if (amt > food) amt = food;
      code = { type: 'code', deal_id: d.id, label: 'Code ' + c, amount_cents: amt };
    }
  }

  const best = (): Discount | null => {
    let b: Discount | null = null;
    for (const a of autos) if (!b || a.amount_cents > b.amount_cents) b = a;
    return b;
  };
  const pickOne = () => {
    const auto = best();
    if (auto && code && code.amount_cents >= auto.amount_cents) {
      q.discounts.push({ ...code });
      q.warnings.push(`Your code saves more than ${auto.label}, so we used the code.`);
    } else if (auto && code) {
      q.discounts.push({ ...auto });
      q.warnings.push(`${auto.label} saves more than your code, so we kept the deal.`);
    } else if (auto) q.discounts.push({ ...auto });
    else if (code) q.discounts.push({ ...code });
  };
  if (st.stacking === 'stack') {
    for (const a of autos) q.discounts.push({ ...a });
    if (code) q.discounts.push({ ...code });
  } else if (st.stacking === 'single') {
    if (hasBundle || (q.coupons ?? []).length > 0) {
      if (code) issue('code_not_combinable', "Codes don't combine with deals.", null);
    } else pickOne();
  } else pickOne();
  let left = sub;
  for (const d of q.discounts) {
    if (d.amount_cents > left) d.amount_cents = left;
    left -= d.amount_cents;
  }
  let after = left;

  if (type === 'pickup') {
    if (!ctx.types.pickup) issue('type_off', "This store isn't taking pickup orders right now.", null);
  } else if (type === 'delivery') {
    if (!ctx.types.delivery) issue('type_off', "This store isn't taking delivery orders right now.", null);
  } else issue('bad_type', 'Choose pickup or delivery.', null);
  if (type === 'delivery' && ctx.types.delivery) {
    if (q.lines.length > 0 && after < st.delivery_min_cents)
      issue('delivery_minimum', `Delivery needs a ${money(st.delivery_min_cents)} minimum. Add ${money(st.delivery_min_cents - after)} more or pick it up.`, null);
    const fee = deliveryFeeFor(st.delivery_fee_cents, st.delivery_bands, cart.distance_m);
    q.delivery_fee_cents = fee;
    const f = st.free_delivery_over_cents;
    if (f !== null && f !== undefined) {
      if (after >= f) q.delivery_fee_cents = 0;
      else if (fee > 0) nudge(`Free delivery over ${money(f)}: add ${money(f - after)}.`);
    }
  }

  if (st.card_fee.mode === 'cash_discount' && cart.payment === 'cash' && after > 0) {
    const cash = pct(after, st.card_fee.bps);
    q.discounts.push({ type: 'cash', deal_id: '', label: 'Cash discount', amount_cents: cash });
    after -= cash;
  }
  if (q.lines.length > 0) q.service_fee_cents = st.service_fee_cents + pct(after, st.service_fee_bps);

  // Tax: each rate's share of the food after discounts, plus the service fee
  // at the store rate. Delivery is untaxed.
  const byRate = new Map<number, number>();
  for (const lt of lineTax) byRate.set(lt.rate, (byRate.get(lt.rate) ?? 0) + lt.total);
  const rates = [...byRate.keys()].sort((a, b) => a - b);
  const off = sub - after;
  let spread = 0;
  rates.forEach((r, k) => {
    const gross = byRate.get(r)!;
    let share = 0;
    if (food > 0) share = k === rates.length - 1 ? off - spread : Math.floor((off * gross) / food);
    spread += share;
    q.tax_cents += pct(gross - share, r);
  });
  q.tax_cents += pct(q.service_fee_cents, st.tax_bps);

  if (st.tips.enabled) {
    if (cart.tip_cents !== null && cart.tip_cents !== undefined && cart.tip_cents >= 0) q.tip_cents = Math.trunc(cart.tip_cents);
    else {
      let p = st.tips.default_pct;
      if (cart.tip_pct !== null && cart.tip_pct !== undefined && cart.tip_pct >= 0 && cart.tip_pct <= 100) p = Math.trunc(cart.tip_pct);
      q.tip_cents = pct(after, p * 100);
    }
  } else if ((cart.tip_cents ?? 0) > 0 || (cart.tip_pct ?? 0) > 0) q.warnings.push("Tips aren't taken here, so none was added.");
  if (st.card_fee.mode === 'surcharge' && cart.payment !== 'cash')
    q.card_fee_cents = pct(after + q.delivery_fee_cents + q.service_fee_cents + q.tax_cents, st.card_fee.bps);
  q.total_cents = after + q.delivery_fee_cents + q.service_fee_cents + q.tax_cents + q.tip_cents + q.card_fee_cents;
  for (const d of q.discounts) q.saved_cents += d.amount_cents;
  return q;
}
