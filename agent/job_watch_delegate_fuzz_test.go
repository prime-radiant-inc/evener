//go:build evenerfuzz

package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

// This file fuzzes the watch-configuration machinery in job_watch.go. The
// target drives a real *jobManager and *Session built with package test
// helpers, so every store, clock, and filesystem effect stays inside a
// t.TempDir / MemMapFs sandbox — no network, process, or shared disk state.
//
// The oracle checks that after every configureWatch / recordWatchSendPending /
// clearWatchByIDMatching, the manager's watch state stays internally
// consistent (every pending key is tracked in pendingOrder, no nil pending
// state, every live config keeps a non-empty watch id).
//
// All new top-level identifiers carry the watchdel_ lane prefix so parallel
// lanes editing package agent never collide.

// --- shared byte-stream reader ---

type watchdel_reader struct {
	data []byte
	pos  int
}

func (r *watchdel_reader) b() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	v := r.data[r.pos]
	r.pos++
	return v
}

func (r *watchdel_reader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.b()) % n
}

func (r *watchdel_reader) boolean() bool { return r.b()&1 == 1 }

func (r *watchdel_reader) pick(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	return opts[r.intn(len(opts))]
}

func (r *watchdel_reader) take(n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, r.b())
	}
	return out
}

// --- fault gate over the append seam ---

// watchdel_faultGate derives a persist-failure decision function from a fuzz-byte
// plan using fuzz/fault. It decorates an in-memory afero.Fs whose single gate
// file exists, so a Stat succeeds unless the fault Schedule trips — turning the
// library's deterministic FS-fault schedule into an append-seam fault the
// jobManager's error branches (rollback, teardown re-append) can be driven with.
func watchdel_faultGate(plan []byte) func() error {
	sched := fault.FromBytes(plan)
	if !sched.Active() {
		return func() error { return nil }
	}
	mem := afero.NewMemMapFs()
	_ = afero.WriteFile(mem, "gate", []byte("x"), 0o644)
	ffs := fault.FS(mem, sched)
	return func() error {
		// The gate file always exists, so a non-nil error is the injected fault,
		// never a natural miss.
		if _, err := ffs.Stat("gate"); err != nil {
			return err
		}
		return nil
	}
}

// --- watch invariant oracle ---

func watchdel_checkWatchInvariants(t *testing.T, jm *jobManager) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	check := func(cfg *watchConfig, where string) {
		if cfg == nil {
			t.Fatalf("watchdel: nil watch config in %s", where)
		}
		if cfg.watchID == "" {
			t.Fatalf("watchdel: live watch config with empty watch id in %s", where)
		}
		order := make(map[jobstore.WatchSendKey]bool, len(cfg.pendingOrder))
		for _, k := range cfg.pendingOrder {
			order[k] = true
		}
		for k, st := range cfg.pending {
			if st == nil {
				t.Fatalf("watchdel: nil pending watch-send state for key %+v in %s", k, where)
			}
			if !order[k] {
				t.Fatalf("watchdel: pending key %+v absent from pendingOrder in %s", k, where)
			}
		}
	}
	for _, cfg := range jm.watches {
		check(cfg, "watches")
	}
	for cfg := range jm.terminalFlush {
		check(cfg, "terminalFlush")
	}
}

