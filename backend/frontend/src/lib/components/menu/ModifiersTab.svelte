<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, toCents, centsInput, type ModifierGroup, type ModifierOption, type MenuSize, type BankGroup, type OptionFlags } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Icon from '$lib/ui/Icon.svelte';

  // Reusable modifier groups (attach them to items in the item panel).
  // `sizes` = the store's sizes; each group's bank data (halves, defaults,
  // kitchen names, per-size prices) is read from the bank on select.
  let { sizes = [] }: { sizes?: MenuSize[] } = $props();

  type BankRow = { option_id: string; name: string; is_default: boolean; kitchen_name: string; prices: Record<string, string> };
  let portions = $state(false);
  let bankRows = $state<BankRow[]>([]);
  async function loadBank(gid: string) {
    const bank = await storeApi<{ groups: Record<string, BankGroup> }>('/me/menu/bank');
    const g = bank.groups[gid];
    portions = g?.portions ?? false;
    bankRows = (g?.options ?? []).map((o) => ({
      option_id: o.id,
      name: o.name,
      is_default: o.is_default,
      kitchen_name: o.kitchen_name,
      prices: Object.fromEntries((o.prices || []).map((p) => [p.size_id, centsInput(p.price_cents)]))
    }));
  }
  async function saveBank() {
    if (!selected) return;
    const options = [];
    for (const r of bankRows) {
      const prices = [];
      for (const sz of sizes) {
        const raw = (r.prices[sz.id] ?? '').trim();
        if (!raw) continue;
        const c = toCents(raw);
        if (c === null || c < 0) return toast.error(`${r.name}, ${sz.name}: “${raw}” isn't a valid price. Not saved.`);
        prices.push({ size_id: sz.id, price_cents: c });
      }
      options.push({ option_id: r.option_id, is_default: r.is_default, kitchen_name: r.kitchen_name, prices });
    }
    try {
      await storeApi(`/me/menu/modifier-groups/${selected.id}/bank`, { method: 'PUT', body: JSON.stringify({ portions, options }) });
      toast.success('Saved');
    } catch (err) {
      toast.error(err);
    }
  }
  let groups = $state<ModifierGroup[]>([]);
  let selected = $state<ModifierGroup | null>(null);
  let options = $state<ModifierOption[]>([]);
  let newGroupName = $state('');
  let newOpt = $state({ name: '', price: '0.00', counts: false });
  // A topping group: one of its options already counts for coupons, so a new one does too unless switched off.
  const toppingGroup = $derived(options.some((o) => o.counts_for_coupons));
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
    newOpt = { name: '', price: '0.00', counts: options.some((o) => o.counts_for_coupons) };
    await loadBank(g.id);
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
        body: JSON.stringify({ name: newOpt.name.trim(), price_delta_cents: delta, counts_for_coupons: newOpt.counts })
      });
      const added = newOpt.name.trim();
      options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${selected.id}/options`);
      newOpt = { name: '', price: '0.00', counts: toppingGroup };
      await loadBank(selected.id);
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
  async function saveOption(o: ModifierOption, msg = 'Option saved') {
    if (badDelta[o.id] !== undefined) return toast.error(badPrice(badDelta[o.id]));
    try {
      const body: Record<string, unknown> = { name: o.name, price_delta_cents: o.price_delta_cents, in_stock: o.in_stock };
      if (o.show_on_driver_ticket !== undefined) body.show_on_driver_ticket = o.show_on_driver_ticket;
      await storeApi(`/me/menu/modifier-groups/${o.group_id}/options/${o.id}`, { method: 'PATCH', body: JSON.stringify(body) });
      toast.success(msg);
    } catch (err) {
      toast.error(err);
    }
  }
  async function setCoupons(o: ModifierOption, counts: boolean) {
    try {
      const f = await storeApi<OptionFlags>(`/me/menu/modifier-groups/${o.group_id}/options/${o.id}/coupons`, {
        method: 'PUT',
        body: JSON.stringify({ counts })
      });
      o.counts_for_coupons = f.counts_for_coupons;
      const where = f.family_name ? ` (family “${f.family_name}”)` : '';
      toast.success(f.counts_for_coupons ? `“${o.name}” counts for coupons${where}` : `“${o.name}” no longer counts for coupons${where}`);
    } catch (err) {
      o.counts_for_coupons = !counts;
      toast.error(err);
    }
  }
  async function deleteOption(o: ModifierOption) {
    try {
      await storeApi(`/me/menu/modifier-groups/${o.group_id}/options/${o.id}`, { method: 'DELETE' });
      if (selected) {
        options = await storeApi<ModifierOption[]>(`/me/menu/modifier-groups/${selected.id}/options`);
        await loadBank(selected.id);
      }
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
          <span class="lbl !mb-0.5" id="mg-opts">Options</span>
          <dl class="help !mt-0 mb-2 grid grid-cols-1 sm:grid-cols-[auto_1fr] gap-x-3 gap-y-0.5">
            <dt class="font-semibold">Show on driver ticket</dt>
            <dd class="m-0">Print this choice on the delivery ticket even when it costs $0 (drinks, desserts, sides). Notes stay off the driver's copy.</dd>
            <dt class="font-semibold">Counts for coupons</dt>
            <dd class="m-0">A coupon's free toppings can include this one. Off for premium toppings like steak or shrimp.</dd>
          </dl>
          <ul class="flex flex-col gap-3" aria-labelledby="mg-opts">
            {#each options as o (o.id)}
              <li class="flex flex-col gap-1.5" data-testid="option-row">
                <div class="flex flex-wrap items-center gap-2">
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
                </div>
                <div class="flex flex-wrap items-center gap-x-5 gap-y-1 text-[13px]">
                  <label class="flex items-center gap-2">
                    <Switch
                      checked={o.show_on_driver_ticket ?? false}
                      label="Show {o.name} on driver ticket"
                      onchange={(v) => {
                        o.show_on_driver_ticket = v;
                        saveOption(o, v ? `“${o.name}” prints on the driver ticket` : `“${o.name}” stays off the driver ticket`);
                      }}
                    />Show on driver ticket
                  </label>
                  <label class="flex items-center gap-2">
                    <Switch
                      checked={o.counts_for_coupons ?? false}
                      label="{o.name} counts for coupons"
                      onchange={(v) => {
                        o.counts_for_coupons = v;
                        setCoupons(o, v);
                      }}
                    />Counts for coupons
                  </label>
                </div>
              </li>
            {:else}
              <li class="text-[13px] text-muted">No options yet.</li>
            {/each}
          </ul>
          <form class="flex flex-wrap gap-2 mt-3" onsubmit={addOption}>
            <input class="field flex-1 min-w-[160px]" placeholder="Option name" aria-label="New option name" bind:value={newOpt.name} />
            <input class="field mono text-right !w-24" aria-label="New option price change ($)" bind:value={newOpt.price} />
            <label class="flex items-center gap-2 text-[13px]">
              <Switch bind:checked={newOpt.counts} label="New option counts for coupons" />Counts for coupons
            </label>
            <Button type="submit" icon="plus">Add option</Button>
          </form>
        </div>

        {#if bankRows.length}
          <div class="pt-4 border-t border-border flex flex-col gap-3">
            <div>
              <span class="lbl !mb-0.5" id="mg-bank">Defaults, kitchen names and size prices</span>
              <p class="help !mt-0">A default option comes on the item unless the customer removes it. Leave a size price empty to use the option's price above.</p>
            </div>
            <label class="flex items-center gap-2.5"><Switch bind:checked={portions} label="Options can go on a half" />Options can go on a half (left / right / whole)</label>
            <div class="overflow-x-auto">
              <table class="w-full text-[13px]" aria-labelledby="mg-bank">
                <thead>
                  <tr class="text-left text-muted">
                    <th class="font-semibold py-1 pr-2">Option</th>
                    <th class="font-semibold py-1 pr-2">Default</th>
                    <th class="font-semibold py-1 pr-2">Kitchen name</th>
                    {#each sizes as sz (sz.id)}<th class="font-semibold py-1 pr-2 whitespace-nowrap">{sz.short || sz.name} ($)</th>{/each}
                  </tr>
                </thead>
                <tbody>
                  {#each bankRows as r (r.option_id)}
                    <tr>
                      <td class="py-1 pr-2 whitespace-nowrap">{r.name}</td>
                      <td class="py-1 pr-2"><Switch bind:checked={r.is_default} label="{r.name} is a default" /></td>
                      <td class="py-1 pr-2"><input class="field !min-w-[120px]" bind:value={r.kitchen_name} placeholder={r.name} aria-label="Kitchen name for {r.name}" /></td>
                      {#each sizes as sz (sz.id)}
                        <td class="py-1 pr-2">
                          <input class="field mono text-right !w-20" inputmode="decimal" bind:value={r.prices[sz.id]} aria-label="{r.name} on {sz.name} ($)" />
                        </td>
                      {/each}
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
            <div><Button variant="primary" onclick={saveBank}>Save defaults and prices</Button></div>
          </div>
        {/if}
      </div>
    {/if}
  </section>
</div>
