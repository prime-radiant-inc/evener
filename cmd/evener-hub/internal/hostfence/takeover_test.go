package hostfence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// Tests for S18's controller-side fencing sequence: the decision taken from the
// observed guard/lease state, the preemptive takeover through the verified
// handle, the bounded kill/wait over the superseded epoch's lease-tracked work,
// and the typed fencing-failure outcome whose boundary lands on an
// orphan-unverified record. The scripted-remote seam is the real embedded
// helper run locally; the store is the real operation store.

// scriptRunner runs one wrapper command through the embedded helper installed
// into a temp dir, exactly as the remote exec seam will: a POSIX shell with the
// helper's state root in its environment. A cancelled context kills the
// invocation and reports it as the transport failure the ssh seam would.
type scriptRunner struct{ remote *fenceRemote }

func (r scriptRunner) Run(ctx context.Context, command string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), "EVENER_FENCE_STATE="+r.remote.state)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return out.String(), errOut.String(), 0, ctx.Err()
		}
		return out.String(), errOut.String(), exit.ExitCode(), nil
	}
	return out.String(), errOut.String(), 0, nil
}

// wrapperFor addresses the test's installed helper through its scripted remote.
func wrapperFor(remote *fenceRemote) Wrapper {
	return Wrapper{Runner: scriptRunner{remote: remote}, Host: "h1", Path: remote.script}
}

// newFenceStore opens a fresh operation store holding one running deploy record
// for host.
func newFenceStore(t *testing.T, host string) (*hostops.Store, hostops.Record) {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	record, err := store.Create(hostops.NewRecord{
		ClientOperationID: "op-" + host, Host: host, Kind: hostops.KindDeploy,
		Generation: 1, IncarnationID: "inc-" + host,
	})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	running, err := store.Transition(record.ID, hostops.StateRunning, nil)
	if err != nil {
		t.Fatalf("transition running: %v", err)
	}
	return store, running
}

