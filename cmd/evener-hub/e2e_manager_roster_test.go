package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestManagerRosterEmptyInitial(t *testing.T) {
	stack, provider := startE2EHubStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	human := stack.dialRPC(ctx, t)
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model, Manager: true,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := started.Thread
	t.Cleanup(func() {
		shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		client := stack.dialRPC(shutdownCtx, t)
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: manager.Evener.Ref}); err != nil {
			t.Errorf("shutdown Manager: %v", err)
		}
	})
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: manager.Evener.Ref, ExpectedInstanceID: manager.Evener.InstanceID, ClientMutationID: newMutationID(t),
		Input: []appwire.InputItem{{Type: "text", Text: "EMPTY-ROSTER-OPENING"}},
	}); err != nil {
		t.Fatal(err)
	}
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	call.RespondToolCall("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}})
	call, err = provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	if !call.Contains("roster_initial: true") {
		call.RespondToolCall("communicate", communicateArgs("empty roster armed"))
		call, err = provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
	}
	frames := rosterModelFrames(t, call)
	if !call.Contains("roster_initial: true") || len(frames) != 1 || len(frames[0]) != 0 {
		t.Fatalf("Manager-only initial frames=%+v, want one authoritative empty snapshot", frames)
	}
	call.RespondToolCall("communicate", communicateArgs("empty snapshot received"))
}

