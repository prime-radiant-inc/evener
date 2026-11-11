package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// Stop can terminalize a start on either side of the canonical input append.
// Only the input that never reached that append may stop fencing old output.
func TestConversationProvenance_InterruptedStartEligibility(t *testing.T) {
	t.Parallel()
	for _, incorporated := range []bool{false, true} {
		for _, recovery := range []string{"live", "restore", "fold-restore"} {
			t.Run(fmt.Sprintf("incorporated=%t/%s", incorporated, recovery), func(t *testing.T) {
				t.Parallel()
				runInterruptedConversationStart(t, incorporated, recovery)
			})
		}
	}
}

func runInterruptedConversationStart(t *testing.T, incorporated bool, recovery string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "interrupted-conversation-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nRetain the input history.\n[END SUMMARY]")}
	}, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))
	response := func(llm.Request) llm.Response {
		call := communicateCall("conversation-result", "opaque-result-817")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{response, response, response, response}})
	// Real completed inputs provide both an older reply and enough history for
	// the summary case, without constructing transcript or provenance state.
	for i := range 4 {
		if _, err := s.ProcessInput(ctx, fmt.Sprintf("opaque-prior-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	oldOutput := interruptedConversationOutput(t, s)
	if !appwire.ConversationOutputEligible(s.ConversationProvenance(), oldOutput) {
		t.Fatal("original committed reply was not eligible before the new admission")
	}
	const stoppedText = "opaque-stopped-639"
	start := appwire.TurnStartParams{ClientMutationID: "stopped-start", Input: []appwire.InputItem{{Type: "text", Text: stoppedText}}}
	started, err := s.AcceptClientMutationStart(start)
	if err != nil {
		t.Fatal(err)
	}
	accepted := s.clientMutations.snapshot()
	if accepted.PendingExecutions[start.ClientMutationID].ExecutionState != "accepted" {
		t.Fatal("start was not durably accepted before stop")
	}
	ids := accepted.Journal[start.ClientMutationID].ConversationInputIDs
	if len(ids) != 1 {
		t.Fatalf("accepted input identities=%v", ids)
	}
	inputID := ids[0]
	if appwire.ConversationOutputEligible(s.ConversationProvenance(), oldOutput) {
		t.Fatal("accepted input did not fence the older reply")
	}
	var cancelAndWait func()
	if incorporated {
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		entered := make(chan struct{})
		done := make(chan error, 1)
		s.client.Register(&conversationRetryAdapter{fakeAdapter: &fakeAdapter{name: "openai"}, complete: func(req llm.Request) (llm.Response, error) {
			if !conversationRequestContains(req, stoppedText) {
				t.Error("interrupted provider request never received the accepted input")
			}
			close(entered)
			<-runCtx.Done()
			return llm.Response{}, runCtx.Err()
		}})
		go func() {
			_, ran, runErr := s.ProcessClientMutationStart(runCtx, nil)
			if !ran {
				runErr = errors.Join(runErr, errors.New("accepted start did not run"))
			}
			done <- runErr
		}()
		<-entered
		cancelAndWait = func() {
			cancel()
			if runErr := <-done; !errors.Is(runErr, context.Canceled) {
				t.Errorf("interrupted runner error=%v", runErr)
			}
		}
	}
	interrupt := appwire.TurnInterruptParams{ClientMutationID: "stop-start"}
	stopped, err := s.InterruptClientMutation(ctx, interrupt, cancelAndWait)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Receipt.TurnID != started.Turn.ID {
		t.Fatal("stop did not target the accepted start")
	}
	checkInterruptedConversationStart(t, s, start, inputID, incorporated)

	if recovery == "fold-restore" {
		s.contextMgr.CheckpointThreshold = 2
		s.contextMgr.SummarizeThreshold = 0.5
		s.contextMgr.PreserveRecentTurns = 0
		forcePressureAbove(t, s, 0.92)
		var timings events.RoundTimings
		if _, _, _, _, _, _, err := s.prepareModelRequestWithError(ctx, 5, &timings); err != nil {
			t.Fatal(err)
		}
		foundSummary := false
		s.mu.Lock()
		history := slices.Clone(s.history)
		s.mu.Unlock()
		for _, turn := range history {
			foundSummary = foundSummary || turn.Kind == schema.TurnSummary
			if slices.Contains(turn.ConversationInputIDs, inputID) {
				t.Fatal("compaction retained the stopped input verbatim instead of testing folded coverage")
			}
		}
		if !foundSummary {
			t.Fatal("real history did not fold into a summary")
		}
	}
	if recovery != "live" {
		s.Close()
		meta, err := schema.LoadSessionMeta(dir, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		client := llm.NewClient()
		s, err = RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		checkInterruptedConversationStart(t, s, start, inputID, incorporated)
	}
	// A reconnect rereads this authoritative projection. Interrupted input
	// already in canonical history must still disqualify the older reply.
	state := s.ConversationProvenance()
	if !state.Authoritative || !slices.Contains(state.AdmittedInputs, inputID) {
		t.Fatalf("stopped admission disappeared or became unknown: %+v", state)
	}
	if retired := slices.Contains(state.RetiredInputs, inputID); retired == incorporated {
		t.Errorf("interrupted input retirement=%t, incorporated=%t", retired, incorporated)
	}
	if eligible := appwire.ConversationOutputEligible(state, oldOutput); eligible == incorporated {
		t.Errorf("older reply eligibility=%t, incorporated=%t", eligible, incorporated)
	}
	const freshText = "opaque-fresh-928"
	requests := 0
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		requests++
		if !conversationRequestContains(req, freshText) || (!incorporated && conversationRequestContains(req, stoppedText)) {
			t.Error("fresh request lost its new input or replayed the never-incorporated stopped input")
		}
		return response(req)
	}}})
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "fresh-start", Input: []appwire.InputItem{{Type: "text", Text: freshText}}}); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := s.ProcessClientMutationStart(ctx, nil); err != nil || !ran {
		t.Fatalf("fresh start ran=%t err=%v", ran, err)
	}
	if requests != 1 {
		t.Fatalf("fresh provider requests=%d", requests)
	}
	state = s.ConversationProvenance()
	output := interruptedConversationOutput(t, s)
	coverage, ok := appwire.ResolveConversationFrontier(state, output.FrontierID)
	if !ok || slices.Contains(coverage, inputID) != incorporated {
		t.Fatalf("fresh actual request coverage=%v, incorporated=%t", coverage, incorporated)
	}
	if !appwire.ConversationOutputEligible(state, output) {
		t.Errorf("fresh committed reply ineligible after stopped start: incorporated=%t state=%+v coverage=%v", incorporated, state, coverage)
	}
}

