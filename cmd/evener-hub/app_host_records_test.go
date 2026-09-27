package hub

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// readHostFileBytes returns path's bytes, failing the test when unreadable.
func readHostFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return raw
}

// readHostRecords loads path the way a boot does and returns the machine
// records the file carries, keyed by host name.
func readHostRecords(t *testing.T, path string) (map[string]HostRecord, map[string]HostGeneration) {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", path, err)
	}
	return cfg.HostRecords, cfg.Generations
}

// hostRecordsFor returns the live record the file carries for name, failing the
// test when it is absent.
func liveRecord(t *testing.T, path, name string) HostRecord {
	t.Helper()
	records, _ := readHostRecords(t, path)
	record, ok := records[name]
	if !ok {
		t.Fatalf("hub.toml %s carries no host_records entry for %q", path, name)
	}
	return record
}

// highWaterFor returns the generation high-water entry the file carries for
// name, failing the test when it is absent.
func highWaterFor(t *testing.T, path, name string) HostGeneration {
	t.Helper()
	_, generations := readHostRecords(t, path)
	record, ok := generations[name]
	if !ok {
		t.Fatalf("hub.toml %s carries no generations entry for %q", path, name)
	}
	return record
}

// TestHubTOMLHostRecordsMintAndSurviveReload pins the durability half of the
// registry slice: a hub.toml host that carries no machine record gets its
// initial (generation, incarnation id, presence epoch) triple at the load
// (spec 08 §15: "boot mints a fresh incarnation id for every hub.toml host name
// with no persisted incarnation in that same atomic hub.toml write"), the
// write records it — and a reload reads the recorded pair back unchanged
// instead of minting again.
func TestHubTOMLHostRecordsMintAndSurviveReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(path, []hostreg.Host{{Name: "alpha", SSH: "alpha.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	_ = newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, hosts, nil)

	first, ok := hosts.Get("alpha")
	if !ok {
		t.Fatal("the loaded hub.toml host vanished")
	}
	if len(first.IncarnationID) != 36 {
		t.Fatalf("the loaded host's incarnation id = %q, want a minted 36-byte one", first.IncarnationID)
	}
	live := liveRecord(t, path, "alpha")
	if live.IncarnationID != first.IncarnationID || live.PresenceEpoch != first.PresenceEpoch {
		t.Fatalf("host_records[alpha] = %+v, want the live pair (%s, %d)", live, first.IncarnationID, first.PresenceEpoch)
	}
	mark := highWaterFor(t, path, "alpha")
	if mark.Generation != first.Generation || mark.IncarnationID != first.IncarnationID || mark.PresenceEpoch != first.PresenceEpoch {
		t.Fatalf("generations[alpha] = %+v, want the live triple (%d, %s, %d)", mark, first.Generation, first.IncarnationID, first.PresenceEpoch)
	}

	// A second boot reads the recorded triple back: same incarnation id, same
	// presence epoch, same generation — and no rewrite, because nothing is
	// missing.
	before := readHostFileBytes(t, path)
	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("second LoadConfig: %v", err)
	}
	hosts2, err := hostreg.New(hostRegistryEntries(cfg2))
	if err != nil {
		t.Fatalf("second hostreg.New: %v", err)
	}
	_ = newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, hosts2, nil)
	second, _ := hosts2.Get("alpha")
	if second.IncarnationID != first.IncarnationID || second.PresenceEpoch != first.PresenceEpoch || second.Generation != first.Generation {
		t.Fatalf("reloaded host = %+v, want the persisted triple (%d, %s, %d)",
			second, first.Generation, first.IncarnationID, first.PresenceEpoch)
	}
	if after := readHostFileBytes(t, path); !bytes.Equal(after, before) {
		t.Fatalf("a boot with every record present rewrote hub.toml:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestHubTOMLWithoutRecordsLoadsWithTheAbsenceDefault pins the compatibility
// rule for a hub.toml written before the machine records existed: the records
// decode as absent (a zero HostRecord, not a fabricated registration), the file
// still loads, and the boot load assigns the initial pair — generation 1, a
// fresh incarnation id, presence epoch 1 — and records it once. The zero value
// is what makes "absent" representable: no mint produces an empty incarnation
// id or a zero epoch, so an unrecorded host can never be mistaken for a live
// re-registration's persisted pair.
func TestHubTOMLWithoutRecordsLoadsWithTheAbsenceDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	legacy := "# This file is machine-managed by the evener hub.\n" +
		"# The hub rewrites it in place; comments and formatting are not preserved.\n\n" +
		"[[hosts]]\nname = \"alpha\"\nssh = \"alpha.example\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("a hub.toml without machine records must still load: %v", err)
	}
	if _, ok := cfg.HostRecords["alpha"]; ok {
		t.Fatalf("host_records[alpha] decoded from a file that carries none: %+v", cfg.HostRecords["alpha"])
	}
	if _, ok := cfg.Generations["alpha"]; ok {
		t.Fatalf("generations[alpha] decoded from a file that carries none: %+v", cfg.Generations["alpha"])
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, hosts, nil)
	initial, _ := hosts.Get("alpha")
	if initial.Generation != 1 {
		t.Fatalf("a host with no persisted mark got generation %d, want the initial 1", initial.Generation)
	}
	if len(initial.IncarnationID) != 36 || initial.PresenceEpoch != 1 {
		t.Fatalf("a host with no persisted pair got (%q, %d), want a minted 36-byte id and epoch 1",
			initial.IncarnationID, initial.PresenceEpoch)
	}
	live := liveRecord(t, path, "alpha")
	if live.IncarnationID != initial.IncarnationID || live.PresenceEpoch != initial.PresenceEpoch {
		t.Fatalf("host_records[alpha] = %+v, want the initial pair (%s, %d)", live, initial.IncarnationID, initial.PresenceEpoch)
	}
	// The name stays live through the compatibility load: an unrecorded host is
	// not a re-registration, and it is not removed, blocked, or re-added.
	rows, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows.Hosts) != 1 || rows.Hosts[0].Name != "alpha" || rows.Hosts[0].Removed {
		t.Fatalf("list after a compatibility load = %+v, want the live alpha row", rows.Hosts)
	}
}

