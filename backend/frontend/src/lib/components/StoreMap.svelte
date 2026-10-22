<script lang="ts">
  import { onMount } from 'svelte';
  import L from 'leaflet';
  import 'leaflet/dist/leaflet.css';
  import type { Location } from '$lib/api';
  import Card from '$lib/ui/Card.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Badge from '$lib/ui/Badge.svelte';

  // The store pin on a small map (OSM raster tiles — rarely used, admin-ish
  // page). Drag the pin (or click the map) to the door, then "Use this spot":
  // that saves it by hand (geo_source manual) so address changes never move it.
  // The circle is the delivery radius; past it orders are tagged "outside area".
  let {
    loc,
    onsave,
    idPrefix = 'sm'
  }: { loc: Location; onsave: (lat: number, lng: number) => Promise<void>; idPrefix?: string } = $props();

  const FALLBACK = { lat: 40.6687, lng: -74.1143 }; // Bayonne, NJ — before the store has a position
  const TILES = 'https://tile.openstreetmap.org/{z}/{x}/{y}.png';
  const ATTRIBUTION = '&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noreferrer">OpenStreetMap</a> contributors';

  let el: HTMLDivElement;
  let map: L.Map | undefined;
  let marker: L.Marker | undefined;
  let circle: L.Circle | undefined;
  let pin = $state<{ lat: number; lng: number } | null>(null); // moved, not saved yet
  let saving = $state(false);

  const saved = $derived(loc.lat != null && loc.lng != null ? { lat: loc.lat, lng: loc.lng } : null);
  const source = $derived(
    loc.geo_source === 'manual'
      ? 'Placed by hand — changing the address will not move it.'
      : loc.geo_source === 'geocoded'
        ? 'Found from the address. Drag it to the door if it is off.'
        : 'Not placed yet. Save the address above, or drag the pin and press Use this spot.'
  );
  const miles = $derived((loc.delivery_radius_m / 1609.344).toFixed(1).replace(/\.0$/, ''));

  const round6 = (n: number) => Math.round(n * 1e6) / 1e6;

  function place(p: L.LatLng, dirty = true) {
    marker?.setLatLng(p);
    circle?.setLatLng(p);
    if (dirty) pin = { lat: round6(p.lat), lng: round6(p.lng) };
  }

  onMount(() => {
    const start = saved ?? FALLBACK;
    map = L.map(el, { scrollWheelZoom: false }).setView([start.lat, start.lng], saved ? 15 : 12);
    L.tileLayer(TILES, { maxZoom: 19, attribution: ATTRIBUTION }).addTo(map);
    const icon = L.divIcon({ className: 'store-pin', html: '<span></span>', iconSize: [26, 26], iconAnchor: [13, 26] });
    marker = L.marker([start.lat, start.lng], { draggable: true, icon, title: 'Store', keyboard: true }).addTo(map);
    circle = L.circle([start.lat, start.lng], {
      radius: loc.delivery_radius_m,
      color: getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#6d4fd4',
      weight: 1,
      fillOpacity: 0.05,
      interactive: false
    }).addTo(map);
    marker.on('drag', () => circle?.setLatLng(marker!.getLatLng()));
    marker.on('dragend', () => place(marker!.getLatLng()));
    map.on('click', (e: L.LeafletMouseEvent) => place(e.latlng));
    return () => map?.remove();
  });

  // A new saved position (geocoded in the background, or saved elsewhere)
  // moves the pin unless the person is mid-edit.
  $effect(() => {
    const s = saved;
    if (!s || pin || !map) return;
    place(L.latLng(s.lat, s.lng), false);
  });

  function reset() {
    pin = null;
    const s = saved ?? FALLBACK;
    place(L.latLng(s.lat, s.lng), false);
    map?.setView([s.lat, s.lng], saved ? 15 : 12);
  }

  function showArea() {
    if (circle && map) map.fitBounds(circle.getBounds(), { padding: [12, 12] });
  }

  async function use() {
    if (!pin || saving) return;
    saving = true;
    try {
      await onsave(pin.lat, pin.lng);
      pin = null;
    } finally {
      saving = false;
    }
  }
</script>

<Card title="Store location" subtitle="Where drivers start. Delivery maps on the tablet route from this pin.">
  <div class="flex flex-col gap-3">
    <div bind:this={el} class="store-map" id="{idPrefix}-map" role="application" aria-label="Store location map"></div>
    <p class="text-[13px] text-text2 m-0">{source}</p>
    <div class="flex flex-wrap items-center gap-2 text-[13px]">
      {#if saved}
        <span class="mono text-muted">{saved.lat.toFixed(5)}, {saved.lng.toFixed(5)}</span>
      {:else}
        <Badge tone="warn">no position</Badge>
      {/if}
      <span class="text-muted">· delivery area {miles} mi</span>
      {#if loc.map}
        <Badge tone="ok" title={loc.map.url}>offline map {loc.map.version}</Badge>
      {:else}
        <Badge tone="mute" title="The console's map-build publishes one for the Pi">no offline map yet</Badge>
      {/if}
    </div>
    <div class="flex flex-wrap gap-2">
      <Button variant="primary" icon="check" loading={saving} disabled={!pin} onclick={use}>Use this spot</Button>
      <Button variant="ghost" disabled={!pin} onclick={reset}>Undo move</Button>
      <Button variant="ghost" icon="layers" onclick={showArea}>Show delivery area</Button>
    </div>
  </div>
</Card>

<style>
  .store-map {
    height: 320px;
    border-radius: 10px;
    border: 1px solid var(--border);
    overflow: hidden;
    z-index: 0;
  }
  .store-map :global(.leaflet-control-attribution) {
    font: 11px/1.4 system-ui, sans-serif;
  }
  :global(html.dark) .store-map :global(.leaflet-tile-pane) {
    filter: invert(1) hue-rotate(180deg) brightness(0.85) contrast(0.9);
  }
  .store-map :global(.store-pin span) {
    display: block;
    width: 26px;
    height: 26px;
    border-radius: 50% 50% 50% 0;
    transform: rotate(-45deg);
    background: var(--accent);
    border: 3px solid #fff;
    box-shadow: 0 2px 6px rgba(0, 0, 0, 0.35);
  }
</style>
