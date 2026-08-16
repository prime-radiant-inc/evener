//go:build serffuzz

package agent

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzRootWatchTreeProgram covers the durable generic watch-journal lifecycle
// between raw watch observation, progress delivery, and terminal catch-up.
// It runs real job managers and sessions, but confines all effects to their
// test-owned state directories, a fake clock, and the package's scripted LLM
// adapter. In particular, createShell only builds durable job records here; it
// never launches a command.
//
// The program covers four coupled transitions that need to agree on the same
// durable ledger:
//   - generic self/caller journal watches are installed, framed from several
//     event payloads, inspected/listed, and cleared;
//   - a concrete output/progress watch records pending sends, then exercises
//     delivered, deferred, and hard-failure delivery outcomes;
//   - a terminal output-match request takes the retained-output catch-up path;
//   - stable delivery and restore preserve pending journal state.
//
// The oracle checks ledger coherence rather than merely asserting no panic:
// manager views are deterministic, terminal catch-up never leaves a
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
		rwlpRunStableDeliveryProgram(t, r)
		rwlpRunRestoreRetryProgram(t, r)
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

	live, err := jm.createShell(createShellOpts{Command: "rwlp live"})
	if err != nil {
		t.Fatalf("create live shell: %v", err)
	}
	terminal, err := jm.createShell(createShellOpts{Command: "rwlp terminal"})
	if err != nil {
		t.Fatalf("create terminal shell: %v", err)
	}
	liveRun := rwlpRunningJob(t, jm, live.JobID)
	if _, err := jm.appendJobOutput(live.JobID, liveRun.output, []byte("booting\nready before install\n")); err != nil {
		t.Fatalf("seed live output: %v", err)
	}

	receiver, err := jm.configureWatch(watchArgs{
		Source:      "self",
		Target:      runtimeMessageAliasCaller,
		Events:      []string{"assistant.tool"},
		EventFilter: &watchEventFilter{ToolName: "read_file", Status: "ok"},
	})
	if err != nil || !receiver.Watching || receiver.WatchID == "" {
		t.Fatalf("configure generic journal watch = (%+v, %v), want live watch", receiver, err)
	}
	// A distinct generic key retains the broad event framing surface while the
	// filtered journal above remains an assistant.tool-only config.
	if broad, err := jm.configureWatch(watchArgs{
		Source: "self",
		Target: "*",
		Events: []string{"*"},
	}); err != nil || !broad.Watching {
		t.Fatalf("configure broad journal watch = (%+v, %v), want live watch", broad, err)
	}

	sidecar, err := jm.configureWatch(watchArgs{
		Target:             live.JobID,
		OutputMatch:        "(?i)ready",
		ProgressIntervalMS: minWatchProgressIntervalMS,
		Send: &watchSendArgs{
			To:             runtimeMessageAliasCaller,
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

	// Drive the progress decisions synchronously. The fake clock leaves their
	// background timers inert, while these calls cover the exact work the timer
	// goroutines would do.
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
	// Generic manager views must have stable ordering across repeated reads.
	firstView := jm.watchListToolResult()
	secondView := jm.watchListToolResult()
	if !reflect.DeepEqual(firstView, secondView) {
		t.Fatalf("manager list is non-deterministic: %+v vs %+v", firstView, secondView)
	}
	if !rwlpHasWatch(firstView, receiver.WatchID) {
		t.Fatalf("manager list omitted installed watch %q: %+v", receiver.WatchID, firstView)
	}
	if inspected := jm.inspectWatchByID(receiver.WatchID); inspected.WatchID != receiver.WatchID {
		t.Fatalf("manager inspect = %+v, want %q", inspected, receiver.WatchID)
	}

	// Settle the first batch using fuzz-selected delivery classifications. A
	// pending results remain available for the later manager clear; delivered and
	// hard-failure paths must retire their own delivery ids exactly once.
	rwlpDeliverPending(t, jm, r)

	if r.bool() {
		if _, err := jm.clearWatchByID(receiver.WatchID); err != nil {
			t.Fatalf("manager clear %q: %v", receiver.WatchID, err)
		}
		if inspected := jm.inspectWatchByID(receiver.WatchID); inspected.WatchID != receiver.WatchID || inspected.Watching {
			t.Fatalf("manager clear inspection = %+v, want terminal history for %q", inspected, receiver.WatchID)
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
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "terminal observer"},
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

func rwlpRunStableDeliveryProgram(t *testing.T, r *rwlpReader) {
	t.Helper()
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{
		Message: fmt.Sprintf("rwlp stable delivery %d", r.intn(8)),
	})
	pending := fixture.requireOnePending(t)
	if !pending.state.StableReceiver || pending.state.SourceDelegateID != "dlg_source" {
		t.Fatalf("stable watch route = %+v", pending.state)
	}
	if _, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, ""); err != nil {
		t.Fatalf("drain stable watch delivery: %v", err)
	}
	if got := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(got) != 0 {
		t.Fatalf("drained stable watch remained pending: %+v", got)
	}
	if got := countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(pending.state)); got != 1 {
		t.Fatalf("stable watch receiver attention count = %d, want 1", got)
	}
}

// rwlpRunRestoreRetryProgram exercises the restore-time delivery split against
// a real persisted Session. The caller frame must be retokened and settled by
// the notification loop, while an already-durable unroutable frame must be
// dropped exactly once during the restore retry pass. Terminalizing the watched
// job before the restart puts both records in terminalFlush, so the final
// assertions also cover detached-config cleanup rather than only live watches.
func rwlpRunRestoreRetryProgram(t *testing.T, r *rwlpReader) {
	t.Helper()
	stateDir := t.TempDir()
	workDir := t.TempDir()
	clk := agenttest.NewFakeClock()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider:  "openai",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("rwlp restored notification") },
	})

	newSession := func(seed uint64) (*Session, func()) {
		env := &agenttest.DenyEnv{WorkDir: workDir, Seed: seed}
		cfg := SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			clock:            clk,
			LLMSleep:         func(context.Context, time.Duration) error { return nil },
		}
		cfg.testOnly = testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		}
		sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
		if err != nil {
			t.Fatalf("new restore fixture session: %v", err)
		}
		return sess, sess.Close
	}

	sess, closeSession := newSession(uint64(r.next()))
	closed := false
	defer func() {
		if !closed {
			closeSession()
		}
	}()
	jm := sess.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "rwlp restore source"})
	if err != nil {
		t.Fatalf("create restore source: %v", err)
	}
	run := rwlpRunningJob(t, jm, rec.JobID)
	watch, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send: &watchSendArgs{
			To:             runtimeMessageAliasCaller,
			Message:        "rwlp restore " + rwlpTexts[r.intn(len(rwlpTexts))],
			IncludeExcerpt: r.bool(),
		},
	})
	if err != nil || !watch.Watching {
		t.Fatalf("configure restore caller watch = (%+v, %v)", watch, err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("rwlp ready before restore\n")); err != nil {
		t.Fatalf("append restore source output: %v", err)
	}
	pending, err := jm.store.LoadWatchSends()
	if err != nil || len(pending.Pending) != 1 {
		t.Fatalf("caller pending before terminal = (%+v, %v), want one", pending, err)
	}
	var callerState jobstore.WatchSendState
	for _, state := range pending.Pending {
		if state != nil {
			callerState = *state
		}
	}
	if callerState.DeliveryID == "" {
		t.Fatal("caller pending lost delivery id")
	}

	// An independently durable invalid target makes the restore retry take its
	// hard-failure branch while the real caller watch follows the token rail.
	// It is appended through the actual manager store, then reconstructed with
	// the caller record on the next Session's restore path.
	droppedState := callerState
	droppedState.Key.ResolvedSendTo = runtimeMessageAliasCaller + "_missing"
	droppedState.Key.WatchID += "_missing"
	droppedState.DeliveryID += "_missing"
	droppedState.UpdateSeq++
	droppedState.Frame += "\ndelivery_id: " + droppedState.DeliveryID
	if err := jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendPending,
		TS:        clk.Now(),
		WatchSend: &droppedState,
	}}); err != nil {
		t.Fatalf("append restore hard-failure pending: %v", err)
	}

	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "rwlp restore terminal", nil); err != nil {
		t.Fatalf("finalize restore source: %v", err)
	}
	if rwlpLiveWatchExists(jm, watch.WatchID) {
		t.Fatalf("terminal source retained live watch %q", watch.WatchID)
	}
	meta := sess.Meta()
	// This is an abrupt process-loss simulation, deliberately distinct from
	// Session.Close. Graceful close drops terminal-flush sends by contract; a
	// crash closes its durable handles without inventing those terminal events,
	// leaving the next Session to reconstruct and retry the persisted ledger.
	if err := jm.closeStoreOnly(); err != nil {
		t.Fatalf("close crashed restore source store: %v", err)
	}
	if sess.transcript != nil {
		if err := sess.transcript.Close(); err != nil {
			t.Fatalf("close crashed restore source transcript: %v", err)
		}
	}
	if sess.cancelFunc != nil {
		sess.cancelFunc()
	}
	closed = true

	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		&agenttest.DenyEnv{WorkDir: workDir, Seed: uint64(r.next())},
		meta,
		RestoreSessionConfig{
			StateDir: stateDir,
			clock:    clk,
			LLMSleep: func(context.Context, time.Duration) error { return nil },
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		},
	)
	if err != nil {
		t.Fatalf("restore watch retry session: %v", err)
	}
	defer restored.Close()

	rwlpAssertRestoreRetryLedger(t, restored.jobManager, callerState.DeliveryID, droppedState.DeliveryID, false)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := restored.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("restore notification turn %d: %v", i, err)
		}
	}
	rwlpAssertRestoreRetryLedger(t, restored.jobManager, callerState.DeliveryID, droppedState.DeliveryID, true)
}

