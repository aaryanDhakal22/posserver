// Store › Menu bulk edit (POST /me/menu/items/bulk, contract/main.md §2.5b "Bulk edit"): the page works out
// each selected item's new values here, sends them in one request (all or nothing), and keeps the old values
// so Undo is the same request with them.
import { storeApi, type BankItem, type Item } from '$lib/api';

export interface BulkChange {
  item_id: string;
  category_id?: string;
  /** "" = the category's printer. */
  printer_id?: string;
  no_kitchen_ticket?: boolean;
  in_stock?: boolean;
  price_cents?: number;
  size_prices?: Record<string, number>;
  tax_rate_bps?: number;
}

/** A selected item with what the bulk edit needs: the item row and its bank sizes. */
export interface Picked {
  item: Item;
  bank?: BankItem;
}

export type PriceMode = 'add' | 'percent' | 'set';
export interface PriceRule {
  mode: PriceMode;
  /** add: cents (may be negative); set: cents; percent: e.g. 10 or -5. */
  amount: number;
  /** Round each new price up to end in .99 (percent and add only). */
  end99: boolean;
}

/** One price after the rule (null = below $0, refused). */
export function applyRule(cents: number, r: PriceRule): number | null {
  let n: number;
  switch (r.mode) {
    case 'set':
      n = r.amount;
      break;
    case 'add':
      n = cents + r.amount;
      break;
    case 'percent':
      n = Math.round((cents * (100 + r.amount)) / 100);
      break;
  }
  // Up to the next X.99 ($14.39 → $14.99, $14.99 stays).
  if (r.end99 && r.mode !== 'set' && n > 0) n = Math.ceil((n + 1) / 100) * 100 - 1;
  return n < 0 ? null : n;
}

/** What a price rule does to one item: its own price, or each of its sizes. */
export interface PricePreview {
  item: Item;
  rows: { label: string; from: number; to: number | null }[];
}

export function previewPrices(picked: Picked[], r: PriceRule, sizeName: (id: string) => string): PricePreview[] {
  return picked.map(({ item, bank }) => {
    const sizes = bank?.sizes ?? [];
    if (sizes.length) {
      return { item, rows: sizes.map((s) => ({ label: sizeName(s.size_id), from: s.price_cents, to: applyRule(s.price_cents, r) })) };
    }
    return { item, rows: [{ label: '', from: item.price_cents, to: applyRule(item.price_cents, r) }] };
  });
}

/** The changes a price rule makes, and the old values (for Undo). */
export function priceChanges(picked: Picked[], r: PriceRule): { changes: BulkChange[]; undo: BulkChange[] } | null {
  const changes: BulkChange[] = [];
  const undo: BulkChange[] = [];
  for (const { item, bank } of picked) {
    const sizes = bank?.sizes ?? [];
    if (sizes.length) {
      const to: Record<string, number> = {};
      const from: Record<string, number> = {};
      for (const s of sizes) {
        const n = applyRule(s.price_cents, r);
        if (n === null) return null;
        to[s.size_id] = n;
        from[s.size_id] = s.price_cents;
      }
      changes.push({ item_id: item.id, size_prices: to });
      undo.push({ item_id: item.id, size_prices: from });
    } else {
      const n = applyRule(item.price_cents, r);
      if (n === null) return null;
      changes.push({ item_id: item.id, price_cents: n });
      undo.push({ item_id: item.id, price_cents: item.price_cents });
    }
  }
  return { changes, undo };
}

/** Sends changes; resolves to the number of items changed. */
export async function applyBulk(changes: BulkChange[]): Promise<number> {
  const r = await storeApi<{ updated: number }>('/me/menu/items/bulk', { method: 'POST', body: JSON.stringify({ changes }) });
  return r.updated;
}
