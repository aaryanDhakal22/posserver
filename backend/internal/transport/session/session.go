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
//
// An admin "open as store" session (IssueAs) appends a 4th field — the
// impersonator label, "<tenant_id>|<location_id>|<exp unix>|<by>" — and gets a
// shorter TTL. A plain manager cookie keeps the 3-field form, so cookies issued
// before the 4th field existed still verify.
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
	// ImpersonateTTL bounds an admin's open-as-store session; short on purpose
	// since it is a support tool, not a working login.
	ImpersonateTTL = 2 * time.Hour
)

// Claims is the decoded, verified content of a session cookie.
type Claims struct {
	TenantID   uuid.UUID
	LocationID uuid.UUID
	Exp        int64
	// ImpersonatedBy is the impersonator label ("admin") of an open-as-store
	// session; "" for a manager's own login.
	ImpersonatedBy string
}

// Signer issues + verifies session cookies. `secure` sets the cookie's Secure
// flag for requests that arrived over HTTPS (directly or via a TLS proxy's
// X-Forwarded-Proto); it must be false in dev (Vite proxies over plain-HTTP
// localhost). A plain-HTTP request never gets a Secure cookie — the browser
// would drop it (staging main is reached over plain HTTP on the LAN).
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
	if c.ImpersonatedBy != "" {
		raw += "|" + c.ImpersonatedBy
	}
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
	// 3 parts = a manager's own session; 4 = impersonated (4th must be non-empty).
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 && len(parts) != 4 {
		return Claims{}, false
	}
	var by string
	if len(parts) == 4 {
		if by = parts[3]; by == "" {
			return Claims{}, false
		}
	}
	tid, err1 := uuid.Parse(parts[0])
	lid, err2 := uuid.Parse(parts[1])
	exp, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return Claims{}, false
	}
	return Claims{TenantID: tid, LocationID: lid, Exp: exp, ImpersonatedBy: by}, true
}

// secureFor reports whether the cookie for this request gets the Secure flag.
func (s *Signer) secureFor(r *http.Request) bool {
	if !s.secure || r == nil {
		return s.secure
	}
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// Issue writes a fresh session cookie scoping the browser to (tenantID, locationID).
func (s *Signer) Issue(w http.ResponseWriter, r *http.Request, tenantID, locationID uuid.UUID) {
	s.issue(w, r, Claims{TenantID: tenantID, LocationID: locationID}, ttl)
}

// ErrBadImpersonator rejects an IssueAs label that can't round-trip the cookie.
var ErrBadImpersonator = errors.New("impersonator label must be non-empty and contain no '|'")

// IssueAs writes an impersonated session cookie: the same (tenant, location)
// scope as Issue, stamped with who opened it (`by`, e.g. "admin") and limited
// to ImpersonateTTL. The caller audits the issue; the stamp lets every request
// on the session be traced back to it.
func (s *Signer) IssueAs(w http.ResponseWriter, r *http.Request, tenantID, locationID uuid.UUID, by string) error {
	if by == "" || strings.ContainsRune(by, '|') {
		return ErrBadImpersonator
	}
	s.issue(w, r, Claims{TenantID: tenantID, LocationID: locationID, ImpersonatedBy: by}, ImpersonateTTL)
	return nil
}

func (s *Signer) issue(w http.ResponseWriter, r *http.Request, c Claims, life time.Duration) {
	c.Exp = time.Now().Add(life).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    s.encode(c),
		Path:     cookiePath,
		HttpOnly: true,
		Secure:   s.secureFor(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(life.Seconds()),
	})
}

// Clear expires the session cookie (logout).
func (s *Signer) Clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     cookiePath,
		HttpOnly: true,
		Secure:   s.secureFor(r),
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
