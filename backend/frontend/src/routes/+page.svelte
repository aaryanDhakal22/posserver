<script lang="ts">
  import { base } from '$app/paths';
</script>

<div class="max-w-xl mx-auto mt-12 space-y-4">
  <h1 class="text-2xl font-semibold">quicc</h1>
  <p class="text-slate-500 dark:text-slate-400">Choose a surface:</p>
  <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
    <a href="{base}/admin" class="card px-5 py-4 hover:border-emerald-500/50 transition-colors">
      <div class="font-medium">Admin</div>
      <div class="text-sm text-slate-500 dark:text-slate-400">
        God-mode: tenants, locations, agent devices. Needs the ADMIN_API_KEY.
      </div>
    </a>
    <a href="{base}/store" class="card px-5 py-4 hover:border-emerald-500/50 transition-colors">
      <div class="font-medium">Store manager</div>
      <div class="text-sm text-slate-500 dark:text-slate-400">
        Log in to manage your store: location, printers, devices.
      </div>
    </a>
  </div>
</div>