// TestFenceKillsSupersededWorkAndAdvances drives the whole sequence against the
// real helper: a crashed incarnation's live lease entry is signaled under the
// taken-over lease, exit-confirmed, and only then does the guard advance — so a
// mutation under the new epoch lands after the kill/wait, never before.
func TestFenceKillsSupersededWorkAndAdvances(t *testing.T) {
	remote := newFenceRemote(t)
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(old)
	entry := crashPerformLeavingCommand(t, remote, old, nil, "exec sleep 30")
	defer killProcess(*entry.Ownership.PID)
	store, record := newFenceStore(t, "h1")
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	verified, err := wrapperFor(remote).Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	outcome, err := verified.Fence(context.Background(), FenceRequest{
		Epoch: next, RecordID: record.ID, Store: store,
		Deadlines: FencingDeadlines{Kill: 10 * time.Second, Wait: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if outcome.Superseded == nil || *outcome.Superseded != old {
		t.Fatalf("outcome superseded = %+v, want %+v", outcome.Superseded, old)
	}
	if outcome.GuardEpoch != 4 {
		t.Fatalf("outcome guard epoch = %d, want 4 (settle 2, takeover 3, advance 4)", outcome.GuardEpoch)
	}
	if len(outcome.Entries) != 1 || len(outcome.Settled) != 1 {
		t.Fatalf("outcome entries = %+v settled = %+v, want the one superseded entry", outcome.Entries, outcome.Settled)
	}
	if recheck := recheckID(t, remote, entry.ID); recheck.Live {
		t.Fatalf("recheck after Fence = %+v, want not live", recheck)
	}
	if stored, ok := store.Record(record.ID); !ok || stored.State != hostops.StateRunning {
		t.Fatalf("record after a clean fencing = %+v (ok %v), want it untouched", stored, ok)
	}
	// The kill/wait and the advance are what a mutation depends on: it lands now.
	work := t.TempDir()
	result, err := verified.Perform(context.Background(), next, "printf x > "+work+"/sideeffect")
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Perform after Fence = (%+v, %v), want the side effect to land", result, err)
	}
	if _, err := os.Stat(filepath.Join(work, "sideeffect")); err != nil {
		t.Fatalf("the mutation did not land: %v", err)
	}
	if settled := remote.status(); settled.Epoch == nil || *settled.Epoch != next || settled.Fence != nil {
		t.Fatalf("helper guard after Fence = %+v, want %+v settled", settled, next)
	}
}

// TestFenceTimeoutPersistsQuarantineAndThenConverges pins §4:107 and §10:189:
// a kill/wait timeout fails the operation with the typed fencing-failure
// outcome on an orphan-unverified record, persists the per-host
// quarantine marker and the timed-out epoch's remote-fencing boundary in the
// same write, never advances the guard, and leaves the host closable-but-
// convergent: the next operation's fresh epoch supersedes the timed-out fence,
// kill/waits, and advances.
func TestFenceTimeoutPersistsQuarantineAndThenConverges(t *testing.T) {
	remote := newFenceRemote(t)
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(old)
	entry := crashPerformLeavingCommand(t, remote, old, nil, `trap '' TERM; while :; do sleep 0.5; done`)
	defer killProcess(*entry.Ownership.PID)
	store, record := newFenceStore(t, "h1")
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	verified, err := wrapperFor(remote).Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	_, err = verified.Fence(context.Background(), FenceRequest{
		Epoch: next, RecordID: record.ID, Store: store,
		Deadlines: FencingDeadlines{Kill: 5 * time.Second, Wait: 300 * time.Millisecond},
	})
	var timeout *FencingTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Fence(stuck orphan) = %v, want *FencingTimeoutError", err)
	}
	if !errors.Is(err, ErrFencingFailure) || timeout.Discriminator != DiscriminatorFencingFailure {
		t.Fatalf("timeout error = %v (discriminator %q), want the %s class", err, timeout.Discriminator, DiscriminatorFencingFailure)
	}
	if timeout.Host != "h1" || timeout.Epoch != next {
		t.Fatalf("timeout names host %q epoch %+v, want h1 and %+v", timeout.Host, timeout.Epoch, next)
	}
	if timeout.PersistErr != nil {
		t.Fatalf("timeout persistence failed: %v", timeout.PersistErr)
	}
	if timeout.Record == nil || timeout.Record.State != hostops.StateOrphanUnverified {
		t.Fatalf("timeout record = %+v, want the persisted orphan-unverified record", timeout.Record)
	}
	// The record carries the remote-fencing boundary §9 defines: the timed-out
	// epoch, the guard-file epoch, and the superseded entry with its ownership.
	stored, ok := store.Record(record.ID)
	if !ok || stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("stored record = %+v (ok %v), want orphan-unverified", stored, ok)
	}
	var boundary []RemoteFencingBoundary
	if err := json.Unmarshal(stored.OrphanBoundary, &boundary); err != nil {
		t.Fatalf("unmarshal boundary %s: %v", stored.OrphanBoundary, err)
	}
	if len(boundary) != 1 || boundary[0].Kind != RemoteFencingBoundaryKind {
		t.Fatalf("boundary = %+v, want one %s entry", boundary, RemoteFencingBoundaryKind)
	}
	if boundary[0].FencingEpoch != next || boundary[0].GuardEpoch != 3 {
		t.Fatalf("boundary epochs = (%+v, %d), want (%+v, 3)", boundary[0].FencingEpoch, boundary[0].GuardEpoch, next)
	}
	if len(boundary[0].LeaseEntries) != 1 || boundary[0].LeaseEntries[0].Ownership.Kind() != OwnershipPID {
		t.Fatalf("boundary lease entries = %+v, want the stuck entry's pid ownership", boundary[0].LeaseEntries)
	}
	if boundary[0].LeaseEntries[0].Ownership.PID == nil || *boundary[0].LeaseEntries[0].Ownership.PID != *entry.Ownership.PID {
		t.Fatalf("boundary ownership = %+v, want the tracked instance %+v", boundary[0].LeaseEntries[0].Ownership, entry.Ownership)
	}
	marker, ok := store.FencingQuarantine("h1")
	if !ok || marker.RecordID != record.ID {
		t.Fatalf("FencingQuarantine(h1) = (%+v, %v), want the marker naming %s", marker, ok, record.ID)
	}
	// The guard did not advance: the remote is still fenced on the timed-out
	// epoch, and the stuck instance is still alive.
	if pending := remote.status(); pending.Fence == nil || pending.Fence.Epoch != next {
		t.Fatalf("helper guard after timeout = %+v, want %+v's fence still pending", pending, next)
	}
	if !processAlive(t, *entry.Ownership.PID) {
		t.Fatal("the process that cannot be proven dead is already gone")
	}
	// The operator confirms the member out-of-band; the next operation's fresh
	// epoch supersedes the timed-out fence and converges the fencing.
	killProcess(*entry.Ownership.PID)
	store2, record2 := newFenceStore(t, "h1")
	fresh := Epoch{BootID: "boot-1", OpSeq: 3}
	outcome, err := verified.Fence(context.Background(), FenceRequest{
		Epoch: fresh, RecordID: record2.ID, Store: store2,
		Deadlines: FencingDeadlines{Kill: 10 * time.Second, Wait: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("converging Fence = %v", err)
	}
	if outcome.Superseded == nil || *outcome.Superseded != next {
		t.Fatalf("converging superseded = %+v, want the timed-out epoch %+v", outcome.Superseded, next)
	}
	if settled := remote.status(); settled.Epoch == nil || *settled.Epoch != fresh || settled.Fence != nil {
		t.Fatalf("helper guard after convergence = %+v, want %+v settled", settled, fresh)
	}
	if recheck := recheckID(t, remote, entry.ID); recheck.Live {
		t.Fatalf("entry after convergence = %+v, want it settled", recheck)
	}
}

// cannedRunner answers the wrapper protocol from a test's script: one response
// per invocation, so a test pins the exact command sequence the worker issues.
type cannedRunner struct {
	t        *testing.T
	commands []string
	respond  func(command string) (string, string, int)
}

func (r *cannedRunner) Run(_ context.Context, command string) (string, string, int, error) {
	r.commands = append(r.commands, command)
	stdout, stderr, exit := r.respond(command)
	return stdout, stderr, exit, nil
}

// statusJSON renders one helper status from a guard state and an entry count.
func statusJSON(t *testing.T, guard GuardState, entries int) string {
	t.Helper()
	raw, err := json.Marshal(guard)
	if err != nil {
		t.Fatalf("marshal guard: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal guard map: %v", err)
	}
	fields["entries"] = json.RawMessage(strconv.Itoa(entries))
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	return string(out)
}

// plannedGuard is the guard state a takeover of next over settled is expected
// to land, computed through the same controller-side rules the worker checks
// the helper against.
func plannedGuard(t *testing.T, settled GuardState, next Epoch) GuardState {
	t.Helper()
	planned, err := settled.Takeover(next)
	if err != nil {
		t.Fatalf("plan takeover: %v", err)
	}
	return planned
}

// TestFenceRefusesStaleFromObservedStateWithoutMutating pins the decision half:
// a state the controller's own rules refuse — here an epoch the guard already
// superseded — aborts with the typed refusal before any mutating remote step.
func TestFenceRefusesStaleFromObservedStateWithoutMutating(t *testing.T) {
	stale := Epoch{BootID: "boot-1", OpSeq: 2}
	holder := Epoch{BootID: "boot-1", OpSeq: 3}
	observed := GuardState{
		Version: 1, GuardEpoch: 4, Epoch: &holder, Holder: &holder, Superseded: &stale,
		BootHighWater: map[string]uint64{"boot-1": 3},
	}
	runner := &cannedRunner{t: t, respond: func(command string) (string, string, int) {
		return statusJSON(t, observed, 0), "", 0
	}}
	store := &recordingQuarantineStore{}
	_, err := (Verified{wrapper: Wrapper{Runner: runner, Host: "h1"}}).Fence(context.Background(), FenceRequest{
		Epoch: stale, RecordID: "00000000000000000001", Store: store,
	})
	if !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Fence(superseded epoch) = %v, want ErrStaleEpoch", err)
	}
	if len(runner.commands) != 1 || !strings.Contains(runner.commands[0], "status") {
		t.Fatalf("Fence(stale) issued %q, want only the read-only observation", runner.commands)
	}
	if store.boundary != nil {
		t.Fatal("a refused fencing persisted a quarantine boundary")
	}
}

// TestGuardTakeoverSupersedesAPendingFence pins the extension the convergence
// path stands on: a crashed incarnation's pending fence is superseded by the
// next epoch, while an epoch the guard already retired or one not newer than
// the fence's epoch within its own boot still refuses stale.
func TestGuardTakeoverSupersedesAPendingFence(t *testing.T) {
	first := Epoch{BootID: "boot-1", OpSeq: 1}
	second := Epoch{BootID: "boot-1", OpSeq: 2}
	pending := GuardState{
		Version: 1, GuardEpoch: 2, Holder: &first,
		Fence:         &FenceState{Epoch: first, GuardEpoch: 2},
		BootHighWater: map[string]uint64{"boot-1": 1},
	}
	next, err := pending.Takeover(second)
	if err != nil {
		t.Fatalf("Takeover over a pending fence = %v, want success", err)
	}
	if next.Fence == nil || next.Fence.Epoch != second || next.Fence.Superseded == nil || *next.Fence.Superseded != first {
		t.Fatalf("fence after the supersede = %+v, want %+v superseding %+v", next.Fence, second, first)
	}
	if next.Holder == nil || *next.Holder != second || next.GuardEpoch != pending.GuardEpoch+1 {
		t.Fatalf("holder/sequence after the supersede = %+v/%d", next.Holder, next.GuardEpoch)
	}
	if next.Superseded == nil || *next.Superseded != first {
		t.Fatalf("superseded after the supersede = %+v, want %+v", next.Superseded, first)
	}
	if _, err := next.Takeover(first); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Takeover of the superseded epoch = %v, want ErrStaleEpoch", err)
	}
	// A lower same-boot epoch never supersedes the fence's epoch.
	held := GuardState{
		Version: 1, GuardEpoch: 2, Holder: &second,
		Fence:         &FenceState{Epoch: second, GuardEpoch: 2},
		BootHighWater: map[string]uint64{"boot-1": 2},
	}
	if _, err := held.Takeover(first); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Takeover(lower epoch) over a pending fence = %v, want ErrStaleEpoch", err)
	}
	// A later boot's epoch supersedes it: the guard sequence is the cross-boot
	// order, never the pair alone.
	if _, err := held.Takeover(Epoch{BootID: "boot-2", OpSeq: 1}); err != nil {
		t.Fatalf("Takeover(later boot) over a pending fence = %v, want success", err)
	}
}

