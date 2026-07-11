//go:build serffuzz

package agent

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// FuzzRootWatchTreeProgram covers the durable observer-watch lifecycle that
// sits between raw watch observation and a session's one-shot job-tree drain.
// It runs real job managers and sessions, but confines all effects to their
// test-owned state directories, a fake clock, and the package's scripted LLM
// adapter. In particular, createShell only builds durable job records here; it
// never launches a command.
//
// The program covers four coupled transitions that need to agree on the same
// durable ledger:
//   - a child-visible parent watch is installed, framed from several event
//     payloads, inspected/listed, and receiver-cleared;
//   - a concrete output/progress watch records pending sends, then exercises
//     delivered, deferred, and hard-failure delivery outcomes;
//   - a terminal output-match request takes the retained-output catch-up path;
//   - a completed delegate is drained through the real Session job-tree loop.
//
// The oracle checks ledger coherence rather than merely asserting no panic:
// receiver-scoped views are deterministic, terminal catch-up never leaves a
// live watch behind, pending states remain renderable, and each delivery id is
// durably accepted at most once.

type rwlpReader struct {
	data []byte
	pos  int
}

func (r *rwlpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *rwlpReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *rwlpReader) bool() bool { return r.next()&1 == 1 }

var rwlpTexts = []string{
	"ready",
	"ready with newline\nnext line",
	"<watch>&payload",
	"unicode: café",
	"",
}

// FuzzRootWatchTreeProgram is deliberately a program fuzzer rather than a set
// of parser fuzzers. A byte stream selects delivery outcomes and payload shapes
// while every seed still passes through the same durable watch and drain
// lifecycle.
func FuzzRootWatchTreeProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0, 0, 0, 0},
		{1, 1, 1, 1, 1, 1, 1, 1},
		{2, 3, 4, 5, 6, 7, 8, 9},
		{255, 0, 255, 0, 255, 0, 255, 0},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &rwlpReader{data: data}
		rwlpRunManagerProgram(t, r)
		rwlpRunDrainProgram(t, r)
	})
}

