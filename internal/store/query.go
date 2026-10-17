package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"dxmed/internal/merge"
)

func sortGroups(gs []*merge.Group) {
	sort.Slice(gs, func(i, j int) bool {
		if !gs[i].FirstSeen.Equal(gs[j].FirstSeen) {
			return gs[i].FirstSeen.Before(gs[j].FirstSeen)
		}
		return gs[i].Key.String() < gs[j].Key.String()
	})
}

// FileQuery filters the file browser.
type FileQuery struct {
	Stream, Status, Q string
	From, To          time.Time
	Limit             int
	Before            string // cursor: received_at|id of the last row of the previous page
}

// where builds a filter; each ? in a condition takes the next value.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, vs ...any) {
	var b strings.Builder
	i := 0
	for _, r := range cond {
		if r == '?' {
			w.args = append(w.args, vs[i])
			i++
			fmt.Fprintf(&b, "$%d", len(w.args))
			continue
		}
		b.WriteRune(r)
	}
	w.conds = append(w.conds, b.String())
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

func limit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// Files lists files newest first with cursor pagination; next is the cursor for the following page.
func (s *Store) Files(ctx context.Context, q FileQuery) (files []File, next string, err error) {
	var w where
	if q.Stream != "" {
		w.add("stream_id = ?", q.Stream)
	}
	if q.Status != "" {
		w.add("status = ANY(?)", strings.Split(q.Status, ","))
	}
	if q.Q != "" {
		w.add("(name ILIKE ? OR id::text = ?)", "%"+q.Q+"%", q.Q)
	}
	if !q.From.IsZero() {
		w.add("received_at >= ?", q.From)
	}
	if !q.To.IsZero() {
		w.add("received_at < ?", q.To)
	}
	if q.Before != "" {
		ts, id, ok := strings.Cut(q.Before, "|")
		t, perr := time.Parse(time.RFC3339Nano, ts)
		if !ok || perr != nil {
			return nil, "", fmt.Errorf("bad cursor")
		}
		w.add("(received_at, id::text) < (?, ?)", t, id)
	}
	n := limit(q.Limit, 100, 1000)
	rows, err := s.db.Query(ctx, `SELECT `+fileCols+` FROM med.file`+w.sql()+
		fmt.Sprintf(" ORDER BY received_at DESC, id::text DESC LIMIT %d", n+1), w.args...)
	if err != nil {
		return nil, "", err
	}
	files, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (File, error) { return scanFile(r) })
	if err != nil {
		return nil, "", err
	}
	if len(files) > n {
		files = files[:n]
		last := files[n-1]
		next = last.ReceivedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
	}
	return files, next, nil
}

// GetFile returns one file.
func (s *Store) GetFile(ctx context.Context, id string) (File, error) {
	f, err := scanFile(s.db.QueryRow(ctx, `SELECT `+fileCols+` FROM med.file WHERE id::text = $1`, id))
	return f, notFound(err)
}

// FileEvent is a status transition.
type FileEvent struct {
	TS     time.Time `json:"ts"`
	Status string    `json:"status"`
	Actor  string    `json:"actor"`
	Detail string    `json:"detail"`
}

// StatRow is a statistics row of a file or report.
type StatRow struct {
	Stream   string          `json:"stream,omitempty"`
	Stage    string          `json:"stage"`
	CallType string          `json:"call_type"`
	Action   string          `json:"action"`
	Reason   string          `json:"reason"`
	Count    int64           `json:"count"`
	Duration decimal.Decimal `json:"duration"`
	Volume   decimal.Decimal `json:"volume"`
}

