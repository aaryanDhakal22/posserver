<script lang="ts">
  import { onMount } from 'svelte';
  import {
    storeApi,
    toCents,
    centsInput,
    dollars,
    errText,
    VIEW_KINDS,
    type Bank,
    type BankItem,
    type MenuView,
    type ViewEntry,
    type ViewBuild,
    type ViewKind,
    type EntryKind
  } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';

  // Menu views (main 3.0): every sales channel reads its own view of the
  // menu above (the bank). A view can mark prices up, hide categories, and
  // change items one by one; Brygid's view maps the vendor's item names to
  // your items instead.
  let { bank, onbankchange }: { bank: Bank | null; onbankchange?: () => void } = $props();

  let views = $state<MenuView[]>([]);
  let sel = $state<MenuView | null>(null);
  let entries = $state<ViewEntry[]>([]);
  let build = $state<ViewBuild | null>(null);

  const items = $derived(
    (bank?.menus ?? []).flatMap((m) => m.categories.flatMap((c) => c.items.map((it) => ({ it, cat: c.name, catId: c.id }))))
  );
  const categories = $derived((bank?.menus ?? []).flatMap((m) => m.categories));
  const itemName = (id: string) => items.find((x) => x.it.id === id)?.it.name ?? 'a deleted item';
  const kindLabel = (k: ViewKind) => VIEW_KINDS.find((x) => x.kind === k)?.label ?? k;

  async function loadViews() {
    views = await storeApi<MenuView[]>('/me/menu/views');
    if (sel) sel = views.find((v) => v.id === sel!.id) ?? null;
    if (!sel && views.length) await pick(views[0]);
  }
  async function refresh() {
    if (!sel) return;
    [entries, build] = await Promise.all([
      storeApi<ViewEntry[]>(`/me/menu/views/${sel.id}/entries`),
      storeApi<ViewBuild>(`/me/menu/views/${sel.id}/build`)
    ]);
  }
  async function pick(v: MenuView) {
    sel = v;
    rules = { markupPct: String(v.rules.markup_bps / 100), round: v.rules.round || 'none', hidden: [...(v.rules.hidden_categories || [])], photo: v.rules.require_photo };
    viewName = v.name;
    await refresh().catch((e) => toast.error(e));
  }
  onMount(() => loadViews().catch((e) => toast.error(e)));

  // --- add / rename / delete views ---------------------------------------------
  let nv = $state<{ kind: ViewKind; name: string }>({ kind: 'web', name: '' });
  async function addView(e: SubmitEvent) {
    e.preventDefault();
    if (!nv.name.trim()) return;
    try {
      const v = await storeApi<MenuView>('/me/menu/views', { method: 'POST', body: JSON.stringify({ kind: nv.kind, name: nv.name.trim() }) });
      nv = { kind: 'web', name: '' };
      await loadViews();
      await pick(views.find((x) => x.id === v.id) ?? v);
      toast.success(`“${v.name}” added — point a channel at it in admin › Channels`);
    } catch (err) {
      toast.error(err);
    }
  }
  let viewName = $state('');
  async function renameView() {
    if (!sel || !viewName.trim() || viewName.trim() === sel.name) return;
    try {
      await storeApi(`/me/menu/views/${sel.id}`, { method: 'PATCH', body: JSON.stringify({ name: viewName.trim() }) });
      await loadViews();
      toast.success('Renamed');
    } catch (err) {
      toast.error(err);
    }
  }
  async function deleteView(v: MenuView) {
    const ok = await confirmDialog({
      title: `Delete view “${v.name}”?`,
      body: 'Its changes go with it. Channels that read it fall back to the default view of their kind.',
      confirmLabel: 'Delete view',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi(`/me/menu/views/${v.id}`, { method: 'DELETE' });
      sel = null;
      await loadViews();
      toast.success('View deleted');
    } catch (err) {
      toast.error(err);
    }
  }

  // --- rules -------------------------------------------------------------------
  let rules = $state({ markupPct: '0', round: 'none' as 'none' | 'ceil_99', hidden: [] as string[], photo: false });
  async function saveRules() {
    if (!sel) return;
    const pct = parseFloat(rules.markupPct || '0');
    if (isNaN(pct) || pct < -50 || pct > 100) return toast.error('Markup must be between -50% and 100%.');
    try {
      await storeApi(`/me/menu/views/${sel.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ rules: { markup_bps: Math.round(pct * 100), round: rules.round, hidden_categories: rules.hidden, require_photo: rules.photo } })
      });
      await loadViews();
      await refresh();
      toast.success('Rules saved');
    } catch (err) {
      toast.error(err);
    }
  }
  function toggleHidden(id: string, on: boolean) {
    rules.hidden = on ? rules.hidden.filter((x) => x !== id) : [...rules.hidden, id];
  }

  // --- entries -----------------------------------------------------------------
  type Draft = {
    id?: string; kind: EntryKind; itemIds: string[]; name: string; description: string; photo: string;
    category: string; price: string; hidden: boolean;
  };
  let draft = $state<Draft | null>(null);
  let draftOpen = $state(false);
  let draftErr = $state('');
  let itemQ = $state('');
  const KIND_HELP: Record<string, string> = {
    item: 'Rename, describe, add a photo, move, reprice or hide one item.',
    merge: 'Show several items as one with a choice (e.g. Cheesecake → Strawberry / Oreo).',
    augment: 'Add an item made of others at its own price (e.g. a Pizza + Wings deal).'
  };
  function newEntry(kind: EntryKind = 'item') {
    draft = { kind, itemIds: [], name: '', description: '', photo: '', category: '', price: '', hidden: false };
    draftErr = '';
    itemQ = '';
    draftOpen = true;
  }
  function editEntry(e: ViewEntry) {
    draft = {
      id: e.id, kind: e.kind, itemIds: [...e.anchor.item_ids], name: e.display_name, description: e.description, photo: e.photo,
      category: e.category, price: e.price_override_cents == null ? '' : centsInput(e.price_override_cents), hidden: e.hidden
    };
    draftErr = '';
    itemQ = '';
    draftOpen = true;
  }
  function toggleItem(id: string) {
    if (!draft) return;
    if (draft.kind === 'item') draft.itemIds = [id];
    else draft.itemIds = draft.itemIds.includes(id) ? draft.itemIds.filter((x) => x !== id) : [...draft.itemIds, id];
  }
  const pickable = $derived(items.filter((x) => !itemQ.trim() || x.it.name.toLowerCase().includes(itemQ.trim().toLowerCase())).slice(0, 60));
  async function saveEntry(ev: SubmitEvent) {
    ev.preventDefault();
    if (!draft || !sel) return;
    draftErr = '';
    let price: number | null = null;
    if (draft.price.trim()) {
      price = toCents(draft.price);
      if (price === null || price < 0) return (draftErr = `“${draft.price}” isn't a valid price.`);
    }
    const body = {
      kind: draft.kind,
      anchor: { item_ids: draft.itemIds },
      display_name: draft.name.trim(),
      description: draft.description.trim(),
      photo: draft.photo.trim(),
      category: draft.category.trim(),
      hidden: draft.hidden,
      price_override_cents: price,
      extra: {}
    };
    try {
      if (draft.id) await storeApi(`/me/menu/entries/${draft.id}`, { method: 'PUT', body: JSON.stringify(body) });
      else await storeApi(`/me/menu/views/${sel.id}/entries`, { method: 'POST', body: JSON.stringify(body) });
      draftOpen = false;
      await refresh();
      toast.success('Saved');
    } catch (err) {
      draftErr = errText(err);
    }
  }
  async function deleteEntry(e: ViewEntry) {
    try {
      await storeApi(`/me/menu/entries/${e.id}`, { method: 'DELETE' });
      await refresh();
      toast.success('Change removed');
    } catch (err) {
      toast.error(err);
    }
  }
  function entryTitle(e: ViewEntry): string {
    if (e.kind === 'map') return `“${e.extra.vendor_name}” → ${itemName(e.anchor.item_ids[0])}`;
    const names = e.anchor.item_ids.map(itemName).join(' + ');
    if (e.kind === 'item') return e.hidden ? `${names} — hidden` : e.display_name ? `${names} → “${e.display_name}”` : names;
    return `“${e.display_name}” = ${names}`;
  }

  // --- Brygid mappings (vendor views) ----------------------------------------------
  let mapPick = $state<Record<string, string>>({});
  async function mapName(name: string) {
    const id = mapPick[name];
    if (!id) return;
    try {
      await storeApi('/me/menu/vendor/maps', { method: 'POST', body: JSON.stringify({ vendor_name: name, item_id: id }) });
      delete mapPick[name];
      await refresh();
      onbankchange?.();
      toast.success(`“${name}” now prints as ${itemName(id)}`);
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<div class="grid grid-cols-1 lg:grid-cols-[260px_minmax(0,1fr)] gap-4 items-start">
  <section class="card p-3" aria-labelledby="vw-title">
    <h2 id="vw-title" class="eyebrow px-2 pt-1 pb-2 m-0">Views</h2>
    {#each VIEW_KINDS as k (k.kind)}
      {@const list = views.filter((v) => v.kind === k.kind)}
      {#if list.length}
        <div class="px-2 pt-2 pb-1 text-[11px] font-semibold text-muted uppercase tracking-wide">{k.label}</div>
        <ul class="flex flex-col gap-0.5">
          {#each list as v (v.id)}
            <li>
              <button type="button" class="nav w-full" aria-current={sel?.id === v.id ? 'page' : undefined} onclick={() => pick(v)}>
                <Icon name={v.kind === 'vendor' ? 'receipt' : 'eye'} size={16} /><span class="truncate">{v.name}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    {/each}
    <form class="flex flex-col gap-2 mt-3 pt-3 border-t border-border" onsubmit={addView}>
      <label for="vw-kind" class="sr-only">Kind</label>
      <select id="vw-kind" class="field" bind:value={nv.kind}>
        {#each VIEW_KINDS.filter((k) => k.kind !== 'vendor') as k (k.kind)}<option value={k.kind}>{k.label}</option>{/each}
      </select>
      <div class="flex gap-2">
        <label for="vw-new" class="sr-only">New view name</label>
        <input id="vw-new" class="field flex-1" placeholder="New view, e.g. Catering" bind:value={nv.name} />
        <Button type="submit" variant="primary" icon="plus" aria-label="Add view" />
      </div>
    </form>
  </section>

  <section class="flex flex-col gap-4 min-w-0">
    {#if !sel}
      <div class="card"><EmptyState icon="eye" title="Pick a view" body="Each sales channel reads its own view of your menu." /></div>
    {:else}
      <div class="card p-5 flex flex-col gap-3">
        <div class="flex flex-wrap items-end gap-2">
          <div class="flex-1 min-w-[200px]">
            <label class="lbl" for="vw-name">{kindLabel(sel.kind)} view</label>
            <input id="vw-name" class="field" bind:value={viewName} onblur={renameView} onkeydown={(e) => e.key === 'Enter' && e.currentTarget.blur()} />
          </div>
          <Button variant="danger-ghost" icon="trash" onclick={() => deleteView(sel!)}>Delete</Button>
        </div>
        <p class="help !mt-0">{VIEW_KINDS.find((k) => k.kind === sel!.kind)?.help}. Changes reach the channel right away.</p>
      </div>

      {#if build?.needs_attention.length}
        <div class="rounded-r12 bg-warn-bg text-warn px-3.5 py-2.5 flex flex-col gap-2" role="status">
          <div class="flex items-center gap-2 font-medium">
            <Icon name="alert" size={18} />{build.needs_attention.length} thing{build.needs_attention.length === 1 ? '' : 's'} need{build.needs_attention.length === 1 ? 's' : ''} attention
          </div>
          <ul class="flex flex-col gap-1.5 text-text2">
            {#each build.needs_attention as n, i (i)}
              <li class="flex flex-wrap items-center gap-2">
                {#if n.code === 'unmatched' && n.name}
                  <span class="flex-1 min-w-[180px]"><b>{n.name}</b> · {n.orders ?? 0} order{n.orders === 1 ? '' : 's'} · prints on every station</span>
                  <label for="map-{i}" class="sr-only">Your item for {n.name}</label>
                  <select id="map-{i}" class="field !w-auto max-w-[260px]" bind:value={mapPick[n.name]}>
                    <option value="">Which item is it?</option>
                    {#each items as x (x.it.id)}<option value={x.it.id}>{x.it.name} · {x.cat}</option>{/each}
                  </select>
                  <Button size="sm" variant="primary" disabled={!mapPick[n.name]} onclick={() => mapName(n.name!)}>Match</Button>
                {:else}
                  <span>{n.name ? `${n.name}: ` : ''}{n.message}</span>
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if sel.kind === 'vendor'}
        <section class="card p-5 flex flex-col gap-3" aria-labelledby="vw-maps">
          <div>
            <h3 id="vw-maps" class="text-base font-semibold m-0">Brygid names</h3>
            <p class="help !mt-1">Online orders print by item name. When Brygid's name differs from yours, match it here once and its tickets go to the right station.</p>
          </div>
          <ul class="flex flex-col gap-1.5">
            {#each entries as e (e.id)}
              <li class="subcard flex items-center gap-2 px-3 py-2">
                <span class="flex-1 min-w-0 truncate">{entryTitle(e)}</span>
                <Button size="xs" variant="ghost" icon="x" aria-label="Remove the match for {e.extra.vendor_name}" onclick={() => deleteEntry(e)} />
              </li>
            {:else}
              <li class="text-[13px] text-muted">No matches yet — names that already equal your item names need none.</li>
            {/each}
          </ul>
        </section>
      {:else}
        <section class="card p-5 flex flex-col gap-4" aria-labelledby="vw-rules">
          <h3 id="vw-rules" class="text-base font-semibold m-0">Prices and categories</h3>
          <div class="flex flex-wrap gap-4 items-end">
            <div>
              <label class="lbl" for="vw-markup">Markup (%)</label>
              <input id="vw-markup" class="field mono text-right !w-24" inputmode="decimal" bind:value={rules.markupPct} />
            </div>
            <div>
              <label class="lbl" for="vw-round">Round marked-up prices</label>
              <select id="vw-round" class="field !w-auto" bind:value={rules.round}>
                <option value="none">To the cent</option>
                <option value="ceil_99">Up to .99</option>
              </select>
            </div>
            <label class="flex items-center gap-2.5 pb-2"><Switch bind:checked={rules.photo} label="Flag items without a photo" />Flag items without a photo</label>
          </div>
          {#if categories.length}
            <div>
              <span class="lbl" id="vw-cats">Categories shown</span>
              <div class="flex flex-wrap gap-x-5 gap-y-2" role="group" aria-labelledby="vw-cats">
                {#each categories as c (c.id)}
                  <label class="flex items-center gap-2">
                    <input type="checkbox" checked={!rules.hidden.includes(c.id)} onchange={(e) => toggleHidden(c.id, e.currentTarget.checked)} />{c.name}
                  </label>
                {/each}
              </div>
            </div>
          {/if}
          <div><Button variant="primary" onclick={saveRules}>Save</Button></div>
        </section>

        <section class="card p-5 flex flex-col gap-3" aria-labelledby="vw-entries">
          <div class="flex flex-wrap items-center gap-2">
            <h3 id="vw-entries" class="text-base font-semibold m-0 flex-1">Changes to items</h3>
            <Button size="sm" icon="pencil" onclick={() => newEntry('item')}>Change an item</Button>
            <Button size="sm" icon="layers" onclick={() => newEntry('merge')}>Combine items</Button>
            <Button size="sm" icon="plus" onclick={() => newEntry('augment')}>Add a deal</Button>
          </div>
          <ul class="flex flex-col gap-1.5">
            {#each entries as e (e.id)}
              <li class="subcard flex items-center gap-2 px-3 py-2">
                <Badge tone="mute">{e.kind === 'item' ? 'item' : e.kind === 'merge' ? 'combined' : 'deal'}</Badge>
                <button type="button" class="flex-1 min-w-0 truncate text-left hover:text-accent" onclick={() => editEntry(e)}>{entryTitle(e)}</button>
                {#if e.price_override_cents != null}<span class="mono text-text2">{dollars(e.price_override_cents)}</span>{/if}
                <Button size="xs" variant="ghost" icon="x" aria-label="Remove this change" onclick={() => deleteEntry(e)} />
              </li>
            {:else}
              <li class="text-[13px] text-muted">None — this view shows your menu as it is (with the rules above).</li>
            {/each}
          </ul>
        </section>

        {#if build}
          <section class="card p-5 flex flex-col gap-3" aria-labelledby="vw-preview">
            <h3 id="vw-preview" class="text-base font-semibold m-0">What the channel sees</h3>
            {#each build.categories as c (c.key)}
              <div>
                <div class="font-semibold mb-1">{c.name}</div>
                <ul class="flex flex-col">
                  {#each c.items as it (it.id)}
                    <li class="flex flex-wrap items-baseline gap-x-3 py-1 border-b border-border text-[13px]">
                      <span class="flex-1 min-w-[140px] truncate">{it.name}{#if it.sold_out}<span class="text-warn"> · sold out</span>{/if}
                        {#if it.choices?.length}<span class="text-muted"> · {it.choices.map((x) => x.name).join(' / ')}</span>{/if}</span>
                      <span class="ml-auto flex flex-wrap justify-end gap-x-3 text-text2">
                        {#if it.sizes.length}
                          {#each it.sizes as sz (sz.size_id)}<span class="whitespace-nowrap">{sz.name} <span class="mono">{dollars(sz.price_cents)}</span></span>{/each}
                        {:else}
                          <span class="mono">{dollars(it.price_cents)}</span>
                        {/if}
                      </span>
                    </li>
                  {/each}
                </ul>
              </div>
            {:else}
              <p class="text-[13px] text-muted m-0">Nothing to show — every category is hidden or the menu is empty.</p>
            {/each}
          </section>
        {/if}
      {/if}
    {/if}
  </section>
</div>

<Dialog
  bind:open={draftOpen}
  title={draft?.kind === 'merge' ? 'Combine items' : draft?.kind === 'augment' ? 'Add a deal' : 'Change an item'}
  description={draft ? KIND_HELP[draft.kind] : ''}
>
  {#if draft}
    <form id="entry-form" class="flex flex-col gap-3.5" onsubmit={saveEntry}>
      <div>
        <span class="lbl" id="ef-items">{draft.kind === 'item' ? 'Item' : 'Items'}</span>
        {#if draft.itemIds.length}
          <div class="flex flex-wrap gap-1.5 mb-2">
            {#each draft.itemIds as id (id)}<Badge tone="mute">{itemName(id)}</Badge>{/each}
          </div>
        {/if}
        <SearchInput id="ef-q" label="Search items" placeholder="Search items" bind:value={itemQ} />
        <ul class="mt-1.5 max-h-48 overflow-y-auto flex flex-col border border-border rounded-lg" aria-labelledby="ef-items">
          {#each pickable as x (x.it.id)}
            <li>
              <label class="flex items-center gap-2 px-2.5 py-1.5 hover:bg-[var(--hover)] cursor-pointer text-[13px]">
                <input
                  type={draft.kind === 'item' ? 'radio' : 'checkbox'}
                  name="ef-item"
                  checked={draft.itemIds.includes(x.it.id)}
                  onchange={() => toggleItem(x.it.id)}
                />
                <span class="flex-1 truncate">{x.it.name}</span><span class="text-muted">{x.cat}</span>
              </label>
            </li>
          {/each}
        </ul>
      </div>
      {#if draft.kind === 'item'}
        <label class="flex items-center gap-2.5"><Switch bind:checked={draft.hidden} label="Hide it on this channel" />Hide it on this channel</label>
      {/if}
      {#if !draft.hidden}
        <div>
          <label class="lbl" for="ef-name">{draft.kind === 'item' ? 'Name on this channel (empty = same)' : 'Name'}</label>
          <input id="ef-name" class="field" bind:value={draft.name} />
        </div>
        <div>
          <label class="lbl" for="ef-desc">Description</label>
          <textarea id="ef-desc" class="field" rows="2" bind:value={draft.description}></textarea>
        </div>
        <div class="grid grid-cols-[minmax(0,1fr)_130px] gap-3">
          <div>
            <label class="lbl" for="ef-cat">Category (empty = its own)</label>
            <input id="ef-cat" class="field" bind:value={draft.category} placeholder={draft.kind === 'augment' ? 'Deals' : ''} />
          </div>
          <div>
            <label class="lbl" for="ef-price">Price ($)</label>
            <input id="ef-price" class="field mono text-right" inputmode="decimal" bind:value={draft.price} placeholder={draft.kind === 'augment' ? 'required' : 'same'} />
          </div>
        </div>
        <div>
          <label class="lbl" for="ef-photo">Photo URL</label>
          <input id="ef-photo" class="field" bind:value={draft.photo} placeholder="https://…" />
        </div>
      {/if}
      {#if draftErr}<p class="field-error !m-0" role="alert">{draftErr}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <Button onclick={() => (draftOpen = false)}>Cancel</Button>
    <Button type="submit" form="entry-form" variant="primary">Save</Button>
  {/snippet}
</Dialog>
