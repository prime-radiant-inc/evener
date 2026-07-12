//go:build serffuzz

package agent

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

// FuzzWatchPendingFrameProgram exercises two deterministic job-watch seams:
// durable pending-send history selection and model-facing frame rendering. The
// history oracle models a pending -> terminal -> pending lifecycle, while the
// renderer oracle checks bounded, normalized, typed output for value and pointer
// event payloads. Neither phase needs a Session, provider, process, or real clock.
func FuzzWatchPendingFrameProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0, 0},
		{1, 2, 3, 4, 5, 6},
		{255, 254, 253, 252, 251},
		{0, 0, 1, 1},
		{0, 0, 2, 2},
		{0, 0, 3, 3},
		{0, 0, 0, 5, 1},
		{0, 0, 0, 6, 7},
		{0, 0, 0, 7, 8},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &wpfpReader{data: data}
		wpfpPendingHistory(t, r)
		wpfpTerminalSnapshots(t, r)
		wpfpRenderFrames(t, r)
	})
}

type wpfpReader struct {
	data []byte
	pos  int
}

func (r *wpfpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *wpfpReader) text(prefix string) string {
	return prefix + []string{"", "alpha", "line1\r\nline2", strings.Repeat("x", 1100)}[int(r.next())%4]
}

func wpfpPendingHistory(t *testing.T, r *wpfpReader) {
	t.Helper()
	key := jobstore.WatchSendKey{
		VisibleSessionID:        "session_visible",
		WatchID:                 "watch_primary",
		WatchTarget:             "job_target",
		ResolvedWatchedIdentity: "job_target",
		ResolvedSendTo:          "dlg_receiver",
		WatchGeneration:         "generation_primary",
	}
	other := key
	other.WatchID = "watch_other"
	first := jobstore.WatchSendState{Key: key, DeliveryID: "delivery_first", UpdateSeq: 1}
	current := jobstore.WatchSendState{Key: key, DeliveryID: "delivery_current", UpdateSeq: 2}
	eventsLog := []jobstore.Event{
		{Kind: jobstore.EventWatchSendPending, Seq: 1, WatchSend: &first},
		{Kind: jobstore.EventWatchSendPending, Seq: 2, WatchSend: &jobstore.WatchSendState{Key: other, DeliveryID: "other", UpdateSeq: 1}},
		{Kind: jobstore.EventWatchSendDelivered, Seq: 3, WatchSend: &first},
		{Kind: jobstore.EventWatchSendPending, Seq: 4, WatchSend: &current},
		{Kind: jobstore.EventDelegateStopGateClosed, Seq: 5, DelegateID: "dlg_receiver"},
	}
	if r.next()&1 != 0 {
		eventsLog[2].Kind = jobstore.EventWatchSendDropped
	}
	if got := watchSendCurrentPendingSeq(eventsLog, current); got != 4 {
		t.Fatalf("current pending seq = %d, want 4", got)
	}
	if got := watchSendPendingCreationSeq(eventsLog, current); got != 4 {
		t.Fatalf("pending creation seq = %d, want 4 after terminal boundary", got)
	}
	legacy := current
	legacy.DeliveryID = ""
	legacy.UpdateSeq = 0
	if got := watchSendCurrentPendingSeq(eventsLog, legacy); got != 4 {
		t.Fatalf("legacy current pending seq = %d, want latest 4", got)
	}
	if watchSendEventMatchesKey(nil, key) || watchSendEventMatchesState(nil, current) {
		t.Fatal("nil durable state matched a watch-send key")
	}
	if watchSendEventMatchesState(&first, current) {
		t.Fatal("different delivery/update state matched current delivery")
	}
	for _, kind := range []jobstore.EventKind{jobstore.EventWatchSendDelivered, jobstore.EventWatchSendDropped, jobstore.EventWatchSendEvicted} {
		if !isWatchSendTerminalEvent(kind) {
			t.Fatalf("terminal kind %q not recognized", kind)
		}
	}
	if isWatchSendTerminalEvent(jobstore.EventWatchSendPending) {
		t.Fatal("pending event classified terminal")
	}
}

