package migrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// sql/: the migrations a database runs: 00001_baseline (2026-11-12, the whole
// schema) and whatever comes after it.
//
//go:embed sql/*.sql
var migrations embed.FS

// history/: the old chain 00001-00048 the baseline was squashed from. Never
// run on a new database; only to bring an old dev / gate database to the
// chain's head before adopting it (adopt).
//
//go:embed history/*.sql
var history embed.FS

// hardening.sql: the baseline minus the chain's head (adopt applies it).
//
//go:embed hardening.sql
var hardeningSQL string

// BaselineVersion is the goose version of sql/00001_baseline.sql.
const BaselineVersion = 1

// Run applies all pending up migrations. Safe to call on every startup —
// goose tracks applied versions and skips already-applied migrations.
func Run(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := refuseV1Database(ctx, db); err != nil {
		return err
	}
	pre, err := preBaseline(ctx, db)
	if err != nil {
		return err
	}
	if pre {
		if !AdoptPreBaseline() {
			return ErrPreBaseline
		}
		if err := adopt(ctx, db); err != nil {
			return fmt.Errorf("adopt a pre-baseline database (dev: task db-wipe && task devmigrate if this keeps failing): %w", err)
		}
	}
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, "sql")
}

// ErrV1Database: the database belongs to the v1 server (old schema). v1's
// goose versions 1-4 are different migrations from v2's 00001-00004, so goose
// would skip v2's base schema and run later migrations against v1's tables.
// v2 needs its own (fresh) database — point DB_NAME at one.
var ErrV1Database = errors.New("this is a v1 database (old schema): v2 main needs a fresh database — set DB_NAME to a new one; see CUTOVER.md")

// ErrPreBaseline: the database ran the old migration chain (before the
// 2026-11-12 baseline). Its goose versions mean something else now, so goose
// would skip the baseline. Staging / prod start from a fresh database
// (deploy/main-deploy.sh --fresh); dev and the gate adopt theirs (adopt).
var ErrPreBaseline = errors.New("this database ran main's old migration chain (before the 2026-11-12 baseline): it needs a fresh database (deploy --fresh); only APP_ENV=dev adopts one")

// AdoptPreBaseline says whether Run may adopt a pre-baseline database: only
// dev (the dev stack and the gate's stacks, whose rows are throwaway).
var AdoptPreBaseline = func() bool { return os.Getenv("APP_ENV") == "dev" }

// refuseV1Database stops before migrating a v1 database: it has goose
// history but none of v2's tables (v1 has orders/items/customers, no tenants).
func refuseV1Database(ctx context.Context, db *sql.DB) error {
	var hasGoose, hasTenants, hasV1Items bool
	q := `SELECT to_regclass('public.goose_db_version') IS NOT NULL,
	             to_regclass('public.tenants') IS NOT NULL,
	             to_regclass('public.items') IS NOT NULL`
	if err := db.QueryRowContext(ctx, q).Scan(&hasGoose, &hasTenants, &hasV1Items); err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if hasGoose && !hasTenants && hasV1Items {
		return ErrV1Database
	}
	return nil
}

// preBaseline: goose has applied something but the baseline's marker table
// isn't there, so it was the old chain.
func preBaseline(ctx context.Context, db *sql.DB) (bool, error) {
	var hasGoose, hasMarker bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.goose_db_version') IS NOT NULL,
		to_regclass('public.schema_baseline') IS NOT NULL`).Scan(&hasGoose, &hasMarker); err != nil {
		return false, fmt.Errorf("inspect database: %w", err)
	}
	if !hasGoose || hasMarker {
		return false, nil
	}
	var top int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&top); err != nil {
		return false, fmt.Errorf("read goose_db_version: %w", err)
	}
	return top > 0, nil
}

// adopt brings an old-chain database to the baseline: the rest of the chain
// (history/), then hardening.sql, then goose's history rewritten to "the
// baseline is applied" — all of the last two in one transaction.
// TestBaselineEqualsChain proves the result is the baseline's schema.
func adopt(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(history)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "history"); err != nil {
		return fmt.Errorf("old chain: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, hardeningSQL); err != nil {
		return fmt.Errorf("hardening: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id > 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, BaselineVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// Baseline reads the database's baseline marker (schema_baseline): which
// baseline it was built from and the old chain's head it was squashed from.
// A restored backup is checked with it (deploy/backup).
func Baseline(ctx context.Context, db *sql.DB) (name string, squashedFrom int, err error) {
	err = db.QueryRowContext(ctx, `SELECT baseline, squashed_from FROM public.schema_baseline WHERE id = 1`).Scan(&name, &squashedFrom)
	return name, squashedFrom, err
}
