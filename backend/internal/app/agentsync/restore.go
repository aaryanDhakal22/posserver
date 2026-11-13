package agentsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/models"
	"quiccpos/main/internal/infra/pushover"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/shared/safego"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RestoreOrder is one order of the open batch as a replacement agent stores
// it: main's snapshot plus the old agent's own floor status (null for orders
// it never acted on, or from before agents reported it — the agent then
// derives it from the snapshot's status).
type RestoreOrder struct {
	Order        order.Snapshot  `json:"order"`
	AgentStatus  json.RawMessage `json:"agent_status"`
	AgentBatchID *int64          `json:"agent_batch_id,omitempty"`
}

// Restore is everything a blank agent needs to continue the shift.
type Restore struct {
	// State is the last agent.state the old agent reported (open batch,
	// kitchen counter, drivers + queue + open shifts, settings) — null if it
	// never reported one.
	State   json.RawMessage `json:"state"`
	StateAt *time.Time      `json:"state_at,omitempty"`
	Orders  []RestoreOrder  `json:"orders"`
	// Drivers is main's mirrored roster — the fallback when State is null.
	Drivers []order.Driver `json:"drivers"`
}

type stateBatch struct {
	Batch struct {
		ID       int64     `json:"id"`
		OpenedAt time.Time `json:"opened_at"`
	} `json:"batch"`
}

// Restore builds the open batch for the location's (new) agent.
func (s *Service) Restore(ctx context.Context, scope sseBroker.Scope) (*Restore, error) {
	q := models.New(s.pool)
	out := &Restore{Orders: []RestoreOrder{}, Drivers: []order.Driver{}}
	since := time.Now().Add(-12 * time.Hour)
	batch := pgtype.Int8{}
	st, err := q.GetAgentState(ctx, pgUUID(scope.LocationID))
	switch {
	case err == nil:
		out.State = st.State
		at := st.At.Time
		out.StateAt = &at
		var sb stateBatch
		if json.Unmarshal(st.State, &sb) == nil && sb.Batch.ID > 0 {
			batch = pgtype.Int8{Int64: sb.Batch.ID, Valid: true}
			if !sb.Batch.OpenedAt.IsZero() {
				since = sb.Batch.OpenedAt
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return nil, err
	}

	sc := order.Scope{TenantID: order.TenantID(scope.TenantID), LocationID: order.LocationID(scope.LocationID)}
	ids, err := q.ListRestoreOrderIDs(ctx, models.ListRestoreOrderIDsParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), BatchID: batch, Since: ts(since),
	})
	if err != nil {
		return nil, err
	}
	for _, pid := range ids {
		id := fromPg(pid)
		o, err := s.orders.GetByID(ctx, sc, order.ID(id))
		if err != nil {
			return nil, fmt.Errorf("load order %s: %w", id, err)
		}
		f, err := q.GetOrderAgentFields(ctx, models.GetOrderAgentFieldsParams{
			ID: pid, TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
		})
		if err != nil {
			return nil, err
		}
		ro := RestoreOrder{Order: o.Snapshot()}
		if len(f.AgentStatus) > 0 {
			ro.AgentStatus = f.AgentStatus
		}
		if f.AgentBatchID.Valid {
			b := f.AgentBatchID.Int64
			ro.AgentBatchID = &b
		}
		out.Orders = append(out.Orders, ro)
	}
	if s.drivers != nil {
		if ds, err := s.drivers.ListDrivers(ctx, sc); err == nil && ds != nil {
			out.Drivers = ds
		}
	}
	return out, nil
}

// --- backups -----------------------------------------------------------------

// MaxBackup bounds one uploaded agent DB copy.
const MaxBackup = 64 << 20

// Backup is one stored agent DB copy (no bytes).
type Backup struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	LocationID uuid.UUID  `json:"location_id"`
	DeviceID   *uuid.UUID `json:"device_id,omitempty"`
	Size       int64      `json:"size"`
	SHA256     string     `json:"sha256"`
	CreatedAt  time.Time  `json:"created_at"`
}

// StoreBackup keeps an agent's nightly SQLite copy (newest 2 per location).
func (s *Service) StoreBackup(ctx context.Context, scope sseBroker.Scope, deviceID uuid.UUID, data []byte) (*Backup, error) {
	if len(data) < 100 || string(data[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("not an SQLite database")
	}
	sum := sha256.Sum256(data)
	q := models.New(s.pool)
	row, err := q.InsertAgentBackup(ctx, models.InsertAgentBackupParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), DeviceID: pgUUID(deviceID),
		Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:]), Data: data,
	})
	if err != nil {
		return nil, err
	}
	if err := q.PruneAgentBackups(ctx, pgUUID(scope.LocationID)); err != nil {
		s.logger.Warn().Err(err).Msg("prune agent backups failed")
	}
	return backupFrom(row.ID, row.TenantID, row.LocationID, row.DeviceID, row.Size, row.Sha256, row.CreatedAt), nil
}

func backupFrom(id, tid, lid, did pgtype.UUID, size int64, sum string, at pgtype.Timestamptz) *Backup {
	b := &Backup{ID: fromPg(id), TenantID: fromPg(tid), LocationID: fromPg(lid), Size: size, SHA256: sum, CreatedAt: at.Time}
	if did.Valid {
		d := fromPg(did)
		b.DeviceID = &d
	}
	return b
}

