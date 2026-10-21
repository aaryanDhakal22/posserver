// Theme: follow the OS (`prefers-color-scheme`) until the person toggles; the
// toggle is remembered in localStorage (same key the old vanilla admin used).
// app.html applies it before first paint so there's no flash.
const STORAGE = 'quicc_admin_theme';

/** Reactive mirror of the current theme for toggles/labels. */
export const themeState = $state({
  dark: typeof document !== 'undefined' && document.documentElement.classList.contains('dark')
});

function stored(): 'dark' | 'light' | null {
  try {
    const t = localStorage.getItem(STORAGE);
    return t === 'dark' || t === 'light' ? t : null;
  } catch {
    return null;
  }
}

function systemDark(): boolean {
  return typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches;
}

function apply(dark: boolean): void {
  document.documentElement.classList.toggle('dark', dark);
  themeState.dark = dark;
}

/** Apply the theme and keep following the OS while nothing is remembered. */
export function initTheme(): () => void {
  if (typeof document === 'undefined') return () => {};
  const t = stored();
  apply(t ? t === 'dark' : systemDark());
  const mq = window.matchMedia('(prefers-color-scheme: dark)');
  const follow = (e: MediaQueryListEvent) => {
    if (stored()) return;
    apply(e.matches);
  };
  mq.addEventListener('change', follow);
  return () => mq.removeEventListener('change', follow);
}

export function toggleTheme(): boolean {
  const dark = !isDark();
  apply(dark);
  try {
    localStorage.setItem(STORAGE, dark ? 'dark' : 'light');
  } catch {
    /* private mode — the toggle still works for this page */
  }
  return dark;
}

export function isDark(): boolean {
  return typeof document !== 'undefined' && document.documentElement.classList.contains('dark');
}
