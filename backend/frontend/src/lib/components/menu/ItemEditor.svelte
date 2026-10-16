<script lang="ts">
  import { onMount } from 'svelte';
  import {
    storeApi, toCents,
    type Item, type Printer, type LivePrinters, type ModifierGroup, type ModifierOption, type ItemGroupLink
  } from '$lib/api';

  let {
    item, printers, live, inheritedPrinter = '', onclose, onsaved, toast
  }: {
    item: Item;
    printers: Printer[];
    live: LivePrinters | null;
    inheritedPrinter?: string;
    onclose: () => void;
    onsaved: (catID: string) => void;
    toast: (m: string, k?: 'ok' | 'error') => void;
  } = $props();

  // editable copy
  let name = $state(item.name);
  let description = $state(item.description);
  let priceStr = $state((item.price_cents / 100).toFixed(2));
  let taxPct = $state((item.tax_rate_bps / 100).toString());
  let inStock = $state(item.in_stock);
  let outUntil = $state(item.out_until ? item.out_until.slice(0, 16) : '');
  let tagsStr = $state((item.tags || []).join(', '));
  let printerId = $state<string | null>(item.printer_id);

  let attached = $state<ItemGroupLink[]>([]);
  let allGroups = $state<ModifierGroup[]>([]);
  let addGroupId = $state('');
  // group id → example option names, shown as a subtitle under each attached group
  let examples = $state<Record<string, string[]>>({});

  const upFor = (name: string) => live?.printers.find((p) => p.name === name)?.up;
  const exampleLine = (gid: string) => (examples[gid] || []).slice(0, 4).join(', ');

  async function loadExamples(id: string) {
    if (examples[id]) return;
    const opts = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${id}/options`).catch(() => []);
    examples[id] = opts.map((o) => o.name);
  }

  onMount(async () => {
    attached = await storeApi<ItemGroupLink[]>(`/me/menu/items/${item.id}/modifier-groups`).catch(() => []);
    allGroups = await storeApi<ModifierGroup[]>('/me/menu/modifier-groups').catch(() => []);
    await Promise.all(attached.map((a) => loadExamples(a.group.id)));
  });

  async function save() {
    try {
      await storeApi(`/me/menu/items/${item.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          name, description,
          price_cents: toCents(priceStr),
          tax_rate_bps: Math.round((parseFloat(taxPct) || 0) * 100),
          in_stock: inStock,
          out_until: !inStock && outUntil ? new Date(outUntil).toISOString() : null,
          tags: tagsStr.split(',').map((t) => t.trim()).filter(Boolean),
          attributes: item.attributes || {}
        })
      });
      if (printerId !== item.printer_id) {
        await storeApi(`/me/menu/items/${item.id}/printer`, { method: 'PATCH', body: JSON.stringify({ printer_id: printerId }) });
      }
      onsaved(item.category_id);
    } catch (e) { toast(String(e), 'error'); }
  }

  async function attach() {
    if (!addGroupId) return;
    try {
      const gid = addGroupId;
      await storeApi(`/me/menu/items/${item.id}/modifier-groups`, { method: 'POST', body: JSON.stringify({ group_id: gid }) });
      attached = await storeApi<ItemGroupLink[]>(`/me/menu/items/${item.id}/modifier-groups`);
      await loadExamples(gid);
      addGroupId = '';
    } catch (e) { toast(String(e), 'error'); }
  }
  async function detach(groupId: string) {
    try {
      await storeApi(`/me/menu/items/${item.id}/modifier-groups/${groupId}`, { method: 'DELETE' });
      attached = await storeApi<ItemGroupLink[]>(`/me/menu/items/${item.id}/modifier-groups`);
    } catch (e) { toast(String(e), 'error'); }
  }
</script>

