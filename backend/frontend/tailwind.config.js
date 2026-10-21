/** @type {import('tailwindcss').Config} */
// Colors are the Quibase design tokens, defined as CSS variables in
// src/app.css (light on :root, dark on html.dark) — so `bg-surface`,
// `text-muted`, `border-border`, … follow the theme automatically.
const v = (name) => `var(--${name})`;

export default {
  darkMode: 'class',
  content: ['./src/**/*.{html,js,svelte,ts}'],
  // Badge tones are built as `b-${tone}` (app.css components), which the
  // content scan can't see.
  safelist: ['b-ok', 'b-warn', 'b-bad', 'b-info', 'b-mute', 'b-brand'],
  theme: {
    extend: {
      colors: {
        canvas: v('canvas'),
        surface: v('surface'),
        elev: v('elev'),
        hover: v('hover'),
        border: v('border'),
        text: v('text'),
        text2: v('text2'),
        muted: v('muted'),
        accent: v('accent'),
        brand: v('brand'),
        'on-brand': v('on-brand'),
        'brand-soft': v('brand-soft'),
        ok: v('ok'),
        'ok-bg': v('ok-bg'),
        warn: v('warn'),
        'warn-bg': v('warn-bg'),
        bad: v('bad'),
        'bad-bg': v('bad-bg'),
        info: v('info'),
        'info-bg': v('info-bg')
      },
      fontFamily: {
        sans: ['Inter', 'system-ui', 'sans-serif'],
        mono: ['"JetBrains Mono"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace']
      },
      borderRadius: {
        r10: '10px',
        r12: '12px',
        r14: '14px',
        r16: '16px'
      }
    }
  },
  plugins: []
};
