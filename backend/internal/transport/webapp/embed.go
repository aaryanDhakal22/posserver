// Package webapp embeds the compiled SvelteKit SPA (backend/frontend → build/)
// into the Go binary and serves it at /app. The `all:` prefix on the embed
// directive is REQUIRED: SvelteKit emits hashed assets under `build/_app/…`, and
// a plain `//go:embed build` skips names starting with `_` or `.`.
//
// At rest the repo only tracks `build/.gitkeep` (so `go build`/Air compile with
// no Node). The Dockerfile's node stage overwrites `build/` with the real output
// before `go build`; `task ui-build` does the same locally. `Built()` lets the
// serve handler return a helpful 503 when only the placeholder is present.
package webapp

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var build embed.FS

// FS returns the compiled app rooted at `build/`.
func FS() fs.FS {
	sub, err := fs.Sub(build, "build")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether a real build is embedded (index.html present) rather
// than just the committed .gitkeep placeholder.
func Built() bool {
	f, err := build.Open("build/index.html")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
