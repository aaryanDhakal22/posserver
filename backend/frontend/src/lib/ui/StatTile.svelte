<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    label,
    value,
    suffix,
    sub,
    subTone = 'muted',
    loading = false,
    href
  }: {
    label: string;
    value: string | number;
    suffix?: string;
    sub?: string;
    subTone?: 'muted' | 'ok' | 'warn' | 'bad';
    loading?: boolean;
    href?: string;
  } = $props();
  const subCls = $derived({ muted: 'text-muted', ok: 'text-ok', warn: 'text-warn', bad: 'text-bad' }[subTone]);
</script>

{#snippet body()}
  <div class="eyebrow">{label}</div>
  {#if loading}
    <div class="h-9 w-16 mt-1.5 rounded-r10 bg-elev animate-pulse" aria-hidden="true"></div>
  {:else}
    <div class="mono text-[30px] font-bold mt-1.5 leading-tight">
      {value}{#if suffix}<span class="text-muted text-lg"> {suffix}</span>{/if}
    </div>
  {/if}
  {#if sub}<div class="text-[13px] {subCls} truncate">{sub}</div>{/if}
{/snippet}

{#if href}
  <a {href} class="card block px-[18px] py-4 no-underline !text-text hover:border-brand transition-colors">{@render body()}</a>
{:else}
  <div class="card px-[18px] py-4">{@render body()}</div>
{/if}
