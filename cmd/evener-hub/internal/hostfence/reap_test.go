//go:build linux

package hostfence

// Crash-fencing 08c §3's local reap: the boot pass that reaps a crashed
// worker's local processes through the persisted boundary-plus-kernel-attested
// identity, marks an unverifiable boundary `orphan-unverified`, and retries on
// every boot. The fake boundary below stands in for agent/execenv's enumeration
// so every disposition is exercised deterministically; the last test drives the
// real cgroup arm when the host delegates one.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// realSleeper spawns a real localhost process with the boundary attributes the
// integration test hands it.
func realSleeper(t *testing.T, attr *syscall.SysProcAttr) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the boundary child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

// fakeBoundary is a scripted LocalBoundaryHandle: its membership is whatever
// the test set, and a verified signal removes the member the way a real kill
// does once it lands.
type fakeBoundary struct {
	members    []execenv.BoundaryMember
	membersErr error
	signalErr  error
	awaitErr   error
	closeErr   error
	// notEnforcing models a platform whose empty enumeration is not proof.
	notEnforcing bool
	killed       []int
	closed       bool
	closeCalls   int
}

func (f *fakeBoundary) Enforcing() bool { return !f.notEnforcing }

func (f *fakeBoundary) Members() ([]execenv.BoundaryMember, error) {
	if f.membersErr != nil {
		return nil, f.membersErr
	}
	return append([]execenv.BoundaryMember(nil), f.members...), nil
}

func (f *fakeBoundary) SignalVerified(pid int, startToken string) error {
	if startToken == "" {
		return errors.New("fake boundary: empty start token")
	}
	if f.signalErr != nil {
		// Model the race the seam refuses to signal through: the pid now names a
		// different instance, so its start token no longer matches.
		for i := range f.members {
			if f.members[i].PID == pid {
				f.members[i].StartToken = "reused"
			}
		}
		return f.signalErr
	}
	f.killed = append(f.killed, pid)
	kept := f.members[:0]
	for _, member := range f.members {
		if member.PID != pid {
			kept = append(kept, member)
		}
	}
	f.members = kept
	return nil
}

func (f *fakeBoundary) Await(wait time.Duration, clean func([]execenv.BoundaryMember) bool) error {
	if f.awaitErr != nil {
		return f.awaitErr
	}
	if clean(f.members) {
		return nil
	}
	return execenv.ErrBoundaryNotSettled
}

func (f *fakeBoundary) Close() error {
	f.closed = true
	f.closeCalls++
	return f.closeErr
}

// newReapStore opens a store under a fresh temp state root.
func newReapStore(t *testing.T) (*hostops.Store, string) {
	t.Helper()
	path := hostops.StorePath(t.TempDir())
	store, err := hostops.Open(path)
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	return store, path
}

// newReapRecord persists one pending deploy record.
func newReapRecord(t *testing.T, store *hostops.Store, host string) hostops.Record {
	t.Helper()
	record, err := store.Create(hostops.NewRecord{
		ClientOperationID: "client-" + host,
		Host:              host,
		Kind:              hostops.KindDeploy,
		Generation:        7,
		IncarnationID:     "inc-" + host,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", host, err)
	}
	return record
}

// linuxIntent is a minimal pre-spawn intent under a stable cgroup path.
func linuxIntent(nonce string) hostops.SpawnIntent {
	return hostops.SpawnIntent{Nonce: nonce, Platform: hostops.SpawnPlatformLinux, CgroupID: "/cg/" + nonce}
}

// openOnce returns an Open seam that serves handle for the first boundary and
// records every identity it was asked for.
func openOnce(handle LocalBoundaryHandle, opened *[]execenv.BoundaryIdentity) func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
	return func(id execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
		if opened != nil {
			*opened = append(*opened, id)
		}
		return handle, nil
	}
}

