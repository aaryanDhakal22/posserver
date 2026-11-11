// Package pricingApp is the use-case layer of pricing (main 3.0 step 4): a
// store's pricing settings (defaults + per channel kind sets) and deals, the
// pricing context a channel prices with (its menu, the bank's item facts,
// its settings and deals, the store's open order types) and quotes.
package pricingApp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/domain/storestatus"
	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var (
	ErrNotFound    = errors.New("not found in this location")
	ErrBadKind     = errors.New("kind must be one of: web, operator, counter, vendor")
	ErrNotTheStore = errors.New("an item, category or size in the deal isn't this store's")
)

// Repo is the pricing store (repositories.PricingRepository).
type Repo interface {
	Settings(ctx context.Context, locationID uuid.UUID) (*pricing.StoreSettings, map[string]pricing.KindOverride, error)
	SaveSettings(ctx context.Context, tenantID, locationID uuid.UUID, s pricing.StoreSettings, over map[string]pricing.KindOverride) error
	Deals(ctx context.Context, locationID uuid.UUID) ([]pricing.StoreDeal, error)
	Deal(ctx context.Context, locationID, id uuid.UUID) (pricing.StoreDeal, error)
	CreateDeal(ctx context.Context, tenantID, locationID uuid.UUID, d pricing.StoreDeal) (pricing.StoreDeal, error)
	UpdateDeal(ctx context.Context, locationID, id uuid.UUID, d pricing.StoreDeal) (pricing.StoreDeal, error)
	DeleteDeal(ctx context.Context, locationID, id uuid.UUID) (bool, error)
}

// Views reads the bank and a channel's menu (the menu views service).
type Views interface {
	Bank(ctx context.Context, locationID uuid.UUID) (*menu.Bank, error)
	Version(ctx context.Context, locationID uuid.UUID) (menu.Version, error)
	ViewFor(ctx context.Context, tenantID, locationID uuid.UUID, kind menuview.Kind, viewID *uuid.UUID) (*menuview.View, error)
	ChannelBuild(ctx context.Context, v *menuview.View) ([]byte, int64, error)
}

// Ordering reads the store's ordering settings (store status).
type Ordering interface {
	Settings(ctx context.Context, tenantID, locationID uuid.UUID) (storestatus.Settings, error)
}

// Locations reads a location (tenancy).
type Locations interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

type Service struct {
	repo     Repo
	views    Views
	ordering Ordering
	locs     Locations
	logger   zerolog.Logger
	now      func() time.Time
	// facts caches each location's item facts by menu version.
	mu    sync.Mutex
	facts map[uuid.UUID]factsAt
}

type factsAt struct {
	version int64
	items   map[string]pricing.ItemFacts
	cats    map[string]bool
	sizes   map[string]bool
}

func NewService(repo Repo, views Views, ordering Ordering, locs Locations, lg zerolog.Logger) *Service {
	return &Service{repo: repo, views: views, ordering: ordering, locs: locs, logger: lg.With().Str("module", "pricing-service").Logger(),
		now: time.Now, facts: map[uuid.UUID]factsAt{}}
}

// --- settings ------------------------------------------------------------------------

// Settings are the store's settings (the defaults when never saved) and its
// kinds' overrides.
func (s *Service) Settings(ctx context.Context, locationID uuid.UUID) (pricing.StoreSettings, map[string]pricing.KindOverride, error) {
	st, over, err := s.repo.Settings(ctx, locationID)
	if err != nil {
		return pricing.StoreSettings{}, nil, err
	}
	if st == nil {
		d := pricing.DefaultStore()
		return d, pricing.DefaultOverrides(), nil
	}
	if st.Tips.Presets == nil {
		st.Tips.Presets = []int{}
	}
	if st.HalfToppingPrice == "" {
		st.HalfToppingPrice = pricing.HalfFull // saved before half and half
	}
	return *st, over, nil
}

func (s *Service) SaveSettings(ctx context.Context, tenantID, locationID uuid.UUID, st pricing.StoreSettings, over map[string]pricing.KindOverride) error {
	if err := st.Validate(); err != nil {
		return err
	}
	for k, o := range over {
		if !validKind(k) {
			return ErrBadKind
		}
		if err := o.Validate(); err != nil {
			return err
		}
		over[k] = o
	}
	return s.repo.SaveSettings(ctx, tenantID, locationID, st, over)
}

