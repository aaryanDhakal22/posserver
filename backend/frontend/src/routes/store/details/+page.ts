// Store › Details & hours moved into Store › Settings (a tile per category); old links land there.
import { redirect } from '@sveltejs/kit';
import { base } from '$app/paths';

export function load() {
  redirect(307, base + '/store/settings');
}
