package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

func waitForLiveSubagentRun(t *testing.T, child *subagent) {
	t.Helper()
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	if done == nil {
		t.Fatal("child has no done channel")
	}
	ctx := tripwireCtx(t)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for subagent run to finish")
	}
	testwait.Until(t, "subagent to settle out of running state", func() bool {
		child.mu.Lock()
		blocked := child.startBlockedLocked()
		child.mu.Unlock()
		return !blocked
	})
}

func countSubagentTranscriptTurns(t *testing.T, s *Session) (noToolSteers, delegateNudges, communicateTurns, acceptedCommunicateTurns, acceptedAssistantTurns int, acceptedAssistantTexts []string) {
	t.Helper()
	for _, turn := range transcriptTurnsOf(t, s) {
		switch turn.Kind {
		case schema.TurnSteering:
			switch turn.SteeringKind {
			case events.SteeringKindNoToolCalls:
				noToolSteers++
			case events.SteeringKindDelegateNudge:
				delegateNudges++
			}
		case schema.TurnCommunicate:
			communicateTurns++
			if turn.AcceptedReply {
				acceptedCommunicateTurns++
			}
		case schema.TurnAssistant:
			if turn.AcceptedReply {
				acceptedAssistantTurns++
				acceptedAssistantTexts = append(acceptedAssistantTexts, lastAgentText(turn.Message))
			}
		}
	}
	return noToolSteers, delegateNudges, communicateTurns, acceptedCommunicateTurns, acceptedAssistantTurns, acceptedAssistantTexts
}

func assertReportedDelegatePacket(t *testing.T, parent *Session, delegateID, wantReport string) delegatestore.TerminalPacket {
	t.Helper()
	aggregate := delegateAggregateSnapshot(t, parent.delegateController, delegateID)
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted {
		t.Fatalf("aggregate LatestOutcome = %#v, want completed", aggregate.LatestOutcome)
	}
	packet := loadDelegateTerminalPacket(t, parent, delegateID)
	if packet.Kind != delegatestore.PacketReported {
		t.Fatalf("packet Kind = %s, want reported", packet.Kind)
	}
	var gotMessage string
	if err := json.Unmarshal(packet.Message, &gotMessage); err != nil {
		t.Fatalf("unmarshal packet.Message: %v", err)
	}
	if gotMessage != wantReport {
		t.Fatalf("packet.Message = %q, want %q", gotMessage, wantReport)
	}
	return packet
}

