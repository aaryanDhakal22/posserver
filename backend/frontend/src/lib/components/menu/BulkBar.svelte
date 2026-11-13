<script lang="ts">
  import { dollars, errText, printerLabel, toCents, type MenuSize, type Printer } from '$lib/api';
  import { applyBulk, priceChanges, previewPrices, type BulkChange, type Picked, type PriceMode, type PriceRule } from '$lib/store/menuBulk';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';

  // Store › Menu bulk edit: what to do with the selected items. Every action is one request (all or
  // nothing) and its toast has Undo (the old values sent back). Used by the Items tab and Find problems.
  let {
    picked,
    printers,
    categories,
    sizes,
    onapplied,
    onclear
  }: {
    picked: Picked[];
    printers: Printer[];
    /** Every category of every menu (the "Move to" list). */
    categories: { id: string; name: string; menu: string }[];
    sizes: MenuSize[];
    /** After a save (undo: true after its Undo, which keeps the selection made since). */
    onapplied: (undo?: boolean) => void | Promise<void>;
    onclear: () => void;
  } = $props();

  const n = $derived(picked.length);
  const items = (k: number) => `${k} item${k === 1 ? '' : 's'}`;
  const sizeName = (id: string) => sizes.find((s) => s.id === id)?.name ?? '?';
  let busy = $state(false);
  let err = $state('');

  async function run(changes: BulkChange[], undo: BulkChange[], what: string) {
    if (busy) return false;
    busy = true;
    err = '';
    try {
      const k = await applyBulk(changes);
      await onapplied();
      toast.success(`${what}: ${items(k)} saved`, {
        action: {
          label: 'Undo',
          run: async () => {
            try {
              await applyBulk(undo);
              await onapplied(true);
              toast.success(`Undone: ${items(undo.length)} back as they were`);
            } catch (e) {
              toast.error(e);
            }
          }
        }
      });
      return true;
    } catch (e) {
      err = errText(e);
      return false;
    } finally {
      busy = false;
    }
  }

  // --- stock -------------------------------------------------------------------------
  function stock(v: boolean) {
    const changes = picked.map(({ item }) => ({ item_id: item.id, in_stock: v }));
    const undo = picked.map(({ item }) => ({ item_id: item.id, in_stock: item.in_stock }));
    run(changes, undo, v ? 'Back in stock' : 'Out of stock').then((ok) => !ok && err && toast.error(err));
  }

  // --- printer -------------------------------------------------------------------------
  let printerOpen = $state(false);
  let printerPick = $state(''); // '' = the category's · '__none' = no kitchen ticket · printer id
  async function savePrinter(e: SubmitEvent) {
    e.preventDefault();
    const changes: BulkChange[] = picked.map(({ item }) =>
      printerPick === '__none' ? { item_id: item.id, no_kitchen_ticket: true } : { item_id: item.id, printer_id: printerPick, no_kitchen_ticket: false }
    );
    const undo: BulkChange[] = picked.map(({ item }) => ({ item_id: item.id, printer_id: item.printer_id ?? '', no_kitchen_ticket: !!item.no_kitchen_ticket }));
    const label = printerPick === '__none' ? 'No kitchen ticket' : printerPick ? `Prints on ${printers.find((p) => p.id === printerPick)?.name}` : 'Same printer as the category';
    if (await run(changes, undo, label)) printerOpen = false;
  }

  // --- category ------------------------------------------------------------------------
  let moveOpen = $state(false);
  let moveTo = $state('');
  const movable = $derived(picked.filter(({ item }) => !(item as { quick_add?: boolean }).quick_add));
  async function saveMove(e: SubmitEvent) {
    e.preventDefault();
    if (!moveTo) return (err = 'Pick a category.');
    const changes = movable.map(({ item }) => ({ item_id: item.id, category_id: moveTo }));
    const undo = movable.map(({ item }) => ({ item_id: item.id, category_id: item.category_id }));
    if (await run(changes, undo, `Moved to ${categories.find((c) => c.id === moveTo)?.name}`)) moveOpen = false;
  }
  const menusOf = $derived([...new Set(categories.map((c) => c.menu))]);

  // --- prices ----------------------------------------------------------------------------
  let priceOpen = $state(false);
  let mode = $state<PriceMode>('add');
  let amount = $state('');
  let end99 = $state(false);
  const rule = $derived.by((): PriceRule | null => {
    const t = amount.trim();
    if (!t) return null;
    if (mode === 'percent') {
      const p = Number(t.replace('%', ''));
      return Number.isFinite(p) && p > -100 && p <= 500 ? { mode, amount: p, end99 } : null;
    }
    const c = toCents(t);
    return c === null || (mode === 'set' && c < 0) ? null : { mode, amount: c, end99 };
  });
  const preview = $derived(rule ? previewPrices(picked, rule, sizeName) : []);
  const below = $derived(preview.some((p) => p.rows.some((r) => r.to === null)));
  async function savePrices(e: SubmitEvent) {
    e.preventDefault();
    if (!rule) return (err = mode === 'percent' ? 'A percent from -99 to 500, like 10 or -5.' : 'A dollar amount, like 1.00 or -0.50.');
    const c = priceChanges(picked, rule);
    if (!c) return (err = 'Some prices would go below $0.00. Nothing was changed.');
    if (await run(c.changes, c.undo, 'Prices changed')) priceOpen = false;
  }
  function openDialog(which: 'printer' | 'move' | 'price') {
    err = '';
    if (which === 'printer') {
      printerPick = '';
      printerOpen = true;
    } else if (which === 'move') {
      moveTo = '';
      moveOpen = true;
    } else {
      amount = '';
      end99 = false;
      priceOpen = true;
    }
  }
