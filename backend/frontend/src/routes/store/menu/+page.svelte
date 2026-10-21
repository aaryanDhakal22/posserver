<script lang="ts">
  import { base } from '$app/paths';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import {
    storeApi, ApiError, dollars, toCents,
    type Menu, type Category, type Item, type Printer, type LivePrinters, type ItemGroupLink,
    type UnmatchedName, type MenuImportResult, fmtTime
  } from '$lib/api';
  import ModifiersTab from '$lib/components/menu/ModifiersTab.svelte';
  import ItemEditor from '$lib/components/menu/ItemEditor.svelte';

  let loading = $state(true);
  let tab = $state<'overview' | 'modifiers'>('overview');
  let banner = $state<{ msg: string; kind: 'ok' | 'error' } | null>(null);

  let menus = $state<Menu[]>([]);
  let activeMenu = $state<Menu | null>(null);
  let categories = $state<Category[]>([]);
  let itemsByCat = $state<Record<string, Item[]>>({});
  let printers = $state<Printer[]>([]);
  let live = $state<LivePrinters | null>(null);
  let unmatched = $state<UnmatchedName[]>([]);
  let editing = $state<Item | null>(null);

  // new-entity inputs
  let newMenuName = $state('');
  let newCatName = $state('');
  let newItemName = $state<Record<string, string>>({});
  let expanded = $state<Record<string, boolean>>({});

  // Errors stay until dismissed or replaced; other messages auto-hide. The
  // previous timer is cleared so a new message isn't cut short by an old one.
  let bannerTimer: ReturnType<typeof setTimeout> | undefined;
  function toast(msg: string, kind: 'ok' | 'error' = 'ok') {
    clearTimeout(bannerTimer);
    banner = { msg, kind };
    bannerTimer = kind === 'error' ? undefined : setTimeout(() => (banner = null), 3500);
  }
  function dismissBanner() {
    clearTimeout(bannerTimer);
    banner = null;
  }
  function err(e: unknown) {
    toast(e instanceof ApiError ? e.message : String(e), 'error');
  }
  const printerName = (id: string | null) =>
    id ? (printers.find((p) => p.id === id)?.name ?? '—') : '';

  // Vendor item names from the last 14 days that print on every station
  // ("unmatched": no menu item of that name) or nowhere specific
  // ("unassigned": no printer on the item or its category).
  async function loadUnmatched() {
    unmatched = await storeApi<UnmatchedName[]>('/me/menu/unmatched?days=14').catch(() => []);
  }

  async function loadMenus() {
    menus = await storeApi<Menu[]>('/me/menu/menus');
    if (!activeMenu && menus.length) activeMenu = menus[0];
    if (activeMenu) await loadCategories();
  }
  async function loadCategories() {
    if (!activeMenu) return;
    categories = await storeApi<Category[]>(`/me/menu/menus/${activeMenu.id}/categories`);
    for (const cat of categories) await loadItems(cat.id);
  }
  async function loadItems(catID: string) {
    itemsByCat[catID] = await storeApi<Item[]>(`/me/menu/categories/${catID}/items`);
  }

  onMount(async () => {
    try {
      await storeApi('/session'); // 401 → login
      printers = await storeApi<Printer[]>('/me/printers');
      live = await storeApi<LivePrinters>('/me/printers/live').catch(() => null);
      await loadMenus();
      await loadUnmatched();
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return goto(base + '/login');
      err(e);
    } finally {
      loading = false;
    }
  });

  async function createMenu() {
    if (!newMenuName.trim()) return;
    try {
      await storeApi('/me/menu/menus', { method: 'POST', body: JSON.stringify({ name: newMenuName.trim() }) });
      newMenuName = '';
      await loadMenus();
      toast('menu created');
    } catch (e) { err(e); }
  }
  async function selectMenu(m: Menu) { activeMenu = m; await loadCategories(); }
  async function toggleMenu(m: Menu) {
    try { await storeApi(`/me/menu/menus/${m.id}`, { method: 'PATCH', body: JSON.stringify({ name: m.name, description: m.description, available: !m.available }) }); await loadMenus(); }
    catch (e) { err(e); }
  }
  async function deleteMenu(m: Menu) {
    if (!confirm(`Delete menu "${m.name}" and all its categories/items?`)) return;
    try { await storeApi(`/me/menu/menus/${m.id}`, { method: 'DELETE' }); if (activeMenu?.id === m.id) activeMenu = null; await loadMenus(); }
    catch (e) { err(e); }
  }

  async function createCategory() {
    if (!activeMenu || !newCatName.trim()) return;
    try {
      await storeApi(`/me/menu/menus/${activeMenu.id}/categories`, { method: 'POST', body: JSON.stringify({ name: newCatName.trim() }) });
      newCatName = '';
      await loadCategories();
    } catch (e) { err(e); }
  }
  async function saveCategory(cat: Category) {
    try { await storeApi(`/me/menu/categories/${cat.id}`, { method: 'PATCH', body: JSON.stringify({ name: cat.name, description: cat.description, available: cat.available, default_printer_id: cat.default_printer_id }) }); toast('category saved'); }
    catch (e) { err(e); }
  }
  async function deleteCategory(cat: Category) {
    if (!confirm(`Delete category "${cat.name}" and its items?`)) return;
    try { await storeApi(`/me/menu/categories/${cat.id}`, { method: 'DELETE' }); await loadCategories(); }
    catch (e) { err(e); }
  }

  async function addItem(cat: Category) {
    const name = (newItemName[cat.id] || '').trim();
    if (!name) return;
    try {
      await storeApi(`/me/menu/categories/${cat.id}/items`, { method: 'POST', body: JSON.stringify({ name, price_cents: 0 }) });
      newItemName[cat.id] = '';
      await loadItems(cat.id);
    } catch (e) { err(e); }
  }
  async function toggleStock(it: Item) {
    try {
      await storeApi(`/me/menu/items/${it.id}`, { method: 'PATCH', body: JSON.stringify({ ...it, in_stock: !it.in_stock }) });
      await loadItems(it.category_id);
    } catch (e) { err(e); }
  }
  async function deleteItem(it: Item) {
    if (!confirm(`Delete "${it.name}"?`)) return;
    try { await storeApi(`/me/menu/items/${it.id}`, { method: 'DELETE' }); await loadItems(it.category_id); }
    catch (e) { err(e); }
  }
  async function onItemSaved(catID: string) {
    editing = null;
    await loadItems(catID);
    toast('item saved');
  }

  // --- CSV import / export ---
  // Import = pick a file → dry-run preview → Apply (or Replace everything).
  let csvInput = $state<HTMLInputElement | null>(null);
  let csvText = $state('');
  let csvName = $state('');
  let csvReplace = $state(false);
  let csvPreview = $state<MenuImportResult | null>(null);
  let csvBusy = $state(false);
  // The last import failure (bad row, unknown printer…), shown next to the
  // import controls until the next attempt — not just a passing toast.
  let csvError = $state('');
  let csvErrorOnApply = $state(false); // the failure happened while applying, not previewing
  const errText = (e: unknown) => (e instanceof ApiError ? e.message : String(e));

  async function postCSV(dryRun: boolean): Promise<MenuImportResult> {
    const q = new URLSearchParams({ dry_run: dryRun ? '1' : '0', replace: csvReplace ? '1' : '0' });
    return storeApi<MenuImportResult>(`/me/menu/import?${q}`, {
      method: 'POST', body: csvText, headers: { 'Content-Type': 'text/csv' }
    });
  }
  async function previewCSV() {
    if (csvBusy) return;
    csvBusy = true;
    csvError = '';
    try { csvPreview = await postCSV(true); }
    catch (e) { csvPreview = null; csvError = errText(e); csvErrorOnApply = false; }
    finally { csvBusy = false; }
  }
  async function pickCSV(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f || csvBusy) return;
    csvName = f.name;
    csvText = await f.text();
    csvReplace = false;
    (e.target as HTMLInputElement).value = '';
    await previewCSV();
  }
  async function applyCSV() {
    if (csvBusy) return;
    if (csvReplace && !confirm('Replace deletes every menu here (categories, items, item printers) and rebuilds it from the file. Continue?')) return;
    csvBusy = true;
    csvError = '';
    try {
      const r = await postCSV(false);
      csvPreview = null; csvText = '';
      activeMenu = null;
      await loadMenus();
      await loadUnmatched();
      toast(`imported: ${r.items} new items, ${r.items_updated} updated, ${r.item_printers + r.category_printers} printer changes`);
    } catch (e) { csvError = errText(e); csvErrorOnApply = true; }
    finally { csvBusy = false; }
  }
  const csvSummary = (r: MenuImportResult) => [
    r.menus_deleted && `${r.menus_deleted} menu(s) deleted`,
    r.menus && `${r.menus} menu(s)`,
    r.categories && `${r.categories} categor${r.categories === 1 ? 'y' : 'ies'}`,
    r.items && `${r.items} new item(s)`,
    r.items_updated && `${r.items_updated} item(s) updated`,
    (r.item_printers || r.category_printers) && `${r.item_printers + r.category_printers} printer change(s)`
  ].filter(Boolean).join(' · ') || 'no changes';

  // --- drag reorder (native HTML5) ---
  let dragCat = $state<string | null>(null);
  async function reorderItems(catID: string, from: number, to: number) {
    const arr = [...(itemsByCat[catID] || [])];
    const [m] = arr.splice(from, 1);
    arr.splice(to, 0, m);
    itemsByCat[catID] = arr;
    try { await storeApi('/me/menu/items/reorder', { method: 'POST', body: JSON.stringify({ ids: arr.map((x) => x.id) }) }); }
    catch (e) { err(e); }
  }