func checkInterruptedConversationStart(t *testing.T, s *Session, start appwire.TurnStartParams, inputID string, incorporated bool) {
	t.Helper()
	snapshot := s.clientMutations.snapshot()
	record := snapshot.Journal[start.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal || record.ExecutionState != "interrupted" || len(record.Payload) != 0 {
		t.Fatalf("stopped start did not terminalize interrupted: %+v", record)
	}
	if len(snapshot.PendingExecutions) != 0 || snapshot.ActiveTurnID != "" || snapshot.InterruptFence != nil {
		t.Fatal("stopped input retained executable work")
	}
	if _, ran, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil || ran {
		t.Fatalf("stopped start replayed: ran=%t err=%v", ran, err)
	}
	canonical := 0
	for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
		if entry.Turn.OriginalOrdinal == nil && slices.Contains(entry.Turn.ConversationInputIDs, inputID) {
			canonical++
			if entry.Turn.Kind != schema.TurnUserInput || entry.Turn.ClientMutationID != start.ClientMutationID {
				t.Fatal("input identity appeared outside its canonical user record")
			}
		}
	}
	want := 0
	if incorporated {
		want = 1
	}
	if canonical != want {
		t.Fatalf("canonical stopped-input records=%d, want %d", canonical, want)
	}
	replayed, err := s.AcceptClientMutationStart(start)
	if err != nil || replayed.Receipt.Disposition != appwire.MutationDispositionReplayed || replayed.Turn.ID != record.StableTurnID {
		t.Fatalf("accepted mutation receipt changed on replay: %+v err=%v", replayed, err)
	}
	if _, ran, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil || ran {
		t.Fatalf("receipt replay resurrected stopped input: ran=%t err=%v", ran, err)
	}
	t.Logf("interrupted start: canonical records=%d, pending=0, receipt replay preserves stopped turn", canonical)
}

func interruptedConversationOutput(t *testing.T, s *Session) *appwire.ConversationOutputProvenance {
	t.Helper()
	var output *appwire.ConversationOutputProvenance
	for index, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
		if item, ok := apptranscript.CommunicateItem(entry.Turn.TurnID, index, entry.Turn); ok {
			output = item.ConversationProvenance
		}
	}
	if output == nil {
		t.Fatal("no canonical reply provenance")
	}
	return output
}
