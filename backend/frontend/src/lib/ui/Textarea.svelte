<script lang="ts">
  import type { HTMLTextareaAttributes } from 'svelte/elements';

  let {
    value = $bindable(),
    mono = false,
    class: cls = '',
    ...rest
  }: HTMLTextareaAttributes & { mono?: boolean; class?: string } = $props();
</script>

<textarea bind:value class="field {mono ? 'mono' : ''} {cls}" {...rest}></textarea>
