package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"dxmed/internal/dedupe"
	"dxmed/internal/merge"
	"dxmed/internal/pipeline"
	"dxmed/internal/route"
)

// File statuses.
const (
	StatusNew       = "NEW"
	StatusLeased    = "LEASED"
	StatusCommitted = "COMMITTED"
	StatusPosted    = "POSTED"
	StatusFailed    = "FAILED"
	StatusDead      = "DEAD"
	StatusDuplicate = "DUPLICATE"
	StatusHeld      = "HELD"
	StatusCancelled = "CANCELLED"
)

// File is one input file in the queue.
type File struct {
	ID          string     `json:"id"`
	Stream      string     `json:"stream"`
	Source      string     `json:"source"`
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Size        int64      `json:"size"`
	SHA256      string     `json:"sha256"`
	Exchange    string     `json:"exchange_id"`
	Status      string     `json:"status"`
	Priority    int        `json:"priority"`
	Attempts    int        `json:"attempts"`
	NotBefore   time.Time  `json:"not_before"`
	LeaseOwner  string     `json:"lease_owner,omitempty"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	ReleaseID   string     `json:"release_id,omitempty"`
	In          int64      `json:"records_in"`
	Out         int64      `json:"records_out"`
	Err         int64      `json:"records_err"`
	Drop        int64      `json:"records_drop"`
	Held        int64      `json:"records_held"`
	Dup         int64      `json:"records_dup"`
	DupOf       string     `json:"dup_of,omitempty"`
	ErrorCode   string     `json:"error_code,omitempty"`
	Error       string     `json:"error,omitempty"`
	ReceivedAt  time.Time  `json:"received_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CommittedAt *time.Time `json:"committed_at,omitempty"`
}

const fileCols = `id::text, stream_id, source, name, path, size, sha256, COALESCE(exchange_id, ''), status, priority,
	attempts, not_before, COALESCE(lease_owner, ''), lease_until, COALESCE(release_id::text, ''),
	COALESCE(records_in, 0), COALESCE(records_out, 0), COALESCE(records_err, 0), COALESCE(records_drop, 0),
	COALESCE(records_held, 0), COALESCE(records_dup, 0), COALESCE(dup_of::text, ''), COALESCE(error_code, ''),
	COALESCE(error, ''), received_at, started_at, committed_at`

func scanFile(row pgx.Row) (File, error) {
	var f File
	err := row.Scan(&f.ID, &f.Stream, &f.Source, &f.Name, &f.Path, &f.Size, &f.SHA256, &f.Exchange, &f.Status, &f.Priority,
		&f.Attempts, &f.NotBefore, &f.LeaseOwner, &f.LeaseUntil, &f.ReleaseID, &f.In, &f.Out, &f.Err, &f.Drop, &f.Held,
		&f.Dup, &f.DupOf, &f.ErrorCode, &f.Error, &f.ReceivedAt, &f.StartedAt, &f.CommittedAt)
	return f, err
}

// NewFile is a file found by the collector.
type NewFile struct {
	Stream, Source, Name, Path, SHA256 string
	Size                               int64
	Priority                           int
}

// RegisterFile queues a file; an existing (source, name) returns created=false.
func (s *Store) RegisterFile(ctx context.Context, n NewFile, actor string) (id string, created bool, err error) {
	if n.Priority == 0 {
		n.Priority = 5
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO med.file (stream_id, source, name, path, size, sha256, priority, received_at, not_before)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8) ON CONFLICT (source, name) DO NOTHING RETURNING id::text`,
			n.Stream, n.Source, n.Name, n.Path, n.Size, n.SHA256, n.Priority, s.now()).Scan(&id)
		if err == pgx.ErrNoRows {
			return tx.QueryRow(ctx, `SELECT id::text FROM med.file WHERE source = $1 AND name = $2`, n.Source, n.Name).Scan(&id)
		}
		if err != nil {
			return err
		}
		created = true
		return event(ctx, tx, id, StatusNew, actor, "collected from "+n.Path, s.now())
	})
	return id, created, err
}

func event(ctx context.Context, tx pgx.Tx, id, status, actor, detail string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO med.file_event (file_id, ts, status, actor, detail) VALUES ($1::uuid, $2, $3, $4, $5)`,
		id, now, status, actor, detail)
	return err
}

