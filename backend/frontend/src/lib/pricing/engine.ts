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
export interface ItemFacts { category_id: string; tax_bps: number }
export interface Settings {
  tax_bps: number; service_fee_cents: number; service_fee_bps: number;
  delivery_fee_cents: number; free_delivery_over_cents: number | null; delivery_min_cents: number;
  tips: { enabled: boolean; presets: number[]; default_pct: number };
  card_fee: { mode: 'none' | 'surcharge' | 'cash_discount' | string; bps: number };
  stacking: 'best' | 'stack' | 'single' | string;
}
export interface Eligible { items: string[] | null; categories: string[] | null; sizes: string[] | null }
export interface Slot { label: string; eligible: Eligible; included_options: number }
export interface Schedule { days: string[] | null; start: string; end: string }
export interface Deal {
  id: string; kind: 'bundle' | 'mix' | 'nth' | 'code' | 'free_item' | string; name: string; description?: string; active: boolean;
  fulfil: string; schedule: Schedule | null;
  price_cents?: number; slots?: Slot[]; eligible?: Eligible | null; qty?: number; each_cents?: number; n?: number; pct?: number;
  code?: string; amount_cents?: number; first_order_only?: boolean; min_subtotal_cents?: number;
}
export interface Context {
  menu: Menu; items: Record<string, ItemFacts>; settings: Settings; deals: Deal[] | null; timezone: string;
  types: { pickup: boolean; delivery: boolean };
}
export interface Pick { group_id: string; option_id: string; portion?: string }
export interface Selection { item_id: string; choice_id?: string; size_id?: string; options?: Pick[] | null }
export interface Line extends Selection { qty?: number; note?: string; deal_id?: string; picks?: Selection[] | null }
export interface Cart {
  type: string; payment?: string; lines: Line[] | null; code?: string;
  tip_cents?: number | null; tip_pct?: number | null; first_order?: boolean | null;
}
export interface QuoteLine {
  index: number; name: string; mods: string[]; qty: number; unit_cents: number; total_cents: number; regular_cents: number | null; deal_id: string;
}
export interface Discount { type: string; deal_id: string; label: string; amount_cents: number }
export interface Issue { code: string; message: string; line: number | null }
export interface Quote {
  lines: QuoteLine[]; subtotal_cents: number; discounts: Discount[]; delivery_fee_cents: number; service_fee_cents: number;
  tax_cents: number; tip_cents: number; card_fee_cents: number; total_cents: number; saved_cents: number;
  errors: Issue[]; warnings: string[]; nudges: string[];
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
  name: string; mods: string[]; base: number; extras: number; unit: number; bankItem: string; rateItem: string; size: string; issues: Issue[];
}

const issueOf = (code: string, message: string): Issue => ({ code, message, line: null });