// TestReapIsANoOpWithNoOpenIntents pins the empty pass: nothing to enumerate,
// no boundary opened, no write.
func TestReapIsANoOpWithNoOpenIntents(t *testing.T) {
	store, path := newReapStore(t)
	newReapRecord(t, store, "h1")
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read store: %v", err)
	}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(&fakeBoundary{}, &opened)})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 0 || len(opened) != 0 {
		t.Fatalf("reap = %d dropped, opened %d boundaries; want a no-op", dropped, len(opened))
	}
	after, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read store: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a reap with no open intents rewrote the store")
	}
}

// TestReapEmptyBoundaryDropsTheIntent pins §3's pre-spawn crash: a
// persisted-but-empty boundary reaps nothing and drops the intent. The record
// is not marked; the later interrupted pass moves it.
func TestReapEmptyBoundaryDropsTheIntent(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(&fakeBoundary{}, &opened)})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 || len(opened) != 1 {
		t.Fatalf("reap = %d dropped, opened %d; want the one intent converged", dropped, len(opened))
	}
	stored, _ := store.Record(record.ID)
	if len(stored.PendingSpawns) != 0 {
		t.Fatalf("the intent survived a clean empty boundary: %+v", stored.PendingSpawns)
	}
	if stored.State != hostops.StatePending {
		t.Fatalf("the record's state = %q, want it left for the interrupted pass", stored.State)
	}
	moved, err := store.RecoverInterrupted()
	if err != nil || moved != 1 {
		t.Fatalf("RecoverInterrupted = %d/%v, want the record moved once", moved, err)
	}
}

// TestReapMarkerlessBoundaryWithMembersMarksOrphanUnverified pins §3's
// markerless rule: with no launcher-observed pair, members are never clean. The
// pass reaps nothing, keeps the intent, and marks the record orphan-unverified
// with the pre-spawn boundary.
func TestReapMarkerlessBoundaryWithMembersMarksOrphanUnverified(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{members: []execenv.BoundaryMember{{PID: 100, StartToken: "42"}}}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened)})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 0 || len(handle.killed) != 0 {
		t.Fatalf("reap dropped %d and killed %v; want neither without a persisted pair", dropped, handle.killed)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("the record's state = %q, want orphan-unverified", stored.State)
	}
	if len(stored.PendingSpawns) != 1 {
		t.Fatalf("the intent was dropped on a fail-closed boundary: %+v", stored.PendingSpawns)
	}
	want := `[{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n1","nonce":"n1"}]`
	if string(stored.OrphanBoundary) != want {
		t.Fatalf("orphanBoundary = %s, want %s", stored.OrphanBoundary, want)
	}
}

// TestReapVerifiedMemberIsKilledAndClears pins the positive arm: a member whose
// kernel start token still matches the persisted pair is signaled, the boundary
// is proven clean, and the intent drops.
func TestReapVerifiedMemberIsKilledAndClears(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 100, "42"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{members: []execenv.BoundaryMember{{PID: 100, StartToken: "42"}}}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened), Observe: func(pid int) (string, error) { return "", execenv.ErrBoundaryMemberGone }})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 || len(handle.killed) != 1 || handle.killed[0] != 100 {
		t.Fatalf("reap = %d dropped, killed %v; want the verified member signaled", dropped, handle.killed)
	}
	if !handle.closed {
		t.Fatal("the proven-clean boundary was not torn down")
	}
	stored, _ := store.Record(record.ID)
	if len(stored.PendingSpawns) != 0 || stored.State == hostops.StateOrphanUnverified {
		t.Fatalf("the verified record = %q/%+v, want the intent dropped and no fence", stored.State, stored.PendingSpawns)
	}
}

// TestReapMismatchedStartTokenReadsClean pins §3's reused-id rule: a member
// whose pid matches but whose start token differs is a different process and is
// never signaled; the boundary reads as already clean.
func TestReapMismatchedStartTokenReadsClean(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 100, "42"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{members: []execenv.BoundaryMember{{PID: 100, StartToken: "99"}}}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened), Observe: func(pid int) (string, error) { return "", execenv.ErrBoundaryMemberGone }})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 || len(handle.killed) != 0 {
		t.Fatalf("reap = %d dropped, killed %v; want the reused id read as clean and never signaled", dropped, handle.killed)
	}
}

