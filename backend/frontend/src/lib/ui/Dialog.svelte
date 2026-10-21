<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  // Modal dialog on the native <dialog> (focus trap, Esc, top layer for free).
  let {
    open = $bindable(false),
    title,
    description,
    width = 440,
    dismissable = true,
    onclose,
    children,
    footer
  }: {
    open?: boolean;
    title: string;
    description?: string;
    width?: number;
    dismissable?: boolean;
    onclose?: () => void;
    children?: Snippet;
    footer?: Snippet;
  } = $props();

  const uid = $props.id();
  let el = $state<HTMLDialogElement>();

  $effect(() => {
    if (!el) return;
    if (open && !el.open) el.showModal();
    else if (!open && el.open) el.close();
  });

  function closed() {
    if (open) open = false;
    onclose?.();
  }
</script>

<dialog
  bind:this={el}
  class="q-dialog"
  aria-labelledby="{uid}-t"
  aria-describedby={description ? `${uid}-d` : undefined}
  onclose={closed}
  oncancel={(e) => {
    if (!dismissable) e.preventDefault();
  }}
  onclick={(e) => {
    if (e.target === el && dismissable) open = false;
  }}
>
  {#if open}
    <div class="card flex flex-col gap-4 p-6" style="width: min({width}px, calc(100vw - 32px)); box-shadow: var(--shadow-pop);">
      <div class="flex items-start gap-3">
        <div class="flex-1 min-w-0">
          <h2 id="{uid}-t" class="m-0 text-lg font-semibold">{title}</h2>
          {#if description}<p id="{uid}-d" class="mt-1.5 text-text2 whitespace-pre-line">{description}</p>{/if}
        </div>
        {#if dismissable}
          <button type="button" class="btn btn-g btn-sm btn-icon -mr-2 -mt-1" aria-label="Close" onclick={() => (open = false)}>
            <Icon name="x" size={16} />
          </button>
        {/if}
      </div>
      {@render children?.()}
      {#if footer}<div class="flex flex-wrap justify-end gap-2">{@render footer()}</div>{/if}
    </div>
  {/if}
</dialog>
