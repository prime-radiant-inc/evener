//go:build serffuzz

package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzWatchCoreConfigEdges exercises the uncommon, deterministic manager-state
// edges in the core watch configuration and token paths. The byte input selects
// ordering and identifiers; every iteration runs all contracts so the seed
// corpus itself remains a useful coverage regression test.
func FuzzWatchCoreConfigEdges(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})
	f.Add([]byte{255, 0, 17, 99})

	f.Fuzz(func(t *testing.T, data []byte) {
		jm := newTestJM(t)
		jm.closeGrace = 0
		t.Cleanup(func() { _ = jm.close() })

		s := &Session{jobManager: jm}
		if _, _, _, ok := s.resolveWatchSendToken(nil); ok {
			t.Fatal("nil token resolved")
		}

		// A live durable record with no runtime entry reaches configureWatch's
		// post-validation recheck and is rejected because attachment requires the
		// concrete live run, not merely a persisted running status.
		storedID := "job_core_stored"
		if len(data) != 0 {
			storedID += string(rune('a' + data[0]%26))
		}
		wcvpAppendStoreJob(t, jm, storedID, jm.sessionID, jobstore.StatusRunning)
		if _, err := jm.configureWatch(watchArgs{Target: storedID, Events: []string{"job.notification"}}); err == nil || !strings.HasPrefix(err.Error(), "target_not_found:") {
			t.Fatalf("stored-only configure error = %v", err)
		}

		// Settlement is the production accounting edge. Starting one below the
		// cap forces exactly this delivery to cross the budget and invoke teardown.
		cfg, err := newWatchConfig(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}}, jm.now())
		if err != nil {
			t.Fatalf("new budget config: %v", err)
		}
		cfg.deliveries = watchDeliveryBudget - 1
		key := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchID: cfg.watchID, WatchTarget: cfg.target, ResolvedWatchedIdentity: jm.sessionID, WatchGeneration: cfg.generation}
		state := jobstore.WatchSendState{Key: key, DeliveryID: "wd_core", UpdateSeq: 1}
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
		cfg.pending[key] = &state
		cfg.pendingOrder = append(cfg.pendingOrder, key)
		jm.mu.Lock()
		jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target}] = cfg
		jm.mu.Unlock()
		if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
			t.Fatalf("settle budget delivery: %v", err)
		}
		if cfg.deliveries != watchDeliveryBudget {
			t.Fatalf("deliveries = %d, want %d", cfg.deliveries, watchDeliveryBudget)
		}

		// A normal durable pending event proves restore continues to function in
		// the same manager after the preceding teardown mutations.
		restoreState := jobstore.WatchSendState{
			Key:        jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchID: "wch_restore", WatchTarget: runtimeMessageAliasCaller, ResolvedWatchedIdentity: jm.sessionID, ResolvedSendTo: runtimeMessageAliasCaller, WatchGeneration: "gen_restore"},
			DeliveryID: "wd_restore", UpdateSeq: 2,
		}
		if err := jm.store.Append(jobstore.Event{Kind: jobstore.EventWatchSendPending, TS: jm.now(), WatchSend: &restoreState}); err != nil {
			t.Fatalf("append restore pending: %v", err)
		}
		if err := jm.restoreWatchSendPending(); err != nil {
			t.Fatalf("restore pending: %v", err)
		}

		jwccExerciseConfigureFailures(t)
		jwccExerciseReplacementBranches(t)
		jwccExerciseInterleavingSeams(t)
	})
}

func jwccManager(t *testing.T) *jobManager {
	t.Helper()
	jm := newTestJM(t)
	jm.closeGrace = 0
	t.Cleanup(func() { _ = jm.close() })
	return jm
}