func validKind(k string) bool {
	for _, x := range pricing.Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// --- deals ---------------------------------------------------------------------------

func (s *Service) Deals(ctx context.Context, locationID uuid.UUID) ([]pricing.StoreDeal, error) {
	return s.repo.Deals(ctx, locationID)
}

func (s *Service) CreateDeal(ctx context.Context, tenantID, locationID uuid.UUID, d pricing.StoreDeal) (pricing.StoreDeal, error) {
	if err := s.validDeal(ctx, locationID, &d); err != nil {
		return d, err
	}
	return s.repo.CreateDeal(ctx, tenantID, locationID, d)
}

func (s *Service) UpdateDeal(ctx context.Context, locationID, id uuid.UUID, d pricing.StoreDeal) (pricing.StoreDeal, error) {
	if _, err := s.repo.Deal(ctx, locationID, id); err != nil {
		return d, notFound(err)
	}
	if err := s.validDeal(ctx, locationID, &d); err != nil {
		return d, err
	}
	out, err := s.repo.UpdateDeal(ctx, locationID, id, d)
	return out, notFound(err)
}

func (s *Service) DeleteDeal(ctx context.Context, locationID, id uuid.UUID) error {
	ok, err := s.repo.DeleteDeal(ctx, locationID, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// validDeal checks the deal's shape, its channel kinds, and that every item,
// category and size it names is the store's.
func (s *Service) validDeal(ctx context.Context, locationID uuid.UUID, d *pricing.StoreDeal) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if err := d.ValidKinds(); err != nil {
		return err
	}
	f, err := s.itemFacts(ctx, locationID)
	if err != nil {
		return err
	}
	check := func(e *pricing.Eligible) error {
		if e == nil {
			return nil
		}
		for _, id := range e.Items {
			if _, ok := f.items[id]; !ok {
				return ErrNotTheStore
			}
		}
		for _, id := range e.Categories {
			if !f.cats[id] {
				return ErrNotTheStore
			}
		}
		for _, id := range e.Sizes {
			if !f.sizes[id] {
				return ErrNotTheStore
			}
		}
		return nil
	}
	if err := check(d.Eligible); err != nil {
		return err
	}
	for i := range d.Slots {
		if err := check(&d.Slots[i].Eligible); err != nil {
			return err
		}
	}
	return nil
}

// itemFacts are the bank's item facts (category, tax, plain pizza), cached per version.
func (s *Service) itemFacts(ctx context.Context, locationID uuid.UUID) (factsAt, error) {
	ver, err := s.views.Version(ctx, locationID)
	if err != nil {
		return factsAt{}, err
	}
	s.mu.Lock()
	cached, ok := s.facts[locationID]
	s.mu.Unlock()
	if ok && cached.version == ver.Version {
		return cached, nil
	}
	bank, err := s.views.Bank(ctx, locationID)
	if err != nil {
		return factsAt{}, err
	}
	f := factsAt{version: bank.Version, items: map[string]pricing.ItemFacts{}, cats: map[string]bool{}, sizes: map[string]bool{}}
	for _, m := range bank.Menus {
		for _, c := range m.Categories {
			f.cats[c.ID.String()] = true
			for _, it := range c.Items {
				f.items[it.ID.String()] = pricing.ItemFacts{CategoryID: c.ID.String(), TaxBps: it.TaxRateBps, HalfBase: pricing.HalfBaseOf(it.Attributes),
					NoTax: pricing.FlagOf(it.Attributes, pricing.NoTaxAttr), OpenPrice: pricing.FlagOf(it.Attributes, pricing.OpenPriceAttr)}
			}
		}
	}
	for _, z := range bank.Sizes {
		f.sizes[z.ID.String()] = true
	}
	s.mu.Lock()
	s.facts[locationID] = f
	s.mu.Unlock()
	return f, nil
}

// --- the context + quotes ----------------------------------------------------------------

// Context is what a channel of a kind (reading viewID, nil = its kind's
// default view) prices with, and its menu version.
func (s *Service) Context(ctx context.Context, tenantID, locationID uuid.UUID, kind string, viewID *uuid.UUID) (*pricing.Context, int64, error) {
	if !validKind(kind) {
		return nil, 0, ErrBadKind
	}
	l, err := s.locs.GetLocation(ctx, locationID)
	if err != nil || l == nil || l.TenantID != tenantID {
		return nil, 0, ErrNotFound
	}
	v, err := s.views.ViewFor(ctx, tenantID, locationID, menuview.Kind(kind), viewID)
	if err != nil {
		return nil, 0, err
	}
	raw, version, err := s.views.ChannelBuild(ctx, v)
	if err != nil {
		return nil, 0, err
	}
	var m pricing.Menu
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, 0, fmt.Errorf("channel menu: %w", err)
	}
	f, err := s.itemFacts(ctx, locationID)
	if err != nil {
		return nil, 0, err
	}
	st, over, err := s.Settings(ctx, locationID)
	if err != nil {
		return nil, 0, err
	}
	all, err := s.repo.Deals(ctx, locationID)
	if err != nil {
		return nil, 0, err
	}
	deals := []pricing.Deal{}
	for _, d := range all {
		if !d.Active {
			continue
		}
		for _, k := range d.ChannelKinds {
			if k == kind {
				deals = append(deals, d.Deal)
				break
			}
		}
	}
	ord, err := s.ordering.Settings(ctx, tenantID, locationID)
	if err != nil {
		return nil, 0, err
	}
	return &pricing.Context{Menu: m, Items: f.items, Settings: pricing.Effective(st, over[kind]), Deals: deals, Timezone: l.Timezone,
		Types: pricing.Types{Pickup: ord.PickupEnabled, Delivery: ord.DeliveryEnabled}, OpenPrices: kind == string(menuview.KindCounter)}, version, nil
}

// Quote prices a cart for a channel of a kind.
func (s *Service) Quote(ctx context.Context, tenantID, locationID uuid.UUID, kind string, viewID *uuid.UUID, cart pricing.Cart) (pricing.Quote, error) {
	c, _, err := s.Context(ctx, tenantID, locationID, kind, viewID)
	if err != nil {
		return pricing.Quote{}, err
	}
	if len(cart.Lines) > 100 {
		cart.Lines = cart.Lines[:100]
	}
	return pricing.Price(*c, cart, s.now()), nil
}
