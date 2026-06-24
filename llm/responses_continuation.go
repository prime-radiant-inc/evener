package llm

import "strings"

type HistoryMode string

const (
	HistoryModeFullHistory         HistoryMode = "full_history"
	HistoryModeResponsesDelta      HistoryMode = "responses_delta"
	HistoryModeFullHistoryFallback HistoryMode = "full_history_fallback"
	HistoryModeChatFallback        HistoryMode = "chat_completions_fallback"
)

type ResponsesContinuationMode string

const (
	ResponsesContinuationOff  ResponsesContinuationMode = "off"
	ResponsesContinuationAuto ResponsesContinuationMode = "auto"
)

type ResponsesEndpointFamily string

const (
	ResponsesEndpointFamilyOpenAIPublic ResponsesEndpointFamily = "openai_public"
	ResponsesEndpointFamilyOpenAICodex  ResponsesEndpointFamily = "openai_codex"
)

type ResponsesContinuationSupport struct {
	EndpointFamily        ResponsesEndpointFamily
	StorageShapeProven    bool
	ProductionPathProven  bool
	Enabled               bool
	MaxAnchorAgeSeconds   int64
	StorageShapeProofID   string
	ProductionPathProofID string
}

type ResponsesContinuationDecision struct {
	HistoryMode HistoryMode
	Reason      string
}

func DefaultResponsesContinuationSupportRegistry() map[ResponsesEndpointFamily]ResponsesContinuationSupport {
	return map[ResponsesEndpointFamily]ResponsesContinuationSupport{
		ResponsesEndpointFamilyOpenAIPublic: disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAIPublic),
		ResponsesEndpointFamilyOpenAICodex:  disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAICodex),
	}
}

func ResponsesContinuationSupportFor(registry map[ResponsesEndpointFamily]ResponsesContinuationSupport, family ResponsesEndpointFamily) ResponsesContinuationSupport {
	if support, ok := registry[family]; ok {
		return support
	}
	return disabledResponsesContinuationSupport(family)
}

func DecideResponsesContinuation(mode ResponsesContinuationMode, support ResponsesContinuationSupport) ResponsesContinuationDecision {
	if mode != ResponsesContinuationAuto {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_off",
		}
	}
	if !support.Enabled {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_endpoint_not_enabled",
		}
	}
	if support.MaxAnchorAgeSeconds <= 0 {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_anchor_age_unbounded",
		}
	}
	return ResponsesContinuationDecision{
		HistoryMode: HistoryModeResponsesDelta,
		Reason:      "continuation_enabled",
	}
}

func DecideResponsesContinuationForRequest(mode ResponsesContinuationMode, support ResponsesContinuationSupport, req Request) ResponsesContinuationDecision {
	decision := DecideResponsesContinuation(mode, support)
	if decision.HistoryMode != HistoryModeResponsesDelta {
		return decision
	}
	if strings.TrimSpace(req.ConversationID) != "" {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_conversation_id_present",
		}
	}
	return decision
}

func disabledResponsesContinuationSupport(family ResponsesEndpointFamily) ResponsesContinuationSupport {
	return ResponsesContinuationSupport{EndpointFamily: family}
}
