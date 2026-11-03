package channelApp

import (
	"context"
	"errors"
	"testing"

	channelDom "quiccpos/main/internal/domain/channel"
	operatorDom "quiccpos/main/internal/domain/operator"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// memRepo keeps channels in memory (only what the operator path uses).
type memRepo struct {
	channelDom.Repository
	byID    map[uuid.UUID]*channelDom.Channel
	created int
}

func (m *memRepo) Get(_ context.Context, id uuid.UUID) (*channelDom.Channel, error) {
	if c, ok := m.byID[id]; ok {
		return c, nil
	}
	return nil, pgx.ErrNoRows
}

func (m *memRepo) Builtin(_ context.Context, loc uuid.UUID, kind channelDom.Kind) (*channelDom.Channel, error) {
	for _, c := range m.byID {
		if c.LocationID == loc && c.Kind == kind && c.Builtin {
			return c, nil
		}
	}
	return nil, pgx.ErrNoRows
}

func (m *memRepo) Create(_ context.Context, n channelDom.NewChannel) (*channelDom.Channel, error) {
	m.created++
	c := &channelDom.Channel{ID: uuid.New(), TenantID: n.TenantID, LocationID: n.LocationID, Kind: n.Kind, Name: n.Name,
		Permissions: n.Permissions, Builtin: n.Builtin}
	m.byID[c.ID] = c
	return c, nil
}

type fakeOps struct{ allowed []operatorDom.AllowedLocation }

func (f fakeOps) AuthenticateSession(_ context.Context, raw string) (*operatorDom.Operator, []operatorDom.AllowedLocation, error) {
	if raw != "qops_good" {
		return nil, nil, errors.New("invalid")
	}
	return &operatorDom.Operator{ID: uuid.New(), Username: "sara", DisplayName: "Sara Smith"}, f.allowed, nil
}

// An operator's sign-in opens the allowed store's built-in Call center
// channel (made once, then reused); another store is refused; a bad token
// is an invalid key.
func TestAuthenticateOperator(t *testing.T) {
	tenant, a, b := uuid.New(), uuid.New(), uuid.New()
	repo := &memRepo{byID: map[uuid.UUID]*channelDom.Channel{}}
	s := NewService(repo, nil, zerolog.Nop())
	s.SetOperators(fakeOps{allowed: []operatorDom.AllowedLocation{{TenantID: tenant, LocationID: a}}})

	ch, op, err := s.AuthenticateOperator(context.Background(), "qops_good", a)
	if err != nil || ch.Kind != channelDom.KindOperator || !ch.Builtin || ch.Name != "Call center" || ch.TenantID != tenant || op.DisplayName != "Sara Smith" {
		t.Fatalf("got %+v %+v %v", ch, op, err)
	}
	if !ch.Permissions.Orders || len(ch.Permissions.Payments) != 2 {
		t.Errorf("operator defaults: %+v", ch.Permissions)
	}
	again, _, _ := s.AuthenticateOperator(context.Background(), "qops_good", a)
	if again.ID != ch.ID || repo.created != 1 {
		t.Errorf("the built-in channel must be made once: created %d", repo.created)
	}
	if _, _, err := s.AuthenticateOperator(context.Background(), "qops_good", b); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("another store: %v", err)
	}
	if _, _, err := s.AuthenticateOperator(context.Background(), "qops_bad", a); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("a bad sign-in: %v", err)
	}
}