// TestReapSignalRaceReadsClean pins the same rule at the signal boundary: a pid
// recycled between enumeration and signal is refused by the seam and reads as
// already clean, never as a fail-closed orphan.
func TestReapSignalRaceReadsClean(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 100, "42"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{
		members:   []execenv.BoundaryMember{{PID: 100, StartToken: "42"}},
		signalErr: execenv.ErrBoundaryIdentityChanged,
	}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened), Observe: func(pid int) (string, error) { return "", execenv.ErrBoundaryMemberGone }})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 || len(handle.killed) != 0 {
		t.Fatalf("reap = %d dropped, killed %v; want the raced member read as clean", dropped, handle.killed)
	}
	stored, _ := store.Record(record.ID)
	if stored.State == hostops.StateOrphanUnverified {
		t.Fatal("a refused signal on a recycled pid fenced the host")
	}
}

// TestReapUnrecognizedMemberMarksOrphanAndKeepsIntent pins §3's fail-closed
// rule: a member whose pid matches no persisted pair — a forked descendant the
// launcher never observed — reads as live. The pass reaps nothing, keeps the
// intent open, and marks the record with the marked local entry.
func TestReapUnrecognizedMemberMarksOrphanAndKeepsIntent(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 100, "42"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{members: []execenv.BoundaryMember{{PID: 200, StartToken: "7"}}}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened), Observe: func(pid int) (string, error) { return "", execenv.ErrBoundaryMemberGone }})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 0 || len(handle.killed) != 0 {
		t.Fatalf("reap = %d dropped, killed %v; want an unrecognized member left alone", dropped, handle.killed)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
	}
	want := `[{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n1","pid":100,"startTime":"42"}]`
	if string(stored.OrphanBoundary) != want {
		t.Fatalf("orphanBoundary = %s, want %s", stored.OrphanBoundary, want)
	}
}

// TestReapEnumerationUnavailableFailsClosed pins the hard fail-closed arm: a
// boundary the pass cannot reach is never treated as empty. The pass keeps the
// intent, marks the record, and reports the unreachable boundary as a
// diagnostic.
func TestReapEnumerationUnavailableFailsClosed(t *testing.T) {
	for name, opts := range map[string]ReapOptions{
		"open fails": {
			Open: func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
				return nil, execenv.ErrBoundaryUnavailable
			},
		},
		"members fail": {
			Open: func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
				return &fakeBoundary{membersErr: execenv.ErrBoundaryUnavailable}, nil
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := newReapStore(t)
			record := newReapRecord(t, store, "h1")
			if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
				t.Fatalf("ArmSpawnIntent: %v", err)
			}
			dropped, err := ReapLocalOrphanBoundary(store, opts)
			if err == nil || !strings.Contains(err.Error(), record.ID) {
				t.Fatalf("reap error = %v, want a diagnostic naming the record", err)
			}
			if dropped != 0 {
				t.Fatalf("reap dropped %d; want the unverifiable intent kept", dropped)
			}
			stored, _ := store.Record(record.ID)
			if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
				t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
			}
		})
	}
}

// TestReapGoneBoundaryWithAPairProvenGoneReadsClean pins the vanished-boundary
// arm: the kernel removes only an empty cgroup, and the recorded instance is
// independently proven gone, so the intent drops.
func TestReapGoneBoundaryWithAPairProvenGoneReadsClean(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 100, "42"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{
		Open: func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
			return nil, execenv.ErrBoundaryGone
		},
		Observe: func(pid int) (string, error) { return "", execenv.ErrBoundaryMemberGone },
	})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("reap dropped %d; want the intent dropped", dropped)
	}
	stored, _ := store.Record(record.ID)
	if len(stored.PendingSpawns) != 0 || stored.State == hostops.StateOrphanUnverified {
		t.Fatalf("the record = %q/%+v, want the intent dropped", stored.State, stored.PendingSpawns)
	}
}

