<script lang="ts" module>
  export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'mute' | 'brand';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  let {
    tone = 'mute',
    dot = false,
    mono = false,
    title,
    class: cls = '',
    children
  }: { tone?: Tone; dot?: boolean; mono?: boolean; title?: string; class?: string; children: Snippet } = $props();
</script>

<span class="badge b-{tone} {mono ? 'mono' : ''} {cls}" {title}>
  {#if dot}<span class="dot" aria-hidden="true"></span>{/if}{@render children()}
</span>
