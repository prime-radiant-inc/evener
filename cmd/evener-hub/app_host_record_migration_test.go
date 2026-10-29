package hub

// The machine records live in the operation store; hub.toml keeps the hosts. A
// hub that last ran a build that kept the records in hub.toml's reserved
// sections moves them at its first boot, and a hub whose store is unavailable
// keeps serving its hosts and takes host changes again once it is started with
// a store that opens.
// These tests drive the real constructors over real files.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// bootOverStore is a process start over configPath and the operation store
// under stateRoot: the registry is built the way startup builds it, then the
// manager boots with the store attached.
func bootOverStore(t *testing.T, configPath, stateRoot string) (*hubHostManager, *hostops.Store) {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	doc := hostBootDocument(cfg, store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	webCfg := hubcore.WebConfig{RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath}
	return newHubHostManager(appsource.NewRegistry(), nil, webCfg, configPath, hosts, t.Logf), store
}

// restartOver is a process start over configPath and a store that is already
// open: the registry is built from the files the way startup builds it, then the
// manager boots.
func restartOver(t *testing.T, configPath string, store *hostops.Store) *hubHostManager {
	t.Helper()
	doc := hostBootDocument(mustLoadConfig(t, configPath), store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	return newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath,
	}, configPath, hosts, t.Logf)
}

// legacyHub builds a hub.toml the previous build could have left: hosts, and
// every record section the live mutations write, produced by running those
// mutations against hub.toml with no operation store. It returns the path and
// the records the file carries.
func legacyHub(t *testing.T) (string, hostRecordSet) {
	t.Helper()
	ctx := context.Background()
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	seed := []hostreg.Host{
		{Name: "keep", SSH: "keep.example"},
		{Name: "gamma", SSH: "gamma.example"},
		{Name: "delta", SSH: "delta.example"},
	}
	if err := writeHubTOMLHosts(configPath, seed); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	if _, err := m.AddResult(ctx, appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "epsilon", Address: "epsilon.example"}, MutationID: "add-epsilon",
	}); err != nil {
		t.Fatalf("Add(epsilon): %v", err)
	}
	// More edits than the superseded-receipt bound keeps, so compaction leaves
	// pruned markers behind.
	for i := range 11 {
		if _, err := m.UpdateResult(ctx, updateRequest(t, m, "keep", appwire.HostEntry{Address: fmt.Sprintf("keep%d.example", i)})); err != nil {
			t.Fatalf("Update(keep) %d: %v", i, err)
		}
	}
	if _, err := m.RemoveResult(ctx, removeRequest(t, m, "gamma")); err != nil {
		t.Fatalf("Remove(gamma): %v", err)
	}
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	if result, err := m.RemoveResult(ctx, removeRequest(t, m, "delta")); err != nil || result.HostMutationTeardownFailureRemoved == nil {
		t.Fatalf("Remove(delta) = (%+v, %v), want the teardown-failure arm", result, err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	records := cfg.recordSet()
	for name, populated := range map[string]int{
		"host_records": len(records.HostRecords), "generations": len(records.Generations),
		"mutation_receipts": len(records.MutationReceipts), "tombstones": len(records.Tombstones),
		"pruned_receipts": len(records.PrunedReceipts), "teardown_remnants": len(records.TeardownRemnants),
		"mirror_commits": len(records.MirrorCommits),
	} {
		if populated == 0 {
			t.Fatalf("the legacy fixture left no %s", name)
		}
	}
	return configPath, records
}

// requireFamilyEquals fails when the store's records differ from want in any of
// the eight maps the family holds.
func requireFamilyEquals(t *testing.T, store *hostops.Store, want hostRecordSet) {
	t.Helper()
	got := mustRecords(t, store)
	want.TeardownRemnants = withRemnantIDs(want.TeardownRemnants)
	requireSameMap(t, "host records", got.HostRecords, want.HostRecords)
	requireSameMap(t, "generations", got.Generations, want.Generations)
	requireSameMap(t, "receipts", got.MutationReceipts, want.MutationReceipts)
	requireSameMap(t, "tombstones", got.Tombstones, want.Tombstones)
	requireSameMap(t, "pruned receipts", got.PrunedReceipts, want.PrunedReceipts)
	requireSameMap(t, "staged receipts", got.StagedReceipts, want.StagedReceipts)
	requireSameMap(t, "teardown remnants", got.TeardownRemnants, want.TeardownRemnants)
	requireSameMap(t, "teardown attempts", got.TeardownAttempts, want.TeardownAttempts)
}

func requireSameMap[V any](t *testing.T, what string, got, want map[string]V) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the store's %s = %+v, want %+v", what, got, want)
	}
}

