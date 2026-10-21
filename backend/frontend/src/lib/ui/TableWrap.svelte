<script lang="ts">
  import type { Snippet } from 'svelte';

  // Horizontal scroll for a .tbl inside a card at narrow widths.
  let { minWidth = 640, children }: { minWidth?: number; children: Snippet } = $props();
</script>

<div class="overflow-x-auto">
  <table class="tbl" style="min-width: {minWidth}px">
    {@render children()}
  </table>
</div>
