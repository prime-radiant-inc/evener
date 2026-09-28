package hub

// The deploy pipeline's boot reconciliation (spec 08b §7, §9, §4): the
// cross-file intent orderings, the compensation arms, the tombstone-derived
// host-removed pass, and the generation-mirror reconciliation — driven through
// the real manager construction path (newHubHostManager), so the passes run
// exactly where the hub runs them.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// pipelineFixture is one hub's durable pair — a hub.toml and an operation store
// — seeded by a test and then handed to the real manager constructor, which is
// where §7's boot passes run.
type pipelineFixture struct {
	dir        string
	configPath string
	store      *hostops.Store
	registry   *hostreg.Registry
}

// newPipelineFixture opens a store under a fresh state root, lets seed populate
// it, writes the hub.toml carrying entries (with their machine records), and
// returns the pair. The manager is constructed later by manager().
func newPipelineFixture(t *testing.T, entries []hostreg.Host, seed func(*hostops.Store)) *pipelineFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := hostops.Open(hostops.StorePath(dir))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	if seed != nil {
		seed(store)
	}
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, entries); err != nil {
		t.Fatalf("writeHubTOMLHosts: %v", err)
	}
	registry, err := hostreg.New(entries)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	return &pipelineFixture{dir: dir, configPath: configPath, store: store, registry: registry}
}

// appendTOML appends raw TOML sections to the fixture's hub.toml.
func (f *pipelineFixture) appendTOML(t *testing.T, body string) {
	t.Helper()
	file, err := os.OpenFile(f.configPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open hub.toml: %v", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString("\n" + body); err != nil {
		t.Fatalf("append hub.toml: %v", err)
	}
}

// manager constructs the real manager over the fixture's pair, running §7's
// boot passes the way the hub does.
func (f *pipelineFixture) manager(t *testing.T) *hubHostManager {
	t.Helper()
	cfg := hubcore.WebConfig{
		RemoteHostRegistry:   f.registry,
		RemoteHostOpsStore:   f.store,
		RemoteHostConfigPath: f.configPath,
	}
	return newHubHostManager(nil, nil, cfg, f.configPath, f.registry, nil)
}

// pipelineEntry is one host entry with a complete persisted identity.
func pipelineEntry(name string, generation uint64) hostreg.Host {
	return hostreg.Host{
		Name:          name,
		SSH:           name + ".example",
		Generation:    generation,
		IncarnationID: "inc-" + name,
		PresenceEpoch: 3,
	}
}

// pipelineToken mints one valid token row for host through the store's own mint
// path.
func pipelineToken(t *testing.T, store *hostops.Store, host string, generation uint64) hostops.Token {
	t.Helper()
	token, err := store.MintToken(hostops.MintRequest{
		Host:               host,
		Generation:         generation,
		IncarnationID:      "inc-" + host,
		EntryHash:          hubTOMLFingerprint([]byte("entry-" + host)),
		HubTOMLFingerprint: hubTOMLFingerprint([]byte("toml-" + host)),
		FactsRevision:      hubTOMLFingerprint([]byte("facts-" + host)),
		FactsCapturedAt:    time.Now().UTC(),
		TargetPath:         "/opt/evener",
		ControllerRevision: "v1.2.3",
		RunningVersion:     "v1.1.0",
		RunningHealthy:     true,
		FreshnessBound:     hostops.DefaultFreshnessBound,
		TTL:                hostops.DefaultTokenTTL,
	})
	if err != nil {
		t.Fatalf("MintToken(%s): %v", host, err)
	}
	return token
}

// newPipelineRemovalFixture models a hub mid-removal: the registry is empty
// (the committed file carries no host), the current hub.toml is the
// post-commit one, and the stash holds the pre-remove bytes — so only a
// compensation's own restore can put the host back into the file.
func newPipelineRemovalFixture(t *testing.T, entry hostreg.Host, registered bool) (*pipelineFixture, hostops.Token, []byte) {
	t.Helper()
	var token hostops.Token
	var registryEntries []hostreg.Host
	if registered {
		registryEntries = []hostreg.Host{entry}
	}
	fixture := newPipelineFixture(t, registryEntries, func(store *hostops.Store) {
		token = pipelineToken(t, store, entry.Name, entry.Generation)
	})
	if err := writeHubTOMLHosts(fixture.configPath, []hostreg.Host{entry}); err != nil {
		t.Fatalf("write pre-remove hub.toml: %v", err)
	}
	preRemove, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	if err := os.WriteFile(hubTOMLStashPath(fixture.configPath, "test-key"), preRemove, 0o600); err != nil {
		t.Fatalf("write stash: %v", err)
	}
	if err := writeHubTOMLHosts(fixture.configPath, nil); err != nil {
		t.Fatalf("write committed hub.toml: %v", err)
	}
	return fixture, token, preRemove
}

// hubTOMLCarriesIntent reports whether the fixture's hub.toml still carries a
// pending_store_sync section for name.
func (f *pipelineFixture) carriesIntent(t *testing.T, name string) bool {
	t.Helper()
	cfg, ok := readPipelineConfig(t, f.configPath)
	if !ok {
		return false
	}
	_, carried := cfg.PendingStoreSync[name]
	return carried
}

// readPipelineConfig decodes the fixture's hub.toml.
func readPipelineConfig(t *testing.T, path string) (Config, bool) {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", path, err)
	}
	return cfg, true
}

// TestBootStoreSyncReappliesAPurgeTheStoreMissed pins §9's swap-landed/
// purge-missing ordering: the file carries the intent, the store still holds
// the row, boot re-applies the purge and clears the intent in its own follow-up
// write, and a second boot writes nothing.
func TestBootStoreSyncReappliesAPurgeTheStoreMissed(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	var token hostops.Token
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		token = pipelineToken(t, store, "m4", 2)
	})
	fixture.appendTOML(t, "[pending_store_sync.m4]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")

	m := fixture.manager(t)
	if _, ok := m.cfg.ops.OutstandingToken("m4"); ok {
		t.Fatal("boot did not re-apply the intent's purge")
	}
	if fixture.carriesIntent(t, "m4") {
		t.Fatal("boot did not clear the converged intent")
	}
	// A second boot has nothing left to do: the store file is untouched.
	before, err := os.ReadFile(hostops.StorePath(fixture.dir))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	fixture.manager(t)
	after, err := os.ReadFile(hostops.StorePath(fixture.dir))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a converged boot rewrote the store")
	}
}