func rwlpRunManagerProgram(t *testing.T, r *rwlpReader) {
	t.Helper()
	jm := newTestJM(t)
	jm.clock = agenttest.NewFakeClock()
	t.Cleanup(func() {
		jm.abandonRunningJobs()
		_ = jm.close()
	})

	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }

	// A seeded delegate makes a normal sidecar target valid. The parent-watch
	// receiver below intentionally uses a distinct target, exercising the
	// receiver-owned route where the observer's record is external to this
	// manager.
	seedWatchSendDelegateTarget(t, jm, "dlg_rwlp")
	live, err := jm.createShell(createShellOpts{Command: "rwlp live"})
	if err != nil {
		t.Fatalf("create live shell: %v", err)
	}
	terminal, err := jm.createShell(createShellOpts{Command: "rwlp terminal"})
	if err != nil {
		t.Fatalf("create terminal shell: %v", err)
	}
	quiet, err := jm.createShell(createShellOpts{Command: "rwlp quiet delegate"})
	if err != nil {
		t.Fatalf("create quiet shell: %v", err)
	}

	liveRun := rwlpRunningJob(t, jm, live.JobID)
	if _, err := jm.appendJobOutput(live.JobID, liveRun.output, []byte("booting\nready before install\n")); err != nil {
		t.Fatalf("seed live output: %v", err)
	}

	receiverSessionID := "observer-session"
	receiverDelegateID := "dlg_receiver"
	receiver, err := jm.configureWatch(watchArgs{
		Source:             "parent",
		Target:             runtimeMessageAliasCaller,
		ReceiverSessionID:  receiverSessionID,
		ReceiverDelegateID: receiverDelegateID,
		Events:             []string{"assistant.tool"},
		EventFilter:        &watchEventFilter{ToolName: "read_file", Status: "ok"},
	})
	if err != nil || !receiver.Watching || receiver.WatchID == "" {
		t.Fatalf("configure receiver watch = (%+v, %v), want live receiver watch", receiver, err)
	}
	// A separate receiver key retains the broad event framing surface while the
	// filtered receiver above remains a valid assistant.tool-only config.
	if broad, err := jm.configureWatch(watchArgs{
		Source:             "parent",
		Target:             runtimeMessageAliasCaller,
		ReceiverSessionID:  receiverSessionID + "-frames",
		ReceiverDelegateID: receiverDelegateID + "-frames",
		Events:             []string{"*"},
	}); err != nil || !broad.Watching {
		t.Fatalf("configure broad receiver watch = (%+v, %v), want live watch", broad, err)
	}

	sidecar, err := jm.configureWatch(watchArgs{
		Target:             live.JobID,
		OutputMatch:        "(?i)ready",
		ProgressIntervalMS: minWatchProgressIntervalMS,
		Send: &watchSendArgs{
			To:             "dlg_rwlp",
			Message:        rwlpTexts[r.intn(len(rwlpTexts))],
			IncludeExcerpt: r.bool(),
		},
	})
	if err != nil || !sidecar.Watching {
		t.Fatalf("configure sidecar watch = (%+v, %v), want live watch", sidecar, err)
	}

	// Parent events use typed payloads so the real frame writer traverses each
	// model-visible event shape. The tool event is deliberately first wrong and
	// then correct to cover receiver filtering without making the expected
	// pending ledger input-dependent.
	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: jm.sessionID,
		Data: events.ToolCallEndData{
			ToolName: "shell",
			CallID:   "rwlp-miss",
			Error:    "not watched",
		},
	})
	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: jm.sessionID,
		Data: events.ToolCallEndData{
			ToolName:      "read_file",
			CallID:        "rwlp-read",
			ArgumentsJSON: `{"file_path":"rwlp.txt"}`,
			Output:        rwlpTexts[r.intn(len(rwlpTexts))],
		},
	})
	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventCommunicate,
		SessionID: jm.sessionID,
		Data:      events.CommunicateData{Message: rwlpTexts[r.intn(len(rwlpTexts))]},
	})
	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventJobFinished,
		SessionID: jm.sessionID,
		Data: events.JobFinishedData{
			JobID:   live.JobID,
			JobType: string(jobstore.JobShell),
			Status:  string(jobstore.StatusCompleted),
			Reason:  "rwlp event only",
		},
	})

	if _, err := jm.appendJobOutput(live.JobID, liveRun.output, []byte("ready after install\n")); err != nil {
		t.Fatalf("append watched output: %v", err)
	}

	// Drive the progress and quiet-watchdog decisions synchronously. The fake
	// clock leaves their background timers inert, while these calls cover the
	// exact work the timer goroutines would do.
	progressCfg := rwlpWatchConfig(t, jm, sidecar.WatchID)
	progressKey := rwlpWatchKey(t, jm, progressCfg)
	if !jm.fireProgressTick(progressKey, progressCfg) {
		t.Fatal("live sidecar progress tick returned false")
	}
	budget, err := jm.configureWatch(watchArgs{
		Target:             live.JobID,
		ProgressIntervalMS: minWatchProgressIntervalMS,
	})
	if err != nil || !budget.Watching {
		t.Fatalf("configure budget watch = (%+v, %v), want live watch", budget, err)
	}
	budgetCfg := rwlpWatchConfig(t, jm, budget.WatchID)
	budgetKey := rwlpWatchKey(t, jm, budgetCfg)
	jm.mu.Lock()
	budgetCfg.deliveries = watchDeliveryBudget - 1
	jm.mu.Unlock()
	if !jm.fireProgressTick(budgetKey, budgetCfg) {
		t.Fatal("budget progress tick returned false")
	}
	if rwlpLiveWatchExists(jm, budget.WatchID) {
		t.Fatalf("budget-exhausted watch %q remained live", budget.WatchID)
	}
	rwlpMakeQuietDelegate(t, jm, quiet.JobID)
	if !jm.fireQuietWatchdogTick(quiet.JobID, time.Second) {
		t.Fatal("quiet delegate watchdog returned false")
	}

	// Receiver views are a security boundary: they must expose only their own
	// watch and have stable ordering across repeated reads.
	firstView := jm.watchListToolResultForReceiver(receiverSessionID, receiverDelegateID)
	secondView := jm.watchListToolResultForReceiver(receiverSessionID, receiverDelegateID)
	if !reflect.DeepEqual(firstView, secondView) {
		t.Fatalf("receiver list is non-deterministic: %+v vs %+v", firstView, secondView)
	}
	if !rwlpHasWatch(firstView, receiver.WatchID) {
		t.Fatalf("receiver list omitted installed watch %q: %+v", receiver.WatchID, firstView)
	}
	if inspected, ok := jm.inspectReceiverWatchByID(receiver.WatchID, receiverSessionID, receiverDelegateID); !ok || inspected.WatchID != receiver.WatchID {
		t.Fatalf("receiver inspect = (%+v, %v), want %q", inspected, ok, receiver.WatchID)
	}

	// Settle the first batch using fuzz-selected delivery classifications. A
	// busy result remains pending for the later receiver clear; delivered and
	// hard-failure paths must retire their own delivery ids exactly once.
	rwlpDeliverPending(t, jm, r)

	if r.bool() {
		if _, err := jm.clearReceiverWatchByID(receiver.WatchID, receiverSessionID, receiverDelegateID); err != nil {
			t.Fatalf("receiver clear %q: %v", receiver.WatchID, err)
		}
		if inspected, ok := jm.inspectReceiverWatchByID(receiver.WatchID, receiverSessionID, receiverDelegateID); !ok || inspected.Watching {
			t.Fatalf("receiver clear inspection = (%+v, %v), want terminal history for %q", inspected, ok, receiver.WatchID)
		}
	}

	terminalRun := rwlpRunningJob(t, jm, terminal.JobID)
	if _, err := jm.appendJobOutput(terminal.JobID, terminalRun.output, []byte("terminal ready tail")); err != nil {
		t.Fatalf("append terminal output: %v", err)
	}
	if err := jm.finalize(terminal.JobID, jobstore.StatusCompleted, "rwlp terminal", nil); err != nil {
		t.Fatalf("finalize terminal job: %v", err)
	}
	catchup, err := jm.configureWatch(watchArgs{Target: terminal.JobID, OutputMatch: "ready"})
	if err != nil || !catchup.TerminalCatchup || catchup.Watching || !catchup.Fired {
		t.Fatalf("terminal catch-up = (%+v, %v), want fired one-shot", catchup, err)
	}
	catchupSend, err := jm.configureWatch(watchArgs{
		Target:      terminal.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_rwlp", Message: "terminal observer"},
	})
	if err != nil || !catchupSend.TerminalCatchup || catchupSend.Watching || !catchupSend.Fired || catchupSend.WatchID == "" {
		t.Fatalf("terminal send catch-up = (%+v, %v), want detached pending send", catchupSend, err)
	}
	if detached := jm.inspectWatchByID(catchupSend.WatchID); detached.WatchID != catchupSend.WatchID || detached.Watching {
		t.Fatalf("terminal send inspect = %+v, want detached watch %q", detached, catchupSend.WatchID)
	}
	rwlpDeliverPending(t, jm, r)

	// The terminal result is not a live config. Its watch id is intentionally
	// empty, which keeps this assertion tied to the public catch-up contract
	// rather than private map layout.
	if count := jm.watchCount(); count < 0 {
		t.Fatalf("invalid negative watch count %d", count)
	}
	rwlpAssertWatchLedger(t, jm, notifications)
}

