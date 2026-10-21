<script lang="ts">
  import { adminApi, type OperatorAdmin, type Location } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import MenuButton from '$lib/ui/MenuButton.svelte';

  let {
    operator,
    locations,
    reload
  }: {
    operator: OperatorAdmin;
    locations: Location[];
    reload: () => void;
  } = $props();

  let showEdit = $state(false);
  let showLocs = $state(false);
  let edit = $state({ display_name: '', password: '' });
  let chosen = $state<Set<string>>(new Set());
  const uid = $props.id();

  const locName = (id: string) => locations.find((l) => l.id === id)?.name ?? id.slice(0, 8);

  function openEdit() {
    edit = { display_name: operator.display_name, password: '' };
    showLocs = false;
    showEdit = !showEdit;
  }
  function openLocs() {
    chosen = new Set(operator.location_ids);
    showEdit = false;
    showLocs = !showLocs;
  }
  function toggle(id: string) {
    const next = new Set(chosen);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    chosen = next;
  }

  async function saveEdit() {
    try {
      await adminApi(`/operators/${operator.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ display_name: edit.display_name, password: edit.password })
      });
      toast.success('Operator updated');
      showEdit = false;
      reload();
    } catch (x) {
      toast.error(x);
    }
  }
  async function saveLocs() {
    try {
      await adminApi(`/operators/${operator.id}/locations`, {
        method: 'PUT',
        body: JSON.stringify({ location_ids: [...chosen] })
      });
      toast.success('Allowed locations updated');
      showLocs = false;
      reload();
    } catch (x) {
      toast.error(x);
    }
  }
  async function revoke() {
    const ok = await confirmDialog({
      title: `Sign out “${operator.display_name}” everywhere?`,
      body: 'Revokes all of their active tokens — any signed-in session stops working immediately.',
      confirmLabel: 'Revoke tokens',
      danger: true
    });
    if (!ok) return;
    try {
      await adminApi(`/operators/${operator.id}/tokens/revoke`, { method: 'POST' });
      toast.success('Tokens revoked');
    } catch (x) {
      toast.error(x);
    }
  }
  async function del() {
    const ok = await confirmDialog({ title: `Delete operator “${operator.display_name}”?`, confirmLabel: 'Delete operator', danger: true });
    if (!ok) return;
    try {
      await adminApi(`/operators/${operator.id}`, { method: 'DELETE' });
      toast.success('Operator deleted');
      reload();
    } catch (x) {
      toast.error(x);
    }
  }
</script>

<div class="px-[18px] py-3.5">
  <div class="flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <div class="flex flex-wrap items-center gap-2">
        <span class="font-semibold">{operator.display_name}</span>
        <Badge mono>@{operator.username}</Badge>
      </div>
      <div class="mt-1.5 flex flex-wrap gap-1.5 items-center">
        {#if operator.location_ids.length}
          {#each operator.location_ids as id}<Badge tone="info">{locName(id)}</Badge>{/each}
        {:else}
          <Badge tone="warn">no locations</Badge>
        {/if}
      </div>
    </div>
    <div class="flex items-center gap-1.5">
      <Button size="sm" icon="pencil" onclick={openEdit} aria-expanded={showEdit}>Edit</Button>
      <Button size="sm" icon="store" onclick={openLocs} aria-expanded={showLocs}>Locations</Button>
      <MenuButton
        label="More actions for {operator.display_name}"
        items={[
          { label: 'Revoke tokens', icon: 'key', onclick: revoke },
          { label: 'Delete operator', icon: 'trash', danger: true, onclick: del }
        ]}
      />
    </div>
  </div>

  {#if showEdit}
    <form
      class="subcard px-4 py-3 mt-3 flex flex-wrap gap-3 items-end"
      onsubmit={(e) => {
        e.preventDefault();
        saveEdit();
      }}
    >
      <div class="grow min-w-[180px]">
        <label class="lbl" for="{uid}-dn">Display name</label>
        <input id="{uid}-dn" class="field" bind:value={edit.display_name} />
      </div>
      <div class="min-w-[180px]">
        <label class="lbl" for="{uid}-pw">New password <span class="font-normal text-muted">(optional)</span></label>
        <input id="{uid}-pw" class="field" type="password" bind:value={edit.password} autocomplete="new-password" />
      </div>
      <Button type="submit" variant="primary">Save</Button>
    </form>
  {/if}

  {#if showLocs}
    <div class="subcard px-4 py-3 mt-3">
      <p class="m-0 mb-2 text-[13px] text-muted" id="{uid}-locs">Locations this operator can read the menu for.</p>
      <div class="flex flex-wrap gap-x-4 gap-y-2" role="group" aria-labelledby="{uid}-locs">
        {#each locations as l (l.id)}
          <label class="flex items-center gap-2">
            <input type="checkbox" class="check" checked={chosen.has(l.id)} onchange={() => toggle(l.id)} />
            {l.name}
          </label>
        {/each}
      </div>
      <Button variant="primary" size="sm" class="mt-3" onclick={saveLocs}>Save locations</Button>
    </div>
  {/if}
</div>