// TestBootStoreSyncClearsAnIntentWhosePurgeLanded pins §9's purge-landed/
// intent-present ordering: the store already holds no matching row, so boot
// clears the intent without resurrecting anything and without touching the
// store.
func TestBootStoreSyncClearsAnIntentWhosePurgeLanded(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		// The mirror is already converged: this test is about the intent, and a
		// fresh store would make the boot's mirror pass write (correctly) and
		// muddy the "no store write" assertion below.
		if err := store.MirrorBoundaries(map[string]hostops.Boundary{
			"m4": {Generation: 2, IncarnationID: "inc-m4", PresenceEpoch: 3},
		}, nil); err != nil {
			t.Fatalf("MirrorBoundaries: %v", err)
		}
	})
	fixture.appendTOML(t, "[pending_store_sync.m4]\ngeneration = 2\ntoken_values = [\"token-that-was-purged\"]\n")
	before, err := os.ReadFile(hostops.StorePath(fixture.dir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read store: %v", err)
	}
	m := fixture.manager(t)
	if fixture.carriesIntent(t, "m4") {
		t.Fatal("boot did not clear the converged intent")
	}
	if _, ok := m.cfg.ops.OutstandingToken("m4"); ok {
		t.Fatal("boot resurrected a row the intent's clear must not")
	}
	after, err := os.ReadFile(hostops.StorePath(fixture.dir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read store: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a converged intent's clear rewrote the store")
	}
}

// TestBootCompensationArmedWithRowsPresentRestoresHubTOML pins §9's armed arm
// with the purge missing: the intent is present and the rows are still there,
// so boot restores hub.toml from the stash, leaves the rows untouched, and
// clears the record and its stash.
func TestBootCompensationArmedWithRowsPresentRestoresHubTOML(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture, token, _ := newPipelineRemovalFixture(t, entry, true)
	fixture.appendTOML(t, "[pending_store_sync.m4]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "m4", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: hubTOMLStashPath(fixture.configPath, "test-key"), Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}

	m := fixture.manager(t)
	if _, ok := m.cfg.ops.Compensation("m4"); ok {
		t.Fatal("boot did not clear the converged compensation record")
	}
	if _, err := os.Stat(hubTOMLStashPath(fixture.configPath, "test-key")); !os.IsNotExist(err) {
		t.Fatal("boot did not prune the cleared compensation's stash")
	}
	if _, ok := m.cfg.ops.OutstandingToken("m4"); !ok {
		t.Fatal("boot dropped rows the purge never landed for")
	}
	// The restored host is actually served: the runtime arm re-applied it to
	// the live registry before the record cleared.
	served, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, listed := hostRowNamed(served.Hosts, "m4"); !listed {
		t.Fatalf("the restored host is not served: %+v", served.Hosts)
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, live := hostEntryNamed(hostRegistryEntries(cfg), "m4"); !live {
		t.Fatal("boot did not restore hub.toml from the stash")
	}
	// A second boot over the converged pair serves the host too, and the
	// compensation does not come back.
	second := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostRegistry:   fixture.registry,
		RemoteHostOpsStore:   fixture.store,
		RemoteHostConfigPath: fixture.configPath,
	}, fixture.configPath, fixture.registry, nil)
	served, err = second.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List (second boot): %v", err)
	}
	if _, listed := hostRowNamed(served.Hosts, "m4"); !listed {
		t.Fatalf("the second boot does not serve the restored host: %+v", served.Hosts)
	}
}

// hostRowNamed returns the listed row for name, if the page carries one.
func hostRowNamed(rows []appwire.HostRow, name string) (appwire.HostRow, bool) {
	for _, row := range rows {
		if row.Name == name {
			return row, true
		}
	}
	return appwire.HostRow{}, false
}