// TestReapGoneBoundaryWithALivePairStaysFenced pins the review's fix: a
// vanished boundary is not proof the process it held is dead. A recorded pair
// still alive with its token keeps the record fenced.
func TestReapGoneBoundaryWithALivePairStaysFenced(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 100, "42"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{
		Open: func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
			return nil, execenv.ErrBoundaryGone
		},
		Observe: func(pid int) (string, error) { return "42", nil },
	})
	if err == nil {
		t.Fatal("a vanished boundary over a live pair produced no diagnostic")
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the intent kept", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
	}
}

// TestReapGoneMarkerlessBoundaryStaysFenced pins the no-evidence rule: a
// vanished boundary that recorded no pair proves nothing, so the record stays
// fenced rather than clearing on the path's absence.
func TestReapGoneMarkerlessBoundaryStaysFenced(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{
		Open: func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
			return nil, execenv.ErrBoundaryGone
		},
	})
	if err == nil {
		t.Fatal("a vanished marker-less boundary produced no diagnostic")
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the intent kept", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
	}
}

// TestReapRetryResolvesAPreviouslyMarkedRecord pins §7's retry: a second boot
// that now enumerates clean resolves the marked record to `interrupted`,
// clearing the boundary and dropping the intent.
func TestReapRetryResolvesAPreviouslyMarkedRecord(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{members: []execenv.BoundaryMember{{PID: 100, StartToken: "42"}}}
	var opened []execenv.BoundaryIdentity
	if _, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened)}); err != nil {
		t.Fatalf("first reap: %v", err)
	}
	marked, _ := store.Record(record.ID)
	if marked.State != hostops.StateOrphanUnverified {
		t.Fatalf("after the first boot the state = %q, want orphan-unverified", marked.State)
	}

	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(&fakeBoundary{}, &opened)})
	if err != nil {
		t.Fatalf("second reap: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("the second boot dropped %d; want the resolved intent dropped", dropped)
	}
	resolved, _ := store.Record(record.ID)
	if resolved.State != hostops.StateInterrupted {
		t.Fatalf("the resolved state = %q, want interrupted", resolved.State)
	}
	if len(resolved.OrphanBoundary) != 0 {
		t.Fatalf("the resolved record kept its boundary: %s", resolved.OrphanBoundary)
	}
	if len(resolved.PendingSpawns) != 0 {
		t.Fatalf("the resolved record kept its intent: %+v", resolved.PendingSpawns)
	}
	if resolved.Result == nil || resolved.Result.Message != hostops.InterruptedNote {
		t.Fatalf("the resolved result = %+v, want the interrupted note", resolved.Result)
	}
}

// TestReapKeepsIntentsOnATerminalRecordAndReportsIt pins the corner the state
// machine keeps out of reach: this build refuses to terminalize a record with an
// open intent, so only a hand-edited or pre-guard store file carries the shape.
// The pass cannot mark a terminal record, so it keeps the intent open and
// reports it rather than silently dropping the fence.
func TestReapKeepsIntentsOnATerminalRecordAndReportsIt(t *testing.T) {
	store := openLegacyTerminalIntentStore(t)
	record, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatal("the hand-written store did not load its record")
	}
	handle := &fakeBoundary{members: []execenv.BoundaryMember{{PID: 200, StartToken: "7"}}}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened)})
	if err == nil || !strings.Contains(err.Error(), record.ID) {
		t.Fatalf("reap error = %v, want one naming the terminal record", err)
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the intent kept", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateComplete || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the terminal record = %q/%+v, want complete with the intent kept", stored.State, stored.PendingSpawns)
	}
}