</script>

<div class="flex items-center justify-between mb-4">
  <div>
    <div class="text-lg font-semibold">Menu Manager</div>
    <div class="text-xs text-slate-500 dark:text-slate-400">categories, items, modifiers, printer routing</div>
  </div>
  <div class="flex gap-1">
    <a class="btn btn-ghost btn-xs" href="/store/v1/me/menu/export.csv" download="menu.csv" title="menu,category,item,price,printer,description">Export CSV</a>
    <button class="btn btn-ghost btn-xs" disabled={csvBusy} onclick={() => csvInput?.click()} title="Add/update items from a CSV — preview first">Import CSV</button>
    <input type="file" accept=".csv,text/csv" class="hidden" disabled={csvBusy} bind:this={csvInput} onchange={pickCSV} />
    <button class="btn btn-xs {tab === 'overview' ? 'btn-dark' : 'btn-ghost'}" onclick={() => (tab = 'overview')}>Overview</button>
    <button class="btn btn-xs {tab === 'modifiers' ? 'btn-dark' : 'btn-ghost'}" onclick={() => (tab = 'modifiers')}>Modifiers</button>
  </div>
</div>

{#if banner}
  <div class="rounded-md border px-4 py-2.5 text-sm mb-4 flex items-start gap-3 {banner.kind === 'error'
    ? 'border-rose-300 bg-rose-50 text-rose-900 dark:border-rose-900/60 dark:bg-rose-950/60 dark:text-rose-200'
    : 'border-emerald-300 bg-emerald-50 text-emerald-900 dark:border-emerald-900/60 dark:bg-emerald-950/50 dark:text-emerald-200'}">
    <span class="flex-1 min-w-0 break-words">{banner.msg}</span>
    <button type="button" class="shrink-0 opacity-60 hover:opacity-100 leading-none text-base" aria-label="dismiss" title="dismiss" onclick={dismissBanner}>×</button>
  </div>
{/if}