// TestBootCompensationPurgeLandedReinsertsTheRows pins §9's hubtoml arm: the
// purge landed before the crash, so boot restores hub.toml from the stash, then
// re-inserts exactly the rows the restored generation revalidates, then clears.
func TestBootCompensationPurgeLandedReinsertsTheRows(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture, token, _ := newPipelineRemovalFixture(t, entry, true)
	fixture.appendTOML(t, "[pending_store_sync.m4]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "m4", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: hubTOMLStashPath(fixture.configPath, "test-key"), Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	// The purge landed: the row is gone and the record advanced past armed.
	if purged, err := fixture.store.PurgeCompensated("m4", []string{token.Value}); err != nil || purged != 1 {
		t.Fatalf("PurgeCompensated = %d/%v, want 1/nil", purged, err)
	}

	m := fixture.manager(t)
	restored, ok := m.cfg.ops.OutstandingToken("m4")
	if !ok || restored.Value != token.Value || restored.Generation != 2 || restored.IncarnationID != "inc-m4" {
		t.Fatalf("re-inserted row = %+v/%v, want the preimage row at the restored identity", restored, ok)
	}
	if _, ok := m.cfg.ops.Compensation("m4"); ok {
		t.Fatal("boot did not clear the converged compensation record")
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, live := hostEntryNamed(hostRegistryEntries(cfg), "m4"); !live {
		t.Fatal("boot did not restore hub.toml from the stash")
	}
}

// TestBootCompensationWithIntentClearedClearsWithoutResurrection pins §9's
// armed arm with the commit past its commit point: the intent is already
// cleared, so the record clears without restoring hub.toml or resurrecting
// rows.
func TestBootCompensationWithIntentClearedClearsWithoutResurrection(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture, token, _ := newPipelineRemovalFixture(t, entry, false)
	// The commit path passed its commit point: the purge landed and the intent
	// was cleared, leaving only the armed record behind.
	if _, err := fixture.store.ApplyStoreSync(hostops.StoreSyncIntent{Host: "m4", Generation: 2, Values: []string{token.Value}}); err != nil {
		t.Fatalf("purge the row: %v", err)
	}
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "m4", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: filepath.Join(fixture.dir, "absent.stash"), Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	m := fixture.manager(t)
	if _, ok := m.cfg.ops.Compensation("m4"); ok {
		t.Fatal("boot did not clear the record whose intent was already cleared")
	}
	if _, ok := m.cfg.ops.OutstandingToken("m4"); ok {
		t.Fatal("boot resurrected a row the commit had purged")
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, live := hostEntryNamed(hostRegistryEntries(cfg), "m4"); live {
		t.Fatal("boot restored hub.toml over a committed removal")
	}
}

// TestRemovePurgesTokensThroughTheIntent pins §9's live remove half: the
// removal's hub.toml commit carries the intent, the store purge drops the row,
// the follow-up write clears the intent, and the committed record and stash do
// not survive the commit point.
func TestRemovePurgesTokensThroughTheIntent(t *testing.T) {
	entry := pipelineEntry("keep", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		pipelineToken(t, store, "keep", 2)
	})
	m := fixture.manager(t)
	if _, ok := m.cfg.ops.OutstandingToken("keep"); !ok {
		t.Fatal("the fixture's token row is missing")
	}
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "keep")); err != nil {
		t.Fatalf("Remove(keep): %v", err)
	}
	if _, ok := m.cfg.ops.OutstandingToken("keep"); ok {
		t.Fatal("the committed removal left its token row behind")
	}
	if _, ok := m.cfg.ops.Compensation("keep"); ok {
		t.Fatal("the committed removal left its compensation record open")
	}
	if _, err := os.Stat(hubTOMLStashPath(fixture.configPath, "test-key")); !os.IsNotExist(err) {
		t.Fatal("the committed removal left its stash behind")
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, carried := cfg.PendingStoreSync["keep"]; carried {
		t.Fatal("the committed removal left its intent in hub.toml")
	}
	if _, carried := cfg.Tombstones["keep"]; !carried {
		t.Fatal("the committed removal did not tombstone the host")
	}
}

// TestRemoveWithoutATokenRowCarriesNoIntentOrStash pins the no-op half: a
// removal with no outstanding token row has nothing to sync, so it stages no
// intent and leaves no stash behind.
func TestRemoveWithoutATokenRowCarriesNoIntentOrStash(t *testing.T) {
	entry := pipelineEntry("keep", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, nil)
	m := fixture.manager(t)
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "keep")); err != nil {
		t.Fatalf("Remove(keep): %v", err)
	}
	if _, err := os.Stat(hubTOMLStashPath(fixture.configPath, "test-key")); !os.IsNotExist(err) {
		t.Fatal("a removal with nothing to sync left a stash behind")
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if len(cfg.PendingStoreSync) != 0 {
		t.Fatalf("a removal with nothing to sync left intents: %+v", cfg.PendingStoreSync)
	}
	if _, open := fixture.store.Compensation("keep"); open {
		t.Fatal("a removal with nothing to sync armed a compensation")
	}
}

// TestBootHostRemovedPassMarksTheRemovedIncarnationsRecords pins §4's
// tombstone-derived pass: after a removal's hub.toml commit, a crash before the
// operation-store mark recovers at boot — only the removed incarnation's
// records are marked, a re-add's record is not, and the removed host's token
// rows drop.
func TestBootHostRemovedPassMarksTheRemovedIncarnationsRecords(t *testing.T) {
	entry := pipelineEntry("keep", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		pipelineToken(t, store, "keep", 2)
		if _, err := store.Create(hostops.NewRecord{
			ClientOperationID: "op-removed", Host: "keep", Kind: hostops.KindDeploy,
			Generation: 2, IncarnationID: "inc-keep",
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		// A re-add's record carries a different incarnation id: the tombstone
		// must never match it.
		if _, err := store.Create(hostops.NewRecord{
			ClientOperationID: "op-readd", Host: "keep", Kind: hostops.KindRestart,
			Generation: 9, IncarnationID: "inc-readd",
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	})
	m := fixture.manager(t)
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "keep")); err != nil {
		t.Fatalf("Remove(keep): %v", err)
	}
	// Boot a second manager over the same pair, this time with the registry the
	// file's own (host-less) live set implies: the pass must mark the removed
	// incarnation's record and leave the re-add's alone.
	second := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore:   fixture.store,
		RemoteHostConfigPath: fixture.configPath,
	}, fixture.configPath, nil, nil)
	records := second.cfg.ops.Records()
	var removedMarked, readdMarked bool
	for _, record := range records {
		switch record.ClientOperationID {
		case "op-removed":
			removedMarked = record.HostRemoved
		case "op-readd":
			readdMarked = record.HostRemoved
		}
	}
	if len(records) == 0 {
		t.Fatal("the fixture's records are missing")
	}
	if !removedMarked {
		t.Fatal("the boot host-removed pass did not mark the removed incarnation's record")
	}
	if readdMarked {
		t.Fatal("the boot host-removed pass marked a re-add's record")
	}
}

// TestBootMirrorRollsBackATornStoreAndWritesTheHighWater pins §4/§7's rollback
// end to end: a store mirror ahead of hub.toml with no commit marker rolls back
// to the file's identity, the discarded number is written back as the name's
// [generations] high-water, and the discarded generation's in-flight record is
// interrupted naming the torn write — no startup refusal.
func TestBootMirrorRollsBackATornStoreAndWritesTheHighWater(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		if err := store.MirrorBoundaries(map[string]hostops.Boundary{
			"m4": {Generation: 9, IncarnationID: "inc-mirror", PresenceEpoch: 5},
		}, nil); err != nil {
			t.Fatalf("MirrorBoundaries: %v", err)
		}
		if _, err := store.Create(hostops.NewRecord{
			ClientOperationID: "op-torn", Host: "m4", Kind: hostops.KindDeploy,
			Generation: 9, IncarnationID: "inc-mirror",
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	})
	m := fixture.manager(t)
	mirror, ok := m.cfg.ops.Boundary("m4")
	if !ok || mirror.Generation != 9 || mirror.IncarnationID != "inc-m4" {
		t.Fatalf("mirror after the boot rollback = %+v, want generation 9 with the file's incarnation", mirror)
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if mark := cfg.Generations["m4"]; mark.Generation != 9 {
		t.Fatalf("hub.toml high-water = %+v, want the discarded generation 9 recorded", mark)
	}
	records := m.cfg.ops.Records()
	if len(records) != 1 || records[0].State != hostops.StateInterrupted || records[0].Result == nil || records[0].Result.Message != hostops.TornWriteNote {
		t.Fatalf("record after the boot rollback = %+v, want interrupted with the torn-write note", records)
	}
}

// TestBootMirrorPushesAFileMarkForward pins the other direction: a hub.toml
// mark newer than the store mirror lands in the mirror in the same boot pass.
func TestBootMirrorPushesAFileMarkForward(t *testing.T) {
	entry := pipelineEntry("m4", 7)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		if err := store.MirrorBoundaries(map[string]hostops.Boundary{
			"m4": {Generation: 2, IncarnationID: "inc-old", PresenceEpoch: 1},
		}, nil); err != nil {
			t.Fatalf("MirrorBoundaries: %v", err)
		}
	})
	m := fixture.manager(t)
	mirror, ok := m.cfg.ops.Boundary("m4")
	if !ok || mirror.Generation != 7 || mirror.IncarnationID != "inc-m4" {
		t.Fatalf("mirror after the boot push-forward = %+v, want the file mark", mirror)
	}
}

