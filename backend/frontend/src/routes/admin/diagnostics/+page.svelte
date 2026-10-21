<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { adminApi, fmtTime, type DiagSummary, type DiagDetail, type Overview, type Tenant } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge, { type Tone } from '$lib/ui/Badge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';

  // Tablet diagnostics: reports Quibase's troubleshooting guides send (via the
  // Pi or straight from the tablet). Everything here is text from a tablet —
  // rendered with {…} only, never {@html}. ?id=<uuid or 8-char prefix> deep-links.

  const KINDS = ['setup', 'guide', 'manual', 'full_check'];
  const OUTCOMES = ['ok', 'fixed', 'needs_part', 'needs_owner', 'not_fixed', 'abandoned', 'info'];

  let reports = $state<DiagSummary[]>([]);
  let tenants = $state<Tenant[]>([]);
  let filter = $state({ tenant_id: '', kind: '', outcome: '', unresolved: false });
  let selected = $state<DiagDetail | null>(null);
  let loading = $state(false);
  let first = $state(true);
  let detailEl = $state<HTMLElement>();

  async function reload() {
    loading = true;
    try {
      const q = new URLSearchParams();
      if (filter.tenant_id) q.set('tenant_id', filter.tenant_id);
      if (filter.kind) q.set('kind', filter.kind);
      if (filter.outcome) q.set('outcome', filter.outcome);
      if (filter.unresolved) q.set('unresolved', 'true');
      q.set('limit', '200');
      reports = await adminApi<DiagSummary[]>('/diag-reports?' + q.toString());
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
      first = false;
    }
  }

  async function open(id: string, push = true) {
    try {
      selected = await adminApi<DiagDetail>('/diag-reports/' + encodeURIComponent(id));
      if (push) {
        const url = new URL(page.url);
        url.searchParams.set('id', selected.id.slice(0, 8));
        goto(url, { replaceState: true, keepFocus: true, noScroll: true });
      }
      // On narrow screens the detail sits below the list — bring it into view.
      if (window.innerWidth < 1024) queueMicrotask(() => detailEl?.scrollIntoView({ behavior: 'smooth', block: 'start' }));
    } catch (e) {
      selected = null;
      toast.error(e);
    }
  }

  function close() {
    selected = null;
    const url = new URL(page.url);
    url.searchParams.delete('id');
    goto(url, { replaceState: true, keepFocus: true, noScroll: true });
  }

  async function setResolved(resolved: boolean) {
    if (!selected) return;
    try {
      selected = await adminApi<DiagDetail>('/diag-reports/' + selected.id, {
        method: 'PATCH',
        body: JSON.stringify({ resolved })
      });
      toast.success(resolved ? 'Marked resolved' : 'Reopened');
      reload();
    } catch (e) {
      toast.error(e);
    }
  }

  function outcomeTone(o: string): Tone {
    switch (o) {
      case 'ok':
      case 'fixed':
        return 'ok';
      case 'info':
        return 'info';
      case 'abandoned':
        return 'warn';
      case 'needs_part':
      case 'needs_owner':
      case 'not_fixed':
        return 'bad';
      default:
        return 'mute';
    }
  }
  function checkClass(s: unknown): string {
    if (s === 'ok' || s === true) return 'text-ok';
    if (s === 'fail' || s === false) return 'text-bad';
    return 'text-muted';
  }
  const plain = (s: string | null | undefined) => (s ? s.replaceAll('_', ' ') : '—');
  const show = (v: unknown) =>
    v === null || v === undefined || v === '' ? '—' : typeof v === 'object' ? JSON.stringify(v) : String(v);
  const entries = (o: unknown): [string, unknown][] =>
    o && typeof o === 'object' && !Array.isArray(o) ? Object.entries(o as Record<string, unknown>) : [];

  onMount(async () => {
    try {
      tenants = (await adminApi<Overview>('/overview')).tenants;
    } catch (e) {
      toast.error(e);
    }
    await reload();
    const id = page.url.searchParams.get('id');
    if (id) open(id, false);
  });
</script>

<svelte:head><title>Diagnostics — quicc admin</title></svelte:head>

