package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// The baseline tests need two throwaway databases (both wiped):
// QUICC_TEST_PG_DSN and QUICC_TEST_PG_RESTORE_DSN, on the same server (the
// roles are the server's). `task test-migrate` starts one in Docker.

// scratch2 opens QUICC_TEST_PG_RESTORE_DSN on an empty public schema.
func scratch2(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("QUICC_TEST_PG_RESTORE_DSN")
	if dsn == "" {
		t.Skip("QUICC_TEST_PG_RESTORE_DSN not set (a second throwaway database: it is wiped)")
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
		_ = wipe()
		db.Close()
	})
	if err := wipe(); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

// fingerprint is everything about the public schema a database built two ways
// must agree on: columns (in order, types, null, defaults, generated),
// constraints, indexes, triggers, functions, table and default privileges.
func fingerprint(t *testing.T, db *sql.DB) []string {
	t.Helper()
	queries := []string{
		`SELECT 'col ' || c.relname || '.' || a.attname || ' #' || a.attnum || ' ' || format_type(a.atttypid, a.atttypmod)
		        || CASE WHEN a.attnotnull THEN ' not null' ELSE '' END
		        || coalesce(' default ' || pg_get_expr(d.adbin, d.adrelid), '') || ' gen=' || a.attgenerated::text || ' id=' || a.attidentity::text
		   FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
		   LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		  WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped`,
		`SELECT 'con ' || conrelid::regclass || ' ' || conname || ' ' || pg_get_constraintdef(oid)
		   FROM pg_constraint WHERE connamespace = 'public'::regnamespace`,
		`SELECT 'idx ' || tablename || ' ' || indexname || ' ' || indexdef FROM pg_indexes WHERE schemaname = 'public'`,
		`SELECT 'trg ' || tgname || ' ' || pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
		  WHERE c.relnamespace = 'public'::regnamespace AND NOT t.tgisinternal`,
		`SELECT 'fn ' || pg_get_functiondef(p.oid) FROM pg_proc p WHERE p.pronamespace = 'public'::regnamespace`,
		`SELECT 'acl ' || c.relname || ' ' || x.grantee::regrole || ' ' || x.privilege_type
		   FROM pg_class c, aclexplode(c.relacl) x WHERE c.relnamespace = 'public'::regnamespace`,
		`SELECT 'defacl ' || d.defaclobjtype::text || ' ' || x.grantee::regrole || ' ' || x.privilege_type
		   FROM pg_default_acl d, aclexplode(d.defaclacl) x WHERE d.defaclnamespace = 'public'::regnamespace`,
		`SELECT 'schema-acl ' || coalesce(x.grantee::regrole::text, 'PUBLIC') || ' ' || x.privilege_type
		   FROM pg_namespace n, aclexplode(n.nspacl) x WHERE n.nspname = 'public'`,
	}
	var out []string
	for _, q := range queries {
		rows, err := db.Query(q + ` ORDER BY 1`)
		if err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		rows.Close()
	}
	return out
}

func sameFingerprint(t *testing.T, wantName string, want []string, gotName string, got []string) {
	t.Helper()
	ws, gs := map[string]bool{}, map[string]bool{}
	for _, l := range want {
		ws[l] = true
	}
	for _, l := range got {
		gs[l] = true
	}
	n := 0
	for _, l := range want {
		if !gs[l] && n < 40 {
			t.Errorf("only in %s: %s", wantName, l)
			n++
		}
	}
	for _, l := range got {
		if !ws[l] && n < 40 {
			t.Errorf("only in %s: %s", gotName, l)
			n++
		}
	}
	if n == 0 && len(want) != len(got) {
		t.Errorf("%s has %d facts, %s %d", wantName, len(want), gotName, len(got))
	}
}

// withAdopt runs f with AdoptPreBaseline forced to v.
func withAdopt(t *testing.T, v bool) {
	t.Helper()
	old := AdoptPreBaseline
	AdoptPreBaseline = func() bool { return v }
	t.Cleanup(func() { AdoptPreBaseline = old })
}

// runChain applies the old chain (history/) as the old binary did.
func runChain(t *testing.T, db *sql.DB) {
	t.Helper()
	goose.SetBaseFS(history)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(context.Background(), db, "history"); err != nil {
		t.Fatalf("old chain: %v", err)
	}
}

