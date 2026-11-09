// Package onlinectl answers the Pi's online-ordering controls (weborder
// stage 3): Quibase pauses the store's channels (website + call center —
// Brygid isn't affected) and marks items and toppings sold out, through the
// Pi, over the duplex channel (agent → main RPCs, answered with data).
package onlinectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	storestatusApp "quiccpos/main/internal/app/storestatus"
	"quiccpos/main/internal/domain/storestatus"
	"quiccpos/main/internal/infra/database/repositories"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// RPC message types the Pi sends.
const (
	MsgOnlineStatus = "online.status"
	MsgOnlinePause  = "online.pause"
	MsgOnlineResume = "online.resume"
	MsgMenuStock    = "menu.stock"
	MsgMenuStockSet = "menu.stock.set"
)

// Presets Quibase offers (main's pause also knows "indefinite").
var presets = map[string]bool{"15m": true, "30m": true, "60m": true, "today": true}

// Stock reads and sets sold out (repositories.StockRepository).
type Stock interface {
	List(ctx context.Context, locationID uuid.UUID) ([]repositories.StockItem, []repositories.StockGroup, error)
	SetItem(ctx context.Context, locationID, id uuid.UUID, inStock, hold bool) (repositories.StockItem, error)
	SetOption(ctx context.Context, locationID, id uuid.UUID, inStock, hold bool) (repositories.StockOption, error)
}

type Service struct {
	status *storestatusApp.Service
	stock  Stock
	logger zerolog.Logger
}

func NewService(status *storestatusApp.Service, stock Stock, lg zerolog.Logger) *Service {
	return &Service{status: status, stock: stock, logger: lg.With().Str("module", "onlinectl").Logger()}
}

// Online is what Quibase shows: open / paused / closed and the pause.
type Online struct {
	State      string                 `json:"state"`
	Accepting  bool                   `json:"accepting"`
	OpenNow    bool                   `json:"open_now"`
	Pause      *storestatus.PauseInfo `json:"pause"`
	ClosesAt   *time.Time             `json:"closes_at"`
	NextOpenAt *time.Time             `json:"next_open_at"`
}

func (s *Service) online(ctx context.Context, scope sseBroker.Scope) (Online, error) {
	f, err := s.status.Status(ctx, scope.TenantID, scope.LocationID)
	if err != nil {
		return Online{}, err
	}
	return Online{State: f.State, Accepting: f.Accepting, OpenNow: f.OpenNow, Pause: f.Pause, ClosesAt: f.ClosesAt, NextOpenAt: f.NextOpenAt}, nil
}

// HandleStatus is online.status {} → Online.
func (s *Service) HandleStatus(ctx context.Context, scope sseBroker.Scope, _ json.RawMessage) (any, error) {
	return s.online(ctx, scope)
}

// HandlePause is online.pause {preset 15m|30m|60m|today, note} → Online.
func (s *Service) HandlePause(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		Preset string `json:"preset"`
		Note   string `json:"note"`
	}
	if err := json.Unmarshal(payload, &b); err != nil || !presets[b.Preset] {
		return nil, fmt.Errorf("%w: online.pause needs {preset: 15m|30m|60m|today, note?}", rpc.ErrBadPayload)
	}
	if len([]rune(strings.TrimSpace(b.Note))) > 140 {
		return nil, fmt.Errorf("%w: the note is limited to 140 characters", rpc.ErrBadPayload)
	}
	if _, err := s.status.Pause(ctx, scope.TenantID, scope.LocationID, b.Preset, b.Note); err != nil {
		return nil, err
	}
	s.logger.Info().Str("location_id", scope.LocationID.String()).Str("preset", b.Preset).Msg("paused from the Pi")
	return s.online(ctx, scope)
}

// HandleResume is online.resume {} → Online.
func (s *Service) HandleResume(ctx context.Context, scope sseBroker.Scope, _ json.RawMessage) (any, error) {
	if _, err := s.status.Resume(ctx, scope.TenantID, scope.LocationID); err != nil {
		return nil, err
	}
	return s.online(ctx, scope)
}

// HandleStock is menu.stock {} → {items, groups}.
func (s *Service) HandleStock(ctx context.Context, scope sseBroker.Scope, _ json.RawMessage) (any, error) {
	items, groups, err := s.stock.List(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "groups": groups}, nil
}

// HandleStockSet is menu.stock.set {kind item|option, id, in_stock, hold} → the row.
func (s *Service) HandleStockSet(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		InStock *bool  `json:"in_stock"`
		Hold    bool   `json:"hold"`
	}
	if err := json.Unmarshal(payload, &b); err != nil || b.InStock == nil || (b.Kind != "item" && b.Kind != "option") {
		return nil, fmt.Errorf("%w: menu.stock.set needs {kind: item|option, id, in_stock, hold?}", rpc.ErrBadPayload)
	}
	id, err := uuid.Parse(b.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: id isn't an id", rpc.ErrBadPayload)
	}
	var out any
	if b.Kind == "item" {
		out, err = s.stock.SetItem(ctx, scope.LocationID, id, *b.InStock, b.Hold)
	} else {
		out, err = s.stock.SetOption(ctx, scope.LocationID, id, *b.InStock, b.Hold)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no such %s at this store", rpc.ErrBadPayload, b.Kind)
	}
	if err != nil {
		return nil, err
	}
	s.logger.Info().Str("location_id", scope.LocationID.String()).Str("kind", b.Kind).Bool("in_stock", *b.InStock).Bool("hold", b.Hold).
		Msg("stock set from the Pi")
	return out, nil
}
