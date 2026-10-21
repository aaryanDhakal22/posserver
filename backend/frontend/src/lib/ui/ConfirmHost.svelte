<script lang="ts">
  import { confirmState } from '$lib/confirm.svelte';
  import Dialog from './Dialog.svelte';
  import Button from './Button.svelte';

  const c = $derived(confirmState.current);
  let typed = $state('');
  let open = $state(false);

  $effect(() => {
    if (c) {
      typed = '';
      open = true;
    } else {
      open = false;
    }
  });
  const blocked = $derived(!!c?.typeToConfirm && typed !== c.typeToConfirm);
</script>

{#if c}
  <Dialog bind:open title={c.title} description={c.body} onclose={() => confirmState.settle(false)}>
    {#if c.typeToConfirm}
      <div>
        <label class="lbl" for="confirm-type">Type <b>{c.typeToConfirm}</b> to confirm</label>
        <input id="confirm-type" class="field" bind:value={typed} autocomplete="off" />
      </div>
    {/if}
    {#snippet footer()}
      <Button onclick={() => confirmState.settle(false)}>{c.cancelLabel ?? 'Cancel'}</Button>
      <Button variant={c.danger ? 'danger' : 'primary'} disabled={blocked} onclick={() => confirmState.settle(true)}>
        {c.confirmLabel ?? 'Confirm'}
      </Button>
    {/snippet}
  </Dialog>
{/if}
