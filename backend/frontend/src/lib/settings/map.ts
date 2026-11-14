// The settings map (settings-map.json, contract/main.md §2.5zm): every store page and setting, where it is
// and what it is called. The Settings search reads it, deep links (path#anchor) land through it, and the
// other apps get a byte copy (npm run sync-settings-map) so their wording can't drift from these pages.
import raw from './settings-map.json';

export type MapField = { id: string; anchor: string; label: string; api: string; synonyms?: string[] };
export type MapPage = {
  id: string;
  title: string;
  group: string;
  path: string;
  crumb: string;
  synonyms?: string[];
  fields: MapField[];
};
export type SettingsMap = { version: number; pages: MapPage[] };

export const SETTINGS_MAP = raw as SettingsMap;

/** A search hit: a page, or one field on a page. */
export type Hit = { page: MapPage; field?: MapField; href: string; text: string; score: number };

const norm = (s: string) =>
  s
    .toLowerCase()
    .replace(/[’']/g, '')
    .replace(/[^a-z0-9%$]+/g, ' ')
    .trim();

export const hrefOf = (page: MapPage, field?: MapField) => page.path + (field ? '#' + field.anchor : '');

/** "Store › Settings › Counter orders › Ticket numbers start at". */
export const crumbOf = (page: MapPage, field?: MapField) => page.crumb + (field ? ' › ' + field.label : '');

function score(q: string, words: string[], label: string, extra: string[]): number {
  const l = norm(label);
  if (l === q) return 100;
  if (l.startsWith(q)) return 80;
  let best = 0;
  if (l.includes(q)) best = 60;
  for (const s of extra) {
    const n = norm(s);
    if (n === q) best = Math.max(best, 70);
    else if (n.startsWith(q)) best = Math.max(best, 50);
    else if (n.includes(q)) best = Math.max(best, 35);
  }
  if (!best && words.length > 1) {
    const hay = [l, ...extra.map(norm)].join(' ');
    if (words.every((w) => hay.includes(w))) best = 30;
  }
  return best;
}

/** Pages and fields matching a query (title, label, synonyms), best first. */
export function searchSettings(query: string, map: SettingsMap = SETTINGS_MAP, limit = 12): Hit[] {
  const q = norm(query);
  if (!q) return [];
  const words = q.split(' ');
  const hits: Hit[] = [];
  for (const p of map.pages) {
    const ps = score(q, words, p.title, p.synonyms ?? []);
    if (ps) hits.push({ page: p, href: hrefOf(p), text: p.crumb, score: ps + 5 });
    for (const f of p.fields) {
      const fs = score(q, words, f.label, [...(f.synonyms ?? []), f.anchor.replace(/_/g, ' ')]);
      const viaPage = !fs && ps >= 70 ? 20 : 0; // "radius" finds the map page and its fields
      if (fs || viaPage) hits.push({ page: p, field: f, href: hrefOf(p, f), text: crumbOf(p, f), score: fs || viaPage });
    }
  }
  return hits.sort((a, b) => b.score - a.score || a.text.localeCompare(b.text)).slice(0, limit);
}

/** The map page a URL path is (with or without the trailing slash). */
export function pageAt(path: string, map: SettingsMap = SETTINGS_MAP): MapPage | undefined {
  const p = path.replace(/\/+$/, '');
  return map.pages.find((x) => x.path === p);
}

// --- finding a field on its page --------------------------------------------------------------

const textOf = (el: Element): string => {
  let out = '';
  for (const n of Array.from(el.childNodes)) {
    if (n.nodeType === 3) out += n.textContent ?? '';
    else if (n instanceof Element && !n.matches('.sr-only, .badge, [aria-hidden="true"]')) out += textOf(n);
  }
  return out;
};
const same = (a: string, b: string) => norm(a) === norm(b);

/**
 * The element a field is on its page: the element with the anchor as its id, else the label, heading,
 * tab or radio group whose text is the field's label (a switch row's label counts by its text).
 */
export function findField(field: MapField, root: ParentNode = document): HTMLElement | null {
  const byId = root.querySelector<HTMLElement>(`[id="${CSS.escape(field.anchor)}"]`);
  if (byId) return byId;
  const cands = root.querySelectorAll<HTMLElement>('label, .lbl, h2, h3, legend, [role=tab], [role=radiogroup][aria-label]');
  for (const el of Array.from(cands)) {
    const t = el.matches('[role=radiogroup]') ? el.getAttribute('aria-label') ?? '' : textOf(el);
    if (same(t, field.label)) return el;
  }
  return null;
}

/** The block to light up for a found element. */
function blockOf(el: HTMLElement): HTMLElement {
  if (el.matches('h2, h3')) return (el.closest('section') as HTMLElement | null) ?? el;
  if (el.matches('[role=tab]')) return el;
  const box = el.closest<HTMLElement>('[data-setting], .uf, .sw-row, .choice');
  if (box) return box;
  if (el.matches('label, .lbl') && el.parentElement) return el.parentElement;
  return el;
}

/** Scrolls to a field, lights it up for a moment and focuses its control (a tab is opened). */
export function showField(el: HTMLElement) {
  if (el.matches('[role=tab]') && el.getAttribute('aria-selected') !== 'true') el.click();
  const block = blockOf(el);
  block.scrollIntoView({ block: 'center', behavior: 'smooth' });
  block.classList.remove('setting-hit');
  void block.offsetWidth; // restart the animation
  block.classList.add('setting-hit');
  setTimeout(() => block.classList.remove('setting-hit'), 2600);
  const ctl =
    (el.matches('label') && (el as HTMLLabelElement).control) ||
    block.querySelector<HTMLElement>('input:not([type=hidden]), select, textarea, [role=switch], [role=radio]');
  (ctl as HTMLElement | null)?.focus({ preventScroll: true });
}

/**
 * Lands a deep link (path#anchor): waits for the page's field to appear (pages load their data first),
 * then shows it. Returns a stop function. An anchor not in the map still works when an element has it as id.
 */
export function landHash(path: string, hash: string, timeoutMs = 6000): () => void {
  const anchor = decodeURIComponent(hash.replace(/^#/, ''));
  if (!anchor) return () => {};
  const page = pageAt(path);
  const field: MapField = page?.fields.find((f) => f.anchor === anchor) ?? { id: anchor, anchor, label: '\u0000', api: '' };
  let done = false;
  const tryNow = () => {
    if (done) return true;
    const el = findField(field);
    if (!el) return false;
    done = true;
    showField(el);
    return true;
  };
  if (tryNow()) return () => {};
  const obs = new MutationObserver(() => {
    if (tryNow()) obs.disconnect();
  });
  obs.observe(document.body, { childList: true, subtree: true });
  const t = setTimeout(() => obs.disconnect(), timeoutMs);
  return () => {
    done = true;
    obs.disconnect();
    clearTimeout(t);
  };
}
