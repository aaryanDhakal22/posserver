<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, dollars, FAMILY_CLASSES, type FamiliesListing, type FamilyKind, type FamilyClass, type FamilyRow } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';

  // Families (the owner's Centurion pass, 2026-11-12): every item and every
  // modifier option belongs to a family (Can Soda, Hot Subs, Pizza Topping…).
  // Coupons take items by family; a mod family's class says whether a coupon
  // may make it free (regular) or never (premium, unpriced).
  let { onchange }: { onchange?: () => void } = $props();

  let data = $state<FamiliesListing | null>(null);
  async function load() {
    data = await storeApi<FamiliesListing>('/me/menu/families');
  }
  onMount(() => load().catch((e) => toast.error(e)));

  let kind = $state<FamilyKind>('item');
  const families = $derived((data?.families ?? []).filter((f) => f.kind === kind));
  const famName = (id: string | null) => data?.families.find((f) => f.id === id)?.name ?? '';
  const classLabel = (c: FamilyClass) => FAMILY_CLASSES.find((x) => x.value === c)?.label ?? '';
  const classTone = (c: FamilyClass) => (c === 'regular' ? 'ok' : c === 'premium' ? 'warn' : 'mute') as 'ok' | 'warn' | 'mute';

  // --- families: add, rename, class, merge, delete ---------------------------------------
  let fresh = $state({ name: '', class: 'regular' as FamilyClass });
  async function add(e: SubmitEvent) {
    e.preventDefault();
    if (!fresh.name.trim()) return;
    try {
      await storeApi('/me/menu/families', {
        method: 'POST',
        body: JSON.stringify({ kind, name: fresh.name, class: kind === 'mod' ? fresh.class : '' })
      });
      toast.success(`“${fresh.name.trim()}” added`);
      fresh.name = '';
      await reload();
    } catch (err) {
      toast.error(err);
    }
  }
  async function reload() {
    await load();
    onchange?.();
  }
  let renaming = $state<{ id: string; name: string } | null>(null);
  async function rename(e: SubmitEvent) {
    e.preventDefault();
    if (!renaming) return;
    try {
      await storeApi(`/me/menu/families/${renaming.id}`, { method: 'PATCH', body: JSON.stringify({ name: renaming.name }) });
      toast.success('Family renamed');
      renaming = null;
      await reload();
    } catch (err) {
      toast.error(err);
    }
  }
  async function setClass(f: FamilyRow, c: string) {
    try {
      await storeApi(`/me/menu/families/${f.id}`, { method: 'PATCH', body: JSON.stringify({ class: c }) });
      toast.success(`${f.name}: ${classLabel(c as FamilyClass).toLowerCase()}`);
      await reload();
    } catch (err) {
      toast.error(err);
    }
  }
  let merging = $state<{ id: string; into: string } | null>(null);
  async function merge() {
    if (!merging || !merging.into) return;
    const from = famName(merging.id), into = famName(merging.into);
    try {
      const r = await storeApi<{ moved_items: number; moved_options: number; coupons_changed: number }>(`/me/menu/families/${merging.id}/merge`, {
        method: 'POST',
        body: JSON.stringify({ into: merging.into })
      });
      const moved = r.moved_items + r.moved_options;
      toast.success(`${from} merged into ${into}: ${moved} moved${r.coupons_changed ? `, ${r.coupons_changed} coupon${r.coupons_changed === 1 ? '' : 's'} updated` : ''}`);
      merging = null;
      await reload();
    } catch (err) {
      toast.error(err);
    }
  }
  async function del(f: FamilyRow) {
    const n = f.items + f.options;
    const ok = await confirmDialog({
      title: `Delete “${f.name}”?`,
      body: n ? `Its ${n} ${kind === 'item' ? 'item' : 'option'}${n === 1 ? '' : 's'} go back to “no family yet”.` : 'Nothing is in it.',
      confirmLabel: 'Delete family',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi(`/me/menu/families/${f.id}`, { method: 'DELETE' });
      toast.success('Family deleted');
      await reload();
    } catch (err) {
      toast.error(err);
    }
  }

  // --- members: filter, select, assign in bulk ------------------------------------------
  let q = $state('');
  let filter = $state(''); // '' = all, 'none' = no family yet, else a family id
  let picked = $state<string[]>([]);
  let target = $state('');
  $effect(() => {
    void kind;
    picked = [];
    filter = '';
    target = '';
  });
  type Member = { id: string; name: string; parent: string; family_id: string | null; price?: number };
  const members = $derived.by((): Member[] => {
    if (!data) return [];
    const rows: Member[] =
      kind === 'item'
        ? data.items.map((i) => ({ id: i.id, name: i.name, parent: i.category, family_id: i.family_id }))
        : data.options.map((o) => ({ id: o.id, name: o.name, parent: o.group, family_id: o.family_id, price: o.price_cents }));
    const needle = q.trim().toLowerCase();
    return rows.filter(
      (m) =>
        (filter === '' || (filter === 'none' ? !m.family_id : m.family_id === filter)) &&
        (!needle || m.name.toLowerCase().includes(needle) || m.parent.toLowerCase().includes(needle))
    );
  });
  const unassigned = $derived(
    data ? (kind === 'item' ? data.items : data.options).filter((m) => !m.family_id).length : 0
  );
  const allPicked = $derived(members.length > 0 && members.every((m) => picked.includes(m.id)));
  function toggleAll() {
    const ids = members.map((m) => m.id);
    picked = allPicked ? picked.filter((id) => !ids.includes(id)) : [...new Set([...picked, ...ids])];
  }
  async function assign(family: string | null) {
    if (!picked.length) return;
    try {
      const body = { family_id: family, [kind === 'item' ? 'items' : 'options']: picked };
      const r = await storeApi<{ items: number; options: number }>('/me/menu/families/assign', { method: 'POST', body: JSON.stringify(body) });
      const n = r.items + r.options;
      toast.success(family ? `${n} moved to ${famName(family)}` : `${n} now without a family`);
      picked = [];
      await reload();
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap items-center gap-3">
    <SegmentedControl
      options={[
        { value: 'item', label: 'Item families', count: data?.families.filter((f) => f.kind === 'item').length },
        { value: 'mod', label: 'Mod families', count: data?.families.filter((f) => f.kind === 'mod').length }
      ]}
      bind:value={kind}
      label="Which families"
    />
    <p class="m-0 flex-1 text-text2 text-[13px] min-w-[220px]">
      {kind === 'item'
        ? 'Coupons take items by family (Can Soda, Hot Subs). A family is per item: a coupon part can also name sizes.'
        : 'A mod family’s class: regular toppings can be free in a coupon, premium and unpriced never.'}
    </p>
  </div>

  <div class="grid gap-4 lg:grid-cols-[minmax(280px,380px)_1fr] items-start">
    <section class="card p-4 flex flex-col gap-3" aria-labelledby="fam-title">
      <h2 id="fam-title" class="text-base font-semibold m-0">{kind === 'item' ? 'Item families' : 'Mod families'}</h2>
      {#if !families.length}
        <EmptyState icon="layers" title="No families yet" body={kind === 'item' ? 'Add e.g. Can Soda, Bottle Soda, Hot Subs, Fries.' : 'Add e.g. Pizza Topping (regular), Premium Topping (premium).'} />
      {:else}
        <ul class="flex flex-col gap-1.5" aria-label="Families">
          {#each families as f (f.id)}
            <li class="subcard p-2.5 flex flex-col gap-2" data-testid="family-row">
              {#if renaming?.id === f.id}
                <form class="flex gap-2" onsubmit={rename}>
                  <input class="field flex-1" bind:value={renaming.name} maxlength="60" aria-label="New name for {f.name}" />
                  <Button size="sm" type="submit" variant="primary">Save</Button>
                  <Button size="sm" variant="ghost" onclick={() => (renaming = null)}>Cancel</Button>
                </form>
              {:else}
                <div class="flex flex-wrap items-center gap-2">
                  <button type="button" class="font-semibold text-left hover:text-accent flex-1 min-w-0 truncate" onclick={() => (filter = f.id)} title="Show its {kind === 'item' ? 'items' : 'options'}">{f.name}</button>
                  {#if kind === 'mod'}<Badge tone={classTone(f.class)}>{classLabel(f.class)}</Badge>{/if}
                  <span class="text-xs text-muted mono">{kind === 'item' ? f.items : f.options}</span>
                  <Button size="xs" variant="ghost" icon="pencil" aria-label="Rename {f.name}" onclick={() => (renaming = { id: f.id, name: f.name })} />
                  <Button size="xs" variant="ghost" icon="layers" aria-label="Merge {f.name}" onclick={() => (merging = { id: f.id, into: '' })} />
                  <Button size="xs" variant="ghost" icon="trash" aria-label="Delete {f.name}" onclick={() => del(f)} />
                </div>
                {#if kind === 'mod'}
                  <select class="field !py-1 text-[13px]" value={f.class} aria-label="Class of {f.name}" onchange={(e) => setClass(f, (e.currentTarget as HTMLSelectElement).value)}>
                    {#each FAMILY_CLASSES as c (c.value)}<option value={c.value} title={c.help}>{c.label}</option>{/each}
                  </select>
                {/if}
                {#if merging?.id === f.id}
                  <div class="flex flex-wrap gap-2 items-center">
                    <select class="field flex-1 min-w-[140px]" bind:value={merging.into} aria-label="Merge {f.name} into">
                      <option value="">Merge into…</option>
                      {#each families.filter((x) => x.id !== f.id) as x (x.id)}<option value={x.id}>{x.name}</option>{/each}
                    </select>
                    <Button size="sm" variant="primary" disabled={!merging.into} onclick={merge}>Merge</Button>
                    <Button size="sm" variant="ghost" onclick={() => (merging = null)}>Cancel</Button>
                  </div>
                {/if}
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
      <form class="flex flex-wrap gap-2 pt-3 border-t border-border" onsubmit={add}>
        <input class="field flex-1 min-w-[160px]" placeholder={kind === 'item' ? 'New family, e.g. Can Soda' : 'New family, e.g. Pizza Topping'} aria-label="New family name" maxlength="60" bind:value={fresh.name} />
        {#if kind === 'mod'}
          <select class="field !w-auto" bind:value={fresh.class} aria-label="New family class">
            {#each FAMILY_CLASSES as c (c.value)}<option value={c.value}>{c.label}</option>{/each}
          </select>
        {/if}
        <Button type="submit" variant="primary" icon="plus">Add family</Button>
      </form>
    </section>

    <section class="card p-4 flex flex-col gap-3" aria-labelledby="mem-title">
      <div class="flex flex-wrap items-center gap-2">
        <h2 id="mem-title" class="text-base font-semibold m-0 flex-1">{kind === 'item' ? 'Items' : 'Modifier options'}</h2>
        {#if unassigned}<Badge tone="warn">{unassigned} with no family yet</Badge>{/if}
      </div>
      <div class="flex flex-wrap gap-2">
        <SearchInput id="fam-q" label="Search" placeholder="Search by name, {kind === 'item' ? 'category' : 'group'}" bind:value={q} class="flex-1 min-w-[180px]" />
        <select class="field !w-auto" bind:value={filter} aria-label="Show">
          <option value="">All</option>
          <option value="none">No family yet</option>
          {#each families as f (f.id)}<option value={f.id}>{f.name}</option>{/each}
        </select>
      </div>
      <div class="flex flex-wrap items-center gap-2 subcard p-2.5" data-testid="bulk-bar">
        <label class="flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={allPicked} onchange={toggleAll} aria-label="Select all shown" />
          {picked.length} selected
        </label>
        <select class="field !w-auto flex-1 min-w-[140px]" bind:value={target} aria-label="Family for the selected">
          <option value="">Set family…</option>
          {#each families as f (f.id)}<option value={f.id}>{f.name}</option>{/each}
        </select>
        <Button size="sm" variant="primary" disabled={!picked.length || !target} onclick={() => assign(target)}>Apply</Button>
        <Button size="sm" variant="ghost" disabled={!picked.length} onclick={() => assign(null)}>Clear family</Button>
      </div>
      {#if !members.length}
        <p class="text-muted text-[13px] m-0">{data ? 'Nothing matches.' : 'Loading…'}</p>
      {:else}
        <ul class="flex flex-col divide-y divide-border max-h-[560px] overflow-y-auto" aria-label="Members">
          {#each members as m (m.id)}
            <li class="flex items-center gap-2.5 py-1.5 text-[13px]" data-testid="member-row">
              <input
                type="checkbox"
                checked={picked.includes(m.id)}
                aria-label="Select {m.name} ({m.parent})"
                onchange={() => (picked = picked.includes(m.id) ? picked.filter((x) => x !== m.id) : [...picked, m.id])}
              />
              <span class="flex-1 min-w-0 truncate"><span class="font-medium">{m.name}</span> <span class="text-muted">· {m.parent}</span></span>
              {#if m.price !== undefined}<span class="mono text-muted">{dollars(m.price)}</span>{/if}
              {#if m.family_id}<Badge tone="brand">{famName(m.family_id)}</Badge>{:else}<Badge tone="warn">No family</Badge>{/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  </div>
</div>
