// Package squarepay connects stores to Square and takes their online card
// payments (weborder stage 2).
//
//   - Service: a store owner connects their Square account (OAuth code flow;
//     tokens AES-GCM-encrypted with SQUARE_TOKEN_KEY, refreshed weekly), picks
//     the Square location payments go to, disconnects; Square's webhooks.
//   - Gateway: payment.Holder on top — a card order is a hold (authorized,
//     not charged), raised for a late tip when Square allows it, completed,
//     voided or refunded; a tip the hold can't carry is charged on the card
//     saved for that order, which is turned off right after.
//
// Card data never reaches main: only Square's ids are kept.
package squarepay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/square"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var (
	ErrDisabled       = errors.New("this server isn't set up for Square")
	ErrNotConnected   = errors.New("this store hasn't connected Square")
	ErrBadState       = errors.New("the Square sign-in link expired or isn't valid — start again")
	ErrDenied         = errors.New("access to Square was not granted")
	ErrUnknownSquare  = errors.New("that Square location isn't one of this account's card-taking locations")
	ErrHoldsOpen      = errors.New("card orders are still waiting to be captured — disconnect after Close batch")
	ErrNoLocation     = errors.New("pick the Square location payments go to")
	ErrUnknownPlace   = errors.New("location not found")
	ErrBadSignature   = errors.New("bad webhook signature")
	errNoPlainKeyInDB = errors.New("token box: ciphertext too short")
)

// Config is how main reaches Square.
type Config struct {
	Env         string // production | sandbox | fake
	AppID       string
	AppSecret   string
	BaseURL     string // Square's API host (or the fake's)
	RedirectURL string // the OAuth callback Square sends the owner back to
	AppURL      string // where the owner lands afterwards (main's SPA)
	TokenKey    []byte // 32 bytes: AES-256-GCM key for tokens + the OAuth state
	WebhookKey  string // the webhook subscription's signature key
	WebhookURL  string // the notification URL Square signs with
}

// Enabled reports whether Square can be used at all.
func (c Config) Enabled() bool {
	return c.AppID != "" && c.AppSecret != "" && c.BaseURL != "" && len(c.TokenKey) == 32
}

// Repo persists connections + holds (repositories.SquareRepository).
type Repo interface {
	Upsert(ctx context.Context, c repositories.SquareConnection) (repositories.SquareConnection, error)
	Get(ctx context.Context, locationID uuid.UUID) (repositories.SquareConnection, error)
	ByMerchant(ctx context.Context, merchantID string) ([]repositories.SquareConnection, error)
	SetLocation(ctx context.Context, locationID uuid.UUID, squareLocationID string) (bool, error)
	UpdateTokens(ctx context.Context, locationID uuid.UUID, accessEnc, refreshEnc []byte, expires time.Time) error
	SetStatus(ctx context.Context, locationID uuid.UUID, status, lastError string) error
	Delete(ctx context.Context, locationID uuid.UUID) (bool, error)
	ToRefresh(ctx context.Context, before time.Time) ([]repositories.SquareConnection, error)
	Holds(ctx context.Context, locationID uuid.UUID, limit int) ([]repositories.CardHold, error)
}

// LocationReader resolves a location's tenant.
type LocationReader interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

type Service struct {
	cfg      Config
	client   *square.Client
	repo     Repo
	locs     LocationReader
	notifier notify.Notifier
	aead     cipher.AEAD
	logger   zerolog.Logger
	now      func() time.Time
}