// watchdel_sessionFlow drives the same watch paths through a real Session rather
// than a manager-only fixture. It keeps all effects at the approved boundaries:
// the scripted adapter, fake clock, DenyEnv, and test-owned persistence root.
// The program deliberately reaches the durable sequence that tends to regress:
// create -> replace watch -> repeated output/coalesce -> notification delivery
// -> clear -> terminal -> restore/re-arm -> notification delivery.
func watchdel_sessionFlow(t *testing.T, data []byte) {
	t.Helper()
	r := &watchdel_reader{data: data}
	stateDir := t.TempDir()
	workDir := t.TempDir()
	clk := agenttest.NewFakeClock()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider:  "openai",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	})
	newSession := func(meta *schema.SessionMeta) (*Session, func()) {
		env := &agenttest.DenyEnv{WorkDir: workDir, Seed: uint64(r.b())}
		var (
			sess *Session
			err  error
		)
		if meta != nil {
			sess, err = RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), env, *meta, RestoreSessionConfig{
				StateDir: stateDir,
				clock:    clk,
				testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			})
		} else {
			cfg := SessionConfig{
				StateDir:         stateDir,
				clock:            clk,
				MaxSubagentDepth: 1,
				NoProjectPrompts: true,
				LLMSleep:         func(context.Context, time.Duration) error { return nil },
			}
			cfg.testOnly = testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}
			sess, err = NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
		}
		if err != nil {
			t.Fatalf("watchdel session setup: %v", err)
		}
		drainDone := make(chan struct{})
		go func() {
			for range sess.Events() {
			}
			close(drainDone)
		}()
		closed := false
		return sess, func() {
			if closed {
				return
			}
			closed = true
			if sess.jobManager != nil {
				sess.jobManager.abandonRunningJobs()
			}
			sess.Close()
			<-drainDone
		}
	}

	sess, closeSession := newSession(nil)
	defer closeSession()
	jm := sess.jobManager
	if jm == nil {
		closeSession()
		t.Fatal("watchdel: Session has no job manager")
	}

	// The job is created through the manager's real persistence/open-output path.
	rec, err := jm.createShell(createShellOpts{Command: "watchdel session flow"})
	if err != nil {
		closeSession()
		t.Fatalf("watchdel create shell: %v", err)
	}
	monotonic := map[string]string{}
	watchdel_checkSessionState(t, jm, monotonic)

	// Same watch key, changed matcher: reaches the durable replace path. Caller
	// delivery keeps the full Session notification boundary in scope.
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
	}); err != nil {
		closeSession()
		t.Fatalf("watchdel install watch: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
	}); err != nil {
		closeSession()
		t.Fatalf("watchdel replace watch: %v", err)
	}
	watchdel_checkSessionState(t, jm, monotonic)

	// Only the pending-send append is faulted. Creation, output, terminalization,
	// and restore remain real; an injected failure must roll the in-memory pending
	// slot back to the same durable fold the oracle reads below.
	origAppend := jm.appendEvent
	gate := watchdel_faultGate(r.take(r.intn(5)))
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendPending {
			if err := gate(); err != nil {
				return err
			}
		}
		return origAppend(e)
	}

	appendOutput := func(prefix string) {
		jm.mu.Lock()
		run := jm.running[rec.JobID]
		jm.mu.Unlock()
		if run == nil {
			t.Fatalf("watchdel: running shell %s disappeared before output", rec.JobID)
		}
		chunk := append([]byte(prefix), r.take(r.intn(16))...)
		chunk = append(chunk, '\n')
		if _, err := jm.appendJobOutput(rec.JobID, run.output, chunk); err != nil {
			t.Fatalf("watchdel append output: %v", err)
		}
		watchdel_checkSessionState(t, jm, monotonic)
	}
	appendOutput("ready one ")
	appendOutput("READY two ") // same pending key; must coalesce before delivery.
	jm.appendEvent = origAppend
	pendingBeforeAccept, err := jm.store.LoadWatchSends()
	if err != nil {
		closeSession()
		t.Fatalf("watchdel pending before accept: %v", err)
	}
	wantWatchDeliveries := make(map[string]bool, len(pendingBeforeAccept.Pending))
	for _, state := range pendingBeforeAccept.Pending {
		if state != nil && state.DeliveryID != "" {
			wantWatchDeliveries[state.DeliveryID] = true
		}
	}

	// A real notification turn, not a queue inspection, accepts the caller token
	// and durably settles its final coalesced delivery ID. A second drain is the
	// exactly-once check: it must not create another accepted delivery.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = sess.ProcessInputKind(ctx, "", nil, EntryNotification)
	_, _ = sess.ProcessInputKind(ctx, "", nil, EntryNotification)
	watchdel_checkSessionState(t, jm, monotonic)
	watchdel_checkAcceptedWatchDeliveries(t, jm, wantWatchDeliveries)

	if _, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller},
		Clear:  true,
	}); err != nil {
		closeSession()
		t.Fatalf("watchdel clear watch: %v", err)
	}
	watchdel_checkSessionState(t, jm, monotonic)

	// Terminalize without accepting its terminal notification. Restore then owns
	// the actual re-arm path; the restored EntryNotification turn is what marks
	// the durable terminal delivery accepted exactly once.
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "watchdel_done", nil); err != nil {
		closeSession()
		t.Fatalf("watchdel finalize: %v", err)
	}
	watchdel_checkSessionState(t, jm, monotonic)
	if running := jm.runningJobIDs(); len(running) != 0 {
		closeSession()
		t.Fatalf("watchdel: %d running jobs after deterministic finalization", len(running))
	}
	watchdel_checkNoLiveWatchForTerminal(t, jm, rec.JobID)
	watchdel_checkTerminalNotifyState(t, jm, rec.JobID, jobstore.NotifyPending)

	meta := sess.Meta()
	closeSession()

	restored, closeRestored := newSession(&meta)
	defer closeRestored()
	if restored.clock != clk || restored.jobManager.clock != clk {
		t.Fatalf("watchdel: restore lost fake clock boundary")
	}
	_, _ = restored.ProcessInputKind(ctx, "", nil, EntryNotification)
	_, _ = restored.ProcessInputKind(ctx, "", nil, EntryNotification)
	watchdel_checkSessionState(t, restored.jobManager, monotonic)
	watchdel_checkTerminalNotifyState(t, restored.jobManager, rec.JobID, jobstore.NotifyDelivered)
	if running := restored.jobManager.runningJobIDs(); len(running) != 0 {
		t.Fatalf("watchdel: restored manager has %d orphaned running jobs", len(running))
	}
}