// TestBootMirrorPreservesAMirrorWithoutAFileEntry pins §4's preserve arm: a
// name hub.toml carries no entry for keeps its mirror, and the surviving mirror
// is written back as the high-water mark — never rolled back.
func TestBootMirrorPreservesAMirrorWithoutAFileEntry(t *testing.T) {
	entry := pipelineEntry("other", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		if err := store.MirrorBoundaries(map[string]hostops.Boundary{
			"gone": {Generation: 11, IncarnationID: "inc-gone", PresenceEpoch: 4},
		}, nil); err != nil {
			t.Fatalf("MirrorBoundaries: %v", err)
		}
	})
	m := fixture.manager(t)
	mirror, ok := m.cfg.ops.Boundary("gone")
	if !ok || mirror.Generation != 11 || mirror.IncarnationID != "inc-gone" {
		t.Fatalf("preserved mirror = %+v, want the store's value", mirror)
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if mark := cfg.Generations["gone"]; mark.Generation != 11 || mark.IncarnationID != "inc-gone" {
		t.Fatalf("hub.toml high-water for the surviving mirror = %+v, want the mirror triple", mark)
	}
}

// TestBootCompensationRuntimeLeavesTheRecordWhenTheRestoredFileIsMissing pins
// §9's runtime arm's failure posture: a runtime-phase record whose restored
// hub.toml cannot be read stays in `compensating-runtime` with its stash
// intact, never a cleared compensation beside a diverged runtime.
func TestBootCompensationRuntimeLeavesTheRecordWhenTheRestoredFileIsMissing(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	var token hostops.Token
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		token = pipelineToken(t, store, "m4", 2)
	})
	stash := filepath.Join(fixture.dir, "live.stash")
	if err := os.WriteFile(stash, []byte("[hosts]\n"), 0o600); err != nil {
		t.Fatalf("write stash: %v", err)
	}
	m := fixture.manager(t)
	// Arm a runtime-phase record and drive the pass directly with an injected
	// runtime-revert failure: §9 pins the record left in `compensating-runtime`
	// with its stash intact, never cleared.
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "m4", Phase: hostops.CompensationRuntime, Rows: []hostops.Token{token},
		Stash: stash, Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	m.testOnlyFailRuntimeRevert = func(string) error { return errors.New("injected runtime-revert failure") }
	m.reconcileCompensations()
	record, ok := m.cfg.ops.Compensation("m4")
	if !ok || hostops.NormalizeCompensationPhase(record.Phase) != hostops.CompensationRuntime {
		t.Fatalf("record after a failed runtime revert = %+v/%v, want it left in %s", record, ok, hostops.CompensationRuntime)
	}
	if _, err := os.Stat(stash); err != nil {
		t.Fatalf("the failed runtime revert pruned the stash: %v", err)
	}
	// The retry, with the failure gone, converges and clears.
	m.testOnlyFailRuntimeRevert = nil
	m.reconcileCompensations()
	if _, ok := m.cfg.ops.Compensation("m4"); ok {
		t.Fatal("the retried compensation did not clear")
	}
	if _, err := os.Stat(stash); !os.IsNotExist(err) {
		t.Fatal("the converged compensation left its stash behind")
	}
}

// TestBootIntentSectionRoundTripsThroughTheWriter pins the writer: an intent a
// write carries lands in hub.toml's [pending_store_sync] table and a later
// write with the name owned drops it, so the section's shape is the one the
// loader validates.
func TestBootIntentSectionRoundTripsThroughTheWriter(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, nil)
	m := fixture.manager(t)
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{storeSync: &pendingHostStoreSync{
		Name:   "m4",
		Intent: HostStoreSyncIntent{Generation: 2, TokenValues: []string{"row-value"}},
	}}); err != nil {
		t.Fatalf("persistHosts: %v", err)
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	intent, carried := cfg.PendingStoreSync["m4"]
	if !carried || intent.Generation != 2 || len(intent.TokenValues) != 1 || intent.TokenValues[0] != "row-value" {
		t.Fatalf("intent after the carrying write = %+v/%v", intent, carried)
	}
	if err := m.persistHosts(entries, entries, hostPersistChange{dropStoreSync: "m4"}); err != nil {
		t.Fatalf("persistHosts(clear): %v", err)
	}
	cfg, _ = readPipelineConfig(t, fixture.configPath)
	if _, carried := cfg.PendingStoreSync["m4"]; carried {
		t.Fatal("the clearing write left the intent in hub.toml")
	}
}

// TestBootMirrorCommitMarkersRoundTripThroughTheWriter pins the marker's
// derivation: a write carrying a name's generation emits the (G, G) marker, so
// the mirror write that follows is authorized, and a later boot with the mirror
// at G keeps it.
func TestBootMirrorCommitMarkersRoundTripThroughTheWriter(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		if err := store.MirrorBoundaries(map[string]hostops.Boundary{
			"m4": {Generation: 2, IncarnationID: "inc-m4", PresenceEpoch: 3},
		}, nil); err != nil {
			t.Fatalf("MirrorBoundaries: %v", err)
		}
	})
	fixture.manager(t)
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	commit, carried := cfg.MirrorCommits["m4"]
	if !carried || commit.HubTOMLGeneration != 2 || commit.StoreGeneration != 2 {
		t.Fatalf("mirror commit marker = %+v/%v, want (2, 2)", commit, carried)
	}
}

