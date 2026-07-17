package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
	apilog "primeradiant.com/serf/llm/apilog"
)

type sessionAttributionAdapter struct {
	response string
}

func (*sessionAttributionAdapter) Name() string { return "openai" }

func (a *sessionAttributionAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	response := llm.Response{
		Provider: "openai",
		Model:    req.Model,
		Message:  llm.Assistant(a.response),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
	}
	attempt := llm.BeginAPIAttempt(ctx, llm.APIAttemptMeta{
		ProviderInstance: "openai",
		RequestModel:     req.Model,
		Method:           http.MethodPost,
		Endpoint:         "https://scripted.invalid/v1/complete",
		RequestBody:      []byte(`{"input":"test"}`),
		StartedAt:        startedAt,
	})
	attempt.Complete(llm.APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte(`{"output":"ok"}`),
		Response:     &response,
		Outcome:      apilog.AttemptSuccess,
		FinishedAt:   startedAt.Add(time.Millisecond),
	})
	return response, nil
}

func (*sessionAttributionAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestSessionAuxiliaryModelCallsAttributeToSessionAPILog(t *testing.T) {
	tests := []struct {
		name     string
		response string
		call     func(*Session, *llm.Client) error
	}{
		{
			name:     "initial namer",
			response: `{"name":"Initial Session Name"}`,
			call: func(s *Session, _ *llm.Client) error {
				return s.nameSessionFromText(context.Background(), sessionNameSourcePrompt, "initial task")
			},
		},
		{
			name:     "compaction namer",
			response: `{"name":"Compacted Session Name"}`,
			call: func(s *Session, _ *llm.Client) error {
				return s.nameSessionFromText(context.Background(), sessionNameSourceCompaction, "compacted task summary")
			},
		},
		{
			name:     "notification prompt hook",
			response: "hook response",
			call: func(s *Session, client *llm.Client) error {
				runner := hooks.NewRunner(client, "gpt-5.2")
				runner.Add(plugin.HookNotification, plugin.RegisteredHook{
					Matcher: "*",
					Type:    "prompt",
					Prompt:  "summarize $MESSAGE",
				})
				s.hookRunner = runner
				s.runNotificationHook(context.Background(), "warning")
				return nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			adapter := &sessionAttributionAdapter{response: test.response}
			client := llm.NewClient()
			client.Register(adapter)
			logger, err := llm.NewSessionAPILogger(stateDir)
			if err != nil {
				t.Fatalf("NewSessionAPILogger: %v", err)
			}
			client.Use(logger)

			s := newSession(t, withClient(client), withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5.2")), withConfig(SessionConfig{
				MaxSubagentDepth: 1,
				NoProjectPrompts: true,
				StateDir:         stateDir,
				testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			}))
			if err := test.call(s, client); err != nil {
				t.Fatalf("auxiliary model call: %v", err)
			}
			if err := logger.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			sessionLog := filepath.Join(stateDir, "sessions", s.ID()+".api.jsonl")
			data, err := os.ReadFile(sessionLog)
			if err != nil {
				t.Fatalf("read session API log: %v", err)
			}
			if len(data) == 0 {
				t.Fatal("session API log is empty")
			}
			decoder := apilog.NewDecoder(strings.NewReader(string(data)), 1<<20)
			record, err := decoder.Next()
			if err != nil {
				t.Fatalf("decode session API log: %v", err)
			}
			if _, ok := record.(apilog.APIAttemptRecord); !ok {
				t.Fatalf("first session API-log record = %T, want APIAttemptRecord", record)
			}
			unattributed := filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")
			if data, err := os.ReadFile(unattributed); !os.IsNotExist(err) {
				t.Fatalf("auxiliary call landed in unattributed bucket (read err=%v):\n%s", err, data)
			}
		})
	}
}

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

	ctx := llm.WithAPILogContext(context.Background(), s.ID())
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

func TestSessionCloseReleasesAPILogRoute(t *testing.T) {
	stateDir := t.TempDir()
	client := llm.NewClient()
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	client.Use(logger)

	s := newSession(t, withClient(client), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         stateDir,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	if err := logger.ReserveSession(s.ID()); err != nil {
		t.Fatalf("ReserveSession: %v", err)
	}

	s.Close()

	reopened, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger after session close: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	if err := reopened.ReserveSession(s.ID()); err != nil {
		t.Fatalf("session API-log route remained owned after Close: %v", err)
	}
}
