<script lang="ts">
  import { storeApi, type Category, type Item, type Menu, type Printer } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Badge from '$lib/ui/Badge.svelte';

  // Menus (create / rename / available / delete) and the active menu's
  // categories (rename / default printer / visible / delete / add).
  let {
    menus,
    active,
    categories,
    itemsByCat,
    printers,
    onselect,
    onreload
  }: {
    menus: Menu[];
    active: Menu | null;
    categories: Category[];
    itemsByCat: Record<string, Item[]>;
    printers: Printer[];
    onselect: (m: Menu) => void;
    onreload: () => Promise<void>;
  } = $props();

  let newMenu = $state('');
  let newCat = $state('');

  async function createMenu(e: SubmitEvent) {
    e.preventDefault();
    if (!newMenu.trim()) return;
    try {
      await storeApi('/me/menu/menus', { method: 'POST', body: JSON.stringify({ name: newMenu.trim() }) });
      newMenu = '';
      await onreload();
      toast.success('Menu created');
    } catch (err) {
      toast.error(err);
    }
  }
  async function saveMenu(m: Menu, patch: Partial<Menu>, msg = '') {
    try {
      await storeApi(`/me/menu/menus/${m.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ name: m.name, description: m.description, available: m.available, ...patch })
      });
      await onreload();
      if (msg) toast.success(msg);
    } catch (err) {
      toast.error(err);
      await onreload();
    }
  }
  function renameMenu(m: Menu, el: HTMLInputElement) {
    const v = el.value.trim();
    if (!v) {
      el.value = m.name;
      return;
    }
    if (v !== m.name) saveMenu(m, { name: v }, 'Menu renamed');
  }
  async function deleteMenu(m: Menu) {
    const ok = await confirmDialog({
      title: `Delete menu “${m.name}”?`,
      body: 'Deletes all of its categories and items.',
      confirmLabel: 'Delete menu',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi(`/me/menu/menus/${m.id}`, { method: 'DELETE' });
      await onreload();
      toast.success('Menu deleted');
    } catch (err) {
      toast.error(err);
    }
  }

  async function createCategory(e: SubmitEvent) {
    e.preventDefault();
    if (!active || !newCat.trim()) return;
    try {
      const n = newCat.trim();
      await storeApi(`/me/menu/menus/${active.id}/categories`, { method: 'POST', body: JSON.stringify({ name: n }) });
      newCat = '';
      await onreload();
      toast.success(`“${n}” added to ${active.name}`);
    } catch (err) {
      toast.error(err);
    }
  }
  async function saveCategory(c: Category, patch: Partial<Category>, msg = '') {
    try {
      await storeApi(`/me/menu/categories/${c.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          name: c.name,
          description: c.description,
          available: c.available,
          default_printer_id: c.default_printer_id,
          ...patch
        })
      });
      await onreload();
      if (msg) toast.success(msg);
    } catch (err) {
      toast.error(err);
      await onreload();
    }
  }
  function renameCategory(c: Category, el: HTMLInputElement) {
    const v = el.value.trim();
    if (!v) {
      el.value = c.name;
      return;
    }
    if (v !== c.name) saveCategory(c, { name: v }, 'Category renamed');
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
      await onreload();
      toast.success('Category deleted');
    } catch (err) {
      toast.error(err);
    }
  }
  const enterBlurs = (e: KeyboardEvent) => {
    if (e.key === 'Enter') (e.currentTarget as HTMLInputElement).blur();
  };
</script>

