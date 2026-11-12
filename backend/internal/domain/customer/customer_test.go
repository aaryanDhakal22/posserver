package customer

import "testing"

func TestSplitJoinName(t *testing.T) {
	for _, c := range []struct{ in, first, last string }{
		{"", "", ""},
		{"  ", "", ""},
		{"Ada", "Ada", ""},
		{"Ada Lovelace", "Ada", "Lovelace"},
		{"  Mary  Ann   Smith ", "Mary Ann", "Smith"},
	} {
		f, l := SplitName(c.in)
		if f != c.first || l != c.last {
			t.Errorf("SplitName(%q) = %q, %q; want %q, %q", c.in, f, l, c.first, c.last)
		}
	}
	for _, c := range []struct{ first, last, want string }{
		{"", "", ""}, {"Ada", "", "Ada"}, {"", "Lovelace", "Lovelace"}, {" Ada ", " Lovelace", "Ada Lovelace"},
	} {
		if got := JoinName(c.first, c.last); got != c.want {
			t.Errorf("JoinName(%q, %q) = %q; want %q", c.first, c.last, got, c.want)
		}
	}
}

func TestImportMayWrite(t *testing.T) {
	for _, c := range []struct {
		source, current string
		want            bool
	}{
		{"", "", true}, {SourceWeb, "", true}, {"", "Ada", true}, {SourceImport, "Ada", true},
		{SourceWeb, "Ada", false}, {SourceCounter, "Ada", false},
	} {
		if got := ImportMayWrite(c.source, c.current); got != c.want {
			t.Errorf("ImportMayWrite(%q, %q) = %v", c.source, c.current, got)
		}
	}
}

func TestSameAddress(t *testing.T) {
	a := Address{Line1: "12 North Main Street", Zip: "21286"}
	for _, c := range []struct {
		b    Address
		want bool
	}{
		{Address{Line1: "12 n. main st", Zip: "21286-1234"}, true},
		{Address{Line1: "12 N Main St"}, true}, // no zip: line 1 alone
		{Address{Line1: "12 N Main St", Zip: "21204"}, false},
		{Address{Line1: "14 N Main St", Zip: "21286"}, false},
		{Address{Line1: "", Zip: "21286"}, false},
	} {
		if got := SameAddress(a, c.b); got != c.want {
			t.Errorf("SameAddress(%+v) = %v", c.b, got)
		}
	}
	if NormLine1("931 Taylor Ave.") != "931 taylor ave" || Zip5("21286-0001") != "21286" || Zip5("212") != "" {
		t.Fatal("normalisers")
	}
}
