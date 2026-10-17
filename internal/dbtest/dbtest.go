// Package dbtest gives tests a freshly migrated med schema in the throw-away database dxmed_test.
// MED_TEST_OWNER_DSN (med_owner) and MED_TEST_APP_DSN (med_app) are set by scripts/gotest.sh; without them
// database tests are skipped. Test binaries hold an advisory lock so packages never share the schema.
package dbtest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"dxmed/internal/store"
)

var ownerDSN, appDSN string

// Main wraps testing.M: it locks the test database, resets the schema and runs the tests.
func Main(m *testing.M) int {
	ownerDSN, appDSN = os.Getenv("MED_TEST_OWNER_DSN"), os.Getenv("MED_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		if os.Getenv("MED_TEST_REQUIRE_DB") != "" {
			fmt.Fprintln(os.Stderr, "dbtest: MED_TEST_OWNER_DSN / MED_TEST_APP_DSN not set")
			return 1
		}
		return m.Run()
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbtest:", err)
		return 1
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('dxmed:test'))`); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest:", err)
		return 1
	}
	if err := Reset(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest:", err)
		return 1
	}
	return m.Run()
}

// Reset drops and recreates schema med, then migrates and creates partitions.
func Reset(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS med CASCADE; CREATE SCHEMA med;
		REVOKE ALL ON SCHEMA med FROM PUBLIC; GRANT USAGE ON SCHEMA med TO med_app, med_ops, med_reader`); err != nil {
		return err
	}
	st, err := store.Open(ctx, ownerDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	_, err = st.EnsurePartitions(ctx, 3)
	return err
}

// Available reports whether database tests run.
func Available() bool { return appDSN != "" }

// Open returns a store connected as med_app; the test is skipped without a database.
func Open(t testing.TB) *store.Store {
	t.Helper()
	if !Available() {
		t.Skip("no test database (run through scripts/gotest.sh)")
	}
	st, err := store.Open(context.Background(), appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// Owner returns a store connected as med_owner.
func Owner(t testing.TB) *store.Store {
	t.Helper()
	if !Available() {
		t.Skip("no test database")
	}
	st, err := store.Open(context.Background(), ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// Exec runs SQL as the owner (test fixtures).
func Exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

// Truncate empties every table of the schema (between independent tests).
func Truncate(t testing.TB) {
	t.Helper()
	Exec(t, `DO $$ DECLARE r record; BEGIN
		FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = 'med' AND tablename <> 'schema_migration'
			AND tablename NOT LIKE '%\_p________' AND tablename NOT LIKE '%\_default' LOOP
			EXECUTE format('TRUNCATE med.%I CASCADE', r.tablename);
		END LOOP; END $$`)
}

// Clock is a settable test clock.
type Clock struct{ T time.Time }

// Now returns the clock time.
func (c *Clock) Now() time.Time { return c.T }

// Add advances the clock.
func (c *Clock) Add(d time.Duration) { c.T = c.T.Add(d) }
