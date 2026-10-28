package hub

// Lock-balance characterization for the host mutations. Add, Update and Remove
// each take the mutation mutex (cfg.mu), the per-host gate (Update and Remove)
// and the in-flight mark across several windows, and every early return has to
// leave all three as it found them. A leaked mutex deadlocks the next mutation
// and a double unlock aborts the process, so each exit path is driven here and
// the lock state is read back afterwards. The window tests pin where each lock
// is held while the mutation runs.

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// balanceExit is one exit path of one mutation: run drives the mutation down
// that path and returns the manager plus the host the mutation named. marked
// says the exit is expected to leave the in-flight mark set (only the conflict
// refusals do, because the test itself set the mark).
type balanceExit struct {
	name   string
	marked bool
	run    func(t *testing.T) (*hubHostManager, string)
}

// assertMutationBalanced reads back everything a finished mutation must have
// released: the mutation mutex, the per-host gate and the in-flight mark.
func assertMutationBalanced(t *testing.T, m *hubHostManager, name string, marked bool) {
	t.Helper()
	if !m.cfg.mu.TryLock() {
		t.Fatal("the mutation left the mutation mutex held")
	}
	if got := m.isMutating(name); got != marked {
		t.Errorf("isMutating(%q) = %v after the mutation, want %v", name, got, marked)
	}
	m.cfg.mu.Unlock()
	release, err := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "probe"})
	if err != nil {
		t.Fatalf("the mutation left the host gate held: %v", err)
	}
	release()
}

func balanceManager(t *testing.T, seed ...string) *hubHostManager {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	hosts := make([]hostreg.Host, 0, len(seed))
	for _, name := range seed {
		hosts = append(hosts, hostreg.Host{Name: name, SSH: name + ".example"})
	}
	if err := writeHubTOMLHosts(configPath, hosts); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	return bootHostManager(t, configPath)
}

func markInFlight(m *hubHostManager, name string) {
	m.cfg.mu.Lock()
	m.markMutating(name)
	m.cfg.mu.Unlock()
}

// openUpdateRemnant leaves an open teardown remnant fencing name while the host
// stays live.
func openUpdateRemnant(t *testing.T, m *hubHostManager, name string) {
	t.Helper()
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected rebind failure") }
	defer func() { m.testOnlyTeardown = nil }()
	result, err := m.UpdateResult(context.Background(), updateRequest(t, m, name, appwire.HostEntry{Address: "fenced.example"}))
	if err != nil || result.HostMutationTeardownFailure == nil {
		t.Fatalf("Update = (%+v, %v), want the teardown-failure arm that opens the remnant", result, err)
	}
}

// openRemoveRemnant leaves an open teardown remnant fencing a removed name.
func openRemoveRemnant(t *testing.T, m *hubHostManager, name string) {
	t.Helper()
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	defer func() { m.testOnlyTeardown = nil }()
	result, err := m.RemoveResult(context.Background(), removeRequest(t, m, name))
	if err != nil || result.HostMutationTeardownFailureRemoved == nil {
		t.Fatalf("Remove = (%+v, %v), want the teardown-failure arm that opens the remnant", result, err)
	}
}

func failTeardown(m *hubHostManager) {
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
}

// handEditOnce rewrites hub.toml to hold only the host name the first time it
// is called, the way an operator's edit lands between a commit's staged write
// and its finalizing write.
func handEditOnce(t *testing.T, configPath, name string) func(string) {
	t.Helper()
	done := false
	return func(string) {
		if done {
			return
		}
		done = true
		if err := os.WriteFile(configPath, []byte("[[hosts]]\nname = \""+name+"\"\nssh = \"hand.example\"\n"), 0o600); err != nil {
			t.Errorf("hand-edit hub.toml: %v", err)
		}
	}
}

func requireRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the mutation succeeded, want a refusal")
	}
}