func TestSubagentBareTextFallback(t *testing.T) {
	t.Parallel()

	t.Run("untyped_idle_direct_finish", func(t *testing.T) {
		t.Parallel()
		const wantReport = "Subagent direct bare-text final report"
		adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
			return bareTextResponse(wantReport, llm.FinishReasonStop)
		})
		parent := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "report directly"})
		if res.Err != nil {
			t.Fatalf("createDelegate: %v", res.Err)
		}
		_, childID, err := decodeRef(res.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		child := parent.subagents.get(childID)
		if child == nil {
			t.Fatalf("subagent %s not found", childID)
		}
		waitForLiveSubagentRun(t, child)

		if got := len(adapter.Requests()); got != 1 {
			t.Fatalf("provider requests = %d, want 1", got)
		}
		assertReportedDelegatePacket(t, parent, res.DelegateID, wantReport)
		noTool, nudges, commTurns, accComm, accAsst, accTexts := countSubagentTranscriptTurns(t, child.sess)
		if noTool != 0 || nudges != 0 || commTurns != 0 || accComm != 0 || accAsst != 1 || len(accTexts) != 1 || accTexts[0] != wantReport {
			t.Fatalf("transcript counts = noTool:%d nudges:%d comm:%d accComm:%d accAsst:%d texts:%q, want 0/0/0/0/1/%q",
				noTool, nudges, commTurns, accComm, accAsst, accTexts, wantReport)
		}
		child.sess.mu.Lock()
		latest := child.sess.latestReport
		child.sess.mu.Unlock()
		if latest.phase != commBoundarySettled || latest.text != wantReport || latest.reply != wantReport {
			t.Fatalf("latestReport = %+v, want settled with %q", latest, wantReport)
		}
	})

	t.Run("untyped_idle_after_tool_call", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		probePath := filepath.Join(dir, "finding.txt")
		if err := os.WriteFile(probePath, []byte("confirmed defect\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		const wantReport = "Found confirmed defect in finding.txt"
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"file_path": probePath})
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "read-1", Name: "read_file", Arguments: args, Type: "function"})
			},
			func(llm.Request) llm.Response {
				return bareTextResponse(wantReport, llm.FinishReasonStop)
			},
		)
		parent := newSession(t, withDir(dir), withAdapter(adapter), withStateDir(t.TempDir()))
		res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "inspect finding.txt"})
		if res.Err != nil {
			t.Fatalf("createDelegate: %v", res.Err)
		}
		_, childID, err := decodeRef(res.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		child := parent.subagents.get(childID)
		if child == nil {
			t.Fatalf("subagent %s not found", childID)
		}
		waitForLiveSubagentRun(t, child)

		if got := len(adapter.Requests()); got != 2 {
			t.Fatalf("provider requests = %d, want 2", got)
		}
		assertReportedDelegatePacket(t, parent, res.DelegateID, wantReport)
		noTool, nudges, commTurns, _, accAsst, accTexts := countSubagentTranscriptTurns(t, child.sess)
		if noTool != 0 || nudges != 0 || commTurns != 0 || accAsst != 1 || len(accTexts) != 1 || accTexts[0] != wantReport {
			t.Fatalf("transcript counts = noTool:%d nudges:%d comm:%d accAsst:%d texts:%q",
				noTool, nudges, commTurns, accAsst, accTexts)
		}
	})

	t.Run("progress_update_and_resume_generation_2", func(t *testing.T) {
		t.Parallel()
		const gen1Report = "Generation 1 bare-text report"
		const gen2Report = "Generation 2 bare-text report"
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return updateResponse("prog-1", "Halfway through inspection")
			},
			func(llm.Request) llm.Response {
				return bareTextResponse(gen1Report, llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return bareTextResponse(gen2Report, llm.FinishReasonStop)
			},
		)
		parent := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "report with progress update"})
		if res.Err != nil {
			t.Fatalf("createDelegate: %v", res.Err)
		}
		_, childID, err := decodeRef(res.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		child := parent.subagents.get(childID)
		if child == nil {
			t.Fatalf("subagent %s not found", childID)
		}
		waitForLiveSubagentRun(t, child)

		assertReportedDelegatePacket(t, parent, res.DelegateID, gen1Report)
		if !child.sess.communicateUpdate() {
			t.Fatal("communicateUpdate() = false after end_turn=false + bare-text finish")
		}
		child.sess.mu.Lock()
		gen1Latest := child.sess.latestReport
		child.sess.mu.Unlock()
		if !gen1Latest.updated || gen1Latest.phase != commBoundarySettled || gen1Latest.reply != gen1Report {
			t.Fatalf("gen1 latestReport = %+v, want updated=true settled %q", gen1Latest, gen1Report)
		}
		noTool, nudges, commTurns, accComm, accAsst, accTexts := countSubagentTranscriptTurns(t, child.sess)
		if noTool != 0 || nudges != 0 || commTurns != 1 || accComm != 0 || accAsst != 1 || len(accTexts) != 1 || accTexts[0] != gen1Report {
			t.Fatalf("gen1 transcript counts = noTool:%d nudges:%d comm:%d accComm:%d accAsst:%d texts:%q",
				noTool, nudges, commTurns, accComm, accAsst, accTexts)
		}

		// Generation 2: resume the idle delegate with delegate_send.
		sendOutcome := (delegateRuntime{owner: parent}).send(tripwireCtx(t), res.DelegateID, "Now give follow-up findings", 60_000)
		if sendOutcome.result.Err != nil || sendOutcome.result.Output != gen2Report {
			t.Fatalf("generation 2 delegate_send = %+v, want Output=%q", sendOutcome.result, gen2Report)
		}
		if sendOutcome.commit != nil {
			if _, err := sendOutcome.commit.Complete(true); err != nil {
				t.Fatalf("commit gen2 delivery: %v", err)
			}
		}
		waitForLiveSubagentRun(t, child)

		assertReportedDelegatePacket(t, parent, res.DelegateID, gen2Report)
		child.sess.mu.Lock()
		gen2Latest := child.sess.latestReport
		child.sess.mu.Unlock()
		if gen2Latest.phase != commBoundarySettled || gen2Latest.reply != gen2Report {
			t.Fatalf("gen2 latestReport = %+v, want settled %q", gen2Latest, gen2Report)
		}
		noTool, nudges, commTurns, accComm, accAsst, accTexts = countSubagentTranscriptTurns(t, child.sess)
		if noTool != 0 || nudges != 0 || commTurns != 1 || accComm != 0 || accAsst != 2 || len(accTexts) != 2 || accTexts[1] != gen2Report {
			t.Fatalf("gen2 transcript counts = noTool:%d nudges:%d comm:%d accComm:%d accAsst:%d texts:%q",
				noTool, nudges, commTurns, accComm, accAsst, accTexts)
		}
	})

	t.Run("subagent_live_owned_work", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		t.Cleanup(env.releaseJob)
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
					Type:      "function",
				})
			},
			func(llm.Request) llm.Response {
				return bareTextResponse("Waiting for background shell...", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return bareTextResponse("Prose acknowledgment on notification wake", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return agenttest.FinalResponse("Verified after job completed")
			},
		)
		parent := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "run shell and report"})
		if res.Err != nil {
			t.Fatalf("createDelegate: %v", res.Err)
		}
		_, childID, err := decodeRef(res.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		child := parent.subagents.get(childID)
		if child == nil {
			t.Fatalf("subagent %s not found", childID)
		}

		ctx := tripwireCtx(t)
		select {
		case <-env.started:
		case <-ctx.Done():
			t.Fatal("child background shell did not start")
		}
		// Bare text while live work runs must park quietly automatically (zero
		// premature report to parent, zero no-tool-calls spin) until job wakes.
		testwait.Until(t, "subagent bare text to park quietly while background job runs", func() bool {
			return len(adapter.Requests()) >= 2 && child.sess.quietlyYielded() && child.sess.drainHeld.Load()
		})
		if noTool, _, _, _, accAsst, _ := countSubagentTranscriptTurns(t, child.sess); noTool != 0 || accAsst != 0 {
			t.Fatalf("while live job running: noTool=%d accAsst=%d, want 0/0", noTool, accAsst)
		}
		var sawQuietTurn bool
		for _, turn := range transcriptTurnsOf(t, child.sess) {
			if turn.Kind == schema.TurnAssistant && turn.QuietYield {
				sawQuietTurn = true
			}
		}
		if !sawQuietTurn {
			t.Fatal("bare-text quiet park did not record TurnAssistant.QuietYield=true")
		}
		// Release the background shell so the drain runs EntryNotification.
		env.releaseJob()
		waitForLiveSubagentRun(t, child)

		if got := len(adapter.Requests()); got != 4 {
			t.Fatalf("provider requests = %d, want 4", got)
		}
		assertReportedDelegatePacket(t, parent, res.DelegateID, "Verified after job completed")
		noTool, nudges, _, _, _, _ := countSubagentTranscriptTurns(t, child.sess)
		if noTool != 1 || nudges != 0 {
			t.Fatalf("noToolSteers = %d (want 1 after job completed), delegateNudges = %d (want 0)", noTool, nudges)
		}
	})

	t.Run("result_schema_rejected", func(t *testing.T) {
		t.Parallel()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return bareTextResponse("Unstructured draft", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return communicateWithStructured("Structured final report", map[string]any{"summary": "ok"})
			},
		)
		parent := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		res := parent.createDelegate(tripwireCtx(t), delegateArgs{
			Task: "return structured summary",
			ResultSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string"},
				},
				"required": []string{"summary"},
			},
		})
		if res.Err != nil {
			t.Fatalf("createDelegate: %v", res.Err)
		}
		_, childID, err := decodeRef(res.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		child := parent.subagents.get(childID)
		if child == nil {
			t.Fatalf("subagent %s not found", childID)
		}
		waitForLiveSubagentRun(t, child)

		noTool, nudges, _, _, _, _ := countSubagentTranscriptTurns(t, child.sess)
		if noTool != 1 || nudges != 0 || len(adapter.Requests()) != 2 {
			t.Fatalf("noTool=%d nudges=%d requests=%d, want 1/0/2", noTool, nudges, len(adapter.Requests()))
		}
		packet := assertReportedDelegatePacket(t, parent, res.DelegateID, "Structured final report")
		if packet.StructuredResultValid == nil || !*packet.StructuredResultValid {
			t.Fatalf("StructuredResultValid = %v, want true", packet.StructuredResultValid)
		}
	})

	t.Run("entry_delegate_attention_bare_text_parks_quietly", func(t *testing.T) {
		t.Parallel()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return bareTextResponse("Attention prose", llm.FinishReasonStop)
			},
		)
		s, c, lease := newAttentionDelegateTestSession(t, t.TempDir(), adapter)
		ctx := context.WithValue(tripwireCtx(t), delegateRunLeaseContextKey{}, lease)
		if _, _, err := s.processOneInput(ctx, "inspect attention", nil, EntryDelegateAttention, nil); err != nil {
			t.Fatalf("processOneInput: %v", err)
		}
		noTool, _, _, _, _, _ := countSubagentTranscriptTurns(t, s)
		if noTool != 0 || len(adapter.Requests()) != 1 || !s.quietlyYielded() {
			t.Fatalf("noTool=%d requests=%d quietlyYielded=%v, want 0/1/true", noTool, len(adapter.Requests()), s.quietlyYielded())
		}
		decision, err := c.completionDecision(lease)
		if err != nil {
			t.Fatalf("completionDecision: %v", err)
		}
		if decision != delegateCompletionFinishNoAction {
			t.Fatalf("completionDecision = %v, want delegateCompletionFinishNoAction", decision)
		}
		var sawQuietTurn bool
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.Kind == schema.TurnAssistant && turn.QuietYield {
				sawQuietTurn = true
			}
		}
		if !sawQuietTurn {
			t.Fatal("attention bare-text quiet park did not record TurnAssistant.QuietYield=true")
		}
	})

	t.Run("stop_hook_block_and_replace", func(t *testing.T) {
		t.Parallel()
		t.Run("block", func(t *testing.T) {
			t.Parallel()
			stateFile := filepath.Join(t.TempDir(), "stop-count")
			hookCmd := `if [ ! -f ` + `"` + stateFile + `"` + ` ]; then touch ` + `"` + stateFile + `"` + `; printf '%s' '{"decision":"block","reason":"Verify again"}'; else printf '%s' '{}'; fi`
			hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{
				"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hookCmd}}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			pluginDir := writePluginHooks(t, "subagent-stop-block", string(hooksJSON))
			adapter := newFakeAdapter("openai",
				func(llm.Request) llm.Response {
					return bareTextResponse("Draft 1", llm.FinishReasonStop)
				},
				func(llm.Request) llm.Response {
					return bareTextResponse("Draft 2", llm.FinishReasonStop)
				},
			)
			parent := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()), withConfig(SessionConfig{
				MaxSubagentDepth: 1,
				PluginDirs:       []string{pluginDir},
				AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			}))
			res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "draft with stop block"})
			if res.Err != nil {
				t.Fatalf("createDelegate: %v", res.Err)
			}
			_, childID, _ := decodeRef(res.TranscriptRef)
			child := parent.subagents.get(childID)
			waitForLiveSubagentRun(t, child)

			if got := len(adapter.Requests()); got != 2 {
				t.Fatalf("provider requests = %d, want 2", got)
			}
			assertReportedDelegatePacket(t, parent, res.DelegateID, "Draft 2")
			noTool, nudges, commTurns, _, accAsst, accTexts := countSubagentTranscriptTurns(t, child.sess)
			if noTool != 0 || nudges != 0 || commTurns != 0 || accAsst != 1 || len(accTexts) != 1 || accTexts[0] != "Draft 2" {
				t.Fatalf("transcript counts = noTool:%d nudges:%d comm:%d accAsst:%d texts:%q", noTool, nudges, commTurns, accAsst, accTexts)
			}
		})

		t.Run("replace", func(t *testing.T) {
			t.Parallel()
			hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{
				"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": `printf '%s' '{"continue":false,"stopReason":"Stop replaced reply"}'`}}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			pluginDir := writePluginHooks(t, "subagent-stop-replace", string(hooksJSON))
			adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
				return bareTextResponse("Vetoed draft", llm.FinishReasonStop)
			})
			parent := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()), withConfig(SessionConfig{
				MaxSubagentDepth: 1,
				PluginDirs:       []string{pluginDir},
				AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			}))
			res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "draft replaced by stop"})
			if res.Err != nil {
				t.Fatalf("createDelegate: %v", res.Err)
			}
			_, childID, _ := decodeRef(res.TranscriptRef)
			child := parent.subagents.get(childID)
			waitForLiveSubagentRun(t, child)

			if got := len(adapter.Requests()); got != 1 {
				t.Fatalf("provider requests = %d, want 1", got)
			}
			assertReportedDelegatePacket(t, parent, res.DelegateID, "Stop replaced reply")
			noTool, nudges, commTurns, accComm, accAsst, _ := countSubagentTranscriptTurns(t, child.sess)
			if noTool != 0 || nudges != 0 || commTurns != 1 || accComm != 1 || accAsst != 0 {
				t.Fatalf("transcript counts = noTool:%d nudges:%d comm:%d accComm:%d accAsst:%d, want 0/0/1/1/0",
					noTool, nudges, commTurns, accComm, accAsst)
			}
		})
	})

	t.Run("subagent_stop_hook", func(t *testing.T) {
		t.Parallel()
		t.Run("inspect_last_assistant_and_block", func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			capture := filepath.Join(dir, "subagent-stop-capture.jsonl")
			stateFile := filepath.Join(dir, "subagent-stop-count")
			hookCmd := appendStdinHook(capture) + `; if [ ! -f ` + `"` + stateFile + `"` + ` ]; then touch ` + `"` + stateFile + `"` + `; printf '%s' '{"decision":"block","reason":"Check more before stopping"}'; else printf '%s' '{}'; fi`
			hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{
				"SubagentStop": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": hookCmd}}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			pluginDir := writePluginHooks(t, "subagentstop-block", string(hooksJSON))
			adapter := newFakeAdapter("openai",
				func(llm.Request) llm.Response {
					return bareTextResponse("Draft 1", llm.FinishReasonStop)
				},
				func(llm.Request) llm.Response {
					return bareTextResponse("Final verified draft", llm.FinishReasonStop)
				},
			)
			parent := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()), withConfig(SessionConfig{
				MaxSubagentDepth: 1,
				PluginDirs:       []string{pluginDir},
				AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			}))
			res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "draft with subagent stop block"})
			if res.Err != nil {
				t.Fatalf("createDelegate: %v", res.Err)
			}
			_, childID, _ := decodeRef(res.TranscriptRef)
			child := parent.subagents.get(childID)
			waitForLiveSubagentRun(t, child)

			assertReportedDelegatePacket(t, parent, res.DelegateID, "Final verified draft")
			captured := decodeHookCapture(t, capture)
			if len(captured) != 1 {
				t.Fatalf("SubagentStop invocations = %d, want 1", len(captured))
			}
			if msg, _ := captured[0]["last_assistant_message"].(string); msg != "Draft 1" {
				t.Fatalf("SubagentStop last_assistant_message = %q, want %q", msg, "Draft 1")
			}
		})

		t.Run("stop_replaces_reply", func(t *testing.T) {
			t.Parallel()
			hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{
				"SubagentStop": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": `printf '%s' '{"continue":false,"stopReason":"SubagentStop replaced"}'`}}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			pluginDir := writePluginHooks(t, "subagentstop-replace", string(hooksJSON))
			adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
				return bareTextResponse("Draft 1", llm.FinishReasonStop)
			})
			parent := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()), withConfig(SessionConfig{
				MaxSubagentDepth: 1,
				PluginDirs:       []string{pluginDir},
				AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			}))
			res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "draft replaced by subagent stop"})
			if res.Err != nil {
				t.Fatalf("createDelegate: %v", res.Err)
			}
			_, childID, _ := decodeRef(res.TranscriptRef)
			child := parent.subagents.get(childID)
			waitForLiveSubagentRun(t, child)

			assertReportedDelegatePacket(t, parent, res.DelegateID, "SubagentStop replaced")
		})
	})

	t.Run("store_error_fails_safe", func(t *testing.T) {
		t.Parallel()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return bareTextResponse("Should fail safe when job store errors", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return agenttest.FinalResponse("Recovered explicitly")
			},
		)
		s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		s.cfg.spawn.parentSessionID = "parent"
		if err := s.jobManager.store.Close(); err != nil {
			t.Fatalf("close job store: %v", err)
		}
		got, err := s.ProcessInputKind(tripwireCtx(t), "Question", nil, EntryUserInput)
		if err != nil {
			t.Fatalf("ProcessInputKind: %v", err)
		}
		noTool, _, _, _, _, _ := countSubagentTranscriptTurns(t, s)
		if noTool != 1 || got != "Recovered explicitly" || len(adapter.Requests()) != 2 {
			t.Fatalf("noTool=%d got=%q requests=%d, want 1 / Recovered explicitly / 2", noTool, got, len(adapter.Requests()))
		}
	})

	t.Run("persisted_shell_attention_blocks_bare_text_until_presented", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		t.Cleanup(env.releaseJob)

		shellFinishedBeforeBareReply := make(chan struct{})
		releaseStep2 := make(chan struct{})
		var step3Req llm.Request
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
					Type:      "function",
				})
			},
			func(llm.Request) llm.Response {
				close(shellFinishedBeforeBareReply)
				<-releaseStep2
				return bareTextResponse("Premature bare text before reading completed shell", llm.FinishReasonStop)
			},
			func(req llm.Request) llm.Response {
				step3Req = req
				return bareTextResponse("Bare prose on notification wake", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return agenttest.FinalResponse("Reported after reading persisted shell attention")
			},
		)
		parent := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "run shell and report"})
		if res.Err != nil {
			t.Fatalf("createDelegate: %v", res.Err)
		}
		_, childID, err := decodeRef(res.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		child := parent.subagents.get(childID)
		if child == nil {
			t.Fatalf("subagent %s not found", childID)
		}

		ctx := tripwireCtx(t)
		select {
		case <-env.started:
		case <-ctx.Done():
			t.Fatal("child background shell did not start")
		}
		select {
		case <-shellFinishedBeforeBareReply:
		case <-ctx.Done():
			t.Fatal("child did not reach step 2")
		}
		// Release the background shell mid-round and wait until persistStableShellAttention
		// removes it from jm.running (so treeHasOutstandingWork alone is false) while its
		// unpresented attention row is durable in the child's transcript.
		env.releaseJob()
		testwait.Until(t, "shell finished into persisted stable attention with empty running map", func() bool {
			outstanding, err := child.sess.treeHasOutstandingWork()
			if err != nil || outstanding {
				return false
			}
			ids, err := child.sess.automaticDelegateAttentionIDs()
			return err == nil && len(ids) == 1
		})
		drainable, err := child.sess.subagentHasDrainableWork()
		if err != nil || !drainable {
			t.Fatalf("subagentHasDrainableWork() = %v, %v; want true, nil for unpresented shell attention", drainable, err)
		}
		close(releaseStep2)

		waitForLiveSubagentRun(t, child)
		if got := len(adapter.Requests()); got != 4 {
			t.Fatalf("provider requests = %d, want 4 (shell -> quiet-parked unpresented bare text -> notification bare text steered -> explicit final report)", got)
		}
		if !requestContainsText(step3Req, "child shell complete") {
			t.Fatalf("step 3 request did not present persisted shell attention: %+v", step3Req.Messages)
		}
		assertReportedDelegatePacket(t, parent, res.DelegateID, "Reported after reading persisted shell attention")
		noTool, nudges, commTurns, accComm, accAsst, _ := countSubagentTranscriptTurns(t, child.sess)
		if noTool != 1 || nudges != 0 || commTurns != 1 || accComm != 0 || accAsst != 0 {
			t.Fatalf("transcript counts = noTool:%d nudges:%d comm:%d accComm:%d accAsst:%d, want 1/0/1/0/0",
				noTool, nudges, commTurns, accComm, accAsst)
		}
		var sawQuietTurn bool
		for _, turn := range transcriptTurnsOf(t, child.sess) {
			if turn.Kind == schema.TurnAssistant && turn.QuietYield {
				sawQuietTurn = true
			}
		}
		if !sawQuietTurn {
			t.Fatal("step 2 unpresented bare text did not record TurnAssistant.QuietYield=true")
		}
	})

	t.Run("persisted_shell_attention_allows_quiet_yield_and_drains", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		t.Cleanup(env.releaseJob)

		inQuietYieldReq := make(chan struct{})
		releaseQuietYieldReq := make(chan struct{})
		var drainReq llm.Request
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
					Type:      "function",
				})
			},
			func(llm.Request) llm.Response {
				close(inQuietYieldReq)
				<-releaseQuietYieldReq
				return quietYieldResponse("quiet-after-persisted-shell", `{"message":"waiting on shell","next":"log_quietly_and_end_turn"}`)
			},
			func(req llm.Request) llm.Response {
				drainReq = req
				return agenttest.FinalResponse("Reported after draining persisted shell attention")
			},
		)
		parent := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		res := parent.createDelegate(tripwireCtx(t), delegateArgs{Task: "run shell and report"})
		if res.Err != nil {
			t.Fatalf("createDelegate: %v", res.Err)
		}
		_, childID, err := decodeRef(res.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		child := parent.subagents.get(childID)
		if child == nil {
			t.Fatalf("subagent %s not found", childID)
		}

		ctx := tripwireCtx(t)
		select {
		case <-env.started:
		case <-ctx.Done():
			t.Fatal("child background shell did not start")
		}
		select {
		case <-inQuietYieldReq:
		case <-ctx.Done():
			t.Fatal("child did not reach quiet-yield request")
		}
		// Finish the shell while the quiet-yield request is in flight: step 2's
		// request was built without the completion, and persistStableShellAttention
		// empties jm.running before communicate({}) executes.
		env.releaseJob()
		testwait.Until(t, "shell finished into persisted stable attention while quiet-yield request in flight", func() bool {
			outstanding, err := child.sess.treeHasOutstandingWork()
			if err != nil || outstanding {
				return false
			}
			ids, err := child.sess.automaticDelegateAttentionIDs()
			return err == nil && len(ids) == 1
		})
		close(releaseQuietYieldReq)

		waitForLiveSubagentRun(t, child)
		if got := len(adapter.Requests()); got != 3 {
			t.Fatalf("provider requests = %d, want 3 (shell -> quiet yield -> drain notification report)", got)
		}
		if !requestContainsText(drainReq, "child shell complete") {
			t.Fatalf("drain notification request did not carry persisted shell completion: %+v", drainReq.Messages)
		}
		var quietAccepted bool
		var completions int
		for _, turn := range transcriptTurnsOf(t, child.sess) {
			if turn.Kind == schema.TurnCompletion {
				completions++
			}
			if turn.Kind != schema.TurnToolResults {
				continue
			}
			for _, part := range turn.Message.Content {
				if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
					continue
				}
				if part.ToolResult.ToolCallID == "quiet-after-persisted-shell" {
					if part.ToolResult.IsError {
						t.Fatalf("quiet yield communicate({}) returned error: %s", part.ToolResult.Content)
					}
					if !schema.IsQuietCommunicateState(part.ToolResult.ToolState) {
						t.Fatalf("quiet yield communicate({}) ToolState = %s, want quiet state", string(part.ToolResult.ToolState))
					}
					quietAccepted = true
				}
			}
		}
		if !quietAccepted {
			t.Fatal("missing accepted quiet communicate({}) tool result in child transcript")
		}
		if completions != 2 {
			t.Fatalf("TurnCompletion entries = %d, want 2 (initial quiet yield + drain notification turn)", completions)
		}
		assertReportedDelegatePacket(t, parent, res.DelegateID, "Reported after draining persisted shell attention")
		noTool, nudges, _, _, _, _ := countSubagentTranscriptTurns(t, child.sess)
		if noTool != 0 || nudges != 0 {
			t.Fatalf("noToolSteers = %d (want 0), delegateNudges = %d (want 0)", noTool, nudges)
		}
	})

	t.Run("direct_nil_store_subagent_is_eligible", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withSteps(func(llm.Request) llm.Response {
			return bareTextResponse("Direct subagent answer", llm.FinishReasonStop)
		}))
		s.cfg.spawn.parentSessionID = "parent"
		got, err := s.ProcessInputKind(tripwireCtx(t), "Question", nil, EntryUserInput)
		if err != nil || got != "Direct subagent answer" || s.State() != SessionIdle {
			t.Fatalf("got=%q state=%v err=%v", got, s.State(), err)
		}
	})
}
