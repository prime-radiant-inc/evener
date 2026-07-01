package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
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
	sess.recordResponseUsage(webResp, llm.Request{})

	// Normal response with a larger full-history estimate exercises the floor.
	cacheRead := 10
	cacheWrite := 5
	normalResp := llm.Response{
		Usage:   llm.Usage{InputTokens: 100, CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite},
		Message: llm.Assistant("done"),
	}
	sess.recordResponseUsage(normalResp, llm.Request{FullHistoryInputTokensEstimate: 9000})
}

// TestS2Cov_MaybeWarnContextUsage covers both the under-threshold no-op and the
// over-threshold warning emission.
func TestS2Cov_MaybeWarnContextUsage(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	col := newChanCollector()
	go col.drain(sess)

	// A tiny context window makes even a small request cross the 80% threshold.
	small := NewOpenAIProfile("gpt-5.2").WithLiveModelInfo(llm.ModelInfo{ContextWindow: 4})
	bigReq := llm.Request{Messages: []llm.Message{llm.User(strings.Repeat("token ", 200))}}
	if !sess.maybeWarnContextUsage(small, bigReq) {
		t.Fatal("expected a context-usage warning over threshold")
	}

	// A huge window with a tiny request stays under the threshold.
	big := NewOpenAIProfile("gpt-5.2").WithLiveModelInfo(llm.ModelInfo{ContextWindow: 1_000_000})
	if sess.maybeWarnContextUsage(big, llm.Request{Messages: []llm.Message{llm.User("hi")}}) {
		t.Fatal("did not expect a warning under threshold")
	}
	// Guard: nil profile and zero window return false.
	if sess.maybeWarnContextUsage(nil, bigReq) {
		t.Fatal("nil profile should not warn")
	}

	sess.Close()
	<-col.done // wait for the drain goroutine to consume every event before asserting
	if !col.contains("Context usage at") {
		t.Fatalf("no context-usage warning emitted; got %v", col.messages())
	}
}
