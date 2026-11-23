package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// hubPayloadWatch configures a hub watch for selected events and returns its
// id. It leaves the initial snapshot to the caller so each test seeds the
// status baseline it wants previous_status to start from.
func hubPayloadWatch(t *testing.T, selected []string) (*jobManager, string) {
	t.Helper()
	jm := newTestJM(t)
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "7@worker", "local:manager", selected, false)
	if err != nil {
		t.Fatal(err)
	}
	return jm, watch.WatchID
}

func hubMetadata(entryID string) appwire.SessionObservationMetadata {
	return appwire.SessionObservationMetadata{
		Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker",
		EntryID: entryID, CausalKnown: true,
	}
}

func hubSnapshot(evener appwire.EvenerThread, status appwire.ThreadStatus) appwire.SessionWatchUpdate {
	evener.Ref, evener.InstanceID = "local:worker", "worker-instance"
	return appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{
			BootGeneration: "7@worker",
			Thread:         appwire.Thread{Status: status, Evener: evener},
		},
		Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker"},
	}
}

func hubNotification(entryID, method string, params any) appwire.SessionWatchUpdate {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return appwire.SessionWatchUpdate{
		Event:        hubEventForMethod(method),
		Notification: &appwire.Notification{Method: method, Params: raw},
		Metadata:     hubMetadata(entryID),
	}
}

func hubEventForMethod(method string) string {
	switch method {
	case appwire.NotifyThreadStatusChanged:
		return "status"
	case appwire.NotifyThreadClosed:
		return "status"
	case appwire.NotifyEvenerSandboxEscalationRequested:
		return "approval"
	}
	return ""
}

// watchFrameEventFields parses the indented fields of a frame's event block so
// a test asserts structured values instead of whole-frame prose. A continuation
// line of an indented field is folded back into that field's value.
func watchFrameEventFields(t *testing.T, frame string) map[string]string {
	t.Helper()
	fields := map[string]string{}
	inEvent := false
	last := ""
	for line := range strings.SplitSeq(frame, "\n") {
		switch {
		case line == "event:":
			inEvent, last = true, ""
		case !inEvent:
		case strings.HasPrefix(line, "    ") && last != "":
			fields[last] += "\n" + strings.TrimPrefix(line, "    ")
		case strings.HasPrefix(line, "  "):
			name, value, ok := strings.Cut(strings.TrimPrefix(line, "  "), ": ")
			if !ok {
				t.Fatalf("event field %q is not name: value in frame:\n%s", line, frame)
			}
			fields[name], last = value, name
		default:
			inEvent, last = false, ""
		}
	}
	return fields
}

func lastHubFrame(t *testing.T, jm *jobManager, id string) string {
	t.Helper()
	frames := hubWatchFrameEvents(t, jm, id)
	if len(frames) == 0 {
		t.Fatalf("watch %s delivered no frame", id)
	}
	return frames[len(frames)-1].Frame
}

// TestHubWatchStatusFrameCarriesTransition pins that a live status change tells
// the Manager what changed: the new status, the one it replaces, the active
// flags, and the turn-level state the frame used to omit entirely.
func TestHubWatchStatusFrameCarriesTransition(t *testing.T) {
	t.Parallel()
	jm, id := hubPayloadWatch(t, []string{"status"})
	if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{}, appwire.ThreadStatus{Type: "idle"})); err != nil {
		t.Fatal(err)
	}
	failed, askPending, askClear := 2, true, false
	if err := jm.onHubObservation(id, hubNotification("status-1", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "worker", Ref: "local:worker",
		Status:          appwire.ThreadStatus{Type: "active", ActiveFlags: []string{"model", "tool"}},
		FailedToolCalls: &failed, AskPending: &askPending, ActiveTurnID: "turn-1",
	})); err != nil {
		t.Fatal(err)
	}
	fields := watchFrameEventFields(t, lastHubFrame(t, jm, id))
	for name, want := range map[string]string{
		"kind": "status", "status": "active", "previous_status": "idle",
		"active_flags": "model, tool", "ask_pending": "true", "failed_tool_calls": "2",
		"active_turn_id": "turn-1",
	} {
		if got := fields[name]; got != want {
			t.Fatalf("status frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
		}
	}

	// The tracked status advances, so the next transition names this one and
	// the ended turn it reports.
	zero := 0
	if err := jm.onHubObservation(id, hubNotification("status-2", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "worker", Ref: "local:worker",
		Status:          appwire.ThreadStatus{Type: "idle"},
		FailedToolCalls: &zero, AskPending: &askClear, EndedTurnID: "turn-1",
	})); err != nil {
		t.Fatal(err)
	}
	fields = watchFrameEventFields(t, lastHubFrame(t, jm, id))
	for name, want := range map[string]string{
		"kind": "status", "status": "idle", "previous_status": "active",
		"ask_pending": "false", "failed_tool_calls": "0", "ended_turn_id": "turn-1",
	} {
		if got := fields[name]; got != want {
			t.Fatalf("second status frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
		}
	}
}

