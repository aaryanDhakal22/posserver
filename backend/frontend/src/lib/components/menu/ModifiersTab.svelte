<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, toCents, type ModifierGroup, type ModifierOption } from '$lib/api';

  let { toast }: { toast: (m: string, k?: 'ok' | 'error') => void } = $props();

  let groups = $state<ModifierGroup[]>([]);
  let selected = $state<ModifierGroup | null>(null);
  let options = $state<ModifierOption[]>([]);
  let newGroupName = $state('');
  let newOpt = $state({ name: '', price: '0.00' });

  function err(e: unknown) { toast(String(e), 'error'); }

  async function load() { groups = await storeApi<ModifierGroup[]>('/me/menu/modifier-groups'); }
  async function selectGroup(g: ModifierGroup) {
    selected = { ...g };
    options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${g.id}/options`);
  }
  onMount(load);

  async function createGroup() {
    if (!newGroupName.trim()) return;
    try {
      const g = await storeApi<ModifierGroup>('/me/menu/modifier-groups', { method: 'POST', body: JSON.stringify({ name: newGroupName.trim(), min_select: 0, max_select: 0 }) });
      newGroupName = '';
      await load();
      await selectGroup(g);
    } catch (e) { err(e); }
  }
  async function saveGroup() {
    if (!selected) return;
    try {
      await storeApi(`/me/menu/modifier-groups/${selected.id}`, { method: 'PATCH', body: JSON.stringify({
        name: selected.name, required: selected.required, min_select: selected.min_select, max_select: selected.max_select, allow_multiple: selected.allow_multiple
      }) });
      await load();
      toast('group saved');
    } catch (e) { err(e); }
  }
  async function deleteGroup(g: ModifierGroup) {
    if (!confirm(`Delete "${g.name}"? It will be detached from all items.`)) return;
    try { await storeApi(`/me/menu/modifier-groups/${g.id}`, { method: 'DELETE' }); if (selected?.id === g.id) selected = null; await load(); }
    catch (e) { err(e); }
  }
  async function addOption() {
    if (!selected || !newOpt.name.trim()) return;
    try {
      await storeApi(`/me/menu/modifier-groups/${selected.id}/options`, { method: 'POST', body: JSON.stringify({ name: newOpt.name.trim(), price_delta_cents: toCents(newOpt.price) }) });
      newOpt = { name: '', price: '0.00' };
      options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${selected.id}/options`);
    } catch (e) { err(e); }
  }
  async function saveOption(o: ModifierOption) {
    try { await storeApi(`/me/menu/modifier-groups/${o.group_id}/options/${o.id}`, { method: 'PATCH', body: JSON.stringify({ name: o.name, price_delta_cents: o.price_delta_cents, in_stock: o.in_stock }) }); toast('option saved'); }
    catch (e) { err(e); }
  }
  async function deleteOption(o: ModifierOption) {
    try { await storeApi(`/me/menu/modifier-groups/${o.group_id}/options/${o.id}`, { method: 'DELETE' }); if (selected) options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${selected.id}/options`); }
    catch (e) { err(e); }
  }
</script>

<div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
  <aside class="card px-3 py-3 h-max">
    <div class="text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400 mb-2">Reusable groups</div>
    <div class="space-y-1">
      {#each groups as g (g.id)}
        <div class="flex items-center gap-1">
          <button class="btn btn-xs flex-1 justify-start {selected?.id === g.id ? 'btn-dark' : 'btn-ghost'}" onclick={() => selectGroup(g)}>{g.name}</button>
          <button class="btn btn-danger btn-xs" onclick={() => deleteGroup(g)}>✕</button>
        </div>
      {/each}
    </div>
    <div class="flex gap-1 mt-3 pt-3 border-t border-slate-200 dark:border-slate-800">
      <input class="field flex-1" placeholder="new group" bind:value={newGroupName} onkeydown={(e) => e.key === 'Enter' && createGroup()} />
      <button class="btn btn-primary btn-xs" onclick={createGroup}>+</button>
    </div>
  </aside>

  <section class="lg:col-span-2">
    {#if !selected}
      <div class="text-sm text-slate-500 dark:text-slate-400">Select or create a modifier group.</div>
    {:else}
      <div class="card px-4 py-4 space-y-3">
        <div><label class="lbl">Name</label><input class="field w-full" bind:value={selected.name} /></div>
        <div class="flex flex-wrap gap-4 items-end">
          <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={selected.required} /> Required</label>
          <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={selected.allow_multiple} /> Allow multiple of same</label>
          <div><label class="lbl">Min select</label><input type="number" min="0" class="field w-20" bind:value={selected.min_select} /></div>
          <div><label class="lbl">Max select (0=∞)</label><input type="number" min="0" class="field w-24" bind:value={selected.max_select} /></div>
          <button class="btn btn-primary btn-xs" onclick={saveGroup}>save rules</button>
        </div>

        <div class="pt-3 border-t border-slate-200 dark:border-slate-800">
          <label class="lbl">Options</label>
          <div class="space-y-1">
            {#each options as o (o.id)}
              <div class="flex items-center gap-2">
                <input class="field flex-1" bind:value={o.name} />
                <input class="field w-24" value={(o.price_delta_cents / 100).toFixed(2)} onchange={(e) => (o.price_delta_cents = toCents((e.target as HTMLInputElement).value))} title="price delta ($)" />
                <button class="chip {o.in_stock ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-200' : 'bg-rose-100 text-rose-800 dark:bg-rose-900/50 dark:text-rose-200'}" onclick={() => { o.in_stock = !o.in_stock; saveOption(o); }}>{o.in_stock ? 'in' : 'out'}</button>
                <button class="btn btn-primary btn-xs" onclick={() => saveOption(o)}>save</button>
                <button class="btn btn-danger btn-xs" onclick={() => deleteOption(o)}>✕</button>
              </div>
            {/each}
          </div>
          <div class="flex gap-1 mt-2">
            <input class="field flex-1" placeholder="option name" bind:value={newOpt.name} />
            <input class="field w-24" bind:value={newOpt.price} title="price delta ($)" />
            <button class="btn btn-ghost btn-xs" onclick={addOption}>add option</button>
          </div>
        </div>
      </div>
    {/if}
  </section>
</div>
