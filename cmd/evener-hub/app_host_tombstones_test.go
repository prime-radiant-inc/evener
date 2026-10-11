package hub

// Registry tests for slice S11's durable tombstones (registry spec 08 §15, §6,
// §4, §11): the one-atomic-write removal that writes [tombstones."<name>"],
// the retained-row projection and its bounds, the global cap and its typed
// refusal, re-add purge and cache clearing, retention expiry at the read,
// mutation, and boot paths, the list row, and the boot collision rule.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// tombstoneFor returns the stored tombstone for name, failing when absent.
func tombstoneFor(t *testing.T, m *hubHostManager, name string) HostTombstone {
	t.Helper()
	tombstone, ok := m.cfg.store.tombstoneSnapshot()[name]
	if !ok {
		t.Fatalf("no tombstone for %q in the store", name)
	}
	return tombstone
}

// rowFor returns the list row for name, failing when absent.
func rowFor(t *testing.T, m *hubHostManager, name string) appwire.HostRow {
	t.Helper()
	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, row := range list.Hosts {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("host %q is not listed: %+v", name, list.Hosts)
	return appwire.HostRow{}
}

// retainedTestRows builds n retained rows newest-first by UpdatedAt, with the
// given base instant and one-second steps.
func retainedTestRows(source string, n int, base int64) []appwire.Thread {
	rows := make([]appwire.Thread, 0, n)
	for i := range n {
		rows = append(rows, appwire.Thread{
			ID:        fmt.Sprintf("%s-t%03d", source, i),
			SessionID: fmt.Sprintf("%s-s%03d", source, i),
			Source:    source,
			Evener:    appwire.EvenerThread{Ref: fmt.Sprintf("evener://%s/%s-t%03d", source, source, i)},
			UpdatedAt: base + int64(i),
			CreatedAt: base,
			CWD:       "/srv/" + source,
			Name:      fmt.Sprintf("session %d", i),
		})
	}
	return rows
}

// TestHostTombstoneProjectionOrderAndStability pins spec §15's projection rule:
// newest-first by each row's UpdatedAt with a (UpdatedAt, row id) total
// tie-break, a missing timestamp sorting oldest, applied deterministically so
// truncation is stable across retries — never the snapshot's row order.
func TestHostTombstoneProjectionOrderAndStability(t *testing.T) {
	rows := []appwire.Thread{
		{ID: "old", UpdatedAt: 10},
		{ID: "missing", UpdatedAt: 0},
		{ID: "new", UpdatedAt: 30},
		{ID: "tie-b", UpdatedAt: 20},
		{ID: "tie-a", UpdatedAt: 20},
	}
	projected, truncated := projectTombstoneRows(rows, 10, 0)
	if truncated {
		t.Fatal("truncated = true with the bound above the row count")
	}
	var ids []string
	for _, raw := range projected {
		var row appwire.Thread
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			t.Fatalf("decode projected row: %v", err)
		}
		ids = append(ids, row.ID)
	}
	want := []string{"new", "tie-a", "tie-b", "old", "missing"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("projection order = %v, want %v (newest-first, row-id tie-break, missing timestamp oldest)", ids, want)
	}
	// Stability: the same input projects to the same bytes on every persist.
	again, _ := projectTombstoneRows(rows, 10, 0)
	if strings.Join(again, "\x00") != strings.Join(projected, "\x00") {
		t.Fatal("projection is not stable across runs")
	}
	// Truncation keeps the newest rows and reports itself.
	cut, truncated := projectTombstoneRows(rows, 2, 0)
	if !truncated {
		t.Fatal("truncated = false at the count bound")
	}
	if len(cut) != 2 {
		t.Fatalf("truncated projection = %d rows, want 2", len(cut))
	}
	var first appwire.Thread
	if err := json.Unmarshal([]byte(cut[0]), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first.ID != "new" {
		t.Fatalf("truncated projection kept %q first, want the newest row", first.ID)
	}
}

// TestHostTombstoneRemovalWritesOneAtomicRecord pins spec §15's record and §4's
// list row: a successful remove's ONE write deletes the live entry and writes
// the tombstone carrying the removed entry's effective HostConfig, its
// identity triple, the advanced presence epoch, the removal timestamp, and the
// retained projection; list renders `removed: true` with the retained count;
// update/remove on the tombstone-only name refuse not-found; add is the
// re-add path.
func TestHostTombstoneRemovalWritesOneAtomicRecord(t *testing.T) {
	f := newUpdateFixture(t)
	removedAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f.m.cfg.now = func() time.Time { return removedAt }
	f.m.cfg.lastGoodThreads = func(sourceID string) []appwire.Thread {
		if sourceID != "side" {
			return nil
		}
		return retainedTestRows("side", 3, 1000)
	}
	host, ok := f.m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture host is not live")
	}
	resp, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side"))
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !resp.Host.Removed {
		t.Fatalf("remove response row = %+v, want removed", resp.Host)
	}
	tombstone := tombstoneFor(t, f.m, "side")
	if tombstone.Entry.Name != "side" || tombstone.Entry.SSH != host.SSH {
		t.Fatalf("tombstone entry = %+v, want the removed entry", tombstone.Entry)
	}
	if tombstone.Origin != hostOriginHubTOML || tombstone.Generation != host.Generation || tombstone.IncarnationID != host.IncarnationID {
		t.Fatalf("tombstone identity = (%q, %d, %q), want the removed entry's", tombstone.Origin, tombstone.Generation, tombstone.IncarnationID)
	}
	if tombstone.PresenceEpoch <= host.PresenceEpoch {
		t.Fatalf("tombstone presence epoch = %d, want an advance past %d", tombstone.PresenceEpoch, host.PresenceEpoch)
	}
	if tombstone.RemovedAt != removedAt.Format(time.RFC3339) {
		t.Fatalf("tombstone removed_at = %q, want %q", tombstone.RemovedAt, removedAt.Format(time.RFC3339))
	}
	if len(tombstone.Rows) != 3 || tombstone.RowsTruncated {
		t.Fatalf("tombstone rows = %d (truncated %v), want the 3 retained rows untruncated", len(tombstone.Rows), tombstone.RowsTruncated)
	}
	// The durable file carries the record: it is what a restart loads.
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	fileTombstone, ok := cfg.Tombstones["side"]
	if !ok {
		t.Fatal("hub.toml carries no tombstone after the removal")
	}
	if fileTombstone.IncarnationID != host.IncarnationID || len(fileTombstone.Rows) != 3 {
		t.Fatalf("hub.toml tombstone = %+v, want the removed identity and rows", fileTombstone)
	}
	if len(cfg.Hosts) != 0 {
		t.Fatalf("hub.toml still carries the removed host: %+v", cfg.Hosts)
	}
	// The high-water triple agrees with the tombstone.
	mark, ok := cfg.Generations["side"]
	if !ok || mark.Generation != tombstone.Generation || mark.IncarnationID != tombstone.IncarnationID || mark.PresenceEpoch != tombstone.PresenceEpoch {
		t.Fatalf("generations[side] = %+v, want the tombstone's triple", mark)
	}
	// list renders the tombstone row.
	row := rowFor(t, f.m, "side")
	if !row.Removed || row.Attached || row.MidAttach || row.RetainedRows == nil || *row.RetainedRows != 3 || row.RowsTruncated {
		t.Fatalf("list row = %+v, want removed, detached, midEnsure false, retainedRows 3", row)
	}
	if row.Generation != host.Generation || row.IncarnationID != host.IncarnationID || row.Origin != hostOriginHubTOML {
		t.Fatalf("list row identity = (%d, %q, %q), want the removed entry's", row.Generation, row.IncarnationID, row.Origin)
	}
	// The facts optionals stay absent — never null — on a tombstone row.
	if row.ServerName != "" || row.ServerVersion != "" || row.HubVersion != "" || row.OS != "" || row.Arch != "" || row.LastAttachErr != "" {
		t.Fatalf("tombstone row carries live state: %+v", row)
	}
	// update and remove on the tombstone-only name refuse not-found.
	if _, err := f.m.Update(context.Background(), updateRequestFor("side", row.Generation, row.IncarnationID, appwire.HostEntry{Address: "new.example"})); err == nil {
		t.Fatal("Update on a tombstone-only name succeeded, want not-found")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	if _, err := f.m.Remove(context.Background(), removeRequestFor("side", row.Generation, row.IncarnationID)); err == nil {
		t.Fatal("Remove on a tombstone-only name succeeded, want not-found")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	if _, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"}); err == nil {
		t.Fatal("Status on a tombstone-only name succeeded, want not-found")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	// add accepts the name as a re-add (spec §4), purging the tombstone.
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-add on a tombstone-only name: %v", err)
	}
	if _, stillTombstoned := f.m.cfg.store.tombstoneSnapshot()["side"]; stillTombstoned {
		t.Fatal("re-add left the tombstone behind")
	}
	cfg, err = LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("LoadConfig after re-add: %v", err)
	}
	if _, carried := cfg.Tombstones["side"]; carried {
		t.Fatal("hub.toml still carries the tombstone after the re-add")
	}
}