func watchdel_checkAcceptedWatchDeliveries(t *testing.T, jm *jobManager, want map[string]bool) {
	t.Helper()
	if len(want) == 0 {
		return // The injected append fault intentionally leaves no accepted delivery.
	}
	events, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("watchdel delivery events: %v", err)
	}
	seen := map[string]int{}
	for _, event := range events {
		if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend != nil {
			seen[event.WatchSend.DeliveryID]++
		}
	}
	for id := range want {
		if seen[id] != 1 {
			t.Fatalf("watchdel: accepted watch delivery %q count=%d, want exactly 1", id, seen[id])
		}
	}
}

func watchdel_checkTerminalNotifyState(t *testing.T, jm *jobManager, jobID string, want jobstore.NotifyState) {
	t.Helper()
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("watchdel terminal state load: %v", err)
	}
	rec := recs[jobID]
	if rec == nil {
		t.Fatalf("watchdel terminal job %q missing", jobID)
	}
	if rec.NotifyState != want {
		t.Fatalf("watchdel terminal job %q notify state=%q, want %q", jobID, rec.NotifyState, want)
	}
}

// watchdel_checkSessionState compares the durable folds with runtime state after
// every operation. The store is the crash-recovery truth; running records and
// pending maps are the live truth. They must agree even when the fault gate
// rejects a watch-send append.
func watchdel_checkSessionState(t *testing.T, jm *jobManager, terminal map[string]string) {
	t.Helper()
	if jm == nil || jm.store == nil {
		t.Fatal("watchdel: nil job manager/store")
	}
	durable, err := jm.store.Load()
	if err != nil {
		t.Fatalf("watchdel Load: %v", err)
	}
	livePending := map[jobstore.WatchSendKey]jobstore.WatchSendState{}
	jm.mu.Lock()
	for id, run := range jm.running {
		if run == nil || run.rec == nil {
			jm.mu.Unlock()
			t.Fatalf("watchdel: nil running job %q", id)
		}
		folded := durable[id]
		if folded == nil {
			jm.mu.Unlock()
			t.Fatalf("watchdel: running job %q missing from durable fold", id)
		}
		if folded.Type != run.rec.Type || folded.Status != run.rec.Status || folded.Reason != run.rec.Reason || folded.TerminalGen != run.rec.TerminalGen {
			jm.mu.Unlock()
			t.Fatalf("watchdel: durable/live job mismatch for %q: durable=%+v live=%+v", id, folded, run.rec)
		}
	}
	collect := func(cfg *watchConfig) {
		if cfg == nil {
			return
		}
		for key, state := range cfg.pending {
			if state != nil {
				livePending[key] = *state
			}
		}
	}
	for _, cfg := range jm.watches {
		collect(cfg)
	}
	for cfg := range jm.terminalFlush {
		collect(cfg)
	}
	jm.mu.Unlock()

	for id, rec := range durable {
		if rec == nil {
			continue
		}
		if prev, ok := terminal[id]; ok {
			if !rec.Status.IsTerminal() || rec.TerminalGen != prev {
				t.Fatalf("watchdel: terminal job regressed %s: generation %q -> status=%q generation=%q", id, prev, rec.Status, rec.TerminalGen)
			}
		} else if rec.Status.IsTerminal() {
			terminal[id] = rec.TerminalGen
		}
	}

	pending, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("watchdel LoadWatchSends: %v", err)
	}
	if len(livePending) != len(pending.Pending) {
		t.Fatalf("watchdel: live pending=%d durable pending=%d", len(livePending), len(pending.Pending))
	}
	for key, live := range livePending {
		folded := pending.Pending[key]
		if folded == nil || folded.DeliveryID != live.DeliveryID || folded.UpdateSeq != live.UpdateSeq || folded.CoalescedCount != live.CoalescedCount {
			t.Fatalf("watchdel: durable/live pending mismatch for %+v: durable=%+v live=%+v", key, folded, live)
		}
	}

	events, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("watchdel LoadEvents: %v", err)
	}
	terminalDelivered := map[string]bool{}
	watchDelivered := map[string]bool{}
	for _, event := range events {
		switch event.Kind {
		case jobstore.EventJobNotificationDelivered:
			key := event.JobID + "\x00" + event.TerminalGen
			if terminalDelivered[key] {
				t.Fatalf("watchdel: terminal notification accepted twice for %q", key)
			}
			terminalDelivered[key] = true
		case jobstore.EventWatchSendDelivered:
			if event.WatchSend == nil || event.WatchSend.DeliveryID == "" {
				t.Fatalf("watchdel: delivered watch send has no delivery id: %+v", event)
			}
			if watchDelivered[event.WatchSend.DeliveryID] {
				t.Fatalf("watchdel: watch delivery accepted twice for %q", event.WatchSend.DeliveryID)
			}
			watchDelivered[event.WatchSend.DeliveryID] = true
		}
	}
}

