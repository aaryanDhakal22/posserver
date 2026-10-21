<script lang="ts">
  // An on/off switch (role=switch). `onchange` gets the new value; bind:checked works too.
  // The button is a 44×44 hit area around the visual track (see .sw in app.css).
  let {
    checked = $bindable(false),
    label,
    disabled = false,
    onchange
  }: { checked?: boolean; label: string; disabled?: boolean; onchange?: (v: boolean) => void } = $props();

  function flip(e: MouseEvent) {
    e.stopPropagation();
    checked = !checked;
    onchange?.(checked);
  }
</script>

<button type="button" class="sw" role="switch" aria-checked={checked} aria-label={label} {disabled} onclick={flip}>
  <span><span></span></span>
</button>
