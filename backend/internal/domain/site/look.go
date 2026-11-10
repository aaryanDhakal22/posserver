package site

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Look is the Website page's document: what the store edits as a draft and
// publishes, and what the storefront renders (GET /channel/v1/site
// `settings`). Empty fields mean "the storefront's default".
type Look struct {
	Preset       string            `json:"preset,omitempty"`      // a storefront preset
	Brand        string            `json:"brand,omitempty"`       // #RRGGBB
	MenuLayout   string            `json:"menu_layout,omitempty"` // compact | photo | printed ("" = the preset's)
	Languages    []string          `json:"languages,omitempty"`   // ["en"] or ["en","es"]
	Logo         string            `json:"logo,omitempty"`        // a site image id (kind logo)
	Cover        string            `json:"cover,omitempty"`       // a site image id (kind cover)
	Texts        Texts             `json:"texts"`
	Blocks       []Block           `json:"blocks,omitempty"` // the home page, in order
	Picks        []string          `json:"picks,omitempty"`  // "From the menu": item ids (≤ 8)
	Announcement Announcement      `json:"announcement"`
	MisoPages    *bool             `json:"miso_pages,omitempty"`
	MisoBlocks   map[string]string `json:"miso_blocks,omitempty"` // block → peek | side | off
	Privacy      string            `json:"privacy,omitempty"`     // "" = the default text
}

// Texts are the store's words; each has an optional Spanish twin ("" = the
// storefront's own text).
type Texts struct {
	Eyebrow      string `json:"eyebrow,omitempty"`
	Headline     string `json:"headline,omitempty"`
	Lead         string `json:"lead,omitempty"`
	AboutTitle   string `json:"about_title,omitempty"`
	About        string `json:"about,omitempty"`
	EyebrowES    string `json:"eyebrow_es,omitempty"`
	HeadlineES   string `json:"headline_es,omitempty"`
	LeadES       string `json:"lead_es,omitempty"`
	AboutTitleES string `json:"about_title_es,omitempty"`
	AboutES      string `json:"about_es,omitempty"`
}

// Block is one home page block, switched on or off.
type Block struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

// Announcement is the one-line bar on top of every page, optionally only
// between two dates (store-local, inclusive; "" = open-ended).
type Announcement struct {
	On     bool   `json:"on"`
	Text   string `json:"text,omitempty"`
	TextES string `json:"text_es,omitempty"`
	From   string `json:"from,omitempty"` // YYYY-MM-DD
	To     string `json:"to,omitempty"`
}

// Presets, menu layouts, home blocks and Miso spots the storefront knows.
var (
	Presets     = []string{"pizzeria-loud", "cafe-calm", "taqueria-bold", "classic-paper"}
	MenuLayouts = []string{"compact", "photo", "printed"}
	BlockIDs    = []string{"hero", "again", "categories", "deals", "picks", "hours", "about", "install"}
	MisoSpots   = []string{"peek", "side", "off"}
)

// MaxLookBytes caps the document.
const MaxLookBytes = 64 << 10

// ErrBadLook is a document the Website page may not save (the message says
// which field).
var ErrBadLook = errors.New("bad website settings")

var colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadLook, fmt.Sprintf(format, a...))
}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func maxLen(field, v string, n int) error {
	if utf8.RuneCountInString(v) > n {
		return bad("%s is longer than %d characters", field, n)
	}
	return nil
}

