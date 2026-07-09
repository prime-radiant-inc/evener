package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// These tests pin the scoping rule for concrete-job-targeted session-event
// watches (PRI-2525). ToolCallEndData and CommunicateData carry no job/session
// identity, and the only session whose events reach a jobManager's evaluator is
// the watcher's own — so a job-targeted watch on those kinds can never observe
// the watched job and instead echoes the watcher's own tool calls back at it
// labeled as job activity. The echo is a self-sustaining coordination loop:
// each frame provokes a communicate, whose TOOL_CALL_END fires the next frame.

// A job-targeted watch must not match tool events, filtered or not: the event
// cannot prove it belongs to the target job.
func TestJobTargetWatchDoesNotMatchOwnToolEvents(t *testing.T) {
	snap := watchEventSnapshot{
		target:       "job_watched",
		targetActive: true,
		eventKinds:   map[events.EventKind]bool{events.EventToolCallEnd: true},
		eventFilter:  &watchEventFilter{ToolName: "communicate", Status: "ok"},
		watchID:      "watch_scope_1",
		generation:   "g1",
	}
	ev := events.SessionEvent{
		Kind: events.EventToolCallEnd,
		Data: events.ToolCallEndData{ToolName: "communicate"},
	}
	if dec := evaluateWatchEvent(snap, ev); dec.matched {
		t.Fatalf("job-targeted watch matched the session's own communicate TOOL_CALL_END: %+v", dec)
	}
}

// The wildcard-events form of the same hole: without an event_filter the gate
// must still refuse identity-less kinds on a concrete job target.
func TestJobTargetWildcardWatchDoesNotMatchOwnToolEvents(t *testing.T) {
	snap := watchEventSnapshot{
		target:         "job_watched",
		targetActive:   true,
		wildcardEvents: true,
		watchID:        "watch_scope_2",
		generation:     "g1",
	}
	ev := events.SessionEvent{
		Kind: events.EventToolCallEnd,
		Data: events.ToolCallEndData{ToolName: "shell"},
	}
	if dec := evaluateWatchEvent(snap, ev); dec.matched {
		t.Fatalf("job-targeted wildcard watch matched an unscopeable tool event: %+v", dec)
	}
}

// Job lifecycle events carry a JobID and stay matchable on a job target.
func TestJobTargetWatchStillMatchesJobLifecycle(t *testing.T) {
	snap := watchEventSnapshot{
		target:       "job_watched",
		targetActive: true,
		eventKinds:   map[events.EventKind]bool{events.EventJobFinished: true},
		watchID:      "watch_scope_3",
		generation:   "g1",
	}
	ev := events.SessionEvent{
		Kind: events.EventJobFinished,
		Data: events.JobFinishedData{JobID: "job_watched"},
	}
	if dec := evaluateWatchEvent(snap, ev); !dec.matched {
		t.Fatalf("job-targeted watch must still match its own job's lifecycle event: %+v", dec)
	}
}

// Session-target watches (self/parent observers) keep tool-event semantics:
// firing on the session's own events is their intended meaning.
func TestSessionTargetWatchStillMatchesToolEvents(t *testing.T) {
	snap := watchEventSnapshot{
		target:       runtimeMessageAliasCaller,
		targetActive: true,
		eventKinds:   map[events.EventKind]bool{events.EventToolCallEnd: true},
		eventFilter:  &watchEventFilter{ToolName: "read_file", Status: "ok"},
		watchID:      "watch_scope_4",
		generation:   "g1",
	}
	ev := events.SessionEvent{
		Kind: events.EventToolCallEnd,
		Data: events.ToolCallEndData{ToolName: "read_file"},
	}
	if dec := evaluateWatchEvent(snap, ev); !dec.matched {
		t.Fatalf("session-target watch must keep matching its own tool events: %+v", dec)
	}
}

// Create-time: serf must refuse the trigger it cannot honor instead of acking
// it. A concrete-job target may only watch job.notification; assistant.tool /
// communicate / "*" (and event_filter, which implies assistant.tool) observe
// the watcher's own session and are rejected with guidance.
func TestJobTargetEventWatchRejectedAtCreate(t *testing.T) {
	cases := []struct {
		name string
		args watchArgs
	}{
		{"assistant.tool with filter", watchArgs{
			Target:      "job_watched",
			Events:      []string{"assistant.tool"},
			EventFilter: &watchEventFilter{ToolName: "communicate", Status: "ok"},
		}},
		{"assistant.tool bare", watchArgs{
			Target: "job_watched",
			Events: []string{"assistant.tool"},
		}},
		{"communicate kind", watchArgs{
			Target: "job_watched",
			Events: []string{"communicate"},
		}},
		{"wildcard", watchArgs{
			Target: "job_watched",
			Events: []string{"*"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWatchEventArgs(tc.args)
			if err == nil {
				t.Fatal("expected invalid_request for an unscopeable event watch on a concrete job target")
			}
			if !strings.Contains(err.Error(), "invalid_request") {
				t.Fatalf("want invalid_request, got: %v", err)
			}
		})
	}
}

// The scoped and session-target forms stay accepted.
func TestScopedEventWatchesStillAcceptedAtCreate(t *testing.T) {
	cases := []struct {
		name string
		args watchArgs
	}{
		{"job.notification on job target", watchArgs{
			Target: "job_watched",
			Events: []string{"job.notification"},
		}},
		{"assistant.tool on session target", watchArgs{
			Target:      runtimeMessageAliasCaller,
			Events:      []string{"assistant.tool"},
			EventFilter: &watchEventFilter{ToolName: "read_file", Status: "ok"},
		}},
		{"communicate on session target", watchArgs{
			Target: runtimeMessageAliasCaller,
			Events: []string{"communicate"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateWatchEventArgs(tc.args); err != nil {
				t.Fatalf("expected accept, got: %v", err)
			}
		})
	}
}
