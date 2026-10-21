// The signed-in store (whoami), loaded once by the store layout and read by
// every /app/store page.
import type { Whoami } from '$lib/api';

export const storeSession = $state<{ who: Whoami | null }>({ who: null });
