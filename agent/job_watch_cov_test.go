package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestTerminalCatchupWithSendCoversSendPath covers the send branch of
// runTerminalCatchup (job_watch.go:2682-2695): a terminal output_match-only
// watch with a Send field carries the match through the durable watch-send rail
// as a one-shot detached delivery rather than just enqueuing a notification.
func TestTerminalCatchupWithSendCoversSendPath(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jobID := terminalShellWithOutput(t, jm, "line one\nserver ready\nline three\n")

	res, err := jm.configureWatch(watchArgs{
		Target:      jobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configureWatch terminal catch-up with send: %v", err)
	}
	if res.Watching {
		t.Fatalf("terminal catch-up must not install a live watch: %+v", res)
	}
	if !res.Fired || !res.TerminalCatchup {
		t.Fatalf("result = %+v, want fired+terminal_catchup", res)
	}
	if res.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q", res.Status, jobstore.StatusCompleted)
	}
	// The send path records a pending watch-send delivery in the durable store.
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("send catch-up must record one pending send, got %d: %+v", len(pending), pending)
	}
	var state *jobstore.WatchSendState
	for _, p := range pending {
		state = p
	}
	if state == nil {
		t.Fatal("nil pending state")
	}
	if state.Key.ResolvedSendTo != runtimeMessageAliasCaller {
		t.Fatalf("pending send target = %q, want caller", state.Key.ResolvedSendTo)
	}
	if !strings.Contains(state.Frame, "observe") {
		t.Fatalf("pending frame = %q, want configured message", state.Frame)
	}
	if !strings.Contains(state.TriggerReason, "output_match: server ready") {
		t.Fatalf("trigger reason = %q, want last matching line", state.TriggerReason)
	}
}

// TestWriteAssistantToolWatchEventErrorField covers the error-branch of
// writeAssistantToolWatchEvent (job_watch.go:4825-4829) which renders the
// "error" and "error_truncated" fields when a tool call ends with an error.
func TestWriteAssistantToolWatchEventErrorField(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writeAssistantToolWatchEvent(&b, events.ToolCallEndData{
		ToolName:      "exec_command",
		CallID:        "call_1",
		Error:         "boom",
		Output:        "partial output",
		ArgumentsJSON: "{}",
	})
	frame := b.String()
	if !strings.Contains(frame, "status: error") {
		t.Fatalf("frame should contain 'status: error': %q", frame)
	}
	if !strings.Contains(frame, "error: boom") {
		t.Fatalf("frame should contain 'error: boom': %q", frame)
	}
	if !strings.Contains(frame, "error_truncated: false") {
		t.Fatalf("frame should contain 'error_truncated: false': %q", frame)
	}
	if !strings.Contains(frame, "output: partial output") {
		t.Fatalf("frame should contain output field: %q", frame)
	}
	if !strings.Contains(frame, "output_truncated: false") {
		t.Fatalf("frame should contain output_truncated: false: %q", frame)
	}
}

// TestWriteAssistantToolWatchEventErrorTruncation covers the truncation branch
// when the error text exceeds the watch event text limit.
func TestWriteAssistantToolWatchEventErrorTruncation(t *testing.T) {
	t.Parallel()
	longErr := strings.Repeat("e", 5000)
	var b strings.Builder
	writeAssistantToolWatchEvent(&b, events.ToolCallEndData{
		ToolName: "exec_command",
		Error:    longErr,
	})
	frame := b.String()
	if !strings.Contains(frame, "error_truncated: true") {
		t.Fatalf("frame should contain 'error_truncated: true': %q", frame)
	}
}

// TestWriteWatchFrameEventPointerTypes covers the pointer-receiver branches of
// writeWatchFrameEvent (job_watch.go:4751-4772) for nil and non-nil pointer data.
func TestWriteWatchFrameEventPointerTypes(t *testing.T) {
	t.Parallel()
	// Non-nil pointer for ToolCallEndData.
	var b strings.Builder
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventToolCallEnd,
		Data: &events.ToolCallEndData{ToolName: "read_file", Error: "fail"},
	})
	if !strings.Contains(b.String(), "tool_name: read_file") {
		t.Fatalf("pointer ToolCallEndData not rendered: %q", b.String())
	}

	// Nil pointer should not panic and produce no tool event.
	b.Reset()
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventToolCallEnd,
		Data: (*events.ToolCallEndData)(nil),
	})
	if strings.Contains(b.String(), "tool_name") {
		t.Fatalf("nil pointer should not render tool event: %q", b.String())
	}

	// Non-nil pointer for CommunicateData.
	b.Reset()
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventCommunicate,
		Data: &events.CommunicateData{Message: "hi", EndTurn: true},
	})
	if !strings.Contains(b.String(), "kind: communicate") || !strings.Contains(b.String(), "end_turn: true") {
		t.Fatalf("pointer CommunicateData not rendered: %q", b.String())
	}

	// Nil pointer for CommunicateData.
	b.Reset()
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventCommunicate,
		Data: (*events.CommunicateData)(nil),
	})
	if strings.Contains(b.String(), "kind: communicate") {
		t.Fatalf("nil communicate pointer should not render: %q", b.String())
	}

	// Non-nil pointer for AssistantTextEndData.
	b.Reset()
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: &events.AssistantTextEndData{Model: "gpt-5", Text: "hello"},
	})
	if !strings.Contains(b.String(), "kind: assistant.message") {
		t.Fatalf("pointer AssistantTextEndData not rendered: %q", b.String())
	}

	// Nil pointer for AssistantTextEndData.
	b.Reset()
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: (*events.AssistantTextEndData)(nil),
	})
	if strings.Contains(b.String(), "assistant.message") {
		t.Fatalf("nil assistant pointer should not render: %q", b.String())
	}

	// Non-nil pointer for JobFinishedData.
	b.Reset()
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventJobFinished,
		Data: &events.JobFinishedData{JobID: "job_1", Status: "completed"},
	})
	if !strings.Contains(b.String(), "kind: job.notification") {
		t.Fatalf("pointer JobFinishedData not rendered: %q", b.String())
	}

	// Nil pointer for JobFinishedData.
	b.Reset()
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventJobFinished,
		Data: (*events.JobFinishedData)(nil),
	})
	if strings.Contains(b.String(), "job.notification") {
		t.Fatalf("nil job pointer should not render: %q", b.String())
	}
}