func jwccDetached(t *testing.T, jm *jobManager, target string) *watchConfig {
	t.Helper()
	cfg, err := newWatchConfig(watchArgs{Target: target, Events: []string{"job.notification"}}, jm.now())
	if err != nil {
		t.Fatalf("new detached config: %v", err)
	}
	key := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchID: cfg.watchID, WatchTarget: target, ResolvedWatchedIdentity: jm.sessionID, WatchGeneration: cfg.generation}
	state := jobstore.WatchSendState{Key: key, DeliveryID: "wd_detached", UpdateSeq: 1}
	cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}
	cfg.pendingOrder = []jobstore.WatchSendKey{key}
	jm.mu.Lock()
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[cfg] = true
	jm.mu.Unlock()
	return cfg
}

func jwccExerciseConfigureFailures(t *testing.T) {
	t.Helper()

	// The catch-up status lookup is a second durable read after target
	// validation. A closed store reports that read failure directly.
	catchup := jwccManager(t)
	if err := catchup.store.Close(); err != nil {
		t.Fatalf("close catch-up store: %v", err)
	}
	if _, err := catchup.configureWatch(watchArgs{Target: "job_missing", OutputMatch: "ready"}); err == nil {
		t.Fatal("closed catch-up store succeeded")
	}

	// Receiver-internal delivery bypasses public send-target validation. With a
	// concrete live run, install still writes durable state — the observed-by
	// link (grants themselves mint per-delivery now, not at create) — so a
	// closed store fails the install and says the store is the reason.
	grant := jwccManager(t)
	rec, err := grant.createShell(createShellOpts{Command: "core config grant"})
	if err != nil {
		t.Fatalf("create grant target: %v", err)
	}
	if err := grant.store.Close(); err != nil {
		t.Fatalf("close grant store: %v", err)
	}
	_, err = grant.configureWatch(watchArgs{Target: rec.JobID, Events: []string{"job.notification"}, Send: &watchSendArgs{To: "dlg_observer"}, ReceiverSendInternal: true, ReceiverSessionID: "S-observer"})
	if err == nil || !strings.Contains(err.Error(), "store closed") {
		t.Fatalf("closed-store install failure = %v, want the store named", err)
	}
	grant.mu.Lock()
	delete(grant.running, rec.JobID)
	grant.mu.Unlock()

	registered := jwccManager(t)
	registered.appendEvents = func([]jobstore.Event) error { return errors.New("registered append") }
	if _, err := registered.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}}); err == nil {
		t.Fatal("registered append failure succeeded")
	}
	registered.appendEvents = nil

	status := jwccManager(t)
	wcvpAppendStoreJob(t, status, "job_nonterminal", status.sessionID, jobstore.StatusRunning)
	if got, terminal, err := status.terminalWatchTargetStatus("job_nonterminal"); err != nil || terminal || got != "" {
		t.Fatalf("nonterminal status = (%q, %v, %v)", got, terminal, err)
	}

	settle := jwccManager(t)
	settle.appendEvent = func(jobstore.Event) error { return errors.New("settle append") }
	if err := settle.settleWatchSendDelivered(&watchConfig{}, jobstore.WatchSendState{}); err == nil {
		t.Fatal("settlement append failure succeeded")
	}
}