func requireNoRecordSections(t *testing.T, configPath string) {
	t.Helper()
	doc, _, err := readHubTOMLDocument(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	if sections := legacyRecordSections(doc); len(sections) != 0 {
		t.Fatalf("hub.toml still carries the record sections %v", sections)
	}
}

func TestBootMovesEveryRecordSectionIntoTheStoreAndKeepsTheOriginal(t *testing.T) {
	configPath, before := legacyHub(t)
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	identities := map[string]hostreg.Host{}
	for _, entry := range hostRegistryEntries(mustLoadConfig(t, configPath)) {
		identities[entry.Name] = entry
	}
	stateRoot := t.TempDir()

	m, store := bootOverStore(t, configPath, stateRoot)

	requireFamilyEquals(t, store, before)
	requireNoRecordSections(t, configPath)
	if _, _, imported := store.HostRecords(); !imported {
		t.Fatal("the store does not hold the records after the boot")
	}
	kept, err := os.ReadFile(configPath + hostPreRecordsSuffix)
	if err != nil {
		t.Fatalf("the original hub.toml was not kept: %v", err)
	}
	if !bytes.Equal(kept, original) {
		t.Fatal("the kept copy is not the hub.toml the upgrade found")
	}
	cfg := mustLoadConfig(t, configPath)
	if len(cfg.Hosts) != len(identities) {
		t.Fatalf("hub.toml declares %d host(s) after the move, want %d", len(cfg.Hosts), len(identities))
	}
	for name, entry := range identities {
		live, ok := m.cfg.hosts.Get(name)
		if !ok || live.Generation != entry.Generation || live.IncarnationID != entry.IncarnationID || live.PresenceEpoch != entry.PresenceEpoch {
			t.Fatalf("host %s serves %+v after the move, want the identity %+v", name, live, entry)
		}
	}

	// The hub takes host changes in its new shape, and a restart changes nothing
	// further: not the store, not hub.toml, not the kept original.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "zeta", Address: "zeta.example"}}); err != nil {
		t.Fatalf("Add after the move: %v", err)
	}
	storeBefore, configBefore := fileBytes(t, hostops.StorePath(stateRoot)), fileBytes(t, configPath)
	bootOverStore(t, configPath, stateRoot)
	if !bytes.Equal(storeBefore, fileBytes(t, hostops.StorePath(stateRoot))) || !bytes.Equal(configBefore, fileBytes(t, configPath)) {
		t.Fatal("a restart over the moved records rewrote the store or hub.toml")
	}
	if !bytes.Equal(kept, fileBytes(t, configPath+hostPreRecordsSuffix)) {
		t.Fatal("a restart rewrote the kept original")
	}
}

func mustLoadConfig(t *testing.T, path string) Config {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", path, err)
	}
	return cfg
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// TestBootAfterACrashBetweenTheImportAndTheStripDropsTheSectionsWithoutImporting
// pins the half-moved state: the store already holds the records and hub.toml
// still carries the sections. The store is the home, so the sections are
// dropped, never imported again over what the store has moved on to.
func TestBootAfterACrashBetweenTheImportAndTheStripDropsTheSectionsWithoutImporting(t *testing.T) {
	configPath, _ := legacyHub(t)
	original := fileBytes(t, configPath)
	stateRoot := t.TempDir()
	_, store := bootOverStore(t, configPath, stateRoot)
	firstCopy := fileBytes(t, configPath+hostPreRecordsSuffix)

	// The store moves on: a receipt the sections never held.
	family, _, _ := store.HostRecords()
	delete(family.Receipts, firstKey(family.Receipts))
	if err := store.CommitHostRecords(family, hostops.HostMirror{}); err != nil {
		t.Fatalf("CommitHostRecords: %v", err)
	}
	moved := mustRecords(t, store)

	// The strip never happened: the sections are back, the file was edited since.
	if err := os.WriteFile(configPath, append(append([]byte(nil), original...), []byte("\n# edited after the upgrade\n")...), 0o600); err != nil {
		t.Fatalf("restore the sections: %v", err)
	}
	bootOverStore(t, configPath, stateRoot)

	requireNoRecordSections(t, configPath)
	requireFamilyEquals(t, store, moved)
	if !bytes.Equal(firstCopy, fileBytes(t, configPath+hostPreRecordsSuffix)) {
		t.Fatal("the second strip overwrote the one-time copy of the original")
	}
}

func firstKey[V any](m map[string]V) string {
	for key := range m {
		return key
	}
	return ""
}

func mustRecords(t *testing.T, store *hostops.Store) hostRecordSet {
	t.Helper()
	records, err := hostOpsRecordStore{ops: store}.records()
	if err != nil {
		t.Fatalf("read the store's records: %v", err)
	}
	return records
}

