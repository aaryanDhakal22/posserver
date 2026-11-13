// Store › Orders: what an order is called, who took it, and its detail (GET /store/v1/me/orders/:id).
// One place for the rules the table, the Overview and the detail panel share (the owner, 2026-11-13).
import type { StoreOrder } from '$lib/api';

export interface OrderLine {
  id: string;
  name: string;
  size?: string;
  quantity: number;
  unit_price_cents: number;
  line_total_cents: number;
  notes?: string;
  portion?: string;
  mode?: string;
  coupon?: string;
  coupon_no?: number;
  sku?: string;
  printer_id?: string;
  routing?: string;
  children?: OrderLine[];
}

export interface FullOrder extends StoreOrder {
  source: string;
  status: string;
  currency: string;
  money: {
    subtotal_cents: number;
    tax_cents: number;
    tip_cents: number;
    total_cents: number;
    fees?: { type: string; label?: string; amount_cents: number; driver_cents?: number }[];
    discounts?: { type: string; code?: string; label?: string; amount_cents: number }[];
  };
  customer: { display_name?: string; phone?: string; email?: string };
  fulfillment: {
    type: string;
    promised_at?: string;
    table_id?: string;
    address?: { line1?: string; line2?: string; city?: string; state?: string; postal_code?: string; notes?: string };
  };
  items: OrderLine[];
  payments?: { method: string; amount_cents: number; tip_cents?: number; last4?: string }[];
  notes?: string;
  placed_at: string;
}

export interface DetailPayment {
  id: string;
  method: string;
  status: string;
  amount_cents: number;
  tip_cents: number;
  last4?: string;
  auth_code?: string;
  origin?: string;
  refund_of?: string;
  approved_by?: string;
  reason?: string;
  flags: string[];
  staff_name?: string;
  batch_id?: number | null;
  captured_at?: string | null;
  received_at: string;
}

export interface OrderDetail {
  order: FullOrder;
  received_at: string;
  updated_at: string;
  agent_ack_at?: string | null;
  agent_status?: AgentStatus | null;
  batch_id?: number | null;
  status_history: { from: string; to: string; at: string; actor: string; reason?: string }[];
  payments: DetailPayment[];
  journal: { at: string; action: string; staff_name?: string; device_id?: string; detail: Record<string, unknown> }[];
  flags: { at: string; code: string; subject: string; detail: Record<string, unknown>; resolved_at?: string | null }[];
  card_hold?: {
    gateway: string; status: string; amount_cents: number; tip_cents: number; added_tip_cents: number;
    captured_cents: number; refunded_cents: number; due_at: string; captured_at?: string | null;
    cancelled_at?: string | null; last_error?: string;
  } | null;
  channel?: { kind: string; name: string; client_order_id?: string; operator?: string } | null;
  staff?: { id: string; name: string; role: string } | null;
  printers: Record<string, { name: string; is_receipt: boolean }>;
}

/** The Pi's own floor status for the order (agent contract §2.1, as it last synced it). */
export interface AgentStatus {
  state?: string;
  arrival_date?: string;
  printed_date?: string;
  accepted_at?: string;
  ready_at?: string;
  out_at?: string;
  delivered_at?: string;
  cancelled_at?: string;
  finished_at?: string;
  finished_by?: string;
  paid_at?: string;
  prep_minutes?: number;
  kitchen_number?: number;
  driver_name?: string;
  auto_accepted?: boolean;
  early_kitchen?: boolean;
  early_receipt?: boolean;
  future?: boolean;
  came_back?: boolean;
}

// ---------------------------------------------------------------------------------------------------
// Names

/** Words that name an order type, never a customer ("Counter" on a walk-in check). */
const NOT_A_NAME = new Set(['counter', 'walk in', 'walk-in', 'walkin', 'pickup', 'pick up', 'delivery', 'takeout', 'take out', 'dine in', 'guest', 'customer']);

