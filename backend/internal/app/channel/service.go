// Package channelApp is the use-case layer for sales channels (main 3.0):
// admin management of a location's channels and their keys, and resolving a
// /channel/v1 bearer key to its channel.
package channelApp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	authSvc "quiccpos/main/internal/app/auth"
	siteApp "quiccpos/main/internal/app/site"
	channelDom "quiccpos/main/internal/domain/channel"
	operatorDom "quiccpos/main/internal/domain/operator"
	siteDom "quiccpos/main/internal/domain/site"
	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// KeyPrefix starts every channel key.
const KeyPrefix = "qch_"

var (
	ErrNotFound     = errors.New("channel not found")
	ErrInvalidKey   = errors.New("invalid or revoked channel key")
	ErrBuiltin      = errors.New("a built-in channel has no key and can't be revoked or deleted")
	ErrBadKind      = errors.New("kind must be one of: web, operator, counter, vendor")
	ErrNameRequired = errors.New("name is required")
	ErrBadPayment   = errors.New("payments must be card and/or cash")
	ErrUnknownPlace = errors.New("location not found")
	ErrBadView      = errors.New("view must be one of this store's views of the channel's kind")
)

type builtinKey struct {
	location uuid.UUID
	kind     channelDom.Kind
}

// LocationReader resolves a location's tenant (the tenancy service).
type LocationReader interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

// ViewReader looks up a menu view's location and kind (the menu views service).
type ViewReader interface {
	ViewKind(ctx context.Context, locationID, viewID uuid.UUID) (string, error)
}

type Service struct {
	builtinMu sync.Mutex
	builtins  map[builtinKey]uuid.UUID // a location's built-in channel of a kind
	operators OperatorAuth
	sites     SiteAuth
	repo      channelDom.Repository
	locs      LocationReader
	views     ViewReader
	logger    zerolog.Logger
}

// SetViews plugs in the menu views (set once at startup).
func (s *Service) SetViews(v ViewReader) { s.views = v }

// Edit renames / re-permissions a channel and (setView) points it at a view,
// checking everything before it writes anything.
func (s *Service) Edit(ctx context.Context, id uuid.UUID, name *string, perms *channelDom.Permissions, setView bool, viewID *uuid.UUID) (*channelDom.Channel, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n, p := cur.Name, cur.Permissions
	if name != nil {
		if n = strings.TrimSpace(*name); n == "" {
			return nil, ErrNameRequired
		}
	}
	if perms != nil {
		if err := validPermissions(*perms); err != nil {
			return nil, err
		}
		p = *perms
	}
	if setView && viewID != nil {
		if s.views == nil {
			return nil, ErrBadView
		}
		if kind, err := s.views.ViewKind(ctx, cur.LocationID, *viewID); err != nil || kind != string(cur.Kind) {
			return nil, ErrBadView
		}
	}
	c, err := s.repo.Update(ctx, id, n, p)
	if err != nil {
		return nil, notFound(err)
	}
	if setView {
		c, err = s.repo.SetView(ctx, id, viewID)
	}
	return c, notFound(err)
}

func NewService(repo channelDom.Repository, locs LocationReader, lg zerolog.Logger) *Service {
	return &Service{repo: repo, locs: locs, builtins: map[builtinKey]uuid.UUID{}, logger: lg.With().Str("module", "channel-service").Logger()}
}

// Create adds a channel to a location and returns it with its key, shown
// once (only the hash is stored). nil permissions = the kind's defaults.
func (s *Service) Create(ctx context.Context, locationID uuid.UUID, kind channelDom.Kind, name string,
	perms *channelDom.Permissions) (*channelDom.Channel, string, error) {
	if !kind.Valid() {
		return nil, "", ErrBadKind
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", ErrNameRequired
	}
	p := channelDom.DefaultPermissions(kind)
	if perms != nil {
		if err := validPermissions(*perms); err != nil {
			return nil, "", err
		}
		p = *perms
	}
	loc, err := s.locs.GetLocation(ctx, locationID)
	if err != nil {
		return nil, "", ErrUnknownPlace
	}
	key, err := newKey()
	if err != nil {
		return nil, "", err
	}
	c, err := s.repo.Create(ctx, channelDom.NewChannel{TenantID: loc.TenantID, LocationID: loc.ID, Kind: kind, Name: name,
		KeyHash: authSvc.HashKey(key), KeyPrefix: key[:len(KeyPrefix)+8], Permissions: p})
	if err != nil {
		return nil, "", err
	}
	s.logger.Info().Str("channel_id", c.ID.String()).Str("kind", string(kind)).Str("location_id", loc.ID.String()).Msg("channel created")
	return c, key, nil
}