// TestFenceRefusesADisagreeingHelperState pins the worker's own record agreeing
// with the helper's guard: a reported fence the controller's rules do not
// produce aborts the operation with no kill/wait and no advance.
func TestFenceRefusesADisagreeingHelperState(t *testing.T) {
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	settled := GuardState{
		Version: 1, GuardEpoch: 2, Epoch: &old, Holder: &old,
		BootHighWater: map[string]uint64{"boot-1": 1},
	}
	agreed := statusJSON(t, plannedGuard(t, settled, next), 0)
	// The helper reports a fence for another epoch: the worker's planned
	// supersede and the helper's landed state disagree.
	other := Epoch{BootID: "boot-1", OpSeq: 5}
	disagreeing := plannedGuard(t, settled, next)
	disagreeing.Fence = &FenceState{Epoch: other, Superseded: &old, GuardEpoch: disagreeing.GuardEpoch}
	runner := &cannedRunner{t: t, respond: func(command string) (string, string, int) {
		switch {
		case strings.Contains(command, " status"):
			return statusJSON(t, settled, 0), "", 0
		case strings.Contains(command, " takeover"):
			return statusJSON(t, disagreeing, 1), "", 0
		default:
			return agreed, "", 0
		}
	}}
	store := &recordingQuarantineStore{}
	_, err := (Verified{wrapper: Wrapper{Runner: runner, Host: "h1"}}).Fence(context.Background(), FenceRequest{
		Epoch: next, RecordID: "00000000000000000001", Store: store,
	})
	if !errors.Is(err, ErrFenceDisagreement) {
		t.Fatalf("Fence(disagreeing helper) = %v, want ErrFenceDisagreement", err)
	}
	for _, command := range runner.commands[2:] {
		if strings.Contains(command, "perform") || strings.Contains(command, " advance") ||
			strings.Contains(command, " kill") || strings.Contains(command, " entries") {
			t.Fatalf("a disagreeing state still issued %q", command)
		}
	}
	if store.boundary != nil {
		t.Fatal("a disagreeing fencing persisted a quarantine boundary")
	}
}

