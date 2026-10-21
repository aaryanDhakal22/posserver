<script lang="ts">
  import { base } from '$app/paths';
  import { errText, plural, storeApi, type MenuImportResult } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Dialog from '$lib/ui/Dialog.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import ImportPrintSummary from './ImportPrintSummary.svelte';

  // CSV import: pick a file → dry-run preview → Apply (or Replace everything).
  let { open = $bindable(false), onimported }: { open?: boolean; onimported: () => void } = $props();

  let input = $state<HTMLInputElement>();
  let text = $state('');
  let name = $state('');
  let replace = $state(false);
  let preview = $state<MenuImportResult | null>(null);
  let busy = $state(false);
  let dragOver = $state(false);
  // The last failure (bad row, unknown printer…) stays until the next attempt.
  let error = $state('');
  let errorOnApply = $state(false);

  function reset() {
    text = '';
    name = '';
    replace = false;
    preview = null;
    error = '';
  }

  async function post(dryRun: boolean): Promise<MenuImportResult> {
    const q = new URLSearchParams({ dry_run: dryRun ? '1' : '0', replace: replace ? '1' : '0' });
    return storeApi<MenuImportResult>(`/me/menu/import?${q}`, { method: 'POST', body: text, headers: { 'Content-Type': 'text/csv' } });
  }
  async function runPreview() {
    if (busy || !text) return;
    busy = true;
    error = '';
    try {
      preview = await post(true);
    } catch (e) {
      preview = null;
      error = errText(e);
      errorOnApply = false;
    } finally {
      busy = false;
    }
  }
  async function take(f: File | undefined) {
    if (!f || busy) return;
    name = f.name;
    text = await f.text();
    replace = false;
    await runPreview();
  }
  async function apply() {
    if (busy || !preview) return;
    if (replace) {
      const ok = await confirmDialog({
        title: 'Replace the whole menu?',
        body: 'Replace deletes every menu here (categories, items, item printers) and rebuilds it from the file.',
        confirmLabel: 'Replace menu',
        danger: true
      });
      if (!ok) return;
    }
    busy = true;
    error = '';
    try {
      const r = await post(false);
      toast.success(
        `Imported: ${plural(r.items, 'new item')}, ${r.items_updated} updated, ${plural(r.item_printers + r.category_printers, 'printer change')}`
      );
      reset();
      open = false;
      onimported();
    } catch (e) {
      error = errText(e);
      errorOnApply = true;
    } finally {
      busy = false;
    }
  }
  const missingPrinters = $derived(/printers that this store doesn't have/i.test(error));
  const summary = (r: MenuImportResult) =>
    [
      r.menus_deleted && `${plural(r.menus_deleted, 'menu')} deleted`,
      r.menus && plural(r.menus, 'new menu'),
      r.categories && plural(r.categories, 'new category', 'new categories'),
      r.items && plural(r.items, 'new item'),
      r.items_updated && `${plural(r.items_updated, 'item')} updated`,
      (r.item_printers || r.category_printers) && plural(r.item_printers + r.category_printers, 'printer change')
    ]
      .filter(Boolean)
      .join(' · ') || 'no changes';
</script>

<Dialog bind:open title="Import menu CSV" width={600} onclose={reset}>
  <div
    role="group"
    aria-label="Menu CSV file"
    class="rounded-r14 border-[1.5px] border-dashed p-6 flex flex-col items-center gap-2 text-center transition-colors {dragOver ? 'border-brand bg-brand-soft' : 'border-border'}"
    ondragover={(e) => {
      e.preventDefault();
      dragOver = true;
    }}
    ondragleave={() => (dragOver = false)}
    ondrop={(e) => {
      e.preventDefault();
      dragOver = false;
      take(e.dataTransfer?.files[0]);
    }}
  >
    <span class="text-accent"><Icon name="upload" size={26} /></span>
    <div class="font-semibold">{name || 'Drop a menu CSV here'}</div>
    <p class="m-0 text-[13px] text-muted">Columns: menu, category, item, price, printer, description.</p>
    <Button size="sm" loading={busy && !preview} onclick={() => input?.click()}>{name ? 'Choose another file' : 'Choose file'}</Button>
    <input
      bind:this={input}
      type="file"
      accept=".csv,text/csv"
      class="hidden"
      onchange={(e) => {
        const el = e.currentTarget as HTMLInputElement;
        take(el.files?.[0]);
        el.value = '';
      }}
    />
  </div>

  {#if error}
    <div class="rounded-r12 px-4 py-3 bg-bad-bg" role="alert">
      <div class="font-semibold text-bad">Import failed{name ? ` — ${name}` : ''}</div>
      <p class="my-1 text-text2 whitespace-pre-line">{error}</p>
      {#if missingPrinters}
        <div class="my-2"><Button size="sm" icon="printer" href="{base}/store/printers">Open Printers</Button></div>
      {/if}
      <p class="m-0 text-[13px] text-text2">
        {errorOnApply
          ? 'The import may have stopped part-way. Fix the problem and import the same file again — a re-import picks up where it stopped.'
          : 'Nothing was changed. Fix the file (or add the missing printers on the Printers page) and try again.'}
      </p>
    </div>
  {/if}

  {#if preview}
    <div class="flex flex-col gap-2">
      <div class="flex flex-wrap items-center gap-2"><Badge tone="ok">Checked</Badge><span class="text-[13px] text-muted">{preview.rows} rows</span></div>
      <div class="font-medium">{summary(preview)}</div>
      <ImportPrintSummary result={preview} />
      <p class="m-0 text-[13px] text-muted">
        Items match by name (case/spaces ignored). Blank price/printer cells keep what's there; nothing is deleted unless you tick Replace.
      </p>
      {#if preview.changes.length}
        <pre class="code subcard px-3 py-2 max-h-56 overflow-auto">{preview.changes.join('\n')}</pre>
      {/if}
      <label class="flex items-center gap-2 mt-1">
        <input type="checkbox" class="check" disabled={busy} bind:checked={replace} onchange={runPreview} />
        Replace — delete all menus first
      </label>
    </div>
  {/if}

  {#snippet footer()}
    <Button href="/store/v1/me/menu/export.csv" download="menu.csv" variant="ghost" icon="download">Export current menu</Button>
    <span class="flex-1"></span>
    <Button onclick={() => (open = false)}>Cancel</Button>
    <Button variant={replace ? 'danger' : 'primary'} disabled={!preview} loading={busy && !!preview} onclick={apply}>
      {replace ? 'Replace menu' : 'Apply'}
    </Button>
  {/snippet}
</Dialog>
