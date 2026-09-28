//go:build linux || darwin

package sshconn

// §3's pre-spawn ownership lifecycle as the production spawn paths drive it,
// with the boundary and the intent store scripted so every disposition is
// deterministic. The junction tests at the end drive the real operation store
// and the real boot reap: the state these tests leave behind is exactly the
// state the reap consumes, which is what makes the reap non-inert.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// fencedTestScope builds a scope over the fake store and boundary, with the
// factory logging its own step so the lifecycle's order is pinnable.
func fencedTestScope(log *fenceTestLog, store *fenceFakeStore, boundary *fenceFakeBoundary) *SpawnScope {
	return &SpawnScope{
		RecordID: "op-1",
		store:    store,
		create: func(string) (SpawnBoundary, error) {
			log.add("create")
			return boundary, nil
		},
	}
}

// TestFencedSpawnCreatesArmsMatchesAndDropsInOrder pins §3's sequence for one
// spawn: the boundary is pre-created and the intent armed BEFORE the exec, the
// launcher-observed (pid, start token) marker lands after it, and a clean exit
// tears the boundary down and drops the intent.
func TestFencedSpawnCreatesArmsMatchesAndDropsInOrder(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{
		log: log,
		id:  BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"},
		// The token is what the kernel would report; the pid is the real child's.
		token: "4242",
	}
	scope := fencedTestScope(log, store, boundary)

	out, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf hi"}, nil)
	if err != nil {
		t.Fatalf("fenced Run: %v", err)
	}
	if string(out) != "hi" {
		t.Fatalf("fenced Run output = %q, want hi", out)
	}

	order := []string{"create", "arm", "spawnattr", "observe", "match", "close", "drop"}
	last := -1
	for _, step := range order {
		at := log.index(step)
		if at < 0 {
			t.Fatalf("the lifecycle skipped %q; log: %s", step, log.joined())
		}
		if at < last {
			t.Fatalf("the lifecycle ran %q out of order; log: %s", step, log.joined())
		}
		last = at
	}
	if len(store.open) != 0 {
		t.Fatalf("a clean exit left %d open intent(s), want none", len(store.open))
	}
	if len(store.matched) != 1 {
		t.Fatalf("matches = %d, want exactly one", len(store.matched))
	}
	match := store.matched[0]
	if match.recordID != "op-1" || match.nonce == "" {
		t.Fatalf("match = %+v, want the scope's record and the armed nonce", match)
	}
	if boundary.observedPid <= 0 || match.pid != boundary.observedPid {
		t.Fatalf("match pid = %d, observed = %d, want the real child's pid", match.pid, boundary.observedPid)
	}
	if match.startTime != "4242" {
		t.Fatalf("match start token = %q, want the observed token", match.startTime)
	}
	if !boundary.released {
		t.Fatal("the spawn attributes' release was never called")
	}
	if !boundary.closed || boundary.closeCalls != 1 {
		t.Fatalf("boundary close calls = %d closed=%t, want one clean teardown", boundary.closeCalls, boundary.closed)
	}
	if len(store.dropped) != 1 || store.dropped[0] != match.nonce {
		t.Fatalf("drops = %v, want the matched nonce %q", store.dropped, match.nonce)
	}
}

// TestFencedSpawnConvergesWhenTheExecFails pins the spawnless-intent rule: a
// Start that never created a process must tear the boundary down and drop the
// armed intent — never leave a name wedged on an intent whose boundary is
// demonstrably empty.
func TestFencedSpawnConvergesWhenTheExecFails(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}
	scope := fencedTestScope(log, store, boundary)

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{filepath.Join(t.TempDir(), "no-such-binary")}, nil)
	if err == nil {
		t.Fatal("a fenced spawn with a missing executable reported no error")
	}
	if store.armCalls != 1 || len(store.open) != 0 {
		t.Fatalf("arm calls = %d, open intents = %d, want one arm converged to none", store.armCalls, len(store.open))
	}
	if len(store.dropped) != 1 {
		t.Fatalf("drops = %v, want the armed intent dropped", store.dropped)
	}
	if !boundary.closed {
		t.Fatal("the boundary was not torn down after the failed exec")
	}
}

// TestFencedSpawnRefusesWhenNoBoundaryCanBePreCreated pins fail-closed: a
// platform or host delegation that cannot pre-create the boundary spawns
// nothing (§3: a not-yet-populated boundary can never authorize a kill, and
// neither can no boundary at all).
func TestFencedSpawnRefusesWhenNoBoundaryCanBePreCreated(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	scope := &SpawnScope{
		RecordID: "op-1",
		store:    store,
		create: func(string) (SpawnBoundary, error) {
			log.add("create")
			return nil, fmt.Errorf("%w: no writable cgroup2 subtree", ErrSpawnBoundaryUnavailable)
		},
	}
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > \"$1\"", "sh", marker}, nil)
	if !errors.Is(err, ErrSpawnBoundaryUnavailable) {
		t.Fatalf("Run error = %v, want ErrSpawnBoundaryUnavailable", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the child ran without a boundary (marker stat = %v)", statErr)
	}
	if store.armCalls != 0 {
		t.Fatalf("arm calls = %d, want none: an unownable spawn must not persist an intent", store.armCalls)
	}
}

