<script lang="ts">
  let {
    options,
    value = $bindable(),
    label,
    onchange
  }: {
    options: { value: string; label: string; count?: number }[];
    value: string;
    label: string;
    onchange?: (v: string) => void;
  } = $props();
</script>

<div role="radiogroup" aria-label={label} class="inline-flex flex-wrap gap-1 p-1 rounded-r12 bg-elev border border-border">
  {#each options as o (o.value)}
    <button
      type="button"
      role="radio"
      aria-checked={value === o.value}
      class="min-h-[30px] px-3 rounded-[8px] text-[13px] font-medium transition-colors {value === o.value
        ? 'bg-surface text-text shadow-sm font-semibold'
        : 'text-text2 hover:text-text'}"
      onclick={() => {
        value = o.value;
        onchange?.(o.value);
      }}
    >
      {o.label}{#if o.count !== undefined}<span class="mono text-muted ml-1.5">{o.count}</span>{/if}
    </button>
  {/each}
</div>
