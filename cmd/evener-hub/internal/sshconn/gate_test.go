package sshconn

// Per-host gate tests (deploy pipeline 08b §5, §12's "Busy-holder classes" and
// "Per-host gate serialization versus an in-flight Ensure deploy" rows). The
// Manager's per-host lock is the production gate every holder contends on, so
// these tests pin two things: the pipeline's TryAcquire shares the gate the
// Manager's own attach/supervisor/update/remove paths take, and a busy refusal
// renders the holder class the spec names.

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestTryAcquireContendsWithEnsureOnOneGate pins the sharing: a pipeline holder
// that try-acquired the gate stops an Ensure from proceeding past the same
// gate, and the Ensure proceeds the moment the holder releases. The
// beforeHostGate seam proves the Ensure reached the gate without a sleep.
func TestTryAcquireContendsWithEnsureOnOneGate(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	arrived := make(chan struct{})
	m := newTestManager(t, reg, fr, Options{beforeHostGate: func(string) { close(arrived) }})

	release, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	<-arrived
	select {
	case err := <-done:
		t.Fatalf("Ensure completed while the pipeline held the gate: %v", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Ensure after the release: %v", err)
	}
	if !m.Attached("alpha") {
		t.Fatal("Ensure did not attach once the gate was free")
	}
	// The gate is free again: another pipeline holder acquires it.
	second, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire after the attach: %v", err)
	}
	second()
}

// TestTryAcquireBusyRendersTheHolderClasses pins §5's two forms as this
// Manager renders them: an operation holder names its operation id, a plan
// holder renders the typed transient form with no operation reference.
func TestTryAcquireBusyRendersTheHolderClasses(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	m := newTestManager(t, reg, &fakeRunner{}, Options{})

	operation, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderOperation, OperationID: "00000000000000000042"})
	if err != nil {
		t.Fatalf("TryAcquire(operation): %v", err)
	}
	_, err = m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	var busy *hostops.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("busy try-acquire = %T (%v), want *hostops.BusyError", err, err)
	}
	if busy.Holder.Kind != hostops.HolderOperation || busy.Holder.OperationID != "00000000000000000042" {
		t.Fatalf("busy holder = %+v, want the operation holder", busy.Holder)
	}
	if !strings.Contains(busy.Error(), "00000000000000000042") {
		t.Fatalf("busy message %q does not name the operation", busy.Error())
	}
	operation()

	plan, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire(plan): %v", err)
	}
	_, err = m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderOperation, OperationID: "op-1"})
	if !errors.As(err, &busy) {
		t.Fatalf("second busy try-acquire = %T (%v), want *hostops.BusyError", err, err)
	}
	if busy.Holder.Kind != hostops.HolderPlan {
		t.Fatalf("busy holder = %+v, want the plan holder", busy.Holder)
	}
	if !strings.Contains(busy.Error(), "plan in progress") {
		t.Fatalf("busy message %q is not the typed transient form", busy.Error())
	}
	plan()
}

// TestTryAcquireSeesTheManagersOwnHolders pins the manager half of the holder
// classes: while an attach holds the gate (inside its preflight/attach work),
// a pipeline try-acquire reports the manager's attach holder — never a free
// gate and never an invented operation — and the gate frees when the attach
// completes.
func TestTryAcquireSeesTheManagersOwnHolders(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			close(startEntered)
			<-releaseStart
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	arrived := make(chan struct{})
	m := newTestManager(t, reg, fr, Options{beforeHostGate: func(string) { close(arrived) }})
	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	<-arrived
	<-startEntered
	_, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	var busy *hostops.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("try-acquire while the attach held the gate = %T (%v), want *hostops.BusyError", err, err)
	}
	if busy.Holder.Kind != hostops.HolderManager || busy.Holder.Activity != "attach" {
		t.Fatalf("busy holder = %+v, want the manager's attach holder", busy.Holder)
	}
	close(releaseStart)
	if err := <-done; err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	release, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("try-acquire after the attach completed: %v", err)
	}
	release()
}