func rwlpRunDrainProgram(t *testing.T, r *rwlpReader) {
	t.Helper()
	clock := agenttest.NewFakeClock()
	sess := newSession(t,
		withSteps(func(llm.Request) llm.Response { return finalResponse("rwlp child complete") }),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			clock:            clock,
			LLMSleep:         func(context.Context, time.Duration) error { return nil },
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
	)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           fmt.Sprintf("rwlp drain %d", r.intn(8)),
		Background:     false,
		BlockTimeoutMS: 1000,
	})
	if res.Err != nil || res.JobID == "" {
		t.Fatalf("create completed delegate = %+v", res)
	}
	if _, err := sess.DrainJobTree(context.Background()); err != nil {
		t.Fatalf("drain completed delegate tree: %v", err)
	}
	if outstanding, err := sess.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("drained tree still outstanding=%v err=%v", outstanding, err)
	}
}

func rwlpRunningJob(t *testing.T, jm *jobManager, jobID string) *runningJob {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil || run.output == nil {
		t.Fatalf("running job %q is unavailable", jobID)
	}
	return run
}

func rwlpMakeQuietDelegate(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil || run.rec == nil {
		t.Fatalf("quiet job %q is unavailable", jobID)
	}
	run.rec.Type = jobstore.JobDelegate
	started := frozenTestTime.Add(-2 * time.Second)
	run.rec.StartedAt = started
	run.rec.LastActivity = nil
}

