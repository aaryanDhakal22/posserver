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
		b := menuview.Compile(bank, v, entries, nil)
		if v.Kind == menuview.KindCounter && v.HasLayout && in.Layout == nil {
			l, err := s.repo.Layout(ctx, v.ID)
			if err != nil {
				return menucheck.Report{}, err
			}
			in.Layout = l
			// Its build gets the layout too: broken buttons are lines of it.
			if l != nil {
				deals, err := s.repo.DealNames(ctx, locationID)
				if err != nil {
					return menucheck.Report{}, err
				}
				menuview.ResolveLayout(&b, bank, v, deals, l)
			}
		}
		in.Views = append(in.Views, menucheck.View{View: v, Build: b})
	}
	return menucheck.Run(in), nil
}