</script>

{#if n}
  <div class="bulkbar" role="region" aria-label="Bulk edit" data-testid="bulk-bar">
    <span class="font-semibold whitespace-nowrap" data-testid="bulk-count">{items(n)} selected</span>
    <div class="bulk-actions">
      <Button size="sm" icon="printer" onclick={() => openDialog('printer')} disabled={busy}>Printer…</Button>
      <Button size="sm" icon="layers" onclick={() => openDialog('move')} disabled={busy}>Move to…</Button>
      <Button size="sm" icon="percent" onclick={() => openDialog('price')} disabled={busy}>Change prices…</Button>
      <Button size="sm" icon="check" onclick={() => stock(true)} disabled={busy}>In stock</Button>
      <Button size="sm" icon="ban" onclick={() => stock(false)} disabled={busy}>Out of stock</Button>
      <Button size="sm" variant="ghost" icon="x" onclick={onclear}>Clear</Button>
    </div>
  </div>
{/if}

<Dialog bind:open={printerOpen} title="Where {items(n)} print" description="Each item's own setting: the category's default printer stays as it is.">
  <form id="bulk-printer" class="flex flex-col gap-3" onsubmit={savePrinter}>
    <div>
      <label class="lbl" for="bulk-printer-pick">Kitchen printer</label>
      <select id="bulk-printer-pick" class="field" bind:value={printerPick}>
        <option value="">Same as their category</option>
        {#each printers as p (p.id)}<option value={p.id}>{printerLabel(p, printers)}</option>{/each}
        <option value="__none">No kitchen ticket (receipt only)</option>
      </select>
      <p class="help">No kitchen ticket: no station ticket, still on the customer receipt (drinks, sauces on the side).</p>
    </div>
    {#if err}<p class="field-error !m-0" role="alert">{err}</p>{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (printerOpen = false)}>Cancel</Button>
    <Button type="submit" form="bulk-printer" variant="primary" loading={busy}>Save {items(n)}</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={moveOpen} title="Move {items(movable.length)}" description="They go to the end of the category you pick.">
  <form id="bulk-move" class="flex flex-col gap-3" onsubmit={saveMove}>
    <div>
      <label class="lbl" for="bulk-move-to">Category</label>
      <select id="bulk-move-to" class="field" bind:value={moveTo}>
        <option value="">Pick a category…</option>
        {#each menusOf as m (m)}
          <optgroup label={m}>
            {#each categories.filter((c) => c.menu === m) as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
          </optgroup>
        {/each}
      </select>
      {#if movable.length < n}<p class="help">The Quick add item stays in its own category.</p>{/if}
    </div>
    {#if err}<p class="field-error !m-0" role="alert">{err}</p>{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (moveOpen = false)}>Cancel</Button>
    <Button type="submit" form="bulk-move" variant="primary" loading={busy} disabled={!movable.length}>Move</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={priceOpen} title="Change the prices of {items(n)}" description="Items with sizes change every size's price." width={520}>
  <form id="bulk-price" class="flex flex-col gap-3.5" onsubmit={savePrices}>
    <SegmentedControl
      label="How"
      options={[
        { value: 'add', label: 'Raise or lower by $' },
        { value: 'percent', label: 'By %' },
        { value: 'set', label: 'Set to $' }
      ]}
      bind:value={mode}
    />
    <div class="flex flex-wrap items-end gap-4">
      <div>
        <label class="lbl" for="bulk-amount">{mode === 'percent' ? 'Percent (− lowers)' : mode === 'set' ? 'New price ($)' : 'Amount ($, − lowers)'}</label>
        <input id="bulk-amount" class="field mono text-right !w-36" inputmode="decimal" bind:value={amount} placeholder={mode === 'percent' ? '10' : '1.00'} />
      </div>
      {#if mode !== 'set'}
        <label class="check-row pb-2"><input type="checkbox" class="check" bind:checked={end99} />Round up to .99</label>
      {/if}
    </div>
    {#if preview.length}
      <div class="preview" data-testid="bulk-preview">
        {#each preview.slice(0, 8) as p (p.item.id)}
          <div class="pv-item">
            <span class="font-medium truncate">{p.item.name}</span>
            <span class="pv-rows text-[13px] text-text2">
              {#each p.rows as r, i (i)}
                <span class="text-muted">{r.label}</span>
                <span class="mono">{dollars(r.from)} → <b class={r.to === null ? 'text-bad' : ''}>{r.to === null ? 'below $0' : dollars(r.to)}</b></span>
              {/each}
            </span>
          </div>
        {/each}
        {#if preview.length > 8}<p class="help !mt-1">…and {items(preview.length - 8)} more.</p>{/if}
      </div>
    {/if}
    {#if below}<p class="field-error !m-0" role="alert">Some prices would go below $0.00.</p>{/if}
    {#if err}<p class="field-error !m-0" role="alert">{err}</p>{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (priceOpen = false)}>Cancel</Button>
    <Button type="submit" form="bulk-price" variant="primary" loading={busy} disabled={!rule || below}>Change {items(n)}</Button>
  {/snippet}
</Dialog>

<style>
  .bulkbar {
    position: sticky;
    bottom: 12px;
    z-index: 6;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px 14px;
    padding: 10px 14px;
    background: var(--surface);
    border: 1px solid var(--brand);
    border-radius: 14px;
    box-shadow: var(--shadow);
  }
  .bulk-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    flex: 1;
    justify-content: flex-end;
  }
  @media (max-width: 640px) {
    .bulkbar {
      flex-direction: column;
      align-items: stretch;
      bottom: 8px;
    }
    .bulk-actions {
      justify-content: flex-start;
      gap: 6px;
    }
  }
  .preview {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border-radius: 12px;
    background: var(--elev);
    border: 1px solid var(--border);
    max-height: 260px;
    overflow: auto;
  }
  .pv-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .pv-rows {
    display: grid;
    grid-template-columns: max-content max-content;
    gap: 0 14px;
  }
</style>