// TestBootClearsAnIntentForANameTheFileDoesNotOwn pins the clear's ownership
// hole: an intent keyed by a name neither live nor tombstoned (a crash window's
// intent) must still clear — otherwise the boot pass rewrites hub.toml forever.
func TestBootClearsAnIntentForANameTheFileDoesNotOwn(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, nil)
	fixture.appendTOML(t, "[pending_store_sync.ghost]\ngeneration = 2\ntoken_values = [\"row-that-was-purged\"]\n")
	fixture.manager(t)
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, carried := cfg.PendingStoreSync["ghost"]; carried {
		t.Fatal("boot did not clear an intent for a name the file does not own")
	}
	before, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	fixture.manager(t)
	after, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the boot pass rewrote hub.toml again for an already-cleared intent")
	}
}

// TestBootCompensationHubTOMLWithIntentClearedDoesNotRestore pins §9's commit
// point on the hubtoml arm: an intent already cleared means the commit path
// passed the commit point, so the record clears without restoring hub.toml and
// without resurrecting the purged rows.
func TestBootCompensationHubTOMLWithIntentClearedDoesNotRestore(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture, token, _ := newPipelineRemovalFixture(t, entry, false)
	// The commit's follow-up write already cleared the intent while the purge
	// landed and the record advanced to the hub.toml phase: the commit path
	// passed its commit point.
	if _, err := fixture.store.ApplyStoreSync(hostops.StoreSyncIntent{Host: "m4", Generation: 2, Values: []string{token.Value}}); err != nil {
		t.Fatalf("purge the row: %v", err)
	}
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "m4", Phase: hostops.CompensationHubTOML, Rows: []hostops.Token{token},
		Stash: hubTOMLStashPath(fixture.configPath, "test-key"), Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	m := fixture.manager(t)
	if _, ok := m.cfg.ops.Compensation("m4"); ok {
		t.Fatal("boot did not clear the record whose intent was already cleared")
	}
	if _, ok := m.cfg.ops.OutstandingToken("m4"); ok {
		t.Fatal("boot resurrected a row the commit had purged")
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, live := hostEntryNamed(hostRegistryEntries(cfg), "m4"); live {
		t.Fatal("boot restored hub.toml over a committed removal")
	}
	if _, err := os.Stat(hubTOMLStashPath(fixture.configPath, "test-key")); !os.IsNotExist(err) {
		t.Fatal("boot left the cleared record's stash behind")
	}
}

// TestBootCompensationRuntimeLeavesTheRecordWhenTheRestoredHostIsNotRegistered
// pins the runtime arm's failure posture: the restored file carries a live host
// the boot registry did not register, so the record stays in
// `compensating-runtime` with its stash intact — and the next boot, whose
// registry loads the restored file, converges and clears it.
func TestBootCompensationRuntimeLeavesTheRecordWhenTheRestoredHostIsNotRegistered(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	var token hostops.Token
	fixture := newPipelineFixture(t, nil, func(store *hostops.Store) {
		token = pipelineToken(t, store, "m4", 2)
	})
	if err := writeHubTOMLHosts(fixture.configPath, []hostreg.Host{entry}); err != nil {
		t.Fatalf("write pre-remove hub.toml: %v", err)
	}
	preRemove, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	stash := hubTOMLStashPath(fixture.configPath, "test-key")
	if err := os.WriteFile(stash, preRemove, 0o600); err != nil {
		t.Fatalf("write stash: %v", err)
	}
	if err := writeHubTOMLHosts(fixture.configPath, nil); err != nil {
		t.Fatalf("rewrite hub.toml: %v", err)
	}
	fixture.appendTOML(t, "[pending_store_sync.m4]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "m4", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: stash, Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	if _, err := fixture.store.PurgeCompensated("m4", []string{token.Value}); err != nil {
		t.Fatalf("PurgeCompensated: %v", err)
	}
	// The registry is empty: the boot loaded the committed (host-less) file.
	m := fixture.manager(t)
	record, open := m.cfg.ops.Compensation("m4")
	if !open || hostops.NormalizeCompensationPhase(record.Phase) != hostops.CompensationRuntime {
		t.Fatalf("record after the unserved runtime revert = %+v/%v, want it left in %s", record, open, hostops.CompensationRuntime)
	}
	if _, err := os.Stat(stash); err != nil {
		t.Fatalf("the unserved runtime revert pruned the stash: %v", err)
	}
	// The next boot loads the restored file, so its registry carries the host
	// with the restored identity: the retry converges and clears.
	restored, _ := readPipelineConfig(t, fixture.configPath)
	registry, err := hostreg.New(hostRegistryEntries(restored))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	second := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostRegistry:   registry,
		RemoteHostOpsStore:   fixture.store,
		RemoteHostConfigPath: fixture.configPath,
	}, fixture.configPath, registry, nil)
	if _, open := second.cfg.ops.Compensation("m4"); open {
		t.Fatal("the next boot did not converge the left-open compensation")
	}
}

// TestBootCompensationRowsUseTheRegistryGenerationWithoutAFileMark pins the
// rows arm's generation source: when the restored file carries a live entry but
// no [generations] mark, the rows the live registry's generation revalidates
// come back — never nothing because the file recorded no number.
func TestBootCompensationRowsUseTheRegistryGenerationWithoutAFileMark(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	var token hostops.Token
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		token = pipelineToken(t, store, "m4", 2)
	})
	preRemove := "# hand-authored\n[[hosts]]\nname = \"m4\"\nssh = \"m4.example\"\n"
	stash := hubTOMLStashPath(fixture.configPath, "test-key")
	if err := os.WriteFile(stash, []byte(preRemove), 0o600); err != nil {
		t.Fatalf("write stash: %v", err)
	}
	if err := writeHubTOMLHosts(fixture.configPath, nil); err != nil {
		t.Fatalf("rewrite hub.toml: %v", err)
	}
	fixture.appendTOML(t, "[pending_store_sync.m4]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "m4", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: stash, Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	if _, err := fixture.store.PurgeCompensated("m4", []string{token.Value}); err != nil {
		t.Fatalf("PurgeCompensated: %v", err)
	}
	m := fixture.manager(t)
	row, ok := m.cfg.ops.OutstandingToken("m4")
	if !ok || row.Value != token.Value {
		t.Fatalf("re-inserted row = %+v/%v, want the preimage row revalidated by the registry's generation", row, ok)
	}
}

