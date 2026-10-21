<script lang="ts">
  import '@fontsource/inter/latin-400.css';
  import '@fontsource/inter/latin-500.css';
  import '@fontsource/inter/latin-600.css';
  import '@fontsource/inter/latin-700.css';
  import '@fontsource/jetbrains-mono/latin-500.css';
  import '@fontsource/jetbrains-mono/latin-700.css';
  import '@fontsource/jetbrains-mono/latin-800.css';
  import '../app.css';
  import { onMount } from 'svelte';
  import { initTheme } from '$lib/theme.svelte';
  import Toaster from '$lib/ui/Toaster.svelte';
  import ConfirmHost from '$lib/ui/ConfirmHost.svelte';

  // Shell-less root: /app/admin/** and /app/store/** bring their own sidebar
  // layouts (and auth gates); /app/login and /app have none.
  let { children } = $props();

  onMount(() => initTheme());
</script>

{@render children()}
<Toaster />
<ConfirmHost />
