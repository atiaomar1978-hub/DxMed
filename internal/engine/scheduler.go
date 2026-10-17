package engine

import (
	"context"
	"fmt"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/merge"
	"dxmed/internal/pipeline"
	"dxmed/internal/store"
)

// DefaultRetention per table (LLD §10).
var DefaultRetention = map[string]time.Duration{
	"file_stat":    400 * 24 * time.Hour,
	"record_key":   35 * 24 * time.Hour,
	"error_record": 30 * 24 * time.Hour,
	"event_log":    30 * 24 * time.Hour,
	"session":      24 * time.Hour,
}

// Scheduler runs periodic housekeeping on the elected leader.
type Scheduler struct {
	E          *Engine
	Holder     string
	Every      time.Duration
	Retention  map[string]time.Duration
	QueueAlarm int64 // files waiting per stream that raise an alarm (0: 1000)

	lastPartitions time.Time
	lastRetention  time.Time
}

// TickResult reports what one tick did.
type TickResult struct {
	Leader  bool
	Reset   int
	Retried int64
	Posted  int64
	Expired int
}

// Tick runs one round of housekeeping if this instance is the leader.
func (s *Scheduler) Tick(ctx context.Context) (TickResult, error) {
	var r TickResult
	st, now := s.E.st, s.E.now()
	ok, err := st.TryLeader(ctx, "scheduler", s.Holder, 3*s.Every)
	if err != nil || !ok {
		return r, err
	}
	r.Leader = true
	ids, err := st.ResetExpiredLeases(ctx)
	if err != nil {
		return r, err
	}
	r.Reset = len(ids)
	if r.Retried, err = st.RetryFailed(ctx); err != nil {
		return r, err
	}
	if r.Posted, err = st.MarkPosted(ctx); err != nil {
		return r, err
	}
	if now.Sub(s.lastPartitions) >= time.Hour {
		if _, err := st.EnsurePartitions(ctx, 7); err != nil {
			return r, err
		}
		s.lastPartitions = now
	}
	if now.Sub(s.lastRetention) >= 24*time.Hour {
		keep := s.Retention
		if keep == nil {
			keep = DefaultRetention
		}
		if _, err := st.Retention(ctx, keep); err != nil {
			return r, err
		}
		s.lastRetention = now
	}
	if r.Expired, err = s.expire(ctx); err != nil {
		return r, err
	}
	return r, s.alarms(ctx)
}

// expire flushes pending groups that reached their TTL, close time or partial limit, per merge stream.
func (s *Scheduler) expire(ctx context.Context) (int, error) {
	e := s.E
	rels, _, err := e.active(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, rel := range rels {
		p, err := e.plan(rel)
		if err != nil || p.Merger == nil {
			continue
		}
		n, err := s.flushStream(ctx, rel, p, func(g *merge.Group) bool { return p.Merger.Expired(g, e.now()) })
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// flushStream collapses the pending groups selected by pick through stage UNCOL and commits the outputs.
func (s *Scheduler) flushStream(ctx context.Context, rel store.Release, p *config.Plan, pick func(*merge.Group) bool) (int, error) {
	return FlushGroups(ctx, s.E, rel, p, nil, pick)
}

// FlushGroups expires pending groups of a stream: the given groups, or the pending ones selected by pick.
// It is used by the scheduler and by the operator "close partials" action.
func FlushGroups(ctx context.Context, e *Engine, rel store.Release, p *config.Plan, groups []*merge.Group, pick func(*merge.Group) bool) (int, error) {
	stream := p.Bundle.Stream.Name
	unlock, err := e.st.LockStream(ctx, stream)
	if err != nil {
		return 0, err
	}
	defer unlock()
	if groups == nil {
		pending, err := e.st.PendingGroups(ctx, stream, 5000)
		if err != nil {
			return 0, err
		}
		for _, g := range pending {
			if pick(g) {
				groups = append(groups, g)
			}
		}
	}
	if len(groups) == 0 {
		return 0, nil
	}
	now := e.now()
	res, err := pipeline.Flush(p, groups, now)
	if err != nil {
		return 0, err
	}
	if _, err := e.commit(ctx, p, rel, "", "scheduler", "flush", "", res, now); err != nil {
		return 0, err
	}
	e.log.Info("partials flushed", "stream", stream, "groups", len(groups), "outputs", len(res.Outputs))
	return len(groups), nil
}

func (s *Scheduler) alarms(ctx context.Context) error {
	st := s.E.st
	d, err := st.GetDashboard(ctx)
	if err != nil {
		return err
	}
	limit := s.QueueAlarm
	if limit <= 0 {
		limit = 1000
	}
	rels, _, err := s.E.active(ctx)
	if err != nil {
		return err
	}
	for name := range rels {
		q := d.Queue[name]
		s.E.m.Queue.Set(float64(q), name)
		s.E.m.Partials.Set(float64(d.Partials[name]), name)
		if err := s.toggle(ctx, q >= limit, "queue_depth", "major", name, fmt.Sprintf("%d files waiting", q)); err != nil {
			return err
		}
	}
	dead := d.ByStatus[store.StatusDead]
	if err := s.toggle(ctx, dead > 0, "dead_files", "major", "", fmt.Sprintf("%d files need attention (status DEAD)", dead)); err != nil {
		return err
	}
	open, err := st.Alarms(ctx, false, 0)
	if err != nil {
		return err
	}
	s.E.m.Alarms.Set(float64(len(open)))
	return nil
}

func (s *Scheduler) toggle(ctx context.Context, on bool, rule, severity, stream, msg string) error {
	if on {
		fresh, err := s.E.st.RaiseAlarm(ctx, rule, severity, stream, msg)
		if fresh {
			s.E.log.Warn("alarm raised", "rule", rule, "stream", stream, "msg", msg)
		}
		return err
	}
	cleared, err := s.E.st.ClearAlarm(ctx, rule, stream)
	if cleared {
		s.E.log.Info("alarm cleared", "rule", rule, "stream", stream)
	}
	return err
}

// Plan returns the compiled plan of a release (used by the API for manual flushes).
func (e *Engine) Plan(r store.Release) (*config.Plan, error) { return e.plan(r) }