// TestHostTombstonesSurviveRestart pins spec §15's durability sentence:
// tombstones persist in hub.toml, boot restores them alongside host entries,
// and they survive a controller restart with rows, config, identity, and
// removal timestamp intact.
func TestHostTombstonesSurviveRestart(t *testing.T) {
	f := newUpdateFixture(t)
	// The removal instant is relative to now, not a fixed date: the restart
	// below boots a manager on the real clock, so a hard-coded instant
	// eventually falls past the 7-day retention and boot prunes the tombstone
	// this test means to watch survive (the sibling collision test's pattern).
	removedAt := time.Now().UTC().Add(-2 * time.Hour)
	f.m.cfg.now = func() time.Time { return removedAt }
	f.m.cfg.lastGoodThreads = func(sourceID string) []appwire.Thread {
		return retainedTestRows(sourceID, 2, 2000)
	}
	before, _ := f.m.cfg.hosts.Get("side")
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	boot := bootHostManager(t, f.configPath)
	tombstone := tombstoneFor(t, boot, "side")
	if tombstone.Entry.SSH != before.SSH || tombstone.Generation != before.Generation ||
		tombstone.IncarnationID != before.IncarnationID || tombstone.RemovedAt != removedAt.Format(time.RFC3339) {
		t.Fatalf("reloaded tombstone = %+v, want the removed entry's record", tombstone)
	}
	if len(tombstone.Rows) != 2 {
		t.Fatalf("reloaded tombstone rows = %d, want 2", len(tombstone.Rows))
	}
	var first appwire.Thread
	if err := json.Unmarshal([]byte(tombstone.Rows[0]), &first); err != nil {
		t.Fatalf("decode retained row: %v", err)
	}
	if first.Source != "side" || first.ID == "" {
		t.Fatalf("reloaded retained row = %+v, want the captured row", first)
	}
	row := rowFor(t, boot, "side")
	if !row.Removed || row.RetainedRows == nil || *row.RetainedRows != 2 {
		t.Fatalf("reloaded list row = %+v, want the tombstone row", row)
	}
	// A re-add over a restart restores above the retained mark, not at it.
	if _, err := boot.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-add after restart: %v", err)
	}
	after, _ := boot.cfg.hosts.Get("side")
	if after.Generation <= tombstone.Generation || after.IncarnationID == tombstone.IncarnationID {
		t.Fatalf("re-added identity = (%d, %q), want strictly above the retained mark (%d, %q)",
			after.Generation, after.IncarnationID, tombstone.Generation, tombstone.IncarnationID)
	}
}

