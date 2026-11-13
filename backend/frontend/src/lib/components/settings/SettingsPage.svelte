<script lang="ts">
  import type { Snippet } from 'svelte';
  import { beforeNavigate, goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { confirmDialog } from '$lib/confirm.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';

  // One Store › Settings page: a breadcrumb back to the tiles, a short calm body and (when the page
  // has something to save) one Save in a bar at the bottom that follows the screen while anything
  // is unsaved. Leaving with unsaved changes asks first.
  let {
    title,
    subtitle = '',
    loading = false,
    dirty = false,
    saving = false,
    error = '',
    saveLabel = 'Save',
    onsave,
    onundo,
    actions,
    children
  }: {
    title: string;
    subtitle?: string;
    loading?: boolean;
    dirty?: boolean;
    saving?: boolean;
    /** Shown in the save bar (role=alert): a bad value, or what a save did and didn't do. */
    error?: string;
    saveLabel?: string;
    /** The page saves with the bar's button (or Enter in a field). None = a page that saves as it goes. */
    onsave?: () => unknown;
    onundo?: () => void;
    actions?: Snippet;
    children: Snippet;
  } = $props();

  let leaving = false;
  beforeNavigate((nav) => {
    if (!dirty || leaving) return;
    if (nav.type === 'leave') {
      nav.cancel(); // the browser asks
      return;
    }
    nav.cancel();
    const to = nav.to?.url;
    confirmDialog({
      title: 'Leave without saving?',
      body: `Your changes to ${title} aren’t saved yet.`,
      confirmLabel: 'Leave',
      cancelLabel: 'Stay',
      danger: true
    }).then((ok) => {
      if (ok && to) {
        leaving = true;
        goto(to);
      }
    });
  });

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!saving) onsave?.();
  }
</script>

<svelte:head><title>{title} — Settings — quicc</title></svelte:head>

<PageHeader crumbs={[{ label: 'Settings', href: base + '/store/settings' }, { label: title }]} {title} {subtitle} {actions} />

<div class="settings-body">
  {#if loading}
    <div class="card p-6"><Skeleton lines={5} /></div>
  {:else if onsave}
    <form class="flex flex-col gap-5" onsubmit={submit} novalidate>
      {@render children()}
      <div class="savebar" class:is-dirty={dirty || !!error} data-testid="settings-savebar">
        <div class="min-w-0 flex-1">
          {#if error}
            <p class="field-error !m-0" role="alert">{error}</p>
          {:else if dirty}
            <span class="text-warn font-medium text-[13px]">Unsaved changes</span>
          {:else}
            <span class="text-muted text-[13px]">All changes saved</span>
          {/if}
        </div>
        {#if dirty && onundo}<Button variant="ghost" icon="undo" onclick={onundo}>Undo changes</Button>{/if}
        <Button type="submit" variant="primary" loading={saving} disabled={!dirty && !error}>{saveLabel}</Button>
      </div>
    </form>
  {:else}
    <div class="flex flex-col gap-5">{@render children()}</div>
  {/if}
</div>

<style>
  .settings-body {
    max-width: 720px;
    width: 100%;
  }
  /* At the end of the form; while something is unsaved it follows the screen's bottom edge. */
  .savebar {
    position: static;
    z-index: 5;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: 10px;
    padding: 10px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 14px;
    box-shadow: var(--shadow);
  }
  .savebar.is-dirty {
    position: sticky;
    bottom: 12px;
    border-color: var(--warn);
  }
</style>
