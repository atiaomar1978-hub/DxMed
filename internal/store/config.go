package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"dxmed/internal/config"
)

// ConfigObject is one version of a configuration document.
type ConfigObject struct {
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	Doc       json.RawMessage `json:"doc,omitempty"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// PutConfig stores a new version. ifVersion is the version the caller edited (0 for a new object);
// a different current version returns ErrConflict (lost-update protection, HTTP ETag).
func (s *Store) PutConfig(ctx context.Context, kind, name string, doc []byte, actor string, ifVersion int) (int, error) {
	var v int
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var cur int
		var live bool
		err := tx.QueryRow(ctx, `SELECT version, doc IS NOT NULL FROM med.config_object WHERE kind = $1 AND name = $2
			ORDER BY version DESC LIMIT 1 FOR UPDATE`, kind, name).Scan(&cur, &live)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if ifVersion >= 0 && ((live && ifVersion != cur) || (!live && ifVersion != 0)) {
			return fmt.Errorf("%w: %s %s is at version %d", ErrConflict, kind, name, cur)
		}
		v = cur + 1
		_, err = tx.Exec(ctx, `INSERT INTO med.config_object (kind, name, version, doc, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, kind, name, v, doc, actor, s.now())
		if isUnique(err) {
			return ErrConflict
		}
		return err
	})
	return v, err
}

// DeleteConfig records a deletion as a new version without a document.
func (s *Store) DeleteConfig(ctx context.Context, kind, name, actor string, ifVersion int) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		var cur int
		var live bool
		err := tx.QueryRow(ctx, `SELECT version, doc IS NOT NULL FROM med.config_object WHERE kind = $1 AND name = $2
			ORDER BY version DESC LIMIT 1 FOR UPDATE`, kind, name).Scan(&cur, &live)
		if err != nil || !live {
			return ErrNotFound
		}
		if ifVersion >= 0 && ifVersion != cur {
			return fmt.Errorf("%w: %s %s is at version %d", ErrConflict, kind, name, cur)
		}
		_, err = tx.Exec(ctx, `INSERT INTO med.config_object (kind, name, version, doc, updated_by, updated_at)
			VALUES ($1, $2, $3, NULL, $4, $5)`, kind, name, cur+1, actor, s.now())
		return err
	})
}

const configCols = `kind, name, version, doc, updated_by, updated_at`

func scanConfig(row pgx.Row) (ConfigObject, error) {
	var o ConfigObject
	var doc []byte
	err := row.Scan(&o.Kind, &o.Name, &o.Version, &doc, &o.UpdatedBy, &o.UpdatedAt)
	o.Doc = doc
	return o, err
}

// GetConfig returns the current version of an object.
func (s *Store) GetConfig(ctx context.Context, kind, name string) (ConfigObject, error) {
	o, err := scanConfig(s.db.QueryRow(ctx, `SELECT `+configCols+` FROM med.config_current
		WHERE kind = $1 AND name = $2 AND doc IS NOT NULL`, kind, name))
	return o, notFound(err)
}

// GetConfigVersion returns one historical version.
func (s *Store) GetConfigVersion(ctx context.Context, kind, name string, version int) (ConfigObject, error) {
	o, err := scanConfig(s.db.QueryRow(ctx, `SELECT `+configCols+` FROM med.config_object
		WHERE kind = $1 AND name = $2 AND version = $3`, kind, name, version))
	return o, notFound(err)
}

