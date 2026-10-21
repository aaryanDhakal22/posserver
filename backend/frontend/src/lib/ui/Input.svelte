<script lang="ts">
  import type { HTMLInputAttributes } from 'svelte/elements';

  let {
    value = $bindable(),
    mono = false,
    invalid = false,
    class: cls = '',
    ...rest
  }: HTMLInputAttributes & { mono?: boolean; invalid?: boolean; class?: string } = $props();
</script>

<input bind:value class="field {mono ? 'mono' : ''} {cls}" aria-invalid={invalid || undefined} {...rest} />
