package appwire

import "context"

const (
	MethodThreadHistoryRead      = "evener/thread/history/read"
	MethodThreadHistoryTurnsList = "evener/thread/history/turns/list"
)

// HistoryServingFacts describes the view served, not a freshness verdict. A
// saved view's AsOf bounds file completeness, not the age of its newest content.
// Empty facts from an older source never imply live or current content.
type HistoryServingFacts struct {
	ServedFrom                    string `json:"servedFrom,omitempty"`
	FallbackReason                string `json:"fallbackReason,omitempty"`
	AsOf                          string `json:"asOf,omitempty"`
	InstanceID                    string `json:"instanceId,omitempty"`
	ServingFactsUnavailableReason string `json:"servingFactsUnavailableReason,omitempty"`
}

type ThreadHistoryReadResponse struct {
	ThreadReadResponse
	HistoryServingFacts
}

type ThreadHistoryTurnsListResponse struct {
	ThreadTurnsListResponse
	HistoryServingFacts
	LatestExecutionTurnID string `json:"latestExecutionTurnId,omitempty"`
}

// HistoryReadResponse publishes private serving metadata only on the versioned
// method. The legacy response's serialization and stamps remain unchanged.
func HistoryReadResponse(response ThreadReadResponse) ThreadHistoryReadResponse {
	out := ThreadHistoryReadResponse{ThreadReadResponse: response}
	out.HistoryServingFacts, out.Authoritative = normalizeHistoryServingFacts(response.ServingFacts, response.Authoritative)
	if out.LatestExecutionTurnID == "" {
		out.LatestExecutionTurnID = response.Thread.Evener.LatestExecutionTurnID
	}
	return out
}

func HistoryTurnsResponse(response ThreadTurnsListResponse) ThreadHistoryTurnsListResponse {
	out := ThreadHistoryTurnsListResponse{ThreadTurnsListResponse: response, LatestExecutionTurnID: response.LatestExecutionTurnID}
	out.HistoryServingFacts, out.Authoritative = normalizeHistoryServingFacts(response.ServingFacts, response.Authoritative)
	return out
}

func normalizeHistoryServingFacts(facts *HistoryServingFacts, authoritative bool) (HistoryServingFacts, bool) {
	var normalized HistoryServingFacts
	if facts != nil {
		normalized = *facts
	}
	if normalized.ServedFrom == "" && normalized.ServingFactsUnavailableReason == "" {
		normalized.ServingFactsUnavailableReason = "source-unsupported"
	}
	switch normalized.ServedFrom {
	case "saved-index":
		authoritative = false
		normalized.InstanceID = ""
	case "live-daemon":
		authoritative = true
	}
	return normalized, authoritative
}

func (response ThreadHistoryReadResponse) Internal() ThreadReadResponse {
	out := response.ThreadReadResponse
	facts := response.HistoryServingFacts
	out.ServingFacts = &facts
	return out
}

func (response ThreadHistoryTurnsListResponse) Internal() ThreadTurnsListResponse {
	out := response.ThreadTurnsListResponse
	facts := response.HistoryServingFacts
	out.ServingFacts = &facts
	out.LatestExecutionTurnID = response.LatestExecutionTurnID
	return out
}

type historyServingContextKey struct{}

// WithHistoryServingFacts opts an internal read into the versioned source
// methods. It never changes legacy request parameters sent to an older source.
func WithHistoryServingFacts(ctx context.Context) context.Context {
	return context.WithValue(ctx, historyServingContextKey{}, true)
}

func HistoryServingFactsRequested(ctx context.Context) bool {
	requested, _ := ctx.Value(historyServingContextKey{}).(bool)
	return requested
}
