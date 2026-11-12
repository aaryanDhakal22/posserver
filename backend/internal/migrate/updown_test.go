package migrate

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// TestMigrationNumbers (no database): numbers are unique and in order with no
// gap, in sql/ (the baseline and what follows it) and in history/ (the old
// chain, whose known gap is 00036, renumbered to 00038), so a parallel
// branch's duplicate or skipped number fails here before it reaches a DB that
// applies strictly in order.
func TestMigrationNumbers(t *testing.T) {
	for _, c := range []struct {
		fsys fs.FS
		dir  string
		gaps map[int]bool
	}{{migrations, "sql", nil}, {history, "history", map[int]bool{36: true}}} {
		entries, err := fs.ReadDir(c.fsys, c.dir)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`^(\d{5})_[a-z0-9_]+\.sql$`)
		seen := map[int]string{}
		var nums []int
		for _, e := range entries {
			m := re.FindStringSubmatch(e.Name())
			if m == nil {
				t.Errorf("%s/%s: not NNNNN_name.sql", c.dir, e.Name())
				continue
			}
			n, _ := strconv.Atoi(m[1])
			if prev, dup := seen[n]; dup {
				t.Errorf("%s: migration %05d twice: %s and %s", c.dir, n, prev, e.Name())
			}
			seen[n] = e.Name()
			nums = append(nums, n)
		}
		sort.Ints(nums)
		if len(nums) == 0 || nums[0] != 1 {
			t.Fatalf("%s: must start at 00001", c.dir)
		}
		for i := 1; i <= nums[len(nums)-1]; i++ {
			if _, ok := seen[i]; !ok && !c.gaps[i] {
				t.Errorf("%s: migration %05d is missing (numbers must not skip)", c.dir, i)
			}
		}
	}
	if !strings.Contains(hardeningSQL, "CREATE TABLE audit_log") {
		t.Fatal("hardening.sql not embedded")
	}
}

// scratch opens QUICC_TEST_PG_DSN on an empty public schema.
func scratch(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("QUICC_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUICC_TEST_PG_DSN not set (a throwaway database: it is wiped)")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	wipe := func() error {
		_, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`)
		return err
	}
	t.Cleanup(func() {
		_ = wipe() // leave it empty for the next test (TestRefusesV1Database expects that)
		db.Close()
	})
	if err := wipe(); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

// TestUpDownUp (S-5): every migration applies, rolls back to nothing and
// applies again on a throwaway database.
func TestUpDownUp(t *testing.T) {
	db, dsn := scratch(t)
	ctx := context.Background()
	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("up: %v", err)
	}
	goose.SetBaseFS(migrations)
	if err := goose.DownToContext(ctx, db, "sql", 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// pgTool runs a PostgreSQL client tool: from PATH, or behind the
// QUICC_TEST_PG_TOOLS prefix (e.g. "docker exec -i quicc-postgres").
func pgTool(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	if p := os.Getenv("QUICC_TEST_PG_TOOLS"); p != "" {
		parts := strings.Fields(p)
		return exec.Command(parts[0], append(append(parts[1:], name), args...)...)
	}
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found (set QUICC_TEST_PG_TOOLS)", name)
	}
	return exec.Command(name, args...)
}

// TestDumpRestores: a migrated database with money rows (generated money
// columns, triggers) dumps and restores with pg_restore --exit-on-error
// (00043's functions once called each other unqualified and a restore, which
// runs with an empty search_path, failed). Needs QUICC_TEST_PG_RESTORE_DSN:
// a second throwaway database (wiped). With QUICC_TEST_PG_TOOLS the DSNs are
// used from inside that container, so give them as it reaches the server.
func TestDumpRestores(t *testing.T) {
	db, dsn := scratch(t)
	target := os.Getenv("QUICC_TEST_PG_RESTORE_DSN")
	if target == "" {
		t.Skip("QUICC_TEST_PG_RESTORE_DSN not set")
	}
	if err := Run(context.Background(), dsn); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO tenants (id, slug, name) VALUES ('11111111-1111-1111-1111-111111111111', 'r', 'Restore')`,
		`INSERT INTO locations (id, tenant_id, slug, name, timezone) VALUES ('22222222-2222-2222-2222-222222222222',
			'11111111-1111-1111-1111-111111111111', 'main', 'Restore', 'America/New_York')`,
		`INSERT INTO orders (id, tenant_id, location_id, source, source_event_id, status, currency, subtotal_cents, tax_cents,
			tip_cents, total_cents, fees, discounts, fulfillment_type, raw_payload, placed_at, received_at, updated_at)
			VALUES ('33333333-3333-3333-3333-333333333333', '11111111-1111-1111-1111-111111111111',
			'22222222-2222-2222-2222-222222222222', 'counter', 'k1', 'accepted', 'USD', 1000, 60, 0, 1460,
			'[{"Type":"delivery","Label":"Delivery","Amount":400,"DriverCents":300}]', '[]', 'delivery', '\x7b7d', now(), now(), now())`,
		`INSERT INTO payments (id, order_id, processor, status, amount_cents) VALUES
			('44444444-4444-4444-4444-444444444444', '33333333-3333-3333-3333-333333333333', 'cash', 'captured', 1460)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	var fee, driver int64
	if err := db.QueryRow(`SELECT delivery_fee_cents, driver_fee_cents FROM orders`).Scan(&fee, &driver); err != nil || fee != 400 || driver != 300 {
		t.Fatalf("generated columns: %d %d %v", fee, driver, err)
	}
	tdb, err := sql.Open("pgx", target)
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	if _, err := tdb.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	defer tdb.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`) //nolint:errcheck
	dump := pgTool(t, "pg_dump", "-Fc", "--no-owner", "-d", dsn)
	out, err := dump.Output()
	if err != nil {
		t.Fatalf("pg_dump: %v", err)
	}
	restore := pgTool(t, "pg_restore", "--exit-on-error", "--no-owner", "-d", target)
	restore.Stdin = strings.NewReader(string(out))
	if msg, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("pg_restore: %v\n%s", err, msg)
	}
	if err := tdb.QueryRow(`SELECT delivery_fee_cents, driver_fee_cents FROM orders`).Scan(&fee, &driver); err != nil || fee != 400 || driver != 300 {
		t.Fatalf("restored: %d %d %v", fee, driver, err)
	}
	// The restore is the same schema (privileges included), marked with the
	// same baseline, with the same audit trail and goose history.
	sameFingerprint(t, "source", fingerprint(t, db), "restored", fingerprint(t, tdb))
	for _, q := range []string{
		`SELECT baseline || '/' || squashed_from FROM schema_baseline`,
		`SELECT count(*)::text FROM audit_log`,
		`SELECT max(version_id)::text FROM goose_db_version WHERE is_applied`,
	} {
		var a, b string
		if err := db.QueryRow(q).Scan(&a); err != nil {
			t.Fatal(err)
		}
		if err := tdb.QueryRow(q).Scan(&b); err != nil || a != b {
			t.Errorf("%s: source %q, restored %q (%v)", q, a, b, err)
		}
	}
}
