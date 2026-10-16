<script lang="ts">
  import '../app.css';
  import { base } from '$app/paths';
  import { page } from '$app/stores';
  import { onMount } from 'svelte';
  import { initTheme, toggleTheme, isDark } from '$lib/theme';

  let { children } = $props();
  let dark = $state(true);

  onMount(() => {
    initTheme();
    dark = isDark();
  });

  function flip() {
    dark = toggleTheme();
  }

  const nav = [
    { href: base + '/admin', label: 'Admin' },
    { href: base + '/admin/operators', label: 'Operators' },
    { href: base + '/store', label: 'Store' },
    { href: base + '/store/menu', label: 'Menu' }
  ];
  const active = (href: string) => {
    const p = $page.url.pathname;
    const matches = nav.filter((n) => p === n.href || p.startsWith(n.href + '/'));
    if (matches.length === 0) return false;
    // longest matching prefix wins so /store/menu highlights Menu, not Store
    const best = matches.reduce((a, b) => (b.href.length > a.href.length ? b : a));
    return best.href === href;
  };
</script>

<header
  class="border-b bg-white border-slate-200 dark:bg-slate-900 dark:border-slate-800 sticky top-0 z-10"
>
  <div class="max-w-6xl mx-auto px-6 py-3 flex items-center justify-between gap-4">
    <div class="flex items-center gap-6">
      <a href="{base}/" class="text-lg font-semibold">quicc</a>
      <nav class="flex items-center gap-1">
        {#each nav as n}
          <a
            href={n.href}
            class="btn btn-xs {active(n.href) ? 'btn-dark' : 'btn-ghost'}">{n.label}</a
          >
        {/each}
      </nav>
    </div>
    <button class="btn btn-xs btn-ghost" onclick={flip} title="Toggle theme">
      {dark ? '☀ light' : '🌙 dark'}
    </button>
  </div>
</header>

<main class="max-w-6xl mx-auto px-6 py-6">
  {@render children()}
</main>
