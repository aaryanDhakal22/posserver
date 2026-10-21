<script lang="ts" module>
  import type { Location } from '$lib/api';

  export interface LocationForm {
    name: string;
    phone: string;
    address_line1: string;
    address_line2: string;
    timezone: string;
    daily_reset_hour: number;
  }
  export function locationForm(l: Location): LocationForm {
    return {
      name: l.name,
      phone: l.phone,
      address_line1: l.address_line1,
      address_line2: l.address_line2,
      timezone: l.timezone,
      daily_reset_hour: l.daily_reset_hour
    };
  }
</script>

<script lang="ts">
  import { TIMEZONES, tzLabel, hourLabel } from '$lib/timezones';

  // The editable location fields, shared by the store details page, the admin
  // store page and the new-store wizard.
  let {
    value = $bindable(),
    showName = true,
    showReset = true,
    idPrefix = 'loc'
  }: { value: LocationForm; showName?: boolean; showReset?: boolean; idPrefix?: string } = $props();

  const tzs = $derived(TIMEZONES.includes(value.timezone) || !value.timezone ? TIMEZONES : [value.timezone, ...TIMEZONES]);
</script>

<div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
  {#if showName}
    <div>
      <label class="lbl" for="{idPrefix}-name">Location name</label>
      <input id="{idPrefix}-name" class="field" bind:value={value.name} />
    </div>
  {/if}
  <div>
    <label class="lbl" for="{idPrefix}-phone">Phone</label>
    <input id="{idPrefix}-phone" class="field mono" type="tel" bind:value={value.phone} autocomplete="off" />
  </div>
  <div>
    <label class="lbl" for="{idPrefix}-a1">Address</label>
    <input id="{idPrefix}-a1" class="field" bind:value={value.address_line1} autocomplete="off" />
  </div>
  <div>
    <label class="lbl" for="{idPrefix}-a2">City, state, ZIP</label>
    <input id="{idPrefix}-a2" class="field" bind:value={value.address_line2} autocomplete="off" />
  </div>
  <div>
    <label class="lbl" for="{idPrefix}-tz">Time zone</label>
    <select id="{idPrefix}-tz" class="field" bind:value={value.timezone}>
      {#each tzs as tz}<option value={tz}>{tzLabel(tz)}</option>{/each}
    </select>
  </div>
  {#if showReset}
    <div>
      <label class="lbl" for="{idPrefix}-reset">New day starts at</label>
      <select id="{idPrefix}-reset" class="field" bind:value={value.daily_reset_hour}>
        {#each Array(24) as _, h}<option value={h}>{hourLabel(h)}</option>{/each}
      </select>
    </div>
  {/if}
</div>
