// Package settingsmap is main's settings map: every store page and setting,
// where it is in the store app (path + anchor), what it is called (crumb +
// label) and its API field (contract/main.md §2.5zm). The canonical file is
// the SPA's frontend/src/lib/settings/settings-map.json; this package embeds a
// byte copy (frontend `npm run sync-settings-map`, the gate checks they match)
// and main serves it at GET /store/v1/settings-map for the other apps, which
// build their "change it in main" links and wording from it.
package settingsmap

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed settings-map.json
var Raw []byte

// Field is one setting on a page.
type Field struct {
	ID       string   `json:"id"`
	Anchor   string   `json:"anchor"`
	Label    string   `json:"label"`
	API      string   `json:"api"`
	Synonyms []string `json:"synonyms,omitempty"`
}

// Page is one store page.
type Page struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Group    string   `json:"group"`
	Path     string   `json:"path"`
	Crumb    string   `json:"crumb"`
	Synonyms []string `json:"synonyms,omitempty"`
	Fields   []Field  `json:"fields"`
}

// Map is the whole document.
type Map struct {
	Version int    `json:"version"`
	About   string `json:"about,omitempty"`
	Pages   []Page `json:"pages"`
}

// Parse decodes a map and checks it (Validate).
func Parse(b []byte) (*Map, error) {
	var m Map
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("settings map: %w", err)
	}
	return &m, m.Validate()
}

// Validate: a version, unique page and field ids, every page under /app/store
// with a crumb starting "Store › ", every field id "<page id>.<anchor>" with a
// label, an anchor of [a-z0-9_-] and its API named.
func (m *Map) Validate() error {
	if m.Version < 1 {
		return fmt.Errorf("settings map: version must be 1 or more")
	}
	pages, fields := map[string]bool{}, map[string]bool{}
	for _, p := range m.Pages {
		if p.ID == "" || pages[p.ID] {
			return fmt.Errorf("settings map: page id %q empty or repeated", p.ID)
		}
		pages[p.ID] = true
		if !strings.HasPrefix(p.Path, "/app/store") || strings.HasSuffix(p.Path, "/") || strings.ContainsAny(p.Path, "#?") {
			return fmt.Errorf("settings map: page %s: path %q is not a store page", p.ID, p.Path)
		}
		if p.Title == "" || !strings.HasPrefix(p.Crumb, "Store › ") || !strings.HasSuffix(p.Crumb, p.Title) {
			return fmt.Errorf("settings map: page %s: crumb %q must start \"Store › \" and end with its title", p.ID, p.Crumb)
		}
		anchors := map[string]bool{}
		for _, f := range p.Fields {
			if f.ID != p.ID+"."+f.Anchor || fields[f.ID] {
				return fmt.Errorf("settings map: field %q must be %s.<anchor> and unique", f.ID, p.ID)
			}
			fields[f.ID] = true
			if f.Anchor == "" || strings.Trim(f.Anchor, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" || anchors[f.Anchor] {
				return fmt.Errorf("settings map: field %s: anchor %q", f.ID, f.Anchor)
			}
			anchors[f.Anchor] = true
			if strings.TrimSpace(f.Label) == "" || strings.TrimSpace(f.API) == "" {
				return fmt.Errorf("settings map: field %s needs a label and its API", f.ID)
			}
		}
	}
	return nil
}

// Load is the embedded map (panics on a bad one: the unit test catches it first).
func Load() *Map {
	m, err := Parse(Raw)
	if err != nil {
		panic(err)
	}
	return m
}

// Field finds a field by id ("counter.counter_start").
func (m *Map) Field(id string) (Page, Field, bool) {
	for _, p := range m.Pages {
		for _, f := range p.Fields {
			if f.ID == id {
				return p, f, true
			}
		}
	}
	return Page{}, Field{}, false
}

// Link is the deep link to a field on a store's main: base + path + "#" + anchor.
func Link(base string, p Page, f Field) string {
	return strings.TrimRight(base, "/") + p.Path + "#" + f.Anchor
}
