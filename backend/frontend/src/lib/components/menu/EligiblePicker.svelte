<script lang="ts">
  import type { Bank, DealEligible } from '$lib/api';
  import SearchInput from '$lib/ui/SearchInput.svelte';

  // What a deal applies to: whole categories, single items, and optionally
  // only some sizes (by the store's size list).
  let { bank, value = $bindable(), idPrefix }: { bank: Bank | null; value: DealEligible; idPrefix: string } = $props();

  let q = $state('');
  const cats = $derived((bank?.menus ?? []).flatMap((m) => m.categories));
  const items = $derived(cats.flatMap((c) => c.items.map((it) => ({ it, cat: c.name }))));
  const shown = $derived(items.filter((x) => !q.trim() || x.it.name.toLowerCase().includes(q.trim().toLowerCase())).slice(0, 40));

  function toggle(list: 'items' | 'categories' | 'sizes', id: string) {
    const cur = value[list] ?? [];
    value[list] = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id];
  }
  const itemName = (id: string) => items.find((x) => x.it.id === id)?.it.name ?? '(gone)';
</script>

<div class="flex flex-col gap-2.5">
  <div>
    <span class="lbl" id="{idPrefix}-cats">Whole categories</span>
    <div class="flex flex-wrap gap-1.5" role="group" aria-labelledby="{idPrefix}-cats">
      {#each cats as c (c.id)}
        <button type="button" class="fchip" aria-pressed={(value.categories ?? []).includes(c.id)} onclick={() => toggle('categories', c.id)}>{c.name}</button>
      {/each}
    </div>
  </div>
  <div>
    <span class="lbl" id="{idPrefix}-items">Or single items</span>
    {#if (value.items ?? []).length}
      <div class="flex flex-wrap gap-1.5 mb-1.5">
        {#each value.items ?? [] as id (id)}
          <button type="button" class="fchip" aria-pressed="true" onclick={() => toggle('items', id)}>{itemName(id)} ✕</button>
        {/each}
      </div>
    {/if}
    <SearchInput id="{idPrefix}-q" label="Search items" placeholder="Search items" bind:value={q} />
    <ul class="mt-1.5 max-h-36 overflow-y-auto border border-border rounded-lg" aria-labelledby="{idPrefix}-items">
      {#each shown as x (x.it.id)}
        <li>
          <label class="flex items-center gap-2 px-2.5 py-1 text-[13px] hover:bg-[var(--hover)] cursor-pointer">
            <input type="checkbox" checked={(value.items ?? []).includes(x.it.id)} onchange={() => toggle('items', x.it.id)} />
            <span class="flex-1 truncate">{x.it.name}</span><span class="text-muted">{x.cat}</span>
          </label>
        </li>
      {/each}
    </ul>
  </div>
  {#if bank?.sizes.length}
    <div>
      <span class="lbl" id="{idPrefix}-sizes">Only these sizes (none = any size)</span>
      <div class="flex flex-wrap gap-1.5" role="group" aria-labelledby="{idPrefix}-sizes">
        {#each bank.sizes as s (s.id)}
          <button type="button" class="fchip" aria-pressed={(value.sizes ?? []).includes(s.id)} onclick={() => toggle('sizes', s.id)}>{s.name}</button>
        {/each}
      </div>
    </div>
  {/if}
</div>
