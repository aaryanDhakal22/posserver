<script lang="ts" module>
  import type { IconName } from './Icon.svelte';

  export interface MenuItem {
    label: string;
    icon?: IconName;
    danger?: boolean;
    disabled?: boolean;
    onclick: () => void;
  }
</script>

<script lang="ts">
  import Icon from './Icon.svelte';

  // A "⋯" button with a small action menu (fixed-positioned so table/scroll
  // containers can't clip it). Esc / outside click closes; arrows move.
  let {
    items,
    label,
    icon = 'more',
    size = 'sm'
  }: { items: MenuItem[]; label: string; icon?: IconName; size?: 'xs' | 'sm' } = $props();

  let open = $state(false);
  let btn = $state<HTMLButtonElement>();
  let menu = $state<HTMLDivElement>();
  let pos = $state({ top: 0, left: 0 });

  function place() {
    if (!btn) return;
    const r = btn.getBoundingClientRect();
    const w = 220;
    pos = { top: r.bottom + 4, left: Math.max(8, Math.min(window.innerWidth - w - 8, r.right - w)) };
  }
  function toggle(e: MouseEvent) {
    e.stopPropagation();
    if (open) return close();
    place();
    open = true;
    queueMicrotask(() => (menu?.querySelector('button:not([disabled])') as HTMLElement | null)?.focus());
  }
  function close(refocus = true) {
    open = false;
    if (refocus) btn?.focus();
  }
  $effect(() => {
    if (!open) return;
    const outside = (e: MouseEvent) => {
      if (!menu?.contains(e.target as Node) && !btn?.contains(e.target as Node)) close(false);
    };
    const away = () => close(false);
    document.addEventListener('mousedown', outside);
    window.addEventListener('resize', away);
    window.addEventListener('scroll', away, true);
    return () => {
      document.removeEventListener('mousedown', outside);
      window.removeEventListener('resize', away);
      window.removeEventListener('scroll', away, true);
    };
  });
  function key(e: KeyboardEvent) {
    const btns = [...(menu?.querySelectorAll('button:not([disabled])') ?? [])] as HTMLElement[];
    const i = btns.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      btns[(i + 1) % btns.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      btns[(i - 1 + btns.length) % btns.length]?.focus();
    } else if (e.key === 'Tab') {
      close(false);
    }
  }
</script>

<button
  bind:this={btn}
  type="button"
  class="btn btn-g btn-icon {size === 'xs' ? 'btn-xs' : 'btn-sm'}"
  aria-label={label}
  aria-haspopup="menu"
  aria-expanded={open}
  onclick={toggle}
>
  <Icon name={icon} size={16} />
</button>
{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div
    bind:this={menu}
    role="menu"
    tabindex="-1"
    aria-label={label}
    class="card fixed z-50 w-[220px] p-1.5 flex flex-col"
    style="top:{pos.top}px;left:{pos.left}px;box-shadow:var(--shadow-pop)"
    onkeydown={key}
    onclick={(e) => e.stopPropagation()}
  >
    {#each items as it}
      <button
        type="button"
        role="menuitem"
        disabled={it.disabled}
        class="flex items-center gap-2.5 w-full text-left px-2.5 py-2 rounded-[8px] text-[13px] font-medium hover:bg-hover disabled:opacity-40 {it.danger
          ? 'text-bad'
          : 'text-text'}"
        onclick={() => {
          close();
          it.onclick();
        }}
      >
        {#if it.icon}<Icon name={it.icon} size={16} />{/if}{it.label}
      </button>
    {/each}
  </div>
{/if}