// TestFencePropagatesTypedKillRefusal pins the trust boundary: a helper refusal
// during the kill/wait classifies as its typed sentinel and aborts the
// operation before the advance, never as a generic transport error.
func TestFencePropagatesTypedKillRefusal(t *testing.T) {
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	settled := GuardState{
		Version: 1, GuardEpoch: 2, Epoch: &old, Holder: &old,
		BootHighWater: map[string]uint64{"boot-1": 1},
	}
	entries := `{"version":1,"entries":[{"id":"n1","command":"deploy","registeredAt":"2026-09-28T00:00:00Z",` +
		`"ownership":{"pid":41,"pidStartTime":"777"},"state":"running"}]}`
	runner := &cannedRunner{t: t, respond: func(command string) (string, string, int) {
		switch {
		case strings.Contains(command, " status"):
			return statusJSON(t, settled, 0), "", 0
		case strings.Contains(command, " takeover"):
			return statusJSON(t, plannedGuard(t, settled, next), 1), "", 0
		case strings.Contains(command, " entries"):
			return entries, "", 0
		case strings.Contains(command, " kill"):
			return "", RefusalPrefix + `{"version":1,"refused":true,"error":"state-corrupt","detail":"entry is corrupt"}`, HelperExitIO
		default:
			t.Fatalf("unexpected command %q", command)
			return "", "", 0
		}
	}}
	store := &recordingQuarantineStore{}
	_, err := (Verified{wrapper: Wrapper{Runner: runner, Host: "h1"}}).Fence(context.Background(), FenceRequest{
		Epoch: next, RecordID: "00000000000000000001", Store: store,
	})
	if !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("Fence(refusing kill) = %v, want ErrStateCorrupt", err)
	}
	for _, command := range runner.commands {
		if strings.Contains(command, " advance") {
			t.Fatalf("a refused kill/wait still issued %q", command)
		}
	}
	if store.boundary != nil {
		t.Fatal("a refused kill/wait persisted a quarantine boundary")
	}
}