// TestMirrorHighWaterRaiseSurvivesALaterRemoval pins the raise's coherence:
// after a boot rollback raised a live name's high-water, a later removal writes
// a tombstone at the raised generation and the file still validates — the
// durable mark never drops below the discarded number.
func TestMirrorHighWaterRaiseSurvivesALaterRemoval(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		if err := store.MirrorBoundaries(map[string]hostops.Boundary{
			"m4": {Generation: 9, IncarnationID: "inc-mirror", PresenceEpoch: 5},
		}, nil); err != nil {
			t.Fatalf("MirrorBoundaries: %v", err)
		}
	})
	m := fixture.manager(t)
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "m4")); err != nil {
		t.Fatalf("Remove(m4): %v", err)
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if mark := cfg.Generations["m4"]; mark.Generation != 9 {
		t.Fatalf("the removal lowered the raised high-water: %+v", mark)
	}
	if tombstone := cfg.Tombstones["m4"]; tombstone.Generation != 9 {
		t.Fatalf("tombstone = %+v, want the raised generation 9", tombstone)
	}
}

// TestRemovePrunesTheStashWhenTheStagedWriteFails pins the stash's cleanup on
// the early failure paths: a removal whose staged write fails behind its rename
// compensates and leaves no stash behind.
func TestRemovePrunesTheStashWhenTheStagedWriteFails(t *testing.T) {
	entry := pipelineEntry("keep", 2)
	var token hostops.Token
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, func(store *hostops.Store) {
		token = pipelineToken(t, store, "keep", 2)
	})
	m := fixture.manager(t)
	_ = token
	flakyHubTOMLDirSync(t, func(call int) bool { return call == 1 })
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "keep")); err == nil {
		t.Fatal("Remove over a failing directory sync succeeded, want a refusal")
	}
	if _, err := os.Stat(hubTOMLStashPath(fixture.configPath, "test-key")); !os.IsNotExist(err) {
		t.Fatal("the compensated staged write left its stash behind")
	}
}

// TestMirrorHighWaterRaiseRaisesATombstonedNamesTwin pins the raise for a
// removed name: the [generations] high-water and the tombstone are one record
// pair (S11's validation ties them), so the boot rollback raises both to the
// discarded number and the file still validates.
func TestMirrorHighWaterRaiseRaisesATombstonedNamesTwin(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, nil)
	m := fixture.manager(t)
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "m4")); err != nil {
		t.Fatalf("Remove(m4): %v", err)
	}
	// The torn store mirror lands after the removal: the file still carries the
	// removed generation while the mirror is ahead.
	if err := fixture.store.MirrorBoundaries(map[string]hostops.Boundary{
		"m4": {Generation: 9, IncarnationID: "inc-mirror", PresenceEpoch: 5},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	// The boot rolls the mirror back and must raise the tombstoned name's
	// [generations] twin together with the tombstone (S11 ties the pair).
	fixture.manager(t)
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	mark, carried := cfg.Generations["m4"]
	if !carried || mark.Generation != 9 {
		t.Fatalf("generations high-water = %+v/%v, want the discarded generation 9", mark, carried)
	}
	tombstone, carried := cfg.Tombstones["m4"]
	if !carried || tombstone.Generation != 9 {
		t.Fatalf("tombstone = %+v/%v, want the raised generation 9", tombstone, carried)
	}
	if tombstone.IncarnationID != mark.IncarnationID || tombstone.PresenceEpoch != mark.PresenceEpoch {
		t.Fatalf("the tombstone/twin pair disagrees: %+v vs %+v", tombstone, mark)
	}
	// A follow-up boot finds the pair converged and writes nothing.
	before, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	fixture.manager(t)
	after, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a converged raise was rewritten by the next boot")
	}
}

// TestBootPrunesAnOrphanStash pins the stash's boot cleanup: a stash no open
// compensation record names (a crash between the stash write and the staged
// write) is pruned before the hub serves.
func TestBootPrunesAnOrphanStash(t *testing.T) {
	entry := pipelineEntry("m4", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{entry}, nil)
	stash := hubTOMLStashPath(fixture.configPath, "orphan-key")
	if err := os.WriteFile(stash, []byte("# orphaned stash\n"), 0o600); err != nil {
		t.Fatalf("write stash: %v", err)
	}
	fixture.manager(t)
	if _, err := os.Stat(stash); !os.IsNotExist(err) {
		t.Fatal("boot did not prune an orphaned stash")
	}
}

// TestBootRestoreInstallsTheMachineRecords pins the restore's record install:
// after a boot compensation restores hub.toml, the store's model carries the
// restored records — so the next hub.toml write re-emits no cleared intent, and
// the boot after that does not purge the very row the compensation restored.
func TestBootRestoreInstallsTheMachineRecords(t *testing.T) {
	entry := pipelineEntry("keep", 2)
	fixture, token, _ := newPipelineRemovalFixture(t, entry, false)
	fixture.appendTOML(t, "[pending_store_sync.keep]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")
	// The commit crashed after PurgeCompensated: the record advanced and the row
	// is gone.
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "keep", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: hubTOMLStashPath(fixture.configPath, "test-key"), Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	if _, err := fixture.store.PurgeCompensated("keep", []string{token.Value}); err != nil {
		t.Fatalf("PurgeCompensated: %v", err)
	}
	m := fixture.manager(t)
	if _, ok := m.cfg.ops.OutstandingToken("keep"); !ok {
		t.Fatal("boot did not re-insert the restored row")
	}
	// The next hub.toml write derives from the restored records: no intent.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "extra", Address: "extra.example"}}); err != nil {
		t.Fatalf("Add(extra): %v", err)
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, carried := cfg.PendingStoreSync["keep"]; carried {
		t.Fatal("the next hub.toml write re-emitted the compensated clear intent")
	}
	if _, ok := m.cfg.ops.OutstandingToken("keep"); !ok {
		t.Fatal("the write path purged the restored row")
	}
	// A follow-up boot must not purge the restored row either.
	second := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore:   fixture.store,
		RemoteHostConfigPath: fixture.configPath,
	}, fixture.configPath, nil, nil)
	if _, ok := second.cfg.ops.OutstandingToken("keep"); !ok {
		t.Fatal("the follow-up boot purged the restored row")
	}
}

