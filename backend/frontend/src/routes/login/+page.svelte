<script lang="ts">
  import { base } from '$app/paths';
  import { goto } from '$app/navigation';
  import { storeApi, ApiError } from '$lib/api';

  let username = $state('');
  let password = $state('');
  let error = $state('');
  let busy = $state(false);

  async function login(e: Event) {
    e.preventDefault();
    error = '';
    busy = true;
    try {
      await storeApi('/session', { method: 'POST', body: JSON.stringify({ username, password }) });
      await goto(base + '/store');
    } catch (err) {
      error = err instanceof ApiError ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="max-w-sm mx-auto mt-16 card px-6 py-6">
  <h1 class="text-lg font-semibold mb-1">Store login</h1>
  <p class="text-xs text-slate-500 dark:text-slate-400 mb-4">
    Manage your store's location, printers and devices.
  </p>
  <form onsubmit={login} class="space-y-3">
    <div>
      <label class="lbl" for="u">Username</label>
      <input id="u" class="field w-full" bind:value={username} autocomplete="username" />
    </div>
    <div>
      <label class="lbl" for="p">Password</label>
      <input
        id="p"
        type="password"
        class="field w-full"
        bind:value={password}
        autocomplete="current-password"
      />
    </div>
    <button class="btn btn-primary w-full justify-center" disabled={busy}>
      {busy ? '…' : 'Log in'}
    </button>
    {#if error}<div class="text-sm text-rose-600 dark:text-rose-400">{error}</div>{/if}
  </form>
</div>