// TestHostTombstonePersistBounds pins spec §15's per-tombstone bounds: at most
// the row-count bound and the serialized-byte bound are persisted, newest-first,
// with rowsTruncated true on the tombstone and the list row — never the full set
// silently.
func TestHostTombstonePersistBounds(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.lastGoodThreads = func(sourceID string) []appwire.Thread {
		return retainedTestRows(sourceID, 700, 5000)
	}
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tombstone := tombstoneFor(t, f.m, "side")
	if len(tombstone.Rows) != DefaultHostTombstoneMaxRows {
		t.Fatalf("persisted rows = %d, want the %d-row bound", len(tombstone.Rows), DefaultHostTombstoneMaxRows)
	}
	if !tombstone.RowsTruncated {
		t.Fatal("rowsTruncated = false at the row bound")
	}
	var first, last appwire.Thread
	if err := json.Unmarshal([]byte(tombstone.Rows[0]), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := json.Unmarshal([]byte(tombstone.Rows[len(tombstone.Rows)-1]), &last); err != nil {
		t.Fatalf("decode last: %v", err)
	}
	if first.UpdatedAt <= last.UpdatedAt {
		t.Fatalf("persisted rows are not newest-first: first updated %d, last %d", first.UpdatedAt, last.UpdatedAt)
	}
	row := rowFor(t, f.m, "side")
	if row.RetainedRows == nil || *row.RetainedRows != DefaultHostTombstoneMaxRows || !row.RowsTruncated {
		t.Fatalf("list row = %+v, want retainedRows %d and rowsTruncated true", row, DefaultHostTombstoneMaxRows)
	}
}

// TestHostTombstoneByteBound pins the serialized-byte half of the persist
// bound: a projection over the byte bound truncates (and reports it), even
// under the row-count bound.
func TestHostTombstoneByteBound(t *testing.T) {
	f := newUpdateFixture(t)
	rows := retainedTestRows("side", 3, 9000)
	one, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	// The bound admits the newest row and nothing more.
	f.m.cfg.policy.tombstoneMaxRowBytes = int64(len(one)) + 1
	f.m.cfg.lastGoodThreads = func(sourceID string) []appwire.Thread { return rows }
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tombstone := tombstoneFor(t, f.m, "side")
	if len(tombstone.Rows) != 1 || !tombstone.RowsTruncated {
		t.Fatalf("byte-bounded projection = %d rows (truncated %v), want 1 truncated row", len(tombstone.Rows), tombstone.RowsTruncated)
	}
}

// TestHostTombstoneGlobalCapEviction pins spec §15's global cap: repeated
// add/remove churn over distinct names converges newest-first by removal
// timestamp to the newest N tombstones, and remnant-gated tombstones are never
// eviction candidates.
func TestHostTombstoneGlobalCapEviction(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "b", SSH: "b.example"}, hostreg.Host{Name: "c", SSH: "c.example"},
		hostreg.Host{Name: "d", SSH: "d.example"}, hostreg.Host{Name: "e", SSH: "e.example"})
	f.m.cfg.policy.tombstoneMaxCount = 3
	base := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	for i, name := range []string{"side", "b", "c", "d", "e"} {
		removedAt := base.Add(time.Duration(i) * time.Hour)
		f.m.cfg.now = func() time.Time { return removedAt }
		if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, name)); err != nil {
			t.Fatalf("Remove(%s): %v", name, err)
		}
	}
	kept := f.m.cfg.store.tombstoneSnapshot()
	if len(kept) != 3 {
		t.Fatalf("tombstones after churn = %v, want the 3 newest", tombstoneNames(kept))
	}
	for _, want := range []string{"c", "d", "e"} {
		if _, ok := kept[want]; !ok {
			t.Fatalf("newest tombstone %q was evicted: %v", want, tombstoneNames(kept))
		}
	}
	// A gated tombstone outlives a newer one: with the cap at 2 and "c"
	// (oldest) gated, eviction takes the oldest EVICTABLE tombstone instead.
	f2 := newUpdateFixture(t, hostreg.Host{Name: "b", SSH: "b.example"}, hostreg.Host{Name: "c", SSH: "c.example"})
	f2.m.cfg.policy.tombstoneMaxCount = 2
	f2.m.cfg.now = func() time.Time { return base }
	if _, err := f2.m.Remove(context.Background(), removeRequest(t, f2.m, "side")); err != nil {
		t.Fatalf("Remove(side): %v", err)
	}
	f2.m.testOnlyRemnantGated = func(name string) bool { return name == "side" }
	f2.m.cfg.now = func() time.Time { return base.Add(time.Hour) }
	if _, err := f2.m.Remove(context.Background(), removeRequest(t, f2.m, "b")); err != nil {
		t.Fatalf("Remove(b): %v", err)
	}
	f2.m.cfg.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := f2.m.Remove(context.Background(), removeRequest(t, f2.m, "c")); err != nil {
		t.Fatalf("Remove(c): %v", err)
	}
	kept = f2.m.cfg.store.tombstoneSnapshot()
	if _, ok := kept["side"]; !ok {
		t.Fatalf("the gated tombstone was evicted: %v", tombstoneNames(kept))
	}
	if _, ok := kept["b"]; ok {
		t.Fatalf("the evictable oldest tombstone survived: %v", tombstoneNames(kept))
	}
	if _, ok := kept["c"]; !ok {
		t.Fatalf("the newest tombstone was evicted: %v", tombstoneNames(kept))
	}
}

