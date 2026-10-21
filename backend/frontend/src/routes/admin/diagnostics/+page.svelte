<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import {
    adminApi, getAdminKey, ApiError, fmtTime,
    type DiagSummary, type DiagDetail, type Overview, type Tenant
  } from '$lib/api';

  // Tablet diagnostics: reports Quibase's troubleshooting guides send (via the
  // Pi or straight from the tablet). Everything here is text from a tablet —
  // rendered with {…} only, never {@html}.

  const KINDS = ['setup', 'guide', 'manual', 'full_check'];
  const OUTCOMES = ['ok', 'fixed', 'needs_part', 'needs_owner', 'not_fixed', 'abandoned', 'info'];

  let reports = $state<DiagSummary[]>([]);
  let tenants = $state<Tenant[]>([]);
  let filter = $state({ tenant_id: '', kind: '', outcome: '', unresolved: false });
  let selected = $state<DiagDetail | null>(null);
  let loading = $state(false);
  let banner = $state<{ msg: string; kind: 'ok' | 'error' | 'info' } | null>(null);

  // Errors stay until dismissed or replaced; other messages auto-hide. The
  // previous timer is cleared so a new message isn't cut short by an old one.
  let bannerTimer: ReturnType<typeof setTimeout> | undefined;
  function toast(msg: string, kind: 'ok' | 'error' | 'info' = 'ok') {
    clearTimeout(bannerTimer);
    banner = { msg, kind };
    bannerTimer = kind === 'error' ? undefined : setTimeout(() => (banner = null), 4000);
  }
  function dismissBanner() {
    clearTimeout(bannerTimer);
    banner = null;
  }
  function err(e: unknown) { toast(e instanceof ApiError ? e.message : String(e), 'error'); }

  async function reload() {
    if (!getAdminKey()) { reports = []; return; }
    loading = true;
    try {
      const q = new URLSearchParams();
      if (filter.tenant_id) q.set('tenant_id', filter.tenant_id);
      if (filter.kind) q.set('kind', filter.kind);
      if (filter.outcome) q.set('outcome', filter.outcome);
      if (filter.unresolved) q.set('unresolved', 'true');
      q.set('limit', '200');
      reports = await adminApi<DiagSummary[]>('/diag-reports?' + q.toString());
    } catch (e) { err(e); } finally { loading = false; }
  }

  async function open(id: string, push = true) {
    try {
      selected = await adminApi<DiagDetail>('/diag-reports/' + encodeURIComponent(id));
      if (push) {
        const url = new URL($page.url);
        url.searchParams.set('id', selected.id.slice(0, 8));
        goto(url, { replaceState: true, keepFocus: true, noScroll: true });
      }
    } catch (e) {
      selected = null;
      err(e);
    }
  }

  function close() {
    selected = null;
    const url = new URL($page.url);
    url.searchParams.delete('id');
    goto(url, { replaceState: true, keepFocus: true, noScroll: true });
  }

  async function setResolved(resolved: boolean) {
    if (!selected) return;
    try {
      selected = await adminApi<DiagDetail>('/diag-reports/' + selected.id, {
        method: 'PATCH', body: JSON.stringify({ resolved })
      });
      toast(resolved ? 'marked resolved' : 'reopened');
      reload();
    } catch (e) { err(e); }
  }

  function outcomeClass(o: string): string {
    switch (o) {
      case 'ok':
      case 'fixed':
        return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-200';
      case 'info':
        return 'bg-sky-100 text-sky-800 dark:bg-sky-900/50 dark:text-sky-200';
      case 'abandoned':
        return 'bg-amber-100 text-amber-800 dark:bg-amber-900/50 dark:text-amber-200';
      case 'needs_part':
      case 'needs_owner':
      case 'not_fixed':
        return 'bg-rose-100 text-rose-800 dark:bg-rose-900/50 dark:text-rose-200';
      default:
        return 'bg-slate-200 text-slate-700 dark:bg-slate-800 dark:text-slate-300';
    }
  }
  function checkClass(s: unknown): string {
    if (s === 'ok' || s === true) return 'text-emerald-600 dark:text-emerald-400';
    if (s === 'fail' || s === false) return 'text-rose-600 dark:text-rose-400';
    return 'text-slate-500 dark:text-slate-400';
  }
  const plain = (s: string | null | undefined) => (s ? s.replaceAll('_', ' ') : '—');
  const show = (v: unknown) =>
    v === null || v === undefined || v === '' ? '—' : typeof v === 'object' ? JSON.stringify(v) : String(v);
  const entries = (o: unknown): [string, unknown][] =>
    o && typeof o === 'object' && !Array.isArray(o) ? Object.entries(o as Record<string, unknown>) : [];

  onMount(async () => {
    if (getAdminKey()) {
      try {
        tenants = (await adminApi<Overview>('/overview')).tenants;
      } catch (e) { err(e); }
    }
    await reload();
    const id = $page.url.searchParams.get('id');
    if (id) open(id, false);
  });
</script>