func NewService(cfg Config, client *square.Client, repo Repo, locs LocationReader, lg zerolog.Logger) (*Service, error) {
	s := &Service{cfg: cfg, client: client, repo: repo, locs: locs, logger: lg.With().Str("module", "squarepay").Logger(), now: time.Now}
	if len(cfg.TokenKey) == 32 {
		block, err := aes.NewCipher(cfg.TokenKey)
		if err != nil {
			return nil, err
		}
		if s.aead, err = cipher.NewGCM(block); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// SetNotifier sends problems (a revoked connection, a dispute) to the owner.
func (s *Service) SetNotifier(n notify.Notifier) { s.notifier = n }

// Enabled reports whether Square is set up on this server.
func (s *Service) Enabled() bool { return s.cfg.Enabled() && s.aead != nil }

// Env is production | sandbox | fake.
func (s *Service) Env() string { return s.cfg.Env }

// AppID is the Square application id (the storefront's Web Payments SDK needs it).
func (s *Service) AppID() string { return s.cfg.AppID }

// --- token box -------------------------------------------------------------------------

func (s *Service) seal(plain string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Service) open(sealed []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return "", errNoPlainKeyInDB
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	return string(plain), err
}

// SealedPrefix marks a text sealed by SealText; a stored value without it is
// plaintext written before sealing existed (read as is).
const SealedPrefix = "sqbox1:"

var errNoTokenKey = errors.New("token box: sealed value but no SQUARE_TOKEN_KEY")

// SealText seals a short secret for a text column (the card nonce an order
// placement keeps while it's in flight) with the same key and AES-GCM box as
// the stores' OAuth tokens: SealedPrefix + base64(nonce|ciphertext). With no
// key (Square off: no Square card token can exist, only the test card's),
// it returns the text unchanged, as the OAuth path stores nothing then.
func (s *Service) SealText(plain string) (string, error) {
	if plain == "" || s.aead == nil {
		return plain, nil
	}
	b, err := s.seal(plain)
	if err != nil {
		return "", err
	}
	return SealedPrefix + base64.RawStdEncoding.EncodeToString(b), nil
}

// OpenText reverses SealText; a value without SealedPrefix is returned as is
// (rows written before the upgrade).
func (s *Service) OpenText(v string) (string, error) {
	rest, ok := strings.CutPrefix(v, SealedPrefix)
	if !ok {
		return v, nil
	}
	if s.aead == nil {
		return "", errNoTokenKey
	}
	b, err := base64.RawStdEncoding.DecodeString(rest)
	if err != nil {
		return "", fmt.Errorf("token box: %w", err)
	}
	return s.open(b)
}

// --- OAuth --------------------------------------------------------------------------

// state = location id . expiry . HMAC (the token key) — no table needed.
func (s *Service) state(locationID uuid.UUID) string {
	body := locationID.String() + "." + strconv.FormatInt(s.now().Add(15*time.Minute).Unix(), 10)
	return body + "." + s.mac(body)
}

func (s *Service) mac(body string) string {
	m := hmac.New(sha256.New, append([]byte("square-oauth-state:"), s.cfg.TokenKey...))
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Service) checkState(state string) (uuid.UUID, error) {
	parts := strings.Split(state, ".")
	if len(parts) != 3 || !hmac.Equal([]byte(s.mac(parts[0]+"."+parts[1])), []byte(parts[2])) {
		return uuid.Nil, ErrBadState
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || s.now().Unix() > exp {
		return uuid.Nil, ErrBadState
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, ErrBadState
	}
	return id, nil
}

// ConnectURL is where the store owner goes to connect Square.
func (s *Service) ConnectURL(locationID uuid.UUID) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	return s.client.AuthorizeURL(s.state(locationID), s.cfg.RedirectURL, s.cfg.Env == "production"), nil
}

// Callback finishes the OAuth flow: it stores the connection and picks the
// Square location when the account has exactly one that takes cards. It
// returns the store and where to send the owner.
func (s *Service) Callback(ctx context.Context, code, state, errParam string) (uuid.UUID, string, error) {
	back := strings.TrimRight(s.cfg.AppURL, "/") + "/app/store/payments?square="
	if !s.Enabled() {
		return uuid.Nil, back + "error", ErrDisabled
	}
	lid, err := s.checkState(state)
	if err != nil {
		return uuid.Nil, back + "expired", err
	}
	if errParam != "" || code == "" {
		return lid, back + "denied", ErrDenied
	}
	loc, err := s.locs.GetLocation(ctx, lid)
	if err != nil {
		return lid, back + "error", ErrUnknownPlace
	}
	tok, err := s.client.ExchangeCode(ctx, code, s.cfg.RedirectURL)
	if err != nil {
		s.logger.Error().Err(err).Str("location_id", lid.String()).Msg("square code exchange failed")
		return lid, back + "error", err
	}
	sqLoc := ""
	if ls, err := s.client.Locations(ctx, tok.AccessToken); err == nil {
		var takes []square.Location
		for _, l := range ls {
			if l.TakesCards() {
				takes = append(takes, l)
			}
		}
		if len(takes) == 1 {
			sqLoc = takes[0].ID
		}
	}
	accessEnc, err := s.seal(tok.AccessToken)
	if err != nil {
		return lid, back + "error", err
	}
	refreshEnc, err := s.seal(tok.RefreshToken)
	if err != nil {
		return lid, back + "error", err
	}
	if _, err := s.repo.Upsert(ctx, repositories.SquareConnection{LocationID: loc.ID, TenantID: loc.TenantID, MerchantID: tok.MerchantID,
		SquareLocationID: sqLoc, AccessEnc: accessEnc, RefreshEnc: refreshEnc, ExpiresAt: tok.Expires()}); err != nil {
		return lid, back + "error", err
	}
	s.logger.Info().Str("location_id", lid.String()).Str("merchant_id", tok.MerchantID).Bool("location_picked", sqLoc != "").Msg("square connected")
	return lid, back + "connected", nil
}

// Status is the Square card on Store › Payments.
type Status struct {
	Enabled          bool              `json:"enabled"`
	Env              string            `json:"env"`
	AppID            string            `json:"app_id"`
	Connected        bool              `json:"connected"`
	State            string            `json:"state"` // connected | revoked | error | "" (never connected)
	MerchantID       string            `json:"merchant_id"`
	SquareLocationID string            `json:"square_location_id"`
	Locations        []square.Location `json:"locations"` // the account's card-taking locations
	LastError        string            `json:"last_error"`
	ConnectedAt      *time.Time        `json:"connected_at"`
	HoldsOpen        int               `json:"holds_open"`
}

func (s *Service) Status(ctx context.Context, locationID uuid.UUID) (Status, error) {
	st := Status{Enabled: s.Enabled(), Env: s.cfg.Env, AppID: s.cfg.AppID, Locations: []square.Location{}}
	c, err := s.repo.Get(ctx, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	at := c.ConnectedAt
	st.State, st.Connected, st.MerchantID, st.SquareLocationID, st.LastError, st.ConnectedAt =
		c.Status, c.Status == "connected", c.MerchantID, c.SquareLocationID, c.LastError, &at
	if holds, err := s.repo.Holds(ctx, locationID, 500); err == nil {
		for _, h := range holds {
			if h.Status == "held" {
				st.HoldsOpen++
			}
		}
	}
	if st.Connected && s.Enabled() {
		tok, err := s.accessToken(ctx, c)
		if err == nil {
			if ls, err := s.client.Locations(ctx, tok); err == nil {
				for _, l := range ls {
					if l.TakesCards() {
						st.Locations = append(st.Locations, l)
					}
				}
			} else {
				st.LastError = err.Error()
			}
		}
	}
	return st, nil
}

// SetLocation picks the Square location the store's payments go to.
func (s *Service) SetLocation(ctx context.Context, locationID uuid.UUID, squareLocationID string) error {
	st, err := s.Status(ctx, locationID)
	if err != nil {
		return err
	}
	if !st.Connected {
		return ErrNotConnected
	}
	for _, l := range st.Locations {
		if l.ID == squareLocationID {
			_, err := s.repo.SetLocation(ctx, locationID, squareLocationID)
			return err
		}
	}
	return ErrUnknownSquare
}

// Disconnect revokes main's access and forgets the account. Refused while
// card holds are open (they could no longer be captured).
func (s *Service) Disconnect(ctx context.Context, locationID uuid.UUID) error {
	c, err := s.repo.Get(ctx, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotConnected
	}
	if err != nil {
		return err
	}
	holds, err := s.repo.Holds(ctx, locationID, 500)
	if err != nil {
		return err
	}
	for _, h := range holds {
		if h.Status == "held" {
			return ErrHoldsOpen
		}
	}
	if s.Enabled() && c.Status != "revoked" {
		if tok, err := s.open(c.AccessEnc); err == nil {
			if err := s.client.Revoke(ctx, tok); err != nil {
				s.logger.Warn().Err(err).Str("location_id", locationID.String()).Msg("square revoke failed (forgetting the connection anyway)")
			}
		}
	}
	_, err = s.repo.Delete(ctx, locationID)
	s.logger.Info().Str("location_id", locationID.String()).Msg("square disconnected")
	return err
}

// --- tokens ---------------------------------------------------------------------------

// RefreshAfter: a token older than this is refreshed (Square: every ≤ 7 days).
const RefreshAfter = 6 * 24 * time.Hour

func (s *Service) accessToken(ctx context.Context, c repositories.SquareConnection) (string, error) {
	if c.Status == "revoked" {
		return "", ErrNotConnected
	}
	if s.now().Add(24*time.Hour).After(c.ExpiresAt) || s.now().Sub(c.RefreshedAt) > RefreshAfter {
		if tok, err := s.refresh(ctx, c); err == nil {
			return tok, nil
		} else if s.now().After(c.ExpiresAt) {
			return "", err
		}
	}
	return s.open(c.AccessEnc)
}

func (s *Service) refresh(ctx context.Context, c repositories.SquareConnection) (string, error) {
	rt, err := s.open(c.RefreshEnc)
	if err != nil {
		return "", err
	}
	tok, err := s.client.Refresh(ctx, rt)
	if err != nil {
		if code := square.StatusOf(err); code == 401 || code == 400 {
			_ = s.repo.SetStatus(ctx, c.LocationID, "error", "refresh failed: "+err.Error())
			s.page(ctx, c.LocationID, "Square disconnected", "main couldn't renew this store's Square connection — reconnect Square in Store › Details, or card orders stop.")
		}
		return "", err
	}
	a, err := s.seal(tok.AccessToken)
	if err != nil {
		return "", err
	}
	r, err := s.seal(tok.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := s.repo.UpdateTokens(ctx, c.LocationID, a, r, tok.Expires()); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// RunRefresh renews tokens older than RefreshAfter every hour until ctx ends.
func (s *Service) RunRefresh(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		cs, err := s.repo.ToRefresh(ctx, s.now().Add(-RefreshAfter))
		if err == nil {
			for _, c := range cs {
				if _, err := s.refresh(ctx, c); err != nil {
					s.logger.Warn().Err(err).Str("location_id", c.LocationID.String()).Msg("square token refresh failed")
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// conn is a store's usable connection: its token + Square location.
func (s *Service) conn(ctx context.Context, locationID uuid.UUID) (string, string, error) {
	if !s.Enabled() {
		return "", "", ErrDisabled
	}
	c, err := s.repo.Get(ctx, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotConnected
	}
	if err != nil {
		return "", "", err
	}
	if c.Status == "revoked" {
		return "", "", ErrNotConnected
	}
	tok, err := s.accessToken(ctx, c)
	if err != nil {
		return "", "", err
	}
	return tok, c.SquareLocationID, nil
}

// Connected reports whether the store can take Square card payments now.
func (s *Service) Connected(ctx context.Context, locationID uuid.UUID) bool {
	if !s.Enabled() {
		return false
	}
	c, err := s.repo.Get(ctx, locationID)
	return err == nil && c.Status == "connected" && c.SquareLocationID != ""
}

func (s *Service) page(ctx context.Context, locationID uuid.UUID, title, msg string) {
	s.logger.Warn().Str("location_id", locationID.String()).Msg(title + ": " + msg)
	if s.notifier != nil {
		_ = s.notifier.Notify(context.WithoutCancel(ctx), notify.Notice{LocationID: locationID, Kind: notify.KindPaymentProblem, Title: title, Message: msg})
	}
}

// key fits an idempotency key into Square's 45 characters.
func key(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:40]
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// --- webhooks -------------------------------------------------------------------------

// VerifyWebhook checks Square's signature: base64(HMAC-SHA256(signature key,
// notification URL + raw body)).
func (s *Service) VerifyWebhook(signature string, body []byte) error {
	if s.cfg.WebhookKey == "" || s.cfg.WebhookURL == "" {
		return ErrBadSignature
	}
	m := hmac.New(sha256.New, []byte(s.cfg.WebhookKey))
	m.Write([]byte(s.cfg.WebhookURL))
	m.Write(body)
	want := base64.StdEncoding.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(strings.TrimSpace(signature))) {
		return ErrBadSignature
	}
	return nil
}

// Event is the slice of a Square webhook main reads.
type Event struct {
	MerchantID string `json:"merchant_id"`
	Type       string `json:"type"`
	EventID    string `json:"event_id"`
	Data       struct {
		ID     string `json:"id"`
		Object struct {
			Payment *square.Payment `json:"payment"`
			Dispute *struct {
				ID       string        `json:"id"`
				Reason   string        `json:"reason"`
				State    string        `json:"state"`
				Amount   *square.Money `json:"amount_money"`
				Payment  string        `json:"disputed_payment_id"`
				Location string        `json:"location_id"`
			} `json:"dispute"`
		} `json:"object"`
	} `json:"data"`
}

// Webhook acts on one verified Square event: a revoked connection and new
// disputes page the owner; payment changes are reconciled by the holds
// service (it is passed in so this package doesn't depend on it).
func (s *Service) Webhook(ctx context.Context, ev Event, onPayment func(context.Context, uuid.UUID, *square.Payment)) error {
	conns, err := s.repo.ByMerchant(ctx, ev.MerchantID)
	if err != nil {
		return err
	}
	switch ev.Type {
	case "oauth.authorization.revoked":
		for _, c := range conns {
			_ = s.repo.SetStatus(ctx, c.LocationID, "revoked", "revoked in Square")
			s.page(ctx, c.LocationID, "Square disconnected", "This store's Square access was revoked in Square. Card orders are off until Square is connected again (Store › Details).")
		}
	case "dispute.created":
		d := ev.Data.Object.Dispute
		for _, c := range conns {
			if d != nil {
				amt := int64(0)
				if d.Amount != nil {
					amt = d.Amount.Amount
				}
				s.page(ctx, c.LocationID, "Card dispute", fmt.Sprintf("A customer disputed a $%d.%02d card payment (%s, payment %s). Answer it in Square's dashboard.",
					amt/100, amt%100, strings.ToLower(strings.ReplaceAll(d.Reason, "_", " ")), d.Payment))
			}
		}
	case "payment.updated", "payment.created":
		if p := ev.Data.Object.Payment; p != nil && onPayment != nil {
			for _, c := range conns {
				onPayment(ctx, c.LocationID, p)
			}
		}
	}
	return nil
}
