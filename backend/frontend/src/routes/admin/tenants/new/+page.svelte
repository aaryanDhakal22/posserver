<script lang="ts">
  import { tick } from 'svelte';
  import { beforeNavigate, goto } from '$app/navigation';
  import { base } from '$app/paths';
  import {
    adminApi,
    ApiError,
    errText,
    openAsStore,
    parseStoreId,
    storeApi,
    storeSignOut,
    type Location,
    type MenuImportResult,
    type Printer,
    type Tenant
  } from '$lib/api';
  import { copyText, generatePassword } from '$lib/clipboard';
  import { hoursFromConfig, hoursToConfig, type WeekHours } from '$lib/hours';
  import { DEFAULT_TIMEZONE } from '$lib/timezones';
  import { themeState, toggleTheme } from '$lib/theme.svelte';
  import { toast } from '$lib/toast.svelte';
  import Brand from '$lib/ui/Brand.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import ImportPrintSummary from '$lib/components/menu/ImportPrintSummary.svelte';
  import HoursEditor from '$lib/components/HoursEditor.svelte';
  import LocationFields, { locationForm, type LocationForm } from '$lib/components/LocationFields.svelte';

  // New-store wizard. Steps 1–2 are admin calls (step 2 creates the tenant +
  // its location); steps 3–5 run as the new store's manager through an
  // "Open as store" session, which is dropped again when leaving for the
  // admin pages. The tenant is created once — going back only reviews it.

  const STEPS: [string, string][] = [
    ['Store', 'Name and store ID'],
    ['Manager login', 'Username and password'],
    ['Address & hours', 'Receipts and alerts'],
    ['Printers', 'Receipt and kitchen'],
    ['Menu', 'Import a CSV (optional)'],
    ['Done', 'Hand-off details']
  ];

  let step = $state(1);
  let reached = $state(1);
  // Steps passed with Skip (or with nothing filled in) — shown as "Skipped".
  let skipped = $state<Record<number, boolean>>({});
  let panel = $state<HTMLElement>();
  let busy = $state(false);
  let errs = $state<Record<string, string>>({});

  // 1 · store
  let name = $state('');
  let slug = $state('');
  let slugTouched = false;
  let storeIdText = $state('');
  // 2 · login
  let username = $state('');
  let usernameTouched = false;
  let password = $state(generatePassword());
  // created
  let created = $state<{ tenant: Tenant; location: Location } | null>(null);
  let sessionOk = $state(false);
  let sessionErr = $state('');
  // 3 · location
  let loc = $state<Location | null>(null);
  let form = $state<LocationForm>({ name: '', phone: '', address_line1: '', address_line2: '', timezone: DEFAULT_TIMEZONE, daily_reset_hour: 0 });
  let hours = $state<WeekHours>(hoursFromConfig({}));
  // 4 · printers
  let printers = $state<Printer[]>([]);
  let np = $state({ name: '', ip: '' });
  // 5 · menu
  let csvName = $state('');
  let csvText = $state('');
  let csvPreview = $state<MenuImportResult | null>(null);
  let csvError = $state('');
  let csvInput = $state<HTMLInputElement>();
  let dragOver = $state(false);

  const slugify = (s: string) =>
    s
      .toLowerCase()
      .replace(/['’]/g, '')
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  const userify = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '');

  function onName() {
    if (!slugTouched) slug = slugify(name);
    if (!usernameTouched) username = userify(name);
  }

  // --- navigation ---------------------------------------------------------
  function enter(n: number) {
    step = n;
    reached = Math.max(reached, n);
    if (n === 3 && sessionOk && !loc) loadLocation();
    if (n === 4 && sessionOk) loadPrinters();
    // Move focus to the new step's heading, so keyboard and screen-reader
    // users start at the top of it (the old step's controls are gone).
    tick().then(() => panel?.querySelector<HTMLElement>('h1')?.focus());
  }
  function leave(n: number, wasSkipped: boolean) {
    skipped[step] = wasSkipped;
    enter(n);
  }
  function skip() {
    if (busy) return;
    leave(step + 1, true);
  }
  function go(n: number) {
    if (busy || n > reached) return;
    enter(n);
  }

  beforeNavigate(({ to }) => {
    // Leaving for anything but the store pages drops the "Open as store" session.
    if (created && sessionOk && !to?.url.pathname.startsWith(base + '/store')) {
      sessionOk = false;
      storeSignOut();
    }
  });

  // --- step 1 / 2 -----------------------------------------------------------
  function checkStore(): boolean {
    errs = {};
    if (!name.trim()) errs.name = 'Give the store a name.';
    if (!/^[a-z0-9]+(-[a-z0-9]+)*$/.test(slug)) errs.slug = 'Lowercase letters, numbers and single dashes only.';
    if (parseStoreId(storeIdText) === undefined) errs.storeId = 'A positive whole number, or leave it blank.';
    return Object.keys(errs).length === 0;
  }

  async function create() {
    errs = {};
    // A blank username creates the store without a login (set one later on its Login tab).
    const u = username.trim();
    if (u.toLowerCase() === 'admin') errs.username = '“admin” is reserved — pick another username.';
    if (u && !password) errs.password = 'Set a password.';
    if (Object.keys(errs).length) return;
    busy = true;
    try {
      const login = u ? { username: u, password } : {};
      const res = await adminApi<Tenant & { location: Location }>('/tenants', {
        method: 'POST',
        body: JSON.stringify({ slug, name: name.trim(), ...login, store_id: parseStoreId(storeIdText) })
      });
      const { location, ...tenant } = res;
      created = { tenant: tenant as Tenant, location };
      toast.success(`${tenant.name} created`);
      await openSession();
      leave(3, false);
    } catch (e) {
      const msg = errText(e);
      if (e instanceof ApiError && /username/i.test(msg)) {
        errs.username = e.status === 409 ? 'That username is already taken — pick another.' : msg;
      } else if (/store.?id/i.test(msg)) {
        errs.storeId = e instanceof ApiError && e.status === 409 ? 'Another store already uses this store ID.' : msg;
        enter(1);
      } else if (/slug/i.test(msg)) {
        errs.slug = msg;
        enter(1);
      } else {
        toast.error(e);
      }
    } finally {
      busy = false;
    }
  }

  async function openSession() {
    if (!created) return;
    sessionErr = '';
    try {
      await openAsStore(created.tenant.id);
      sessionOk = true;
    } catch (e) {
      sessionOk = false;
      sessionErr = errText(e);
    }
  }
  async function retrySession() {
    busy = true;
    await openSession();
    busy = false;
    if (sessionOk) enter(step);
  }

  // --- step 3 ---------------------------------------------------------------
  async function loadLocation() {
    try {
      loc = await storeApi<Location>('/me/location');
      form = locationForm(loc);
      if (!form.timezone) form.timezone = DEFAULT_TIMEZONE;
      hours = hoursFromConfig(loc.config);
    } catch (e) {
      toast.error(e);
    }
  }
  async function saveLocation(): Promise<boolean> {
    if (!loc) return false;
    try {
      // PATCH is a full replace: send every field, merging hours into the config.
      loc = await storeApi<Location>('/me/location', {
        method: 'PATCH',
        body: JSON.stringify({
          ...form,
          daily_reset_hour: Number(form.daily_reset_hour) || 0,
          config: { ...(loc.config ?? {}), opening_hours: hoursToConfig(hours) }
        })
      });
      return true;
    } catch (e) {
      toast.error(e);
      return false;
    }
  }

  // --- step 4 ---------------------------------------------------------------
  async function loadPrinters() {
    try {
      printers = await storeApi<Printer[]>('/me/printers');
    } catch (e) {
      toast.error(e);
    }
  }
  async function addPrinter(): Promise<boolean> {
    errs = {};
    if (!np.name.trim()) {
      errs.printer = 'Give the printer a name.';
      return false;
    }
    try {
      await storeApi('/me/printers', { method: 'POST', body: JSON.stringify({ name: np.name.trim(), ip: np.ip.trim() }) });
      np = { name: '', ip: '' };
      await loadPrinters();
      return true;
    } catch (e) {
      errs.printer = errText(e);
      return false;
    }
  }
  async function useForReceipts(p: Printer) {
    try {
      await storeApi('/me/printers/' + p.id, { method: 'PATCH', body: JSON.stringify({ name: p.name, ip: p.ip, is_receipt: true }) });
      await loadPrinters();
    } catch (e) {
      toast.error(e);
    }
  }
  async function removePrinter(p: Printer) {
    try {
      await storeApi('/me/printers/' + p.id, { method: 'DELETE' });
      await loadPrinters();
    } catch (e) {
      toast.error(e);
    }
  }

  // --- step 5 ---------------------------------------------------------------
  async function takeFile(f: File | undefined) {
    if (!f) return;
    csvName = f.name;
    csvText = await f.text();
    csvPreview = null;
    csvError = '';
    busy = true;
    try {
      csvPreview = await storeApi<MenuImportResult>('/me/menu/import?dry_run=true', {
        method: 'POST',
        body: csvText,
        headers: { 'Content-Type': 'text/csv' }
      });
    } catch (e) {
      csvError = errText(e);
    } finally {
      busy = false;
    }
  }
  async function importMenu(): Promise<boolean> {
    try {
      const r = await storeApi<MenuImportResult>('/me/menu/import', {
        method: 'POST',
        body: csvText,
        headers: { 'Content-Type': 'text/csv' }
      });
      toast.success(`Menu imported: ${r.items} items in ${r.categories} categories`);
      return true;
    } catch (e) {
      csvError = errText(e) + ' — the import may have stopped part-way; importing the same file again picks up where it stopped.';
      return false;
    }
  }
  const template = $derived(
    'data:text/csv;charset=utf-8,' +
      encodeURIComponent(
        `menu,category,item,price,printer,description\nMain menu,Pizza,Cheese Pizza,12.99,${printers.find((p) => !p.is_receipt)?.name ?? printers[0]?.name ?? ''},\n`
      )
  );

  // --- footer ---------------------------------------------------------------
  async function next() {
    if (busy) return;
    if (step === 1) {
      if (created || checkStore()) enter(2);
      return;
    }
    if (step === 2) {
      if (created) return enter(3);
      return create();
    }
    if (!sessionOk) return leave(step + 1, true);
    busy = true;
    try {
      if (step === 3) {
        if (await saveLocation()) leave(4, false);
      } else if (step === 4) {
        if (np.name.trim() && !(await addPrinter())) return;
        leave(5, printers.length === 0);
      } else if (step === 5) {
        if (csvPreview && !(await importMenu())) return;
        leave(6, !csvPreview);
      }
    } finally {
      busy = false;
    }
  }
  /** Each step's <form> submits here (Enter in a field = Continue). */
  function submitStep(e: SubmitEvent, n: number) {
    e.preventDefault();
    // Only the step on screen; Enter on step 1 never reaches step 2's create.
    if (n !== step) return;
    next();
  }
  function back() {
    if (step > 1) enter(step - 1);
  }
  const cta = $derived(
    step === 2 ? (created ? 'Continue' : 'Create store') : step === 5 ? (csvPreview ? 'Import and finish' : 'Continue') : 'Continue'
  );

  // --- step 6 ---------------------------------------------------------------
  const loginUrl = $derived(typeof location !== 'undefined' ? `${location.origin}${base}/login` : base + '/login');
  const NEXT_STEPS = [
    'Plug in the Pi with a fresh card and open http://quiccpos-pi.',
    'Sign in there with the manager login — the Pi links itself.',
    'On the tablet, download Quibase from utility.quiccpos.com and follow its setup.'
  ];
  function copyAll() {
    const t = created?.tenant;
    const text = [
      `quicc — ${t?.name ?? name}`,
      `Sign in at: ${loginUrl}`,
      ...(t?.has_login ? [`Username: ${t.username}`, `Password: ${password}`] : ['Login: none yet']),
      `Store ID: ${t?.store_id ?? '—'}`,
      '',
      'Next, at the store:',
      ...NEXT_STEPS.map((s, i) => `${i + 1}. ${s}`)
    ].join('\n');
    copyText(text, 'Hand-off details copied');
  }
  async function backToOverview() {
    if (sessionOk) {
      sessionOk = false;
      await storeSignOut();
    }
    goto(base + '/admin');
  }

  // "Finish later": the store exists, so leaving needs the hand-off details.
  let laterOpen = $state(false);
  const errMissingPrinters = $derived(/printers that this store doesn't have/i.test(csvError));
  const stepState = (n: number) => (n === step ? 'current' : skipped[n] && n < reached ? 'skipped' : n < reached ? 'done' : 'todo');
</script>

<svelte:head><title>New store — quicc admin</title></svelte:head>

<div class="min-h-screen flex flex-col">
  <header class="flex flex-wrap items-center gap-3.5 px-4 sm:px-6 py-3.5 border-b border-border bg-surface">
    <Brand size={30} />
    <div class="font-bold">New store</div>
    <span class="text-muted hidden md:inline" aria-hidden="true">·</span>
    <span class="text-muted hidden md:inline">Step {step} of 6</span>
    <span class="flex-1"></span>
    <button type="button" class="btn btn-g" aria-label="{themeState.dark ? 'Light' : 'Dark'} mode" onclick={toggleTheme}>
      <Icon name={themeState.dark ? 'sun' : 'moon'} size={16} /><span class="hidden sm:inline">{themeState.dark ? 'Light' : 'Dark'} mode</span>
    </button>
    {#if created && step < 6}
      <button type="button" class="btn" onclick={() => (laterOpen = true)}>Finish later</button>
    {:else}
      <a class="btn" href="{base}/admin">{step === 6 ? 'Close' : 'Cancel'}</a>
    {/if}
  </header>

  <div class="flex-1 w-full max-w-[1120px] mx-auto flex flex-wrap content-start gap-x-7 gap-y-5 px-4 sm:px-6 pt-7 pb-12">
    <!-- phones / tablets: a compact progress bar instead of the step list -->
    <div class="md:hidden w-full flex flex-col gap-2">
      <div class="flex items-baseline gap-2 text-[14px]">
        <span class="font-semibold">Step {step} of 6</span>
        <span class="text-muted" aria-hidden="true">·</span>
        <span class="text-text2 truncate">{STEPS[step - 1][0]}</span>
      </div>
      <div class="flex gap-1" aria-hidden="true">
        {#each STEPS as _, i}
          {@const st = stepState(i + 1)}
          <span class="h-1.5 flex-1 rounded-full {st === 'current' ? 'bg-brand' : st === 'done' ? 'bg-ok' : st === 'skipped' ? 'bg-muted opacity-50' : 'bg-border'}"></span>
        {/each}
      </div>
    </div>

    <nav aria-label="Steps" class="hidden md:flex flex-[0_0_240px] flex-col gap-1">
      {#each STEPS as [title, sub], i}
        {@const n = i + 1}
        {@const st = stepState(n)}
        <button
          type="button"
          class="flex items-center gap-3 w-full text-left px-3 py-2.5 rounded-r10 text-[14px] font-medium {st === 'current'
            ? 'bg-brand-soft text-text font-semibold'
            : 'text-text2 hover:bg-hover'} disabled:cursor-default disabled:hover:bg-transparent"
          aria-current={st === 'current' ? 'step' : undefined}
          disabled={n > reached || busy}
          onclick={() => go(n)}
        >
          <span
            class="w-[26px] h-[26px] rounded-full flex items-center justify-center mono text-xs font-bold shrink-0 border-[1.5px] {st === 'current'
              ? 'bg-brand border-brand text-on-brand'
              : st === 'done'
                ? 'bg-ok-bg border-transparent text-ok'
                : st === 'skipped'
                  ? 'bg-elev border-border border-dashed text-muted'
                  : 'border-border text-muted'}"
          >
            {#if st === 'done'}<Icon name="check" size={13} stroke={3} label="Done" />{:else if st === 'skipped'}<Icon name="minus" size={13} stroke={3} label="Skipped" />{:else}{n}{/if}
          </span>
          <span>
            <span class="block">{title}</span>
            <span class="block text-xs font-normal {st === 'current' ? 'text-text2' : 'text-muted'}">{st === 'skipped' ? 'Skipped' : sub}</span>
          </span>
        </button>
      {/each}
    </nav>

    <section bind:this={panel} class="card flex-[999_1_560px] min-w-0 p-5 sm:p-7 flex flex-col gap-[22px]">
      {#if step === 1}
        <form id="wiz-step" class="contents" onsubmit={(e) => submitStep(e, 1)}>
        <div>
          <h1 tabindex="-1" class="m-0 text-[22px] font-bold focus:outline-none">Name the store</h1>
          <p class="mt-1.5 text-muted">This is what managers, receipts and the tablet show.</p>
        </div>
        {#if created}
          <p class="m-0 px-3.5 py-3 rounded-r10 bg-brand-soft text-[13px]">
            The store is created. Change its name or store ID later from the store's page.
          </p>
        {/if}
        <div>
          <label class="lbl" for="w-name">Store name</label>
          <!-- svelte-ignore a11y_autofocus -->
          <input id="w-name" class="field" autofocus bind:value={name} oninput={onName} readonly={!!created} aria-invalid={!!errs.name || undefined} placeholder="Sal's Deli" />
          {#if errs.name}<p class="field-error" role="alert">{errs.name}</p>{/if}
        </div>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div>
            <label class="lbl" for="w-slug">Short name (URL)</label>
            <input
              id="w-slug"
              class="field mono"
              bind:value={slug}
              oninput={() => (slugTouched = true)}
              readonly={!!created}
              aria-invalid={!!errs.slug || undefined}
              autocapitalize="off"
              spellcheck="false"
            />
            {#if errs.slug}<p class="field-error" role="alert">{errs.slug}</p>{:else}<p class="help">Filled in from the name. Lowercase letters, numbers and dashes.</p>{/if}
          </div>
          <div>
            <label class="lbl" for="w-sid">Online ordering store ID</label>
            <input
              id="w-sid"
              class="field mono"
              inputmode="numeric"
              bind:value={storeIdText}
              readonly={!!created}
              aria-invalid={!!errs.storeId || undefined}
              placeholder="9003"
              autocomplete="off"
            />
            {#if errs.storeId}<p class="field-error" role="alert">{errs.storeId}</p>{:else}<p class="help">The store_id the ordering vendor sends. Orders are routed by it.</p>{/if}
          </div>
        </div>
        </form>
      {:else if step === 2}
        <form id="wiz-step" class="contents" onsubmit={(e) => submitStep(e, 2)}>
        <div>
          <h1 tabindex="-1" class="m-0 text-[22px] font-bold focus:outline-none">Manager login</h1>
          <p class="mt-1.5 text-muted">The store manager signs in here and on the Pi with this. You can reset it later.</p>
        </div>
        <div>
          <label class="lbl" for="w-user">Username</label>
          <input
            id="w-user"
            class="field mono"
            bind:value={username}
            oninput={() => (usernameTouched = true)}
            readonly={!!created}
            aria-invalid={!!errs.username || undefined}
            autocapitalize="off"
            spellcheck="false"
            autocomplete="off"
          />
          {#if errs.username}<p class="field-error" role="alert">{errs.username}</p>{:else}<p class="help">Can't be “admin”. Must be unique across stores. Leave it empty to add a login later.</p>{/if}
        </div>
        <div>
          <label class="lbl" for="w-pw">Password</label>
          <div class="flex flex-wrap gap-2">
            <input id="w-pw" class="field mono flex-1 min-w-[200px]" bind:value={password} readonly={!!created} autocomplete="new-password" aria-invalid={!!errs.password || undefined} />
            {#if !created}<Button onclick={() => (password = generatePassword())}>Generate new</Button>{/if}
            <Button icon="copy" aria-label="Copy password" onclick={() => copyText(password, 'Password copied')} />
          </div>
          {#if errs.password}<p class="field-error" role="alert">{errs.password}</p>{:else}<p class="help">Shown again on the last step so you can hand it over.</p>{/if}
        </div>
        <p class="m-0 px-3.5 py-3 rounded-r10 bg-brand-soft text-[13px]">
          {created ? 'The store is created with this login.' : 'Continuing creates the store. The next steps fill in its details as if you were the manager.'}
        </p>
        </form>
      {:else if step >= 3 && step <= 5 && !sessionOk}
        <div>
          <h1 tabindex="-1" class="m-0 text-[22px] font-bold focus:outline-none">{STEPS[step - 1][0]}</h1>
          <p class="mt-1.5 text-muted">These steps run as the store's manager.</p>
        </div>
        <div class="rounded-r12 px-4 py-3.5 flex flex-col gap-2 bg-warn-bg text-warn">
          <div class="font-semibold flex items-center gap-2"><Icon name="alert" size={18} />Couldn't open the store as its manager</div>
          <p class="m-0 text-text2">{sessionErr || 'No store session.'} You can skip ahead and fill this in later with “Open as store” on the store's page.</p>
          <div><Button size="sm" loading={busy} onclick={retrySession}>Try again</Button></div>
        </div>
      {:else if step === 3}
        <form id="wiz-step" class="contents" onsubmit={(e) => submitStep(e, 3)}>
        <div>
          <h1 tabindex="-1" class="m-0 text-[22px] font-bold focus:outline-none">Address &amp; opening hours</h1>
          <p class="mt-1.5 text-muted">Printed on receipts. Hours decide when quiet-store alerts are on.</p>
        </div>
        {#if loc}
          <LocationFields bind:value={form} showName={false} showReset={false} idPrefix="w-loc" />
          <HoursEditor bind:hours idPrefix="w-hours" />
        {:else}
          <Skeleton lines={5} />
        {/if}
        </form>
      {:else if step === 4}
        <div>
          <h1 tabindex="-1" class="m-0 text-[22px] font-bold focus:outline-none">Printers</h1>
          <p class="mt-1.5 text-muted">Add each thermal printer on the store's network. The Pi tests them once it's linked.</p>
        </div>
        <div class="flex flex-col gap-2.5">
          {#each printers as p (p.id)}
            <div class="grid items-center gap-2.5" style="grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto auto">
              <span class="field !flex items-center truncate" aria-label="Printer name">{p.name}</span>
              <span class="field mono !flex items-center truncate" aria-label="IP address">{p.ip || '—'}</span>
              {#if p.is_receipt}
                <Badge tone="brand">Receipts</Badge>
              {:else}
                <Button variant="ghost" size="sm" onclick={() => useForReceipts(p)}>Use for receipts</Button>
              {/if}
              <Button variant="ghost" size="sm" icon="x" aria-label="Remove {p.name}" onclick={() => removePrinter(p)} />
            </div>
          {/each}
          <form
            class="grid items-start gap-2.5"
            style="grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto"
            onsubmit={(e) => {
              e.preventDefault();
              addPrinter();
            }}
          >
            <input class="field" placeholder="Name, e.g. Grill" aria-label="New printer name" bind:value={np.name} aria-invalid={!!errs.printer || undefined} />
            <input class="field mono" placeholder="192.168.1.x" aria-label="New printer IP" bind:value={np.ip} />
            <Button type="submit">Add</Button>
          </form>
          {#if errs.printer}<p class="field-error !mt-0" role="alert">{errs.printer}</p>{/if}
        </div>
        <p class="help !m-0">The first printer prints receipts. Menu items pick their kitchen printer later.</p>
      {:else if step === 5}
        <div>
          <h1 tabindex="-1" class="m-0 text-[22px] font-bold focus:outline-none">Menu</h1>
          <p class="mt-1.5 text-muted">Import a CSV now, or skip and build it in the menu editor.</p>
        </div>
        <div
          role="group"
          aria-label="Menu CSV file"
          class="rounded-r14 border-[1.5px] border-dashed p-7 flex flex-col items-center gap-2.5 text-center transition-colors {dragOver
            ? 'border-brand bg-brand-soft'
            : 'border-border'}"
          ondragover={(e) => {
            e.preventDefault();
            dragOver = true;
          }}
          ondragleave={() => (dragOver = false)}
          ondrop={(e) => {
            e.preventDefault();
            dragOver = false;
            takeFile(e.dataTransfer?.files[0]);
          }}
        >
          <span class="text-accent"><Icon name="upload" size={28} /></span>
          <div class="font-semibold">Drop a menu CSV here</div>
          <div class="flex flex-wrap justify-center gap-2">
            <Button loading={busy} onclick={() => csvInput?.click()}>Choose file</Button>
            <a class="btn btn-g" href={template} download="menu-template.csv">Download template</a>
          </div>
          <input
            bind:this={csvInput}
            type="file"
            accept=".csv,text/csv"
            class="hidden"
            onchange={(e) => {
              const input = e.currentTarget as HTMLInputElement;
              takeFile(input.files?.[0]);
              input.value = '';
            }}
          />
        </div>
        {#if csvError}
          <div class="rounded-r12 px-4 py-3 bg-bad-bg text-bad" role="alert">
            <div class="font-semibold">Can't import {csvName}</div>
            <p class="m-0 mt-1 text-text2 whitespace-pre-line">{csvError}</p>
            {#if errMissingPrinters}
              <div class="flex flex-wrap gap-2 mt-3">
                <Button size="sm" icon="arrow-left" onclick={() => enter(4)}>Back to printers</Button>
                <Button size="sm" variant="ghost" onclick={skip}>Skip menu</Button>
              </div>
            {/if}
          </div>
        {/if}
        {#if csvPreview}
          <div class="card px-[18px] py-4 flex flex-col gap-2.5">
            <div class="flex items-center gap-2.5"><Badge tone="ok">Checked</Badge><span class="mono text-[13px]">{csvName}</span></div>
            <div class="grid grid-cols-3 gap-3">
              {#each [[csvPreview.menus, 'menu', 'menus'], [csvPreview.categories, 'category', 'categories'], [csvPreview.items + csvPreview.items_updated, 'item', 'items']] as [n, one, many] (one)}
                <div><div class="mono text-[22px] font-bold">{n}</div><div class="help !m-0">{n === 1 ? one : many}</div></div>
              {/each}
            </div>
            <ImportPrintSummary result={csvPreview} />
            <p class="help !m-0">Nothing is saved until you continue.</p>
          </div>
        {/if}
      {:else if step === 6 && created}
        <div class="flex items-center gap-3">
          <span class="w-10 h-10 rounded-full bg-ok-bg text-ok flex items-center justify-center"><Icon name="check" size={22} stroke={2.4} /></span>
          <div>
            <h1 tabindex="-1" class="m-0 text-[22px] font-bold focus:outline-none">{created.tenant.name} is ready</h1>
            <p class="mt-1 text-muted">Hand these to the manager.</p>
          </div>
        </div>
        <div class="card bg-elev px-[18px] py-1">
          <div class="kv"><span>Sign in at</span><span class="mono break-all">{loginUrl}</span></div>
          {#if created.tenant.has_login}
            <div class="kv"><span>Username</span><span class="mono">{created.tenant.username}</span></div>
            <div class="kv"><span>Password</span><span class="mono break-all">{password}</span></div>
          {:else}
            <div class="kv"><span>Login</span><span class="text-warn">None yet — set one on the store's Login tab before the hand-off.</span></div>
          {/if}
          <div class="kv"><span>Store ID</span><span class="mono">{created.tenant.store_id ?? '—'}</span></div>
        </div>
        <div>
          <div class="lbl">Next, at the store</div>
          <ol class="m-0 pl-5 flex flex-col gap-1.5 text-text2 list-decimal">
            <li>Plug in the Pi with a fresh card and open <span class="mono">http://quiccpos-pi</span>.</li>
            <li>Sign in there with the manager login — the Pi links itself.</li>
            <li>On the tablet, download Quibase from <span class="mono">utility.quiccpos.com</span> and follow its setup.</li>
          </ol>
        </div>
        <div class="flex flex-wrap gap-2.5">
          <Button icon="copy" onclick={copyAll}>Copy all</Button>
          {#if sessionOk}
            <Button variant="primary" icon="eye" href="{base}/store">Open store</Button>
          {:else}
            <Button variant="primary" href="{base}/admin/tenants/{created.tenant.id}">Open store page</Button>
          {/if}
          <Button variant="ghost" onclick={backToOverview}>Back to overview</Button>
        </div>
      {/if}

      {#if step < 6}
        <div class="flex justify-between gap-2.5 pt-[18px] border-t border-border">
          <Button variant="ghost" onclick={back} disabled={step === 1 || busy}>Back</Button>
          <div class="flex gap-2.5">
            {#if step >= 3 && step <= 5}<Button disabled={busy} onclick={skip}>Skip</Button>{/if}
            {#if !(step >= 3 && step <= 5 && !sessionOk)}
              {#if step <= 3}
                <Button type="submit" form="wiz-step" variant="primary" loading={busy} disabled={step === 3 && !loc}>{cta}</Button>
              {:else}
                <Button variant="primary" loading={busy} onclick={next}>{cta}</Button>
              {/if}
            {/if}
          </div>
        </div>
      {/if}
    </section>
  </div>
</div>

{#if created}
  <Dialog
    bind:open={laterOpen}
    title="Finish {created.tenant.name} later?"
    description="The store is already created. Hand these to the manager, or note them down — the password isn't shown again. The rest can be filled in from the store's page."
    width={500}
  >
    <div class="subcard px-4 py-1">
      <div class="kv"><span>Sign in at</span><span class="mono break-all">{loginUrl}</span></div>
      {#if created.tenant.has_login}
        <div class="kv"><span>Username</span><span class="mono">{created.tenant.username}</span></div>
        <div class="kv"><span>Password</span><span class="mono break-all">{password}</span></div>
      {:else}
        <div class="kv"><span>Login</span><span class="text-warn">None yet — set one on the store's Login tab.</span></div>
      {/if}
      <div class="kv"><span>Store ID</span><span class="mono">{created.tenant.store_id ?? '—'}</span></div>
    </div>
    {#snippet footer()}
      <Button icon="copy" class="mr-auto" onclick={copyAll}>Copy all</Button>
      <Button onclick={() => (laterOpen = false)}>Keep going</Button>
      <Button
        variant="primary"
        onclick={() => {
          laterOpen = false;
          backToOverview();
        }}>Leave</Button
      >
    {/snippet}
  </Dialog>
{/if}
