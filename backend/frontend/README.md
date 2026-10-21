# quicc main — frontend

The compiled **SvelteKit** UI for `main/` (SPA, TypeScript, Tailwind, dark-first).
It replaces the old hand-rolled `static/files/*.html` pages and is **embedded into
the Go binary** (`internal/transport/webapp`, `//go:embed all:build`) — no nginx.

Two surfaces, one app (mounted at base path **`/app`**):

- **`/app/admin`** — god-mode: tenants / locations / agent devices. Auth =
  `Authorization: Bearer <ADMIN_API_KEY>` (pasted, stored in `localStorage`).
- **`/app/store`** (+ `/app/login`) — store manager: edit your location, printers,
  and devices. Auth = a signed **httpOnly session cookie** set by
  `POST /store/v1/session`; the app calls the scope-clamped `/store/v1/me/*` routes.

`/admin` and `/store` 302-redirect into `/app/*` for back-compat.

## Develop

```bash
# from main/  (needs the API up: `task devup`)
task ui-dev      # Vite dev server + HMR on :5173, proxies /api,/admin/v1,/store/v1 → :1323
```

Open `http://localhost:5173/app/admin` or `/app/store`. In dev the session
cookie is issued without the `Secure` flag (config: `APP_ENV=dev`) so it works
over plain-HTTP localhost.

## Build (embed into the binary)

```bash
task ui-build    # npm run build → copies build/ into internal/transport/webapp/build/
```

`go build`/Docker then embeds it. At rest only `webapp/build/.gitkeep` is tracked;
the real output is gitignored. A binary built with **no** UI (only the
placeholder) serves `503 "UI not built"` at `/app` while the API keeps working —
use `task ui-dev` for UI work, or `task ui-build` before building a full binary.

Production: the `node:20` stage in `backend/Dockerfile*` runs `npm ci && npm run
build` and the Go stage embeds the result — CI is unchanged.

## Stack

SvelteKit 2 + Svelte 5 (runes) + `@sveltejs/adapter-static` (SPA fallback) +
Vite + TypeScript + Tailwind 3. Package manager: **npm** (`package-lock.json`).
