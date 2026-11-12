package repositories

import (
	"errors"
	"strings"
	"testing"
)

// fakeBox seals as "sealed:" + text; text without the prefix opens as is.
type fakeBox struct{}

func (fakeBox) SealText(p string) (string, error) { return "sealed:" + p, nil }
func (fakeBox) OpenText(s string) (string, error) {
	if s == "sealed:bad" {
		return "", errors.New("can't open")
	}
	return strings.TrimPrefix(s, "sealed:"), nil
}

func TestPlacementTokenBox(t *testing.T) {
	r := &PlacementRepository{}
	if got, _ := r.sealToken("cnon:x"); got != "cnon:x" {
		t.Fatalf("no box: sealToken = %q, want it unchanged", got)
	}
	r.SetTokenBox(fakeBox{})
	if got, _ := r.sealToken("cnon:x"); got != "sealed:cnon:x" {
		t.Fatalf("sealToken = %q", got)
	}
	if got, _ := r.sealToken(""); got != "" {
		t.Fatalf("empty token sealed to %q", got)
	}
	p, err := r.opened(Placement{CardToken: "sealed:cnon:x"})
	if err != nil || p.CardToken != "cnon:x" {
		t.Fatalf("opened = %q, %v", p.CardToken, err)
	}
	// A plaintext row from before the upgrade still reads.
	if p, _ := r.opened(Placement{CardToken: "cnon:legacy"}); p.CardToken != "cnon:legacy" {
		t.Fatalf("legacy row = %q", p.CardToken)
	}
	if _, err := r.opened(Placement{CardToken: "sealed:bad"}); err == nil {
		t.Fatal("a token that can't be opened was read")
	}
	// The sweeper's lists open every row; one that can't be opened is blanked,
	// not a failed list.
	ps, err := r.openedAll([]Placement{{CardToken: "sealed:a"}, {CardToken: "sealed:bad"}, {CardToken: "plain"}}, nil)
	if err != nil || ps[0].CardToken != "a" || ps[1].CardToken != "" || ps[2].CardToken != "plain" {
		t.Fatalf("openedAll = %+v, %v", ps, err)
	}
	// The 3-D Secure verification token is sealed and opened the same way.
	p, err = r.opened(Placement{CardToken: "sealed:c", VerificationToken: "sealed:v"})
	if err != nil || p.CardToken != "c" || p.VerificationToken != "v" {
		t.Fatalf("opened with verification = %+v, %v", p, err)
	}
	if _, err := r.opened(Placement{VerificationToken: "sealed:bad"}); err == nil {
		t.Fatal("a verification token that can't be opened was read")
	}
	ps, _ = r.openedAll([]Placement{{CardToken: "sealed:c", VerificationToken: "sealed:bad"}}, nil)
	if ps[0].CardToken != "" || ps[0].VerificationToken != "" {
		t.Fatalf("openedAll left a half-opened row: %+v", ps[0])
	}
}