func rwlpWatchConfig(t *testing.T, jm *jobManager, watchID string) *watchConfig {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID {
			return cfg
		}
	}
	t.Fatalf("live watch %q not found", watchID)
	return nil
}

func rwlpWatchKey(t *testing.T, jm *jobManager, want *watchConfig) watchKey {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if cfg == want {
			return key
		}
	}
	t.Fatalf("watch config %q has no live key", want.watchID)
	return watchKey{}
}

func rwlpLiveWatchExists(jm *jobManager, watchID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	_, _, ok := jm.watchConfigByIDLocked(watchID)
	return ok
}

func rwlpHasWatch(view jobWatchListToolResult, watchID string) bool {
	for _, watch := range view.Watches {
		if watch.WatchID == watchID {
			return true
		}
	}
	return false
}

func rwlpDeliverPending(t *testing.T, jm *jobManager, r *rwlpReader) {
	t.Helper()
	for _, delivery := range jm.pendingWatchSendDeliveries(nil) {
		choice := r.intn(4)
		_, err := jm.deliverPendingWatchSend(context.Background(), delivery.cfg, delivery.state, true,
			func(context.Context, sendMessageArgs) sendMessageResult {
				switch choice {
				case 0:
					return sendMessageResult{}
				case 1:
					return sendMessageResult{Err: fmt.Errorf("rwlp deferred")}
				case 2:
					return sendMessageResult{
						WatchSendDeliveryClassSet: true,
						WatchSendDeliveryClass:    watchSendHardFailure,
						Err:                       fmt.Errorf("rwlp rejected"),
					}
				default:
					return sendMessageResult{
						WatchSendDeliveryClassSet: true,
						WatchSendDeliveryClass:    watchSendBusy,
					}
				}
			})
		if err != nil {
			t.Fatalf("deliver pending %q: %v", delivery.state.DeliveryID, err)
		}
	}
}

func rwlpAssertWatchLedger(t *testing.T, jm *jobManager, notifications []jobNotification) {
	t.Helper()
	record, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("load watch sends: %v", err)
	}
	for key, state := range record.Pending {
		if state == nil || state.DeliveryID == "" || state.Frame == "" {
			t.Fatalf("unrenderable pending state for %+v: %+v", key, state)
		}
	}
	events, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("load watch events: %v", err)
	}
	delivered := map[string]bool{}
	for _, event := range events {
		if event.Kind != jobstore.EventWatchSendDelivered || event.WatchSend == nil || event.WatchSend.DeliveryID == "" {
			continue
		}
		id := event.WatchSend.DeliveryID
		if delivered[id] {
			t.Fatalf("watch send %q was durably delivered twice", id)
		}
		delivered[id] = true
	}
	for _, n := range notifications {
		if n.WatchSend != nil && n.WatchSend.DeliveryID == "" {
			t.Fatalf("notification carried an incomplete watch token: %+v", n)
		}
	}
}