// TestBootMintsRecordsForAHostThatNeverHadAny pins the first boot of a hub.toml
// that holds hosts and nothing else: every host gets its identity recorded in
// the store, the one the registry serves, and no copy of an original with no
// sections is kept.
func TestBootMintsRecordsForAHostThatNeverHadAny(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "alpha", SSH: "alpha.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m, store := bootOverStore(t, configPath, t.TempDir())
	records := mustRecords(t, store)
	live, _ := m.cfg.hosts.Get("alpha")
	if got := records.HostRecords["alpha"]; got.IncarnationID != live.IncarnationID || got.PresenceEpoch != live.PresenceEpoch {
		t.Fatalf("the store records %+v for alpha, the registry serves %+v", got, live)
	}
	if got := records.Generations["alpha"]; got.Generation != live.Generation {
		t.Fatalf("the store records generation %d for alpha, the registry serves %d", got.Generation, live.Generation)
	}
	if _, err := os.Stat(configPath + hostPreRecordsSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a hub.toml with no record sections was copied aside: %v", err)
	}
}

// TestHostsKeepServingAndChangesResumeWhenTheStoreReturns pins the store-down
// disposition end to end: the hosts in hub.toml are listed, add, edit and remove
// refuse as retryable without touching hub.toml, and once the hub starts with a
// store the same requests succeed and their records are durable.
func TestHostsKeepServingAndChangesResumeWhenTheStoreReturns(t *testing.T) {
	ctx := context.Background()
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg := mustLoadConfig(t, configPath)
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: configPath, RemoteHostOpsUnavailable: true,
	}, configPath, hosts, t.Logf)

	if rows := hostRows(t, m); rows["keep"].Removed || rows["keep"].Name != "keep" {
		t.Fatalf("the hosts in hub.toml are not served: %+v", rows)
	}
	before := fileBytes(t, configPath)
	_, err = m.AddResult(ctx, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}, MutationID: "add-extra"})
	requireWireError(t, err, appwire.CodeUnavailable)
	_, err = m.UpdateResult(ctx, updateRequest(t, m, "keep", appwire.HostEntry{Address: "edited.example"}))
	requireWireError(t, err, appwire.CodeUnavailable)
	_, err = m.RemoveResult(ctx, removeRequest(t, m, "keep"))
	requireWireError(t, err, appwire.CodeUnavailable)
	if !bytes.Equal(before, fileBytes(t, configPath)) {
		t.Fatal("a refused host change touched hub.toml")
	}

	// The hub started again with a store that opens.
	stateRoot := t.TempDir()
	m, store := bootOverStore(t, configPath, stateRoot)
	if _, _, imported := store.HostRecords(); !imported {
		t.Fatal("starting with the store did not move the host records into it")
	}

	if _, err := m.AddResult(ctx, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}, MutationID: "add-extra"}); err != nil {
		t.Fatalf("Add after the store returned: %v", err)
	}
	if receipts := mustRecords(t, store).MutationReceipts; len(receipts) == 0 {
		t.Fatal("the add's receipt did not land in the store")
	}
	if _, err := m.UpdateResult(ctx, updateRequest(t, m, "keep", appwire.HostEntry{Address: "edited.example"})); err != nil {
		t.Fatalf("Update after the store returned: %v", err)
	}
	if _, err := m.RemoveResult(ctx, removeRequest(t, m, "keep")); err != nil {
		t.Fatalf("Remove after the store returned: %v", err)
	}
	// A restart over the same files serves exactly that: the records the changes
	// wrote are durable, the removed host is a tombstone.
	second, _ := bootOverStore(t, configPath, stateRoot)
	rows := hostRows(t, second)
	if rows["extra"].Removed || rows["extra"].Address != "extra.example" {
		t.Fatalf("the added host is not served after a restart: %+v", rows["extra"])
	}
	if !rows["keep"].Removed {
		t.Fatalf("the removed host is not a tombstone after a restart: %+v", rows["keep"])
	}
	replay, err := second.AddResult(ctx, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}, MutationID: "add-extra"})
	if err != nil || replay.HostMutationCommitted == nil {
		t.Fatalf("replay of the add after a restart = (%+v, %v), want the recorded committed arm", replay, err)
	}
}

// TestStartupKeepsServingWhenTheStoreCannotOpen pins startup's half: an
// operation store that still cannot open after the startup retries leaves the
// runtime serving the hosts in hub.toml with host changes paused until the hub
// is started with a store that opens.
func TestStartupKeepsServingWhenTheStoreCannotOpen(t *testing.T) {
	previous := hostOpsOpenRetryDelays
	hostOpsOpenRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { hostOpsOpenRetryDelays = previous })

	attempts := 0
	r := &hubRuntime{stderr: new(bytes.Buffer), openOps: func() (*hostops.Store, error) {
		attempts++
		return nil, errors.New("the store path cannot be opened")
	}}

	r.openOpsStore()

	if r.opsStore != nil || !r.opsUnavailable {
		t.Fatalf("runtime = store %v unavailable %v, want no store and the unavailable disposition", r.opsStore, r.opsUnavailable)
	}
	if attempts != 1+len(hostOpsOpenRetryDelays) {
		t.Fatalf("the store was opened %d time(s), want the first attempt and each retry", attempts)
	}
}