// TestFenceTimeoutWithFailingPersistenceStillFailsTyped pins the fail-closed
// report: when the orphan-unverified write itself fails, the fencing failure
// still surfaces typed, with the persistence error kept as evidence.
func TestFenceTimeoutWithFailingPersistenceStillFailsTyped(t *testing.T) {
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	settled := GuardState{
		Version: 1, GuardEpoch: 2, Epoch: &old, Holder: &old,
		BootHighWater: map[string]uint64{"boot-1": 1},
	}
	entries := `{"version":1,"entries":[{"id":"n1","command":"deploy","registeredAt":"2026-09-28T00:00:00Z",` +
		`"ownership":{"pid":41,"pidStartTime":"777"},"state":"running"}]}`
	kill := `{"version":1,"id":"n1","signaled":true,"live":true,"state":"running","remaining":[{"pid":41,"startToken":"777"}]}`
	recheck := `{"version":1,"id":"n1","live":true,"state":"running","ownership":{"pid":41,"pidStartTime":"777"}}`
	runner := &cannedRunner{t: t, respond: func(command string) (string, string, int) {
		switch {
		case strings.Contains(command, " status"):
			return statusJSON(t, settled, 0), "", 0
		case strings.Contains(command, " takeover"):
			return statusJSON(t, plannedGuard(t, settled, next), 1), "", 0
		case strings.Contains(command, " entries"):
			return entries, "", 0
		case strings.Contains(command, " kill"):
			return kill, "", 0
		case strings.Contains(command, " recheck"):
			return recheck, "", 0
		default:
			t.Fatalf("unexpected command %q", command)
			return "", "", 0
		}
	}}
	store := &recordingQuarantineStore{err: errors.New("store is closed")}
	_, err := (Verified{wrapper: Wrapper{Runner: runner, Host: "h1"}}).Fence(context.Background(), FenceRequest{
		Epoch: next, RecordID: "00000000000000000001", Store: store,
		Deadlines: FencingDeadlines{Kill: time.Second, Wait: 100 * time.Millisecond},
	})
	var timeout *FencingTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Fence = %v, want *FencingTimeoutError", err)
	}
	if timeout.PersistErr == nil || timeout.Record != nil {
		t.Fatalf("timeout persistence = (%v, %+v), want the failure kept as evidence", timeout.PersistErr, timeout.Record)
	}
	if !errors.Is(err, ErrFencingFailure) {
		t.Fatalf("Fence = %v, want it in the fencing-failure class", err)
	}
}

