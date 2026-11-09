// Package menuviewApp is the use-case layer of the menu bank and its views
// (main 3.0): the bank's sizes and per-size data, each location's views (one
// default per channel kind, more on demand), their entries, and compiled
// builds cached per menu version. It also maps a vendor's item names to bank
// items for kitchen routing (the Brygid view).
package menuviewApp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	menuApp "quiccpos/main/internal/app/menu"
	menuDom "quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var (
	ErrNotFound    = errors.New("not found in this location")
	ErrBadSize     = errors.New("a size needs a name")
	ErrSizeTaken   = errors.New("a size with that name already exists")
	ErrBadPrice    = errors.New("prices can't be negative")
	ErrUnknownSize = errors.New("size not in this store")
	ErrBadKind     = errors.New("kind must be one of: web, operator, counter, vendor")
	ErrBadName     = errors.New("name is required")
	ErrBadRules    = errors.New("rules: markup_bps must be -5000..10000 and round none or ceil_99")
	ErrBadEntry    = errors.New("bad entry")
)

// DefaultNames are a location's default view per kind.
var DefaultNames = map[menuview.Kind]string{
	menuview.KindWeb: "Web", menuview.KindOperator: "Call center", menuview.KindCounter: "Counter", menuview.KindVendor: "Brygid",
}