// TestUnscopedSpawnTouchesNoBoundaryOrIntentStore pins the read-only half: a
// spawn on a context with no scope must not create a boundary, arm an intent,
// match, or drop. It is exactly the plain spawn the read-only preflight path
// uses — §6 exempts those one-shots, and no record owns them.
func TestUnscopedSpawnTouchesNoBoundaryOrIntentStore(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/plain"}}

	out, err := (execRunner{}).Run(context.Background(), []string{"/bin/sh", "-c", "printf plain"}, nil)
	if err != nil {
		t.Fatalf("unscoped Run: %v", err)
	}
	if string(out) != "plain" {
		t.Fatalf("unscoped Run output = %q, want plain", out)
	}
	if got := log.joined(); got != "" {
		t.Fatalf("an unscoped spawn touched the fence: %s", got)
	}
	if store.armCalls+store.matchCalls+store.dropCalls != 0 {
		t.Fatalf("an unscoped spawn used the intent store: %+v", store)
	}
	if boundary.closeCalls != 0 {
		t.Fatal("an unscoped spawn closed a boundary")
	}
}

// TestSpawnRefusedAfterAFailedArmTearsTheBoundaryDownFirst pins the arm-failure
// order: nothing may be launched without a persisted intent, and the intent may
// be dropped only AFTER the boundary's teardown proved it clean — a drop ahead
// of an untorn boundary would leave an untracked boundary no reap can find.
func TestSpawnRefusedAfterAFailedArmTearsTheBoundaryDownFirst(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log, armErr: errors.New("store write failed")}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}
	scope := fencedTestScope(log, store, boundary)
	marker := filepath.Join(t.TempDir(), "ran")

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > \"$1\"", "sh", marker}, nil)
	if err == nil || !strings.Contains(err.Error(), "store write failed") {
		t.Fatalf("Run error = %v, want the arm refusal surfaced", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the child ran without a persisted intent (marker stat = %v)", statErr)
	}
	closeAt, dropAt := log.index("close"), log.index("drop")
	if closeAt < 0 || dropAt < 0 || closeAt > dropAt {
		t.Fatalf("the teardown must precede the intent drop; log: %s", log.joined())
	}
	if !boundary.closed || len(store.open) != 0 || store.orphanCalls != 0 {
		t.Fatalf("boundary closed=%t open=%d orphan writes=%d, want a converged refusal", boundary.closed, len(store.open), store.orphanCalls)
	}
}

// TestSpawnRefusedAfterAFailedArmKeepsALandedIntentWhenTheBoundaryWillNotClose
// pins the post-rename arm failure: the store can report an error for a write
// that landed durably, so the landed intent must stay armed when the boundary
// will not come down — it is the durable trace the boot reap retries.
func TestSpawnRefusedAfterAFailedArmKeepsALandedIntentWhenTheBoundaryWillNotClose(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log, armErr: errors.New("post-rename failure"), armLandedOnErr: true}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}, closeErr: syscall.EBUSY}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 50 * time.Millisecond

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > /dev/null"}, nil)
	if err == nil || !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("Run error = %v, want the arm refusal and the teardown failure joined", err)
	}
	if len(store.open) != 1 {
		t.Fatalf("open intents = %d, want the landed intent kept as the trace", len(store.open))
	}
	if store.orphanCalls != 0 {
		t.Fatalf("orphan-boundary writes = %d, want none: the armed intent is the trace", store.orphanCalls)
	}
	if len(store.dropped) != 0 {
		t.Fatal("the landed intent was dropped ahead of an unverified teardown")
	}
}

// TestSpawnRefusedAfterAFailedArmRecordsTheBoundaryWhenNoIntentLanded pins the
// other half of the same failure: when nothing landed, the untorn boundary must
// still be tracked, and §9's local-markerless entry persisted through the
// store's unverified-boundary write is what the boot reap enumerates.
func TestSpawnRefusedAfterAFailedArmRecordsTheBoundaryWhenNoIntentLanded(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log, armErr: errors.New("store write failed")}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}, closeErr: syscall.EBUSY}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 50 * time.Millisecond

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > /dev/null"}, nil)
	if err == nil || !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("Run error = %v, want the refusal and the teardown failure joined", err)
	}
	if store.orphanCalls != 1 || len(store.orphanWrites) != 1 {
		t.Fatalf("orphan-boundary writes = %d, want exactly one durable trace", store.orphanCalls)
	}
	var members []struct {
		Kind     string
		Platform string
		CgroupID string
		Nonce    string
	}
	if err := json.Unmarshal(store.orphanWrites[0], &members); err != nil || len(members) != 1 {
		t.Fatalf("the persisted boundary = %s (%v), want one local-markerless entry", store.orphanWrites[0], err)
	}
	entry := members[0]
	if entry.Kind != "local-markerless" || entry.Platform != BoundaryPlatformLinux || entry.CgroupID != "/cg/op-1" || entry.Nonce == "" {
		t.Fatalf("the persisted boundary entry = %+v, want a reaping local-markerless entry", entry)
	}
}

