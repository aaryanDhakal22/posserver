package squarepay

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func boxWith(t *testing.T, key []byte) *Service {
	t.Helper()
	s, err := NewService(Config{TokenKey: key}, nil, nil, nil, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testKey(seed string) []byte {
	k := sha256.Sum256([]byte(seed))
	return k[:]
}

func TestSealTextRoundTrip(t *testing.T) {
	s := boxWith(t, testKey("a"))
	const nonce = "cnon:card-nonce-ok"
	sealed, err := s.SealText(nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, SealedPrefix) || strings.Contains(sealed, nonce) {
		t.Fatalf("sealed = %q: want the prefix and no plaintext", sealed)
	}
	again, _ := s.SealText(nonce)
	if again == sealed {
		t.Fatal("two seals of the same text are identical (nonce reused)")
	}
	got, err := s.OpenText(sealed)
	if err != nil || got != nonce {
		t.Fatalf("OpenText = %q, %v; want %q", got, err, nonce)
	}
	if e, _ := s.SealText(""); e != "" {
		t.Fatalf("empty text sealed to %q", e)
	}
}

func TestOpenTextLegacyPlaintext(t *testing.T) {
	// Rows written before sealing existed: no prefix, read as is.
	s := boxWith(t, testKey("a"))
	for _, v := range []string{"cnon:card-nonce-ok", "", "ccof:abc"} {
		if got, err := s.OpenText(v); err != nil || got != v {
			t.Errorf("OpenText(%q) = %q, %v; want it unchanged", v, got, err)
		}
	}
}

func TestSealTextWithoutKey(t *testing.T) {
	// No SQUARE_TOKEN_KEY (Square off): nothing to seal with, stored as given,
	// and a sealed value can't be opened.
	off := boxWith(t, nil)
	if got, err := off.SealText("cnon:x"); err != nil || got != "cnon:x" {
		t.Fatalf("SealText without key = %q, %v", got, err)
	}
	sealed, _ := boxWith(t, testKey("a")).SealText("cnon:x")
	if _, err := off.OpenText(sealed); err == nil {
		t.Fatal("opened a sealed value without a key")
	}
}

func TestOpenTextWrongKeyOrDamaged(t *testing.T) {
	sealed, _ := boxWith(t, testKey("a")).SealText("cnon:x")
	if _, err := boxWith(t, testKey("b")).OpenText(sealed); err == nil {
		t.Fatal("opened with another key")
	}
	if _, err := boxWith(t, testKey("a")).OpenText(SealedPrefix + "!!"); err == nil {
		t.Fatal("opened a damaged value")
	}
}
