// Package customers is the use-case layer for store customer accounts
// (weborder stage 2): sign-in with a phone + code (Twilio Verify; a fake that
// logs the code on dev / staging), sessions (90 days, renewed on use), the
// profile, saved addresses, saved cards (on Square) and deleting the account.
// Accounts are per store and only the store's web channel opens them.
package customers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	authSvc "quiccpos/main/internal/app/auth"
	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// Codes sends and checks sign-in codes (twilioverify.Client or Fake).
type Codes interface {
	Send(ctx context.Context, e164, channel string) error
	Check(ctx context.Context, e164, code string) (bool, error)
}

// Repo persists accounts (repositories.CustomerRepository).
type Repo interface {
	SignIn(ctx context.Context, tenantID, locationID uuid.UUID, phone, name, tokenHash string, expires time.Time) (customer.Customer, bool, int64, error)
	BySession(ctx context.Context, tokenHash string, locationID uuid.UUID, renewTo time.Time) (customer.Customer, error)
	EndSession(ctx context.Context, tokenHash string) error
	PruneSessions(ctx context.Context) (int64, error)
	Get(ctx context.Context, id uuid.UUID) (customer.Customer, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, name, email string, nameSet bool) (customer.Customer, error)
	SetSquareID(ctx context.Context, id uuid.UUID, squareID string) error
	Delete(ctx context.Context, id uuid.UUID) error
	Addresses(ctx context.Context, customerID uuid.UUID) ([]customer.Address, error)
	SaveAddress(ctx context.Context, customerID uuid.UUID, id *uuid.UUID, a customer.Address) (customer.Address, error)
	DeleteAddress(ctx context.Context, customerID, id uuid.UUID) (bool, error)
	Cards(ctx context.Context, customerID uuid.UUID) ([]customer.Card, error)
	Card(ctx context.Context, customerID, id uuid.UUID) (customer.Card, error)
	AddCard(ctx context.Context, customerID uuid.UUID, c customer.Card) (customer.Card, error)
	DeleteCard(ctx context.Context, customerID, id uuid.UUID) (bool, error)
	Orders(ctx context.Context, customerID uuid.UUID, before time.Time, limit int) ([]repositories.ChannelOrder, error)
	Order(ctx context.Context, customerID, orderID uuid.UUID) (repositories.ChannelOrder, error)
	ReplyEmail(ctx context.Context, locationID uuid.UUID) (string, error)
	SetReplyEmail(ctx context.Context, tenantID, locationID uuid.UUID, email string) error
	// Terminal stage 0c (import.go): the bulk import's transaction (commit
	// false = a dry run, rolled back) and the Pi's change feed.
	Import(ctx context.Context, commit bool, fn func(customer.ImportTx) error) error
	Changes(ctx context.Context, locationID uuid.UUID, afterAt time.Time, afterID uuid.UUID, limit int) ([]customer.Change, error)
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]customer.Customer, map[uuid.UUID][]customer.Address, error)
	PruneTombstones(ctx context.Context, before time.Time) (int64, error)
	// Terminal 1d (counter.go): a counter upsert in one transaction, marking
	// its floor event seen with fn's result (duplicate: seen already, nothing kept).
	CounterUpsert(ctx context.Context, tenantID, locationID, eventID uuid.UUID, typ string,
		fn func(tx customer.CounterTx) (string, error)) (string, error)
}

// CardRemover turns a saved card off at the processor (Square).
type CardRemover interface {
	DisableSavedCard(ctx context.Context, locationID uuid.UUID, gateway, cardRef string) error
}

type Service struct {
	repo    Repo
	codes   Codes
	cards   CardRemover
	limiter *limiter
	logger  zerolog.Logger
	now     func() time.Time
}

func NewService(repo Repo, codes Codes, lg zerolog.Logger) *Service {
	return &Service{repo: repo, codes: codes, limiter: newLimiter(), logger: lg.With().Str("module", "customers").Logger(), now: time.Now}
}

// SetCardRemover lets deleting a card / an account remove it from Square.
func (s *Service) SetCardRemover(c CardRemover) { s.cards = c }

// Enabled reports whether codes can be sent.
func (s *Service) Enabled() bool { return s.codes != nil }

// NormPhone keeps the 10 digits of a US phone number ("" when it isn't one).
func NormPhone(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 11 && d[0] == '1' {
		d = d[1:]
	}
	if len(d) != 10 || d[0] == '0' || d[0] == '1' {
		return ""
	}
	return d
}

