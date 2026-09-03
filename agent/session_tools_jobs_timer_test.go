package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
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
	// The note is bounded where it is stored (watchMessageMaxChars), not at the
	// tighter output_match bound, so job_list agrees with formatJobWatch and with
	// the tool description's verbatim claim.
	long := strings.Repeat("n", watchMessageMaxChars-8)
	if got := watchConditionSummary(&watchConfig{timer: true, timerSeconds: 300, note: long}); got != "repeat_seconds: 300; note: "+long {
		t.Fatalf("a %d-char note was truncated in the summary: len=%d", len(long), len(got))
	}
}

// TestMarshalWatchResult_TimerFieldsSurviveConfigToToolResult crosses both hops a
// timer takes on its way to the model: watchResultFromConfig reads the config's
// seconds and drops the derived progressIntervalMS, and marshalWatchResult routes
// those seconds into after_seconds or repeat_seconds by oneShot.
func TestMarshalWatchResult_TimerFieldsSurviveConfigToToolResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		cfg                *watchConfig
		afterSeconds       int
		repeatSeconds      int
		note               string
		progressIntervalMS int
	}{
		{
			name:          "repeat timer",
			cfg:           &watchConfig{timer: true, timerSeconds: 300, progressIntervalMS: 300000, note: "n"},
			repeatSeconds: 300,
			note:          "n",
		},
		{
			name:         "one-shot timer",
			cfg:          &watchConfig{timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600000},
			afterSeconds: 600,
		},
		{
			name:               "job progress watch",
			cfg:                &watchConfig{progressIntervalMS: 1000},
			progressIntervalMS: 1000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value, err := marshalWatchResult(watchResultFromConfig(tc.cfg, false), 0)
			if err != nil {
				t.Fatalf("marshalWatchResult: %v", err)
			}
			state, ok := value.(tool.StateResult)
			if !ok {
				t.Fatalf("value = %T, want tool.StateResult", value)
			}
			out, ok := state.State.(jobWatchToolResult)
			if !ok {
				t.Fatalf("state = %T, want jobWatchToolResult", state.State)
			}
			if out.AfterSeconds != tc.afterSeconds {
				t.Errorf("after_seconds = %d, want %d", out.AfterSeconds, tc.afterSeconds)
			}
			if out.RepeatSeconds != tc.repeatSeconds {
				t.Errorf("repeat_seconds = %d, want %d", out.RepeatSeconds, tc.repeatSeconds)
			}
			if out.Note != tc.note {
				t.Errorf("note = %q, want %q", out.Note, tc.note)
			}
			if out.ProgressIntervalMS != tc.progressIntervalMS {
				t.Errorf("progress_interval_ms = %d, want %d", out.ProgressIntervalMS, tc.progressIntervalMS)
			}
		})
	}
}

func TestJobWatchTool_TimerRefusedWhenTurnEndsProcess(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.cfg.TurnEndsProcess = true
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "c1", Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","repeat_seconds":300}`),
	})
	if !res.IsError || !strings.Contains(res.Output, "timers need a session that outlives the turn") {
		t.Fatalf("run-mode timer create: %+v", res)
	}
	s.cfg.TurnEndsProcess = false
	ok := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "c2", Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","repeat_seconds":300,"note":"x"}`),
	})
	if ok.IsError || !strings.Contains(ok.Output, "every 300s") {
		t.Fatalf("served timer create: %+v", ok)
	}
}