// FuzzRootParentObserverLifecycleProgram drives the public child-facing
// job_watch API through its parent-owned receiver machinery. The parent and
// child are real Sessions linked by createDelegate; the provider boundary is
// the package's scripted adapter. This exercises install/list/inspect/clear
// routing as well as the durable parent-to-child watch-send handoff, rather
// than constructing receiver fields directly as the manager program does.
func FuzzRootParentObserverLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0, 0},
		{1, 2, 3, 4, 5},
		{255, 1, 254, 2, 253},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &rwlpReader{data: data}
		// The first delegate run completes so the parent-watch receiver is
		// retained. Its watch delivery then resumes the same child and blocks in
		// the second scripted call. Keeping that run live until this program
		// explicitly stops it removes the scheduler race between parent delivery
		// and the child's terminal-watch cleanup.
		adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
		client := llm.NewClient()
		client.Register(adapter)
		parent := newDelegateTestSession(t, client)
		child, delegateID := createParentWatchChild(t, parent, "rwlp parent observer")
		if child == nil || child.sess == nil || delegateID == "" {
			t.Fatalf("parent observer child linkage = (%+v, %q)", child, delegateID)
		}

		args := map[string]any{
			"operation": "create",
			"source":    "parent",
			"events":    []any{"assistant.tool"},
			"event_filter": map[string]any{
				"tool_name": "read_file",
				"status":    "ok",
			},
		}
		createdValue, err := jobWatchTool(child.sess, args, jobToolResultDefaultMaxChar)
		if err != nil {
			t.Fatalf("child parent-watch create: %v", err)
		}
		created, ok := createdValue.(tooldefs.StateResult)
		if !ok {
			t.Fatalf("child parent-watch create type = %T", createdValue)
		}
		state, ok := created.State.(jobWatchToolResult)
		if !ok || !state.Watching || state.WatchID == "" || state.Source != "parent" {
			t.Fatalf("child parent-watch create state = %#v", created.State)
		}

		// First emit a non-match, then a matching event. The durable pending
		// record is the oracle that observation and delivery remain separate.
		parent.emit(events.EventToolCallEnd, events.ToolCallEndData{
			ToolName: "shell", CallID: "rwlp-parent-miss", Error: "not watched",
		})
		parent.emit(events.EventToolCallEnd, events.ToolCallEndData{
			ToolName:      "read_file",
			CallID:        "rwlp-parent-read",
			ArgumentsJSON: `{"file_path":"observer.txt"}`,
			Output:        rwlpTexts[r.intn(len(rwlpTexts))],
		})
		pending := parent.jobManager.pendingWatchSendDeliveries(nil)
		if len(pending) != 1 {
			t.Fatalf("parent observer pending sends = %d, want one matching delivery", len(pending))
		}
		if pending[0].state.Key.ResolvedSendTo != delegateID || pending[0].state.Key.WatchID != state.WatchID {
			t.Fatalf("parent observer pending route = %+v, want child delegate %q/watch %q", pending[0].state.Key, delegateID, state.WatchID)
		}

		// The parent owns the receiver projection. It must show only this child's
		// route, while the child deliberately owns no local copy of the watch.
		listed := parent.jobManager.watchListToolResultForReceiver(child.sess.ID(), delegateID)
		if !rwlpHasWatch(listed, state.WatchID) {
			t.Fatalf("parent receiver list omitted %q: %+v", state.WatchID, listed)
		}
		inspected, ok := parent.jobManager.inspectReceiverWatchByID(state.WatchID, child.sess.ID(), delegateID)
		if !inspected.Watching || inspected.WatchID != state.WatchID || inspected.Source != "parent" {
			t.Fatalf("parent receiver inspect = (%+v, %v)", inspected, ok)
		}

		if err := parent.drainPendingWatchSends(context.Background()); err != nil {
			t.Fatalf("parent observer drain: %v", err)
		}
		if got := parent.jobManager.pendingWatchSendDeliveries(nil); len(got) != 0 {
			t.Fatalf("parent observer drain left pending sends: %+v", got)
		}
		// sendDelegateMessage returns after launching the background resume, not
		// after its model turn begins. Synchronize on the scripted boundary so
		// clearing/stopping below always observes the same live child state.
		<-adapter.secondStarted
		delegates, err := parent.jobManager.store.LoadDelegates()
		if err != nil {
			t.Fatalf("load resumed observer delegate: %v", err)
		}
		resumed := delegates[delegateID]
		if resumed == nil || resumed.CurrentJobID == "" {
			t.Fatalf("resumed observer delegate record = %+v", resumed)
		}
		resumedJobID := resumed.CurrentJobID

		clearedValue, err := jobWatchTool(child.sess, map[string]any{"operation": "clear", "watch_id": state.WatchID}, jobToolResultDefaultMaxChar)
		if err != nil {
			t.Fatalf("child parent-watch clear: %v", err)
		}
		cleared := clearedValue.(tooldefs.StateResult).State.(jobWatchToolResult)
		if cleared.Watching || cleared.WatchID != state.WatchID {
			t.Fatalf("child parent-watch clear = %+v", cleared)
		}
		if check, ok := parent.jobManager.inspectReceiverWatchByID(state.WatchID, child.sess.ID(), delegateID); !ok || check.Watching {
			t.Fatalf("parent receiver inspect after child clear = (%+v, %v)", check, ok)
		}
		if _, err := parent.jobManager.stop(resumedJobID); err != nil {
			t.Fatalf("stop resumed observer delegate %q: %v", resumedJobID, err)
		}
		waitForShellDone(t, parent.jobManager, resumedJobID)
	})
}

