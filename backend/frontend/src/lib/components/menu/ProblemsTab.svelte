<script lang="ts" module>
  /** GET /me/menu/problems (contract/main.md §2.5zj). */
  export interface MenuProblem {
    check: 'pos' | 'printer' | 'price' | 'mods';
    code: string;
    severity: 'error' | 'review' | 'info';
    message: string;
    item_id?: string;
    item_name?: string;
    category_id?: string;
    category?: string;
    group_id?: string;
    group_name?: string;
    option_id?: string;
    option_name?: string;
    sizes?: string[];
    channels?: string[];
  }
  export interface ProblemsReport {
    version: number;
    counts: Record<string, number>;
    problems: MenuProblem[];
  }
</script>

<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, errText, type Bank, type BankItem, type MenuSize, type Printer } from '$lib/api';
  import type { Picked } from '$lib/store/menuBulk';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Icon, { type IconName } from '$lib/ui/Icon.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import BulkBar from './BulkBar.svelte';

  // Store › Menu › Find problems: what to look at before trusting the menu, one check at a time, each a
  // count and a list. Item problems can be selected and fixed in bulk (printer, prices, stock, category);
  // every row opens the item (or the modifier group) to fix it by hand.
  let {
    bank,
    printers,
    sizes,
    onopenitem,
    onopengroup,
    onchanged
  }: {
    bank: Bank | null;
    printers: Printer[];
    sizes: MenuSize[];
    onopenitem: (itemId: string) => void;
    onopengroup: (groupId: string) => void;
    /** After a bulk fix: the page reloads its lists (and the bank). */
    onchanged: () => Promise<void>;
  } = $props();

  const CHECKS: { id: MenuProblem['check']; title: string; blurb: string; icon: IconName }[] = [
    { id: 'pos', title: 'Not on the counter', blurb: 'Items a cashier can’t ring up at the terminal.', icon: 'monitor' },
    { id: 'printer', title: 'No kitchen printer', blurb: 'Items that print on every station.', icon: 'printer' },
    { id: 'price', title: 'Prices to check', blurb: '$0 prices and sizes that look like a price is missing.', icon: 'percent' },
    { id: 'mods', title: 'Modifier problems', blurb: 'Counter screens, empty groups, duplicates.', icon: 'list' }
  ];

  let report = $state<ProblemsReport | null>(null);
  let err = $state('');
  let loading = $state(false);
  let check = $state<MenuProblem['check']>('pos');
  let q = $state('');
  let showInfo = $state(false);
  let selected = $state<Set<string>>(new Set());

  async function load() {
    loading = true;
    err = '';
    try {
      const first = !report;
      report = await storeApi<ProblemsReport>('/me/menu/problems');
      // Open on the first check that has something (later loads stay where the person is).
      if (first && !report.counts[check]) check = CHECKS.find((c) => report!.counts[c.id])?.id ?? check;
    } catch (e) {
      err = errText(e);
    } finally {
      loading = false;
    }
  }
  onMount(load);

  const inCheck = $derived((report?.problems ?? []).filter((p) => p.check === check));
  const infoCount = $derived(inCheck.filter((p) => p.severity === 'info').length);
  const shown = $derived.by(() => {
    const s = q.trim().toLowerCase();
    return inCheck.filter(
      (p) =>
        (showInfo || p.severity !== 'info') &&
        (!s || [p.item_name, p.category, p.group_name, p.option_name, p.message].some((x) => x?.toLowerCase().includes(s)))
    );
  });
  const shownItems = $derived([...new Set(shown.filter((p) => p.item_id).map((p) => p.item_id!))]);

  const bankItems = $derived(
    new Map<string, BankItem>((bank?.menus ?? []).flatMap((m) => m.categories.flatMap((c) => c.items.map((it) => [it.id, it] as const))))
  );
  const categories = $derived((bank?.menus ?? []).flatMap((m) => m.categories.map((c) => ({ id: c.id, name: c.name, menu: m.name }))));
  const picked = $derived<Picked[]>(
    [...selected].map((id) => bankItems.get(id)).filter((b): b is BankItem => !!b).map((b) => ({ item: b, bank: b }))
  );

  function toggle(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selected = next;
  }
  const allShownSelected = $derived(shownItems.length > 0 && shownItems.every((id) => selected.has(id)));
  function toggleAll() {
    const next = new Set(selected);
    if (allShownSelected) shownItems.forEach((id) => next.delete(id));
    else shownItems.forEach((id) => next.add(id));
    selected = next;
  }
  function pick(c: MenuProblem['check']) {
    check = c;
    selected = new Set();
    q = '';
  }
  async function afterFix(undo = false) {
    if (!undo) selected = new Set();
    await onchanged();
    await load();
  }

  const SEV: Record<MenuProblem['severity'], { tone: 'bad' | 'warn' | 'mute'; label: string }> = {
    error: { tone: 'bad', label: 'Broken' },
    review: { tone: 'warn', label: 'Check' },
    info: { tone: 'mute', label: 'Hidden on purpose' }
  };
  const CHANNEL: Record<string, string> = { web: 'Website', operator: 'Call center', counter: 'Counter' };
  const title = (p: MenuProblem) =>
    p.item_name ?? (p.option_name ? `${p.group_name} › ${p.option_name}` : (p.group_name ?? ''));
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap items-center gap-3">
    <p class="m-0 text-text2 flex-1 min-w-[240px]">
      What to look at before you trust the menu. Select items to fix them together, or open one to fix it by hand.
      {#if report}<span class="text-muted">Checked menu version {report.version}.</span>{/if}
    </p>
    <Button size="sm" icon="refresh" loading={loading} onclick={load}>Check again</Button>
  </div>

  <div class="checks" role="tablist" aria-label="Checks">
    {#each CHECKS as c (c.id)}
      {@const count = report?.counts[c.id] ?? 0}
      <button
        type="button"
        role="tab"
        class="chk"
        class:on={check === c.id}
        aria-selected={check === c.id}
        data-testid="check-{c.id}"
        onclick={() => pick(c.id)}
      >
        <span class="flex items-center gap-2">
          <Icon name={c.icon} size={18} />
          <span class="font-semibold">{c.title}</span>
          <span class="ml-auto count" class:zero={!count} data-testid="count-{c.id}">{report ? count : '…'}</span>
        </span>
        <span class="text-[13px] text-muted text-left">{c.blurb}</span>
      </button>
    {/each}
  </div>

  {#if err}
    <p class="field-error" role="alert">{err}</p>
  {:else if !report}
    <div class="card p-5"><Skeleton lines={6} /></div>
  {:else}
    <section class="card overflow-hidden" aria-label={CHECKS.find((c) => c.id === check)?.title}>
      <div class="flex flex-wrap items-center gap-2.5 px-4 py-3 border-b border-border">
        {#if shownItems.length}
          <label class="check-row font-medium">
            <input type="checkbox" class="check" checked={allShownSelected} onchange={toggleAll} aria-label="Select every item shown" />
            Select all
          </label>
        {/if}
        <SearchInput id="problem-search" label="Search problems" placeholder="Search" bind:value={q} class="flex-[1_1_200px] max-w-[320px]" />
        {#if infoCount}
          <button type="button" class="fchip" aria-pressed={showInfo} onclick={() => (showInfo = !showInfo)}>
            Hidden on purpose ({infoCount})
          </button>
        {/if}
        <span class="ml-auto text-[13px] text-muted">{shown.length} shown</span>
      </div>
      {#if !shown.length}
        <div class="px-4 py-8 text-center text-muted" data-testid="no-problems">
          {inCheck.length ? 'Nothing matches.' : 'Nothing to fix here.'}
          {#if infoCount && !showInfo}<br /><span class="text-[13px]">{infoCount} item{infoCount === 1 ? ' is' : 's are'} hidden from the counter on purpose.</span>{/if}
        </div>
      {:else}
        <ul class="list">
          {#each shown as p, i (p.code + (p.item_id ?? '') + (p.group_id ?? '') + (p.option_id ?? '') + i)}
            <li class="prow" data-testid="problem-row" data-code={p.code}>
              <span class="pt-0.5">
                {#if p.item_id}
                  <input type="checkbox" class="check" checked={selected.has(p.item_id)} onchange={() => toggle(p.item_id!)} aria-label="Select {p.item_name}" />
                {/if}
              </span>
              <div class="min-w-0 flex flex-col gap-1">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="font-semibold">{title(p)}</span>
                  {#if p.category}<span class="text-[13px] text-muted">in {p.category}</span>{/if}
                  <Badge tone={SEV[p.severity].tone}>{SEV[p.severity].label}</Badge>
                </div>
                <p class="m-0 text-[13px] text-text2">{p.message}</p>
                {#if p.sizes?.length || (check === 'pos' && p.channels)}
                  <div class="flex flex-wrap gap-1.5">
                    {#each p.sizes ?? [] as s (s)}<Badge tone="mute">{s}</Badge>{/each}
                    {#if check === 'pos'}
                      {#each p.channels ?? [] as ch (ch)}<Badge tone="info">On the {CHANNEL[ch] ?? ch}</Badge>{/each}
                    {/if}
                  </div>
                {/if}
              </div>
              <span class="flex gap-1.5">
                {#if p.item_id}
                  <Button size="sm" onclick={() => onopenitem(p.item_id!)} aria-label="Open {p.item_name}">Open</Button>
                {:else if p.group_id}
                  <Button size="sm" onclick={() => onopengroup(p.group_id!)} aria-label="Open {p.group_name}">Open group</Button>
                {/if}
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
    {#if check === 'pos' && shown.some((p) => p.code === 'no_counter_button')}
      <p class="help !mt-0">A counter button is added in Menu › Views › Counter (the counter layout), or with the store’s menu document.</p>
    {/if}
  {/if}

  <BulkBar {picked} {printers} {categories} {sizes} onapplied={afterFix} onclear={() => (selected = new Set())} />
</div>

<style>
  .checks {
    display: grid;
    gap: 10px;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr));
  }
  .chk {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 14px 16px;
    border-radius: 14px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
    text-align: left;
    cursor: pointer;
  }
  .chk:hover {
    border-color: var(--brand);
  }
  .chk.on {
    border-color: var(--brand);
    background: var(--brand-soft);
    box-shadow: inset 0 0 0 1px var(--brand);
  }
  .count {
    min-width: 28px;
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--warn-bg, var(--elev));
    color: var(--warn);
    font-weight: 700;
    font-size: 13px;
    text-align: center;
  }
  .count.zero {
    background: var(--elev);
    color: var(--muted);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .prow {
    display: grid;
    grid-template-columns: 22px minmax(0, 1fr) auto;
    gap: 12px;
    align-items: start;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .prow:last-child {
    border-bottom: 0;
  }
</style>
