package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

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
			field := strings.TrimPrefix(line, "  ")
			name, value, ok := strings.Cut(field, ": ")
			if !ok && strings.HasSuffix(field, ":") {
				// A header whose value is a list on the following lines.
				name, value = strings.TrimSuffix(field, ":"), ""
			} else if !ok {
				t.Fatalf("event field %q is not name: value in frame:\n%s", line, frame)
			}
			fields[name], last = value, name
		default:
			inEvent, last = false, ""
		}
	}
	return fields
}

// watchFrameList reads a list-valued event field into its entries, so a test
// asserts each entry's boundary rather than splitting on a separator.
func watchFrameList(t *testing.T, fields map[string]string, name string) []string {
	t.Helper()
	var entries []string
	for line := range strings.SplitSeq(fields[name], "\n") {
		if entry, ok := strings.CutPrefix(line, "- "); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func lastHubFrame(t *testing.T, jm *jobManager, id string) string {
	t.Helper()
	frames := hubWatchPendingSends(t, jm, id)
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
	t.Run("close names the status it ended", func(t *testing.T) {
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
	})
	// On shutdown the daemon emits thread/status/changed(closed) immediately
	// before thread/closed. The pair is one transition: a throttled status half
	// must not turn the delivered close into a closed-to-closed frame.
	t.Run("every-Nth shutdown sequence keeps the pre-close status", func(t *testing.T) {
		t.Parallel()
		jm := newTestJM(t)
		watch, err := jm.configureHubWatchContext(context.Background(), "local:worker", "worker-instance", "7@worker", "local:manager", []string{"status"}, false, "", 2)
		if err != nil {
			t.Fatal(err)
		}
		id := watch.WatchID
		if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{}, appwire.ThreadStatus{Type: appwire.ThreadStatusIdle})); err != nil {
			t.Fatal(err)
		}
		if err := jm.onHubObservation(id, hubNotification("status-closed", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			ThreadID: "worker", Ref: "local:worker", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		})); err != nil {
			t.Fatal(err)
		}
		if err := jm.onHubObservation(id, hubNotification("closed-1", appwire.NotifyThreadClosed, appwire.ThreadClosedParams{ThreadID: "worker", Ref: "local:worker"})); err != nil {
			t.Fatal(err)
		}
		sends := hubWatchPendingSends(t, jm, id)
		if len(sends) != 1 {
			t.Fatalf("delivered %d frames, want 1: the every-Nth status half must be throttled", len(sends))
		}
		fields := watchFrameEventFields(t, sends[0].Frame)
		for name, want := range map[string]string{
			"kind": "status", "status": appwire.ThreadStatusClosed, "previous_status": appwire.ThreadStatusIdle, "method": appwire.NotifyThreadClosed,
		} {
			if got := fields[name]; got != want {
				t.Fatalf("closed frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
			}
		}
	})
}

// TestHubWatchQuestionFrameCarriesSnapshotQuestion pins that a question
// observation the watcher replays from a snapshot tells the Manager what was
// asked, so the wake does not force a full session read.
func TestHubWatchQuestionFrameCarriesSnapshotQuestion(t *testing.T) {
	t.Parallel()
	t.Run("running ask names its active turn", func(t *testing.T) {
		t.Parallel()
		jm, id := hubPayloadWatch(t, []string{"question"})
		if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{
			ActiveTurnID: "turn-q", AskPending: true,
			PendingQuestion: &appwire.PendingQuestion{
				Question: "Ship it?\nReally?",
				// One label carries the separator the old join used, so a
				// comma-joined frame would read as three choices.
				Options: []string{"Deploy, then monitor", "Wait"},
				Count:   2,
			},
		}, appwire.ThreadStatus{Type: "awaiting"})); err != nil {
			t.Fatal(err)
		}
		frame := lastHubFrame(t, jm, id)
		fields := watchFrameEventFields(t, frame)
		for name, want := range map[string]string{
			"kind": "question", "question": "Ship it?\nReally?",
			"count": "2", "active_turn_id": "turn-q", "status": "awaiting",
		} {
			if got := fields[name]; got != want {
				t.Fatalf("question frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
			}
		}
		if got, want := watchFrameList(t, fields, "options"), []string{"Deploy, then monitor", "Wait"}; !slices.Equal(got, want) {
			t.Fatalf("question frame options = %q, want %q (frame fields %v)", got, want, fields)
		}
		if got := len([]rune(frame)); got > watchFrameMaxChars {
			t.Fatalf("question frame is %d runes, over the %d-rune cap", got, watchFrameMaxChars)
		}
	})
	// A settled ask has no active turn in the snapshot: the question's own turn
	// is the ended turn the daemon reports live, and the frame must still name
	// the turn the Manager has to read to answer.
	t.Run("settled ask names the question's turn", func(t *testing.T) {
		t.Parallel()
		jm, id := hubPayloadWatch(t, []string{"question"})
		if err := jm.onHubObservation(id, hubSnapshot(appwire.EvenerThread{
			AskPending: true,
			PendingQuestion: &appwire.PendingQuestion{
				Question: "Ship it?",
				Count:    1,
				TurnID:   "turn-ask",
			},
		}, appwire.ThreadStatus{Type: "awaiting"})); err != nil {
			t.Fatal(err)
		}
		fields := watchFrameEventFields(t, lastHubFrame(t, jm, id))
		for name, want := range map[string]string{
			"kind": "question", "question": "Ship it?", "count": "1",
			"status": "awaiting", "ended_turn_id": "turn-ask",
		} {
			if got := fields[name]; got != want {
				t.Fatalf("settled question frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
			}
		}
		if got := fields["active_turn_id"]; got != "" {
			t.Fatalf("settled question frame active_turn_id = %q, want none", got)
		}
	})
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
	askPending := true
	t.Run("active ask names the running turn", func(t *testing.T) {
		t.Parallel()
		jm, id := hubPayloadWatch(t, []string{"question"})
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
		if got := fields["ended_turn_id"]; got != "" {
			t.Fatalf("live active question frame ended_turn_id = %q, want none", got)
		}
	})
	// A turn that ends on ask_user settles into awaiting with the active turn
	// cleared, so the ask is identified only by the turn the daemon ended.
	t.Run("settled ask names the ended turn", func(t *testing.T) {
		t.Parallel()
		jm, id := hubPayloadWatch(t, []string{"question"})
		update := hubNotification("question-1", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			ThreadID: "worker", Ref: "local:worker",
			Status: appwire.ThreadStatus{Type: "awaiting"}, AskPending: &askPending,
			EndedTurnID: "turn-ask",
		})
		update.Event = "question"
		if err := jm.onHubObservation(id, update); err != nil {
			t.Fatal(err)
		}
		fields := watchFrameEventFields(t, lastHubFrame(t, jm, id))
		for name, want := range map[string]string{
			"kind": "question", "status": "awaiting", "ended_turn_id": "turn-ask",
		} {
			if got := fields[name]; got != want {
				t.Fatalf("settled question frame %s = %q, want %q (frame fields %v)", name, got, want, fields)
			}
		}
		if got := fields["active_turn_id"]; got != "" {
			t.Fatalf("settled question frame active_turn_id = %q, want none", got)
		}
	})
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
