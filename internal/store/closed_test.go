package store_test

import (
	"testing"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/dbtest"
	"dxmed/internal/dedupe"
	"dxmed/internal/pipeline"
	"dxmed/internal/store"
)

// Every operation on a closed pool reports an error instead of panicking or returning partial data.
func TestClosedPool(t *testing.T) {
	st := dbtest.Open(t)
	st.Close()
	f := &store.File{ID: "x"}
	calls := map[string]func() error{
		"PutConfig":    func() error { _, err := st.PutConfig(ctx, "route", "R", []byte(`{}`), "a", 0); return err },
		"DeleteConfig": func() error { return st.DeleteConfig(ctx, "route", "R", "a", 0) },
		"ListConfig":   func() error { _, err := st.ListConfig(ctx, ""); return err },
		"Publish":      func() error { _, err := st.Publish(ctx, config.Bundle{}, "", "a"); return err },
		"Activate":     func() error { _, err := st.Activate(ctx, "x"); return err },
		"Releases":     func() error { _, err := st.Releases(ctx, ""); return err },
		"Register":     func() error { _, _, err := st.RegisterFile(ctx, store.NewFile{}, "a"); return err },
		"Lease":        func() error { _, err := st.Lease(ctx, "w", nil, time.Second); return err },
		"Heartbeat":    func() error { return st.Heartbeat(ctx, "x", "w", time.Second) },
		"Fail":         func() error { _, err := st.Fail(ctx, f, "w", "c", "m", 0, 1); return err },
		"RetryFailed":  func() error { _, err := st.RetryFailed(ctx); return err },
		"ResetLeases":  func() error { _, err := st.ResetExpiredLeases(ctx); return err },
		"FileAction":   func() error { _, err := st.FileAction(ctx, "x", "hold", "a"); return err },
		"Redrive":      func() error { _, err := st.Redrive(ctx, "", "a"); return err },
		"PriorFiles":   func() error { _, err := st.PriorFiles(ctx, "S", dedupe.FileInfo{}); return err },
		"KnownKeys": func() error {
			_, err := st.State(ctx, time.Hour).KnownKeys("S", []dedupe.Key{{}})
			return err
		},
		"LoadGroups": func() error {
			_, err := st.State(ctx, time.Hour).LoadGroups("S", []dedupe.Key{{}})
			return err
		},
		"PendingGroups": func() error { _, err := st.PendingGroups(ctx, "S", 1); return err },
		"GroupsByKey":   func() error { _, err := st.GroupsByKey(ctx, "S", nil); return err },
		"LockStream":    func() error { _, err := st.LockStream(ctx, "S"); return err },
		"Commit":        func() error { _, err := st.Commit(ctx, store.Commit{Result: &pipeline.Result{}}); return err },
		"MarkVisible":   func() error { return st.MarkVisible(ctx, nil) },
		"Invisible":     func() error { _, err := st.InvisibleOutputs(ctx, 1); return err },
		"MarkPosted":    func() error { _, err := st.MarkPosted(ctx); return err },
		"Files":         func() error { _, _, err := st.Files(ctx, store.FileQuery{}); return err },
		"FileDetail":    func() error { _, err := st.GetFileDetail(ctx, "x"); return err },
		"Outputs":       func() error { _, err := st.Outputs(ctx, store.OutputQuery{}); return err },
		"Errors":        func() error { _, err := st.Errors(ctx, store.ErrorQuery{}); return err },
		"Partials":      func() error { _, err := st.Partials(ctx, "", 1); return err },
		"Volume":        func() error { _, err := st.Volume(ctx, store.ReportQuery{}); return err },
		"Routes":        func() error { _, err := st.Routes(ctx, store.ReportQuery{}); return err },
		"Reasons":       func() error { _, err := st.Reasons(ctx, store.ReportQuery{}); return err },
		"Dashboard":     func() error { _, err := st.GetDashboard(ctx); return err },
		"AddLogs":       func() error { return st.AddLogs(ctx, []store.LogEntry{{}}) },
		"Logs":          func() error { _, err := st.Logs(ctx, store.LogQuery{}); return err },
		"Audit":         func() error { return st.Audit(ctx, store.AuditEntry{}) },
		"AuditLog":      func() error { _, err := st.AuditLog(ctx, store.AuditQuery{}); return err },
		"RaiseAlarm":    func() error { _, err := st.RaiseAlarm(ctx, "r", "minor", "", ""); return err },
		"ClearAlarm":    func() error { _, err := st.ClearAlarm(ctx, "r", ""); return err },
		"AckAlarm":      func() error { return st.AckAlarm(ctx, "x", "a") },
		"Alarms":        func() error { _, err := st.Alarms(ctx, true, 1); return err },
		"CreateUser":    func() error { return st.CreateUser(ctx, store.User{}) },
		"UpdateUser":    func() error { return st.UpdateUser(ctx, "u", nil, false, "") },
		"GetUser":       func() error { _, err := st.GetUser(ctx, "u"); return err },
		"Users":         func() error { _, err := st.Users(ctx); return err },
		"CreateSession": func() error { return st.CreateSession(ctx, nil, "u", time.Hour, "") },
		"GetSession":    func() error { _, err := st.GetSession(ctx, nil); return err },
		"DeleteSession": func() error { return st.DeleteSession(ctx, nil) },
		"WorkerBeat":    func() error { return st.WorkerBeat(ctx, store.Worker{}) },
		"Workers":       func() error { _, err := st.Workers(ctx, time.Hour); return err },
		"TryLeader":     func() error { _, err := st.TryLeader(ctx, "n", "h", time.Second); return err },
		"Settings":      func() error { _, err := st.Settings(ctx); return err },
		"PutSetting":    func() error { return st.PutSetting(ctx, "k", []byte("1"), "a") },
		"Partitions":    func() error { _, err := st.EnsurePartitions(ctx, 1); return err },
		"Retention": func() error {
			_, err := st.Retention(ctx, map[string]time.Duration{"event_log": time.Hour})
			return err
		},
		"RetentionSessions": func() error {
			_, err := st.Retention(ctx, map[string]time.Duration{"session": time.Hour})
			return err
		},
		"LeasePostJob":  func() error { _, err := st.LeasePostJob(ctx, time.Second); return err },
		"FinishPostJob": func() error { return st.FinishPostJob(ctx, &store.PostJob{}, nil, 0, 1, "") },
		"PutLabCase":    func() error { return st.PutLabCase(ctx, store.LabCase{}) },
		"LabCases":      func() error { _, err := st.LabCases(ctx, "S"); return err },
		"DeleteLabCase": func() error { return st.DeleteLabCase(ctx, "S", "n") },
		"Migrate":       func() error { _, err := st.Migrate(ctx); return err },
	}
	for name, fn := range calls {
		if err := fn(); err == nil {
			t.Errorf("%s: no error on closed pool", name)
		}
	}
}