// tombstoneNames sorts a tombstone set's names for readable failures.
func tombstoneNames[T any](set map[string]T) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestHostTombstoneCapacityRefusal pins spec §15/§12's typed refusal: a persist
// that would exceed a global bound and can only do so by evicting a
// remnant-gated tombstone refuses with `tombstone-capacity`, naming the bound
// and the blocking names — via the test-only gated seam the S10
// testOnlyParkPostCommit precedent established.
func TestHostTombstoneCapacityRefusal(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "b", SSH: "b.example"}, hostreg.Host{Name: "c", SSH: "c.example"})
	f.m.cfg.policy.tombstoneMaxCount = 2
	base := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	f.m.cfg.now = func() time.Time { return base }
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove(side): %v", err)
	}
	f.m.testOnlyRemnantGated = func(name string) bool { return name == "side" }
	f.m.cfg.now = func() time.Time { return base.Add(time.Hour) }
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "b")); err != nil {
		t.Fatalf("Remove(b): %v", err)
	}
	f.m.testOnlyRemnantGated = func(name string) bool { return name == "side" || name == "b" }
	// The next removal fits only by evicting a gated tombstone: every candidate
	// is gated, so it refuses typed and commits nothing.
	before := readHubTOMLBytes(t, f.configPath)
	f.m.cfg.now = func() time.Time { return base.Add(2 * time.Hour) }
	_, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "c"))
	if err == nil {
		t.Fatal("Remove(c) succeeded with every eviction candidate gated")
	}
	info, data, code := deployWireInfo(t, err)
	if info != appwire.ErrorTombstoneCapacity || code != appwire.CodeConflict {
		t.Fatalf("refusal = (%q, %d), want (%q, %d)", info, code, appwire.ErrorTombstoneCapacity, appwire.CodeConflict)
	}
	if bound := receiptField(t, data, "bound"); bound != appwire.TombstoneCapacityBoundCount {
		t.Fatalf("refusal bound = %q, want %q", bound, appwire.TombstoneCapacityBoundCount)
	}
	var blocking []string
	if err := json.Unmarshal(data["blockingNames"], &blocking); err != nil {
		t.Fatalf("refusal carries no readable blockingNames: %v", err)
	}
	sort.Strings(blocking)
	if strings.Join(blocking, ",") != "b,side" {
		t.Fatalf("refusal blockingNames = %v, want the two gated candidates", blocking)
	}
	// The refusal committed nothing: the live host stays live and the file is
	// byte-identical.
	if _, ok := f.m.cfg.hosts.Get("c"); !ok {
		t.Fatal("the refused removal dropped its live host")
	}
	if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(after, before) {
		t.Fatalf("a refused removal rewrote hub.toml:\n%s", after)
	}
}