/** A name typed in capitals ("DANNII GORDON") in Title Case, like the receipts; mixed case stays as typed. */
export function titleCase(s: string): string {
  if (!s || /[a-z]/.test(s)) return s;
  return s.toLowerCase().replace(/(^|[\s\-'’.(])(\p{L})/gu, (_, p: string, c: string) => p + c.toUpperCase());
}

/** The customer's name, or '' (one rule everywhere: never an order type, never "—"). */
export function customerName(o: Pick<StoreOrder, 'customer'>): string {
  const n = (o.customer?.display_name ?? '').trim();
  if (!n || NOT_A_NAME.has(n.toLowerCase())) return '';
  return titleCase(n);
}

/** How the order came in: Counter · Call center · Online (the ordering vendor) · Website · … */
export function channelLabel(o: Pick<StoreOrder, 'source_metadata'> & { source?: string }): string {
  const meta = o.source_metadata ?? {};
  switch (o.source) {
    case 'counter':
      return meta.device_kind === 'callcenter' ? 'Call center' : 'Counter';
    case 'online':
    case 'vendor':
      return 'Online';
    case 'web':
      return 'Website';
    case 'operator':
      return 'Call center';
    case 'mobile':
      return 'App';
    case 'kiosk':
      return 'Kiosk';
    case 'pos':
      return 'POS';
    default:
      return o.source ? o.source[0].toUpperCase() + o.source.slice(1) : '';
  }
}

/**
 * The order's number as the store says it. Counter and call-center orders are numbered by the counter (their
 * check / ticket number, 1, 2, 3… each day); online and website orders get a kitchen number when the kitchen
 * accepts them (from 1100). Before that (or with none): a short code from the order's id.
 */
export function orderNumber(o: Pick<StoreOrder, 'id' | 'kitchen_order_number'>): string {
  if (o.kitchen_order_number != null) return String(o.kitchen_order_number);
  return shortCode(o.id);
}

/** What the number is: "Ticket" for the counter's own numbering, "Kitchen" for the online range. */
export function numberKind(o: Pick<StoreOrder, 'kitchen_order_number'> & { source?: string }): string {
  if (o.kitchen_order_number == null) return 'Code';
  return o.source === 'counter' ? 'Ticket' : 'Kitchen';
}

/** A short, stable code for an order (the last 6 characters of its id, upper case). */
export function shortCode(id: string): string {
  return 'Q-' + id.replace(/-/g, '').slice(-6).toUpperCase();
}

/** The channel's own order reference worth showing (the vendor's order number), or ''. */
export function externalRef(o: Pick<StoreOrder, 'source_event_id'> & { source?: string }): string {
  if ((o.source === 'online' || o.source === 'vendor') && o.source_event_id) return '#' + o.source_event_id;
  return '';
}

export function typeLabel(t?: string): string {
  switch (t) {
    case 'dine_in':
      return 'Dine in';
    case 'counter':
      return 'Walk-in';
    case '':
    case undefined:
      return '';
    default:
      return t[0].toUpperCase() + t.slice(1).replaceAll('_', ' ');
  }
}

/** A tender as staff say it. */
export function tenderLabel(method: string): string {
  switch (method) {
    case 'cash':
      return 'Cash';
    case 'card':
      return 'Card (reader)';
    case 'manual_card':
      return 'Card, manually entered';
    case 'square_app':
      return 'Card on the Square app';
    case 'comp':
      return 'Comp';
    case 'other':
      return 'Other';
    case 'door_tip':
      return 'Door tip';
    default:
      return method ? method[0].toUpperCase() + method.slice(1).replaceAll('_', ' ') : 'Payment';
  }
}

export function phoneText(p?: string): string {
  const d = (p ?? '').replace(/\D/g, '');
  if (d.length === 10) return `(${d.slice(0, 3)}) ${d.slice(3, 6)}-${d.slice(6)}`;
  if (d.length === 11 && d[0] === '1') return `(${d.slice(1, 4)}) ${d.slice(4, 7)}-${d.slice(7)}`;
  return p ?? '';
}

/** A time with the day when it isn't today ("5:27 PM", "Nov 12, 5:27 PM"). */
export function whenText(t?: string | null): string {
  if (!t) return '';
  const d = new Date(t);
  if (Number.isNaN(d.getTime())) return '';
  const n = new Date();
  const time = d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  if (d.toDateString() === n.toDateString()) return time;
  const y = new Date(n);
  y.setDate(n.getDate() - 1);
  if (d.toDateString() === y.toDateString()) return `Yesterday, ${time}`;
  return d.toLocaleDateString([], { month: 'short', day: 'numeric', ...(d.getFullYear() === n.getFullYear() ? {} : { year: 'numeric' }) }) + ', ' + time;
}

// ---------------------------------------------------------------------------------------------------
// The timeline

export interface TimelineStep {
  at: string;
  label: string;
  detail?: string;
  tone?: 'ok' | 'bad' | 'mute';
}

const JOURNAL_LABEL: Record<string, string> = {
  reprint: 'Receipt reprinted',
  price_change: 'Price changed',
  discount: 'Discount',
  employee_meal: 'Employee meal',
  comp: 'Comped',
  'order.picked_up': 'Picked up',
  'draft.take_over': 'Taken over on another terminal',
  'customer.add': 'Customer added',
  'order.placed': 'Placed at the terminal',
  'order.changed': 'Changed',
  'order.void': 'Voided',
  'order.reprint': 'Receipt reprinted',
  'order.kitchen': 'Sent to the kitchen again',
  'order.paid': 'Paid',
  'order.time': 'Time changed',
  'delivery.out_of_area': 'Outside the delivery area (acknowledged)'
};

/** Everything that happened to the order, oldest first (the Pi's times where it sent them). */
export function timeline(d: OrderDetail): TimelineStep[] {
  const o = d.order;
  const st = d.agent_status ?? {};
  const out: TimelineStep[] = [];
  const add = (at: string | null | undefined, label: string, detail?: string, tone?: TimelineStep['tone']) => {
    if (at) out.push({ at, label, detail, tone });
  };
  add(o.placed_at, 'Placed', channelLabel(o) + (customerName(o) ? ` · ${customerName(o)}` : ''));
  if (Math.abs(new Date(d.received_at).getTime() - new Date(o.placed_at).getTime()) > 60_000)
    add(d.received_at, 'Synced to quicc', 'the Pi sent it later than it was placed', 'mute');
  if (st.accepted_at)
    add(st.accepted_at, st.auto_accepted ? 'Accepted (automatically)' : 'Accepted', [
      o.kitchen_order_number != null ? `${numberKind(o)} ${o.kitchen_order_number}` : '',
      st.prep_minutes ? `${st.prep_minutes} min` : ''
    ].filter(Boolean).join(' · '));
  add(st.printed_date, 'Tickets printed');
  add(st.ready_at, 'Ready');
  add(st.out_at, 'Out for delivery', st.driver_name || o.driver_name || undefined);
  add(st.delivered_at, 'Delivered', st.driver_name || o.driver_name || undefined, 'ok');
  if (!st.delivered_at) add(st.finished_at, o.fulfillment?.type === 'delivery' ? 'Finished' : 'Picked up', st.finished_by, 'ok');
  add(st.paid_at, 'Paid');
  const cancel = [...d.status_history].reverse().find((s) => s.to === 'cancelled');
  if (cancel) add(st.cancelled_at || cancel.at, 'Cancelled', cancel.reason?.replace(/^void:\s*/, '') || undefined, 'bad');
  // Status changes the Pi sent without its own times (older Pis, online orders).
  const have = new Set(out.map((s) => s.label));
  const MAP: Record<string, string> = { accepted: 'Accepted', ready: 'Ready', on_delivery: 'Out for delivery', completed: 'Delivered', rejected: 'Rejected' };
  for (const s of d.status_history) {
    const l = MAP[s.to];
    if (l && !have.has(l) && !(l === 'Accepted' && have.has('Accepted (automatically)'))) add(s.at, l);
  }
  for (const j of d.journal) {
    // The order's own steps are above already (placed, the void with its reason).
    if (j.action === 'order.placed' || ((j.action === 'void' || j.action === 'order.void') && cancel)) continue;
    add(j.at, JOURNAL_LABEL[j.action] ?? j.action.replace(/[._]/g, ' '), j.staff_name || undefined, 'mute');
  }
  for (const p of d.payments) {
    const at = p.captured_at || p.received_at;
    const amt = (p.amount_cents < 0 ? '−' : '') + '$' + (Math.abs(p.amount_cents) / 100).toFixed(2);
    if (p.amount_cents < 0) add(at, 'Refund', `${amt} ${tenderLabel(p.method)}${p.staff_name ? ' · ' + p.staff_name : ''}`, 'bad');
    else if (p.status === 'captured' || p.status === 'authorized')
      add(at, p.amount_cents === 0 && p.tip_cents > 0 ? 'Tip added' : 'Payment', `${p.amount_cents === 0 ? '$' + (p.tip_cents / 100).toFixed(2) : amt} ${tenderLabel(p.method)}${p.staff_name ? ' · ' + p.staff_name : ''}`);
  }
  return out.sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime());
}

/** The kitchen tickets the order's lines route to: station name → line names; receipt-only lines apart. */
export function stations(d: OrderDetail): { name: string; lines: string[] }[] {
  const by = new Map<string, string[]>();
  for (const l of d.order.items ?? []) {
    const p = l.printer_id ? d.printers[l.printer_id] : undefined;
    const key = l.routing === 'none' ? 'Receipt only' : p ? p.name : 'Every station (no printer set)';
    by.set(key, [...(by.get(key) ?? []), `${l.quantity > 1 ? l.quantity + '× ' : ''}${l.name}`]);
  }
  return [...by.entries()].map(([name, lines]) => ({ name, lines }));
}

/** A modifier line as the kitchen reads it: "1st half: Pepperoni", "No Salt". */
export function childLabel(c: OrderLine): string {
  const half = c.portion === 'first' || c.portion === 'left' ? '1st half: ' : c.portion === 'second' || c.portion === 'right' ? '2nd half: ' : '';
  let name = c.name.replace(/\s*\((1st|2nd) half\)\s*$/i, '');
  if (c.mode === 'no' && !/^no\b/i.test(name)) name = 'No ' + name;
  if (c.mode === 'only' && !/^only\b/i.test(name)) name = 'Only ' + name;
  return half + name;
}