// TestHubTOMLPresenceEpochAdvancesOncePerMutation pins spec 08 §1's counter
// against the file: "advanced on every add, remove, re-add, and expiry purge",
// each advance recorded in the same atomic hub.toml write as the host change —
// and an update, which is none of those, leaves the epoch alone.
func TestHubTOMLPresenceEpochAdvancesOncePerMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, nil, nil)

	entry := appwire.HostEntry{Name: "alpha", Address: "alpha.example"}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: entry}); err != nil {
		t.Fatalf("Add(alpha): %v", err)
	}
	added := liveRecord(t, path, "alpha")
	if added.PresenceEpoch != 1 {
		t.Fatalf("after the first add the recorded presence epoch = %d, want 1", added.PresenceEpoch)
	}
	addedMark := highWaterFor(t, path, "alpha")
	if addedMark.PresenceEpoch != 1 || addedMark.IncarnationID != added.IncarnationID {
		t.Fatalf("generations[alpha] = %+v, want the live triple with epoch 1", addedMark)
	}

	entry.Address = "alpha2.example"
	if _, err := m.Update(context.Background(), appwire.HostUpdateParams{Name: "alpha", Entry: entry}); err != nil {
		t.Fatalf("Update(alpha): %v", err)
	}
	updated := liveRecord(t, path, "alpha")
	if updated.PresenceEpoch != added.PresenceEpoch {
		t.Fatalf("an update advanced the presence epoch to %d, want %d", updated.PresenceEpoch, added.PresenceEpoch)
	}
	if updated.IncarnationID != added.IncarnationID {
		t.Fatalf("an update rotated the incarnation id %q -> %q", added.IncarnationID, updated.IncarnationID)
	}
	if mark := highWaterFor(t, path, "alpha"); mark.Generation <= addedMark.Generation {
		t.Fatalf("an update did not advance the recorded generation: %+v", mark)
	}

	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "alpha"}); err != nil {
		t.Fatalf("Remove(alpha): %v", err)
	}
	records, _ := readHostRecords(t, path)
	if _, ok := records["alpha"]; ok {
		t.Fatalf("the removed name kept its live host_records entry: %+v", records["alpha"])
	}
	removed := highWaterFor(t, path, "alpha")
	if removed.PresenceEpoch != added.PresenceEpoch+1 {
		t.Fatalf("the removal advanced the recorded presence epoch to %d, want %d", removed.PresenceEpoch, added.PresenceEpoch+1)
	}
	if removed.IncarnationID != added.IncarnationID {
		t.Fatalf("the removal's high-water incarnation = %q, want the removed entry's %q", removed.IncarnationID, added.IncarnationID)
	}

	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: entry}); err != nil {
		t.Fatalf("re-Add(alpha): %v", err)
	}
	readded := liveRecord(t, path, "alpha")
	if readded.PresenceEpoch != removed.PresenceEpoch+1 {
		t.Fatalf("the re-add advanced the presence epoch to %d, want %d", readded.PresenceEpoch, removed.PresenceEpoch+1)
	}
	if readded.IncarnationID == added.IncarnationID {
		t.Fatalf("the re-add reused the removed incarnation id %q", added.IncarnationID)
	}
}

