<script lang="ts" module>
  import type { IconName } from './Icon.svelte';

  export interface NavItem {
    href: string;
    label: string;
    icon: IconName;
    /** Path prefix that marks this item current (defaults to exact href). */
    match?: string;
    badge?: string | number;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { page } from '$app/state';
  import { afterNavigate } from '$app/navigation';
  import { themeState, toggleTheme } from '$lib/theme.svelte';
  import Icon from './Icon.svelte';
  import Brand from './Brand.svelte';

  // Sidebar app frame: 220px left sidebar from `md` up; below that a top bar
  // whose menu button opens the same nav.
  let {
    title,
    subtitle,
    nav,
    onsignout,
    banner,
    children
  }: {
    title: string;
    subtitle: string;
    nav: NavItem[];
    onsignout: () => void;
    banner?: Snippet;
    children: Snippet;
  } = $props();

  let mobileOpen = $state(false);
  afterNavigate(() => (mobileOpen = false));

  function current(n: NavItem): boolean {
    const p = page.url.pathname.replace(/\/$/, '');
    if (n.match) return p === n.match || p.startsWith(n.match + '/');
    return p === n.href;
  }
</script>

{#snippet brand()}
  <div class="flex items-center gap-2.5 px-1.5 min-w-0">
    <Brand size={32} />
    <div class="min-w-0">
      <div class="font-bold tracking-[-0.01em] truncate" title={title}>{title}</div>
      <div class="text-xs text-muted truncate">{subtitle}</div>
    </div>
  </div>
{/snippet}

{#snippet navList()}
  <nav aria-label="Main" class="flex flex-col gap-0.5">
    {#each nav as n (n.href)}
      <a class="nav" href={n.href} aria-current={current(n) ? 'page' : undefined}>
        <Icon name={n.icon} size={18} />{n.label}
        {#if n.badge !== undefined && n.badge !== '' && n.badge !== 0}<span class="badge b-warn ml-auto">{n.badge}</span>{/if}
      </a>
    {/each}
  </nav>
{/snippet}

{#snippet footer()}
  <div class="flex flex-col gap-0.5">
    <button type="button" class="nav" onclick={toggleTheme}>
      <Icon name={themeState.dark ? 'sun' : 'moon'} size={18} />{themeState.dark ? 'Light' : 'Dark'} mode
    </button>
    <button type="button" class="nav" onclick={onsignout}>
      <Icon name="logout" size={18} />Sign out
    </button>
  </div>
{/snippet}

<a href="#main" class="sr-only-focusable fixed top-2 left-2 z-[70] btn btn-p">Skip to content</a>
<div class="min-h-screen flex flex-col">
  {@render banner?.()}
  <div class="flex-1 flex flex-col md:flex-row min-h-0">
    <!-- phone / tablet: top bar -->
    <header class="md:hidden sticky top-0 z-30 bg-surface border-b border-border">
      <div class="flex items-center gap-3 px-4 py-2.5">
        <div class="flex-1 min-w-0">{@render brand()}</div>
        <button
          type="button"
          class="btn btn-sm btn-icon"
          aria-label={mobileOpen ? 'Close menu' : 'Open menu'}
          aria-expanded={mobileOpen}
          aria-controls="mobile-nav"
          onclick={() => (mobileOpen = !mobileOpen)}
        >
          <Icon name={mobileOpen ? 'x' : 'hamburger'} size={18} />
        </button>
      </div>
      {#if mobileOpen}
        <div id="mobile-nav" class="px-3 pb-3 flex flex-col gap-3 border-t border-border pt-3">
          {@render navList()}
          <div class="border-t border-border pt-2">{@render footer()}</div>
        </div>
      {/if}
    </header>

    <!-- desktop: sidebar -->
    <aside class="hidden md:flex w-[220px] shrink-0 flex-col gap-5 px-3.5 py-5 border-r border-border bg-surface sticky top-0 h-[100dvh] overflow-y-auto">
      {@render brand()}
      {@render navList()}
      <div class="mt-auto">{@render footer()}</div>
    </aside>

    <main id="main" class="flex-1 min-w-0 px-4 py-6 sm:px-6 md:px-8 md:py-7 pb-12 flex flex-col gap-6 [&>*]:min-w-0">
      {@render children()}
    </main>
  </div>
</div>
