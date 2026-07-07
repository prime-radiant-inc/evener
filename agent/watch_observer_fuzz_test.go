//go:build serffuzz

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/fuzz/oracle"
)

// This file fuzzes the watch/observer machinery in job_watch.go and
// observer_grants.go that the existing job_watch_delegate lane leaves partly
// dark: the restore-time watch-send target classifier
// (classifyRestoredWatchSendTarget), the model-facing frame renderer
// (buildWatchFrame), the durable pending-watch-send reconstructor
// (restoreWatchSendPending), the session-target/receiver remainder of
// configureWatch, and the historical observer-grant inverter
// (LoadSessionObserverGrants). Each target is driven from a real *Session /
// *jobManager built with the package's own newSession / newTestJM helpers, so
// every store, clock, and filesystem effect stays inside a t.TempDir sandbox —
// no network, no process, no real disk outside the sandbox.
//
// ORACLES (all real preserved-invariant / determinism oracles, not bare
// never-panic):
//   - classify: deterministic in (class, reason); and the class/reason are
//     internally consistent — a hard failure ALWAYS carries a reason, and a
//     delivered/busy classification NEVER does.
//   - frame: deterministic; and the rendered frame is bounded to
//     watchFrameMaxChars runes regardless of input.
//   - restore: two managers fed identical durable events reconstruct the exact
//     same pending set, and the reconstructed watch state is internally
//     consistent (every pending key tracked in pendingOrder, no nil state,
//     nextUpdateSeq dominates every pending update seq).
//   - configureWatch: the manager's watch state stays internally consistent
//     after every session-target / receiver install and clear.
//   - grants: LoadSessionObserverGrants is deterministic and every observer
//     list it returns is sorted and duplicate-free.
//
// All new top-level identifiers carry the wobs_ lane prefix so parallel lanes
// editing package agent never collide.

// --- shared byte-stream reader ---

type wobs_reader struct {
	data []byte
	pos  int
}

func (r *wobs_reader) b() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	v := r.data[r.pos]
	r.pos++
	return v
}

func (r *wobs_reader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.b()) % n
}

func (r *wobs_reader) boolean() bool { return r.b()&1 == 1 }

func (r *wobs_reader) pick(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	return opts[r.intn(len(opts))]
}

// str draws a short bounded string from the byte plan, mixing in CRLF and
// multi-byte runes so the frame renderer's line-ending normalization and rune
// truncation get exercised without unbounded plateau growth.
func (r *wobs_reader) str() string {
	n := r.intn(6)
	alphabet := []string{"a", "b", "\n", "\r\n", "\r", "  ", "é", "🙂", ":", "long-token-"}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(alphabet[r.intn(len(alphabet))])
	}
	return b.String()
}

// ==========================================================================
// Target 1: classifyRestoredWatchSendTarget
// ==========================================================================

// wobs_classifyResult bundles the classifier's two outputs so the determinism
// oracle can compare them in one shot.
type wobs_classifyResult struct {
	class  watchSendDeliveryClass
	reason string
}

// wobs_seedClassifyDelegate installs one delegate (and, unless jobID is empty, a
// matching started job) owned as specified, so a classify target of the given
// delegate id reaches a chosen ownership/type/resumability branch instead of a
// plain miss. A nil-owner ("") records the manager's own session.
func wobs_seedClassifyDelegate(t *testing.T, jm *jobManager, delegateID, delegateOwner, jobID string, jobType jobstore.JobType, jobOwner string, resumable bool) {
	t.Helper()
	now := jm.now()
	if delegateOwner == "" {
		delegateOwner = jm.sessionID
	}
	if jobOwner == "" {
		jobOwner = jm.sessionID
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: delegateID,
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "child_" + delegateID,
			TranscriptRef:    encodeRef("", "child_"+delegateID),
			OwnerSessionID:   delegateOwner,
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_" + delegateID,
			Resumable:        resumable,
		},
	}); err != nil {
		t.Fatalf("seed delegate %s: %v", delegateID, err)
	}
	if jobID == "" {
		return
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobType,
		DelegateID:       delegateID,
		OwnerSessionID:   jobOwner,
		VisibleToSession: jm.sessionID,
		TranscriptRef:    encodeRef("", "child_"+delegateID),
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("seed delegate job %s: %v", jobID, err)
	}
}