// TestSpawnRefusedAfterASpawnAttrFailureConvergesWhenTheBoundaryCloses pins the
// SpawnAttr arm with a clean teardown: the armed intent is dropped only after
// the boundary proves empty, and the refusal is the SpawnAttr failure.
func TestSpawnRefusedAfterASpawnAttrFailureConvergesWhenTheBoundaryCloses(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}, attrErr: errors.New("no spawn handle")}
	scope := fencedTestScope(log, store, boundary)

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > /dev/null"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no spawn handle") {
		t.Fatalf("Run error = %v, want the SpawnAttr refusal surfaced", err)
	}
	closeAt, dropAt := log.index("close"), log.index("drop")
	if closeAt < 0 || dropAt < 0 || closeAt > dropAt {
		t.Fatalf("the teardown must precede the intent drop; log: %s", log.joined())
	}
	if len(store.open) != 0 || len(store.dropped) != 1 {
		t.Fatalf("open=%d dropped=%v, want the armed intent converged after the teardown", len(store.open), store.dropped)
	}
	if store.orphanCalls != 0 {
		t.Fatalf("orphan-boundary writes = %d, want none after a clean teardown", store.orphanCalls)
	}
}

// TestSpawnRefusedAfterASpawnAttrFailureRetainsTheArmedIntentWhenTheBoundaryWillNotClose
// pins the fail-closed side: the intent is armed, so a boundary that will not
// come down keeps it for the boot reap rather than dropping it into an
// untracked boundary.
func TestSpawnRefusedAfterASpawnAttrFailureRetainsTheArmedIntentWhenTheBoundaryWillNotClose(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{
		log:      log,
		id:       BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"},
		attrErr:  errors.New("no spawn handle"),
		closeErr: syscall.EBUSY,
	}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 50 * time.Millisecond

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > /dev/null"}, nil)
	if err == nil || !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("Run error = %v, want the SpawnAttr refusal and the teardown failure joined", err)
	}
	if len(store.open) != 1 || len(store.dropped) != 0 {
		t.Fatalf("open=%d dropped=%v, want the armed intent kept for the boot reap", len(store.open), store.dropped)
	}
	if store.orphanCalls != 0 {
		t.Fatalf("orphan-boundary writes = %d, want none: the intent already names the boundary", store.orphanCalls)
	}
}

// TestFencedSpawnRefusesWithoutAnIntentStore pins the scope's own contract: a
// scope with no store cannot arm the intent §3 requires, so the spawn is
// refused rather than launched unowned.
func TestFencedSpawnRefusesWithoutAnIntentStore(t *testing.T) {
	scope := &SpawnScope{RecordID: "op-1"}
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > \"$1\"", "sh", marker}, nil)
	if err == nil {
		t.Fatal("a scope with no intent store spawned a child")
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the child ran without an intent store (marker stat = %v)", statErr)
	}
}

// TestFencedSpawnKillsAndConvergesWhenTheMatchFails pins the live half of
// "crash after spawn, before the marker": when the launcher cannot observe and
// persist the (pid, start token), the child is killed and the now-empty
// boundary is converged (closed, intent dropped) rather than left fenced on a
// process the launcher can no longer own.
func TestFencedSpawnKillsAndConvergesWhenTheMatchFails(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{
		log:        log,
		id:         BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"},
		observeErr: errors.New("cannot read the start token"),
	}
	scope := fencedTestScope(log, store, boundary)

	start := time.Now()
	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "sleep 30"}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot read the start token") {
		t.Fatalf("Run error = %v, want the observed failure surfaced", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the unmatched child was not killed (Run took %s)", elapsed)
	}
	if len(store.dropped) != 1 || len(store.open) != 0 {
		t.Fatalf("drops = %v, open = %d, want the intent converged to none", store.dropped, len(store.open))
	}
	if !boundary.closed {
		t.Fatal("the boundary was not torn down after the killed child")
	}
}

// TestFencedSpawnRetriesTheTeardownUntilTheBoundaryIsEmpty pins the bounded
// teardown: a boundary whose last member has not been reaped yet (the kernel
// refuses the removal) is retried, and the intent drops once it comes down.
func TestFencedSpawnRetriesTheTeardownUntilTheBoundaryIsEmpty(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{
		log:       log,
		id:        BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"},
		closeErrs: []error{syscall.EBUSY, nil},
	}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 500 * time.Millisecond

	out, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf ok"}, nil)
	if err != nil || string(out) != "ok" {
		t.Fatalf("fenced Run = %q/%v, want ok", out, err)
	}
	if boundary.closeCalls < 2 {
		t.Fatalf("close calls = %d, want a retry after the busy refusal", boundary.closeCalls)
	}
	if len(store.dropped) != 1 || len(store.open) != 0 {
		t.Fatalf("drops = %v, open = %d, want the intent dropped once the boundary came down", store.dropped, len(store.open))
	}
}

