package migrate

import (
	"context"
	"database/sql"
	"fmt"
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
// gap but the known 00036 (renumbered to 00038, see CLAUDE.md), so a parallel
// branch's duplicate or skipped number fails here before it reaches a DB that
// applies strictly in order.
func TestMigrationNumbers(t *testing.T) {
	entries, err := fs.ReadDir(migrations, "sql")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^(\d{5})_[a-z0-9_]+\.sql$`)
	seen := map[int]string{}
	var nums []int
	for _, e := range entries {
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("%s: not NNNNN_name.sql", e.Name())
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if prev, dup := seen[n]; dup {
			t.Errorf("migration %05d twice: %s and %s", n, prev, e.Name())
		}
		seen[n] = e.Name()
		nums = append(nums, n)
	}
	sort.Ints(nums)
	allowedGap := map[int]bool{36: true}
	for i := 1; i <= nums[len(nums)-1]; i++ {
		if _, ok := seen[i]; !ok && !allowedGap[i] {
			t.Errorf("migration %05d is missing (numbers must not skip)", i)
		}
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

// columns lists every public table's columns + indexes, comparable across two
// schemas.
func describe(t *testing.T, db *sql.DB, schema string) []string {
	t.Helper()
	var out []string
	rows, err := db.Query(`SELECT table_name, column_name, data_type, is_nullable, COALESCE(column_default, ''), is_generated
		FROM information_schema.columns WHERE table_schema = $1 AND table_name <> 'goose_db_version'
		ORDER BY table_name, ordinal_position`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tbl, col, typ, null, def, gen string
		if err := rows.Scan(&tbl, &col, &typ, &null, &def, &gen); err != nil {
			t.Fatal(err)
		}
		def = strings.ReplaceAll(def, schema+".", "")
		out = append(out, fmt.Sprintf("col %s.%s %s null=%s default=%s generated=%s", tbl, col, typ, null, def, gen))
	}
	rows.Close()
	rows, err = db.Query(`SELECT tablename, indexname, regexp_replace(indexdef, ' ON [a-z_0-9]+\.', ' ON ')
		FROM pg_indexes WHERE schemaname = $1 AND tablename <> 'goose_db_version' ORDER BY tablename, indexname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tbl, idx, def string
		if err := rows.Scan(&tbl, &idx, &def); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("idx %s.%s %s", tbl, idx, def))
	}
	rows.Close()
	return out
}

// TestSchemaMirror (S-5): sql/schema.sql (sqlc's source) has the same tables,
// columns (in order) and indexes as the migrations.
func TestSchemaMirror(t *testing.T) {
	db, dsn := scratch(t)
	if err := Run(context.Background(), dsn); err != nil {
		t.Fatalf("up: %v", err)
	}
	src, err := os.ReadFile("../../sql/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS mirror CASCADE; CREATE SCHEMA mirror`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA IF EXISTS mirror CASCADE`) })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `SET search_path TO mirror, public`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), string(src)); err != nil {
		t.Fatalf("schema.sql: %v", err)
	}
	want, got := describe(t, db, "public"), describe(t, db, "mirror")
	ws, gs := map[string]bool{}, map[string]bool{}
	for _, l := range want {
		ws[l] = true
	}
	for _, l := range got {
		gs[l] = true
	}
	for _, l := range want {
		if !gs[l] {
			t.Errorf("schema.sql lacks: %s", l)
		}
	}
	for _, l := range got {
		if !ws[l] {
			t.Errorf("schema.sql has, migrations don't: %s", l)
		}
	}
	if len(want) == len(got) {
		for i := range want {
			if want[i] != got[i] && ws[got[i]] && gs[want[i]] {
				t.Errorf("column order differs at %d: migrations %q, schema.sql %q", i, want[i], got[i])
				break
			}
		}
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
}