// wobs_finishDelegateJob marks a delegate job terminal (stopped, not
// runtime_lost) and resumable, so the classifier's terminal-delegate path
// reaches the resumability assessment rather than the running short-circuit.
func wobs_finishDelegateJob(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	now := jm.now()
	resumable := true
	if err := jm.appendEvent(jobstore.Event{
		Kind:    jobstore.EventJobFinished,
		TS:      now,
		JobID:   jobID,
		Status:  jobstore.StatusStopped,
		Reason:  "user_stopped",
		EndedAt: &now,
	}); err != nil {
		t.Fatalf("finish delegate job %s: %v", jobID, err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:      jobstore.EventJobSessionAssigned,
		TS:        now,
		JobID:     jobID,
		Resumable: &resumable,
	}); err != nil {
		t.Fatalf("mark delegate job %s resumable: %v", jobID, err)
	}
}

// FuzzWobsClassifyRestoredTarget drives classifyRestoredWatchSendTarget against
// a Session seeded with delegates and jobs in every ownership/status shape, over
// a fuzzed target string. The classifier is what restore uses to decide whether
// a pending watch-send frame is deliverable, busy (retryable), or a hard
// failure, so its class/reason contract must hold for any target string.
func FuzzWobsClassifyRestoredTarget(f *testing.F) {
	// Byte 0 selects the target; one seed per target index locks in every
	// classifier branch the seeded fixtures make reachable.
	for i := byte(0); i < 21; i++ {
		f.Add([]byte{i, 0})
	}
	f.Add([]byte{})
	f.Add([]byte{7, 8, 9, 10})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &wobs_reader{data: data}
		idx := r.intn(21)
		s := newSession(t, withConfig(SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
		}))
		jm := s.jobManager

		// Running resumable delegate owned by this session (dlg_obs / job_obs_delegate).
		watchdel_seedObserverDelegate(t, jm)
		// Delegate owned by a DIFFERENT session (not_controllable at the delegate).
		wobs_seedClassifyDelegate(t, jm, "dlg_foreign", "OTHER-SESSION", "job_foreign_delegate", jobstore.JobDelegate, "OTHER-SESSION", true)
		// Delegate whose job record is owned by a descendant session.
		wobs_seedClassifyDelegate(t, jm, "dlg_descjob", "", "job_descjob", jobstore.JobDelegate, "DESCENDANT", true)
		// Delegate whose job is a non-delegate (shell) type.
		wobs_seedClassifyDelegate(t, jm, "dlg_shelljob", "", "job_shelljob", jobstore.JobShell, "", true)
		// Delegate marked not-resumable.
		wobs_seedClassifyDelegate(t, jm, "dlg_norsm", "", "job_norsm", jobstore.JobDelegate, "", false)
		// Delegate with no job history at all.
		wobs_seedClassifyDelegate(t, jm, "dlg_nojob", "", "", jobstore.JobDelegate, "", true)
		// Delegate whose job finished terminal + resumable (not runtime_lost) — the
		// dlg_ branch reaches the resumability assessment tail through it.
		wobs_seedClassifyDelegate(t, jm, "dlg_term", "", "job_term", jobstore.JobDelegate, "", true)
		wobs_finishDelegateJob(t, jm, "job_term")
		// Plain running shell job (non-delegate).
		shell, err := jm.createShell(createShellOpts{Command: "worker"})
		if err != nil {
			return
		}
		// Terminal, resumable stopped delegate (runtime_lost) — reaches the
		// resumability-assessment tail on the job_ branch.
		rec := seedStoppedDelegateRestoreRecord(t, s)
		markStoredDelegateResumable(t, s, rec)

		targets := []string{
			"caller", "watched", "main", "",
			"dlg_obs", "dlg_foreign", "dlg_missing", "dlg_descjob",
			"dlg_shelljob", "dlg_norsm", "dlg_nojob", "dlg_term",
			"job_obs_delegate", "job_foreign_delegate", shell.JobID, rec.JobID,
			"job_missing", s.ID(),
			"dlg_x", "job_x", "weird",
		}
		target := targets[idx%len(targets)]

		classify := func(tgt string) wobs_classifyResult {
			class, reason := s.classifyRestoredWatchSendTarget(tgt)
			return wobs_classifyResult{class: class, reason: reason}
		}
		oracle.Deterministic(t, classify, target, oracle.DeepEqual[wobs_classifyResult])

		got := classify(target)
		if (got.class == watchSendHardFailure) != (got.reason != "") {
			t.Fatalf("wobs: classify class/reason inconsistent for %q: class=%d reason=%q", target, got.class, got.reason)
		}
	})
}

