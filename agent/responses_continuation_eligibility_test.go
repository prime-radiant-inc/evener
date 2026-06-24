package agent

import (
	"testing"
	"time"

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