func watchdel_checkNoLiveWatchForTerminal(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		if cfg != nil && cfg.target == jobID {
			t.Fatalf("watchdel: terminal job %q retains live watch %q", jobID, cfg.watchID)
		}
	}
}

// --- watch args synthesis ---

var (
	watchdel_outputMatches = []string{"", "ready", "APPROVAL", ".*", "(", "a(b", "[0-9]+"}
	watchdel_eventNames    = []string{"assistant.tool", "communicate", "job.notification", "*", "assistant.message", "bogus"}
	watchdel_sendTargets   = []string{"caller", "dlg_missing", "watched", "main", "job_x", "*", ""}
	watchdel_sources       = []string{"", "self", "parent", "job_missing"}
	watchdel_progress      = []int{0, 0, 0, -1, 500, 1000, 5000, 3600001}
	watchdel_toolNames     = []string{"", "read_file", "shell"}
	watchdel_statuses      = []string{"", "ok", "error", "weird"}
)

func watchdel_drawWatchArgs(r *watchdel_reader, watchedJobID, observerJobID string) watchArgs {
	targets := []string{watchedJobID, "caller", "*", "job_missing", observerJobID}
	a := watchArgs{
		Target:             targets[r.intn(len(targets))],
		Source:             r.pick(watchdel_sources),
		OutputMatch:        r.pick(watchdel_outputMatches),
		ProgressIntervalMS: watchdel_progress[r.intn(len(watchdel_progress))],
		Every:              r.intn(3),
		Clear:              r.boolean(),
	}
	nEvents := r.intn(3)
	for i := 0; i < nEvents; i++ {
		a.Events = append(a.Events, r.pick(watchdel_eventNames))
	}
	if r.boolean() {
		a.EventFilter = &watchEventFilter{
			ToolName: r.pick(watchdel_toolNames),
			Status:   r.pick(watchdel_statuses),
		}
	}
	if r.boolean() {
		a.Send = &watchSendArgs{
			To:             r.pick(watchdel_sendTargets),
			Message:        "observe",
			IncludeExcerpt: r.boolean(),
		}
	}
	return a
}

// --- recordWatchSendPending driver ---

func watchdel_driveRecordPending(t *testing.T, r *watchdel_reader, jm *jobManager, watchedJobID string) {
	jm.mu.Lock()
	var cfg *watchConfig
	for _, c := range jm.watches {
		if c != nil && c.send != nil {
			cfg = c
			break
		}
	}
	jm.mu.Unlock()
	if cfg == nil {
		return
	}
	root := events.SessionEvent{Kind: events.EventCommunicate, SessionID: jm.sessionID}
	d := jm.watchSendSnapshot(cfg, watchedJobID, "fuzz-trigger", root)
	if r.boolean() {
		jm.mu.Lock()
		if jm.terminalFlush == nil {
			jm.terminalFlush = make(map[*watchConfig]bool)
		}
		jm.terminalFlush[cfg] = true
		jm.mu.Unlock()
		d.allowAfterTerminalExpiry = true
	}
	n := r.intn(40) + 1
	for i := 0; i < n; i++ {
		st := jm.watchSendState(d, cfg.send.To)
		st.Key.ResolvedWatchedIdentity = fmt.Sprintf("job_%d", r.intn(48))
		st.UpdateSeq = uint64(r.intn(2000))
		if r.boolean() {
			jm.mu.Lock()
			if cfg.settledUpdateSeq == nil {
				cfg.settledUpdateSeq = make(map[jobstore.WatchSendKey]uint64)
			}
			cfg.settledUpdateSeq[st.Key] = uint64(r.intn(2000))
			jm.mu.Unlock()
		}
		_ = jm.recordWatchSendPending(st, d)
	}
}