// TestBootRestoreReversesTheHostRemovedMarks pins §4's reversal on the
// compensation path: a record the boot's host-removed pass marked (its
// incarnation was removed, then the compensation restored it) replays on a
// same-key retry instead of refusing as a current-generation `host-removed`
// record, and the row renders live.
func TestBootRestoreReversesTheHostRemovedMarks(t *testing.T) {
	entry := pipelineEntry("keep", 2)
	fixture, _, preRemove := newPipelineRemovalFixture(t, entry, true)
	if _, err := fixture.store.Create(hostops.NewRecord{
		ClientOperationID: "op-1", Host: "keep", Kind: hostops.KindDeploy,
		Generation: 2, IncarnationID: "inc-keep",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := fixture.manager(t)
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "keep")); err != nil {
		t.Fatalf("Remove(keep): %v", err)
	}
	// The crash window: the commit's intent is still carried, the record is
	// armed, and the purged row's preimage is captured. The stash goes down with
	// the arm: the boot prune removes an orphan, and no record names it yet.
	token := pipelineToken(t, fixture.store, "keep", 2)
	fixture.appendTOML(t, "[pending_store_sync.keep]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")
	stash := hubTOMLStashPath(fixture.configPath, "mutation-crash/keep/remove/2/inc-keep")
	if err := os.WriteFile(stash, preRemove, 0o600); err != nil {
		t.Fatalf("write stash: %v", err)
	}
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "keep", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: stash, Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	// Boot 1 (the crash's boot): the registry loads the committed, host-less
	// file. The host-removed pass marks the record and drops its row; the
	// compensation restores hub.toml but cannot yet serve the host, so the
	// record stays open in `compensating-runtime`.
	crashed := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore:   fixture.store,
		RemoteHostConfigPath: fixture.configPath,
	}, fixture.configPath, nil, nil)
	_ = crashed
	if record, open := fixture.store.Compensation("keep"); !open || hostops.NormalizeCompensationPhase(record.Phase) != hostops.CompensationRuntime {
		t.Fatalf("record after the crash boot = %+v/%v, want it left in %s", record, open, hostops.CompensationRuntime)
	}
	// Boot 2 loads the restored file's live set: the runtime arm converges and
	// reverses the marks the removed incarnation's records carried.
	registry, err := hostreg.New([]hostreg.Host{entry})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	second := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostRegistry:   registry,
		RemoteHostOpsStore:   fixture.store,
		RemoteHostConfigPath: fixture.configPath,
	}, fixture.configPath, registry, nil)
	_ = second
	if _, open := fixture.store.Compensation("keep"); open {
		t.Fatal("the compensation did not converge")
	}
	record, hit, err := fixture.store.LookupOperation(hostops.OperationDedupQuery{
		ClientOperationID: "op-1",
		Host:              "keep",
		Kind:              hostops.KindDeploy,
		Current:           hostops.OperationPair{Generation: 2, IncarnationID: "inc-keep"},
	})
	if err != nil {
		t.Fatalf("the same-key retry was refused: %v", err)
	}
	if !hit || record.State != hostops.StateInterrupted {
		t.Fatalf("same-key retry = %+v/%v, want the interrupted record replayed", record, hit)
	}
	if record.HostRemoved {
		t.Fatal("the replayed record still renders host-removed")
	}
	cfg, _ := readPipelineConfig(t, fixture.configPath)
	if _, live := hostEntryNamed(hostRegistryEntries(cfg), "keep"); !live {
		t.Fatal("the compensation did not restore hub.toml")
	}
}

// TestPerCommitStashIsNotClobberedByALaterRemoval pins the per-commit stash:
// an open record's restore source is its own file, so a later removal of another
// host cannot overwrite it — and the boot then restores that record's own bytes,
// not the later commit's.
func TestPerCommitStashIsNotClobberedByALaterRemoval(t *testing.T) {
	alpha := pipelineEntry("alpha", 2)
	beta := pipelineEntry("beta", 2)
	fixture := newPipelineFixture(t, []hostreg.Host{alpha, beta}, nil)
	preAlpha, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	m := fixture.manager(t)
	// Alpha's stash goes down after the boot prune: no record names it yet, and
	// the prune would remove an orphan.
	stashAlpha := hubTOMLStashPath(fixture.configPath, "mutation-a/alpha/remove/2/inc-alpha")
	if err := os.WriteFile(stashAlpha, preAlpha, 0o600); err != nil {
		t.Fatalf("write alpha's stash: %v", err)
	}
	// Beta's removal commits; its stash is written and pruned on success.
	pipelineToken(t, fixture.store, "beta", 2)
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "beta")); err != nil {
		t.Fatalf("Remove(beta): %v", err)
	}
	kept, err := os.ReadFile(stashAlpha)
	if err != nil {
		t.Fatalf("alpha's stash is gone: %v", err)
	}
	if !bytes.Equal(kept, preAlpha) {
		t.Fatal("a later removal clobbered the open record's stash")
	}
	// The boot restores alpha's record from alpha's stash: its own bytes.
	fixture.appendTOML(t, "[pending_store_sync.alpha]\ngeneration = 2\ntoken_values = [\"row-that-was-purged\"]\n")
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "alpha", Phase: hostops.CompensationHubTOML, Stash: stashAlpha, Generation: 2,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	registry, err := hostreg.New([]hostreg.Host{alpha})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	second := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostRegistry:   registry,
		RemoteHostOpsStore:   fixture.store,
		RemoteHostConfigPath: fixture.configPath,
	}, fixture.configPath, registry, nil)
	_ = second
	restored, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatalf("read hub.toml after the boot: %v", err)
	}
	if !bytes.Equal(restored, preAlpha) {
		t.Fatalf("the boot restored bytes that are not the record's own:\n%s", restored)
	}
}

