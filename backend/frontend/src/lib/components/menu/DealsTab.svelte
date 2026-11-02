<script lang="ts">
  import { onMount } from 'svelte';
  import {
    storeApi,
    toCents,
    centsInput,
    dollars,
    errText,
    DEAL_KINDS,
    CHANNEL_KIND_LABELS,
    type Bank,
    type StoreDeal,
    type DealKind,
    type DealEligible
  } from '$lib/api';
  import { price, type Context, type Quote } from '$lib/pricing/engine';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import EligiblePicker from './EligiblePicker.svelte';

  // The store's deals (main 3.0): combos, mix & match, every Nth, promo
  // codes and free items, each switched on for some channels. The preview
  // prices a test cart in the browser with the same engine main uses.
  let { bank }: { bank: Bank | null } = $props();

  let deals = $state<StoreDeal[]>([]);
  async function load() {
    deals = await storeApi<StoreDeal[]>('/me/deals');
  }
  onMount(() => load().catch((e) => toast.error(e)));

  const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
  const cats = $derived((bank?.menus ?? []).flatMap((m) => m.categories));
  const items = $derived(cats.flatMap((c) => c.items));
  function eligLabel(e: DealEligible | null | undefined): string {
    if (!e) return '';
    const names = [
      ...(e.categories ?? []).map((id) => cats.find((c) => c.id === id)?.name ?? '(gone)'),
      ...(e.items ?? []).map((id) => items.find((i) => i.id === id)?.name ?? '(gone)')
    ];
    const sizes = (e.sizes ?? []).map((id) => bank?.sizes.find((s) => s.id === id)?.name ?? '(gone)');
    return names.join(', ') + (sizes.length ? ` (${sizes.join(', ')})` : '');
  }
  function summary(d: StoreDeal): string {
    switch (d.kind) {
      case 'bundle':
        return `${(d.slots ?? []).map((s) => s.label).join(' + ')} for ${dollars(d.price_cents ?? 0)}`;
      case 'mix':
        return `Any ${d.qty} of ${eligLabel(d.eligible)} for ${dollars(d.each_cents ?? 0)} each`;
      case 'nth':
        return `Every ${d.n === 2 ? '2nd' : d.n === 3 ? '3rd' : `${d.n}th`} ${eligLabel(d.eligible)} ${d.pct}% off`;
      case 'code':
        return `${d.code}: ${d.pct ? `${d.pct}% off` : `${dollars(d.amount_cents ?? 0)} off`}${d.min_subtotal_cents ? ` over ${dollars(d.min_subtotal_cents)}` : ''}${d.first_order_only ? ', first order only' : ''}`;
      case 'free_item':
        return `Free ${eligLabel(d.eligible)} once the order reaches ${dollars(d.min_subtotal_cents ?? 0)}`;
    }
    return '';
  }
  function when(d: StoreDeal): string {
    const s = d.schedule;
    const parts: string[] = [];
    if (s?.days?.length) parts.push(s.days.map((x) => x[0].toUpperCase() + x.slice(1)).join(' '));
    if (s?.start || s?.end) parts.push(`${s?.start || 'open'}–${s?.end || 'close'}`);
    if (d.fulfil === 'pickup' || d.fulfil === 'delivery') parts.push(`${d.fulfil} only`);
    return parts.join(' · ');
  }

  async function setActive(d: StoreDeal, v: boolean) {
    try {
      await storeApi(`/me/deals/${d.id}`, { method: 'PUT', body: JSON.stringify({ ...d, active: v }) });
      d.active = v;
      toast.success(v ? `${d.name} is on` : `${d.name} is off`);
    } catch (e) {
      toast.error(e);
    }
  }
  async function del(d: StoreDeal) {
    if (!(await confirmDialog({ title: `Delete “${d.name}”?`, body: 'Carts holding it show it as ended.', confirmLabel: 'Delete deal', danger: true }))) return;
    try {
      await storeApi(`/me/deals/${d.id}`, { method: 'DELETE' });
      await load();
      toast.success('Deal deleted');
    } catch (e) {
      toast.error(e);
    }
  }

  // --- the editor --------------------------------------------------------------------
  type Draft = {
    id?: string; kind: DealKind; name: string; description: string; active: boolean; channels: string[]; fulfil: string;
    days: string[]; start: string; end: string;
    price: string; slots: { label: string; eligible: DealEligible; included: number }[];
    eligible: DealEligible; qty: number; each: string; n: number; pct: number;
    code: string; codeMode: 'pct' | 'amount'; amount: string; min: string; first: boolean;
  };
  const noElig = (): DealEligible => ({ items: [], categories: [], sizes: [] });
  let draft = $state<Draft | null>(null);
  let open = $state(false);
  let err = $state('');
  let saving = $state(false);

  function blank(kind: DealKind): Draft {
    return {
      kind, name: '', description: '', active: true, channels: ['web', 'operator', 'counter'], fulfil: '', days: [], start: '', end: '',
      price: '', slots: [{ label: 'Pizza', eligible: noElig(), included: 0 }], eligible: noElig(), qty: 2, each: '', n: 2, pct: 50,
      code: '', codeMode: 'pct', amount: '', min: '', first: false
    };
  }
  function edit(d?: StoreDeal) {
    if (!d) {
      draft = blank('mix');
    } else {
      draft = {
        ...blank(d.kind), id: d.id, name: d.name, description: d.description, active: d.active, channels: [...d.channel_kinds],
        fulfil: d.fulfil === 'both' ? '' : d.fulfil, days: [...(d.schedule?.days ?? [])], start: d.schedule?.start ?? '', end: d.schedule?.end ?? '',
        price: d.price_cents !== undefined ? centsInput(d.price_cents) : '',
        slots: (d.slots ?? []).map((s) => ({ label: s.label, eligible: { items: [...(s.eligible.items ?? [])], categories: [...(s.eligible.categories ?? [])], sizes: [...(s.eligible.sizes ?? [])] }, included: s.included_options })),
        eligible: d.eligible ? { items: [...(d.eligible.items ?? [])], categories: [...(d.eligible.categories ?? [])], sizes: [...(d.eligible.sizes ?? [])] } : noElig(),
        qty: d.qty ?? 2, each: d.each_cents !== undefined ? centsInput(d.each_cents) : '', n: d.n ?? 2, pct: d.pct ?? 50,
        code: d.code ?? '', codeMode: d.amount_cents ? 'amount' : 'pct', amount: d.amount_cents ? centsInput(d.amount_cents) : '',
        min: d.min_subtotal_cents ? centsInput(d.min_subtotal_cents) : '', first: !!d.first_order_only
      };
      if (!draft.slots.length) draft.slots = blank('bundle').slots;
    }
    err = '';
    open = true;
    preview.lines = [];
  }

  const cents = (s: string) => (s.trim() ? toCents(s) : 0);
  /** The draft as the API takes it (null + err when a price doesn't parse). */
  function body(d: Draft): StoreDeal | null {
    const money = [d.price, d.each, d.amount, d.min].map(cents);
    if (money.some((x) => x === null || x < 0)) {
      err = "A price isn't a valid amount — use e.g. 9.99.";
      return null;
    }
    const [priceC, eachC, amountC, minC] = money as number[];
    const base = {
      id: d.id ?? '', kind: d.kind, name: d.name, description: d.description, active: d.active, channel_kinds: d.channels, fulfil: d.fulfil,
      schedule: d.days.length || d.start || d.end ? { days: d.days, start: d.start, end: d.end } : null
    };
    switch (d.kind) {
      case 'bundle':
        return { ...base, price_cents: priceC, slots: d.slots.map((s) => ({ label: s.label, eligible: s.eligible, included_options: Number(s.included) || 0 })) };
      case 'mix':
        return { ...base, eligible: d.eligible, qty: Number(d.qty), each_cents: eachC };
      case 'nth':
        return { ...base, eligible: d.eligible, n: Number(d.n), pct: Number(d.pct) };
      case 'code':
        return { ...base, code: d.code, ...(d.codeMode === 'pct' ? { pct: Number(d.pct) } : { amount_cents: amountC }), min_subtotal_cents: minC, first_order_only: d.first };
      case 'free_item':
        return { ...base, eligible: d.eligible, min_subtotal_cents: minC };
    }
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!draft || saving) return;
    err = '';
    const b = body(draft);
    if (!b) return;
    saving = true;
    try {
      if (draft.id) await storeApi(`/me/deals/${draft.id}`, { method: 'PUT', body: JSON.stringify(b) });
      else await storeApi('/me/deals', { method: 'POST', body: JSON.stringify(b) });
      open = false;
      await load();
      toast.success(`${b.name} saved`);
    } catch (x) {
      err = errText(x);
    } finally {
      saving = false;
    }
  }

  // --- the preview: a test cart priced in the browser ------------------------------
  let preview = $state<{ ctx: Context | null; kind: string; lines: { item_id: string; size_id: string; qty: number }[]; add: string }>({
    ctx: null, kind: '', lines: [], add: ''
  });
  async function loadContext(kind: string) {
    if (preview.kind === kind && preview.ctx) return;
    preview.ctx = await storeApi<Context>(`/me/pricing/context?kind=${kind}`).catch(() => null);
    preview.kind = kind;
  }
  $effect(() => {
    if (open && draft) loadContext(draft.channels[0] ?? 'web');
  });
  const menuItems = $derived((preview.ctx?.menu.categories ?? []).flatMap((c) => c.items).filter((i) => !i.choices?.length));
  function addLine() {
    const it = menuItems.find((i) => i.id === preview.add);
    if (!it) return;
    preview.lines = [...preview.lines, { item_id: it.id, size_id: it.sizes?.[0]?.size_id ?? '', qty: 1 }];
    preview.add = '';
  }
  // Required groups get their first option so the test cart prices cleanly.
  function autoOptions(itemId: string) {
    const it = menuItems.find((i) => i.id === itemId);
    const groups = preview.ctx?.menu.modifier_groups ?? [];
    return (it?.modifier_group_ids ?? [])
      .map((gid) => groups.find((g) => g.id === gid))
      .filter((g) => g && (g.required || g.min_select > 0) && g.options.length)
      .map((g) => ({ group_id: g!.id, option_id: g!.options[0].id, portion: 'whole' }));
  }
  const quote = $derived.by((): Quote | null => {
    if (!preview.ctx || !draft || !preview.lines.length) return null;
    const b = body($state.snapshot(draft) as Draft);
    if (!b) return null;
    const others = (preview.ctx.deals ?? []).filter((x) => x.id !== b.id);
    const ctx = { ...preview.ctx, deals: [...others, { ...b, id: b.id || 'draft', active: true }] };
    const lines = preview.lines.map((l) => ({ ...l, options: autoOptions(l.item_id) }));
    return price(ctx as Context, { type: 'pickup', payment: 'card', lines, code: b.kind === 'code' ? b.code : '' }, new Date());
  });
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap items-center gap-2">
    <p class="m-0 flex-1 text-text2 text-[13px] min-w-[220px]">Deals run on the channels you pick (website, call center, counter). Brygid orders keep Brygid's prices.</p>
    <Button variant="primary" icon="plus" onclick={() => edit()}>Add deal</Button>
  </div>
  {#if !deals.length}
    <div class="card"><EmptyState icon="receipt" title="No deals yet" body="Add a combo, a mix & match, a 2nd-one-half-off, a promo code or a free item." /></div>
  {:else}
    <ul class="flex flex-col gap-2">
      {#each deals as d (d.id)}
        <li class="card p-4 flex flex-wrap items-center gap-3">
          <Switch checked={d.active} label="{d.name} on" onchange={(v) => setActive(d, v)} />
          <div class="flex-1 min-w-[220px]">
            <div class="flex flex-wrap items-center gap-2">
              <button type="button" class="font-semibold hover:text-accent text-left" onclick={() => edit(d)}>{d.name}</button>
              <Badge tone="mute">{DEAL_KINDS.find((k) => k.kind === d.kind)?.label}</Badge>
              {#each d.channel_kinds as k (k)}<Badge tone="brand">{CHANNEL_KIND_LABELS[k] ?? k}</Badge>{/each}
            </div>
            <div class="text-[13px] text-text2 mt-0.5">{summary(d)}</div>
            {#if when(d)}<div class="text-xs text-muted mt-0.5">{when(d)}</div>{/if}
          </div>
          <Button size="sm" icon="pencil" onclick={() => edit(d)}>Edit</Button>
          <Button size="sm" variant="ghost" icon="trash" aria-label="Delete {d.name}" onclick={() => del(d)} />
        </li>
      {/each}
    </ul>
  {/if}
</div>

<Dialog bind:open title={draft?.id ? 'Edit deal' : 'Add deal'} width={760}>
  {#if draft}
    <form id="deal-form" class="flex flex-col gap-4" onsubmit={save}>
      {#if !draft.id}
        <SegmentedControl options={DEAL_KINDS.map((k) => ({ value: k.kind, label: k.label }))} bind:value={draft.kind} label="Kind of deal" />
      {/if}
      <p class="help !mt-0">{DEAL_KINDS.find((k) => k.kind === draft!.kind)?.help}</p>
      <div class="grid gap-3 sm:grid-cols-2">
        <div>
          <label class="lbl" for="dl-name">Name</label>
          <input id="dl-name" class="field" bind:value={draft.name} placeholder="2 pizzas $10 each" maxlength="80" />
        </div>
        <div>
          <label class="lbl" for="dl-desc">Description (optional)</label>
          <input id="dl-desc" class="field" bind:value={draft.description} />
        </div>
      </div>

      {#if draft.kind === 'bundle'}
        <div>
          <label class="lbl" for="dl-price">Combo price ($)</label>
          <input id="dl-price" class="field mono text-right !w-32" inputmode="decimal" bind:value={draft.price} />
        </div>
        {#each draft.slots as slot, i (i)}
          <div class="subcard p-3.5 flex flex-col gap-3">
            <div class="flex flex-wrap items-end gap-2">
              <div class="flex-1 min-w-[160px]">
                <label class="lbl" for="dl-slot-{i}">Step {i + 1}</label>
                <input id="dl-slot-{i}" class="field" bind:value={slot.label} placeholder="Pizza" />
              </div>
              <div>
                <label class="lbl" for="dl-inc-{i}">Toppings included</label>
                <input id="dl-inc-{i}" type="number" min="0" class="field mono !w-24" bind:value={slot.included} />
              </div>
              <Button size="sm" variant="ghost" icon="trash" aria-label="Remove step {i + 1}" disabled={draft.slots.length < 2} onclick={() => draft!.slots.splice(i, 1)} />
            </div>
            <EligiblePicker {bank} bind:value={slot.eligible} idPrefix="dl-s{i}" />
          </div>
        {/each}
        <div><Button size="sm" icon="plus" onclick={() => draft!.slots.push({ label: '', eligible: noElig(), included: 0 })}>Add a step</Button></div>
      {:else if draft.kind === 'code'}
        <div class="flex flex-wrap gap-3 items-end">
          <div>
            <label class="lbl" for="dl-code">Code</label>
            <input id="dl-code" class="field mono uppercase !w-40" bind:value={draft.code} placeholder="SAVE10" maxlength="20" />
          </div>
          <SegmentedControl options={[{ value: 'pct', label: '% off' }, { value: 'amount', label: '$ off' }]} bind:value={draft.codeMode} label="Kind of discount" />
          {#if draft.codeMode === 'pct'}
            <div><label class="lbl" for="dl-pct">% off</label><input id="dl-pct" type="number" min="1" max="100" class="field mono !w-24" bind:value={draft.pct} /></div>
          {:else}
            <div><label class="lbl" for="dl-amt">$ off</label><input id="dl-amt" class="field mono text-right !w-28" inputmode="decimal" bind:value={draft.amount} /></div>
          {/if}
          <div><label class="lbl" for="dl-min">Minimum order ($)</label><input id="dl-min" class="field mono text-right !w-28" inputmode="decimal" bind:value={draft.min} /></div>
        </div>
        <label class="flex items-center gap-2.5"><Switch bind:checked={draft.first} label="First order only" />First order only</label>
      {:else}
        <EligiblePicker {bank} bind:value={draft.eligible} idPrefix="dl-el" />
        <div class="flex flex-wrap gap-3">
          {#if draft.kind === 'mix'}
            <div><label class="lbl" for="dl-qty">How many</label><input id="dl-qty" type="number" min="2" class="field mono !w-24" bind:value={draft.qty} /></div>
            <div><label class="lbl" for="dl-each">Price each ($)</label><input id="dl-each" class="field mono text-right !w-28" inputmode="decimal" bind:value={draft.each} /></div>
          {:else if draft.kind === 'nth'}
            <div><label class="lbl" for="dl-n">Every Nth</label><input id="dl-n" type="number" min="2" class="field mono !w-24" bind:value={draft.n} /></div>
            <div><label class="lbl" for="dl-pct2">% off</label><input id="dl-pct2" type="number" min="1" max="100" class="field mono !w-24" bind:value={draft.pct} /></div>
          {:else if draft.kind === 'free_item'}
            <div><label class="lbl" for="dl-min2">Once the order reaches ($)</label><input id="dl-min2" class="field mono text-right !w-28" inputmode="decimal" bind:value={draft.min} /></div>
          {/if}
        </div>
      {/if}

      <div class="grid gap-3 sm:grid-cols-2 pt-3 border-t border-border">
        <div>
          <span class="lbl" id="dl-ch">Runs on</span>
          <div class="flex flex-wrap gap-1.5" role="group" aria-labelledby="dl-ch">
            {#each ['web', 'operator', 'counter', 'vendor'] as k (k)}
              <button
                type="button"
                class="fchip"
                aria-pressed={draft.channels.includes(k)}
                onclick={() => (draft!.channels = draft!.channels.includes(k) ? draft!.channels.filter((x) => x !== k) : [...draft!.channels, k])}
                >{CHANNEL_KIND_LABELS[k]}</button
              >
            {/each}
          </div>
        </div>
        <div>
          <label class="lbl" for="dl-ful">For</label>
          <select id="dl-ful" class="field" bind:value={draft.fulfil}>
            <option value="">Pickup and delivery</option>
            <option value="pickup">Pickup only</option>
            <option value="delivery">Delivery only</option>
          </select>
        </div>
        <div>
          <span class="lbl" id="dl-days">Days (none = every day)</span>
          <div class="flex flex-wrap gap-1.5" role="group" aria-labelledby="dl-days">
            {#each DAYS as day (day)}
              <button type="button" class="fchip !px-2.5" aria-pressed={draft.days.includes(day)} onclick={() => (draft!.days = draft!.days.includes(day) ? draft!.days.filter((x) => x !== day) : [...draft!.days, day])}
                >{day[0].toUpperCase() + day.slice(1)}</button
              >
            {/each}
          </div>
        </div>
        <div class="flex gap-2 items-end">
          <div><label class="lbl" for="dl-start">From</label><input id="dl-start" type="time" class="field !w-auto" bind:value={draft.start} /></div>
          <div><label class="lbl" for="dl-end">Until</label><input id="dl-end" type="time" class="field !w-auto" bind:value={draft.end} /></div>
        </div>
      </div>
      <label class="flex items-center gap-2.5"><Switch bind:checked={draft.active} label="Deal on" />On</label>

      {#if draft.kind !== 'bundle'}
        <div class="subcard p-3.5 flex flex-col gap-2">
          <span class="lbl !mb-0">Try it</span>
          <p class="help !mt-0">Build a test cart: priced in your browser with this deal as it is now ({CHANNEL_KIND_LABELS[draft.channels[0] ?? 'web']}).</p>
          {#each preview.lines as l, i (i)}
            {@const it = menuItems.find((x) => x.id === l.item_id)}
            <div class="flex flex-wrap items-center gap-2 text-[13px]">
              <input type="number" min="1" max="50" class="field mono !w-16" bind:value={l.qty} aria-label="Quantity" />
              <span class="flex-1 min-w-[120px]">{it?.name}</span>
              {#if it?.sizes?.length}
                <select class="field !w-auto" bind:value={l.size_id} aria-label="Size">
                  {#each it.sizes as s (s.size_id)}<option value={s.size_id}>{s.name}</option>{/each}
                </select>
              {/if}
              <Button size="xs" variant="ghost" icon="x" aria-label="Remove" onclick={() => preview.lines.splice(i, 1)} />
            </div>
          {/each}
          <div class="flex gap-2">
            <select class="field flex-1" bind:value={preview.add} aria-label="Add an item to the test cart">
              <option value="">Add an item…</option>
              {#each menuItems as it (it.id)}<option value={it.id}>{it.name}</option>{/each}
            </select>
            <Button size="sm" icon="plus" disabled={!preview.add} onclick={addLine}>Add</Button>
          </div>
          {#if quote}
            <div class="text-[13px] flex flex-col gap-0.5 mt-1">
              <div class="flex justify-between"><span>Subtotal</span><span class="mono">{dollars(quote.subtotal_cents)}</span></div>
              {#each quote.discounts as x, i (i)}<div class="flex justify-between text-ok"><span>{x.label}</span><span class="mono">−{dollars(x.amount_cents)}</span></div>{/each}
              <div class="flex justify-between font-semibold"><span>Total with tax</span><span class="mono">{dollars(quote.total_cents)}</span></div>
              {#each quote.nudges as n, i (i)}<div class="text-muted">{n}</div>{/each}
              {#each quote.warnings as w, i (i)}<div class="text-muted">{w}</div>{/each}
              {#each quote.errors.filter((x) => x.code.startsWith('code')) as x, i (i)}<div class="text-warn">{x.message}</div>{/each}
            </div>
          {/if}
        </div>
      {/if}
      {#if err}<p class="field-error !m-0" role="alert">{err}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <Button onclick={() => (open = false)}>Cancel</Button>
    <Button type="submit" form="deal-form" variant="primary" loading={saving}>Save deal</Button>
  {/snippet}
</Dialog>
