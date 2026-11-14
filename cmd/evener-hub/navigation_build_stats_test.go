package hub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// buildRecords returns the captured navigation-build records, in order.
func buildRecords(logs *logCapture) []capturedLog {
	var out []capturedLog
	for _, rec := range logs.all() {
		if _, ok := rec.attrs["event"]; ok {
			out = append(out, rec)
		}
	}
	return out
}

func navigationStatsFixture(t *testing.T, logs *logCapture) *WebServer {
	t.Helper()
	now := time.Now()
	meta := func(dir string, age time.Duration, subagentOf string) schema.SessionMeta {
		m := schema.SessionMeta{ID: mustSessionID(t), CreatedAt: now.Add(-age - time.Hour), UpdatedAt: now.Add(-age)}
		m.EnvInfo.WorkingDir = dir
		if subagentOf != "" {
			m.IsSubagent = true
			m.ParentSessionID = subagentOf
		}
		return m
	}
	active := meta("/nonexistent/active", time.Hour, "")
	metas := []schema.SessionMeta{
		active,
		meta("/nonexistent/active", time.Hour, active.ID),
		meta("/nonexistent/active", 30*24*time.Hour, ""),
		meta("/nonexistent/active", 31*24*time.Hour, ""),
	}
	old := meta("/nonexistent/old", 60*24*time.Hour, "")
	metas = append(metas, old, meta("/nonexistent/old", 61*24*time.Hour, ""), meta("/nonexistent/old", 62*24*time.Hour, ""),
		meta("/nonexistent/old", 60*24*time.Hour, old.ID))
	past := hubcore.NewPastIndex("")
	past.SeedForTest(metas)
	return NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: past, Log: logs.logger()})
}

// A completed build reports its size: roots versus subagents, and archived
// roots split by whether their project is wholly archived.
func TestNavigationBuildLogsCountsForFixture(t *testing.T) {
	logs := &logCapture{}
	web := navigationStatsFixture(t, logs)
	service := newNavigationService(navigationServiceConfig{
		Source:    webNavigationSource{web: web},
		Log:       logs.logger(),
		statsSlow: time.Nanosecond,
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	records := buildRecords(logs)
	if len(records) != 1 {
		t.Fatalf("build records = %+v, want exactly one", records)
	}
	attrs := records[0].attrs
	if got := attrs["event"]; got != "completed" {
		t.Errorf("event = %v, want completed", got)
	}
	want := map[string]int64{
		"metas":                            8,
		"subagents":                        2,
		"roots":                            6,
		"archived_roots":                   5,
		"archived_roots_archived_projects": 3,
		"archived_roots_active_projects":   2,
		"dirs":                             2,
		"live":                             0,
		"restarts":                         0,
	}
	for key, value := range want {
		if got, ok := attrs[key].(int64); !ok || got != value {
			t.Errorf("%s = %v, want %d (record %+v)", key, attrs[key], value, records[0])
		}
	}
	for _, key := range []string{"total", "inputs", "resolve", "tree", "projection", "fingerprints", "next_states", "resources"} {
		if _, ok := attrs[key]; !ok {
			t.Errorf("record lacks %s: %+v", key, records[0])
		}
	}
	if n, ok := attrs["resources"].(int64); !ok || n == 0 {
		t.Errorf("resources = %v, want a positive count", attrs["resources"])
	}
}

// Fast builds stay quiet, including the first: a chatty hub must not log every
// invalidation, and a test that builds a hub must print nothing.
func TestNavigationFastBuildsDoNotLog(t *testing.T) {
	logs := &logCapture{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Log = logs.logger()
		cfg.statsSlow = time.Hour
	})
	for _, title := range []string{"one", "two", "three"} {
		source.changeTitle(title)
		if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
			t.Fatal(err)
		}
	}
	if records := buildRecords(logs); len(records) != 0 {
		t.Errorf("build records = %+v, want none", records)
	}
}

// A fast build logs once the periodic interval has passed since the service
// started or last logged.
func TestNavigationFastBuildLogsPeriodically(t *testing.T) {
	logs := &logCapture{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	now := time.Unix(1_700_000_000, 0).UTC()
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Log = logs.logger()
		cfg.statsSlow = time.Hour
		cfg.Now = func() time.Time { return now }
	})
	source.changeTitle("before the interval")
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(navigationBuildStatsInterval)
	source.changeTitle("after the interval")
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("right after the periodic line")
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	if records := buildRecords(logs); len(records) != 1 {
		t.Errorf("build records = %+v, want exactly the periodic line", records)
	}
}