// Lease takes the next NEW file of the given streams (FOR UPDATE SKIP LOCKED); nil when the queue is empty.
func (s *Store) Lease(ctx context.Context, owner string, streams []string, ttl time.Duration) (*File, error) {
	var f File
	err := s.tx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		var err error
		f, err = scanFile(tx.QueryRow(ctx, `UPDATE med.file f SET status = 'LEASED', lease_owner = $1, lease_until = $2,
			attempts = attempts + 1, started_at = $3
			WHERE f.id = (SELECT id FROM med.file WHERE status = 'NEW' AND stream_id = ANY($4) AND not_before <= $3
				ORDER BY priority, received_at FOR UPDATE SKIP LOCKED LIMIT 1)
			RETURNING `+fileCols, owner, now.Add(ttl), now, streams))
		if err != nil {
			return err
		}
		return event(ctx, tx, f.ID, StatusLeased, owner, fmt.Sprintf("attempt %d", f.Attempts), now)
	})
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Heartbeat extends a lease.
func (s *Store) Heartbeat(ctx context.Context, id, owner string, ttl time.Duration) error {
	tag, err := s.db.Exec(ctx, `UPDATE med.file SET lease_until = $3 WHERE id::text = $1 AND status = 'LEASED' AND lease_owner = $2`,
		id, owner, s.now().Add(ttl))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return err
}

// Release a lease with a terminal or retry outcome.
func (s *Store) finishLease(ctx context.Context, id, owner, status, code, msg string, notBefore time.Time, extra string, args ...any) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		all := append([]any{id, owner, status, code, msg, notBefore}, args...)
		tag, err := tx.Exec(ctx, `UPDATE med.file SET status = $3, error_code = NULLIF($4, ''), error = NULLIF($5, ''),
			not_before = $6, lease_owner = NULL, lease_until = NULL`+extra+`
			WHERE id::text = $1 AND status = 'LEASED' AND lease_owner = $2`, all...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		detail := msg
		if code != "" {
			detail = code + ": " + msg
		}
		return event(ctx, tx, id, status, owner, detail, s.now())
	})
}

// Fail records a processing error: back to NEW after backoff, or DEAD after maxAttempts.
func (s *Store) Fail(ctx context.Context, f *File, owner, code, msg string, backoff time.Duration, maxAttempts int) (string, error) {
	status := StatusFailed
	if f.Attempts >= maxAttempts {
		status = StatusDead
	}
	err := s.finishLease(ctx, f.ID, owner, status, code, msg, s.now().Add(backoff), "")
	return status, err
}

// MarkDuplicate rejects a whole file as a duplicate of another.
func (s *Store) MarkDuplicate(ctx context.Context, f *File, owner string, v dedupe.Verdict) error {
	return s.finishLease(ctx, f.ID, owner, StatusDuplicate, "DUP_"+v.Reason, v.Describe(), s.now(),
		", dup_of = NULLIF($7, '')::uuid", v.AgainstID)
}

// MarkFileError ends processing because a rule flagged the file (flow "error"); it is not retried.
func (s *Store) MarkFileError(ctx context.Context, f *File, owner, msg string) error {
	return s.finishLease(ctx, f.ID, owner, StatusDead, "FILE_RULE", msg, s.now(), "")
}