// Masked shows a phone as (410) •••-0101.
func Masked(phone10 string) string {
	if len(phone10) != 10 {
		return ""
	}
	return "(" + phone10[:3] + ") •••-" + phone10[6:]
}

// --- sign-in -------------------------------------------------------------------------

// CallAfter is how long after a text the customer may ask for a call.
const CallAfter = 30 * time.Second

// SendResult is what the storefront shows after a code went out.
type SendResult struct {
	SentTo        string `json:"sent_to"`
	Channel       string `json:"channel"`
	CallAfterSecs int    `json:"call_after_s"`
}

// SendCode texts (or calls) a sign-in code. Limits per store + phone: 3 sends
// in 10 min, 8 a day; per caller IP: 15 an hour.
func (s *Service) SendCode(ctx context.Context, locationID uuid.UUID, phone, channel, ip string) (SendResult, error) {
	if s.codes == nil {
		return SendResult{}, ErrDisabled
	}
	p := NormPhone(phone)
	if p == "" {
		return SendResult{}, customer.ErrBadPhone
	}
	if channel == "" {
		channel = "sms"
	}
	if channel != "sms" && channel != "call" {
		return SendResult{}, fmt.Errorf("%w: channel must be sms or call", ErrBadInput)
	}
	key := locationID.String() + "|" + p
	if channel == "call" {
		if last, ok := s.limiter.lastSMS(key); ok && s.now().Sub(last) < CallAfter {
			return SendResult{}, customer.ErrCallTooSoon
		}
	}
	if !s.limiter.allow("send10m|"+key, 3, 10*time.Minute) || !s.limiter.allow("sendday|"+key, 8, 24*time.Hour) ||
		(ip != "" && !s.limiter.allow("sendip|"+ip, 15, time.Hour)) {
		return SendResult{}, customer.ErrTooMany
	}
	if err := s.codes.Send(ctx, customer.E164(p), channel); err != nil {
		s.logger.Warn().Err(err).Str("channel", channel).Msg("sign-in code not sent")
		return SendResult{}, ErrSendFailed
	}
	if channel == "sms" {
		s.limiter.markSMS(key)
	}
	return SendResult{SentTo: Masked(p), Channel: channel, CallAfterSecs: int(CallAfter.Seconds())}, nil
}

var (
	ErrDisabled   = errors.New("sign-in isn't set up on this server")
	ErrSendFailed = errors.New("the code couldn't be sent — try again or ask for a call")
	ErrBadInput   = errors.New("bad input")
)

// SignedIn is a new session.
type SignedIn struct {
	Session    string            `json:"session"` // qcs_…: send as X-Customer-Session
	ExpiresAt  time.Time         `json:"expires_at"`
	Customer   customer.Customer `json:"customer"`
	New        bool              `json:"new"`
	JoinedOrds int64             `json:"joined_orders"`
}

// Verify checks the code and opens a session (creating the account the
// first time; a verified phone takes over its guest web orders).
func (s *Service) Verify(ctx context.Context, tenantID, locationID uuid.UUID, phone, code, name, ip string) (SignedIn, error) {
	if s.codes == nil {
		return SignedIn{}, ErrDisabled
	}
	p := NormPhone(phone)
	if p == "" {
		return SignedIn{}, customer.ErrBadPhone
	}
	code = strings.TrimSpace(code)
	key := locationID.String() + "|" + p
	if !s.limiter.allow("check|"+key, 6, 10*time.Minute) || (ip != "" && !s.limiter.allow("checkip|"+ip, 30, time.Hour)) {
		return SignedIn{}, customer.ErrTooMany
	}
	if len(code) < 4 || len(code) > 10 {
		return SignedIn{}, customer.ErrBadCode
	}
	ok, err := s.codes.Check(ctx, customer.E164(p), code)
	if err != nil {
		return SignedIn{}, err
	}
	if !ok {
		return SignedIn{}, customer.ErrBadCode
	}
	raw, hash, err := newSession()
	if err != nil {
		return SignedIn{}, err
	}
	exp := s.now().Add(customer.SessionFor)
	c, created, joined, err := s.repo.SignIn(ctx, tenantID, locationID, p, clip(strings.TrimSpace(name), 80), hash, exp)
	if err != nil {
		return SignedIn{}, err
	}
	s.limiter.reset("check|" + key)
	s.logger.Info().Str("customer_id", c.ID.String()).Bool("new", created).Int64("joined_orders", joined).Msg("customer signed in")
	return SignedIn{Session: raw, ExpiresAt: exp, Customer: c, New: created, JoinedOrds: joined}, nil
}

