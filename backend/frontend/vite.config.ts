import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Dev: Vite serves the SPA with HMR on :5173 and proxies the API surfaces to
// the Go server on :1323. The session cookie (Path=/store) rides through the
// proxy because the browser sees everything as same-origin (localhost:5173).
export default defineConfig({
  plugins: [sveltekit()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:1323', changeOrigin: false },
      '/admin/v1': { target: 'http://localhost:1323', changeOrigin: false },
      '/store/v1': { target: 'http://localhost:1323', changeOrigin: false }
    }
  }
});
