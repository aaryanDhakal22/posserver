<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { storeApi, type Staff } from '$lib/api';
  import CallcenterCard from '$lib/components/store/CallcenterCard.svelte';
  import SettingsPage from '$lib/components/settings/SettingsPage.svelte';

  // Store › Settings › Call center: the call-center app's idle lock, the computers it is open on and the
  // orders per agent (the card saves its own setting). The agents themselves are people on Store › Staff.
  let agents = $state(0);
  onMount(() =>
    storeApi<Staff[]>('/me/staff')
      .then((s) => (agents = s.filter((p) => p.role === 'callcenter' && p.active).length))
      .catch(() => {})
  );
</script>

<SettingsPage title="Call center" subtitle="The call-center app: its login, the computers it’s open on and what each agent took.">
  <p class="text-[13px] text-text2 m-0">Call-center agents are people with the Call center role on <a href="{base}/store/staff">Staff</a>.</p>
  <CallcenterCard {agents} />
</SettingsPage>