// Output is an output file row.
type Output struct {
	ID        string    `json:"id"`
	FileID    string    `json:"file_id,omitempty"`
	Stream    string    `json:"stream"`
	Route     string    `json:"route"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Records   int64     `json:"records"`
	Bytes     int64     `json:"bytes"`
	SHA256    string    `json:"sha256"`
	Visible   bool      `json:"visible"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrorRecord is a rejected record.
type ErrorRecord struct {
	Day    time.Time       `json:"day"`
	FileID string          `json:"file_id,omitempty"`
	Stream string          `json:"stream"`
	Seq    int64           `json:"seq"`
	Offset int64           `json:"offset"`
	Stage  string          `json:"stage"`
	Reason string          `json:"reason"`
	Detail json.RawMessage `json:"detail,omitempty"`
	RawHex string          `json:"raw_hex,omitempty"`
}

// FileDetail is everything known about one file.
type FileDetail struct {
	File    File          `json:"file"`
	Events  []FileEvent   `json:"events"`
	Stats   []StatRow     `json:"stats"`
	Outputs []Output      `json:"outputs"`
	Errors  []ErrorRecord `json:"errors"`
}

// GetFileDetail loads a file with events, statistics, outputs and the first rejected records.
func (s *Store) GetFileDetail(ctx context.Context, id string) (FileDetail, error) {
	f, err := s.GetFile(ctx, id)
	if err != nil {
		return FileDetail{}, err
	}
	d := FileDetail{File: f}
	rows, _ := s.db.Query(ctx, `SELECT ts, status, actor, detail FROM med.file_event WHERE file_id = $1::uuid ORDER BY ts, id`, f.ID)
	if d.Events, err = pgx.CollectRows(rows, pgx.RowToStructByPos[FileEvent]); err != nil {
		return d, err
	}
	if d.Stats, err = s.stats(ctx, `SELECT stream_id, stage, call_type, action, reason, sum(cnt), sum(duration_s), sum(volume)
		FROM med.file_stat WHERE file_id = $1::uuid GROUP BY 1, 2, 3, 4, 5 ORDER BY 2, 3, 4, 5`, f.ID); err != nil {
		return d, err
	}
	if d.Outputs, err = s.Outputs(ctx, OutputQuery{FileID: f.ID}); err != nil {
		return d, err
	}
	d.Errors, err = s.Errors(ctx, ErrorQuery{FileID: f.ID, Limit: 200})
	return d, err
}

func (s *Store) stats(ctx context.Context, q string, args ...any) ([]StatRow, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[StatRow])
	if out == nil {
		out = []StatRow{}
	}
	return out, err
}

// OutputQuery filters output files.
type OutputQuery struct {
	FileID, Stream, Route string
	From, To              time.Time
	Limit                 int
}

// Outputs lists output files newest first.
func (s *Store) Outputs(ctx context.Context, q OutputQuery) ([]Output, error) {
	var w where
	if q.FileID != "" {
		w.add("file_id::text = ?", q.FileID)
	}
	if q.Stream != "" {
		w.add("stream_id = ?", q.Stream)
	}
	if q.Route != "" {
		w.add("route = ?", q.Route)
	}
	if !q.From.IsZero() {
		w.add("created_at >= ?", q.From)
	}
	if !q.To.IsZero() {
		w.add("created_at < ?", q.To)
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, COALESCE(file_id::text, ''), stream_id, route, name, path, records, bytes, sha256,
		visible, created_at FROM med.output_file`+w.sql()+fmt.Sprintf(" ORDER BY created_at DESC, name LIMIT %d", limit(q.Limit, 500, 5000)), w.args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Output])
	if out == nil {
		out = []Output{}
	}
	return out, err
}

// ErrorQuery filters rejected records.
type ErrorQuery struct {
	FileID, Stream, Reason string
	From, To               time.Time
	Limit                  int
}

// Errors lists rejected records.
func (s *Store) Errors(ctx context.Context, q ErrorQuery) ([]ErrorRecord, error) {
	var w where
	if q.FileID != "" {
		w.add("file_id::text = ?", q.FileID)
	}
	if q.Stream != "" {
		w.add("stream_id = ?", q.Stream)
	}
	if q.Reason != "" {
		w.add("reason = ?", q.Reason)
	}
	if !q.From.IsZero() {
		w.add("day >= ?", day(q.From))
	}
	if !q.To.IsZero() {
		w.add("day <= ?", day(q.To))
	}
	rows, err := s.db.Query(ctx, `SELECT day, COALESCE(file_id::text, ''), stream_id, seq, offset_bytes, stage, reason, detail, raw
		FROM med.error_record`+w.sql()+fmt.Sprintf(" ORDER BY day DESC, file_id, seq LIMIT %d", limit(q.Limit, 500, 5000)), w.args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ErrorRecord, error) {
		var e ErrorRecord
		var detail, raw []byte
		err := r.Scan(&e.Day, &e.FileID, &e.Stream, &e.Seq, &e.Offset, &e.Stage, &e.Reason, &detail, &raw)
		if string(detail) != "null" {
			e.Detail = detail
		}
		e.RawHex = hex.EncodeToString(raw)
		return e, err
	})
	if out == nil {
		out = []ErrorRecord{}
	}
	return out, err
}

// Partial summarises a pending merge group.
type Partial struct {
	Key       string    `json:"key"`
	Stream    string    `json:"stream"`
	ReleaseID string    `json:"release_id"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Count     int       `json:"count"`
}

// Partials lists pending groups of a stream ("" for all).
func (s *Store) Partials(ctx context.Context, stream string, n int) ([]Partial, error) {
	rows, err := s.db.Query(ctx, `SELECT encode(key_hash, 'hex'), stream_id, release_id::text, first_seen, last_seen, cnt
		FROM med.partial_record WHERE $1 = '' OR stream_id = $1 ORDER BY first_seen LIMIT $2`, stream, limit(n, 500, 5000))
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Partial])
	if out == nil {
		out = []Partial{}
	}
	return out, err
}

