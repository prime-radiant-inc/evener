package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func userRoleTextMessages(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		if text := m.Text(); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func hasToolCallNamed(msgs []llm.Message, name string) bool {
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.Name == name {
				return true
			}
		}
	}
	return false
}

func hasToolResultNamed(msgs []llm.Message, name string) bool {
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.Name == name {
				return true
			}
		}
	}
	return false
}

func newResponsesAnchorTurn(suffix string, call llm.ToolCallData) schema.Turn {
	turn := schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
	})
	turn.ResponseID = "resp_anchor_" + suffix
	turn.ResponseIDHash = "hash_anchor_" + suffix
	turn.ResponseEndpoint = "responses"
	turn.ResponseStorageScopeFingerprint = "scope_" + suffix
	turn.ResponseRequestFingerprint = "req_fp_" + suffix
	turn.ResponseContextMarker = responseContextMarkerV1
	return turn
}

func readPersistedTranscriptEntries(t *testing.T, stateDir, sessionID string) []transcript.Entry {
	t.Helper()
	tw, entries, err := transcript.OpenWriterForSession(schema.SessionTranscriptPath(stateDir, sessionID), sessionID)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	return entries
}

func TestAgentNotesAndURLToolsKeepTranscriptRowWhileSuppressingModelSelfEcho(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	workDir := t.TempDir()
	const (
		agentNoteSentinel = "opaque-agent-note-701\nNow: step1\nNext: step2"
		urlSentinel       = "https://opaque-702.test/doc"
		labelSentinel     = "opaque-label-702"
	)

	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(_ llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"note": agentNoteSentinel, "intent": "setting agent note"})
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-note-1",
								Name:      "notes_agent_set",
								Arguments: args,
							},
						}},
					},
				}
			},
			func(_ llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"url": urlSentinel, "label": labelSentinel, "intent": "adding url"})
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-url-add-2",
								Name:      "urls_add",
								Arguments: args,
							},
						}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				var addedID string
				for _, m := range req.Messages {
					for _, p := range m.Content {
						if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.Name == "urls_add" {
							text, _ := p.ToolResult.Content.(string)
							addedID = extractNotesLinkID(strings.TrimSpace(text))
						}
					}
				}
				args, _ := json.Marshal(map[string]any{"id": addedID, "intent": "removing url"})
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-url-rm-3",
								Name:      "urls_remove",
								Arguments: args,
							},
						}},
					},
				}
			},
			func(_ llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{
					"message":  "done",
					"end_turn": true,
					"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}},
					"intent":   "finishing turn",
				})
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-comm-4",
								Name:      "communicate",
								Arguments: args,
							},
						}},
					},
				}
			},
		},
	}

	c := llm.NewClient()
	c.Register(adapter)
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s.mu.Lock()
	s.naming.set = true
	s.mu.Unlock()

	if _, err := s.ProcessInput(context.Background(), "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d, want 4", len(requests))
	}

	// 1. s.history and the persisted transcript must retain all 3 TurnNotesContext turns,
	// and apptranscript.ProjectTurn must project each one to ThreadItemEventKindNotesContext.
	s.mu.Lock()
	histCopy := append([]schema.Turn(nil), s.history...)
	s.mu.Unlock()
	reg := apptranscript.NewToolCallRegistry()
	notesTurnsInHistory := 0
	for i, turn := range histCopy {
		if turn.Kind == schema.TurnToolResults {
			for _, p := range turn.Message.Content {
				if p.ToolResult != nil && p.ToolResult.IsError {
					t.Fatalf("unexpected tool error in turn %d (%s): %v", i, p.ToolResult.Name, p.ToolResult.Content)
				}
			}
		}
		items := apptranscript.ProjectTurn("t1", i, turn, reg, nil, nil)
		if turn.Kind == schema.TurnNotesContext {
			notesTurnsInHistory++
			if len(items) != 1 || items[0].EventKind != appwire.ThreadItemEventKindNotesContext {
				t.Fatalf("projected items for TurnNotesContext[%d] = %+v, want one ThreadItemEventKindNotesContext", i, items)
			}
		}
	}
	if notesTurnsInHistory != 3 {
		t.Fatalf("TurnNotesContext count in s.history = %d, want 3; kinds=%v", notesTurnsInHistory, responsesContinuationTurnKinds(histCopy))
	}

	sessionID := s.ID()
	s.Close()
	entries := readPersistedTranscriptEntries(t, stateDir, sessionID)
	notesTurnsInTranscript := 0
	for _, e := range entries {
		if e.Turn.Kind == schema.TurnNotesContext {
			notesTurnsInTranscript++
		}
	}
	if notesTurnsInTranscript != 3 {
		t.Fatalf("TurnNotesContext count in persisted transcript = %d, want 3", notesTurnsInTranscript)
	}

	// 2. Provider requests in rounds 2, 3, and 4 must carry the tool calls/results
	// while omitting the self-echoed <shared-notes> user messages.
	for rIdx := 1; rIdx < len(requests); rIdx++ {
		userTexts := strings.Join(userRoleTextMessages(requests[rIdx].Messages), "\n")
		if strings.Contains(userTexts, "opaque-agent-note-701") {
			t.Fatalf("round %d request user messages echoed agent note:\n%s", rIdx+1, userTexts)
		}
		if strings.Contains(userTexts, urlSentinel) {
			t.Fatalf("round %d request user messages echoed agent URL:\n%s", rIdx+1, userTexts)
		}
	}
	if !hasToolCallNamed(requests[3].Messages, "notes_agent_set") || !hasToolResultNamed(requests[3].Messages, "notes_agent_set") {
		t.Fatalf("round 4 request lost notes_agent_set tool call or result")
	}
}

