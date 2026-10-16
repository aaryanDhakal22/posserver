// Dark-first theme, persisted to localStorage (same key the old vanilla admin
// used, so the preference carries over).
const STORAGE = 'quicc_admin_theme';

export function initTheme(): void {
  if (typeof document === 'undefined') return;
  const t = localStorage.getItem(STORAGE) || 'dark';
  document.documentElement.classList.toggle('dark', t === 'dark');
}

export function toggleTheme(): boolean {
  const dark = !document.documentElement.classList.contains('dark');
  document.documentElement.classList.toggle('dark', dark);
  localStorage.setItem(STORAGE, dark ? 'dark' : 'light');
  return dark;
}

export function isDark(): boolean {
  return typeof document !== 'undefined' && document.documentElement.classList.contains('dark');
}
