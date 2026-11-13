package menuviewApp

import (
	"context"

	"quiccpos/main/internal/domain/menucheck"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

// Problems runs Store › Menu › Find problems (menucheck) on a location: the
// bank, every channel view compiled now (vendor views aside) and the counter
// view's layout. GET /store/v1/me/menu/problems; the console's store check
// reads the same route.
func (s *Service) Problems(ctx context.Context, tenantID, locationID uuid.UUID) (menucheck.Report, error) {
	views, err := s.Views(ctx, tenantID, locationID)
	if err != nil {
		return menucheck.Report{}, err
	}
	bank, err := s.repo.LoadBank(ctx, locationID)
	if err != nil {
		return menucheck.Report{}, err
	}
	in := menucheck.Input{Bank: bank}
	for _, v := range views {
		if v.Kind == menuview.KindVendor {
			continue
		}
		entries, err := s.repo.ListEntries(ctx, v.ID)
		if err != nil {
			return menucheck.Report{}, err
		}
		in.Views = append(in.Views, menucheck.View{View: v, Build: menuview.Compile(bank, v, entries, nil)})
		if v.Kind == menuview.KindCounter && v.HasLayout && in.Layout == nil {
			l, err := s.repo.Layout(ctx, v.ID)
			if err != nil {
				return menucheck.Report{}, err
			}
			in.Layout = l
		}
	}
	return menucheck.Run(in), nil
}
