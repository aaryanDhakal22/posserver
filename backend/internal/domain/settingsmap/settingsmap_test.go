package settingsmap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const frontend = "../../../frontend"

func TestEmbeddedMapIsValid(t *testing.T) {
	m := Load()
	if len(m.Pages) < 20 {
		t.Fatalf("only %d pages", len(m.Pages))
	}
	for _, id := range []string{"counter.counter_start", "counter.kitchen_start", "map.delivery_radius", "map.delivery_fee_bands"} {
		if _, _, ok := m.Field(id); !ok {
			t.Errorf("field %s missing", id)
		}
	}
	p, f, _ := m.Field("counter.counter_start")
	if got := Link("https://main.example/", p, f); got != "https://main.example/app/store/settings/counter#counter_start" {
		t.Errorf("link %s", got)
	}
}

// Every page path is a real SPA route (frontend/src/routes/<path without /app>/+page.svelte).
func TestPagesAreRoutes(t *testing.T) {
	if _, err := os.Stat(frontend); err != nil {
		t.Skip("no frontend next to the backend")
	}
	for _, p := range Load().Pages {
		dir := filepath.Join(frontend, "src/routes", strings.TrimPrefix(p.Path, "/app"))
		if _, err := os.Stat(filepath.Join(dir, "+page.svelte")); err != nil {
			t.Errorf("page %s: %s is not a route (%v)", p.ID, p.Path, err)
		}
	}
}

// This copy is byte-equal to the SPA's canonical file (npm run sync-settings-map).
func TestCopyMatchesCanonical(t *testing.T) {
	canon, err := os.ReadFile(filepath.Join(frontend, "src/lib/settings/settings-map.json"))
	if err != nil {
		t.Skip("no frontend next to the backend")
	}
	if !bytes.Equal(canon, Raw) {
		t.Fatal("internal/domain/settingsmap/settings-map.json differs from frontend/src/lib/settings/settings-map.json: run npm run sync-settings-map in frontend/")
	}
}

func TestValidateRefuses(t *testing.T) {
	bad := []string{
		`{"version":0,"pages":[]}`,
		`{"version":1,"pages":[{"id":"a","title":"A","path":"/app/store/a","crumb":"Store › A","fields":[]},{"id":"a","title":"A","path":"/app/store/a","crumb":"Store › A","fields":[]}]}`,
		`{"version":1,"pages":[{"id":"a","title":"A","path":"/elsewhere","crumb":"Store › A","fields":[]}]}`,
		`{"version":1,"pages":[{"id":"a","title":"A","path":"/app/store/a","crumb":"Main › A","fields":[]}]}`,
		`{"version":1,"pages":[{"id":"a","title":"A","path":"/app/store/a","crumb":"Store › A","fields":[{"id":"b.x","anchor":"x","label":"X","api":"PUT"}]}]}`,
		`{"version":1,"pages":[{"id":"a","title":"A","path":"/app/store/a","crumb":"Store › A","fields":[{"id":"a.X Y","anchor":"X Y","label":"X","api":"PUT"}]}]}`,
		`{"version":1,"pages":[{"id":"a","title":"A","path":"/app/store/a","crumb":"Store › A","fields":[{"id":"a.x","anchor":"x","label":"","api":"PUT"}]}]}`,
	}
	for i, b := range bad {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