// rwlpAssertRestoreRetryLedger asserts the durable semantics shared by both
// restore outcomes: each delivery id reaches at most one terminal event, the
// caller state is the sole retryable record before its notification accept, and
// settling everything releases every detached terminal-flush configuration.
func rwlpAssertRestoreRetryLedger(t *testing.T, jm *jobManager, callerID, droppedID string, settled bool) {
	t.Helper()
	pending, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("load restored watch sends: %v", err)
	}
	if settled {
		if len(pending.Pending) != 0 {
			t.Fatalf("restored terminal pendings = %+v, want none after accept", pending.Pending)
		}
	} else if len(pending.Pending) != 1 {
		t.Fatalf("restored retryable pendings = %+v, want caller only", pending.Pending)
	}

	events, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("load restored watch events: %v", err)
	}
	terminal := map[string]jobstore.EventKind{}
	for _, event := range events {
		if event.WatchSend == nil || event.WatchSend.DeliveryID == "" {
			continue
		}
		if !isWatchSendTerminalEvent(event.Kind) {
			continue
		}
		id := event.WatchSend.DeliveryID
		if previous, duplicate := terminal[id]; duplicate {
			t.Fatalf("watch delivery %q reached terminal events %q and %q", id, previous, event.Kind)
		}
		terminal[id] = event.Kind
	}
	if terminal[droppedID] != jobstore.EventWatchSendDropped {
		t.Fatalf("restore hard-failure delivery %q terminal event = %q, want dropped", droppedID, terminal[droppedID])
	}
	if settled {
		if terminal[callerID] != jobstore.EventWatchSendDelivered {
			t.Fatalf("restored caller delivery %q terminal event = %q, want delivered", callerID, terminal[callerID])
		}
	} else if _, delivered := terminal[callerID]; delivered {
		t.Fatalf("caller delivery %q settled before notification accept as %q", callerID, terminal[callerID])
	}

	jm.mu.Lock()
	flushCount := len(jm.terminalFlush)
	liveCount := len(jm.watches)
	jm.mu.Unlock()
	if settled && (flushCount != 0 || liveCount != 0) {
		t.Fatalf("terminal watch cleanup left live=%d flush=%d", liveCount, flushCount)
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
		switch r.intn(4) {
		case 0:
			if err := jm.settleWatchSendDelivered(delivery.cfg, delivery.state); err != nil {
				t.Fatalf("settle pending %q: %v", delivery.state.DeliveryID, err)
			}
		case 2:
			if err := jm.dropWatchSend(delivery.state, delivery.cfg, "rwlp rejected"); err != nil {
				t.Fatalf("drop pending %q: %v", delivery.state.DeliveryID, err)
			}
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
// job_watch API through the stable controller's parent-source edge. A durable
// descriptor grants the capability, the child's exact lease authorizes the
// tool call, and the receiver transcript remains the delivery authority.
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
		controller, _ := newDelegateControllerTestHarness(t, 2, 1)
		parentJM, err := newJobManager(controller.stateDir, "root-session", func(jobNotification) {})
		if err != nil {
			t.Fatalf("new parent observer manager: %v", err)
		}
		childID := "child-dlg_observer"
		childJM, err := newJobManager(controller.stateDir, childID, func(jobNotification) {})
		if err != nil {
			_ = parentJM.closeStoreOnly()
			t.Fatalf("new child observer manager: %v", err)
		}
		parent := &Session{
			id: "root-session", stateDir: controller.stateDir,
			delegateController: controller, delegateRootSessionID: "root-session",
			jobManager: parentJM, state: SessionIdle,
		}
		child := &Session{
			id: childID, stateDir: controller.stateDir, owningDelegateID: "dlg_observer",
			delegateController: controller, delegateRootSessionID: "root-session",
			jobManager: childJM, state: SessionIdle,
		}
		parentJM.delegateController = controller
		childJM.delegateController = controller
		descriptor := stableToolDescriptor(parent, "dlg_observer", "")
		descriptor.ParentWatchGranted = true
		lease := delegateLease{delegateID: "dlg_observer", generation: 1}
		controller.mu.Lock()
		_, err = controller.appendLocked(
			delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: lease.delegateID, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
			delegateControllerRunStartedEvent(lease.delegateID, lease.generation, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()),
		)
		if err == nil {
			controller.rootRuntime = parent
			controller.live[lease.delegateID] = &delegateLiveState{
				runtime: child,
				binding: &delegateRuntimeBinding{
					lease: lease, cancel: func() {}, ready: true, runtime: child,
				},
			}
		}
		controller.mu.Unlock()
		if err != nil {
			_ = childJM.closeStoreOnly()
			_ = parentJM.closeStoreOnly()
			t.Fatalf("seed stable parent observer: %v", err)
		}
		childTranscriptPath := transcriptPath(controller.stateDir, childID)
		writer, err := transcript.NewWriter(childTranscriptPath, transcript.Header{SessionID: childID})
		if err != nil {
			_ = childJM.closeStoreOnly()
			_ = parentJM.closeStoreOnly()
			t.Fatalf("new child observer transcript: %v", err)
		}
		child.attachTranscript(writer)
		t.Cleanup(func() {
			_ = child.closeAttachedTranscript()
			_ = childJM.closeStoreOnly()
			_ = parentJM.closeStoreOnly()
		})
		leaseContext := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)

		args := map[string]any{
			"operation": "create",
			"source":    "parent",
			"events":    []any{"assistant.tool"},
			"event_filter": map[string]any{
				"tool_name": "read_file",
				"status":    "ok",
			},
		}
		createdValue, err := jobWatchToolWithContext(leaseContext, child, args, jobToolResultDefaultMaxChar)
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
		onSessionEventKD(parentJM, events.EventToolCallEnd, events.ToolCallEndData{
			ToolName: "shell", CallID: "rwlp-parent-miss", Error: "not watched",
		})
		onSessionEventKD(parentJM, events.EventToolCallEnd, events.ToolCallEndData{
			ToolName:      "read_file",
			CallID:        "rwlp-parent-read",
			ArgumentsJSON: `{"file_path":"observer.txt"}`,
			Output:        rwlpTexts[r.intn(len(rwlpTexts))],
		})
		pending := parentJM.pendingWatchSendDeliveries(nil)
		if len(pending) != 1 {
			t.Fatalf("parent observer pending sends = %d, want one matching delivery", len(pending))
		}
		pendingState := pending[0].state
		if !pendingState.StableReceiver || pendingState.ReceiverSessionID != childID || pendingState.ReceiverDelegateID != lease.delegateID || pendingState.Key.WatchID != state.WatchID {
			t.Fatalf("parent observer pending route = %+v, want stable child %q/%q watch %q", pendingState, childID, lease.delegateID, state.WatchID)
		}

		// The parent owns the receiver projection. It must show only this child's
		// route, while the child deliberately owns no local copy of the watch.
		listed := parentJM.watchListToolResultForReceiver(child.ID(), lease.delegateID)
		if !rwlpHasWatch(listed, state.WatchID) {
			t.Fatalf("parent receiver list omitted %q: %+v", state.WatchID, listed)
		}
		inspected, ok := parentJM.inspectReceiverWatchByID(state.WatchID, child.ID(), lease.delegateID)
		if !inspected.Watching || inspected.WatchID != state.WatchID || inspected.Source != "parent" {
			t.Fatalf("parent receiver inspect = (%+v, %v)", inspected, ok)
		}

		if err := parent.drainPendingWatchSends(context.Background()); err != nil {
			t.Fatalf("parent observer drain: %v", err)
		}
		if got := parentJM.pendingWatchSendDeliveries(nil); len(got) != 0 {
			t.Fatalf("parent observer drain left pending sends: %+v", got)
		}
		attentionID := stableWatchAttentionID(pendingState)
		if got := countAttentionEntries(t, childTranscriptPath, attentionID); got != 1 {
			t.Fatalf("parent observer attention count = %d, want 1", got)
		}

		clearedValue, err := jobWatchToolWithContext(leaseContext, child, map[string]any{"operation": "clear", "watch_id": state.WatchID}, jobToolResultDefaultMaxChar)
		if err != nil {
			t.Fatalf("child parent-watch clear: %v", err)
		}
		cleared := clearedValue.(tooldefs.StateResult).State.(jobWatchToolResult)
		if cleared.Watching || cleared.WatchID != state.WatchID {
			t.Fatalf("child parent-watch clear = %+v", cleared)
		}
		if check, ok := parentJM.inspectReceiverWatchByID(state.WatchID, child.ID(), lease.delegateID); !ok || check.Watching {
			t.Fatalf("parent receiver inspect after child clear = (%+v, %v)", check, ok)
		}
	})
}
