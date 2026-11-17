package agent

import (
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

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
	if responsesContinuationPrefixCarriesModelAuthoredFrame(history, anchorIndex) {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_prefix_model_authored_frame",
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

// responsesContinuationPrefixCarriesModelAuthoredFrame reports whether the
// prefix the provider retains, history up to and including the anchor, carries
// assistant text the replay escape would rewrite.
//
// A continuation sends only the delta and lets the provider replay its own
// retained copy of the prefix, which holds the model's original bytes. A frame
// the model invented there would come back with the spelling a harness frame
// opens with, so the escape every other path applies would be bypassed
// (#6493). The retained prefix cannot be rewritten, so the anchor is refused
// and the request carries the full history instead — where the escape does
// reach it. Histories whose prefix needs no escaping keep anchoring.
func responsesContinuationPrefixCarriesModelAuthoredFrame(history []schema.Turn, anchorIndex int) bool {
	for i := 0; i <= anchorIndex; i++ {
		if history[i].Kind != schema.TurnAssistant {
			continue
		}
		for _, part := range history[i].Message.Content {
			if part.Kind != llm.ContentText || part.Machinery || part.Text == "" {
				continue
			}
			if sysmsg.EscapeModelAuthoredFrames(part.Text) != part.Text {
				return true
			}
		}
	}
	return false
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