// TestHostTombstoneReAddPurgesAndClearsCaches pins spec §4/§15's re-add: the
// same staged commit purges the tombstone, clears the name-keyed caches, and
// mints a generation strictly above the retained high-water mark; a
// publication captured under the obsolete generation is rejected.
func TestHostTombstoneReAddPurgesAndClearsCaches(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.lastGoodThreads = func(sourceID string) []appwire.Thread {
		return retainedTestRows(sourceID, 1, 100)
	}
	cache := &hubcore.RemoteThreadCache{}
	f.m.cfg.remoteCache = cache
	cache.RegisterSource("side")
	oldGeneration, _ := cache.SourceGeneration("side")
	var forgotten []string
	f.m.cfg.forgetLastGoodThreads = func(sourceID string) { forgotten = append(forgotten, sourceID) }
	before, _ := f.m.cfg.hosts.Get("side")
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tombstone := tombstoneFor(t, f.m, "side")
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	reAdded, ok := f.m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("re-add did not register the host")
	}
	if reAdded.Generation <= tombstone.Generation {
		t.Fatalf("re-added generation = %d, want strictly above the retained mark %d", reAdded.Generation, tombstone.Generation)
	}
	if reAdded.IncarnationID == before.IncarnationID || reAdded.IncarnationID == tombstone.IncarnationID {
		t.Fatalf("re-added incarnation %q reuses a removed identity", reAdded.IncarnationID)
	}
	if reAdded.PresenceEpoch <= tombstone.PresenceEpoch {
		t.Fatalf("re-added presence epoch = %d, want above the tombstone's %d", reAdded.PresenceEpoch, tombstone.PresenceEpoch)
	}
	if !slices.Contains(forgotten, "side") {
		t.Fatalf("re-add did not clear the name-keyed retained rows (forgotten = %v)", forgotten)
	}
	if _, stillTombstoned := f.m.cfg.store.tombstoneSnapshot()["side"]; stillTombstoned {
		t.Fatal("re-add left the tombstone behind")
	}
	// An obsolete-generation publication is rejected: a walk captured under the
	// pre-remove registration cannot publish its rows under the re-added name.
	newGeneration, ok := cache.SourceGeneration("side")
	if !ok || newGeneration == oldGeneration {
		t.Fatalf("re-add did not re-register the source under a fresh generation (old %d, new %d, ok %v)", oldGeneration, newGeneration, ok)
	}
	cache.StoreWalkSnapshot(hubcore.RemoteThreadSnapshot{
		Threads: []appwire.Thread{{ID: "stale-t1", Source: "side", Evener: appwire.EvenerThread{Ref: "evener://side/stale-t1"}}},
		Sources: map[string]hubcore.RemoteSourceSnapshot{
			"side": {Threads: []appwire.Thread{{ID: "stale-t1", Source: "side"}}, Complete: true},
		},
	}, map[string]uint64{"side": oldGeneration})
	for _, thread := range cache.Get() {
		if thread.ID == "stale-t1" {
			t.Fatal("a publication captured under the obsolete generation was admitted after the re-add")
		}
	}
	// A tombstone-tagged publication — a walk that read the tombstone before
	// the re-add and publishes after it — is dropped the same way: the
	// re-registered name supersedes the tombstone merge (spec §15: "a
	// concurrent re-add wins by the generation rule ... its new-generation
	// publication supersedes the tombstone merge for that name").
	cache.StoreWalkSnapshot(hubcore.RemoteThreadSnapshot{
		Threads: []appwire.Thread{{ID: "stale-tomb", Source: "side", Evener: appwire.EvenerThread{Ref: "evener://side/stale-tomb"}}},
		Sources: map[string]hubcore.RemoteSourceSnapshot{
			"side": {Threads: []appwire.Thread{{ID: "stale-tomb", Source: "side"}}, Tombstoned: true},
		},
	}, map[string]uint64{})
	for _, thread := range cache.Get() {
		if thread.ID == "stale-tomb" {
			t.Fatal("a tombstone-tagged publication was admitted after the re-add")
		}
	}
}

// TestHostTombstoneExpiryReadFilter pins spec §15's lazy expiry: `list` filters
// an expired tombstone in memory WITHOUT pruning durably, and the next mutation
// path prunes it in its atomic write, advancing and persisting the name's
// presence epoch in the high-water entry.
func TestHostTombstoneExpiryReadFilter(t *testing.T) {
	f := newUpdateFixture(t)
	removedAt := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	f.m.cfg.now = func() time.Time { return removedAt }
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tombstone := tombstoneFor(t, f.m, "side")
	// Past retention: list omits the row and writes nothing.
	f.m.cfg.now = func() time.Time { return removedAt.Add(DefaultHostTombstoneRetention + time.Hour) }
	before := readHubTOMLBytes(t, f.configPath)
	list, err := f.m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, row := range list.Hosts {
		if row.Name == "side" {
			t.Fatalf("expired tombstone still listed: %+v", row)
		}
	}
	if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(after, before) {
		t.Fatalf("list pruned durably:\n%s", after)
	}
	// The next mutation prunes it durably and advances the presence epoch.
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "later", Address: "later.example"}}); err != nil {
		t.Fatalf("Add(later): %v", err)
	}
	if _, still := f.m.cfg.store.tombstoneSnapshot()["side"]; still {
		t.Fatal("the mutation-path write did not prune the expired tombstone")
	}
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, carried := cfg.Tombstones["side"]; carried {
		t.Fatal("hub.toml still carries the expired tombstone")
	}
	mark, ok := cfg.Generations["side"]
	if !ok {
		t.Fatal("the prune dropped the name's high-water entry instead of advancing it")
	}
	if mark.PresenceEpoch <= tombstone.PresenceEpoch {
		t.Fatalf("prune recorded presence epoch %d, want an advance past %d", mark.PresenceEpoch, tombstone.PresenceEpoch)
	}
	if mark.Generation != tombstone.Generation || mark.IncarnationID != tombstone.IncarnationID {
		t.Fatalf("prune changed the mark's identity: %+v", mark)
	}
}