func TestAgentNotesSelfEchoSuppressedInResponsesDeltaWithPriorHumanNote(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()

	if _, err := s.SetHumanNote("human-seed", "opaque-human-703"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	s.maybeAppendNotesContext()

	s.appendTurn(schema.TurnUserInput, llm.User("go"))

	args, _ := json.Marshal(map[string]any{"note": "opaque-agent-704"})
	call := llm.ToolCallData{ID: "call-1", Name: "notes_agent_set", Arguments: args}
	assistantTurn := newResponsesAnchorTurn("1", call)
	s.mu.Lock()
	s.history = append(s.history, assistantTurn)
	s.mu.Unlock()

	out := notesToolExec(context.Background(), t, s, "call-1", "notes_agent_set", map[string]any{"note": "opaque-agent-704"})
	s.appendTurn(schema.TurnToolResults, llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call-1",
				Name:       "notes_agent_set",
				Content:    out,
			},
		}},
	})
	s.maybeAppendNotesContext()

	s.mu.Lock()
	hist := append([]schema.Turn(nil), s.history...)
	s.mu.Unlock()

	candidate, decision := selectResponsesContinuationAnchorCandidate(s.cfg, hist)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta {
		t.Fatalf("Continuation decision = %+v, want HistoryModeResponsesDelta", decision)
	}
	for _, dt := range candidate.Delta {
		if dt.Kind == schema.TurnNotesContext {
			t.Fatalf("Responses delta retained self-echo TurnNotesContext in candidate.Delta: %+v", candidate.Delta)
		}
	}
	deltaMsgs := responsesContinuationDeltaMessages([]llm.Message{llm.System("sys")}, candidate.Delta)
	if !hasToolResultNamed(deltaMsgs, "notes_agent_set") {
		t.Fatalf("Responses delta messages lost notes_agent_set tool result: %+v", deltaMsgs)
	}
	if joined := strings.Join(userRoleTextMessages(deltaMsgs), "\n"); strings.Contains(joined, "opaque-agent-704") {
		t.Fatalf("Responses delta messages echoed agent note:\n%s", joined)
	}
}

