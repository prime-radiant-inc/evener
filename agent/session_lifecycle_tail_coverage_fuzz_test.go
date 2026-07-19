//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/mcp"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// FuzzSessionLifecycleTailCoverage drives the small entry and tail branches that
// are otherwise hard to combine with the longer lifecycle programs. The model
// boundary is always scripted and the session clock and execution environment
// are test-owned.
func FuzzSessionLifecycleTailCoverage(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 3, 4, 5} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed byte) {
		mode := seed % 6
		bare := mode == 4
		s := sltcNewSession(t, bare, mode == 2)

		var ctx context.Context = context.Background()
		kind := EntryUserInput
		switch mode {
		case 0:
			s.Close()
		case 1:
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			ctx = cancelled
		case 2:
			s.mu.Lock()
			s.turns = 1
			s.mu.Unlock()
		case 3:
			kind = EntryContinuation
			s.mu.Lock()
			s.steeringQueue = append(s.steeringQueue, steeringMessage{Text: "queued steering"})
			s.mu.Unlock()
		case 4:
			kind = EntryWatchDelivery
		case 5:
			kind = EntryNotification
			s.mu.Lock()
			s.steeringQueue = append(s.steeringQueue, steeringMessage{Text: "notification steering"})
			s.mu.Unlock()
		}

		_, err := s.ProcessInputKind(ctx, "scripted input", nil, kind)
		switch mode {
		case 0:
			if err == nil || err.Error() != "session is closed" {
				t.Fatalf("closed input error = %v", err)
			}
		case 1:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled input error = %v", err)
			}
		case 2:
			// MaxTurns rejection is explicit (85c394d5 "make session budget
			// exhaustion explicit"): an input arriving with turns already at the
			// limit is declined with a typed turns-budget exhaustion, not
			// silently dropped.
			be, exhausted := budgetExhaustionFromError(err)
			if !exhausted || be.Budget != exhaustedBudgetTurns {
				t.Fatalf("mode 2 input error = %v, want max_turns budget exhaustion", err)
			}
		default:
			if err != nil {
				t.Fatalf("mode %d input error = %v", mode, err)
			}
		}
	})
}

func FuzzSessionLifecycleNotificationFaultCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, seed byte) {
		_ = seed
		loadSession := sltcNewSession(t, false, false)
		store, err := jobstore.OpenNoSync(filepath.Join(t.TempDir(), "jobs.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		loadSession.jobManager = &jobManager{store: store}
		if got, retry, injected := loadSession.filterDeliverableJobNotifications([]jobNotification{{JobID: "absent"}}); len(got) != 0 || len(retry) != 0 || len(injected) != 0 {
			t.Fatalf("absent record filtering = (%d,%d,%d)", len(got), len(retry), len(injected))
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, retry, _ := loadSession.filterDeliverableJobNotifications([]jobNotification{{JobID: "closed"}}); len(retry) != 1 {
			t.Fatalf("closed-store retry count = %d, want 1", len(retry))
		}
		if got, retry, injected := loadSession.filterDeliverableJobNotifications([]jobNotification{{Status: jobNotificationEventWatch}}); len(got) != 1 || len(retry) != 0 || len(injected) != 0 {
			t.Fatalf("watch-only filtering = (%d,%d,%d)", len(got), len(retry), len(injected))
		}
		loadSession.jobManager = nil

		markSession := sltcNewSession(t, false, false)
		jm := &jobManager{
			appendEvent: func(jobstore.Event) error { return errors.New("append failed") },
			forward:     func(jobstore.Event) error { return errors.New("forward failed") },
			parentJobID: "parent",
			now:         func() time.Time { return time.Unix(1, 0) },
		}
		markSession.jobManager = jm
		if failed := markSession.markJobNotificationsDelivered([]deliverableJobNotification{{notification: jobNotification{JobID: "nonterminal"}}}); len(failed) != 0 {
			t.Fatalf("nonterminal mark returned %d retries", len(failed))
		}
		failed := markSession.markJobNotificationsDelivered([]deliverableJobNotification{{notification: jobNotification{JobID: "job"}, terminalGen: "gen"}})
		if len(failed) != 1 {
			t.Fatalf("append failure count = %d, want 1", len(failed))
		}
		jm.appendEvent = func(jobstore.Event) error { return nil }
		failed = markSession.markJobNotificationsDelivered([]deliverableJobNotification{{notification: jobNotification{JobID: "job"}, terminalGen: "gen"}})
		if len(failed) != 0 {
			t.Fatalf("forward-only failure returned %d retries", len(failed))
		}
		markSession.jobManager = nil

		appendSession := sltcNewSession(t, false, false)
		appendSession.enqueueJobNotification(jobNotification{JobID: "watch", Status: jobNotificationEventWatch})
		appendCtx := context.WithValue(context.Background(), sessionLifecycleFaultsKey{}, map[string]error{"append_notification": errors.New("append failed")})
		if appendSession.acceptNotificationInput(appendCtx) {
			t.Fatal("notification proceeded after durable append failure")
		}
		loopSession := sltcNewSession(t, false, false)
		loopCtx := context.WithValue(context.Background(), sessionLifecycleFaultsKey{}, map[string]error{
			"inject_watch_notification": errors.New("inject"),
			"settle_watch":              errors.New("settle"),
		})
		if !loopSession.acceptNotificationInput(loopCtx) {
			t.Fatal("injected watch notification did not proceed")
		}
		settleCtx := context.WithValue(context.Background(), sessionLifecycleFaultsKey{}, map[string]error{"settle_watch": errors.New("settle failed")})
		appendSession.settleDeliveredWatchNotification(settleCtx, deliverableJobNotification{watchJM: &jobManager{}})
		settleJM := &jobManager{appendEvent: func(jobstore.Event) error { return nil }, now: func() time.Time { return time.Unix(2, 0) }}
		appendSession.settleDeliveredWatchNotification(context.Background(), deliverableJobNotification{watchJM: settleJM})
	})
}

func FuzzSessionLifecycleTeardownCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, seed byte) {
		_ = seed
		blocked := sltcNewSession(t, false, false)
		blocked.mu.Lock()
		blocked.askPending = []askQuestion{{Header: "question", Question: "pending"}}
		blocked.mu.Unlock()
		if out, err := blocked.ProcessInputKind(context.Background(), "wake", nil, EntryContinuation); err != nil || out != "" {
			t.Fatalf("awaiting autonomous wake = (%q,%v)", out, err)
		}

		closed := sltcNewSession(t, false, false)
		closed.mu.Lock()
		closed.closing = true
		closed.mu.Unlock()
		if _, _, err := closed.processOneInput(context.Background(), "closed", nil, EntryUserInput, nil); err == nil {
			t.Fatal("direct closed input returned nil error")
		}

		discard := sltcNewSession(t, false, false)
		discard.mcpMgr = &mcp.Manager{}
		discard.discardRestoredCandidate()

		parent := sltcNewSession(t, false, false)
		parent.mcpMgr = &mcp.Manager{}
		parent.cfg.ExportATIFPath = parent.stateDir
		child := sltcNewSession(t, false, false)
		child.mu.Lock()
		child.env = execenv.NewLocalExecutionEnvironment(t.TempDir())
		child.mu.Unlock()
		parent.subagents.track(&subagent{id: "owned", sess: child, ownsEnv: true, done: make(chan struct{})})
		parent.Close()
	})
}

func FuzzSessionLifecyclePhaseFaultCoverage(f *testing.F) {
	points := []string{
		"panic", "slash", "round_cancel", "abort_after_model", "abort_after_log",
		"warn", "abort_after_usage", "emit_assistant", "abort_before_tools",
		"abort_before_tools_cancel", "exec_tools", "persist_tools", "after_action",
		"post_tool_steer", "yield_observer",
	}
	for i := range points {
		f.Add(byte(i))
	}
	f.Fuzz(func(t *testing.T, seed byte) {
		point := points[int(seed)%len(points)]
		s := sltcNewToolSession(t)
		if point == "slash" {
			s.pluginCommands = map[string]plugin.Command{"expand": {Name: "expand", Body: "expanded $ARGUMENTS"}}
		}
		ctx := context.WithValue(context.Background(), sessionLifecycleFaultsKey{}, map[string]error{point: errors.New("injected " + point)})
		if point == "panic" {
			defer func() {
				if recover() == nil {
					t.Fatal("panic seam did not panic")
				}
			}()
		}
		input := "/missing"
		if point == "slash" {
			input = "/expand input"
		}
		_, _, err := s.processOneInput(ctx, input, nil, EntryUserInput, nil)
		if point == "panic" {
			return
		}
		if point != "slash" && point != "warn" && point != "yield_observer" && err == nil {
			t.Fatalf("phase %q returned nil error", point)
		}
	})
}

func FuzzSessionLifecyclePromptHookCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, seed byte) {
		_ = seed
		s := sltcNewSession(t, false, false)
		hookClient := llm.NewClient()
		hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant(`{"systemMessage":"visible hook message","hookSpecificOutput":{"additionalContext":"hook context"}}`)}
		}})
		runner := hooks.NewRunner(hookClient, "gpt-5.2")
		runner.Add(plugin.HookUserPromptSubmit, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "return hook output"})
		s.hookRunner = runner
		if _, err := s.ProcessInput(context.Background(), "hooked input", nil); err != nil {
			t.Fatalf("hooked input: %v", err)
		}
	})
}

func sltcNewToolSession(t *testing.T) *Session {
	t.Helper()
	root := t.TempDir()
	args, err := json.Marshal(map[string]any{"action": "view"})
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "task", Name: "task_list", Arguments: args, Type: "function"})
		},
	})
	cfg := SessionConfig{NoProjectPrompts: true, StateDir: root, MaxToolRoundsPerInput: 1, clock: agenttest.NewFakeClock()}
	cfg.testOnly = testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}
	s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), &agenttest.DenyEnv{WorkDir: root, Seed: 101}, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func sltcNewSession(t *testing.T, bare, maxTurns bool) *Session {
	t.Helper()
	root := t.TempDir()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			if bare {
				return llm.Response{Message: llm.Assistant("watch acknowledged")}
			}
			return agenttest.FinalResponse("done")
		},
	})
	cfg := SessionConfig{
		NoProjectPrompts: true,
		StateDir:         root,
		clock:            agenttest.NewFakeClock(),
	}
	if maxTurns {
		cfg.MaxTurns = 1
	}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
	}
	s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), &agenttest.DenyEnv{WorkDir: root, Seed: 100}, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// FuzzSessionLifecyclePureTailCoverage covers totality and edge contracts of
// the pure lifecycle classifiers without constructing a session.
func FuzzSessionLifecyclePureTailCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, seed byte) {
		_ = seed
		if !errors.Is(&emptyResponseExhaustedError{retries: maxEmptyRetries}, errEmptyResponseExhausted) {
			t.Fatal("empty-response exhaustion lost sentinel identity")
		}
		if (&emptyResponseExhaustedError{}).Is(errBareTextWithoutResultTool) {
			t.Fatal("empty-response exhaustion matched unrelated sentinel")
		}

		cases := []struct {
			in   drainInputs
			want drainAction
			skip bool
		}{
			{drainInputs{Awaiting: true, QueuedText: "reply"}, runQueued, true},
			{drainInputs{Awaiting: true}, goIdle, true},
			{drainInputs{FollowUp: "follow"}, runFollowUp, false},
			{drainInputs{QueuedImages: 1}, runQueued, false},
			{drainInputs{NotificationsPending: true}, runNotification, false},
			{drainInputs{}, armGoalGate, false},
			{drainInputs{RanKind: EntryNotification, HaveDeferredCont: true}, runDeferredContInline, true},
			{drainInputs{RanKind: EntryNotification}, goIdle, true},
		}
		for _, tc := range cases {
			got, skip := selectDrainNextAction(tc.in)
			if got != tc.want || skip != tc.skip {
				t.Fatalf("selectDrainNextAction(%+v) = (%v,%v), want (%v,%v)", tc.in, got, skip, tc.want, tc.skip)
			}
		}

		if durableJobNotificationAlreadyInjected(`<job-notification event="watch" job_id="x"></job-notification>`, `job_id="x"`) {
			t.Fatal("watch notification was treated as durable injection")
		}
		if durableJobNotificationAlreadyInjected(`<job-notification status="watch" job_id="x">`, `job_id="x"`) {
			t.Fatal("unterminated watch notification was treated as durable injection")
		}

		s := &Session{}
		if s.jobNotificationAlreadyInjected("") {
			t.Fatal("empty job id was treated as injected")
		}
		survivors, retry, injected := s.filterDeliverableJobNotifications([]jobNotification{{JobID: "missing"}})
		if len(survivors) != 0 || len(retry) != 1 || len(injected) != 0 {
			t.Fatalf("nil-manager filtering = (%d,%d,%d), want (0,1,0)", len(survivors), len(retry), len(injected))
		}
	})
}
