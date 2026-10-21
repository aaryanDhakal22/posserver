package migrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed sql/*.sql
var migrations embed.FS

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