// GroupsByKey loads specific pending groups (hex keys) for a manual close.
func (s *Store) GroupsByKey(ctx context.Context, stream string, hexKeys []string) ([]*merge.Group, error) {
	var ks [][]byte
	for _, h := range hexKeys {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 16 {
			return nil, fmt.Errorf("bad key %q", h)
		}
		ks = append(ks, b)
	}
	m, err := s.groups(ctx, `SELECT key_hash, first_seen, last_seen, payload FROM med.partial_record
		WHERE stream_id = $1 AND key_hash = ANY($2)`, stream, ks)
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

// ReportQuery bounds a report.
type ReportQuery struct {
	Stream   string
	From, To time.Time
}

func (q ReportQuery) bounds(now time.Time) (time.Time, time.Time) {
	from, to := q.From, q.To
	if to.IsZero() {
		to = now.Add(time.Minute) // exclusive bound that includes rows written just now
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -7)
	}
	return from, to
}

// VolumeRow is a per-day, per-stream reconciliation line.
type VolumeRow struct {
	Day    time.Time `json:"day"`
	Stream string    `json:"stream"`
	Files  int64     `json:"files"`
	In     int64     `json:"in"`
	Out    int64     `json:"out"`
	Err    int64     `json:"err"`
	Drop   int64     `json:"drop"`
	Held   int64     `json:"held"`
	Dup    int64     `json:"dup"`
}

// Volume reports processed files and records per day and stream (by commit time).
func (s *Store) Volume(ctx context.Context, q ReportQuery) ([]VolumeRow, error) {
	from, to := q.bounds(s.now())
	rows, err := s.db.Query(ctx, `SELECT date_trunc('day', committed_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', stream_id, count(*),
		sum(records_in), sum(records_out), sum(records_err), sum(records_drop), sum(records_held), sum(records_dup)
		FROM med.file WHERE committed_at >= $1 AND committed_at < $2 AND ($3 = '' OR stream_id = $3)
		GROUP BY 1, 2 ORDER BY 1, 2`, from, to, q.Stream)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[VolumeRow])
	if out == nil {
		out = []VolumeRow{}
	}
	return out, err
}

// RouteRow reports output volume per route.
type RouteRow struct {
	Day     time.Time `json:"day"`
	Stream  string    `json:"stream"`
	Route   string    `json:"route"`
	Files   int64     `json:"files"`
	Records int64     `json:"records"`
	Bytes   int64     `json:"bytes"`
}