// A build whose source moves mid-capture is retried; the line says how often.
func TestNavigationBuildLogCountsRestarts(t *testing.T) {
	logs := &logCapture{}
	inner := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source := &restartingNavigationSource{testNavigationSource: inner}
	service := newNavigationService(navigationServiceConfig{
		Source:     source,
		Generation: func() (string, error) { return "00112233445566778899aabbccddeeff", nil },
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Log:        logs.logger(),
		statsSlow:  time.Nanosecond,
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	records := buildRecords(logs)
	if len(records) != 1 {
		t.Fatalf("build records = %+v, want one", records)
	}
	if got := records[0].attrs["restarts"]; got != int64(1) {
		t.Errorf("restarts = %v, want 1 (record %+v)", got, records[0])
	}
}

type restartingNavigationSource struct {
	*testNavigationSource
	once sync.Once
}

func (s *restartingNavigationSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	snapshot, err := s.testNavigationSource.Capture(ctx, generation, now)
	s.once.Do(func() { s.changeTitle("moved mid-capture") })
	return snapshot, err
}

// A build that hits its deadline always logs, and names the phase it was in.
func TestNavigationBuildTimeoutAlwaysLogs(t *testing.T) {
	logs := &logCapture{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Log = logs.logger()
		cfg.BuildTimeout = 20 * time.Millisecond
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err == nil {
		t.Fatal("refresh of a blocked capture succeeded")
	}
	records := buildRecords(logs)
	if len(records) != 1 || records[0].attrs["event"] != "timeout" || records[0].attrs["phase"] != "capture" {
		t.Fatalf("build records = %+v, want one timeout record naming the capture phase", records)
	}
	if got := records[0].attrs["restarts"]; got != int64(0) {
		t.Errorf("restarts = %v, want 0", got)
	}
}

// The timeout line names the phase that was running, not the next one.
func TestNavigationBuildTimeoutNamesProjectionPhase(t *testing.T) {
	old := buildNavigationServiceProjectionContext
	buildNavigationServiceProjectionContext = func(ctx context.Context, _ navigationBuildInputs) (navigationProjection, error) {
		<-ctx.Done()
		return navigationProjection{}, ctx.Err()
	}
	t.Cleanup(func() { buildNavigationServiceProjectionContext = old })
	logs := &logCapture{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Log = logs.logger()
		cfg.BuildTimeout = 20 * time.Millisecond
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err == nil {
		t.Fatal("refresh with a stuck projection succeeded")
	}
	records := buildRecords(logs)
	if len(records) != 1 || records[0].attrs["event"] != "timeout" || records[0].attrs["phase"] != "projection" {
		t.Fatalf("build records = %+v, want one timeout record naming projection", records)
	}
}

// A build that fails before its deadline logs the phase and the error, so a
// navigation surface that stops updating has a cause in the hub log.
func TestNavigationBuildFailureLogsPhaseAndError(t *testing.T) {
	captureErr := errors.New("favorite store unreadable")
	projectionErr := errors.New("projection refused")
	for _, tc := range []struct {
		name  string
		phase string
		want  error
		setup func(t *testing.T, source *testNavigationSource)
	}{
		{
			name:  "capture",
			phase: "capture",
			want:  captureErr,
			setup: func(_ *testing.T, source *testNavigationSource) { source.err = captureErr },
		},
		{
			name:  "projection",
			phase: "projection",
			want:  projectionErr,
			setup: func(t *testing.T, _ *testNavigationSource) {
				old := buildNavigationServiceProjectionContext
				buildNavigationServiceProjectionContext = func(context.Context, navigationBuildInputs) (navigationProjection, error) {
					return navigationProjection{}, projectionErr
				}
				t.Cleanup(func() { buildNavigationServiceProjectionContext = old })
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &logCapture{}
			source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
			tc.setup(t, source)
			service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
				cfg.Log = logs.logger()
				cfg.statsSlow = time.Hour
			})
			if _, err := service.Refresh(t.Context(), navigationChangeHint{}); !errors.Is(err, tc.want) {
				t.Fatalf("refresh error = %v, want %v", err, tc.want)
			}
			records := logs.all()
			if len(records) != 1 {
				t.Fatalf("log records = %+v, want one failure record carrying phase, stats and error", records)
			}
			if got := records[0].attrs["event"]; got != "failed" {
				t.Errorf("logged event = %v, want failed", got)
			}
			if got := records[0].attrs["phase"]; got != tc.phase {
				t.Errorf("logged phase = %v, want %q", got, tc.phase)
			}
			if loggedErr, ok := records[0].attrs["error"].(error); !ok || !errors.Is(loggedErr, tc.want) {
				t.Errorf("logged error = %v, want %v", records[0].attrs["error"], tc.want)
			}
		})
	}
}

// A build canceled because the service is stopping is not a failure worth a
// line: shutdown would otherwise log one per in-flight build.
func TestNavigationBuildCanceledByStopDoesNotLog(t *testing.T) {
	logs := &logCapture{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Log = logs.logger()
	})
	refreshed := make(chan error, 1)
	go func() {
		_, err := service.Refresh(t.Context(), navigationChangeHint{})
		refreshed <- err
	}()
	<-source.entered
	service.stopLifecycle()
	if err := <-refreshed; err == nil {
		t.Fatal("refresh of a build canceled by stop succeeded")
	}
	if records := logs.all(); len(records) != 0 {
		t.Errorf("log records = %+v, want none", records)
	}
}

// A finalized flight's tail can log while the next flight's build finishes, so
// the log's last-logged time must tolerate concurrent callers (run with -race).
func TestNavigationBuildStatsLogToleratesConcurrentBuilds(t *testing.T) {
	logs := &logCapture{}
	statsLog := navigationBuildStatsLog{log: logs.logger(), lastLogged: time.Unix(1_700_000_000, 0)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			statsLog.completed(navigationBuildStats{}, time.Unix(1_700_000_000, 0).Add(navigationBuildStatsInterval))
			statsLog.timedOut(navigationBuildStats{phase: "capture"})
		})
	}
	wg.Wait()
	if records := buildRecords(logs); len(records) != 9 {
		t.Errorf("build records = %d, want 1 completed plus 8 timeouts", len(records))
	}
}