// TestBaselineEqualsChain: the squash is faithful. A database that ran the
// old chain and was adopted (chain + hardening.sql, goose history rewritten)
// and an empty database migrated by the baseline have the same schema,
// privileges included; the adopted one then reads as "baseline applied".
func TestBaselineEqualsChain(t *testing.T) {
	chainDB, chainDSN := scratch(t)
	baseDB, baseDSN := scratch2(t)
	ctx := context.Background()
	runChain(t, chainDB)
	withAdopt(t, true)
	if err := Run(ctx, chainDSN); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := Run(ctx, baseDSN); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	sameFingerprint(t, "chain+hardening", fingerprint(t, chainDB), "baseline", fingerprint(t, baseDB))
	for _, d := range []struct {
		name string
		dsn  string
		db   *sql.DB
	}{{"adopted", chainDSN, chainDB}, {"baseline", baseDSN, baseDB}} {
		st, err := ReadStatus(ctx, d.dsn)
		if err != nil || st.Current != st.Latest || st.Current < BaselineVersion || st.Pending != 0 {
			t.Errorf("%s: status %+v %v", d.name, st, err)
		}
		name, from, err := Baseline(ctx, d.db)
		if err != nil || name != "2026-11-12" || from != 48 {
			t.Errorf("%s: baseline marker %q %d %v", d.name, name, from, err)
		}
	}
	// Run again: nothing to do on either.
	if err := Run(ctx, chainDSN); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// TestRefusesPreBaseline: outside dev an old-chain database is refused and
// left as it was (staging / prod start from a fresh database).
func TestRefusesPreBaseline(t *testing.T) {
	db, dsn := scratch(t)
	runChain(t, db)
	withAdopt(t, false)
	if err := Run(context.Background(), dsn); !errors.Is(err, ErrPreBaseline) {
		t.Fatalf("want ErrPreBaseline, got %v", err)
	}
	var marker, audit bool
	db.QueryRow(`SELECT to_regclass('public.schema_baseline') IS NOT NULL, to_regclass('public.audit_log') IS NOT NULL`).Scan(&marker, &audit)
	if marker || audit {
		t.Fatal("a refused database must not change")
	}
}

// TestSchemaFile: sql/schema.sql (sqlc's source, the readable schema) builds
// the same schema as the migrations. After a new migration, add its
// statements to schema.sql.
func TestSchemaFile(t *testing.T) {
	migDB, migDSN := scratch(t)
	fileDB, _ := scratch2(t)
	if err := Run(context.Background(), migDSN); err != nil {
		t.Fatalf("up: %v", err)
	}
	src, err := os.ReadFile("../../sql/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileDB.Exec(string(src)); err != nil {
		t.Fatalf("schema.sql: %v", err)
	}
	// goose's own table exists only where goose ran.
	drop := func(lines []string) []string {
		var out []string
		for _, l := range lines {
			if !strings.Contains(l, "goose_db_version") {
				out = append(out, l)
			}
		}
		return out
	}
	sameFingerprint(t, "migrations", drop(fingerprint(t, migDB)), "schema.sql", drop(fingerprint(t, fileDB)))
}

// mustExec runs statements, failing the test on the first error.
func mustExec(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

// denied runs q and wants it refused.
func denied(t *testing.T, conn *sql.Conn, q string) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), q); err == nil {
		t.Errorf("allowed, want refused: %s", q)
	}
}

const (
	tTenant   = "11111111-1111-1111-1111-111111111111"
	tLocation = "22222222-2222-2222-2222-222222222222"
	tOrder    = "33333333-3333-3333-3333-333333333333"
	tPayment  = "44444444-4444-4444-4444-444444444444"
)

