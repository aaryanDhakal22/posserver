<script lang="ts">
  import { onMount } from 'svelte';
  import { fmtTime, storeApi, timeAgo, type Device, type Minted } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import SecretReveal from '$lib/ui/SecretReveal.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';

  // This location's device keys. Managers mint mobile/kiosk/ingest keys; the
  // Pi's own key arrives when it links with the manager login.
  let devices = $state<Device[] | null>(null);
  let nd = $state({ name: '', kind: 'mobile' });
  let minting = $state(false);
  let minted = $state<Minted | null>(null);
  let nameErr = $state('');

  const KINDS = [
    { value: 'mobile', label: 'Mobile' },
    { value: 'kiosk', label: 'Kiosk' },
    { value: 'ingest', label: 'Ingest' }
  ];
  const kindLabel = (k: string) => ({ agent: 'Pi', mobile: 'Mobile', kiosk: 'Kiosk', ingest: 'Ingest' })[k] ?? k;

  async function load() {
    devices = await storeApi<Device[]>('/me/devices');
  }
  onMount(() => load().catch((e) => toast.error(e)));

  async function mint(e: SubmitEvent) {
    e.preventDefault();
    nameErr = '';
    if (!nd.name.trim()) return (nameErr = 'Give the device a name.');
    minting = true;
    try {
      minted = await storeApi<Minted>('/me/devices', { method: 'POST', body: JSON.stringify({ name: nd.name.trim(), kind: nd.kind }) });
      nd = { name: '', kind: 'mobile' };
      await load();
    } catch (err) {
      toast.error(err);
    } finally {
      minting = false;
    }
  }

  async function revoke(d: Device) {
    const ok = await confirmDialog({
      title: `Revoke “${d.name}”?`,
      body:
        d.kind === 'agent'
          ? "This is the Pi's key — orders stop reaching the store until the Pi is linked again."
          : 'Its key stops working immediately.',
      confirmLabel: 'Revoke key',
      danger: true
    });
    if (!ok) return;
    try {
      await storeApi('/me/devices/' + d.id + '/revoke', { method: 'POST' });
      await load();
      toast.success('Key revoked');
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<svelte:head><title>Devices — quicc</title></svelte:head>

<PageHeader title="Devices" subtitle="Keys that let devices talk to quicc for this store" />

{#if minted}
  <SecretReveal title="New device key — copy it now, it is never shown again" secret={minted.api_key} ondismiss={() => (minted = null)}>
    For <b>{minted.device.name}</b> ({kindLabel(minted.device.kind)}).
  </SecretReveal>
{/if}

<Card title="Device keys" padded={false}>
  {#if devices === null}
    <div class="px-5 pb-5"><Skeleton lines={3} /></div>
  {:else if devices.length}
    <div class="overflow-x-auto">
      <table class="tbl stack min-w-[600px]">
        <thead><tr><th>Name</th><th>Kind</th><th>Key</th><th>Last used</th><th><span class="sr-only">Actions</span></th></tr></thead>
        <tbody>
          {#each devices as d (d.id)}
            <tr>
              <td class="font-semibold"><span>{d.name}{#if d.revoked_at}<Badge tone="bad" class="ml-2">Revoked</Badge>{/if}</span></td>
              <td data-label="Kind">{kindLabel(d.kind)}</td>
              <td data-label="Key" class="mono">{d.key_prefix}…</td>
              <td data-label="Last used" title={fmtTime(d.last_used_at)}>{d.last_used_at ? timeAgo(d.last_used_at) : 'never'}</td>
              <td class="text-right stack-actions"><Button size="sm" disabled={!!d.revoked_at} onclick={() => revoke(d)}>Revoke</Button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <EmptyState icon="tablet" title="No devices yet" body="The Pi appears here once it's linked. Create a key below for anything else." />
  {/if}
</Card>

<Card title="New device key">
  <form class="flex flex-wrap items-end gap-3" onsubmit={mint}>
    <div class="flex-1 min-w-[200px]">
      <label class="lbl" for="dev-name">Name</label>
      <input id="dev-name" class="field" bind:value={nd.name} placeholder="Front tablet" aria-invalid={!!nameErr || undefined} />
      {#if nameErr}<p class="field-error" role="alert">{nameErr}</p>{/if}
    </div>
    <div>
      <span class="lbl">Kind</span>
      <SegmentedControl label="Device kind" options={KINDS} bind:value={nd.kind} />
    </div>
    <Button type="submit" variant="primary" icon="key" loading={minting}>Create key</Button>
  </form>
</Card>