// RetryFailed moves FAILED files whose backoff elapsed back to NEW.
func (s *Store) RetryFailed(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE med.file SET status = 'NEW' WHERE status = 'FAILED' AND not_before <= $1`, s.now())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ResetExpiredLeases returns files with expired leases to NEW (crashed workers).
func (s *Store) ResetExpiredLeases(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE med.file SET status = 'NEW', lease_owner = NULL, lease_until = NULL
			WHERE status = 'LEASED' AND lease_until < $1 RETURNING id::text`, s.now())
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := event(ctx, tx, id, StatusNew, "scheduler", "lease expired", s.now()); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// Operator actions on files.
var fileActions = map[string]struct {
	from []string
	to   string
}{
	"reprocess": {[]string{StatusCommitted, StatusPosted, StatusDead, StatusFailed, StatusDuplicate, StatusCancelled}, StatusNew},
	"hold":      {[]string{StatusNew, StatusFailed}, StatusHeld},
	"release":   {[]string{StatusHeld}, StatusNew},
	"cancel":    {[]string{StatusNew, StatusHeld, StatusFailed, StatusDead}, StatusCancelled},
}

// FileAction applies an operator action. Reprocessing a committed file does not retract its outputs;
// record-level duplicate detection protects downstream unless the operator also purges keys.
func (s *Store) FileAction(ctx context.Context, id, action, actor string) (File, error) {
	a, ok := fileActions[action]
	if !ok {
		return File{}, fmt.Errorf("unknown action %q", action)
	}
	var f File
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		f, err = scanFile(tx.QueryRow(ctx, `UPDATE med.file SET status = $2, not_before = $4, attempts = CASE WHEN $2 = 'NEW' THEN 0 ELSE attempts END,
			error_code = NULL, error = NULL WHERE id::text = $1 AND status = ANY($3) RETURNING `+fileCols, id, a.to, a.from, s.now()))
		if err == pgx.ErrNoRows {
			var st string
			if tx.QueryRow(ctx, `SELECT status FROM med.file WHERE id::text = $1`, id).Scan(&st) != nil {
				return ErrNotFound
			}
			return fmt.Errorf("%w: cannot %s a file in status %s", ErrConflict, action, st)
		}
		if err != nil {
			return err
		}
		return event(ctx, tx, id, a.to, actor, action, s.now())
	})
	return f, err
}

