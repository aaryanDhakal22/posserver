package authSvc

import (
	"crypto/subtle"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is bcrypt's default work factor — logins are rare (a manager or
// operator signing in), so the ~50-100ms cost is fine.
const bcryptCost = bcrypt.DefaultCost

// HashPassword hashes a human-chosen password (store-manager / operator login)
// with bcrypt. Device keys and tokens are NOT passwords — they're long random
// strings and keep using HashKey (a fast SHA-256 lookup key).
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword checks password against a stored hash. It accepts both the
// current bcrypt format ("$2…") and the legacy unsalted SHA-256 hex (what
// HashKey produced before bcrypt). needsRehash is true when the stored hash is
// legacy and the password matched — the caller should re-save it with
// HashPassword so the account migrates on its next successful login.
func VerifyPassword(password, stored string) (ok, needsRehash bool) {
	if stored == "" || password == "" {
		return false, false
	}
	if strings.HasPrefix(stored, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil, false
	}
	if subtle.ConstantTimeCompare([]byte(HashKey(password)), []byte(stored)) == 1 {
		return true, true
	}
	return false, false
}
