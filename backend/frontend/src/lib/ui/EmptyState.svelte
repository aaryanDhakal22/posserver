<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon, { type IconName } from './Icon.svelte';

  let {
    icon = 'info',
    title,
    body,
    action
  }: { icon?: IconName; title: string; body?: string; action?: Snippet } = $props();
</script>

<div class="flex flex-col items-center text-center gap-2 px-6 py-10">
  <span class="w-11 h-11 rounded-r12 bg-elev text-accent flex items-center justify-center"><Icon name={icon} size={20} /></span>
  <div class="font-semibold mt-1">{title}</div>
  {#if body}<p class="text-muted max-w-md">{body}</p>{/if}
  {#if action}<div class="mt-2 flex flex-wrap justify-center gap-2">{@render action()}</div>{/if}
</div>
