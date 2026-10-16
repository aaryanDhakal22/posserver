package migrate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
)

// TestRefusesV1Database needs a throwaway Postgres database:
// QUICC_TEST_PG_DSN=postgres://…/scratch go test ./internal/migrate/
// It builds a v1-shaped schema there, expects Run to refuse it, then cleans up.
func TestRefusesV1Database(t *testing.T) {
	dsn := os.Getenv("QUICC_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUICC_TEST_PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE goose_db_version (id serial PRIMARY KEY, version_id bigint NOT NULL, is_applied boolean NOT NULL, tstamp timestamp DEFAULT now())`,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true), (1, true), (2, true), (3, true), (4, true)`,
		`CREATE TABLE items (id serial PRIMARY KEY, name text)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, `DROP TABLE IF EXISTS items, goose_db_version`)
	})
	if err := Run(ctx, dsn); !errors.Is(err, ErrV1Database) {
		t.Fatalf("want ErrV1Database, got %v", err)
	}
	var tenants bool
	db.QueryRowContext(ctx, `SELECT to_regclass('public.tenants') IS NOT NULL`).Scan(&tenants)
	if tenants {
		t.Fatal("nothing may be migrated on a v1 database")
	}
}
