//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func FuzzSessionStateGoalExactCoverage(f *testing.F) {
	for seed := byte(0); seed < 4; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed byte) {
		_ = seed
		fuzzExactGoal(t)
		fuzzExactState(t)
		fuzzExactRepair(t)
		fuzzExactNamer(t)
		if predictionMessage(true, 0.1) == predictionMessage(false, 0.9) {
			t.Fatal("compact predictions collapsed")
		}
	})
}

func fuzzExactGoal(t *testing.T) {
	t.Helper()
	s := &Session{}
	if _, ok := s.currentGoalContinuation(); ok {
		t.Fatal("empty goal continued")
	}
	kicks := 0
	s.SetKickFunc(func(string) { kicks++ })
	s.getOrCreateGoalStore().Set("exact", time.Unix(1, 0))
	if !s.settleGoalOnIdle() || kicks != 1 {
		t.Fatal("active idle goal was not kicked")
	}

	active := func() *Session {
		x := &Session{}
		x.getOrCreateGoalStore().Set("exact", time.Unix(1, 0))
		return x
	}
	userInterrupted := active()
	ctx := WithQueuedInputDrainOnInterrupt(context.Background(), context.Background())
	userInterrupted.terminateGoalOnError(ctx, context.Canceled)
	shutdown := active()
	root, cancel := context.WithCancel(context.Background())
	cancel()
	shutdown.terminateGoalOnError(context.WithValue(context.Background(), queuedInputDrainContextKey{}, queuedInputDrainConfig{rootCtx: root}), context.Canceled)
	failed := active()
	failed.terminateGoalOnError(context.Background(), errors.New("failed"))
	if status, _, _ := failed.GoalStatus(); status != string(goal.StatusBlocked) {
		t.Fatalf("failed goal status = %q", status)
	}
}

func fuzzExactState(t *testing.T) {
	t.Helper()
	s := &Session{state: SessionIdle}
	s.enqueueJobNotification(jobNotification{JobID: "pending"})
	if !s.autonomyInFlight() {
		t.Fatal("notification autonomy missing")
	}
	s = &Session{state: SessionIdle, inputQueue: []queuedInput{{}}}
	if !s.autonomyInFlight() {
		t.Fatal("queue autonomy missing")
	}
	cache := 3
	if cumulativeUsageSnapshot(llm.Usage{CacheReadTokens: &cache}).CacheReadTokens != 3 {
		t.Fatal("cache usage missing")
	}
	if (&Session{}).ContextPressure() != 0 {
		t.Fatal("nil context pressure nonzero")
	}
	closed := &Session{closing: true}
	closed.setStateIfOpenLocked(SessionProcessing)
	if closed.state == SessionProcessing {
		t.Fatal("closing state changed")
	}

	now := time.Unix(20, 0)
	timed := &Session{clock: agenttest.NewFakeClockAt(now), turnStartedAt: now.Add(time.Second)}
	if timed.accumulateWorkLocked() != 0 {
		t.Fatal("negative work was not clamped")
	}
	closing := &Session{closing: true}
	if !errors.Is(closing.abortIfClosing(context.Background()), context.Canceled) || !errors.Is(closing.errIfClosing(), context.Canceled) {
		t.Fatal("closing guards failed")
	}
	boundary := &Session{state: SessionProcessing, events: make(chan events.SessionEvent, 4), clock: agenttest.NewFakeClock()}
	boundary.finishProcessingAtBoundary(context.WithValue(context.Background(), pendingWatchSendDrainFaultKey{}, errors.New("drain")), SessionIdle)

	restored := &Session{state: SessionIdle, history: []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Assistant("done"))}}
	restored.recomputeRestoredState()
	if restored.state != SessionAwaiting {
		t.Fatalf("restored state = %q", restored.state)
	}
	restored = &Session{state: SessionIdle, history: []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Assistant("done"))}}
	restored.enqueueJobNotification(jobNotification{JobID: "pending"})
	restored.recomputeRestoredState()
	if restored.state != SessionIdle {
		t.Fatalf("autonomous restored state = %q", restored.state)
	}
}

func fuzzExactRepair(t *testing.T) {
	t.Helper()
	call := llm.ToolCallData{ID: "orphan", Name: "tool"}
	s := &Session{history: []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})}}
	if s.repairOrphanedToolResults("restore") != 1 {
		t.Fatal("orphan was not repaired")
	}
	s.retryPendingCallerWatchSendsAfterRepair(context.Background())
	s.retryPendingCallerWatchSendsAfterRepair(context.WithValue(context.Background(), pendingWatchSendDrainFaultKey{}, errors.New("drain")))
}