// TestStartupRetriesATransientStoreOpenFailure pins the retry that lets a start
// over a store that opens a moment late come up whole: a failed open is tried
// again before the hub gives up and serves without the store.
func TestStartupRetriesATransientStoreOpenFailure(t *testing.T) {
	previous := hostOpsOpenRetryDelays
	hostOpsOpenRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { hostOpsOpenRetryDelays = previous })

	opened, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	attempts := 0
	r := &hubRuntime{stderr: new(bytes.Buffer), openOps: func() (*hostops.Store, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("the store is busy")
		}
		return opened, nil
	}}

	r.openOpsStore()

	if r.opsStore != opened || r.opsUnavailable {
		t.Fatalf("runtime = store %v unavailable %v, want the store opened on the third attempt", r.opsStore, r.opsUnavailable)
	}
}

// legacyRemovalCrash runs the previous build's cross-file removal of "m4"
// against hub.toml and the store, cut at step, and returns the fixture the
// upgrade boots over. "keep" is a second host the hub must go on serving.
func legacyRemovalCrash(t *testing.T, step crashStep) *pipelineFixture {
	t.Helper()
	fixture := newPipelineFixture(t, []hostreg.Host{pipelineEntry("m4", 2), pipelineEntry("keep", 3)}, func(store *hostops.Store) {
		pipelineToken(t, store, "m4", 2)
	})
	m := fixture.legacyManager(t)
	armCut(m, step)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), removeRequest(t, m, "m4")) })
	return fixture
}

// requireServesNormallyAfterUpgrade drives the upgraded hub through the host
// changes the unsettled work would have blocked: it lists its hosts, adds one,
// edits one and removes one, and a restart over the result serves the same.
func requireServesNormallyAfterUpgrade(t *testing.T, fixture *pipelineFixture, m *hubHostManager) {
	t.Helper()
	ctx := context.Background()
	if rows := hostRows(t, m); rows["keep"].Removed || !rows["m4"].Removed {
		t.Fatalf("the upgraded hub lists %+v, want keep live and m4 removed", rows)
	}
	if _, err := m.AddResult(ctx, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}, MutationID: newTestMutationID()}); err != nil {
		t.Fatalf("Add after the upgrade: %v", err)
	}
	if _, err := m.UpdateResult(ctx, updateRequest(t, m, "keep", appwire.HostEntry{Address: "keep2.example"})); err != nil {
		t.Fatalf("Update after the upgrade: %v", err)
	}
	if _, err := m.RemoveResult(ctx, removeRequest(t, m, "extra")); err != nil {
		t.Fatalf("Remove after the upgrade: %v", err)
	}
	restarted := fixture.restart(t)
	rows := hostRows(t, restarted)
	if rows["keep"].Address != "keep2.example" || !rows["extra"].Removed || !rows["m4"].Removed {
		t.Fatalf("a restart serves %+v, want the host changes made after the upgrade", rows)
	}
}

// TestUpgradeFromAnInterruptedLegacyRemovalReachesWhatTheOldBuildWould pins the
// settle-then-move order for a removal the old build cut after its staged write:
// the intent in hub.toml is applied (the token row is purged), the staged
// marker is finalized forward, the records move into the store, and the hub
// serves host changes.
func TestUpgradeFromAnInterruptedLegacyRemovalReachesWhatTheOldBuildWould(t *testing.T) {
	fixture := legacyRemovalCrash(t, cutStaged)
	if !fixture.carriesIntent(t, "m4") {
		t.Fatal("the fixture did not leave the old build's cross-file intent in hub.toml")
	}

	m := fixture.restart(t)

	if _, ok := m.opsStore().OutstandingToken("m4"); ok {
		t.Fatal("the removed host's token row survived the upgrade")
	}
	if len(m.opsStore().Compensations()) != 0 {
		t.Fatal("the upgrade left a compensation open")
	}
	if names := pipelineStashSiblings(t, fixture.configPath); len(names) != 0 {
		t.Fatalf("the upgrade left stash files behind: %v", names)
	}
	requireNoRecordSections(t, fixture.configPath)
	if staged := mustRecords(t, fixture.store).StagedReceipts; len(staged) != 0 {
		t.Fatalf("the upgrade left staged markers: %+v", staged)
	}
	requireServesNormallyAfterUpgrade(t, fixture, m)
}

// TestUpgradeFromAnInterruptedLegacyCompensationReachesWhatTheOldBuildWould is
// the same for a removal the old build cut after its purge: the armed
// compensation record is past its commit point, so the upgrade clears it
// without resurrecting rows, and the hub serves host changes.
func TestUpgradeFromAnInterruptedLegacyCompensationReachesWhatTheOldBuildWould(t *testing.T) {
	fixture := legacyRemovalCrash(t, cutFlipped)
	if _, open := fixture.store.Compensation("m4"); !open {
		t.Fatal("the fixture did not leave the old build's compensation record open")
	}

	m := fixture.restart(t)

	if _, open := m.opsStore().Compensation("m4"); open {
		t.Fatal("the upgrade left the compensation open")
	}
	if _, ok := m.opsStore().OutstandingToken("m4"); ok {
		t.Fatal("the upgrade resurrected the purged token row")
	}
	if names := pipelineStashSiblings(t, fixture.configPath); len(names) != 0 {
		t.Fatalf("the upgrade left stash files behind: %v", names)
	}
	requireNoRecordSections(t, fixture.configPath)
	requireServesNormallyAfterUpgrade(t, fixture, m)
}

