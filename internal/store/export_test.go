package store

import (
	"context"
	"io/fs"
)

// MigrateFS applies migrations from fsys (tests).
func (s *Store) MigrateFS(ctx context.Context, fsys fs.FS) ([]string, error) {
	return migrate(ctx, s.db, fsys)
}
