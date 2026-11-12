// Package optionflags is a modifier option's two plain switches in Store ›
// Menu (MENU-PLAN, the owner 2026-11-12, migration 00046): "Show on driver
// ticket" (a column) and "Counts for coupons" (the option's mod family
// class: the switch moves the option between the group's regular and
// premium families; no new pricing data).
package optionflags

import (
	"context"
	"errors"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

// ErrNotFound: no such group / option in the store (or the option isn't the group's).
var ErrNotFound = errors.New("no such modifier group or option in this store")

// Repo is repositories.OptionFlagsRepository.
type Repo interface {
	GroupOptions(ctx context.Context, locationID, groupID uuid.UUID) ([]menu.GroupOption, []menu.Family, bool, error)
	SetDriverTicket(ctx context.Context, locationID, optionID uuid.UUID, on bool) (bool, error)
	SetOptionFamily(ctx context.Context, tenantID, locationID, groupID, optionID uuid.UUID,
		decide func(opts []menu.GroupOption, fams []menu.Family) (bool, *uuid.UUID, *menu.Family, error)) (*menu.Family, bool, error)
}

type Service struct{ repo Repo }

func NewService(repo Repo) *Service { return &Service{repo: repo} }

// Flags are an option's switches as the editor shows them.
type Flags struct {
	OptionID           uuid.UUID  `json:"option_id"`
	ShowOnDriverTicket bool       `json:"show_on_driver_ticket"`
	CountsForCoupons   bool       `json:"counts_for_coupons"`
	FamilyID           *uuid.UUID `json:"family_id"`
	FamilyName         string     `json:"family_name,omitempty"`
}

// Group reads the flags of a group's options (by option id).
func (s *Service) Group(ctx context.Context, locationID, groupID uuid.UUID) (map[uuid.UUID]Flags, error) {
	opts, fams, ok, err := s.repo.GroupOptions(ctx, locationID, groupID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	out := make(map[uuid.UUID]Flags, len(opts))
	for _, o := range opts {
		out[o.ID] = flagsOf(o, fams)
	}
	return out, nil
}

func flagsOf(o menu.GroupOption, fams []menu.Family) Flags {
	f := Flags{OptionID: o.ID, ShowOnDriverTicket: o.ShowOnDriverTicket, CountsForCoupons: menu.CountsForCoupons(o.FamilyID, fams),
		FamilyID: o.FamilyID}
	if o.FamilyID != nil {
		for _, x := range fams {
			if x.ID == *o.FamilyID {
				f.FamilyName = x.Name
			}
		}
	}
	return f
}

// Option reads one option's flags (it must be the group's).
func (s *Service) Option(ctx context.Context, locationID, groupID, optionID uuid.UUID) (Flags, error) {
	all, err := s.Group(ctx, locationID, groupID)
	if err != nil {
		return Flags{}, err
	}
	f, ok := all[optionID]
	if !ok {
		return Flags{}, ErrNotFound
	}
	return f, nil
}

// SetDriverTicket sets an option's "Show on driver ticket".
func (s *Service) SetDriverTicket(ctx context.Context, locationID, optionID uuid.UUID, on bool) error {
	ok, err := s.repo.SetDriverTicket(ctx, locationID, optionID, on)
	if err == nil && !ok {
		return ErrNotFound
	}
	return err
}

// SetCounts sets an option's "Counts for coupons" (menu.CouponFamily says
// where it moves) and returns its flags afterwards.
func (s *Service) SetCounts(ctx context.Context, tenantID, locationID, groupID, optionID uuid.UUID, counts bool) (Flags, error) {
	return s.setFamily(ctx, tenantID, locationID, groupID, optionID, func(opts []menu.GroupOption, fams []menu.Family) (bool, *uuid.UUID, *menu.Family) {
		return menu.CouponFamily(opts, fams, optionID, counts)
	})
}

// NewOption applies the create-time default to a just-created option:
// counts nil = on when the group is a topping group (it already has an
// option in a regular family), else the option stays without a family;
// counts true = on; counts false = premium in a topping group, else no
// family.
func (s *Service) NewOption(ctx context.Context, tenantID, locationID, groupID, optionID uuid.UUID, counts *bool) (Flags, error) {
	return s.setFamily(ctx, tenantID, locationID, groupID, optionID, func(opts []menu.GroupOption, fams []menu.Family) (bool, *uuid.UUID, *menu.Family) {
		others := make([]menu.GroupOption, 0, len(opts))
		for _, o := range opts {
			if o.ID != optionID {
				others = append(others, o)
			}
		}
		topping := menu.ToppingGroup(others, fams)
		switch {
		case counts == nil && !topping, counts != nil && !*counts && !topping:
			return true, nil, nil
		case counts != nil && !*counts:
			// A premium topping of a topping group.
			fam, create := menu.CouponTarget(opts, fams, false)
			return false, fam, create
		default:
			return menu.CouponFamily(opts, fams, optionID, true)
		}
	})
}

func (s *Service) setFamily(ctx context.Context, tenantID, locationID, groupID, optionID uuid.UUID,
	decide func([]menu.GroupOption, []menu.Family) (bool, *uuid.UUID, *menu.Family)) (Flags, error) {
	_, ok, err := s.repo.SetOptionFamily(ctx, tenantID, locationID, groupID, optionID,
		func(opts []menu.GroupOption, fams []menu.Family) (bool, *uuid.UUID, *menu.Family, error) {
			for _, o := range opts {
				if o.ID == optionID {
					keep, fam, create := decide(opts, fams)
					return keep, fam, create, nil
				}
			}
			return false, nil, nil, ErrNotFound
		})
	if err != nil {
		return Flags{}, err
	}
	if !ok {
		return Flags{}, ErrNotFound
	}
	return s.Option(ctx, locationID, groupID, optionID)
}