// TestHostTombstoneBootPrune pins spec §15's boot prune: a boot prunes expired
// tombstones durably even with no mutation since expiry, in the same atomic
// write posture, advancing the presence epoch into the high-water entry.
func TestHostTombstoneBootPrune(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	expired := HostTombstone{
		Name: "gone",
		Entry: HostConfig{
			Name: "gone",
			SSH:  "gone.example",
		},
		Origin:        hostOriginHubTOML,
		Generation:    1,
		IncarnationID: "00000000-0000-4000-8000-000000000001",
		PresenceEpoch: 4,
		// Long past any retention the test could run into.
		RemovedAt: "2020-01-01T00:00:00Z",
		Rows:      nil,
	}
	records := hostTOMLRecords{
		highWater: map[string]HostGeneration{
			"gone": {Generation: 1, IncarnationID: expired.IncarnationID, PresenceEpoch: 4},
		},
		tombstones: map[string]HostTombstone{"gone": expired},
	}
	if err := writeHubTOMLHostsRecords(configPath, nil, nil, records, false); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	boot := bootHostManager(t, configPath)
	if _, still := boot.cfg.store.tombstoneSnapshot()["gone"]; still {
		t.Fatal("boot did not prune the expired tombstone")
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, carried := cfg.Tombstones["gone"]; carried {
		t.Fatal("the boot prune did not rewrite hub.toml")
	}
	mark, ok := cfg.Generations["gone"]
	if !ok || mark.PresenceEpoch <= expired.PresenceEpoch {
		t.Fatalf("boot prune mark = %+v (ok %v), want an advanced presence epoch", mark, ok)
	}
	// The prune is per-boot idempotent: a second boot leaves the file alone.
	before := readHubTOMLBytes(t, configPath)
	_ = bootHostManager(t, configPath)
	if after := readHubTOMLBytes(t, configPath); !bytes.Equal(after, before) {
		t.Fatalf("a converged second boot rewrote hub.toml:\n%s", after)
	}
}

// TestHostTombstoneExpirySkipsGatedNames pins spec §15/§6: expiry never purges
// a tombstone whose name still holds an open teardown remnant, and the
// in-memory filter keeps rendering it (via the test-only gated seam).
func TestHostTombstoneExpirySkipsGatedNames(t *testing.T) {
	f := newUpdateFixture(t)
	removedAt := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	f.m.cfg.now = func() time.Time { return removedAt }
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	f.m.testOnlyRemnantGated = func(name string) bool { return name == "side" }
	f.m.cfg.now = func() time.Time { return removedAt.Add(DefaultHostTombstoneRetention + time.Hour) }
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "later", Address: "later.example"}}); err != nil {
		t.Fatalf("Add(later): %v", err)
	}
	if _, still := f.m.cfg.store.tombstoneSnapshot()["side"]; !still {
		t.Fatal("the mutation-path prune dropped a remnant-gated tombstone")
	}
	row := rowFor(t, f.m, "side")
	if !row.Removed {
		t.Fatalf("gated expired tombstone is not rendered: %+v", row)
	}
}

// TestHostTombstoneBootCollisionRebasesLiveEntry pins spec §15's boot collision:
// a retained tombstone colliding with a newly live entry makes the live host a
// new incarnation — generation strictly above the tombstone's high-water mark,
// fresh incarnation, advanced epoch — so pre-collision records stay superseded
// and live records stay unmarked.
func TestHostTombstoneBootCollisionRebasesLiveEntry(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	const oldIncarnation = "00000000-0000-4000-8000-0000000000aa"
	oldGeneration := uint64(5)
	// The hand edit: side is live again at the OLD identity, while the retained
	// tombstone still names the removed generation.
	entries := []hostreg.Host{{
		Name: "side", SSH: "side.example",
		Generation: oldGeneration, IncarnationID: oldIncarnation, PresenceEpoch: 6,
	}}
	recent := time.Now().UTC().Add(-2 * time.Hour)
	removedReceipt := newHostMutationReceipt("m-old", hostMutationRemove,
		hostreg.Host{Name: "side", SSH: "side.example", Generation: oldGeneration, IncarnationID: oldIncarnation, PresenceEpoch: 6},
		recent)
	receiptKey := hostMutationReceiptKey("m-old", "side", hostMutationRemove, hostMutationIdentity{
		Generation: oldGeneration, IncarnationID: oldIncarnation,
	})
	records := hostTOMLRecords{
		highWater: map[string]HostGeneration{
			"side": {Generation: oldGeneration, IncarnationID: oldIncarnation, PresenceEpoch: 6},
		},
		receipts: map[string]HostMutationReceipt{receiptKey: removedReceipt},
		tombstones: map[string]HostTombstone{
			"side": {
				Name: "side",
				Entry: HostConfig{
					Name: "side", SSH: "side.example",
				},
				Origin:        hostOriginHubTOML,
				Generation:    oldGeneration,
				IncarnationID: oldIncarnation,
				PresenceEpoch: 6,
				RemovedAt:     recent.Format(time.RFC3339),
			},
		},
	}
	if err := writeHubTOMLHostsRecords(configPath, entries, nil, records, false); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	boot := bootHostManager(t, configPath)
	rebased, ok := boot.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the colliding live entry vanished at boot")
	}
	if rebased.Generation <= oldGeneration {
		t.Fatalf("rebased generation = %d, want strictly above %d", rebased.Generation, oldGeneration)
	}
	if rebased.IncarnationID == oldIncarnation || rebased.IncarnationID == "" {
		t.Fatalf("rebased incarnation = %q, want a fresh id", rebased.IncarnationID)
	}
	if rebased.PresenceEpoch <= 6 {
		t.Fatalf("rebased presence epoch = %d, want an advance", rebased.PresenceEpoch)
	}
	// The tombstone is consumed by the live entry and the boot write persists
	// the rebased pair.
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, carried := cfg.Tombstones["side"]; carried {
		t.Fatal("the boot write left the colliding tombstone behind")
	}
	record, ok := cfg.HostRecords["side"]
	if !ok || record.IncarnationID != rebased.IncarnationID {
		t.Fatalf("host_records[side] = %+v, want the rebased incarnation", record)
	}
	// The pre-collision remove record stays at or below the mark: a replay is a
	// superseded hit for recovery, never a current-generation match, and the
	// live pair carries no old record.
	hit, err := boot.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID: "m-old", Name: "side", Kind: hostMutationRemove,
		Current:      hostMutationIdentity{Generation: rebased.Generation, IncarnationID: rebased.IncarnationID},
		CurrentKnown: true,
	})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if hit == nil || hit.Direct {
		t.Fatalf("pre-collision replay = %+v, want a superseded (non-direct) hit", hit)
	}
}

