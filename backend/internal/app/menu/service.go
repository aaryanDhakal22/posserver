// Package menuApp is the use-case layer over the menu domain: validation +
// ownership enforcement (a manager session is scoped to one location, so every
// mutation is checked against that location, not just the tenant).
package menuApp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tenancyApp "quiccpos/main/internal/app/tenancy"
	menuDom "quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/order"
	tenancyDom "quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/menu"

type Service struct {
	repo    menuDom.Repository
	tenancy *tenancyApp.Service
	logger  zerolog.Logger
	meters  observability.Meters
	tracer  trace.Tracer
	// listPrinters resolves a location's printers (tenancy's; swapped in tests).
	listPrinters func(ctx context.Context, locationID uuid.UUID) ([]tenancyDom.Printer, error)
}

func NewService(repo menuDom.Repository, tenancy *tenancyApp.Service, meters observability.Meters, lg zerolog.Logger) *Service {
	s := &Service{
		repo:    repo,
		tenancy: tenancy,
		logger:  lg.With().Str("module", "menu-service").Logger(),
		meters:  meters,
		tracer:  otel.Tracer(tracerName),
	}
	if tenancy != nil {
		s.listPrinters = tenancy.ListPrintersByLocation
	}
	return s
}

// mutateSpan opens a menu.mutate span for a create/update/delete operation.
func (s *Service) mutateSpan(ctx context.Context, entity, op string) (context.Context, trace.Span) {
	return s.tracer.Start(ctx, "menu.mutate", trace.WithAttributes(
		attribute.String("menu.entity", entity),
		attribute.String("menu.op", op),
	))
}

// recordMutation finalises a menu.mutate: bumps menu.mutations on success,
// marks the span on error.
func (s *Service) recordMutation(ctx context.Context, span trace.Span, entity, op string, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "mutation failed")
		return
	}
	s.meters.MenuMutations.Add(ctx, 1, metric.WithAttributes(
		attribute.String("entity", entity),
		attribute.String("op", op),
	))
}

var errNotFound = errors.New("not found in this location")

func assertLoc(entityLoc, scopeLoc uuid.UUID) error {
	if entityLoc != scopeLoc {
		return errNotFound
	}
	return nil
}

// --- Menus -----------------------------------------------------------------

func (s *Service) ListMenus(ctx context.Context, locationID uuid.UUID) ([]menuDom.Menu, error) {
	// The menu editor's top-level load is the manager's canonical menu read.
	s.meters.MenuReads.Add(ctx, 1, metric.WithAttributes(attribute.String("surface", "manager")))
	return s.repo.ListMenusByLocation(ctx, locationID)
}

func (s *Service) CreateMenu(ctx context.Context, tenantID, locationID uuid.UUID, name, description string) (*menuDom.Menu, error) {
	ctx, span := s.mutateSpan(ctx, "menu", "create")
	defer span.End()
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	m, err := s.repo.CreateMenu(ctx, menuDom.CreateMenuParams{TenantID: tenantID, LocationID: locationID, Name: name, Description: description, Available: true})
	s.recordMutation(ctx, span, "menu", "create", err)
	return m, err
}

