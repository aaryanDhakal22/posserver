package tracking

import (
	"context"
	"errors"
	"testing"
	"time"

	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type fakeRepo struct {
	Repo
	rows   []repositories.AlertRow
	marked []uuid.UUID
	pruned time.Time
	asked  [3]time.Time
}

func (f *fakeRepo) Prune(_ context.Context, before time.Time) (int64, error) {
	f.pruned = before
	return 0, nil
}
func (f *fakeRepo) ToAlert(_ context.Context, a, b, c time.Time) ([]repositories.AlertRow, error) {
	f.asked = [3]time.Time{a, b, c}
	return f.rows, nil
}
func (f *fakeRepo) MarkAlerted(_ context.Context, id uuid.UUID) error {
	f.marked = append(f.marked, id)
	return nil
}

type fakeNotifier struct {
	got []notify.Notice
	err error
}

func (f *fakeNotifier) Notify(_ context.Context, n notify.Notice) error {
	f.got = append(f.got, n)
	return f.err
}

// An order the Pi hasn't accepted pages the owner once (marked); a Pushover
// failure is retried next minute (not marked); "not configured" is final.
func TestAlert(t *testing.T) {
	now := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
	a, b := uuid.New(), uuid.New()
	repo := &fakeRepo{rows: []repositories.AlertRow{{OrderID: a, LocationID: uuid.New(), ChannelName: "Call center", Customer: "Grace Hopper"}}}
	n := &fakeNotifier{}
	s := NewService(repo, zerolog.Nop())
	s.Wire(nil, nil, n)
	s.now = func() time.Time { return now }
	s.maintain(context.Background())
	if !repo.pruned.Equal(now.Add(-Retention)) || !repo.asked[0].Equal(now.Add(-AlertAfter)) || !repo.asked[1].Equal(now.Add(-6*time.Hour)) {
		t.Errorf("windows: pruned %v asked %v", repo.pruned, repo.asked)
	}
	if len(n.got) != 1 || n.got[0].Kind != notify.KindOrderNotAccepted || len(repo.marked) != 1 || repo.marked[0] != a {
		t.Fatalf("paged %+v marked %v", n.got, repo.marked)
	}
	if want := "A Call center order for Grace hasn't been accepted on the tablet for 10 minutes. Check the tablet and the Pi."; n.got[0].Message != want {
		t.Errorf("message %q", n.got[0].Message)
	}

	repo.rows, repo.marked, n.err = []repositories.AlertRow{{OrderID: b}}, nil, errors.New("pushover down")
	s.maintain(context.Background())
	if len(repo.marked) != 0 {
		t.Error("a failed page must be retried, not marked")
	}
	n.err = notify.ErrNotConfigured
	s.maintain(context.Background())
	if len(repo.marked) != 1 {
		t.Error("no Pushover configured: logged, marked, not retried every minute")
	}
}

func TestDriverOnlyWhileOut(t *testing.T) {
	if d := driverOf("accepted", "Abdul Rahman"); d != nil {
		t.Errorf("accepted: %+v", d)
	}
	if d := driverOf("on_delivery", "Abdul Rahman"); d == nil || d.FirstName != "Abdul" {
		t.Errorf("out: %+v", d)
	}
	if d := driverOf("completed", "  "); d != nil {
		t.Errorf("no name: %+v", d)
	}
}
