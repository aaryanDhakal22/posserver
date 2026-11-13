<script lang="ts">
  // A number with its unit inside the box ($ in front, %, min, sec … behind), a label above and the
  // help (with the default) under it.
  let {
    id,
    label,
    unit = '',
    prefix = false,
    decimal = false,
    help = '',
    invalid = false,
    disabled = false,
    placeholder = '',
    value = $bindable()
  }: {
    id: string;
    label: string;
    unit?: string;
    prefix?: boolean;
    decimal?: boolean;
    help?: string;
    invalid?: boolean;
    disabled?: boolean;
    placeholder?: string;
    value: string;
  } = $props();
</script>

<div class="uf">
  <label class="lbl" for={id}>{label}{#if unit}<span class="sr-only"> ({unit})</span>{/if}</label>
  <div class="unit-field" class:prefix class:has-unit={!!unit}>
    <input
      {id}
      class="field mono"
      inputmode={decimal ? 'decimal' : 'numeric'}
      autocomplete="off"
      {placeholder}
      {disabled}
      bind:value
      aria-invalid={invalid || undefined}
      aria-describedby={help ? id + '-help' : undefined}
    />
    {#if unit}<span class="unit" aria-hidden="true">{unit}</span>{/if}
  </div>
  {#if help}<p class="help" id="{id}-help">{help}</p>{/if}
</div>

<style>
  .uf {
    min-width: 0;
  }
  .uf .help {
    margin-top: 6px;
    max-width: 52ch;
  }
  .unit-field {
    position: relative;
    max-width: 200px; /* a number sits next to its label, not across the screen */
  }
  .unit-field.has-unit .field {
    padding-right: 52px;
    text-align: right;
  }
  .unit-field.prefix .field {
    padding-right: 12px;
    padding-left: 26px;
  }
  .unit {
    position: absolute;
    top: 50%;
    right: 12px;
    transform: translateY(-50%);
    color: var(--muted);
    font-size: 13px;
    pointer-events: none;
  }
  .unit-field.prefix .unit {
    right: auto;
    left: 12px;
  }
</style>
