<script lang="ts">
  // Loading placeholder: `lines` grey bars (or one block of `height`).
  let { lines = 3, height, class: cls = '' }: { lines?: number; height?: string; class?: string } = $props();
</script>

<div class="flex flex-col gap-2.5 {cls}" aria-busy="true" aria-label="Loading">
  {#if height}
    <div class="rounded-r12 bg-elev animate-pulse" style="height: {height}"></div>
  {:else}
    {#each Array(lines) as _, i}
      <div class="h-4 rounded-md bg-elev animate-pulse" style="width: {[92, 76, 84, 60, 70][i % 5]}%"></div>
    {/each}
  {/if}
</div>