// TestHostsKeepServingWhenTheStoreHoldsRecordsThisBuildCannotRead pins the
// disposition for records the store holds in a shape this build refuses (a
// field from a newer build): the hosts in hub.toml keep serving, host changes
// pause, and the records stay in the store untouched rather than being
// reinterpreted or rewritten.
func TestHostsKeepServingWhenTheStoreHoldsRecordsThisBuildCannotRead(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	stateRoot := t.TempDir()
	store, err := hostops.Open(hostops.StorePath(stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	foreign := hostops.HostRecords{
		Hosts:       map[string]json.RawMessage{"keep": json.RawMessage(`{"incarnation_id":"inc-keep","presence_epoch":1,"from_a_newer_build":true}`)},
		Generations: map[string]json.RawMessage{"keep": json.RawMessage(`{"generation":1,"incarnation_id":"inc-keep","presence_epoch":1}`)},
	}
	if err := store.ImportHostRecords(foreign, hostops.HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	before, _, _ := store.HostRecords()

	m, _ := bootOverStore(t, configPath, stateRoot)

	if rows := hostRows(t, m); rows["keep"].Name != "keep" || rows["keep"].Removed {
		t.Fatalf("the hosts in hub.toml are not served: %+v", rows)
	}
	_, err = m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}})
	requireWireError(t, err, appwire.CodeUnavailable)
	after, _, _ := store.HostRecords()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("the unreadable records were rewritten")
	}

	// Once the store holds records this build reads (a start with the build that
	// wrote them), host changes resume.
	repaired := foreign
	repaired.Hosts = map[string]json.RawMessage{"keep": json.RawMessage(`{"incarnation_id":"inc-keep","presence_epoch":1}`)}
	if err := store.CommitHostRecords(repaired, hostops.HostMirror{}); err != nil {
		t.Fatalf("repair the records: %v", err)
	}
	m, _ = bootOverStore(t, configPath, stateRoot)
	if err := m.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused after the records became readable: %v", err)
	}
	if _, err := m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}}); err != nil {
		t.Fatalf("Add after the records became readable: %v", err)
	}
}

