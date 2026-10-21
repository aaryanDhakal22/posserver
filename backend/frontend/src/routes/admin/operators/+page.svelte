<script lang="ts">
  import { onMount } from 'svelte';
  import { adminApi, type OperatorAdmin, type Overview, type Location } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import OperatorRow from '$lib/components/OperatorRow.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';

  // Call-center order-takers: username + password → read-only menu token,
  // scoped to allowed locations.
  let operators = $state<OperatorAdmin[]>([]);
  let locations = $state<Location[]>([]);
  let loading = $state(true);
  let showNew = $state(false);
  let creating = $state(false);
  let nw = $state({ username: '', password: '', display_name: '' });
  let formErr = $state('');

  async function reload() {
    try {
      const [ops, ov] = await Promise.all([adminApi<OperatorAdmin[]>('/operators'), adminApi<Overview>('/overview')]);
      operators = ops;
      locations = ov.locations;
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
    }
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    formErr = '';
    if (!nw.username.trim() || !nw.password || !nw.display_name.trim())
      return (formErr = 'Display name, username and password are all required.');
    creating = true;
    try {
      await adminApi('/operators', { method: 'POST', body: JSON.stringify(nw) });
      nw = { username: '', password: '', display_name: '' };
      showNew = false;
      toast.success('Operator created — choose their locations next');
      reload();
    } catch (err) {
      formErr = err instanceof Error ? err.message : String(err);
    } finally {
      creating = false;
    }
  }

  onMount(reload);
</script>

<svelte:head><title>Operators — quicc admin</title></svelte:head>

<PageHeader title="Operators" subtitle="Call-center order-takers · username + password → a read-only menu token for their allowed locations">
  {#snippet actions()}
    <Button variant="primary" icon="plus" aria-expanded={showNew} onclick={() => (showNew = !showNew)}>New operator</Button>
  {/snippet}
</PageHeader>

{#if showNew}
  <form class="card p-5 flex flex-col gap-4" onsubmit={create}>
    <h2 class="m-0 text-[15px] font-semibold">New operator</h2>
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
      <div><label class="lbl" for="op-dn">Display name</label><input id="op-dn" class="field" bind:value={nw.display_name} placeholder="Jane Doe" /></div>
      <div><label class="lbl" for="op-un">Username</label><input id="op-un" class="field mono" bind:value={nw.username} autocomplete="off" autocapitalize="off" placeholder="jane" /></div>
      <div><label class="lbl" for="op-pw">Password</label><input id="op-pw" class="field" type="password" bind:value={nw.password} autocomplete="new-password" /></div>
    </div>
    {#if formErr}<p class="field-error !m-0" role="alert">{formErr}</p>{/if}
    <div class="flex flex-wrap items-center gap-3">
      <Button type="submit" variant="primary" loading={creating}>Create operator</Button>
      <Button variant="ghost" onclick={() => (showNew = false)}>Cancel</Button>
      <span class="text-[13px] text-muted">After creating, use “Locations” to grant menu access.</span>
    </div>
  </form>
{/if}

<section class="card divide-y divide-border">
  {#if loading}
    <div class="p-5"><Skeleton lines={3} /></div>
  {:else if operators.length}
    {#each operators as op (op.id)}
      <OperatorRow operator={op} {locations} {reload} />
    {/each}
  {:else}
    <EmptyState icon="users" title="No operators yet" body="Operators read the menu for the stores you allow — create one to get started." />
  {/if}
</section>
