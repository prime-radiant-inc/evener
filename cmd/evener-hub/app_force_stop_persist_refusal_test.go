package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// A recovery-persistence failure commits nothing and signals nothing, so the
// refusal must stay admission-neutral like every other refusal that canceled
// nothing: the deferred fence release must not publish the admission epoch or
// the connection sequence for a record that never landed. RoboRev reported the
// bypass on the resume-lifecycle series: the return skipped refuseStop, so the
// unrejected Finish minted an advance for an uncommitted refusal.
func TestForceStopPersistFailureLeavesAdmissionNeutral(t *testing.T) {
	runDir := t.TempDir()
	stateRoot := t.TempDir()
	sessionID := hubtest.SessionID(t)
	entry := rendezvous.Entry{
		PID: 4301, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID,
		Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
	}
	writeRendezvous(t, runDir, entry)
	locks, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, HubStateRoot: stateRoot, ResumeLocks: locks, DeletionStore: store, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})}
	before := locks.RecoveryState(sessionID)
	// The obstruction helper holds the recovery directory aside, so it must
	// exist: nothing has committed yet on a fresh root.
	if err := os.MkdirAll(filepath.Join(stateRoot, "recovery"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := obstructRecoveryDirectory(t, stateRoot)
	defer restore()
	err = forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
	if err == nil || !strings.Contains(err.Error(), "persist session recovery") {
		t.Fatalf("persist failure did not surface as the stop's error: %v", err)
	}
	after := locks.RecoveryState(sessionID)
	if after.Epoch != before.Epoch || after.LastRecoverySequence != before.LastRecoverySequence || after.Stopping != before.Stopping {
		t.Fatalf("uncommitted refusal left the fence published: before=%+v after=%+v", before, after)
	}
	if slices.Contains(events, "kill") {
		t.Fatal("persist failure still signaled the daemon")
	}
}

// The escape's unconfirmed-claim check must be the sibling predicate's: an
// unconfirmed claim whose workspace ref needs canonicalization still names
// the alias (localSpawnWorkspaceRef parses and trims), and an unconfirmed
// claim whose identity cannot be resolved at all can never be excluded. A
// hand-rolled raw-field match misses both and would let the escape confirm
// the old owner's exit and launch a replacement while a live unknown process
// may hold the group.
func TestResumeRefusesClaimlessRecoveryWithNonCanonicalWorkspaceClaim(t *testing.T) {
	runDir := t.TempDir()
	sessionID := "noncanonical-claim-owner"
	forkAlias := "noncanonical-claim-fork"
	// The claim's only identity is a workspace ref that parses to the group's
	// other alias but does not equal it byte-for-byte.
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1007, Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", WorkspaceRef: " local:" + forkAlias})
	roster := liveClaimRoster(runDir, &fakeProber{shouldFail: true})
	roster.Refresh()
	if len(roster.UnconfirmedEntries()) != 1 {
		t.Fatal("setup did not stage an unconfirmed claim")
	}
	locks, _, owner := persistedUnconfirmedRecovery(t, sessionID)
	if _, err := locks.PersistForceStopWithOwner([]string{forkAlias, sessionID}, sessionID, owner); err != nil {
		t.Fatal(err)
	}
	spawned := false
	cfg := hubcore.WebConfig{
		Roster:      roster,
		ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
			// The persisted owner is provably gone, so only the unconfirmed
			// claim can keep the refusal.
			return nil, daemonprocess.ErrExited
		}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned = true
			return rendezvous.Entry{}, errors.New("spawn sentinel")
		}},
	}
	_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{Session: sessionID})
	if spawned {
		t.Fatal("resume launched a replacement while an unconfirmed live process claimed the group's workspace")
	}
	if err == nil || !strings.Contains(err.Error(), "resume owner exit is unconfirmed") {
		t.Fatalf("non-canonical workspace claim did not keep the refusal: %v", err)
	}
}