// ParseLook checks a document and returns it normalized (trimmed texts,
// unknown fields refused). Image ids are checked by the caller.
func ParseLook(raw []byte) (Look, []byte, error) {
	var l Look
	if len(raw) > MaxLookBytes {
		return l, nil, bad("the settings are larger than %d bytes", MaxLookBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return l, nil, bad("%s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if l.Preset != "" && !oneOf(l.Preset, Presets) {
		return l, nil, bad("preset must be one of %s", strings.Join(Presets, ", "))
	}
	if l.Brand != "" && !colorRe.MatchString(l.Brand) {
		return l, nil, bad("brand must be a colour like #E2363B")
	}
	l.Brand = strings.ToUpper(l.Brand)
	if l.MenuLayout != "" && !oneOf(l.MenuLayout, MenuLayouts) {
		return l, nil, bad("menu_layout must be one of %s", strings.Join(MenuLayouts, ", "))
	}
	if len(l.Languages) > 0 {
		en, es := false, false
		for _, x := range l.Languages {
			switch x {
			case "en":
				en = true
			case "es":
				es = true
			default:
				return l, nil, bad("languages may be en and es")
			}
		}
		if !en {
			return l, nil, bad("languages must include en")
		}
		l.Languages = []string{"en"}
		if es {
			l.Languages = append(l.Languages, "es")
		}
	}
	for _, id := range []struct{ f, v string }{{"logo", l.Logo}, {"cover", l.Cover}} {
		if id.v != "" {
			if _, err := uuid.Parse(id.v); err != nil {
				return l, nil, bad("%s must be an uploaded image", id.f)
			}
		}
	}
	t := &l.Texts
	for _, f := range []struct {
		name string
		v    *string
		n    int
	}{
		{"eyebrow", &t.Eyebrow, 40}, {"headline", &t.Headline, 80}, {"lead", &t.Lead, 200},
		{"about_title", &t.AboutTitle, 80}, {"about", &t.About, 2000},
		{"eyebrow_es", &t.EyebrowES, 40}, {"headline_es", &t.HeadlineES, 80}, {"lead_es", &t.LeadES, 200},
		{"about_title_es", &t.AboutTitleES, 80}, {"about_es", &t.AboutES, 2000},
		{"announcement.text", &l.Announcement.Text, 140}, {"announcement.text_es", &l.Announcement.TextES, 140},
		{"privacy", &l.Privacy, 8000},
	} {
		*f.v = strings.TrimSpace(*f.v)
		if err := maxLen(f.name, *f.v, f.n); err != nil {
			return l, nil, err
		}
	}
	seen := map[string]bool{}
	for _, b := range l.Blocks {
		if !oneOf(b.ID, BlockIDs) {
			return l, nil, bad("unknown home block %q", b.ID)
		}
		if seen[b.ID] {
			return l, nil, bad("home block %q is listed twice", b.ID)
		}
		seen[b.ID] = true
	}
	if len(l.Picks) > 8 {
		return l, nil, bad("pick up to 8 items")
	}
	for _, p := range l.Picks {
		if p == "" || len(p) > 64 {
			return l, nil, bad("picks must be item ids")
		}
	}
	for _, d := range []struct{ f, v string }{{"announcement.from", l.Announcement.From}, {"announcement.to", l.Announcement.To}} {
		if d.v != "" {
			if _, err := time.Parse("2006-01-02", d.v); err != nil {
				return l, nil, bad("%s must be a date like 2026-12-24", d.f)
			}
		}
	}
	if a := l.Announcement; a.From != "" && a.To != "" && a.To < a.From {
		return l, nil, bad("the announcement ends before it starts")
	}
	if l.Announcement.On && l.Announcement.Text == "" {
		return l, nil, bad("the announcement bar needs its text")
	}
	for k, v := range l.MisoBlocks {
		if !oneOf(k, BlockIDs) || !oneOf(v, MisoSpots) {
			return l, nil, bad("miso_blocks: %q → %q isn't a home block and peek, side or off", k, v)
		}
	}
	out, err := json.Marshal(l)
	return l, out, err
}

// Images are the image ids a document uses.
func (l Look) Images() []string {
	var ids []string
	for _, v := range []string{l.Logo, l.Cover} {
		if v != "" {
			ids = append(ids, v)
		}
	}
	return ids
}

// Image is an uploaded logo or cover photo.
type Image struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	ContentType string    `json:"content_type"`
	Size        int       `json:"size"`
	Bytes       []byte    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
}

// MaxImageBytes caps one upload (the page crops and shrinks before sending).
const MaxImageBytes = 2 << 20

// ImageKinds are the images a website has.
var ImageKinds = []string{"logo", "cover"}

// ErrBadImage is an upload that isn't a PNG, JPEG, WebP (or, for a logo, SVG).
var ErrBadImage = errors.New("send a PNG, JPEG or WebP image (a logo may also be SVG), up to 2 MB")

// SniffImage is the upload's real type ("" = not an image we take). The
// declared Content-Type is not trusted.
func SniffImage(kind string, b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	if kind == "logo" && utf8.Valid(b) {
		head := strings.ToLower(string(b[:min(len(b), 512)]))
		if strings.Contains(head, "<svg") {
			return "image/svg+xml"
		}
	}
	return ""
}
