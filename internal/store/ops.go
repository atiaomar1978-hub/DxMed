package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LogEntry is one event-log line.
type LogEntry struct {
	TS        time.Time       `json:"ts"`
	Level     string          `json:"level"`
	Component string          `json:"component"`
	Stream    string          `json:"stream,omitempty"`
	FileID    string          `json:"file_id,omitempty"`
	Msg       string          `json:"msg"`
	Attrs     json.RawMessage `json:"attrs,omitempty"`
}

// AddLogs appends event-log lines (batched by the obs package).
func (s *Store) AddLogs(ctx context.Context, es []LogEntry) error {
	rows := make([][]any, len(es))
	for i, e := range es {
		var fid any
		if e.FileID != "" {
			fid = e.FileID
		}
		var attrs any
		if len(e.Attrs) > 0 {
			attrs = []byte(e.Attrs)
		}
		rows[i] = []any{e.TS, e.Level, e.Component, nullStr(e.Stream), fid, e.Msg, attrs}
	}
	_, err := s.db.CopyFrom(ctx, pgx.Identifier{"med", "event_log"}, []string{"ts", "level", "component", "stream_id", "file_id", "msg", "attrs"},
		pgx.CopyFromRows(rows))
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// LogQuery filters the log viewer; After returns lines newer than a timestamp (live tail).
type LogQuery struct {
	Level, Component, Stream, FileID, Q string
	From, To, After                     time.Time
	Limit                               int
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// Logs searches the event log, newest first (oldest first with After).
func (s *Store) Logs(ctx context.Context, q LogQuery) ([]LogEntry, error) {
	var w where
	if q.Level != "" {
		var lv []string
		for l, r := range levelRank {
			if r >= levelRank[q.Level] {
				lv = append(lv, l)
			}
		}
		w.add("level = ANY(?)", lv)
	}
	if q.Component != "" {
		w.add("component = ?", q.Component)
	}
	if q.Stream != "" {
		w.add("stream_id = ?", q.Stream)
	}
	if q.FileID != "" {
		w.add("file_id::text = ?", q.FileID)
	}
	if q.Q != "" {
		w.add("(msg ILIKE ? OR attrs::text ILIKE ?)", "%"+q.Q+"%", "%"+q.Q+"%")
	}
	if !q.From.IsZero() {
		w.add("ts >= ?", q.From)
	}
	if !q.To.IsZero() {
		w.add("ts < ?", q.To)
	}
	order := "DESC"
	if !q.After.IsZero() {
		w.add("ts > ?", q.After)
		order = "ASC"
	}
	rows, err := s.db.Query(ctx, `SELECT ts, level, component, COALESCE(stream_id, ''), COALESCE(file_id::text, ''), msg, attrs
		FROM med.event_log`+w.sql()+fmt.Sprintf(" ORDER BY ts %s LIMIT %d", order, limit(q.Limit, 500, 5000)), w.args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (LogEntry, error) {
		var e LogEntry
		var attrs []byte
		err := r.Scan(&e.TS, &e.Level, &e.Component, &e.Stream, &e.FileID, &e.Msg, &attrs)
		e.Attrs = attrs
		return e, err
	})
	if out == nil {
		out = []LogEntry{}
	}
	return out, err
}

// AuditEntry is one audited change.
type AuditEntry struct {
	ID     string          `json:"id"`
	TS     time.Time       `json:"ts"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Kind   string          `json:"object_kind"`
	Name   string          `json:"object_name"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
	IP     string          `json:"ip"`
}

// Audit records a change.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	var before, after any
	if len(e.Before) > 0 {
		before = []byte(e.Before)
	}
	if len(e.After) > 0 {
		after = []byte(e.After)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO med.audit_log (ts, actor, action, object_kind, object_name, before, after, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, s.now(), e.Actor, e.Action, e.Kind, e.Name, before, after, e.IP)
	return err
}

// AuditQuery filters the audit trail.
type AuditQuery struct {
	Actor, Action, Kind, Name string
	From, To                  time.Time
	Limit                     int
}

// AuditLog lists audited changes newest first.
func (s *Store) AuditLog(ctx context.Context, q AuditQuery) ([]AuditEntry, error) {
	var w where
	if q.Actor != "" {
		w.add("actor = ?", q.Actor)
	}
	if q.Action != "" {
		w.add("action = ?", q.Action)
	}
	if q.Kind != "" {
		w.add("object_kind = ?", q.Kind)
	}
	if q.Name != "" {
		w.add("object_name = ?", q.Name)
	}
	if !q.From.IsZero() {
		w.add("ts >= ?", q.From)
	}
	if !q.To.IsZero() {
		w.add("ts < ?", q.To)
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, ts, actor, action, object_kind, object_name, before, after, ip FROM med.audit_log`+
		w.sql()+fmt.Sprintf(" ORDER BY ts DESC, id DESC LIMIT %d", limit(q.Limit, 200, 2000)), w.args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		var b, a []byte
		err := r.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.Kind, &e.Name, &b, &a, &e.IP)
		e.Before, e.After = b, a
		return e, err
	})
	if out == nil {
		out = []AuditEntry{}
	}
	return out, err
}

// Alarm is a raised condition.
type Alarm struct {
	ID        string     `json:"id"`
	Rule      string     `json:"rule"`
	Severity  string     `json:"severity"`
	Stream    string     `json:"stream"`
	Message   string     `json:"message"`
	RaisedAt  time.Time  `json:"raised_at"`
	AckedBy   string     `json:"acked_by,omitempty"`
	AckedAt   *time.Time `json:"acked_at,omitempty"`
	ClearedAt *time.Time `json:"cleared_at,omitempty"`
}

// RaiseAlarm opens an alarm unless the same rule is already open for the stream; it reports whether it is new.
func (s *Store) RaiseAlarm(ctx context.Context, rule, severity, stream, msg string) (bool, error) {
	var fresh bool
	err := s.db.QueryRow(ctx, `INSERT INTO med.alarm (rule, severity, stream_id, message, raised_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (rule, stream_id) WHERE cleared_at IS NULL DO UPDATE SET message = $4, severity = $2
		RETURNING xmax = 0`, rule, severity, stream, msg, s.now()).Scan(&fresh)
	return fresh, err
}

// ClearAlarm closes an open alarm.
func (s *Store) ClearAlarm(ctx context.Context, rule, stream string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE med.alarm SET cleared_at = $3 WHERE rule = $1 AND stream_id = $2 AND cleared_at IS NULL`,
		rule, stream, s.now())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// AckAlarm acknowledges an alarm.
func (s *Store) AckAlarm(ctx context.Context, id, actor string) error {
	tag, err := s.db.Exec(ctx, `UPDATE med.alarm SET acked_by = $2, acked_at = $3 WHERE id::text = $1 AND acked_at IS NULL`, id, actor, s.now())
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Alarms lists alarms (open only unless all).
func (s *Store) Alarms(ctx context.Context, all bool, n int) ([]Alarm, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, rule, severity, stream_id, message, raised_at, COALESCE(acked_by, ''), acked_at, cleared_at
		FROM med.alarm WHERE $1 OR cleared_at IS NULL ORDER BY raised_at DESC LIMIT $2`, all, limit(n, 200, 2000))
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Alarm])
	if out == nil {
		out = []Alarm{}
	}
	return out, err
}

// User is an application account.
type User struct {
	Username  string     `json:"username"`
	PWHash    string     `json:"-"`
	Roles     []string   `json:"roles"`
	Disabled  bool       `json:"disabled"`
	CreatedAt time.Time  `json:"created_at"`
	LastLogin *time.Time `json:"last_login,omitempty"`
}

const userCols = `username, pw_hash, roles, disabled, created_at, last_login`

// CreateUser adds an account; ErrConflict when it exists.
func (s *Store) CreateUser(ctx context.Context, u User) error {
	_, err := s.db.Exec(ctx, `INSERT INTO med.app_user (username, pw_hash, roles, disabled, created_at) VALUES ($1, $2, $3, $4, $5)`,
		u.Username, u.PWHash, u.Roles, u.Disabled, s.now())
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// UpdateUser changes roles, disabled flag and (when pwHash is not empty) the password; disabling or a
// password change ends the user's sessions.
func (s *Store) UpdateUser(ctx context.Context, username string, roles []string, disabled bool, pwHash string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE med.app_user SET roles = $2, disabled = $3, pw_hash = COALESCE(NULLIF($4, ''), pw_hash)
			WHERE username = $1`, username, roles, disabled, pwHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if disabled || pwHash != "" {
			_, err = tx.Exec(ctx, `DELETE FROM med.session WHERE username = $1`, username)
		}
		return err
	})
}

// GetUser loads an account.
func (s *Store) GetUser(ctx context.Context, username string) (User, error) {
	var u User
	err := s.db.QueryRow(ctx, `SELECT `+userCols+` FROM med.app_user WHERE username = $1`, username).
		Scan(&u.Username, &u.PWHash, &u.Roles, &u.Disabled, &u.CreatedAt, &u.LastLogin)
	return u, notFound(err)
}

// Users lists accounts.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM med.app_user ORDER BY username`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[User])
}

// Session is a login session (the token itself is never stored, only its hash).
type Session struct {
	Username  string    `json:"username"`
	Roles     []string  `json:"roles"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateSession stores a session and records the login time.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, username string, ttl time.Duration, ip string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO med.session (token_hash, username, created_at, expires_at, ip) VALUES ($1, $2, $3, $4, $5)`,
			tokenHash, username, s.now(), s.now().Add(ttl), ip); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE med.app_user SET last_login = $2 WHERE username = $1`, username, s.now())
		return err
	})
}

// GetSession resolves a live session of an enabled user.
func (s *Store) GetSession(ctx context.Context, tokenHash []byte) (Session, error) {
	var ss Session
	err := s.db.QueryRow(ctx, `SELECT u.username, u.roles, s.expires_at FROM med.session s JOIN med.app_user u USING (username)
		WHERE s.token_hash = $1 AND s.expires_at > $2 AND NOT u.disabled`, tokenHash, s.now()).Scan(&ss.Username, &ss.Roles, &ss.ExpiresAt)
	return ss, notFound(err)
}

// DeleteSession logs out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `DELETE FROM med.session WHERE token_hash = $1`, tokenHash)
	return err
}

// Worker is a running component instance.
type Worker struct {
	ID        string          `json:"id"`
	Component string          `json:"component"`
	Host      string          `json:"host"`
	PID       int             `json:"pid"`
	Started   time.Time       `json:"started"`
	Seen      time.Time       `json:"seen"`
	Info      json.RawMessage `json:"info,omitempty"`
}

// WorkerBeat registers or refreshes a component instance.
func (s *Store) WorkerBeat(ctx context.Context, w Worker) error {
	var info any
	if len(w.Info) > 0 {
		info = []byte(w.Info)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO med.worker (id, component, host, pid, started, seen, info) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET seen = $6, info = $7`, w.ID, w.Component, w.Host, w.PID, w.Started, s.now(), info)
	return err
}