func (s *Service) UpdateMenu(ctx context.Context, tenantID, locationID, id uuid.UUID, name, description string, available bool) (*menuDom.Menu, error) {
	ctx, span := s.mutateSpan(ctx, "menu", "update")
	defer span.End()
	cur, err := s.repo.GetMenu(ctx, id, tenantID)
	if err != nil {
		return nil, errNotFound
	}
	if err := assertLoc(cur.LocationID, locationID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	m, err := s.repo.UpdateMenu(ctx, menuDom.UpdateMenuParams{ID: id, TenantID: tenantID, Name: name, Description: description, Available: available})
	s.recordMutation(ctx, span, "menu", "update", err)
	return m, err
}

func (s *Service) DeleteMenu(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	ctx, span := s.mutateSpan(ctx, "menu", "delete")
	defer span.End()
	if err := s.ownMenu(ctx, tenantID, locationID, id); err != nil {
		return err
	}
	err := s.repo.DeleteMenu(ctx, id, tenantID)
	s.recordMutation(ctx, span, "menu", "delete", err)
	return err
}

func (s *Service) ReorderMenus(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return s.repo.ReorderMenus(ctx, tenantID, ids)
}

// --- Categories ------------------------------------------------------------

func (s *Service) ListCategories(ctx context.Context, tenantID, locationID, menuID uuid.UUID) ([]menuDom.Category, error) {
	if err := s.ownMenu(ctx, tenantID, locationID, menuID); err != nil {
		return nil, err
	}
	return s.repo.ListCategoriesByMenu(ctx, menuID)
}

func (s *Service) CreateCategory(ctx context.Context, tenantID, locationID, menuID uuid.UUID, name, description string, defaultPrinter *uuid.UUID) (*menuDom.Category, error) {
	ctx, span := s.mutateSpan(ctx, "category", "create")
	defer span.End()
	if err := s.ownMenu(ctx, tenantID, locationID, menuID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := s.validatePrinter(ctx, locationID, defaultPrinter); err != nil {
		return nil, err
	}
	cat, err := s.repo.CreateCategory(ctx, menuDom.CreateCategoryParams{
		TenantID: tenantID, LocationID: locationID, MenuID: menuID, Name: name, Description: description,
		Available: true, DefaultPrinterID: defaultPrinter,
	})
	s.recordMutation(ctx, span, "category", "create", err)
	return cat, err
}

func (s *Service) UpdateCategory(ctx context.Context, tenantID, locationID, id uuid.UUID, name, description string, available bool, defaultPrinter *uuid.UUID) (*menuDom.Category, error) {
	ctx, span := s.mutateSpan(ctx, "category", "update")
	defer span.End()
	if err := s.ownCategory(ctx, tenantID, locationID, id); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := s.validatePrinter(ctx, locationID, defaultPrinter); err != nil {
		return nil, err
	}
	cat, err := s.repo.UpdateCategory(ctx, menuDom.UpdateCategoryParams{ID: id, TenantID: tenantID, Name: name, Description: description, Available: available, DefaultPrinterID: defaultPrinter})
	s.recordMutation(ctx, span, "category", "update", err)
	return cat, err
}

func (s *Service) DeleteCategory(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	ctx, span := s.mutateSpan(ctx, "category", "delete")
	defer span.End()
	if err := s.ownCategory(ctx, tenantID, locationID, id); err != nil {
		return err
	}
	err := s.repo.DeleteCategory(ctx, id, tenantID)
	s.recordMutation(ctx, span, "category", "delete", err)
	return err
}

func (s *Service) ReorderCategories(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return s.repo.ReorderCategories(ctx, tenantID, ids)
}

// --- Items -----------------------------------------------------------------

func (s *Service) ListItems(ctx context.Context, tenantID, locationID, categoryID uuid.UUID) ([]menuDom.Item, error) {
	if err := s.ownCategory(ctx, tenantID, locationID, categoryID); err != nil {
		return nil, err
	}
	return s.repo.ListItemsByCategory(ctx, categoryID)
}

func (s *Service) CreateItem(ctx context.Context, tenantID, locationID, categoryID uuid.UUID, name, description string, priceCents int64, taxRateBps int, tags []string) (*menuDom.Item, error) {
	ctx, span := s.mutateSpan(ctx, "item", "create")
	defer span.End()
	if err := s.ownCategory(ctx, tenantID, locationID, categoryID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if priceCents < 0 {
		return nil, errors.New("price cannot be negative")
	}
	if taxRateBps < 0 {
		return nil, errors.New("tax rate cannot be negative")
	}
	it, err := s.repo.CreateItem(ctx, menuDom.CreateItemParams{
		TenantID: tenantID, LocationID: locationID, CategoryID: categoryID, Name: name, Description: description,
		PriceCents: priceCents, TaxRateBps: taxRateBps, InStock: true, Tags: tags,
	})
	s.recordMutation(ctx, span, "item", "create", err)
	return it, err
}

// UpdateItemInput carries the editable item fields.
type UpdateItemInput struct {
	Name, Description string
	PriceCents        int64
	TaxRateBps        int
	InStock           bool
	OutUntil          *time.Time
	Tags              []string
	Attributes        map[string]any
}

func (s *Service) UpdateItem(ctx context.Context, tenantID, locationID, id uuid.UUID, in UpdateItemInput) (*menuDom.Item, error) {
	ctx, span := s.mutateSpan(ctx, "item", "update")
	defer span.End()
	if err := s.ownItem(ctx, tenantID, locationID, id); err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, errors.New("name is required")
	}
	if in.PriceCents < 0 {
		return nil, errors.New("price cannot be negative")
	}
	if in.InStock {
		in.OutUntil = nil // out_until only meaningful when out of stock
	}
	it, err := s.repo.UpdateItem(ctx, menuDom.UpdateItemParams{
		ID: id, TenantID: tenantID, Name: in.Name, Description: in.Description, PriceCents: in.PriceCents,
		TaxRateBps: in.TaxRateBps, InStock: in.InStock, OutUntil: in.OutUntil, Tags: in.Tags, Attributes: in.Attributes,
	})
	s.recordMutation(ctx, span, "item", "update", err)
	return it, err
}

func (s *Service) AssignItemPrinter(ctx context.Context, tenantID, locationID, id uuid.UUID, printerID *uuid.UUID) error {
	if err := s.ownItem(ctx, tenantID, locationID, id); err != nil {
		return err
	}
	if err := s.validatePrinter(ctx, locationID, printerID); err != nil {
		return err
	}
	return s.repo.SetItemPrinter(ctx, id, tenantID, printerID)
}

func (s *Service) DeleteItem(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	ctx, span := s.mutateSpan(ctx, "item", "delete")
	defer span.End()
	if err := s.ownItem(ctx, tenantID, locationID, id); err != nil {
		return err
	}
	err := s.repo.DeleteItem(ctx, id, tenantID)
	s.recordMutation(ctx, span, "item", "delete", err)
	return err
}

func (s *Service) ReorderItems(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return s.repo.ReorderItems(ctx, tenantID, ids)
}

// --- Modifier groups + options ---------------------------------------------

func (s *Service) ListGroups(ctx context.Context, locationID uuid.UUID) ([]menuDom.ModifierGroup, error) {
	return s.repo.ListGroupsByLocation(ctx, locationID)
}

// GroupRules bundles the modifier-group selection rules.
type GroupRules struct {
	Required             bool
	MinSelect, MaxSelect int
	AllowMultiple        bool
}

func (r GroupRules) validate() error {
	if r.MinSelect < 0 || r.MaxSelect < 0 {
		return errors.New("min/max select cannot be negative")
	}
	if r.MaxSelect != 0 && r.MinSelect > r.MaxSelect {
		return errors.New("min select cannot exceed max select")
	}
	if r.Required && r.MinSelect < 1 {
		return errors.New("a required group must allow at least one selection (min ≥ 1)")
	}
	return nil
}

func (s *Service) CreateGroup(ctx context.Context, tenantID, locationID uuid.UUID, name string, rules GroupRules) (*menuDom.ModifierGroup, error) {
	ctx, span := s.mutateSpan(ctx, "modifier_group", "create")
	defer span.End()
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := rules.validate(); err != nil {
		return nil, err
	}
	g, err := s.repo.CreateGroup(ctx, menuDom.CreateGroupParams{TenantID: tenantID, LocationID: locationID, Name: name, Required: rules.Required, MinSelect: rules.MinSelect, MaxSelect: rules.MaxSelect, AllowMultiple: rules.AllowMultiple})
	s.recordMutation(ctx, span, "modifier_group", "create", err)
	return g, err
}

func (s *Service) UpdateGroup(ctx context.Context, tenantID, locationID, id uuid.UUID, name string, rules GroupRules) (*menuDom.ModifierGroup, error) {
	ctx, span := s.mutateSpan(ctx, "modifier_group", "update")
	defer span.End()
	if err := s.ownGroup(ctx, tenantID, locationID, id); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := rules.validate(); err != nil {
		return nil, err
	}
	g, err := s.repo.UpdateGroup(ctx, menuDom.UpdateGroupParams{ID: id, TenantID: tenantID, Name: name, Required: rules.Required, MinSelect: rules.MinSelect, MaxSelect: rules.MaxSelect, AllowMultiple: rules.AllowMultiple})
	s.recordMutation(ctx, span, "modifier_group", "update", err)
	return g, err
}

func (s *Service) DeleteGroup(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	ctx, span := s.mutateSpan(ctx, "modifier_group", "delete")
	defer span.End()
	if err := s.ownGroup(ctx, tenantID, locationID, id); err != nil {
		return err
	}
	err := s.repo.DeleteGroup(ctx, id, tenantID) // detach-then-delete
	s.recordMutation(ctx, span, "modifier_group", "delete", err)
	return err
}

func (s *Service) ListOptions(ctx context.Context, tenantID, locationID, groupID uuid.UUID) ([]menuDom.ModifierOption, error) {
	if err := s.ownGroup(ctx, tenantID, locationID, groupID); err != nil {
		return nil, err
	}
	return s.repo.ListOptionsByGroup(ctx, groupID)
}

func (s *Service) CreateOption(ctx context.Context, tenantID, locationID, groupID uuid.UUID, name string, priceDelta int64) (*menuDom.ModifierOption, error) {
	if err := s.ownGroup(ctx, tenantID, locationID, groupID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	return s.repo.CreateOption(ctx, menuDom.CreateOptionParams{TenantID: tenantID, LocationID: locationID, GroupID: groupID, Name: name, PriceDeltaCents: priceDelta, InStock: true})
}

func (s *Service) UpdateOption(ctx context.Context, tenantID, locationID, id uuid.UUID, name string, priceDelta int64, inStock bool) (*menuDom.ModifierOption, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	return s.repo.UpdateOption(ctx, menuDom.UpdateOptionParams{ID: id, TenantID: tenantID, Name: name, PriceDeltaCents: priceDelta, InStock: inStock})
}

func (s *Service) DeleteOption(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) error {
	return s.repo.DeleteOption(ctx, id, tenantID)
}

func (s *Service) ReorderOptions(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return s.repo.ReorderOptions(ctx, tenantID, ids)
}

// --- Item ↔ group ----------------------------------------------------------

func (s *Service) AttachGroup(ctx context.Context, tenantID, locationID, itemID, groupID uuid.UUID) error {
	if err := s.ownItem(ctx, tenantID, locationID, itemID); err != nil {
		return err
	}
	if err := s.ownGroup(ctx, tenantID, locationID, groupID); err != nil {
		return err
	}
	links, _ := s.repo.ListGroupsForItem(ctx, itemID, tenantID)
	return s.repo.AttachGroup(ctx, tenantID, itemID, groupID, len(links))
}

func (s *Service) DetachGroup(ctx context.Context, tenantID, locationID, itemID, groupID uuid.UUID) error {
	if err := s.ownItem(ctx, tenantID, locationID, itemID); err != nil {
		return err
	}
	return s.repo.DetachGroup(ctx, tenantID, itemID, groupID)
}

func (s *Service) ListGroupsForItem(ctx context.Context, tenantID, locationID, itemID uuid.UUID) ([]menuDom.ItemGroupLink, error) {
	if err := s.ownItem(ctx, tenantID, locationID, itemID); err != nil {
		return nil, err
	}
	return s.repo.ListGroupsForItem(ctx, itemID, tenantID)
}

// GetFullMenu is the read-API assembly for a location. Its only caller today
// is the operator (POS read) surface, so the read is attributed to `operator`.
func (s *Service) GetFullMenu(ctx context.Context, locationID uuid.UUID) (*menuDom.FullMenu, error) {
	ctx, span := s.tracer.Start(ctx, "menu.get_full")
	defer span.End()
	s.meters.MenuReads.Add(ctx, 1, metric.WithAttributes(attribute.String("surface", "operator")))
	full, err := s.repo.GetFullMenu(ctx, locationID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "load full menu")
	}
	return full, err
}

// --- ownership helpers -----------------------------------------------------

func (s *Service) ownMenu(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	m, err := s.repo.GetMenu(ctx, id, tenantID)
	if err != nil {
		return errNotFound
	}
	return assertLoc(m.LocationID, locationID)
}
func (s *Service) ownCategory(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	c, err := s.repo.GetCategory(ctx, id, tenantID)
	if err != nil {
		return errNotFound
	}
	return assertLoc(c.LocationID, locationID)
}
func (s *Service) ownItem(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	it, err := s.repo.GetItem(ctx, id, tenantID)
	if err != nil {
		return errNotFound
	}
	return assertLoc(it.LocationID, locationID)
}
func (s *Service) ownGroup(ctx context.Context, tenantID, locationID, id uuid.UUID) error {
	g, err := s.repo.GetGroup(ctx, id, tenantID)
	if err != nil {
		return errNotFound
	}
	return assertLoc(g.LocationID, locationID)
}

// validatePrinter ensures a printer (if set) belongs to this location.
func (s *Service) validatePrinter(ctx context.Context, locationID uuid.UUID, printerID *uuid.UUID) error {
	if printerID == nil {
		return nil
	}
	printers, err := s.listPrinters(ctx, locationID)
	if err != nil {
		return fmt.Errorf("resolve printers: %w", err)
	}
	for _, p := range printers {
		if p.ID == *printerID {
			return nil
		}
	}
	return errors.New("printer does not belong to this location")
}

// IsNotFound lets handlers map ownership failures to 404.
func IsNotFound(err error) bool { return errors.Is(err, errNotFound) }

// --- Kitchen routing ----------------------------------------------------------

// RouteItems maps vendor item names to kitchen printers for one location, in
// order. A name matches a menu item when they're equal ignoring case and
// repeated whitespace (size is not part of the name). The effective printer is
// the item's own printer, else its category's default. When several menu items
// share a name, the best candidate wins (available menus/categories first, then
// menu/category/item position). Implements the online caster's ItemRouter.
func (s *Service) RouteItems(ctx context.Context, locationID uuid.UUID, names []string) ([]order.ItemRoute, error) {
	byName, err := s.routingTable(ctx, locationID)
	if err != nil {
		return nil, err
	}
	out := make([]order.ItemRoute, len(names))
	for i, n := range names {
		e, ok := byName[normalizeItemName(n)]
		switch {
		case !ok:
			out[i] = order.ItemRoute{Routing: order.RoutingUnmatched}
		case e.PrinterID == nil:
			id := e.ItemID
			out[i] = order.ItemRoute{MenuItemID: &id, Routing: order.RoutingUnassigned}
		default:
			id, pid := e.ItemID, *e.PrinterID
			out[i] = order.ItemRoute{MenuItemID: &id, PrinterID: &pid, Routing: order.RoutingMatched}
		}
		s.meters.OrderItemsRouted.Add(ctx, 1, metric.WithAttributes(attribute.String("routing", string(out[i].Routing))))
	}
	return out, nil
}

// routingTable is the location's menu items keyed by normalized name (first
// = best candidate wins, per the query's ordering).
func (s *Service) routingTable(ctx context.Context, locationID uuid.UUID) (map[string]menuDom.RouteEntry, error) {
	entries, err := s.repo.ListRoutingByLocation(ctx, locationID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]menuDom.RouteEntry, len(entries))
	for _, e := range entries {
		k := normalizeItemName(e.Name)
		if _, seen := byName[k]; !seen { // rows are best-first
			byName[k] = e
		}
	}
	return byName, nil
}

// UnmatchedName is one vendor item name from recent orders that doesn't route
// to a printer: Routing "unmatched" (no menu item of that name) or
// "unassigned" (matched, but neither the item nor its category has a printer).
type UnmatchedName struct {
	Name     string    `json:"name"`
	Routing  string    `json:"routing"`
	Orders   int64     `json:"orders"`
	LastSeen time.Time `json:"last_seen"`
}

// UnmatchedNames lists recent vendor item names that wouldn't route to a
// kitchen printer today — so the manager can fix menu names / printers. Names
// that differ only in case/spacing are merged (the most-ordered spelling is
// shown). Most-ordered first.
func (s *Service) UnmatchedNames(ctx context.Context, tenantID, locationID uuid.UUID, since time.Time) ([]UnmatchedName, error) {
	byName, err := s.routingTable(ctx, locationID)
	if err != nil {
		return nil, err
	}
	recent, err := s.repo.ListRecentItemNames(ctx, tenantID, locationID, since)
	if err != nil {
		return nil, err
	}
	merged := map[string]*UnmatchedName{}
	best := map[string]int64{}
	for _, r := range recent {
		k := normalizeItemName(r.Name)
		routing := order.RoutingUnmatched
		if e, ok := byName[k]; ok {
			if e.PrinterID != nil {
				continue
			}
			routing = order.RoutingUnassigned
		}
		m, ok := merged[k]
		if !ok {
			m = &UnmatchedName{Routing: string(routing)}
			merged[k] = m
		}
		m.Orders += r.Orders
		if r.LastSeen.After(m.LastSeen) {
			m.LastSeen = r.LastSeen
		}
		if r.Orders > best[k] {
			best[k], m.Name = r.Orders, r.Name
		}
	}
	out := make([]UnmatchedName, 0, len(merged))
	for _, m := range merged {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orders != out[j].Orders {
			return out[i].Orders > out[j].Orders
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// normalizeItemName is the menu-matching key: lowercased, whitespace collapsed.
func normalizeItemName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
