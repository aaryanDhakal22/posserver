package session

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
	s.Issue(rec, nil, tid, lid, 0)
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
	s.Issue(rec, nil, uuid.New(), uuid.New(), 0)
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
	a.Issue(rec, nil, uuid.New(), uuid.New(), 0)
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
	req := httptest.NewRequest("POST", "/store/v1/session", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	s.Issue(rec, req, uuid.New(), uuid.New(), 0)
	if !rec.Result().Cookies()[0].Secure {
		t.Fatal("expected Secure cookie when secure=true over HTTPS")
	}
	// Plain HTTP (staging on the LAN): a Secure cookie would be dropped.
	rec = httptest.NewRecorder()
	s.Issue(rec, httptest.NewRequest("POST", "/store/v1/session", nil), uuid.New(), uuid.New(), 0)
	if rec.Result().Cookies()[0].Secure {
		t.Fatal("expected no Secure flag over plain HTTP")
	}
}

// verifyCookie runs Verify on a request carrying ck.
func verifyCookie(s *Signer, ck *http.Cookie) (Claims, bool) {
	req := httptest.NewRequest(http.MethodGet, "/store/v1/session", nil)
	req.AddCookie(ck)
	return s.Verify(req)
}

func TestIssueAsRoundtripAndTTL(t *testing.T) {
	s := mustSigner(t)
	tid, lid := uuid.New(), uuid.New()

	rec := httptest.NewRecorder()
	before := time.Now()
	if err := s.IssueAs(rec, nil, tid, lid, "admin", 0); err != nil {
		t.Fatalf("IssueAs: %v", err)
	}
	ck := rec.Result().Cookies()[0]
	if ck.Name != CookieName || !ck.HttpOnly || ck.Path != "/store" {
		t.Fatalf("unexpected cookie attrs: %+v", ck)
	}
	if ck.MaxAge != int((2 * time.Hour).Seconds()) {
		t.Fatalf("MaxAge = %d, want 2h", ck.MaxAge)
	}
	claims, ok := verifyCookie(s, ck)
	if !ok {
		t.Fatal("Verify failed on a fresh impersonated cookie")
	}
	if claims.TenantID != tid || claims.LocationID != lid || claims.ImpersonatedBy != "admin" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if exp := before.Add(2 * time.Hour).Unix(); claims.Exp < exp || claims.Exp > exp+5 {
		t.Fatalf("Exp = %d, want ~%d (2h)", claims.Exp, exp)
	}
}

func TestIssuePlainHasNoImpersonator(t *testing.T) {
	s := mustSigner(t)
	rec := httptest.NewRecorder()
	s.Issue(rec, nil, uuid.New(), uuid.New(), 0)
	ck := rec.Result().Cookies()[0]
	if ck.MaxAge != int((12 * time.Hour).Seconds()) {
		t.Fatalf("MaxAge = %d, want 12h", ck.MaxAge)
	}
	raw, err := base64.RawURLEncoding.DecodeString(ck.Value[:strings.LastIndexByte(ck.Value, '.')])
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "|"); n != 2 {
		t.Fatalf("plain session payload has %d separators, want 2 (3 fields)", n)
	}
	claims, ok := verifyCookie(s, ck)
	if !ok || claims.ImpersonatedBy != "" {
		t.Fatalf("plain session: ok=%v impersonated_by=%q", ok, claims.ImpersonatedBy)
	}
}

// A cookie minted before the 4th field existed (3 parts) must still verify.
func TestVerifyAcceptsLegacyThreePartCookie(t *testing.T) {
	s := mustSigner(t)
	tid, lid := uuid.New(), uuid.New()
	raw := tid.String() + "|" + lid.String() + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	payload := base64.RawURLEncoding.EncodeToString([]byte(raw))
	ck := &http.Cookie{Name: CookieName, Value: payload + "." + s.mac(payload)}

	claims, ok := verifyCookie(s, ck)
	if !ok || claims.TenantID != tid || claims.LocationID != lid || claims.ImpersonatedBy != "" {
		t.Fatalf("legacy cookie: ok=%v claims=%+v", ok, claims)
	}
}

func TestVerifyRejectsTamperedImpersonatedCookie(t *testing.T) {
	s := mustSigner(t)
	tid, lid := uuid.New(), uuid.New()
	rec := httptest.NewRecorder()
	if err := s.IssueAs(rec, nil, tid, lid, "admin", 0); err != nil {
		t.Fatal(err)
	}
	ck := rec.Result().Cookies()[0]
	sig := ck.Value[strings.LastIndexByte(ck.Value, '.')+1:]

	// Strip the impersonator but keep the old signature: must not verify.
	raw := tid.String() + "|" + lid.String() + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	forged := &http.Cookie{Name: CookieName, Value: base64.RawURLEncoding.EncodeToString([]byte(raw)) + "." + sig}
	if _, ok := verifyCookie(s, forged); ok {
		t.Fatal("Verify accepted a re-written payload under the old signature")
	}
	// Relabel the impersonator, same signature.
	relabel := &http.Cookie{Name: CookieName, Value: base64.RawURLEncoding.EncodeToString([]byte(raw+"|root")) + "." + sig}
	if _, ok := verifyCookie(s, relabel); ok {
		t.Fatal("Verify accepted a relabelled impersonated cookie")
	}
}

