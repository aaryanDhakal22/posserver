package textclean

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStrip(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"Aaryan D'Souza", "Aaryan D'Souza"},
		{"Jalapeño ½ & ½ 🍕", "Jalapeño ½ & ½ 🍕"},
		{"Bob\x1bp\x00\x19\xfa", "Bobp"},                    // ESC p 0 25 250: the drawer kick
		{"Ann\x10\x14\x01\x00\x01", "Ann"},                  // DLE DC4: real-time pulse
		{"Cut\x1dV\x00 here", "CutV here"},                  // GS V 0: cut
		{"\x1b@reset", "@reset"},                            // ESC @
		{"ring bell\nPAID - CARD", "ring bell PAID - CARD"}, // a forged line
		{"a\tb\r\nc", "a b  c"},
		{"del\x7fete", "delete"},
		{"c1\u0085\u009bx", "c1x"},
		{"line sep x", "line sep x"},
		{"bad\xff\xfeutf8", "badutf8"},
		{"ok \xc3\xa9 then \xc3", "ok é then "},
	} {
		got := Strip(c.in)
		if got != c.want {
			t.Errorf("Strip(%q) = %q, want %q", c.in, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Strip(%q) is not valid UTF-8: %q", c.in, got)
		}
		for i := 0; i < len(got); i++ {
			if got[i] < 0x20 || got[i] == 0x7f {
				t.Errorf("Strip(%q) keeps control byte %#x", c.in, got[i])
			}
		}
		if again := Strip(got); again != got {
			t.Errorf("Strip is not stable on %q: %q", got, again)
		}
	}
}

func TestClean(t *testing.T) {
	if got := Clean("hello", 80); got != "hello" {
		t.Errorf("short text changed: %q", got)
	}
	long := strings.Repeat("é", 100)
	if got := Clean(long, 80); utf8.RuneCountInString(got) != 80 || !utf8.ValidString(got) {
		t.Errorf("clip by characters: %d runes", utf8.RuneCountInString(got))
	}
	if got := Clean(strings.Repeat("x", 80), 80); len(got) != 80 {
		t.Errorf("text of exactly the limit was cut: %d", len(got))
	}
	if got := Clean("ab\x1bcd", 3); got != "abc" {
		t.Errorf("strip then clip = %q", got)
	}
	if got := Clean(long, 0); got != long {
		t.Error("max 0 must not clip")
	}
}

func TestAny(t *testing.T) {
	in := map[string]any{
		"taken_by": "Sam\x1b@",
		"n":        float64(3),
		"cart":     []any{map[string]any{"name": "Pie\nPAID"}, "x\x07", true, nil},
	}
	out := Any(in).(map[string]any)
	if out["taken_by"] != "Sam@" || out["n"] != float64(3) {
		t.Errorf("out = %v", out)
	}
	cart := out["cart"].([]any)
	if cart[0].(map[string]any)["name"] != "Pie PAID" || cart[1] != "x" || cart[2] != true || cart[3] != nil {
		t.Errorf("cart = %v", cart)
	}
	if in["taken_by"] != "Sam\x1b@" {
		t.Error("Any changed its input")
	}
}
