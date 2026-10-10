package hub

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type navigationStatsLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *navigationStatsLog) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l *navigationStatsLog) buildLines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		if strings.HasPrefix(line, "navigation build") {
			out = append(out, line)
		}
	}
	return out
}

// statsFields parses "navigation build: k=v k=v" into a map.
func statsFields(t *testing.T, line string) map[string]string {
	t.Helper()
	_, rest, ok := strings.Cut(line, ": ")
	if !ok {
		t.Fatalf("stats line has no fields: %q", line)
	}
	fields := map[string]string{}
	for part := range strings.FieldsSeq(rest) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			t.Fatalf("malformed field %q in %q", part, line)
		}
		fields[key] = value
	}
	return fields
}

func navigationStatsFixture(t *testing.T, logs *navigationStatsLog) *WebServer {
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
	return NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: past, Logf: logs.Logf})
}

// A completed build reports its size: roots versus subagents, and archived
// roots split by whether their project is wholly archived.
func TestNavigationBuildLogsCountsForFixture(t *testing.T) {
	logs := &navigationStatsLog{}
	web := navigationStatsFixture(t, logs)
	service := newNavigationService(navigationServiceConfig{
		Source:    webNavigationSource{web: web},
		Logf:      logs.Logf,
		statsSlow: time.Nanosecond,
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	lines := logs.buildLines()
	if len(lines) != 1 {
		t.Fatalf("build lines = %q, want exactly one", lines)
	}
	fields := statsFields(t, lines[0])
	want := map[string]string{
		"metas":                            "8",
		"subagents":                        "2",
		"roots":                            "6",
		"archived_roots":                   "5",
		"archived_roots_archived_projects": "3",
		"archived_roots_active_projects":   "2",
		"dirs":                             "2",
		"live":                             "0",
		"restarts":                         "0",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("%s = %q, want %q (line %q)", key, fields[key], value, lines[0])
		}
	}
	for _, key := range []string{"total", "inputs", "resolve", "tree", "projection", "fingerprints", "next_states", "resources"} {
		if fields[key] == "" {
			t.Errorf("line lacks %s: %q", key, lines[0])
		}
	}
	if n, err := strconv.Atoi(fields["resources"]); err != nil || n == 0 {
		t.Errorf("resources = %q, want a positive count", fields["resources"])
	}
}

// Fast builds stay quiet, including the first: a chatty hub must not log every
// invalidation, and a test that builds a hub must print nothing.
func TestNavigationFastBuildsDoNotLog(t *testing.T) {
	logs := &navigationStatsLog{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Logf = logs.Logf
		cfg.statsSlow = time.Hour
	})
	for _, title := range []string{"one", "two", "three"} {
		source.changeTitle(title)
		if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
			t.Fatal(err)
		}
	}
	if lines := logs.buildLines(); len(lines) != 0 {
		t.Errorf("build lines = %q, want none", lines)
	}
}

// A fast build logs once the periodic interval has passed since the service
// started or last logged.
func TestNavigationFastBuildLogsPeriodically(t *testing.T) {
	logs := &navigationStatsLog{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	now := time.Unix(1_700_000_000, 0).UTC()
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Logf = logs.Logf
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
	if lines := logs.buildLines(); len(lines) != 1 {
		t.Errorf("build lines = %q, want exactly the periodic line", lines)
	}
}

// A build whose source moves mid-capture is retried; the line says how often.
func TestNavigationBuildLogCountsRestarts(t *testing.T) {
	logs := &navigationStatsLog{}
	inner := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source := &restartingNavigationSource{testNavigationSource: inner}
	service := newNavigationService(navigationServiceConfig{
		Source:     source,
		Generation: func() (string, error) { return "00112233445566778899aabbccddeeff", nil },
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Logf:       logs.Logf,
		statsSlow:  time.Nanosecond,
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	lines := logs.buildLines()
	if len(lines) != 1 {
		t.Fatalf("build lines = %q, want one", lines)
	}
	if got := statsFields(t, lines[0])["restarts"]; got != "1" {
		t.Errorf("restarts = %q, want 1 (line %q)", got, lines[0])
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
	logs := &navigationStatsLog{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Logf = logs.Logf
		cfg.BuildTimeout = 20 * time.Millisecond
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err == nil {
		t.Fatal("refresh of a blocked capture succeeded")
	}
	lines := logs.buildLines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "navigation build timed out in capture: ") {
		t.Fatalf("build lines = %q, want one timeout line naming the capture phase", lines)
	}
	if got := statsFields(t, lines[0])["restarts"]; got != "0" {
		t.Errorf("restarts = %q, want 0", got)
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
	logs := &navigationStatsLog{}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Logf = logs.Logf
		cfg.BuildTimeout = 20 * time.Millisecond
	})
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err == nil {
		t.Fatal("refresh with a stuck projection succeeded")
	}
	lines := logs.buildLines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "navigation build timed out in projection: ") {
		t.Fatalf("build lines = %q, want one timeout line naming projection", lines)
	}
}

// A finalized flight's tail can log while the next flight's build finishes, so
// the log's last-logged time must tolerate concurrent callers (run with -race).
func TestNavigationBuildStatsLogToleratesConcurrentBuilds(t *testing.T) {
	logs := &navigationStatsLog{}
	statsLog := navigationBuildStatsLog{logf: logs.Logf, lastLogged: time.Unix(1_700_000_000, 0)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			statsLog.completed(navigationBuildStats{}, time.Unix(1_700_000_000, 0).Add(navigationBuildStatsInterval))
			statsLog.timedOut(navigationBuildStats{phase: "capture"})
		})
	}
	wg.Wait()
	if lines := logs.buildLines(); len(lines) != 9 {
		t.Errorf("build lines = %d, want 1 completed plus 8 timeouts", len(lines))
	}
}