// Workers lists instances seen within the window.
func (s *Store) Workers(ctx context.Context, within time.Duration) ([]Worker, error) {
	rows, err := s.db.Query(ctx, `SELECT id, component, host, pid, started, seen, info FROM med.worker WHERE seen >= $1 ORDER BY component, id`,
		s.now().Add(-within))
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Worker, error) {
		var w Worker
		var info []byte
		err := r.Scan(&w.ID, &w.Component, &w.Host, &w.PID, &w.Started, &w.Seen, &info)
		w.Info = info
		return w, err
	})
	if out == nil {
		out = []Worker{}
	}
	return out, err
}

// TryLeader acquires or renews a named leadership lease; it reports whether holder is the leader.
func (s *Store) TryLeader(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	var got string
	err := s.db.QueryRow(ctx, `INSERT INTO med.leader (name, holder, until) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET holder = $2, until = $3 WHERE med.leader.holder = $2 OR med.leader.until < $4
		RETURNING holder`, name, holder, s.now().Add(ttl), s.now()).Scan(&got)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil && got == holder, err
}

// Settings is the key/value settings table.
func (s *Store) Settings(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.db.Query(ctx, `SELECT key, value FROM med.setting ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// PutSetting stores one setting.
func (s *Store) PutSetting(ctx context.Context, key string, value json.RawMessage, actor string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO med.setting (key, value, updated_by, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_by = $3, updated_at = $4`, key, []byte(value), actor, s.now())
	return err
}