func TestAddResultLeavesTheLockStateBalancedOnEveryExit(t *testing.T) {
	add := func(name string) appwire.HostAddParams {
		return appwire.HostAddParams{Entry: appwire.HostEntry{Name: name, Address: name + ".example"}}
	}
	exits := []balanceExit{
		{name: "in-flight conflict", marked: true, run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			markInFlight(m, "new")
			_, err := m.AddResult(context.Background(), add("new"))
			requireRefusal(t, err)
			return m, "new"
		}},
		{name: "remnant fence", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			openRemoveRemnant(t, m, "side")
			_, err := m.AddResult(context.Background(), add("side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "keyless ambiguous", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			result, err := m.AddResult(context.Background(), add("side"))
			if err != nil || result.HostMutationAmbiguous == nil {
				t.Fatalf("Add = (%+v, %v), want the ambiguous arm", result, err)
			}
			return m, "side"
		}},
		{name: "duplicate", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			params := appwire.HostAddParams{
				Entry:      appwire.HostEntry{Name: "side", Address: "elsewhere.example"},
				MutationID: newTestMutationID(),
			}
			_, err := m.AddResult(context.Background(), params)
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "generation counter exhausted", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			m.cfg.hosts.SeedHighWater(map[string]hostreg.HighWater{"other": {Generation: math.MaxUint64}})
			_, err := m.AddResult(context.Background(), add("new"))
			requireRefusal(t, err)
			return m, "new"
		}},
		{name: "registry refuses the entry after the staged write", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			m.testOnlyAfterStage = func(name string) {
				if err := m.cfg.hosts.Add(hostreg.Host{Name: name, SSH: "raced.example"}); err != nil {
					t.Errorf("race the registry: %v", err)
				}
			}
			_, err := m.AddResult(context.Background(), add("new"))
			requireRefusal(t, err)
			return m, "new"
		}},
		{name: "staged write fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 1 })
			_, err := m.AddResult(context.Background(), add("new"))
			requireRefusal(t, err)
			return m, "new"
		}},
		{name: "finalizing write fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 2 })
			_, err := m.AddResult(context.Background(), add("new"))
			requireRefusal(t, err)
			return m, "new"
		}},
		{name: "committed", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			result, err := m.AddResult(context.Background(), add("new"))
			if err != nil || result.HostMutationCommitted == nil {
				t.Fatalf("Add = (%+v, %v), want the committed arm", result, err)
			}
			return m, "new"
		}},
		{name: "collision dropped", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			m.testOnlyAfterStage = handEditOnce(t, m.cfg.configPath, "hand")
			result, err := m.AddResult(context.Background(), add("new"))
			if err != nil || result.HostMutationCollisionDropped == nil {
				t.Fatalf("Add = (%+v, %v), want the collision-dropped arm", result, err)
			}
			return m, "new"
		}},
	}
	runBalanceExits(t, exits)
}

