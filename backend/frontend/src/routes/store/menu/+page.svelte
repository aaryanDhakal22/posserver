<script lang="ts">
  import { onMount } from 'svelte';
  import {
    storeApi,
    toCents,
    centsInput,
    dollars,
    fmtTime,
    errText,
    type Menu,
    type Category,
    type Item,
    type Printer,
    type LivePrinters,
    type UnmatchedName,
    printerLabel
  } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Tabs from '$lib/ui/Tabs.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import MenuButton from '$lib/ui/MenuButton.svelte';
  import ItemEditor from '$lib/components/menu/ItemEditor.svelte';
  import ModifiersTab from '$lib/components/menu/ModifiersTab.svelte';
  import CategoriesTab from '$lib/components/menu/CategoriesTab.svelte';
  import ImportDialog from '$lib/components/menu/ImportDialog.svelte';
  import { loadPiStatus, type PiLink } from '$lib/store/pi';

  // Menu editor: items grouped by category (inline price + stock, side panel
  // for the rest), modifier groups, categories/menus, CSV import/export.
  // Kitchen routing is stamped on each online order at ingest, so changes
  // apply to the next order.

  let loading = $state(true);
  let tab = $state('items');

  let menus = $state<Menu[]>([]);
  let activeMenu = $state<Menu | null>(null);
  let categories = $state<Category[]>([]);
  let itemsByCat = $state<Record<string, Item[]>>({});
  let printers = $state<Printer[]>([]);
  let live = $state<LivePrinters | null>(null);
  let piState = $state<PiLink>('unknown');
  let unmatched = $state<UnmatchedName[]>([]);
  let showUnmatched = $state(false);
  let editing = $state<Item | null>(null);
  let importOpen = $state(false);

  // filters
  let q = $state('');
  let catF = $state('');
  let printerF = $state(''); // '' any · '__none' no kitchen printer · printer id
  let outOnly = $state(false);
  let noPrinterOnly = $state(false);
  let collapsed = $state<Record<string, boolean>>({});

  const printerName = (id: string | null) => {
    const p = id ? printers.find((x) => x.id === id) : undefined;
    return id ? (p ? printerLabel(p, printers) : '—') : '';
  };
  const effective = (it: Item, c: Category) => it.printer_id ?? c.default_printer_id;

  // --- loading --------------------------------------------------------------
  // Vendor item names from the last 14 days that print on every station
  // ("unmatched": no menu item of that name) or have no printer ("unassigned").
  async function loadUnmatched() {
    unmatched = await storeApi<UnmatchedName[]>('/me/menu/unmatched?days=14').catch(() => []);
  }
  async function loadMenus() {
    menus = await storeApi<Menu[]>('/me/menu/menus');
    if (activeMenu) activeMenu = menus.find((m) => m.id === activeMenu!.id) ?? null;
    if (!activeMenu && menus.length) activeMenu = menus[0];
    if (activeMenu) await loadCategories();
    else categories = [];
  }
  async function loadCategories() {
    if (!activeMenu) return;
    categories = await storeApi<Category[]>(`/me/menu/menus/${activeMenu.id}/categories`);
    const lists = await Promise.all(categories.map((c) => storeApi<Item[]>(`/me/menu/categories/${c.id}/items`)));
    const next: Record<string, Item[]> = {};
    categories.forEach((c, i) => (next[c.id] = lists[i]));
    itemsByCat = next;
  }
  async function loadItems(catID: string) {
    itemsByCat[catID] = await storeApi<Item[]>(`/me/menu/categories/${catID}/items`);
  }
  async function loadPrinters() {
    printers = await storeApi<Printer[]>('/me/printers');
  }

  onMount(async () => {
    try {
      await Promise.all([loadPrinters(), loadMenus()]);
      ({ live, pi: piState } = await loadPiStatus());
      await loadUnmatched();
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
    }
  });

  async function selectMenu(m: Menu) {
    activeMenu = m;
    catF = '';
    await loadCategories().catch((e) => toast.error(e));
  }
  async function onImported() {
    activeMenu = null;
    await Promise.all([loadMenus(), loadPrinters()]).catch((e) => toast.error(e));
    await loadUnmatched();
  }

  // --- derived views --------------------------------------------------------
  const filtering = $derived(!!q.trim() || !!catF || !!printerF || outOnly || noPrinterOnly);
  const allItems = $derived(categories.flatMap((c) => (itemsByCat[c.id] || []).map((it) => ({ it, c }))));
  const itemCount = $derived(allItems.length);
  const noPrinterCount = $derived(allItems.filter(({ it, c }) => !effective(it, c)).length);
  const groups = $derived.by(() => {
    const s = q.trim().toLowerCase();
    return categories
      .filter((c) => !catF || c.id === catF)
      .map((c) => ({
        cat: c,
        items: (itemsByCat[c.id] || []).filter((it) => {
          const eff = effective(it, c);
          if (s && !it.name.toLowerCase().includes(s)) return false;
          if (printerF === '__none' ? !!eff : printerF && eff !== printerF) return false;
          if (outOnly && it.in_stock) return false;
          if (noPrinterOnly && eff) return false;
          return true;
        })
      }))
      .filter((g) => !filtering || g.items.length);
  });
  const shownCount = $derived(groups.reduce((n, g) => n + g.items.length, 0));

  function clearFilters() {
    q = '';
    catF = '';
    printerF = '';
    outOnly = false;
    noPrinterOnly = false;
  }
  function showNoPrinter() {
    clearFilters();
    noPrinterOnly = true;
  }

  /** "10 Small $6.99 · 12 Medium $9.99" from attributes.sizes when present, else tags/description. */
  function subtitle(it: Item): string {
    const sizes = (it.attributes as Record<string, unknown> | undefined)?.sizes;
    if (Array.isArray(sizes) && sizes.length) {
      return sizes
        .map((s) => {
          if (typeof s === 'string') return s;
          const o = s as Record<string, unknown>;
          const price = typeof o.price_cents === 'number' ? ' ' + dollars(o.price_cents) : '';
          return `${o.name ?? ''}${price}`.trim();
        })
        .filter(Boolean)
        .join(' · ');
    }
    return [it.tags?.join(', '), it.description].filter(Boolean).join(' · ');
  }

  // --- inline edits (the PATCH takes the whole item, as before) -------------
  async function patchItem(it: Item, patch: Partial<Item>) {
    await storeApi(`/me/menu/items/${it.id}`, { method: 'PATCH', body: JSON.stringify({ ...it, ...patch }) });
  }
  async function commitPrice(it: Item, el: HTMLInputElement) {
    const c = toCents(el.value);
    if (c === null || c < 0) {
      toast.error(`“${el.value}” isn't a valid price — use e.g. 12.50. Not saved.`);
      el.value = centsInput(it.price_cents);
      return;
    }
    el.value = centsInput(c);
    if (c === it.price_cents) return;
    const prev = it.price_cents;
    it.price_cents = c;
    try {
      await patchItem(it, { price_cents: c });
      toast.success(`${it.name} → ${dollars(c)}`);
    } catch (e) {
      it.price_cents = prev;
      el.value = centsInput(prev);
      toast.error(e);
    }
  }
  async function setStock(it: Item, v: boolean, undoable = true) {
    const prev = it.in_stock;
    it.in_stock = v;
    try {
      await patchItem(it, { in_stock: v });
      toast.success(
        v ? `${it.name} is back in stock` : `${it.name} marked out of stock`,
        undoable ? { action: { label: 'Undo', run: () => setStock(it, prev, false) } } : {}
      );
    } catch (e) {
      it.in_stock = prev;
      toast.error(e);
    }
  }

  // --- drag reorder (native HTML5, within a category) -----------------------
  let drag = $state<{ cat: string; id: string } | null>(null);
  let dropTarget = $state<string | null>(null);
  async function reorder(catID: string, fromId: string, toId: string) {
    const arr = [...(itemsByCat[catID] || [])];
    const from = arr.findIndex((x) => x.id === fromId);
    const to = arr.findIndex((x) => x.id === toId);
    if (from < 0 || to < 0 || from === to) return;
    const [m] = arr.splice(from, 1);
    arr.splice(to, 0, m);
    itemsByCat[catID] = arr;
    try {
      await storeApi('/me/menu/items/reorder', { method: 'POST', body: JSON.stringify({ ids: arr.map((x) => x.id) }) });
    } catch (e) {
      toast.error(e);
      await loadItems(catID).catch(() => {});
    }
  }

  // --- category actions -----------------------------------------------------
  let catDialog = $state<{ cat: Category; name: string; printer: string; focus: 'name' | 'printer' } | null>(null);
  let catDialogOpen = $state(false);
  function editCategory(c: Category, focus: 'name' | 'printer') {
    catDialog = { cat: c, name: c.name, printer: c.default_printer_id ?? '', focus };
    catDialogOpen = true;
  }
  async function saveCategory(c: Category, patch: Partial<Category>, msg: string) {
    try {
      await storeApi(`/me/menu/categories/${c.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ name: c.name, description: c.description, available: c.available, default_printer_id: c.default_printer_id, ...patch })
      });
      await loadCategories();
      toast.success(msg);
      return true;
    } catch (e) {
      toast.error(e);
      return false;
    }
  }
  async function submitCategory(e: SubmitEvent) {
    e.preventDefault();
    if (!catDialog || !catDialog.name.trim()) return;
    const ok = await saveCategory(catDialog.cat, { name: catDialog.name.trim(), default_printer_id: catDialog.printer || null }, 'Category saved');
    if (ok) catDialogOpen = false;
  }
  async function deleteCategory(c: Category) {
    const n = (itemsByCat[c.id] || []).length;
    const ok = await confirmDialog({
      title: `Delete category “${c.name}”?`,
      body: n ? `Its ${n} item${n === 1 ? '' : 's'} are deleted too.` : 'It has no items.',
      confirmLabel: 'Delete category',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi(`/me/menu/categories/${c.id}`, { method: 'DELETE' });
      await loadCategories();
      toast.success('Category deleted');
    } catch (e) {
      toast.error(e);
    }
  }

  // --- add item -------------------------------------------------------------
  let addOpen = $state(false);
  let ni = $state({ name: '', cat: '', price: '' });
  let niErr = $state('');
  let adding = $state(false);
  function openAdd(catId = '') {
    ni = { name: '', cat: catId || catF || categories[0]?.id || '', price: '' };
    niErr = '';
    addOpen = true;
  }
  async function addItem(e: SubmitEvent) {
    e.preventDefault();
    niErr = '';
    if (!ni.name.trim()) return (niErr = 'Give the item a name.');
    if (!ni.cat) return (niErr = 'Pick a category (create one on the Categories tab first).');
    const cents = ni.price.trim() ? toCents(ni.price) : 0;
    if (cents === null || cents < 0) return (niErr = `“${ni.price}” isn't a valid price — use e.g. 12.50.`);
    adding = true;
    try {
      const created = await storeApi<Item>(`/me/menu/categories/${ni.cat}/items`, {
        method: 'POST',
        body: JSON.stringify({ name: ni.name.trim(), price_cents: cents })
      });
      await loadItems(ni.cat);
      addOpen = false;
      toast.success(`${ni.name.trim()} added`);
      if (created?.id) editing = (itemsByCat[ni.cat] || []).find((x) => x.id === created.id) ?? created;
    } catch (err) {
      niErr = errText(err);
    } finally {
      adding = false;
    }
  }

  // --- item panel -----------------------------------------------------------
  async function onItemSaved(catID: string) {
    editing = null;
    await loadItems(catID).catch((e) => toast.error(e));
    toast.success('Item saved');
  }
  async function onItemDeleted(catID: string) {
    editing = null;
    await loadItems(catID).catch((e) => toast.error(e));
    toast.success('Item deleted');
  }
  const editCat = $derived(editing ? categories.find((c) => c.id === editing!.category_id) : undefined);

  const TABS = [
    { id: 'items', label: 'Items' },
    { id: 'modifiers', label: 'Modifier groups' },
    { id: 'categories', label: 'Categories' }
  ];
</script>

<svelte:head><title>Menu — quicc</title></svelte:head>

<PageHeader
  title="Menu"
  subtitle={activeMenu
    ? `${activeMenu.name} · ${categories.length} categor${categories.length === 1 ? 'y' : 'ies'} · ${itemCount} item${itemCount === 1 ? '' : 's'} · changes apply to the next order`
    : 'Categories, items, modifiers and printer routing'}
>
  {#snippet actions()}
    {#if menus.length > 1}
      <label for="menu-pick" class="sr-only">Menu</label>
      <select
        id="menu-pick"
        class="field !w-auto"
        value={activeMenu?.id ?? ''}
        onchange={(e) => {
          const m = menus.find((x) => x.id === e.currentTarget.value);
          if (m) selectMenu(m);
        }}
      >
        {#each menus as m (m.id)}<option value={m.id}>{m.name}{m.available ? '' : ' (off)'}</option>{/each}
      </select>
    {/if}
    <Button icon="upload" onclick={() => (importOpen = true)}>Import CSV</Button>
    <Button icon="download" href="/store/v1/me/menu/export.csv" download="menu.csv" title="menu,category,item,price,printer,description">Export</Button>
    <Button variant="primary" icon="plus" disabled={!categories.length} onclick={() => openAdd()}>Add item</Button>
  {/snippet}
</PageHeader>

<Tabs tabs={TABS} bind:value={tab} label="Menu sections" idPrefix="mt" />

<div id="mt-panel" role="tabpanel" aria-labelledby="mt-{tab}" class="flex flex-col gap-4">
  {#if loading}
    <div class="card p-5"><Skeleton lines={8} /></div>
  {:else if tab === 'modifiers'}
    <ModifiersTab />
  {:else if tab === 'categories'}
    <CategoriesTab
      {menus}
      active={activeMenu}
      {categories}
      {itemsByCat}
      {printers}
      onselect={selectMenu}
      onreload={async () => {
        await loadMenus().catch((e) => toast.error(e));
      }}
    />
  {:else if !activeMenu}
    <section class="card">
      <EmptyState icon="utensils" title="No menu yet" body="Import a CSV of your menu, or create a menu and its categories on the Categories tab.">
        {#snippet action()}
          <Button variant="primary" icon="upload" onclick={() => (importOpen = true)}>Import CSV</Button>
          <Button onclick={() => (tab = 'categories')}>Create a menu</Button>
        {/snippet}
      </EmptyState>
    </section>
  {:else}
    {#if noPrinterCount}
      <div class="flex flex-wrap items-center gap-2.5 px-3.5 py-2.5 rounded-r12 bg-warn-bg text-warn font-medium" role="status">
        <Icon name="alert" size={18} />
        <span class="flex-1 min-w-[200px]">
          {noPrinterCount === 1 ? '1 item has' : `${noPrinterCount} items have`} no kitchen printer — {noPrinterCount === 1 ? 'it prints' : 'they print'} on every station.
        </span>
        <Button size="sm" onclick={showNoPrinter}>Show {noPrinterCount === 1 ? 'it' : 'them'}</Button>
      </div>
    {/if}
    {#if unmatched.length}
      <div class="rounded-r12 bg-warn-bg text-warn" role="status">
        <div class="flex flex-wrap items-center gap-2.5 px-3.5 py-2.5 font-medium">
          <Icon name="receipt" size={18} />
          <span class="flex-1 min-w-[200px]">
            {unmatched.length} item name{unmatched.length === 1 ? '' : 's'} from online orders in the last 14 days didn't reach a kitchen printer.
          </span>
          <Button size="sm" aria-expanded={showUnmatched} onclick={() => (showUnmatched = !showUnmatched)}>{showUnmatched ? 'Hide' : 'Details'}</Button>
          <Button size="sm" variant="ghost" icon="refresh" aria-label="Refresh" onclick={loadUnmatched} />
        </div>
        {#if showUnmatched}
          <div class="px-3.5 pb-3 text-text2">
            <p class="m-0 mb-2 text-[13px]">
              <b>Not on the menu</b> = no menu item with that name (it prints on every station) · <b>No printer</b> = the item and its category have
              no printer. Rename the menu item to the vendor's name, or set a printer.
            </p>
            <div class="flex flex-wrap gap-1.5">
              {#each unmatched as u (u.name)}
                <Badge tone={u.routing === 'unmatched' ? 'warn' : 'mute'} title="{u.orders} order{u.orders === 1 ? '' : 's'}, last {fmtTime(u.last_seen)}">
                  {u.name} · {u.routing === 'unmatched' ? 'not on the menu' : 'no printer'} · {u.orders}×
                </Badge>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <div class="flex flex-wrap items-center gap-2" role="search" aria-label="Filter items">
      <SearchInput id="item-search" label="Search items" placeholder="Search items" bind:value={q} class="flex-[1_1_240px] max-w-[360px]" />
      <label for="f-cat" class="sr-only">Category</label>
      <select id="f-cat" class="field !w-auto" bind:value={catF}>
        <option value="">All categories</option>
        {#each categories as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
      </select>
      <label for="f-printer" class="sr-only">Printer</label>
      <select id="f-printer" class="field !w-auto" bind:value={printerF}>
        <option value="">Any printer</option>
        {#each printers as p (p.id)}<option value={p.id}>{printerLabel(p, printers)}</option>{/each}
        <option value="__none">No kitchen printer</option>
      </select>
      <button type="button" class="fchip" aria-pressed={outOnly} onclick={() => (outOnly = !outOnly)}>Out of stock</button>
      <button type="button" class="fchip" aria-pressed={noPrinterOnly} onclick={() => (noPrinterOnly = !noPrinterOnly)}>No kitchen printer</button>
      {#if filtering}<Button size="sm" variant="ghost" onclick={clearFilters}>Clear</Button>{/if}
    </div>

    <section class="card overflow-hidden" aria-label="Items">
      <div class="overflow-x-auto">
        <div class="sm:min-w-[620px]">
          <div class="irow head" aria-hidden="true">
            <span class="c-grip"><span class="sr-only">Reorder</span></span>
            <span>Item</span>
            <span class="text-right">Price</span>
            <span class="c-printer">Prints on</span>
            <span class="whitespace-nowrap">In stock</span>
            <span><span class="sr-only">Open</span></span>
          </div>
          {#each groups as g (g.cat.id)}
            {@const c = g.cat}
            <div>
              <div class="cat">
                <button
                  type="button"
                  class="flex items-center gap-2 flex-1 min-w-0 text-left font-semibold py-2.5 -my-2.5"
                  aria-expanded={!collapsed[c.id]}
                  onclick={() => (collapsed[c.id] = !collapsed[c.id])}
                >
                  <Icon name={collapsed[c.id] ? 'chevron-right' : 'chevron-down'} size={16} class="text-muted" />
                  <span class="truncate">{c.name}</span>
                  <span class="text-muted font-medium whitespace-nowrap">· {g.items.length} item{g.items.length === 1 ? '' : 's'}</span>
                  {#if !c.available}<Badge tone="warn">Hidden</Badge>{/if}
                </button>
                <span class="text-xs text-muted font-medium hidden sm:inline">Default printer</span>
                <Badge tone={c.default_printer_id ? 'mute' : 'warn'} class="max-w-[38%] sm:max-w-none overflow-hidden" title="Default printer">
                  <span class="truncate">{printerName(c.default_printer_id) || 'None'}</span>
                </Badge>
                <MenuButton
                  size="sm"
                  label="Actions for {c.name}"
                  items={[
                    { label: 'Add item here', icon: 'plus', onclick: () => openAdd(c.id) },
                    { label: 'Rename…', icon: 'pencil', onclick: () => editCategory(c, 'name') },
                    { label: 'Change default printer…', icon: 'printer', onclick: () => editCategory(c, 'printer') },
                    {
                      label: c.available ? 'Hide from the menu' : 'Show on the menu',
                      icon: 'eye',
                      onclick: () => saveCategory(c, { available: !c.available }, c.available ? `${c.name} hidden` : `${c.name} shown`)
                    },
                    { label: 'Delete category', icon: 'trash', danger: true, onclick: () => deleteCategory(c) }
                  ]}
                />
              </div>
              {#if !collapsed[c.id]}
                {#each g.items as it (it.id)}
                  {@const eff = effective(it, c)}
                  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
                  <div
                    class="irow item {editing?.id === it.id ? 'sel' : ''} {dropTarget === it.id ? 'drop' : ''}"
                    draggable="true"
                    ondragstart={(e) => {
                      drag = { cat: c.id, id: it.id };
                      e.dataTransfer?.setData('text/plain', it.id);
                    }}
                    ondragover={(e) => {
                      if (drag?.cat === c.id) {
                        e.preventDefault();
                        dropTarget = it.id;
                      }
                    }}
                    ondragleave={() => {
                      if (dropTarget === it.id) dropTarget = null;
                    }}
                    ondrop={(e) => {
                      e.preventDefault();
                      if (drag && drag.cat === c.id) reorder(c.id, drag.id, it.id);
                      drag = null;
                      dropTarget = null;
                    }}
                    ondragend={() => {
                      drag = null;
                      dropTarget = null;
                    }}
                    onclick={() => (editing = it)}
                  >
                    <span class="c-grip text-muted cursor-grab" title="Drag to reorder"><Icon name="grip" size={14} /></span>
                    <span class="min-w-0">
                      <button type="button" class="block w-full text-left font-semibold truncate hover:text-accent py-1.5 -my-1.5" onclick={(e) => { e.stopPropagation(); editing = it; }}>
                        {it.name}
                      </button>
                      {#if subtitle(it)}<span class="block text-xs text-muted truncate">{subtitle(it)}</span>{/if}
                      <span class="sm:hidden block text-xs truncate {eff ? 'text-text2' : 'text-warn font-medium'}">
                        {eff ? `Prints on ${printerName(eff)}` : 'No printer — prints on every station'}
                      </span>
                    </span>
                    <span class="text-right">
                      <input
                        class="cell"
                        value={centsInput(it.price_cents)}
                        inputmode="decimal"
                        aria-label="Price of {it.name} ($)"
                        onclick={(e) => e.stopPropagation()}
                        onkeydown={(e) => {
                          if (e.key === 'Enter') e.currentTarget.blur();
                          if (e.key === 'Escape') {
                            e.currentTarget.value = centsInput(it.price_cents);
                            e.currentTarget.blur();
                          }
                        }}
                        onblur={(e) => commitPrice(it, e.currentTarget)}
                      />
                    </span>
                    <span class="c-printer min-w-0">
                      {#if !eff}
                        <Badge tone="warn" title="No printer on the item or its category — prints on every station">Every station</Badge>
                      {:else if it.printer_id}
                        <span class="text-text2 truncate block">{printerName(it.printer_id)}</span>
                      {:else}
                        <span class="text-text2 truncate block">{printerName(eff)}</span>
                        <span class="block text-[11px] text-muted truncate">from {c.name}</span>
                      {/if}
                    </span>
                    <span><Switch checked={it.in_stock} label="{it.name} in stock" onchange={(v) => setStock(it, v)} /></span>
                    <span class="text-muted"><Icon name="chevron-right" size={16} /></span>
                  </div>
                {:else}
                  {#if !filtering}
                    <div class="px-4 py-3 border-b border-border text-[13px] text-muted">
                      No items yet. <button type="button" class="text-accent font-medium hover:underline" onclick={() => openAdd(c.id)}>Add one</button>
                    </div>
                  {/if}
                {/each}
              {/if}
            </div>
          {/each}
          {#if !groups.length}
            <div class="px-4 py-7 text-center text-muted">
              {#if filtering}
                No items match. <Button size="sm" variant="ghost" onclick={clearFilters}>Clear filters</Button>
              {:else}
                No categories yet — add one on the <button type="button" class="text-accent font-medium hover:underline" onclick={() => (tab = 'categories')}>Categories</button> tab.
              {/if}
            </div>
          {/if}
        </div>
      </div>
    </section>
    {#if filtering}<p class="text-xs text-muted -mt-2">Showing {shownCount} of {itemCount} items.</p>{/if}
  {/if}
</div>

{#if editing}
  {#key editing.id}
    <ItemEditor
      item={editing}
      categoryName={editCat?.name ?? ''}
      {printers}
      {live}
      {piState}
      inheritedPrinter={editCat?.default_printer_id ? printerName(editCat.default_printer_id) : ''}
      onclose={() => (editing = null)}
      onsaved={onItemSaved}
      ondeleted={onItemDeleted}
    />
  {/key}
{/if}

<ImportDialog bind:open={importOpen} onimported={onImported} />

<Dialog bind:open={addOpen} title="Add item" description="You can set its printer, tax and modifiers next.">
  <form id="add-item" class="flex flex-col gap-3.5" onsubmit={addItem}>
    <div>
      <label class="lbl" for="ni-name">Name</label>
      <!-- svelte-ignore a11y_autofocus -->
      <input id="ni-name" class="field" bind:value={ni.name} autofocus />
    </div>
    <div class="grid grid-cols-[minmax(0,1fr)_120px] gap-3">
      <div>
        <label class="lbl" for="ni-cat">Category</label>
        <select id="ni-cat" class="field" bind:value={ni.cat}>
          {#each categories as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
        </select>
      </div>
      <div>
        <label class="lbl" for="ni-price">Price ($)</label>
        <input id="ni-price" class="field mono text-right" inputmode="decimal" placeholder="0.00" bind:value={ni.price} />
      </div>
    </div>
    {#if niErr}<p class="field-error !m-0" role="alert">{niErr}</p>{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (addOpen = false)}>Cancel</Button>
    <Button type="submit" form="add-item" variant="primary" loading={adding}>Add item</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={catDialogOpen} title={catDialog ? `Edit “${catDialog.cat.name}”` : 'Edit category'}>
  {#if catDialog}
    <form id="cat-form" class="flex flex-col gap-3.5" onsubmit={submitCategory}>
      <div>
        <label class="lbl" for="cd-name">Name</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input id="cd-name" class="field" bind:value={catDialog.name} autofocus={catDialog.focus === 'name'} />
      </div>
      <div>
        <label class="lbl" for="cd-printer">Default printer</label>
        <!-- svelte-ignore a11y_autofocus -->
        <select id="cd-printer" class="field" bind:value={catDialog.printer} autofocus={catDialog.focus === 'printer'}>
          <option value="">None — items print on every station unless they have their own</option>
          {#each printers as p (p.id)}<option value={p.id}>{printerLabel(p, printers)}</option>{/each}
        </select>
        <p class="help">Items without their own printer use this one.</p>
      </div>
    </form>
  {/if}
  {#snippet footer()}
    <Button onclick={() => (catDialogOpen = false)}>Cancel</Button>
    <Button type="submit" form="cat-form" variant="primary">Save</Button>
  {/snippet}
</Dialog>

<style>
  .irow {
    display: grid;
    grid-template-columns: 20px minmax(0, 1fr) 100px 150px 64px 20px;
    gap: 12px;
    align-items: center;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
  }
  .irow.head {
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    background: var(--surface);
  }
  .irow.item {
    cursor: pointer;
  }
  .irow.item:hover {
    background: var(--hover);
  }
  .irow.sel {
    background: var(--brand-soft);
  }
  .irow.drop {
    box-shadow: inset 0 2px 0 var(--brand);
  }
  .cat {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px 8px 16px;
    background: var(--elev);
    border-bottom: 1px solid var(--border);
  }
  /* Phones: name (+ sizes / printer line) · price · in stock · chevron. */
  @media (max-width: 639px) {
    .irow {
      grid-template-columns: minmax(0, 1fr) 76px 48px 16px;
      gap: 8px;
      padding: 10px 12px 10px 16px;
    }
    .irow .c-grip,
    .irow .c-printer {
      display: none;
    }
    .irow .cell {
      width: 76px;
    }
    .cat {
      gap: 8px;
      padding: 6px 8px 6px 16px;
    }
  }
</style>
