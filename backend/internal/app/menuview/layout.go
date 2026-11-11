package menuviewApp

import (
	"context"
	"errors"

	menuDom "quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

var (
	// ErrNotCounter: a layout belongs on a counter view only.
	ErrNotCounter = errors.New("only a counter view has a layout")
	// ErrNoLayout: the view has no layout.
	ErrNoLayout = errors.New("this view has no layout")
)

// LayoutError is a layout the store refuses, with every problem found.
type LayoutError struct {
	Problems []menuview.Problem
}

func (e *LayoutError) Error() string { return "the layout has problems" }

// Layout is a counter view's layout.
func (s *Service) Layout(ctx context.Context, locationID, viewID uuid.UUID) (*menuview.Layout, error) {
	v, err := s.View(ctx, locationID, viewID)
	if err != nil {
		return nil, err
	}
	if v.Kind != menuview.KindCounter {
		return nil, ErrNotCounter
	}
	l, err := s.repo.Layout(ctx, viewID)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, ErrNoLayout
	}
	return l, nil
}

// CheckLayout normalizes a layout and checks it against the location's bank
// and deals (nil = fine).
func (s *Service) CheckLayout(ctx context.Context, locationID uuid.UUID, l *menuview.Layout) ([]menuview.Problem, error) {
	menuview.NormalizeLayout(l)
	bank, err := s.repo.LoadBank(ctx, locationID)
	if err != nil {
		return nil, err
	}
	deals, err := s.repo.DealNames(ctx, locationID)
	if err != nil {
		return nil, err
	}
	return menuview.ValidateLayout(l, menuview.LayoutRefs{Bank: bank, Deals: deals}), nil
}

// SetLayout replaces a counter view's layout after checking it (a
// *LayoutError lists every problem; nothing is saved then). The save bumps
// the menu version.
func (s *Service) SetLayout(ctx context.Context, locationID, viewID uuid.UUID, l *menuview.Layout) (*menuview.Layout, error) {
	v, err := s.View(ctx, locationID, viewID)
	if err != nil {
		return nil, err
	}
	if v.Kind != menuview.KindCounter {
		return nil, ErrNotCounter
	}
	problems, err := s.CheckLayout(ctx, locationID, l)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, &LayoutError{Problems: problems}
	}
	if err := s.repo.SetLayout(ctx, viewID, l); err != nil {
		return nil, err
	}
	return l, nil
}

// ClearLayout removes a counter view's layout (no layout is not an error).
func (s *Service) ClearLayout(ctx context.Context, locationID, viewID uuid.UUID) error {
	v, err := s.View(ctx, locationID, viewID)
	if err != nil {
		return err
	}
	if v.Kind != menuview.KindCounter {
		return ErrNotCounter
	}
	if !v.HasLayout {
		return nil
	}
	return s.repo.SetLayout(ctx, viewID, nil)
}

// resolveLayout adds a counter view's layout to its build.
func (s *Service) resolveLayout(ctx context.Context, v *menuview.View, bank *menuDom.Bank, b *menuview.Build) error {
	if v.Kind != menuview.KindCounter {
		return nil
	}
	l, err := s.repo.Layout(ctx, v.ID)
	if err != nil || l == nil {
		return err
	}
	deals, err := s.repo.DealNames(ctx, v.LocationID)
	if err != nil {
		return err
	}
	menuview.ResolveLayout(b, bank, *v, deals, l)
	return nil
}