// ==========================================================================
// Target 2: buildWatchFrame
// ==========================================================================

// wobs_drawEvent synthesizes a SessionEvent whose Data is one of the payload
// shapes writeWatchFrameEvent switches on (value and pointer variants, plus a
// nil-data case), with fuzzed string fields so truncation and line-ending
// normalization inside the frame renderer are exercised.
func wobs_drawEvent(r *wobs_reader) events.SessionEvent {
	ev := events.SessionEvent{SessionID: "S1"}
	switch r.intn(9) {
	case 0:
		// nil data: writeWatchFrameEvent falls through with no event block.
	case 1:
		ev.Kind = events.EventCommunicate
		ev.Data = events.CommunicateData{EndTurn: r.boolean(), Message: r.str()}
	case 2:
		ev.Kind = events.EventCommunicate
		ev.Data = &events.CommunicateData{EndTurn: r.boolean(), Message: r.str()}
	case 3:
		ev.Data = events.AssistantTextEndData{Text: r.str(), Model: r.str(), FinishReason: r.str()}
	case 4:
		ev.Data = (*events.AssistantTextEndData)(nil)
	case 5:
		ev.Kind = events.EventToolCallEnd
		ev.Data = events.ToolCallEndData{ToolName: r.str(), CallID: r.str(), ArgumentsJSON: r.str(), Output: r.str(), Error: r.str()}
	case 6:
		ev.Kind = events.EventToolCallEnd
		ev.Data = &events.ToolCallEndData{ToolName: r.str(), Output: r.str()}
	case 7:
		code := r.intn(256)
		ev.Data = events.JobFinishedData{JobID: r.str(), Status: r.str(), Reason: r.str(), ExitCode: &code, OutputBytes: int64(r.intn(1 << 20))}
	case 8:
		ev.Data = &events.JobFinishedData{JobID: r.str()}
	}
	return ev
}

func wobs_drawProvenance(r *wobs_reader) *provenance.Causal {
	switch r.intn(3) {
	case 0:
		return nil
	case 1:
		return &provenance.Causal{}
	default:
		return provenance.WithWatch(nil, r.str(), r.str(), r.str(), "S1", "job_watched")
	}
}

// FuzzWobsBuildWatchFrame renders a model-facing watch frame from a fuzzed watch
// config, target job, triggering event, and provenance. The frame is what the
// model actually reads, so it must render for any input, stay bounded, and be
// reproducible for a fixed job (its excerpt reads retained output, which is
// stable between two immediate reads).
func FuzzWobsBuildWatchFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0})
	f.Add([]byte{2, 1, 1})
	f.Add([]byte{5, 1, 2, 3, 1})
	f.Add([]byte{7, 0, 1, 2, 3, 4, 5})
	f.Add([]byte{3, 3, 3, 9, 9, 9, 2, 2, 2})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &wobs_reader{data: data}
		jm := newTestJM(t)
		t.Cleanup(func() { jm.closeGrace = 0; _ = jm.close() })

		shell, err := jm.createShell(createShellOpts{Command: "worker"})
		if err != nil {
			return
		}
		if b := r.intn(9000); b > 0 {
			feedJob(jm, shell.JobID, []byte(fmt.Sprintf("output-%s-%d", r.str(), b)))
		}

		cfg := &watchConfig{
			watchID: r.str(),
			send: &watchSendArgs{
				To:             r.pick([]string{"caller", "dlg_obs", ""}),
				Message:        r.str(),
				IncludeExcerpt: r.boolean(),
			},
		}
		jobIDs := []string{shell.JobID, "caller", "*", "", "job_missing"}
		jobID := jobIDs[r.intn(len(jobIDs))]
		trigger := r.str()
		deliveryID := r.str()
		ev := wobs_drawEvent(r)
		p := wobs_drawProvenance(r)

		build := func(struct{}) string {
			return jm.buildWatchFrame(cfg, jobID, trigger, deliveryID, ev, p)
		}
		oracle.Deterministic(t, build, struct{}{}, func(a, b string) bool { return a == b })

		frame := build(struct{}{})
		if n := len([]rune(frame)); n > watchFrameMaxChars {
			t.Fatalf("wobs: frame exceeds cap: %d > %d runes", n, watchFrameMaxChars)
		}
	})
}