// TestTryAcquireSerializesWithRemoveHost pins that a mutation contends on the
// same gate: a removal cannot run while a pipeline holder holds the gate, and
// it completes once the holder releases — no half-rebound host and no
// interleaving teardown.
func TestTryAcquireSerializesWithRemoveHost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	m := newTestManager(t, reg, &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}, Options{})

	release, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- m.RemoveHost("alpha") }()
	waitForGateContenders(t, m, "alpha", 2)
	select {
	case err := <-done:
		t.Fatalf("RemoveHost completed while the pipeline held the gate: %v", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("RemoveHost after the release: %v", err)
	}
	if _, ok := reg.Get("alpha"); ok {
		t.Fatal("the removal did not drop the registry entry")
	}
}

// TestTryAcquireRefusesAnEmptyHost pins the fail-closed guard: a gate keyed by
// the empty name would serialize unrelated hosts, so it is refused outright.
func TestTryAcquireRefusesAnEmptyHost(t *testing.T) {
	m := newTestManager(t, testRegistry(t), &fakeRunner{}, Options{})
	if _, err := m.TryAcquire("   ", hostops.Holder{Kind: hostops.HolderPlan}); err == nil {
		t.Fatal("TryAcquire accepted an empty host name")
	}
}

// TestManagerHoldAsPublishesThePromotedHolder pins the production half of the
// promotion affordance: a deploy/restart acquires the Manager's per-host lock
// before its record exists (the pre-record window renders the transient form),
// and promotes the holder to the operation class once the record id is known.
// A promotion against a free gate refuses.
func TestManagerHoldAsPublishesThePromotedHolder(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	m := newTestManager(t, reg, &fakeRunner{}, Options{})

	release, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderOperation})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if _, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan}); err == nil {
		t.Fatal("a held gate admitted a second holder")
	}
	if err := m.HoldAs("alpha", hostops.Holder{Kind: hostops.HolderOperation, OperationID: "00000000000000000042"}); err != nil {
		t.Fatalf("HoldAs: %v", err)
	}
	_, err = m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	var busy *hostops.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("busy after the promotion = %T (%v), want *hostops.BusyError", err, err)
	}
	if busy.Holder.Kind != hostops.HolderOperation || busy.Holder.OperationID != "00000000000000000042" {
		t.Fatalf("busy holder = %+v, want the promoted operation", busy.Holder)
	}
	release()
	if err := m.HoldAs("alpha", hostops.Holder{Kind: hostops.HolderOperation, OperationID: "1"}); !errors.Is(err, hostops.ErrGateNotHeld) {
		t.Fatalf("HoldAs on a free gate = %v, want ErrGateNotHeld", err)
	}
}

// TestAddHostRefusesAHeldGateRatherThanWaiting pins AddHost's try-acquire: the
// hub's Add calls it while holding the process-wide mutation mutex, so it must
// fail fast with the typed busy refusal instead of parking on a gate the
// pipeline took first (the one reverse-order wait that could close a deadlock
// cycle with a plan).
func TestAddHostRefusesAHeldGateRatherThanWaiting(t *testing.T) {
	host := hostreg.Host{Name: "beta", SSH: "beta.example"}
	reg := testRegistry(t)
	m := newTestManager(t, reg, &fakeRunner{}, Options{})

	release, err := m.TryAcquire("beta", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	err = m.AddHost(host)
	var busy *hostops.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("AddHost against a held gate = %T (%v), want *hostops.BusyError", err, err)
	}
	if busy.Holder.Kind != hostops.HolderPlan {
		t.Fatalf("busy holder = %+v, want the plan holder", busy.Holder)
	}
	if _, ok := reg.Get("beta"); ok {
		t.Fatal("an add refused by a held gate inserted the entry anyway")
	}
	release()
	if err := m.AddHost(host); err != nil {
		t.Fatalf("AddHost after the release: %v", err)
	}
	if _, ok := reg.Get("beta"); !ok {
		t.Fatal("the add did not register the entry once the gate was free")
	}
}