// Routes reports output files and records per day, stream and route.
func (s *Store) Routes(ctx context.Context, q ReportQuery) ([]RouteRow, error) {
	from, to := q.bounds(s.now())
	rows, err := s.db.Query(ctx, `SELECT date_trunc('day', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', stream_id, route,
		count(*), sum(records), sum(bytes) FROM med.output_file
		WHERE created_at >= $1 AND created_at < $2 AND ($3 = '' OR stream_id = $3) GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, from, to, q.Stream)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[RouteRow])
	if out == nil {
		out = []RouteRow{}
	}
	return out, err
}

// Reasons reports record counts per stage, action and reason (Readg BIAUTO_STAT).
func (s *Store) Reasons(ctx context.Context, q ReportQuery) ([]StatRow, error) {
	from, to := q.bounds(s.now())
	return s.stats(ctx, `SELECT stream_id, stage, call_type, action, reason, sum(cnt), sum(duration_s), sum(volume)
		FROM med.file_stat WHERE day >= $1 AND day <= $2 AND ($3 = '' OR stream_id = $3)
		GROUP BY 1, 2, 3, 4, 5 ORDER BY 1, 2, 3, 4, 5`, day(from), day(to), q.Stream)
}

// Dashboard is the landing page summary.
type Dashboard struct {
	ByStatus      map[string]int64 `json:"by_status"`
	Queue         map[string]int64 `json:"queue"`
	Last24h       VolumeRow        `json:"last_24h"`
	Partials      map[string]int64 `json:"partials"`
	ActiveAlarms  int64            `json:"active_alarms"`
	Workers       []Worker         `json:"workers"`
	PendingPost   int64            `json:"pending_post_actions"`
	ActiveStreams int64            `json:"active_streams"`
}

func (s *Store) countMap(ctx context.Context, q string, args ...any) (map[string]int64, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// GetDashboard collects the summary counters.
func (s *Store) GetDashboard(ctx context.Context) (Dashboard, error) {
	var d Dashboard
	var err error
	if d.ByStatus, err = s.countMap(ctx, `SELECT status, count(*) FROM med.file GROUP BY 1`); err != nil {
		return d, err
	}
	if d.Queue, err = s.countMap(ctx, `SELECT stream_id, count(*) FROM med.file WHERE status IN ('NEW', 'LEASED') GROUP BY 1`); err != nil {
		return d, err
	}
	if d.Partials, err = s.countMap(ctx, `SELECT stream_id, count(*) FROM med.partial_record GROUP BY 1`); err != nil {
		return d, err
	}
	now := s.now()
	err = s.db.QueryRow(ctx, `SELECT count(*), COALESCE(sum(records_in), 0), COALESCE(sum(records_out), 0), COALESCE(sum(records_err), 0),
		COALESCE(sum(records_drop), 0), COALESCE(sum(records_held), 0), COALESCE(sum(records_dup), 0)
		FROM med.file WHERE committed_at >= $1`, now.Add(-24*time.Hour)).Scan(&d.Last24h.Files, &d.Last24h.In, &d.Last24h.Out,
		&d.Last24h.Err, &d.Last24h.Drop, &d.Last24h.Held, &d.Last24h.Dup)
	if err != nil {
		return d, err
	}
	d.Last24h.Day = day(now)
	if err = s.db.QueryRow(ctx, `SELECT count(*) FROM med.alarm WHERE cleared_at IS NULL`).Scan(&d.ActiveAlarms); err != nil {
		return d, err
	}
	if err = s.db.QueryRow(ctx, `SELECT count(*) FROM med.post_action_job WHERE status <> 'DONE'`).Scan(&d.PendingPost); err != nil {
		return d, err
	}
	if err = s.db.QueryRow(ctx, `SELECT count(*) FROM med.release WHERE active`).Scan(&d.ActiveStreams); err != nil {
		return d, err
	}
	d.Workers, err = s.Workers(ctx, 5*time.Minute)
	return d, err
}
