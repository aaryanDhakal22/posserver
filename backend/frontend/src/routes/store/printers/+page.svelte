<script lang="ts">
  import { onMount } from 'svelte';
  import { tick } from 'svelte';
  import {
    errText,
    fmtTime,
    hhmm,
    storeApi,
    timeAgo,
    type FoundPrinter,
    type LivePrinter,
    type LivePrinters,
    type Printer,
    type PrinterIPChange
  } from '$lib/api';
  import Card from '$lib/ui/Card.svelte';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { menuUsage, type PrinterUsage } from '$lib/store/menuUsage';
  import { toast } from '$lib/toast.svelte';
  import type { Tone } from '$lib/ui/Badge.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import { loadPiStatus, PI_NOT_LINKED, type PiLink } from '$lib/store/pi';

  // Printers with the Pi's live status. Kitchen stations print only their own
  // items (set per item / category in the Menu); the receipt printer prints the
  // full customer receipt. Every change reaches the store's Pi immediately.
  let printers = $state<Printer[] | null>(null);
  let live = $state<LivePrinters | null>(null);
  let pi = $state<PiLink>('unknown');
  let usage = $state<Record<string, PrinterUsage> | null>(null);

  let ipChanges = $state<PrinterIPChange[]>([]);

  let adding = $state(false);
  let np = $state({ name: '', ip: '', mac: '' });
  let addErr = $state('');
  let saving = $state(false);

  let editId = $state<string | null>(null);
  let edit = $state({ name: '', ip: '' });

  async function loadPrinters() {
    printers = await storeApi<Printer[]>('/me/printers');
  }
  async function loadLive() {
    ({ live, pi } = await loadPiStatus());
  }
  async function loadUsage() {
    usage = await menuUsage();
  }
  async function loadIPChanges() {
    ipChanges = await storeApi<PrinterIPChange[]>('/me/printers/ip-changes?limit=20').catch(() => ipChanges);
  }

  onMount(() => {
    loadPrinters().catch((e) => toast.error(e));
    loadLive();
    loadUsage();
    loadIPChanges();
    const t = setInterval(() => {
      loadLive();
      loadIPChanges();
    }, 25000);
    return () => clearInterval(t);
  });

  function liveFor(p: Printer): LivePrinter | undefined {
    const roster = live?.printers ?? [];
    return roster.find((x) => x.name === p.name) ?? roster.find((x) => !!p.ip && x.ip === p.ip);
  }
  function status(p: Printer): { label: string; tone: Tone; down: LivePrinter | null } {
    if (pi === 'none') return { label: 'No Pi yet', tone: 'mute', down: null };
    if (!live) return { label: 'Status unknown', tone: 'mute', down: null };
    if (!live.agent_online) return { label: 'Pi offline', tone: 'mute', down: null };
    const lp = liveFor(p);
    if (!lp) return { label: 'Not checked yet', tone: 'mute', down: null };
    return lp.up ? { label: 'Ready', tone: 'ok', down: null } : { label: 'Not responding', tone: 'bad', down: lp };
  }
  const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`;

  // The MAC the Pi learned (stored with the printer, else from its live report).
  const macFor = (p: Printer) => p.mac || liveFor(p)?.mac || '';
  // The printer's newest IP change, when it's from the last 7 days.
  function recentMove(p: Printer): PrinterIPChange | null {
    const c = ipChanges.find((x) => x.printer_id === p.id);
    return c && Date.now() - new Date(c.at).getTime() < 7 * 86400_000 ? c : null;
  }
  // Printers the Pi found on its network that aren't set up yet (agent ≥ 2.0.6).
  const found = $derived<FoundPrinter[]>(
    (live?.found ?? []).filter((f) => !(printers ?? []).some((p) => p.ip === f.ip || (!!f.mac && p.mac === f.mac)))
  );
  async function addFound(f: FoundPrinter) {
    np = { name: '', ip: f.ip, mac: f.mac };
    addErr = '';
    adding = true;
    await tick();
    document.getElementById('np-name')?.focus();
  }
  function usageText(p: Printer): string {
    const u = usage?.[p.id];
    const items = u ? `${plural(u.effective, 'menu item')} print here` : '';
    if (p.is_receipt) return items && u?.effective ? `Prints every receipt · ${items}` : 'Prints every receipt';
    return usage ? items || 'No menu items print here yet' : '';
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    addErr = '';
    if (!np.name.trim()) return (addErr = 'Give the printer a name.');
    saving = true;
    try {
      // The MAC only rides along while the IP is still the found printer's.
      const mac = found.some((f) => f.ip === np.ip.trim() && f.mac === np.mac) ? np.mac : '';
      await storeApi('/me/printers', { method: 'POST', body: JSON.stringify({ name: np.name.trim(), ip: np.ip.trim(), mac }) });
      toast.success(`“${np.name.trim()}” added`);
      np = { name: '', ip: '', mac: '' };
      adding = false;
      await loadPrinters();
      loadLive();
    } catch (err) {
      addErr = errText(err);
    } finally {
      saving = false;
    }
  }

  function startEdit(p: Printer) {
    editId = p.id;
    edit = { name: p.name, ip: p.ip };
  }
  async function saveEdit(p: Printer) {
    if (!edit.name.trim()) return toast.error('A printer needs a name.');
    try {
      await storeApi('/me/printers/' + p.id, { method: 'PATCH', body: JSON.stringify({ name: edit.name.trim(), ip: edit.ip.trim() }) });
      toast.success('Printer saved');
      editId = null;
      await loadPrinters();
      loadIPChanges();
    } catch (err) {
      toast.error(err);
    }
  }

  async function useForReceipts(p: Printer) {
    try {
      await storeApi('/me/printers/' + p.id, { method: 'PATCH', body: JSON.stringify({ name: p.name, ip: p.ip, is_receipt: true }) });
      await loadPrinters();
      toast.success(`“${p.name}” now prints the customer receipt`);
    } catch (err) {
      toast.error(err);
    }
  }

  async function remove(p: Printer) {
    // Deleting a printer unassigns it from the menu: items fall back to their
    // category's printer, and anything left without one prints on every station.
    const u = usage?.[p.id];
    const lines: string[] = [];
    if (p.is_receipt) lines.push('It prints every receipt — that job moves to another printer.');
    if (!usage) lines.push('Any menu categories or items sent to this printer lose it.');
    else if (u && (u.cats || u.items))
      lines.push(
        `${plural(u.effective, 'menu item')} print on it (${plural(u.items, 'item')} set directly, ${plural(u.cats, 'category', 'categories')} by default). ` +
          "They'll print on their category's printer instead, or on every station if they're left without one."
      );
    else lines.push('No menu categories or items are sent to this printer.');
    lines.push('If the printer only moved or got a new IP address, cancel and edit its IP instead.');
    const ok = await confirmDialog({ title: `Delete “${p.name}”?`, body: lines.join('\n\n'), confirmLabel: 'Delete printer', danger: true });
    if (!ok) return;
    try {
      await storeApi('/me/printers/' + p.id, { method: 'DELETE' });
      await loadPrinters();
      const hadMenu = !!u && u.cats + u.items > 0;
      loadUsage();
      toast.success(`“${p.name}” deleted`, {
        action: {
          label: 'Undo',
          run: async () => {
            try {
              const back = await storeApi<Printer>('/me/printers', { method: 'POST', body: JSON.stringify({ name: p.name, ip: p.ip }) });
              if (p.is_receipt && back?.id)
                await storeApi('/me/printers/' + back.id, { method: 'PATCH', body: JSON.stringify({ name: p.name, ip: p.ip, is_receipt: true }) });
              await loadPrinters();
              toast.success(hadMenu ? `“${p.name}” is back — reassign its menu items in the Menu` : `“${p.name}” is back`);
            } catch (err) {
              toast.error(err);
            }
          }
        }
      });
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<svelte:head><title>Printers — quicc</title></svelte:head>

<PageHeader
  title="Printers"
  subtitle={pi === 'none'
    ? PI_NOT_LINKED
    : live && !live.agent_online
      ? 'The Pi is offline — live status is unavailable'
      : 'Live status from the Pi · checked every 25 s'}
>
  {#snippet actions()}
    <Button
      variant="primary"
      icon="plus"
      aria-expanded={adding}
      onclick={() => {
        if (!adding) np = { name: '', ip: '', mac: '' };
        adding = !adding;
      }}>Add printer</Button
    >
  {/snippet}
</PageHeader>

{#if adding}
  <form class="card p-[18px] grid gap-2.5 items-start" style="grid-template-columns: repeat(auto-fit, minmax(180px, 1fr))" onsubmit={add}>
    <div>
      <label class="lbl" for="np-name">Name</label>
      <input id="np-name" class="field" bind:value={np.name} placeholder="e.g. Grill" aria-invalid={!!addErr || undefined} />
      {#if addErr}<p class="field-error" role="alert">{addErr}</p>{/if}
    </div>
    <div>
      <label class="lbl" for="np-ip">IP address</label>
      <input id="np-ip" class="field mono" bind:value={np.ip} placeholder="192.168.1.x" />
      {#if np.mac}<p class="help mono">found on the network · {np.mac}</p>{/if}
    </div>
    <div class="flex gap-2 sm:pt-[25px]">
      <Button type="submit" variant="primary" loading={saving}>Add</Button>
      <Button variant="ghost" onclick={() => (adding = false)}>Cancel</Button>
    </div>
  </form>
{/if}

{#if found.length}
  <Card title="Found on the network" subtitle="Printers the Pi found that aren't set up yet — add one to start printing on it">
    <ul class="m-0 p-0 list-none flex flex-col">
      {#each found as f (f.ip)}
        <li class="flex flex-wrap items-center gap-3 py-2.5 border-t border-border first:border-t-0">
          <span class="text-accent"><Icon name="printer" size={18} /></span>
          <span class="flex-1 min-w-[160px]">
            <span class="mono font-semibold">{f.ip}</span>
            {#if f.mac}<span class="block mono text-xs text-muted">{f.mac}</span>{/if}
          </span>
          <Button size="sm" icon="plus" onclick={() => addFound(f)}>Add</Button>
        </li>
      {/each}
    </ul>
  </Card>
{/if}

{#if printers === null}
  <div class="grid gap-3.5" style="grid-template-columns: repeat(auto-fill, minmax(300px, 1fr))">
    {#each [0, 1, 2] as _}<div class="card p-[18px]"><Skeleton lines={3} /></div>{/each}
  </div>
{:else if printers.length === 0}
  <section class="card">
    <EmptyState icon="printer" title="No printers yet" body="Add each thermal printer on the store's network. The first one prints receipts.">
      {#snippet action()}<Button variant="primary" icon="plus" onclick={() => (adding = true)}>Add printer</Button>{/snippet}
    </EmptyState>
  </section>
{:else}
  <div class="grid gap-3.5" style="grid-template-columns: repeat(auto-fill, minmax(min(300px, 100%), 1fr))">
    {#each printers as p (p.id)}
      {@const st = status(p)}
      <article class="card p-[18px] flex flex-col gap-3.5" aria-label={p.name}>
        {#if editId === p.id}
          <form
            class="flex flex-col gap-3"
            onsubmit={(e) => {
              e.preventDefault();
              saveEdit(p);
            }}
          >
            <div>
              <label class="lbl" for="ed-name-{p.id}">Name</label>
              <input id="ed-name-{p.id}" class="field" bind:value={edit.name} />
            </div>
            <div>
              <label class="lbl" for="ed-ip-{p.id}">IP address</label>
              <input id="ed-ip-{p.id}" class="field mono" bind:value={edit.ip} />
            </div>
            <div class="flex gap-2">
              <Button type="submit" variant="primary" size="sm">Save</Button>
              <Button variant="ghost" size="sm" onclick={() => (editId = null)}>Cancel</Button>
            </div>
          </form>
        {:else}
          <div class="flex items-start gap-3">
            <span class="w-10 h-10 rounded-r12 bg-elev text-accent flex items-center justify-center shrink-0"><Icon name="printer" size={20} /></span>
            <div class="flex-1 min-w-0">
              <div class="font-bold text-[15px] truncate">{p.name}</div>
              <div class="mono text-[13px] text-muted">{p.ip || 'no IP set'}</div>
              {#if macFor(p)}<div class="mono text-[11px] text-muted" title="Hardware address — the Pi finds the printer by it if its IP changes">{macFor(p)}</div>{/if}
              {#if recentMove(p)}
                {@const mv = recentMove(p)!}
                <div class="text-xs text-text2 mt-0.5" title="{mv.source === 'agent' ? 'The Pi found it at a new IP' : 'Changed by a manager'} · {fmtTime(mv.at)}">
                  moved from <span class="mono">{mv.old_ip}</span> · {timeAgo(mv.at)}
                </div>
              {/if}
            </div>
            <Badge tone={st.tone} dot>{st.label}</Badge>
          </div>
          <div class="flex flex-wrap gap-1.5 items-center min-h-[22px]">
            {#if p.is_receipt}<Badge tone="brand">Receipts</Badge>{/if}
            <span class="text-[13px] text-text2">{usageText(p)}</span>
          </div>
          {#if st.down}
            <p class="m-0 text-[13px] text-bad">
              Not responding{st.down.last_checked ? ` (checked ${hhmm(st.down.last_checked)})` : ''}. Its tickets are rerouted to another
              printer, marked REROUTED.
              {#if st.down.last_error}<span class="block text-muted mono text-xs mt-1" title={fmtTime(st.down.last_checked)}>{st.down.last_error}</span>{/if}
            </p>
          {/if}
        {/if}
        <div class="flex flex-wrap gap-2 pt-3 border-t border-border mt-auto">
          {#if editId !== p.id}<Button size="sm" icon="pencil" onclick={() => startEdit(p)}>Edit</Button>{/if}
          {#if !p.is_receipt}<Button size="sm" variant="ghost" onclick={() => useForReceipts(p)}>Use for receipts</Button>{/if}
          <span class="flex-1"></span>
          <Button size="sm" variant="danger-ghost" onclick={() => remove(p)}>Delete</Button>
        </div>
      </article>
    {/each}
  </div>
{/if}

{#if ipChanges.length}
  <Card title="IP changes" subtitle="When a printer gets a new address the Pi finds it and follows it; manager edits show here too">
    <ul class="m-0 p-0 list-none flex flex-col text-[13px]">
      {#each ipChanges as c (c.id)}
        <li class="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 border-t border-border first:border-t-0">
          <span class="font-semibold min-w-[110px]">{c.printer_name}</span>
          <span class="mono flex-1 min-w-[180px]">{c.old_ip || '—'} → {c.new_ip}</span>
          <Badge tone={c.source === 'agent' ? 'info' : 'mute'}>{c.source === 'agent' ? 'Pi followed it' : 'Manager'}</Badge>
          <span class="text-muted" title={fmtTime(c.at)}>{timeAgo(c.at)}</span>
        </li>
      {/each}
    </ul>
  </Card>
{/if}