<div class="flex flex-wrap items-center justify-between gap-3 mb-4">
  <div>
    <div class="text-lg font-semibold">Diagnostics</div>
    <div class="text-xs text-slate-500 dark:text-slate-400">
      troubleshooting reports from Quibase tablets · sent via the Pi or straight from the tablet
    </div>
  </div>
  <button class="btn btn-ghost btn-xs" onclick={reload} disabled={loading}>{loading ? 'loading…' : 'refresh'}</button>
</div>

{#if banner}
  <div
    class="rounded-md border px-4 py-3 text-sm mb-4 flex items-start gap-3 {banner.kind === 'error'
      ? 'border-rose-300 bg-rose-50 text-rose-900 dark:border-rose-900/60 dark:bg-rose-950/60 dark:text-rose-200'
      : 'border-emerald-300 bg-emerald-50 text-emerald-900 dark:border-emerald-900/60 dark:bg-emerald-950/50 dark:text-emerald-200'}"
  >
    <span class="flex-1 min-w-0 break-words">{banner.msg}</span>
    <button type="button" class="shrink-0 opacity-60 hover:opacity-100 leading-none text-base" aria-label="dismiss" title="dismiss" onclick={dismissBanner}>×</button>
  </div>
{/if}

<section class="card mb-4 px-4 py-3">
  <div class="flex flex-wrap items-end gap-3">
    <div>
      <label class="lbl" for="f-store">store</label>
      <select id="f-store" class="field" bind:value={filter.tenant_id} onchange={reload}>
        <option value="">any</option>
        {#each tenants as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
      </select>
    </div>
    <div>
      <label class="lbl" for="f-kind">kind</label>
      <select id="f-kind" class="field" bind:value={filter.kind} onchange={reload}>
        <option value="">any</option>
        {#each KINDS as k}<option value={k}>{plain(k)}</option>{/each}
      </select>
    </div>
    <div>
      <label class="lbl" for="f-outcome">outcome</label>
      <select id="f-outcome" class="field" bind:value={filter.outcome} onchange={reload}>
        <option value="">any</option>
        {#each OUTCOMES as o}<option value={o}>{plain(o)}</option>{/each}
      </select>
    </div>
    <label class="flex items-center gap-2 text-sm pb-1.5">
      <input type="checkbox" bind:checked={filter.unresolved} onchange={reload} /> unresolved only
    </label>
  </div>
</section>

<div class="grid grid-cols-1 lg:grid-cols-5 gap-4 items-start">
  <section class="card lg:col-span-2 divide-y divide-slate-200 dark:divide-slate-800">
    {#if reports.length}
      {#each reports as r (r.id)}
        <button
          class="w-full text-left px-4 py-3 hover:bg-slate-50 dark:hover:bg-slate-800/60 {selected?.id === r.id
            ? 'bg-slate-50 dark:bg-slate-800/60'
            : ''}"
          onclick={() => open(r.id)}
        >
          <div class="flex items-center gap-2 mb-1">
            <span class="chip {outcomeClass(r.outcome)}">{plain(r.outcome)}</span>
            <span class="chip bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300">{plain(r.kind)}</span>
            {#if r.resolved_at}<span class="text-xs text-emerald-600 dark:text-emerald-400">✓ resolved</span>{/if}
            {#if r.paged_at}<span class="text-xs text-slate-500" title="owner paged {fmtTime(r.paged_at)}">paged</span>{/if}
          </div>
          <div class="text-sm font-medium truncate">{r.headline || plain(r.scenario) || '(no headline)'}</div>
          <div class="text-xs text-slate-500 dark:text-slate-400 truncate">
            {r.store_name} · {r.tablet_name || r.tablet_id || 'tablet'} · via {r.via === 'pi' ? 'Pi' : 'tablet'} · {fmtTime(r.received_at)}
          </div>
        </button>
      {/each}
    {:else}
      <div class="px-4 py-6 text-sm text-slate-500 dark:text-slate-400">
        {getAdminKey() ? 'No reports match.' : 'Set your ADMIN_API_KEY on the Admin tab first.'}
      </div>
    {/if}
  </section>

  <section class="card lg:col-span-3">
    {#if selected}
      {@const rep = selected.report || {}}
      <div class="px-4 py-3 border-b border-slate-200 dark:border-slate-800 flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <div class="flex items-center gap-2 mb-1">
            <span class="chip {outcomeClass(selected.outcome)}">{plain(selected.outcome)}</span>
            <span class="text-xs text-slate-500 dark:text-slate-400">
              {plain(selected.kind)}{selected.scenario ? ' · ' + plain(selected.scenario) : ''}
            </span>
          </div>
          <div class="font-semibold">{selected.headline || '(no headline)'}</div>
          <div class="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
            {selected.store_name} · sent via {selected.via === 'pi' ? 'the Pi' : 'the tablet'} · received {fmtTime(selected.received_at)}
          </div>
          <div class="text-xs mono text-slate-400 mt-0.5">{selected.id}</div>
        </div>
        <div class="flex gap-2">
          {#if selected.resolved_at}
            <button class="btn btn-ghost btn-xs" onclick={() => setResolved(false)}>reopen</button>
          {:else}
            <button class="btn btn-primary btn-xs" onclick={() => setResolved(true)}>mark resolved</button>
          {/if}
          <button class="btn btn-ghost btn-xs" onclick={close}>close</button>
        </div>
      </div>

      <div class="px-4 py-3 space-y-4 text-sm">
        <div class="grid grid-cols-2 sm:grid-cols-3 gap-3">
          <div><div class="lbl">cause</div>{plain(selected.cause)}</div>
          <div><div class="lbl">fixed by</div>{plain(selected.fixed_by)}</div>
          <div><div class="lbl">created on tablet</div>{fmtTime(selected.created_at)}</div>
          <div><div class="lbl">owner paged</div>{fmtTime(selected.paged_at)}</div>
          <div><div class="lbl">resolved</div>{fmtTime(selected.resolved_at)}</div>
        </div>
        {#if rep.note}
          <div><div class="lbl">note from staff</div><div class="whitespace-pre-wrap">{rep.note}</div></div>
        {/if}

        {#if rep.steps?.length}
          <div>
            <div class="lbl">steps</div>
            <ol class="border-l-2 border-slate-200 dark:border-slate-700 ml-1 space-y-2">
              {#each rep.steps as st, i}
                <li class="pl-3 relative">
                  <span class="absolute -left-[5px] top-1.5 h-2 w-2 rounded-full bg-slate-400"></span>
                  <div class="font-medium">{i + 1}. {plain(String(st.id ?? 'step'))}</div>
                  <div class="text-xs text-slate-500 dark:text-slate-400">
                    {st.answer ? 'answer: ' + st.answer : ''}{st.result ? ' · result: ' + plain(String(st.result)) : ''}{st.at ? ' · ' + fmtTime(String(st.at)) : ''}
                  </div>
                </li>
              {/each}
            </ol>
          </div>
        {/if}

        {#if entries(rep.checks).length}
          <div>
            <div class="lbl">checks</div>
            <table class="w-full text-xs">
              <tbody class="divide-y divide-slate-200 dark:divide-slate-800">
                {#each entries(rep.checks) as [name, c]}
                  {@const chk = (c ?? {}) as Record<string, unknown>}
                  <tr>
                    <td class="py-1 pr-3 mono whitespace-nowrap">{name}</td>
                    <td class="py-1 pr-3 font-medium {checkClass(chk.status)}">{show(chk.status)}</td>
                    <td class="py-1 pr-3">{show(chk.plain)}</td>
                    <td class="py-1 text-slate-500 dark:text-slate-400 break-all">{chk.details ? show(chk.details) : ''}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div class="subcard px-3 py-2">
            <div class="lbl">tablet</div>
            <dl class="text-xs grid grid-cols-[auto,1fr] gap-x-3 gap-y-0.5">
              <dt class="text-slate-500">name</dt><dd>{selected.tablet_name || '—'}</dd>
              <dt class="text-slate-500">tablet id</dt><dd class="mono break-all">{selected.tablet_id || '—'}</dd>
              <dt class="text-slate-500">Quibase</dt><dd>{selected.app_version || '—'}</dd>
              {#each entries(rep.tablet).filter(([k]) => !['name', 'app_version', 'checks'].includes(k)) as [k, v]}
                <dt class="text-slate-500">{plain(k)}</dt><dd class="break-all">{show(v)}</dd>
              {/each}
              {#each entries(rep.tablet?.checks) as [k, v]}
                <dt class="text-slate-500">{plain(k)}</dt><dd class={checkClass(v)}>{show(v)}</dd>
              {/each}
            </dl>
          </div>
          <div class="subcard px-3 py-2">
            <div class="lbl">Pi</div>
            <dl class="text-xs grid grid-cols-[auto,1fr] gap-x-3 gap-y-0.5">
              <dt class="text-slate-500">version</dt><dd>{selected.agent_version || '—'}</dd>
              {#each entries(rep.pi).filter(([k]) => !['version', 'health', 'last_diag'].includes(k)) as [k, v]}
                <dt class="text-slate-500">{plain(k)}</dt><dd class="break-all">{show(v)}</dd>
              {/each}
            </dl>
            {#if rep.pi?.health}
              <div class="lbl mt-2">health (from the Pi)</div>
              <pre class="text-[0.7rem] mono whitespace-pre-wrap break-all">{JSON.stringify(rep.pi.health, null, 2)}</pre>
            {/if}
          </div>
        </div>

        {#if rep.log?.length}
          <div>
            <div class="lbl">tablet log ({rep.log.length} lines)</div>
            <pre class="subcard px-3 py-2 text-[0.7rem] mono max-h-80 overflow-auto whitespace-pre-wrap break-all">{rep.log.join('\n')}</pre>
          </div>
        {/if}

        <details>
          <summary class="cursor-pointer text-xs text-slate-500 dark:text-slate-400">raw report JSON</summary>
          <pre class="subcard mt-2 px-3 py-2 text-[0.7rem] mono max-h-96 overflow-auto">{JSON.stringify(rep, null, 2)}</pre>
        </details>
      </div>
    {:else}
      <div class="px-4 py-6 text-sm text-slate-500 dark:text-slate-400">Pick a report to see its steps, checks and log.</div>
    {/if}
  </section>
</div>