func TestDecodeRejectsMalformedImpersonator(t *testing.T) {
	s := mustSigner(t)
	base := uuid.NewString() + "|" + uuid.NewString() + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	for _, raw := range []string{base + "|", base + "|admin|extra"} {
		payload := base64.RawURLEncoding.EncodeToString([]byte(raw))
		if _, ok := s.decode(payload + "." + s.mac(payload)); ok {
			t.Fatalf("decode accepted validly-signed malformed payload %q", raw)
		}
	}
}

func TestIssueAsRejectsBadLabel(t *testing.T) {
	s := mustSigner(t)
	for _, by := range []string{"", "ad|min"} {
		rec := httptest.NewRecorder()
		if err := s.IssueAs(rec, nil, uuid.New(), uuid.New(), by, 0); err == nil {
			t.Fatalf("IssueAs accepted label %q", by)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatalf("IssueAs(%q) set a cookie despite the error", by)
		}
	}
}

// The store's session epoch (M8) rides in the signed payload: 0 keeps the
// older cookie forms byte for byte (an older main still reads them), above 0
// adds the 5th field, for a manager's own and an impersonated session.
func TestEpochRoundtripAndOldForms(t *testing.T) {
	s := mustSigner(t)
	tid, lid := uuid.New(), uuid.New()
	payload := func(ck *http.Cookie) string {
		raw, err := base64.RawURLEncoding.DecodeString(ck.Value[:strings.LastIndexByte(ck.Value, '.')])
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	verify := func(ck *http.Cookie) Claims {
		req := httptest.NewRequest(http.MethodGet, "/store/v1/session", nil)
		req.AddCookie(ck)
		c, ok := s.Verify(req)
		if !ok {
			t.Fatalf("Verify failed for %q", payload(ck))
		}
		return c
	}

	rec := httptest.NewRecorder()
	s.Issue(rec, nil, tid, lid, 0)
	ck := rec.Result().Cookies()[0]
	if n := strings.Count(payload(ck), "|"); n != 2 {
		t.Fatalf("epoch 0 must keep the 3-field form, got %q", payload(ck))
	}
	if c := verify(ck); c.Epoch != 0 || c.ImpersonatedBy != "" {
		t.Fatalf("claims = %+v", c)
	}

	rec = httptest.NewRecorder()
	s.Issue(rec, nil, tid, lid, 7)
	ck = rec.Result().Cookies()[0]
	if c := verify(ck); c.Epoch != 7 || c.ImpersonatedBy != "" || c.TenantID != tid || c.LocationID != lid {
		t.Fatalf("claims = %+v (payload %q)", c, payload(ck))
	}

	rec = httptest.NewRecorder()
	if err := s.IssueAs(rec, nil, tid, lid, "admin", 3); err != nil {
		t.Fatal(err)
	}
	if c := verify(rec.Result().Cookies()[0]); c.Epoch != 3 || c.ImpersonatedBy != "admin" {
		t.Fatalf("claims = %+v", c)
	}

	// The epoch is signed: changing it breaks the cookie.
	rec = httptest.NewRecorder()
	s.Issue(rec, nil, tid, lid, 1)
	ck = rec.Result().Cookies()[0]
	forged := base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSuffix(payload(ck), "1")+"2")) + ck.Value[strings.LastIndexByte(ck.Value, '.'):]
	req := httptest.NewRequest(http.MethodGet, "/store/v1/session", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: forged})
	if _, ok := s.Verify(req); ok {
		t.Fatal("a cookie with a changed epoch verified")
	}
	// A 5-field payload with a junk or zero epoch is refused even when signed.
	for _, raw := range []string{
		tid.String() + "|" + lid.String() + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "||0",
		tid.String() + "|" + lid.String() + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "||x",
		tid.String() + "|" + lid.String() + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "||1|extra",
	} {
		p := base64.RawURLEncoding.EncodeToString([]byte(raw))
		req := httptest.NewRequest(http.MethodGet, "/store/v1/session", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: p + "." + s.mac(p)})
		if _, ok := s.Verify(req); ok {
			t.Fatalf("payload %q verified", raw)
		}
	}
}
