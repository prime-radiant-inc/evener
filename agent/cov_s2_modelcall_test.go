package agent

import (
	"testing"

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
	sess.recordResponseUsage(webResp, llm.Request{}, sess.currentProfile())

	// Normal response with a larger full-history estimate exercises the floor.
	cacheRead := 10
	cacheWrite := 5
	normalResp := llm.Response{
		Usage:   llm.Usage{InputTokens: 100, CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite},
		Message: llm.Assistant("done"),
	}
	sess.recordResponseUsage(normalResp, llm.Request{FullHistoryInputTokensEstimate: 9000}, sess.currentProfile())
}