// ListBackups lists stored backups (all locations when locationID is Nil).
func (s *Service) ListBackups(ctx context.Context, locationID uuid.UUID) ([]Backup, error) {
	loc := pgtype.UUID{}
	if locationID != uuid.Nil {
		loc = pgUUID(locationID)
	}
	rows, err := models.New(s.pool).ListAgentBackups(ctx, loc)
	if err != nil {
		return nil, err
	}
	out := make([]Backup, 0, len(rows))
	for _, r := range rows {
		out = append(out, *backupFrom(r.ID, r.TenantID, r.LocationID, r.DeviceID, r.Size, r.Sha256, r.CreatedAt))
	}
	return out, nil
}

// BackupData returns one backup's bytes.
func (s *Service) BackupData(ctx context.Context, id uuid.UUID) ([]byte, time.Time, error) {
	row, err := models.New(s.pool).GetAgentBackupData(ctx, pgUUID(id))
	if err != nil {
		return nil, time.Time{}, err
	}
	return row.Data, row.CreatedAt.Time, nil
}

// --- offline pager -------------------------------------------------------------

// RunOfflinePager checks every 30 s, for each location with an active agent
// device, whether its agent is connected. Two pages come out of it:
//
//   - the admin's (technical): gone OfflineAfter (2 min) inside the
//     location's opening hours (±15 min; only stores with hours set), once
//     per outage and again when back, to the admin's devices only;
//   - the store owner's (main 4.0.2, checkOwnerOffline): plain English after
//     owner_alert_offline_minutes (5) inside opening hours (else 09:00-02:00),
//     once per outage + one "back online", to the owner's devices + the boss
//     key. The Pi skips its own owner offline alert (store_config
//     owner_offline_alert_by_main), so the owner gets one, never two.
func (s *Service) RunOfflinePager(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.checkOffline(ctx, time.Now())
	}
}

func (s *Service) checkOffline(ctx context.Context, now time.Time) {
	locs, err := s.tenancy.ListLocations(ctx)
	if err != nil {
		return
	}
	for _, l := range locs {
		devs, err := s.tenancy.ListDevicesByLocation(ctx, l.ID)
		if err != nil {
			continue
		}
		hasAgent := false
		for _, d := range devs {
			if d.Kind == "agent" && d.RevokedAt == nil {
				hasAgent = true
			}
		}
		if !hasAgent { // no Pi to miss: forget any outage
			s.offMu.Lock()
			delete(s.offSince, l.ID)
			delete(s.offPaged, l.ID)
			if st := s.owner[l.ID]; st != nil && !st.inflight {
				delete(s.owner, l.ID)
			}
			s.offMu.Unlock()
			continue
		}
		tz, err := time.LoadLocation(l.Timezone)
		if err != nil {
			tz = time.UTC
		}
		connected := s.broker.SubscribersFor(sseBroker.Scope{TenantID: l.TenantID, LocationID: l.ID}) > 0
		s.checkAdminOffline(ctx, l, tz, connected, now)
		s.checkOwnerOffline(ctx, l, tz, connected, now)
	}
}

// checkAdminOffline is the admin's technical page (stores with opening hours).
func (s *Service) checkAdminOffline(ctx context.Context, l tenancy.Location, tz *time.Location, connected bool, now time.Time) {
	hours, ok, err := tenancy.HoursFromConfig(l.Config)
	if err != nil || !ok {
		return
	}
	s.offMu.Lock()
	since, wasOff := s.offSince[l.ID]
	paged := s.offPaged[l.ID]
	switch {
	case connected:
		delete(s.offSince, l.ID)
		delete(s.offPaged, l.ID)
	case !wasOff:
		s.offSince[l.ID] = now
	}
	s.offMu.Unlock()

	open := hours.IsOpen(now, tz, 15*time.Minute)
	switch {
	case !connected && wasOff && !paged && open && now.Sub(since) >= s.OfflineAfter:
		s.offMu.Lock()
		s.offPaged[l.ID] = true
		s.offMu.Unlock()
		s.pages.Add(ctx, 1, observability.WithStore(ctx, l.TenantID))
		msg := fmt.Sprintf("%s: the Pi (agent) has been offline for %d min during opening hours — orders are not printing. Check its power/network; if it doesn't come back, swap in the spare SD card.",
			l.Name, int(now.Sub(since).Minutes()))
		s.page(ctx, "Agent offline", msg, "siren")
	case connected && paged:
		s.page(ctx, "Agent back online", l.Name+": the Pi is connected again.", "classical")
	}
}

// page sends the admin's page: the admin's devices only (never the owner's
// phones or the boss key — the owner gets main's own plain-English page).
// Without admin devices nothing goes out: an unscoped page would ring the
// owner's phones too.
func (s *Service) page(ctx context.Context, title, msg, sound string) {
	s.logger.Warn().Ctx(ctx).Str("title", title).Msg(msg)
	safego.Go(context.WithoutCancel(ctx), "agentsync.admin_page", func(ctx context.Context) {
		if err := s.pager.SendAdmin(ctx, title, msg, sound); err != nil && !errors.Is(err, pushover.ErrNotConfigured) {
			s.logger.Warn().Err(err).Msg("admin page not sent")
		}
	})
}
