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
	initialEntries := rosterModelEntries(t, initial, watchID)
	if len(initialEntries) != 2 || initialEntries[first.Evener.Ref].Ref != first.Evener.Ref || initialEntries[second.Evener.Ref].Ref != second.Evener.Ref {
		t.Fatalf("initial roster refs=%+v, want both workers", initialEntries)
	}
	assertRosterExcludesManager(t, initialEntries, managerRef, manager.SessionID)
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
		entries := rosterModelEntries(t, wake, watchID)
		assertRosterExcludesManager(t, entries, managerRef, manager.SessionID)
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
	postClearEntries := rosterModelEntries(t, postClear, "")
	assertRosterExcludesManager(t, postClearEntries, managerRef, cleared.Thread.SessionID)
	postClear.RespondToolCall("communicate", communicateArgs("new snapshot received"))
	last := start(false)
	for {
		wake := managerCall()
		entries := rosterModelEntries(t, wake, "")
		assertRosterExcludesManager(t, entries, managerRef, cleared.Thread.SessionID)
		wake.RespondToolCall("communicate", communicateArgs("post-Clear delta received"))
		if entries[last.Evener.Ref].Kind == "new" {
			break
		}
	}
}

func rosterModelEntries(t *testing.T, call *fakellm.Call, watchID string) map[string]appwire.RosterEvent {
	t.Helper()
	if watchID != "" && !call.Contains(watchID) {
		t.Fatalf("Manager model round lacks roster watch ID %s", watchID)
	}
	texts := call.Texts()
	for i := len(texts) - 1; i >= 0; i-- {
		text := texts[i]
		const marker = "roster_events: "
		start := strings.Index(text, marker)
		if start < 0 {
			continue
		}
		line := strings.SplitN(text[start+len(marker):], "\n", 2)[0]
		var events []appwire.RosterEvent
		if err := json.Unmarshal([]byte(line), &events); err != nil {
			t.Fatalf("decode model-visible roster events: %v", err)
		}
		entries := make(map[string]appwire.RosterEvent, len(events))
		for _, event := range events {
			if event.Ref == "" || entries[event.Ref].Ref != "" {
				t.Fatalf("invalid or duplicate roster ref: %+v", events)
			}
			entries[event.Ref] = event
		}
		return entries
	}
	t.Fatal("Manager model call contains no delivered roster frame")
	return nil
}

func assertRosterExcludesManager(t *testing.T, entries map[string]appwire.RosterEvent, ref, sessionID string) {
	t.Helper()
	for _, entry := range entries {
		if entry.Ref == ref || entry.Ref == "local:"+sessionID || entry.ID == sessionID {
			t.Fatalf("Manager appears in roster frame: %+v", entry)
		}
	}
}
