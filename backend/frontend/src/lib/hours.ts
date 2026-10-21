// Opening hours live in the location config as
//   config.opening_hours = { mon: [{open:"HH:MM", close:"HH:MM"}, …], … sun }
// The editor shows one open period per day; extra periods set through the API
// are kept as they are. main pages the owner when the Pi is offline for 2+
// minutes inside these hours (a close earlier than the open runs past midnight).

export const DAYS: [string, string, string][] = [
  ['mon', 'Mon', 'Monday'],
  ['tue', 'Tue', 'Tuesday'],
  ['wed', 'Wed', 'Wednesday'],
  ['thu', 'Thu', 'Thursday'],
  ['fri', 'Fri', 'Friday'],
  ['sat', 'Sat', 'Saturday'],
  ['sun', 'Sun', 'Sunday']
];

export type HoursRange = { open: string; close: string };
export type DayHours = { on: boolean; open: string; close: string; extra: HoursRange[] };
export type WeekHours = Record<string, DayHours>;

export function hoursFromConfig(config: Record<string, unknown> | null | undefined): WeekHours {
  const oh = ((config ?? {}) as Record<string, unknown>).opening_hours as Record<string, HoursRange[]> | undefined;
  const next: WeekHours = {};
  for (const [d] of DAYS) {
    const ranges = Array.isArray(oh?.[d]) ? oh![d] : [];
    next[d] = {
      on: ranges.length > 0,
      open: ranges[0]?.open ?? '11:00',
      close: ranges[0]?.close ?? '23:00',
      extra: ranges.slice(1)
    };
  }
  return next;
}

export function hoursToConfig(hours: WeekHours): Record<string, HoursRange[]> {
  const out: Record<string, HoursRange[]> = {};
  for (const [d] of DAYS) {
    const h = hours[d];
    out[d] = h?.on ? [{ open: h.open, close: h.close }, ...h.extra] : [];
  }
  return out;
}

export function anyHours(hours: WeekHours): boolean {
  return Object.values(hours).some((h) => h.on);
}

/** "11:00 – 22:00" for today (in the browser's day), "Closed", or "—" when no hours are set. */
export function todayHours(config: Record<string, unknown> | null | undefined): string {
  const hours = hoursFromConfig(config);
  if (!anyHours(hours)) return '—';
  const d = DAYS[(new Date().getDay() + 6) % 7][0];
  const h = hours[d];
  return h.on ? `${h.open} – ${h.close}` : 'Closed';
}
