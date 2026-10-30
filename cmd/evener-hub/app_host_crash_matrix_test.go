package hub

// The host mutation crash matrix: add, update and remove are each cut at every
// durable step of their commit path (after the staged write, after the runtime
// flip, in the middle of the teardown, and not at all), the process is
// "restarted" over the very hub.toml and operation store the cut left behind,
// and the recovered hub must serve the committed outcome. The assertions are on
// outcomes a client can observe (list rows, a replayed mutation id, the hosts
// the file declares, the store's token rows, the generations hosts were handed),
// so the matrix stays valid while the records behind those outcomes move
// between files.
//
// A crash is a panic raised from a test seam: it unwinds past every compensation
// the mutation would otherwise run and leaves exactly the durable state the
// writes before the seam produced. The restart rebuilds the registry, the
// operation store handle and the manager from disk through the same
// constructors the hub's startup uses.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// crashCut is the panic value the crash seams raise.
type crashCut struct{}

// crashHub is one hub's durable state: a hub.toml and the operation store's
// state root. boot rebuilds a manager over whatever they hold.
type crashHub struct {
	configPath string
	// stateRoot is the state root of the most recent boot's store, and store is
	// the handle that boot opened.
	stateRoot string
	store     *hostops.Store
}

func newCrashHub(t *testing.T, seed []hostreg.Host) *crashHub {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(configPath, seed); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	return &crashHub{configPath: configPath, stateRoot: t.TempDir()}
}

