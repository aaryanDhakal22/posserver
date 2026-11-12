// Package textclean makes text that came from outside (a vendor order, a
// customer typing on a website, a call-center agent, the Pi) safe to store
// and hand on (security review H3).
//
// Such text ends up on thermal printers, which read ESC / GS / DLE bytes in
// the middle of a name as commands: a customer called "\x1bp\x00\x19\xfa"
// kicked the cash drawer, "\x1dV\x00" cut the paper, and a note with a line
// break printed "PAID - CARD" as a line of its own. The Pi and the terminal
// strip them again where they print; main strips them where an order or a
// customer is built, so nothing downstream ever sees them.
package textclean

import (
	"strings"
	"unicode/utf8"
)

// Strip returns s without control characters: a tab, line feed, carriage
// return or Unicode line / paragraph separator becomes one space, every
// other C0 control (below 0x20), DEL (0x7F), C1 control (U+0080–U+009F) and
// byte that isn't valid UTF-8 is dropped. Clean text comes back as it is.
func Strip(s string) string {
	if clean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n <= 1: // not UTF-8
		case r == '\t' || r == '\n' || r == '\r' || r == ' ' || r == ' ':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clean(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return false
		}
		if c >= 0x80 { // multi-byte: take the slow path only if something in it needs work
			for _, r := range s[i:] {
				if r == utf8.RuneError || (r >= 0x80 && r <= 0x9f) || r == ' ' || r == ' ' || r < 0x20 || r == 0x7f {
					return false
				}
			}
			return true
		}
	}
	return true
}

// Clean is Strip, then cut to at most max characters (runes); max <= 0 = no
// limit. Text that is clean and short enough comes back as it is.
func Clean(s string, max int) string {
	s = Strip(s)
	if max <= 0 || len(s) <= max { // bytes >= runes: short enough for sure
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// Any strips every string inside a decoded JSON value (maps, slices,
// strings) and returns the cleaned copy; other values pass as they are.
func Any(v any) any {
	switch t := v.(type) {
	case string:
		return Strip(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = Any(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = Any(e)
		}
		return out
	}
	return v
}
