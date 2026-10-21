// Timezone options for the location timezone dropdown. US zones (the restaurants
// are US-based) plus UTC; New York is the default.
export const TIMEZONES = [
	'America/New_York',
	'America/Chicago',
	'America/Denver',
	'America/Phoenix',
	'America/Los_Angeles',
	'America/Anchorage',
	'Pacific/Honolulu',
	'UTC'
];

export const DEFAULT_TIMEZONE = 'America/New_York';

const TZ_LABELS: Record<string, string> = {
	'America/New_York': 'Eastern',
	'America/Chicago': 'Central',
	'America/Denver': 'Mountain',
	'America/Phoenix': 'Arizona',
	'America/Los_Angeles': 'Pacific',
	'America/Anchorage': 'Alaska',
	'Pacific/Honolulu': 'Hawaii',
	UTC: 'UTC'
};

/** "Eastern — America/New_York" (or the bare zone when unknown). */
export function tzLabel(tz: string): string {
	const l = TZ_LABELS[tz];
	return l && l !== tz ? `${l} — ${tz}` : tz;
}

/** Short name, "Eastern". */
export function tzShort(tz: string): string {
	return TZ_LABELS[tz] ?? tz;
}

/** 0–23 → "12:00 AM" … "11:00 PM" (the location's daily reset hour). */
export function hourLabel(h: number): string {
	const ampm = h < 12 ? 'AM' : 'PM';
	const hh = h % 12 === 0 ? 12 : h % 12;
	return `${hh}:00 ${ampm}`;
}
