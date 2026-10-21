<script lang="ts">
  import { DAYS, anyHours, type WeekHours } from '$lib/hours';

  // One open period per day; extra periods set through the API are kept.
  // Below `sm` each day's checkbox sits above its two time inputs, so the
  // full times (AM/PM included) fit on a phone.
  let { hours = $bindable(), idPrefix = 'hours' }: { hours: WeekHours; idPrefix?: string } = $props();

  // "Copy to all open days" copies the first open day's times (in week order).
  const first = $derived(DAYS.find(([d]) => hours[d]?.on));
  const openDays = $derived(DAYS.filter(([d]) => hours[d]?.on).length);

  function copyFirst() {
    if (!first) return;
    const src = hours[first[0]];
    for (const [d] of DAYS) if (d !== first[0] && hours[d]?.on) hours[d] = { ...hours[d], open: src.open, close: src.close };
  }
</script>

<div class="flex flex-col gap-2.5">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <span class="lbl !mb-0" id="{idPrefix}-lbl">Opening hours</span>
    <button
      type="button"
      class="btn btn-g btn-sm"
      disabled={openDays < 2}
      title={first ? `Copy ${first[2]}'s hours to every other open day` : 'Tick the days the store is open first'}
      onclick={copyFirst}
    >
      Copy {first ? `${first[2]}'s hours` : 'hours'} to all open days
    </button>
  </div>
  <p class="help !mt-0">
    If the Pi goes offline for 2 minutes while you're open, the owner gets a Pushover. A close time earlier than the open time
    runs past midnight.
    {#if !anyHours(hours)}<b class="text-warn">No hours set — the offline alert is off.</b>{/if}
  </p>
  <div class="flex flex-col gap-2" role="group" aria-labelledby="{idPrefix}-lbl">
    {#each DAYS as [d, , long]}
      {#if hours[d]}
        <div class="grid grid-cols-2 sm:grid-cols-[minmax(110px,140px)_minmax(0,1fr)_minmax(0,1fr)] items-center gap-x-2.5 gap-y-1">
          <label class="check-row col-span-2 sm:col-span-1 font-medium justify-self-start">
            <input type="checkbox" class="check" bind:checked={hours[d].on} />{long}{#if !hours[d].on}<span class="sm:hidden text-muted font-normal">· Closed</span>{/if}
          </label>
          {#if hours[d].on}
            <input type="time" class="field mono" bind:value={hours[d].open} aria-label="{long} opens" />
            <input type="time" class="field mono" bind:value={hours[d].close} aria-label="{long} closes" />
          {:else}
            <span class="text-muted col-span-2 hidden sm:block">Closed</span>
          {/if}
        </div>
        {#if hours[d].on && hours[d].extra.length}
          <p class="help !mt-0 sm:pl-[150px]">+{hours[d].extra.length} more period{hours[d].extra.length === 1 ? '' : 's'} set elsewhere — kept as is.</p>
        {/if}
      {/if}
    {/each}
  </div>
</div>
