package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

// Status is where a database stands against the migrations embedded in this
// binary: Current = the highest applied version (0 = none), Latest = the
// newest embedded one, Pending = how many would run.
type Status struct {
	Current int64 `json:"current"`
	Latest  int64 `json:"latest"`
	Pending int   `json:"pending"`
}

// ReadStatus reports Status without changing anything (a fresh database
// without goose's table reads Current 0). The deploy script asks this before
// it decides whether a migration will run (deploy/main-deploy.sh).
func ReadStatus(ctx context.Context, dsn string) (Status, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return Status{}, err
	}
	defer db.Close()
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return Status{}, err
	}
	all, err := goose.CollectMigrations("sql", 0, goose.MaxVersion)
	if err != nil {
		return Status{}, fmt.Errorf("embedded migrations: %w", err)
	}
	var st Status
	if len(all) > 0 {
		st.Latest = all[len(all)-1].Version
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.goose_db_version') IS NOT NULL`).Scan(&exists); err != nil {
		return Status{}, fmt.Errorf("inspect database: %w", err)
	}
	applied := map[int64]bool{}
	if exists {
		rows, err := db.QueryContext(ctx, `SELECT version_id, is_applied FROM goose_db_version ORDER BY id`)
		if err != nil {
			return Status{}, fmt.Errorf("read goose_db_version: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var v int64
			var ok bool
			if err := rows.Scan(&v, &ok); err != nil {
				return Status{}, err
			}
			applied[v] = ok // the newest row for a version wins (goose's own rule)
		}
		if err := rows.Err(); err != nil {
			return Status{}, err
		}
	}
	for v, ok := range applied {
		if ok && v > st.Current {
			st.Current = v
		}
	}
	for _, m := range all {
		if !applied[m.Version] {
			st.Pending++
		}
	}
	return st, nil
}
