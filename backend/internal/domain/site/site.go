// Package site models a store's website (weborder stage 2): the hosts it is
// served on and the storefront's platform keys. The storefront is one server
// for every store: it signs in with a platform key and names the host it is
// serving (X-Site-Host); main maps the host to a location and answers as that
// location's built-in web channel.
package site

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// KeyPrefix starts every platform key.
const KeyPrefix = "qpk_"

// DefaultBaseDomain is the platform's own domain: <slug>.quiccpos.com.
const DefaultBaseDomain = "quiccpos.com"

var (
	ErrBadHost   = errors.New("host must be a domain name like order.example.com")
	ErrHostTaken = errors.New("that host is already used by a store")
)

// Domain is one host a store's website is served on.
type Domain struct {
	Host       string    `json:"host"`
	TenantID   uuid.UUID `json:"tenant_id"`
	LocationID uuid.UUID `json:"location_id"`
	Primary    bool      `json:"primary"`
	Verified   bool      `json:"verified"`
	CreatedAt  time.Time `json:"created_at"`
}

// PlatformKey is a storefront server's key (only its hash is stored).
type PlatformKey struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Settings is a store's website look and texts (the Website page).
type Settings struct {
	LocationID uuid.UUID `json:"location_id"`
	Draft      []byte    `json:"-"`
	Published  []byte    `json:"-"`
	Version    int64     `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// NormalizeHost lowercases a host and drops a port, a scheme and a trailing
// dot ("" when it isn't a domain name).
func NormalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")
	if len(h) > 253 || !hostRe.MatchString(h) {
		return ""
	}
	return h
}

// OnPlatform reports whether host is a subdomain of the platform's domain
// (only an admin may add those; they are verified at once).
func OnPlatform(host, base string) bool {
	return host == base || strings.HasSuffix(host, "."+base)
}
