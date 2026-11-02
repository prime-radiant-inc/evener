package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestManagerWatchAnswerIsNotCommunicate(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := apptranscript.ProjectEntryParts("answer", 1, schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "answer", Kind: schema.TurnUserInput, Message: llm.User("answer"), AnswerToTurnIDs: []string{"question"}}, nil, nil, nil)
	if len(items) != 1 || items[0].Observation == nil {
		t.Fatal("missing actual input observation")
	}
	if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "question"}, Turns: []appwire.Turn{{ID: "answer", Items: items}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID {
			t.Fatal("accepted input created a communicate watch send")
		}
	}
	if !managerWatchActive(jm, watch.WatchID) {
		t.Fatal("accepted input completed watch")
	}
}

func TestManagerWatchResyncProjectedCompletion(t *testing.T) {
	t.Parallel()
	for _, active := range []string{"", "C"} {
		t.Run("resync_active_"+active, func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
			jm := root.jobManager
			const ref, instance, generation = "local:worker", "instance", "7@worker"
			scope := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation}
			watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", nil, true)
			if err != nil {
				t.Fatal(err)
			}
			deliver := func(u appwire.SessionWatchUpdate) {
				t.Helper()
				if err := jm.onHubObservation(watch.WatchID, u); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func(active string, turns []appwire.Turn) appwire.SessionWatchUpdate {
				return appwire.SessionWatchUpdate{Metadata: scope, Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: active}, Turns: turns}}}
			}
			old := managerProjectedTerminalItem(t, "A")
			deliver(snapshot("B", []appwire.Turn{{ID: "A", Items: []appwire.ThreadItem{old}}, {ID: "B"}}))
			if !managerWatchActive(jm, watch.WatchID) {
				t.Fatal("old A completed B watch")
			}
			deliver(appwire.SessionWatchUpdate{Metadata: scope, Notification: &appwire.Notification{Method: appwire.NotifyEvenerThreadResync}})
			// Write and project B only after interruption. No synthetic EntryID or
			// original execution boot is injected into the real projected observation.
			winner := managerProjectedTerminalItem(t, "B")
			recovered := snapshot(active, []appwire.Turn{{ID: "A", Items: []appwire.ThreadItem{old}}, {ID: "B", Items: []appwire.ThreadItem{winner}}})
			if active != "" {
				recovered.Snapshot.Thread.Turns = append(recovered.Snapshot.Thread.Turns, appwire.Turn{ID: active})
			}
			deliver(recovered)
			if managerWatchActive(jm, watch.WatchID) {
				t.Fatal("real projected B terminal in resync did not complete watch")
			}
			deliver(recovered)
			live := *winner.Observation
			live.Ref, live.InstanceID, live.GenerationID = ref, instance, generation
			live.EntryID = winner.TranscriptKey // The live hub adapter's recorded item identity.
			deliver(appwire.SessionWatchUpdate{Event: "communicate", Metadata: live})
			completed, sends := 0, 0
			for _, event := range loadJobStoreEvents(t, jm) {
				if event.WatchID == watch.WatchID && event.Watch != nil && event.Watch.EndReason == "completed" {
					completed++
				}
				if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID {
					sends++
				}
			}
			if completed != 1 || sends != 2 {
				t.Fatalf("completed=%d sends=%d, want one completion and interruption plus terminal", completed, sends)
			}
			if winner.Observation.EntryID != "" || winner.Observation.GenerationID != "" {
				t.Fatal("snapshot adaptation modified recorded provenance")
			}
		})
	}
}

func managerProjectedTerminalItem(t *testing.T, turnID string) appwire.ThreadItem {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	for _, entry := range []schema.Turn{
		{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-" + turnID, Name: "communicate", Arguments: []byte(`{"message":"result"}`)}}}}},
		{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCommunicate, CausalKnown: true, Communicate: &schema.CommunicateInfo{CallID: "call-" + turnID, Message: "result", EndTurn: true, EndReason: "done"}},
		{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call-" + turnID, Name: "communicate", Content: `{"accepted":true,"end_turn":true}`}}}}},
	} {
		if err := w.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := transcriptindex.Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	window, err := index.Latest(20)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range window.Candidates {
		item := candidate.Item
		if item.Observation == nil {
			continue
		}
		m := item.Observation
		if m.EntryID != "" || item.TranscriptKey == "" || m.TurnID != turnID || m.TerminalAccepted == nil || !*m.TerminalAccepted {
			t.Fatalf("unexpected real projection: %+v metadata=%+v", item, m)
		}
		return item
	}
	t.Fatal("no projected terminal observation")
	return appwire.ThreadItem{}
}

func TestManagerWatchPagedCompletionReplay(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "instance", "7@worker"
	scope := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation}
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(u appwire.SessionWatchUpdate) {
		t.Helper()
		if err := jm.onHubObservation(watch.WatchID, u); err != nil {
			t.Fatal(err)
		}
	}
	deliver(appwire.SessionWatchUpdate{Metadata: scope, Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "B"}, Turns: []appwire.Turn{{ID: "B"}}}}})
	deliver(appwire.SessionWatchUpdate{Metadata: scope, Notification: &appwire.Notification{Method: appwire.NotifyEvenerThreadResync}})
	old, winner, newer := managerProjectedTerminalItem(t, "A"), managerProjectedTerminalItem(t, "B"), managerProjectedTerminalItem(t, "C")
	// Latest recovery window contains only C. Older pages produce ordinary
	// recorded communicate evidence, not a manufactured latest-work snapshot.
	deliver(appwire.SessionWatchUpdate{Metadata: scope, Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "C"}, Turns: []appwire.Turn{{ID: "C", Items: []appwire.ThreadItem{newer}}}}}})
	itemUpdate := func(item appwire.ThreadItem) appwire.SessionWatchUpdate {
		m := *item.Observation
		m.Ref, m.InstanceID, m.GenerationID, m.EntryID = ref, instance, generation, item.TranscriptKey
		return appwire.SessionWatchUpdate{Event: "communicate", Metadata: m}
	}
	deliver(itemUpdate(old))
	deliver(itemUpdate(newer))
	if !managerWatchActive(jm, watch.WatchID) {
		t.Fatal("A or C completed the B watch")
	}
	deliver(itemUpdate(winner))
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("paged B evidence did not complete the watch")
	}
	deliver(itemUpdate(winner)) // Duplicate live history/updated after replay.
	deliver(itemUpdate(newer))
	completed, sends := 0, 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchID == watch.WatchID && event.Watch != nil && event.Watch.EndReason == "completed" {
			completed++
		}
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID {
			sends++
		}
	}
	if completed != 1 || sends != 2 {
		t.Fatalf("completed=%d sends=%d, want one completion and interruption plus terminal", completed, sends)
	}
}