// TestFencedSpawnKeepsTheIntentOpenWhenTheBoundaryWillNotTearDown pins the
// fail-closed arm: a boundary that still holds a member the launcher never
// observed is live, never clean, so the intent stays open for the next boot's
// reap (or orphan-resolve) and the run reports the unsettled teardown.
func TestFencedSpawnKeepsTheIntentOpenWhenTheBoundaryWillNotTearDown(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{
		log:      log,
		id:       BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"},
		closeErr: syscall.EBUSY,
	}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 50 * time.Millisecond

	out, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf ok"}, nil)
	if string(out) != "ok" {
		t.Fatalf("fenced Run output = %q, want the command's own output kept", out)
	}
	if err == nil || !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("Run error = %v, want the unsettled teardown surfaced", err)
	}
	if len(store.dropped) != 0 || len(store.open) != 1 {
		t.Fatalf("drops = %v, open = %d, want the intent kept open", store.dropped, len(store.open))
	}
}

// TestNonEnforcingBoundaryDropsOnTheChildsOwnExit pins the Darwin arm's
// asymmetry: the (pgid, session id) pair cannot prove emptiness, so the fence
// drops the intent on the reaped child's own exit instead of requiring a
// teardown the platform cannot offer — while the boot reap keeps its own
// fail-closed rule for whatever a crash left open.
func TestNonEnforcingBoundaryDropsOnTheChildsOwnExit(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{
		log:          log,
		id:           BoundaryID{Platform: BoundaryPlatformDarwin, PGID: 100, SessionID: 100},
		notEnforcing: true,
		// A teardown the platform cannot offer must not wedge the record.
		closeErr: errors.New("no teardown on this arm"),
	}
	scope := fencedTestScope(log, store, boundary)

	out, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf darwin"}, nil)
	if err != nil || string(out) != "darwin" {
		t.Fatalf("fenced Run = %q/%v, want darwin", out, err)
	}
	if len(store.dropped) != 1 || len(store.open) != 0 {
		t.Fatalf("drops = %v, open = %d, want the intent dropped on the child's exit", store.dropped, len(store.open))
	}
}

// --- the wiring's state as the boot reap consumes it ---

// fenceReapHandle is a scripted hostfence.LocalBoundaryHandle standing in for
// execenv's enumeration.
type fenceReapHandle struct {
	members    []execenv.BoundaryMember
	membersErr error
	closed     bool
}

func (h *fenceReapHandle) Enforcing() bool { return true }

func (h *fenceReapHandle) Members() ([]execenv.BoundaryMember, error) {
	if h.membersErr != nil {
		return nil, h.membersErr
	}
	return append([]execenv.BoundaryMember(nil), h.members...), nil
}

func (h *fenceReapHandle) SignalVerified(int, string) error {
	return errors.New("the reap signaled a member no persisted pair verified")
}

func (h *fenceReapHandle) Await(time.Duration, func([]execenv.BoundaryMember) bool) error { return nil }

func (h *fenceReapHandle) Close() error {
	h.closed = true
	return nil
}

// newFenceStore opens a real operation store under a fresh state root.
func newFenceStore(t *testing.T) *hostops.Store {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	return store
}

// newRunningFenceRecord persists one running deploy record the fence arms on.
func newRunningFenceRecord(t *testing.T, store *hostops.Store) hostops.Record {
	t.Helper()
	return newRunningFenceRecordOfKind(t, store, hostops.KindDeploy)
}