func (s *Service) List(ctx context.Context, locationID *uuid.UUID) ([]channelDom.Channel, error) {
	return s.repo.List(ctx, locationID)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*channelDom.Channel, error) {
	c, err := s.repo.Get(ctx, id)
	return c, notFound(err)
}

// Rotate issues a new key (the old one stops working at once; a revoked
// channel is active again).
func (s *Service) Rotate(ctx context.Context, id uuid.UUID) (*channelDom.Channel, string, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if cur.Builtin {
		return nil, "", ErrBuiltin
	}
	key, err := newKey()
	if err != nil {
		return nil, "", err
	}
	c, err := s.repo.SetKey(ctx, id, authSvc.HashKey(key), key[:len(KeyPrefix)+8])
	if err != nil {
		return nil, "", notFound(err)
	}
	s.logger.Info().Str("channel_id", id.String()).Msg("channel key rotated")
	return c, key, nil
}

// Revoke stops the channel's key (idempotent).
func (s *Service) Revoke(ctx context.Context, id uuid.UUID) (*channelDom.Channel, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Builtin {
		return nil, ErrBuiltin
	}
	c, err := s.repo.Revoke(ctx, id)
	if err == nil {
		s.logger.Info().Str("channel_id", id.String()).Msg("channel revoked")
	}
	return c, notFound(err)
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if cur.Builtin {
		return ErrBuiltin
	}
	ok, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// Authenticate resolves a bearer key to its active channel. No caching:
// a revoke or a rotation takes effect on the next request.
func (s *Service) Authenticate(ctx context.Context, key string) (*channelDom.Channel, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, KeyPrefix) {
		return nil, ErrInvalidKey
	}
	c, err := s.repo.Authenticate(ctx, authSvc.HashKey(key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidKey
	}
	if err != nil {
		return nil, fmt.Errorf("channel lookup: %w", err)
	}
	go func(id uuid.UUID) { _ = s.repo.Touch(context.Background(), id) }(c.ID)
	return c, nil
}

func validPermissions(p channelDom.Permissions) error {
	for _, m := range p.Payments {
		if m != channelDom.PayCard && m != channelDom.PayCash {
			return ErrBadPayment
		}
	}
	if p.Payments == nil || slices.Contains(p.Payments, "") {
		return ErrBadPayment
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func newKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return KeyPrefix + hex.EncodeToString(buf), nil
}

// BuiltinVendor is a location's built-in vendor channel (Brygid's feed),
// created on first use. It has no key: it is how the SQS vendor feed's
// orders are linked to a channel (main 3.0).
func (s *Service) BuiltinVendor(ctx context.Context, tenantID, locationID uuid.UUID) (uuid.UUID, error) {
	c, err := s.builtin(ctx, tenantID, locationID, channelDom.KindVendor, "Brygid")
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

// builtin is a location's built-in channel of a kind, created on first use
// (cached; permissions are read fresh on every use).
func (s *Service) builtin(ctx context.Context, tenantID, locationID uuid.UUID, kind channelDom.Kind, name string) (*channelDom.Channel, error) {
	s.builtinMu.Lock()
	defer s.builtinMu.Unlock()
	key := builtinKey{locationID, kind}
	if id, ok := s.builtins[key]; ok {
		if c, err := s.repo.Get(ctx, id); err == nil {
			return c, nil
		}
		delete(s.builtins, key)
	}
	c, err := s.repo.Builtin(ctx, locationID, kind)
	if errors.Is(err, pgx.ErrNoRows) {
		c, err = s.repo.Create(ctx, channelDom.NewChannel{TenantID: tenantID, LocationID: locationID, Kind: kind,
			Name: name, Permissions: channelDom.DefaultPermissions(kind), Builtin: true})
	}
	if err != nil {
		return nil, err
	}
	s.builtins[key] = c.ID
	return c, nil
}

// OperatorAuth checks an operator's channel sign-in (the operator service).
type OperatorAuth interface {
	AuthenticateSession(ctx context.Context, raw string) (*operatorDom.Operator, []operatorDom.AllowedLocation, error)
}

// SetOperators lets operators' sign-ins open /channel/v1 (main 3.0 step 7).
func (s *Service) SetOperators(o OperatorAuth) { s.operators = o }

// ErrLocationNotAllowed: the operator may not take orders for that store.
var ErrLocationNotAllowed = errors.New("this operator may not take orders for that store (X-Location-ID)")

// AuthenticateOperator turns an operator's sign-in + the store they work on
// into that store's built-in Call center channel and the operator.
func (s *Service) AuthenticateOperator(ctx context.Context, raw string, locationID uuid.UUID) (*channelDom.Channel, *channelDom.Operator, error) {
	if s.operators == nil {
		return nil, nil, ErrInvalidKey
	}
	op, allowed, err := s.operators.AuthenticateSession(ctx, raw)
	if err != nil {
		return nil, nil, ErrInvalidKey
	}
	var tenantID uuid.UUID
	for _, a := range allowed {
		if a.LocationID == locationID {
			tenantID = a.TenantID
		}
	}
	if tenantID == uuid.Nil {
		return nil, nil, ErrLocationNotAllowed
	}
	c, err := s.builtin(ctx, tenantID, locationID, channelDom.KindOperator, "Call center")
	if err != nil {
		return nil, nil, fmt.Errorf("operator channel: %w", err)
	}
	return c, &channelDom.Operator{ID: op.ID, Username: op.Username, DisplayName: op.DisplayName}, nil
}

// --- the storefront (weborder stage 2) ----------------------------------------------

// SiteAuth checks the storefront's platform key and resolves the host it is
// serving (the site service).
type SiteAuth interface {
	Authenticate(ctx context.Context, key string) (siteDom.PlatformKey, error)
	Resolve(ctx context.Context, host string) (siteDom.Domain, error)
}

// SetSites lets the storefront's platform key open /channel/v1.
func (s *Service) SetSites(a SiteAuth) { s.sites = a }

var (
	ErrUnknownSite  = errors.New("no store is served on that host")
	ErrHostRequired = errors.New("send the host the storefront is serving as X-Site-Host")
)

// WebChannelName is the name of a store's built-in web channel.
const WebChannelName = "Website"

// AuthenticateSite turns the storefront's platform key + the host it serves
// into that store's built-in web channel.
func (s *Service) AuthenticateSite(ctx context.Context, key, host string) (*channelDom.Channel, error) {
	if s.sites == nil {
		return nil, ErrInvalidKey
	}
	if _, err := s.sites.Authenticate(ctx, key); errors.Is(err, siteApp.ErrInvalidKey) {
		return nil, ErrInvalidKey
	} else if err != nil {
		return nil, fmt.Errorf("platform key lookup: %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return nil, ErrHostRequired
	}
	d, err := s.sites.Resolve(ctx, host)
	if errors.Is(err, siteApp.ErrUnknownSite) {
		return nil, ErrUnknownSite
	}
	if err != nil {
		return nil, fmt.Errorf("site lookup: %w", err)
	}
	c, err := s.builtin(ctx, d.TenantID, d.LocationID, channelDom.KindWeb, WebChannelName)
	if err != nil {
		return nil, fmt.Errorf("web channel: %w", err)
	}
	return c, nil
}

// EnsureBuiltinWeb creates the store's built-in web channel if it has none.
func (s *Service) EnsureBuiltinWeb(ctx context.Context, tenantID, locationID uuid.UUID) error {
	_, err := s.builtin(ctx, tenantID, locationID, channelDom.KindWeb, WebChannelName)
	return err
}