// --- clearWatchByIDMatching driver ---

func watchdel_driveClearByID(t *testing.T, r *watchdel_reader, jm *jobManager) {
	jm.mu.Lock()
	ids := []string{"wch_missing", ""}
	for _, c := range jm.watches {
		if c != nil {
			ids = append(ids, c.watchID)
		}
	}
	jm.mu.Unlock()
	wid := ids[r.intn(len(ids))]
	var allow func(*watchConfig) bool
	switch r.intn(3) {
	case 0:
		allow = func(*watchConfig) bool { return true }
	case 1:
		allow = func(c *watchConfig) bool { return watchConfigVisibleToSession(c, jm.sessionID) }
	default:
		allow = func(*watchConfig) bool { return false }
	}
	_, _ = jm.clearWatchByIDMatching(wid, allow, r.boolean())
}

// FuzzWatchdelWatchOps drives configureWatch, recordWatchSendPending, and
// clearWatchByIDMatching against one real jobManager, injecting persist faults
// through the append seam, and asserts the watch-state invariant after each step.
func FuzzWatchdelWatchOps(f *testing.F) {
	f.Add([]byte{})
	// The session-backed flow reads this as a one-byte fault plan containing an
	// injected failure, then proves the rollback agrees with the durable fold.
	f.Add([]byte{7, 1, 0, 9, 11})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{0, 1, 0, 3, 0, 2, 1, 1, 4, 4, 0, 0})
	f.Add([]byte{2, 2, 2, 2, 9, 9, 9, 9, 1, 1, 1, 1, 5, 5, 5, 5})
	f.Add([]byte{4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 44, 48, 52, 56, 60, 64})

	f.Fuzz(func(t *testing.T, data []byte) {
		watchdel_sessionFlow(t, data)

		r := &watchdel_reader{data: data}
		jm := newTestJM(t)

		watched, err := jm.createShell(createShellOpts{Command: "watched"})
		if err != nil {
			return
		}
		observer, err := jm.createShell(createShellOpts{Command: "observer"})
		if err != nil {
			return
		}
		// Install one guaranteed-valid caller send-watch before the fault gate so
		// recordWatchSendPending is always reachable regardless of the fuzzed op
		// stream (a concrete output_match watch delivering to the caller passes
		// validation and carries a send). The fuzzed configureWatch ops below may
		// replace or clear it, exercising those transitions too.
		if _, err := jm.configureWatch(watchArgs{
			Target:      watched.JobID,
			OutputMatch: "seed-match",
			Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
		}); err != nil {
			return
		}

		origAppend := jm.appendEvent
		origAppends := jm.appendEvents
		gate := watchdel_faultGate(r.take(r.intn(8)))
		jm.appendEvent = func(e jobstore.Event) error {
			if ferr := gate(); ferr != nil {
				return ferr
			}
			return origAppend(e)
		}
		jm.appendEvents = func(es []jobstore.Event) error {
			if ferr := gate(); ferr != nil {
				return ferr
			}
			return origAppends(es)
		}
		t.Cleanup(func() {
			jm.appendEvent = origAppend
			jm.appendEvents = origAppends
			jm.mu.Lock()
			for _, cfg := range jm.watches {
				closeWatchConfig(cfg)
			}
			jm.mu.Unlock()
			jm.closeGrace = 0
			_ = jm.close()
		})

		nOps := r.intn(6) + 1
		for i := 0; i < nOps; i++ {
			a := watchdel_drawWatchArgs(r, watched.JobID, observer.JobID)
			_, _ = jm.configureWatch(a)
			watchdel_checkWatchInvariants(t, jm)
		}

		watchdel_driveRecordPending(t, r, jm, watched.JobID)
		watchdel_checkWatchInvariants(t, jm)

		clears := r.intn(3) + 1
		for i := 0; i < clears; i++ {
			watchdel_driveClearByID(t, r, jm)
			watchdel_checkWatchInvariants(t, jm)
		}
	})
}