// newRunningFenceRecordOfKind persists one running record of kind.
func newRunningFenceRecordOfKind(t *testing.T, store *hostops.Store, kind hostops.Kind) hostops.Record {
	t.Helper()
	record, err := store.Create(hostops.NewRecord{
		ClientOperationID: "client-" + string(kind),
		Host:              "h1",
		Kind:              kind,
		Generation:        7,
		IncarnationID:     "inc-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	record, err = store.TransitionToState(record.ID, hostops.StateRunning, nil, "started")
	if err != nil {
		t.Fatalf("TransitionToState: %v", err)
	}
	return record
}

// TestCrashBetweenArmAndSpawnIsConvergedByTheBootReap is the first
// fault-injection arm: the pre-spawn intent landed and the exec never happened
// (the boundary is pre-created and empty). The boot reap must enumerate the
// empty boundary, tear it down, drop the intent, and leave the record able to
// surface as `interrupted` — never a name wedged on an intent no process
// backs.
func TestCrashBetweenArmAndSpawnIsConvergedByTheBootReap(t *testing.T) {
	store := newFenceStore(t)
	record := newRunningFenceRecord(t, store)
	scope := &SpawnScope{RecordID: record.ID, store: store}
	boundary := &fenceFakeBoundary{log: &fenceTestLog{}, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}

	nonce, landed, err := scope.armSpawnIntent(boundary)
	if err != nil {
		t.Fatalf("armSpawnIntent: %v", err)
	}
	if nonce == "" || !landed {
		t.Fatalf("armSpawnIntent = %q/%t, want a minted nonce and a landed intent", nonce, landed)
	}
	if got := store.SpawnIntentRecords(); len(got) != 1 {
		t.Fatalf("persisted intents = %d, want 1: the pre-spawn write is what makes the boundary reapable", len(got))
	}

	handle := &fenceReapHandle{}
	dropped, err := hostfence.ReapLocalOrphanBoundary(store, hostfence.ReapOptions{
		Open: func(execenv.BoundaryIdentity) (hostfence.LocalBoundaryHandle, error) { return handle, nil },
	})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("reap dropped %d intent(s), want 1", dropped)
	}
	if !handle.closed {
		t.Fatal("the reap did not tear the empty boundary down")
	}
	if got := store.SpawnIntentRecords(); len(got) != 0 {
		t.Fatalf("intents after the reap = %d, want none", len(got))
	}
	// §7's boot order runs the reap first and the interrupted transition after
	// it: with the intent dropped, the crashed operation surfaces as
	// `interrupted`, never a stuck pending/running record (acceptance 2).
	if _, err := store.RecoverInterrupted(); err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	got, ok := store.Record(record.ID)
	if !ok {
		t.Fatal("the record disappeared")
	}
	if got.State != hostops.StateInterrupted {
		t.Fatalf("record state = %q, want interrupted", got.State)
	}
}

// TestCleanExitLeavesNothingForTheBootReap is the other half: a spawn that ran
// to completion under the fence drops its intent and closes its boundary, so
// the record can reach a terminal state (the store refuses a terminal record
// with an open intent) and the next boot's reap has nothing to converge.
func TestCleanExitLeavesNothingForTheBootReap(t *testing.T) {
	store := newFenceStore(t)
	record := newRunningFenceRecord(t, store)
	boundary := &fenceFakeBoundary{log: &fenceTestLog{}, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}, token: "4242"}
	scope := &SpawnScope{
		RecordID: record.ID,
		store:    store,
		create:   func(string) (SpawnBoundary, error) { return boundary, nil },
	}

	out, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf ok"}, nil)
	if err != nil || string(out) != "ok" {
		t.Fatalf("fenced Run = %q/%v, want ok", out, err)
	}
	if got := store.SpawnIntentRecords(); len(got) != 0 {
		t.Fatalf("intents after a clean exit = %d, want none", len(got))
	}
	if _, err := store.TransitionToState(record.ID, hostops.StateComplete, &hostops.Result{OK: true, Message: "done"}, "done"); err != nil {
		t.Fatalf("the record could not complete after a clean fenced spawn: %v", err)
	}
}

// TestKeptOpenIntentBlocksTheTerminalWrite pins why the drop is load-bearing:
// while the fence has an intent open (a boundary that would not come down), the
// store refuses to terminalize the record — the name stays fenced for the
// boot reap or orphan-resolve instead of being recorded as finished.
func TestKeptOpenIntentBlocksTheTerminalWrite(t *testing.T) {
	store := newFenceStore(t)
	record := newRunningFenceRecord(t, store)
	boundary := &fenceFakeBoundary{
		log:      &fenceTestLog{},
		id:       BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"},
		token:    "4242",
		closeErr: syscall.EBUSY,
	}
	scope := &SpawnScope{
		RecordID: record.ID,
		store:    store,
		create:   func(string) (SpawnBoundary, error) { return boundary, nil },
		settle:   50 * time.Millisecond,
	}

	if _, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf ok"}, nil); err == nil {
		t.Fatal("a boundary that would not come down reported no error")
	}
	if got := store.SpawnIntentRecords(); len(got) != 1 {
		t.Fatalf("intents after the unsettled teardown = %d, want 1 kept open", len(got))
	}
	if _, err := store.TransitionToState(record.ID, hostops.StateComplete, &hostops.Result{OK: true, Message: "done"}, "done"); err == nil {
		t.Fatal("the store terminalized a record whose spawn intent is still open")
	}
}

