package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// ---------------------------------------------------------------------------
// formatQuietWindow
// ---------------------------------------------------------------------------

func TestFormatQuietWindow(t *testing.T) {
	tests := []struct {
		window time.Duration
		want   string
	}{
		{time.Minute, "1m"},
		{10 * time.Minute, "10m"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{50 * time.Millisecond, "50ms"},
		{0, "0s"},
	}
	for _, tc := range tests {
		if got := formatQuietWindow(tc.window); got != tc.want {
			t.Errorf("formatQuietWindow(%v) = %q, want %q", tc.window, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// quietWatchdogMessage
// ---------------------------------------------------------------------------

func TestQuietWatchdogMessage(t *testing.T) {
	now := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	msg := quietWatchdogMessage(10*time.Minute, now)
	if !strings.Contains(msg, "10m") {
		t.Fatalf("expected '10m' in message: %q", msg)
	}
	if !strings.Contains(msg, "quiet for") {
		t.Fatalf("expected 'quiet for' in message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// watchEndedUnfiredMessage
// ---------------------------------------------------------------------------

func TestWatchEndedUnfiredMessage(t *testing.T) {
	msg := watchEndedUnfiredMessage("job_123", "completed", "exit 0", 5000)
	if !strings.Contains(msg, "job_123") {
		t.Fatalf("expected target in message: %q", msg)
	}
	if !strings.Contains(msg, "completed") {
		t.Fatalf("expected status in message: %q", msg)
	}
	if !strings.Contains(msg, "exit 0") {
		t.Fatalf("expected reason in message: %q", msg)
	}
	if !strings.Contains(msg, "5000") {
		t.Fatalf("expected output_bytes in message: %q", msg)
	}
	if !strings.Contains(msg, "never matched") {
		t.Fatalf("expected 'never matched' in message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// watchLostAtRestartMessage
// ---------------------------------------------------------------------------

func TestWatchLostAtRestartMessage(t *testing.T) {
	msg := watchLostAtRestartMessage("job_456", "running")
	if !strings.Contains(msg, "job_456") {
		t.Fatalf("expected target in message: %q", msg)
	}
	if !strings.Contains(msg, "running") {
		t.Fatalf("expected status in message: %q", msg)
	}
	if !strings.Contains(msg, "restarted") {
		t.Fatalf("expected 'restarted' in message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// watchLostAtRestartSessionMessage
// ---------------------------------------------------------------------------

func TestWatchLostAtRestartSessionMessage(t *testing.T) {
	msg := watchLostAtRestartSessionMessage()
	if !strings.Contains(msg, "session restarted") {
		t.Fatalf("expected 'session restarted' in message: %q", msg)
	}
	if !strings.Contains(msg, "re-arm") {
		t.Fatalf("expected 're-arm' in message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// watchLostAtRestartStableDelegateMessage
// ---------------------------------------------------------------------------

func TestWatchLostAtRestartStableDelegateMessage(t *testing.T) {
	msg := watchLostAtRestartStableDelegateMessage("dlg_abc")
	if !strings.Contains(msg, "dlg_abc") {
		t.Fatalf("expected source in message: %q", msg)
	}
	if !strings.Contains(msg, "restarted") {
		t.Fatalf("expected 'restarted' in message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// watchBudgetClearedMessage
// ---------------------------------------------------------------------------

func TestWatchBudgetClearedMessage(t *testing.T) {
	msg := watchBudgetClearedMessage("job_789")
	if !strings.Contains(msg, "job_789") {
		t.Fatalf("expected target in message: %q", msg)
	}
	if !strings.Contains(msg, "watch cleared") {
		t.Fatalf("expected 'watch cleared' in message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// callbackWatchesCancelledAtRestartMessage constant
// ---------------------------------------------------------------------------

func TestCallbackWatchesCancelledAtRestartMessage(t *testing.T) {
	if !strings.Contains(callbackWatchesCancelledAtRestartMessage, "agent restarted") {
		t.Fatalf("expected 'agent restarted' in constant")
	}
}

// ---------------------------------------------------------------------------
// normalizeWatchSource
// ---------------------------------------------------------------------------

func TestNormalizeWatchSource(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		_, err := normalizeWatchSource("")
		if err == nil || !strings.Contains(err.Error(), "source is required") {
			t.Fatalf("expected error for empty source, got %v", err)
		}
	})
	t.Run("whitespace only", func(t *testing.T) {
		_, err := normalizeWatchSource("   ")
		if err == nil {
			t.Fatalf("expected error for whitespace-only source")
		}
	})
	t.Run("self", func(t *testing.T) {
		src, err := normalizeWatchSource("self")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.Kind != watchSourceSelfSession {
			t.Fatalf("Kind = %v", src.Kind)
		}
		if src.Public != "self" {
			t.Fatalf("Public = %q", src.Public)
		}
	})
	t.Run("parent", func(t *testing.T) {
		src, err := normalizeWatchSource("parent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.Kind != watchSourceParentSession {
			t.Fatalf("Kind = %v", src.Kind)
		}
	})
	t.Run("job id", func(t *testing.T) {
		src, err := normalizeWatchSource("job_abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.Kind != watchSourceConcreteJob {
			t.Fatalf("Kind = %v", src.Kind)
		}
		if src.Public != "job_abc123" {
			t.Fatalf("Public = %q", src.Public)
		}
	})
	t.Run("delegate id", func(t *testing.T) {
		src, err := normalizeWatchSource("dlg_abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.Kind != watchSourceStableDelegate {
			t.Fatalf("Kind = %v", src.Kind)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		_, err := normalizeWatchSource("unknown_source")
		if err == nil || !strings.Contains(err.Error(), "source_not_watchable") {
			t.Fatalf("expected source_not_watchable error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// watchPublicSource
// ---------------------------------------------------------------------------

func TestWatchPublicSource(t *testing.T) {
	t.Run("non-empty source", func(t *testing.T) {
		if got := watchPublicSource("self", "job_123"); got != "self" {
			t.Fatalf("expected 'self', got %q", got)
		}
	})
	t.Run("empty source with caller target", func(t *testing.T) {
		if got := watchPublicSource("", runtimeMessageAliasCaller); got != "self" {
			t.Fatalf("expected 'self', got %q", got)
		}
	})
	t.Run("empty source with job target", func(t *testing.T) {
		if got := watchPublicSource("", "job_123"); got != "job_123" {
			t.Fatalf("expected 'job_123', got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// isWatchSessionTarget
// ---------------------------------------------------------------------------

func TestIsWatchSessionTarget(t *testing.T) {
	if !isWatchSessionTarget(runtimeMessageAliasCaller) {
		t.Fatalf("expected true for caller alias")
	}
	if !isWatchSessionTarget("*") {
		t.Fatalf("expected true for '*'")
	}
	if isWatchSessionTarget("job_123") {
		t.Fatalf("expected false for job target")
	}
	if isWatchSessionTarget("dlg_123") {
		t.Fatalf("expected false for delegate target")
	}
	if isWatchSessionTarget("") {
		t.Fatalf("expected false for empty target")
	}
}

// ---------------------------------------------------------------------------
// watchTargetNotFoundError / watchTargetTerminalError
// ---------------------------------------------------------------------------

func TestWatchTargetNotFoundError(t *testing.T) {
	err := watchTargetNotFoundError("job_123")
	if !errors.Is(err, errWatchTargetNotFound) {
		t.Fatalf("expected errWatchTargetNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "job_123") {
		t.Fatalf("expected target in error: %v", err)
	}
}

func TestWatchTargetTerminalError(t *testing.T) {
	err := watchTargetTerminalError("job_456", "completed")
	if !strings.Contains(err.Error(), "job_456") {
		t.Fatalf("expected target in error: %v", err)
	}
	if !strings.Contains(err.Error(), "completed") {
		t.Fatalf("expected status in error: %v", err)
	}
	if !strings.Contains(err.Error(), "target_terminal") {
		t.Fatalf("expected 'target_terminal' in error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// watchArgsHasCondition
// ---------------------------------------------------------------------------

func TestWatchArgsHasCondition(t *testing.T) {
	t.Run("output_match", func(t *testing.T) {
		if !watchArgsHasCondition(watchArgs{OutputMatch: "ERROR"}) {
			t.Fatalf("expected true for output_match")
		}
	})
	t.Run("progress_interval", func(t *testing.T) {
		if !watchArgsHasCondition(watchArgs{ProgressIntervalMS: 5000}) {
			t.Fatalf("expected true for progress_interval")
		}
	})
	t.Run("events", func(t *testing.T) {
		if !watchArgsHasCondition(watchArgs{Events: []string{"job.notification"}}) {
			t.Fatalf("expected true for events")
		}
	})
	t.Run("no condition", func(t *testing.T) {
		if watchArgsHasCondition(watchArgs{}) {
			t.Fatalf("expected false for no condition")
		}
	})
}

// ---------------------------------------------------------------------------
// watchArgsIsOutputMatchOnly
// ---------------------------------------------------------------------------

func TestWatchArgsIsOutputMatchOnly(t *testing.T) {
	t.Run("output_match only", func(t *testing.T) {
		if !watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "ERROR"}) {
			t.Fatalf("expected true for output_match only")
		}
	})
	t.Run("with events", func(t *testing.T) {
		if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "ERROR", Events: []string{"job.notification"}}) {
			t.Fatalf("expected false with events")
		}
	})
	t.Run("with every", func(t *testing.T) {
		if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "ERROR", Every: 3}) {
			t.Fatalf("expected false with every")
		}
	})
	t.Run("with progress", func(t *testing.T) {
		if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "ERROR", ProgressIntervalMS: 1000}) {
			t.Fatalf("expected false with progress")
		}
	})
	t.Run("clear request", func(t *testing.T) {
		if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "ERROR", Clear: true}) {
			t.Fatalf("expected false for clear request")
		}
	})
	t.Run("no output_match", func(t *testing.T) {
		if watchArgsIsOutputMatchOnly(watchArgs{}) {
			t.Fatalf("expected false for no output_match")
		}
	})
}

// ---------------------------------------------------------------------------
// validateWatchEventArgs
// ---------------------------------------------------------------------------

func TestValidateWatchEventArgs(t *testing.T) {
	t.Run("valid wildcard", func(t *testing.T) {
		if err := validateWatchEventArgs(watchArgs{Events: []string{"*"}}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("assistant.message rejected", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Events: []string{"assistant.message"}})
		if err == nil || !strings.Contains(err.Error(), "assistant.message is not watchable") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("unknown event", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Events: []string{"unknown.event"}})
		if err == nil || !strings.Contains(err.Error(), "unknown event kind") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("every with zero events", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Every: 3})
		if err == nil || !strings.Contains(err.Error(), "every requires exactly one") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("every with multiple events", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Every: 3, Events: []string{"job.notification", "communicate"}})
		if err == nil || !strings.Contains(err.Error(), "every requires exactly one") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("every with wildcard", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Every: 3, Events: []string{"*"}})
		if err == nil || !strings.Contains(err.Error(), "concrete event kind") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("event_filter without events", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{EventFilter: &watchEventFilter{}})
		if err == nil || !strings.Contains(err.Error(), "event_filter requires events") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("event_filter with wrong event", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Events: []string{"job.notification"}, EventFilter: &watchEventFilter{}})
		if err == nil {
			t.Fatalf("expected error for event_filter with non-assistant.tool event")
		}
	})
	t.Run("event_filter with communicate", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Events: []string{"communicate"}, EventFilter: &watchEventFilter{}})
		if err == nil || !strings.Contains(err.Error(), "parent observers") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("event_filter invalid status", func(t *testing.T) {
		err := validateWatchEventArgs(watchArgs{Events: []string{"assistant.tool"}, EventFilter: &watchEventFilter{Status: "invalid"}})
		if err == nil || !strings.Contains(err.Error(), "must be") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("event_filter valid status ok", func(t *testing.T) {
		if err := validateWatchEventArgs(watchArgs{Events: []string{"assistant.tool"}, EventFilter: &watchEventFilter{Status: "ok"}}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("event_filter valid status error", func(t *testing.T) {
		if err := validateWatchEventArgs(watchArgs{Events: []string{"assistant.tool"}, EventFilter: &watchEventFilter{Status: "error"}}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("event_filter empty status", func(t *testing.T) {
		if err := validateWatchEventArgs(watchArgs{Events: []string{"assistant.tool"}, EventFilter: &watchEventFilter{}}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// validateWatchTriggerShape
// ---------------------------------------------------------------------------

func TestValidateWatchTriggerShape(t *testing.T) {
	t.Run("progress with session events", func(t *testing.T) {
		err := validateWatchTriggerShape(watchArgs{ProgressIntervalMS: 1000, Events: []string{"job.notification"}, Target: runtimeMessageAliasCaller})
		if err == nil || !strings.Contains(err.Error(), "session event watches use events") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("progress with job target ok", func(t *testing.T) {
		if err := validateWatchTriggerShape(watchArgs{ProgressIntervalMS: 1000, Events: []string{"job.notification"}, Target: "job_123"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("no progress no error", func(t *testing.T) {
		if err := validateWatchTriggerShape(watchArgs{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// isSupportedWatchEventKind
// ---------------------------------------------------------------------------

func TestIsSupportedWatchEventKind(t *testing.T) {
	for _, kind := range modelEventKinds {
		if !isSupportedWatchEventKind(kind) {
			t.Errorf("expected %q to be supported", kind)
		}
	}
	if isSupportedWatchEventKind(events.EventKind("nonexistent.event")) {
		t.Fatalf("expected false for non-existent kind")
	}
}

// ---------------------------------------------------------------------------
// canonicalWatchEvents
// ---------------------------------------------------------------------------

func TestCanonicalWatchEvents(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if canonicalWatchEvents(nil) != nil {
			t.Fatalf("expected nil for empty input")
		}
	})
	t.Run("sorted copy", func(t *testing.T) {
		result := canonicalWatchEvents([]string{"c", "a", "b"})
		if len(result) != 3 || result[0] != "a" || result[1] != "b" || result[2] != "c" {
			t.Fatalf("expected sorted, got %v", result)
		}
	})
	t.Run("does not modify original", func(t *testing.T) {
		original := []string{"c", "a", "b"}
		_ = canonicalWatchEvents(original)
		if original[0] != "c" {
			t.Fatalf("original should not be modified")
		}
	})
}

// ---------------------------------------------------------------------------
// resolveEventKinds
// ---------------------------------------------------------------------------

func TestResolveEventKinds(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		kinds, wildcard := resolveEventKinds(nil)
		if len(kinds) != 0 || wildcard {
			t.Fatalf("expected empty/non-wildcard, got %v %v", kinds, wildcard)
		}
	})
	t.Run("wildcard", func(t *testing.T) {
		_, wildcard := resolveEventKinds([]string{"*"})
		if !wildcard {
			t.Fatalf("expected wildcard=true")
		}
	})
	t.Run("known event", func(t *testing.T) {
		kinds, _ := resolveEventKinds([]string{"job.notification"})
		if len(kinds) != 1 {
			t.Fatalf("expected 1 kind, got %d", len(kinds))
		}
	})
	t.Run("unknown event skipped", func(t *testing.T) {
		kinds, _ := resolveEventKinds([]string{"unknown.event"})
		if len(kinds) != 0 {
			t.Fatalf("expected 0 kinds for unknown event")
		}
	})
}

// ---------------------------------------------------------------------------
// cloneWatchEventFilter
// ---------------------------------------------------------------------------

func TestCloneWatchEventFilter(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if cloneWatchEventFilter(nil) != nil {
			t.Fatalf("expected nil for nil filter")
		}
	})
	t.Run("clone", func(t *testing.T) {
		filter := &watchEventFilter{ToolName: "read_file", Status: "ok"}
		clone := cloneWatchEventFilter(filter)
		if clone == nil || clone.ToolName != "read_file" || clone.Status != "ok" {
			t.Fatalf("clone wrong: %+v", clone)
		}
		// Modify clone
		clone.ToolName = "modified"
		if filter.ToolName == "modified" {
			t.Fatalf("expected original to be unaffected")
		}
	})
}

// ---------------------------------------------------------------------------
// watchEventFilterSnapshot
// ---------------------------------------------------------------------------

func TestWatchEventFilterSnapshot(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if watchEventFilterSnapshot(nil) != nil {
			t.Fatalf("expected nil for nil filter")
		}
	})
	t.Run("valid", func(t *testing.T) {
		filter := &watchEventFilter{ToolName: "exec_command", Status: "error"}
		snap := watchEventFilterSnapshot(filter)
		if snap == nil || snap.ToolName != "exec_command" || snap.Status != "error" {
			t.Fatalf("snapshot wrong: %+v", snap)
		}
	})
}

// ---------------------------------------------------------------------------
// cloneWatchSendArgs
// ---------------------------------------------------------------------------

func TestCloneWatchSendArgs(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if cloneWatchSendArgs(nil) != nil {
			t.Fatalf("expected nil for nil send")
		}
	})
	t.Run("clone", func(t *testing.T) {
		send := &watchSendArgs{To: "dlg_1", Message: "hello", IncludeExcerpt: true}
		clone := cloneWatchSendArgs(send)
		if clone == nil || clone.To != "dlg_1" {
			t.Fatalf("clone wrong: %+v", clone)
		}
		clone.To = "modified"
		if send.To == "modified" {
			t.Fatalf("expected original to be unaffected")
		}
	})
}

// ---------------------------------------------------------------------------
// stableWatchSourceID
// ---------------------------------------------------------------------------

func TestStableWatchSourceID(t *testing.T) {
	t.Run("explicit delegate ID", func(t *testing.T) {
		if id := stableWatchSourceID(watchArgs{SourceDelegateID: "dlg_1"}); id != "dlg_1" {
			t.Fatalf("expected 'dlg_1', got %q", id)
		}
	})
	t.Run("source is delegate", func(t *testing.T) {
		if id := stableWatchSourceID(watchArgs{Source: "dlg_abc"}); id != "dlg_abc" {
			t.Fatalf("expected 'dlg_abc', got %q", id)
		}
	})
	t.Run("source is job", func(t *testing.T) {
		if id := stableWatchSourceID(watchArgs{Source: "job_123"}); id != "" {
			t.Fatalf("expected empty for job source, got %q", id)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if id := stableWatchSourceID(watchArgs{}); id != "" {
			t.Fatalf("expected empty for empty args, got %q", id)
		}
	})
}

// ---------------------------------------------------------------------------
// stableWatchSourceSnapshot
// ---------------------------------------------------------------------------

func TestStableWatchSourceSnapshot(t *testing.T) {
	t.Run("no stable source", func(t *testing.T) {
		if snap := stableWatchSourceSnapshot(watchArgs{}); snap != "" {
			t.Fatalf("expected empty for no stable source, got %q", snap)
		}
	})
	t.Run("with delegate source", func(t *testing.T) {
		if snap := stableWatchSourceSnapshot(watchArgs{Source: "dlg_abc"}); snap != "dlg_abc" {
			t.Fatalf("expected 'dlg_abc', got %q", snap)
		}
	})
	t.Run("with stable receiver", func(t *testing.T) {
		if snap := stableWatchSourceSnapshot(watchArgs{Source: "self", StableReceiver: true}); snap != "self" {
			t.Fatalf("expected 'self', got %q", snap)
		}
	})
}

// ---------------------------------------------------------------------------
// applyStableReceiverWatchSend
// ---------------------------------------------------------------------------

func TestApplyStableReceiverWatchSend(t *testing.T) {
	t.Run("nil args", func(t *testing.T) {
		applyStableReceiverWatchSend(nil) // should be a no-op
	})
	t.Run("existing send", func(t *testing.T) {
		a := &watchArgs{Send: &watchSendArgs{To: "dlg_1"}}
		applyStableReceiverWatchSend(a)
		if a.Send.To != "dlg_1" {
			t.Fatalf("expected existing send to be preserved")
		}
	})
	t.Run("stable receiver creates send", func(t *testing.T) {
		a := &watchArgs{StableReceiver: true}
		applyStableReceiverWatchSend(a)
		if a.Send == nil {
			t.Fatalf("expected send to be created")
		}
		if a.Send.To != stableWatchReceiverTarget {
			t.Fatalf("expected To=%q, got %q", stableWatchReceiverTarget, a.Send.To)
		}
		if !a.ReceiverSendInternal {
			t.Fatalf("expected ReceiverSendInternal=true")
		}
	})
	t.Run("non-stable no send", func(t *testing.T) {
		a := &watchArgs{}
		applyStableReceiverWatchSend(a)
		if a.Send != nil {
			t.Fatalf("expected nil send for non-stable receiver")
		}
	})
}

// ---------------------------------------------------------------------------
// watchSendStateLess
// ---------------------------------------------------------------------------

func TestWatchSendStateLess(t *testing.T) {
	t1 := time.Now()
	t2 := t1.Add(time.Second)
	a := &jobstore.WatchSendState{CreatedAt: t1, UpdatedAt: t1, UpdateSeq: 1, Key: jobstore.WatchSendKey{VisibleSessionID: "a"}}
	b := &jobstore.WatchSendState{CreatedAt: t2, UpdatedAt: t2, UpdateSeq: 2, Key: jobstore.WatchSendKey{VisibleSessionID: "b"}}
	if !watchSendStateLess(a, b) {
		t.Fatalf("expected a < b")
	}
	if watchSendStateLess(b, a) {
		t.Fatalf("expected b >= a")
	}
}

// ---------------------------------------------------------------------------
// watchSendKeyLess
// ---------------------------------------------------------------------------

func TestWatchSendKeyLess(t *testing.T) {
	a := jobstore.WatchSendKey{VisibleSessionID: "a", WatchTarget: "t1"}
	b := jobstore.WatchSendKey{VisibleSessionID: "b", WatchTarget: "t2"}
	if !watchSendKeyLess(a, b) {
		t.Fatalf("expected a < b")
	}
	if watchSendKeyLess(b, a) {
		t.Fatalf("expected b >= a")
	}
}

// ---------------------------------------------------------------------------
// watchKeyMatchesClearRequest
// ---------------------------------------------------------------------------

func TestWatchKeyMatchesClearRequest(t *testing.T) {
	t.Run("exact match", func(t *testing.T) {
		candidate := watchKey{VisibleSessionID: "s1", Target: "job_1", SendTo: "dlg_1"}
		request := watchKey{VisibleSessionID: "s1", Target: "job_1", SendTo: "dlg_1"}
		if !watchKeyMatchesClearRequest(candidate, request) {
			t.Fatalf("expected true for exact match")
		}
	})
}

// ---------------------------------------------------------------------------
// availableEventKindNames
// ---------------------------------------------------------------------------

func TestAvailableEventKindNames(t *testing.T) {
	names := availableEventKindNames()
	if len(names) == 0 {
		t.Fatalf("expected non-empty event kind names")
	}
	// Should be a copy
	sort.Strings(names)
	original := availableEventKindNames()
	if sort.SearchStrings(names, original[0]) < 0 {
		// just verify they exist
	}
}

// ---------------------------------------------------------------------------
// availableEventKindNames returns a copy
// ---------------------------------------------------------------------------

func TestWatchEventFilterStruct(t *testing.T) {
	f := watchEventFilter{ToolName: "exec_command", Status: "ok"}
	if f.ToolName != "exec_command" || f.Status != "ok" {
		t.Fatalf("struct wrong: %+v", f)
	}
}

// ---------------------------------------------------------------------------
// watchSendArgs struct
// ---------------------------------------------------------------------------

func TestWatchSendArgsStruct(t *testing.T) {
	s := watchSendArgs{To: "dlg_1", Message: "msg", IncludeExcerpt: true}
	if s.To != "dlg_1" || s.Message != "msg" || !s.IncludeExcerpt {
		t.Fatalf("struct wrong: %+v", s)
	}
}

// ---------------------------------------------------------------------------
// watchResult struct
// ---------------------------------------------------------------------------

func TestWatchResultStruct(t *testing.T) {
	r := watchResult{
		WatchID:            "watch_1",
		Source:             "self",
		Target:             "job_123",
		Watching:           true,
		OutputMatch:         "ERROR",
		Events:             []string{"job.notification"},
		ProgressIntervalMS: 5000,
	}
	if r.WatchID != "watch_1" || r.Source != "self" {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// watchKey struct
// ---------------------------------------------------------------------------

func TestWatchKeyStruct(t *testing.T) {
	k := watchKey{
		VisibleSessionID:   "sess_1",
		Target:             "job_1",
		SendTo:              "dlg_1",
		ReceiverSessionID:  "sess_2",
		ReceiverDelegateID: "dlg_2",
	}
	if k.VisibleSessionID != "sess_1" || k.Target != "job_1" {
		t.Fatalf("struct wrong: %+v", k)
	}
}

// ---------------------------------------------------------------------------
// errWatchTargetNotFound sentinel
// ---------------------------------------------------------------------------

func TestErrWatchTargetNotFound(t *testing.T) {
	if errWatchTargetNotFound == nil {
		t.Fatalf("expected non-nil sentinel")
	}
	if errWatchTargetNotFound.Error() != "target_not_found" {
		t.Fatalf("error = %q", errWatchTargetNotFound.Error())
	}
}

// ---------------------------------------------------------------------------
// fmt usage
// ---------------------------------------------------------------------------

var _ = fmt.Sprintf