// EnsurePartitions creates daily partitions for today-1 .. today+daysAhead.
func (s *Store) EnsurePartitions(ctx context.Context, daysAhead int) (int, error) {
	t := day(s.now())
	var n int
	err := s.db.QueryRow(ctx, `SELECT med.ensure_partitions($1::date, $2::date)`, t.AddDate(0, 0, -1), t.AddDate(0, 0, daysAhead)).Scan(&n)
	return n, err
}

// Retention drops whole daily partitions older than the retention of each table and purges rows in the
// default partitions; it returns the number of partitions dropped per table.
func (s *Store) Retention(ctx context.Context, keep map[string]time.Duration) (map[string]int, error) {
	out := map[string]int{}
	col := map[string]string{"file_stat": "day", "record_key": "day", "error_record": "day", "event_log": "ts"}
	for _, t := range []string{"file_stat", "record_key", "error_record", "event_log"} {
		d, ok := keep[t]
		if !ok {
			continue
		}
		cut := day(s.now().Add(-d))
		var n int
		if err := s.db.QueryRow(ctx, `SELECT med.drop_partitions($1, $2::date)`, t, cut).Scan(&n); err != nil {
			return out, err
		}
		if _, err := s.db.Exec(ctx, fmt.Sprintf(`DELETE FROM med.%s_default WHERE %s < $1`, t, col[t]), cut); err != nil {
			return out, err
		}
		out[t] = n
	}
	if d, ok := keep["session"]; ok {
		if _, err := s.db.Exec(ctx, `DELETE FROM med.session WHERE expires_at < $1`, s.now().Add(-d)); err != nil {
			return out, err
		}
	}
	return out, nil
}