// openLegacyTerminalIntentStore writes a store file carrying the shape only a
// hand-edited or pre-guard file can have — a terminal record with an open spawn
// intent — and opens it.
func openLegacyTerminalIntentStore(t *testing.T) *hostops.Store {
	t.Helper()
	dir := t.TempDir()
	path := hostops.StorePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	const body = `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy",` +
		`"state":"complete","generation":7,"incarnationId":"inc-h1",` +
		`"pendingSpawns":[{"nonce":"n1","platform":"linux","cgroupId":"/cg/n1"}],` +
		`"result":{"ok":true,"message":"done"},"createdAt":"2026-09-28T00:00:00Z",` +
		`"updatedAt":"2026-09-28T00:00:00Z","hostRemoved":false,"sequence":1}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	store, err := hostops.Open(path)
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	return store
}

// TestReapUnverifiableBoundaryPathKeepsTheIntent pins the fail-closed arm
// against the real seam: a persisted cgroup path outside any reachable cgroup2
// hierarchy must never read as a vanished (clean) boundary, so the intent is
// kept and the record stays fenced.
func TestReapUnverifiableBoundaryPathKeepsTheIntent(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, hostops.SpawnIntent{
		Nonce: "n1", Platform: hostops.SpawnPlatformLinux, CgroupID: filepath.Join(t.TempDir(), "child"),
	}); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{})
	if err == nil {
		t.Fatal("an unverifiable boundary path produced no diagnostic")
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the intent kept", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
	}
}

// TestReapKeepsFencedWhenThePersistedBoundaryHasNoEntries pins the empty-boundary
// fail-closed rule: a local orphan-unverified record with an empty persisted
// boundary and no intent has nothing to verify, so the pass must never resolve
// it to interrupted on no evidence.
func TestReapKeepsFencedWhenThePersistedBoundaryHasNoEntries(t *testing.T) {
	store := openLegacyEmptyBoundaryStore(t)
	handle := &fakeBoundary{}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened)})
	if err == nil {
		t.Fatal("an entry-less boundary produced no diagnostic")
	}
	if dropped != 0 || len(opened) != 0 {
		t.Fatalf("reap = %d dropped, opened %d; want it left fenced", dropped, len(opened))
	}
	stored, _ := store.Record("00000000000000000001")
	if stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("the record's state = %q, want it still fenced", stored.State)
	}
}

// openLegacyEmptyBoundaryStore writes a store file carrying a local
// orphan-unverified record whose persisted boundary array is empty — a shape the
// API now refuses to write, but a hand-edited or pre-guard file can carry — and
// opens it.
func openLegacyEmptyBoundaryStore(t *testing.T) *hostops.Store {
	t.Helper()
	return openLegacyBoundaryStore(t, "[]")
}

// openLegacyBoundaryStore writes a store file carrying one local
// orphan-unverified record with the given persisted boundary array and opens
// it — a shape the API refuses to write, but a hand-edited or pre-guard file
// can carry.
func openLegacyBoundaryStore(t *testing.T, boundary string) *hostops.Store {
	t.Helper()
	dir := t.TempDir()
	path := hostops.StorePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	body := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy",` +
		`"state":"orphan-unverified","generation":7,"incarnationId":"inc-h1","orphanBoundary":` + boundary + `,` +
		`"createdAt":"2026-09-28T00:00:00Z","updatedAt":"2026-09-28T00:00:00Z","hostRemoved":false}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	store, err := hostops.Open(path)
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	return store
}

// TestReapResolvesACustodyImportedOrphanWithNoIntents pins M1: a local
// `orphan-unverified` record whose intent set is gone (a custody import, or an
// intent-cleared write) is still retried every boot. It must be resolved from
// its persisted boundary entries, never left admission-fenced forever.
func TestReapResolvesACustodyImportedOrphanWithNoIntents(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	// Clear the intent but keep the marked boundary with its recorded pair.
	boundary := []byte(`[{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n1","pid":100,"startTime":"42"}]`)
	if _, err := store.SetOrphanBoundary(record.ID, boundary, []string{"n1"}); err != nil {
		t.Fatalf("SetOrphanBoundary: %v", err)
	}
	stored, _ := store.Record(record.ID)
	if len(stored.PendingSpawns) != 0 || stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("the fixture is wrong: %q/%+v", stored.State, stored.PendingSpawns)
	}

	// The boundary enumerates empty and the recorded pair is gone: it resolves.
	handle := &fakeBoundary{}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, nil)})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("reap dropped %d; want the intent-less record counted as converged", dropped)
	}
	resolved, _ := store.Record(record.ID)
	if resolved.State != hostops.StateInterrupted || len(resolved.OrphanBoundary) != 0 {
		t.Fatalf("the record = %q/%s, want interrupted with its boundary cleared", resolved.State, resolved.OrphanBoundary)
	}
}

