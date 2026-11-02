<script lang="ts">
  import { onMount } from 'svelte';
  import { adminApi, type ChannelAdmin, type ChannelKind, type Overview, type Location } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import ChannelRow from '$lib/components/ChannelRow.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SecretReveal from '$lib/ui/SecretReveal.svelte';

  // Sales channels (main 3.0): every way an order reaches a store — the web
  // storefront, the call center, a counter terminal, a vendor feed — has its
  // own key for the /channel/v1 API. Admin only.
  const KINDS: { value: ChannelKind; label: string }[] = [
    { value: 'web', label: 'Web storefront' },
    { value: 'operator', label: 'Call center' },
    { value: 'counter', label: 'Counter terminal' },
    { value: 'vendor', label: 'Vendor feed' }
  ];

  let channels = $state<ChannelAdmin[]>([]);
  let locations = $state<Location[]>([]);
  let loading = $state(true);
  let showNew = $state(false);
  let creating = $state(false);
  let nw = $state<{ location_id: string; kind: ChannelKind; name: string }>({ location_id: '', kind: 'web', name: '' });
  let formErr = $state('');
  let shown = $state<{ title: string; key: string } | null>(null);

  const byStore = $derived(
    locations
      .map((l) => ({ loc: l, list: channels.filter((c) => c.location_id === l.id) }))
      .filter((g) => g.list.length)
  );

  async function reload() {
    try {
      const [chs, ov] = await Promise.all([adminApi<ChannelAdmin[]>('/channels'), adminApi<Overview>('/overview')]);
      channels = chs;
      locations = ov.locations;
      if (!nw.location_id && locations.length) nw.location_id = locations[0].id;
    } catch (e) {
      toast.error(e);
    } finally {
      loading = false;
    }
  }

  function reveal(title: string, key: string) {
    shown = { title, key };
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    formErr = '';
    if (!nw.location_id || !nw.name.trim()) return (formErr = 'Choose a store and name the channel.');
    creating = true;
    try {
      const ch = await adminApi<ChannelAdmin>('/channels', { method: 'POST', body: JSON.stringify(nw) });
      reveal(`Key for “${ch.name}” — shown once`, ch.key ?? '');
      nw = { ...nw, name: '' };
      showNew = false;
      reload();
    } catch (err) {
      formErr = err instanceof Error ? err.message : String(err);
    } finally {
      creating = false;
    }
  }

  onMount(reload);
</script>

<svelte:head><title>Channels — quicc admin</title></svelte:head>

<PageHeader title="Channels" subtitle="Every way an order reaches a store — web, call center, counter, vendor feed — with its own key for the channel API">
  {#snippet actions()}
    <Button variant="primary" icon="plus" aria-expanded={showNew} onclick={() => (showNew = !showNew)}>New channel</Button>
  {/snippet}
</PageHeader>

{#if shown}
  <SecretReveal title={shown.title} secret={shown.key} ondismiss={() => (shown = null)}>
    Give it to the channel's adapter as <span class="mono">Authorization: Bearer …</span>. It is never shown again; “New key” issues another and stops this one.
  </SecretReveal>
{/if}

{#if showNew}
  <form class="card p-5 flex flex-col gap-4" onsubmit={create}>
    <h2 class="m-0 text-[15px] font-semibold">New channel</h2>
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
      <div>
        <label class="lbl" for="ch-store">Store</label>
        <Select id="ch-store" bind:value={nw.location_id}>
          {#each locations as l (l.id)}<option value={l.id}>{l.name}</option>{/each}
        </Select>
      </div>
      <div>
        <label class="lbl" for="ch-kind">Kind</label>
        <Select id="ch-kind" bind:value={nw.kind}>
          {#each KINDS as k (k.value)}<option value={k.value}>{k.label}</option>{/each}
        </Select>
      </div>
      <div><label class="lbl" for="ch-name">Name</label><input id="ch-name" class="field" bind:value={nw.name} placeholder="Website" /></div>
    </div>
    {#if formErr}<p class="field-error !m-0" role="alert">{formErr}</p>{/if}
    <div class="flex flex-wrap items-center gap-3">
      <Button type="submit" variant="primary" loading={creating}>Create channel</Button>
      <Button variant="ghost" onclick={() => (showNew = false)}>Cancel</Button>
      <span class="text-[13px] text-muted">The key is shown once, right after creating.</span>
    </div>
  </form>
{/if}

{#if loading}
  <section class="card p-5"><Skeleton lines={3} /></section>
{:else if byStore.length}
  {#each byStore as g (g.loc.id)}
    <section class="card divide-y divide-border">
      <h2 class="m-0 px-[18px] py-3 text-[14px] font-semibold">{g.loc.name}</h2>
      {#each g.list as ch (ch.id)}
        <ChannelRow channel={ch} {reload} {reveal} />
      {/each}
    </section>
  {/each}
{:else}
  <section class="card">
    <EmptyState icon="key" title="No channels yet" body="A channel is how an ordering app plugs into a store. Create one to get its key." />
  </section>
{/if}
