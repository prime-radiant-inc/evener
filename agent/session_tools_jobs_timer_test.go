package agent

import (
	"strings"
	"testing"
)

func TestFormatJobWatch_TimerCreateTextShowsIntervalAndNote(t *testing.T) {
	t.Parallel()
	repeat := formatJobWatch(jobWatchToolResult{WatchID: "w1", Source: "self", Watching: true, RepeatSeconds: 300, Note: "PR #123"})
	if !strings.Contains(repeat, "every 300s") || !strings.Contains(repeat, "note: PR #123") || strings.Contains(repeat, "ms") {
		t.Fatalf("repeat create text = %q", repeat)
	}
	one := formatJobWatch(jobWatchToolResult{WatchID: "w2", Source: "self", Watching: true, AfterSeconds: 600})
	if !strings.Contains(one, "after 600s") {
		t.Fatalf("one-shot create text = %q", one)
	}
	progress := formatJobWatch(jobWatchToolResult{WatchID: "w3", Source: "job_1", Watching: true, ProgressIntervalMS: 300000})
	if !strings.Contains(progress, "progress_interval_ms 300000ms") {
		t.Fatalf("job progress text must be distinguishable: %q", progress)
	}
}

func TestWatchConditionSummary_Timers(t *testing.T) {
	t.Parallel()
	repeat := watchConditionSummary(&watchConfig{timer: true, timerSeconds: 300, progressIntervalMS: 300000, note: "PR #123 <x>"})
	if repeat != "repeat_seconds: 300; note: PR #123 <x>" {
		t.Fatalf("repeat summary = %q", repeat)
	}
	one := watchConditionSummary(&watchConfig{timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600000})
	if one != "after_seconds: 600" {
		t.Fatalf("one-shot summary = %q", one)
	}
	if got := watchConditionSummary(&watchConfig{progressIntervalMS: 1000}); got != "progress_interval_ms: 1000" {
		t.Fatalf("job progress summary changed: %q", got)
	}
}
