package diag

import (
	"context"
	"sort"
	"sync"
	"time"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"
)

// fakeDB emulates the handful of diag queries in memory. Embedding the sqlc
// interface means any query a test did not expect panics (nil receiver).
type fakeDB struct {
	models.Querier
	mu      sync.Mutex
	seq     int
	tokens  []*fakeToken
	reports map[uuid.UUID]models.InsertDiagReportParams
	paged   map[uuid.UUID]time.Time
	now     func() time.Time
	touches int
}

type fakeToken struct {
	id       uuid.UUID
	location uuid.UUID
	tabletID string
	hash     string
	seq      int
	lastUsed pgtype.Timestamptz
	revoked  bool
}

func newFakeDB(now func() time.Time) *fakeDB {
	return &fakeDB{reports: map[uuid.UUID]models.InsertDiagReportParams{}, paged: map[uuid.UUID]time.Time{}, now: now}
}

func (f *fakeDB) RevokeTabletTokensForTablet(_ context.Context, a models.RevokeTabletTokensForTabletParams) (int64, error) {
	var n int64
	for _, t := range f.tokens {
		if t.location == fromPg(a.LocationID) && t.tabletID == a.TabletID && !t.revoked {
			t.revoked = true
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) InsertTabletReportToken(_ context.Context, a models.InsertTabletReportTokenParams) (pgtype.UUID, error) {
	f.seq++
	t := &fakeToken{id: uuid.New(), location: fromPg(a.LocationID), tabletID: a.TabletID, hash: a.TokenHash, seq: f.seq}
	f.tokens = append(f.tokens, t)
	return pgUUID(t.id), nil
}

func (f *fakeDB) RevokeExcessTabletTokens(_ context.Context, a models.RevokeExcessTabletTokensParams) (int64, error) {
	var active []*fakeToken
	for _, t := range f.tokens {
		if t.location == fromPg(a.LocationID) && !t.revoked {
			active = append(active, t)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].seq > active[j].seq }) // newest first
	var n int64
	for i, t := range active {
		if i >= int(a.Keep) {
			t.revoked = true
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) GetActiveTabletTokenByHash(_ context.Context, hash string) (models.GetActiveTabletTokenByHashRow, error) {
	for _, t := range f.tokens {
		if t.hash == hash && !t.revoked {
			return models.GetActiveTabletTokenByHashRow{ID: pgUUID(t.id), LocationID: pgUUID(t.location), TabletID: t.tabletID, LastUsedAt: t.lastUsed}, nil
		}
	}
	return models.GetActiveTabletTokenByHashRow{}, pgx.ErrNoRows
}

func (f *fakeDB) TouchTabletToken(_ context.Context, id pgtype.UUID) error {
	f.touches++
	for _, t := range f.tokens {
		if t.id == fromPg(id) {
			t.lastUsed = ts(f.now())
		}
	}
	return nil
}

func (f *fakeDB) DiagReportExists(_ context.Context, id pgtype.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.reports[fromPg(id)]
	return ok, nil
}

func (f *fakeDB) InsertDiagReport(_ context.Context, a models.InsertDiagReportParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.reports[fromPg(a.ID)]; ok {
		return 0, nil
	}
	f.reports[fromPg(a.ID)] = a
	return 1, nil
}

func (f *fakeDB) LastDiagPage(_ context.Context, a models.LastDiagPageParams) (pgtype.Timestamptz, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	last := time.Unix(0, 0)
	for id, at := range f.paged {
		r := f.reports[id]
		if fromPg(r.LocationID) == fromPg(a.LocationID) && r.Scenario == a.Scenario && r.Outcome == a.Outcome && at.After(last) {
			last = at
		}
	}
	return ts(last), nil
}

func (f *fakeDB) MarkDiagReportPaged(_ context.Context, id pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paged[fromPg(id)] = f.now()
	return nil
}

type fakePager struct {
	sent []struct{ title, msg string }
}

func (p *fakePager) Enabled() bool { return true }
func (p *fakePager) Send(_ context.Context, title, msg, _ string) error {
	p.sent = append(p.sent, struct{ title, msg string }{title, msg})
	return nil
}

type fakeTenants struct{ name string }

func (t fakeTenants) GetTenant(_ context.Context, id uuid.UUID) (*tenancy.Tenant, error) {
	return &tenancy.Tenant{ID: id, Name: t.name}, nil
}

// testService builds a Service on the fake DB with a controllable clock and
// pages run inline.
func testService(clock *time.Time) (*Service, *fakeDB, *fakePager) {
	now := func() time.Time { return *clock }
	db := newFakeDB(now)
	pager := &fakePager{}
	s := newService(db, func(ctx context.Context, fn func(Queries) error) error { return fn(db) },
		pager, fakeTenants{name: "Lombardi's"}, zerolog.Nop())
	s.now = now
	s.limits.setClock(now)
	s.async = func(fn func()) { fn() }
	return s, db, pager
}
