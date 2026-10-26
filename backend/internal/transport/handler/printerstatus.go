package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"quiccpos/main/internal/app/printerstatus"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
)

// PrinterStatusHandler serves the agent-reported live printer roster to the
// store-manager UI (behind StoreSessionAuth — scope from the cookie).
type PrinterStatusHandler struct {
	cache     *printerstatus.Cache
	requester *rpc.Requester
	meters    observability.Meters
	logger    zerolog.Logger
}

func NewPrinterStatusHandler(cache *printerstatus.Cache, requester *rpc.Requester, meters observability.Meters, logger zerolog.Logger) *PrinterStatusHandler {
	return &PrinterStatusHandler{cache: cache, requester: requester, meters: meters, logger: logger.With().Str("module", "printer-status").Logger()}
}

// MeGetLive serves GET /store/v1/me/printers/live: an on-demand pull of the
// agent's live roster (short timeout), falling back to the cached report. The
// response carries the report age + whether an agent is currently connected so
// the UI can flag stale/offline data, each printer's MAC, and `found` — printers
// the Pi found on its network that aren't in the roster yet.
func (h *PrinterStatusHandler) MeGetLive(c *echo.Context) error {
	ctx := c.Request().Context()
	tid, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("login required"))
	}
	scope := sseBroker.Scope{TenantID: tid, LocationID: lid}

	// Try a fresh pull; refresh the cache if it succeeds.
	reply, err := h.requester.Request(ctx, scope, rpc.MsgPrinterReportRequest, nil, 2*time.Second)
	if err == nil {
		var rep printerstatus.Report
		if json.Unmarshal(reply.Payload, &rep) == nil {
			h.cache.Put(scope, rep)
		}
	}
	agentOnline := !errors.Is(err, rpc.ErrAgentOffline)

	// result: live (fresh duplex reply), offline (no agent connected), or
	// cache (agent connected but the pull timed out / errored → served stale).
	queryResult := "live"
	switch {
	case errors.Is(err, rpc.ErrAgentOffline):
		queryResult = "offline"
	case err != nil:
		queryResult = "cache"
	}
	h.meters.PrintersLiveQueries.Add(ctx, 1, observability.WithStore(ctx, tid, attribute.String("result", queryResult)))

	rep, age, cached := h.cache.Get(scope)
	out := map[string]any{
		"agent_online": agentOnline,
		"printers":     rep.Printers, // nil → JSON null; fine, UI treats as empty
		// Printers the Pi found on its network that aren't in the roster yet
		// (agents before 2.0.6 don't scan → always empty).
		"found": rep.Found,
	}
	if cached {
		out["reported_at"] = rep.ReportedAt
		out["age_seconds"] = int(age.Seconds())
	}
	if rep.Printers == nil {
		out["printers"] = []printerstatus.LivePrinter{}
	}
	if rep.Found == nil {
		out["found"] = []printerstatus.FoundPrinter{}
	}
	return c.JSON(http.StatusOK, out)
}
