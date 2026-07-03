package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestS2Cov_RenderSystemPrompt_AppendFiles covers the SystemPromptAppend read
// loops in both buildPromptData and renderSystemPrompt: a readable file is
// folded into the prompt and its source log, while a missing file is skipped.
func TestS2Cov_RenderSystemPrompt_AppendFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "extra.md")
	if err := os.WriteFile(good, []byte("EXTRA_APPENDED_INSTRUCTIONS"), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist.md")

	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{good, missing},
	}))

	prompt := sess.renderSystemPrompt(sess.env)
	if !strings.Contains(prompt, "EXTRA_APPENDED_INSTRUCTIONS") {
		t.Fatalf("rendered prompt missing appended file content")
	}

	var sawAppend bool
	for _, src := range sess.promptSourceLog {
		if strings.HasPrefix(src.Label, "append:") {
			sawAppend = true
		}
	}
	if !sawAppend {
		t.Fatalf("prompt source log missing append entry: %+v", sess.promptSourceLog)
	}
}

// TestS2Cov_ValidateModelFallbacks_RejectsCrossProvider covers the cross-provider
// fallback rejection path via NewSession, which validates fallbacks at init.
func TestS2Cov_ValidateModelFallbacks_RejectsCrossProvider(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		MaxSubagentDepth: 1,
		ModelFallbacks:   []string{"anthropic/claude-haiku-4-5-20251001"},
	})
	if err == nil || !strings.Contains(err.Error(), "cross-provider fallbacks are not supported") {
		t.Fatalf("err = %v, want cross-provider fallback rejection", err)
	}
}
