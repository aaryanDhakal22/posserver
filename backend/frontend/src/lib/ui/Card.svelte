<script lang="ts">
  import type { Snippet } from 'svelte';

  // A surface. With `title` it gets a header row (title + optional actions).
  let {
    title,
    subtitle,
    actions,
    padded = true,
    class: cls = '',
    children
  }: {
    title?: string;
    subtitle?: string;
    actions?: Snippet;
    padded?: boolean;
    class?: string;
    children?: Snippet;
  } = $props();
</script>

<section class="card {padded && !title ? 'p-5' : ''} {cls}">
  {#if title}
    <div class="flex flex-wrap items-center justify-between gap-3 px-5 pt-4 {padded ? 'pb-1' : 'pb-3'}">
      <div class="min-w-0">
        <h2 class="text-[15px] font-semibold m-0">{title}</h2>
        {#if subtitle}<p class="text-[13px] text-muted mt-0.5">{subtitle}</p>{/if}
      </div>
      {#if actions}<div class="flex flex-wrap items-center gap-2">{@render actions()}</div>{/if}
    </div>
    <div class={padded ? 'px-5 pb-5 pt-3' : ''}>{@render children?.()}</div>
  {:else}
    {@render children?.()}
  {/if}
</section>
