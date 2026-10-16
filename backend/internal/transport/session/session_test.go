package session

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func mustSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := New("test-secret-at-least-16-bytes-long", false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestIssueVerifyRoundtrip(t *testing.T) {
	s := mustSigner(t)
	tid, lid := uuid.New(), uuid.New()

	rec := httptest.NewRecorder()
	s.Issue(rec, tid, lid)
	ck := rec.Result().Cookies()[0]
	if ck.Name != CookieName || !ck.HttpOnly || ck.Path != "/store" {
		t.Fatalf("unexpected cookie attrs: %+v", ck)
	}

	req := httptest.NewRequest(http.MethodGet, "/store/v1/me/location", nil)
	req.AddCookie(ck)
	claims, ok := s.Verify(req)
	if !ok {
		t.Fatal("Verify failed on a freshly issued cookie")
	}
	if claims.TenantID != tid || claims.LocationID != lid {
		t.Fatalf("claims mismatch: got %v/%v want %v/%v", claims.TenantID, claims.LocationID, tid, lid)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	s := mustSigner(t)
	rec := httptest.NewRecorder()
	s.Issue(rec, uuid.New(), uuid.New())
	ck := rec.Result().Cookies()[0]
	// Flip the last sig char to a guaranteed-different one (a fixed "0" was a
	// no-op whenever the sig already ended in "0" — ~1/64 flake).
	last := ck.Value[len(ck.Value)-1]
	flipped := byte('A')
	if last == 'A' {
		flipped = 'B'
	}
	ck.Value = ck.Value[:len(ck.Value)-1] + string(flipped)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(ck)
	if _, ok := s.Verify(req); ok {
		t.Fatal("Verify accepted a tampered cookie")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	a := mustSigner(t)
	b, _ := New("a-totally-different-secret-16b+++", false)
	rec := httptest.NewRecorder()
	a.Issue(rec, uuid.New(), uuid.New())
	ck := rec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(ck)
	if _, ok := b.Verify(req); ok {
		t.Fatal("cookie signed by A verified under B's secret")
	}
}

func TestNewRejectsShortSecret(t *testing.T) {
	if _, err := New("short", false); err == nil {
		t.Fatal("expected error for a too-short secret")
	}
}

func TestSecureFlag(t *testing.T) {
	s, _ := New("test-secret-at-least-16-bytes-long", true)
	rec := httptest.NewRecorder()
	s.Issue(rec, uuid.New(), uuid.New())
	if !rec.Result().Cookies()[0].Secure {
		t.Fatal("expected Secure cookie when secure=true")
	}
}
