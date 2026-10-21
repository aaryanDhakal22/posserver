<script lang="ts">
  import { tick } from 'svelte';

  // Underline tabs (role=tablist). Arrow keys move between tabs. On a narrow
  // screen the strip scrolls sideways; a fade on the clipped edge shows there
  // are more tabs, and the selected tab is kept in view.
  let {
    tabs,
    value = $bindable(),
    label = 'Sections',
    idPrefix = 'tab',
    onchange
  }: {
    tabs: { id: string; label: string; badge?: string | number }[];
    value: string;
    label?: string;
    idPrefix?: string;
    onchange?: (id: string) => void;
  } = $props();

  let list: HTMLDivElement;
  let fadeL = $state(false);
  let fadeR = $state(false);

  function edges() {
    if (!list) return;
    fadeL = list.scrollLeft > 2;
    fadeR = list.scrollLeft + list.clientWidth < list.scrollWidth - 2;
  }
  function reveal() {
    const el = list?.querySelector<HTMLElement>('[aria-selected=true]');
    if (!el || list.scrollWidth <= list.clientWidth) return;
    const pad = 32;
    if (el.offsetLeft - pad < list.scrollLeft) list.scrollLeft = Math.max(0, el.offsetLeft - pad);
    else if (el.offsetLeft + el.offsetWidth + pad > list.scrollLeft + list.clientWidth)
      list.scrollLeft = el.offsetLeft + el.offsetWidth + pad - list.clientWidth;
    edges();
  }

  $effect(() => {
    if (!list) return;
    const ro = new ResizeObserver(edges);
    ro.observe(list);
    return () => ro.disconnect();
  });
  $effect(() => {
    void value;
    void tabs.length;
    tick().then(reveal);
  });

  function pick(id: string) {
    value = id;
    onchange?.(id);
  }
  function key(e: KeyboardEvent, i: number) {
    let j = -1;
    if (e.key === 'ArrowRight') j = (i + 1) % tabs.length;
    else if (e.key === 'ArrowLeft') j = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') j = 0;
    else if (e.key === 'End') j = tabs.length - 1;
    if (j < 0) return;
    e.preventDefault();
    pick(tabs[j].id);
    (list.querySelectorAll('[role=tab]')[j] as HTMLElement | undefined)?.focus();
  }
</script>

<div class="relative">
  <div bind:this={list} role="tablist" aria-label={label} class="tabs-strip flex overflow-x-auto border-b border-border" onscroll={edges}>
    {#each tabs as t, i (t.id)}
      <button
        type="button"
        role="tab"
        id="{idPrefix}-{t.id}"
        aria-selected={value === t.id}
        aria-controls="{idPrefix}-panel"
        tabindex={value === t.id ? 0 : -1}
        class="tab"
        onclick={() => pick(t.id)}
        onkeydown={(e) => key(e, i)}
      >
        {t.label}{#if t.badge !== undefined && t.badge !== '' && t.badge !== 0}<span class="badge b-warn ml-1.5">{t.badge}</span>{/if}
      </button>
    {/each}
  </div>
  {#if fadeL}<span class="fade left-0" style="background: linear-gradient(to left, transparent, var(--canvas))" aria-hidden="true"></span>{/if}
  {#if fadeR}<span class="fade right-0" style="background: linear-gradient(to right, transparent, var(--canvas))" aria-hidden="true"></span>{/if}
</div>

<style>
  .tabs-strip {
    scrollbar-width: none;
  }
  .tabs-strip::-webkit-scrollbar {
    display: none;
  }
  .fade {
    position: absolute;
    top: 0;
    bottom: 1px;
    width: 36px;
    pointer-events: none;
  }
</style>