// TestReapKeepsAnIntentlessOrphanFencedWhenItsPairIsAlive pins M3 for the
// custody-import shape: the recorded pair is alive outside an empty boundary,
// so the record stays fenced.
func TestReapKeepsAnIntentlessOrphanFencedWhenItsPairIsAlive(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	boundary := []byte(`[{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n1","pid":100,"startTime":"42"}]`)
	if _, err := store.SetOrphanBoundary(record.ID, boundary, []string{"n1"}); err != nil {
		t.Fatalf("SetOrphanBoundary: %v", err)
	}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{
		Open:    openOnce(&fakeBoundary{}, nil),
		Observe: func(pid int) (string, error) { return "42", nil },
	})
	if err == nil {
		t.Fatal("an alive recorded pair produced no diagnostic")
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the record kept fenced", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || string(stored.OrphanBoundary) != string(boundary) {
		t.Fatalf("the record = %q/%s, want it left fenced with its boundary", stored.State, stored.OrphanBoundary)
	}
}

// TestReapKeepsFencedWhenARecordedPairIsAliveOutsideTheBoundary pins M3 for the
// intent shape: a process that migrated out of the cgroup is not a member, so
// membership enumerates empty, but its recorded (pid, start token) pair is
// still alive — the pass must not clear the intent.
func TestReapKeepsFencedWhenARecordedPairIsAliveOutsideTheBoundary(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 100, "42"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{
		Open:    openOnce(handle, &opened),
		Observe: func(pid int) (string, error) { return "42", nil },
	})
	if err == nil {
		t.Fatal("an alive recorded pair produced no diagnostic")
	}
	if dropped != 0 || len(handle.killed) != 0 {
		t.Fatalf("reap = %d dropped, killed %v; want the intent kept", dropped, handle.killed)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
	}
}

// TestReapNonEnforcingBoundaryNeverClears pins the Darwin arm's fail-closed
// rule: where an empty enumeration is not proof (a setsid'd descendant leaves
// the pair), the pass never reads the boundary as clean, never signals, and
// keeps the intent open.
func TestReapNonEnforcingBoundaryNeverClears(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{notEnforcing: true}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened)})
	if err == nil {
		t.Fatal("a non-enforcing boundary produced no diagnostic")
	}
	if dropped != 0 || len(handle.killed) != 0 {
		t.Fatalf("reap = %d dropped, killed %v; want no clear and no signal", dropped, handle.killed)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
	}
}

// TestReapNarrowsAnAlreadyMarkedRecordAndDropsCleanIntents pins the two-group
// regression: a record already in `orphan-unverified` whose boundary holds one
// clean group and one failing group must be narrowed to the failing group and
// have the clean group's intent dropped, in one write. `Transition` refuses a
// same-state move, so this is only correct through SetOrphanBoundary.
func TestReapNarrowsAnAlreadyMarkedRecordAndDropsCleanIntents(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	for _, nonce := range []string{"n1", "n2"} {
		if _, err := store.ArmSpawnIntent(record.ID, linuxIntent(nonce)); err != nil {
			t.Fatalf("ArmSpawnIntent(%s): %v", nonce, err)
		}
	}
	marked := `[{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n1","nonce":"n1"},{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n2","nonce":"n2"}]`
	if _, err := store.SetOrphanBoundary(record.ID, []byte(marked), nil); err != nil {
		t.Fatalf("SetOrphanBoundary: %v", err)
	}

	open := func(id execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
		if strings.HasSuffix(id.CgroupID, "n1") {
			// The first group's boundary is empty: clean.
			return &fakeBoundary{}, nil
		}
		// The second still holds a member no persisted pair accounts for.
		return &fakeBoundary{members: []execenv.BoundaryMember{{PID: 9, StartToken: "1"}}}, nil
	}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: open})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("reap dropped %d; want the clean group's intent dropped", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("the record's state = %q, want it still fenced", stored.State)
	}
	if len(stored.PendingSpawns) != 1 || stored.PendingSpawns[0].Nonce != "n2" {
		t.Fatalf("the intents = %+v, want only n2", stored.PendingSpawns)
	}
	want := `[{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n2","nonce":"n2"}]`
	if string(stored.OrphanBoundary) != want {
		t.Fatalf("orphanBoundary = %s, want the narrowed %s", stored.OrphanBoundary, want)
	}
}

