package agent

import (
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// anchorResponsePredatesBoundary reports whether the anchor's stored response
// was produced before the turn that opens the active history.
//
// A checkpoint or summary turn stands for the history it folded away, and the
// provider's retained response does not know about the fold: it still carries
// the folded turns as they were when it was generated. Anchoring to it would
// re-present text the fold removed — including a fabricated frame — even
// though every turn now in scope is clean. The boundary turn is written at the
// fold, so a response generated before that timestamp cannot have seen the
// compacted history and is refused.
func anchorResponsePredatesBoundary(history []schema.Turn, activeStart int, anchor schema.Turn) bool {
	if activeStart <= 0 || activeStart > len(history) {
		return false
	}
	boundary := history[activeStart-1]
	if boundary.Kind != schema.TurnCheckpoint && boundary.Kind != schema.TurnSummary {
		return false
	}
	if anchor.Timestamp.IsZero() || boundary.Timestamp.IsZero() {
		return false
	}
	return anchor.Timestamp.Before(boundary.Timestamp)
}

const responseContextMarkerV1 = "cont-ctx-v1"

type responsesContinuationAnchorCandidate struct {
	TurnIndex int
	Turn      schema.Turn
	Delta     []schema.Turn
}

type responsesContinuationHistoryReservation struct {
	TurnCount int
	LastKind  schema.TurnKind
	LastStamp time.Time
}

func selectResponsesContinuationAnchorCandidate(cfg SessionConfig, history []schema.Turn) (responsesContinuationAnchorCandidate, llm.ResponsesContinuationDecision) {
	if cfg.SystemPromptAsUser {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_system_prompt_as_user",
		}
	}

	activeStart := latestContextBoundaryIndex(history) + 1
	anchorIndex := -1
	for i := len(history) - 1; i >= activeStart; i-- {
		if history[i].Kind == schema.TurnAssistant {
			anchorIndex = i
			break
		}
	}
	if anchorIndex < 0 {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_no_active_anchor",
		}
	}

	anchor := history[anchorIndex]
	if !turnHasResponsesContinuationMetadata(anchor) {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_anchor_metadata_missing",
		}
	}

	delta := responsesContinuationDeltaTurns(history, anchorIndex+1)
	if len(delta) == 0 {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_delta_empty",
		}
	}
	if reason := responsesContinuationDeltaIneligibleReason(anchor, delta); reason != "" {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      reason,
		}
	}
	if responsesContinuationAnchorCarriesEscapableText(history, activeStart, anchorIndex) {
		// The provider retains the anchor's own bytes and replays them from
		// previous_response_id; only the delta is sent. A frame the projection
		// would escape in the local copy therefore reaches the model in the
		// provider's spelling, which is the defect this escape exists to stop
		// (#6493 review). The retained prefix cannot be rewritten, so the
		// request carries the escaped full history instead.
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_prefix_model_authored_frame",
		}
	}
	if anchorResponsePredatesBoundary(history, activeStart, anchor) {
		// The stored response was produced from history that a checkpoint has
		// since folded away. Its retained context still holds whatever that
		// history said, including a frame the fold removed, so anchoring to it
		// brings the frame back even though the local scan above finds nothing
		// to escape (#6493 review). A response produced from the compacted
		// history has no such prefix and anchors normally.
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_anchor_predates_boundary",
		}
	}

	return responsesContinuationAnchorCandidate{
		TurnIndex: anchorIndex,
		Turn:      anchor,
		Delta:     delta,
	}, llm.ResponsesContinuationDecision{
		HistoryMode: llm.HistoryModeResponsesDelta,
		Reason:      "continuation_anchor_candidate",
	}
}

func responsesContinuationDeltaTurns(history []schema.Turn, start int) []schema.Turn {
	if start >= len(history) {
		return nil
	}
	tail := history[start:]
	for i := start; i < len(history); i++ {
		if history[i].Kind == schema.TurnNotesContext && isNotesSelfEchoTurn(history, i) {
			filtered := make([]schema.Turn, 0, len(tail)-1)
			filtered = append(filtered, history[start:i]...)
			for j := i + 1; j < len(history); j++ {
				if history[j].Kind == schema.TurnNotesContext && isNotesSelfEchoTurn(history, j) {
					continue
				}
				filtered = append(filtered, history[j])
			}
			return attentionTransparentTurns(filtered)
		}
	}
	return attentionTransparentTurns(tail)
}