// ==========================================================================
// Target 3: restoreWatchSendPending
// ==========================================================================

// wobs_drawWatchSendKey draws a durable watch-send key. WatchTarget is drawn
// from a set that includes the caller alias so restore's "parent" sourcePublic
// branch (receiver + caller target) is reachable.
func wobs_drawWatchSendKey(r *wobs_reader) jobstore.WatchSendKey {
	return jobstore.WatchSendKey{
		VisibleSessionID:        r.pick([]string{"S1", "S2", ""}),
		WatchID:                 r.pick([]string{"wch_a", "wch_b", ""}),
		WatchTarget:             r.pick([]string{"caller", "job_1", "job_2", ""}),
		ResolvedWatchedIdentity: r.pick([]string{"job_1", "job_2", "job_3"}),
		ResolvedSendTo:          r.pick([]string{"caller", "dlg_x", ""}),
		WatchGeneration:         r.pick([]string{"g1", "g2", ""}),
	}
}

// wobs_seedWatchSendEvents appends a fuzzed sequence of pending / settling
// watch-send events to a store so restoreWatchSendPending has durable state to
// fold and reconstruct.
func wobs_seedWatchSendEvents(r *wobs_reader, store *jobstore.Store, base time.Time) {
	n := r.intn(10) + 1
	for i := 0; i < n; i++ {
		key := wobs_drawWatchSendKey(r)
		seq := uint64(r.intn(64))
		st := &jobstore.WatchSendState{
			Key:               key,
			DeliveryID:        fmt.Sprintf("dlv_%d", i),
			UpdateSeq:         seq,
			Message:           r.str(),
			ReceiverSessionID: r.pick([]string{"", "S9"}),
			CreatedAt:         base.Add(time.Duration(r.intn(8)) * time.Second),
			UpdatedAt:         base.Add(time.Duration(r.intn(8)) * time.Second),
		}
		kind := jobstore.EventWatchSendPending
		switch r.intn(4) {
		case 1:
			kind = jobstore.EventWatchSendDelivered
		case 2:
			kind = jobstore.EventWatchSendDropped
		case 3:
			kind = jobstore.EventWatchSendEvicted
		}
		_ = store.Append(jobstore.Event{Kind: kind, TS: base, WatchSend: st})
	}
}

// wobs_pendingKeySet collects every pending key across a manager's terminal-flush
// configs, plus the internal-consistency assertions, and returns the sorted key
// list for a determinism comparison.
func wobs_pendingKeySet(t *testing.T, jm *jobManager) []jobstore.WatchSendKey {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	var keys []jobstore.WatchSendKey
	for cfg := range jm.terminalFlush {
		if len(cfg.pending) == 0 {
			t.Fatalf("wobs: terminalFlush config with empty pending")
		}
		order := make(map[jobstore.WatchSendKey]bool, len(cfg.pendingOrder))
		for _, k := range cfg.pendingOrder {
			order[k] = true
		}
		for k, st := range cfg.pending {
			if st == nil {
				t.Fatalf("wobs: nil pending state for key %+v", k)
			}
			if !order[k] {
				t.Fatalf("wobs: pending key %+v absent from pendingOrder", k)
			}
			if st.UpdateSeq > cfg.nextUpdateSeq {
				t.Fatalf("wobs: pending update seq %d exceeds nextUpdateSeq %d", st.UpdateSeq, cfg.nextUpdateSeq)
			}
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return watchSendKeyLess(keys[i], keys[j]) })
	return keys
}