// The model is scripted; session creation, questions, watch registration and
// delivered notifications all cross a disposable hub and real daemons.
func TestManagerRosterEndToEnd(t *testing.T) {
	stack, provider := startE2EHubStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	human := stack.dialRPC(ctx, t)
	start := func(manager bool) appwire.Thread {
		t.Helper()
		out, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{
			Harness: "evener", CWD: stack.workDir, Model: stack.model, Manager: manager,
			LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ref := out.Thread.Evener.Ref
		t.Cleanup(func() {
			shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			client := stack.dialRPC(shutdownCtx, t)
			if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
				t.Errorf("shutdown %s: %v", ref, err)
			}
		})
		return out.Thread
	}
	first, second := start(false), start(false)
	const opening = "ROSTER-MANAGER-OPENING"
	startTurn := func(thread appwire.Thread, input string) {
		t.Helper()
		if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{
			Ref: thread.Evener.Ref, ExpectedInstanceID: thread.Evener.InstanceID, ClientMutationID: newMutationID(t),
			Input: []appwire.InputItem{{Type: "text", Text: input}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(worker appwire.Thread, marker string) {
		t.Helper()
		startTurn(worker, marker)
		questionCall, err := provider.Next(ctx.Done())
		if err != nil || !questionCall.Contains(marker) {
			t.Fatalf("expected worker model round: %v, call=%v", err, questionCall)
		}
		questionCall.RespondToolCall("ask_user", map[string]any{"questions": []any{map[string]any{
			"question": "Proceed?", "options": []any{map[string]any{"label": "Yes", "detail": "Proceed"}, map[string]any{"label": "No", "detail": "Wait"}},
		}}})
		awaitThread(ctx, t, human, worker.Evener.Ref, "pending worker question", func(thread appwire.Thread) bool { return thread.Evener.AskPending })
	}
	ask(first, "ROSTER-WORKER-FIRST")
	// A published attention transition is an actual hub tick, so the broker
	// has both workers before the Manager subscribes. No timer guesses when
	// the hub's asynchronous attention worker has caught up.
	attentionPublished := false
	for !attentionPublished {
		select {
		case notice := <-human.Notifications():
			if notice.Method != appwire.NotifyEvenerAttentionChanged {
				continue
			}
			var changed appwire.AttentionChangedPayload
			if err := json.Unmarshal(notice.Params, &changed); err != nil {
				t.Fatal(err)
			}
			for _, entry := range changed.Changed {
				attentionPublished = attentionPublished || entry.Level == "needs_you" && entry.AskPending
			}
		case <-ctx.Done():
			t.Fatal("hub did not publish the worker question before Manager subscribed")
		}
	}
	manager := start(true)
	managerRef := manager.Evener.Ref
	startTurn(manager, opening)
	managerCall := func() *fakellm.Call {
		t.Helper()
		call, err := provider.Next(ctx.Done())
		if err != nil || !call.Contains(opening) {
			t.Fatalf("expected Manager model round: %v, call=%v", err, call)
		}
		return call
	}
	call := managerCall()
	call.RespondToolCall("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}})
	call = managerCall()
	watches, err := clientRequest[appwire.SessionWatchesResponse](ctx, human, appwire.MethodEvenerThreadWatchesList, appwire.SessionActivityListParams{Ref: managerRef})
	if err != nil {
		t.Fatal(err)
	}
	watchID, watchCount := "", 0
	for _, row := range watches.Watches {
		if row.State == appwire.SessionWatchStateArmed && row.Watch.Source == "top_level" {
			watchID = row.Watch.ID
			watchCount++
		}
	}
	if watchCount != 1 {
		t.Fatalf("armed aggregate roster watch count=%d, want 1: %+v", watchCount, watches.Watches)
	}
	// The initial notification is delivered to the model, not read out of the
	// Manager's pending journal. It must contain every existing ordinary root.
	initial := call
	if !initial.Contains("roster_initial: true") {
		call.RespondToolCall("communicate", communicateArgs("watch armed"))
		initial = managerCall()
	}
	if !initial.Contains("roster_initial: true") {
		t.Fatal("first delivered roster frame is not marked as a full snapshot")
	}
	initialEntries := rosterModelEntries(t, initial, watchID, managerRef, manager.SessionID)
	if len(initialEntries) != 2 || initialEntries[first.Evener.Ref].Ref != first.Evener.Ref || initialEntries[second.Evener.Ref].Ref != second.Evener.Ref {
		t.Fatalf("initial roster refs=%+v, want both workers", initialEntries)
	}
	if entry := initialEntries[first.Evener.Ref]; entry.Level != "needs_you" || !entry.AskPending {
		t.Fatalf("initial question state=%+v", entry)
	}
	// Keep the Manager's model call open while two meaningful changes queue.
	ask(second, "ROSTER-WORKER-SECOND")
	barrier := start(false)
	initial.RespondToolCall("communicate", communicateArgs("snapshot received"))
	seen := map[string]bool{first.Evener.Ref: true}
	// Drain through the new session's notification to prove both queued
	// changes were delivered, whether merged or in separate frames.
	barrierSeen := false
	for !barrierSeen || len(seen) != 2 {
		wake := managerCall()
		entries := rosterModelEntries(t, wake, watchID, managerRef, manager.SessionID)
		for ref, entry := range entries {
			if ref == barrier.Evener.Ref && entry.Kind == "new" {
				barrierSeen = true
			}
			if (ref == first.Evener.Ref || ref == second.Evener.Ref) && entry.Level == "needs_you" && entry.AskPending {
				seen[ref] = true
			}
		}
		wake.RespondToolCall("communicate", communicateArgs("roster delta received"))
	}
	// Seeing barrier's new entry above proves the daemon already holds its old
	// session in the roster baseline before Clear replaces that session ID.
	awaitThread(ctx, t, human, barrier.Evener.Ref, "worker Clear capability", func(thread appwire.Thread) bool { return thread.Evener.Capabilities.Clear })
	clearedWorker, err := clientRequest[appwire.ThreadClearResponse](ctx, human, appwire.MethodThreadClear, appwire.ThreadClearParams{
		Ref: barrier.Evener.Ref, ExpectedInstanceID: barrier.Evener.InstanceID, ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if clearedWorker.Thread.Evener.Ref != barrier.Evener.Ref || clearedWorker.Thread.SessionID == barrier.SessionID {
		t.Fatalf("worker Clear did not retain workspace and replace session: old=%+v new=%+v", barrier, clearedWorker.Thread)
	}
	workerClear := managerCall()
	workerClearEntries := rosterModelEntries(t, workerClear, watchID, managerRef, manager.SessionID)
	if entry := workerClearEntries[barrier.Evener.Ref]; entry.Ref != barrier.Evener.Ref || entry.Kind != "new" {
		t.Fatalf("worker Clear delivered entry=%+v, want live replacement", entry)
	}
	workerClear.RespondToolCall("communicate", communicateArgs("worker Clear received"))
	// Clear retains the workspace ref, but replaces the Manager session ID.
	awaitThread(ctx, t, human, managerRef, "Manager Clear capability", func(thread appwire.Thread) bool { return thread.Evener.Capabilities.Clear })
	cleared, err := clientRequest[appwire.ThreadClearResponse](ctx, human, appwire.MethodThreadClear, appwire.ThreadClearParams{
		Ref: managerRef, ExpectedInstanceID: manager.Evener.InstanceID, ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Thread.Evener.Ref != managerRef || cleared.Thread.SessionID == manager.SessionID || !cleared.Thread.Evener.Manager {
		t.Fatalf("Clear did not preserve Manager workspace while replacing session ID: old=%+v new=%+v", manager.Evener, cleared.Thread.Evener)
	}
	startTurn(cleared.Thread, opening)
	call = managerCall()
	call.RespondToolCall("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}})
	call = managerCall()
	postClear := call
	if !postClear.Contains("roster_initial: true") {
		call.RespondToolCall("communicate", communicateArgs("watch rearmed"))
		postClear = managerCall()
	}
	if !postClear.Contains("roster_initial: true") {
		t.Fatal("post-Clear roster frame is not marked as a fresh snapshot")
	}
	postClearEntries := rosterModelEntries(t, postClear, "", managerRef, cleared.Thread.SessionID)
	if len(postClearEntries) != 3 {
		t.Fatalf("post-Clear snapshot has %d refs, want three workers: %+v", len(postClearEntries), postClearEntries)
	}
	for _, worker := range []appwire.Thread{first, second, barrier} {
		if postClearEntries[worker.Evener.Ref].Ref != worker.Evener.Ref {
			t.Fatalf("post-Clear snapshot missed %s: %+v", worker.Evener.Ref, postClearEntries)
		}
	}
	postClear.RespondToolCall("communicate", communicateArgs("new snapshot received"))
	last := start(false)
	for {
		wake := managerCall()
		entries := rosterModelEntries(t, wake, "", managerRef, cleared.Thread.SessionID)
		wake.RespondToolCall("communicate", communicateArgs("post-Clear delta received"))
		if entries[last.Evener.Ref].Kind == "new" {
			break
		}
	}
}

func rosterModelEntries(t *testing.T, call *fakellm.Call, watchID, managerRef, sessionID string) map[string]appwire.RosterEvent {
	t.Helper()
	if watchID != "" && !call.Contains(watchID) {
		t.Fatalf("Manager model round lacks roster watch ID %s", watchID)
	}
	frames := rosterModelFrames(t, call)
	for _, entries := range frames {
		assertRosterExcludesManager(t, entries, managerRef, sessionID)
	}
	return frames[len(frames)-1]
}

func rosterModelFrames(t *testing.T, call *fakellm.Call) []map[string]appwire.RosterEvent {
	t.Helper()
	var frames []map[string]appwire.RosterEvent
	for _, text := range call.Texts() {
		for line := range strings.SplitSeq(text, "\n") {
			const marker = "roster_events: "
			_, payload, found := strings.Cut(line, marker)
			if !found {
				continue
			}
			var events []appwire.RosterEvent
			if err := json.Unmarshal([]byte(payload), &events); err != nil {
				t.Fatalf("decode model-visible roster events: %v", err)
			}
			entries := make(map[string]appwire.RosterEvent, len(events))
			for _, event := range events {
				if event.Ref == "" || entries[event.Ref].Ref != "" {
					t.Fatalf("invalid or duplicate roster ref: %+v", events)
				}
				entries[event.Ref] = event
			}
			frames = append(frames, entries)
		}
	}
	if len(frames) == 0 {
		t.Fatal("Manager model call contains no delivered roster frame")
	}
	return frames
}

func TestRosterModelFramesReplay(t *testing.T) {
	frame := func(ref string) string {
		t.Helper()
		encoded, err := json.Marshal([]appwire.RosterEvent{{Ref: ref, Level: "idle", Kind: "new"}})
		if err != nil {
			t.Fatal(err)
		}
		return "roster_events: " + string(encoded) + "\n"
	}
	call := &fakellm.Call{Body: map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": frame("local:manager") + frame("local:older-worker")},
		map[string]any{"role": "user", "content": frame("local:latest-worker")},
	}}}
	frames := rosterModelFrames(t, call)
	if len(frames) != 3 || frames[0]["local:manager"].Ref == "" || frames[1]["local:older-worker"].Ref == "" || frames[2]["local:latest-worker"].Ref == "" {
		t.Fatalf("replayed roster frames=%+v, want all three in wire order", frames)
	}
}

func assertRosterExcludesManager(t *testing.T, entries map[string]appwire.RosterEvent, ref, sessionID string) {
	t.Helper()
	for _, entry := range entries {
		if entry.Ref == ref || entry.Ref == "local:"+sessionID || entry.ID == sessionID {
			t.Fatalf("Manager appears in roster frame: %+v", entry)
		}
	}
}