// Repo is the bank + views store (repositories.MenuBankRepository).
type Repo interface {
	LoadBank(ctx context.Context, locationID uuid.UUID) (*menuDom.Bank, error)
	Version(ctx context.Context, locationID uuid.UUID) (menuDom.Version, error)
	CreateSize(ctx context.Context, tenantID, locationID uuid.UUID, name, short string, position int) (*menuDom.Size, error)
	ListSizes(ctx context.Context, locationID uuid.UUID) ([]menuDom.Size, error)
	GetSize(ctx context.Context, id, tenantID uuid.UUID) (*menuDom.Size, error)
	UpdateSize(ctx context.Context, id, tenantID uuid.UUID, name, short string) (*menuDom.Size, error)
	ReorderSizes(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error
	DeleteSize(ctx context.Context, id, tenantID uuid.UUID) (bool, error)
	SetItemBank(ctx context.Context, tenantID, locationID, itemID uuid.UUID, x menuDom.ItemExtra) error
	SetGroupBank(ctx context.Context, tenantID, locationID, groupID uuid.UUID, x menuDom.GroupExtra) error
	CreateView(ctx context.Context, tenantID, locationID uuid.UUID, kind menuview.Kind, name string, rules menuview.Rules) (*menuview.View, error)
	GetView(ctx context.Context, id uuid.UUID) (*menuview.View, error)
	FirstViewOfKind(ctx context.Context, locationID uuid.UUID, kind menuview.Kind) (*menuview.View, error)
	EnsureDefaultView(ctx context.Context, tenantID, locationID uuid.UUID, kind menuview.Kind, name string, rules menuview.Rules) (*menuview.View, error)
	ListViews(ctx context.Context, locationID uuid.UUID) ([]menuview.View, error)
	UpdateView(ctx context.Context, id uuid.UUID, name string, rules menuview.Rules) (*menuview.View, error)
	DeleteView(ctx context.Context, id uuid.UUID) (bool, error)
	CreateEntry(ctx context.Context, v *menuview.View, e menuview.Entry) (*menuview.Entry, error)
	GetEntry(ctx context.Context, id uuid.UUID) (*menuview.Entry, error)
	ListEntries(ctx context.Context, viewID uuid.UUID) ([]menuview.Entry, error)
	UpdateEntry(ctx context.Context, e menuview.Entry) (*menuview.Entry, error)
	DeleteEntry(ctx context.Context, id uuid.UUID) (bool, error)
	CachedBuild(ctx context.Context, viewID uuid.UUID) (json.RawMessage, int64, bool, error)
	SaveBuild(ctx context.Context, viewID uuid.UUID, version int64, build json.RawMessage) error
}

// Unmatched lists vendor names recent orders carried that nothing routed
// (the menu service's UnmatchedNames).
type Unmatched interface {
	UnmatchedNames(ctx context.Context, tenantID, locationID uuid.UUID, since time.Time) ([]menuApp.UnmatchedName, error)
}

type Service struct {
	repo      Repo
	unmatched Unmatched
	logger    zerolog.Logger
	// mem holds each view's latest build (by menu version) in this process.
	mu  sync.Mutex
	mem map[uuid.UUID]built
}

func NewService(repo Repo, unmatched Unmatched, lg zerolog.Logger) *Service {
	return &Service{repo: repo, unmatched: unmatched, logger: lg.With().Str("module", "menuview-service").Logger(), mem: map[uuid.UUID]built{}}
}

// --- the bank --------------------------------------------------------------------

func (s *Service) Bank(ctx context.Context, locationID uuid.UUID) (*menuDom.Bank, error) {
	return s.repo.LoadBank(ctx, locationID)
}

// Version is a location's menu version (bumped by every bank or view change).
func (s *Service) Version(ctx context.Context, locationID uuid.UUID) (menuDom.Version, error) {
	return s.repo.Version(ctx, locationID)
}

func (s *Service) Sizes(ctx context.Context, locationID uuid.UUID) ([]menuDom.Size, error) {
	return s.repo.ListSizes(ctx, locationID)
}

func (s *Service) CreateSize(ctx context.Context, tenantID, locationID uuid.UUID, name, short string) (*menuDom.Size, error) {
	name, short = strings.TrimSpace(name), strings.TrimSpace(short)
	if name == "" {
		return nil, ErrBadSize
	}
	have, err := s.repo.ListSizes(ctx, locationID)
	if err != nil {
		return nil, err
	}
	for _, x := range have {
		if strings.EqualFold(x.Name, name) {
			return nil, ErrSizeTaken
		}
	}
	return s.repo.CreateSize(ctx, tenantID, locationID, name, short, len(have))
}

func (s *Service) UpdateSize(ctx context.Context, tenantID, locationID, id uuid.UUID, name, short string) (*menuDom.Size, error) {
	if err := s.ownSize(ctx, tenantID, locationID, id); err != nil {
		return nil, err
	}
	name, short = strings.TrimSpace(name), strings.TrimSpace(short)
	if name == "" {
		return nil, ErrBadSize
	}
	have, err := s.repo.ListSizes(ctx, locationID)
	if err != nil {
		return nil, err
	}
	for _, x := range have {
		if x.ID != id && strings.EqualFold(x.Name, name) {
			return nil, ErrSizeTaken
		}
	}
	return s.repo.UpdateSize(ctx, id, tenantID, name, short)
}

func (s *Service) ReorderSizes(ctx context.Context, tenantID, locationID uuid.UUID, ids []uuid.UUID) error {
	for _, id := range ids {
		if err := s.ownSize(ctx, tenantID, locationID, id); err != nil {
			return err
		}
	}
	return s.repo.ReorderSizes(ctx, tenantID, ids)
}

func (s *Service) DeleteSize(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	if err := s.ownSize(ctx, tenantID, locationID, id); err != nil {
		return err
	}
	ok, err := s.repo.DeleteSize(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func (s *Service) ownSize(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	sz, err := s.repo.GetSize(ctx, id, tenantID)
	if err != nil || sz.LocationID != locationID {
		return ErrNotFound
	}
	return nil
}

// SetItemBank replaces an item's kitchen name, SKU and sizes (every size
// must be one of the store's; no repeats; prices ≥ 0).
func (s *Service) SetItemBank(ctx context.Context, tenantID, locationID, itemID uuid.UUID, x menuDom.ItemExtra) error {
	bank, err := s.repo.LoadBank(ctx, locationID)
	if err != nil {
		return err
	}
	if _, _, ok := bank.Item(itemID); !ok {
		return ErrNotFound
	}
	seen := map[uuid.UUID]bool{}
	for _, sz := range x.Sizes {
		if _, ok := bank.SizeByID(sz.SizeID); !ok || seen[sz.SizeID] {
			return ErrUnknownSize
		}
		if sz.PriceCents < 0 {
			return ErrBadPrice
		}
		seen[sz.SizeID] = true
	}
	x.KitchenName, x.SKU = strings.TrimSpace(x.KitchenName), strings.TrimSpace(x.SKU)
	return s.repo.SetItemBank(ctx, tenantID, locationID, itemID, x)
}

// SetGroupBank sets a group's halves flag and its options' defaults, kitchen
// names and per-size prices.
func (s *Service) SetGroupBank(ctx context.Context, tenantID, locationID, groupID uuid.UUID, x menuDom.GroupExtra) error {
	bank, err := s.repo.LoadBank(ctx, locationID)
	if err != nil {
		return err
	}
	g, ok := bank.Groups[groupID]
	if !ok {
		return ErrNotFound
	}
	opts := map[uuid.UUID]bool{}
	for _, o := range g.Options {
		opts[o.ID] = true
	}
	for i, o := range x.Options {
		if !opts[o.OptionID] {
			return ErrNotFound
		}
		seen := map[uuid.UUID]bool{}
		for _, p := range o.Prices {
			if _, ok := bank.SizeByID(p.SizeID); !ok || seen[p.SizeID] {
				return ErrUnknownSize
			}
			if p.PriceCents < 0 {
				return ErrBadPrice
			}
			seen[p.SizeID] = true
		}
		x.Options[i].KitchenName = strings.TrimSpace(o.KitchenName)
	}
	return s.repo.SetGroupBank(ctx, tenantID, locationID, groupID, x)
}

// --- views ---------------------------------------------------------------------

// Views lists a location's views, creating the default one of every kind on
// first use.
func (s *Service) Views(ctx context.Context, tenantID, locationID uuid.UUID) ([]menuview.View, error) {
	for _, k := range menuview.Kinds {
		if _, err := s.DefaultView(ctx, tenantID, locationID, k); err != nil {
			return nil, err
		}
	}
	return s.repo.ListViews(ctx, locationID)
}

// DefaultView is a location's first view of a kind, created when missing.
func (s *Service) DefaultView(ctx context.Context, tenantID, locationID uuid.UUID, kind menuview.Kind) (*menuview.View, error) {
	return s.repo.EnsureDefaultView(ctx, tenantID, locationID, kind, DefaultNames[kind], menuview.Rules{Round: menuview.RoundNone, HiddenCategories: []uuid.UUID{}})
}

// View returns one of a location's views.
func (s *Service) View(ctx context.Context, locationID, id uuid.UUID) (*menuview.View, error) {
	v, err := s.repo.GetView(ctx, id)
	if err != nil || v.LocationID != locationID {
		return nil, ErrNotFound
	}
	return v, nil
}

// ViewFor is the view a channel reads: its own when set (and still the
// store's), else the store's default view of its kind.
func (s *Service) ViewFor(ctx context.Context, tenantID, locationID uuid.UUID, kind menuview.Kind, viewID *uuid.UUID) (*menuview.View, error) {
	if viewID != nil {
		if v, err := s.View(ctx, locationID, *viewID); err == nil {
			return v, nil
		}
	}
	return s.DefaultView(ctx, tenantID, locationID, kind)
}

// ViewKind is the kind of one of a location's views (channelApp.ViewReader).
func (s *Service) ViewKind(ctx context.Context, locationID, id uuid.UUID) (string, error) {
	v, err := s.View(ctx, locationID, id)
	if err != nil {
		return "", err
	}
	return string(v.Kind), nil
}

func (s *Service) CreateView(ctx context.Context, tenantID, locationID uuid.UUID, kind menuview.Kind, name string, rules menuview.Rules) (*menuview.View, error) {
	if !kind.Valid() {
		return nil, ErrBadKind
	}
	if name = strings.TrimSpace(name); name == "" {
		return nil, ErrBadName
	}
	if err := validRules(&rules); err != nil {
		return nil, err
	}
	return s.repo.CreateView(ctx, tenantID, locationID, kind, name, rules)
}

func (s *Service) UpdateView(ctx context.Context, locationID, id uuid.UUID, name *string, rules *menuview.Rules) (*menuview.View, error) {
	v, err := s.View(ctx, locationID, id)
	if err != nil {
		return nil, err
	}
	n, r := v.Name, v.Rules
	if name != nil {
		if n = strings.TrimSpace(*name); n == "" {
			return nil, ErrBadName
		}
	}
	if rules != nil {
		if err := validRules(rules); err != nil {
			return nil, err
		}
		r = *rules
	}
	return s.repo.UpdateView(ctx, id, n, r)
}

func (s *Service) DeleteView(ctx context.Context, locationID, id uuid.UUID) error {
	if _, err := s.View(ctx, locationID, id); err != nil {
		return err
	}
	_, err := s.repo.DeleteView(ctx, id)
	return err
}

func validRules(r *menuview.Rules) error {
	if r.Round == "" {
		r.Round = menuview.RoundNone
	}
	if r.MarkupBps < -5000 || r.MarkupBps > 10000 || (r.Round != menuview.RoundNone && r.Round != menuview.RoundCeil99) {
		return ErrBadRules
	}
	if r.HiddenCategories == nil {
		r.HiddenCategories = []uuid.UUID{}
	}
	return nil
}

// --- entries -------------------------------------------------------------------

func (s *Service) Entries(ctx context.Context, locationID, viewID uuid.UUID) ([]menuview.Entry, error) {
	if _, err := s.View(ctx, locationID, viewID); err != nil {
		return nil, err
	}
	return s.repo.ListEntries(ctx, viewID)
}

// CreateEntry adds an entry to a view; its anchors must be the location's
// bank items (an item entry exactly one, a merge two or more, a map one).
func (s *Service) CreateEntry(ctx context.Context, locationID, viewID uuid.UUID, e menuview.Entry) (*menuview.Entry, error) {
	v, err := s.View(ctx, locationID, viewID)
	if err != nil {
		return nil, err
	}
	if err := s.validEntry(ctx, v, &e); err != nil {
		return nil, err
	}
	return s.repo.CreateEntry(ctx, v, e)
}

func (s *Service) UpdateEntry(ctx context.Context, locationID, entryID uuid.UUID, e menuview.Entry) (*menuview.Entry, error) {
	cur, err := s.entry(ctx, locationID, entryID)
	if err != nil {
		return nil, err
	}
	v, err := s.View(ctx, locationID, cur.ViewID)
	if err != nil {
		return nil, err
	}
	e.ID, e.ViewID, e.Kind = cur.ID, cur.ViewID, cur.Kind
	if err := s.validEntry(ctx, v, &e); err != nil {
		return nil, err
	}
	return s.repo.UpdateEntry(ctx, e)
}

func (s *Service) DeleteEntry(ctx context.Context, locationID, entryID uuid.UUID) error {
	if _, err := s.entry(ctx, locationID, entryID); err != nil {
		return err
	}
	_, err := s.repo.DeleteEntry(ctx, entryID)
	return err
}

func (s *Service) entry(ctx context.Context, locationID, id uuid.UUID) (*menuview.Entry, error) {
	e, err := s.repo.GetEntry(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if _, err := s.View(ctx, locationID, e.ViewID); err != nil {
		return nil, ErrNotFound
	}
	return e, nil
}

func (s *Service) validEntry(ctx context.Context, v *menuview.View, e *menuview.Entry) error {
	if err := entryShape(v.Kind, e); err != nil {
		return err
	}
	bank, err := s.repo.LoadBank(ctx, v.LocationID)
	if err != nil {
		return err
	}
	return entryRefs(bank, e)
}

// ValidEntry checks an entry of a view of kind k against a bank and trims its
// names (the bank import checks every entry against the bank it is writing).
func ValidEntry(bank *menuDom.Bank, k menuview.Kind, e *menuview.Entry) error {
	if err := entryShape(k, e); err != nil {
		return err
	}
	return entryRefs(bank, e)
}

// ValidRules checks (and defaults) a view's rules.
func ValidRules(r *menuview.Rules) error { return validRules(r) }

// entryShape checks an entry's kind, anchors and prices (no bank needed).
func entryShape(k menuview.Kind, e *menuview.Entry) error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrBadEntry, msg) }
	switch e.Kind {
	case menuview.EntryItem, menuview.EntryMerge, menuview.EntryAugment:
		if k == menuview.KindVendor {
			return bad("a vendor view only maps names (kind map)")
		}
	case menuview.EntryMap:
		if k != menuview.KindVendor {
			return bad("only a vendor view maps names")
		}
	default:
		return bad("kind must be item, merge, augment or map")
	}
	n := len(e.Anchor.ItemIDs)
	switch {
	case e.Kind == menuview.EntryItem && n != 1, e.Kind == menuview.EntryMap && n != 1:
		return bad("anchor one bank item")
	case e.Kind == menuview.EntryMerge && n < 2:
		return bad("a merge anchors two or more bank items")
	case e.Kind == menuview.EntryAugment && (n == 0 || e.PriceOverrideCents == nil || strings.TrimSpace(e.DisplayName) == ""):
		return bad("an added item needs a name, a price and bank items")
	case e.Kind == menuview.EntryMap && strings.TrimSpace(e.Extra.VendorName) == "":
		return bad("a mapping needs the vendor's item name")
	}
	if e.PriceOverrideCents != nil && *e.PriceOverrideCents < 0 {
		return ErrBadPrice
	}
	for _, p := range e.Extra.SizePrices {
		if p < 0 {
			return ErrBadPrice
		}
	}
	return nil
}

// entryRefs checks that an entry's items and sizes are the bank's, and trims
// its names.
func entryRefs(bank *menuDom.Bank, e *menuview.Entry) error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrBadEntry, msg) }
	for _, id := range e.Anchor.ItemIDs {
		if _, _, ok := bank.Item(id); !ok {
			return bad("anchor " + id.String() + " is not an item of this store")
		}
	}
	for k := range e.Extra.SizePrices {
		id, err := uuid.Parse(k)
		if err != nil {
			return ErrUnknownSize
		}
		if _, ok := bank.SizeByID(id); !ok {
			return ErrUnknownSize
		}
	}
	for k := range e.Extra.Labels {
		if id, err := uuid.Parse(k); err != nil || !slices.Contains(e.Anchor.ItemIDs, id) {
			return bad("a label names an item the entry doesn't anchor")
		}
	}
	if e.Anchor.SizeID != nil {
		if _, ok := bank.SizeByID(*e.Anchor.SizeID); !ok {
			return ErrUnknownSize
		}
	}
	e.DisplayName, e.Category = strings.TrimSpace(e.DisplayName), strings.TrimSpace(e.Category)
	e.Extra.VendorName, e.Extra.VendorSize = strings.TrimSpace(e.Extra.VendorName), strings.TrimSpace(e.Extra.VendorSize)
	return nil
}

