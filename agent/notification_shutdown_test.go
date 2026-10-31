package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestNotificationShutdown(t *testing.T) {
	t.Parallel()
	t.Run("lifetime_callback", testNotificationShutdownLifetimeCallback)
	t.Run("entered_watch_persistence_and_reopen", testNotificationShutdownEnteredWatch)
	t.Run("daemon_retains_watch_and_hint_is_not_input", testNotificationShutdownDaemon)
	for _, oneShot := range []bool{true, false} {
		t.Run(fmt.Sprintf("one_shot=%t/child_completion_during_settlement", oneShot), func(t *testing.T) {
			testNotificationShutdownChild(t, oneShot)
		})
		for _, question := range []bool{true, false} {
			t.Run(fmt.Sprintf("one_shot=%t/question=%t", oneShot, question), func(t *testing.T) {
				testNotificationShutdownHold(t, oneShot, question)
			})
		}
	}
	t.Run("yielded_wait_with_live_producer", TestNotificationResumesYieldedWait)
}

func testNotificationShutdownChild(t *testing.T, oneShot bool) {
	adapter := newMessagingAdapter("SHUTDOWN-CHILD-731a")
	childGate, accepted := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(childGate) })
	defer release()
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.TurnEndsProcess = oneShot })
	wake, hint := newDrainWake()
	var acceptedOnce sync.Once
	root.SetNotifyFunc(func() {
		hint()
		if root.hasPendingNotifications() {
			acceptedOnce.Do(func() { close(accepted) })
		}
	})
	adapter.script("root", func(llm.Request) llm.Response {
		return delegateToolResponse("shutdown-child", "SHUTDOWN-CHILD-731a")
	}, func(llm.Request) llm.Response {
		// The parent's final request is already built. Finish the real child
		// here, then wait for actual durable receiver custody before returning.
		release()
		awaitSignal(t, accepted, "child completion receiver custody")
		return finalResponse("parent-final-before-child-observation-731a")
	}, func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{"child-report-during-final-731a"}, nil)
		return finalResponse("parent-observed-child-731a")
	})
	adapter.script("SHUTDOWN-CHILD-731a", func(llm.Request) llm.Response {
		awaitSignal(t, childGate, "parent final request")
		return finalResponse("child-report-during-final-731a")
	})
	ctx := context.Background()
	if _, err := root.ProcessInput(ctx, "launch child and settle", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := root.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
	}
	if got := len(adapter.requestsFor("root")); got != 3 {
		t.Fatalf("parent requests=%d, want launch, final, and one observation", got)
	}
	assertNotificationPending(t, root, []string{})
	acceptedIDs := 0
	for _, entry := range notificationRawEntries(t, root.TranscriptPath()) {
		if entry.Turn.AttentionID != "" && strings.Contains(entry.Turn.Message.Text(), "child-report-during-final-731a") {
			acceptedIDs++
			requireOwnedDrainAttentionObservedOnce(t, root, entry.Turn.AttentionID)
		}
	}
	if acceptedIDs != 1 {
		t.Fatalf("durable child report acceptances=%d, want 1", acceptedIDs)
	}
}

