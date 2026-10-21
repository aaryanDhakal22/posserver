package tenancyApp

import (
	"context"
	"fmt"
	"testing"

	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// printerRepo is an in-memory printer store for the printer-follow logic;
// any other repository method panics (nil embedded interface).
type printerRepo struct {
	tenancy.Repository
	printers map[uuid.UUID]*tenancy.Printer
	changes  []tenancy.PrinterIPChange
	// moveRace makes MovePrinterIP report that the IP changed underneath it.
	moveRace bool
}

func (r *printerRepo) GetPrinterByID(_ context.Context, id uuid.UUID) (*tenancy.Printer, error) {
	p, ok := r.printers[id]
	if !ok {
		return nil, fmt.Errorf("get printer: %w", pgx.ErrNoRows)
	}
	cp := *p
	return &cp, nil
}

func (r *printerRepo) UpdatePrinter(_ context.Context, id uuid.UUID, name, ip string) (*tenancy.Printer, error) {
	p := r.printers[id]
	p.Name, p.IP = name, ip
	cp := *p
	return &cp, nil
}

func (r *printerRepo) SetPrinterMAC(_ context.Context, id uuid.UUID, mac string) error {
	r.printers[id].MAC = mac
	return nil
}

func (r *printerRepo) MovePrinterIP(_ context.Context, locationID, id uuid.UUID, oldIP, newIP, mac, source string) (*tenancy.Printer, error) {
	p := r.printers[id]
	if r.moveRace || p.LocationID != locationID || p.IP != oldIP {
		return nil, tenancy.ErrPrinterIPChanged
	}
	p.IP = newIP
	if mac != "" {
		p.MAC = mac
	}
	r.changes = append(r.changes, tenancy.PrinterIPChange{PrinterID: id, OldIP: oldIP, NewIP: newIP, MAC: p.MAC, Source: source})
	cp := *p
	return &cp, nil
}

func (r *printerRepo) InsertPrinterIPChange(_ context.Context, c tenancy.PrinterIPChange) error {
	r.changes = append(r.changes, c)
	return nil
}

func newPrinterFixture() (*Service, *printerRepo, tenancy.Printer) {
	p := tenancy.Printer{ID: uuid.New(), TenantID: uuid.New(), LocationID: uuid.New(), Name: "Pizza", IP: "10.0.0.20"}
	cp := p
	repo := &printerRepo{printers: map[uuid.UUID]*tenancy.Printer{p.ID: &cp}}
	return NewService(repo, zerolog.Nop()), repo, p
}

func TestMovePrinter(t *testing.T) {
	ctx := context.Background()
	const mac = "AA-BB-CC-00-11-22"

	t.Run("applied: moves, sets the MAC, logs source agent", func(t *testing.T) {
		svc, repo, p := newPrinterFixture()
		res, err := svc.MovePrinter(ctx, p.TenantID, p.LocationID, p.ID, "10.0.0.20", "10.0.0.31", mac)
		if err != nil || !res.Applied || !res.Changed || res.Reason != "" {
			t.Fatalf("res=%+v err=%v, want applied+changed", res, err)
		}
		got := repo.printers[p.ID]
		if got.IP != "10.0.0.31" || got.MAC != "aa:bb:cc:00:11:22" {
			t.Fatalf("printer = %+v, want new ip + normalized mac", got)
		}
		if len(repo.changes) != 1 || repo.changes[0].Source != tenancy.IPChangeByAgent {
			t.Fatalf("changes = %+v, want one agent change", repo.changes)
		}
	})

	t.Run("repeat of a move already made: applied, no second log row", func(t *testing.T) {
		svc, repo, p := newPrinterFixture()
		repo.printers[p.ID].IP = "10.0.0.31"
		res, err := svc.MovePrinter(ctx, p.TenantID, p.LocationID, p.ID, "10.0.0.20", "10.0.0.31", mac)
		if err != nil || !res.Applied || res.Changed {
			t.Fatalf("res=%+v err=%v, want applied without change", res, err)
		}
		if len(repo.changes) != 0 || repo.printers[p.ID].MAC != "aa:bb:cc:00:11:22" {
			t.Fatalf("changes=%+v mac=%q", repo.changes, repo.printers[p.ID].MAC)
		}
	})

	t.Run("stale old ip: a manager edit in between wins", func(t *testing.T) {
		svc, repo, p := newPrinterFixture()
		res, err := svc.MovePrinter(ctx, p.TenantID, p.LocationID, p.ID, "10.0.0.99", "10.0.0.31", mac)
		if err != nil || res.Applied || res.Reason != MoveStaleOldIP {
			t.Fatalf("res=%+v err=%v, want stale_old_ip", res, err)
		}
		if repo.printers[p.ID].IP != "10.0.0.20" || len(repo.changes) != 0 {
			t.Fatal("a stale move must change nothing")
		}
	})

	t.Run("stale when the ip changes between read and write", func(t *testing.T) {
		svc, repo, p := newPrinterFixture()
		repo.moveRace = true
		res, err := svc.MovePrinter(ctx, p.TenantID, p.LocationID, p.ID, "10.0.0.20", "10.0.0.31", "")
		if err != nil || res.Applied || res.Reason != MoveStaleOldIP {
			t.Fatalf("res=%+v err=%v, want stale_old_ip", res, err)
		}
	})

	t.Run("another location's printer is not found", func(t *testing.T) {
		svc, _, p := newPrinterFixture()
		res, err := svc.MovePrinter(ctx, p.TenantID, uuid.New(), p.ID, "10.0.0.20", "10.0.0.31", "")
		if err != nil || res.Applied || res.Reason != MoveNotFound {
			t.Fatalf("res=%+v err=%v, want not_found", res, err)
		}
		res, err = svc.MovePrinter(ctx, p.TenantID, p.LocationID, uuid.New(), "10.0.0.20", "10.0.0.31", "")
		if err != nil || res.Reason != MoveNotFound {
			t.Fatalf("unknown printer: res=%+v err=%v, want not_found", res, err)
		}
	})
}

func TestUpdatePrinterLogsManagerIPChange(t *testing.T) {
	ctx := context.Background()
	svc, repo, p := newPrinterFixture()

	if _, err := svc.UpdatePrinter(ctx, p.ID, "Pizza oven", "10.0.0.20"); err != nil {
		t.Fatal(err)
	}
	if len(repo.changes) != 0 {
		t.Fatalf("a rename must not log an ip change: %+v", repo.changes)
	}
	if _, err := svc.UpdatePrinter(ctx, p.ID, "Pizza oven", "10.0.0.40"); err != nil {
		t.Fatal(err)
	}
	if len(repo.changes) != 1 {
		t.Fatalf("changes = %+v, want one", repo.changes)
	}
	c := repo.changes[0]
	if c.Source != tenancy.IPChangeByManager || c.OldIP != "10.0.0.20" || c.NewIP != "10.0.0.40" || c.LocationID != p.LocationID {
		t.Fatalf("change = %+v", c)
	}
}

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"AA:BB:CC:DD:EE:FF":   "aa:bb:cc:dd:ee:ff",
		" aa-bb-cc-dd-ee-ff ": "aa:bb:cc:dd:ee:ff",
		"":                    "",
		"not-a-mac":           "",
	} {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}