func TestConcurrentHumanNoteOrURLRemovalDuringAgentNotesRoundReachesModel(t *testing.T) {
	t.Parallel()

	t.Run("human note edit in same window", func(t *testing.T) {
		t.Parallel()
		s := newNotesToolSession(t)
		defer s.Close()

		args, _ := json.Marshal(map[string]any{"note": "opaque-agent-705"})
		call := llm.ToolCallData{ID: "call-1", Name: "notes_agent_set", Arguments: args}
		s.appendTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
		})
		out := notesToolExec(context.Background(), t, s, "call-1", "notes_agent_set", map[string]any{"note": "opaque-agent-705"})
		s.appendTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: "call-1",
					Name:       "notes_agent_set",
					Content:    out,
				},
			}},
		})
		if _, err := s.SetHumanNote("h-concurrent", "opaque-human-705"); err != nil {
			t.Fatalf("SetHumanNote: %v", err)
		}
		s.maybeAppendNotesContext()

		s.mu.Lock()
		msgs := expandHistory(append([]schema.Turn(nil), s.history...), replayScope{})
		s.mu.Unlock()
		joined := strings.Join(userRoleTextMessages(msgs), "\n")
		if !strings.Contains(joined, "opaque-human-705") || !strings.Contains(joined, "opaque-agent-705") {
			t.Fatalf("mixed human+agent TurnNotesContext was suppressed from expandHistory:\n%s", joined)
		}
	})

	t.Run("human url removal in same window", func(t *testing.T) {
		t.Parallel()
		s := newNotesToolSession(t)
		defer s.Close()

		u1, err := s.addSessionURL("https://opaque-keep-706.test/1", "keep")
		if err != nil {
			t.Fatalf("add u1: %v", err)
		}
		u2, err := s.addSessionURL("https://opaque-drop-706.test/2", "drop")
		if err != nil {
			t.Fatalf("add u2: %v", err)
		}
		s.maybeAppendNotesContext()

		args, _ := json.Marshal(map[string]any{"note": "opaque-agent-706"})
		call := llm.ToolCallData{ID: "call-1", Name: "notes_agent_set", Arguments: args}
		asst := newResponsesAnchorTurn("706", call)
		s.mu.Lock()
		s.history = append(s.history, asst)
		s.mu.Unlock()
		out := notesToolExec(context.Background(), t, s, "call-1", "notes_agent_set", map[string]any{"note": "opaque-agent-706"})
		s.appendTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: "call-1",
					Name:       "notes_agent_set",
					Content:    out,
				},
			}},
		})
		if removed, err := s.RemoveSessionURL("u-concurrent", u2.ID); err != nil || !removed {
			t.Fatalf("RemoveSessionURL = %v, %v; want true, nil", removed, err)
		}
		s.maybeAppendNotesContext()

		s.mu.Lock()
		hist := append([]schema.Turn(nil), s.history...)
		s.mu.Unlock()
		msgs := expandHistory(hist, replayScope{})
		userMsgs := userRoleTextMessages(msgs)
		if len(userMsgs) != 2 {
			t.Fatalf("user messages in expandHistory = %d, want 2 (initial projection + human URL removal projection): %+v", len(userMsgs), userMsgs)
		}
		if !strings.Contains(userMsgs[1], u1.ID) || strings.Contains(userMsgs[1], u2.ID) {
			t.Fatalf("second projection = %q, want %s retained and %s removed", userMsgs[1], u1.ID, u2.ID)
		}

		candidate, decision := selectResponsesContinuationAnchorCandidate(s.cfg, hist)
		if decision.HistoryMode != llm.HistoryModeResponsesDelta {
			t.Fatalf("Continuation decision = %+v, want HistoryModeResponsesDelta", decision)
		}
		deltaMsgs := responsesContinuationDeltaMessages([]llm.Message{llm.System("sys")}, candidate.Delta)
		deltaUserMsgs := userRoleTextMessages(deltaMsgs)
		if len(deltaUserMsgs) != 1 || !strings.Contains(deltaUserMsgs[0], u1.ID) || strings.Contains(deltaUserMsgs[0], u2.ID) {
			t.Fatalf("delta user messages = %+v, want human URL removal projection carrying %s without %s", deltaUserMsgs, u1.ID, u2.ID)
		}
	})

	t.Run("human removes sole url in same window under responses delta", func(t *testing.T) {
		t.Parallel()
		s := newNotesToolSession(t)
		defer s.Close()

		sole, err := s.addSessionURL("https://opaque-sole-706b.test/only", "only")
		if err != nil {
			t.Fatalf("add sole: %v", err)
		}
		s.maybeAppendNotesContext()

		args, _ := json.Marshal(map[string]any{"note": "opaque-agent-706b"})
		call := llm.ToolCallData{ID: "call-1", Name: "notes_agent_set", Arguments: args}
		asst := newResponsesAnchorTurn("706b", call)
		s.mu.Lock()
		s.history = append(s.history, asst)
		s.mu.Unlock()

		out := notesToolExec(context.Background(), t, s, "call-1", "notes_agent_set", map[string]any{"note": "opaque-agent-706b"})
		s.appendTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: "call-1",
					Name:       "notes_agent_set",
					Content:    out,
				},
			}},
		})
		if removed, err := s.RemoveSessionURL("u-sole", sole.ID); err != nil || !removed {
			t.Fatalf("RemoveSessionURL = %v, %v; want true, nil", removed, err)
		}
		s.maybeAppendNotesContext()

		s.mu.Lock()
		hist := append([]schema.Turn(nil), s.history...)
		s.mu.Unlock()
		candidate, decision := selectResponsesContinuationAnchorCandidate(s.cfg, hist)
		if decision.HistoryMode != llm.HistoryModeResponsesDelta {
			t.Fatalf("Continuation decision = %+v, want HistoryModeResponsesDelta", decision)
		}
		deltaMsgs := responsesContinuationDeltaMessages([]llm.Message{llm.System("sys")}, candidate.Delta)
		deltaUserMsgs := userRoleTextMessages(deltaMsgs)
		if len(deltaUserMsgs) != 1 || !strings.Contains(deltaUserMsgs[0], "opaque-agent-706b") || strings.Contains(deltaUserMsgs[0], sole.ID) {
			t.Fatalf("delta user messages = %+v, want projection reflecting sole URL removal", deltaUserMsgs)
		}
	})
}

