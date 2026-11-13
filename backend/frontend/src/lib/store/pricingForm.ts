// Store › Settings › Tax and deals + Fees and tips: the store's pricing settings (GET/PUT /me/pricing).
// The PUT takes the whole body, so each page loads it, edits its own part and sends the rest back as
// it came (half_topping_price etc. would reset otherwise — main/CLAUDE.md "Half and half").
// Delivery fee = the store's share + the driver's share (TERMINAL-PLAN "Delivery fee"); the customer
// pays the total (delivery_fee_cents), the split feeds the driver reports.
import { centsInput, dollars, toCents, type PricingBody, type PricingFees, type PricingTips } from '$lib/api';

/** main's default driver share, capped at the fee. */
export const DEFAULT_DRIVER_CENTS = 300;
/** The channel kinds that may have their own fees and tips (the vendor's are kept as they are). */
export const KINDS = ['web', 'operator', 'counter'];

export type FeesForm = { service: string; servicePct: string; storeShare: string; driverShare: string; freeOver: string; min: string };
export type TipsForm = { enabled: boolean; presets: string; def: string };
export type KindForm = { ownFees: boolean; fees: FeesForm; ownTips: boolean; tips: TipsForm };

export function feesForm(f: PricingFees | null): FeesForm {
  const total = f?.delivery_fee_cents ?? 0;
  const driver = Math.min(f?.delivery_driver_cents ?? DEFAULT_DRIVER_CENTS, total);
  return {
    service: centsInput(f?.service_fee_cents ?? 0), servicePct: String((f?.service_fee_bps ?? 0) / 100),
    storeShare: centsInput(total - driver), driverShare: centsInput(driver),
    freeOver: f?.free_delivery_over_cents == null ? '' : centsInput(f.free_delivery_over_cents), min: centsInput(f?.delivery_min_cents ?? 0)
  };
}
export function feeTotal(f: FeesForm): string {
  const a = f.storeShare.trim() ? toCents(f.storeShare) : 0;
  const b = f.driverShare.trim() ? toCents(f.driverShare) : 0;
  return a === null || b === null || a < 0 || b < 0 ? '—' : '$' + centsInput(a + b);
}
export function tipsForm(t: PricingTips | null): TipsForm {
  return { enabled: !!t?.enabled, presets: (t?.presets ?? [15, 18, 20]).join(', '), def: String(t?.default_pct ?? 18) };
}
export function kindsForm(b: PricingBody): Record<string, KindForm> {
  const out: Record<string, KindForm> = {};
  for (const k of KINDS) {
    const o = b.kinds[k];
    out[k] = { ownFees: !!o?.fees, fees: feesForm(o?.fees ?? b.store.fees), ownTips: !!o?.tips, tips: tipsForm(o?.tips ?? b.store.tips) };
  }
  return out;
}

export function feesOf(f: FeesForm, where: string): PricingFees {
  const c = (s: string, what: string) => {
    const v = s.trim() ? toCents(s) : 0;
    if (v === null || v < 0) throw new Error(`${where}: “${s}” isn't a valid ${what}.`);
    return v;
  };
  return {
    service_fee_cents: c(f.service, 'online fee'), service_fee_bps: Math.round((parseFloat(f.servicePct) || 0) * 100),
    delivery_fee_cents: c(f.storeShare, 'store share') + c(f.driverShare, 'driver share'),
    delivery_driver_cents: c(f.driverShare, 'driver share'), free_delivery_over_cents: f.freeOver.trim() ? c(f.freeOver, 'amount') : null,
    delivery_min_cents: c(f.min, 'minimum')
  };
}
export function tipsOf(t: TipsForm): PricingTips {
  return {
    enabled: t.enabled,
    presets: t.presets.split(',').map((x) => parseInt(x.trim(), 10)).filter((x) => !isNaN(x)),
    default_pct: parseInt(t.def, 10) || 0
  };
}

export const STACKING: Record<string, string> = {
  best: 'Combos + the one best deal or code',
  stack: 'Everything that matches stacks',
  single: 'One deal per order'
};

/** The Settings tiles' one-liners. */
export function taxSummary(b: PricingBody): string {
  const tax = (b.store.tax_bps / 100).toString();
  const stack = b.store.stacking === 'best' ? 'the best deal' : b.store.stacking === 'stack' ? 'deals stack' : 'one deal per order';
  return `Sales tax ${tax}% · ${stack}`;
}
export function feesSummary(b: PricingBody): string {
  const f = b.store.fees;
  const t = b.store.tips;
  const own = KINDS.filter((k) => b.kinds[k]?.fees || b.kinds[k]?.tips).length;
  return [
    `Delivery ${dollars(f?.delivery_fee_cents ?? 0)}`,
    f?.service_fee_cents || f?.service_fee_bps ? `online fee ${dollars(f.service_fee_cents)}${f.service_fee_bps ? ` + ${f.service_fee_bps / 100}%` : ''}` : 'no online fee',
    t?.enabled ? 'asks for tips' : 'no tips',
    own ? `${own} channel${own === 1 ? '' : 's'} with their own` : ''
  ]
    .filter(Boolean)
    .join(' · ');
}
