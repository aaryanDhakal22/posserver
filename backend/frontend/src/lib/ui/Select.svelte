<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLSelectAttributes } from 'svelte/elements';

  let {
    value = $bindable(),
    class: cls = '',
    children,
    ...rest
  }: HTMLSelectAttributes & { class?: string; children: Snippet } = $props();
</script>

<select bind:value class="field {cls}" {...rest}>
  {@render children()}
</select>
