package agent

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func TestNewSessionKeepsDispatchedSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	appendPath := filepath.Join(dir, "append.md")
	const initial = "APPEND_FIXTURE_00"
	const updated = "APPEND_FIXTURE_100"
	if err := os.WriteFile(appendPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	read := mkToolCall("read-append", "read_file", map[string]any{"file_path": appendPath})
	adapter := newFakeAdapter("openai", append([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			if err := os.WriteFile(appendPath, []byte(updated), 0o600); err != nil {
				t.Error(err)
			}
			return agenttest.ToolCallResponse(read)
		},
	}, repeatFinalResponse(2, "ok")...)...)
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t, withClient(client), withDir(dir), withConfig(SessionConfig{
		AgentsDocPath:      filepath.Join(dir, "no-personal-AGENTS.md"),
		SystemPromptAppend: []string{appendPath},
	}))
	sendOneUserInput(t, sess, "first")
	if err := sess.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	sendOneUserInput(t, sess, "second")

	reqs := adapter.Requests()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want initial, tool continuation and subsequent input", len(reqs))
	}
	if reqs[0].Model != reqs[1].Model || reqs[2].Model != "gpt-5.4" || reqs[0].Model == reqs[2].Model {
		t.Fatalf("unexpected model sequence: %q, %q, %q", reqs[0].Model, reqs[1].Model, reqs[2].Model)
	}
	seenRead := false
	for _, msg := range reqs[1].Messages {
		for _, part := range msg.Content {
			if result := part.ToolResult; result != nil && result.ToolCallID == read.ID && !result.IsError {
				seenRead = true
			}
		}
	}
	if !seenRead {
		t.Fatal("continuation did not receive the successful read result")
	}
	var first string
	for i, req := range reqs {
		if len(req.Messages) == 0 || req.Messages[0].Role != llm.RoleSystem || len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Kind != llm.ContentText {
			t.Fatalf("request %d did not start with a single system text part", i+1)
		}
		prompt := req.Messages[0].Content[0].Text
		t.Logf("request %d (%s): system prompt %d bytes, sha256 %x, updated fixture present: %t", i+1, req.Model, len(prompt), sha256.Sum256([]byte(prompt)), strings.Contains(prompt, updated))
		if i == 0 {
			first = prompt
			if !strings.Contains(first, initial) {
				t.Fatal("initial request did not carry the configured append fixture")
			}
		} else if prompt != first {
			t.Errorf("request %d changed the system prompt from request 1", i+1)
		}
	}
}

// SystemPromptAppend files are documented as always applied. A file that cannot
// be read is still skipped, but the omission must be disclosed through the
// existing render-warning channel instead of silently dropping the operator's
// instructions (CORE-08).
func TestRenderSystemPromptDisclosesUnreadableAppend(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "extra.md")
	const goodSentinel = "GOOD_APPEND_SENTINEL"
	if err := os.WriteFile(good, []byte(goodSentinel), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist.md")

	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{good, missing},
	}))
	_, warning, _ := sess.renderSystemPrompt(sess.env)
	if warning == "" {
		t.Fatal("an unreadable SystemPromptAppend file was skipped without any diagnostic")
	}
	if !strings.Contains(sess.cachedSystemPrompt, goodSentinel) {
		t.Fatalf("the readable append file was not applied:\n%s", sess.cachedSystemPrompt)
	}
}

// A failed refresh must not replace a valid cached prompt with diagnostic
// prose: the model would then run on "Template rendering failed: ..." instead
// of the last prompt that rendered (CORE-08).
func TestRefreshSystemPromptCacheKeepsValidPromptOnRenderFailure(t *testing.T) {
	const cachedSentinel = "CACHED_VALID_PROMPT_SENTINEL"
	sess := newSession(t)
	sess.mu.Lock()
	sess.systemPromptFrozen = false
	sess.cachedSystemPrompt = cachedSentinel
	sess.mu.Unlock()

	forceSystemPromptRenderFailure(t)
	warning, _ := sess.refreshSystemPromptCache(sess.env)
	if warning == "" {
		t.Fatal("the render failure was not reported")
	}
	if sess.cachedSystemPrompt != cachedSentinel {
		t.Fatalf("cached prompt after a failed refresh = %q, want the previous valid prompt", sess.cachedSystemPrompt)
	}
}
