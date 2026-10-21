<script lang="ts">
  import { adminApi, ApiError, type OperatorAdmin, type Location } from '$lib/api';

  let {
    operator, locations, reload, toast
  }: {
    operator: OperatorAdmin;
    locations: Location[];
    reload: () => void;
    toast: (m: string, k?: 'ok' | 'error' | 'info') => void;
  } = $props();

  let showEdit = $state(false);
  let showLocs = $state(false);
  let edit = $state({ display_name: operator.display_name, password: '' });
  let chosen = $state<Set<string>>(new Set(operator.location_ids));

  function err(x: unknown) { toast(x instanceof ApiError ? x.message : String(x), 'error'); }
  const locName = (id: string) => locations.find((l) => l.id === id)?.name ?? id.slice(0, 8);

  function openEdit() { edit = { display_name: operator.display_name, password: '' }; showEdit = !showEdit; }
  function openLocs() { chosen = new Set(operator.location_ids); showLocs = !showLocs; }
  function toggle(id: string) {
    const next = new Set(chosen);
    next.has(id) ? next.delete(id) : next.add(id);
    chosen = next;
  }

  async function saveEdit() {
    try {
      await adminApi(`/operators/${operator.id}`, { method: 'PATCH', body: JSON.stringify({ display_name: edit.display_name, password: edit.password }) });
      toast('operator updated');
      showEdit = false;
      reload();
    } catch (x) { err(x); }
  }
  async function saveLocs() {
    try {
      await adminApi(`/operators/${operator.id}/locations`, { method: 'PUT', body: JSON.stringify({ location_ids: [...chosen] }) });
      toast('allowed locations updated');
      showLocs = false;
      reload();
    } catch (x) { err(x); }
  }
  async function revoke() {
    if (!confirm(`Revoke all active tokens for "${operator.display_name}"? Any signed-in session stops working immediately.`)) return;
    try { await adminApi(`/operators/${operator.id}/tokens/revoke`, { method: 'POST' }); toast('tokens revoked'); }
    catch (x) { err(x); }
  }
  async function del() {
    if (!confirm(`Delete operator "${operator.display_name}"?`)) return;
    try { await adminApi(`/operators/${operator.id}`, { method: 'DELETE' }); toast('operator deleted'); reload(); }
    catch (x) { err(x); }
  }
</script>

<div class="px-4 py-3">
  <div class="flex items-start justify-between gap-4">
    <div>
      <div class="flex items-center gap-2">
        <span class="font-medium">{operator.display_name}</span>
        <span class="chip bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300">@{operator.username}</span>
      </div>
      <div class="text-xs text-slate-500 dark:text-slate-400 mt-1 flex flex-wrap gap-1 items-center">
        {#if operator.location_ids.length}
          {#each operator.location_ids as id}
            <span class="chip bg-sky-100 text-sky-800 dark:bg-sky-900/50 dark:text-sky-200">{locName(id)}</span>
          {/each}
        {:else}
          <span class="chip bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200">no locations</span>
        {/if}
      </div>
    </div>
    <div class="flex flex-wrap gap-1 justify-end">
      <button class="btn btn-ghost btn-xs" onclick={openEdit}>edit</button>
      <button class="btn btn-ghost btn-xs" onclick={openLocs}>locations</button>
      <button class="btn btn-ghost btn-xs" onclick={revoke}>revoke tokens</button>
      <button class="btn btn-danger btn-xs" onclick={del}>delete</button>
    </div>
  </div>

  {#if showEdit}
    <div class="subcard px-3 py-2.5 mt-3 flex flex-wrap gap-2 items-end">
      <div class="grow"><label class="lbl">display name</label><input class="field w-full" bind:value={edit.display_name} /></div>
      <div><label class="lbl">new password <span class="normal-case text-slate-400">(optional)</span></label><input class="field" type="password" bind:value={edit.password} autocomplete="new-password" /></div>
      <button class="btn btn-primary btn-xs" onclick={saveEdit}>save</button>
    </div>
  {/if}

  {#if showLocs}
    <div class="subcard px-3 py-2.5 mt-3">
      <div class="text-xs text-slate-500 dark:text-slate-400 mb-2">Locations this operator can read the menu for.</div>
      <div class="flex flex-wrap gap-2">
        {#each locations as l (l.id)}
          <label class="flex items-center gap-1.5 text-sm">
            <input type="checkbox" checked={chosen.has(l.id)} onchange={() => toggle(l.id)} />
            {l.name}
          </label>
        {/each}
      </div>
      <button class="btn btn-primary btn-xs mt-3" onclick={saveLocs}>save locations</button>
    </div>
  {/if}
</div>
