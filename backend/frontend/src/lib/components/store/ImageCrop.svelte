<script lang="ts">
  // Frame a logo (square) or cover photo (16:9) in the browser before it's uploaded: drag the
  // picture, zoom, then we draw the framed part to a canvas — a logo becomes a 512 × 512 PNG, a
  // cover a 1600 × 900 JPEG — so main only ever stores small files. (SVG logos skip this.)
  import Dialog from '$lib/ui/Dialog.svelte';
  import Button from '$lib/ui/Button.svelte';

  let {
    file = $bindable<File | null>(null),
    kind,
    ondone
  }: { file?: File | null; kind: 'logo' | 'cover'; ondone: (blob: Blob) => void } = $props();

  const W = $derived(kind === 'logo' ? 300 : 448); // the frame on screen
  const H = $derived(kind === 'logo' ? 300 : 252);
  const OUT = $derived(kind === 'logo' ? { w: 512, h: 512, type: 'image/png' } : { w: 1600, h: 900, type: 'image/jpeg' });

  let img = $state<HTMLImageElement | null>(null);
  let url = $state('');
  let zoom = $state(1); // 1 = the picture just covers the frame
  let x = $state(0); // the picture's offset from centred, in screen px
  let y = $state(0);
  let drag: { px: number; py: number; x: number; y: number } | null = null;

  $effect(() => {
    if (!file) return;
    const u = URL.createObjectURL(file);
    const i = new Image();
    i.onload = () => {
      img = i;
      zoom = 1;
      x = y = 0;
    };
    i.src = url = u;
    return () => URL.revokeObjectURL(u);
  });

  // screen px per picture px at zoom 1: cover the frame
  const base = $derived(img ? Math.max(W / img.naturalWidth, H / img.naturalHeight) : 1);
  const scale = $derived(base * zoom);
  const dw = $derived(img ? img.naturalWidth * scale : 0);
  const dh = $derived(img ? img.naturalHeight * scale : 0);
  // keep the frame covered
  const clamp = (v: number, size: number, frame: number) => Math.max(-(size - frame) / 2, Math.min((size - frame) / 2, v));
  const cx = $derived(clamp(x, dw, W));
  const cy = $derived(clamp(y, dh, H));

  function down(e: PointerEvent) {
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    drag = { px: e.clientX, py: e.clientY, x: cx, y: cy };
  }
  function move(e: PointerEvent) {
    if (!drag) return;
    x = drag.x + e.clientX - drag.px;
    y = drag.y + e.clientY - drag.py;
  }
  function key(e: KeyboardEvent) {
    const step = e.shiftKey ? 20 : 5;
    if (e.key === 'ArrowLeft') x = cx + step;
    else if (e.key === 'ArrowRight') x = cx - step;
    else if (e.key === 'ArrowUp') y = cy + step;
    else if (e.key === 'ArrowDown') y = cy - step;
    else return;
    e.preventDefault();
  }

  function use() {
    if (!img) return;
    const c = document.createElement('canvas');
    c.width = OUT.w;
    c.height = OUT.h;
    const g = c.getContext('2d')!;
    // the frame's top-left in picture px
    const sx = (dw / 2 - cx - W / 2) / scale;
    const sy = (dh / 2 - cy - H / 2) / scale;
    if (OUT.type === 'image/jpeg') {
      g.fillStyle = '#fff';
      g.fillRect(0, 0, c.width, c.height);
    }
    g.imageSmoothingQuality = 'high';
    g.drawImage(img, sx, sy, W / scale, H / scale, 0, 0, OUT.w, OUT.h);
    c.toBlob((b) => b && ondone(b), OUT.type, 0.86);
    file = null;
  }
</script>

<Dialog
  open={!!file}
  title={kind === 'logo' ? 'Frame your logo' : 'Frame your cover photo'}
  description={kind === 'logo' ? 'Drag the picture, zoom in or out. We keep a 512 × 512 copy.' : 'Drag the picture, zoom in or out. We keep a 16 : 9 copy.'}
  width={520}
  onclose={() => (file = null)}
>
  <div class="flex flex-col items-center gap-4">
    <div
      class="frame"
      class:round={kind === 'logo'}
      style="width:{W}px;height:{H}px"
      role="slider"
      tabindex="0"
      aria-label="Move the picture (arrow keys)"
      aria-valuenow={Math.round(zoom * 100)}
      onpointerdown={down}
      onpointermove={move}
      onpointerup={() => (drag = null)}
      onkeydown={key}
    >
      {#if img}<img src={url} alt="" draggable="false" style="width:{dw}px;height:{dh}px;transform:translate({cx}px,{cy}px)" />{/if}
    </div>
    <label class="flex items-center gap-3 w-full max-w-[360px]">
      <span class="lbl !m-0">Zoom</span>
      <input class="flex-1" type="range" min="1" max="4" step="0.01" bind:value={zoom} />
    </label>
  </div>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (file = null)}>Cancel</Button>
    <Button variant="primary" disabled={!img} onclick={use}>Use this</Button>
  {/snippet}
</Dialog>

<style>
  .frame { position: relative; overflow: hidden; display: grid; place-items: center; border-radius: 10px; background: var(--elev); cursor: grab; touch-action: none; outline-offset: 3px; }
  .frame.round { border-radius: 22%; }
  .frame:active { cursor: grabbing; }
  .frame img { position: absolute; max-width: none; user-select: none; pointer-events: none; }
</style>
