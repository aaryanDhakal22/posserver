<script lang="ts">
  import type { Snippet } from 'svelte';

  // A labelled form row: <Field label="Phone" id="ph"><Input id="ph" …/></Field>
  let {
    label,
    id,
    help,
    error,
    class: cls = '',
    aside,
    children
  }: {
    label: string;
    id?: string;
    help?: string;
    error?: string;
    class?: string;
    /** Something to the right of the label (e.g. a small button). */
    aside?: Snippet;
    children: Snippet;
  } = $props();
</script>

<div class={cls}>
  {#if aside}
    <div class="flex items-center justify-between gap-2 mb-1.5">
      <label class="lbl !mb-0" for={id}>{label}</label>
      {@render aside()}
    </div>
  {:else}
    <label class="lbl" for={id}>{label}</label>
  {/if}
  {@render children()}
  {#if error}
    <p class="field-error" id={id ? id + '-err' : undefined} role="alert">{error}</p>
  {:else if help}
    <p class="help" id={id ? id + '-help' : undefined}>{help}</p>
  {/if}
</div>