func TestUpdateResultLeavesTheLockStateBalancedOnEveryExit(t *testing.T) {
	ctx := context.Background()
	edit := appwire.HostEntry{Address: "edited.example"}
	exits := []balanceExit{
		{name: "in-flight conflict", marked: true, run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			markInFlight(m, "side")
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "remnant fence", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			openUpdateRemnant(t, m, "side")
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "unknown host", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			_, err := m.UpdateResult(ctx, updateRequestFor("ghost", 1, "inc", edit))
			requireRefusal(t, err)
			return m, "ghost"
		}},
		{name: "stale identity", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			generation, incarnation := testMutationIdentity(t, m, "side")
			_, err := m.UpdateResult(ctx, updateRequestFor("side", generation+7, incarnation, edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "invalid entry", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", appwire.HostEntry{}))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "generation counter exhausted", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			m.cfg.hosts.SeedHighWater(map[string]hostreg.HighWater{"side": {Generation: math.MaxUint64}})
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "staged write fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 1 })
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "runtime flip fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 2 })
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "teardown failure", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			failTeardown(m)
			result, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			if err != nil || result.HostMutationTeardownFailure == nil {
				t.Fatalf("Update = (%+v, %v), want the teardown-failure arm", result, err)
			}
			return m, "side"
		}},
		{name: "teardown failure then finalizing write fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			failTeardown(m)
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 3 })
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "entry vanished during the rebind", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			m.testOnlyTeardown = func(_ context.Context, name string) error { return m.cfg.hosts.Remove(name) }
			result, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			if err != nil || result.HostMutationTeardownFailure == nil {
				t.Fatalf("Update = (%+v, %v), want the teardown-failure arm", result, err)
			}
			return m, "side"
		}},
		{name: "committed with finalizing write failing", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 3 })
			_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "committed", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			result, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			if err != nil || result.HostMutationCommitted == nil {
				t.Fatalf("Update = (%+v, %v), want the committed arm", result, err)
			}
			return m, "side"
		}},
		{name: "committed with changed roots", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			rooted := appwire.HostEntry{Address: "side.example", Roots: []string{"/work/a"}}
			result, err := m.UpdateResult(ctx, updateRequest(t, m, "side", rooted))
			if err != nil || result.HostMutationCommitted == nil {
				t.Fatalf("Update = (%+v, %v), want the committed arm", result, err)
			}
			return m, "side"
		}},
		{name: "collision dropped", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			m.testOnlyParkPostCommit = handEditOnce(t, m.cfg.configPath, "hand")
			result, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			if err != nil || result.HostMutationCollisionDropped == nil {
				t.Fatalf("Update = (%+v, %v), want the collision-dropped arm", result, err)
			}
			return m, "side"
		}},
		{name: "teardown failure with collision dropped", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			failTeardown(m)
			m.testOnlyParkPostCommit = handEditOnce(t, m.cfg.configPath, "hand")
			result, err := m.UpdateResult(ctx, updateRequest(t, m, "side", edit))
			if err != nil || result.HostMutationCollisionDropped == nil {
				t.Fatalf("Update = (%+v, %v), want the collision-dropped arm", result, err)
			}
			return m, "side"
		}},
	}
	runBalanceExits(t, exits)
}

func TestRemoveResultLeavesTheLockStateBalancedOnEveryExit(t *testing.T) {
	ctx := context.Background()
	exits := []balanceExit{
		{name: "in-flight conflict", marked: true, run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			markInFlight(m, "side")
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "remnant fence", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			openUpdateRemnant(t, m, "side")
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "unknown host", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t)
			_, err := m.RemoveResult(ctx, removeRequestFor("ghost", 1, "inc"))
			requireRefusal(t, err)
			return m, "ghost"
		}},
		{name: "stale identity", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			generation, incarnation := testMutationIdentity(t, m, "side")
			_, err := m.RemoveResult(ctx, removeRequestFor("side", generation+7, incarnation))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "presence counter exhausted", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			m.cfg.hosts.SeedHighWater(map[string]hostreg.HighWater{"side": {PresenceEpoch: math.MaxUint64}})
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "staged write fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 1 })
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "runtime flip fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 2 })
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "teardown failure", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			failTeardown(m)
			result, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			if err != nil || result.HostMutationTeardownFailureRemoved == nil {
				t.Fatalf("Remove = (%+v, %v), want the teardown-failure arm", result, err)
			}
			return m, "side"
		}},
		{name: "teardown failure then finalizing write fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			failTeardown(m)
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 3 })
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "committed with finalizing write failing", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 3 })
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			requireRefusal(t, err)
			return m, "side"
		}},
		{name: "committed", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			result, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			if err != nil || result.HostMutationCommittedRemoved == nil {
				t.Fatalf("Remove = (%+v, %v), want the committed arm", result, err)
			}
			return m, "side"
		}},
		{name: "collision dropped", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			m.testOnlyParkPostCommit = handEditOnce(t, m.cfg.configPath, "side")
			result, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			if err != nil || result.HostMutationCollisionDropped == nil {
				t.Fatalf("Remove = (%+v, %v), want the collision-dropped arm", result, err)
			}
			return m, "side"
		}},
		{name: "teardown failure with collision dropped", run: func(t *testing.T) (*hubHostManager, string) {
			m := balanceManager(t, "side")
			failTeardown(m)
			m.testOnlyParkPostCommit = handEditOnce(t, m.cfg.configPath, "side")
			result, err := m.RemoveResult(ctx, removeRequest(t, m, "side"))
			if err != nil || result.HostMutationCollisionDropped == nil {
				t.Fatalf("Remove = (%+v, %v), want the collision-dropped arm", result, err)
			}
			return m, "side"
		}},
	}
	runBalanceExits(t, exits)
}