// TestSpawnedButUnmatchedIntentStaysFencedAtBoot is the second fault-injection
// arm: the child spawned and the launcher-observed marker never landed (a crash
// between the spawn and the post-spawn persist). The boundary holds a live
// member no persisted pair accounts for, so the boot reap must keep the intent
// open and mark the record `orphan-unverified` — never clean, never
// `interrupted`, until the operator or a later enumeration resolves it.
func TestSpawnedButUnmatchedIntentStaysFencedAtBoot(t *testing.T) {
	store := newFenceStore(t)
	record := newRunningFenceRecord(t, store)
	scope := &SpawnScope{RecordID: record.ID, store: store}
	boundary := &fenceFakeBoundary{log: &fenceTestLog{}, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}

	if _, _, err := scope.armSpawnIntent(boundary); err != nil {
		t.Fatalf("armSpawnIntent: %v", err)
	}
	// The spawn itself: a real localhost child the launcher never got to match.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the boundary child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	token, err := execenv.ObserveProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ObserveProcess: %v", err)
	}

	handle := &fenceReapHandle{members: []execenv.BoundaryMember{{PID: cmd.Process.Pid, StartToken: token}}}
	dropped, err := hostfence.ReapLocalOrphanBoundary(store, hostfence.ReapOptions{
		Open: func(execenv.BoundaryIdentity) (hostfence.LocalBoundaryHandle, error) { return handle, nil },
	})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d intent(s) on an unrecognized live member, want 0", dropped)
	}
	if got := store.SpawnIntentRecords(); len(got) != 1 {
		t.Fatalf("intents after the fail-closed reap = %d, want the intent kept open", len(got))
	}
	got, ok := store.Record(record.ID)
	if !ok {
		t.Fatal("the record disappeared")
	}
	if got.State != hostops.StateOrphanUnverified {
		t.Fatalf("record state = %q, want orphan-unverified", got.State)
	}
	if len(got.OrphanBoundary) == 0 {
		t.Fatal("the orphan-unverified record carries no persisted boundary")
	}
}

