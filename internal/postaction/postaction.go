// Package postaction executes route post-actions on committed output files: copy and move to another
// directory, gzip, a .sha256 checksum sidecar and delete. Every step is idempotent so a retried step after
// a crash completes instead of failing.
package postaction

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"dxmed/internal/route"
	"dxmed/internal/store"
)

// Do runs one step on path and returns the file's path afterwards (changed by move and gzip).
func Do(pa route.PostAction, path string) (string, error) {
	switch pa.Op {
	case "copy":
		dst := filepath.Join(pa.Args["dir"], filepath.Base(path))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && exists(dst) {
			return path, nil
		}
		return path, copyFile(path, dst)
	case "move":
		dst := filepath.Join(pa.Args["dir"], filepath.Base(path))
		if !exists(path) && exists(dst) {
			return dst, nil
		}
		if err := os.MkdirAll(pa.Args["dir"], 0o755); err != nil {
			return path, err
		}
		if err := os.Rename(path, dst); err != nil {
			if err := copyFile(path, dst); err != nil {
				return path, err
			}
			if err := os.Remove(path); err != nil {
				return dst, err
			}
		}
		return dst, nil
	case "gzip":
		dst := path + ".gz"
		if !exists(path) && exists(dst) {
			return dst, nil
		}
		if err := gzipFile(path, dst); err != nil {
			return path, err
		}
		return dst, os.Remove(path)
	case "checksum":
		sum, err := sha256File(path)
		if err != nil {
			return path, err
		}
		return path, writeAtomic(path+".sha256", []byte(sum+"  "+filepath.Base(path)+"\n"))
	case "delete":
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return path, err
		}
		return path, nil
	}
	return path, fmt.Errorf("unknown post action %q", pa.Op)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeAtomic writes a hidden temporary file next to dst, fsyncs it and renames it.
func writeAtomic(dst string, data []byte) error {
	return atomicFile(dst, func(w io.Writer) error { _, err := w.Write(data); return err })
}

func atomicFile(dst string, fill func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	err = fill(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return atomicFile(dst, func(w io.Writer) error { _, err := io.Copy(w, in); return err })
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return atomicFile(dst, func(w io.Writer) error {
		zw := gzip.NewWriter(w)
		zw.Name = filepath.Base(src)
		if _, err := io.Copy(zw, in); err != nil {
			return err
		}
		return zw.Close()
	})
}

// Queue is the part of the store used by the runner.
type Queue interface {
	LeasePostJob(ctx context.Context, ttl time.Duration) (*store.PostJob, error)
	FinishPostJob(ctx context.Context, j *store.PostJob, runErr error, backoff time.Duration, maxAttempts int, newPath string) error
}

// Runner drains the post-action queue.
type Runner struct {
	Q           Queue
	Log         *slog.Logger
	MaxAttempts int
	Backoff     func(attempt int) time.Duration
	OnFailure   func(op string)
}

// Backoff doubles from 10 s up to 10 min.
func Backoff(attempt int) time.Duration {
	d := 10 * time.Second << min(attempt, 6)
	return min(d, 10*time.Minute)
}

// Drain runs jobs until the queue is empty; it returns the number of jobs run.
func (r *Runner) Drain(ctx context.Context) (int, error) {
	n := 0
	for ctx.Err() == nil {
		j, err := r.Q.LeasePostJob(ctx, 5*time.Minute)
		if err != nil {
			return n, err
		}
		if j == nil {
			return n, nil
		}
		var pa route.PostAction
		runErr := json.Unmarshal(j.Op, &pa)
		newPath := j.Path
		if runErr == nil {
			newPath, runErr = Do(pa, j.Path)
		}
		if runErr != nil {
			r.Log.Warn("post action failed", "op", pa.Op, "path", j.Path, "attempt", j.Attempts, "err", runErr)
			if r.OnFailure != nil {
				r.OnFailure(pa.Op)
			}
		}
		if err := r.Q.FinishPostJob(ctx, j, runErr, r.Backoff(j.Attempts), r.MaxAttempts, newPath); err != nil {
			return n, err
		}
		n++
	}
	return n, ctx.Err()
}