func jwccExerciseReplacementBranches(t *testing.T) {
	t.Helper()
	args := watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}}

	// Equal reconfiguration still tears down matching detached residue. Exercise
	// both the atomic append rollback and successful cleanup paths.
	equalFail := jwccManager(t)
	if _, err := equalFail.configureWatch(args); err != nil {
		t.Fatalf("install equal-fail watch: %v", err)
	}
	jwccDetached(t, equalFail, runtimeMessageAliasCaller)
	equalFail.appendEvents = func([]jobstore.Event) error { return errors.New("equal teardown") }
	if _, err := equalFail.configureWatch(args); err == nil {
		t.Fatal("equal teardown failure succeeded")
	}
	equalFail.appendEvents = nil

	equalOK := jwccManager(t)
	if _, err := equalOK.configureWatch(args); err != nil {
		t.Fatalf("install equal-success watch: %v", err)
	}
	jwccDetached(t, equalOK, runtimeMessageAliasCaller)
	if _, err := equalOK.configureWatch(args); err != nil {
		t.Fatalf("equal detached cleanup: %v", err)
	}

	replace := jwccManager(t)
	if _, err := replace.configureWatch(args); err != nil {
		t.Fatalf("install replacement watch: %v", err)
	}
	replace.appendEvents = func([]jobstore.Event) error { return errors.New("replacement append") }
	if _, err := replace.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}); err == nil {
		t.Fatal("replacement append failure succeeded")
	}
	replace.appendEvents = nil

	// With no live config, matching terminal-flush residue takes the detached-only
	// path. Drive its append rollback and its successful removal/relock path.
	detachedFail := jwccManager(t)
	jwccDetached(t, detachedFail, runtimeMessageAliasCaller)
	detachedFail.appendEvent = func(jobstore.Event) error { return errors.New("detached append") }
	if _, err := detachedFail.configureWatch(args); err == nil {
		t.Fatal("detached append failure succeeded")
	}
	detachedFail.appendEvent = detachedFail.store.Append

	detachedOK := jwccManager(t)
	jwccDetached(t, detachedOK, runtimeMessageAliasCaller)
	if _, err := detachedOK.configureWatch(args); err != nil {
		t.Fatalf("detached cleanup configure: %v", err)
	}
}

func jwccExerciseInterleavingSeams(t *testing.T) {
	t.Helper()

	// The target may disappear between the optimistic durable validation and
	// the locked attachment recheck. Closing the store at that exact unlocked
	// boundary gives the second validation a deterministic observable failure.
	revalidate := jwccManager(t)
	wcvpAppendStoreJob(t, revalidate, "job_revalidate", revalidate.sessionID, jobstore.StatusRunning)
	_, err := revalidate.configureWatchWithHooks(
		watchArgs{Target: "job_revalidate", Events: []string{"job.notification"}},
		watchConfigureHooks{beforeTargetRevalidate: func(string) {
			if closeErr := revalidate.store.Close(); closeErr != nil {
				t.Fatalf("close revalidation store: %v", closeErr)
			}
		}},
	)
	if err == nil || !errors.Is(err, jobstore.ErrStoreClosed) {
		t.Fatalf("target revalidation error = %v", err)
	}

	// Detached teardown releases jm.mu for durable I/O. Model the legitimate
	// competing installer winning that interval and assert this call returns the
	// winner rather than overwriting it.
	winnerJM := jwccManager(t)
	jwccDetached(t, winnerJM, runtimeMessageAliasCaller)
	winner, err := newWatchConfig(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}, winnerJM.now())
	if err != nil {
		t.Fatalf("new winner config: %v", err)
	}
	result, err := winnerJM.configureWatchWithHooks(
		watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}},
		watchConfigureHooks{afterDetachedTeardown: func(key watchKey) {
			winnerJM.mu.Lock()
			winnerJM.watches[key] = winner
			winnerJM.mu.Unlock()
		}},
	)
	if err != nil {
		t.Fatalf("configure after competing install: %v", err)
	}
	if result.WatchID != winner.watchID {
		t.Fatalf("winning watch id = %q, want %q", result.WatchID, winner.watchID)
	}

	// Folded state is defensive against nil values even though the concrete
	// event fold currently never emits one. The injected loader exercises that
	// robustness boundary without fabricating on-disk JSON.
	restore := jwccManager(t)
	nilKey := jobstore.WatchSendKey{WatchID: "wch_nil", WatchTarget: runtimeMessageAliasCaller}
	if err := restore.restoreWatchSendPendingFrom(func() (jobstore.WatchSendRecord, error) {
		return jobstore.WatchSendRecord{Pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{nilKey: nil}}, nil
	}); err != nil {
		t.Fatalf("restore nil pending state: %v", err)
	}
	if len(restore.terminalFlush) != 0 {
		t.Fatalf("nil pending restore created %d configs", len(restore.terminalFlush))
	}
}