// PostJob is a queued post-action step.
type PostJob struct {
	ID       string          `json:"id"`
	OutputID string          `json:"output_id"`
	Path     string          `json:"path"`
	Step     int             `json:"step"`
	Op       json.RawMessage `json:"op"`
	Attempts int             `json:"attempts"`
}

// LeasePostJob takes the next runnable step whose output is visible and whose previous steps are done.
func (s *Store) LeasePostJob(ctx context.Context, ttl time.Duration) (*PostJob, error) {
	var j PostJob
	err := s.db.QueryRow(ctx, `UPDATE med.post_action_job j SET status = 'RUNNING', attempts = attempts + 1, lease_until = $2
		FROM med.output_file o WHERE o.id = j.output_file_id AND j.id = (
			SELECT j2.id FROM med.post_action_job j2 JOIN med.output_file o2 ON o2.id = j2.output_file_id
			WHERE o2.visible AND (j2.status IN ('PENDING', 'FAILED') OR (j2.status = 'RUNNING' AND j2.lease_until < $1))
			AND j2.not_before <= $1
			AND NOT EXISTS (SELECT 1 FROM med.post_action_job p WHERE p.output_file_id = j2.output_file_id AND p.step < j2.step AND p.status <> 'DONE')
			ORDER BY j2.not_before FOR UPDATE OF j2 SKIP LOCKED LIMIT 1)
		RETURNING j.id::text, o.id::text, o.path, j.step, j.op, j.attempts`, s.now(), s.now().Add(ttl)).
		Scan(&j.ID, &j.OutputID, &j.Path, &j.Step, &j.Op, &j.Attempts)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// FinishPostJob records the outcome of a step; newPath updates the output path after move.
func (s *Store) FinishPostJob(ctx context.Context, j *PostJob, runErr error, backoff time.Duration, maxAttempts int, newPath string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if runErr == nil {
			if _, err := tx.Exec(ctx, `UPDATE med.post_action_job SET status = 'DONE', last_error = NULL, lease_until = NULL WHERE id::text = $1`, j.ID); err != nil {
				return err
			}
			if newPath != "" && newPath != j.Path {
				_, err := tx.Exec(ctx, `UPDATE med.output_file SET path = $2 WHERE id::text = $1`, j.OutputID, newPath)
				return err
			}
			return nil
		}
		status := "FAILED"
		if j.Attempts >= maxAttempts {
			status = "DEAD"
		}
		_, err := tx.Exec(ctx, `UPDATE med.post_action_job SET status = $2, last_error = $3, not_before = $4, lease_until = NULL WHERE id::text = $1`,
			j.ID, status, runErr.Error(), s.now().Add(backoff))
		return err
	})
}

// LabCase is a saved Template Lab test case (publish gate).
type LabCase struct {
	Stream    string          `json:"stream"`
	Name      string          `json:"name"`
	FileName  string          `json:"file_name"`
	Sample    []byte          `json:"sample,omitempty"`
	Expected  json.RawMessage `json:"expected"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// PutLabCase stores a test case.
func (s *Store) PutLabCase(ctx context.Context, c LabCase) error {
	_, err := s.db.Exec(ctx, `INSERT INTO med.lab_case (stream, name, sample, file_name, expected, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (stream, name) DO UPDATE SET sample = $3, file_name = $4, expected = $5,
		updated_by = $6, updated_at = $7`, c.Stream, c.Name, c.Sample, c.FileName, []byte(c.Expected), c.UpdatedBy, s.now())
	return err
}

// LabCases lists the test cases of a stream with their samples.
func (s *Store) LabCases(ctx context.Context, stream string) ([]LabCase, error) {
	rows, err := s.db.Query(ctx, `SELECT stream, name, file_name, sample, expected, updated_by, updated_at FROM med.lab_case
		WHERE stream = $1 ORDER BY name`, stream)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (LabCase, error) {
		var c LabCase
		var exp []byte
		err := r.Scan(&c.Stream, &c.Name, &c.FileName, &c.Sample, &exp, &c.UpdatedBy, &c.UpdatedAt)
		c.Expected = exp
		return c, err
	})
	if out == nil {
		out = []LabCase{}
	}
	return out, err
}

// DeleteLabCase removes a test case.
func (s *Store) DeleteLabCase(ctx context.Context, stream, name string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM med.lab_case WHERE stream = $1 AND name = $2`, stream, name)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