// seedStore inserts a store, an order and its cash payment (as the caller).
func seedStore(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) {
	t.Helper()
	mustExec(t, db,
		fmt.Sprintf(`INSERT INTO tenants (id, slug, name) VALUES ('%s', 'r', 'Restore')`, tTenant),
		fmt.Sprintf(`INSERT INTO locations (id, tenant_id, slug, name, timezone) VALUES ('%s', '%s', 'main', 'Restore', 'America/New_York')`, tLocation, tTenant),
		fmt.Sprintf(`INSERT INTO orders (id, tenant_id, location_id, source, source_event_id, status, currency, subtotal_cents, tax_cents,
			tip_cents, total_cents, fees, discounts, fulfillment_type, raw_payload, placed_at, received_at, updated_at)
			VALUES ('%s', '%s', '%s', 'counter', 'k1', 'accepted', 'USD', 1000, 60, 0, 1460,
			'[{"Type":"delivery","Label":"Delivery","Amount":400,"DriverCents":300}]', '[]', 'delivery', '\x7b7d', now(), now(), now())`, tOrder, tTenant, tLocation),
		fmt.Sprintf(`INSERT INTO payments (id, order_id, processor, status, amount_cents) VALUES ('%s', '%s', 'cash', 'captured', 1460)`, tPayment, tOrder),
	)
}

// TestAuditTrail: a money change and a settings change each write exactly the
// expected audit row, with the actor main set on the connection; a change of
// noise columns writes none; secrets never reach the log; the app role can
// only add to it, and nobody can change or remove a row.
func TestAuditTrail(t *testing.T) {
	db, dsn := scratch(t)
	ctx := context.Background()
	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("up: %v", err)
	}
	seedStore(t, db)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mustExec(t, conn, `SET ROLE quicc_app`,
		`SELECT set_config('quicc.actor', '{"k":"manager","id":"`+tTenant+`","n":"owner","rid":"r-1","ip":"10.1.2.3"}', false),
		        set_config('quicc.trace', 'trace-1', false)`)
	var mark int64
	conn.QueryRowContext(ctx, `SELECT COALESCE(max(id), 0) FROM audit_log`).Scan(&mark)

	// money: a payment's amount and tip change
	mustExec(t, conn, `UPDATE payments SET amount_cents = 1500, tip_cents = 40 WHERE id = '`+tPayment+`'`)
	// settings: the terminal settings (version / updated_at alone are noise)
	mustExec(t, conn,
		`INSERT INTO terminal_settings (location_id, tenant_id, settings) VALUES ('`+tLocation+`', '`+tTenant+`', '{"a":1}')`,
		`UPDATE terminal_settings SET version = version + 1, updated_at = now()`,
		`UPDATE terminal_settings SET settings = '{"a":2}', version = version + 1`,
		`INSERT INTO staff (tenant_id, location_id, first_name, pin_hash, role) VALUES ('`+tTenant+`', '`+tLocation+`', 'Ann', 'secret-hash', 'cashier')`,
		// an order's floor step is not audited, its cancel is
		`UPDATE orders SET kitchen_order_number = 1101 WHERE id = '`+tOrder+`'`,
		`UPDATE orders SET status = 'cancelled' WHERE id = '`+tOrder+`'`,
	)

	rows, err := conn.QueryContext(ctx, `SELECT actor_kind, actor_name, source, action, entity, entity_id,
		COALESCE(before::text, ''), COALESCE(after::text, ''), trace_id, request_id, ip, location_id::text
		FROM audit_log WHERE id > $1 ORDER BY id`, mark)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var kind, name, src, action, entity, id, before, after, trace, rid, ip, loc string
		if err := rows.Scan(&kind, &name, &src, &action, &entity, &id, &before, &after, &trace, &rid, &ip, &loc); err != nil {
			t.Fatal(err)
		}
		if kind != "manager" || name != "owner" || src != "db" || trace != "trace-1" || rid != "r-1" || ip != "10.1.2.3" || loc != tLocation {
			t.Errorf("actor / store not recorded: %s %s %s %s %s %s %s", kind, name, src, trace, rid, ip, loc)
		}
		if strings.Contains(after, "secret-hash") || strings.Contains(before, "secret-hash") {
			t.Errorf("a secret reached the log: %s", after)
		}
		got = append(got, action+" "+entity+" "+before+" -> "+after)
	}
	rows.Close()
	want := []string{
		`update payments {"tip_cents": 0, "amount_cents": 1460} -> {"tip_cents": 40, "amount_cents": 1500}`,
		"insert terminal_settings",
		`update terminal_settings {"settings": {"a": 1}} -> {"settings": {"a": 2}}`,
		"insert staff",
		`update orders {"status": "accepted"} -> {"status": "cancelled"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("audit rows:\n%s\nwant %d", strings.Join(got, "\n"), len(want))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("row %d: got %s, want %s…", i, got[i], want[i])
		}
	}

	// the app role: INSERT + SELECT only on the log, rows only elsewhere, no DDL
	mustExec(t, conn, `INSERT INTO audit_log (source, action, entity) VALUES ('app', 'POST', '/store/v1/me/x')`)
	for _, q := range []string{
		`UPDATE audit_log SET actor_name = 'x'`,
		`DELETE FROM audit_log`,
		`TRUNCATE audit_log`,
		`TRUNCATE payments`,
		`DROP TABLE money_flags`,
		`ALTER TABLE payments ADD COLUMN x int`,
		`CREATE TABLE evil (id int)`,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (99, true)`,
		`UPDATE schema_baseline SET baseline = 'x'`,
	} {
		denied(t, conn, q)
	}
	mustExec(t, conn, `SELECT count(*) FROM goose_db_version`, `RESET ROLE`)
	// the owner can't change the log either (only by dropping the trigger: DDL)
	denied(t, conn, `DELETE FROM audit_log`)
	denied(t, conn, `UPDATE audit_log SET actor_name = 'x'`)
}

