package agentsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tenancyApp "quiccpos/main/internal/app/tenancy"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// LinkRequest is what a blank agent sends after a manager types the store
// login on the tablet (or the agent's web UI).
type LinkRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	AgentName string `json:"agent_name"`
	// RemoteAddr is the caller (logged with the link); filled by the handler.
	RemoteAddr string `json:"-"`
}

// LinkResult is everything a replacement agent needs to come up: its key,
// who it belongs to, the manager credential for offline unlock and the runtime
// secrets.
type LinkResult struct {
	APIKey   string      `json:"api_key"`
	DeviceID uuid.UUID   `json:"device_id"`
	Tenant   LinkTenant  `json:"tenant"`
	Location LinkLoc     `json:"location"`
	Manager  Manager     `json:"manager"`
	Secrets  Secrets     `json:"secrets"`
	Revoked  []uuid.UUID `json:"revoked_agents"`
}

type LinkTenant struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

type LinkLoc struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Timezone string    `json:"timezone"`
}

// Manager is the store manager login an agent unlocks its web UI with.
type Manager struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

// Link verifies the manager login, mints a new agent key for that store's
// location, revokes every other agent of the location (one live agent per
// restaurant — an old Pi that comes back wipes itself instead of printing
// twice) and returns the runtime secrets.
func (s *Service) Link(ctx context.Context, req LinkRequest) (*LinkResult, error) {
	// Throttled per username (behind Traefik every caller shares the proxy's
	// address) plus a global ceiling against spraying many usernames.
	key := "u:" + strings.ToLower(strings.TrimSpace(req.Username))
	if !s.linkLimit.allow(key) || !s.linkLimit.allowN(linkGlobalKey, linkGlobalMax) {
		return nil, ErrThrottled
	}
	tenant, err := s.tenancy.AuthenticateTenant(ctx, req.Username, req.Password)
	if err != nil {
		if errors.Is(err, tenancyApp.ErrInvalidCredentials) {
			s.linkLimit.fail(key)
			s.linkLimit.fail(linkGlobalKey)
			return nil, ErrBadCredentials
		}
		return nil, err
	}
	s.linkLimit.reset(key)
	loc, err := s.tenancy.PrimaryLocation(ctx, tenant.ID)
	if err != nil {
		return nil, fmt.Errorf("store has no location: %w", err)
	}
	name := strings.TrimSpace(req.AgentName)
	if name == "" {
		name = "pi"
	}
	name = fmt.Sprintf("%s (linked %s)", name, time.Now().Format("2006-01-02 15:04"))

	minted, err := s.tenancy.MintDevice(ctx, loc.ID, name, tenancyApp.DeviceKindAgent)
	if err != nil {
		return nil, fmt.Errorf("mint agent: %w", err)
	}
	devs, err := s.tenancy.ListDevicesByLocation(ctx, loc.ID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	var revoked []uuid.UUID
	for _, d := range devs {
		if d.Kind == tenancyApp.DeviceKindAgent && d.RevokedAt == nil && d.ID != minted.Device.ID {
			if err := s.tenancy.RevokeDevice(ctx, d.ID); err != nil {
				return nil, fmt.Errorf("revoke old agent: %w", err)
			}
			revoked = append(revoked, d.ID)
		}
	}
	user, hash, err := s.tenancy.ManagerCredential(ctx, tenant.ID)
	if err != nil {
		return nil, fmt.Errorf("manager credential: %w", err)
	}
	s.links.Add(ctx, 1, metric.WithAttributes(attribute.Bool("replaced", len(revoked) > 0)))
	s.logger.Info().Ctx(ctx).Str("tenant", tenant.Slug).Str("device_id", minted.Device.ID.String()).Str("remote", req.RemoteAddr).
		Int("revoked_agents", len(revoked)).Msg("agent linked with the manager login")
	if len(revoked) > 0 {
		msg := fmt.Sprintf("%s: the Pi was replaced and linked again (old agent revoked). Send a new spare SD card.", tenant.Name)
		go func(ctx context.Context) {
			if err := s.pager.Send(ctx, "Pi replaced", msg, "pushover"); err != nil {
				s.logger.Warn().Err(err).Msg("relink page failed")
			}
		}(context.WithoutCancel(ctx))
	}
	return &LinkResult{
		APIKey:   minted.ClearKey,
		DeviceID: minted.Device.ID,
		Tenant:   LinkTenant{ID: tenant.ID, Slug: tenant.Slug, Name: tenant.Name},
		Location: LinkLoc{ID: loc.ID, Name: loc.Name, Timezone: loc.Timezone},
		Manager:  Manager{Username: user, PasswordHash: hash},
		Secrets:  s.secrets,
		Revoked:  revoked,
	}, nil
}

// Credential returns the current manager login hash for an agent's location
// (a linked agent refreshes its offline unlock with it).
func (s *Service) Credential(ctx context.Context, tenantID uuid.UUID) (*Manager, error) {
	user, hash, err := s.tenancy.ManagerCredential(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return &Manager{Username: user, PasswordHash: hash}, nil
}

// limiter allows `max` failures per key within `window`.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *limiter) prune(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = keep
	}
	return keep
}

// linkGlobalKey/Max cap failed links across all usernames per window.
const (
	linkGlobalKey = "*"
	linkGlobalMax = 30
)

func (l *limiter) allow(key string) bool { return l.allowN(key, l.max) }

func (l *limiter) allowN(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, time.Now())) < max
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.fails) > 10_000 { // random usernames: drop expired entries
		for k := range l.fails {
			l.prune(k, now)
		}
	}
	l.fails[key] = append(l.prune(key, now), now)
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
