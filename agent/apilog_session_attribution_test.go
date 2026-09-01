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

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	apilog "primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
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

// attemptRecordingLiveModels returns a scripted llm.LiveModelLister listing
// (installable as fakeAdapter.liveModels) that behaves like
// sessionAttributionAdapter.Complete for the model-listing seam: it opens and
// completes a real llm.APIAttempt against whatever ctx it is given, so the
// resulting canonical API-log record's session attribution reflects only what
// the caller's ctx carried in, never a mocked shortcut.
func attemptRecordingLiveModels(providerInstance string) func(context.Context) ([]registry.Model, error) {
	return func(ctx context.Context) ([]registry.Model, error) {
		startedAt := time.Unix(1_700_000_000, 0).UTC()
		attempt := llm.BeginAPIAttempt(ctx, llm.APIAttemptMeta{
			ProviderInstance: providerInstance,
			// "*" matches llm/providers/chatcompletions/models.go's own
			// ListModels: a listing isn't about any one model, but
			// RequestModel is unconditionally required
			// (llm/apilog/record.go) for the attempt to marshal at all.
			RequestModel: "*",
			Method:       http.MethodGet,
			Endpoint:     "https://scripted.invalid/v1/models",
			StartedAt:    startedAt,
		})
		attempt.Complete(llm.APIAttemptResult{
			StatusCode:   http.StatusOK,
			ResponseBody: []byte(`{"data":[]}`),
			Outcome:      apilog.AttemptSuccess,
			FinishedAt:   startedAt.Add(time.Millisecond),
		})
		return []registry.Model{{ID: "gpt-5.2"}}, nil
	}
}

// assertSessionAPILogAttributed fails t unless stateDir/sessions/<sessionID>.api.jsonl
// holds at least one record and stateDir/sessions/unattributed.api.jsonl was
// never created.
func assertSessionAPILogAttributed(t *testing.T, stateDir, sessionID string) {
	t.Helper()
	sessLog := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
	data, err := os.ReadFile(sessLog)
	if err != nil {
		t.Fatalf("read session API log: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("session API log is empty")
	}
	unattributed := filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")
	if _, err := os.Stat(unattributed); !os.IsNotExist(err) {
		data, _ := os.ReadFile(unattributed)
		t.Fatalf("pre-session live model listing landed in unattributed bucket (stat err=%v):\n%s", err, data)
	}
}

// TestPreSessionLiveModelListingAttribution pins issue #745: the live
// model-listing call NewSession and RestoreSessionFromMetaWithConfig each
// issue before any per-turn ctx exists (agent/live_model_metadata.go) must
// attribute to the session's own id whenever one is already available at that
// call site — restore's meta.ID, or a durable delegate's controller-reserved
// cfg.spawn.sessionID — and only fall back to the shared unattributed bucket
// when no id genuinely exists yet, as for a brand-new root session.
func TestPreSessionLiveModelListingAttribution(t *testing.T) {
	t.Run("restore attributes to the restored session id", func(t *testing.T) {
		stateDir := t.TempDir()
		client := llm.NewClient()
		client.Register(&fakeAdapter{name: "openai", liveModels: attemptRecordingLiveModels("openai")})
		logger, err := llm.NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		client.Use(logger)

		meta := schema.SessionMeta{
			ID:        "restored-attribution-session",
			ProfileID: "openai",
			Model:     "gpt-5.2",
			Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		}
		restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		t.Cleanup(func() { restored.Close() })

		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		assertSessionAPILogAttributed(t, stateDir, meta.ID)
	})

	t.Run("NewSession attributes to a pre-reserved delegate session id", func(t *testing.T) {
		stateDir := t.TempDir()
		client := llm.NewClient()
		client.Register(&fakeAdapter{name: "openai", liveModels: attemptRecordingLiveModels("openai")})
		logger, err := llm.NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		client.Use(logger)

		// A durable delegate's child session id is minted and reserved by the
		// delegate controller before NewSession is ever called
		// (delegate_tree_start.go's ReserveCreate); NewSession receives it
		// pre-populated on cfg.spawn.sessionID (delegateRuntime.construct ->
		// prepareSubagentRunFromSelection). Setting it directly here exercises
		// that exact contract without standing up the whole delegate stack.
		childID := identifier.MustNewSessionID()
		cfg := SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			StateDir:         stateDir,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}
		cfg.spawn.sessionID = childID
		sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(func() { sess.Close() })
		if got := sess.ID(); got != childID {
			t.Fatalf("session id = %q, want the pre-reserved %q", got, childID)
		}

		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		assertSessionAPILogAttributed(t, stateDir, childID)
	})

	t.Run("a genuinely fresh root session still lands in unattributed", func(t *testing.T) {
		stateDir := t.TempDir()
		client := llm.NewClient()
		client.Register(&fakeAdapter{name: "openai", liveModels: attemptRecordingLiveModels("openai")})
		logger, err := llm.NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		client.Use(logger)

		sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			StateDir:         stateDir,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(func() { sess.Close() })

		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		// No session id exists until after this call (identifier.NewSessionID()
		// is minted later in NewSession), so this is the one call site the
		// issue leaves unattributed on purpose: there is structurally nothing
		// to attribute to yet.
		unattributed := filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")
		if _, err := os.Stat(unattributed); err != nil {
			t.Fatalf("fresh root session's pre-session listing did not land in unattributed: %v", err)
		}
		ownLog := filepath.Join(stateDir, "sessions", sess.ID()+".api.jsonl")
		if _, err := os.Stat(ownLog); !os.IsNotExist(err) {
			t.Fatalf("fresh root session's pre-session listing unexpectedly reached its own log (stat err=%v)", err)
		}
	})
}