// TestDeployForOperationCarriesTheScopeToItsCommands pins the seam between the
// hub's operation worker and the fence: the context the worker hands
// DeployForOperation is inherited by every one-shot command the deploy step
// runs, so the spawn scope reaches execRunner's fence instead of being lost at
// the seam. The read-only preflight, run first on a scope-less context, stays
// unarmed.
func TestDeployForOperationCarriesTheScopeToItsCommands(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				return []byte(`{"protocol":"` + appwire.ProtocolVersion + `","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"` + appwire.ProtocolVersion + `","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	type sighting struct {
		command string
		scoped  bool
	}
	var seen []sighting
	innerRun := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		_, scoped := SpawnScopeFrom(ctx)
		seen = append(seen, sighting{command: strings.Join(argv, " "), scoped: scoped})
		return innerRun(ctx, argv, stdin)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})

	facts, err := m.preflight(context.Background(), host)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("the preflight ran no commands")
	}
	for _, run := range seen {
		if run.scoped {
			t.Fatalf("a preflight command ran armed: %s", run.command)
		}
	}

	seen = nil
	scope := NewSpawnScope("op-deploy-1", &fenceFakeStore{log: &fenceTestLog{}})
	if _, _, err := m.DeployForOperation(WithSpawnScope(context.Background(), scope), host, facts); err != nil {
		t.Fatalf("DeployForOperation: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("the deploy ran no commands")
	}
	var mutating int
	for _, run := range seen {
		if strings.Contains(run.command, "launch-check") {
			// The post-deploy launch-contract re-read is a read-only §6 one-shot:
			// it runs outside the scope, like the preflight, so only the deploy's
			// mutating commands are armed.
			if run.scoped {
				t.Fatalf("the read-only launch-contract re-read ran armed: %s", run.command)
			}
			continue
		}
		mutating++
		if !run.scoped {
			t.Fatalf("a mutating deploy command ran with no spawn scope: %s", run.command)
		}
	}
	if mutating == 0 {
		t.Fatalf("the deploy ran no mutating command; sightings: %+v", seen)
	}
}

// TestUnverifiedBoundaryTraceIsReapable drives the fallback trace end to end:
// the §9 local-markerless entry a refused spawn persists (no intent landed, the
// boundary would not come down) is exactly what the boot reap enumerates, tears
// down, and resolves — never an untracked boundary.
func TestUnverifiedBoundaryTraceIsReapable(t *testing.T) {
	store := newFenceStore(t)
	record := newRunningFenceRecord(t, store)
	boundary := &fenceFakeBoundary{log: &fenceTestLog{}, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}

	entries, err := markerlessBoundaryEntries(boundary.Identity(), "nonce-trace")
	if err != nil {
		t.Fatalf("markerlessBoundaryEntries: %v", err)
	}
	if _, err := store.SetOrphanBoundary(record.ID, entries, nil); err != nil {
		t.Fatalf("SetOrphanBoundary: %v", err)
	}
	got, ok := store.Record(record.ID)
	if !ok || got.State != hostops.StateOrphanUnverified {
		t.Fatalf("record = %+v, want orphan-unverified", got)
	}

	var opened []execenv.BoundaryIdentity
	handle := &fenceReapHandle{}
	dropped, err := hostfence.ReapLocalOrphanBoundary(store, hostfence.ReapOptions{
		Open: func(id execenv.BoundaryIdentity) (hostfence.LocalBoundaryHandle, error) {
			opened = append(opened, id)
			return handle, nil
		},
	})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("reap converged %d row(s), want 1", dropped)
	}
	if len(opened) != 1 || opened[0].CgroupID != "/cg/op-1" {
		t.Fatalf("the reap opened %+v, want the persisted markerless cgroup", opened)
	}
	if !handle.closed {
		t.Fatal("the reap did not tear the marked boundary down")
	}
	resolved, ok := store.Record(record.ID)
	if !ok || resolved.State != hostops.StateInterrupted {
		t.Fatalf("record = %+v, want interrupted once the trace was reaped", resolved)
	}
}

// TestSpawnRefusedRetriesTheTraceWrite pins the compound-failure hardening: a
// transient store refusal of the §9 unverified-boundary write is retried, so a
// pre-exec refusal still leaves the untorn boundary durably recorded.
func TestSpawnRefusedRetriesTheTraceWrite(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{
		log:        log,
		armErr:     errors.New("store write failed"),
		orphanErrs: []error{errors.New("write failed"), errors.New("write failed"), nil},
	}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}, closeErr: syscall.EBUSY}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 50 * time.Millisecond

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > /dev/null"}, nil)
	if err == nil || !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("Run error = %v, want the refusal and the teardown failure joined", err)
	}
	if store.orphanCalls != 3 || len(store.orphanWrites) != 1 {
		t.Fatalf("orphan writes = %d calls / %d persisted, want the write retried to one durable trace", store.orphanCalls, len(store.orphanWrites))
	}
}

// TestSpawnRefusedConvergesWhenTheTraceWriteFailsButTheBoundaryComesDown pins
// the other side of the compound failure: when the store cannot carry the
// trace, one more bounded teardown window runs — an empty boundary that comes
// down needs no trace, so the write's failure is reported without an untracked
// boundary left behind.
func TestSpawnRefusedConvergesWhenTheTraceWriteFailsButTheBoundaryComesDown(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log, armErr: errors.New("store write failed"), orphanErr: errors.New("store is not writable")}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}
	// The teardown refuses until the store has been asked for a trace; the
	// second bounded window (after the trace write failed) then lets it come
	// down.
	boundary.onClose = func() error {
		if store.orphanCalls > 0 {
			return nil
		}
		return syscall.EBUSY
	}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 50 * time.Millisecond

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > /dev/null"}, nil)
	if err == nil || !strings.Contains(err.Error(), "store is not writable") {
		t.Fatalf("Run error = %v, want the failed trace write reported", err)
	}
	if !boundary.closed {
		t.Fatal("the boundary did not come down on the second bounded teardown window")
	}
}

// TestSpawnRefusedReportsAnUntrackedBoundaryOnlyWhenTheStoreAndKernelBothRefuse
// pins the last-resort honesty: a store that cannot write the trace and a
// boundary that will not come down leave the (empty, child-less) boundary
// untracked, and the refusal says so instead of swallowing it.
func TestSpawnRefusedReportsAnUntrackedBoundaryOnlyWhenTheStoreAndKernelBothRefuse(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log, armErr: errors.New("store write failed"), orphanErr: errors.New("store is not writable")}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}, closeErr: syscall.EBUSY}
	scope := fencedTestScope(log, store, boundary)
	scope.settle = 50 * time.Millisecond

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope),
		[]string{"/bin/sh", "-c", "printf ran > /dev/null"}, nil)
	if err == nil || !strings.Contains(err.Error(), "untracked") {
		t.Fatalf("Run error = %v, want the untracked boundary named", err)
	}
	if boundary.closed {
		t.Fatal("the boundary was closed, so this test does not exercise the untracked arm")
	}
	if store.orphanCalls != 3 {
		t.Fatalf("orphan write calls = %d, want the bounded retries before giving up", store.orphanCalls)
	}
}

// TestRestartForOperationCarriesTheScopeToItsCommands is the restart twin of
// the deploy seam pin: the context the hub hands RestartForOperation reaches
// every command the restart step runs, so the spawn scope cannot be lost at the
// seam. The seam's own read-only pre-restart running probe is explicitly
// unarmed — it runs via WithoutSpawnScope, the deploy twin of the launch-
// contract re-read, both §6's exempt read-only one-shots — while the mutating
// step's commands, including the proven-replacement waits it runs, stay armed
// (§3 arms what the step spawns).
func TestRestartForOperationCarriesTheScopeToItsCommands(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(int) ([]byte, error) {
			return []byte(`{"protocol":"` + appwire.ProtocolVersion + `","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	type sighting struct {
		command string
		scoped  bool
	}
	var seen []sighting
	innerRun := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		_, scoped := SpawnScopeFrom(ctx)
		seen = append(seen, sighting{command: strings.Join(argv, " "), scoped: scoped})
		return innerRun(ctx, argv, stdin)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})

	facts, err := m.preflight(context.Background(), host)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	for _, run := range seen {
		if run.scoped {
			t.Fatalf("a preflight command ran armed: %s", run.command)
		}
	}

	seen = nil
	scope := NewSpawnScope("op-restart-1", &fenceFakeStore{log: &fenceTestLog{}})
	if err := m.RestartForOperation(WithSpawnScope(context.Background(), scope), host, facts); err != nil {
		t.Fatalf("RestartForOperation: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("the restart step ran no commands")
	}
	var restarts, probes int
	firstProbe := true
	for _, run := range seen {
		if strings.Contains(run.command, "api/health") && firstProbe {
			// The seam's own read-only pre-restart probe runs before the
			// mutating step, so the first health sighting is it — the restart
			// twin of the deploy seam's unarmed launch-contract re-read. Later
			// health probes belong to the restart's proven-replacement wait
			// inside the mutating step, which §3 arms like the rest of that
			// step's spawns.
			firstProbe = false
			probes++
			if run.scoped {
				t.Fatalf("the read-only pre-restart probe ran armed: %s", run.command)
			}
			continue
		}
		if !run.scoped {
			t.Fatalf("a mutating restart command ran with no spawn scope: %s", run.command)
		}
		if strings.Contains(run.command, "systemctl restart") {
			restarts++
		}
	}
	if restarts == 0 {
		t.Fatalf("the restart step ran no restart command; sightings: %+v", seen)
	}
	if probes == 0 {
		t.Fatalf("the read-only pre-restart probe did not run; sightings: %+v", seen)
	}
}

