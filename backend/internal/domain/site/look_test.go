package site

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLookNormalizes(t *testing.T) {
	l, out, err := ParseLook([]byte(`{"preset":"cafe-calm","brand":"#e2363b","languages":["es","en"],
		"texts":{"headline":"  Hot pies  "},"blocks":[{"id":"hero","on":true},{"id":"about","on":false}],
		"picks":["a","b"],"announcement":{"on":true,"text":"Closed Thanksgiving","from":"2026-11-26","to":"2026-11-26"},
		"miso_pages":false,"miso_blocks":{"hero":"side"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if l.Brand != "#E2363B" || l.Texts.Headline != "Hot pies" || strings.Join(l.Languages, ",") != "en,es" {
		t.Fatalf("not normalized: %+v", l)
	}
	if !strings.Contains(string(out), `"brand":"#E2363B"`) || strings.Contains(string(out), `"logo"`) {
		t.Fatalf("stored form: %s", out)
	}
	if _, _, err := ParseLook([]byte(`{}`)); err != nil {
		t.Fatalf("empty document: %v", err)
	}
}

func TestParseLookRefuses(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown field":      `{"colour":"red"}`,
		"preset":             `{"preset":"neon"}`,
		"brand":              `{"brand":"red"}`,
		"layout":             `{"menu_layout":"grid"}`,
		"no english":         `{"languages":["es"]}`,
		"french":             `{"languages":["en","fr"]}`,
		"logo not an id":     `{"logo":"http://x/y.png"}`,
		"long headline":      `{"texts":{"headline":"` + strings.Repeat("x", 81) + `"}}`,
		"unknown block":      `{"blocks":[{"id":"video","on":true}]}`,
		"block twice":        `{"blocks":[{"id":"hero","on":true},{"id":"hero","on":false}]}`,
		"nine picks":         `{"picks":["1","2","3","4","5","6","7","8","9"]}`,
		"bad date":           `{"announcement":{"from":"26/11/2026"}}`,
		"ends before start":  `{"announcement":{"from":"2026-11-27","to":"2026-11-26"}}`,
		"bar without text":   `{"announcement":{"on":true}}`,
		"bad miso spot":      `{"miso_blocks":{"hero":"dance"}}`,
		"miso unknown block": `{"miso_blocks":{"video":"peek"}}`,
		"not json":           `{`,
	} {
		if _, _, err := ParseLook([]byte(doc)); !errors.Is(err, ErrBadLook) {
			t.Errorf("%s: want ErrBadLook, got %v", name, err)
		}
	}
}

func TestSniffImage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n....")
	if SniffImage("cover", png) != "image/png" || SniffImage("logo", []byte("\xff\xd8\xff\xe0")) != "image/jpeg" {
		t.Fatal("png/jpeg")
	}
	if SniffImage("cover", []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")) != "image/webp" {
		t.Fatal("webp")
	}
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)
	if SniffImage("logo", svg) != "image/svg+xml" || SniffImage("cover", svg) != "" {
		t.Fatal("svg only for a logo")
	}
	if SniffImage("logo", []byte("GIF89a")) != "" {
		t.Fatal("gif refused")
	}
}