// FuzzWobsRestoreWatchSendPending feeds two fresh managers the SAME fuzzed
// durable watch-send event stream, restores both, and asserts they reconstruct
// the identical pending set (a real determinism oracle over a map-driven
// reconstruction) while each stays internally consistent.
func FuzzWobsRestoreWatchSendPending(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{1, 1, 1, 1, 2, 2, 2, 2})
	f.Add([]byte{4, 4, 8, 8, 0, 0, 16, 16})
	f.Add([]byte{2, 5, 7, 11, 13, 17, 19, 23})

	f.Fuzz(func(t *testing.T, data []byte) {
		jmA := newTestJM(t)
		jmB := newTestJM(t)
		t.Cleanup(func() {
			jmA.closeGrace = 0
			jmB.closeGrace = 0
			_ = jmA.close()
			_ = jmB.close()
		})
		base := frozenTestTime

		// Draw the event plan once, replay it into both stores identically.
		rA := &wobs_reader{data: data}
		wobs_seedWatchSendEvents(rA, jmA.store, base)
		rB := &wobs_reader{data: data}
		wobs_seedWatchSendEvents(rB, jmB.store, base)

		if err := jmA.restoreWatchSendPending(); err != nil {
			t.Fatalf("wobs: restore A: %v", err)
		}
		if err := jmB.restoreWatchSendPending(); err != nil {
			t.Fatalf("wobs: restore B: %v", err)
		}

		keysA := wobs_pendingKeySet(t, jmA)
		keysB := wobs_pendingKeySet(t, jmB)
		if !oracle.DeepEqual(keysA, keysB) {
			t.Fatalf("wobs: restore nondeterministic\n A = %+v\n B = %+v", keysA, keysB)
		}
	})
}

// ==========================================================================
// Target 4: configureWatch (session-target / receiver remainder)
// ==========================================================================

// FuzzWobsConfigureWatchSession drives configureWatch through its session-target
// and receiver-observer paths — caller/"*" targets, output_match/include_excerpt
// session-target rejections, receiver_session_id / receiver_delegate_id parent
// observers, and clears — asserting the watch state stays internally consistent
// after each op regardless of the fuzzed shape.
func FuzzWobsConfigureWatchSession(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2})
	f.Add([]byte{1, 3, 5, 7})
	f.Add([]byte{2, 2, 2, 2, 1, 1})
	f.Add([]byte{4, 0, 6, 0, 3, 0, 5})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &wobs_reader{data: data}
		jm := newTestJM(t)
		watchdel_seedObserverDelegate(t, jm)

		t.Cleanup(func() {
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
			a := watchArgs{
				Target:             r.pick([]string{"caller", "*"}),
				Source:             r.pick([]string{"", "self", "parent"}),
				OutputMatch:        r.pick([]string{"", "ready", "("}),
				ProgressIntervalMS: []int{0, 0, 1000, -1}[r.intn(4)],
				Every:              r.intn(3),
				Clear:              r.boolean(),
			}
			nEvents := r.intn(3)
			for j := 0; j < nEvents; j++ {
				a.Events = append(a.Events, r.pick([]string{"assistant.tool", "communicate", "job.notification", "*"}))
			}
			if r.boolean() {
				a.EventFilter = &watchEventFilter{
					ToolName: r.pick([]string{"", "read_file"}),
					Status:   r.pick([]string{"", "ok", "error"}),
				}
			}
			switch r.intn(4) {
			case 1:
				a.Send = &watchSendArgs{To: r.pick([]string{"caller", "dlg_obs"}), Message: "m", IncludeExcerpt: r.boolean()}
			case 2:
				a.ReceiverSessionID = "S-observer"
			case 3:
				a.ReceiverDelegateID = "dlg_obs"
			}
			_, _ = jm.configureWatch(a)
			wobs_checkConfigureInvariants(t, jm)
		}
	})
}