// TestRestartRecordIntentStateIsReapable pins the restart record's half of the
// convergence contract: a restart-only Ensure's record carries a pre-spawn
// intent exactly like the deploy record's, so `SpawnIntentRecords` surfaces it
// to the boot reap and the same pass converges a spawnless intent.
func TestRestartRecordIntentStateIsReapable(t *testing.T) {
	store := newFenceStore(t)
	record := newRunningFenceRecordOfKind(t, store, hostops.KindRestart)
	scope := &SpawnScope{RecordID: record.ID, store: store}
	boundary := &fenceFakeBoundary{log: &fenceTestLog{}, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}

	nonce, landed, err := scope.armSpawnIntent(boundary)
	if err != nil || !landed || nonce == "" {
		t.Fatalf("armSpawnIntent = %q/%t/%v, want a landed intent on the restart record", nonce, landed, err)
	}
	open := store.SpawnIntentRecords()
	if len(open) != 1 || open[0].ID != record.ID || open[0].Kind != hostops.KindRestart {
		t.Fatalf("SpawnIntentRecords = %+v, want the restart record the reap reads", open)
	}

	// The spawnless intent (the exec never happened) converges through the same
	// boot reap the deploy record uses.
	handle := &fenceReapHandle{}
	dropped, err := hostfence.ReapLocalOrphanBoundary(store, hostfence.ReapOptions{
		Open: func(execenv.BoundaryIdentity) (hostfence.LocalBoundaryHandle, error) { return handle, nil },
	})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("reap converged %d row(s), want 1", dropped)
	}
	if !handle.closed {
		t.Fatal("the reap did not tear the empty restart boundary down")
	}
	if got := store.SpawnIntentRecords(); len(got) != 0 {
		t.Fatalf("intents after the reap = %d, want none", len(got))
	}
	if _, err := store.RecoverInterrupted(); err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	resolved, ok := store.Record(record.ID)
	if !ok || resolved.State != hostops.StateInterrupted {
		t.Fatalf("restart record = %+v, want interrupted once the reap converged it", resolved)
	}
}

// TestFencedSpawnReportsASuccessfulCommandWhenTheChildIsAlreadyGone pins the
// gone-vs-unobservable rule: a child that exits between Start and Observe reads
// as already clean (§3), so the caller sees the command's own successful result
// and the empty boundary's intent is dropped — never a spurious failure for a
// fast command whose side effects landed.
func TestFencedSpawnReportsASuccessfulCommandWhenTheChildIsAlreadyGone(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{
		log:        log,
		id:         BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"},
		observeErr: ErrSpawnMemberGone,
	}
	scope := fencedTestScope(log, store, boundary)

	out, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf fast"}, nil)
	if err != nil {
		t.Fatalf("Run = %q/%v, want the command's own success: a child gone before Observe is already clean", out, err)
	}
	if string(out) != "fast" {
		t.Fatalf("output = %q, want fast", out)
	}
	if len(store.dropped) != 1 || len(store.open) != 0 {
		t.Fatalf("drops = %v, open = %d, want the markerless intent dropped once the boundary proved empty", store.dropped, len(store.open))
	}
	if len(store.matched) != 0 {
		t.Fatal("a gone child was matched as if it had been observed")
	}
	if !boundary.closed {
		t.Fatal("the boundary was not torn down")
	}
}

// TestFencedSpawnRefusesAnEmptyArgvBeforeAnySideEffect pins the refusal order: a
// fenced spawn with no argv can never exec, so it refuses before the boundary is
// created and before any intent is armed — nothing to tear down, nothing to
// converge.
func TestFencedSpawnRefusesAnEmptyArgvBeforeAnySideEffect(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/op-1"}}
	scope := fencedTestScope(log, store, boundary)

	_, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "empty argv") {
		t.Fatalf("Run error = %v, want the empty-argv refusal", err)
	}
	if got := log.joined(); got != "" {
		t.Fatalf("the refused spawn touched the boundary machinery: %s", got)
	}
	if store.armCalls != 0 || boundary.closeCalls != 0 {
		t.Fatalf("arm calls = %d, close calls = %d, want no side effects at all", store.armCalls, boundary.closeCalls)
	}
}
