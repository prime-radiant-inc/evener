package agent

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestS2Cov_RecordResponseUsage covers the server-web-search skip branch and the
// full-history-estimate floor branch of recordResponseUsage.
func TestS2Cov_RecordResponseUsage(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	go func() {
		for range sess.Events() {
		}
	}()

	// Server web search inflates usage (~2x); the token baseline update is skipped.
	webResp := llm.Response{
		Usage:   llm.Usage{InputTokens: 500, TotalTokens: 500},
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentWebSearch}}},
	}
	sess.recordResponseUsage(sessionModelResponse{Response: webResp}, llm.Request{}, sess.currentProfile())

	// Normal response with a larger full-history estimate exercises the floor.
	cacheRead := 10
	cacheWrite := 5
	normalResp := llm.Response{
		Usage:   llm.Usage{InputTokens: 100, CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite},
		Message: llm.Assistant("done"),
	}
	input := modelHistorySnapshot{turns: []schema.Turn{}}
	sess.recordResponseUsage(sessionModelResponse{Response: normalResp, history: input}, llm.Request{FullHistoryInputTokensEstimate: 9000}, sess.currentProfile())
	if got := sess.contextMgr.LastInputTokens(); got != 9000 {
		t.Errorf("recorded input tokens = %d, want the full-history estimate of 9000", got)
	}

	const issuer = "https://issuer.example.invalid/v1/responses"
	const other = "https://other.example.invalid/v1/responses"
	reasoning := llm.Assistant("visible answer")
	reasoning.Content = append(reasoning.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: "rs_67", EncryptedContent: "opaque-67"}})
	for _, tc := range []struct {
		name                string
		assistant           llm.Message
		delta               bool
		foreignKeepsHistory bool
	}{
		{"native reasoning", reasoning.BindReasoningEndpoint(issuer), false, false},
		{"stored continuation", llm.Assistant("visible answer"), true, false},
		{"visible conversation", llm.Assistant("visible answer"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(t)
			user := llm.User("continue")
			s.appendTurn(schema.TurnAssistant, tc.assistant)
			s.appendTurn(schema.TurnUserInput, user)
			measured := modelHistorySnapshot{turns: s.history, revision: s.historyRevision}
			req := llm.Request{Messages: []llm.Message{tc.assistant, user}, FullHistoryInputTokensEstimate: 9000}
			if tc.delta {
				req.Continuation = &llm.ContinuationMetadata{AnchorEndpoint: issuer, FullHistory: req.Messages}
				req.HistoryMode, req.PreviousResponseID = llm.HistoryModeResponsesDelta, "resp_92"
				req.Messages = []llm.Message{user}
			}

			local := s.contextMgr.EstimateUsage(s.history, 0).Used
			const billed = 12_000
			for i, endpoint := range []string{
				"https://issuer.example.invalid/another/path",
				other,
				issuer,
			} {
				answer := llm.Response{Usage: llm.Usage{InputTokens: billed}}
				llm.StampEndpointURL(&answer, endpoint, llm.APILogCredentialMaterial{})
				s.recordResponseUsage(sessionModelResponse{Response: answer, history: measured}, req, s.currentProfile())
				wantTokens, wantPressure := billed, billed
				if endpoint == other && !tc.foreignKeepsHistory {
					wantTokens, wantPressure = 0, local
				}
				if got := s.Meta().LastInputTokens; got != wantTokens {
					t.Errorf("provider at %s measured %d resident tokens, want %d", endpoint, got, wantTokens)
				}
				if got := s.contextMgr.EstimateUsage(s.history, 0).Used; got != wantPressure {
					t.Errorf("provider at %s left context usage at %d, want %d", endpoint, got, wantPressure)
				}
				if got := s.CumulativeUsageSnapshot().InputTokens; got != (i+1)*billed {
					t.Errorf("total billed input = %d, want %d", got, (i+1)*billed)
				}
			}
		})
	}
}
