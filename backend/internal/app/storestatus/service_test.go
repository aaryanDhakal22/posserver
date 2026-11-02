package storestatusApp

import (
	"context"
	"testing"
	"time"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type fakeRepo struct {
	Repo
	rows      []repositories.RestockRow
	restocked []uuid.UUID
	ensured   int
}

func (f *fakeRepo) ExpireOutUntil(context.Context) (int64, error) { return 0, nil }
func (f *fakeRepo) EnsureAll(_ context.Context, l []tenancy.Location) error {
	f.ensured = len(l)
	return nil
}
func (f *fakeRepo) RestockRows(context.Context) ([]repositories.RestockRow, error) {
	return f.rows, nil
}
func (f *fakeRepo) Restock(_ context.Context, id uuid.UUID, _ time.Time) (int64, error) {
	f.restocked = append(f.restocked, id)
	return 1, nil
}

type fakeLocs struct{ Locations }

func (fakeLocs) ListLocations(context.Context) ([]tenancy.Location, error) {
	return []tenancy.Location{{ID: uuid.New()}, {ID: uuid.New()}}, nil
}

// A location restocks once per reset: when its last restock is before the
// latest reset hour (store time), never again until the next one.
func TestRestockAtTheResetHour(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 2, 4, 30, 0, 0, ny)
	ts := func(h, m int) *time.Time { v := time.Date(2026, 11, 2, h, m, 0, 0, ny); return &v }
	due, done, later, utc := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := &fakeRepo{rows: []repositories.RestockRow{
		{LocationID: due, RestockedAt: ts(3, 0), Timezone: "America/New_York", DailyResetHour: 4},
		{LocationID: done, RestockedAt: ts(4, 1), Timezone: "America/New_York", DailyResetHour: 4},
		{LocationID: later, RestockedAt: ts(3, 0), Timezone: "America/New_York", DailyResetHour: 5},
		// 4:30 New York = 9:30 UTC: a UTC store at reset hour 9 is due.
		{LocationID: utc, RestockedAt: ts(3, 0), Timezone: "", DailyResetHour: 9},
	}}
	s := NewService(repo, fakeLocs{}, nil, zerolog.Nop())
	s.now = func() time.Time { return now }
	s.restock(context.Background())
	if repo.ensured != 2 {
		t.Errorf("rows ensured for %d locations", repo.ensured)
	}
	if len(repo.restocked) != 2 || repo.restocked[0] != due || repo.restocked[1] != utc {
		t.Errorf("restocked %v, want [due utc]", repo.restocked)
	}
}