// TestEnsureDeployHookRunsUnderTheGateBeforeAnyRemoteWrite pins the Ensure
// path's operation hook (deploy pipeline 08b §6): it is called with the host's
// per-host gate already held and before the deploy step's first remote write,
// and a hook that cannot persist its record refuses the deploy with nothing
// launched.
func TestEnsureDeployHookRunsUnderTheGateBeforeAnyRemoteWrite(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	reg := testRegistry(t, host)
	fr := &fakeRunner{
		runFn: cannedRun(map[string][]byte{
			// The on-disk build and the running hub are an older revision, so the
			// ladder decides to deploy this controller's build.
			"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`),
			"api/health":   []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`),
		}),
		startFn: goodStartFn(t),
	}
	gateHeld := make(chan bool, 1)
	m := newTestManager(t, reg, fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("binary"), 0o755)
		},
	})
	m.SetEnsureDeployHook(func(h hostreg.Host) (func(error), error) {
		_, err := m.TryAcquire(h.Name, hostops.Holder{Kind: hostops.HolderPlan})
		gateHeld <- err != nil
		return nil, errors.New("the operation store is not configured")
	})

	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "operation store is not configured") {
		t.Fatalf("Ensure error = %v, want the hook's refusal surfaced", err)
	}
	if held := <-gateHeld; !held {
		t.Fatal("the Ensure deploy hook ran without the host's gate held")
	}
}

// ensureCommandSighting is one command the Ensure ladder ran, with whether the
// Ensure record was still non-terminal at that moment.
type ensureCommandSighting struct {
	command    string
	recordOpen bool
}

// ensureDeployTestHarness builds the Ensure ladder for a host whose on-disk
// build is this controller's predecessor: the ladder deploys this controller's
// build and then restarts the hub onto it. The hook creates a real record and
// returns its scope plus a finish that transitions the record the way the hub's
// own finish does. restartErr, when non-nil, fails the restart leg's
// `systemctl restart`.
func ensureDeployTestHarness(t *testing.T, restartErr error) (*Manager, *hostops.Store, *string, *[]ensureCommandSighting) {
	t.Helper()
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				return []byte(`{"protocol":"evener-appwire-v4","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v6","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	store := ensureDeployTestStore(t)
	var recordID string
	var seen []ensureCommandSighting
	innerRun := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		command := strings.Join(argv, " ")
		open := false
		if recordID != "" {
			if record, ok := store.Record(recordID); ok {
				open = !record.State.Terminal()
			}
		}
		seen = append(seen, ensureCommandSighting{command: command, recordOpen: open})
		if strings.Contains(command, "systemctl restart") && restartErr != nil {
			return nil, restartErr
		}
		return innerRun(ctx, argv, stdin)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})
	m.SetEnsureDeployHook(func(host hostreg.Host) (func(error), error) {
		record, err := store.Create(hostops.NewRecord{
			ClientOperationID: "ensure-1",
			Host:              host.Name,
			Kind:              hostops.KindDeploy,
			Generation:        host.Generation,
			IncarnationID:     host.IncarnationID,
		})
		if err != nil {
			return nil, err
		}
		recordID = record.ID
		return func(err error) {
			if err == nil {
				_, _ = store.TransitionToState(record.ID, hostops.StateComplete,
					&hostops.Result{OK: true, Message: "Ensure-triggered deploy complete"}, "complete")
				return
			}
			_, _ = store.TransitionToState(record.ID, hostops.StateFailed,
				&hostops.Result{OK: false, Message: err.Error()}, "failed")
		}, nil
	})
	return m, store, &recordID, &seen
}

// ensureDeployTestStore opens the real operation store the Ensure spawn-scope
// tests arm against.
func ensureDeployTestStore(t *testing.T) *hostops.Store {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	return store
}

// restartSightings returns the commands that ran the restart leg.
func restartSightings(seen []ensureCommandSighting) []ensureCommandSighting {
	var out []ensureCommandSighting
	for _, run := range seen {
		if strings.Contains(run.command, "systemctl restart") {
			out = append(out, run)
		}
	}
	return out
}

// TestEnsureDeployKeepsTheRecordOpenThroughTheRestartLeg pins M1: an Ensure
// that deploys and then restarts keeps the deploy record non-terminal through
// the restart leg, and the record completes only after the leg.
func TestEnsureDeployKeepsTheRecordOpenThroughTheRestartLeg(t *testing.T) {
	m, store, recordID, seen := ensureDeployTestHarness(t, nil)

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	restarts := restartSightings(*seen)
	if len(restarts) == 0 {
		t.Fatal("the scenario never reached the restart leg; the pin would be vacuous")
	}
	for _, run := range restarts {
		if !run.recordOpen {
			t.Fatal("the deploy record was already terminal when the restart leg ran")
		}
	}
	record, ok := store.Record(*recordID)
	if !ok {
		t.Fatal("the Ensure record disappeared")
	}
	if record.State != hostops.StateComplete {
		t.Fatalf("record state = %q, want complete after a clean deploy and restart leg", record.State)
	}
}

// TestEnsureRestartLegFailureFailsTheStillOpenRecord pins M1's failure half: a
// restart leg that fails must record the failure on the record the deploy kept
// open — never a `complete` record for an operation that did not finish.
func TestEnsureRestartLegFailureFailsTheStillOpenRecord(t *testing.T) {
	m, store, recordID, seen := ensureDeployTestHarness(t, errors.New("systemctl restart failed"))

	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "systemctl restart failed") {
		t.Fatalf("Ensure error = %v, want the restart leg's failure surfaced", err)
	}
	restarts := restartSightings(*seen)
	if len(restarts) == 0 {
		t.Fatal("the scenario never reached the restart leg; the pin would be vacuous")
	}
	for _, run := range restarts {
		if !run.recordOpen {
			t.Fatal("the record was already terminal when the failing restart leg ran")
		}
	}
	record, ok := store.Record(*recordID)
	if !ok {
		t.Fatal("the Ensure record disappeared")
	}
	if record.State != hostops.StateFailed {
		t.Fatalf("record state = %q, want failed after the restart leg failed", record.State)
	}
	if record.Result == nil || !strings.Contains(record.Result.Message, "systemctl restart failed") {
		t.Fatalf("record result = %+v, want the restart leg's failure verbatim", record.Result)
	}
}

// ensureRestartTestHarness builds the Ensure ladder for a restart-only attempt:
// the on-disk build is this controller's, while the running hub serves a stale
// one, so ensureDecision returns restart without deploy. The restart hook mints
// a real record and returns its scope plus a finish that transitions it the way
// the hub's own finish does.
func ensureRestartTestHarness(t *testing.T, restartErr error) (*Manager, *hostops.Store, *string, *[]ensureCommandSighting) {
	t.Helper()
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(int) ([]byte, error) {
			return []byte(`{"protocol":"` + appwire.ProtocolVersion + `","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(call int) ([]byte, error) {
			// The pre-decision probe sees the stale process; the probes after
			// the restart see the replacement.
			if call == 0 {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	store := ensureDeployTestStore(t)
	var recordID string
	var seen []ensureCommandSighting
	innerRun := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		command := strings.Join(argv, " ")
		open := false
		if recordID != "" {
			if record, ok := store.Record(recordID); ok {
				open = !record.State.Terminal()
			}
		}
		seen = append(seen, ensureCommandSighting{command: command, recordOpen: open})
		if strings.Contains(command, "systemctl restart") && restartErr != nil {
			return nil, restartErr
		}
		return innerRun(ctx, argv, stdin)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha"})
	m.SetEnsureRestartHook(func(host hostreg.Host) (func(error), error) {
		record, err := store.Create(hostops.NewRecord{
			ClientOperationID: "ensure-restart-1",
			Host:              host.Name,
			Kind:              hostops.KindRestart,
			Generation:        host.Generation,
			IncarnationID:     host.IncarnationID,
		})
		if err != nil {
			return nil, err
		}
		recordID = record.ID
		return func(err error) {
			if err == nil {
				_, _ = store.TransitionToState(record.ID, hostops.StateComplete,
					&hostops.Result{OK: true, Message: "Ensure-triggered restart complete"}, "complete")
				return
			}
			_, _ = store.TransitionToState(record.ID, hostops.StateFailed,
				&hostops.Result{OK: false, Message: err.Error()}, "failed")
		}, nil
	})
	return m, store, &recordID, &seen
}

// TestEnsureRestartOnlyMintsRecordAndKeepsItOpen pins the restart-only Ensure
// path's record: a decision with no deploy still mints a durable restart record
// through the hook, keeps the record non-terminal while the leg runs, and
// completes it after.
func TestEnsureRestartOnlyMintsRecordAndKeepsItOpen(t *testing.T) {
	m, store, recordID, seen := ensureRestartTestHarness(t, nil)
	m.SetEnsureDeployHook(func(hostreg.Host) (func(error), error) {
		t.Error("the deploy hook ran for a restart-only decision")
		return nil, errors.New("no deploy expected")
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	restarts := restartSightings(*seen)
	if len(restarts) == 0 {
		t.Fatalf("the scenario never reached the restart leg; sightings: %+v", *seen)
	}
	for _, run := range restarts {
		if !run.recordOpen {
			t.Fatal("the record was already terminal when the leg ran")
		}
	}
	record, ok := store.Record(*recordID)
	if !ok {
		t.Fatal("the restart record disappeared")
	}
	if record.Kind != hostops.KindRestart {
		t.Fatalf("record kind = %q, want restart", record.Kind)
	}
	if record.State != hostops.StateComplete {
		t.Fatalf("record state = %q, want complete after a clean restart-only leg", record.State)
	}
}

// TestEnsureRestartOnlyLegFailureRecordsFailed pins the failure half: a failed
// restart-only leg records `failed` on the record the hook minted, never a
// `complete` record for an operation that did not finish.
func TestEnsureRestartOnlyLegFailureRecordsFailed(t *testing.T) {
	m, store, recordID, seen := ensureRestartTestHarness(t, errors.New("systemctl restart failed"))

	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "systemctl restart failed") {
		t.Fatalf("Ensure error = %v, want the restart leg's failure surfaced", err)
	}
	restarts := restartSightings(*seen)
	if len(restarts) == 0 {
		t.Fatal("the scenario never reached the restart leg; the pin would be vacuous")
	}
	record, ok := store.Record(*recordID)
	if !ok {
		t.Fatal("the restart record disappeared")
	}
	if record.State != hostops.StateFailed {
		t.Fatalf("record state = %q, want failed after the restart-only leg failed", record.State)
	}
	if record.Result == nil || !strings.Contains(record.Result.Message, "systemctl restart failed") {
		t.Fatalf("record result = %+v, want the leg's failure verbatim", record.Result)
	}
}

// TestEnsureRestartOnlyRefusesWithoutARecorder pins §6's rule at the restart
// leg: a restart-only attempt with no durable-record hook wired performs no
// mutating SSH command. It refuses before the restart, while the read-only
// decision probes that ran first (§6 allows probing and staging) are not the
// refusal's business.
func TestEnsureRestartOnlyRefusesWithoutARecorder(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(int) ([]byte, error) {
			return []byte(`{"protocol":"` + appwire.ProtocolVersion + `","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	var commands []string
	innerRun := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		commands = append(commands, strings.Join(argv, " "))
		return innerRun(ctx, argv, stdin)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha"})

	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "no restart recorder wired") {
		t.Fatalf("Ensure error = %v, want the no-recorder refusal", err)
	}
	for _, command := range commands {
		if strings.Contains(command, "systemctl restart") {
			t.Fatalf("the refused attempt ran a mutating restart anyway: %s", command)
		}
	}
	if len(commands) == 0 {
		t.Fatal("the attempt ran no commands at all: the refusal must land at the leg, after the read-only probes §6 allows")
	}
}

// TestEnsureDeployRefusesWithoutARecorder pins the durable-record contract on
// the deploy arm, the twin of the restart-only refusal: a Manager with no
// deploy recorder wired performs no mutating SSH command (§6), so the attempt
// refuses before the push while the read-only decision probes that ran first
// are not the refusal's business.
func TestEnsureDeployRefusesWithoutARecorder(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(int) ([]byte, error) {
			return []byte(`{"protocol":"` + appwire.ProtocolVersion + `","version":"oldsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	var commands []string
	innerRun := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		commands = append(commands, strings.Join(argv, " "))
		return innerRun(ctx, argv, stdin)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: writeStageBinary})

	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "no deploy recorder wired") {
		t.Fatalf("Ensure error = %v, want the no-recorder refusal", err)
	}
	for _, command := range commands {
		if strings.Contains(command, "cat >") || strings.Contains(command, "nohup") {
			t.Fatalf("the refused attempt ran a mutating command anyway: %s", command)
		}
	}
}