// TestHostTombstoneRecordValidationRefusesBoot pins spec §6's reserved-
// namespace posture: a tombstone record this build cannot decode or validate
// is refused loudly at load — the hard startup error — never read as a
// half-understood record or dropped by the next rewrite.
func TestHostTombstoneRecordValidationRefusesBoot(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	raw := hostTOMLBanner + `[tombstones."gone"]
name = "gone"
removed_at = "2026-09-20T00:00:00Z"
origin = "hub.toml"
generation = 3
incarnation_id = ""
presence_epoch = 4
rows_truncated = false

[tombstones."gone".entry]
name = "gone"
ssh = "gone.example"
`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("an incomplete tombstone record loaded, want the loud refusal")
	} else if !strings.Contains(err.Error(), "tombstones") {
		t.Fatalf("refusal = %v, want it to name the tombstones section", err)
	}
	// An undecodable extra field inside the record is the same refusal.
	raw2 := strings.Replace(raw, "incarnation_id = \"\"", "incarnation_id = \"00000000-0000-4000-8000-000000000001\"\nfuture_field = 7", 1)
	if err := os.WriteFile(configPath, []byte(raw2), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("a tombstone carrying a field this build does not decode loaded")
	}
}

// TestHostTombstoneWriteLeavesEntryFingerprints pins the fingerprint rule's
// by-construction half (spec §6: "machine-managed records ... excluded by
// construction, so a receipt write or a tombstone compaction cannot read as
// external configuration drift"): a removal's tombstone write does not move a
// live host's own entry fingerprint, which hashes the [[hosts]] entry alone.
func TestHostTombstoneWriteLeavesEntryFingerprints(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "keep", SSH: "keep.example"})
	keep, _ := f.m.cfg.hosts.Get("keep")
	before := hostEntryFingerprint(keep)
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	after, ok := f.m.cfg.hosts.Get("keep")
	if !ok {
		t.Fatal("the removal dropped an unrelated host")
	}
	if got := hostEntryFingerprint(after); got != before {
		t.Fatalf("entry fingerprint moved from %q to %q across a tombstone write", before, got)
	}
}

// TestHostTombstoneKnobsLoadAndDefaults pins the owner-adjustable knob family
// (spec §15: "owner-adjustable knobs in the same family as the cleared-marker
// TTL; the defaults ship in the implementing PR"): the shipped defaults, the
// explicit overrides, the integer-duration refusal, and the floor that keeps
// an unset knob from disabling a bound.
func TestHostTombstoneKnobsLoadAndDefaults(t *testing.T) {
	defaults := DefaultConfig()
	if defaults.HostTombstoneRetention != DefaultHostTombstoneRetention ||
		defaults.HostTombstoneMaxRows != DefaultHostTombstoneMaxRows ||
		defaults.HostTombstoneMaxRowBytes != DefaultHostTombstoneMaxRowBytes ||
		defaults.HostTombstoneMaxCount != DefaultHostTombstoneMaxCount ||
		defaults.HostTombstoneMaxBytes != DefaultHostTombstoneMaxBytes ||
		defaults.HostSupersededReceiptMaxCount != DefaultHostSupersededReceiptMaxCount ||
		defaults.HostPrunedReceiptMaxCount != DefaultHostPrunedReceiptMaxCount ||
		defaults.HostKeylessAuditMaxCount != DefaultHostKeylessAuditMaxCount {
		t.Fatalf("DefaultConfig does not ship the tombstone/retention defaults: %+v", defaults)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	body := "host_tombstone_retention = \"1h\"\n" +
		"host_tombstone_max_rows = 7\n" +
		"host_tombstone_max_count = 3\n" +
		"host_superseded_receipt_ttl = \"2h\"\n" +
		"host_pruned_receipt_max_count = 5\n" +
		"host_keyless_audit_ttl = \"3h\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HostTombstoneRetention != time.Hour || cfg.HostTombstoneMaxRows != 7 || cfg.HostTombstoneMaxCount != 3 ||
		cfg.HostSupersededReceiptTTL != 2*time.Hour || cfg.HostPrunedReceiptMaxCount != 5 || cfg.HostKeylessAuditTTL != 3*time.Hour {
		t.Fatalf("loaded knobs = %+v, want the explicit values", cfg)
	}
	// The integer duration footgun is refused, not floored.
	if err := os.WriteFile(path, []byte("host_tombstone_retention = 7\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("an integer host_tombstone_retention loaded")
	} else if !strings.Contains(err.Error(), "host_tombstone_retention") {
		t.Fatalf("refusal = %v, want it to name the knob", err)
	}
	// A zero knob takes the default rather than disabling the bound.
	if err := os.WriteFile(path, []byte("host_tombstone_max_rows = 0\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HostTombstoneMaxRows != DefaultHostTombstoneMaxRows {
		t.Fatalf("zero knob = %d, want the default %d", cfg.HostTombstoneMaxRows, DefaultHostTombstoneMaxRows)
	}
}

// TestHostTombstoneRequiresTheHighWaterTwin pins the review finding: a
// tombstone without its [generations] twin is not a shape this build writes —
// its twin is what seeds the boot counters, so a twinless record would let a
// re-add mint at or below the retained mark (§1: "Re-add mints strictly above
// every retained high-water mark for the name"). The load refuses loudly, and
// the complete pair re-adds strictly above the mark.
func TestHostTombstoneRequiresTheHighWaterTwin(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	raw := hostTOMLBanner + `[tombstones."gone"]
name = "gone"
removed_at = "2026-09-20T00:00:00Z"
origin = "hub.toml"
generation = 5
incarnation_id = "00000000-0000-4000-8000-0000000000aa"
presence_epoch = 6
rows_truncated = false

[tombstones."gone".entry]
name = "gone"
ssh = "gone.example"
`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("a twinless tombstone loaded, want the loud refusal")
	} else if !strings.Contains(err.Error(), "high-water twin") {
		t.Fatalf("refusal = %v, want it to name the missing twin", err)
	}
	// The complete pair loads and re-adds strictly above the mark. The
	// twinless file is removed first: the writer refuses to rewrite a file it
	// cannot read (the reserved-namespace posture again).
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove the refused file: %v", err)
	}
	records := hostTOMLRecords{
		highWater: map[string]HostGeneration{
			"gone": {Generation: 5, IncarnationID: "00000000-0000-4000-8000-0000000000aa", PresenceEpoch: 6},
		},
		tombstones: map[string]HostTombstone{
			"gone": {
				Name: "gone",
				Entry: HostConfig{
					Name: "gone", SSH: "gone.example",
				},
				Origin:        hostOriginHubTOML,
				Generation:    5,
				IncarnationID: "00000000-0000-4000-8000-0000000000aa",
				PresenceEpoch: 6,
				RemovedAt:     time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
			},
		},
	}
	if err := writeHubTOMLHostsRecords(configPath, nil, nil, records, false); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	boot := bootHostManager(t, configPath)
	if _, err := boot.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "gone", Address: "gone.example"}}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	added, _ := boot.cfg.hosts.Get("gone")
	if added.Generation <= 5 {
		t.Fatalf("re-add minted generation %d at or below the retained mark 5", added.Generation)
	}
}