func TestHubWatchClosedFrameCarriesStatusAndMethod(t *testing.T) {
	t.Parallel()
	jm, id := hubPayloadWatch(t, []string{"status"})
	if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{}, appwire.ThreadStatus{Type: "active"})); err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubObservation(id, hubNotification("closed-1", appwire.NotifyThreadClosed, appwire.ThreadClosedParams{ThreadID: "worker", Ref: "local:worker"})); err != nil {
		t.Fatal(err)
	}
	fields := watchFrameEventFields(t, lastHubFrame(t, jm, id))
	for name, want := range map[string]string{
		"kind": "status", "status": "closed", "previous_status": "active", "method": appwire.NotifyThreadClosed,
	} {
		if got := fields[name]; got != want {
			t.Fatalf("closed frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
		}
	}
}

// TestHubWatchQuestionFrameCarriesSnapshotQuestion pins that a question
// observation the watcher replays from a snapshot tells the Manager what was
// asked, so the wake does not force a full session read.
func TestHubWatchQuestionFrameCarriesSnapshotQuestion(t *testing.T) {
	t.Parallel()
	jm, id := hubPayloadWatch(t, []string{"question"})
	if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{
		ActiveTurnID: "turn-q", AskPending: true,
		PendingQuestion: &appwire.PendingQuestion{
			Question: "Ship it?\nReally?",
			Options:  []string{"yes", "no"},
			Count:    2,
		},
	}, appwire.ThreadStatus{Type: "awaiting"})); err != nil {
		t.Fatal(err)
	}
	frame := lastHubFrame(t, jm, id)
	fields := watchFrameEventFields(t, frame)
	for name, want := range map[string]string{
		"kind": "question", "question": "Ship it?\nReally?", "options": "yes, no",
		"count": "2", "active_turn_id": "turn-q", "status": "awaiting",
	} {
		if got := fields[name]; got != want {
			t.Fatalf("question frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
		}
	}
	if got := len([]rune(frame)); got > watchFrameMaxChars {
		t.Fatalf("question frame is %d runes, over the %d-rune cap", got, watchFrameMaxChars)
	}
}

func TestHubWatchQuestionFrameBoundsQuestionText(t *testing.T) {
	t.Parallel()
	jm, id := hubPayloadWatch(t, []string{"question"})
	if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{
		ActiveTurnID: "turn-q", AskPending: true,
		PendingQuestion: &appwire.PendingQuestion{Question: strings.Repeat("q", 5000), Count: 1},
	}, appwire.ThreadStatus{Type: "awaiting"})); err != nil {
		t.Fatal(err)
	}
	frame := lastHubFrame(t, jm, id)
	fields := watchFrameEventFields(t, frame)
	question := fields["question"]
	// 1200 is a loose upper bound: it proves the field is cut well below the
	// 4096-rune frame cap without pinning the exact per-field bound here.
	if len([]rune(question)) > 1200 {
		t.Fatalf("question field is %d runes, over its per-field bound", len([]rune(question)))
	}
	if !strings.HasSuffix(question, "truncated]") {
		t.Fatalf("a bounded question does not say it was truncated: %q", question)
	}
	if got := len([]rune(frame)); got > watchFrameMaxChars {
		t.Fatalf("question frame is %d runes, over the %d-rune cap", got, watchFrameMaxChars)
	}
}