// restartStateRoot copies the operation store's files to a fresh state root and
// makes that the hub's. The store caches its state per canonical path for the
// life of the process, so reopening the old path would hand back the crashed
// process's memory; a fresh path forces the load from the bytes on disk, which
// is what a real restart reads.
func (h *crashHub) restartStateRoot(t *testing.T) {
	t.Helper()
	next := t.TempDir()
	storeDir := filepath.Dir(hostops.StorePath(h.stateRoot))
	nextDir := filepath.Dir(hostops.StorePath(next))
	if err := os.MkdirAll(nextDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", nextDir, err)
	}
	files, err := os.ReadDir(storeDir)
	if errors.Is(err, os.ErrNotExist) {
		h.stateRoot = next
		return
	}
	if err != nil {
		t.Fatalf("read %s: %v", storeDir, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(storeDir, file.Name()))
		if err != nil {
			t.Fatalf("read store file %s: %v", file.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(nextDir, file.Name()), data, 0o600); err != nil {
			t.Fatalf("copy store file %s: %v", file.Name(), err)
		}
	}
	h.stateRoot = next
}

// boot is a process start: registry from the file, the operation store loaded
// from its files, and the manager constructor, which runs every boot recovery
// pass. The first boot opens the empty store; every later boot loads what the
// previous one left on disk.
func (h *crashHub) boot(t *testing.T, restart bool) (*hubHostManager, *hostops.Store) {
	t.Helper()
	if restart {
		h.restartStateRoot(t)
	}
	cfg, err := LoadConfig(h.configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	store, err := hostops.Open(hostops.StorePath(h.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	if restart {
		requireSameStoreState(t, h.store, store)
	}
	h.store = store
	doc := hostBootDocument(cfg, legacyRecordsOf(t, h.configPath), store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	webCfg := hubcore.WebConfig{RemoteHostOpsStore: store, RemoteHostConfigPath: h.configPath}
	m := newHubHostManager(appsource.NewRegistry(), nil, webCfg, h.configPath, hosts, func(string, ...any) {})
	return m, store
}

// requireSameStoreState fails when the store a restart loaded from disk differs
// from the one the crashed process held in memory: every write the process
// acknowledged must have landed in the file.
func requireSameStoreState(t *testing.T, crashed, restarted *hostops.Store) {
	t.Helper()
	if crashed.Sequence() != restarted.Sequence() {
		t.Fatalf("restarted store sequence = %d, the crashed process held %d", restarted.Sequence(), crashed.Sequence())
	}
	if !reflect.DeepEqual(crashed.Boundaries(), restarted.Boundaries()) {
		t.Fatalf("restarted store boundaries = %+v, the crashed process held %+v", restarted.Boundaries(), crashed.Boundaries())
	}
	for _, host := range []string{"keep", "side"} {
		before, hadBefore := crashed.OutstandingToken(host)
		after, hasAfter := restarted.OutstandingToken(host)
		if hadBefore != hasAfter || before.Value != after.Value {
			t.Fatalf("restarted store token row for %s = (%q, %v), the crashed process held (%q, %v)", host, after.Value, hasAfter, before.Value, hadBefore)
		}
	}
}

// declaredHosts is the [[hosts]] set the file declares, name to address.
func (h *crashHub) declaredHosts(t *testing.T) map[string]string {
	t.Helper()
	cfg, err := LoadConfig(h.configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	declared := make(map[string]string, len(cfg.Hosts))
	for _, host := range cfg.Hosts {
		declared[host.Name] = host.SSH
	}
	return declared
}

func (h *crashHub) fileBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	return data
}

// hostRows indexes the list response by host name; a removed host's tombstone
// row is keyed by its name too.
func hostRows(t *testing.T, m *hubHostManager) map[string]appwire.HostRow {
	t.Helper()
	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := make(map[string]appwire.HostRow, len(list.Hosts))
	for _, row := range list.Hosts {
		rows[row.Name] = row
	}
	return rows
}

// runUntilCrash runs op and requires it to stop at a crash seam.
func runUntilCrash(t *testing.T, op func()) {
	t.Helper()
	defer func() {
		switch recovered := recover(); recovered.(type) {
		case crashCut:
		case nil:
			t.Fatal("the mutation finished without reaching its crash cut")
		default:
			panic(recovered)
		}
	}()
	op()
}

// mintHostToken puts one outstanding confirmation-token row in the store for
// the host's live identity, the row a removal must purge.
func mintHostToken(t *testing.T, store *hostops.Store, host hostreg.Host) {
	t.Helper()
	_, err := store.MintTokenIfQuiescent(hostops.MintRequest{
		Host:               host.Name,
		Generation:         host.Generation,
		IncarnationID:      host.IncarnationID,
		EntryHash:          hubTOMLFingerprint([]byte("entry-" + host.Name)),
		HubTOMLFingerprint: hubTOMLFingerprint([]byte("toml-" + host.Name)),
		FactsRevision:      hubTOMLFingerprint([]byte("facts-" + host.Name)),
		FactsCapturedAt:    time.Now().UTC(),
		TargetPath:         "/opt/evener",
		ControllerRevision: "v1.2.3",
		RunningVersion:     "v1.1.0",
		RunningHealthy:     true,
		FreshnessBound:     hostops.DefaultFreshnessBound,
		TTL:                hostops.DefaultTokenTTL,
	}, store.Sequence())
	if err != nil {
		t.Fatalf("MintTokenIfQuiescent(%s): %v", host.Name, err)
	}
}

// crashStep names the durable step a mutation is cut at.
type crashStep string

const (
	// cutStaged: the staged write landed; nothing live changed yet.
	cutStaged crashStep = "staged"
	// cutFlipped: the runtime flip landed and the commit is past its point of
	// no return; the post-commit teardown has not started.
	cutFlipped crashStep = "flipped"
	// cutTeardown: the post-commit teardown started and died.
	cutTeardown crashStep = "teardown"
	// cutResponseLost: the mutation finished; only its response was lost.
	cutResponseLost crashStep = "response-lost"
)

// armCut installs the seam that crashes the mutation at step; cutResponseLost
// installs nothing.
func armCut(m *hubHostManager, step crashStep) {
	crash := func() { panic(crashCut{}) }
	switch step {
	case cutStaged:
		m.testOnlyAfterStage = func(string) { crash() }
	case cutFlipped:
		m.testOnlyParkPostCommit = func(string) { crash() }
	case cutTeardown:
		m.testOnlyTeardown = func(context.Context, string) error { crash(); return nil }
	}
}

// crashMutation is one mutation the matrix cuts: how to issue it, and how a
// client holding the same request replays it after the restart.
type crashMutation struct {
	id    string
	issue func(m *hubHostManager) (appwire.HostMutationResult, error)
}

func addMutation(address string) crashMutation {
	params := appwire.HostAddParams{
		Entry:      appwire.HostEntry{Name: "side", Address: address},
		MutationID: newTestMutationID(),
	}
	return crashMutation{id: params.MutationID, issue: func(m *hubHostManager) (appwire.HostMutationResult, error) {
		return m.AddResult(context.Background(), params)
	}}
}

func updateMutation(t *testing.T, m *hubHostManager, address string) crashMutation {
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: address})
	return crashMutation{id: params.MutationID, issue: func(m *hubHostManager) (appwire.HostMutationResult, error) {
		return m.UpdateResult(context.Background(), params)
	}}
}

func removeMutation(t *testing.T, m *hubHostManager) crashMutation {
	params := removeRequest(t, m, "side")
	return crashMutation{id: params.MutationID, issue: func(m *hubHostManager) (appwire.HostMutationResult, error) {
		return m.RemoveResult(context.Background(), params)
	}}
}

// requireSettledRecords checks the machine records the restart left behind,
// before anything replays the mutation: no staged marker is still waiting for a
// finalizer, no teardown remnant is open, and the mutation's receipt is the
// committed one (recovered by boot unless the commit had finished already).
func requireSettledRecords(t *testing.T, m *hubHostManager, mutationID string, bootRecovered bool) {
	t.Helper()
	if staged := m.cfg.store.stagedSnapshot(); len(staged) != 0 {
		t.Fatalf("the restart left %d staged marker(s) unfinalized", len(staged))
	}
	for id, remnant := range m.cfg.store.remnantSnapshot() {
		if remnant.open() {
			t.Fatalf("the restart left teardown remnant %s open", id)
		}
	}
	var found bool
	for key, receipt := range m.cfg.store.receiptsSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok || scope.MutationID != mutationID {
			continue
		}
		found = true
		if receipt.Outcome != hostReceiptOutcomeCommitted {
			t.Fatalf("the mutation's receipt outcome = %q, want committed", receipt.Outcome)
		}
		if receipt.BootRecovered != bootRecovered {
			t.Fatalf("the mutation's receipt bootRecovered = %v, want %v", receipt.BootRecovered, bootRecovered)
		}
	}
	if !found {
		t.Fatal("the restart holds no receipt for the mutation")
	}
}

// incarnationGeneration is one observation of a name's identity.
type incarnationGeneration struct {
	incarnation string
	generation  uint64
}

// requireNeverReused fails when a later incarnation of a name carries a
// generation at or below an earlier incarnation's, or when one incarnation's
// generation moves backward.
func requireNeverReused(t *testing.T, seen []incarnationGeneration) {
	t.Helper()
	for i := 1; i < len(seen); i++ {
		prev, next := seen[i-1], seen[i]
		if next.incarnation == prev.incarnation {
			if next.generation < prev.generation {
				t.Fatalf("generation moved backward within incarnation %s: %d then %d", next.incarnation, prev.generation, next.generation)
			}
			continue
		}
		if next.generation <= prev.generation {
			t.Fatalf("generation %d was handed to incarnation %s after incarnation %s held %d", next.generation, next.incarnation, prev.incarnation, prev.generation)
		}
	}
}

// matrixOutcome is what every cut of one mutation kind must converge to.
type matrixOutcome struct {
	// live is the address the host serves at, or "" when the host is gone.
	live string
}

func TestHostMutationCrashCutsConvergeToTheCommittedOutcome(t *testing.T) {
	keep := hostreg.Host{Name: "keep", SSH: "keep.example"}
	side := hostreg.Host{Name: "side", SSH: "side.example"}
	const edited = "edited.example"
	const added = "added.example"

	type scenario struct {
		name  string
		steps []crashStep
		// seed is the hub.toml the hub starts with.
		seed []hostreg.Host
		// prepare runs on the first boot before the cut, e.g. to remove the host a
		// re-add re-creates or to give a removal a token row to purge.
		prepare func(t *testing.T, m *hubHostManager, store *hostops.Store)
		// mutate builds the mutation from the booted hub.
		mutate func(t *testing.T, m *hubHostManager) crashMutation
		want   matrixOutcome
	}
	allSteps := []crashStep{cutStaged, cutFlipped, cutTeardown, cutResponseLost}
	// An add has no flip and no teardown of its own.
	addSteps := []crashStep{cutStaged, cutResponseLost}

	scenarios := []scenario{
		{
			name: "add", steps: addSteps, seed: []hostreg.Host{keep},
			mutate: func(*testing.T, *hubHostManager) crashMutation { return addMutation(added) },
			want:   matrixOutcome{live: added},
		},
		{
			name: "re-add after removal", steps: addSteps, seed: []hostreg.Host{keep, side},
			prepare: func(t *testing.T, m *hubHostManager, _ *hostops.Store) {
				if _, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side")); err != nil {
					t.Fatalf("remove before the re-add: %v", err)
				}
			},
			mutate: func(*testing.T, *hubHostManager) crashMutation { return addMutation(added) },
			want:   matrixOutcome{live: added},
		},
		{
			name: "update", steps: allSteps, seed: []hostreg.Host{keep, side},
			mutate: func(t *testing.T, m *hubHostManager) crashMutation { return updateMutation(t, m, edited) },
			want:   matrixOutcome{live: edited},
		},
		{
			name: "remove", steps: allSteps, seed: []hostreg.Host{keep, side},
			mutate: removeMutation,
			want:   matrixOutcome{},
		},
		{
			name: "remove with an outstanding token", steps: allSteps, seed: []hostreg.Host{keep, side},
			prepare: func(t *testing.T, m *hubHostManager, store *hostops.Store) {
				host, ok := m.cfg.hosts.Get("side")
				if !ok {
					t.Fatal("side is not registered")
				}
				mintHostToken(t, store, host)
			},
			mutate: removeMutation,
			want:   matrixOutcome{},
		},
	}

	for _, sc := range scenarios {
		for _, step := range sc.steps {
			t.Run(sc.name+"/"+string(step), func(t *testing.T) {
				t.Parallel()
				hub := newCrashHub(t, sc.seed)
				m, store := hub.boot(t, false)
				if sc.prepare != nil {
					sc.prepare(t, m, store)
				}
				var seen []incarnationGeneration
				observe := func(m *hubHostManager) {
					if row, ok := hostRows(t, m)["side"]; ok {
						seen = append(seen, incarnationGeneration{row.IncarnationID, row.Generation})
					}
				}
				observe(m)
				keepBefore := hostRows(t, m)["keep"]

				mutation := sc.mutate(t, m)
				armCut(m, step)
				if step == cutResponseLost {
					result, err := mutation.issue(m)
					if err != nil {
						t.Fatalf("mutation: %v", err)
					}
					if result.HostMutationCommitted == nil && result.HostMutationCommittedRemoved == nil {
						t.Fatalf("mutation result = %+v, want a committed arm", result)
					}
				} else {
					runUntilCrash(t, func() { _, _ = mutation.issue(m) })
				}

				recovered, recoveredStore := hub.boot(t, true)
				requireSettledRecords(t, recovered, mutation.id, step != cutResponseLost)
				rows := hostRows(t, recovered)
				side, present := rows["side"]
				switch {
				case sc.want.live != "":
					if !present || side.Removed || side.Address != sc.want.live {
						t.Fatalf("recovered side row = %+v (present %v), want live at %q", side, present, sc.want.live)
					}
					if declared := hub.declaredHosts(t)["side"]; declared != sc.want.live {
						t.Fatalf("hub.toml declares side at %q, want %q", declared, sc.want.live)
					}
				default:
					if present && !side.Removed {
						t.Fatalf("recovered side row = %+v, want the host gone", side)
					}
					if _, declared := hub.declaredHosts(t)["side"]; declared {
						t.Fatal("hub.toml still declares the removed host")
					}
					if !present {
						t.Fatal("the removal left no tombstone row")
					}
					if _, tokenLeft := recoveredStore.OutstandingToken("side"); tokenLeft {
						t.Fatal("the removed host's confirmation token survived recovery")
					}
				}
				if side.OpenRemnantID != "" {
					t.Fatalf("recovery left an open teardown remnant %q", side.OpenRemnantID)
				}
				keepAfter := rows["keep"]
				if keepAfter.Removed || keepAfter.Generation != keepBefore.Generation || keepAfter.IncarnationID != keepBefore.IncarnationID {
					t.Fatalf("the cut disturbed the sibling host: before %+v after %+v", keepBefore, keepAfter)
				}
				if declared := hub.declaredHosts(t); declared["keep"] != keep.SSH {
					t.Fatalf("hub.toml declares keep at %q, want %q", declared["keep"], keep.SSH)
				}
				observe(recovered)

				// The client that lost the response retries the same request: it
				// gets the recorded outcome and nothing changes.
				before := hub.fileBytes(t)
				replay, err := mutation.issue(recovered)
				if err != nil {
					t.Fatalf("replay after the restart: %v", err)
				}
				if replay.HostMutationCommitted == nil && replay.HostMutationCommittedRemoved == nil {
					t.Fatalf("replay = %+v, want the recorded committed arm", replay)
				}
				if got := hostRows(t, recovered)["side"]; got.Generation != side.Generation || got.IncarnationID != side.IncarnationID || got.Removed != side.Removed {
					t.Fatalf("the replay changed the host: before %+v after %+v", side, got)
				}
				if after := hub.fileBytes(t); !bytes.Equal(after, before) {
					t.Fatal("the replay rewrote hub.toml")
				}

				// A further restart over the recovered state changes nothing.
				second, _ := hub.boot(t, true)
				if after := hub.fileBytes(t); !bytes.Equal(after, before) {
					t.Fatal("a second restart rewrote hub.toml")
				}
				if got := hostRows(t, second)["side"]; got.Generation != side.Generation || got.Removed != side.Removed {
					t.Fatalf("a second restart changed the host: before %+v after %+v", side, got)
				}

				// The hub keeps serving mutations, and the identities it hands out
				// keep rising across the cut.
				if sc.want.live != "" {
					if _, err := second.RemoveResult(context.Background(), removeRequest(t, second, "side")); err != nil {
						t.Fatalf("remove after recovery: %v", err)
					}
				}
				if _, err := second.AddResult(context.Background(), appwire.HostAddParams{
					Entry:      appwire.HostEntry{Name: "side", Address: "again.example"},
					MutationID: newTestMutationID(),
				}); err != nil {
					t.Fatalf("add after recovery: %v", err)
				}
				observe(second)
				requireNeverReused(t, seen)
			})
		}
	}
}