// TestReapRefusesAMarkerlessEntryCarryingBothArms pins the §9 discriminator
// rule: a markerless entry whose platform disagrees with the fields it carries
// is corrupt, so it must fail closed rather than silently enumerate the wrong
// boundary.
func TestReapRefusesAMarkerlessEntryCarryingBothArms(t *testing.T) {
	store := openLegacyBoundaryStore(t, `[{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n1","pgid":2,"sessionId":2,"nonce":"n1"}]`)
	record, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatal("the hand-written store did not load its record")
	}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(&fakeBoundary{}, nil)})
	if err == nil || !strings.Contains(err.Error(), record.ID) {
		t.Fatalf("reap error = %v, want one naming the record", err)
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the record kept fenced", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("the record's state = %q, want it still fenced", stored.State)
	}
}

// TestReapPermanentCloseFailureFailsFast pins the bounded-teardown refinement: a
// non-busy teardown failure is reported at once instead of retrying for the
// whole wait.
func TestReapPermanentCloseFailureFailsFast(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{closeErr: errors.New("operation not permitted")}
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Wait: 10 * time.Second, Open: openOnce(handle, nil)})
	if err == nil {
		t.Fatal("a permanent teardown failure produced no diagnostic")
	}
	if handle.closeCalls != 1 {
		t.Fatalf("a permanent teardown failure closed the boundary %d time(s); want exactly one attempt", handle.closeCalls)
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the intent kept", dropped)
	}
}

// TestReapCloseFailureIsUnsettled pins §3's teardown half: a boundary that does
// not come down is not a clean boundary, so the pass keeps the intent and marks
// the record unverified rather than dropping it over a live directory.
func TestReapCloseFailureIsUnsettled(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{closeErr: syscall.EBUSY}
	var opened []execenv.BoundaryIdentity
	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Wait: 50 * time.Millisecond, Open: openOnce(handle, &opened)})
	if err == nil || !strings.Contains(err.Error(), record.ID) {
		t.Fatalf("reap error = %v, want one naming the record whose boundary did not tear down", err)
	}
	if handle.closeCalls < 2 {
		t.Fatalf("a busy teardown was closed %d time(s); want it retried within the bound", handle.closeCalls)
	}
	if dropped != 0 {
		t.Fatalf("reap dropped %d; want the intent kept over an un-torn boundary", dropped)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != hostops.StateOrphanUnverified || len(stored.PendingSpawns) != 1 {
		t.Fatalf("the record = %q/%+v, want orphan-unverified with the intent kept", stored.State, stored.PendingSpawns)
	}
}