// Redrive moves every DEAD file of a stream ("" for all) back to NEW.
func (s *Store) Redrive(ctx context.Context, stream, actor string) (int64, error) {
	var n int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE med.file SET status = 'NEW', attempts = 0, not_before = $2, error_code = NULL, error = NULL
			WHERE status = 'DEAD' AND ($1 = '' OR stream_id = $1) RETURNING id::text`, stream, s.now())
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := event(ctx, tx, id, StatusNew, actor, "redrive", s.now()); err != nil {
				return err
			}
		}
		n = int64(len(ids))
		return nil
	})
	return n, err
}

// PriorFiles returns committed files of the stream that may duplicate f (same name, checksum, or an
// overlapping sequence/time range on the same exchange), plus the newest range of the exchange for gap checks.
func (s *Store) PriorFiles(ctx context.Context, stream string, f dedupe.FileInfo) ([]dedupe.FileInfo, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id::text, f.source, f.name, f.sha256, COALESCE(r.exchange_id, ''),
			COALESCE(r.seq_from, 0), COALESCE(r.seq_to, 0), r.time_from, r.time_to
		FROM med.file f LEFT JOIN med.file_seq_range r ON r.file_id = f.id
		WHERE f.stream_id = $1 AND f.status IN ('COMMITTED', 'POSTED') AND f.id::text <> $2 AND (
			f.name = $3 OR f.sha256 = $4
			OR (r.exchange_id = $5 AND $7 > 0 AND r.seq_from <= $7 AND r.seq_to >= $6)
			OR (r.exchange_id = $5 AND $8::timestamptz IS NOT NULL AND r.time_from <= $9 AND r.time_to >= $8)
			OR f.id = (SELECT r2.file_id FROM med.file_seq_range r2 WHERE r2.stream_id = $1 AND r2.exchange_id = $5
				AND r2.seq_to > 0 ORDER BY r2.seq_to DESC LIMIT 1))
		LIMIT 50`, stream, f.ID, f.Name, f.SHA256, f.Exchange, f.SeqFrom, f.SeqTo, nullTime(f.TimeFrom), nullTime(f.TimeTo))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dedupe.FileInfo
	for rows.Next() {
		var p dedupe.FileInfo
		var tf, tt *time.Time
		if err := rows.Scan(&p.ID, &p.Source, &p.Name, &p.SHA256, &p.Exchange, &p.SeqFrom, &p.SeqTo, &tf, &tt); err != nil {
			return nil, err
		}
		if tf != nil {
			p.TimeFrom, p.TimeTo = *tf, *tt
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// State adapts the store to pipeline.State for one stream; keys older than retention are ignored.
func (s *Store) State(ctx context.Context, retention time.Duration) pipeline.State {
	return &state{s: s, ctx: ctx, retention: retention}
}

type state struct {
	s         *Store
	ctx       context.Context
	retention time.Duration
}

func keyBytes(ks []dedupe.Key) [][]byte {
	out := make([][]byte, len(ks))
	for i := range ks {
		out[i] = ks[i][:]
	}
	return out
}

func toKey(b []byte) dedupe.Key {
	var k dedupe.Key
	copy(k[:], b)
	return k
}

func (st *state) KnownKeys(stream string, ks []dedupe.Key) (map[dedupe.Key]bool, error) {
	rows, err := st.s.db.Query(st.ctx, `SELECT key_hash FROM med.record_key WHERE stream_id = $1 AND key_hash = ANY($2) AND day >= $3`,
		stream, keyBytes(ks), day(st.s.now().Add(-st.retention)))
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	out := make(map[dedupe.Key]bool, len(found))
	for _, b := range found {
		out[toKey(b)] = true
	}
	return out, nil
}

func (st *state) LoadGroups(stream string, ks []dedupe.Key) (map[dedupe.Key]*merge.Group, error) {
	return st.s.groups(st.ctx, `SELECT key_hash, first_seen, last_seen, payload FROM med.partial_record
		WHERE stream_id = $1 AND key_hash = ANY($2)`, stream, keyBytes(ks))
}

func (s *Store) groups(ctx context.Context, q string, args ...any) (map[dedupe.Key]*merge.Group, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[dedupe.Key]*merge.Group{}
	for rows.Next() {
		var kb, payload []byte
		g := &merge.Group{}
		if err := rows.Scan(&kb, &g.FirstSeen, &g.LastSeen, &payload); err != nil {
			return nil, err
		}
		g.Key = toKey(kb)
		if g.Parts, err = merge.Decode(payload); err != nil {
			return nil, fmt.Errorf("partials %s: %w", g.Key, err)
		}
		out[g.Key] = g
	}
	return out, rows.Err()
}

// PendingGroups returns up to limit pending groups of a stream, oldest first.
func (s *Store) PendingGroups(ctx context.Context, stream string, limit int) ([]*merge.Group, error) {
	m, err := s.groups(ctx, `SELECT key_hash, first_seen, last_seen, payload FROM med.partial_record
		WHERE stream_id = $1 ORDER BY first_seen LIMIT $2`, stream, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*merge.Group, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sortGroups(out)
	return out, nil
}

// LockStream serialises merge processing of a stream across workers and the scheduler (session advisory lock).
func (s *Store) LockStream(ctx context.Context, stream string) (func(), error) {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('med:merge:' || $1))`, stream); err != nil {
		conn.Release()
		return nil, err
	}
	return func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('med:merge:' || $1))`, stream)
		conn.Release()
	}, nil
}

// Written describes a temporary output file produced inside the commit transaction.
type Written struct {
	Name    string // final file name
	TmpPath string // fsynced temporary file in the route directory
	Bytes   int64
	SHA256  string
}