// TestHubTOMLPresenceEpochSurvivesReloadAcrossRemoval pins the counter's
// durability across the case it exists for: a removal, a restart, and a re-add.
// The removal's advanced epoch is persisted in the name's high-water record, so
// the restarted hub reads it back and the re-add advances past it instead of
// reusing the removed incarnation's epoch — even though the name has no live
// entry in between, and even though the registry's counter starts empty in the
// new process.
func TestHubTOMLPresenceEpochSurvivesReloadAcrossRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	entry := appwire.HostEntry{Name: "alpha", Address: "alpha.example"}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: entry}); err != nil {
		t.Fatalf("Add(alpha): %v", err)
	}
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "alpha"}); err != nil {
		t.Fatalf("Remove(alpha): %v", err)
	}
	removed := highWaterFor(t, path, "alpha")

	// A restart: the boot load reads the removed name's high-water record.
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m2 := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, hosts, nil)
	if _, err := m2.Add(context.Background(), appwire.HostAddParams{Entry: entry}); err != nil {
		t.Fatalf("re-Add(alpha) after the restart: %v", err)
	}
	readded := liveRecord(t, path, "alpha")
	if readded.PresenceEpoch <= removed.PresenceEpoch {
		t.Fatalf("the re-add's presence epoch = %d, want above the removed %d", readded.PresenceEpoch, removed.PresenceEpoch)
	}
	if readded.IncarnationID == removed.IncarnationID {
		t.Fatalf("the re-add reused the removed incarnation id %q", removed.IncarnationID)
	}
}