func testNotificationShutdownHold(t *testing.T, oneShot, question bool) {
	adapter := &fakeAdapter{name: "openai"}
	s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir(), TurnEndsProcess: oneShot}), withoutGitSnapshot())
	wake, hint := newDrainWake()
	s.SetNotifyFunc(hint)
	entered, release := make(chan struct{}), make(chan struct{})
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			if question {
				return toolCallResponse(askUserCall("shutdown-question", askUserArgsValid()))
			}
			close(entered)
			<-release
			return finalResponse("before-explicit-stop-731a")
		},
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{"held-shutdown-body-731a"}, nil)
			return finalResponse("held-shutdown-observed-731a")
		},
	}
	ctx := context.Background()
	const id = "shutdown-held-731a"
	if question {
		if _, err := s.ProcessInput(ctx, "start a question", nil); err != nil {
			t.Fatal(err)
		}
		observationEnqueue(t, s, id, "held-shutdown-body-731a")
	} else {
		go func() {
			for range s.Events() {
			}
		}()
		initialCtx, cancelInitial := context.WithCancel(ctx)
		defer cancelInitial()
		initialDone := make(chan error, 1)
		go func() {
			_, err := s.ProcessInput(initialCtx, "initial request interrupted by user Stop", nil)
			initialDone <- err
		}()
		<-entered
		// This request cannot cover input produced after its construction. Stop
		// cancels and joins that real request without earning an observation.
		observationEnqueue(t, s, id, "held-shutdown-body-731a")
		if _, err := s.InterruptClientMutation(ctx, appwire.TurnInterruptParams{ClientMutationID: "shutdown-explicit-stop"}, func() {
			cancelInitial()
			close(release)
			if err := <-initialDone; err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("interrupted initial input: %v", err)
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("held drain made %d requests, want only the initial request", got)
	}
	assertNotificationPending(t, s, []string{id})
	fold, err := readDelegateAttentionFold(s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(fold.resolutions) != 0 {
		t.Fatalf("held drain wrote unearned observation receipts: %+v", fold.resolutions)
	}
	if !question {
		if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
			ClientMutationID: "shutdown-reengage",
			Input:            []appwire.InputItem{{Type: "text", Text: "resume the user's work"}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, ran, err := s.ProcessClientMutationStart(ctx, nil); err != nil || !ran {
			t.Fatalf("user reengagement ran=%t err=%v", ran, err)
		}
	} else if _, err := s.ProcessInput(ctx, "the user answers", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("release made %d requests, want exactly one continuation", got)
	}
	assertNotificationPending(t, s, []string{})
	requireOwnedDrainAttentionObservedOnce(t, s, id)
}

func testNotificationShutdownLifetimeCallback(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir(), TurnEndsProcess: true}), withoutGitSnapshot())
	wakes, hint := newDrainWake()
	s.SetNotifyFunc(hint)
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("shutdown-initial-731a") },
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{"shutdown-body-71af"}, nil)
			return finalResponse("shutdown-observed-71af")
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := s.ProcessInput(ctx, "shutdown-start-71af", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainJobTree(ctx, wakes); err != nil {
		t.Fatal(err)
	}
	observationEnqueue(t, s, "shutdown-late-71af", "shutdown-body-71af")
	waitForTestSignal(t, wakes, "lifetime wake after terminal drain")
	if _, err := s.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{})
}

func testNotificationShutdownEnteredWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		adapter := &fakeAdapter{name: "openai"}
		s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir(), TurnEndsProcess: true}), withoutGitSnapshot())
		wake, hint := newDrainWake()
		s.SetNotifyFunc(hint)
		watch := func(id string) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: id, Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","source":"self","events":["assistant.tool"],"event_filter":{"tool_name":"read_file"},"note":"entered-source-731a"}`), Type: "function"})
		}
		observed := 0
		adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return watch("first-watch") },
			func(llm.Request) llm.Response { return finalResponse("initial-entered-731a") },
			func(req llm.Request) llm.Response {
				observationRequest(t, req, []string{"entered-source-731a"}, nil)
				observed++
				return finalResponse("observed-entered-731a")
			},
			func(llm.Request) llm.Response { return watch("reopened-watch") },
			func(llm.Request) llm.Response { return finalResponse("reopened-entered-731a") },
			func(req llm.Request) llm.Response {
				observationRequest(t, req, []string{"entered-source-731a"}, nil)
				observed++
				return finalResponse("observed-reopened-731a")
			},
		}
		ctx := context.Background()
		if _, err := s.ProcessInput(ctx, "install first watch", nil); err != nil {
			t.Fatal(err)
		}
		jm := s.jobManager
		entered, release := make(chan struct{}), make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		appendEvent := jm.appendEvent
		jm.appendEvent = func(ev jobstore.Event) error {
			if ev.Kind == jobstore.EventWatchSendPending {
				close(entered)
				<-release
			}
			return appendEvent(ev)
		}
		read := llm.ToolCallData{ID: "source-read", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"missing-entered-731a","intent":"Reading source to exercise the registered watch callback."}`), Type: "function"}
		toolDone := make(chan struct{})
		go func() { defer close(toolDone); s.execTool(ctx, read, llm.FinishReasonToolCalls) }()
		<-entered
		drainDone := make(chan error, 1)
		go func() { _, err := s.DrainJobTree(ctx, wake); drainDone <- err }()
		synctest.Wait()
		select {
		case err := <-drainDone:
			t.Fatalf("drain returned before entered source persistence: %v", err)
		default:
		}
		if got := len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)); got != 0 {
			t.Fatalf("blocked source already persisted %d pending events", got)
		}
		before := time.Now()
		close(release)
		released = true
		<-toolDone
		if err := <-drainDone; err != nil {
			t.Fatal(err)
		}
		jm.appendEvent = appendEvent
		if observed != 1 || !time.Now().Equal(before) {
			t.Fatalf("observed=%d clock=%s, want one observation before clock %s advances", observed, time.Now(), before)
		}
		if jm.watchCount() != 0 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
			t.Fatal("terminal cleanup retained future production or dropped the entered frame")
		}
		assertNotificationPending(t, s, []string{})
		if _, err := s.ProcessInput(ctx, "install a useful watch after terminal cleanup", nil); err != nil {
			t.Fatal(err)
		}
		s.execTool(ctx, read, llm.FinishReasonToolCalls)
		if _, err := s.DrainJobTree(ctx, wake); err != nil {
			t.Fatal(err)
		}
		if observed != 2 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)) != 2 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 2 {
			t.Fatalf("reopened production did not persist and deliver both frames, observed=%d", observed)
		}
		assertNotificationPending(t, s, []string{})
	})
}

func TestNotificationShutdownPreservesDetachedJob(t *testing.T) {
	t.Parallel()
	fifo, release := waitProducerFIFO(t)
	defer release()
	// Read the detached process's completion through a second fixture-owned FIFO.
	completed := filepath.Join(t.TempDir(), "completed")
	if out, err := exec.Command("mkfifo", completed).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	f, err := os.OpenFile(completed, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	finished := make(chan error, 1)
	readerExited := make(chan struct{})
	go func() {
		defer close(readerExited)
		var b [1]byte
		_, err := f.Read(b[:])
		finished <- err
	}()
	defer func() {
		release()
		_ = f.Close()
		waitForTestSignal(t, readerExited, "detached completion FIFO reader cleanup")
	}()
	clock := &notificationTickerClock{FakeClock: agenttest.NewFakeClock(), started: make(chan *notificationTicker, 1)}
	adapter := &fakeAdapter{name: "openai"}
	// Timers are correctly refused in a fresh one-shot session. A daemon may
	// install them, and a later one-shot resume owns their terminal cleanup.
	s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir(), clock: clock}), withoutGitSnapshot())
	wakes, hint := newDrainWake()
	s.SetNotifyFunc(hint)
	command := fmt.Sprintf("read value < %q; printf x > %q", fifo, completed)
	shellArgs, _ := json.Marshal(map[string]any{"command": command, "mode": "detached"})
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: "detached-731a", Name: "shell", Arguments: shellArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: "future-731a", Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","source":"self","repeat_seconds":60,"note":"future-detached-731a"}`), Type: "function"})
		},
		func(llm.Request) llm.Response { return finalResponse("detached-final-731a") },
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{"future-detached-731a", callbackWatchesCancelledAtRestartMessage}, nil)
			return finalResponse("detached-frame-observed-731a")
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := s.ProcessInput(ctx, "detached-start-731a", nil); err != nil {
		t.Fatal(err)
	}
	if s.jobManager.watchCount() != 1 {
		t.Fatalf("real repeated watch was not installed: %s", lastToolResultText(adapter.Requests()[2].Messages))
	}
	for takeDrainWake(wakes) {
	}
	select {
	case <-clock.started:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for repeated watch ticker registration")
	}
	clock.Advance(time.Minute)
	waitForTestSignal(t, wakes, "real timer receiver custody")
	s.jobManager.watchCallbackMu.Lock()
	produced := s.jobManager.watchCallbacksDone
	s.jobManager.watchCallbackMu.Unlock()
	if produced != nil {
		waitForTestSignal(t, produced, "produced timer callback return")
	}
	producedFrames := producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendPending)
	if len(producedFrames) != 1 {
		t.Fatalf("produced frames before cleanup = %d, want 1", len(producedFrames))
	}
	timerDelivery := producedFrames[0].WatchSend.DeliveryID
	copyDir := copyWaitingState(t, s.stateDir)
	meta, err := schema.LoadSessionMeta(copyDir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
		StateDir: copyDir, TurnEndsProcess: true, clock: clock,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.SetNotifyFunc(hint)
	if _, err := s.DrainJobTree(ctx, wakes); err != nil {
		t.Fatal(err)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatal("terminal drain left future repeated-watch production open")
	}
	// No clock advances during the drain, and no future frame may be invented.
	assertNotificationPending(t, s, []string{})
	// Restore closes callback registrations and produces its own durable end
	// notice. It is a second source, not another firing of the repeated timer.
	delivered := producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendDelivered)
	if len(delivered) != 2 || len(producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendDropped)) != 0 {
		t.Fatalf("delivered=%d, want original frame and restart notice without drops", len(delivered))
	}
	for _, event := range delivered {
		state := event.WatchSend
		if state.DeliveryID != timerDelivery && (!state.EndNotice || state.TriggerReason != "runtime_lost") {
			t.Fatalf("unexpected additional delivered source: %+v", state)
		}
		requireOwnedDrainAttentionObservedOnce(t, s, "watch:"+state.DeliveryID+":1")
	}
	for takeDrainWake(wakes) {
	}
	clock.Advance(time.Minute)
	clock.Drain()
	if takeDrainWake(wakes) || len(adapter.Requests()) != 4 || len(producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendPending)) != 2 {
		t.Fatal("canceled future timer invented a wake, request, or source frame")
	}
	s.Close()
	release()
	waitForTestSignal(t, readerExited, "detached process completion after session close")
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func testNotificationShutdownDaemon(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir()}), withoutGitSnapshot())
	wake, hint := newDrainWake()
	s.SetNotifyFunc(hint)
	observed := 0
	observe := func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{"daemon-watch-731a"}, nil)
		observed++
		return finalResponse("daemon-observed-731a")
	}
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: "daemon-watch", Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","source":"self","events":["assistant.tool"],"event_filter":{"tool_name":"read_file"},"note":"daemon-watch-731a"}`), Type: "function"})
		},
		func(llm.Request) llm.Response { return finalResponse("daemon-initial-731a") },
		observe, observe,
	}
	ctx := context.Background()
	if _, err := s.ProcessInput(ctx, "install daemon watch", nil); err != nil {
		t.Fatal(err)
	}
	read := llm.ToolCallData{ID: "daemon-read", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"missing-daemon-731a","intent":"Reading source to exercise retained daemon production."}`), Type: "function"}
	for i := range 2 {
		s.execTool(ctx, read, llm.FinishReasonToolCalls)
		if _, err := s.DrainJobTree(ctx, wake); err != nil {
			t.Fatal(err)
		}
		if observed != i+1 || s.jobManager.watchCount() != 1 {
			t.Fatalf("observed=%d active watches=%d after daemon drain", observed, s.jobManager.watchCount())
		}
	}
	before := len(adapter.requests)
	s.notify()
	if _, err := s.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) != before {
		t.Fatal("resource-only hint manufactured an extra provider request")
	}
	if len(producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendPending)) != 2 || len(producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendDelivered)) != 2 || len(producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendDropped)) != 0 {
		t.Fatal("daemon source journal lost or discarded produced frames")
	}
	assertNotificationPending(t, s, []string{})
}

// drainWakeForTest installs the host-owned hint before a test enters the drain.
func drainWakeForTest(s *Session) chan struct{} {
	wake, hint := newDrainWake()
	s.mu.Lock()
	previous := s.notifyFunc
	s.mu.Unlock()
	s.SetNotifyFunc(func() {
		hint()
		if previous != nil {
			previous()
		}
	})
	return wake
}
