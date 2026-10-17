// Package ctl implements medctl: schema migration, bootstrap of the first administrator, seeding the
// example configuration, publishing, user management, sample file generation and status.
package ctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dxmed/internal/auth"
	"dxmed/internal/ops"
	"dxmed/internal/sample"
	"dxmed/internal/store"
)

// Env is the process environment seen by a command.
type Env struct {
	Out, Err io.Writer
	Getenv   func(string) string
	Now      func() time.Time
	Open     func(ctx context.Context) (*store.Store, error)
}

// OSEnv returns the real environment; the database comes from MED_DB_* variables.
func OSEnv() Env {
	return Env{Out: os.Stdout, Err: os.Stderr, Getenv: os.Getenv, Now: time.Now,
		Open: func(ctx context.Context) (*store.Store, error) { return store.Open(ctx, store.SettingsFromEnv().DSN()) }}
}

const usage = `usage: medctl <command> [flags]

commands:
  migrate                         apply schema migrations (run as med_owner)
  bootstrap --admin NAME          create the first administrator (password from MED_ADMIN_PASSWORD)
  seed --root DIR                 load and publish the example configuration (paths under DIR)
  publish STREAM [--note T] [--force]   publish the current drafts of a stream
  user add NAME --roles R1,R2     add a user (password from MED_USER_PASSWORD)
  gen msc|pgw --dir DIR [--files N] [--records N] [--start SEQ]   write sample CDR files
  partitions [--days N]           create daily partitions ahead
  status                          file counts per status and active releases
`

// Run executes medctl and returns the exit code.
func Run(ctx context.Context, args []string, e Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(e.Out, usage)
		return 0
	}
	cmds := map[string]func(context.Context, []string, Env) error{
		"migrate": migrate, "bootstrap": bootstrap, "seed": seed, "publish": publish, "user": user,
		"gen": gen, "partitions": partitions, "status": status,
	}
	fn, ok := cmds[args[0]]
	if !ok {
		fmt.Fprintf(e.Err, "medctl: unknown command %q\n%s", args[0], usage)
		return 2
	}
	if err := fn(ctx, args[1:], e); err != nil {
		fmt.Fprintf(e.Err, "medctl %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func flags(name string, e Env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.Err)
	return fs
}

// parse accepts flags before and after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func withStore(ctx context.Context, e Env, fn func(*store.Store) error) error {
	st, err := e.Open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}

func migrate(ctx context.Context, args []string, e Env) error {
	return withStore(ctx, e, func(st *store.Store) error {
		applied, err := st.Migrate(ctx)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Fprintln(e.Out, "schema up to date")
		}
		for _, a := range applied {
			fmt.Fprintln(e.Out, "applied", a)
		}
		n, err := st.EnsurePartitions(ctx, 7)
		if err == nil {
			fmt.Fprintf(e.Out, "partitions created: %d\n", n)
		}
		return err
	})
}

func bootstrap(ctx context.Context, args []string, e Env) error {
	fs := flags("bootstrap", e)
	admin := fs.String("admin", "admin", "administrator user name")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	pw := e.Getenv("MED_ADMIN_PASSWORD")
	if pw == "" {
		return errors.New("MED_ADMIN_PASSWORD is not set")
	}
	return withStore(ctx, e, func(st *store.Store) error {
		if _, err := st.GetUser(ctx, *admin); err == nil {
			fmt.Fprintf(e.Out, "user %s exists\n", *admin)
			return nil
		}
		h, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		if err := st.CreateUser(ctx, store.User{Username: *admin, PWHash: h, Roles: []string{auth.Admin}}); err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "created administrator %s\n", *admin)
		return st.Audit(ctx, store.AuditEntry{Actor: "medctl", Action: "user.create", Kind: "user", Name: *admin})
	})
}

func seed(ctx context.Context, args []string, e Env) error {
	fs := flags("seed", e)
	root := fs.String("root", "", "runtime root replacing /var/med in the examples")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !filepath.IsAbs(*root) {
		return errors.New("--root must be an absolute directory")
	}
	return withStore(ctx, e, func(st *store.Store) error {
		r, err := ops.Seed(ctx, st, filepath.Clean(*root), "medctl", e.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "config: %d created, %d updated, %d unchanged; releases: %s\n", r.Created, r.Updated, r.Unchanged,
			strings.Join(r.Releases, ", "))
		return nil
	})
}

func publish(ctx context.Context, args []string, e Env) error {
	fs := flags("publish", e)
	note := fs.String("note", "", "release note")
	force := fs.Bool("force", false, "publish although Template Lab cases fail")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("one stream name required")
	}
	return withStore(ctx, e, func(st *store.Store) error {
		r, err := ops.Publish(ctx, st, pos[0], *note, "medctl", *force, e.Now())
		for _, p := range r.Problems {
			fmt.Fprintf(e.Out, "%s %s: %s\n", p.Severity, p.Path, p.Message)
		}
		for name, d := range r.Gate {
			fmt.Fprintf(e.Out, "lab case %s: %s\n", name, strings.Join(d, "; "))
		}
		if err != nil {
			return err
		}
		if r.Unchanged {
			fmt.Fprintf(e.Out, "%s unchanged (release #%d active)\n", pos[0], r.Release.Number)
		} else {
			fmt.Fprintf(e.Out, "published %s release #%d (%s)\n", pos[0], r.Release.Number, r.Release.SHA256[:12])
		}
		return nil
	})
}

