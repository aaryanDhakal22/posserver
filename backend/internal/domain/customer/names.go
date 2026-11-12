package customer

import "strings"

// Name sources: who last wrote a customer's name or notes. The bulk import
// only overwrites a field it wrote itself (or an empty one), never what the
// customer typed on the website or staff typed at the counter.
const (
	SourceNone    = ""
	SourceWeb     = "web"
	SourceCounter = "counter"
	SourceImport  = "import"
)

// JoinName is the full name kept in `name` (the website's field) for a
// first + last name: "First Last", either part may be empty.
func JoinName(first, last string) string {
	return strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
}

// SplitName is the first + last name for a full name the website wrote:
// the last word is the last name, everything before it the first name; a
// single word is a first name only. Runs of spaces collapse.
func SplitName(name string) (first, last string) {
	f := strings.Fields(name)
	switch len(f) {
	case 0:
		return "", ""
	case 1:
		return f[0], ""
	}
	return strings.Join(f[:len(f)-1], " "), f[len(f)-1]
}

// ImportMayWrite reports whether the bulk import may overwrite a field last
// written by source (only an empty or an imported one).
func ImportMayWrite(source, current string) bool {
	return current == "" || source == SourceNone || source == SourceImport
}