// TestHubTOMLRecordsLandInTheSameWriteAsTheHostChange pins the atomicity the
// spec's "in that same atomic write" demands: a write that refuses carries
// neither the host change nor its records, so a reader of the file can never
// see one without the other. The pre-rename failure covers a write that lands
// nothing; the post-rename failure covers the compensation that restores the
// pre-mutation contents.
func TestHubTOMLRecordsLandInTheSameWriteAsTheHostChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, path, nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "keep", Address: "k.example"}}); err != nil {
		t.Fatalf("Add(keep): %v", err)
	}
	before := readHostFileBytes(t, path)

	// A write that cannot land: hub.toml is replaced by a directory, so the
	// temp-file rename fails before anything is committed.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove hub.toml: %v", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir hub.toml: %v", err)
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
		t.Fatal("Add over a failing write succeeded, want refusal")
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("remove hub.toml directory: %v", err)
	}
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatalf("restore hub.toml: %v", err)
	}
	records, _ := readHostRecords(t, path)
	if _, ok := records["side"]; ok {
		t.Fatal("a refused add left a host_records entry behind")
	}

	// A write whose rename lands but whose directory sync fails is compensated:
	// the file returns to the pre-mutation contents, records included.
	flakyHubTOMLDirSync(t, func(call int) bool { return call == 1 })
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
		t.Fatal("Add whose directory sync failed reported success, want the compensated refusal")
	}
	after := readHostFileBytes(t, path)
	if !bytes.Equal(after, before) {
		t.Fatalf("the compensated write did not restore the pre-add file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	records, _ = readHostRecords(t, path)
	if _, ok := records["side"]; ok {
		t.Fatal("a compensated add left a host_records entry behind")
	}
}

// TestBoundaryMirrorFollowsEveryHubTOMLMutation pins the mirror side of spec 08
// §7 — "the store mirrors it into the per-host boundary record on the same
// writes that mirror the generation" — against the file: every mutation's
// recorded triple is the triple the operation store's boundary record carries.
func TestBoundaryMirrorFollowsEveryHubTOMLMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	ops, err := hostops.Open(hostops.StorePath(dir))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	cfg := hubcore.WebConfig{RemoteHostOpsStore: ops}
	m := newHubHostManager(appsource.NewRegistry(), nil, cfg, path, nil, nil)

	entry := appwire.HostEntry{Name: "alpha", Address: "alpha.example"}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: entry}); err != nil {
		t.Fatalf("Add(alpha): %v", err)
	}
	mirrored, ok := ops.Boundary("alpha")
	if !ok {
		t.Fatal("the add wrote no boundary record")
	}
	live := liveRecord(t, path, "alpha")
	if mirrored.IncarnationID != live.IncarnationID || mirrored.PresenceEpoch != live.PresenceEpoch {
		t.Fatalf("mirrored boundary = %+v, want the file's live record %+v", mirrored, live)
	}
	if mark := highWaterFor(t, path, "alpha"); mirrored.Generation != mark.Generation {
		t.Fatalf("mirrored generation = %d, want the file's %d", mirrored.Generation, mark.Generation)
	}

	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "alpha"}); err != nil {
		t.Fatalf("Remove(alpha): %v", err)
	}
	removedMark := highWaterFor(t, path, "alpha")
	mirrored, ok = ops.Boundary("alpha")
	if !ok {
		t.Fatal("the removal dropped the boundary record instead of advancing it")
	}
	if mirrored.Generation != removedMark.Generation || mirrored.IncarnationID != removedMark.IncarnationID || mirrored.PresenceEpoch != removedMark.PresenceEpoch {
		t.Fatalf("after the removal the mirror = %+v, want the file's high-water triple %+v", mirrored, removedMark)
	}

	// A mutation whose hub.toml write refuses must not reach the mirror: the
	// mirror is written behind a committed file write, so it can never carry a
	// change the file does not hold.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove hub.toml: %v", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir hub.toml: %v", err)
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
		t.Fatal("Add over a failing hub.toml write succeeded, want refusal")
	}
	if _, ok := ops.Boundary("side"); ok {
		t.Fatal("a refused hub.toml write reached the boundary mirror")
	}
}

// TestHubTOMLKeepsRecordsForNamesTheWriteDoesNotOwn pins the machine-record
// half of the rewrite's preservation rule: a rewrite replaces the records of
// the names the mutation owns and carries every other record through, so a
// hand-added host's record is never dropped by an unrelated mutation.
func TestHubTOMLKeepsRecordsForNamesTheWriteDoesNotOwn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "alpha", Address: "alpha.example"}}); err != nil {
		t.Fatalf("Add(alpha): %v", err)
	}
	// A hand-added entry with its own machine records, made while the hub runs.
	hand := "\n[[hosts]]\nname = \"beta\"\nssh = \"beta.example\"\n" +
		"\n[host_records.beta]\nincarnation_id = \"hand-minted\"\npresence_epoch = 4\n" +
		"\n[generations.beta]\ngeneration = 9\nincarnation_id = \"hand-minted\"\npresence_epoch = 4\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open hub.toml: %v", err)
	}
	if _, err := file.WriteString(hand); err != nil {
		t.Fatalf("append the hand-added host: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close hub.toml: %v", err)
	}
	records, generations := readHostRecords(t, path)
	if !strings.Contains(records["beta"].IncarnationID, "hand-minted") || generations["beta"].Generation != 9 {
		t.Fatalf("the fixture did not load as intended: %+v %+v", records["beta"], generations["beta"])
	}

	// A mutation of alpha must carry beta's records through untouched.
	entry := appwire.HostEntry{Name: "alpha", Address: "alpha2.example"}
	if _, err := m.Update(context.Background(), appwire.HostUpdateParams{Name: "alpha", Entry: entry}); err != nil {
		t.Fatalf("Update(alpha): %v", err)
	}
	records, generations = readHostRecords(t, path)
	if records["beta"].IncarnationID != "hand-minted" || records["beta"].PresenceEpoch != 4 {
		t.Fatalf("the rewrite dropped the hand-added host's live record: %+v", records["beta"])
	}
	if generations["beta"].Generation != 9 {
		t.Fatalf("the rewrite dropped the hand-added host's high-water record: %+v", generations["beta"])
	}
}