func user(ctx context.Context, args []string, e Env) error {
	fs := flags("user", e)
	roles := fs.String("roles", auth.Viewer, "comma separated roles")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || pos[0] != "add" {
		return errors.New("usage: user add NAME --roles R1,R2")
	}
	rs := strings.Split(*roles, ",")
	if err := auth.ValidRoles(rs); err != nil {
		return err
	}
	pw := e.Getenv("MED_USER_PASSWORD")
	if err := auth.CheckPasswordPolicy(pw); err != nil {
		return fmt.Errorf("MED_USER_PASSWORD: %w", err)
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	return withStore(ctx, e, func(st *store.Store) error {
		if err := st.CreateUser(ctx, store.User{Username: pos[1], PWHash: h, Roles: rs}); err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "created user %s (%s)\n", pos[1], strings.Join(rs, ","))
		return st.Audit(ctx, store.AuditEntry{Actor: "medctl", Action: "user.create", Kind: "user", Name: pos[1]})
	})
}

func gen(ctx context.Context, args []string, e Env) error {
	fs := flags("gen", e)
	dir := fs.String("dir", "", "target directory")
	files := fs.Int("files", 1, "number of files")
	records := fs.Int("records", 1000, "records (msc) or sessions (pgw) per file")
	start := fs.Int64("start", 1, "first file sequence number")
	dups := fs.Int("dups", 0, "duplicate records appended to each msc file")
	exch := fs.String("exchange", "", "exchange id (default MSC01 / PGW01)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (pos[0] != "msc" && pos[0] != "pgw") {
		return errors.New("kind msc or pgw required")
	}
	if *dir == "" {
		return errors.New("--dir required")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	now := e.Now().UTC().Truncate(time.Second)
	for i := 0; i < *files; i++ {
		seq := *start + int64(i)
		var name string
		var data []byte
		if pos[0] == "msc" {
			g := sample.MSCGen{Exchange: or(*exch, "MSC01"), FileSeq: seq, Records: *records, FirstSeq: (seq-1)*int64(*records) + 1,
				Start: now.Add(-time.Hour), Duplicates: *dups}
			name, data = g.Name(), g.Build()
		} else {
			// Each pgw file holds partial n of 3 for the same sessions: files 1..3 complete a batch.
			n := int((seq-1)%3) + 1
			batch := uint64((seq - 1) / 3)
			g := sample.PGWGen{Exchange: or(*exch, "PGW01"), FileSeq: seq, Sessions: *records, FirstCharging: 1000000 + batch*uint64(*records),
				From: n, To: n, Total: 3, Start: now.Add(-2 * time.Hour)}
			name, data = g.Name(), g.Build()
		}
		if err := writeAtomic(filepath.Join(*dir, name), data); err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "%s %d bytes\n", filepath.Join(*dir, name), len(data))
	}
	return nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// writeAtomic writes a hidden temporary file and renames it, so collectors never see partial files.
func writeAtomic(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func partitions(ctx context.Context, args []string, e Env) error {
	fs := flags("partitions", e)
	days := fs.Int("days", 7, "days ahead")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	return withStore(ctx, e, func(st *store.Store) error {
		n, err := st.EnsurePartitions(ctx, *days)
		if err == nil {
			fmt.Fprintf(e.Out, "partitions created: %d\n", n)
		}
		return err
	})
}

func status(ctx context.Context, args []string, e Env) error {
	return withStore(ctx, e, func(st *store.Store) error {
		d, err := st.GetDashboard(ctx)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(d.ByStatus))
		for k := range d.ByStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(e.Out, "files %-10s %d\n", k, d.ByStatus[k])
		}
		fmt.Fprintf(e.Out, "last 24h: files=%d in=%d out=%d err=%d drop=%d held=%d dup=%d\n", d.Last24h.Files, d.Last24h.In,
			d.Last24h.Out, d.Last24h.Err, d.Last24h.Drop, d.Last24h.Held, d.Last24h.Dup)
		rel, err := st.ActiveReleases(ctx)
		if err != nil {
			return err
		}
		for _, r := range rel {
			fmt.Fprintf(e.Out, "release %-14s #%d %s by %s\n", r.Stream, r.Number, r.PublishedAt.UTC().Format(time.RFC3339), r.PublishedBy)
		}
		fmt.Fprintf(e.Out, "alarms open: %d, workers: %d, pending post-actions: %d\n", d.ActiveAlarms, len(d.Workers), d.PendingPost)
		return nil
	})
}
