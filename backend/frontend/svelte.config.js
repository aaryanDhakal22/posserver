import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    // Pure SPA: a single fallback index.html, client-side routing. The Go
    // binary embeds `build/` and serves it under /app.
    adapter: adapter({ fallback: 'index.html', pages: 'build', assets: 'build', strict: false }),
    paths: { base: '/app' },
  },
};

export default config;
