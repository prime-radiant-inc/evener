package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// TestHostManageAddKeepsConcurrentAttachStateRecordedMidCommit pins the
// round-15 finding: the lifecycle events a concurrent attach delivers land
// through observeEvent, which takes no mutation mutex and carries no
// generation, so the add's retained-state reset must have already run by the
// time the registry entry becomes visible. An unconditional drop placed after
// the insert erased whatever a concurrent attach of the newly visible host had
// recorded — its midAttach for an in-progress connect, its lastAttachError
// for one that already failed — and nothing recreated the record until the
// attach's next event, which a terminal failure never sends.
//
// Deterministic, no sleeps on the assertion path: the add parks inside its
// commit at the store's first take (the pre-write snapshot) because
// the test holds the store's mutex, the commit's cfg.mu hold proves the add
// is inside its critical section, and the stale-record wait below proves the
// reset already ran before the event fires (post-fix the reset precedes the
// park; pre-fix it never runs while the add is parked, the wait falls
// through after its budget, and the erasure the finding names is exactly
// what the assertions then catch).
func TestHostManageAddKeepsConcurrentAttachStateRecordedMidCommit(t *testing.T) {
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, "", hosts, nil)

	// Seed the stale record the re-add contract sweeps: a failed attach from
	// the name's previous life, whose lastAttachError a re-added host must
	// not inherit.
	m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventFailed, Err: errors.New("stale attach error")})

	// Park the add inside its commit: the store's mutex is held, so
	// the add blocks at its first store take — the pre-save snapshot — with
	// the mutation mutex held and nothing exposed yet.
	m.cfg.store.mu.Lock()
	type addResult struct {
		row appwire.HostRow
		err error
	}
	addDone := make(chan addResult, 1)
	go func() {
		row, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}})
		addDone <- addResult{row: row, err: err}
	}()

	// Wait until the add is inside its mutation critical section: from its
	// first line the commit holds cfg.mu, so a failed TryLock proves it.
	// A TryLock that still succeeds once the budget is spent means the add
	// never entered it, and the choreography below is unsound — fail there
	// rather than proceed.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !m.cfg.mu.TryLock() {
			break
		}
		m.cfg.mu.Unlock()
		time.Sleep(100 * time.Microsecond)
	}
	if m.cfg.mu.TryLock() {
		m.cfg.mu.Unlock()
		t.Fatal("the add never entered its mutation critical section")
	}

	// Post-fix the retained-state reset has already run when the add parks
	// here — it precedes the snapshot — so the stale record is gone; waiting
	// for that keeps the event below from racing the reset. Pre-fix the reset
	// never runs while the add is parked (it sits after the registry insert,
	// at the end of the commit), so the wait falls through after its budget
	// and the assertions below catch the erasure the finding names.
	sweepDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(sweepDeadline) {
		m.cfg.state.mu.Lock()
		rec := m.cfg.state.records["side"]
		m.cfg.state.mu.Unlock()
		if rec == nil {
			break
		}
		time.Sleep(100 * time.Microsecond)
	}

	// The concurrent attach's lifecycle event: once the add's insert lands,
	// an explicit Connect validates against the live registry and dials, and
	// the event that attach emits — through the same OnEvent binding main.go
	// wires to observeEvent — lands while the add is parked mid-commit. The
	// event path takes no cfg.mu, exactly why the reset's position is the
	// only thing that can keep this record.
	m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventState, State: sshconn.StatePreflighting})

	// Let the commit finish.
	m.cfg.store.mu.Unlock()
	res := <-addDone
	if res.err != nil {
		t.Fatalf("Add = %v", res.err)
	}

	// The raced record must survive the commit — the response row folds it,
	// and so does every later row — while the stale error must not render.
	if !res.row.MidAttach {
		t.Fatalf("response row = %+v, want midAttach: the add's bookkeeping erased the lifecycle state a concurrent attach recorded during the commit", res.row)
	}
	if res.row.LastAttachErr != "" {
		t.Fatalf("response row = %+v, want no stale lastAttachError: the re-add must sweep the previous life's record", res.row)
	}
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status = %v", err)
	}
	if !resp.Host.MidAttach {
		t.Fatalf("status row = %+v, want midAttach: the concurrent attach's record must outlive the add's commit", resp.Host)
	}
	if resp.Host.LastAttachErr != "" {
		t.Fatalf("status row = %+v, want no stale lastAttachError: the re-added host starts clean", resp.Host)
	}
}

// TestHostManageHubTOMLSaveSyncsItsDirectory pins the durable-commit
// contract's missing half (the round-15 second finding): the rewrite's temp
// file was synced before the rename, but the containing directory never was,
// so a crash right after a successful add/remove could lose the committed
// entry despite the synced file. The save must open and sync the parent
// directory after the rename — and per the repo-wide rename idiom (the
// hubcore deletion and transcript-display stores, the server's thread-clear
// journal), a directory it cannot open is a loud failure, not a silent
// durability gap.
//
// Falsifiable on a real filesystem without power-loss simulation: a
// write-only directory still lets the temp file be created, synced, and
// renamed inside it (create and rename need write permission, not read), so
// a save that never touches the directory succeeds — the pre-fix behavior —
// while the directory-syncing save refuses it.
func TestHostManageHubTOMLSaveSyncsItsDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the permission-based check cannot fail for root")
	}
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatalf("chmod the hub.toml directory write-only: %v", err)
	}
	path := filepath.Join(dir, "hub.toml")
	err := writeHubTOMLHosts(path, []hostreg.Host{{Name: "side", SSH: "s.example"}})
	if err == nil {
		t.Fatal("writeHubTOMLHosts succeeded into a directory it cannot open, want the post-rename directory sync to refuse the commit")
	}
	if !strings.Contains(err.Error(), "hub.toml directory") {
		t.Fatalf("writeHubTOMLHosts error = %q, want the directory-sync step's refusal", err)
	}
}

// TestHubTOMLSyncUnsupported covers the sync tolerance the write shares
// with the hubcore deletion store and the thread-clear journal: a filesystem
// that cannot sync a directory at all (ENOSYS, ENOTSUP, EINVAL) keeps a
// working save instead of a hard failure, while every other error surfaces.
func TestHubTOMLSyncUnsupported(t *testing.T) {
	for _, err := range []error{syscall.ENOSYS, syscall.ENOTSUP, syscall.EINVAL} {
		if !hubTOMLSyncUnsupported(err) {
			t.Fatalf("hubTOMLSyncUnsupported(%v) = false, want true", err)
		}
	}
	if hubTOMLSyncUnsupported(nil) {
		t.Fatal("hubTOMLSyncUnsupported(nil) = true, want false")
	}
	if hubTOMLSyncUnsupported(errors.New("other")) {
		t.Fatal(`hubTOMLSyncUnsupported("other") = true, want false`)
	}
}