// TestReapLeavesRemoteAndUnavailableBoundariesAlone pins the boundary this pass
// does not own: §4's remote-fencing boundary and §5's boundary-unavailable
// entry are never enumerated or rewritten by the local reap, so its intent
// data can never clobber the proof orphan-resolve needs.
func TestReapLeavesRemoteAndUnavailableBoundariesAlone(t *testing.T) {
	cases := map[string]string{
		"remote fencing":       `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"b1","opSeq":3},"guardEpoch":7,"leaseEntries":[]}]`,
		"boundary unavailable": `[{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"/custody"}]`,
	}
	for name, boundary := range cases {
		t.Run(name, func(t *testing.T) {
			store, _ := newReapStore(t)
			record := newReapRecord(t, store, "h1")
			if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
				t.Fatalf("ArmSpawnIntent: %v", err)
			}
			if _, err := store.Transition(record.ID, hostops.StateOrphanUnverified, func(r *hostops.Record) {
				r.OrphanBoundary = []byte(boundary)
			}); err != nil {
				t.Fatalf("Transition: %v", err)
			}
			handle := &fakeBoundary{}
			var opened []execenv.BoundaryIdentity
			dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened)})
			if err != nil {
				t.Fatalf("ReapLocalOrphanBoundary: %v", err)
			}
			if dropped != 0 || len(opened) != 0 {
				t.Fatalf("the reap enumerated a %s boundary (dropped %d, opened %d)", name, dropped, len(opened))
			}
			stored, _ := store.Record(record.ID)
			if string(stored.OrphanBoundary) != boundary || len(stored.PendingSpawns) != 1 {
				t.Fatalf("the record = %s/%+v, want it untouched", stored.OrphanBoundary, stored.PendingSpawns)
			}
		})
	}
}

// TestReapFailClosedMarkIsDurable pins the durability of the disposition: the
// mark and its boundary are in the store file, not just in memory.
func TestReapFailClosedMarkIsDurable(t *testing.T) {
	store, path := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	handle := &fakeBoundary{members: []execenv.BoundaryMember{{PID: 100, StartToken: "42"}}}
	var opened []execenv.BoundaryIdentity
	if _, err := ReapLocalOrphanBoundary(store, ReapOptions{Open: openOnce(handle, &opened)}); err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for _, want := range []string{`"state":"orphan-unverified"`, `"kind":"local-markerless"`, `"nonce":"n1"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the store file does not carry %s:\n%s", want, raw)
		}
	}
	_ = record
}

// TestReapRealCgroupArmReapsALocalChild drives the whole path on a real kernel
// boundary: a child spawned into the created cgroup is killed through the
// persisted (pid, start token) pair and the intent drops. It skips on hosts
// with no writable cgroup2 subtree, exactly the "where available" the spec
// names.
func TestReapRealCgroupArmReapsALocalChild(t *testing.T) {
	store, _ := newReapStore(t)
	record := newReapRecord(t, store, "h1")
	boundary, err := execenv.CreateBoundary("")
	if errors.Is(err, execenv.ErrBoundaryUnavailable) {
		t.Skipf("no writable cgroup2 subtree: %v", err)
	}
	if err != nil {
		t.Fatalf("CreateBoundary: %v", err)
	}
	identity := boundary.Identity()
	if _, err := store.ArmSpawnIntent(record.ID, hostops.SpawnIntent{
		Nonce: "n1", Platform: hostops.SpawnPlatformLinux, CgroupID: identity.CgroupID,
	}); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	attr, release, err := boundary.SpawnAttr()
	if err != nil {
		t.Fatalf("SpawnAttr: %v", err)
	}
	cmd := realSleeper(t, attr)
	release()
	token, err := boundary.Observe(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", cmd.Process.Pid, token); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	// Reap the child so the kernel can retire its cgroup entry as soon as the
	// boundary kills it; a live-parent zombie otherwise keeps the directory
	// populated.
	waited := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(waited) }()

	dropped, err := ReapLocalOrphanBoundary(store, ReapOptions{Wait: 10 * time.Second})
	if err != nil {
		t.Fatalf("ReapLocalOrphanBoundary: %v", err)
	}
	<-waited
	if dropped != 1 {
		t.Fatalf("reap dropped %d; want the verified intent dropped", dropped)
	}
	if err := cmd.Process.Signal(os.Interrupt); err == nil {
		t.Fatal("the reaped child is still alive")
	}
	stored, _ := store.Record(record.ID)
	if len(stored.PendingSpawns) != 0 || stored.State == hostops.StateOrphanUnverified {
		t.Fatalf("the record = %q/%+v, want the intent dropped", stored.State, stored.PendingSpawns)
	}
}