{#if csvError}
  <div class="card px-4 py-3 mb-4 border-rose-300 dark:border-rose-500/40 text-rose-900 dark:text-rose-200">
    <div class="flex items-baseline justify-between gap-2">
      <div class="font-semibold text-sm">Import failed{csvName ? ` — ${csvName}` : ''}</div>
      <button class="btn btn-ghost btn-xs" onclick={() => (csvError = '')}>dismiss</button>
    </div>
    <pre class="text-xs whitespace-pre-wrap break-words my-1 font-mono">{csvError}</pre>
    <p class="text-xs opacity-80">
      {csvErrorOnApply
        ? 'The import may have stopped part-way. Fix the problem and import the same file again — a re-import picks up where it stopped.'
        : 'Nothing was changed. Fix the file (or add the missing printers in /app/store) and use Import CSV again.'}
    </p>
  </div>
{/if}

{#if csvPreview}
  <div class="card px-4 py-3 mb-4 border-sky-300 dark:border-sky-500/40">
    <div class="flex items-baseline justify-between gap-2">
      <div class="font-semibold text-sm">Import preview — {csvName} ({csvPreview.rows} rows)</div>
      <button class="btn btn-ghost btn-xs" onclick={() => { csvPreview = null; csvText = ''; }}>cancel</button>
    </div>
    <div class="text-sm my-1">{csvSummary(csvPreview)}</div>
    <p class="text-xs text-slate-500 dark:text-slate-400 mb-2">
      Items match by name (case/spaces ignored). Blank price/printer cells keep what's there; nothing is
      deleted unless you tick Replace.
    </p>
    {#if csvPreview.changes.length}
      <pre class="text-xs max-h-64 overflow-auto rounded bg-slate-50 dark:bg-slate-900 p-2 mb-2">{csvPreview.changes.join('\n')}</pre>
    {/if}
    <div class="flex items-center gap-3">
      <label class="text-sm flex items-center gap-1">
        <input type="checkbox" disabled={csvBusy} bind:checked={csvReplace} onchange={previewCSV} /> Replace — delete all menus first
      </label>
      <button class="btn btn-primary btn-xs" disabled={csvBusy} onclick={applyCSV}>{csvReplace ? 'Replace menu' : 'Apply'}</button>
    </div>
  </div>
{/if}

{#if loading}
  <div class="text-slate-500 dark:text-slate-400">Loading…</div>
{:else if tab === 'modifiers'}
  <ModifiersTab {toast} />
{:else}
  <div class="grid grid-cols-1 lg:grid-cols-4 gap-4">
    <!-- Menu rail -->
    <aside class="card px-3 py-3 lg:col-span-1 h-max">
      <div class="text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400 mb-2">Menus</div>
      <div class="space-y-1">
        {#each menus as m (m.id)}
          <div class="flex items-center gap-1">
            <button class="btn btn-xs flex-1 justify-start {activeMenu?.id === m.id ? 'btn-dark' : 'btn-ghost'}" onclick={() => selectMenu(m)}>
              {m.name}{#if !m.available}<span class="ml-1 opacity-60">(off)</span>{/if}
            </button>
            <button class="btn btn-ghost btn-xs" title="toggle available" onclick={() => toggleMenu(m)}>{m.available ? '●' : '○'}</button>
            <button class="btn btn-danger btn-xs" onclick={() => deleteMenu(m)}>✕</button>
          </div>
        {/each}
      </div>
      <div class="flex gap-1 mt-3 pt-3 border-t border-slate-200 dark:border-slate-800">
        <input class="field flex-1" placeholder="new menu" bind:value={newMenuName} />
        <button class="btn btn-primary btn-xs" onclick={createMenu}>+</button>
      </div>
    </aside>

    <!-- Categories + items -->
    <section class="lg:col-span-3 space-y-3">
      {#if unmatched.length}
        <div class="card px-4 py-3 border-amber-300 dark:border-amber-500/40">
          <div class="flex items-baseline justify-between">
            <div class="font-semibold text-sm">Not reaching a kitchen printer (last 14 days)</div>
            <button class="btn btn-ghost btn-xs" onclick={loadUnmatched}>refresh</button>
          </div>
          <p class="text-xs text-slate-500 dark:text-slate-400 mb-2">
            <b>unmatched</b> = no menu item with that exact name (it prints on every station) ·
            <b>no printer</b> = the item and its category have no printer. Rename the menu item to the
            vendor's name, or set a printer.
          </p>
          <div class="flex flex-wrap gap-1">
            {#each unmatched as u (u.name)}
              <span class="chip {u.routing === 'unmatched' ? 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-200' : 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200'}"
                title="{u.orders} order(s), last {fmtTime(u.last_seen)}">
                {u.name} · {u.routing === 'unmatched' ? 'unmatched' : 'no printer'} · {u.orders}×
              </span>
            {/each}
          </div>
        </div>
      {/if}
      {#if !activeMenu}
        <div class="text-sm text-slate-500 dark:text-slate-400">Create or select a menu.</div>
      {:else}
        {#each categories as cat (cat.id)}
          <div class="card px-5 py-4">
            <div class="flex items-center justify-between gap-2">
              <button class="flex items-center gap-2 text-left" onclick={() => (expanded[cat.id] = !expanded[cat.id])}>
                <span class="text-slate-400">{expanded[cat.id] ? '▾' : '▸'}</span>
                <span class="text-base font-semibold">{cat.name}</span>
                <span class="text-sm text-slate-500 dark:text-slate-400">· {(itemsByCat[cat.id] || []).length} items{#if !cat.available} · unavailable{/if}</span>
              </button>
              <div class="flex gap-1">
                <select class="field text-xs" bind:value={cat.default_printer_id} onchange={() => saveCategory(cat)} title="default printer">
                  <option value={null}>no printer</option>
                  {#each printers as p}<option value={p.id}>{p.name}</option>{/each}
                </select>
                <button class="btn btn-ghost btn-xs" onclick={() => { cat.available = !cat.available; saveCategory(cat); }}>{cat.available ? 'hide' : 'show'}</button>
                <button class="btn btn-danger btn-xs" onclick={() => deleteCategory(cat)}>delete</button>
              </div>
            </div>

            {#if expanded[cat.id]}
              <div class="mt-3 space-y-1">
                {#each (itemsByCat[cat.id] || []) as it, i (it.id)}
                  <div
                    class="flex items-center gap-3 px-3.5 py-3 rounded-lg border-l-2 {it.in_stock ? 'border-emerald-500' : 'border-rose-500'} bg-white dark:bg-slate-900 hover:bg-slate-50 dark:hover:bg-slate-800/60 transition"
                    draggable="true"
                    role="listitem"
                    ondragstart={() => (dragCat = cat.id + ':' + i)}
                    ondragover={(e) => e.preventDefault()}
                    ondrop={(e) => { e.preventDefault(); const [c, f] = (dragCat || '').split(':'); if (c === cat.id) reorderItems(cat.id, +f, i); dragCat = null; }}
                  >
                    <span class="cursor-grab text-slate-400 select-none text-lg">⠿</span>
                    <!-- whole item is clickable → opens the edit drawer -->
                    <button type="button" class="flex-1 min-w-0 text-left" title="Edit item" onclick={() => (editing = it)}>
                      <div class="text-base font-medium truncate">{it.name}</div>
                      <div class="text-sm text-slate-500 dark:text-slate-400">
                        {dollars(it.price_cents)}{#if it.printer_id} · 🖨 {printerName(it.printer_id)}{:else if cat.default_printer_id} · <span class="italic opacity-75">🖨 {printerName(cat.default_printer_id)} (from category)</span>{/if}{#if it.tags.length} · {it.tags.join(', ')}{/if}
                      </div>
                    </button>
                    <button class="chip {it.in_stock ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-200' : 'bg-rose-100 text-rose-800 dark:bg-rose-900/50 dark:text-rose-200'}" onclick={() => toggleStock(it)}>
                      {it.in_stock ? 'in stock' : 'out'}
                    </button>
                    <button class="btn btn-danger btn-xs" title="Delete item" onclick={() => deleteItem(it)}>✕</button>
                  </div>
                {/each}
                <div class="flex gap-1 pt-1">
                  <input class="field flex-1" placeholder="add item…" bind:value={newItemName[cat.id]} onkeydown={(e) => e.key === 'Enter' && addItem(cat)} />
                  <button class="btn btn-primary btn-xs" onclick={() => addItem(cat)}>add</button>
                </div>
              </div>
            {/if}
          </div>
        {/each}

        <div class="flex gap-1">
          <input class="field flex-1" placeholder="new category…" bind:value={newCatName} onkeydown={(e) => e.key === 'Enter' && createCategory()} />
          <button class="btn btn-primary btn-xs" onclick={createCategory}>+ category</button>
        </div>
      {/if}
    </section>
  </div>
{/if}

{#if editing}
  {@const editCat = categories.find((c) => c.id === editing!.category_id)}
  <ItemEditor
    item={editing}
    {printers}
    {live}
    inheritedPrinter={editCat?.default_printer_id ? printerName(editCat.default_printer_id) : ''}
    onclose={() => (editing = null)}
    onsaved={onItemSaved}
    {toast}
  />
{/if}
