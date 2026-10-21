// Pure SPA: no server-side rendering, no prerendering. The Go binary serves a
// single fallback index.html and the client router takes over.
export const ssr = false;
export const prerender = false;
