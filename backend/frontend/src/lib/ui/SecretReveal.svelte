<script lang="ts">
  import type { Snippet } from 'svelte';
  import { copyText } from '$lib/clipboard';
  import Icon from './Icon.svelte';
  import Button from './Button.svelte';

  // A one-time secret (device key / agent token): shown once, copy it now.
  let {
    title,
    secret,
    ondismiss,
    children
  }: { title: string; secret: string; ondismiss: () => void; children?: Snippet } = $props();
</script>

<div class="rounded-r14 border px-5 py-4 flex flex-col gap-3" style="border-color: var(--warn); background: var(--warn-bg);" role="region" aria-label={title}>
  <div class="flex items-center gap-2 font-semibold text-warn"><Icon name="key" size={18} />{title}</div>
  <div class="mono text-[13px] break-all rounded-r10 px-3 py-2 bg-surface border border-border select-all">{secret}</div>
  {#if children}<div class="text-[13px] text-text2">{@render children()}</div>{/if}
  <div class="flex flex-wrap gap-2">
    <Button variant="primary" size="sm" icon="copy" onclick={() => copyText(secret, 'Key copied')}>Copy key</Button>
    <Button size="sm" onclick={ondismiss}>I've saved it</Button>
  </div>
</div>
