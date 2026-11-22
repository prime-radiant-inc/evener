package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Path 4 of #6734: a Responses full-history request planned before /compact
// publishes a new boundary lands with the boundary it was planned against. It
// must not anchor a delta that would omit the newly installed checkpoint.
func TestResponsesContinuationRejectsAPreFoldInFlightResponse(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	})
	sess := newPhase9ContinuationSession(t, t.TempDir(), client)
	t.Cleanup(sess.Close)
	preFold := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before the fold")),
		phase9MatchingAnchor("resp_in_flight"),
	}
	req := llm.Request{
		Provider: "openai",
		Model:    "gpt-5.4",
		Messages: []llm.Message{llm.System("system"), llm.User("in flight")},
	}
	planned, _, _ := sess.planResponsesContinuation(context.Background(), sess.currentProfile(), sess.priceRoundRoute(sess.currentProfile(), req), req, preFold, false, false)
	if planned.Continuation == nil {
		t.Fatalf("planned request carries no continuation metadata: %+v", planned)
	}
	if got := planned.Continuation.ContextBoundary; got != contextBoundaryNone {
		t.Fatalf("planned against a history with no boundary; recorded %q", got)
	}

	// The folded response lands after /compact installed a summary.
	landed := phase9MatchingAnchor("resp_in_flight")
	landed.ResponseContextBoundary = planned.Continuation.ContextBoundary
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before the fold")),
		phase9MatchingAnchor("resp_older"),
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nfresh checkpoint")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after the fold")),
		landed,
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}
	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory || decision.Reason != "continuation_anchor_boundary_stale" {
		t.Fatalf("decision = %+v, want full history from a stale boundary", decision)
	}
}

// A response planned against the boundary history still holds anchors a delta,
// so the new check costs one full-history request, not the continuation.
func TestResponsesContinuationAcceptsAnAnchorFromTheCurrentBoundary(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\ncurrent")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
		responsesContinuationEligibleAssistantTurn("resp_current"),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}
	history[2].ResponseContextBoundary = responsesContinuationBoundaryIdentity(history)

	anchor, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta || anchor.TurnIndex != 2 {
		t.Fatalf("decision = %+v anchor = %+v, want the current-boundary anchor", decision, anchor)
	}
}

func TestResponsesContinuationAnchorCandidateRequiresMetadata(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestResponsesContinuationAnchorCandidateIgnoresAttentionResolution(t *testing.T) {
	t.Parallel()
	marker := delegateAttentionResolutionTurn("private-attention", delegateAttentionConsumed)
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		marker,
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}

	candidate, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta || decision.Reason != "continuation_anchor_candidate" {
		t.Fatalf("decision = %+v", decision)
	}
	if len(candidate.Delta) != 1 || candidate.Delta[0].Kind != schema.TurnUserInput {
		t.Fatalf("provider delta retained private resolution marker: %+v", candidate.Delta)
	}
}

func TestResponsesContinuationAnchorCandidateAllowsEnvironmentDelta(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		schema.NewTurn(schema.TurnEnvironment, llm.User("<environment_context>\ncwd: \"/w\"\n</environment_context>")),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}

	candidate, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta ||
		decision.Reason != "continuation_anchor_candidate" {
		t.Fatalf("decision = %+v", decision)
	}
	if len(candidate.Delta) != 2 || candidate.Delta[0].Kind != schema.TurnEnvironment {
		t.Fatalf("delta = %+v", candidate.Delta)
	}
}