func fuzzExactNamer(t *testing.T) {
	t.Helper()
	s := &Session{}
	empty := schema.NewTurn(schema.TurnSummary, llm.User(" "))
	s.launchCompactionNamer(context.Background(), empty)
	if err := s.nameSessionFromCompactionTurn(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	s.naming.value, s.naming.source = "manual", sessionNameSourceUser
	s.launchCompactionNamer(context.Background(), schema.NewTurn(schema.TurnSummary, llm.User("summary")))
	if err := s.nameSessionFromCompactionTurn(context.Background(), schema.NewTurn(schema.TurnSummary, llm.User("summary"))); err != nil {
		t.Fatal(err)
	}
	s.naming.value, s.naming.source = "named", sessionNameSourceUser
	if s.shouldApplySessionNameLocked(sessionNameSourcePrompt) || s.shouldApplySessionNameLocked("unknown") {
		t.Fatal("name precedence failed")
	}

	launcher := &Session{stateDir: t.TempDir(), profile: WithCheapModel(NewOpenAIProfile("main"), "cheap")}
	launcher.nameSessionFromTextFunc = func(context.Context, string, string) error { return nil }
	eligible := schema.NewTurn(schema.TurnSummary, llm.User("summary"))
	(&Session{stateDir: t.TempDir(), profile: NewOpenAIProfile("main")}).launchCompactionNamer(context.Background(), eligible)
	launcher.launchCompactionNamer(context.Background(), empty)
	launcher.naming.value, launcher.naming.source = "manual", sessionNameSourceUser
	launcher.launchCompactionNamer(context.Background(), eligible)
	launcher.naming.value, launcher.naming.source = "", ""
	launcher.closing = true
	launcher.launchCompactionNamer(context.Background(), eligible)
	launcher.closing = false
	launcher.launchCompactionNamer(nil, eligible)
	launcher.sendersWG.Wait()

	launcher.naming.value, launcher.naming.source = "manual", sessionNameSourceUser
	if err := launcher.nameSessionFromText(context.Background(), sessionNameSourcePrompt, "ignored"); err != nil {
		t.Fatal(err)
	}
	launcher.naming.value, launcher.naming.source = "manual", sessionNameSourceUser
	if err := launcher.applySessionNameResult(sessionNameResult{Name: "late", Source: sessionNameSourceCompaction}); err != nil {
		t.Fatal(err)
	}

	failure := &Session{stateDir: t.TempDir(), id: "failure", client: llm.NewClient(), profile: WithCheapModel(NewOpenAIProfile("main"), "cheap")}
	failure.cfg.LLMSleep = noNamerSleep
	failure.client.Register(namerFuzzErrorAdapter{})
	failure.cfg.testOnly.namerClient = failure.client
	if err := failure.nameSessionFromText(context.Background(), sessionNameSourcePrompt, "fail"); err == nil {
		t.Fatal("scripted naming failure succeeded")
	}

	writer, err := transcript.NewWriter(filepath.Join(t.TempDir(), "transcript.jsonl"), transcript.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	handler := &Session{transcript: writer, events: make(chan events.SessionEvent, 8), stateDir: t.TempDir(), id: "handler"}
	handler.taskStore = task.NewTaskStore(t.TempDir(), "tasks")
	if _, err := handler.taskStore.Append([]task.TaskInput{{Description: "verify"}}); err != nil {
		t.Fatal(err)
	}
	handler.handleCompactionTurn(eligible)
	handler.reportCompactionTranscriptAppend(errors.New("append"))

	badDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.stateDir, s.id = badDir, "id"
	s.appendSessionNamerLog(sessionlog.SessionLogEntry{})
	s.stateDir = t.TempDir()
	s.reportSessionNamerLogAppend(errors.New("append"))
	openDir := t.TempDir()
	s.stateDir, s.id = openDir, "open"
	logPath := filepath.Join(openDir, sessionsSubdir, "open.log.jsonl")
	if err := os.MkdirAll(logPath, 0o755); err != nil {
		t.Fatal(err)
	}
	s.appendSessionNamerLog(sessionlog.SessionLogEntry{})

	s.closing = true
	if err := s.applySessionNameResult(sessionNameResult{Name: "late", Source: sessionNameSourcePrompt}); !errors.Is(err, context.Canceled) {
		t.Fatalf("late name result error = %v", err)
	}
}
