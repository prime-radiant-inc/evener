package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// TRIPWIRE: this test uses only the scripted provider boundary and a local
// transcript index; it has no network or model I/O.
func TestManagerWatchModelSwitchExecutionAnchor(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "execution A completed") },
	}}
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(adapter),
		withConfig(SessionConfig{StateDir: t.TempDir(), testOnly: testConfig{skipGitSnapshot: true}}),
	)
	if _, err := sess.ProcessInput(t.Context(), "completed execution A", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if err := sess.SetModel("gpt-4.1-mini"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	index, err := transcriptindex.Open(sess.TranscriptPath(), filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	window, err := index.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	var turns []appwire.Turn
	for _, candidate := range window.Candidates {
		if len(turns) == 0 || turns[len(turns)-1].ID != candidate.TurnID {
			turn := candidate.Turn
			turn.Items = nil
			turns = append(turns, turn)
		}
		turns[len(turns)-1].Items = append(turns[len(turns)-1].Items, candidate.Item)
	}
	if len(turns) < 2 {
		t.Fatalf("indexed projection has %d turns, want completed execution plus model-switch turn", len(turns))
	}
	gapID := turns[len(turns)-1].ID
	executionID := ""
	var completedExecution, projectedModelSwitch bool
	for _, entry := range answerLinkRecords(t, sess) {
		if entry.TurnKind == schema.TurnSpanExecution && executionID == "" {
			executionID = entry.TurnID
		}
		if entry.Kind == schema.TurnCompletion && entry.Completion != nil && entry.Completion.Status == schema.TurnCompleted {
			completedExecution = true
		}
		if entry.TurnKind == schema.TurnSpanGap && entry.Kind == schema.TurnModelSwitch {
			projectedModelSwitch = true
		}
	}
	if !completedExecution || !projectedModelSwitch {
		t.Fatalf("canonical history lacks completed execution A or actual model-switch gap: completed=%v modelSwitch=%v", completedExecution, projectedModelSwitch)
	}

	const ref, instance, generation = "local:worker", "worker-instance", "1@worker"
	jm := sess.jobManager
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"question"}, true)
	if err != nil {
		t.Fatal(err)
	}
	update := appwire.SessionWatchUpdate{
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
		Snapshot: &appwire.ThreadReadResponse{
			BootGeneration:        generation,
			Snapshot:              &appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: window.Length},
			LatestExecutionTurnID: window.LatestExecutionTurnID,
			Thread:                appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}, Turns: turns},
		},
	}
	if err := jm.onHubObservation(watch.WatchID, update); err != nil {
		t.Fatalf("initial indexed snapshot: %v", err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatalf("accepted terminal observation for completed execution %q did not end watch across model-switch gap %q", executionID, gapID)
	}
	completed := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchID == watch.WatchID && event.Watch != nil && event.Watch.EndReason == "completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("durable completion count=%d, want exactly one", completed)
	}

	// Rebuild the exact producer history with a later canonical execution input
	// but no completion record, as after interruption. The model-switch gap and
	// completed A remain the real records generated above.
	unfinishedPath := filepath.Join(t.TempDir(), "unfinished.jsonl")
	writer, err := transcript.NewWriter(unfinishedPath, transcript.Header{SessionID: "unfinished-anchor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range answerLinkRecords(t, sess) {
		if err := writer.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "execution-B", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("unfinished execution B")}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	unfinishedIndex, err := transcriptindex.Open(unfinishedPath, filepath.Join(t.TempDir(), "unfinished-index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unfinishedIndex.Close() })
	unfinishedWindow, err := unfinishedIndex.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	if unfinishedWindow.LatestExecutionTurnID != "execution-B" {
		t.Fatalf("unfinished latest execution=%q, want execution-B", unfinishedWindow.LatestExecutionTurnID)
	}
	unfinishedTurns := projectAnchorWindow(unfinishedWindow.Candidates)
	unfinishedWatch, err := jm.configureHubWatch(ref+"-unfinished", instance, generation, "local:manager", []string{"question"}, true)
	if err != nil {
		t.Fatal(err)
	}
	unfinishedSnapshot := update
	unfinishedSnapshot.Metadata.Ref = ref + "-unfinished"
	unfinishedSnapshot.Snapshot = &appwire.ThreadReadResponse{
		BootGeneration:        generation,
		LatestExecutionTurnID: unfinishedWindow.LatestExecutionTurnID,
		Snapshot:              &appwire.SnapshotIdentity{Incarnation: unfinishedWindow.Incarnation, Length: unfinishedWindow.Length},
		Thread:                appwire.Thread{Evener: appwire.EvenerThread{Ref: ref + "-unfinished", InstanceID: instance}, Turns: unfinishedTurns},
	}
	if err := jm.onHubObservation(unfinishedWatch.WatchID, unfinishedSnapshot); err != nil {
		t.Fatalf("unfinished indexed snapshot: %v", err)
	}
	if !managerWatchActive(jm, unfinishedWatch.WatchID) {
		t.Fatal("accepted completion for old execution A completed later unfinished execution B")
	}

	activeWatch, err := jm.configureHubWatch(ref+"-active", instance, generation, "local:manager", []string{"question"}, true)
	if err != nil {
		t.Fatal(err)
	}
	activeUpdate := update
	activeUpdate.Metadata.Ref = ref + "-active"
	activeUpdate.Snapshot = &appwire.ThreadReadResponse{
		BootGeneration:        generation,
		LatestExecutionTurnID: executionID,
		Snapshot:              &appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: window.Length},
		Thread:                appwire.Thread{Evener: appwire.EvenerThread{Ref: ref + "-active", InstanceID: instance, ActiveTurnID: "active"}, Turns: turns},
	}
	if err := jm.onHubObservation(activeWatch.WatchID, activeUpdate); err != nil {
		t.Fatalf("active indexed snapshot: %v", err)
	}
	jm.mu.Lock()
	_, activeCfg, ok := jm.watchConfigByIDLocked(activeWatch.WatchID)
	jm.mu.Unlock()
	if !ok || activeCfg.hubTurnID != "active" {
		t.Fatalf("active watch anchor=%q, want active turn", activeCfg.hubTurnID)
	}
	if !managerWatchActive(jm, activeWatch.WatchID) {
		t.Fatal("historical terminal observation completed an active turn")
	}
}

func projectAnchorWindow(candidates []appitempaging.TranscriptItemCandidate) []appwire.Turn {
	var turns []appwire.Turn
	for _, candidate := range candidates {
		if len(turns) == 0 || turns[len(turns)-1].ID != candidate.TurnID {
			turn := candidate.Turn
			turn.Items = nil
			turns = append(turns, turn)
		}
		turns[len(turns)-1].Items = append(turns[len(turns)-1].Items, candidate.Item)
	}
	return turns
}
