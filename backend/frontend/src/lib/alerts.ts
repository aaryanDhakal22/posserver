// The store owner's alert phones live in the location config as
//   config.pushover_owner_devices = "OwnerPhone,Tablet2"
// (Pushover device names on the store's account). The Pi sends floor problems
// to them; main checks the same rule on save (400 bad_device).

export const OWNER_DEVICES_KEY = 'pushover_owner_devices';

const DEVICE_RE = /^[A-Za-z0-9_-]{1,25}$/;

/** Trimmed, de-duplicated names from a comma list (empty items dropped). */
export function splitDevices(raw: string): string[] {
  const out: string[] = [];
  for (const d of raw.split(',').map((s) => s.trim())) {
    if (d && !out.includes(d)) out.push(d);
  }
  return out;
}

/** The first name Pushover would refuse, or null when the list is fine. */
export function badDevice(raw: string): string | null {
  return splitDevices(raw).find((d) => !DEVICE_RE.test(d)) ?? null;
}

/** The saved list as the input shows it. */
export function devicesFromConfig(config: Record<string, unknown> | null | undefined): string {
  const v = config?.[OWNER_DEVICES_KEY];
  return typeof v === 'string' ? splitDevices(v).join(', ') : '';
}