func wobs_checkConfigureInvariants(t *testing.T, jm *jobManager) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if cfg == nil {
			t.Fatalf("wobs: nil live watch config for key %+v", key)
		}
		if cfg.watchID == "" {
			t.Fatalf("wobs: live watch config with empty watch id for key %+v", key)
		}
		order := make(map[jobstore.WatchSendKey]bool, len(cfg.pendingOrder))
		for _, k := range cfg.pendingOrder {
			order[k] = true
		}
		for k, st := range cfg.pending {
			if st == nil {
				t.Fatalf("wobs: nil pending state for key %+v", k)
			}
			if !order[k] {
				t.Fatalf("wobs: pending key %+v absent from pendingOrder", k)
			}
		}
	}
}

// ==========================================================================
// Target 5: LoadSessionObserverGrants
// ==========================================================================

// wobs_writeGrantLog writes a fuzzed jobs.jsonl containing watch-read-grant
// events and delegate/non-delegate job records (with transcript refs in every
// resolvable and unresolvable shape), then optionally appends a corrupt trailing
// line so LoadSessionObserverGrants' open/load error tail is reachable.
func wobs_writeGrantLog(t *testing.T, r *wobs_reader, path string) {
	t.Helper()
	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatalf("wobs: open grant log: %v", err)
	}
	now := frozenTestTime
	observers := []string{"obs_1", "obs_2", ""}
	watchedJobs := []string{"job_del_local", "job_del_proj", "job_del_bad", "job_shell", "job_absent", ""}

	// Seed a set of watched job records in assorted shapes.
	jobShapes := []struct {
		jobID string
		typ   jobstore.JobType
		ref   string
	}{
		{"job_del_local", jobstore.JobDelegate, encodeRef("", "worker_a")},
		{"job_del_proj", jobstore.JobDelegate, encodeRef("bucket9", "worker_b")},
		{"job_del_bad", jobstore.JobDelegate, "garbage-ref"},
		{"job_shell", jobstore.JobShell, encodeRef("", "worker_c")},
	}
	for _, js := range jobShapes {
		started := now
		_ = store.Append(jobstore.Event{
			Kind:          jobstore.EventJobStarted,
			TS:            now,
			JobID:         js.jobID,
			Type:          js.typ,
			TranscriptRef: js.ref,
			StartedAt:     &started,
		})
	}

	nGrants := r.intn(8) + 1
	for i := 0; i < nGrants; i++ {
		_ = store.Append(jobstore.Event{
			Kind:              jobstore.EventWatchReadGrant,
			TS:                now,
			JobID:             watchedJobs[r.intn(len(watchedJobs))],
			ObserverSessionID: observers[r.intn(len(observers))],
		})
	}
	_ = store.Close()

	if r.boolean() {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatalf("wobs: reopen for corruption: %v", err)
		}
		_, _ = f.WriteString("{ this is not valid json\n")
		_ = f.Close()
	}
}

// wobs_grantsConsistent asserts every observer list is sorted and duplicate-free.
func wobs_grantsConsistent(t *testing.T, out map[string][]string) {
	t.Helper()
	for worker, obs := range out {
		if worker == "" {
			t.Fatalf("wobs: empty worker key in grants")
		}
		for i := 1; i < len(obs); i++ {
			if obs[i-1] >= obs[i] {
				t.Fatalf("wobs: observer list not strictly sorted/deduped for %q: %v", worker, obs)
			}
		}
	}
}

// FuzzWobsLoadObserverGrants writes a fuzzed on-disk grant log and asserts
// LoadSessionObserverGrants inverts it deterministically into a well-formed
// worker→observers map (or fails cleanly on a corrupt log) without panicking.
func FuzzWobsLoadObserverGrants(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{1, 1, 1, 1})
	f.Add([]byte{2, 4, 6, 8, 10})
	f.Add([]byte{3, 5, 7, 9, 11, 13})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &wobs_reader{data: data}
		stateDir := t.TempDir()
		const sessionID = "S1"
		dir := jobsDir(stateDir, sessionID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("wobs: mkdir jobs dir: %v", err)
		}
		path := filepath.Join(dir, "jobs.jsonl")
		wobs_writeGrantLog(t, r, path)

		load := func(struct{}) map[string][]string {
			out, _ := LoadSessionObserverGrants(stateDir, sessionID)
			return out
		}
		oracle.Deterministic(t, load, struct{}{}, oracle.DeepEqual[map[string][]string])
		wobs_grantsConsistent(t, load(struct{}{}))
	})
}