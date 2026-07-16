package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/llm"
	apilog "primeradiant.com/serf/llm/apilog"
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

func TestSessionSettlesProviderResolutionFailureBeforeTransport(t *testing.T) {
	stateDir := t.TempDir()
	client := llm.NewClient()
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	client.Use(logger)
	policy := llm.RetryPolicy{MaxRetries: 0}
	s := newSession(t, withClient(client), withConfig(SessionConfig{
		LLMRetryPolicy:   &policy,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         stateDir,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))

	ctx := llm.WithAPILogContext(context.Background(), s.ID(), 0)
	_, _, attempt, callErr := s.callModelWithFallback(ctx, NewOpenAIProfile("model-a"), llm.Request{
		Provider: "openai",
		Model:    "model-a",
		Messages: []llm.Message{llm.User("hello")},
	}, "", 1)
	if callErr == nil {
		t.Fatal("callModelWithFallback succeeded without a registered provider")
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := filepath.Join(stateDir, "sessions", s.ID()+".api.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("decode zero-attempt settlement: %v", err)
	}
	settlement, ok := record.(apilog.APIAttemptGroupSettlement)
	if !ok {
		t.Fatalf("record = %T, want APIAttemptGroupSettlement", record)
	}
	if settlement.AttemptGroupID != attempt.AttemptGroupID || settlement.FinalAttemptID != "" || settlement.FinalAttemptCount != 0 {
		t.Fatalf("zero-attempt settlement = %+v", settlement)
	}
	if settlement.Outcome != apilog.AttemptTransportFail {
		t.Fatalf("settlement outcome = %q, want %q", settlement.Outcome, apilog.AttemptTransportFail)
	}
	if tail, err := decoder.Next(); tail != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("tail = (%T, %v), want clean EOF", tail, err)
	}
}