// recordingQuarantineStore is the operation-store seam the worker persists a
// timeout through, faked so a unit test can read exactly what it was handed.
type recordingQuarantineStore struct {
	boundary json.RawMessage
	record   hostops.Record
	err      error
}

func (s *recordingQuarantineStore) QuarantineFencing(recordID string, boundary json.RawMessage) (hostops.Record, error) {
	s.boundary = boundary
	if s.err != nil {
		return hostops.Record{}, s.err
	}
	s.record = hostops.Record{
		ID: recordID, Host: "h1", Kind: hostops.KindDeploy, State: hostops.StateOrphanUnverified,
		Generation: 1, IncarnationID: "inc-h1", OrphanBoundary: boundary,
	}
	return s.record, nil
}

// ctxCancelRunner answers the read and kill steps, then blocks in the wait's
// recheck until the caller's context ends.
type ctxCancelRunner struct {
	t       *testing.T
	settled GuardState
	next    Epoch
}

func (r *ctxCancelRunner) Run(ctx context.Context, command string) (string, string, int, error) {
	switch {
	case strings.Contains(command, " status"):
		return statusJSON(r.t, r.settled, 0), "", 0, nil
	case strings.Contains(command, " takeover"):
		return statusJSON(r.t, plannedGuard(r.t, r.settled, r.next), 1), "", 0, nil
	case strings.Contains(command, " entries"):
		return `{"version":1,"entries":[{"id":"n1","command":"deploy","registeredAt":"2026-09-28T00:00:00Z",` +
			`"ownership":{"pid":41,"pidStartTime":"777"},"state":"running"}]}`, "", 0, nil
	case strings.Contains(command, " kill"):
		return `{"version":1,"id":"n1","signaled":true,"live":true,"state":"running","remaining":[{"pid":41,"startToken":"777"}]}`, "", 0, nil
	case strings.Contains(command, " recheck"):
		<-ctx.Done()
		return "", "", 0, ctx.Err()
	default:
		r.t.Fatalf("unexpected command %q", command)
		return "", "", 0, nil
	}
}