func TestNotesSelfEchoReprojectsAfterCompactionIncludingPreservedTail(t *testing.T) {
	t.Parallel()

	t.Run("compaction after self-echo round", func(t *testing.T) {
		t.Parallel()
		s := newNotesToolSession(t)
		defer s.Close()

		args, _ := json.Marshal(map[string]any{"note": "opaque-agent-707"})
		call := llm.ToolCallData{ID: "call-1", Name: "notes_agent_set", Arguments: args}
		s.appendTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
		})
		out := notesToolExec(context.Background(), t, s, "call-1", "notes_agent_set", map[string]any{"note": "opaque-agent-707"})
		s.appendTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: "call-1",
					Name:       "notes_agent_set",
					Content:    out,
				},
			}},
		})
		s.maybeAppendNotesContext()

		// Before compaction, the self-echo is suppressed.
		s.mu.Lock()
		preMsgs := expandHistory(append([]schema.Turn(nil), s.history...), replayScope{})
		s.mu.Unlock()
		if joined := strings.Join(userRoleTextMessages(preMsgs), "\n"); strings.Contains(joined, "opaque-agent-707") {
			t.Fatalf("pre-compaction expandHistory echoed agent note:\n%s", joined)
		}

		// Compaction folds history into TurnSummary and resets notes projection.
		s.mu.Lock()
		s.history = []schema.Turn{schema.NewTurn(schema.TurnSummary, llm.User("summary"))}
		s.mu.Unlock()
		s.resetNotesProjectionAfterCompaction()

		var timings events.RoundTimings
		_, _, _, req, _, _, err := s.prepareModelRequestWithError(t.Context(), 0, &timings)
		if err != nil {
			t.Fatalf("prepareModelRequestWithError: %v", err)
		}
		joined := strings.Join(userRoleTextMessages(req.Messages), "\n")
		if !strings.Contains(joined, "opaque-agent-707") {
			t.Fatalf("post-compaction request omitted re-projected agent note:\n%s", joined)
		}
	})

	t.Run("compaction with pre-compaction tool round in PreserveRecentTurns tail", func(t *testing.T) {
		t.Parallel()
		s := newNotesToolSession(t)
		defer s.Close()

		t0 := time.Now().UTC().Add(-2 * time.Minute)
		args, _ := json.Marshal(map[string]any{"note": "opaque-agent-708"})
		call := llm.ToolCallData{ID: "call-1", Name: "notes_agent_set", Arguments: args}
		asst := schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
		})
		asst.Timestamp = t0
		out := notesToolExec(context.Background(), t, s, "call-1", "notes_agent_set", map[string]any{"note": "opaque-agent-708"})
		tr := schema.NewTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: "call-1",
					Name:       "notes_agent_set",
					Content:    out,
				},
			}},
		})
		tr.Timestamp = t0.Add(time.Second)
		chk := schema.NewTurn(schema.TurnCheckpoint, llm.User("checkpoint"))
		chk.Timestamp = t0.Add(2 * time.Second)

		// Simulate checkpointFold placing TurnCheckpoint before the preserved tail
		// [asst, tr], followed by resetNotesProjectionAfterCompaction and the
		// post-compaction maybeAppendNotesContext re-projection.
		s.mu.Lock()
		s.history = []schema.Turn{chk, asst, tr}
		s.mu.Unlock()
		s.resetNotesProjectionAfterCompaction()
		s.maybeAppendNotesContext()

		s.mu.Lock()
		msgs := expandHistory(append([]schema.Turn(nil), s.history...), replayScope{})
		s.mu.Unlock()
		joined := strings.Join(userRoleTextMessages(msgs), "\n")
		if !strings.Contains(joined, "opaque-agent-708") {
			t.Fatalf("post-compaction re-projection after preserved tail was suppressed:\n%s", joined)
		}

		// Also verify the OriginalOrdinal guard when timestamps are identical.
		sameTime := t0
		ord := uint64(3)
		asstOrd := asst
		asstOrd.Timestamp = sameTime
		asstOrd.OriginalOrdinal = &ord
		trOrd := tr
		trOrd.Timestamp = sameTime
		trOrd.OriginalOrdinal = &ord
		chkOrd := chk
		chkOrd.Timestamp = sameTime
		notesOrd := s.history[len(s.history)-1]
		notesOrd.Timestamp = sameTime
		notesOrd.OriginalOrdinal = nil
		ordMsgs := expandHistory([]schema.Turn{chkOrd, asstOrd, trOrd, notesOrd}, replayScope{})
		if joinedOrd := strings.Join(userRoleTextMessages(ordMsgs), "\n"); !strings.Contains(joinedOrd, "opaque-agent-708") {
			t.Fatalf("OriginalOrdinal guard failed to keep post-compaction re-projection:\n%s", joinedOrd)
		}
	})
}