// TestHostTombstoneEntryNameMustMatchKey pins roborev's Low finding on the S11
// review: the tombstone's nested effective-entry name is part of the record
// shape this build writes (it always equals the key), so a record whose
// `entry.name` disagrees — empty or another name — is refused loudly at load
// instead of round-tripping verbatim as a shape the hub can never produce
// (spec §6: "a reserved value whose shape this build cannot decode is refused
// loudly before any rewrite").
func TestHostTombstoneEntryNameMustMatchKey(t *testing.T) {
	for _, entryName := range []string{"", "other"} {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "hub.toml")
		raw := hostTOMLBanner + `[generations."gone"]
generation = 3
incarnation_id = "00000000-0000-4000-8000-000000000001"
presence_epoch = 4

[tombstones."gone"]
name = "gone"
removed_at = "2026-09-20T00:00:00Z"
origin = "hub.toml"
generation = 3
incarnation_id = "00000000-0000-4000-8000-000000000001"
presence_epoch = 4
rows_truncated = false
` + "\n[tombstones.\"gone\".entry]\nname = " + strconv.Quote(entryName) + "\nssh = \"gone.example\"\n"
		if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
			t.Fatalf("write hub.toml: %v", err)
		}
		if _, err := LoadConfig(configPath); err == nil {
			t.Fatalf("a tombstone whose entry.name is %q loaded, want the loud refusal", entryName)
		} else if !strings.Contains(err.Error(), "entry names") {
			t.Fatalf("refusal = %v, want it to name the disagreeing entry name", err)
		}
	}
}

// TestHostRemovalMirrorsTheRemovalMarker pins the S6 bridge §4's removed-host
// ordering reads: the same hub.toml write that records a removal tombstone
// forwards the removal instant into the operation store's mirror, and a re-add
// clears it — a tombstone's removed_at is the horizon anchor, and the store
// never dates a removal itself.
func TestHostRemovalMirrorsTheRemovalMarker(t *testing.T) {
	f := newUpdateFixture(t)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	f.m.cfg.ops = store
	removedAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f.m.cfg.now = func() time.Time { return removedAt }

	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tombstone := tombstoneFor(t, f.m, "side")
	markers := store.RemovedHosts()
	marker, ok := markers["side"]
	if !ok {
		t.Fatalf("removal markers = %+v, want the removed host dated", markers)
	}
	if marker.RemovedAt.Format(time.RFC3339) != tombstone.RemovedAt {
		t.Fatalf("mirrored removal = %s, want the tombstone's own removed_at %s", marker.RemovedAt.Format(time.RFC3339), tombstone.RemovedAt)
	}
	if marker.Generation != tombstone.Generation || marker.IncarnationID != tombstone.IncarnationID {
		t.Fatalf("mirrored removed pair = %d/%s, want the tombstone's own %d/%s",
			marker.Generation, marker.IncarnationID, tombstone.Generation, tombstone.IncarnationID)
	}
	// The name never held an operation record, so §4's rule has nothing to
	// validate and the store does not mirror a boundary it would discard: the
	// removal marker is the mirror's durable half here.
	if _, ok := store.Boundary("side"); ok {
		t.Fatal("the store mirrored a boundary for a record-less removed host")
	}

	// A re-add is the live mirror: the marker goes, so no later pass treats the
	// name as removed.
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if _, ok := store.RemovedHosts()["side"]; ok {
		t.Fatal("the re-add left the removal marker behind")
	}
}