// TestFenceCallerCancellationNeverQuarantines pins the deadline family's edge:
// only this worker's own kill/wait bounds quarantine. A caller's context ending
// aborts the fencing without a record, because the remote fence stays pending
// and a retry converges it — the alternative would close a host for a shutdown.
func TestFenceCallerCancellationNeverQuarantines(t *testing.T) {
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	settled := GuardState{
		Version: 1, GuardEpoch: 2, Epoch: &old, Holder: &old,
		BootHighWater: map[string]uint64{"boot-1": 1},
	}
	runner := &ctxCancelRunner{t: t, settled: settled, next: next}
	store := &recordingQuarantineStore{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := (Verified{wrapper: Wrapper{Runner: runner, Host: "h1"}}).Fence(ctx, FenceRequest{
		Epoch: next, RecordID: "00000000000000000001", Store: store,
		Deadlines: FencingDeadlines{Kill: time.Second, Wait: time.Second},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Fence(canceled caller) = %v, want context.Canceled", err)
	}
	if store.boundary != nil {
		t.Fatal("a caller cancellation persisted a quarantine boundary")
	}
}

// TestFencingDeadlinesDefaults pins the shipped deadline family: a zero value
// takes the documented default, and an explicit one is never overridden.
func TestFencingDeadlinesDefaults(t *testing.T) {
	got := FencingDeadlines{}.withDefaults()
	if got.Kill != DefaultFencingKillDeadline || got.Wait != DefaultFencingWaitDeadline {
		t.Fatalf("zero deadlines = %+v, want %v/%v", got, DefaultFencingKillDeadline, DefaultFencingWaitDeadline)
	}
	explicit := FencingDeadlines{Kill: time.Second, Wait: 2 * time.Second}.withDefaults()
	if explicit.Kill != time.Second || explicit.Wait != 2*time.Second {
		t.Fatalf("explicit deadlines = %+v, want them untouched", explicit)
	}
}

// TestRemoteFencingBoundaryShape pins the exact §9 wire shape the store
// persists: one `remote-fencing` member carrying the timed-out epoch, the
// guard-file epoch, and the lease-tracked entries with their ownership.
func TestRemoteFencingBoundaryShape(t *testing.T) {
	pid := 41
	boundary := NewRemoteFencingBoundary(
		Epoch{BootID: "boot-1", OpSeq: 2}, 3,
		[]LeaseRef{{
			Command: "deploy --now", RegisteredAt: "2026-09-28T10:00:00Z",
			Ownership: Ownership{PID: &pid, PIDStartTime: "777"},
		}},
	)
	raw, err := boundary.BoundaryArray()
	if err != nil {
		t.Fatalf("BoundaryArray: %v", err)
	}
	want := `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
		`"leaseEntries":[{"command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z","ownership":{"pid":41,"pidStartTime":"777"}}]}]`
	if string(raw) != want {
		t.Fatalf("BoundaryArray = %s, want %s", raw, want)
	}
	if err := boundary.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for name, broken := range map[string]RemoteFencingBoundary{
		"wrong kind":     {Kind: "local-linux", FencingEpoch: Epoch{BootID: "b", OpSeq: 1}, GuardEpoch: 1},
		"no epoch":       {Kind: RemoteFencingBoundaryKind, GuardEpoch: 1},
		"no guard epoch": {Kind: RemoteFencingBoundaryKind, FencingEpoch: Epoch{BootID: "b", OpSeq: 1}},
		"bad ref": {Kind: RemoteFencingBoundaryKind, FencingEpoch: Epoch{BootID: "b", OpSeq: 1}, GuardEpoch: 1,
			LeaseEntries: []LeaseRef{{Command: "", RegisteredAt: "t", Ownership: Ownership{Nonce: "n"}}}},
	} {
		if err := broken.Validate(); err == nil {
			t.Errorf("%s: Validate = nil, want refusal", name)
		}
		if _, err := broken.BoundaryArray(); err == nil {
			t.Errorf("%s: BoundaryArray = nil error, want refusal", name)
		}
	}
}