<div class="fixed inset-0 bg-black/40 z-20" role="button" tabindex="-1" onclick={onclose} onkeydown={(e) => e.key === 'Escape' && onclose()}></div>
<div class="fixed right-0 top-0 h-full w-full max-w-lg z-30 bg-white dark:bg-slate-900 border-l border-slate-200 dark:border-slate-800 overflow-y-auto p-6 text-[15px]">
  <div class="flex items-center justify-between mb-5">
    <div class="text-lg font-semibold">Edit item</div>
    <button class="btn btn-ghost btn-xs" onclick={onclose}>✕ close</button>
  </div>

  <div class="space-y-4">
    <div><label class="lbl">Name</label><input class="field w-full" bind:value={name} /></div>
    <div><label class="lbl">Description</label><textarea class="field w-full h-16" bind:value={description}></textarea></div>
    <div class="grid grid-cols-2 gap-3">
      <div><label class="lbl">Price ($)</label><input class="field w-full" bind:value={priceStr} /></div>
      <div><label class="lbl">Tax rate (%)</label><input class="field w-full" bind:value={taxPct} /></div>
    </div>
    <div>
      <label class="lbl">Printer</label>
      <select class="field w-full" bind:value={printerId}>
        <option value={null}>— (use category default{#if inheritedPrinter}: {inheritedPrinter}{/if})</option>
        {#each printers as p}
          <option value={p.id}>{p.name}{#if upFor(p.name) === true} · online{:else if upFor(p.name) === false} · offline{/if}</option>
        {/each}
      </select>
      {#if printerId === null}
        <div class="text-xs text-slate-500 dark:text-slate-400 mt-1">
          {#if inheritedPrinter}Inherits <span class="font-medium">🖨 {inheritedPrinter}</span> from its category.{:else}No category default set — this item won't print anywhere.{/if}
        </div>
      {/if}
      {#if live && !live.agent_online}<div class="text-xs text-amber-600 dark:text-amber-400 mt-1">agent offline — printer status may be stale</div>{/if}
    </div>
    <div><label class="lbl">Tags (comma-separated)</label><input class="field w-full" bind:value={tagsStr} placeholder="High protein, Spicy" /></div>
    <div class="flex items-center gap-2">
      <input id="instock" type="checkbox" bind:checked={inStock} />
      <label for="instock" class="text-sm">In stock</label>
    </div>
    {#if !inStock}
      <div><label class="lbl">Out of stock until (optional)</label><input type="datetime-local" class="field w-full" bind:value={outUntil} /></div>
    {/if}

    <div class="pt-3 border-t border-slate-200 dark:border-slate-800">
      <label class="lbl">Modifier groups</label>
      <div class="space-y-1.5">
        {#each attached as a (a.group.id)}
          <div class="flex items-center justify-between gap-2 subcard px-3 py-2">
            <div class="min-w-0">
              <div class="text-sm font-medium truncate">{a.group.name}{#if a.group.required} · <span class="text-slate-500">required</span>{/if}</div>
              {#if exampleLine(a.group.id)}
                <div class="text-xs text-slate-500 dark:text-slate-400 truncate">e.g. {exampleLine(a.group.id)}</div>
              {/if}
            </div>
            <button class="btn btn-danger btn-xs shrink-0" onclick={() => detach(a.group.id)}>detach</button>
          </div>
        {:else}
          <div class="text-xs text-slate-500 dark:text-slate-400">No modifier groups attached.</div>
        {/each}
      </div>
      <div class="flex gap-1 mt-2">
        <select class="field flex-1" bind:value={addGroupId}>
          <option value="">attach a group…</option>
          {#each allGroups.filter((g) => !attached.some((a) => a.group.id === g.id)) as g}<option value={g.id}>{g.name}</option>{/each}
        </select>
        <button class="btn btn-ghost btn-xs" onclick={attach}>attach</button>
      </div>
    </div>

    <button class="btn btn-primary w-full justify-center mt-2" onclick={save}>Save item</button>
  </div>
</div>
