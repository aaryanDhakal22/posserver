package agentsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/tenancy"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
)

// OnPrintersChanged registers the hook run after the agent changed the
// location's printers (a printer followed to a new IP, a found printer added).
// cmd/api wires the store_config publisher, so the agent gets the new roster
// mid-stream like after a manager edit.
func (s *Service) OnPrintersChanged(fn func(ctx context.Context, tenantID, locationID uuid.UUID)) {
	s.printersChanged = fn
}

func (s *Service) publishPrinters(ctx context.Context, scope sseBroker.Scope) {
	if s.printersChanged != nil {
		s.printersChanged(ctx, scope.TenantID, scope.LocationID)
	}
}

// printerMovedReply is the printer.moved reply: reason "" (applied),
// "stale_old_ip" or "not_found".
type printerMovedReply struct {
	Applied bool   `json:"applied"`
	Reason  string `json:"reason"`
}

// HandlePrinterMoved is the rpc.InboundHandler for printer.moved
// {printer_id, old_ip, new_ip, mac}: the Pi found a printer that stopped
// answering at a new IP (same MAC). Applied only to a printer of the reporting
// agent's location and only while it still has old_ip — a manager edit in
// between wins (stale_old_ip). The change is logged in printer_ip_changes
// (source "agent") and the new roster pushed to the agent.
func (s *Service) HandlePrinterMoved(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		PrinterID string `json:"printer_id"`
		OldIP     string `json:"old_ip"`
		NewIP     string `json:"new_ip"`
		MAC       string `json:"mac"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: printer.moved: %v", rpc.ErrBadPayload, err)
	}
	if strings.TrimSpace(b.OldIP) == "" || strings.TrimSpace(b.NewIP) == "" {
		return nil, fmt.Errorf("%w: printer.moved needs old_ip and new_ip", rpc.ErrBadPayload)
	}
	id, err := uuid.Parse(b.PrinterID)
	if err != nil {
		return printerMovedReply{Reason: tenancyApp.MoveNotFound}, nil
	}
	res, err := s.tenancy.MovePrinter(ctx, scope.TenantID, scope.LocationID, id, b.OldIP, b.NewIP, b.MAC)
	if err != nil {
		return nil, err
	}
	lg := s.logger.With().Str("location_id", scope.LocationID.String()).Str("printer_id", id.String()).
		Str("old_ip", b.OldIP).Str("new_ip", b.NewIP).Str("mac", b.MAC).Logger()
	switch {
	case res.Changed:
		lg.Warn().Ctx(ctx).Str("printer", res.Printer.Name).Msg("printer moved: the Pi followed it to a new IP")
		s.publishPrinters(ctx, scope)
	case !res.Applied:
		lg.Info().Ctx(ctx).Str("reason", res.Reason).Msg("printer move not applied")
	}
	return printerMovedReply{Applied: res.Applied, Reason: res.Reason}, nil
}

// HandlePrinterAdd is the rpc.InboundHandler for printer.add {name, ip, mac}:
// a manager added a printer the Pi found on the network (Quibase setup, the
// Pi's Printers page). The reply is always a payload (HTTP 200):
// {ok:true, id, name, ip} or {ok:false, code: "name_taken"|"bad_request"}.
// The first printer of a location becomes its receipt printer, as in /app/store.
func (s *Service) HandlePrinterAdd(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		Name string `json:"name"`
		IP   string `json:"ip"`
		MAC  string `json:"mac"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return printerAddFail("bad_request"), nil
	}
	b.Name, b.IP = strings.TrimSpace(b.Name), strings.TrimSpace(b.IP)
	if b.Name == "" || b.IP == "" {
		return printerAddFail("bad_request"), nil
	}
	// Names are matched case-insensitively here ("Kitchen" vs "kitchen" would
	// be two stations nobody can tell apart on the tablet).
	existing, err := s.tenancy.ListPrintersByLocation(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	for _, p := range existing {
		if strings.EqualFold(p.Name, b.Name) {
			return printerAddFail("name_taken"), nil
		}
	}
	p, err := s.tenancy.AddPrinter(ctx, scope.LocationID, b.Name, b.IP, b.MAC)
	if errors.Is(err, tenancy.ErrPrinterNameTaken) {
		return printerAddFail("name_taken"), nil
	}
	if err != nil {
		return nil, err
	}
	s.logger.Info().Ctx(ctx).Str("location_id", scope.LocationID.String()).Str("printer", p.Name).
		Str("ip", p.IP).Str("mac", p.MAC).Msg("printer added from the Pi (found on the network)")
	s.publishPrinters(ctx, scope)
	return map[string]any{"ok": true, "id": p.ID.String(), "name": p.Name, "ip": p.IP}, nil
}

func printerAddFail(code string) map[string]any { return map[string]any{"ok": false, "code": code} }
