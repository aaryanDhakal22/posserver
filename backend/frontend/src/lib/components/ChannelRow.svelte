<script lang="ts">
  import { adminApi, type ChannelAdmin, type ChannelPermissions } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import MenuButton from '$lib/ui/MenuButton.svelte';
  import Switch from '$lib/ui/Switch.svelte';

  let {
    channel,
    reload,
    reveal
  }: { channel: ChannelAdmin; reload: () => void; reveal: (title: string, key: string) => void } = $props();

  const KIND: Record<string, string> = { web: 'Web', operator: 'Call center', counter: 'Counter', vendor: 'Vendor' };
  const PORTS: { key: 'menu' | 'quote' | 'orders' | 'track'; label: string }[] = [
    { key: 'menu', label: 'Read the menu' },
    { key: 'quote', label: 'Price carts' },
    { key: 'orders', label: 'Place orders' },
    { key: 'track', label: 'Follow orders' }
  ];

  let showEdit = $state(false);
  let name = $state('');
  let perms = $state<ChannelPermissions>({ menu: false, quote: false, orders: false, track: false, payments: [] });
  const uid = $props.id();

  function openEdit() {
    name = channel.name;
    perms = { ...channel.permissions, payments: [...channel.permissions.payments] };
    showEdit = !showEdit;
  }
  function togglePay(m: 'card' | 'cash', on: boolean) {
    perms.payments = on ? [...new Set([...perms.payments, m])] : perms.payments.filter((x) => x !== m);
  }

  async function save() {
    try {
      await adminApi(`/channels/${channel.id}`, { method: 'PATCH', body: JSON.stringify({ name, permissions: perms }) });
      toast.success('Channel updated');
      showEdit = false;
      reload();
    } catch (x) {
      toast.error(x);
    }
  }
  async function rotate() {
    const ok = await confirmDialog({
      title: `New key for “${channel.name}”?`,
      body: 'The current key stops working at once — update the adapter with the new one.',
      confirmLabel: 'Issue new key'
    });
    if (!ok) return;
    try {
      const ch = await adminApi<ChannelAdmin>(`/channels/${channel.id}/rotate`, { method: 'POST' });
      reveal(`New key for “${ch.name}” — shown once`, ch.key ?? '');
      reload();
    } catch (x) {
      toast.error(x);
    }
  }
  async function revoke() {
    const ok = await confirmDialog({
      title: `Revoke “${channel.name}”?`,
      body: 'Its key stops working immediately. “New key” brings the channel back.',
      confirmLabel: 'Revoke',
      danger: true
    });
    if (!ok) return;
    try {
      await adminApi(`/channels/${channel.id}/revoke`, { method: 'POST' });
      toast.success('Channel revoked');
      reload();
    } catch (x) {
      toast.error(x);
    }
  }
  async function del() {
    const ok = await confirmDialog({ title: `Delete channel “${channel.name}”?`, confirmLabel: 'Delete channel', danger: true });
    if (!ok) return;
    try {
      await adminApi(`/channels/${channel.id}`, { method: 'DELETE' });
      toast.success('Channel deleted');
      reload();
    } catch (x) {
      toast.error(x);
    }
  }

  const used = $derived(channel.last_used_at ? new Date(channel.last_used_at).toLocaleString() : 'never');
</script>

<div class="px-[18px] py-3.5">
  <div class="flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <div class="flex flex-wrap items-center gap-2">
        <span class="font-semibold">{channel.name}</span>
        <Badge tone="info">{KIND[channel.kind] ?? channel.kind}</Badge>
        {#if channel.builtin}<Badge tone="brand">built-in</Badge>{/if}
        {#if channel.active}<Badge tone="ok" dot>active</Badge>{:else if !channel.builtin}<Badge tone="bad" dot>revoked</Badge>{/if}
        {#if channel.key_prefix}<Badge mono>{channel.key_prefix}…</Badge>{/if}
      </div>
      <div class="mt-1.5 flex flex-wrap gap-1.5 items-center text-[13px] text-muted">
        {#each PORTS as p (p.key)}{#if channel.permissions[p.key]}<Badge>{p.label}</Badge>{/if}{/each}
        {#each channel.permissions.payments as m (m)}<Badge tone="mute">{m}</Badge>{/each}
        <span>· last used {used}</span>
      </div>
      {#if channel.builtin}
        <p class="m-0 mt-1 text-xs text-muted">
          {channel.kind === 'operator'
            ? 'Operators sign in to it (admin › Operators) and work this store; no key.'
            : 'Holds the orders this feed sends; no key.'}
        </p>
      {/if}
    </div>
    {#if channel.builtin}
      <div class="flex items-center gap-1.5">
        <Button size="sm" icon="pencil" onclick={openEdit} aria-expanded={showEdit}>Edit</Button>
      </div>
    {:else}
      <div class="flex items-center gap-1.5">
        <Button size="sm" icon="pencil" onclick={openEdit} aria-expanded={showEdit}>Edit</Button>
        <Button size="sm" icon="key" onclick={rotate}>New key</Button>
        <MenuButton
          label="More actions for {channel.name}"
          items={[
            { label: 'Revoke', icon: 'x', onclick: revoke },
            { label: 'Delete channel', icon: 'trash', danger: true, onclick: del }
          ]}
        />
      </div>
    {/if}
  </div>

  {#if showEdit}
    <form
      class="subcard px-4 py-3 mt-3 flex flex-col gap-3"
      onsubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <div class="max-w-[320px]">
        <label class="lbl" for="{uid}-name">Name</label>
        <input id="{uid}-name" class="field" bind:value={name} />
      </div>
      <div class="flex flex-wrap gap-x-6 gap-y-2">
        {#each PORTS as p (p.key)}
          <label class="flex items-center gap-2 text-[14px]"><Switch label={p.label} bind:checked={perms[p.key]} />{p.label}</label>
        {/each}
      </div>
      <div class="flex flex-wrap gap-x-6 gap-y-2 items-center">
        <span class="text-[13px] text-muted">Payments:</span>
        {#each ['card', 'cash'] as const as m (m)}
          <label class="flex items-center gap-2 text-[14px]">
            <Switch label="Take {m}" checked={perms.payments.includes(m)} onchange={(v) => togglePay(m, v)} />{m}
          </label>
        {/each}
      </div>
      <div class="flex gap-2">
        <Button type="submit" variant="primary" size="sm">Save</Button>
        <Button variant="ghost" size="sm" onclick={() => (showEdit = false)}>Cancel</Button>
      </div>
    </form>
  {/if}
</div>