func TestNotesSelfEchoSurvivesRestoreAndForkWithoutReprojectingDuplicate(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	live, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	live.appendTurn(schema.TurnUserInput, llm.User("start"))
	args, _ := json.Marshal(map[string]any{"note": "opaque-agent-709"})
	call := llm.ToolCallData{ID: "call-1", Name: "notes_agent_set", Arguments: args}
	live.appendTurn(schema.TurnAssistant, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
	})
	out := notesToolExec(context.Background(), t, live, "call-1", "notes_agent_set", map[string]any{"note": "opaque-agent-709"})
	live.appendTurn(schema.TurnToolResults, llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call-1",
				Name:       "notes_agent_set",
				Content:    out,
			},
		}},
	})
	live.maybeAppendNotesContext()
	meta := live.Meta()
	live.Close()

	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), env, meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	// Unchanged store after restore must not append a duplicate TurnNotesContext.
	restored.mu.Lock()
	beforeLen := len(restored.history)
	restored.mu.Unlock()
	restored.maybeAppendNotesContext()
	restored.mu.Lock()
	afterLen := len(restored.history)
	restoredHist := append([]schema.Turn(nil), restored.history...)
	restored.mu.Unlock()
	if afterLen != beforeLen {
		t.Fatalf("restored history length after maybeAppendNotesContext = %d, want %d", afterLen, beforeLen)
	}

	restoredMsgs := expandHistory(restoredHist, replayScope{})
	if !hasToolCallNamed(restoredMsgs, "notes_agent_set") || !hasToolResultNamed(restoredMsgs, "notes_agent_set") {
		t.Fatalf("restored expandHistory lost notes_agent_set tool call or result")
	}
	if joined := strings.Join(userRoleTextMessages(restoredMsgs), "\n"); strings.Contains(joined, "opaque-agent-709") {
		t.Fatalf("restored expandHistory echoed self-authored agent note in user message:\n%s", joined)
	}

	// Verify fork_context path (escapeInheritedHistory) suppresses the self-echo too.
	entries := readPersistedTranscriptEntries(t, stateDir, meta.ID)
	forkHist := escapeInheritedHistory(entries)
	forkMsgs := expandHistory(forkHist, replayScope{})
	if !hasToolCallNamed(forkMsgs, "notes_agent_set") || !hasToolResultNamed(forkMsgs, "notes_agent_set") {
		t.Fatalf("forked expandHistory lost notes_agent_set tool call or result")
	}
	if joined := strings.Join(userRoleTextMessages(forkMsgs), "\n"); strings.Contains(joined, "opaque-agent-709") {
		t.Fatalf("forked expandHistory echoed self-authored agent note in user message:\n%s", joined)
	}
}