// --- builds --------------------------------------------------------------------

// unmatchedWindow is how far back a vendor view looks for unmatched names.
const unmatchedWindow = 30 * 24 * time.Hour

// built is one view's build at one menu version: the whole build (the
// manager's view, with maps + needs attention) and the channel's copy.
type built struct {
	version       int64
	full, channel []byte
}

// ChannelMenu is a build as a channel sees it: no needs-attention list, no maps.
type ChannelMenu struct {
	View           menuview.BuildView       `json:"view"`
	Version        int64                    `json:"version"`
	Currency       string                   `json:"currency"`
	Sizes          []menuview.BuildSize     `json:"sizes"`
	Categories     []menuview.BuildCategory `json:"categories"`
	ModifierGroups []menuview.BuildGroup    `json:"modifier_groups"`
}

// Build returns a view's whole build at the current menu version.
func (s *Service) Build(ctx context.Context, v *menuview.View) (json.RawMessage, int64, error) {
	b, err := s.build(ctx, v)
	return b.full, b.version, err
}

// ChannelBuild returns a view's build as a channel sees it.
func (s *Service) ChannelBuild(ctx context.Context, v *menuview.View) ([]byte, int64, error) {
	b, err := s.build(ctx, v)
	return b.channel, b.version, err
}

// build serves a view at the current menu version: from memory, else the
// build cached in the database (when it has the current format), else
// compiled now and cached in both. Vendor views are always compiled (they
// read the recent unmatched names, which no menu version tracks).
func (s *Service) build(ctx context.Context, v *menuview.View) (built, error) {
	ver, err := s.repo.Version(ctx, v.LocationID)
	if err != nil {
		return built{}, err
	}
	cacheable := v.Kind != menuview.KindVendor
	if cacheable {
		s.mu.Lock()
		m, ok := s.mem[v.ID]
		s.mu.Unlock()
		if ok && m.version == ver.Version {
			return m, nil
		}
		raw, at, ok, err := s.repo.CachedBuild(ctx, v.ID)
		if err != nil {
			return built{}, err
		}
		if ok && at == ver.Version && formatOf(raw) == menuview.BuildFormat {
			return s.remember(v.ID, at, raw)
		}
	}
	b, err := s.Compile(ctx, v)
	if err != nil {
		return built{}, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return built{}, err
	}
	if !cacheable {
		return channelOf(b.Version, raw)
	}
	if err := s.repo.SaveBuild(ctx, v.ID, b.Version, raw); err != nil {
		s.logger.Warn().Err(err).Str("view_id", v.ID.String()).Msg("cache view build")
	}
	return s.remember(v.ID, b.Version, raw)
}