// OutputSpec is one output file of a commit. Write runs inside the transaction once the route sequence
// number is allocated (so headers and names can carry it and numbering has no gaps).
type OutputSpec struct {
	Route       string
	Dir         string
	Records     int64
	Write       func(seq int64) (Written, error)
	PostActions []route.PostAction
}

// Committed is a committed output file; the caller renames TmpPath to Path and calls MarkVisible.
type Committed struct {
	ID      string
	Route   string
	Path    string
	TmpPath string
}

// Commit is one atomic unit: a processed input file (FileID set) or a scheduler flush (FileID empty).
type Commit struct {
	FileID    string
	Owner     string
	Stream    string
	ReleaseID string
	Result    *pipeline.Result
	Outputs   []OutputSpec
}

// Commit writes everything produced by one run in a single transaction.
func (s *Store) Commit(ctx context.Context, c Commit) ([]Committed, error) {
	var out []Committed
	err := s.tx(ctx, func(tx pgx.Tx) error {
		out = out[:0]
		now := s.now()
		today := day(now)
		res := c.Result
		var fileID any
		if c.FileID != "" {
			fileID = c.FileID
			k := res.Counts
			tag, err := tx.Exec(ctx, `UPDATE med.file SET status = 'COMMITTED', lease_owner = NULL, lease_until = NULL,
				release_id = $3::uuid, exchange_id = NULLIF($4, ''), records_in = $5, records_out = $6, records_err = $7,
				records_drop = $8, records_held = $9, records_dup = $10, committed_at = $11, error_code = NULL, error = NULL
				WHERE id::text = $1 AND status = 'LEASED' AND lease_owner = $2`,
				c.FileID, c.Owner, c.ReleaseID, res.Range.Exchange, k.In, k.Out, k.Err, k.Drop, k.Held, k.Dup, now)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrLeaseLost
			}
			rg := res.Range
			if _, err := tx.Exec(ctx, `INSERT INTO med.file_seq_range (file_id, stream_id, exchange_id, seq_from, seq_to, time_from, time_to)
				VALUES ($1::uuid, $2, $3, $4, $5, $6, $7) ON CONFLICT (file_id) DO UPDATE SET exchange_id = $3, seq_from = $4,
				seq_to = $5, time_from = $6, time_to = $7`, c.FileID, c.Stream, rg.Exchange, rg.SeqFrom, rg.SeqTo,
				nullTime(rg.TimeFrom), nullTime(rg.TimeTo)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM med.output_file WHERE file_id = $1::uuid AND NOT visible`, c.FileID); err != nil {
				return err
			}
			if err := event(ctx, tx, c.FileID, StatusCommitted, c.Owner, fmt.Sprintf("in=%d out=%d err=%d drop=%d held=%d dup=%d",
				k.In, k.Out, k.Err, k.Drop, k.Held, k.Dup), now); err != nil {
				return err
			}
		}
		for _, o := range c.Outputs {
			var seq int64
			if err := tx.QueryRow(ctx, `INSERT INTO med.route_seq (stream_id, route, next_seq) VALUES ($1, $2, 2)
				ON CONFLICT (stream_id, route) DO UPDATE SET next_seq = med.route_seq.next_seq + 1 RETURNING next_seq - 1`,
				c.Stream, o.Route).Scan(&seq); err != nil {
				return err
			}
			w, err := o.Write(seq)
			if err != nil {
				return fmt.Errorf("route %s: %w", o.Route, err)
			}
			path := o.Dir + "/" + w.Name
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO med.output_file (file_id, stream_id, route, name, path, tmp_path, records, bytes, sha256, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
				fileID, c.Stream, o.Route, w.Name, path, w.TmpPath, o.Records, w.Bytes, w.SHA256, now).Scan(&id); err != nil {
				return err
			}
			for i, pa := range o.PostActions {
				op, _ := json.Marshal(pa)
				if _, err := tx.Exec(ctx, `INSERT INTO med.post_action_job (output_file_id, step, op, not_before) VALUES ($1::uuid, $2, $3, $4)`,
					id, i+1, op, now); err != nil {
					return err
				}
			}
			out = append(out, Committed{ID: id, Route: o.Route, Path: path, TmpPath: w.TmpPath})
		}
		var stats [][]any
		for _, st := range res.Stats {
			stats = append(stats, []any{today, fileID, c.Stream, st.Stage, st.CallType, st.Action, st.Reason, st.Count, st.Duration, st.Volume})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"med", "file_stat"},
			[]string{"day", "file_id", "stream_id", "stage", "call_type", "action", "reason", "cnt", "duration_s", "volume"},
			pgx.CopyFromRows(stats)); err != nil {
			return err
		}
		if c.FileID != "" {
			var keys [][]any
			for _, k := range res.NewKeys {
				keys = append(keys, []any{today, c.Stream, k[:], c.FileID})
			}
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"med", "record_key"}, []string{"day", "stream_id", "key_hash", "file_id"},
				pgx.CopyFromRows(keys)); err != nil {
				if isUnique(err) {
					return ErrRetry
				}
				return err
			}
		}
		var rejects [][]any
		for _, r := range res.Rejects {
			detail, _ := json.Marshal(r.Errors)
			rejects = append(rejects, []any{today, fileID, c.Stream, r.Seq, r.Offset, r.Stage, r.Reason, detail, r.Raw})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"med", "error_record"},
			[]string{"day", "file_id", "stream_id", "seq", "offset_bytes", "stage", "reason", "detail", "raw"},
			pgx.CopyFromRows(rejects)); err != nil {
			return err
		}
		if len(res.Deletes) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM med.partial_record WHERE stream_id = $1 AND key_hash = ANY($2)`,
				c.Stream, keyBytes(res.Deletes)); err != nil {
				return err
			}
		}
		for _, g := range res.Upserts {
			payload, err := merge.Encode(g.Parts)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO med.partial_record (stream_id, key_hash, release_id, first_seen, last_seen, cnt, payload)
				VALUES ($1, $2, $3::uuid, $4, $5, $6, $7) ON CONFLICT (stream_id, key_hash) DO UPDATE SET release_id = $3::uuid,
				last_seen = $5, cnt = $6, payload = $7`, c.Stream, g.Key[:], c.ReleaseID, g.FirstSeen, g.LastSeen, len(g.Parts), payload); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarkVisible flags outputs as renamed to their final path.
func (s *Store) MarkVisible(ctx context.Context, ids []string) error {
	_, err := s.db.Exec(ctx, `UPDATE med.output_file SET visible = true WHERE id::text = ANY($1)`, ids)
	return err
}

// InvisibleOutputs returns committed outputs whose rename has not been confirmed (crash recovery).
func (s *Store) InvisibleOutputs(ctx context.Context, limit int) ([]Committed, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, route, path, tmp_path FROM med.output_file WHERE NOT visible
		ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Committed, error) {
		var c Committed
		err := r.Scan(&c.ID, &c.Route, &c.Path, &c.TmpPath)
		return c, err
	})
}

// MarkPosted moves committed files whose post-actions are all done to POSTED.
func (s *Store) MarkPosted(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `WITH p AS (
		UPDATE med.file f SET status = 'POSTED' WHERE status = 'COMMITTED'
		AND NOT EXISTS (SELECT 1 FROM med.output_file o WHERE o.file_id = f.id AND NOT o.visible)
		AND NOT EXISTS (SELECT 1 FROM med.output_file o JOIN med.post_action_job j ON j.output_file_id = o.id
			WHERE o.file_id = f.id AND j.status <> 'DONE')
		RETURNING f.id)
		INSERT INTO med.file_event (file_id, ts, status, actor, detail)
		SELECT id, $1, 'POSTED', 'post-actions', 'outputs visible, post-actions done' FROM p`, s.now())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