// TestStartupKeepsServingWhenTheStoreCannotBeProvenAndResumesWhenItCan pins the
// corrupt-store disposition end to end: a store file whose custody cannot be
// proven is left untouched, startup serves the hosts in hub.toml with host
// changes paused, and once the file is repaired the retry attaches the store,
// moves the records into it and host changes work again.
func TestStartupKeepsServingWhenTheStoreCannotBeProvenAndResumesWhenItCan(t *testing.T) {
	previous := hostOpsOpenRetryDelays
	hostOpsOpenRetryDelays = nil
	t.Cleanup(func() { hostOpsOpenRetryDelays = previous })

	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	stateRoot := t.TempDir()
	path := hostops.StorePath(stateRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"sequence":0,"allocatorHighWaterMark":2,"records":[`), 0o600); err != nil {
		t.Fatalf("write the corrupt store: %v", err)
	}
	r := &hubRuntime{hubStateRoot: stateRoot, stderr: new(bytes.Buffer)}

	r.openOpsStore()

	if r.opsStore != nil || !r.opsUnavailable {
		t.Fatalf("runtime = store %v unavailable %v, want no store and the unavailable disposition", r.opsStore, r.opsUnavailable)
	}
	hosts, err := hostreg.New(hostRegistryEntries(mustLoadConfig(t, configPath)))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: configPath, RemoteHostOpsUnavailable: true,
	}, configPath, hosts, t.Logf)
	if rows := hostRows(t, m); rows["keep"].Name != "keep" || rows["keep"].Removed {
		t.Fatalf("the hosts in hub.toml are not served: %+v", rows)
	}
	_, err = m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}})
	requireWireError(t, err, appwire.CodeUnavailable)

	// The operator repairs the file and starts the hub again.
	if err := os.WriteFile(path, []byte(`{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`), 0o600); err != nil {
		t.Fatalf("repair the store: %v", err)
	}
	m, repaired := bootOverStore(t, configPath, stateRoot)

	if err := m.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still paused after the store was repaired: %v", err)
	}
	if records := mustRecords(t, repaired); records.HostRecords["keep"].IncarnationID == "" {
		t.Fatalf("the host records were not moved into the repaired store: %+v", records.HostRecords)
	}
	if _, err := m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}}); err != nil {
		t.Fatalf("Add after the repair: %v", err)
	}
}

// TestUnreadableHubTOMLAtBootDoesNotMoveEmptyRecordsIntoTheStore pins the guard
// on the import: a boot that cannot read the records it would move must not
// mark the store as their home, or the real records become unreachable once the
// file reads again.
func TestUnreadableHubTOMLAtBootDoesNotMoveEmptyRecordsIntoTheStore(t *testing.T) {
	configPath, records := legacyHub(t)
	original := fileBytes(t, configPath)
	cfg := mustLoadConfig(t, configPath)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	doc := hostBootDocument(cfg, store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}

	previous := configReadFile
	configReadFile = func(string) ([]byte, error) { return nil, errors.New("injected read failure") }
	webCfg := hubcore.WebConfig{RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath}
	m := newHubHostManager(appsource.NewRegistry(), nil, webCfg, configPath, hosts, t.Logf)
	configReadFile = previous

	if _, _, imported := store.HostRecords(); imported {
		t.Fatal("a boot that could not read hub.toml moved records into the store")
	}
	if !bytes.Equal(fileBytes(t, configPath), original) {
		t.Fatal("hub.toml changed although it could not be read")
	}
	if m.mutationRefusal() == nil {
		t.Fatal("host changes are taken while the records are unsettled")
	}

	m = restartOver(t, configPath, store)

	requireFamilyEquals(t, store, records)
	if err := m.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused once hub.toml reads: %v", err)
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}}); err != nil {
		t.Fatalf("Add after the records moved: %v", err)
	}
}

// TestAnUnreadableRecordDoesNotHideAnOpenTeardownRemnant pins the fence's
// independence: a record this build cannot read pauses host changes, but an
// open teardown remnant in the same family still fences its host, so attach,
// deploy and restart see unresolved cleanup the same as on any other boot.
func TestAnUnreadableRecordDoesNotHideAnOpenTeardownRemnant(t *testing.T) {
	configPath, records := legacyHub(t)
	stateRoot := t.TempDir()
	_, store := bootOverStore(t, configPath, stateRoot)
	family, _, _ := store.HostRecords()
	for key := range family.Receipts {
		family.Receipts[key] = json.RawMessage(`{"from_a_newer_build":true}`)
		break
	}
	// Nor may a remnant or an attempt this build cannot read.
	family.Remnants["remnant-from-a-newer-build"] = json.RawMessage(`{"from_a_newer_build":true}`)
	// An open remnant that names its host keeps that host fenced even when this
	// build cannot read the rest of it.
	family.Remnants["open-remnant-from-a-newer-build"] = json.RawMessage(`{"host":"keep","from_a_newer_build":true}`)
	family.Attempts = map[string]json.RawMessage{"attempt-from-a-newer-build": json.RawMessage(`{"from_a_newer_build":true}`)}
	if err := store.CommitHostRecords(family, hostops.HostMirror{}); err != nil {
		t.Fatalf("tamper with a receipt: %v", err)
	}
	fenced := ""
	for _, remnant := range records.TeardownRemnants {
		if remnant.Resolved == nil {
			fenced = remnant.Host
		}
	}
	if fenced == "" {
		t.Fatal("the legacy fixture left no open remnant")
	}

	m, _ := bootOverStore(t, configPath, stateRoot)

	if m.mutationRefusal() == nil {
		t.Fatal("host changes are taken while a record is unreadable")
	}
	if _, open := m.openRemnantID(fenced); !open {
		t.Fatalf("the open remnant for %q is hidden by an unrelated unreadable record", fenced)
	}
	if _, open := m.openRemnantID("keep"); !open {
		t.Fatal("an open remnant this build cannot read in full no longer fences its host")
	}
}

// TestAReadFailureAfterTheLegacyBootStartedStillKeepsTheRecordsInHubTOML pins
// the guard on the actual load rather than on a read before it: hub.toml reads
// once and then fails, as a transient error can, and nothing may be imported
// from what the failed load left empty.
func TestAReadFailureAfterTheLegacyBootStartedStillKeepsTheRecordsInHubTOML(t *testing.T) {
	configPath, records := legacyHub(t)
	original := fileBytes(t, configPath)
	cfg := mustLoadConfig(t, configPath)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	doc := hostBootDocument(cfg, store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}

	previous := configReadFile
	reads := 0
	configReadFile = func(path string) ([]byte, error) {
		reads++
		if reads > 1 {
			return nil, errors.New("injected read failure")
		}
		return previous(path)
	}
	webCfg := hubcore.WebConfig{RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath}
	newHubHostManager(appsource.NewRegistry(), nil, webCfg, configPath, hosts, t.Logf)
	configReadFile = previous

	if _, _, imported := store.HostRecords(); imported {
		t.Fatal("a load that failed was imported as the hub's records")
	}
	if !bytes.Equal(fileBytes(t, configPath), original) {
		t.Fatal("hub.toml changed although its records could not be read")
	}
	restartOver(t, configPath, store)
	requireFamilyEquals(t, store, records)
}

// TestMigrationWaitsForADurableCopyOfTheOriginalHubTOML pins the order the
// promise needs: the original hub.toml is kept before anything rewrites it, and
// a copy that cannot be kept (here a directory in its place) stops the move and
// leaves the file as it was until it can.
func TestMigrationWaitsForADurableCopyOfTheOriginalHubTOML(t *testing.T) {
	configPath, records := legacyHub(t)
	original := fileBytes(t, configPath)
	copyPath := configPath + hostPreRecordsSuffix
	if err := os.Mkdir(copyPath, 0o700); err != nil {
		t.Fatalf("occupy the copy path: %v", err)
	}
	cfg := mustLoadConfig(t, configPath)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	doc := hostBootDocument(cfg, store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath,
	}, configPath, hosts, t.Logf)

	if _, _, imported := store.HostRecords(); imported {
		t.Fatal("the records moved although the original could not be kept")
	}
	if !bytes.Equal(fileBytes(t, configPath), original) {
		t.Fatal("hub.toml was rewritten although the original could not be kept")
	}
	if m.mutationRefusal() == nil {
		t.Fatal("host changes are taken while the move is stopped")
	}

	if err := os.Remove(copyPath); err != nil {
		t.Fatalf("clear the copy path: %v", err)
	}
	m = restartOver(t, configPath, store)

	requireFamilyEquals(t, store, records)
	if !bytes.Equal(fileBytes(t, copyPath), original) {
		t.Fatal("the kept copy is not the original hub.toml")
	}
	if err := m.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused after the move: %v", err)
	}
}

// TestMigrationKeepsTheCountersOfANameOnlyTheMirrorRemembers pins the import's
// handling of a boundary the family would otherwise lack: the legacy pass that
// writes such a boundary's counters into hub.toml can fail to land, and the
// import must not forget a generation and presence epoch the mirror holds.
func TestMigrationKeepsTheCountersOfANameOnlyTheMirrorRemembers(t *testing.T) {
	fixture := newPipelineFixture(t, []hostreg.Host{pipelineEntry("keep", 3)}, nil)
	boundary := hostops.Boundary{Generation: 9, IncarnationID: "inc-ghost", PresenceEpoch: 5}
	if err := fixture.store.MirrorHostState(hostops.HostMirror{Boundaries: map[string]hostops.Boundary{"ghost": boundary}}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	m := fixture.legacyManager(t)

	m.migrateLegacyRecords(fixture.store)

	mark := mustRecords(t, fixture.store).Generations["ghost"]
	if mark.Generation != boundary.Generation || mark.IncarnationID != boundary.IncarnationID || mark.PresenceEpoch != boundary.PresenceEpoch {
		t.Fatalf("the family holds %+v for ghost, want the mirror's %+v", mark, boundary)
	}
	if got := fixture.store.Boundaries()["ghost"]; got != boundary {
		t.Fatalf("the mirror holds %+v for ghost after the import, want %+v", got, boundary)
	}

	// The manager carries the counters on: an unrelated host change keeps them,
	// and a re-add of the name mints above them.
	if _, err := m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}, MutationID: newTestMutationID()}); err != nil {
		t.Fatalf("Add(extra): %v", err)
	}
	if kept := mustRecords(t, fixture.store).Generations["ghost"]; kept != mark {
		t.Fatalf("an unrelated host change left ghost's counters as %+v, want %+v", kept, mark)
	}
	if _, err := m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "ghost", Address: "ghost.example"}, MutationID: newTestMutationID()}); err != nil {
		t.Fatalf("Add(ghost): %v", err)
	}
	readded := mustRecords(t, fixture.store).Generations["ghost"]
	if readded.Generation <= boundary.Generation || readded.PresenceEpoch <= boundary.PresenceEpoch {
		t.Fatalf("the re-add of ghost minted %+v, want generation and presence epoch above %+v", readded, boundary)
	}
}

// TestMigrationRaisesAGenerationBelowTheMirrorsForARemovedName covers the other
// way the family can fall behind the mirror: the legacy records carry a mark for
// a removed name that is lower than the one the mirror holds (a high-water write
// that never landed), and the import must keep the higher generation, tombstone
// twin included.
func TestMigrationRaisesAGenerationBelowTheMirrorsForARemovedName(t *testing.T) {
	configPath, records := legacyHub(t)
	removed := firstKey(records.Tombstones)
	if removed == "" {
		t.Fatal("the legacy fixture left no tombstone")
	}
	held := records.Tombstones[removed]
	cfg := mustLoadConfig(t, configPath)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	spent := held.Generation + 4
	if err := store.MirrorHostState(hostops.HostMirror{Boundaries: map[string]hostops.Boundary{
		removed: {Generation: spent, IncarnationID: held.IncarnationID, PresenceEpoch: held.PresenceEpoch},
	}}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	doc := hostBootDocument(cfg, store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	legacy := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: configPath,
	}, configPath, hosts, t.Logf)
	legacy.cfg.ops.Store(store)

	legacy.migrateLegacyRecords(store)

	family := mustRecords(t, store)
	if family.Generations[removed].Generation != spent || family.Tombstones[removed].Generation != spent {
		t.Fatalf("the family holds mark %+v and tombstone generation %d for %q, want the mirror's %d", family.Generations[removed], family.Tombstones[removed].Generation, removed, spent)
	}
}

// TestMigrationRaisesAPresenceEpochTheMirrorHoldsEvenAtTheSameGeneration is the
// epoch half of the mirror rule: the two counters rise on their own, and the
// tombstone twin follows the mark so the family still validates.
func TestMigrationRaisesAPresenceEpochTheMirrorHoldsEvenAtTheSameGeneration(t *testing.T) {
	configPath, records := legacyHub(t)
	removed := firstKey(records.Tombstones)
	held := records.Tombstones[removed]
	cfg := mustLoadConfig(t, configPath)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	spentEpoch := held.PresenceEpoch + 3
	if err := store.MirrorHostState(hostops.HostMirror{Boundaries: map[string]hostops.Boundary{
		removed: {Generation: held.Generation, IncarnationID: held.IncarnationID, PresenceEpoch: spentEpoch},
	}}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	doc := hostBootDocument(cfg, nil)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	legacy := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: configPath,
	}, configPath, hosts, t.Logf)
	legacy.cfg.ops.Store(store)

	legacy.migrateLegacyRecords(store)

	family := mustRecords(t, store)
	if family.Generations[removed].PresenceEpoch != spentEpoch || family.Tombstones[removed].PresenceEpoch != spentEpoch {
		t.Fatalf("the family holds mark %+v and tombstone epoch %d for %q, want the mirror's epoch %d", family.Generations[removed], family.Tombstones[removed].PresenceEpoch, removed, spentEpoch)
	}
}

// TestMigrationRaisesARestoredHostTheRegistryNeverHeldToTheMirrorsCounters
// covers a host only the restored hub.toml declares: the registry never held
// it, so seeding cannot raise it, and the import must not replace the mirror's
// higher counters with the entry's own.
func TestMigrationRaisesARestoredHostTheRegistryNeverHeldToTheMirrorsCounters(t *testing.T) {
	fixture := newPipelineFixture(t, []hostreg.Host{pipelineEntry("keep", 3)}, nil)
	m := fixture.legacyManager(t)
	sibling := hostreg.Host{Name: "sibling", SSH: "sibling.example", Generation: 2, IncarnationID: "inc-sibling", PresenceEpoch: 1}
	m.cfg.store.set(append(m.cfg.store.snapshot(), sibling))
	boundary := hostops.Boundary{Generation: 6, IncarnationID: sibling.IncarnationID, PresenceEpoch: 4}
	if err := fixture.store.MirrorHostState(hostops.HostMirror{Boundaries: map[string]hostops.Boundary{"sibling": boundary}}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}

	m.migrateLegacyRecords(fixture.store)

	mark := mustRecords(t, fixture.store).Generations["sibling"]
	if mark.Generation < boundary.Generation || mark.PresenceEpoch < boundary.PresenceEpoch {
		t.Fatalf("the family holds %+v for the restored host, want counters at or above the mirror's %+v", mark, boundary)
	}
	if got := fixture.store.Boundaries()["sibling"]; got.Generation < boundary.Generation || got.PresenceEpoch < boundary.PresenceEpoch {
		t.Fatalf("the mirror holds %+v after the import, want at or above %+v", got, boundary)
	}
}

// TestMigrationWaitsWhenTheOriginalCannotBeInspectedForTheCopy pins that a read
// failure while deciding whether the original is worth keeping is not taken for
// a file without records: the move waits, and no copy-less rewrite happens.
func TestMigrationWaitsWhenTheOriginalCannotBeInspectedForTheCopy(t *testing.T) {
	configPath, records := legacyHub(t)
	original := fileBytes(t, configPath)
	cfg := mustLoadConfig(t, configPath)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	doc := hostBootDocument(cfg, store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	previous := configReadFile
	reads := 0
	configReadFile = func(path string) ([]byte, error) {
		reads++
		if reads == 1 {
			return nil, errors.New("injected read failure")
		}
		return previous(path)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath,
	}, configPath, hosts, t.Logf)
	configReadFile = previous

	if _, _, imported := store.HostRecords(); imported {
		t.Fatal("the records moved although the original could not be inspected")
	}
	if !bytes.Equal(fileBytes(t, configPath), original) {
		t.Fatal("hub.toml changed although the original could not be inspected")
	}
	if _, err := os.Stat(configPath + hostPreRecordsSuffix); err == nil {
		t.Fatal("a copy was kept of a file that was never read")
	}
	if m.mutationRefusal() == nil {
		t.Fatal("host changes are taken while the move waits")
	}

	m = restartOver(t, configPath, store)

	requireFamilyEquals(t, store, records)
	if !bytes.Equal(fileBytes(t, configPath+hostPreRecordsSuffix), original) {
		t.Fatal("the kept copy is not the original hub.toml")
	}
	if err := m.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused after the move: %v", err)
	}
}