// TestIndexesUsed: on a few thousand rows (enough for the planner to prefer
// an index) the lookups the 2026-11-12 EXPLAIN pass indexed use their index.
// The measured before / after at 6 months of volume is in spec/main.md §8.24.
func TestIndexesUsed(t *testing.T) {
	db, dsn := scratch(t)
	ctx := context.Background()
	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("up: %v", err)
	}
	seedStore(t, db)
	mustExec(t, db,
		`INSERT INTO orders (id, tenant_id, location_id, source, source_event_id, status, currency, subtotal_cents, tax_cents,
			tip_cents, total_cents, customer_phone, fulfillment_type, raw_payload, placed_at, received_at, updated_at)
		 SELECT md5('o' || n)::uuid, '`+tTenant+`', '`+tLocation+`', 'counter', 'ev' || n, 'completed', 'USD', 1000, 60, 0, 1060,
			'410555' || lpad((n % 5000)::text, 4, '0'), 'pickup', '\x7b7d', now() - n * interval '4 minutes', now() - n * interval '4 minutes', now()
		 FROM generate_series(1, 20000) n`,
		`INSERT INTO payments (id, order_id, processor, status, amount_cents, received_at)
		 SELECT md5('p' || n)::uuid, md5('o' || n)::uuid, 'cash', 'captured', 1060, now() - n * interval '4 minutes' FROM generate_series(1, 20000) n`,
		`INSERT INTO card_holds (order_id, tenant_id, location_id, gateway, payment_ref, amount_cents, status, due_at, captured_at, payment_id)
		 SELECT md5('o' || n)::uuid, '`+tTenant+`', '`+tLocation+`', 'square', 'sq' || n, 1060, 'captured', now(), now(), md5('p' || n)::uuid
		 FROM generate_series(1, 20000, 3) n`,
		`ANALYZE`,
	)
	for _, c := range []struct{ index, query string }{
		{"idx_card_holds_payment", `SELECT order_id FROM card_holds WHERE payment_id = md5('p4')::uuid`},
		{"idx_card_holds_ref", `SELECT order_id FROM card_holds WHERE location_id = '` + tLocation + `' AND payment_ref = 'sq4'`},
		{"idx_payments_location_received", `SELECT processor, sum(amount_cents) FROM payments WHERE location_id = '` + tLocation + `' AND received_at >= now() - interval '1 day' GROUP BY processor`},
		{"idx_orders_phone", `SELECT id FROM orders WHERE location_id = '` + tLocation + `' AND customer_phone = '4105550123'`},
		{"idx_orders_source_event", `SELECT id FROM orders WHERE source_event_id = 'ev123' ORDER BY received_at DESC LIMIT 200`},
		// (another store's page: this one's rows are all of the log)
		{"idx_audit_log_location", `SELECT id FROM audit_log WHERE location_id = '99999999-9999-9999-9999-999999999999' ORDER BY id DESC LIMIT 50`},
	} {
		var plan string
		if err := db.QueryRow(`EXPLAIN (FORMAT JSON) ` + c.query).Scan(&plan); err != nil {
			t.Fatalf("%v\n%s", err, c.query)
		}
		if !strings.Contains(plan, `"`+c.index+`"`) {
			t.Errorf("%s not used by %s\n%s", c.index, c.query, plan)
		}
	}
}