// TestHubWatchLiveQuestionFrameCarriesTurnContext pins the fallback for a live
// ask: the hub notification carries no question text, so the frame must at
// least name the status and turn it belongs to.
func TestHubWatchLiveQuestionFrameCarriesTurnContext(t *testing.T) {
	t.Parallel()
	jm, id := hubPayloadWatch(t, []string{"question"})
	askPending := true
	update := hubNotification("question-1", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "worker", Ref: "local:worker",
		Status: appwire.ThreadStatus{Type: "awaiting"}, AskPending: &askPending, ActiveTurnID: "turn-live",
	})
	// The hub sends one status-changed notification and labels the derived
	// question observation itself.
	update.Event = "question"
	if err := jm.onHubObservation(id, update); err != nil {
		t.Fatal(err)
	}
	fields := watchFrameEventFields(t, lastHubFrame(t, jm, id))
	for name, want := range map[string]string{
		"kind": "question", "status": "awaiting", "active_turn_id": "turn-live",
	} {
		if got := fields[name]; got != want {
			t.Fatalf("live question frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
		}
	}
}

// TestHubWatchApprovalFrameCarriesEscalation pins that both the snapshot
// surface-on-entry and the live request deliver the card's identity and the
// redacted fields a human needs to decide.
func TestHubWatchApprovalFrameCarriesEscalation(t *testing.T) {
	t.Parallel()
	jm, id := hubPayloadWatch(t, []string{"approval"})
	if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{
		PendingEscalations: []appwire.SandboxEscalationRequested{{
			ThreadID: "worker", Ref: "local:worker", EscalationID: "esc-snapshot",
			Mode: "workspace-write", Tool: "write_file", Kind: "write",
			DeniedPath: "/outside/file", RequestedPath: "/tmp/link",
		}},
	}, appwire.ThreadStatus{Type: "awaiting"})); err != nil {
		t.Fatal(err)
	}
	fields := watchFrameEventFields(t, lastHubFrame(t, jm, id))
	for name, want := range map[string]string{
		"kind": "approval", "escalation_id": "esc-snapshot", "tool": "write_file",
		"kind_detail": "write", "mode": "workspace-write", "path": "/outside/file",
		"requested_path": "/tmp/link",
	} {
		if got := fields[name]; got != want {
			t.Fatalf("snapshot approval frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
		}
	}

	if err := jm.onHubObservation(id, hubNotification("approval-1", appwire.NotifyEvenerSandboxEscalationRequested, appwire.SandboxEscalationRequested{
		ThreadID: "worker", Ref: "local:worker", EscalationID: "esc-live",
		Mode: "workspace-write", Tool: "edit_file", Kind: "write", DeniedPath: "/outside/other",
	})); err != nil {
		t.Fatal(err)
	}
	fields = watchFrameEventFields(t, lastHubFrame(t, jm, id))
	for name, want := range map[string]string{
		"kind": "approval", "escalation_id": "esc-live", "tool": "edit_file", "path": "/outside/other",
	} {
		if got := fields[name]; got != want {
			t.Fatalf("live approval frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
		}
	}
	if _, has := fields["requested_path"]; has {
		t.Fatalf("an escalation without a requested path renders one: %v", fields)
	}
}

// TestHubWatchObservationDegradesWithoutNotification pins the older-peer and
// malformed-notification cases: the frame still names the event kind, omits
// what it cannot know, and raises no error.
func TestHubWatchObservationDegradesWithoutNotification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		entry string
		raw   string
	}{
		{"no notification", "status-none", ""},
		{"malformed params", "status-bad", "{not-json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			jm, id := hubPayloadWatch(t, []string{"status"})
			update := appwire.SessionWatchUpdate{Event: "status", Metadata: hubMetadata(tc.entry)}
			if tc.raw != "" {
				update.Notification = &appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: json.RawMessage(tc.raw)}
			}
			if err := jm.onHubObservation(id, update); err != nil {
				t.Fatal(err)
			}
			frame := lastHubFrame(t, jm, id)
			fields := watchFrameEventFields(t, frame)
			if got := fields["kind"]; got != "status" {
				t.Fatalf("degraded frame kind = %q, want the event kind named (fields %v)", got, fields)
			}
			if _, has := fields["status"]; has {
				t.Fatalf("degraded frame invents a status it cannot know: %v", fields)
			}
		})
	}
}

// hubWatchFrameEvents returns the pending watch-send states recorded for id.
func hubWatchFrameEvents(t *testing.T, jm *jobManager, id string) []jobstore.WatchSendState {
	t.Helper()
	var got []jobstore.WatchSendState
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == id {
			got = append(got, *event.WatchSend)
		}
	}
	return got
}
