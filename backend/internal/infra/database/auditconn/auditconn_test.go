package auditconn_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"quiccpos/main/internal/infra/database/auditconn"
	"quiccpos/main/internal/migrate"
)

func TestActorJSON(t *testing.T) {
	got := auditconn.Actor{Kind: "manager", ID: "t1", IP: "10.0.0.1"}.JSON()
	if got != `{"id":"t1","ip":"10.0.0.1","k":"manager"}` {
		t.Fatal(got)
	}
}

type actorKey struct{}

// TestPoolCarriesTheActor (QUICC_TEST_PG_DSN, wiped): a write through a pool
// with Install records the request's actor; the next hand-out of the same
// connection to work without an actor records "system" (the setting is
// reset, never inherited).
func TestPoolCarriesTheActor(t *testing.T) {
	dsn := os.Getenv("QUICC_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUICC_TEST_PG_DSN not set (a throwaway database: it is wiped)")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wipe := func() { db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`) } //nolint:errcheck
	wipe()
	t.Cleanup(wipe)
	if err := migrate.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1 // one connection: the second hand-out is the same one
	auditconn.Install(cfg, func(ctx context.Context) (auditconn.Actor, bool) {
		a, ok := ctx.Value(actorKey{}).(auditconn.Actor)
		return a, ok
	})
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	as := context.WithValue(ctx, actorKey{}, auditconn.Actor{Kind: "device", ID: "dev-1", Name: "Pi", RequestID: "rq"})
	if _, err := pool.Exec(as, `INSERT INTO tenants (id, slug, name) VALUES ('11111111-1111-1111-1111-111111111111', 'a', 'A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tenants SET name = 'B'`); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT action, actor_kind, actor_id, actor_name, request_id FROM audit_log WHERE entity = 'tenants' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var a, k, id, n, rq string
		if err := rows.Scan(&a, &k, &id, &n, &rq); err != nil {
			t.Fatal(err)
		}
		got = append(got, a+" "+k+" "+id+" "+n+" "+rq)
	}
	want := []string{"insert device dev-1 Pi rq", "update system   "}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %q, want %q", got, want)
	}
}
