<script lang="ts">
  import { toasts } from '$lib/toast.svelte';
  import Icon from './Icon.svelte';

  // The toaster is a manual popover so it lives in the top layer: an open
  // modal Dialog/Drawer would otherwise cover it. Re-showing it on every new
  // toast moves it above a modal opened since.
  let el: HTMLDivElement;
  $effect(() => {
    if (toasts.items.length === 0 || !el?.showPopover) return;
    try {
      if (el.matches(':popover-open')) el.hidePopover();
      el.showPopover();
    } catch {
      // popover unsupported: stays a fixed element under any modal
    }
  });
</script>

<div
  bind:this={el}
  popover="manual"
  class="toaster fixed z-[60] bottom-4 right-4 left-4 sm:left-auto flex flex-col items-end gap-2 pointer-events-none"
  aria-live="polite"
>
  {#each toasts.items as t (t.id)}
    <div
      role={t.kind === 'error' ? 'alert' : 'status'}
      class="toast pointer-events-auto flex items-start gap-2.5 px-4 py-3 rounded-r12 w-full sm:w-auto sm:max-w-[440px]"
    >
      <span class="mt-px {t.kind === 'error' ? 'text-[#F87171]' : t.kind === 'info' ? 'text-[#AC94F4]' : 'text-[#4ADE80]'}">
        <Icon name={t.kind === 'error' ? 'alert' : t.kind === 'info' ? 'info' : 'check'} size={18} stroke={2.4} />
      </span>
      <span class="flex-1 min-w-0 break-words">{t.message}</span>
      {#if t.action}
        <button
          type="button"
          class="shrink-0 font-semibold text-[13px] text-[#AC94F4] hover:underline"
          onclick={async () => {
            const a = t.action;
            toasts.dismiss(t.id);
            await a?.run();
          }}>{t.action.label}</button
        >
      {/if}
      <button type="button" class="shrink-0 opacity-70 hover:opacity-100 -mr-1" aria-label="Dismiss" onclick={() => toasts.dismiss(t.id)}>
        <Icon name="x" size={16} />
      </button>
    </div>
  {/each}
</div>

<style>
  /* undo the UA popover box (centered, bordered, inset:0) */
  .toaster {
    inset: auto 1rem 1rem auto;
    margin: 0;
    padding: 0;
    border: 0;
    background: transparent;
    overflow: visible;
  }
  @media (max-width: 639px) {
    .toaster {
      left: 1rem;
    }
  }
  .toaster:not(:popover-open) {
    display: flex;
  }
  .toast {
    background: #1a1530;
    color: #f4f1fb;
    box-shadow: 0 12px 30px rgba(15, 13, 26, 0.3);
  }
  :global(html.dark) .toast {
    background: #2d2844;
    border: 1px solid #3a3456;
  }
</style>
