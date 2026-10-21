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
  let priceErr = $state('');
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
      printerId !== it0.printer_id
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
    saving = true;
    try {
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
      if (printerId !== it0.printer_id) {
        await storeApi(`/me/menu/items/${it0.id}/printer`, { method: 'PATCH', body: JSON.stringify({ printer_id: printerId }) });
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
    {#if priceErr}<p class="field-error !-mt-2" role="alert">{priceErr}</p>{/if}
    <div>
      <label class="lbl" for="ie-printer">Kitchen printer</label>
      <select id="ie-printer" class="field" bind:value={printerId}>
        <option value={null}>{inheritedPrinter ? `Same as category (${inheritedPrinter})` : 'None — prints on every station'}</option>
        {#each printers as p (p.id)}
          <option value={p.id}>{printerLabel(p, printers)}{upFor(p.name) === true ? ' · online' : upFor(p.name) === false ? ' · offline' : ''}</option>
        {/each}
      </select>
      {#if printerId === null && !inheritedPrinter}
        <p class="help !text-warn">Neither the item nor its category has a printer, so it prints on every kitchen station.</p>
      {/if}
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