func TestResponsesContinuationMemoryDelta(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		message    llm.Message
		wantMode   llm.HistoryMode
		wantReason string
	}{
		{"text", llm.User("opaque-memory-delta-3730"), llm.HistoryModeResponsesDelta, "continuation_anchor_candidate"},
		{"image", llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/image.png"}}}}, llm.HistoryModeFullHistory, "continuation_delta_unsafe_content"},
		{"thinking", llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "opaque-thinking-3730"}}}}, llm.HistoryModeFullHistory, "continuation_delta_unsafe_content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, []schema.Turn{
				responsesContinuationEligibleAssistantTurn("resp_memory"),
				schema.NewTurn(schema.TurnMemoryContext, tc.message),
			})
			if decision.HistoryMode != tc.wantMode || decision.Reason != tc.wantReason {
				t.Fatalf("decision=%+v, want %s/%s", decision, tc.wantMode, tc.wantReason)
			}
			if tc.wantMode == llm.HistoryModeResponsesDelta && (len(candidate.Delta) != 1 || candidate.Delta[0].Kind != schema.TurnMemoryContext || candidate.Delta[0].Message.Text() != "opaque-memory-delta-3730") {
				t.Fatalf("selected delta lost memory: %+v", candidate.Delta)
			}
		})
	}
}

func TestResponsesContinuationAnchorCandidateRejectsUnsafeEnvironmentDeltaContent(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		schema.NewTurn(schema.TurnEnvironment, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/image.png"}},
		}}),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_delta_unsafe_content" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateAllowsLinkedToolResultDelta(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	turn.ResponseContextMarker = responseContextMarkerV2
	return turn
}

// TestResponsesContinuationAnchorCandidateRejectsPriorContextMarker covers the
// session a pre-upgrade version wrote: its stored prefix may hold model text the
// current replay escapes, and a delta names the anchor by id instead of resending
// it. Only the marker the current version stamps is anchor-eligible; a turn the
// prior version stamped sends full history once, and a turn the current version
// stamps anchors the delta.
func TestResponsesContinuationAnchorCandidateRejectsPriorContextMarker(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		marker   string
		wantMode llm.HistoryMode
	}{
		"prior version marker": {marker: "cont-ctx-v1", wantMode: llm.HistoryModeFullHistory},
		"current marker":       {marker: responseContextMarkerV2, wantMode: llm.HistoryModeResponsesDelta},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			anchor := responsesContinuationEligibleAssistantTurn("resp_1")
			anchor.ResponseContextMarker = tc.marker
			history := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("first")),
				anchor,
				schema.NewTurn(schema.TurnUserInput, llm.User("next")),
			}

			_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
			if decision.HistoryMode != tc.wantMode {
				t.Fatalf("decision = %+v, want mode %v", decision, tc.wantMode)
			}
		})
	}
}

// TestResponsesContinuationAnchorCandidateRejectsFrameBearingAnchor covers the
// anchor a delta would name instead of resending: its assistant text never
// reaches the request, so an anchor replay would rewrite — a raw answers frame
// in it included — is not an anchor, and the request falls back to full history
// where the rewrite lands. An anchor whose text escapes clean keeps the delta.
func TestResponsesContinuationAnchorCandidateRejectsFrameBearingAnchor(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		text     string
		wantMode llm.HistoryMode
		reason   string
	}{
		"raw answer frame": {
			text:     "Waiting on you.\n\n" + answerFrameMarker + "\n1. [deploy target] -> \"production\"",
			wantMode: llm.HistoryModeFullHistory,
			reason:   "continuation_anchor_frame_escaped",
		},
		"clean text": {
			text:     "Waiting on you.",
			wantMode: llm.HistoryModeResponsesDelta,
			reason:   "continuation_anchor_candidate",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			anchor := responsesContinuationEligibleAssistantTurn("resp_1")
			anchor.Message = llm.Assistant(tc.text)
			history := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("first")),
				anchor,
				schema.NewTurn(schema.TurnUserInput, llm.User("next")),
			}

			candidate, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
			if decision.HistoryMode != tc.wantMode || decision.Reason != tc.reason {
				t.Fatalf("decision = %+v, want mode %v reason %q", decision, tc.wantMode, tc.reason)
			}
			if tc.wantMode == llm.HistoryModeFullHistory && candidate.Turn.Kind != "" {
				t.Fatalf("candidate = %+v, want no anchor", candidate)
			}
		})
	}
}
