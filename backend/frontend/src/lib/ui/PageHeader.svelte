<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    subtitle,
    crumbs,
    badges,
    actions
  }: {
    title: string;
    subtitle?: string;
    /** Breadcrumb trail above the title: [{label, href?}] */
    crumbs?: { label: string; href?: string }[];
    badges?: Snippet;
    actions?: Snippet;
  } = $props();
</script>

<header class="flex flex-col gap-2">
  {#if crumbs?.length}
    <nav aria-label="Breadcrumb" class="text-[13px] text-muted">
      {#each crumbs as c, i}
        {#if i > 0}<span class="mx-1.5" aria-hidden="true">/</span>{/if}
        {#if c.href}<a href={c.href} class="inline-block py-[7px] -my-[7px] px-1 -mx-1 !text-muted no-underline hover:!text-text">{c.label}</a>{:else}<span aria-current="page">{c.label}</span>{/if}
      {/each}
    </nav>
  {/if}
  <div class="flex flex-wrap items-end justify-between gap-3">
    <div class="min-w-0">
      <h1 class="m-0 text-2xl font-bold tracking-[-0.01em] break-words">{title}</h1>
      {#if subtitle}<p class="mt-1 text-muted">{subtitle}</p>{/if}
      {#if badges}<div class="flex flex-wrap gap-2 mt-2">{@render badges()}</div>{/if}
    </div>
    {#if actions}<div class="flex flex-wrap items-center gap-2">{@render actions()}</div>{/if}
  </div>
</header>
