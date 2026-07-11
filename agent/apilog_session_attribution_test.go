package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/llm"
)

// TestSideCallsAttributeToSessionAPILog proves that LLM side calls made outside
// callModelWithFallback's per-attempt context — here the compaction summarizer
// — still carry the session id, so a per-session API logger routes them to the
// session's own <id>.api.jsonl instead of the unattributed bucket.
func TestSideCallsAttributeToSessionAPILog(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	client.Use(logger)

	s := newSession(t, withClient(client), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         stateDir,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	seedSessionHistory(t, s, 14)

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sessLog := filepath.Join(stateDir, "sessions", s.ID()+".api.jsonl")
	if _, err := os.Stat(sessLog); err != nil {
		t.Fatalf("session api log missing: %v", err)
	}
	unattributed := filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")
	if _, err := os.Stat(unattributed); !os.IsNotExist(err) {
		data, _ := os.ReadFile(unattributed)
		t.Fatalf("side call landed in unattributed bucket (stat err=%v):\n%s", err, data)
	}
}
