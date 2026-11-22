package agent

import (
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// responseContextMarkerV2 gates Responses continuation anchor eligibility. The
// marker is stamped on each turn the continuation-aware schema writes, and a
// turn is an anchor only when it carries this exact value. It is bumped
// whenever replay's projection of a stored turn changes: an anchor's text never
// reaches a delta request, so a prefix written before the change could hold
// model text the current replay escapes, and a turn the earlier version stamped
// must send full history once — where the projection lands — before a turn this
// version stamped anchors a delta again.
const responseContextMarkerV2 = "cont-ctx-v2"

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

	boundaryIndex := latestContextBoundaryIndex(history)
	activeStart := boundaryIndex + 1
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
	if responsesContinuationAnchorBoundaryStale(anchor, history, boundaryIndex) {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_anchor_boundary_stale",
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
	if responsesContinuationAnchorRewritten(anchor) {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_anchor_frame_escaped",
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

// responsesContinuationAnchorRewritten reports whether replay would rewrite the
// anchor's assistant text. A delta names the anchor with previous_response_id
// and sends only the turns after it, so the anchor's text never reaches the
// request: a rewrite applied to the local copy would leave the provider's
// stored assistant output — a fabricated answers frame included — in front of
// the model. An anchor replay rewrites is therefore not a delta anchor, and the
// request falls back to full history, where the rewrite lands.
func responsesContinuationAnchorRewritten(anchor schema.Turn) bool {
	return escapeTurnFrames(anchor, anchor.Message).Text() != anchor.Message.Text()
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

// contextBoundaryNone names the boundary a request was planned against when
// history held no checkpoint or summary. A turn with an empty name predates the
// field, so it is never checked against the current boundary: older sessions
// keep anchoring as they did.
const contextBoundaryNone = "none"

// contextBoundaryIdentity names one context boundary turn. Its kind and
// timestamp identify it: a fold's new checkpoint or summary always has a later
// timestamp, while a rewrite that moves the same boundary to another index
// keeps its name.
func contextBoundaryIdentity(boundary schema.Turn) string {
	return string(boundary.Kind) + "@" + boundary.Timestamp.UTC().Format(time.RFC3339Nano)
}

// responsesContinuationBoundaryIdentity names the active context boundary in
// history, or contextBoundaryNone when there is none.
func responsesContinuationBoundaryIdentity(history []schema.Turn) string {
	idx := latestContextBoundaryIndex(history)
	if idx < 0 {
		return contextBoundaryNone
	}
	return contextBoundaryIdentity(history[idx])
}

// responsesContinuationAnchorBoundaryStale reports whether anchor was planned
// against a different context boundary than history now holds. A response that
// landed after a fold published a new boundary was planned against the old (or
// absent) one, so its provider context lacks the new checkpoint and it must not
// anchor a delta that would omit it (#6734).
func responsesContinuationAnchorBoundaryStale(anchor schema.Turn, history []schema.Turn, boundaryIndex int) bool {
	planned := strings.TrimSpace(anchor.ResponseContextBoundary)
	if planned == "" {
		return false
	}
	current := contextBoundaryNone
	if boundaryIndex >= 0 {
		current = contextBoundaryIdentity(history[boundaryIndex])
	}
	return planned != current
}

func turnHasResponsesContinuationMetadata(turn schema.Turn) bool {
	return strings.TrimSpace(turn.ResponseID) != "" &&
		strings.TrimSpace(turn.ResponseIDHash) != "" &&
		strings.TrimSpace(turn.ResponseEndpoint) != "" &&
		strings.TrimSpace(turn.ResponseStorageScopeFingerprint) != "" &&
		strings.TrimSpace(turn.ResponseRequestFingerprint) != "" &&
		strings.TrimSpace(turn.ResponseContextMarker) == responseContextMarkerV2
}
