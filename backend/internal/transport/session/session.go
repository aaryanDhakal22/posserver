// Package session mints + verifies the store-manager web session cookie.
//
// Unlike the agent's per-boot session (agent/internal/transport/session.go),
// main runs blue-green: two API slots serve behind Traefik and a deploy swaps
// which is primary. If the signing secret were random per boot, a swap (or a
// second replica) would invalidate every manager's cookie. So the secret comes
// from config (SESSION_SECRET, Doppler-managed, identical across slots) and must
// be stable.
//
// The cookie is opaque + tamper-evident: base64url(payload) + "." + hex HMAC,
// where payload = "<tenant_id>|<location_id>|<exp unix>". It carries the
// manager's SCOPE, so no store-side lookup is needed to know which
// (tenant, location) a request is clamped to.
package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// CookieName is scoped to Path=/store so it only rides on store-manager
	// requests, never the device/admin/api surfaces.
	CookieName = "quicc_store_session"
	cookiePath = "/store"
	ttl        = 12 * time.Hour
	minSecret  = 16
)

// Claims is the decoded, verified content of a session cookie.
type Claims struct {
	TenantID   uuid.UUID
	LocationID uuid.UUID
	Exp        int64
}

// Signer issues + verifies session cookies. `secure` sets the cookie's Secure
// flag; it must be false in dev (Vite proxies over plain-HTTP localhost, where a
// Secure cookie is silently dropped) and true in stg/prd (behind Traefik TLS).
type Signer struct {
	secret []byte
	secure bool
}

func New(secret string, secure bool) (*Signer, error) {
	if len(secret) < minSecret {
		return nil, errors.New("SESSION_SECRET must be at least 16 bytes")
	}
	return &Signer{secret: []byte(secret), secure: secure}, nil
}

func (s *Signer) mac(payload string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Signer) encode(c Claims) string {
	raw := c.TenantID.String() + "|" + c.LocationID.String() + "|" + strconv.FormatInt(c.Exp, 10)
	payload := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return payload + "." + s.mac(payload)
}

func (s *Signer) decode(value string) (Claims, bool) {
	dot := strings.LastIndexByte(value, '.')
	if dot < 0 {
		return Claims{}, false
	}
	payload, sig := value[:dot], value[dot+1:]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(s.mac(payload))) != 1 {
		return Claims{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Claims{}, false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return Claims{}, false
	}
	tid, err1 := uuid.Parse(parts[0])
	lid, err2 := uuid.Parse(parts[1])
	exp, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return Claims{}, false
	}
	return Claims{TenantID: tid, LocationID: lid, Exp: exp}, true
}

// Issue writes a fresh session cookie scoping the browser to (tenantID, locationID).
func (s *Signer) Issue(w http.ResponseWriter, tenantID, locationID uuid.UUID) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    s.encode(Claims{TenantID: tenantID, LocationID: locationID, Exp: time.Now().Add(ttl).Unix()}),
		Path:     cookiePath,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// Clear expires the session cookie (logout).
func (s *Signer) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     cookiePath,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// Verify returns the claims if the request carries a valid, unexpired cookie.
func (s *Signer) Verify(r *http.Request) (Claims, bool) {
	ck, err := r.Cookie(CookieName)
	if err != nil {
		return Claims{}, false
	}
	c, ok := s.decode(ck.Value)
	if !ok || time.Now().Unix() > c.Exp {
		return Claims{}, false
	}
	return c, true
}