// TestCompensateStagedCrossFileLeavesAStaleRecordUntouched pins the ownership
// rule (L6): an open record from an earlier mutation of the same name (another
// generation, another commit's stash) is not this commit's to clear, and both
// stashes are left alone.
func TestCompensateStagedCrossFileLeavesAStaleRecordUntouched(t *testing.T) {
	entry := pipelineEntry("keep", 2)
	fixture, token, _ := newPipelineRemovalFixture(t, entry, false)
	m := fixture.manager(t)
	stash := hubTOMLStashPath(fixture.configPath, "test-key")
	if err := os.WriteFile(stash, []byte("# this commit's stash\n"), 0o600); err != nil {
		t.Fatalf("write stash: %v", err)
	}
	staleStash := filepath.Join(fixture.dir, "earlier.stash")
	if err := os.WriteFile(staleStash, []byte("# an earlier commit's stash\n"), 0o600); err != nil {
		t.Fatalf("write stale stash: %v", err)
	}
	if err := fixture.store.ArmCompensation(hostops.Compensation{
		Host: "keep", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
		Stash: staleStash, Generation: 7,
	}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	plan := &hostCommitPlan{Kind: hostMutationRemove, Name: "keep", Key: "test-key", Entry: entry}
	cause := errors.New("injected arm failure")
	m.cfg.mu.Lock()
	m.markMutating("keep")
	if _, err := m.compensateStagedCrossFile(plan, []hostreg.Host{entry}, cause, stash); err == nil {
		t.Fatal("the compensation reported success")
	}
	m.cfg.mu.Lock()
	marked := m.isMutating("keep")
	m.cfg.mu.Unlock()
	if marked {
		t.Fatal("the compensation left the mutation mark set")
	}
	record, open := m.cfg.ops.Compensation("keep")
	if !open || record.Generation != 7 || record.Stash != staleStash {
		t.Fatalf("the stale record was touched: %+v/%v, want generation 7 and its own stash", record, open)
	}
	if _, err := os.Stat(stash); err != nil {
		t.Fatalf("this commit's stash was pruned: %v", err)
	}
	if _, err := os.Stat(staleStash); err != nil {
		t.Fatalf("the stale record's stash was pruned: %v", err)
	}
}

// TestCompensateStagedCrossFileLeavesTheRecordWhenAStepFails pins §9's live
// failure posture: when any step of the arm fails the record and its stash are
// left for the next boot — never a cleared compensation beside a half-restored
// state — the mutation mark is released on every path, and a retry with the
// failure gone converges and prunes the stash.
func TestCompensateStagedCrossFileLeavesTheRecordWhenAStepFails(t *testing.T) {
	for _, failedStep := range []string{"advance-rows", "reinsert", "advance-clear", "clear"} {
		t.Run(failedStep, func(t *testing.T) {
			entry := pipelineEntry("keep", 2)
			fixture, token, _ := newPipelineRemovalFixture(t, entry, false)
			m := fixture.manager(t)
			// Arm the record after construction, so the boot pass cannot resolve
			// it first: this models the live commit failing mid-flight, which is
			// the only path that runs this helper.
			fixture.appendTOML(t, "[pending_store_sync.keep]\ngeneration = 2\ntoken_values = [\""+token.Value+"\"]\n")
			preRemove, err := os.ReadFile(fixture.configPath)
			if err != nil {
				t.Fatalf("read hub.toml: %v", err)
			}
			// The stash goes down with the arm, after construction: the boot
			// prune removes an orphan stash, and no record names this one yet.
			stash := hubTOMLStashPath(fixture.configPath, "test-key")
			if err := os.WriteFile(stash, preRemove, 0o600); err != nil {
				t.Fatalf("write stash: %v", err)
			}
			if err := fixture.store.ArmCompensation(hostops.Compensation{
				Host: "keep", Phase: hostops.CompensationArmed, Rows: []hostops.Token{token},
				Stash: stash, Generation: 2,
			}); err != nil {
				t.Fatalf("ArmCompensation: %v", err)
			}
			if _, err := fixture.store.PurgeCompensated("keep", []string{token.Value}); err != nil {
				t.Fatalf("PurgeCompensated: %v", err)
			}
			m.testOnlyFailCompensationStep = func(_ string, step string) error {
				if step == failedStep {
					return errors.New("injected step failure")
				}
				return nil
			}
			plan := &hostCommitPlan{Kind: hostMutationRemove, Name: "keep", Key: "test-key", Entry: entry}
			cause := errors.New("injected commit-step failure")
			m.cfg.mu.Lock()
			m.markMutating("keep")
			if _, err := m.compensateStagedCrossFile(plan, []hostreg.Host{entry}, cause, stash); err == nil {
				t.Fatal("the compensation reported success")
			}
			m.cfg.mu.Lock()
			marked := m.isMutating("keep")
			m.cfg.mu.Unlock()
			if marked {
				t.Fatal("the compensation left the mutation mark set")
			}
			wantPhase := hostops.CompensationHubTOML
			switch failedStep {
			case "reinsert":
				wantPhase = hostops.CompensationRows
			case "advance-clear":
				wantPhase = hostops.CompensationRuntime
			case "clear":
				wantPhase = hostops.CompensationClear
			}
			record, open := m.cfg.ops.Compensation("keep")
			if !open || hostops.NormalizeCompensationPhase(record.Phase) != wantPhase {
				t.Fatalf("record after the failed step = %+v/%v, want it left in %s", record, open, wantPhase)
			}
			if _, err := os.Stat(stash); err != nil {
				t.Fatalf("the failed step pruned the stash: %v", err)
			}
			// The retry, with the failure gone, walks the rest of the arm and
			// clears.
			m.testOnlyFailCompensationStep = nil
			m.cfg.mu.Lock()
			m.markMutating("keep")
			if _, err := m.compensateStagedCrossFile(plan, []hostreg.Host{entry}, cause, stash); err == nil {
				t.Fatal("the retried compensation reported success without the cause")
			}
			if _, open := m.cfg.ops.Compensation("keep"); open {
				t.Fatal("the retried compensation did not clear")
			}
			if _, err := os.Stat(stash); !os.IsNotExist(err) {
				t.Fatal("the converged retry left the stash behind")
			}
			if _, ok := m.cfg.ops.OutstandingToken("keep"); !ok {
				t.Fatal("the retried compensation did not re-insert the purged row")
			}
		})
	}
}
