package agent

import (
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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

	delta := append([]schema.Turn(nil), history[anchorIndex+1:]...)
	if len(delta) == 0 {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_delta_empty",
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
	for i := len(history) - 1; i >= 0; i-- {
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