func (s *Service) remember(viewID uuid.UUID, version int64, raw []byte) (built, error) {
	b, err := channelOf(version, raw)
	if err != nil {
		return built{}, err
	}
	s.mu.Lock()
	s.mem[viewID] = b
	s.mu.Unlock()
	return b, nil
}

func channelOf(version int64, raw []byte) (built, error) {
	var m ChannelMenu
	if err := json.Unmarshal(raw, &m); err != nil {
		return built{}, err
	}
	ch, err := json.Marshal(m)
	if err != nil {
		return built{}, err
	}
	return built{version: version, full: raw, channel: ch}, nil
}

func formatOf(raw []byte) int {
	var f struct {
		Format int `json:"build_format"`
	}
	_ = json.Unmarshal(raw, &f)
	return f.Format
}

// Compile compiles a view now (vendor views also read the recent unmatched
// names, which no menu version tracks — so they are never served cached).
func (s *Service) Compile(ctx context.Context, v *menuview.View) (menuview.Build, error) {
	bank, err := s.repo.LoadBank(ctx, v.LocationID)
	if err != nil {
		return menuview.Build{}, err
	}
	entries, err := s.repo.ListEntries(ctx, v.ID)
	if err != nil {
		return menuview.Build{}, err
	}
	var unmatched []menuview.UnmatchedName
	if v.Kind == menuview.KindVendor && s.unmatched != nil {
		names, err := s.unmatched.UnmatchedNames(ctx, v.TenantID, v.LocationID, time.Now().Add(-unmatchedWindow))
		if err != nil {
			return menuview.Build{}, err
		}
		for _, n := range names {
			if n.Routing == "unmatched" {
				unmatched = append(unmatched, menuview.UnmatchedName{Name: n.Name, Orders: n.Orders})
			}
		}
	}
	return menuview.Compile(bank, *v, entries, unmatched), nil
}

// --- vendor routing --------------------------------------------------------------

// VendorMaps is a location's vendor name → bank item mappings (its vendor
// views' map entries, keyed by menuview.NormName). The online caster's item
// routing checks these before its name match (menuApp.VendorMapper).
func (s *Service) VendorMaps(ctx context.Context, locationID uuid.UUID) (map[string]uuid.UUID, error) {
	v, err := s.repo.FirstViewOfKind(ctx, locationID, menuview.KindVendor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := s.repo.ListEntries(ctx, v.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]uuid.UUID{}
	for _, e := range entries {
		if e.Kind == menuview.EntryMap && len(e.Anchor.ItemIDs) == 1 && e.Extra.VendorName != "" {
			out[menuview.NormName(e.Extra.VendorName)] = e.Anchor.ItemIDs[0]
		}
	}
	return out, nil
}