<div class="grid grid-cols-1 xl:grid-cols-[minmax(0,380px)_minmax(0,1fr)] gap-4 items-start">
  <Card title="Menus" subtitle="Turn a menu off to hide it everywhere">
    <ul class="flex flex-col gap-2">
      {#each menus as m (m.id)}
        <li class="flex items-center gap-2">
          <input
            class="field flex-1 min-w-0"
            value={m.name}
            aria-label="Menu name"
            onblur={(e) => renameMenu(m, e.currentTarget)}
            onkeydown={enterBlurs}
          />
          <Switch checked={m.available} label="{m.name} available" onchange={(v) => saveMenu(m, { available: v }, v ? `${m.name} is on` : `${m.name} is off — hidden everywhere`)} />
          {#if active?.id === m.id}
            <Badge tone="brand">Showing</Badge>
          {:else}
            <Button size="sm" variant="ghost" onclick={() => onselect(m)}>Show</Button>
          {/if}
          <Button size="sm" variant="ghost" icon="trash" aria-label="Delete menu {m.name}" onclick={() => deleteMenu(m)} />
        </li>
      {/each}
    </ul>
    <form class="flex gap-2 mt-3 pt-3 border-t border-border" onsubmit={createMenu}>
      <label for="new-menu" class="sr-only">New menu name</label>
      <input id="new-menu" class="field flex-1" placeholder="New menu, e.g. Lunch" bind:value={newMenu} />
      <Button type="submit" icon="plus">Add menu</Button>
    </form>
  </Card>

  {#if active}
    <Card title="Categories in {active.name}" subtitle="Items without their own printer use the category's default printer" padded={false}>
      <div role="table" aria-label="Categories in {active.name}">
        <div class="crow head" role="row">
          <span role="columnheader">Category</span><span role="columnheader">Items</span><span role="columnheader">Default printer</span>
          <span role="columnheader">Visible</span><span role="columnheader"><span class="sr-only">Delete</span></span>
        </div>
        {#each categories as c (c.id)}
          {@const n = (itemsByCat[c.id] || []).length}
          <div class="crow" role="row">
            <span class="c-name" role="cell">
              <input class="field" value={c.name} aria-label="Category name" onblur={(e) => renameCategory(c, e.currentTarget)} onkeydown={enterBlurs} />
            </span>
            <span class="c-count mono text-text2" role="cell">{n}<span class="sm:hidden font-sans">{' '}item{n === 1 ? '' : 's'}</span></span>
            <span class="c-printer" role="cell">
              <select
                class="field"
                aria-label="Default printer for {c.name}"
                value={c.default_printer_id ?? ''}
                onchange={(e) => saveCategory(c, { default_printer_id: e.currentTarget.value || null }, 'Default printer saved')}
              >
                <option value="">No default printer</option>
                {#each printers as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
              </select>
            </span>
            <span class="c-vis" role="cell">
              <Switch checked={c.available} label="{c.name} visible" onchange={(v) => saveCategory(c, { available: v }, v ? `${c.name} shown on the menu` : `${c.name} hidden from the menu`)} />
            </span>
            <span class="c-del text-right" role="cell">
              <Button size="sm" variant="ghost" icon="trash" aria-label="Delete category {c.name}" onclick={() => deleteCategory(c)} />
            </span>
          </div>
        {:else}
          <p class="m-0 text-muted text-center py-6">No categories yet.</p>
        {/each}
      </div>
      <form class="flex gap-2 px-5 py-4 border-t border-border" onsubmit={createCategory}>
        <label for="new-cat" class="sr-only">New category name</label>
        <input id="new-cat" class="field flex-1" placeholder="New category, e.g. Salads" bind:value={newCat} />
        <Button type="submit" icon="plus">Add category</Button>
      </form>
    </Card>
  {/if}
</div>

<style>
  .crow {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 56px minmax(0, 1fr) 56px 40px;
    gap: 12px;
    align-items: center;
    padding: 8px 20px;
    border-bottom: 1px solid var(--border);
  }
  .crow.head {
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    background: var(--elev);
    padding-top: 10px;
    padding-bottom: 10px;
  }
  /* Phones: name + delete on top, printer · item count · visible below. */
  @media (max-width: 639px) {
    .crow {
      grid-template-columns: minmax(0, 1fr) auto auto;
      grid-template-areas: 'name name del' 'printer count vis';
      gap: 8px 12px;
      padding: 12px 16px;
    }
    .crow.head {
      display: none;
    }
    .c-name {
      grid-area: name;
    }
    .c-count {
      grid-area: count;
    }
    .c-printer {
      grid-area: printer;
      min-width: 0;
    }
    .c-vis {
      grid-area: vis;
    }
    .c-del {
      grid-area: del;
    }
  }
</style>
