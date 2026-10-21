<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, toCents, centsInput, type ModifierGroup, type ModifierOption } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Icon from '$lib/ui/Icon.svelte';

  // Reusable modifier groups (attach them to items in the item panel).
  let groups = $state<ModifierGroup[]>([]);
  let selected = $state<ModifierGroup | null>(null);
  let options = $state<ModifierOption[]>([]);
  let newGroupName = $state('');
  let newOpt = $state({ name: '', price: '0.00' });
  // option id → the unparseable price text typed into its row (blocks saving it)
  let badDelta = $state<Record<string, string>>({});
  const badPrice = (raw: string) => `Price “${raw}” isn't a valid amount — use e.g. 1.50, -0.50 or 0. Not saved.`;

  async function load() {
    groups = await storeApi<ModifierGroup[]>('/me/menu/modifier-groups');
  }
  async function selectGroup(g: ModifierGroup) {
    selected = { ...g };
    badDelta = {};
    options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${g.id}/options`);
  }
  onMount(() => load().catch((e) => toast.error(e)));

  async function createGroup(e: SubmitEvent) {
    e.preventDefault();
    if (!newGroupName.trim()) return;
    try {
      const g = await storeApi<ModifierGroup>('/me/menu/modifier-groups', {
        method: 'POST',
        body: JSON.stringify({ name: newGroupName.trim(), min_select: 0, max_select: 0 })
      });
      newGroupName = '';
      await load();
      await selectGroup(g);
      toast.success(`“${g.name}” created — add its options`);
    } catch (err) {
      toast.error(err);
    }
  }
  async function saveGroup() {
    if (!selected) return;
    try {
      await storeApi(`/me/menu/modifier-groups/${selected.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          name: selected.name,
          required: selected.required,
          min_select: selected.min_select,
          max_select: selected.max_select,
          allow_multiple: selected.allow_multiple
        })
      });
      await load();
      toast.success('Group saved');
    } catch (err) {
      toast.error(err);
    }
  }
  async function deleteGroup(g: ModifierGroup) {
    const ok = await confirmDialog({
      title: `Delete “${g.name}”?`,
      body: 'It will be detached from every item that uses it.',
      confirmLabel: 'Delete group',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi(`/me/menu/modifier-groups/${g.id}`, { method: 'DELETE' });
      if (selected?.id === g.id) selected = null;
      await load();
      toast.success('Group deleted');
    } catch (err) {
      toast.error(err);
    }
  }
  async function addOption(e: SubmitEvent) {
    e.preventDefault();
    if (!selected || !newOpt.name.trim()) return;
    const delta = toCents(newOpt.price);
    if (delta === null) return toast.error(badPrice(newOpt.price));
    try {
      await storeApi(`/me/menu/modifier-groups/${selected.id}/options`, {
        method: 'POST',
        body: JSON.stringify({ name: newOpt.name.trim(), price_delta_cents: delta })
      });
      const added = newOpt.name.trim();
      newOpt = { name: '', price: '0.00' };
      options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${selected.id}/options`);
      toast.success(`“${added}” added`);
    } catch (err) {
      toast.error(err);
    }
  }
  function setDelta(o: ModifierOption, raw: string) {
    const c = toCents(raw);
    if (c === null) {
      badDelta[o.id] = raw;
      toast.error(badPrice(raw));
      return;
    }
    delete badDelta[o.id];
    o.price_delta_cents = c;
  }
  async function saveOption(o: ModifierOption) {
    if (badDelta[o.id] !== undefined) return toast.error(badPrice(badDelta[o.id]));
    try {
      await storeApi(`/me/menu/modifier-groups/${o.group_id}/options/${o.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ name: o.name, price_delta_cents: o.price_delta_cents, in_stock: o.in_stock })
      });
      toast.success('Option saved');
    } catch (err) {
      toast.error(err);
    }
  }
  async function deleteOption(o: ModifierOption) {
    try {
      await storeApi(`/me/menu/modifier-groups/${o.group_id}/options/${o.id}`, { method: 'DELETE' });
      if (selected) options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${selected.id}/options`);
      toast.success(`“${o.name}” removed`);
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<div class="grid grid-cols-1 lg:grid-cols-3 gap-4 items-start">
  <section class="card p-3" aria-labelledby="mg-title">
    <h2 id="mg-title" class="eyebrow px-2 pt-1 pb-2 m-0">Reusable groups</h2>
    <ul class="flex flex-col gap-0.5">
      {#each groups as g (g.id)}
        <li class="flex items-center gap-1">
          <button type="button" class="nav flex-1 min-w-0" aria-current={selected?.id === g.id ? 'page' : undefined} onclick={() => selectGroup(g)}>
            <Icon name="layers" size={16} /><span class="truncate">{g.name}</span>
          </button>
          <Button size="sm" variant="ghost" icon="trash" aria-label="Delete {g.name}" onclick={() => deleteGroup(g)} />
        </li>
      {:else}
        <li class="px-2 py-1 text-[13px] text-muted">No groups yet.</li>
      {/each}
    </ul>
    <form class="flex gap-2 mt-3 pt-3 border-t border-border" onsubmit={createGroup}>
      <label for="mg-new" class="sr-only">New group name</label>
      <input id="mg-new" class="field flex-1" placeholder="New group, e.g. Toppings" bind:value={newGroupName} />
      <Button type="submit" variant="primary" icon="plus" aria-label="Create group" />
    </form>
  </section>

  <section class="lg:col-span-2">
    {#if !selected}
      <div class="card"><EmptyState icon="layers" title="Pick a modifier group" body="Or create one — e.g. Toppings, Crust, Sauce — then attach it to items in the item panel." /></div>
    {:else}
      <div class="card p-5 flex flex-col gap-4">
        <div>
          <label class="lbl" for="mg-name">Name</label>
          <input id="mg-name" class="field" bind:value={selected.name} />
        </div>
        <div class="flex flex-wrap gap-5 items-end">
          <label class="flex items-center gap-2.5"><Switch bind:checked={selected.required} label="Required" />Required</label>
          <label class="flex items-center gap-2.5"><Switch bind:checked={selected.allow_multiple} label="Allow multiple of the same option" />Allow multiple of same</label>
          <div><label class="lbl" for="mg-min">Min select</label><input id="mg-min" type="number" min="0" class="field mono !w-24" bind:value={selected.min_select} /></div>
          <div><label class="lbl" for="mg-max">Max select (0 = any)</label><input id="mg-max" type="number" min="0" class="field mono !w-28" bind:value={selected.max_select} /></div>
          <Button variant="primary" onclick={saveGroup}>Save rules</Button>
        </div>

        <div class="pt-4 border-t border-border">
          <span class="lbl" id="mg-opts">Options</span>
          <ul class="flex flex-col gap-2" aria-labelledby="mg-opts">
            {#each options as o (o.id)}
              <li class="flex flex-wrap items-center gap-2">
                <input class="field flex-1 min-w-[160px]" bind:value={o.name} aria-label="Option name" />
                <input
                  class="field mono text-right !w-24"
                  value={centsInput(o.price_delta_cents)}
                  aria-invalid={badDelta[o.id] !== undefined || undefined}
                  aria-label="Price change for {o.name} ($)"
                  onchange={(e) => setDelta(o, e.currentTarget.value)}
                />
                <Switch
                  checked={o.in_stock}
                  label="{o.name} in stock"
                  onchange={(v) => {
                    o.in_stock = v;
                    saveOption(o);
                  }}
                />
                <Button size="sm" onclick={() => saveOption(o)}>Save</Button>
                <Button size="sm" variant="ghost" icon="trash" aria-label="Delete {o.name}" onclick={() => deleteOption(o)} />
              </li>
            {:else}
              <li class="text-[13px] text-muted">No options yet.</li>
            {/each}
          </ul>
          <form class="flex flex-wrap gap-2 mt-3" onsubmit={addOption}>
            <input class="field flex-1 min-w-[160px]" placeholder="Option name" aria-label="New option name" bind:value={newOpt.name} />
            <input class="field mono text-right !w-24" aria-label="New option price change ($)" bind:value={newOpt.price} />
            <Button type="submit" icon="plus">Add option</Button>
          </form>
        </div>
      </div>
    {/if}
  </section>
</div>
