<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { goto } from '$app/navigation';
  import { searchSettings, type Hit } from '$lib/settings/map';
  import {
    storeApi,
    type HoursException,
    type Location,
    type OrderingSettings,
    type PricingBody,
    type Printer,
    type SquareStatus,
    type Staff,
    type TerminalSettings
  } from '$lib/api';
  import { todayHours } from '$lib/hours';
  import { devicesFromConfig, splitDevices } from '$lib/alerts';
  import { feesSummary, taxSummary } from '$lib/store/pricingForm';
  import { terminalSummary, type TerminalGroup } from '$lib/store/terminalSettings';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Icon, { type IconName } from '$lib/ui/Icon.svelte';

  // Store › Settings: a calm grid of categories. Each tile says what it holds right now in one line and
  // opens its own short page with one Save. Nothing is edited here.
  let loc = $state<Location | null>(null);
  let ordering = $state<{ settings: OrderingSettings; exceptions: HoursException[] } | null>(null);
  let pricing = $state<PricingBody | null>(null);
  let term = $state<TerminalSettings | null>(null);
  let staff = $state<Staff[] | null>(null);
  let printers = $state<Printer[] | null>(null);
  let square = $state<SquareStatus | null>(null);
  let email = $state<string | null>(null);

  onMount(() => {
    const quiet = <T,>(p: Promise<T>, set: (v: T) => void) => p.then(set).catch(() => {});
    quiet(storeApi<Location>('/me/location'), (v) => (loc = v));
    quiet(storeApi<typeof ordering>('/me/ordering'), (v) => (ordering = v));
    quiet(storeApi<PricingBody>('/me/pricing'), (v) => (pricing = v));
    quiet(storeApi<TerminalSettings>('/me/terminal/settings'), (v) => (term = v));
    quiet(storeApi<Staff[]>('/me/staff'), (v) => (staff = v));
    quiet(storeApi<Printer[]>('/me/printers'), (v) => (printers = v));
    quiet(storeApi<SquareStatus>('/me/square'), (v) => (square = v));
    quiet(storeApi<{ reply_email: string }>('/me/site/contact'), (v) => (email = v.reply_email));
  });

  // Search: any page or setting by its name or a word people use for it ("radius", "tip", "ticket"),
  // from the settings map; picking one opens its page with that setting lit up.
  let query = $state('');
  let active = $state(0);
  const hits = $derived(searchSettings(query));
  function open(h: Hit | undefined) {
    if (h) goto(h.href);
  }
  function searchKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' && hits.length) {
      e.preventDefault();
      active = (active + 1) % hits.length;
    } else if (e.key === 'ArrowUp' && hits.length) {
      e.preventDefault();
      active = (active - 1 + hits.length) % hits.length;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      open(hits[active]);
    } else if (e.key === 'Escape') {
      query = '';
    }
  }

  type Tile = { slug: string; title: string; icon: IconName; href?: string; summary: () => string | null };
  type Group = { title: string; blurb: string; tiles: Tile[] };

  const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`;
  const t = (g: TerminalGroup) => () => (term ? terminalSummary(g, term) : null);
  const upcoming = (x: HoursException[]) => x.filter((e) => e.date >= new Date().toISOString().slice(0, 10)).length;

  const groups: Group[] = [
    {
      title: 'Your store',
      blurb: 'Who you are, when you’re open, where you are.',
      tiles: [
        { slug: 'details', title: 'Store details', icon: 'store',
          summary: () => (loc ? [loc.name, loc.phone, loc.address_line1].filter(Boolean).join(' · ') + (email === '' ? ' · no store email' : '') : null) },
        { slug: 'hours', title: 'Hours', icon: 'clock',
          summary: () => {
            if (!loc) return null;
            const today = todayHours(loc.config);
            const sp = ordering ? upcoming(ordering.exceptions) : 0;
            return `${today === '—' ? 'No hours set' : `Today ${today === 'Closed' ? 'closed' : today}`}${sp ? ` · ${plural(sp, 'special day')}` : ''}`;
          } },
        { slug: 'map', title: 'Map and delivery area', icon: 'map-pin',
          summary: () => {
            if (!loc) return null;
            const mi = (loc.delivery_radius_m / 1609.344).toFixed(1).replace(/\.0$/, '');
            const pin = loc.geo_source === 'manual' ? 'Pin placed by hand' : loc.geo_source === 'geocoded' ? 'Pin from the address' : 'Pin not placed yet';
            return `${pin} · delivers ${mi} mi`;
          } },
        { slug: 'alerts', title: 'Alerts', icon: 'bell',
          summary: () => {
            if (!loc) return null;
            const n = splitDevices(devicesFromConfig(loc.config)).length;
            return n ? `${plural(n, 'phone')} get floor problems` : 'No phones yet';
          } }
      ]
    },
    {
      title: 'Ordering and money',
      blurb: 'What customers can order and what they pay.',
      tiles: [
        { slug: 'ordering', title: 'Ordering', icon: 'bag',
          summary: () => {
            if (!ordering) return null;
            const s = ordering.settings;
            const parts = [
              s.pickup_enabled ? `Pickup ${s.pickup_eta_minutes} min` : 'No pickup',
              s.delivery_enabled ? `delivery ${s.delivery_eta_minutes} min` : 'no delivery',
              s.schedule_days ? `order ahead ${plural(s.schedule_days, 'day')}` : 'ASAP only'
            ];
            return parts.join(' · ');
          } },
        { slug: 'tax', title: 'Tax and deals', icon: 'percent', summary: () => (pricing ? taxSummary(pricing) : null) },
        { slug: 'fees', title: 'Fees and tips', icon: 'truck', summary: () => (pricing ? feesSummary(pricing) : null) },
        { slug: 'payments', title: 'Payments', icon: 'card', href: base + '/store/payments',
          summary: () => (square ? (square.connected ? 'Square connected' : 'Square not connected') + (square.holds_open ? ` · ${plural(square.holds_open, 'card hold')} open` : '') : null) }
      ]
    },
    {
      title: 'Counter terminal',
      blurb: 'How the counter terminals behave. Changes reach the terminals through the Pi.',
      tiles: [
        { slug: 'cashier', title: 'Cashier limits', icon: 'shield', summary: t('cashier') },
        { slug: 'counter', title: 'Counter orders', icon: 'receipt', summary: t('counter') },
        { slug: 'screen', title: 'Login and screen', icon: 'monitor', summary: t('screen') },
        { slug: 'card', title: 'Card payments', icon: 'card', summary: t('card') },
        { slug: 'voids', title: 'Void and waste reasons', icon: 'ban', summary: t('voids') },
        { slug: 'end-of-day', title: 'End of day', icon: 'moon', summary: t('endofday') }
      ]
    },
    {
      title: 'People and devices',
      blurb: 'Who works here and what is plugged in.',
      tiles: [
        { slug: 'staff', title: 'Staff', icon: 'users', href: base + '/store/staff',
          summary: () => (staff ? (staff.length ? `${plural(staff.filter((p) => p.active).length, 'person', 'people')} can log in` : 'Nobody yet') : null) },
        { slug: 'callcenter', title: 'Call center', icon: 'phone',
          summary: () => {
            if (!staff) return null;
            const n = staff.filter((p) => p.role === 'callcenter' && p.active).length;
            return `${plural(n, 'agent')}${term ? ` · locks after ${term.callcenter_idle_lock_minutes ?? 15} min` : ''}`;
          } },
        { slug: 'receipts', title: 'Receipts and printing', icon: 'receipt', summary: t('receipts') },
        { slug: 'printers', title: 'Printers', icon: 'printer', href: base + '/store/printers',
          summary: () => (printers ? (printers.length ? plural(printers.length, 'printer') : 'No printers yet') : null) },
        { slug: 'devices', title: 'Devices', icon: 'tablet', href: base + '/store/devices', summary: () => 'The Pi, tablets and terminals' },
        { slug: 'website', title: 'Website', icon: 'layers', href: base + '/store/website', summary: () => 'Your ordering website: look, pages, domain' }
      ]
    }
  ];
</script>

<svelte:head><title>Settings — quicc</title></svelte:head>

<PageHeader title="Settings" subtitle="Pick what you want to change. Each one opens on its own page." />

<div class="flex flex-col gap-8 max-w-[1100px]">
  <div class="search" role="search">
    <label class="sr-only" for="settings-search">Search settings</label>
    <span class="search-ico" aria-hidden="true"><Icon name="search" size={18} /></span>
    <input
      id="settings-search"
      class="field"
      type="search"
      placeholder="Search settings: radius, tip, ticket numbers, printer…"
      autocomplete="off"
      spellcheck="false"
      role="combobox"
      aria-expanded={hits.length > 0}
      aria-controls="settings-search-list"
      aria-activedescendant={hits.length ? `ss-hit-${active}` : undefined}
      bind:value={query}
      oninput={() => (active = 0)}
      onkeydown={searchKey}
      data-testid="settings-search"
    />
    {#if query.trim()}
      <ul class="hits" id="settings-search-list" role="listbox" aria-label="Settings found" data-testid="settings-search-hits">
        {#each hits as h, i (h.href)}
          <li id="ss-hit-{i}" role="option" aria-selected={i === active}>
            <a class="hit" class:on={i === active} href={h.href} onmouseenter={() => (active = i)}>
              <span class="hit-title">{h.field ? h.field.label : h.page.title}</span>
              <span class="hit-crumb">{h.field ? h.page.crumb : h.page.group}</span>
            </a>
          </li>
        {:else}
          <li class="hit-none">Nothing called that. Try another word.</li>
        {/each}
      </ul>
    {/if}
  </div>
  {#each groups as g (g.title)}
    <section aria-labelledby="sg-{g.title.replace(/\W+/g, '-')}">
      <h2 class="grp" id="sg-{g.title.replace(/\W+/g, '-')}">{g.title}</h2>
      <p class="grp-blurb">{g.blurb}</p>
      <ul class="tiles">
        {#each g.tiles as tile (tile.slug)}
          {@const s = tile.summary()}
          <li>
            <a class="tile" href={tile.href ?? `${base}/store/settings/${tile.slug}`} data-testid="tile-{tile.slug}">
              <span class="ico"><Icon name={tile.icon} size={20} /></span>
              <span class="min-w-0 flex-1">
                <span class="tile-title">{tile.title}</span>
                <span class="tile-sum">{s ?? '…'}</span>
              </span>
              <Icon name="chevron-right" size={18} class="text-muted shrink-0" />
            </a>
          </li>
        {/each}
      </ul>
    </section>
  {/each}
</div>

<style>
  .search {
    position: relative;
    max-width: 560px;
  }
  .search .field {
    padding-left: 38px;
  }
  .search-ico {
    position: absolute;
    left: 12px;
    top: 11px;
    color: var(--muted);
    pointer-events: none;
  }
  .hits {
    position: absolute;
    z-index: 20;
    left: 0;
    right: 0;
    top: calc(100% + 6px);
    margin: 0;
    padding: 6px;
    list-style: none;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
    box-shadow: var(--shadow-pop, var(--shadow));
    max-height: min(60vh, 440px);
    overflow-y: auto;
  }
  .hit {
    display: flex;
    flex-direction: column;
    padding: 8px 10px;
    border-radius: 8px;
    color: var(--text);
    text-decoration: none;
  }
  .hit.on {
    background: var(--elev);
  }
  .hit-title {
    font-weight: 600;
    font-size: 14px;
  }
  .hit-crumb {
    font-size: 12px;
    color: var(--muted);
  }
  .hit-none {
    padding: 10px;
    font-size: 13px;
    color: var(--muted);
  }
  .grp {
    font-size: 13px;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text2);
    margin: 0;
  }
  .grp-blurb {
    margin: 2px 0 12px;
    font-size: 13px;
    color: var(--muted);
  }
  .tiles {
    display: grid;
    gap: 12px;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 300px), 1fr));
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .tile {
    display: flex;
    align-items: center;
    gap: 14px;
    height: 100%;
    padding: 16px 16px 16px 18px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 14px;
    color: var(--text);
    text-decoration: none;
    transition:
      border-color 0.12s,
      box-shadow 0.12s;
  }
  .tile:hover {
    border-color: var(--brand);
    box-shadow: var(--shadow);
    color: var(--text);
  }
  .tile:focus-visible {
    outline: 2px solid var(--brand);
    outline-offset: 2px;
  }
  .ico {
    width: 40px;
    height: 40px;
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 12px;
    background: var(--elev);
    color: var(--brand);
  }
  .tile-title {
    display: block;
    font-weight: 600;
    font-size: 15px;
  }
  .tile-sum {
    display: -webkit-box;
    margin-top: 2px;
    font-size: 13px;
    color: var(--muted);
    overflow: hidden;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }
</style>