func wpfpTerminalSnapshots(t *testing.T, r *wpfpReader) {
	t.Helper()
	key := jobstore.WatchSendKey{
		VisibleSessionID: "session_visible", WatchID: "watch_snap", WatchTarget: "job_snap",
		ResolvedWatchedIdentity: "job_snap", ResolvedSendTo: "dlg_snap", WatchGeneration: "gen_snap",
	}
	state := &jobstore.WatchSendState{Key: key, DeliveryID: "delivery_snap", UpdateSeq: 7}
	cfg := &watchConfig{
		watchID: "watch_snap", target: "job_snap", receiverSessionID: "receiver_session",
		receiverDelegateID: "receiver_delegate", send: &watchSendArgs{To: "dlg_snap"},
		pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: state}, pendingOrder: []jobstore.WatchSendKey{key},
	}
	request := watchKey{VisibleSessionID: "session_visible", Target: "job_snap", SendTo: "dlg_snap", ReceiverSessionID: "receiver_session", ReceiverDelegateID: "receiver_delegate"}
	snapshot := watchSendTerminalSnapshotMatchingKeyLocked(cfg, request, jobstore.EventWatchSendDropped, "fixture drop", time.Unix(int64(r.next()), 0))
	if len(snapshot.events) != 1 || snapshot.events[0].WatchSend == nil || snapshot.events[0].WatchSend.DiagnosticReason != "fixture drop" {
		t.Fatalf("matching terminal snapshot = %+v", snapshot)
	}
	if state.DiagnosticReason != "" {
		t.Fatal("terminal snapshot mutated live pending state")
	}
	if !watchConfigMatchesWatchKey(cfg, request) || !watchConfigSendToMatchesWatchKey(cfg, request) || !watchConfigReceiverMatchesWatchKey(cfg, request) {
		t.Fatal("matching watch config rejected its exact public key")
	}
	wrongReceiver := request
	wrongReceiver.ReceiverSessionID = "someone_else"
	if watchConfigMatchesWatchKey(cfg, wrongReceiver) || watchConfigSendToMatchesWatchKey(cfg, wrongReceiver) {
		t.Fatal("receiver-scoped watch matched a different receiver")
	}
	alias := request
	alias.SendTo = runtimeMessageAliasWatched
	if !watchConfigMatchesWatchKey(cfg, alias) {
		t.Fatal("historical watched alias did not match watch configuration")
	}
	wrongTarget := request
	wrongTarget.Target = "job_other"
	if got := watchSendTerminalSnapshotMatchingKeyLocked(cfg, wrongTarget, jobstore.EventWatchSendDropped, "no", time.Time{}); len(got.events) != 0 {
		t.Fatalf("wrong target snapshot emitted events: %+v", got.events)
	}
}

func wpfpRenderFrames(t *testing.T, r *wpfpReader) {
	t.Helper()
	text := r.text("message-")
	selector := int(r.next()) % 8
	var data events.EventData
	switch selector {
	case 0:
		data = events.AssistantTextEndData{Text: text, Model: "model-x", FinishReason: "stop"}
	case 1:
		data = &events.AssistantTextEndData{Text: text, Model: "model-x", FinishReason: "stop"}
	case 2:
		data = events.ToolCallEndData{ToolName: "read_file", CallID: "call-1", ArgumentsJSON: "{}", Output: text}
	case 3:
		data = &events.ToolCallEndData{ToolName: "read_file", CallID: "call-1", ArgumentsJSON: "{}", Error: text}
	case 4:
		data = events.CommunicateData{Message: text, EndTurn: r.next()&1 != 0}
	case 5:
		data = &events.CommunicateData{Message: text, EndTurn: r.next()&1 != 0}
	case 6:
		exit := int(r.next())
		data = events.JobFinishedData{JobID: "job_child", JobType: "delegate", Status: "completed", Reason: text, ExitCode: &exit, OutputBytes: int64(r.next())}
	default:
		data = &events.JobFinishedData{JobID: "job_child", JobType: "delegate", Status: "failed", Reason: text, OutputBytes: int64(r.next())}
	}
	p := provenance.WithWatch(nil, "watch_source", "generation_source", "delivery_source", "session_source", "job_source")
	var first, second strings.Builder
	writeWatchFrameProvenance(&first, p)
	writeWatchFrameEvent(&first, events.SessionEvent{Data: data})
	writeWatchFrameProvenance(&second, p)
	writeWatchFrameEvent(&second, events.SessionEvent{Data: data})
	if first.String() != second.String() {
		t.Fatalf("frame rendering is non-deterministic:\nfirst=%q\nsecond=%q", first.String(), second.String())
	}
	frame := first.String()
	if strings.Contains(frame, "\r") || !strings.Contains(frame, "watch_id: watch_source") || !strings.Contains(frame, "latest_delivery_id: delivery_source") || !strings.Contains(frame, "event:\n") {
		t.Fatalf("malformed normalized frame: %q", frame)
	}
	var external strings.Builder
	writeWatchFrameProvenance(&external, nil)
	if external.String() != "provenance: external\n" {
		t.Fatalf("external provenance = %q", external.String())
	}

	unicodeText := "a\u03b2\u4e16\u754c"
	for _, max := range []int{-1, 0, 1, 3, utf8.RuneCountInString(unicodeText), 100} {
		got := limitWatchText(unicodeText, max)
		if !utf8.ValidString(got) {
			t.Fatalf("limitWatchText split UTF-8 at max %d: %q", max, got)
		}
		if max > 0 && utf8.RuneCountInString(got) > max {
			t.Fatalf("limitWatchText returned %d runes for max %d", utf8.RuneCountInString(got), max)
		}
	}
}