class Engine {
  items = new Map<string, BuildItem>();
  groups = new Map<string, BuildGroup>();
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
    const r = this.ctx.items?.[bankItem]?.tax_bps ?? 0;
    return r > 0 ? r : this.ctx.settings.tax_bps;
  }
  price(sel: Selection, free: number): Priced {
    const it = this.items.get(sel.item_id);
    if (!it) return { name: 'Unknown item', mods: [], base: 0, extras: 0, unit: 0, bankItem: '', rateItem: '', size: '', issues: [issueOf('unknown_item', 'An item in your order is no longer on the menu.')] };
    const p: Priced = { name: it.name, mods: [], base: 0, extras: 0, unit: 0, bankItem: '', rateItem: '', size: '', issues: [] };
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
    const extras: number[] = [];
    const opts = sel.options ?? [];
    const groupIds = it.modifier_group_ids ?? [];
    for (const gid of groupIds) {
      const g = this.groups.get(gid);
      if (!g) continue;
      const picks = opts.filter((pk) => pk.group_id === g.id);
      const seen = new Set<string>();
      for (const pk of picks) {
        let o: BuildOption | undefined;
        for (const x of g.options ?? []) if (x.id === pk.option_id) o = x;
        if (!o) {
          p.issues.push(issueOf('unknown_option', `${it.name}: an option is no longer available.`));
          continue;
        }
        if (seen.has(pk.option_id) && !g.allow_multiple) p.issues.push(issueOf('option_rules', `${it.name}: ${o.name} only once.`));
        seen.add(pk.option_id);
        if (o.sold_out) p.issues.push(issueOf('sold_out', `${o.name} is sold out right now.`));
        const half = g.portions && (pk.portion === 'left' || pk.portion === 'right') ? ` (${pk.portion} half)` : '';
        if (!o.is_default || half !== '') p.mods.push(o.name + half);
        let price = o.price_cents;
        for (const op of o.prices ?? []) if (p.size !== '' && op.size_id === p.size) price = op.price_cents;
        if (price > 0) extras.push(price);
      }
      // "NO onions": a default left off in a pick-many group.
      if (g.max_select !== 1) for (const o of g.options ?? []) if (o.is_default && !seen.has(o.id)) p.mods.push('NO ' + o.name);
      let min = g.min_select;
      if (g.required && min < 1) min = 1;
      if (picks.length < min) p.issues.push(issueOf('option_rules', `${it.name}: choose ${min === 1 ? 'a' : String(min)} ${g.name.toLowerCase()}.`));
      if (g.max_select > 0 && picks.length > g.max_select) p.issues.push(issueOf('option_rules', `${it.name}: up to ${g.max_select} ${g.name.toLowerCase()}.`));
    }
    for (const pk of opts) {
      if (!groupIds.includes(pk.group_id)) {
        p.issues.push(issueOf('unknown_option', `${it.name}: an option doesn't belong to this item.`));
        break;
      }
    }
    extras.sort((a, b) => b - a);
    extras.forEach((x, i) => {
      if (i >= free) p.extras += x;
    });
    p.base = base;
    p.unit = base + p.extras;
    return p;
  }
}

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
  const units: Unit[] = [];
  const lineTax: { total: number; rate: number }[] = [];
  let hasBundle = false;

  (cart.lines ?? []).forEach((line, i) => {
    let qty = Math.trunc(line.qty ?? 0);
    if (qty < 1) qty = 1;
    if (qty > 50) qty = 50;
    if (!line.deal_id) {
      const p = e.price(line, 0);
      for (const is of p.issues) issue(is.code, is.message, i);
      const mods = [...p.mods];
      const note = (line.note ?? '').trim();
      if (note) mods.push(`“${note}”`);
      q.lines.push({ index: i, name: p.name, mods, qty, unit_cents: p.unit, total_cents: p.unit * qty, regular_cents: null, deal_id: '' });
      lineTax.push({ total: p.unit * qty, rate: e.rate(p.rateItem) });
      if (p.bankItem && p.issues.length === 0) for (let k = 0; k < qty; k++) units.push({ price: p.unit, item: p.bankItem, category: e.category(p.bankItem), size: p.size });
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
  for (const l of q.lines) q.subtotal_cents += l.total_cents;
  const sub = q.subtotal_cents;

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
      if (sub >= min) amount = Math.min(...el.map((u) => u.price));
      else nudge(`Add ${money(min - sub)} more for ${d.name}.`);
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
    else if (sub < (d.min_subtotal_cents ?? 0)) issue('code_minimum', `${c} needs ${money(d.min_subtotal_cents ?? 0)} or more.`, null);
    else if (d.first_order_only && cart.first_order === false) issue('code_first_order', `${c} is for a first order only.`, null);
    else {
      let amt = d.amount_cents ?? 0;
      if ((d.pct ?? 0) > 0) amt = pct(sub, (d.pct ?? 0) * 100);
      if (amt > sub) amt = sub;
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
    if (hasBundle) {
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
    q.delivery_fee_cents = st.delivery_fee_cents;
    const f = st.free_delivery_over_cents;
    if (f !== null && f !== undefined) {
      if (after >= f) q.delivery_fee_cents = 0;
      else if (st.delivery_fee_cents > 0) nudge(`Free delivery over ${money(f)}: add ${money(f - after)}.`);
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
    if (sub > 0) share = k === rates.length - 1 ? off - spread : Math.floor((off * gross) / sub);
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
