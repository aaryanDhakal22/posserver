<script lang="ts">
  import { storeApi, type MenuSize } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';

  // The store-wide size list ("12 Medium", "16 X-Large"). Items pick their
  // sizes from it (item panel), and modifier options can price per size.
  let { sizes, onreload }: { sizes: MenuSize[]; onreload: () => Promise<void> } = $props();

  let fresh = $state({ name: '', short: '' });

  async function add(e: SubmitEvent) {
    e.preventDefault();
    if (!fresh.name.trim()) return;
    try {
      await storeApi('/me/menu/sizes', { method: 'POST', body: JSON.stringify(fresh) });
      toast.success(`“${fresh.name.trim()}” added`);
      fresh = { name: '', short: '' };
      await onreload();
    } catch (err) {
      toast.error(err);
    }
  }
  async function save(s: MenuSize) {
    try {
      await storeApi(`/me/menu/sizes/${s.id}`, { method: 'PATCH', body: JSON.stringify({ name: s.name, short: s.short }) });
      toast.success('Size saved');
      await onreload();
    } catch (err) {
      toast.error(err);
    }
  }
  async function move(i: number, by: number) {
    const ids = sizes.map((s) => s.id);
    const j = i + by;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    try {
      await storeApi('/me/menu/sizes/reorder', { method: 'POST', body: JSON.stringify({ ids }) });
      await onreload();
    } catch (err) {
      toast.error(err);
    }
  }
  async function del(s: MenuSize) {
    const ok = await confirmDialog({
      title: `Delete size “${s.name}”?`,
      body: 'Every item and option price for this size goes with it.',
      confirmLabel: 'Delete size',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi(`/me/menu/sizes/${s.id}`, { method: 'DELETE' });
      toast.success('Size deleted');
      await onreload();
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<section class="card p-5 flex flex-col gap-4 max-w-[720px]" aria-labelledby="sz-title">
  <div>
    <h2 id="sz-title" class="text-base font-semibold m-0">Sizes</h2>
    <p class="help !mt-1">One list for the whole store. Give an item its sizes and prices in the item panel; toppings can cost more on a bigger size.</p>
  </div>
  {#if !sizes.length}
    <EmptyState icon="layers" title="No sizes yet" body="Add the sizes you sell, e.g. 10 Small, 12 Medium, 14 Large, 16 X-Large." />
  {:else}
    <ul class="flex flex-col gap-2" aria-label="Sizes">
      {#each sizes as s, i (s.id)}
        <li class="flex flex-wrap items-center gap-2">
          <input class="field flex-1 min-w-[160px]" bind:value={s.name} aria-label="Size name" />
          <input class="field !w-28" bind:value={s.short} placeholder="Short" aria-label="Short name for {s.name}" />
          <Button size="sm" onclick={() => save(s)}>Save</Button>
          <Button size="sm" variant="ghost" icon="chevron-down" class="rotate-180" aria-label="Move {s.name} up" disabled={i === 0} onclick={() => move(i, -1)} />
          <Button size="sm" variant="ghost" icon="chevron-down" aria-label="Move {s.name} down" disabled={i === sizes.length - 1} onclick={() => move(i, 1)} />
          <Button size="sm" variant="ghost" icon="trash" aria-label="Delete {s.name}" onclick={() => del(s)} />
        </li>
      {/each}
    </ul>
  {/if}
  <form class="flex flex-wrap gap-2 pt-3 border-t border-border" onsubmit={add}>
    <input class="field flex-1 min-w-[160px]" placeholder="New size, e.g. 12 Medium" aria-label="New size name" bind:value={fresh.name} />
    <input class="field !w-28" placeholder="Short" aria-label="New size short name" bind:value={fresh.short} />
    <Button type="submit" variant="primary" icon="plus">Add size</Button>
  </form>
</section>
