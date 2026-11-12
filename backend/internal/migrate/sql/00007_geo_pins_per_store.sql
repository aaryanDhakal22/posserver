-- A map pin belongs to the store that set it (security review, 2026-11-12).
-- geo.pin / geo.choose wrote the pinned geo_cache row under the address's own
-- key, which every store's lookups read: one store's manager could move
-- where another store's drivers are sent for the same address. Pins are now
-- kept under 'pin|<tenant_id>|<key>' (app/geo PinCacheKey), read before the
-- shared geocode entry. The pins made so far are handed to every store there
-- is today (which store set one was never recorded), then leave the shared key.

-- +goose Up
INSERT INTO public.geo_cache (address_key, lat, lng, quality, source, pinned, updated_at)
SELECT 'pin|' || t.id::text || '|' || g.address_key, g.lat, g.lng, g.quality, g.source, true, g.updated_at
FROM public.geo_cache g CROSS JOIN public.tenants t
WHERE g.pinned AND g.address_key NOT LIKE 'pin|%'
ON CONFLICT (address_key) DO NOTHING;
DELETE FROM public.geo_cache WHERE pinned AND address_key NOT LIKE 'pin|%';

-- +goose Down
-- The newest pin of each address goes back under the shared key.
INSERT INTO public.geo_cache (address_key, lat, lng, quality, source, pinned, updated_at)
SELECT DISTINCT ON (k) k, lat, lng, quality, source, true, updated_at
FROM (SELECT substr(address_key, 42) AS k, lat, lng, quality, source, updated_at
      FROM public.geo_cache WHERE address_key LIKE 'pin|%') p
ORDER BY k, updated_at DESC
ON CONFLICT (address_key) DO UPDATE SET lat = EXCLUDED.lat, lng = EXCLUDED.lng, quality = EXCLUDED.quality,
    source = EXCLUDED.source, pinned = true, updated_at = EXCLUDED.updated_at;
DELETE FROM public.geo_cache WHERE address_key LIKE 'pin|%';