// crossFileRemoval builds a hub whose host "m4" holds an outstanding
// confirmation token, so its removal takes §9's cross-file commit.
func crossFileRemoval(t *testing.T) *hubHostManager {
	t.Helper()
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		pipelineToken(t, store, "m4", 2)
	})
	return fixture.manager(t)
}

func TestRemoveResultCrossFileCommitLeavesTheLockStateBalancedOnEveryExit(t *testing.T) {
	ctx := context.Background()
	exits := []balanceExit{
		{name: "committed", run: func(t *testing.T) (*hubHostManager, string) {
			m := crossFileRemoval(t)
			result, err := m.RemoveResult(ctx, removeRequest(t, m, "m4"))
			if err != nil || result.HostMutationCommittedRemoved == nil {
				t.Fatalf("Remove = (%+v, %v), want the committed arm", result, err)
			}
			if _, ok := m.cfg.ops.OutstandingToken("m4"); ok {
				t.Fatal("the cross-file commit left the token row behind")
			}
			return m, "m4"
		}},
		{name: "stash cannot be written", run: func(t *testing.T) (*hubHostManager, string) {
			m := crossFileRemoval(t)
			req := removeRequest(t, m, "m4")
			key := hostMutationReceiptKey(req.MutationID, "m4", hostMutationRemove, hostMutationIdentity{
				Generation: req.ExpectedGeneration, IncarnationID: req.ExpectedIncarnationID,
			})
			if err := os.Mkdir(hubTOMLStashPath(m.cfg.configPath, key), 0o700); err != nil {
				t.Fatalf("block the stash path: %v", err)
			}
			_, err := m.RemoveResult(ctx, req)
			requireRefusal(t, err)
			return m, "m4"
		}},
		{name: "compensation cannot be armed", run: func(t *testing.T) (*hubHostManager, string) {
			m := crossFileRemoval(t)
			token, ok := m.cfg.ops.OutstandingToken("m4")
			if !ok {
				t.Fatal("fixture lost the token row")
			}
			if err := m.cfg.ops.ArmCompensation(hostops.Compensation{
				Host: "m4", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
				Stash: hubTOMLStashPath(m.cfg.configPath, "earlier"), Generation: 2,
			}); err != nil {
				t.Fatalf("pre-arm: %v", err)
			}
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "m4"))
			requireRefusal(t, err)
			return m, "m4"
		}},
		{name: "intent clearing write fails", run: func(t *testing.T) (*hubHostManager, string) {
			m := crossFileRemoval(t)
			flakyHubTOMLDirSync(t, func(call int) bool { return call == 3 })
			_, err := m.RemoveResult(ctx, removeRequest(t, m, "m4"))
			requireRefusal(t, err)
			return m, "m4"
		}},
		{name: "teardown failure", run: func(t *testing.T) (*hubHostManager, string) {
			m := crossFileRemoval(t)
			failTeardown(m)
			result, err := m.RemoveResult(ctx, removeRequest(t, m, "m4"))
			if err != nil || result.HostMutationTeardownFailureRemoved == nil {
				t.Fatalf("Remove = (%+v, %v), want the teardown-failure arm", result, err)
			}
			return m, "m4"
		}},
	}
	runBalanceExits(t, exits)
}

func runBalanceExits(t *testing.T, exits []balanceExit) {
	t.Helper()
	for _, exit := range exits {
		t.Run(exit.name, func(t *testing.T) {
			m, name := exit.run(t)
			assertMutationBalanced(t, m, name, exit.marked)
		})
	}
}

