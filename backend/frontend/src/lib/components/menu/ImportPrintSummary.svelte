<script lang="ts">
  import { plural, type MenuImportResult } from '$lib/api';
  import Icon from '$lib/ui/Icon.svelte';

  // "8 items will print on Grill" lines for a menu import (or its preview).
  let { result }: { result: MenuImportResult } = $props();
  const noPrinter = $derived(result.items_no_printer ?? 0);
</script>

{#if result.prints_on?.length || noPrinter}
  <ul class="m-0 p-0 list-none flex flex-col gap-1 text-[13px]" aria-label="Where the items will print">
    {#each result.prints_on ?? [] as p (p.printer)}
      <li class="flex items-center gap-2 text-text2">
        <Icon name="printer" size={14} class="text-muted shrink-0" />
        <span><b class="text-text">{plural(p.items, 'item')}</b> will print on <b class="text-text">{p.printer}</b></span>
      </li>
    {/each}
    {#if noPrinter}
      <li class="flex items-center gap-2 text-warn font-medium">
        <Icon name="alert" size={14} class="shrink-0" />
        <span>{plural(noPrinter, 'item')} {noPrinter === 1 ? 'has' : 'have'} no kitchen printer — {noPrinter === 1 ? 'it prints' : 'they print'} on every station</span>
      </li>
    {/if}
  </ul>
{/if}