func (s *Store) listConfig(ctx context.Context, q string, args ...any) ([]ConfigObject, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigObject{}
	for rows.Next() {
		o, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListConfig returns the current version of every live object of a kind ("" for all kinds).
func (s *Store) ListConfig(ctx context.Context, kind string) ([]ConfigObject, error) {
	return s.listConfig(ctx, `SELECT `+configCols+` FROM med.config_current
		WHERE doc IS NOT NULL AND ($1 = '' OR kind = $1) ORDER BY kind, name`, kind)
}

// ConfigVersions lists the history of one object, newest first (documents omitted).
func (s *Store) ConfigVersions(ctx context.Context, kind, name string) ([]ConfigObject, error) {
	return s.listConfig(ctx, `SELECT kind, name, version, CASE WHEN doc IS NULL THEN NULL ELSE '{}'::jsonb END,
		updated_by, updated_at FROM med.config_object WHERE kind = $1 AND name = $2 ORDER BY version DESC`, kind, name)
}

// Getter adapts the store to config.Assemble.
func (s *Store) Getter(ctx context.Context) config.Getter {
	return func(kind, name string) ([]byte, error) {
		o, err := s.GetConfig(ctx, kind, name)
		if err != nil {
			return nil, err
		}
		return o.Doc, nil
	}
}

// Release is a published, immutable bundle of one stream.
type Release struct {
	ID          string        `json:"id"`
	Stream      string        `json:"stream"`
	Number      int           `json:"number"`
	Bundle      config.Bundle `json:"bundle"`
	SHA256      string        `json:"sha256"`
	Note        string        `json:"note"`
	PublishedBy string        `json:"published_by"`
	PublishedAt time.Time     `json:"published_at"`
	Active      bool          `json:"active"`
}

const releaseCols = `id::text, stream, number, bundle, bundle_sha256, note, published_by, published_at, active`

func scanRelease(row pgx.Row) (Release, error) {
	var r Release
	var b []byte
	if err := row.Scan(&r.ID, &r.Stream, &r.Number, &b, &r.SHA256, &r.Note, &r.PublishedBy, &r.PublishedAt, &r.Active); err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r.Bundle)
}

// Publish stores a bundle as the next release of its stream and activates it.
func (s *Store) Publish(ctx context.Context, b config.Bundle, note, actor string) (Release, error) {
	var r Release
	body, _ := json.Marshal(b)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('med:release:' || $1))`, b.Stream.Name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE med.release SET active = false WHERE stream = $1 AND active`, b.Stream.Name); err != nil {
			return err
		}
		var err error
		r, err = scanRelease(tx.QueryRow(ctx, `INSERT INTO med.release (stream, number, bundle, bundle_sha256, note, published_by, published_at, active)
			SELECT $1, COALESCE(max(number), 0) + 1, $2, $3, $4, $5, $6, true FROM med.release WHERE stream = $1
			RETURNING `+releaseCols, b.Stream.Name, body, b.Hash(), note, actor, s.now()))
		return err
	})
	return r, err
}

// Activate makes a release the active one of its stream (rollback / roll forward).
func (s *Store) Activate(ctx context.Context, id string) (Release, error) {
	var r Release
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var stream string
		if err := tx.QueryRow(ctx, `SELECT stream FROM med.release WHERE id::text = $1`, id).Scan(&stream); err != nil {
			return notFound(err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('med:release:' || $1))`, stream); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE med.release SET active = false WHERE stream = $1 AND active`, stream); err != nil {
			return err
		}
		var err error
		r, err = scanRelease(tx.QueryRow(ctx, `UPDATE med.release SET active = true WHERE id::text = $1 RETURNING `+releaseCols, id))
		return err
	})
	return r, err
}

// GetRelease returns one release.
func (s *Store) GetRelease(ctx context.Context, id string) (Release, error) {
	r, err := scanRelease(s.db.QueryRow(ctx, `SELECT `+releaseCols+` FROM med.release WHERE id::text = $1`, id))
	return r, notFound(err)
}

func (s *Store) releases(ctx context.Context, q string, args ...any) ([]Release, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Releases lists the releases of a stream ("" for all streams), newest first.
func (s *Store) Releases(ctx context.Context, stream string) ([]Release, error) {
	return s.releases(ctx, `SELECT `+releaseCols+` FROM med.release WHERE $1 = '' OR stream = $1
		ORDER BY stream, number DESC`, stream)
}

// ActiveReleases returns the active release of every stream.
func (s *Store) ActiveReleases(ctx context.Context) ([]Release, error) {
	return s.releases(ctx, `SELECT `+releaseCols+` FROM med.release WHERE active ORDER BY stream`)
}
