package agent

import (
	"fmt"
	"os"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/fuzz/fault"
)

// This file fuzzes the watch-configuration and delegate-resume machinery in
// job_watch.go and job_delegate.go. The targets are heavily branch-laden install/
// validation/teardown state machines whose error tails (persist failure, replace,
// idempotent re-configure, eviction, corrupt-restore) unit tests reach only
// piecemeal. The two fuzz bodies drive them from a real *jobManager / *Session
// built with the package's own newTestJM / newSession helpers, so every store,
// clock, and filesystem effect stays inside a t.TempDir / MemMapFs sandbox — no
// network, no process, no real disk outside the sandbox.
//
// ORACLES (both are real preserved-invariant / determinism oracles, not bare
// never-panic):
//   - watch side: after every configureWatch / recordWatchSendPending /
//     clearWatchByIDMatching, the manager's watch state stays internally
//     consistent (every pending key is tracked in pendingOrder, no nil pending
//     state, every live config keeps a non-empty watch id).
//   - delegate side: assessDelegateResumability is a pure function of its rec +
//     on-disk fixture (two calls agree on Resumable and Reason), a resumable
//     preflight always carries a non-nil Preflight, and an unresumable result
//     always names a reason. restoreTerminalDelegateChildClaimed returns a live
//     subagent XOR an error.
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

// --- watch args synthesis ---

var (
	watchdel_outputMatches = []string{"", "ready", "APPROVAL", ".*", "(", "a(b", "[0-9]+"}
	watchdel_eventNames    = []string{"assistant.tool", "communicate", "job.notification", "*", "assistant.message", "bogus"}
	watchdel_sendTargets   = []string{"caller", "dlg_obs", "dlg_missing", "watched", "main", "job_x", "*", ""}
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

// watchdel_seedObserverDelegate installs a running, resumable delegate named
// dlg_obs owned by the manager's session so a fuzzed watch send target of
// "dlg_obs" passes validateWatchSendTarget (exercising the delegate-send install
// path and read-grant minting) rather than always erroring out early.
func watchdel_seedObserverDelegate(t *testing.T, jm *jobManager) {
	t.Helper()
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_obs",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "child_obs",
			TranscriptRef:    encodeRef("", "child_obs"),
			OwnerSessionID:   jm.sessionID,
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_obs",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("seed observer delegate: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            "job_obs_delegate",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_obs",
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		TranscriptRef:    encodeRef("", "child_obs"),
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("seed observer delegate job: %v", err)
	}
}

// FuzzWatchdelWatchOps drives configureWatch, recordWatchSendPending, and
// clearWatchByIDMatching against one real jobManager, injecting persist faults
// through the append seam, and asserts the watch-state invariant after each step.
func FuzzWatchdelWatchOps(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{0, 1, 0, 3, 0, 2, 1, 1, 4, 4, 0, 0})
	f.Add([]byte{2, 2, 2, 2, 9, 9, 9, 9, 1, 1, 1, 1, 5, 5, 5, 5})
	f.Add([]byte{4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 44, 48, 52, 56, 60, 64})

	f.Fuzz(func(t *testing.T, data []byte) {
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
		watchdel_seedObserverDelegate(t, jm)

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

// --- delegate resume fuzzer ---

func watchdel_perturbDelegate(t *testing.T, r *watchdel_reader, s *Session, rec *jobstore.JobRecord) {
	switch r.intn(15) {
	case 0:
		// baseline: leave the resumable fixture intact.
	case 1:
		rec.DelegateRestore = nil
	case 2:
		rec.Type = jobstore.JobShell
	case 3:
		rec.DelegateRestore.ChildSessionID = ""
	case 4:
		rec.DelegateRestore.TranscriptRef = "bogus-ref"
	case 5:
		rec.DelegateRestore.ParentSessionID = "OTHER-PARENT"
	case 6:
		rec.DelegateRestore.ParentJobID = "job_other"
	case 7:
		rec.DelegateRestore.OwnerSessionID = "OTHER-OWNER"
	case 8:
		rec.DelegateRestore.VisibleSessionID = "OTHER-VIS"
	case 9:
		rec.DelegateRestore.LocalEnvPolicy = "bogus-policy"
	case 10:
		rec.DelegateRestore.WorkingDir = ""
	case 11:
		rec.DelegateRestore.ResolvedModel = ""
	case 12:
		rec.DelegateRestore.FrozenSkillNames = []string{"skill"}
		rec.DelegateRestore.FrozenSkillBodies = nil
	case 13:
		body := r.take(r.intn(24))
		if len(body) == 0 {
			body = []byte("{ not json")
		}
		_ = os.WriteFile(childSessionMetaPath(s, rec), body, 0o644)
	case 14:
		_ = os.Remove(childTranscriptPath(s, rec))
	}
}

// FuzzWatchdelDelegateResume seeds a real resumable stopped-delegate restore
// fixture (child session meta + transcript on disk), perturbs it from fuzz bytes,
// and drives assessDelegateResumability + restoreTerminalDelegateChildClaimed.
func FuzzWatchdelDelegateResume(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 0})
	f.Add([]byte{4, 1})
	f.Add([]byte{11, 0})
	f.Add([]byte{13, 1, 2, 3, 4})
	f.Add([]byte{14, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &watchdel_reader{data: data}
		s := newSession(t, withConfig(SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
		}))
		rec := seedStoppedDelegateRestoreRecord(t, s)
		markStoredDelegateResumable(t, s, rec)

		watchdel_perturbDelegate(t, r, s, rec)

		mode := delegateResumabilityPreflight
		if r.boolean() {
			mode = delegateResumabilityProjection
		}

		first := s.assessDelegateResumability(rec, mode)
		second := s.assessDelegateResumability(rec, mode)
		if first.Resumable != second.Resumable || first.Reason != second.Reason {
			t.Fatalf("watchdel: assessDelegateResumability nondeterministic\n first  = %+v\n second = %+v", first, second)
		}
		if !first.Resumable && first.Reason == "" {
			t.Fatalf("watchdel: unresumable assessment carries no reason: %+v", first)
		}
		if mode == delegateResumabilityPreflight && first.Resumable && first.Preflight == nil {
			t.Fatalf("watchdel: resumable preflight has nil Preflight")
		}

		if mode == delegateResumabilityPreflight && first.Resumable {
			childID := rec.DelegateRestore.ChildSessionID
			sub, err := s.restoreTerminalDelegateChild(rec, childID, first.Preflight)
			if err == nil && (sub == nil || sub.sess == nil) {
				t.Fatalf("watchdel: restoreTerminalDelegateChildClaimed returned no error but no live subagent: sub=%v", sub)
			}
		}
	})
}
