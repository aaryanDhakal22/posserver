package handler

import (
	"context"
	"net/http"
	"time"

	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// KitchenStatusHandler serves admin's per-store "kitchen offline" badge: the
// same Pi liveness check (app/kitchen) that refuses channel orders with 409
// kitchen_offline. A retired Pi whose agent key was never revoked shows up
// here as offline (DURABILITY-PLAN.md; the rule is "revoke the key when you
// retire the Pi", deploy/pi/RECOVERY.md).
type KitchenStatusHandler struct {
	locations func(ctx context.Context) ([]tenancy.Location, error)
	kitchen   KitchenCheck // nil: every store reads online
	logger    zerolog.Logger
}

func NewKitchenStatusHandler(locations func(ctx context.Context) ([]tenancy.Location, error), kitchen KitchenCheck, logger zerolog.Logger) *KitchenStatusHandler {
	return &KitchenStatusHandler{locations: locations, kitchen: kitchen, logger: logger.With().Str("module", "kitchen-status-handler").Logger()}
}

type kitchenStatusJSON struct {
	TenantID   uuid.UUID  `json:"tenant_id"`
	LocationID uuid.UUID  `json:"location_id"`
	Offline    bool       `json:"kitchen_offline"`
	AwaySince  *time.Time `json:"away_since"`
}

// AdminList — GET /admin/v1/kitchen: every location's kitchen_offline (true =
// channel orders are being refused right now) and since when its Pi is away.
// A location whose check fails is left out (logged) rather than failing all.
func (h *KitchenStatusHandler) AdminList(c *echo.Context) error {
	ctx := c.Request().Context()
	locs, err := h.locations(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list locations failed"))
	}
	out := make([]kitchenStatusJSON, 0, len(locs))
	for _, l := range locs {
		row := kitchenStatusJSON{TenantID: l.TenantID, LocationID: l.ID}
		if h.kitchen != nil {
			off, since, err := h.kitchen.Offline(ctx, l.TenantID, l.ID)
			if err != nil {
				h.logger.Warn().Ctx(ctx).Err(err).Str("location_id", l.ID.String()).Msg("kitchen check failed")
				continue
			}
			row.Offline = off
			if off && !since.IsZero() {
				s := since.UTC()
				row.AwaySince = &s
			}
		}
		out = append(out, row)
	}
	return c.JSON(http.StatusOK, out)
}
