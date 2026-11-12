package migrate

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestCommandMigratesAndReports (deploy/main-deploy.sh's one-off container):
// `app migrate status` on an empty database reads version 0 with every
// migration pending, `app migrate` applies them all and the status after says
// none pending. Needs QUICC_TEST_PG_DSN like the other database tests.
func TestCommandMigratesAndReports(t *testing.T) {
	_, dsn := scratch(t)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_HOST", cfg.Host)
	t.Setenv("DB_PORT", fmt.Sprint(cfg.Port))
	t.Setenv("DB_NAME", cfg.Database)
	t.Setenv("DB_USER", "nobody-should-use-this")
	t.Setenv("DB_PASSWORD", "wrong")
	// The migrator role wins over DB_USER when set.
	t.Setenv("DB_MIGRATOR_USER", cfg.User)
	t.Setenv("DB_MIGRATOR_PASSWORD", cfg.Password)

	ctx := context.Background()
	before, err := ReadStatus(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if before.Current != 0 || before.Pending == 0 || before.Latest == 0 {
		t.Fatalf("empty database: want current 0 and pending > 0, got %+v", before)
	}
	if code := Command([]string{"status"}); code != 0 {
		t.Fatalf("migrate status exit %d", code)
	}
	if code := Command(nil); code != 0 {
		t.Fatalf("migrate exit %d", code)
	}
	after, err := ReadStatus(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pending != 0 || after.Current != before.Latest {
		t.Fatalf("after migrate: want current %d, none pending; got %+v", before.Latest, after)
	}
	if code := Command([]string{"sideways"}); code != 2 {
		t.Fatalf("unknown mode: want exit 2, got %d", code)
	}
	t.Setenv("DB_HOST", "")
	if code := Command(nil); code != 2 {
		t.Fatalf("no DB_HOST: want exit 2, got %d", code)
	}
}
