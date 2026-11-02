<script lang="ts">
  import { onMount } from 'svelte';
  import { storeApi, type OrderingSettings, type HoursException } from '$lib/api';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Switch from '$lib/ui/Switch.svelte';

  // Dates that are closed or keep their own hours (holidays, events, an
  // early close). Saving a date that already has one replaces it.
  let list = $state<HoursException[]>([]);
  let fresh = $state({ date: '', closed: true, open: '11:00', close: '21:00', note: '' });
  let saving = $state(false);

  async function load() {
    list = (await storeApi<{ settings: OrderingSettings; exceptions: HoursException[] }>('/me/ordering')).exceptions;
  }
  onMount(() => load().catch((e) => toast.error(e)));

  function fmtDate(d: string) {
    const [y, m, day] = d.split('-').map(Number);
    return new Date(y, m - 1, day).toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' });
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    if (!fresh.date || saving) return;
    saving = true;
    try {
      await storeApi('/me/hours-exceptions', {
        method: 'POST',
        body: JSON.stringify({
          date: fresh.date,
          closed: fresh.closed,
          hours: fresh.closed ? [] : [{ open: fresh.open, close: fresh.close }],
          note: fresh.note
        })
      });
      toast.success(`${fmtDate(fresh.date)} saved`);
      fresh = { ...fresh, date: '', note: '' };
      await load();
    } catch (err) {
      toast.error(err);
    } finally {
      saving = false;
    }
  }
  async function del(x: HoursException) {
    if (!(await confirmDialog({ title: `Remove ${fmtDate(x.date)}?`, body: 'That day goes back to the weekly hours.', confirmLabel: 'Remove' }))) return;
    try {
      await storeApi(`/me/hours-exceptions/${x.id}`, { method: 'DELETE' });
      await load();
    } catch (err) {
      toast.error(err);
    }
  }
</script>

<Card title="Special days" subtitle="Holidays, events, an early close. They override the weekly hours for that date.">
  <div class="flex flex-col gap-3">
    <ul class="flex flex-col gap-1.5">
      {#each list as x (x.id)}
        <li class="subcard flex flex-wrap items-center gap-2 px-3 py-2">
          <span class="font-medium min-w-[110px]">{fmtDate(x.date)}</span>
          {#if x.closed}<Badge tone="warn">Closed</Badge>{:else}<Badge tone="mute">{x.hours.map((h) => `${h.open}–${h.close}`).join(', ')}</Badge>{/if}
          <span class="flex-1 min-w-0 truncate text-text2 text-[13px]">{x.note}</span>
          <Button size="xs" variant="ghost" icon="x" aria-label="Remove {fmtDate(x.date)}" onclick={() => del(x)} />
        </li>
      {:else}
        <li class="text-[13px] text-muted">None coming up.</li>
      {/each}
    </ul>
    <form class="flex flex-wrap items-end gap-2.5 pt-3 border-t border-border" onsubmit={add}>
      <div>
        <label class="lbl" for="sp-date">Date</label>
        <input id="sp-date" type="date" class="field !w-auto" bind:value={fresh.date} required />
      </div>
      <label class="flex items-center gap-2 pb-2"><Switch bind:checked={fresh.closed} label="Closed all day" />Closed</label>
      {#if !fresh.closed}
        <div>
          <label class="lbl" for="sp-open">Open</label>
          <input id="sp-open" type="time" class="field !w-auto" bind:value={fresh.open} />
        </div>
        <div>
          <label class="lbl" for="sp-close">Close</label>
          <input id="sp-close" type="time" class="field !w-auto" bind:value={fresh.close} />
        </div>
      {/if}
      <div class="flex-1 min-w-[160px]">
        <label class="lbl" for="sp-note">Note</label>
        <input id="sp-note" class="field" bind:value={fresh.note} placeholder="Thanksgiving" maxlength="140" />
      </div>
      <Button type="submit" icon="plus" loading={saving} disabled={!fresh.date}>Add</Button>
    </form>
  </div>
</Card>
