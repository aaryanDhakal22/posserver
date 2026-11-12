package siteApp

import (
	"errors"
	"testing"

	"quiccpos/main/internal/domain/site"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestReceiptLogo(t *testing.T) {
	png := site.Image{ID: uuid.New(), Kind: "logo", ContentType: "image/png", Bytes: []byte("\x89PNG\r\n\x1a\nxx")}
	svg := site.Image{ID: uuid.New(), Kind: "logo", ContentType: "image/svg+xml", Bytes: []byte("<svg/>")}
	cover := site.Image{ID: uuid.New(), Kind: "cover", ContentType: "image/jpeg", Bytes: []byte("\xff\xd8\xffxx")}
	images := map[uuid.UUID]site.Image{png.ID: png, svg.ID: svg, cover.ID: cover}
	load := func(id uuid.UUID) (site.Image, error) {
		if img, ok := images[id]; ok {
			return img, nil
		}
		return site.Image{}, pgx.ErrNoRows
	}
	cases := []struct {
		name      string
		published string
		want      *uuid.UUID
	}{
		{"nothing published", ``, nil},
		{"empty look", `{}`, nil},
		{"no logo", `{"brand":"#2F9E5B"}`, nil},
		{"svg logo", `{"logo":"` + svg.ID.String() + `"}`, nil},
		{"cover id as logo", `{"logo":"` + cover.ID.String() + `"}`, nil},
		{"gone image", `{"logo":"` + uuid.NewString() + `"}`, nil},
		{"not a uuid", `{"logo":"nope"}`, nil},
		{"png logo", `{"logo":"` + png.ID.String() + `"}`, &png.ID},
	}
	for _, c := range cases {
		got, err := receiptLogo([]byte(c.published), load)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got logo %s, want none", c.name, got.ID)
		case c.want != nil && (got == nil || got.ID != *c.want):
			t.Errorf("%s: got %v, want %s", c.name, got, *c.want)
		}
	}

	boom := errors.New("db down")
	if _, err := receiptLogo([]byte(`{"logo":"`+png.ID.String()+`"}`), func(uuid.UUID) (site.Image, error) { return site.Image{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("a failed load must surface, got %v", err)
	}
}

func TestPrintableLogoAndETag(t *testing.T) {
	for ct, want := range map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/svg+xml": false, "": false} {
		if PrintableLogo(ct) != want {
			t.Errorf("PrintableLogo(%q) = %v", ct, !want)
		}
	}
	a, b := LogoETag([]byte("one")), LogoETag([]byte("two"))
	if a == b || a[0] != '"' || a[len(a)-1] != '"' || a != LogoETag([]byte("one")) {
		t.Fatalf("etags %s %s", a, b)
	}
}
