<script lang="ts">
  import type { Snippet } from 'svelte';

  // A calm block on a settings page: a heading, one line saying what it is for, then its fields.
  let { title, description = '', id, actions, children }: { title: string; description?: string; id?: string; actions?: Snippet; children: Snippet } =
    $props();
  const hid = $derived((id ?? title.toLowerCase().replace(/[^a-z0-9]+/g, '-')) + '-h');
</script>

<section class="card sec" aria-labelledby={hid}>
  <div class="flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <h2 class="sec-title" id={hid}>{title}</h2>
      {#if description}<p class="sec-desc">{description}</p>{/if}
    </div>
    {#if actions}<div class="flex flex-wrap items-center gap-2">{@render actions()}</div>{/if}
  </div>
  {@render children()}
</section>

<style>
  .sec {
    display: flex;
    flex-direction: column;
    gap: 18px;
    padding: 22px 24px 24px;
    min-width: 0;
  }
  @media (max-width: 520px) {
    .sec {
      padding: 18px 16px 20px;
    }
  }
  .sec-title {
    font-size: 15px;
    font-weight: 650;
    margin: 0;
  }
  .sec-desc {
    margin: 4px 0 0;
    font-size: 13px;
    color: var(--muted);
    max-width: 60ch;
  }
</style>
