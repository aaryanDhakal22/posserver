// Package familiesApp is the Families editor (the owner's Centurion pass,
// 2026-11-12, migration 00045): a store's item / mod families, their members
// and the coupons that name them.
package familiesApp

import (
	"context"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrNotFound  = errors.New("no such family in this store")
	ErrNameTaken = menu.ErrFamilyNameTaken
	ErrWrongKind = errors.New("an item family is for items, a mod family for options")
	ErrNotStore  = errors.New("an item or option isn't this store's")
	ErrTooMany   = errors.New("up to 2,000 items and options at once")
)

// InCoupon: a family coupons still name (merge it instead of deleting it).
type InCoupon struct{ Coupons []string }

func (e *InCoupon) Error() string { return fmt.Sprintf("coupons name this family: %v", e.Coupons) }

// Repo is the families store (repositories.FamilyRepository).
type Repo interface {
	List(ctx context.Context, locationID uuid.UUID) ([]menu.Family, []menu.FamilyMember, []menu.FamilyMember, error)
	Get(ctx context.Context, locationID, id uuid.UUID) (menu.Family, error)
	Create(ctx context.Context, tenantID, locationID uuid.UUID, f menu.Family) (menu.Family, error)
	Update(ctx context.Context, locationID uuid.UUID, f menu.Family) (menu.Family, error)
	Delete(ctx context.Context, locationID, id uuid.UUID) (bool, error)
	CouponsNaming(ctx context.Context, locationID, id uuid.UUID) ([]string, error)
	Merge(ctx context.Context, locationID, from, into uuid.UUID) (int, int, int, error)
	Assign(ctx context.Context, locationID uuid.UUID, family *uuid.UUID, items, options []uuid.UUID) (int, int, error)
}

type Service struct{ repo Repo }

func NewService(repo Repo) *Service { return &Service{repo: repo} }

// FamilyRow is a family with its member counts.
type FamilyRow struct {
	menu.Family
	Items   int `json:"items"`
	Options int `json:"options"`
}

// ItemRow / OptionRow are the editor's members.
type ItemRow struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	Category string     `json:"category"`
	FamilyID *uuid.UUID `json:"family_id"`
}

type OptionRow struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Group      string     `json:"group"`
	GroupID    uuid.UUID  `json:"group_id"`
	PriceCents int64      `json:"price_cents"`
	FamilyID   *uuid.UUID `json:"family_id"`
}

// Listing is GET /store/v1/me/menu/families.
type Listing struct {
	Families []FamilyRow `json:"families"`
	Items    []ItemRow   `json:"items"`
	Options  []OptionRow `json:"options"`
}

func (s *Service) List(ctx context.Context, locationID uuid.UUID) (*Listing, error) {
	fams, items, opts, err := s.repo.List(ctx, locationID)
	if err != nil {
		return nil, err
	}
	out := &Listing{Families: []FamilyRow{}, Items: []ItemRow{}, Options: []OptionRow{}}
	nItems, nOpts := map[uuid.UUID]int{}, map[uuid.UUID]int{}
	for _, it := range items {
		out.Items = append(out.Items, ItemRow{ID: it.ID, Name: it.Name, Category: it.Parent, FamilyID: it.FamilyID})
		if it.FamilyID != nil {
			nItems[*it.FamilyID]++
		}
	}
	for _, o := range opts {
		out.Options = append(out.Options, OptionRow{ID: o.ID, Name: o.Name, Group: o.Parent, GroupID: o.ParentID, PriceCents: o.PriceCents, FamilyID: o.FamilyID})
		if o.FamilyID != nil {
			nOpts[*o.FamilyID]++
		}
	}
	for _, f := range fams {
		out.Families = append(out.Families, FamilyRow{Family: f, Items: nItems[f.ID], Options: nOpts[f.ID]})
	}
	return out, nil
}

func (s *Service) get(ctx context.Context, locationID, id uuid.UUID) (menu.Family, error) {
	f, err := s.repo.Get(ctx, locationID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

func (s *Service) Create(ctx context.Context, tenantID, locationID uuid.UUID, f menu.Family) (menu.Family, error) {
	if err := f.Normalize(); err != nil {
		return f, err
	}
	return s.repo.Create(ctx, tenantID, locationID, f)
}

// Update renames a family / changes a mod family's class (nil = keep).
func (s *Service) Update(ctx context.Context, locationID, id uuid.UUID, name, class *string) (menu.Family, error) {
	f, err := s.get(ctx, locationID, id)
	if err != nil {
		return f, err
	}
	if name != nil {
		f.Name = *name
	}
	if class != nil {
		f.Class = *class
	}
	if err := f.Normalize(); err != nil {
		return f, err
	}
	out, err := s.repo.Update(ctx, locationID, f)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

func (s *Service) Delete(ctx context.Context, locationID, id uuid.UUID) error {
	if _, err := s.get(ctx, locationID, id); err != nil {
		return err
	}
	names, err := s.repo.CouponsNaming(ctx, locationID, id)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return &InCoupon{Coupons: names}
	}
	ok, err := s.repo.Delete(ctx, locationID, id)
	if err == nil && !ok {
		return ErrNotFound
	}
	return err
}

// MergeResult is what a merge moved.
type MergeResult struct {
	Family         menu.Family `json:"family"`
	MovedItems     int         `json:"moved_items"`
	MovedOptions   int         `json:"moved_options"`
	CouponsChanged int         `json:"coupons_changed"`
}

func (s *Service) Merge(ctx context.Context, locationID, from, into uuid.UUID) (*MergeResult, error) {
	if from == into {
		return nil, errors.Join(menu.ErrBadFamily, errors.New("a family can't merge into itself"))
	}
	a, err := s.get(ctx, locationID, from)
	if err != nil {
		return nil, err
	}
	b, err := s.get(ctx, locationID, into)
	if err != nil {
		return nil, err
	}
	if a.Kind != b.Kind {
		return nil, ErrWrongKind
	}
	ni, no, nc, err := s.repo.Merge(ctx, locationID, from, into)
	if err != nil {
		return nil, err
	}
	return &MergeResult{Family: b, MovedItems: ni, MovedOptions: no, CouponsChanged: nc}, nil
}

// Assign sets the family of items and options (nil = none): an item family
// for items, a mod family for options, every id the store's.
func (s *Service) Assign(ctx context.Context, locationID uuid.UUID, family *uuid.UUID, items, options []uuid.UUID) (int, int, error) {
	if len(items)+len(options) > 2000 {
		return 0, 0, ErrTooMany
	}
	if family != nil {
		f, err := s.get(ctx, locationID, *family)
		if err != nil {
			return 0, 0, err
		}
		if (len(items) > 0 && f.Kind != menu.FamilyItem) || (len(options) > 0 && f.Kind != menu.FamilyMod) {
			return 0, 0, ErrWrongKind
		}
	}
	_, its, opts, err := s.repo.List(ctx, locationID)
	if err != nil {
		return 0, 0, err
	}
	have := map[uuid.UUID]bool{}
	for _, m := range its {
		have[m.ID] = true
	}
	for _, id := range items {
		if !have[id] {
			return 0, 0, ErrNotStore
		}
	}
	have = map[uuid.UUID]bool{}
	for _, m := range opts {
		have[m.ID] = true
	}
	for _, id := range options {
		if !have[id] {
			return 0, 0, ErrNotStore
		}
	}
	return s.repo.Assign(ctx, locationID, family, items, options)
}