func newSession() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw := customer.SessionPrefix + hex.EncodeToString(b)
	return raw, authSvc.HashKey(raw), nil
}

// Authenticate is the customer behind a session at this store (renewed).
func (s *Service) Authenticate(ctx context.Context, locationID uuid.UUID, raw string) (customer.Customer, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, customer.SessionPrefix) {
		return customer.Customer{}, customer.ErrBadSession
	}
	c, err := s.repo.BySession(ctx, authSvc.HashKey(raw), locationID, s.now().Add(customer.SessionFor))
	if errors.Is(err, pgx.ErrNoRows) {
		return customer.Customer{}, customer.ErrBadSession
	}
	return c, err
}

// SignOut ends a session.
func (s *Service) SignOut(ctx context.Context, raw string) error {
	return s.repo.EndSession(ctx, authSvc.HashKey(strings.TrimSpace(raw)))
}

// RunPrune drops expired sessions every hour until ctx ends.
func (s *Service) RunPrune(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := s.repo.PruneSessions(ctx); err == nil && n > 0 {
			s.logger.Debug().Int64("sessions", n).Msg("expired customer sessions pruned")
		}
		if n, err := s.repo.PruneTombstones(ctx, s.now().Add(-TombstoneKeep)); err == nil && n > 0 {
			s.logger.Debug().Int64("tombstones", n).Msg("old customer deletions forgotten")
		}
		s.limiter.sweep()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- the account ----------------------------------------------------------------------

// Account is GET /channel/v1/account.
type Account struct {
	customer.Customer
	Addresses []customer.Address `json:"addresses"`
	Cards     []customer.Card    `json:"cards"`
}

func (s *Service) Account(ctx context.Context, c customer.Customer) (Account, error) {
	as, err := s.repo.Addresses(ctx, c.ID)
	if err != nil {
		return Account{}, err
	}
	cs, err := s.repo.Cards(ctx, c.ID)
	if err != nil {
		return Account{}, err
	}
	return Account{Customer: c, Addresses: as, Cards: cs}, nil
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// ValidEmail reports whether e looks like an email address ("" is fine).
func ValidEmail(e string) bool { return e == "" || (len(e) <= 254 && emailRe.MatchString(e)) }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// UpdateProfile sets name and/or email (nil = keep).
func (s *Service) UpdateProfile(ctx context.Context, c customer.Customer, name, email *string) (customer.Customer, error) {
	n, e := c.Name, c.Email
	if name != nil {
		if n = strings.TrimSpace(*name); len([]rune(n)) > 80 {
			return c, customer.ErrTooLong
		}
	}
	if email != nil {
		if e = strings.TrimSpace(*email); !ValidEmail(e) {
			return c, customer.ErrBadEmail
		}
	}
	return s.repo.UpdateProfile(ctx, c.ID, n, e, name != nil && n != c.Name)
}

func cleanAddress(a customer.Address) (customer.Address, error) {
	t := func(s string, n int) string { return clip(strings.TrimSpace(s), n) }
	a.Label, a.Line1, a.Line2, a.City = t(a.Label, 40), t(a.Line1, 120), t(a.Line2, 120), t(a.City, 80)
	a.State, a.Zip, a.Notes = strings.ToUpper(t(a.State, 2)), t(a.Zip, 10), t(a.Notes, 300)
	if a.Line1 == "" || (a.Zip == "" && a.City == "") {
		return a, customer.ErrBadAddress
	}
	return a, nil
}

// SaveAddress adds (id nil) or replaces one of the account's addresses.
func (s *Service) SaveAddress(ctx context.Context, c customer.Customer, id *uuid.UUID, a customer.Address) (customer.Address, error) {
	a, err := cleanAddress(a)
	if err != nil {
		return a, err
	}
	out, err := s.repo.SaveAddress(ctx, c.ID, id, a)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, customer.ErrNotFound
	}
	return out, err
}

func (s *Service) DeleteAddress(ctx context.Context, c customer.Customer, id uuid.UUID) error {
	ok, err := s.repo.DeleteAddress(ctx, c.ID, id)
	if err == nil && !ok {
		return customer.ErrNotFound
	}
	return err
}

// Card is one of the account's saved cards.
func (s *Service) Card(ctx context.Context, c customer.Customer, id uuid.UUID) (customer.Card, error) {
	card, err := s.repo.Card(ctx, c.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return card, customer.ErrNotFound
	}
	return card, err
}

// RememberCard keeps a card the processor saved at checkout (and the
// processor's customer id the first time).
func (s *Service) RememberCard(ctx context.Context, customerID uuid.UUID, processorCustomerID string, card customer.Card) error {
	c, err := s.repo.Get(ctx, customerID)
	if err != nil {
		return err
	}
	if processorCustomerID != "" && c.SquareCustomerID != processorCustomerID {
		if err := s.repo.SetSquareID(ctx, customerID, processorCustomerID); err != nil {
			return err
		}
	}
	_, err = s.repo.AddCard(ctx, customerID, card)
	return err
}

// DeleteCard forgets a saved card and turns it off at the processor.
func (s *Service) DeleteCard(ctx context.Context, c customer.Customer, id uuid.UUID) error {
	card, err := s.Card(ctx, c, id)
	if err != nil {
		return err
	}
	if s.cards != nil {
		if err := s.cards.DisableSavedCard(ctx, c.LocationID, card.Gateway, card.Ref); err != nil {
			s.logger.Warn().Err(err).Str("customer_id", c.ID.String()).Msg("saved card not turned off at the processor (forgotten here anyway)")
		}
	}
	_, err = s.repo.DeleteCard(ctx, c.ID, id)
	return err
}

// Delete removes the account: its saved cards are turned off at the
// processor; its orders stay for the store, unlinked.
func (s *Service) Delete(ctx context.Context, c customer.Customer) error {
	cards, err := s.repo.Cards(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, card := range cards {
		if s.cards != nil {
			if err := s.cards.DisableSavedCard(ctx, c.LocationID, card.Gateway, card.Ref); err != nil {
				s.logger.Warn().Err(err).Msg("saved card not turned off at the processor while deleting an account")
			}
		}
	}
	if err := s.repo.Delete(ctx, c.ID); err != nil {
		return err
	}
	s.logger.Info().Str("customer_id", c.ID.String()).Msg("customer deleted their account")
	return nil
}

// OrderLink is one of the account's order links (customer.ErrNotFound = not theirs).
func (s *Service) OrderLink(ctx context.Context, c customer.Customer, orderID uuid.UUID) (repositories.ChannelOrder, error) {
	l, err := s.repo.Order(ctx, c.ID, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, customer.ErrNotFound
	}
	return l, err
}

// OrderLinks are the account's orders, newest first.
func (s *Service) OrderLinks(ctx context.Context, c customer.Customer, before time.Time, limit int) ([]repositories.ChannelOrder, error) {
	return s.repo.Orders(ctx, c.ID, before, limit)
}

// --- the store's reply-to email ----------------------------------------------------------

func (s *Service) ReplyEmail(ctx context.Context, locationID uuid.UUID) (string, error) {
	return s.repo.ReplyEmail(ctx, locationID)
}

func (s *Service) SetReplyEmail(ctx context.Context, tenantID, locationID uuid.UUID, email string) error {
	email = strings.TrimSpace(email)
	if !ValidEmail(email) {
		return customer.ErrBadEmail
	}
	return s.repo.SetReplyEmail(ctx, tenantID, locationID, email)
}

// --- limits -------------------------------------------------------------------------------

// limiter is an in-process sliding window (main runs as one process).
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	sms  map[string]time.Time
	now  func() time.Time
}

func newLimiter() *limiter {
	return &limiter{hits: map[string][]time.Time{}, sms: map[string]time.Time{}, now: time.Now}
}

func (l *limiter) allow(key string, max int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < per {
			keep = append(keep, t)
		}
	}
	if len(keep) >= max {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

func (l *limiter) markSMS(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sms[key] = l.now()
}

func (l *limiter) lastSMS(key string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.sms[key]
	return t, ok
}

// sweep drops windows older than a day.
func (l *limiter) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, ts := range l.hits {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > 24*time.Hour {
			delete(l.hits, k)
		}
	}
	for k, t := range l.sms {
		if now.Sub(t) > time.Hour {
			delete(l.sms, k)
		}
	}
}

// ByID is one customer (the placement sweeper restores a signed-in order's account).
func (s *Service) ByID(ctx context.Context, id uuid.UUID) (customer.Customer, error) {
	return s.repo.Get(ctx, id)
}