// responsesContinuationAnchorCarriesEscapableText reports whether the
// projection would rewrite any assistant text the provider retains up to and
// including the anchor. It asks the escape itself rather than pattern-matching
// a frame, so a rule change in the escape cannot leave this check behind.
func responsesContinuationAnchorCarriesEscapableText(history []schema.Turn, from, anchorIndex int) bool {
	for i := from; i <= anchorIndex && i < len(history); i++ {
		if history[i].Kind != schema.TurnAssistant {
			continue
		}
		// The anchor decision is only made for an OpenAI Responses request, so
		// the escape is asked against that adapter's message boundaries.
		//
		// The escape reports whether it changed the message; a rewrite confined
		// to a thinking block is invisible to Text() but still reaches the
		// provider in the retained prefix (#6493 review).
		if _, changed := escapeModelAuthoredTurnFramesReporting(history[i].Message, registry.ProtocolOpenAIResponses, false); changed {
			return true
		}
	}
	return false
}

func responsesContinuationDeltaIneligibleReason(anchor schema.Turn, delta []schema.Turn) string {
	anchorToolCallIDs := responsesContinuationToolCallIDs(anchor.Message)
	for _, turn := range delta {
		if turn.Kind.TranscriptOnly() {
			continue // never on the wire, so nothing a continuation must carry
		}
		switch {
		case turn.Kind == schema.TurnUserInput || turn.Kind.HarnessContext():
			// Conversation and harness context are eligible as they stand;
			// the unsafe-content check below still applies.
		case turn.Kind == schema.TurnToolResults || turn.Kind == schema.TurnTool:
			if reason := responsesContinuationToolResultsIneligibleReason(turn.Message, anchorToolCallIDs); reason != "" {
				return reason
			}
		default:
			return "continuation_delta_unsupported_turn_kind"
		}
		if responsesContinuationMessageHasUnsafeDeltaContent(turn.Message) {
			return "continuation_delta_unsafe_content"
		}
	}
	return ""
}

func responsesContinuationToolCallIDs(msg llm.Message) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, part := range msg.Content {
		if part.Kind != llm.ContentToolCall || part.ToolCall == nil {
			continue
		}
		id := strings.TrimSpace(part.ToolCall.ID)
		if id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func responsesContinuationToolResultsIneligibleReason(msg llm.Message, anchorToolCallIDs map[string]struct{}) string {
	for _, part := range msg.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
			continue
		}
		if len(part.ToolResult.ImageData) > 0 || strings.TrimSpace(part.ToolResult.ImageMediaType) != "" {
			return "continuation_delta_unsafe_content"
		}
		callID := strings.TrimSpace(part.ToolResult.ToolCallID)
		if callID == "" {
			return "continuation_delta_orphaned_tool_result"
		}
		if _, ok := anchorToolCallIDs[callID]; !ok {
			return "continuation_delta_orphaned_tool_result"
		}
	}
	return ""
}

func responsesContinuationMessageHasUnsafeDeltaContent(msg llm.Message) bool {
	for _, part := range msg.Content {
		switch part.Kind {
		case llm.ContentText:
		case llm.ContentToolResult:
		default:
			return true
		}
	}
	return false
}

func reserveResponsesContinuationHistoryBase(history []schema.Turn) responsesContinuationHistoryReservation {
	reservation := responsesContinuationHistoryReservation{TurnCount: len(history)}
	if len(history) == 0 {
		return reservation
	}
	last := history[len(history)-1]
	reservation.LastKind = last.Kind
	reservation.LastStamp = last.Timestamp
	return reservation
}

func responsesContinuationHistoryBaseStillCurrent(reservation responsesContinuationHistoryReservation, history []schema.Turn) bool {
	if reservation.TurnCount != len(history) {
		return false
	}
	if len(history) == 0 {
		return reservation.LastKind == "" && reservation.LastStamp.IsZero()
	}
	last := history[len(history)-1]
	return reservation.LastKind == last.Kind && reservation.LastStamp.Equal(last.Timestamp)
}

func latestContextBoundaryIndex(history []schema.Turn) int {
	for i := range slices.Backward(history) {
		switch history[i].Kind {
		case schema.TurnCheckpoint, schema.TurnSummary:
			return i
		}
	}
	return -1
}

func turnHasResponsesContinuationMetadata(turn schema.Turn) bool {
	return strings.TrimSpace(turn.ResponseID) != "" &&
		strings.TrimSpace(turn.ResponseIDHash) != "" &&
		strings.TrimSpace(turn.ResponseEndpoint) != "" &&
		strings.TrimSpace(turn.ResponseStorageScopeFingerprint) != "" &&
		strings.TrimSpace(turn.ResponseRequestFingerprint) != "" &&
		strings.TrimSpace(turn.ResponseContextMarker) == responseContextMarkerV1
}
