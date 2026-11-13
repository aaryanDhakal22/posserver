// A view build's needs-attention lines about rows that are gone (contract/main.md §2.5zb build route):
// entries whose bank item was deleted (anchor_gone) and counter layout buttons / flows that no longer
// resolve (layout_*_missing). Each line names its thing by what it still has: a button's own caption and
// where it sits, an entry's name and what it changes, else a short item id. Menu › Views and Menu › Find
// problems both use these, and both fix a line the same way (remove the entry / the button).
import { storeApi, type NeedsAttention } from '$lib/api';

export interface ButtonCell {
  page: number;
  page_name?: string;
  row: number;
  col: number;
}
export interface Attention extends NeedsAttention {
  entry_kind?: string;
  change?: string;
  price_cents?: number;
  where_kind?: 'tab' | 'screen' | 'flow';
  where?: string;
  cell?: ButtonCell;
  caption?: string;
}

export const shortId = (id?: string) => (id ?? '').slice(0, 8);
const q = (s: string) => `“${s}”`;

/** An entry whose item was deleted (the whole entry can go: an item change, not a merge or an added item). */
export const isGoneEntry = (n: Attention) => n.code === 'anchor_gone';
export const removableEntry = (n: Attention) => isGoneEntry(n) && !!n.entry_id && (!n.entry_kind || n.entry_kind === 'item');
/** A counter layout button (or flow) that no longer resolves. */
export const isBrokenButton = (n: Attention) => n.code.startsWith('layout_') && !!n.where_kind;

/** Where a button sits, in words: "page “12" Med”, row 2, column 3". */
export function cellText(c?: ButtonCell): string {
  if (!c) return '';
  return `${c.page_name ? `page ${q(c.page_name)}, ` : ''}row ${c.row + 1}, column ${c.col + 1}`;
}

/** The line in the manager's words: what it is (title) and the rest (detail). */
export function describe(n: Attention, viewLabel = ''): { title: string; detail: string } {
  if (n.code === 'anchor_gone') {
    const lost = n.entry_kind === 'merge' || n.entry_kind === 'augment' ? 'one of its items was deleted' : 'item deleted';
    const what = n.entry_kind === 'merge' ? 'Combined item' : n.entry_kind === 'augment' ? 'Added item' : `${viewLabel ? `${viewLabel} change` : 'Change'}`;
    const detail = [n.change, `item ${shortId(n.item_id)}`].filter(Boolean).join(' · ');
    if (n.name) return { title: `${what} for ${q(n.name)} (${lost})`, detail };
    return { title: `${what} for a deleted item ${shortId(n.item_id)}${n.change ? ` (${n.change})` : ''}`, detail: '' };
  }
  if (isBrokenButton(n)) {
    if (n.where_kind === 'flow') {
      return n.name
        ? { title: `Counter flow of ${q(n.name)}: this view doesn’t show the item`, detail: '' }
        : { title: `Counter flow of a deleted item ${shortId(n.item_id)}`, detail: '' };
    }
    const label = n.caption || n.name || '';
    const btn = `Button${label ? ` ${q(label)}` : ''} on ${n.where_kind} ${q(n.where ?? '')}`;
    let why = n.message;
    switch (n.code) {
      case 'layout_item_missing':
        why = n.name ? `this view doesn’t show ${q(n.name)}` : 'its item was deleted';
        break;
      case 'layout_size_missing':
        why = 'its size no longer exists';
        break;
      case 'layout_option_missing':
        why = 'its option was deleted';
        break;
      case 'layout_deal_missing':
        why = 'its deal was deleted';
        break;
    }
    const id = !label && n.item_id ? `item ${shortId(n.item_id)}` : '';
    return { title: `${btn}: ${why}`, detail: [cellText(n.cell), id].filter(Boolean).join(' · ') };
  }
  return { title: `${n.name ? `${n.name}: ` : ''}${n.message}`, detail: '' };
}

/** POST …/views/:id/layout/remove: the lines' buttons (and flows) out of the counter layout. */
export async function removeButtons(viewId: string, lines: Attention[]): Promise<{ buttons: number; flows: number }> {
  const buttons = lines
    .filter((n) => (n.where_kind === 'tab' || n.where_kind === 'screen') && n.cell)
    .map((n) => ({ where_kind: n.where_kind, where: n.where ?? '', page: n.cell!.page, row: n.cell!.row, col: n.cell!.col }));
  const flows = lines.filter((n) => n.where_kind === 'flow' && n.item_id).map((n) => n.item_id!);
  return storeApi(`/me/menu/views/${viewId}/layout/remove`, { method: 'POST', body: JSON.stringify({ buttons, flows }) });
}

/** DELETE each line's entry (once per entry). */
export async function removeEntries(lines: Attention[]): Promise<number> {
  const ids = [...new Set(lines.filter(removableEntry).map((n) => n.entry_id!))];
  for (const id of ids) await storeApi(`/me/menu/entries/${id}`, { method: 'DELETE' });
  return ids.length;
}
