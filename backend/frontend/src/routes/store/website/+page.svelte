<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { storeApi, type Website, type SiteLook, type SiteImage, type SiteDomain, type BlockId, type MisoSpot } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import { confirmDialog } from '$lib/confirm.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import ImageCrop from '$lib/components/store/ImageCrop.svelte';

  // Store › Website (owner, 2026-11-09): the store's website look, edited as a draft (saved as you
  // go) and previewed on the real storefront through a private link, then published. Editor on the
  // left, the live preview on the right. Logos and cover photos are framed in the browser first.

  type Texts = NonNullable<SiteLook['texts']>;
  type Doc = {
    preset: string;
    brand: string;
    menu_layout: '' | 'compact' | 'photo' | 'printed';
    spanish: boolean;
    logo: string;
    cover: string;
    texts: Required<Texts>;
    blocks: { id: BlockId; on: boolean }[];
    picks: string[];
    announcement: { on: boolean; text: string; text_es: string; from: string; to: string };
    miso_pages: boolean;
    miso_blocks: Partial<Record<BlockId, MisoSpot>>;
    privacy: string;
  };

  const PRESETS = [
    { id: 'pizzeria-loud', name: 'Pizzeria, loud', desc: 'Dark, condensed signage type (the Quibase look)', canvas: '#16100F', swatch: ['#16100F', '#2A1F1E'], layout: 'compact' },
    { id: 'cafe-calm', name: 'Café, calm', desc: 'Light, soft serif headings, side menu', canvas: '#F7F3EC', swatch: ['#F7F3EC', '#EBC79B'], layout: 'photo' },
    { id: 'taqueria-bold', name: 'Taquería, bold', desc: 'Deep blue, heavy type, bottom tab bar', canvas: '#1B1F4A', swatch: ['#1B1F4A', '#FF4D8D'], layout: 'printed' },
    { id: 'classic-paper', name: 'Classic paper', desc: 'White paper, serif, printed-menu feel', canvas: '#FFFFFF', swatch: ['#FFFFFF', '#E8E2D6'], layout: 'printed' }
  ];
  const LAYOUTS = [
    { id: 'compact', label: 'List' },
    { id: 'photo', label: 'Tiles' },
    { id: 'printed', label: 'Printed' }
  ] as const;
  const BLOCKS: Record<BlockId, { name: string; desc: string; edit?: boolean }> = {
    hero: { name: 'Hero', desc: 'Headline, intro and the order button', edit: true },
    again: { name: 'Order again', desc: 'Only signed-in customers with past orders see it' },
    categories: { name: 'Categories', desc: 'A button for each menu category' },
    deals: { name: 'Deals', desc: 'Shows by itself when you have active deals' },
    picks: { name: 'From the menu', desc: 'Up to 8 items you pick', edit: true },
    hours: { name: 'Hours & contact', desc: "The week's hours, address, phone, delivery area (from Details & hours)" },
    about: { name: 'About us', desc: 'Your story, with the cover photo', edit: true },
    install: { name: 'Install the app', desc: 'How to put your site on a phone home screen' }
  };
  const DEFAULT_BLOCKS: Doc['blocks'] = [
    { id: 'hero', on: true },
    { id: 'again', on: true },
    { id: 'categories', on: true },
    { id: 'deals', on: true },
    { id: 'picks', on: true },
    { id: 'hours', on: true },
    { id: 'about', on: false },
    { id: 'install', on: false }
  ];
  const TEXT_KEYS = ['eyebrow', 'headline', 'lead', 'about_title', 'about', 'eyebrow_es', 'headline_es', 'lead_es', 'about_title_es', 'about_es'] as const;
  const PAGES = [
    { id: '', label: 'Home' },
    { id: 'menu', label: 'Menu' },
    { id: 'item', label: 'An item' },
    { id: 'deals', label: 'Deals' },
    { id: 'cart', label: 'Cart' },
    { id: 'checkout', label: 'Checkout' },
    { id: 'privacy', label: 'Privacy' }
  ];
  const DEVICES = [
    { id: 'phone', label: 'Phone', w: 390, h: 760 },
    { id: 'tablet', label: 'Tablet', w: 834, h: 1000 },
    { id: 'desktop', label: 'Desktop', w: 1280, h: 860 }
  ];

  let site = $state<Website | null>(null);
  let doc = $state<Doc | null>(null);
  let saving = $state<'idle' | 'saving' | 'saved' | 'error'>('idle');
  let saveError = $state('');
  let busy = $state('');
  let open = $state<BlockId | ''>('');
  let items = $state<{ id: string; name: string; category: string }[]>([]);
  let domains = $state<SiteDomain[]>([]);
  let baseDomain = $state('quiccpos.com');
  let store = $state<{ name: string; phone: string; address: string; email: string }>({ name: '', phone: '', address: '', email: '' });
  let ownHost = $state('');
  let editingPrivacy = $state(false);
  let pageSel = $state('');
  let device = $state('phone');
  let frameKey = $state(0);
  let cropKind = $state<'logo' | 'cover'>('logo');
  let cropFile = $state<File | null>(null);
  let pickQuery = $state('');
  let dragging = $state<number | null>(null);

  function toDoc(l: SiteLook): Doc {
    const texts = Object.fromEntries(TEXT_KEYS.map((k) => [k, l.texts?.[k] ?? ''])) as Required<Texts>;
    const blocks = (l.blocks ?? []).filter((b) => BLOCKS[b.id]);
    for (const d of DEFAULT_BLOCKS) if (!blocks.some((b) => b.id === d.id)) blocks.push({ ...d });
    return {
      preset: l.preset || 'pizzeria-loud',
      brand: l.brand || '#E2363B',
      menu_layout: l.menu_layout || '',
      spanish: !l.languages || l.languages.includes('es'),
      logo: l.logo ?? '',
      cover: l.cover ?? '',
      texts,
      blocks,
      picks: l.picks ?? [],
      announcement: { on: false, text: '', text_es: '', from: '', to: '', ...(l.announcement ?? {}) },
      miso_pages: l.miso_pages !== false,
      miso_blocks: l.miso_blocks ?? { hero: 'peek' },
      privacy: l.privacy ?? ''
    };
  }

  function toLook(d: Doc): SiteLook {
    return {
      preset: d.preset,
      brand: d.brand.toUpperCase(),
      ...(d.menu_layout ? { menu_layout: d.menu_layout } : {}),
      languages: d.spanish ? ['en', 'es'] : ['en'],
      ...(d.logo ? { logo: d.logo } : {}),
      ...(d.cover ? { cover: d.cover } : {}),
      texts: Object.fromEntries(Object.entries(d.texts).filter(([, v]) => v.trim())),
      blocks: d.blocks,
      picks: d.picks,
      announcement: d.announcement.on || d.announcement.text ? d.announcement : { on: false },
      miso_pages: d.miso_pages,
      miso_blocks: d.miso_blocks,
      ...(d.privacy.trim() ? { privacy: d.privacy } : {})
    };
  }

  onMount(async () => {
    try {
      site = await storeApi<Website>('/me/site/website');
      doc = toDoc(site.draft);
      lastSent = JSON.stringify(toLook(doc));
    } catch (e) {
      toast.error(e);
    }
    void loadDomains();
    void loadStore();
    void loadItems();
  });

  async function loadDomains() {
    try {
      const r = await storeApi<{ base_domain: string; domains: SiteDomain[] }>('/me/site/domains');
      domains = r.domains;
      baseDomain = r.base_domain;
    } catch (e) {
      toast.error(e);
    }
  }
  async function loadStore() {
    try {
      const [loc, contact] = await Promise.all([
        storeApi<{ name: string; phone: string; address_line1: string; address_line2: string }>('/me/location'),
        storeApi<{ reply_email: string }>('/me/site/contact')
      ]);
      store = { name: loc.name, phone: loc.phone, address: [loc.address_line1, loc.address_line2].filter(Boolean).join(', '), email: contact.reply_email };
    } catch {
      /* the privacy default just has fewer details */
    }
  }
  async function loadItems() {
    try {
      const views = await storeApi<{ id: string; kind: string }[]>('/me/menu/views');
      const web = views.find((v) => v.kind === 'web');
      if (!web) return;
      const b = await storeApi<{ categories: { name: string; items: { id: string; name: string }[] }[] }>(`/me/menu/views/${web.id}/build`);
      items = b.categories.flatMap((c) => c.items.map((i) => ({ id: i.id, name: i.name, category: c.name })));
    } catch {
      /* the picker stays empty */
    }
  }

  // ---- the draft saves itself (debounced); every save refreshes the preview
  let lastSent = '';
  let timer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    if (!doc) return;
    const body = JSON.stringify(toLook(doc));
    if (body === untrack(() => lastSent)) return;
    clearTimeout(timer);
    untrack(() => (saving = 'saving'));
    timer = setTimeout(() => save(body), 600);
  });

  async function save(body: string) {
    try {
      site = await storeApi<Website>('/me/site/website/draft', { method: 'PUT', body });
      lastSent = body;
      saving = 'saved';
      saveError = '';
      frameKey++;
    } catch (e) {
      saving = 'error';
      saveError = (e as Error).message;
    }
  }

  async function publish() {
    busy = 'publish';
    try {
      clearTimeout(timer);
      if (doc && JSON.stringify(toLook(doc)) !== lastSent) await save(JSON.stringify(toLook(doc)));
      site = await storeApi<Website>('/me/site/website/publish', { method: 'POST' });
      toast.success('Published · your website shows it within a minute');
      frameKey++;
    } catch (e) {
      toast.error(e);
    } finally {
      busy = '';
    }
  }

  async function discard() {
    if (!(await confirmDialog({ title: 'Discard your draft?', body: 'Your website stays as it is now. The changes in the draft are lost.', confirmLabel: 'Discard', danger: true }))) return;
    busy = 'discard';
    try {
      clearTimeout(timer);
      site = await storeApi<Website>('/me/site/website/draft', { method: 'DELETE' });
      doc = toDoc(site.draft);
      lastSent = JSON.stringify(toLook(doc));
      saving = 'idle';
      frameKey++;
    } catch (e) {
      toast.error(e);
    } finally {
      busy = '';
    }
  }

  // ---- uploads
  let picker = $state<HTMLInputElement>();
  function choose(kind: 'logo' | 'cover') {
    cropKind = kind;
    picker?.click();
  }
  async function picked(e: Event) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    (e.currentTarget as HTMLInputElement).value = '';
    if (!f) return;
    if (cropKind === 'logo' && f.type === 'image/svg+xml') return upload(f, 'image/svg+xml');
    if (!/^image\/(png|jpeg|webp)$/.test(f.type)) return toast.error('Pick a PNG, JPEG or WebP picture' + (cropKind === 'logo' ? ' (or an SVG logo)' : ''));
    cropFile = f;
  }
  async function upload(blob: Blob, type = blob.type) {
    busy = cropKind;
    try {
      const img = await storeApi<SiteImage>(`/me/site/images?kind=${cropKind}`, { method: 'POST', body: blob, headers: { 'Content-Type': type } });
      if (doc) doc[cropKind] = img.id;
    } catch (e) {
      toast.error(e);
    } finally {
      busy = '';
    }
  }

  // ---- blocks
  function move(i: number, to: number) {
    if (!doc || to < 0 || to >= doc.blocks.length) return;
    const [b] = doc.blocks.splice(i, 1);
    doc.blocks.splice(to, 0, b);
  }
  function catSpot(id: BlockId): MisoSpot {
    return doc?.miso_blocks[id] ?? 'off';
  }
  function cycleCat(id: BlockId) {
    if (!doc) return;
    const next: Record<MisoSpot, MisoSpot> = { off: 'peek', peek: 'side', side: 'off' };
    doc.miso_blocks = { ...doc.miso_blocks, [id]: next[catSpot(id)] };
  }
  const CAT_LABEL: Record<MisoSpot, string> = { off: 'No Miso', peek: 'Miso peeks over it', side: 'Miso sits beside it' };

  const pickItems = $derived(doc ? doc.picks.map((id) => items.find((i) => i.id === id) ?? { id, name: 'No longer on the menu', category: '' }) : []);
  const pickMatches = $derived(
    pickQuery.trim().length < 2 || !doc ? [] : items.filter((i) => !doc!.picks.includes(i.id) && i.name.toLowerCase().includes(pickQuery.trim().toLowerCase())).slice(0, 6)
  );

  // ---- contrast (WCAG): the brand colour with its button text, and against the page
  function lum(hex: string) {
    const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
    return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
  }
  const ratio = (a: string, b: string) => {
    const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
    return (x + 0.05) / (y + 0.05);
  };
  const validHex = $derived(!!doc && /^#[0-9a-f]{6}$/i.test(doc.brand));
  const checks = $derived.by(() => {
    if (!doc || !validHex) return null;
    const p = PRESETS.find((x) => x.id === doc!.preset) ?? PRESETS[0];
    const onBrand = Math.max(ratio(doc.brand, '#FFFFFF'), ratio(doc.brand, '#1A1530'));
    const onPage = ratio(doc.brand, p.canvas);
    // Buttons carry bold text (3:1 is enough there); the colour must also stand out on the page.
    return { onBrand, onPage, ok: onBrand >= 3 && onPage >= 2 };
  });

  // ---- preview
  const slug = (s: string) => s.normalize('NFKD').replace(/[̀-ͯ]/g, '').toLowerCase().replace(/&/g, ' and ').replace(/['’"]/g, '').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  const pagePath = $derived(pageSel === 'item' ? (items[0] ? `menu/${slug(items[0].name)}` : 'menu') : pageSel);
  const frameSrc = $derived(site?.preview_url ? site.preview_url + (pagePath ? '/' + pagePath : '') : '');
  const dev = $derived(DEVICES.find((d) => d.id === device)!);
  let paneW = $state(600);
  const fit = $derived(Math.min(1, (paneW - 2) / dev.w));

  const primary = $derived(domains.find((d) => d.primary && d.verified) ?? domains.find((d) => d.verified));
  const pending = $derived(domains.filter((d) => !d.verified));
  async function requestDomain() {
    busy = 'domain';
    try {
      await storeApi('/me/site/domains', { method: 'POST', body: JSON.stringify({ host: ownHost }) });
      ownHost = '';
      await loadDomains();
    } catch (e) {
      toast.error(e);
    } finally {
      busy = '';
    }
  }
  async function dropDomain(host: string) {
    if (!(await confirmDialog({ title: `Remove ${host}?`, confirmLabel: 'Remove', danger: true }))) return;
    try {
      await storeApi(`/me/site/domains/${encodeURIComponent(host)}`, { method: 'DELETE' });
      await loadDomains();
    } catch (e) {
      toast.error(e);
    }
  }
  const sub = (host: string) => host.split('.').slice(0, -2).join('.') || '@';

  const defaultPrivacy = $derived(
    [
      [store.name, store.address, store.phone].filter(Boolean).join(' · '),
      'What we collect: your name, phone, email (optional), delivery addresses and your orders, so we can make and deliver them and send receipts.',
      'Cards: card numbers go straight to Square, our card processor. We never see or store them.',
      "We don't sell or share your information.",
      `Your account: delete it any time from Account › Delete my account.${store.email || store.phone ? ` Questions: ${[store.email, store.phone].filter(Boolean).join(' or ')}.` : ''}`
    ].join('\n\n')
  );

  function when(t: string | null) {
    return t ? new Date(t).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : '';
  }
  async function copy(text: string) {
    await navigator.clipboard.writeText(text).catch(() => {});
    toast.success('Copied');
  }
</script>

<svelte:head><title>Website — quicc</title></svelte:head>

<input bind:this={picker} type="file" accept={cropKind === 'logo' ? 'image/png,image/jpeg,image/webp,image/svg+xml' : 'image/png,image/jpeg,image/webp'} class="hidden" onchange={picked} />
<ImageCrop bind:file={cropFile} kind={cropKind} ondone={(b) => upload(b)} />

<PageHeader title="Website" subtitle="How your ordering site looks. Changes go to a draft; only you see them in the preview until you publish.">
  {#snippet actions()}
    {#if site?.preview_url}<a class="btn btn-g" href={frameSrc} target="_blank" rel="noopener"><Icon name="external" size={16} />Open preview</a>{/if}
    {#if site?.has_draft}<Button variant="ghost" loading={busy === 'discard'} onclick={discard}>Discard draft</Button>{/if}
    <Button variant="primary" icon="upload" loading={busy === 'publish'} disabled={!site?.has_draft || saving === 'error'} onclick={publish}>Publish</Button>
  {/snippet}
</PageHeader>

{#if !site || !doc}
  <Skeleton lines={8} />
{:else}
  <div class="status">
    {#if primary}<Badge tone="ok" dot>Live</Badge><a class="mono" href="https://{primary.host}" target="_blank" rel="noopener">{primary.host}</a>{:else}<Badge tone="warn" dot>No address yet</Badge>{/if}
    {#if site.published_at}<span class="text-muted">· published {when(site.published_at)}{site.published_by === 'admin' ? ' by an admin' : ''}</span>{:else}<span class="text-muted">· not published yet (the default look shows)</span>{/if}
    <span class="grow"></span>
    {#if saving === 'saving'}<span class="text-muted">Saving…</span>{:else if saving === 'error'}<span class="field-error !m-0">Not saved: {saveError}</span>{:else if saving === 'saved'}<span class="text-muted">Draft saved</span>{/if}
  </div>
  {#if site.has_draft && site.changes.length}
    <div class="changes"><Icon name="pencil" size={16} /><span><b>{site.changes.length} {site.changes.length === 1 ? 'change' : 'changes'}</b> in your draft. Only you see them in the preview until you publish.</span></div>
  {/if}

  <div class="layout">
    <div class="editor">
      <!-- Look -->
      <Card title="Look" subtitle="Preset, colour, logo, menu layout">
        <div class="flex flex-col gap-5">
          <div>
            <span class="lbl">Preset</span>
            <div class="presets" role="radiogroup" aria-label="Preset">
              {#each PRESETS as p (p.id)}
                <button type="button" role="radio" aria-checked={doc.preset === p.id} class="preset" class:on={doc.preset === p.id} onclick={() => (doc!.preset = p.id)}>
                  <span class="sw2"><i style="background:{p.swatch[0]}"></i><i style="background:{p.swatch[1]}"></i><i style="background:{doc.brand}"></i></span>
                  <b>{p.name}</b><span class="text-muted text-[13px]">{p.desc}</span>
                </button>
              {/each}
            </div>
          </div>

          <div>
            <label class="lbl" for="brand">Brand colour</label>
            <div class="flex items-center gap-3 flex-wrap">
              <input type="color" class="colorpick" value={validHex ? doc.brand : '#E2363B'} oninput={(e) => (doc!.brand = (e.currentTarget as HTMLInputElement).value.toUpperCase())} aria-label="Pick a colour" />
              <input id="brand" class="field mono !w-[120px]" bind:value={doc.brand} maxlength="7" aria-invalid={!validHex || undefined} />
              {#if checks}
                {#if checks.ok}<Badge tone="ok">Readable · white text on it {checks.onBrand.toFixed(1)}:1</Badge>
                {:else}<Badge tone="warn">Hard to see · {checks.onPage.toFixed(1)}:1 on the page</Badge>{/if}
              {/if}
            </div>
            {#if !validHex}<p class="field-error">A colour like #E2363B</p>
            {:else if checks && !checks.ok}<p class="help">This colour nearly disappears on the {(PRESETS.find((p) => p.id === doc!.preset) ?? PRESETS[0]).name} background. Buttons will be hard to spot; pick a stronger one.</p>
            {:else}<p class="help">The site uses a deeper or lighter shade of it for small text where needed, so every text stays readable.</p>{/if}
          </div>

          <div class="imgrow">
            <div class="thumb logo">{#if doc.logo}<img src="/store/v1/me/site/images/{doc.logo}" alt="Logo" />{:else}<span>{store.name.replace(/[^A-Za-z]/g, '').slice(0, 1) || 'L'}</span>{/if}</div>
            <div class="grow">
              <b>Logo</b>
              <p class="text-muted text-[13px] m-0">{doc.logo ? 'Shown in the header and as the app icon.' : 'No logo yet: the name shows in type. A logo also becomes the app icon.'}</p>
            </div>
            <div class="flex gap-2">
              <Button size="sm" icon="upload" loading={busy === 'logo'} onclick={() => choose('logo')}>{doc.logo ? 'Change' : 'Upload'}</Button>
              {#if doc.logo}<Button size="sm" variant="ghost" onclick={() => (doc!.logo = '')}>Remove</Button>{/if}
            </div>
          </div>
          <div class="imgrow">
            <div class="thumb cover">{#if doc.cover}<img src="/store/v1/me/site/images/{doc.cover}" alt="Cover" />{:else}<span>16 : 9</span>{/if}</div>
            <div class="grow">
              <b>Cover photo</b>
              <p class="text-muted text-[13px] m-0">Behind the home headline and About us. JPG or PNG, cropped to 16:9.</p>
            </div>
            <div class="flex gap-2">
              <Button size="sm" icon="upload" loading={busy === 'cover'} onclick={() => choose('cover')}>{doc.cover ? 'Change' : 'Upload'}</Button>
              {#if doc.cover}<Button size="sm" variant="ghost" onclick={() => (doc!.cover = '')}>Remove</Button>{/if}
            </div>
          </div>

          <div>
            <span class="lbl">Menu layout</span>
            <div class="seg" role="radiogroup" aria-label="Menu layout">
              {#each LAYOUTS as l (l.id)}
                {@const own = (PRESETS.find((p) => p.id === doc!.preset) ?? PRESETS[0]).layout}
                <button type="button" role="radio" aria-checked={(doc.menu_layout || own) === l.id} class:on={(doc.menu_layout || own) === l.id} onclick={() => (doc!.menu_layout = l.id === own ? '' : l.id)}>{l.label}</button>
              {/each}
            </div>
            <p class="help">List shows a big-initial tile until items have photos (photos come with the menu editor, later).</p>
          </div>

          <div class="toggle">
            <div><b>Miso the cat chef</b><p class="text-muted text-[13px] m-0">On cart, checkout and sign-in. Order tracking, closed and error screens always show Miso.</p></div>
            <Switch label="Miso on cart, checkout and sign-in" bind:checked={doc.miso_pages} />
          </div>
          <div class="toggle">
            <div><b>Spanish</b><p class="text-muted text-[13px] m-0">An EN / ES switch in the header; menu names stay as written.</p></div>
            <Switch label="Spanish" bind:checked={doc.spanish} />
          </div>
        </div>
      </Card>

      <!-- Home page -->
      <Card title="Home page" subtitle="Drag to reorder · switch blocks on or off · the cat puts Miso around a block">
        <ul class="blocks">
          {#each doc.blocks as blk, i (blk.id)}
            {@const meta = BLOCKS[blk.id]}
            <li
              class="blk"
              class:off={!blk.on}
              class:dragging={dragging === i}
              draggable="true"
              ondragstart={(e) => {
                dragging = i;
                e.dataTransfer?.setData('text/plain', String(i));
              }}
              ondragover={(e) => {
                e.preventDefault();
                if (dragging !== null && dragging !== i) {
                  move(dragging, i);
                  dragging = i;
                }
              }}
              ondragend={() => (dragging = null)}
            >
              <div class="blk-row">
                <span class="grip" aria-hidden="true"><Icon name="grip" size={16} /></span>
                <div class="grow min-w-0">
                  <b>{meta.name}</b>
                  <span class="text-muted text-[13px] block">
                    {#if blk.id === 'picks'}{doc.picks.length ? `${doc.picks.length} picked (up to 8)` : 'None picked: the first item of each category'}
                    {:else}{meta.desc}{/if}
                  </span>
                </div>
                <span class="sr-only">Position {i + 1} of {doc.blocks.length}</span>
                <button type="button" class="mini" aria-label="Move {meta.name} up" disabled={i === 0} onclick={() => move(i, i - 1)}>↑</button>
                <button type="button" class="mini" aria-label="Move {meta.name} down" disabled={i === doc.blocks.length - 1} onclick={() => move(i, i + 1)}>↓</button>
                <button type="button" class="cat" class:on={catSpot(blk.id) !== 'off'} aria-label="{CAT_LABEL[catSpot(blk.id)]} — change" title={CAT_LABEL[catSpot(blk.id)]} onclick={() => cycleCat(blk.id)}>
                  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d="M4 4l4 4h8l4-4v9a8 8 0 0 1-16 0z" fill={catSpot(blk.id) !== 'off' ? 'currentColor' : 'none'} stroke="currentColor" stroke-width="2" stroke-linejoin="round" /><circle cx="9.5" cy="13" r="1.2" fill={catSpot(blk.id) !== 'off' ? 'var(--surface)' : 'currentColor'} /><circle cx="14.5" cy="13" r="1.2" fill={catSpot(blk.id) !== 'off' ? 'var(--surface)' : 'currentColor'} /></svg>
                  {#if catSpot(blk.id) !== 'off'}<small>{catSpot(blk.id) === 'peek' ? 'peeks' : 'beside'}</small>{/if}
                </button>
                {#if meta.edit}<button type="button" class="mini" aria-expanded={open === blk.id} aria-label="Edit {meta.name}" onclick={() => (open = open === blk.id ? '' : blk.id)}><Icon name={open === blk.id ? 'minus' : 'pencil'} size={14} /></button>{/if}
                <Switch label="Show {meta.name}" bind:checked={blk.on} />
              </div>

              {#if open === blk.id && blk.id === 'hero'}
                <div class="blk-edit">
                  <label class="lbl" for="h-headline">Headline</label>
                  <input id="h-headline" class="field" maxlength="80" placeholder="Made from your menu (e.g. “Pizza, desi & subs.”)" bind:value={doc.texts.headline} />
                  <label class="lbl" for="h-lead">Intro</label>
                  <textarea id="h-lead" class="field" rows="2" maxlength="200" placeholder="Delivered to your door, or pick up at {store.address || 'your store'}." bind:value={doc.texts.lead}></textarea>
                  {#if doc.spanish}
                    <label class="lbl" for="h-headline-es">Headline (Spanish)</label>
                    <input id="h-headline-es" class="field" maxlength="80" placeholder="Empty: our Spanish text" bind:value={doc.texts.headline_es} />
                    <label class="lbl" for="h-lead-es">Intro (Spanish)</label>
                    <textarea id="h-lead-es" class="field" rows="2" maxlength="200" placeholder="Empty: our Spanish text" bind:value={doc.texts.lead_es}></textarea>
                  {/if}
                  <p class="help">Empty = made from the menu and the store details.</p>
                </div>
              {:else if open === blk.id && blk.id === 'picks'}
                <div class="blk-edit">
                  {#each pickItems as it, j (it.id)}
                    <div class="pick"><span class="grow">{it.name}{#if it.category}<span class="text-muted">{` · ${it.category}`}</span>{/if}</span>
                      <button type="button" class="mini" aria-label="Up" disabled={j === 0} onclick={() => doc!.picks.splice(j - 1, 0, ...doc!.picks.splice(j, 1))}>↑</button>
                      <button type="button" class="mini" aria-label="Remove {it.name}" onclick={() => doc!.picks.splice(j, 1)}>×</button>
                    </div>
                  {/each}
                  {#if doc.picks.length < 8}
                    <input class="field" placeholder={items.length ? 'Add an item: type its name' : 'Loading the menu…'} bind:value={pickQuery} />
                    {#each pickMatches as m (m.id)}
                      <button type="button" class="match" onclick={() => { doc!.picks.push(m.id); pickQuery = ''; }}>{m.name} <span class="text-muted">· {m.category}</span></button>
                    {/each}
                  {/if}
                  <p class="help">None picked = the first item of each category.</p>
                </div>
              {:else if open === blk.id && blk.id === 'about'}
                <div class="blk-edit">
                  <label class="lbl" for="a-title">Title</label>
                  <input id="a-title" class="field" maxlength="80" placeholder="About us" bind:value={doc.texts.about_title} />
                  <label class="lbl" for="a-story">Story</label>
                  <textarea id="a-story" class="field" rows="5" maxlength="2000" placeholder="How it started, what you're known for. A blank line starts a new paragraph." bind:value={doc.texts.about}></textarea>
                  {#if doc.spanish}
                    <label class="lbl" for="a-title-es">Title (Spanish)</label>
                    <input id="a-title-es" class="field" maxlength="80" placeholder="Sobre nosotros" bind:value={doc.texts.about_title_es} />
                    <label class="lbl" for="a-story-es">Story (Spanish)</label>
                    <textarea id="a-story-es" class="field" rows="4" maxlength="2000" placeholder="Empty: the English story" bind:value={doc.texts.about_es}></textarea>
                  {/if}
                  {#if !doc.texts.about.trim()}<p class="help">About us shows once it has a story.</p>{/if}
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      </Card>

      <!-- Announcement -->
      <Card title="Announcement bar" subtitle="One line on top of every page">
        <div class="flex flex-col gap-3">
          <div class="toggle">
            <div><b>Show the bar</b><p class="text-muted text-[13px] m-0">Only between the dates (leave them empty for always).</p></div>
            <Switch label="Show the announcement bar" bind:checked={doc.announcement.on} />
          </div>
          <div>
            <label class="lbl" for="ann">Text</label>
            <input id="ann" class="field" maxlength="140" placeholder="Closed Thanksgiving Day · open Friday 11 am" bind:value={doc.announcement.text} aria-invalid={(doc.announcement.on && !doc.announcement.text.trim()) || undefined} />
            {#if doc.announcement.on && !doc.announcement.text.trim()}<p class="field-error">The bar needs its text</p>{/if}
          </div>
          {#if doc.spanish}
            <div><label class="lbl" for="ann-es">Text (Spanish)</label><input id="ann-es" class="field" maxlength="140" placeholder="Empty: the English text" bind:value={doc.announcement.text_es} /></div>
          {/if}
          <div class="flex gap-3 flex-wrap">
            <div><label class="lbl" for="ann-from">From</label><input id="ann-from" type="date" class="field" bind:value={doc.announcement.from} /></div>
            <div><label class="lbl" for="ann-to">To</label><input id="ann-to" type="date" class="field" bind:value={doc.announcement.to} /></div>
          </div>
        </div>
      </Card>

      <!-- Address -->
      <Card title="Address" subtitle="Where customers find your site">
        <div class="flex flex-col gap-3">
          {#each domains.filter((d) => d.verified) as d (d.host)}
            <div class="host"><span class="mono grow">{d.host}</span>{#if d.primary}<Badge tone="brand">Primary</Badge>{/if}<Button size="sm" variant="ghost" icon="copy" onclick={() => copy('https://' + d.host)}>Copy</Button></div>
          {:else}
            <p class="text-muted m-0">No address yet: an admin gives your site its quiccpos.com address.</p>
          {/each}
          {#each pending as d (d.host)}
            <div class="pending">
              <div class="host"><span class="mono grow">{d.host}</span><Badge tone="warn">Waiting for check</Badge><Button size="sm" variant="danger-ghost" onclick={() => dropDomain(d.host)}>Remove</Button></div>
              <p class="text-[13px] m-0">Add this record at your domain company; an admin checks it and switches it on.</p>
              <div class="dns mono"><span>Type</span><b>CNAME</b><span>Name</span><b>{sub(d.host)}</b><span>Value</span><b>stores.{baseDomain}</b></div>
            </div>
          {/each}
          {#if !pending.length}
            <div>
              <label class="lbl" for="own">Use your own domain</label>
              <div class="flex gap-2">
                <input id="own" class="field mono" placeholder="order.yourpizza.com" bind:value={ownHost} />
                <Button loading={busy === 'domain'} disabled={!ownHost.trim()} onclick={requestDomain}>Request</Button>
              </div>
              <p class="help">We show you one DNS record to add at your domain company; an admin checks it and switches it on.</p>
            </div>
          {/if}
        </div>
      </Card>

      <!-- Privacy -->
      <Card title="Privacy policy" subtitle="Linked in the footer of every page">
        <div class="flex flex-col gap-3">
          <div class="flex items-center gap-2">
            {#if doc.privacy.trim()}<Badge tone="info">Your own text</Badge>{:else}<Badge tone="ok">Using the default</Badge>{/if}
            <span class="grow"></span>
            {#if !editingPrivacy}<Button size="sm" icon="pencil" onclick={() => { if (!doc!.privacy.trim()) doc!.privacy = defaultPrivacy; editingPrivacy = true; }}>Edit</Button>{/if}
            {#if doc.privacy.trim()}<Button size="sm" variant="ghost" onclick={() => { doc!.privacy = ''; editingPrivacy = false; }}>Back to the default</Button>{/if}
          </div>
          {#if editingPrivacy}
            <textarea class="field" rows="10" maxlength="8000" bind:value={doc.privacy}></textarea>
            <p class="help">A blank line starts a new paragraph. The default fills in your store's details by itself.</p>
          {:else}
            <div class="policy">{#each (doc.privacy.trim() || defaultPrivacy).split(/\n\s*\n/) as p, i (i)}<p>{p}</p>{/each}</div>
          {/if}
        </div>
      </Card>
    </div>

    <!-- Preview -->
    <aside class="preview">
      <div class="pv-bar">
        <select class="field !w-auto" bind:value={pageSel} aria-label="Page">
          {#each PAGES as p (p.id)}<option value={p.id}>{p.label}</option>{/each}
        </select>
        <div class="seg" role="radiogroup" aria-label="Device">
          {#each DEVICES as d (d.id)}<button type="button" role="radio" aria-checked={device === d.id} class:on={device === d.id} onclick={() => (device = d.id)}>{d.label}</button>{/each}
        </div>
        <Badge tone={site.has_draft ? 'brand' : 'mute'}>{site.has_draft ? 'Draft' : 'Published'}</Badge>
      </div>
      <div class="pane" bind:clientWidth={paneW}>
        {#if frameSrc}
          <div class="device" style="width:{dev.w * fit}px;height:{dev.h * fit}px">
            {#key frameKey}
              <iframe title="Preview of your website" src={frameSrc} style="width:{dev.w}px;height:{dev.h}px;transform:scale({fit})"></iframe>
            {/key}
          </div>
        {:else}
          <p class="text-muted p-6">The preview shows once your site has an address.</p>
        {/if}
      </div>
    </aside>
  </div>
{/if}

<style>
  .status { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 12px; font-size: 14px; }
  .status a { color: var(--text); }
  .grow { flex: 1; }
  .changes { display: flex; align-items: center; gap: 10px; padding: 10px 14px; margin-bottom: 16px; border-radius: 10px; background: var(--brand-soft); color: var(--text); font-size: 14px; }
  .layout { display: grid; gap: 20px; align-items: start; }
  @media (min-width: 1200px) { .layout { grid-template-columns: minmax(440px, 560px) 1fr; } .preview { position: sticky; top: 16px; } }
  .editor { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
  .presets { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .preset { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; padding: 12px; border: 1px solid var(--border); border-radius: 12px; background: var(--surface); text-align: left; }
  .preset.on { border-color: var(--accent); box-shadow: 0 0 0 2px var(--brand-soft); }
  .sw2 { display: flex; margin-bottom: 4px; }
  .sw2 i { width: 22px; height: 22px; border-radius: 50%; border: 1px solid var(--border); margin-right: -6px; }
  .colorpick { width: 44px; height: 40px; padding: 0; border: 1px solid var(--border); border-radius: 10px; background: none; }
  .imgrow { display: flex; align-items: center; gap: 12px; }
  .thumb { flex: none; display: grid; place-items: center; overflow: hidden; background: var(--elev); color: var(--muted); font-weight: 700; border: 1px solid var(--border); }
  .thumb.logo { width: 52px; height: 52px; border-radius: 12px; font-size: 22px; }
  .thumb.cover { width: 92px; height: 52px; border-radius: 8px; font-size: 12px; }
  .thumb img { width: 100%; height: 100%; object-fit: contain; }
  .thumb.cover img { object-fit: cover; }
  .seg { display: inline-flex; padding: 3px; gap: 2px; border-radius: 10px; background: var(--elev); }
  .seg button { min-height: 34px; padding: 0 14px; border-radius: 8px; font-weight: 600; font-size: 14px; color: var(--text2); }
  .seg button.on { background: var(--surface); color: var(--text); box-shadow: var(--shadow); }
  .toggle { display: flex; align-items: center; gap: 12px; justify-content: space-between; }
  .blocks { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .blk { border: 1px solid var(--border); border-radius: 12px; background: var(--surface); }
  .blk.off .blk-row b { color: var(--muted); }
  .blk.dragging { opacity: 0.5; }
  .blk-row { display: flex; align-items: center; gap: 6px; padding: 8px 6px 8px 10px; }
  .grip { color: var(--muted); cursor: grab; }
  .mini { width: 30px; height: 30px; border-radius: 8px; color: var(--text2); display: grid; place-items: center; }
  .mini:hover:not(:disabled) { background: var(--hover); }
  .mini:disabled { opacity: 0.3; }
  .cat { display: inline-flex; align-items: center; gap: 3px; height: 30px; padding: 0 6px; border-radius: 8px; color: var(--muted); }
  .cat.on { color: var(--accent); background: var(--brand-soft); }
  .cat small { font-size: 11px; font-weight: 700; }
  .blk-edit { display: flex; flex-direction: column; gap: 6px; padding: 4px 14px 14px; border-top: 1px solid var(--border); }
  .blk-edit .lbl { margin-top: 8px; }
  .pick { display: flex; align-items: center; gap: 4px; padding: 6px 8px; border-radius: 8px; background: var(--elev); font-size: 14px; }
  .match { text-align: left; padding: 8px 10px; border-radius: 8px; font-size: 14px; }
  .match:hover { background: var(--hover); }
  .host { display: flex; align-items: center; gap: 8px; }
  .pending { display: flex; flex-direction: column; gap: 8px; padding: 12px; border-radius: 10px; background: var(--elev); }
  .dns { display: grid; grid-template-columns: auto 1fr; gap: 4px 14px; font-size: 13px; }
  .dns span { color: var(--muted); }
  .policy { display: flex; flex-direction: column; gap: 8px; font-size: 14px; color: var(--text2); max-height: 260px; overflow: auto; }
  .policy p { margin: 0; }
  .preview { min-width: 0; display: flex; flex-direction: column; gap: 10px; }
  .pv-bar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  .pane { min-height: 300px; display: flex; justify-content: center; padding: 12px 0; border-radius: 14px; background: var(--elev); }
  .device { overflow: hidden; border-radius: 14px; border: 1px solid var(--border); box-shadow: var(--shadow-pop); background: #000; }
  .device iframe { border: 0; transform-origin: 0 0; display: block; }
</style>
