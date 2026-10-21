<script lang="ts">
  // The lavender "q" tile.
  let { size = 32 }: { size?: number } = $props();
</script>

<div
  class="flex items-center justify-center shrink-0 select-none"
  style="width:{size}px;height:{size}px;border-radius:{Math.round(size * 0.28)}px;background:#AC94F4;color:#241B47;font:800 {Math.round(size * 0.56)}px 'JetBrains Mono', monospace;"
  aria-hidden="true"
>
  q
</div>