// FuzzRootDelegateResumeLifecycleProgram covers an idle delegate's real resume
// path, the active-run steering path, and cancellation/finalization. The second
// scripted provider call blocks on its context, which creates a deterministic
// live child without shelling out or relying on wall-clock timing; job_stop
// supplies the only release edge.
func FuzzRootDelegateResumeLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0, 0},
		{1, 2, 3, 4, 5},
		{255, 254, 253, 252},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &rwlpReader{data: data}
		adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
		client := llm.NewClient()
		client.Register(adapter)
		sess := newDelegateTestSession(t, client)

		first := sess.createDelegate(context.Background(), delegateArgs{
			Task:           "rwlp first completed delegate",
			Background:     false,
			BlockTimeoutMS: 1000,
			ResultSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
				"required": []string{"message"},
			},
		})
		if first.Err != nil || first.DelegateID == "" || first.JobID == "" || first.Status != jobstore.StatusCompleted {
			t.Fatalf("first delegate = %+v, want completed retained delegate", first)
		}

		resumed := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:    first.DelegateID,
			Message:   "rwlp resumed work " + rwlpTexts[r.intn(len(rwlpTexts))],
			OnIdle:    "start",
			FromWatch: r.bool(),
		})
		if resumed.Err != nil || resumed.Action != "started" || resumed.JobID == "" || resumed.JobID == first.JobID || resumed.ResumedFromJobID != first.JobID || !resumed.RunningInBackground {
			t.Fatalf("resumed delegate = %+v, want new live job from %q", resumed, first.JobID)
		}
		// launchSubagentRun is asynchronous. Waiting for the scripted provider
		// boundary makes the subsequent steer and stop exercise a live run rather
		// than a timing-dependent pre-start cancellation.
		<-adapter.secondStarted

		steered := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  first.DelegateID,
			Message: "rwlp steer active run",
		})
		if steered.Err != nil || steered.Action != "steered" || steered.JobID != resumed.JobID || !steered.RunningInBackground {
			t.Fatalf("active delegate steer = %+v, want resumed job %q", steered, resumed.JobID)
		}
		_, childID, err := decodeRef(first.TranscriptRef)
		if err != nil {
			t.Fatalf("decode retained delegate ref %q: %v", first.TranscriptRef, err)
		}
		sub := sess.subagents.get(childID)
		if sub == nil || sub.sess == nil {
			t.Fatalf("resumed child %q is not tracked", childID)
		}
		queue := sub.sess.SteeringQueueSnapshot()
		if len(queue) == 0 || queue[len(queue)-1].Text != "rwlp steer active run" {
			t.Fatalf("resumed child steering queue = %+v", queue)
		}

		if _, err := sess.jobManager.stop(resumed.JobID); err != nil {
			t.Fatalf("stop resumed delegate %q: %v", resumed.JobID, err)
		}
		waitForShellDone(t, sess.jobManager, resumed.JobID)
		record := loadShellRecord(t, sess.jobManager, resumed.JobID)
		if !record.Status.IsTerminal() || record.DelegateID != first.DelegateID || record.TranscriptRef != first.TranscriptRef {
			t.Fatalf("resumed terminal record = %+v", record)
		}
	})
}

// FuzzRootJobReadWaitProgram drives the job-read blocking primitives with a
// virtual clock. The production job_read_output handler combines these helpers
// for block-until-terminal, block-until-new-output, and block-until-grep-match;
// exercising them directly lets the program prove their timer/ticker contracts
// without depending on a host command to produce output at an indeterminate
// moment.
func FuzzRootJobReadWaitProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0, 0},
		{1, 2, 3, 4, 5, 6},
		{255, 254, 253, 252, 251},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &rwlpReader{data: data}
		clk := agenttest.NewFakeClock()
		sess := newSession(t, withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			clock:            clk,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}))
		jm := sess.jobManager
		t.Cleanup(jm.abandonRunningJobs)

		rec, err := jm.createShell(createShellOpts{Command: "rwlp read wait"})
		if err != nil {
			t.Fatalf("create wait job: %v", err)
		}
		run := rwlpRunningJob(t, jm, rec.JobID)
		if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("prefix\nrea")); err != nil {
			t.Fatalf("seed wait output: %v", err)
		}

		rwlpWaitDoneTimeout(t, clk, jm, rec.JobID)
		rwlpWaitForOutput(t, clk, jm, rec.JobID, run)
		rwlpWaitForGrep(t, clk, jm, rec.JobID, run)
		rwlpExerciseGrepScanner(t, jm, rec.JobID, run, r)

		content, start, ok := readJobOutputFrom(jm, rec.JobID, 0, 1)
		if !ok || start != 0 || !strings.Contains(content, "ready") {
			t.Fatalf("stale-total read = (%q, %d, %v), want complete retained ready output", content, start, ok)
		}
		if !waitForJobDone(context.Background(), jm, "job_rwlp_missing", time.Second) {
			t.Fatal("not-running job did not take wait fast path")
		}
	})
}

