<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import {
    storeApi,
    toCents,
    centsInput,
    type Item,
    type Printer,
    type LivePrinters,
    type ModifierGroup,
    type ModifierOption,
    type ItemGroupLink,
    type MenuSize,
    type BankItem,
    printerLabel
  } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import type { PiLink } from '$lib/store/pi';

  // The item side panel. The parent keys it by item id, so the editable copy
  // below is taken once per item.
  let {
    item,
    categoryName = '',
    printers,
    live,
    piState = 'unknown',
    inheritedPrinter = '',
    categoryNoKitchen = false,
    sizes = [],
    bank,
    onclose,
    onsaved,
    ondeleted
  }: {
    item: Item;
    categoryName?: string;
    printers: Printer[];
    live: LivePrinters | null;
    piState?: PiLink;
    inheritedPrinter?: string;
    /** The category has "no kitchen ticket" (reaches an item without its own printer). */
    categoryNoKitchen?: boolean;
    /** The store's sizes and this item's bank data (main 3.0). */
    sizes?: MenuSize[];
    bank?: BankItem;
    onclose: () => void;
    onsaved: (catID: string) => void;
    ondeleted: (catID: string) => void;
  } = $props();

  // <input type="datetime-local"> holds local wall-clock time with no zone;
  // out_until is a UTC instant. Convert both ways so a save doesn't shift it.
  function toLocalInput(iso: string | null | undefined): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  const it0 = untrack(() => item);
  let open = $state(true);
  let name = $state(it0.name);
  let description = $state(it0.description);
  let priceStr = $state(centsInput(it0.price_cents));
  let taxPct = $state((it0.tax_rate_bps / 100).toString());
  let inStock = $state(it0.in_stock);
  let outUntil = $state(toLocalInput(it0.out_until));
  let tagsStr = $state((it0.tags || []).join(', '));
  let printerId = $state<string | null>(it0.printer_id);
  let noKitchen = $state(!!it0.no_kitchen_ticket);
  // Receipt only: its own flag, or its category's when it has no printer of its own.
  const receiptOnly = $derived(noKitchen || (printerId === null && categoryNoKitchen));
  let priceErr = $state('');
  // Bank data: kitchen name, SKU, sizes (saved with the item).
  const b0 = untrack(() => ({
    kitchen: bank?.kitchen_name ?? '',
    sku: bank?.sku ?? '',
    sizes: (bank?.sizes ?? []).map((x) => ({ size_id: x.size_id, price: centsInput(x.price_cents) }))
  }));
  let kitchenName = $state(b0.kitchen);
  let sku = $state(b0.sku);
  let sizeRows = $state(b0.sizes.map((x) => ({ ...x })));
  let addSizeId = $state('');
  const bankDirty = $derived(
    kitchenName !== b0.kitchen || sku !== b0.sku || JSON.stringify(sizeRows) !== JSON.stringify(b0.sizes)
  );
  const sizeName = (id: string) => sizes.find((x) => x.id === id)?.name ?? '?';
  function addSize() {
    if (!addSizeId) return;
    sizeRows = [...sizeRows, { size_id: addSizeId, price: priceStr }];
    // Keep the rows in the store's size order.
    const at = (id: string) => sizes.findIndex((x) => x.id === id);
    sizeRows.sort((a, b) => at(a.size_id) - at(b.size_id));
    addSizeId = '';
  }
  let saving = $state(false);

  let attached = $state<ItemGroupLink[]>([]);
  let allGroups = $state<ModifierGroup[]>([]);
  let addGroupId = $state('');
  // group id → example option names, shown under each attached group
  let examples = $state<Record<string, string[]>>({});

  // Unsaved edits to the form above (modifier groups save on their own).
  const dirty = $derived(
    name !== it0.name ||
      description !== it0.description ||
      priceStr !== centsInput(it0.price_cents) ||
      taxPct !== (it0.tax_rate_bps / 100).toString() ||
      inStock !== it0.in_stock ||
      (!inStock && outUntil !== toLocalInput(it0.out_until)) ||
      tagsStr !== (it0.tags || []).join(', ') ||
      printerId !== it0.printer_id ||
      noKitchen !== !!it0.no_kitchen_ticket ||
      bankDirty
  );
  async function beforeClose(): Promise<boolean> {
    if (!dirty || saving) return !saving;
    return confirmDialog({
      title: 'Discard changes?',
      body: `Your edits to “${it0.name}” haven't been saved.`,
      confirmLabel: 'Discard changes',
      cancelLabel: 'Keep editing',
      danger: true
    });
  }

  const upFor = (n: string) => (piState === 'online' ? live?.printers?.find((p) => p.name === n)?.up : undefined);
  const exampleLine = (gid: string) => (examples[gid] || []).slice(0, 4).join(', ');

  async function loadExamples(id: string) {
    if (examples[id]) return;
    const opts = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${id}/options`).catch(() => []);
    examples[id] = opts.map((o) => o.name);
  }

  onMount(async () => {
    attached = await storeApi<ItemGroupLink[]>(`/me/menu/items/${it0.id}/modifier-groups`).catch(() => []);
    allGroups = await storeApi<ModifierGroup[]>('/me/menu/modifier-groups').catch(() => []);
    await Promise.all(attached.map((a) => loadExamples(a.group.id)));
  });

  async function save() {
    priceErr = '';
    const priceCents = toCents(priceStr);
    if (priceCents === null || priceCents < 0) {
      priceErr = `“${priceStr}” isn't a valid amount — use e.g. 12.50 (or 0 for free).`;
      return;
    }
    // "2026-10-22T09:00" (no zone) parses as local time — the instant the person picked.
    const outUntilDate = !inStock && outUntil ? new Date(outUntil) : null;
    if (outUntilDate && isNaN(outUntilDate.getTime())) {
      toast.error('“Out of stock until” isn’t a valid date/time. Not saved.');
      return;
    }
    const sizeCents = sizeRows.map((r) => ({ size_id: r.size_id, price_cents: toCents(r.price) }));
    const badSize = sizeCents.findIndex((r) => r.price_cents === null || r.price_cents < 0);
    if (badSize >= 0) {
      priceErr = `${sizeName(sizeRows[badSize].size_id)}: “${sizeRows[badSize].price}” isn't a valid price.`;
      return;
    }
    saving = true;
    try {
      if (bankDirty) {
        await storeApi(`/me/menu/items/${it0.id}/bank`, {
          method: 'PUT',
          body: JSON.stringify({ kitchen_name: kitchenName, sku, sizes: sizeCents })
        });
      }
      await storeApi(`/me/menu/items/${it0.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          name,
          description,
          price_cents: priceCents,
          tax_rate_bps: Math.round((parseFloat(taxPct) || 0) * 100),
          in_stock: inStock,
          out_until: outUntilDate ? outUntilDate.toISOString() : null,
          tags: tagsStr
            .split(',')
            .map((t) => t.trim())
            .filter(Boolean),
          attributes: it0.attributes || {}
        })
      });
      if (printerId !== it0.printer_id || noKitchen !== !!it0.no_kitchen_ticket) {
        await storeApi(`/me/menu/items/${it0.id}/printer`, {
          method: 'PATCH',
          body: JSON.stringify({ printer_id: printerId, no_kitchen_ticket: noKitchen })
        });
      }
      onsaved(it0.category_id);
    } catch (e) {
      toast.error(e);
    } finally {
      saving = false;
    }
  }

  async function del() {
    const ok = await confirmDialog({ title: `Delete “${it0.name}”?`, body: 'It disappears from the menu right away.', confirmLabel: 'Delete item', danger: true });
    if (!ok) return;
    try {
      await storeApi(`/me/menu/items/${it0.id}`, { method: 'DELETE' });
      ondeleted(it0.category_id);
    } catch (e) {
      toast.error(e);
    }
  }

  async function attach() {
    if (!addGroupId) return;
    try {
      const gid = addGroupId;
      await storeApi(`/me/menu/items/${it0.id}/modifier-groups`, { method: 'POST', body: JSON.stringify({ group_id: gid }) });
      attached = await storeApi<ItemGroupLink[]>(`/me/menu/items/${it0.id}/modifier-groups`);
      await loadExamples(gid);
      addGroupId = '';
      toast.success(`${attached.find((a) => a.group.id === gid)?.group.name ?? 'Group'} attached to ${it0.name}`);
    } catch (e) {
      toast.error(e);
    }
  }
  async function detach(groupId: string) {
    try {
      const gname = attached.find((a) => a.group.id === groupId)?.group.name ?? 'Group';
      await storeApi(`/me/menu/items/${it0.id}/modifier-groups/${groupId}`, { method: 'DELETE' });
      attached = await storeApi<ItemGroupLink[]>(`/me/menu/items/${it0.id}/modifier-groups`);
      toast.success(`${gname} removed from ${it0.name}`);
    } catch (e) {
      toast.error(e);
    }
  }
</script>

<Drawer bind:open title={it0.name} eyebrow={categoryName} {onclose} {beforeClose}>
  <form
    id="item-form"
    class="flex flex-col gap-4"
    onsubmit={(e) => {
      e.preventDefault();
      save();
    }}
  >
    <div>
      <label class="lbl" for="ie-name">Name</label>
      <input id="ie-name" class="field" bind:value={name} />
      <p class="help">Online orders match items by this name (size ignored).</p>
    </div>
    <div>
      <label class="lbl" for="ie-desc">Description</label>
      <textarea id="ie-desc" class="field" rows="2" bind:value={description} placeholder="Shown to call-center operators"></textarea>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="lbl" for="ie-price">Price ($)</label>
        <input id="ie-price" class="field mono text-right" inputmode="decimal" bind:value={priceStr} aria-invalid={!!priceErr || undefined} />
      </div>
      <div>
        <label class="lbl" for="ie-tax">Tax rate (%)</label>
        <input id="ie-tax" class="field mono text-right" inputmode="decimal" bind:value={taxPct} />
      </div>
    </div>
    <div>
      <span class="lbl" id="ie-sizes">Sizes</span>
      {#if sizeRows.length}
        <ul class="flex flex-col gap-1.5" aria-labelledby="ie-sizes">
          {#each sizeRows as r, i (r.size_id)}
            <li class="flex items-center gap-2">
              <span class="flex-1 min-w-0 truncate">{sizeName(r.size_id)}</span>
              <input class="field mono text-right !w-28" inputmode="decimal" bind:value={r.price} aria-label="Price for {sizeName(r.size_id)} ($)" />
              <Button size="xs" variant="ghost" icon="x" aria-label="Remove {sizeName(r.size_id)}" onclick={() => sizeRows.splice(i, 1)} />
            </li>
          {/each}
        </ul>
        <p class="help">With sizes, the size prices are what customers pay; the price above is only used without one.</p>
      {/if}
      {#if sizes.filter((x) => !sizeRows.some((r) => r.size_id === x.id)).length}
        <div class="flex gap-2 mt-1.5">
          <label for="ie-addsize" class="sr-only">Add a size</label>
          <select id="ie-addsize" class="field flex-1" bind:value={addSizeId}>
            <option value="">Add a size…</option>
            {#each sizes.filter((x) => !sizeRows.some((r) => r.size_id === x.id)) as x (x.id)}<option value={x.id}>{x.name}</option>{/each}
          </select>
          <Button icon="plus" disabled={!addSizeId} onclick={addSize}>Add</Button>
        </div>
      {:else if !sizes.length}
        <p class="help !mt-0">Sold in one size. Add the store's sizes on the Sizes tab to price this by size.</p>
      {/if}
    </div>
    {#if priceErr}<p class="field-error !-mt-2" role="alert">{priceErr}</p>{/if}
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="lbl" for="ie-kitchen">Kitchen name</label>
        <input id="ie-kitchen" class="field" bind:value={kitchenName} placeholder="Same as name" />
      </div>
      <div>
        <label class="lbl" for="ie-sku">SKU / POS code</label>
        <input id="ie-sku" class="field mono" bind:value={sku} />
      </div>
    </div>
    <div>
      <label class="lbl" for="ie-printer">Kitchen printer</label>
      <select id="ie-printer" class="field" bind:value={printerId} disabled={noKitchen}>
        <option value={null}
          >{categoryNoKitchen
            ? 'Same as category (no kitchen ticket)'
            : inheritedPrinter
              ? `Same as category (${inheritedPrinter})`
              : 'None — prints on every station'}</option
        >
        {#each printers as p (p.id)}
          <option value={p.id}>{printerLabel(p, printers)}{upFor(p.name) === true ? ' · online' : upFor(p.name) === false ? ' · offline' : ''}</option>
        {/each}
      </select>
      {#if printerId === null && !inheritedPrinter && !receiptOnly}
        <p class="help !text-warn">Neither the item nor its category has a printer, so it prints on every kitchen station.</p>
      {/if}
      <div class="flex items-center gap-2.5 mt-2">
        <Switch bind:checked={noKitchen} label="No kitchen ticket" />
        <span>No kitchen ticket</span>
      </div>
      <p class="help" data-testid="no-kitchen-hint">
        Prints on the receipt only: no station ticket, not in 'Also on this order'.{receiptOnly && !noKitchen
          ? ' Its category is set this way.'
          : ''}
      </p>
      {#if piState === 'offline'}<p class="help !text-warn">The Pi is offline — printer status may be stale.</p>{/if}
    </div>
    <div>
      <label class="lbl" for="ie-tags">Tags (comma-separated)</label>
      <input id="ie-tags" class="field" bind:value={tagsStr} placeholder="High protein, Spicy" />
    </div>
    <div class="flex items-center gap-2.5">
      <Switch bind:checked={inStock} label="In stock" />
      <span>{inStock ? 'In stock' : 'Out of stock — the tablet shows it as 86’d'}</span>
    </div>
    {#if !inStock}
      <div>
        <label class="lbl" for="ie-until">Out of stock until (optional)</label>
        <input id="ie-until" type="datetime-local" class="field" bind:value={outUntil} />
      </div>
    {/if}
  </form>

  <div class="mt-5 pt-4 border-t border-border">
    <span class="lbl !mb-0.5" id="ie-groups">Modifier groups</span>
    <p class="help !mt-0 mb-2">Attaching or removing a group saves right away.</p>
    <ul class="flex flex-col gap-1.5" aria-labelledby="ie-groups">
      {#each attached as a (a.group.id)}
        <li class="subcard flex items-center justify-between gap-2 px-3 py-2">
          <div class="min-w-0">
            <div class="font-medium truncate">{a.group.name}{#if a.group.required}<span class="text-muted font-normal"> · required</span>{/if}</div>
            {#if exampleLine(a.group.id)}<div class="text-xs text-muted truncate">e.g. {exampleLine(a.group.id)}</div>{/if}
          </div>
          <Button size="xs" variant="ghost" icon="x" aria-label="Detach {a.group.name}" onclick={() => detach(a.group.id)} />
        </li>
      {:else}
        <li class="text-[13px] text-muted">No modifier groups attached.</li>
      {/each}
    </ul>
    <div class="flex gap-2 mt-2">
      <label for="ie-attach" class="sr-only">Attach a modifier group</label>
      <select id="ie-attach" class="field flex-1" bind:value={addGroupId}>
        <option value="">Attach a group…</option>
        {#each allGroups.filter((g) => !attached.some((a) => a.group.id === g.id)) as g (g.id)}<option value={g.id}>{g.name}</option>{/each}
      </select>
      <Button icon="plus" disabled={!addGroupId} onclick={attach}>Attach</Button>
    </div>
  </div>

  {#snippet footer()}
    <Button type="submit" form="item-form" variant="primary" class="flex-1" loading={saving}>Save item</Button>
    <Button variant="danger-ghost" onclick={del}><Icon name="trash" size={16} />Delete</Button>
  {/snippet}
</Drawer>