// lockProbe records, at each hub.toml write, whether the mutation mutex was
// held by the mutation doing the write.
func lockProbe(t *testing.T, m *hubHostManager) *[]bool {
	t.Helper()
	var held []bool
	saved := hubTOMLSyncDir
	hubTOMLSyncDir = func(dir string) error {
		free := m.cfg.mu.TryLock()
		if free {
			m.cfg.mu.Unlock()
		}
		held = append(held, !free)
		return saved(dir)
	}
	t.Cleanup(func() { hubTOMLSyncDir = saved })
	return &held
}

// postCommitProbe is the testOnlyParkPostCommit hook for the window between a
// commit and its live phase: the mutation mutex is free, the in-flight mark
// fences the name, and the per-host gate is still held.
func postCommitProbe(t *testing.T, m *hubHostManager, window *[]string) func(string) {
	t.Helper()
	return func(name string) {
		if !m.cfg.mu.TryLock() {
			*window = append(*window, "mutex-held")
			return
		}
		if m.isMutating(name) {
			*window = append(*window, "marked")
		}
		m.cfg.mu.Unlock()
		if release, err := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "probe"}); err != nil {
			*window = append(*window, "gate-held")
		} else {
			release()
		}
	}
}

func TestMutationLockWindows(t *testing.T) {
	ctx := context.Background()
	wantHeldWrites := func(t *testing.T, held []bool, writes int) {
		t.Helper()
		if len(held) != writes {
			t.Fatalf("hub.toml writes = %d, want %d", len(held), writes)
		}
		for i, h := range held {
			if !h {
				t.Errorf("hub.toml write %d ran without the mutation mutex", i+1)
			}
		}
	}
	wantWindow := func(t *testing.T, window []string) {
		t.Helper()
		if !slices.Equal(window, []string{"marked", "gate-held"}) {
			t.Errorf("post-commit window = %v, want the mutex released, the name marked and the gate held", window)
		}
	}
	t.Run("add holds the mutex across both writes", func(t *testing.T) {
		m := balanceManager(t)
		held := lockProbe(t, m)
		if _, err := m.AddResult(ctx, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "new", Address: "new.example"}}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		wantHeldWrites(t, *held, 2)
	})
	t.Run("update", func(t *testing.T) {
		m := balanceManager(t, "side")
		held := lockProbe(t, m)
		var window []string
		m.testOnlyParkPostCommit = postCommitProbe(t, m, &window)
		if _, err := m.UpdateResult(ctx, updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})); err != nil {
			t.Fatalf("Update: %v", err)
		}
		wantHeldWrites(t, *held, 3)
		wantWindow(t, window)
	})
	t.Run("remove", func(t *testing.T) {
		m := balanceManager(t, "side")
		held := lockProbe(t, m)
		var window []string
		m.testOnlyParkPostCommit = postCommitProbe(t, m, &window)
		if _, err := m.RemoveResult(ctx, removeRequest(t, m, "side")); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		wantHeldWrites(t, *held, 3)
		wantWindow(t, window)
	})
	t.Run("cross-file remove", func(t *testing.T) {
		m := crossFileRemoval(t)
		held := lockProbe(t, m)
		var window []string
		m.testOnlyParkPostCommit = postCommitProbe(t, m, &window)
		if _, err := m.RemoveResult(ctx, removeRequest(t, m, "m4")); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		wantHeldWrites(t, *held, 4)
		wantWindow(t, window)
	})
	t.Run("refused runtime flip releases before the compensation returns", func(t *testing.T) {
		m := balanceManager(t, "side")
		flakyHubTOMLDirSync(t, func(call int) bool { return call == 2 })
		_, err := m.UpdateResult(ctx, updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"}))
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("Update = %v, want the flip's refusal", err)
		}
		assertMutationBalanced(t, m, "side", false)
	})
}