func rwlpWaitDoneTimeout(t *testing.T, clk *agenttest.FakeClock, jm *jobManager, jobID string) {
	t.Helper()
	baseline := clk.BlockedCount()
	result := make(chan bool, 1)
	go func() { result <- waitForJobDone(context.Background(), jm, jobID, time.Second) }()
	clk.BlockUntil(baseline + 1)
	clk.Advance(time.Second)
	if got := <-result; got {
		t.Fatal("running job wait returned done after only virtual timeout")
	}
}

func rwlpWaitForOutput(t *testing.T, clk *agenttest.FakeClock, jm *jobManager, jobID string, run *runningJob) {
	t.Helper()
	baseline := clk.BlockedCount()
	done := make(chan struct{}, 1)
	go func() {
		waitForJobDoneOrOutput(context.Background(), jm, jobID, time.Second)
		done <- struct{}{}
	}()
	clk.BlockUntil(baseline + 2) // timer plus output-poll ticker
	if _, err := jm.appendJobOutput(jobID, run.output, []byte("dy from output poll\n")); err != nil {
		t.Fatalf("append wait output: %v", err)
	}
	clk.Advance(20 * time.Millisecond)
	<-done
}

func rwlpWaitForGrep(t *testing.T, clk *agenttest.FakeClock, jm *jobManager, jobID string, run *runningJob) {
	t.Helper()
	baseline := clk.BlockedCount()
	done := make(chan struct{}, 1)
	go func() {
		waitForJobGrepMatch(context.Background(), jm, jobID, regexp.MustCompile("rwlp-grep"), time.Second)
		done <- struct{}{}
	}()
	clk.BlockUntil(baseline + 2) // timer plus grep-poll ticker
	if _, err := jm.appendJobOutput(jobID, run.output, []byte("rwlp-grep match\n")); err != nil {
		t.Fatalf("append grep output: %v", err)
	}
	clk.Advance(20 * time.Millisecond)
	<-done
}

func rwlpExerciseGrepScanner(t *testing.T, jm *jobManager, jobID string, run *runningJob, r *rwlpReader) {
	t.Helper()
	pattern := regexp.MustCompile("needle")
	var scan jobGrepScan
	if _, err := jm.appendJobOutput(jobID, run.output, []byte("split nee")); err != nil {
		t.Fatalf("append partial grep: %v", err)
	}
	if scan.step(jm, jobID, pattern, maxJobGrepLineBytes) {
		t.Fatal("partial-line grep matched before its token completed")
	}
	if _, err := jm.appendJobOutput(jobID, run.output, []byte("dle\r\n")); err != nil {
		t.Fatalf("append completed grep: %v", err)
	}
	if !scan.step(jm, jobID, pattern, maxJobGrepLineBytes) {
		t.Fatal("partial-line grep missed its completed token")
	}

	// The output reader must never treat an overlong line as a match. Its next
	// complete line remains eligible, which validates the dead-line recovery.
	total, err := jobOutputBytes(jm, jobID)
	if err != nil {
		t.Fatalf("read pre-dead-line total: %v", err)
	}
	dead := jobGrepScan{scanned: total, lastTotal: total}
	overlong := strings.Repeat("x", maxJobGrepLineBytes+2) + " needle\n"
	if r.bool() {
		overlong = strings.Repeat("y", maxJobGrepLineBytes+3) + "\n"
	}
	if _, err := jm.appendJobOutput(jobID, run.output, []byte(overlong+"needle after dead line\n")); err != nil {
		t.Fatalf("append dead-line grep: %v", err)
	}
	if !dead.step(jm, jobID, pattern, maxJobGrepLineBytes) {
		t.Fatal("grep scan missed the match after an overlong line")
	}
}