// TestLiveWatchSummariesForReceiver covers liveWatchSummariesForReceiver
// (job_watch.go:2067-2095) including the empty-arg guard and the
// receiver-matching loop.
func TestLiveWatchSummariesForReceiver(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	// Empty args → nil.
	if got := jm.liveWatchSummariesForReceiver("", ""); got != nil {
		t.Fatalf("empty receiver args should return nil, got %+v", got)
	}
	if got := jm.liveWatchSummariesForReceiver("sess", ""); got != nil {
		t.Fatalf("empty delegate id should return nil, got %+v", got)
	}

	// Install a watch with a receiver session/delegate.
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:             rec.JobID,
		Events:             []string{"assistant.tool"},
		ReceiverSessionID:  "recv_session",
		ReceiverDelegateID: "dlg_recv",
	})
	if err != nil {
		t.Fatalf("configureWatch: %v", err)
	}

	// Matching receiver returns one entry.
	entries := jm.liveWatchSummariesForReceiver("recv_session", "dlg_recv")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].ID == "" {
		t.Fatal("entry ID is empty")
	}

	// Non-matching receiver returns zero entries.
	entries = jm.liveWatchSummariesForReceiver("other_session", "dlg_other")
	if len(entries) != 0 {
		t.Fatalf("non-matching entries = %d, want 0: %+v", len(entries), entries)
	}
}

// TestRememberWatchLineageEviction covers the eviction branch of
// rememberWatchLineageLocked (job_watch.go:2352-2356) which fires when the
// lineage key order exceeds watchLineageKeyCap (64).
func TestRememberWatchLineageEviction(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	// Fill the lineage beyond the cap to trigger eviction.
	for i := range watchLineageKeyCap + 2 {
		key := watchKey{
			VisibleSessionID: jm.sessionID,
			Target:           "job_evict_" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			SendTo:           runtimeMessageAliasCaller,
		}
		cfg, err := newWatchConfig(watchArgs{
			Target: key.Target,
			Events: []string{"assistant.tool"},
			Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "m"},
		}, jm.now())
		if err != nil {
			t.Fatalf("newWatchConfig: %v", err)
		}
		jm.mu.Lock()
		jm.rememberWatchLineageLocked(key, cfg)
		jm.mu.Unlock()
	}

	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watchLineageOrder) > watchLineageKeyCap {
		t.Fatalf("lineage order = %d, want <= %d", len(jm.watchLineageOrder), watchLineageKeyCap)
	}
	// The first two keys should have been evicted.
	if len(jm.watchLineage) != len(jm.watchLineageOrder) {
		t.Fatalf("lineage map size %d != order size %d", len(jm.watchLineage), len(jm.watchLineageOrder))
	}
}

// TestClearReceiverWatchByIDTerminalFlushLookup covers the terminalFlush
// lookup branch in clearReceiverWatchByID (job_watch.go:1442-1448) which
// finds a watch config in the terminalFlush set when it's not in jm.watches.
func TestClearReceiverWatchByIDTerminalFlushLookup(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	// Create a watch config that lives only in terminalFlush (not jm.watches).
	cfg, err := newWatchConfig(watchArgs{
		Target:             "job_term",
		Events:             []string{"assistant.tool"},
		ReceiverSessionID:  "recv_session",
		ReceiverDelegateID: "dlg_recv",
		Send:               &watchSendArgs{To: runtimeMessageAliasCaller, Message: "m"},
	}, jm.now())
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}

	jm.mu.Lock()
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[cfg] = true
	jm.mu.Unlock()

	// Clearing with matching receiver should find it in terminalFlush and
	// return a non-watching result.
	res, err := jm.clearReceiverWatchByID(cfg.watchID, "recv_session", "dlg_recv")
	if err != nil {
		t.Fatalf("clearReceiverWatchByID: %v", err)
	}
	if res.Watching {
		t.Fatalf("result should not be watching: %+v", res)
	}

	// Non-matching receiver should not find it.
	res2, err := jm.clearReceiverWatchByID(cfg.watchID, "other", "other")
	if err != nil {
		t.Fatalf("clearReceiverWatchByID non-match: %v", err)
	}
	if res2.Watching {
		t.Fatalf("non-matching result should not be watching: %+v", res2)
	}
}

// TestWriteWatchFrameEventUnknownType covers the default case of
// writeWatchFrameEvent where the event data is an unrecognized type.
func TestWriteWatchFrameEventUnknownType(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writeWatchFrameEvent(&b, events.SessionEvent{
		Kind: events.EventContextCompaction,
		Data: events.WarningData{Message: "compaction"},
	})
	// Unknown types produce no event section.
	if strings.Contains(b.String(), "event:") {
		t.Fatalf("unknown event type should not render event section: %q", b.String())
	}
}