// sessionAPILogProviderInstances decodes every APIAttemptRecord in
// stateDir/sessions/<sessionID>.api.jsonl and returns the set of
// provider_instance values recorded there.
func sessionAPILogProviderInstances(t *testing.T, stateDir, sessionID string) map[string]bool {
	t.Helper()
	f, err := os.Open(filepath.Join(stateDir, "sessions", sessionID+".api.jsonl"))
	if err != nil {
		t.Fatalf("open session API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	instances := map[string]bool{}
	for {
		record, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode session API log: %v", err)
		}
		if attempt, ok := record.(apilog.APIAttemptRecord); ok {
			instances[attempt.ProviderInstance] = true
		}
	}
	return instances
}

// TestNewSessionReleasesPreSessionAPILogRouteOnMembershipFailure pins a
// roborev finding on PR #752: attributing NewSession's pre-session listing to
// a pre-reserved delegate session id (TestPreSessionLiveModelListingAttribution
// above) opens and locks sessions/<id>.api.jsonl before NewSession ever
// acquires ownership of that id or registers its ordinary construction
// failure cleanup. A membership-validation failure right after a successful,
// attributed listing — the earliest possible NewSession failure once that
// route is open — must still release it; otherwise every later use of the
// same *APILogger (a live daemon serving repeated failed delegate starts, or
// a later restore of the same id) hits ErrAPILogTargetLocked/"unavailable"
// for that id for the rest of the process's life.
func TestNewSessionReleasesPreSessionAPILogRouteOnMembershipFailure(t *testing.T) {
	stateDir := t.TempDir()
	client := llm.NewClient()
	// attemptRecordingLiveModels always lists "gpt-5.2"; requesting a
	// different model below makes resolveLiveModelProfileValidated's
	// membership check reject it right after the (successful, attributed)
	// listing completes.
	client.Register(&fakeAdapter{name: "openai", liveModels: attemptRecordingLiveModels("openai")})
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	client.Use(logger)

	childID := identifier.MustNewSessionID()
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         stateDir,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}
	cfg.spawn.sessionID = childID
	_, err = NewSession(client, NewOpenAIProfile("gpt-5.9-does-not-exist"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		t.Fatal("NewSession with a model absent from the live list = nil error, want non-nil")
	}

	// A second, independent *APILogger against the same stateDir stands in
	// for a competing process or a later restore of the same id (the shape
	// cmd/evener/fresh_session_ownership_test.go's foreignLogger uses). The
	// FIRST logger (still open above, wired to client, not yet closed) is
	// what a live daemon process would still be holding: if NewSession
	// leaked the fd/lock it opened while attributing the failed listing to
	// childID, this reservation fails with ErrAPILogTargetLocked.
	contender, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger (contender): %v", err)
	}
	defer contender.Close() //nolint:errcheck
	if err := contender.ReserveSession(childID); err != nil {
		t.Fatalf("contender ReserveSession(%q) failed -- pre-session API-log route leaked: %v", childID, err)
	}
}

// TestNewSessionAttributesOtherProviderStartupListingToSessionAPILog pins a
// roborev LOW finding on PR #752: captureModelAvailability's startup listing
// for every OTHER delegate-eligible provider (not the session's own, whose
// result is reused from the pre-session listing) uses s.sessionCtx, which
// carries no API-log attribution at all — s.id is fully valid by this point
// (called after session construction and ownership acquisition), so this is
// a pure oversight, not a structural gap like NewSession's own pre-session
// listing.
func TestNewSessionAttributesOtherProviderStartupListingToSessionAPILog(t *testing.T) {
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", liveModels: attemptRecordingLiveModels("openai")})
	client.Register(&fakeAdapter{name: "anthropic", liveModels: attemptRecordingLiveModels("anthropic")})
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	client.Use(logger)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         stateDir,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	instances := sessionAPILogProviderInstances(t, stateDir, sess.ID())
	if !instances["anthropic"] {
		t.Fatalf("other-provider startup listing (anthropic) was not attributed to the session API log; recorded provider_instance values: %v", instances)
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
	}, nil, "", 1)
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
