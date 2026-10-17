// Package store is the PostgreSQL access layer (schema med in the shared DxPS cluster): migrations,
// configuration and releases, the file queue with leasing, the single-transaction commit, pending partials,
// reports, logs, audit, users and sessions.
package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Errors returned by the store.
var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrLeaseLost = errors.New("lease lost")
	ErrRetry     = errors.New("concurrent update, retry")
)

// Store wraps a connection pool.
type Store struct {
	db  *pgxpool.Pool
	now func() time.Time
}

// Settings describe the database connection (from MED_DB_* environment variables).
type Settings struct {
	Host, Port, Name, User, Password, CA string
	MaxConns                             int
}

// SettingsFromEnv reads MED_DB_HOST, MED_DB_PORT, MED_DB_NAME, MED_DB_USER, MED_DB_PASSWORD, MED_DB_CA and
// MED_DB_MAX_CONNS.
func SettingsFromEnv() Settings {
	s := Settings{Host: env("MED_DB_HOST", "localhost"), Port: env("MED_DB_PORT", "5433"), Name: env("MED_DB_NAME", "dxps"),
		User: env("MED_DB_USER", "med_app"), Password: os.Getenv("MED_DB_PASSWORD"), CA: os.Getenv("MED_DB_CA")}
	s.MaxConns, _ = strconv.Atoi(os.Getenv("MED_DB_MAX_CONNS"))
	return s
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// DSN renders a connection URL. With a CA file the server certificate and host name are verified (TLS).
func (s Settings) DSN() string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(s.User, s.Password), Host: s.Host + ":" + s.Port, Path: "/" + s.Name}
	q := url.Values{}
	if s.CA != "" {
		q.Set("sslmode", "verify-full")
		q.Set("sslrootcert", s.CA)
	} else {
		q.Set("sslmode", "disable")
	}
	q.Set("application_name", "dxmed")
	if s.MaxConns > 0 {
		q.Set("pool_max_conns", strconv.Itoa(s.MaxConns))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Open connects and pings.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("database config: %w", err)
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("database: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.db.Close() }

// Pool exposes the pool for health checks.
func (s *Store) Pool() *pgxpool.Pool { return s.db }

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Migrate applies embedded migrations not yet recorded in med.schema_migration (run as med_owner).
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	return migrate(ctx, s.db, migrations)
}

func migrate(ctx context.Context, db *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	names, _ := fs.Glob(fsys, "migrations/*.sql")
	sort.Strings(names)
	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('med:migrate'))`); err != nil {
		return nil, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('med:migrate'))`) //nolint:errcheck
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS med.schema_migration (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
		ALTER TABLE med.schema_migration ADD COLUMN IF NOT EXISTS sha256 text`); err != nil {
		return nil, fmt.Errorf("migration table: %w", err)
	}
	var applied []string
	for _, n := range names {
		base := strings.TrimPrefix(n, "migrations/")
		sql, _ := fs.ReadFile(fsys, n)
		h := sha256.Sum256(sql)
		sum := hex.EncodeToString(h[:])
		var stored *string
		err := conn.QueryRow(ctx, `SELECT sha256 FROM med.schema_migration WHERE name = $1`, base).Scan(&stored)
		switch {
		case err == nil && stored != nil && *stored != sum:
			return applied, fmt.Errorf("migration %s was changed after it was applied; add a new migration instead", base)
		case err == nil:
			if _, err := conn.Exec(ctx, `UPDATE med.schema_migration SET sha256 = $2 WHERE name = $1 AND sha256 IS NULL`, base, sum); err != nil {
				return applied, err
			}
			continue
		case err != pgx.ErrNoRows:
			return applied, err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO med.schema_migration (name, sha256) VALUES ($1, $2)`, base, sum)
			return err
		})
		if err != nil {
			return applied, fmt.Errorf("migration %s: %w", base, err)
		}
		applied = append(applied, base)
	}
	return applied, nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
