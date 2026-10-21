<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  // Side panel (native modal <dialog> pinned right; full width on phones).
  // `beforeClose` guards the person's close requests (✕, backdrop, Esc):
  // return false (or a promise of false) to keep the panel open — e.g. to ask
  // "Discard changes?". Setting `open = false` from code is never guarded.
  let {
    open = $bindable(false),
    title,
    eyebrow,
    width = 460,
    onclose,
    beforeClose,
    children,
    footer
  }: {
    open?: boolean;
    title: string;
    eyebrow?: string;
    width?: number;
    onclose?: () => void;
    beforeClose?: () => boolean | Promise<boolean>;
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
    // The browser closed a guarded panel by itself (Chrome skips a cancelable
    // 'cancel' after a repeated Esc, or closes it together with a dialog opened
    // on top without a click). Its contents stay mounted while `open` is true:
    // ask now (or let the pending question decide) and re-open if kept.
    if (open && beforeClose && el?.isConnected) {
      forced = true;
      requestClose();
      return;
    }
    if (open) open = false;
    onclose?.();
  }

  let asking = false;
  let forced = false;
  async function requestClose() {
    if (asking) return;
    if (beforeClose) {
      asking = true;
      let ok = false;
      try {
        ok = await beforeClose();
      } finally {
        asking = false;
      }
      if (!ok) {
        if (forced && el && !el.open) el.showModal();
        forced = false;
        return;
      }
    }
    forced = false;
    if (el && !el.open) {
      // already closed by the browser: no 'close' event will follow
      if (open) {
        open = false;
        onclose?.();
      }
    } else open = false;
  }

  function onWindowKey(e: KeyboardEvent) {
    // Cancelling the Esc keydown stops the browser's own close request, which
    // can't always be cancelled later. Esc inside another dialog (the
    // "Discard changes?" one) is that dialog's.
    if (e.key !== 'Escape' || !beforeClose || !open || !el?.open || e.defaultPrevented) return;
    const d = (e.target as Element | null)?.closest?.('dialog');
    if (d && d !== el) return;
    e.preventDefault();
    requestClose();
  }
</script>

<svelte:window onkeydown={onWindowKey} />

<dialog
  bind:this={el}
  class="q-drawer"
  style="width: min({width}px, 100vw)"
  aria-labelledby="{uid}-t"
  onclose={closed}
  oncancel={(e) => {
    if (!beforeClose) return;
    e.preventDefault();
    requestClose();
  }}
  onclick={(e) => {
    if (e.target === el) requestClose();
  }}
>
  {#if open}
    <div class="flex flex-col h-full">
      <div class="flex items-start gap-3 px-5 pt-5 pb-3 border-b border-border">
        <div class="flex-1 min-w-0">
          {#if eyebrow}<div class="text-xs text-muted">{eyebrow}</div>{/if}
          <h2 id="{uid}-t" class="m-0 mt-0.5 text-lg font-semibold break-words">{title}</h2>
        </div>
        <button type="button" class="btn btn-g btn-sm btn-icon -mr-1" aria-label="Close panel" onclick={requestClose}>
          <Icon name="x" size={16} />
        </button>
      </div>
      <div class="flex-1 overflow-y-auto px-5 py-4">{@render children?.()}</div>
      {#if footer}<div class="flex flex-wrap gap-2 px-5 py-4 border-t border-border">{@render footer()}</div>{/if}
    </div>
  {/if}
</dialog>