// The committed-with-error counterpart: when the record's rename landed but a
// later sync step failed, the recovery obligation IS installed (the visible
// store carries it) even though the stop returns an error. That refusal
// mutated the world, so the fence must publish: admissions from before the
// stop are stale and the obligation stands. Rolling the fence back (the
// neutral path for an uncommitted refusal) would let a pre-stop admission
// survive the whole stop/resume cycle. RoboRev reported the
// committed-but-error case after the uncommitted fix.
func TestForceStopCommittedPersistErrorStillPublishesTheFence(t *testing.T) {
	runDir := t.TempDir()
	stateRoot := t.TempDir()
	sessionID := hubtest.SessionID(t)
	entry := rendezvous.Entry{
		PID: 4301, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID,
		Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
	}
	writeRendezvous(t, runDir, entry)
	locks, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, HubStateRoot: stateRoot, ResumeLocks: locks, DeletionStore: store, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})}
	before := locks.RecoveryState(sessionID)
	// The rename lands; the AfterRename step fails: committed with error.
	boom := errors.New("after-rename sync failed")
	locks.SetRecoveryStoreFaultsForTest(nil, func() error { return boom })
	err = forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
	if err == nil || !strings.Contains(err.Error(), "persist session recovery") {
		t.Fatalf("committed-with-error did not surface as the stop's error: %v", err)
	}
	after := locks.RecoveryState(sessionID)
	if after.Epoch != before.Epoch+1 {
		t.Fatalf("committed refusal did not mint exactly one epoch advance: before=%+v after=%+v", before, after)
	}
	if !after.ResumeRequired || after.ResumeSessionID != sessionID {
		t.Fatalf("committed refusal lost the recovery obligation: %+v", after)
	}
	if slices.Contains(events, "kill") {
		t.Fatal("persist failure still signaled the daemon")
	}
	// The obligation is durable enough to restore: the renamed record carries it.
	restored, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.RecoveryState(sessionID).ResumeRequired {
		t.Fatal("committed record was not visible to a reload")
	}
}

// The committed-with-error refusal must publish the ROOT fence — the
// obligation landed — while the pre-cancellation descendant fences still
// reject: no signal ever reached the shared process, so nothing happened to
// the descendants, and leaving their fences published would stale child
// clients for a stop that never touched them. RoboRev reported the split on
// the committed-signal fix.
func TestForceStopCommittedPersistErrorRejectsDescendantFences(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	parent := buildRPCParentSession(t, stateDir)
	child := buildUpgradeDelegate(t, stateDir, parent)
	stateRoot := t.TempDir()
	locks, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	entry := rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir, Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	writeRendezvous(t, runDir, entry)
	var events []string
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, HubStateRoot: stateRoot, ResumeLocks: locks,
		DeletionStore: func() *hubcore.DeletionStore {
			s, err := hubcore.NewDeletionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return s
		}(),
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			events = append(events, "open")
			return &forceStopProcess{events: &events}, nil
		})}
	parentBefore := locks.RecoveryState(parent)
	childBefore := locks.RecoveryState(child)
	boom := errors.New("after-rename sync failed")
	locks.SetRecoveryStoreFaultsForTest(nil, func() error { return boom })
	err = forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil)
	if err == nil || !strings.Contains(err.Error(), "persist session recovery") {
		t.Fatalf("committed-with-error did not surface as the stop's error: %v", err)
	}
	parentAfter := locks.RecoveryState(parent)
	childAfter := locks.RecoveryState(child)
	if parentAfter.Epoch != parentBefore.Epoch+1 || !parentAfter.ResumeRequired {
		t.Fatalf("root fence did not publish the committed obligation: before=%+v after=%+v", parentBefore, parentAfter)
	}
	if childAfter.Epoch != childBefore.Epoch || childAfter.LastRecoverySequence != childBefore.LastRecoverySequence {
		t.Fatalf("descendant fence published though nothing signaled the shared process: before=%+v after=%+v", childBefore, childAfter)
	}
	if slices.Contains(events, "kill") {
		t.Fatal("persist failure still signaled the daemon")
	}
}
