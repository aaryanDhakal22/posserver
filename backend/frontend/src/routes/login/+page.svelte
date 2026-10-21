<script lang="ts">
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { adminSignIn, errText, safeNext, storeSignIn } from '$lib/api';
  import { themeState, toggleTheme } from '$lib/theme.svelte';
  import Brand from '$lib/ui/Brand.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Button from '$lib/ui/Button.svelte';

  // One sign-in form. Username "admin" → the password is the ADMIN_API_KEY
  // (checked against main, kept in localStorage); anyone else is a store
  // manager (session cookie). ?next= returns to where they were.
  let username = $state('');
  let password = $state('');
  let error = $state('');
  let busy = $state(false);

  const isAdmin = $derived(username.trim().toLowerCase() === 'admin');

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy) return;
    error = '';
    if (!username.trim() || !password) {
      error = isAdmin ? 'Paste the admin key.' : 'Enter your username and password.';
      return;
    }
    busy = true;
    const next = page.url.searchParams.get('next');
    try {
      if (isAdmin) {
        await adminSignIn(password);
        await goto(safeNext(next, '/admin') || base + '/admin', { replaceState: true });
      } else {
        await storeSignIn(username.trim(), password);
        await goto(safeNext(next, '/store') || base + '/store', { replaceState: true });
      }
    } catch (err) {
      error = errText(err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>Sign in — quicc</title></svelte:head>

<div class="min-h-screen flex flex-col">
  <div class="flex justify-end px-6 py-4">
    <button type="button" class="btn btn-sm" onclick={toggleTheme} aria-label="Toggle light or dark">
      <Icon name={themeState.dark ? 'sun' : 'moon'} size={16} />
      <span>{themeState.dark ? 'Light' : 'Dark'}</span>
    </button>
  </div>
  <main class="flex-1 flex items-center justify-center px-4 pt-6 pb-24">
    <div class="w-full max-w-[400px] flex flex-col gap-7">
      <div class="flex flex-col items-center gap-3.5">
        <Brand size={52} />
        <div class="text-center">
          <h1 class="m-0 text-2xl font-bold tracking-[-0.01em]">Sign in to quicc</h1>
          <p class="mt-1.5 text-muted">Manage your store, menu and printers.</p>
        </div>
      </div>
      <form class="card rounded-r16 p-7 flex flex-col gap-[18px]" onsubmit={submit} novalidate>
        <div>
          <label class="lbl" for="u">Username</label>
          <input
            id="u"
            class="field !min-h-[44px] !rounded-r12"
            bind:value={username}
            autocomplete="username"
            autocapitalize="off"
            spellcheck="false"
            placeholder="storename"
          />
        </div>
        <div>
          <label class="lbl" for="p">{isAdmin ? 'Admin key' : 'Password'}</label>
          <input
            id="p"
            type="password"
            class="field !min-h-[44px] !rounded-r12 {isAdmin ? 'mono' : ''}"
            bind:value={password}
            autocomplete={isAdmin ? 'off' : 'current-password'}
            aria-describedby={isAdmin ? 'p-help' : undefined}
            aria-invalid={error ? true : undefined}
          />
          {#if isAdmin}
            <p id="p-help" class="help">Paste the ADMIN_API_KEY from Doppler or your password manager.</p>
          {/if}
        </div>
        {#if error}
          <p class="m-0 text-[13px] font-medium text-bad flex items-start gap-2" role="alert">
            <Icon name="alert" size={16} class="mt-px" />{error}
          </p>
        {/if}
        <Button type="submit" variant="primary" size="lg" class="w-full" loading={busy}>
          {isAdmin ? 'Sign in as admin' : 'Sign in'}
        </Button>
      </form>
      <p class="m-0 text-center text-[13px] text-muted">Forgot your password? Ask Quicc support to reset it.</p>
    </div>
  </main>
</div>
