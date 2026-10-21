<script lang="ts">
  import Icon from './Icon.svelte';

  let {
    value = $bindable(''),
    label,
    placeholder,
    id,
    class: cls = ''
  }: { value?: string; label: string; placeholder?: string; id: string; class?: string } = $props();
</script>

<div class="relative {cls}">
  <span class="absolute left-[11px] top-[11px] text-muted pointer-events-none"><Icon name="search" size={16} /></span>
  <label for={id} class="sr-only">{label}</label>
  <input {id} type="search" class="field !pl-[34px]" {placeholder} bind:value autocomplete="off" />
</div>
