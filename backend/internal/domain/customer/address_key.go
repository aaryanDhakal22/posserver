package customer

import (
	"strings"
	"unicode"
)

// streetWords shortens the words a street line is written both ways with,
// so "12 North Main Street" and "12 n. main st" are the same address.
var streetWords = map[string]string{
	"street": "st", "str": "st", "avenue": "ave", "av": "ave", "road": "rd", "drive": "dr", "lane": "ln",
	"court": "ct", "boulevard": "blvd", "place": "pl", "circle": "cir", "terrace": "ter", "parkway": "pkwy",
	"highway": "hwy", "square": "sq", "trail": "trl", "plaza": "plz", "pike": "pike",
	"north": "n", "south": "s", "east": "e", "west": "w",
	"northeast": "ne", "northwest": "nw", "southeast": "se", "southwest": "sw",
}

// NormLine1 is an address's first line for matching: lower case, no
// punctuation, single spaces, common street words shortened.
func NormLine1(line1 string) string {
	f := strings.FieldsFunc(strings.ToLower(line1), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, w := range f {
		if s, ok := streetWords[w]; ok {
			f[i] = s
		}
	}
	return strings.Join(f, " ")
}

// Zip5 is the 5-digit zip ("" when there is none).
func Zip5(zip string) string {
	var b strings.Builder
	for _, r := range zip {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
			if b.Len() == 5 {
				return b.String()
			}
		}
	}
	return ""
}

// SameAddress reports whether two addresses are the same place for the bulk
// import's de-duplication: the same normalised line 1 and the same 5-digit
// zip (a missing zip on either side matches on line 1 alone).
func SameAddress(a, b Address) bool {
	la, lb := NormLine1(a.Line1), NormLine1(b.Line1)
	if la == "" || la != lb {
		return false
	}
	za, zb := Zip5(a.Zip), Zip5(b.Zip)
	return za == "" || zb == "" || za == zb
}