<PageHeader title="Diagnostics" subtitle="Troubleshooting reports from Quibase tablets · sent via the Pi or straight from the tablet">
  {#snippet actions()}
    <Button icon="refresh" loading={loading} onclick={reload}>Refresh</Button>
  {/snippet}
</PageHeader>

<section class="card px-[18px] py-3.5" aria-label="Filters">
  <div class="flex flex-wrap items-end gap-3">
    <div>
      <label class="lbl" for="f-store">Store</label>
      <select id="f-store" class="field !w-auto min-w-[160px]" bind:value={filter.tenant_id} onchange={reload}>
        <option value="">Any</option>
        {#each tenants as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
      </select>
    </div>
    <div>
      <label class="lbl" for="f-kind">Kind</label>
      <select id="f-kind" class="field !w-auto min-w-[130px]" bind:value={filter.kind} onchange={reload}>
        <option value="">Any</option>
        {#each KINDS as k}<option value={k}>{plain(k)}</option>{/each}
      </select>
    </div>
    <div>
      <label class="lbl" for="f-outcome">Outcome</label>
      <select id="f-outcome" class="field !w-auto min-w-[140px]" bind:value={filter.outcome} onchange={reload}>
        <option value="">Any</option>
        {#each OUTCOMES as o}<option value={o}>{plain(o)}</option>{/each}
      </select>
    </div>
    <button
      type="button"
      class="fchip mb-[3px]"
      aria-pressed={filter.unresolved}
      onclick={() => {
        filter.unresolved = !filter.unresolved;
        reload();
      }}>Unresolved only</button
    >
  </div>
</section>

<div class="grid grid-cols-1 lg:grid-cols-5 gap-4 items-start">
  <section class="card lg:col-span-2 overflow-hidden" aria-label="Reports">
    {#if first}
      <div class="p-5"><Skeleton lines={4} /></div>
    {:else if reports.length}
      <ul class="divide-y divide-border">
        {#each reports as r (r.id)}
          <li>
            <button
              type="button"
              class="w-full text-left px-[18px] py-3 hover:bg-hover {selected?.id === r.id ? 'bg-brand-soft' : ''}"
              aria-current={selected?.id === r.id ? 'true' : undefined}
              onclick={() => open(r.id)}
            >
              <div class="flex flex-wrap items-center gap-2 mb-1">
                <Badge tone={outcomeTone(r.outcome)}>{plain(r.outcome)}</Badge>
                <Badge>{plain(r.kind)}</Badge>
                {#if r.resolved_at}<span class="text-xs text-ok inline-flex items-center gap-1"><Icon name="check" size={12} stroke={3} />resolved</span>{/if}
                {#if r.paged_at}<span class="text-xs text-muted" title="owner paged {fmtTime(r.paged_at)}">paged</span>{/if}
              </div>
              <div class="font-semibold truncate">{r.headline || plain(r.scenario) || '(no headline)'}</div>
              <div class="text-xs text-muted truncate">
                {r.store_name} · {r.tablet_name || r.tablet_id || 'tablet'} · via {r.via === 'pi' ? 'Pi' : 'tablet'} · {fmtTime(r.received_at)}
              </div>
            </button>
          </li>
        {/each}
      </ul>
    {:else}
      <EmptyState icon="activity" title="No reports match" body="Try another store or outcome, or turn off “Unresolved only”." />
    {/if}
  </section>

  <section class="card lg:col-span-3 scroll-mt-4" bind:this={detailEl} aria-label="Report">
    {#if selected}
      {@const rep = selected.report || {}}
      <div class="px-5 py-4 border-b border-border flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2 mb-1.5">
            <Badge tone={outcomeTone(selected.outcome)}>{plain(selected.outcome)}</Badge>
            <span class="text-xs text-muted">{plain(selected.kind)}{selected.scenario ? ' · ' + plain(selected.scenario) : ''}</span>
          </div>
          <h2 class="m-0 text-base font-semibold">{selected.headline || '(no headline)'}</h2>
          <div class="text-xs text-muted mt-0.5">
            {selected.store_name} · sent via {selected.via === 'pi' ? 'the Pi' : 'the tablet'} · received {fmtTime(selected.received_at)}
          </div>
          <div class="text-xs mono text-muted mt-0.5">{selected.id}</div>
        </div>
        <div class="flex gap-2">
          {#if selected.resolved_at}
            <Button size="sm" onclick={() => setResolved(false)}>Reopen</Button>
          {:else}
            <Button size="sm" variant="primary" icon="check" onclick={() => setResolved(true)}>Mark resolved</Button>
          {/if}
          <Button size="sm" variant="ghost" icon="x" aria-label="Close report" onclick={close} />
        </div>
      </div>

      <div class="px-5 py-4 flex flex-col gap-5">
        <div class="grid grid-cols-2 sm:grid-cols-3 gap-3">
          <div><div class="eyebrow mb-1">Cause</div>{plain(selected.cause)}</div>
          <div><div class="eyebrow mb-1">Fixed by</div>{plain(selected.fixed_by)}</div>
          <div><div class="eyebrow mb-1">Created on tablet</div>{fmtTime(selected.created_at)}</div>
          <div><div class="eyebrow mb-1">Owner paged</div>{fmtTime(selected.paged_at)}</div>
          <div><div class="eyebrow mb-1">Resolved</div>{fmtTime(selected.resolved_at)}</div>
        </div>
        {#if rep.note}
          <div><div class="eyebrow mb-1">Note from staff</div><div class="whitespace-pre-wrap">{rep.note}</div></div>
        {/if}

        {#if rep.steps?.length}
          <div>
            <div class="eyebrow mb-2">Steps</div>
            <ol class="border-l-2 border-border ml-1 flex flex-col gap-2">
              {#each rep.steps as st, i}
                <li class="pl-3 relative">
                  <span class="absolute -left-[5px] top-1.5 h-2 w-2 rounded-full bg-brand"></span>
                  <div class="font-medium">{i + 1}. {plain(String(st.id ?? 'step'))}</div>
                  <div class="text-xs text-muted">
                    {st.answer ? 'answer: ' + st.answer : ''}{st.result ? ' · result: ' + plain(String(st.result)) : ''}{st.at ? ' · ' + fmtTime(String(st.at)) : ''}
                  </div>
                </li>
              {/each}
            </ol>
          </div>
        {/if}

        {#if entries(rep.checks).length}
          <div>
            <div class="eyebrow mb-2">Checks</div>
            <div class="overflow-x-auto subcard">
              <table class="w-full text-xs">
                <tbody class="divide-y divide-border">
                  {#each entries(rep.checks) as [name, c]}
                    {@const chk = (c ?? {}) as Record<string, unknown>}
                    <tr>
                      <td class="py-1.5 px-3 mono whitespace-nowrap">{name}</td>
                      <td class="py-1.5 pr-3 font-semibold {checkClass(chk.status)}">{show(chk.status)}</td>
                      <td class="py-1.5 pr-3">{show(chk.plain)}</td>
                      <td class="py-1.5 pr-3 text-muted break-all">{chk.details ? show(chk.details) : ''}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </div>
        {/if}

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div class="subcard px-4 py-3">
            <div class="eyebrow mb-1.5">Tablet</div>
            <dl class="text-xs grid grid-cols-[auto,1fr] gap-x-3 gap-y-1">
              <dt class="text-muted">name</dt><dd>{selected.tablet_name || '—'}</dd>
              <dt class="text-muted">tablet id</dt><dd class="mono break-all">{selected.tablet_id || '—'}</dd>
              <dt class="text-muted">Quibase</dt><dd class="mono">{selected.app_version || '—'}</dd>
              {#each entries(rep.tablet).filter(([k]) => !['name', 'app_version', 'checks'].includes(k)) as [k, v]}
                <dt class="text-muted">{plain(k)}</dt><dd class="break-all">{show(v)}</dd>
              {/each}
              {#each entries(rep.tablet?.checks) as [k, v]}
                <dt class="text-muted">{plain(k)}</dt><dd class={checkClass(v)}>{show(v)}</dd>
              {/each}
            </dl>
          </div>
          <div class="subcard px-4 py-3">
            <div class="eyebrow mb-1.5">Pi</div>
            <dl class="text-xs grid grid-cols-[auto,1fr] gap-x-3 gap-y-1">
              <dt class="text-muted">version</dt><dd class="mono">{selected.agent_version || '—'}</dd>
              {#each entries(rep.pi).filter(([k]) => !['version', 'health', 'last_diag'].includes(k)) as [k, v]}
                <dt class="text-muted">{plain(k)}</dt><dd class="break-all">{show(v)}</dd>
              {/each}
            </dl>
            {#if rep.pi?.health}
              <div class="eyebrow mt-3 mb-1">Health (from the Pi)</div>
              <pre class="code text-[11px]">{JSON.stringify(rep.pi.health, null, 2)}</pre>
            {/if}
          </div>
        </div>

        {#if rep.log?.length}
          <div>
            <div class="eyebrow mb-1.5">Tablet log ({rep.log.length} lines)</div>
            <pre class="code subcard px-3 py-2 text-[11px] max-h-80 overflow-auto">{rep.log.join('\n')}</pre>
          </div>
        {/if}

        <details>
          <summary class="cursor-pointer text-xs text-muted">Raw report JSON</summary>
          <pre class="code subcard mt-2 px-3 py-2 text-[11px] max-h-96 overflow-auto">{JSON.stringify(rep, null, 2)}</pre>
        </details>
      </div>
    {:else}
      <EmptyState icon="activity" title="Pick a report" body="See its steps, checks and the tablet's log." />
    {/if}
  </section>
</div>
