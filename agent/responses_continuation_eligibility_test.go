package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestResponsesContinuationAnchorCandidateRequiresMetadata(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}
	history[1].ResponseContextMarker = ""

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_anchor_metadata_missing" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateUsesLatestContextBoundary(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before")),
		responsesContinuationEligibleAssistantTurn("resp_before"),
		schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_no_active_anchor" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateTreatsSummaryAsBoundary(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before")),
		responsesContinuationEligibleAssistantTurn("resp_before"),
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_no_active_anchor" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateSelectsLatestActiveAssistant(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before")),
		responsesContinuationEligibleAssistantTurn("resp_before"),
		schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
		responsesContinuationEligibleAssistantTurn("resp_after"),
		schema.NewTurn(schema.TurnUserInput, llm.User("delta")),
	}

	anchor, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta ||
		decision.Reason != "continuation_anchor_candidate" {
		t.Fatalf("decision = %+v", decision)
	}
	if anchor.TurnIndex != 4 || anchor.Turn.ResponseID != "resp_after" {
		t.Fatalf("anchor = %+v", anchor)
	}
	if len(anchor.Delta) != 1 || anchor.Delta[0].Message.Text() != "delta" {
		t.Fatalf("delta = %+v", anchor.Delta)
	}
}

func TestResponsesContinuationAnchorCandidateSystemPromptAsUserUsesFullHistory(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{SystemPromptAsUser: true}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_system_prompt_as_user" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateRejectsEmptyDelta(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_delta_empty" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateRejectsUnsupportedDeltaTurnKind(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		schema.NewTurn(schema.TurnSteering, llm.User("steer")),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_delta_unsupported_turn_kind" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateAllowsLinkedToolResultDelta(t *testing.T) {
	anchor := responsesContinuationEligibleAssistantTurn("resp_1")
	anchor.Message = llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:   "call_anchor",
			Name: "shell",
			Type: "function",
		},
	}}}
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		anchor,
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_anchor", "shell", "ok", false)),
	}

	candidate, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta ||
		decision.Reason != "continuation_anchor_candidate" {
		t.Fatalf("decision = %+v", decision)
	}
	if len(candidate.Delta) != 1 || candidate.Delta[0].Kind != schema.TurnToolResults {
		t.Fatalf("candidate = %+v", candidate)
	}
}

func TestResponsesContinuationAnchorCandidateRejectsOrphanedToolResultDelta(t *testing.T) {
	anchor := responsesContinuationEligibleAssistantTurn("resp_1")
	anchor.Message = llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:   "call_anchor",
			Name: "shell",
			Type: "function",
		},
	}}}
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		anchor,
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_other", "shell", "ok", false)),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_delta_orphaned_tool_result" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateRejectsUnsafeDeltaContent(t *testing.T) {
	tests := []struct {
		name string
		part llm.ContentPart
	}{
		{
			name: "image",
			part: llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/image.png"}},
		},
		{
			name: "thinking",
			part: llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "hidden"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			history := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("first")),
				responsesContinuationEligibleAssistantTurn("resp_1"),
				schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{tc.part}}),
			}

			_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
			if decision.HistoryMode != llm.HistoryModeFullHistory ||
				decision.Reason != "continuation_delta_unsafe_content" {
				t.Fatalf("decision = %+v", decision)
			}
		})
	}
}

func TestResponsesContinuationAnchorCandidateUsesRestoredActiveBoundary(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	client := llm.NewClient()
	meta := schema.SessionMeta{
		ID:        "01JRESPONSES4B",
		ProfileID: "openai",
		Model:     "gpt-5.4",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 200},
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	restoredHistory := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("pre-restore user")),
		responsesContinuationEligibleAssistantTurn("resp_before_restore_boundary"),
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nrestored state")),
		schema.NewTurn(schema.TurnUserInput, llm.User("post-restore user")),
	}

	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.4"),
		execenv.NewLocalExecutionEnvironment(dir),
		meta,
		RestoreSessionConfig{
			StateDir:      stateDir,
			resumeHistory: restoredHistory,
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer sess.Close()

	_, decision := selectResponsesContinuationAnchorCandidate(sess.cfg, sess.history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_no_active_anchor" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationHistoryReservationStillCurrentForSameBase(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
	}

	reservation := reserveResponsesContinuationHistoryBase(history)
	if !responsesContinuationHistoryBaseStillCurrent(reservation, history) {
		t.Fatalf("reservation should still match unchanged history: %+v", reservation)
	}
}

func TestResponsesContinuationHistoryReservationRejectsAppendedTurn(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
	}
	reservation := reserveResponsesContinuationHistoryBase(history)

	history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User("new input")))
	if responsesContinuationHistoryBaseStillCurrent(reservation, history) {
		t.Fatalf("reservation should be stale after appended turn: %+v", reservation)
	}
}

func TestResponsesContinuationHistoryReservationRejectsCompactedOrShortenedHistory(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
	}
	reservation := reserveResponsesContinuationHistoryBase(history)

	compacted := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]")),
	}
	if responsesContinuationHistoryBaseStillCurrent(reservation, compacted) {
		t.Fatalf("reservation should be stale after compaction: %+v", reservation)
	}
	if responsesContinuationHistoryBaseStillCurrent(reservation, history[:1]) {
		t.Fatalf("reservation should be stale after shortened history: %+v", reservation)
	}
}

func TestResponsesContinuationHistoryReservationRejectsSameLengthDifferentLastTurn(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
	}
	reservation := reserveResponsesContinuationHistoryBase(history)

	replaced := append([]schema.Turn(nil), history...)
	replacement := responsesContinuationEligibleAssistantTurn("resp_replacement")
	replacement.Timestamp = time.Date(2026, 6, 24, 13, 0, 0, 0, time.UTC)
	replaced[len(replaced)-1] = replacement
	if responsesContinuationHistoryBaseStillCurrent(reservation, replaced) {
		t.Fatalf("reservation should be stale after same-length replacement: %+v", reservation)
	}
}

func TestResponsesContinuationHistoryReservationAllowsEmptyHistoryUntilChanged(t *testing.T) {
	var history []schema.Turn
	reservation := reserveResponsesContinuationHistoryBase(history)
	if !responsesContinuationHistoryBaseStillCurrent(reservation, history) {
		t.Fatalf("empty reservation should match empty history: %+v", reservation)
	}

	history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User("first")))
	if responsesContinuationHistoryBaseStillCurrent(reservation, history) {
		t.Fatalf("empty reservation should be stale after appended turn: %+v", reservation)
	}
}

func responsesContinuationEligibleAssistantTurn(responseID string) schema.Turn {
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("assistant"))
	turn.Timestamp = time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	turn.ResponseID = responseID
	turn.ResponseIDHash = "cont-handle-v1:response_id:" + responseID
	turn.ResponseProvider = "openai"
	turn.ResponseModel = "gpt-5.4"
	turn.ResponseRequestModel = "gpt-5.4"
	turn.ResponseEndpoint = "https://api.openai.com/v1/responses"
	turn.ResponseStorageScopeFingerprint = "cont-scope-v1:storage_scope:" + responseID
	turn.ResponseRequestFingerprint = "cont-req-v1:" + responseID
	turn.ResponseContextMarker = responseContextMarkerV1
	return turn
}
