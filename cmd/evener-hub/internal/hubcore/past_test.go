package hubcore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

func writeMeta(t *testing.T, dir string, meta schema.SessionMeta) {
	t.Helper()
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
}

// foldNow folds entry against the index's current rebuildGen, the way a Find
// probe that observed no racing Rebuild would.
func foldNow(t *testing.T, idx *PastIndex, entry PastEntry) {
	t.Helper()
	idx.mu.RLock()
	rebuildGen := idx.rebuildGen
	idGen := idx.idGen[entry.ID]
	idx.mu.RUnlock()
	if !idx.foldOne(entry, rebuildGen, idGen) {
		t.Fatalf("foldOne declined for %s with no racing Rebuild", entry.ID)
	}
}

// fuzzScenarioPastIndex_FoldReplacesStalerIndexedRow pins a Rebuild swapping in
// the row it scanned (v1) after a Find's probe already read the newer on-disk
// meta (v2): foldOne must replace the stale indexed row, not keep it.
func fuzzScenarioPastIndex_FoldReplacesStalerIndexedRow(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "scanned-v1", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	fired := 0
	idx.SetOnChange(func() { fired++ })

	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "probed-v2", UpdatedAt: time.Unix(1_700_000_100, 0).UTC()})
	probe, ok, _ := idx.probeOne(id)
	if !ok {
		t.Fatal("expected probeOne to read the session")
	}
	foldNow(t, idx, probe)

	got, ok := idx.findCached(id)
	if !ok {
		t.Fatal("session missing from the index after the fold")
	}
	if got.Meta.Name != "probed-v2" {
		t.Fatalf("fold kept the staler indexed row: Name=%q, want %q", got.Meta.Name, "probed-v2")
	}
	if fired != 1 {
		t.Fatalf("onChange fired %d times for the fold replacement, want 1", fired)
	}
}

// fuzzScenarioPastIndex_RenameOrdersByEqualRevisionNameUpdatedAt pins the
// NameUpdatedAt fallback at equal Revision and UpdatedAt: a rename that re-saves
// at the same revision (e.g. legacy rows) must still win by its newer
// NameUpdatedAt. The other rename scenario goes through SaveSessionMeta, which
// bumps Revision, so it never reaches this fallback.
func fuzzScenarioPastIndex_RenameOrdersByEqualRevisionNameUpdatedAt(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{{ID: id, Name: "old", UpdatedAt: base, NameUpdatedAt: base, Revision: 7}})

	foldNow(t, idx, PastEntry{ID: id, Meta: schema.SessionMeta{
		ID:            id,
		Name:          "new",
		UpdatedAt:     base,
		NameUpdatedAt: base.Add(time.Minute),
		Revision:      7,
	}})

	got, ok := idx.findCached(id)
	if !ok {
		t.Fatal("session missing from the index after the fold")
	}
	if got.Meta.Name != "new" {
		t.Fatalf("equal-revision rename did not win on NameUpdatedAt: Name=%q, want %q", got.Meta.Name, "new")
	}
}

// fuzzScenarioPastIndex_FoldReplacesStalerIndexedRowOnRename pins the rename half
// of the freshness check: a rename preserves UpdatedAt and stamps NameUpdatedAt
// (app_rename.go), so a probe that read the renamed meta must still replace a
// stale scanned row.
func fuzzScenarioPastIndex_FoldReplacesStalerIndexedRowOnRename(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "old-name", NameUpdatedAt: base, UpdatedAt: base})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	fired := 0
	idx.SetOnChange(func() { fired++ })

	// Rename-only update: same UpdatedAt, newer NameUpdatedAt.
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "new-name", NameUpdatedAt: base.Add(time.Minute), UpdatedAt: base})
	probe, ok, _ := idx.probeOne(id)
	if !ok {
		t.Fatal("expected probeOne to read the session")
	}
	foldNow(t, idx, probe)

	got, ok := idx.findCached(id)
	if !ok {
		t.Fatal("session missing from the index after the fold")
	}
	if got.Meta.Name != "new-name" {
		t.Fatalf("fold discarded the rename-only update: Name=%q, want %q", got.Meta.Name, "new-name")
	}
	if fired != 1 {
		t.Fatalf("onChange fired %d times for the rename fold, want 1", fired)
	}
}

// fuzzScenarioPastIndex_StaleUpdateMetaDoesNotClobberNewerRow pins that an
// UpdateMeta carrying older metadata than the indexed row (a concurrent
// Rebuild/fold advanced it) is rejected rather than overwriting the newer row.
func fuzzScenarioPastIndex_StaleUpdateMetaDoesNotClobberNewerRow(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{{ID: id, Name: "newer", UpdatedAt: base, Revision: 5}})

	if changed := idx.UpdateMeta(id, schema.SessionMeta{ID: id, Name: "older", UpdatedAt: base, Revision: 1}); changed {
		t.Fatal("UpdateMeta of an older revision reported a change")
	}
	got, ok := idx.findCached(id)
	if !ok {
		t.Fatal("session missing from the index")
	}
	if got.Meta.Name != "newer" {
		t.Fatalf("stale UpdateMeta clobbered the newer indexed row: Name=%q, want %q", got.Meta.Name, "newer")
	}
}

// fuzzScenarioPastIndex_EvictionInvalidatesInFlightProbe pins that an eviction
// invalidates a probe that read the session before it: foldOne must decline for a
// probe whose id generation predates the eviction, so a concurrent Find cannot
// reinsert the deleted row after another Find evicted it.
func fuzzScenarioPastIndex_EvictionInvalidatesInFlightProbe(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{{ID: id, Name: "seeded", UpdatedAt: base}})

	idx.mu.RLock()
	probeRebuildGen := idx.rebuildGen
	probeIDGen := idx.idGen[id]
	idx.mu.RUnlock()

	if !idx.evict(id, probeRebuildGen, probeIDGen) { // another Find confirmed the disk no longer holds it
		t.Fatal("evict declined despite current generations")
	}

	// The in-flight probe's entry (read before the eviction) must not be folded.
	if idx.foldOne(PastEntry{ID: id, Meta: schema.SessionMeta{ID: id, Name: "probe", UpdatedAt: base}}, probeRebuildGen, probeIDGen) {
		t.Fatal("foldOne accepted an in-flight probe that predates the eviction")
	}
	if _, ok := idx.findCached(id); ok {
		t.Fatal("the evicted session was reinserted by the stale probe")
	}
}

// fuzzScenarioPastIndex_DeletedSessionEvictedAfterRacedRebuildSwap pins the
// Rebuild-side half: a Rebuild scans a session that is then deleted and swaps
// its now-stale scan in during Find's probe. Find's re-probe confirms the disk
// no longer holds it and evicts the row, so neither this nor a later cached Find
// returns the deleted session.
func fuzzScenarioPastIndex_DeletedSessionEvictedAfterRacedRebuildSwap(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))

	paused := make(chan struct{})
	release := make(chan struct{})
	prevSwap := pastBeforeRebuildSwap
	pastBeforeRebuildSwap = func() {
		close(paused)
		<-release
	}
	defer func() { pastBeforeRebuildSwap = prevSwap }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = idx.Rebuild()
	}()
	<-paused // the scan saw the session and is paused before its swap

	var once sync.Once
	idx.afterFindProbe = func() {
		once.Do(func() {
			// Delete the session, then publish the Rebuild's (now stale) scan.
			if err := os.Remove(sessionMetaPath(proj, id)); err != nil {
				t.Errorf("remove meta: %v", err)
			}
			close(release)
			<-done
		})
	}
	defer func() { idx.afterFindProbe = nil }()

	if got, ok := idx.Find(id); ok {
		t.Fatalf("Find returned a session deleted before the Rebuild swap: %+v", got)
	}
	if _, ok := idx.findCached(id); ok {
		t.Fatal("the deleted session stayed cached after the raced Rebuild swap")
	}
	if got, ok := idx.Find(id); ok {
		t.Fatalf("a later cached Find returned the evicted session: %+v", got)
	}
}

// fuzzScenarioPastIndex_DeletedSessionEvictedWhenRebuildSwapsBeforeProbe pins the
// window where a Rebuild swap lands between Find's top-level cache miss and its
// probe: the index then holds a row the disk no longer has, and Find's miss must
// evict it rather than leave it cached for the next rebuild interval.
func fuzzScenarioPastIndex_DeletedSessionEvictedWhenRebuildSwapsBeforeProbe(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))

	paused := make(chan struct{})
	release := make(chan struct{})
	prevSwap := pastBeforeRebuildSwap
	pastBeforeRebuildSwap = func() {
		close(paused)
		<-release
	}
	defer func() { pastBeforeRebuildSwap = prevSwap }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = idx.Rebuild()
	}()
	<-paused // the scan saw the session and is paused before its swap

	var once sync.Once
	idx.afterFindCacheMiss = func() {
		once.Do(func() {
			// Delete the session, then publish the Rebuild's stale scan so the
			// index holds it before Find probes.
			if err := os.Remove(sessionMetaPath(proj, id)); err != nil {
				t.Errorf("remove meta: %v", err)
			}
			close(release)
			<-done
		})
	}
	defer func() { idx.afterFindCacheMiss = nil }()

	if got, ok := idx.Find(id); ok {
		t.Fatalf("Find returned a session deleted before the Rebuild swap: %+v", got)
	}
	if _, ok := idx.findCached(id); ok {
		t.Fatal("the deleted session stayed cached after the Rebuild swap")
	}
	if got, ok := idx.Find(id); ok {
		t.Fatalf("a later Find returned the evicted session: %+v", got)
	}
}

// fuzzScenarioPastIndex_IndeterminateProbeMissDoesNotEvict pins the Medium: a
// probe that cannot read a project (unlistable sessions dir) is not proof of
// deletion, so Find must not evict a valid cached row for it — it returns the
// row the index still holds instead of reporting a false miss.
func fuzzScenarioPastIndex_IndeterminateProbeMissDoesNotEvict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on a directory is a no-op on Windows; the permission gate cannot be exercised")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	writeMeta(t, proj, schema.SessionMeta{ID: id, UpdatedAt: base})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	sessionsDir := filepath.Join(proj, "sessions")
	t.Cleanup(func() { _ = os.Chmod(sessionsDir, 0o755) })

	// A Rebuild scans S and pauses before its swap.
	paused := make(chan struct{})
	release := make(chan struct{})
	prevSwap := pastBeforeRebuildSwap
	pastBeforeRebuildSwap = func() {
		close(paused)
		<-release
	}
	defer func() { pastBeforeRebuildSwap = prevSwap }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = idx.Rebuild()
	}()
	<-paused

	var once sync.Once
	idx.afterFindProbe = func() {
		once.Do(func() {
			// Delete S and make its sessions dir unreadable, then publish the
			// stale scan (which still holds S). Find's next probe is now an
			// indeterminate miss.
			if err := os.Remove(sessionMetaPath(proj, id)); err != nil {
				t.Errorf("remove meta: %v", err)
			}
			if err := os.Chmod(sessionsDir, 0o000); err != nil {
				t.Errorf("chmod sessions: %v", err)
			}
			close(release)
			<-done
		})
	}
	defer func() { idx.afterFindProbe = nil }()

	// The probe is indeterminate, so the cached row must survive; Find reports
	// the row the index still holds rather than a false miss.
	got, ok := idx.Find(id)
	if !ok || got.ID != id {
		t.Fatalf("Find returned %+v, %v for a cached session an indeterminate probe must not evict", got, ok)
	}
	if _, ok := idx.findCached(id); !ok {
		t.Fatal("an indeterminate probe miss evicted a valid cached session")
	}
}

// fuzzScenarioPastIndex_TimestampNeutralFoldFiresOnChange pins that a fold which
// adopts a fork-label re-save (same timestamps) still fires onChange, so the Hub
// bumps/invalidates navigation.
func fuzzScenarioPastIndex_TimestampNeutralFoldFiresOnChange(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "s", UpdatedAt: base, NameUpdatedAt: base})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	fired := 0
	idx.SetOnChange(func() { fired++ })

	// A fork tag re-saves without moving either timestamp.
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "s", UpdatedAt: base, NameUpdatedAt: base, ForkLabel: "child"})
	probe, ok, _ := idx.probeOne(id)
	if !ok {
		t.Fatal("expected probeOne to read the session")
	}
	foldNow(t, idx, probe)

	if fired != 1 {
		t.Fatalf("timestamp-neutral fold fired onChange %d times, want 1", fired)
	}
}

// fuzzScenarioPastIndex_EvictingAbsentIDInvalidatesInFlightProbe pins Medium 2:
// a confirmed deletion must advance the id's generation even when the id is not
// currently indexed, so an in-flight probe (which reaches eviction via a cache
// miss) cannot pass its guard and reinsert the deleted row.
func fuzzScenarioPastIndex_EvictingAbsentIDInvalidatesInFlightProbe(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	idx := NewPastIndex("")
	idx.mu.RLock()
	before := idx.idGen[id]
	rebuildGen := idx.rebuildGen
	idx.mu.RUnlock()

	if !idx.evict(id, rebuildGen, before) { // the id is not indexed; a cache-miss Find reaches here
		t.Fatal("evict declined despite current generations")
	}

	idx.mu.RLock()
	after := idx.idGen[id]
	idx.mu.RUnlock()
	if after == before {
		t.Fatal("evicting an absent id did not bump its id generation")
	}
	if idx.foldOne(PastEntry{ID: id, Meta: schema.SessionMeta{ID: id}}, 0, before) {
		t.Fatal("foldOne accepted an in-flight probe that predates the eviction")
	}
}

// fuzzScenarioPastIndex_EvictDeclinesWhenGenerationsChanged pins Medium 2's
// TOCTOU: eviction must re-validate the generations the probe observed inside
// evict, so a Rebuild swap or another eviction between Find's check and the call
// cannot delete a row the probe never saw.
func fuzzScenarioPastIndex_EvictDeclinesWhenGenerationsChanged(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{{ID: id}})
	idx.mu.RLock()
	rebuildGen := idx.rebuildGen
	idGen := idx.idGen[id]
	idx.mu.RUnlock()

	if !idx.evict(id, rebuildGen, idGen) {
		t.Fatal("evict declined with the current generations")
	}
	if idx.evict(id, rebuildGen, idGen) {
		t.Fatal("evict proceeded with generations a later eviction had superseded")
	}
}

// fuzzScenarioPastIndex_EvictDeclinesWhenFoldRacedProbe pins the medium eviction
// TOCTOU: a miss captures the id's generation before its probe, but a concurrent
// fold can index the session in the window before the miss reaches evict. Since
// that fold bumps the id's generation, the stale miss must decline the eviction
// rather than delete the freshly folded row.
func fuzzScenarioPastIndex_EvictDeclinesWhenFoldRacedProbe(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	idx := NewPastIndex("")
	idx.mu.RLock()
	rebuildGen := idx.rebuildGen
	probeIDGen := idx.idGen[id]
	idx.mu.RUnlock()

	// A concurrent fold indexes the session between the miss's probe and its
	// eviction.
	if !idx.foldOne(PastEntry{ID: id, Meta: schema.SessionMeta{ID: id, Name: "folded"}}, rebuildGen, probeIDGen) {
		t.Fatal("foldOne declined with the current generations")
	}
	// The stale miss's eviction must re-validate the per-id generation and
	// decline instead of deleting the just-folded row.
	if idx.evict(id, rebuildGen, probeIDGen) {
		t.Fatal("evict deleted a row a racing fold had published")
	}
	if _, ok := idx.findCached(id); !ok {
		t.Fatal("the folded row was dropped by the stale miss")
	}
}

// fuzzScenarioPastIndex_EvictingUnrelatedIDPreservesInFlightFold pins the low
// global-generation interference: eviction invalidation is keyed per id, so a
// confirmed-absence eviction for an unrelated session cannot make an in-flight
// fold for another session decline (which, repeated, would exhaust Find's probe
// attempts and return a false miss for a session that exists on disk).
func fuzzScenarioPastIndex_EvictingUnrelatedIDPreservesInFlightFold(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	const other = "02wMz5Txv8Vo4rqb3QYZuV"
	idx := NewPastIndex("")
	idx.mu.RLock()
	rebuildGen := idx.rebuildGen
	probeIDGen := idx.idGen[id]
	otherIDGen := idx.idGen[other]
	idx.mu.RUnlock()

	// An unrelated id's confirmed-absence eviction...
	if !idx.evict(other, rebuildGen, otherIDGen) {
		t.Fatal("evict of the unrelated id declined")
	}
	// ...must leave an in-flight fold of the first id able to proceed.
	if !idx.foldOne(PastEntry{ID: id, Meta: schema.SessionMeta{ID: id, Name: "in-flight"}}, rebuildGen, probeIDGen) {
		t.Fatal("evicting an unrelated id invalidated a fold for another session")
	}
	if _, ok := idx.findCached(id); !ok {
		t.Fatal("the folded row is missing")
	}
}

// fuzzScenarioPastIndex_ConfirmedMissPrunesIDGeneration pins the bounded-memory
// half of the per-id fence: a confirmed miss for a nonexistent id must not leave
// a permanent idGen entry, nor leave a probe pin behind.
func fuzzScenarioPastIndex_ConfirmedMissPrunesIDGeneration(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, ok := idx.Find(id); ok {
		t.Fatal("Find returned a session that does not exist")
	}
	idx.mu.RLock()
	_, pinned := idx.probePins[id]
	_, fenced := idx.idGen[id]
	idx.mu.RUnlock()
	if pinned {
		t.Fatal("a completed Find left a probe pin")
	}
	if fenced {
		t.Fatal("a confirmed miss left a permanent generation fence")
	}
}

// fuzzScenarioPastIndex_RebuildPrunesStaleIDGenerations pins that a Rebuild keeps
// idGen bounded by the live index: an id the rescan no longer holds loses its
// generation fence.
func fuzzScenarioPastIndex_RebuildPrunesStaleIDGenerations(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	idx.SeedForTest([]schema.SessionMeta{{ID: id, Name: "seeded"}})
	idx.UpdateMeta(id, schema.SessionMeta{ID: id, Name: "renamed", Revision: 2})
	idx.mu.RLock()
	_, hasFence := idx.idGen[id]
	idx.mu.RUnlock()
	if !hasFence {
		t.Fatal("UpdateMeta did not advance the id's generation")
	}
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	idx.mu.RLock()
	_, still := idx.idGen[id]
	idx.mu.RUnlock()
	if still {
		t.Fatal("Rebuild kept a generation fence for an id it no longer indexes")
	}
}

// fuzzScenarioPastIndex_IndeterminateMissReturnsConcurrentlyIndexedRow pins the
// low finding: a concurrent fold or Rebuild can index the id between Find's
// top-level cache miss and its probe; if that probe is indeterminate, Find must
// still return the row the index now holds rather than report a false miss.
// Returning a cached row is not eviction, so the "never evict on an
// indeterminate miss" guarantee is untouched.
func fuzzScenarioPastIndex_IndeterminateMissReturnsConcurrentlyIndexedRow(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	// A matched project whose id is invalid makes probeOne indeterminate rather
	// than an authoritative absence (see ValidateProjectID's 10-char suffix rule).
	if err := os.MkdirAll(filepath.Join(projects, "not-a-project"), 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	idx := NewPastIndex(filepath.Join(projects, "*"))

	idx.afterFindCacheMiss = func() {
		// A concurrent writer indexed the session between Find's top-level cache
		// miss and its probe.
		foldNow(t, idx, PastEntry{ID: id, Meta: schema.SessionMeta{ID: id, Name: "raced"}})
	}
	defer func() { idx.afterFindCacheMiss = nil }()

	got, ok := idx.Find(id)
	if !ok {
		t.Fatal("Find reported a miss for a session a concurrent writer had indexed")
	}
	if got.ID != id {
		t.Fatalf("Find returned %q, want %q", got.ID, id)
	}
}

// fuzzScenarioPastIndex_MissingGlobBaseIsDeterminate pins the low: a projects
// root that does not exist is a definite absence, not an indeterminate one, so
// Find's miss is authoritative and evicts a stale cached row rather than
// serving (and holding) it until the next Rebuild.
func fuzzScenarioPastIndex_MissingGlobBaseIsDeterminate(t *testing.T) {
	root := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	// The projects root does not exist.
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	idx.afterFindCacheMiss = func() {
		// A stale Rebuild indexed the id before Find probes.
		foldNow(t, idx, PastEntry{ID: id, Meta: schema.SessionMeta{ID: id, Name: "stale"}})
	}
	defer func() { idx.afterFindCacheMiss = nil }()

	if got, ok := idx.Find(id); ok {
		t.Fatalf("Find returned %+v for a session under a missing projects root", got)
	}
	if _, ok := idx.findCached(id); ok {
		t.Fatal("a missing projects root is a determinate miss and must evict the stale row")
	}
}

// fuzzScenarioPastIndex_UnreadableGlobRootIsIndeterminate pins Medium 3: an
// inaccessible projects root makes filepath.Glob return no matches with no
// error, which must not read as an authoritative absence.
func fuzzScenarioPastIndex_UnreadableGlobRootIsIndeterminate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on a directory is a no-op on Windows; the permission gate cannot be exercised")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(filepath.Join(projects, "*"))
	if err := os.Chmod(projects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(projects, 0o755) })

	entry, found, determinate := idx.probeOne("02wMz5Txv1C3Hut0M8GCeB")
	if found {
		t.Fatalf("expected no session, got %+v", entry)
	}
	if determinate {
		t.Fatal("an unreadable glob root must be an indeterminate miss")
	}
}

// fuzzScenarioPastIndex_FindReProbesSessionCreatedDuringRebuild pins the
// successful re-probe path: a Rebuild scans while the session does not yet
// exist, the session is created, and the Rebuild publishes its (session-less)
// scan between Find's probe and its fold. Find must re-probe and index the
// session instead of reporting a miss.
func fuzzScenarioPastIndex_FindReProbesSessionCreatedDuringRebuild(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	paused := make(chan struct{})
	release := make(chan struct{})
	prevSwap := pastBeforeRebuildSwap
	pastBeforeRebuildSwap = func() {
		close(paused)
		<-release
	}
	defer func() { pastBeforeRebuildSwap = prevSwap }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = idx.Rebuild()
	}()
	<-paused // the scan saw an empty projects root and is paused before its swap

	const id = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})

	var once sync.Once
	idx.afterFindProbe = func() {
		// Publish the Rebuild's empty scan between the probe and the fold. Find
		// re-probes, so the seam fires again; release only once.
		once.Do(func() {
			close(release)
			<-done
		})
	}
	defer func() { idx.afterFindProbe = nil }()

	got, ok := idx.Find(id)
	if !ok {
		t.Fatal("Find missed a session created during a Rebuild scan")
	}
	if got.ID != id {
		t.Fatalf("Find returned %q, want %q", got.ID, id)
	}
	if _, ok := idx.findCached(id); !ok {
		t.Fatal("the session was not indexed after the re-probe")
	}
}

// fuzzScenarioPastIndex_FindDoesNotResurrectSessionRemovedByRebuild pins that a
// Rebuild completing during Find's probe (its scan did not find the session,
// because it was deleted after the probe read it) suppresses the fold instead of
// re-inserting the stale probe.
func fuzzScenarioPastIndex_FindDoesNotResurrectSessionRemovedByRebuild(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))

	idx.afterFindProbe = func() {
		// The session is deleted after the probe read it, and a Rebuild scans
		// (finding nothing) and swaps in the deletion.
		if err := os.Remove(sessionMetaPath(proj, id)); err != nil {
			t.Errorf("remove meta: %v", err)
		}
		if _, err := idx.Rebuild(); err != nil {
			t.Errorf("Rebuild: %v", err)
		}
	}
	defer func() { idx.afterFindProbe = nil }()

	if got, ok := idx.Find(id); ok {
		t.Fatalf("Find resurrected a session deleted during the probe: %+v", got)
	}
	if _, ok := idx.findCached(id); ok {
		t.Fatal("a deleted session is present in the index")
	}
}

// fuzzScenarioPastIndex_LegacyFirstResaveBeatsItsLegacyRow pins the mixed-pair
// tie: the first timestamp-neutral re-save of a legacy session advances Revision
// 0 -> 1 without moving either timestamp, so a probe carrying it must replace the
// legacy indexed row.
func fuzzScenarioPastIndex_LegacyFirstResaveBeatsItsLegacyRow(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{{ID: id, Name: "legacy", UpdatedAt: base}})

	// The first re-save (an AppendSessionObservedBy) bumps Revision to 1 with the
	// same timestamps.
	foldNow(t, idx, PastEntry{ID: id, Meta: schema.SessionMeta{
		ID:         id,
		Name:       "legacy",
		UpdatedAt:  base,
		Revision:   1,
		ObservedBy: []string{"02wMz5Txv8Vo4rqb3QYZuV"},
	}})

	got, ok := idx.findCached(id)
	if !ok {
		t.Fatal("session missing from the index after the fold")
	}
	if !slices.Contains(got.Meta.ObservedBy, "02wMz5Txv8Vo4rqb3QYZuV") {
		t.Fatalf("fold dropped the first re-save of a legacy row: ObservedBy=%v", got.Meta.ObservedBy)
	}
}

// fuzzScenarioPastIndex_LegacyRowOrdersByTimestampAgainstRevisioned pins that a
// row written before the Revision field existed (Revision 0) is ordered by its
// timestamps, not treated as older than every revisioned row. A legacy probe
// with a newer UpdatedAt must replace an older revisioned indexed row.
func fuzzScenarioPastIndex_LegacyRowOrdersByTimestampAgainstRevisioned(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{{ID: id, Name: "indexed", UpdatedAt: base, Revision: 5}})

	foldNow(t, idx, PastEntry{ID: id, Meta: schema.SessionMeta{ID: id, Name: "probe", UpdatedAt: base.Add(time.Minute)}})

	got, ok := idx.findCached(id)
	if !ok {
		t.Fatal("session missing from the index after the fold")
	}
	if got.Meta.Name != "probe" {
		t.Fatalf("legacy probe with a newer UpdatedAt was discarded for a revisioned row: Name=%q, want %q", got.Meta.Name, "probe")
	}
}

// fuzzScenarioPastIndex_StaleProbeDoesNotClobberNewerIndexedRow pins the
// tie-break direction: with equal timestamps a stale probe must not overwrite a
// newer indexed row. The probe reads v1, an external timestamp-neutral re-save
// produces v2, and a Rebuild indexes v2; folding the stale v1 probe must keep v2.
func fuzzScenarioPastIndex_StaleProbeDoesNotClobberNewerIndexedRow(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "s", UpdatedAt: base, NameUpdatedAt: base})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	probe, ok, _ := idx.probeOne(id) // reads v1
	if !ok {
		t.Fatal("expected probeOne to read the session")
	}

	// External timestamp-neutral re-save to v2, indexed by a concurrent Rebuild.
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "s", UpdatedAt: base, NameUpdatedAt: base, ForkLabel: "child"})
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got, _ := idx.findCached(id); got.Meta.ForkLabel != "child" {
		t.Fatalf("setup: the index did not take the re-save: ForkLabel=%q", got.Meta.ForkLabel)
	}

	foldNow(t, idx, probe) // stale v1 must not clobber the indexed v2

	got, ok := idx.findCached(id)
	if !ok {
		t.Fatal("session missing from the index after the fold")
	}
	if got.Meta.ForkLabel != "child" {
		t.Fatalf("stale probe clobbered the newer indexed row: ForkLabel=%q, want %q", got.Meta.ForkLabel, "child")
	}
}

// fuzzScenarioPastIndex_FoldReplacesStalerIndexedRowWithoutTimestampChange pins
// that freshness is not gated on timestamps alone: a fork tag (ForkLabel) and an
// observer append (ObservedBy) re-save the meta without advancing UpdatedAt or
// NameUpdatedAt, so a probe that read the re-saved meta must still replace the
// stale indexed row.
func fuzzScenarioPastIndex_FoldReplacesStalerIndexedRowWithoutTimestampChange(t *testing.T) {
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	cases := []struct {
		name  string
		newer schema.SessionMeta
		check func(t *testing.T, got schema.SessionMeta)
	}{
		{
			name:  "fork label",
			newer: schema.SessionMeta{ID: id, Name: "s", UpdatedAt: base, NameUpdatedAt: base, ForkLabel: "child"},
			check: func(t *testing.T, got schema.SessionMeta) {
				if got.ForkLabel != "child" {
					t.Fatalf("fold dropped the fork label: ForkLabel=%q, want %q", got.ForkLabel, "child")
				}
			},
		},
		{
			name:  "observer append",
			newer: schema.SessionMeta{ID: id, Name: "s", UpdatedAt: base, NameUpdatedAt: base, ObservedBy: []string{"02wMz5Txv8Vo4rqb3QYZuV"}},
			check: func(t *testing.T, got schema.SessionMeta) {
				if !slices.Contains(got.ObservedBy, "02wMz5Txv8Vo4rqb3QYZuV") {
					t.Fatalf("fold dropped the observer append: ObservedBy=%v", got.ObservedBy)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			proj := filepath.Join(root, "projects", "project-x-0123456789")
			if err := os.MkdirAll(proj, 0o755); err != nil {
				t.Fatal(err)
			}
			writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "s", UpdatedAt: base, NameUpdatedAt: base})
			idx := NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := idx.Rebuild(); err != nil {
				t.Fatal(err)
			}

			writeMeta(t, proj, tc.newer)
			probe, ok, _ := idx.probeOne(id)
			if !ok {
				t.Fatal("expected probeOne to read the session")
			}
			foldNow(t, idx, probe)

			got, ok := idx.findCached(id)
			if !ok {
				t.Fatal("session missing from the index after the fold")
			}
			tc.check(t, got.Meta)
		})
	}
}

// fuzzScenarioPastIndex_FindReturnsLiveRowAfterFold pins that Find returns the
// row the index actually holds after foldOne, not the probe's. A concurrent
// writer can index a strictly newer row for the id between this Find's cache
// lookup and foldOne; returning the probe's older meta would hand the caller
// stale state in exactly the probe-vs-scan race this addresses.
func fuzzScenarioPastIndex_FindReturnsLiveRowAfterFold(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	base := time.Unix(1_700_000_000, 0).UTC()
	writeMeta(t, proj, schema.SessionMeta{ID: id, Name: "probed-v1", UpdatedAt: base})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))

	idx.afterFindProbe = func() {
		// A concurrent writer indexed a newer row for the same id first.
		foldNow(t, idx, PastEntry{
			ID:       id,
			Meta:     schema.SessionMeta{ID: id, Name: "live-v2", UpdatedAt: base.Add(time.Minute), Revision: 2},
			StateDir: proj,
		})
	}
	defer func() { idx.afterFindProbe = nil }()

	got, ok := idx.Find(id)
	if !ok {
		t.Fatal("expected Find to surface the session")
	}
	if got.Meta.Name != "live-v2" {
		t.Fatalf("Find returned the probe's stale meta: Name=%q, want %q", got.Meta.Name, "live-v2")
	}
}

func fuzzScenarioPastIndex_RebuildLoadsAllMetas(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "projects", "project-a-0123456789")
	projB := filepath.Join(root, "projects", "project-b-0123456789")
	for _, p := range []string{projA, projB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	writeMeta(t, projA, schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		Model:          "gpt-5.2",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/a"},
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-1 * time.Hour),
		OriginalPrompt: "fix the bug",
	})
	writeMeta(t, projB, schema.SessionMeta{
		ID:             "02wMz5Txv2enqVTitaig6F",
		Model:          "claude-opus-4-7",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/b"},
		CreatedAt:      now.Add(-30 * time.Minute),
		UpdatedAt:      now,
		OriginalPrompt: "refactor auth",
	})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := idx.All()
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	// Sorted by UpdatedAt desc.
	if got[0].ID != "02wMz5Txv2enqVTitaig6F" {
		t.Errorf("first: %s", got[0].ID)
	}
}

func fuzzScenarioPastIndex_RebuildOrdersByUpdatedCreatedTitleAndID(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvFpYrooBkiqxAp",
		CreatedAt:      updated.Add(-2 * time.Hour),
		UpdatedAt:      updated,
		OriginalPrompt: "beta task",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv8Vo4rqb3QYZuV",
		CreatedAt:      updated.Add(-time.Hour),
		UpdatedAt:      updated,
		OriginalPrompt: "alpha task",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvHIJQPOuIBJQct",
		CreatedAt:      updated.Add(-3 * time.Hour),
		UpdatedAt:      updated.Add(-time.Hour),
		OriginalPrompt: "bravo task",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvIl3yzzcpdlu4x",
		CreatedAt:      updated.Add(-3 * time.Hour),
		UpdatedAt:      updated.Add(-time.Hour),
		OriginalPrompt: "alpha task",
	})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	got := idx.Search("task", 10, 0)
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.ID)
	}
	want := []string{"02wMz5Txv8Vo4rqb3QYZuV", "02wMz5TxvFpYrooBkiqxAp", "02wMz5TxvIl3yzzcpdlu4x", "02wMz5TxvHIJQPOuIBJQct"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", gotIDs, want)
	}
}

func fuzzScenarioPastIndex_Search(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/a"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix the bug in handler",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv2enqVTitaig6F",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/b"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "refactor auth flow",
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv2enqVTitaig6F" {
		t.Fatalf("Search auth: got %v", got)
	}
	got = idx.Search("/work/a", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("Search /work/a: got %v", got)
	}
	got = idx.Search("02wMz5Txv2enqVTitaig6F", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv2enqVTitaig6F" {
		t.Fatalf("Search 01B: got %v", got)
	}
	got = idx.Search("xyz", 50, 0)
	if len(got) != 0 {
		t.Fatalf("Search xyz: got %v", got)
	}
}

func fuzzScenarioPastIndex_SearchMatchesGeneratedName(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv47YP64RR3B9YJ",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("cheap model", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv47YP64RR3B9YJ" {
		t.Fatalf("Search cheap model: got %v", got)
	}
}

func fuzzScenarioPastIndex_SearchSQLiteFTSMatchesGeneratedName(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv47YP64RR3B9YJ",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".evener", "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("cheap model", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv47YP64RR3B9YJ" {
		t.Fatalf("Search cheap model: got %v", got)
	}
}

func fuzzScenarioPastIndex_SearchUsesSQLiteFTSWhenConfigured(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv5aIxgf9yVdd0N",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/auth-service"},
		UpdatedAt:      time.Now().Add(time.Hour),
		OriginalPrompt: "repair login token refresh",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv733WHFsVy66SR",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/invoices"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "invoice cleanup",
	})

	dbPath := filepath.Join(root, ".evener", "index.db")
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected sqlite index at %s: %v", dbPath, err)
	}

	got := idx.Search("token", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv5aIxgf9yVdd0N" {
		t.Fatalf("Search token: got %v", got)
	}
	got = idx.Search("auth-service", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv5aIxgf9yVdd0N" {
		t.Fatalf("Search working dir: got %v", got)
	}
	got = idx.Search("02wMz5Txv733", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv733WHFsVy66SR" {
		t.Fatalf("Search ID prefix: got %v", got)
	}
	got = idx.Search("733", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv733WHFsVy66SR" {
		t.Fatalf("Search ID substring: got %v", got)
	}
}

func fuzzScenarioPastIndex_SearchWithSQLitePreservesSubstringMatches(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvKDoXaaLN6ENX1",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/prefix"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "auth token cleanup",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvLgZ6BB3uYgqz5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/substr"},
		UpdatedAt:      time.Now().Add(-time.Minute),
		OriginalPrompt: "preauth redirect cleanup",
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".evener", "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth", 50, 0)
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.ID)
	}
	want := []string{"02wMz5TxvKDoXaaLN6ENX1", "02wMz5TxvLgZ6BB3uYgqz5"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("Search auth IDs=%v, want %v", gotIDs, want)
	}
}

func fuzzScenarioPastIndex_SearchWithSQLiteMergesFTSAndSubstringMatches(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvBRJC3228LTWod",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/fts"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "auth token cleanup",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvLgZ6BB3uYgqz5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/substr"},
		UpdatedAt:      time.Now().Add(-time.Minute),
		OriginalPrompt: "preauth cleanup",
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".evener", "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth cleanup", 50, 0)
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.ID)
	}
	want := []string{"02wMz5TxvBRJC3228LTWod", "02wMz5TxvLgZ6BB3uYgqz5"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("Search auth cleanup IDs=%v, want %v", gotIDs, want)
	}
}

func fuzzScenarioPastIndex_SQLiteIndexUsesPrivateFilePermissions(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5TxvLgZ6BB3uYgqz5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/private"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "sensitive prompt",
	})
	indexDir := filepath.Join(root, ".evener")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(indexDir, "index.db")
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("index dir mode=%#o, want existing 0755", got)
	}
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("index db mode=%#o, want 0600", got)
	}
}

func fuzzScenarioPastIndex_SearchFallsBackWhenSQLiteUnavailable(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/a"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix auth",
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), root)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("fallback search: got %v", got)
	}
}

func fuzzScenarioPastIndex_Pagination(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	for i := range 5 {
		writeMeta(t, proj, schema.SessionMeta{
			ID:        []string{"02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv2enqVTitaig6F", "02wMz5Txv47YP64RR3B9YJ", "02wMz5Txv5aIxgf9yVdd0N", "02wMz5Txv733WHFsVy66SR"}[i],
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	page1 := idx.Search("", 2, 0)
	page2 := idx.Search("", 2, 2)
	page3 := idx.Search("", 2, 4)
	if len(page1) != 2 || len(page2) != 2 || len(page3) != 1 {
		t.Fatalf("pagination: %d/%d/%d", len(page1), len(page2), len(page3))
	}
}

func fuzzScenarioPastIndex_Find(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		UpdatedAt: time.Now(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got, ok := idx.Find("02wMz5Txv1C3Hut0M8GCeB")
	if !ok {
		t.Fatal("expected found")
	}
	if got.Meta.ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Errorf("meta.ID: %q", got.Meta.ID)
	}
	if got.StateDir != proj {
		t.Errorf("StateDir: %q want %q", got.StateDir, proj)
	}
}

func fuzzScenarioPastIndex_FindRefreshesNewSessionOnMiss(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Find("02wMz5Txv8Vo4rqb3QYZuV"); ok {
		t.Fatal("session should not be indexed before meta exists")
	}

	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv8Vo4rqb3QYZuV",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "created after hub start",
	})
	got, ok := idx.Find("02wMz5Txv8Vo4rqb3QYZuV")
	if !ok {
		t.Fatal("expected Find to refresh a newly persisted session")
	}
	if got.StateDir != proj {
		t.Errorf("StateDir: %q want %q", got.StateDir, proj)
	}
}

func fuzzScenarioPastIndex_FindWithMalformedGlob(t *testing.T) {
	idx := NewPastIndex("[unclosed")
	// Rebuild must propagate the glob compile error rather than swallow it: a
	// silently-ignored ErrBadPattern leaves the index empty so Find still
	// reports false, masking the lost error.
	if _, err := idx.Rebuild(); !errors.Is(err, filepath.ErrBadPattern) {
		t.Fatalf("expected Rebuild to propagate ErrBadPattern, got %v", err)
	}
	got, ok := idx.Find("anything")
	if ok {
		t.Fatal("expected Find to return false on malformed glob")
	}
	if got.ID != "" || got.StateDir != "" {
		t.Errorf("expected zero PastEntry on miss, got %+v", got)
	}
}

// fuzzScenarioPastIndex_FindMissDoesNotRebuild pins the cost contract the
// probe exists for: a Find that misses must not decode every meta on disk.
// The probe reads only the requested session's file; the observable proof is
// that a DIFFERENT session's meta, persisted after the index was built, does
// not enter the index via the miss (a full Rebuild would have indexed it).
func fuzzScenarioPastIndex_FindMissDoesNotRebuild(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		UpdatedAt: time.Now(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// A second session lands on disk after the index was built.
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv2enqVTitaig6F",
		UpdatedAt: time.Now(),
	})

	// A valid-shaped id, absent everywhere — minted rather than hand-typed
	// so it cannot silently regress to an INVALID id (which returns at
	// Find's ValidateSessionID guard and makes this scenario vacuous).
	absent := hubtest.SessionID(t)
	if _, ok := idx.Find(absent); ok {
		t.Fatal("expected miss for a session that does not exist")
	}
	if _, ok := idx.findCached("02wMz5Txv2enqVTitaig6F"); ok {
		t.Fatal("Find miss indexed an unrelated session; the miss path must not run a full rebuild")
	}
	if all := idx.All(); len(all) != 1 {
		t.Fatalf("index holds %d entries after a Find miss, want the 1 it was built with", len(all))
	}
}

// fuzzScenarioPastIndex_FindProbeKeepsLastProject pins the probe's
// multi-project resolution: Rebuild's byID map keeps the LAST project in
// glob order for a session present in several projects, and the probe must
// agree so a probe-folded entry matches what the next Rebuild would produce.
func fuzzScenarioPastIndex_FindProbeKeepsLastProject(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "projects", "project-a-0123456789")
	projB := filepath.Join(root, "projects", "project-b-0123456789")
	for _, p := range []string{projA, projB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMeta(t, projA, schema.SessionMeta{
		ID:             "02wMz5Txv8Vo4rqb3QYZuV",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "from project a",
	})
	writeMeta(t, projB, schema.SessionMeta{
		ID:             "02wMz5Txv8Vo4rqb3QYZuV",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "from project b",
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	got, ok := idx.Find("02wMz5Txv8Vo4rqb3QYZuV")
	if !ok {
		t.Fatal("expected the probe to find the session")
	}
	if got.StateDir != projB {
		t.Fatalf("StateDir = %q, want the last project in glob order %q", got.StateDir, projB)
	}
	if got.Meta.OriginalPrompt != "from project b" {
		t.Fatalf("OriginalPrompt = %q, want the last project's meta", got.Meta.OriginalPrompt)
	}
	// The fold must agree with what a full Rebuild produces for the same id.
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	rebuilt, ok := idx.findCached("02wMz5Txv8Vo4rqb3QYZuV")
	if !ok || rebuilt.StateDir != projB || rebuilt.Meta.OriginalPrompt != "from project b" {
		t.Fatalf("probe and rebuild disagree: probe=%+v rebuilt=%+v ok=%v", got, rebuilt, ok)
	}
}

// fuzzScenarioPastIndex_FindFoldsProbedEntry pins the fold: a session the
// probe surfaces must enter the sorted index (All/Find see it afterward) and
// fire onChange exactly once for the content delta — the same gating a full
// Rebuild applies.
func fuzzScenarioPastIndex_FindFoldsProbedEntry(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		UpdatedAt: time.Unix(1_700_000_000, 0).UTC(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	fired := 0
	idx.SetOnChange(func() { fired++ })

	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv2enqVTitaig6F",
		UpdatedAt: time.Unix(1_700_000_100, 0).UTC(),
	})
	entry, ok := idx.Find("02wMz5Txv2enqVTitaig6F")
	if !ok {
		t.Fatal("expected Find to surface the newly persisted session")
	}
	if entry.StateDir != proj {
		t.Fatalf("StateDir = %q, want %q", entry.StateDir, proj)
	}
	if all := idx.All(); len(all) != 2 {
		t.Fatalf("index holds %d entries after the probe fold, want 2", len(all))
	}
	if fired != 1 {
		t.Fatalf("onChange fired %d times for the probe fold, want 1", fired)
	}
	// A second Find of the now-indexed id must be a pure cache hit: no
	// further delta, no further fire.
	if _, ok := idx.Find("02wMz5Txv2enqVTitaig6F"); !ok {
		t.Fatal("expected the folded entry to be cached")
	}
	if fired != 1 {
		t.Fatalf("onChange fired %d times after a cache-hit Find, want 1", fired)
	}
}

// fuzzScenarioPastIndex_FindFoldWritesFTS pins the fold's FTS mirror on the
// production configuration: the hub always runs NewPastIndexWithDB (main.go),
// so a probe-folded session must land in the SQLite FTS index too, or search
// would miss a session Find just surfaced until the next full Rebuild. The
// assertion goes through searchFTS (the FTS-only path), not Search: Search
// unions FTS with the in-memory scan, which would rescue the assertion even
// if the fold never wrote FTS.
func fuzzScenarioPastIndex_FindFoldWritesFTS(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// A second session lands on disk after the index was built, then Find
	// folds it; Search must surface it through the FTS path.
	const foldedID = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{
		ID:             foldedID,
		UpdatedAt:      time.Unix(1_700_000_100, 0).UTC(),
		OriginalPrompt: "fts-probe-needle",
	})
	if _, ok := idx.Find(foldedID); !ok {
		t.Fatal("expected Find to surface the newly persisted session")
	}
	results, ok := idx.searchFTS("fts-probe-needle")
	if !ok {
		t.Fatal("FTS index unavailable after the fold; searchFTS must serve the folded row")
	}
	if !slices.ContainsFunc(results, func(e PastEntry) bool { return e.ID == foldedID }) {
		t.Fatalf("FTS did not surface the probe-folded session %s (results: %d entries)", foldedID, len(results))
	}
}

// fuzzScenarioPastIndex_SearchRepairsStaleFTS pins the repair path for a FTS
// lost-update: when the FTS mirror is stale (a fold's or rebuild's rebuildFTS
// lost its SQLITE_BUSY race, or the db was briefly unwritable), the in-memory
// index still serves Find from cache — but search would miss the session. A
// Search must notice i.fts is false and re-publish, so searchFTS serves every
// indexed id again. Without the repair, staleness persisted until the next
// full Rebuild (which itself could lose the same race).
func fuzzScenarioPastIndex_SearchRepairsStaleFTS(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{
		ID:             sessionID,
		UpdatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		OriginalPrompt: "fts-repair-needle",
	})
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// Simulate the lost-update: force the FTS mirror stale while the
	// in-memory index stays populated.
	idx.mu.Lock()
	idx.fts = false
	idx.mu.Unlock()
	if results, ok := idx.searchFTS("fts-repair-needle"); ok && len(results) > 0 {
		t.Fatalf("FTS unexpectedly served the session while stale (results: %d)", len(results))
	}

	// A Search must repair the mirror (and still serve the session from
	// the in-memory scan while it does).
	results := idx.Search("fts-repair-needle", 10, 0)
	if !slices.ContainsFunc(results, func(e PastEntry) bool { return e.ID == sessionID }) {
		t.Fatalf("Search did not serve the session while the mirror was stale (results: %d entries)", len(results))
	}
	results, ok := idx.searchFTS("fts-repair-needle")
	if !ok {
		t.Fatal("FTS index still unavailable after the repair; Search must re-publish a stale mirror")
	}
	if !slices.ContainsFunc(results, func(e PastEntry) bool { return e.ID == sessionID }) {
		t.Fatalf("FTS did not surface the repaired session %s (results: %d entries)", sessionID, len(results))
	}
}

// fuzzScenarioPastIndex_FailedPublishMarksFTSStale pins the other half of the
// repair contract: a publish whose FTS write fails (a fold's or rebuild's
// rebuildFTS losing its SQLITE_BUSY race) must leave i.fts false — not
// stale-but-marked-healthy — or Search's staleness gate cannot see the loss.
// The gate only detects what failed publishes admit to.
func fuzzScenarioPastIndex_FailedPublishMarksFTSStale(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{
		ID:             sessionID,
		UpdatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		OriginalPrompt: "fts-failed-publish-needle",
	})
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// Break the db for the next publish, then fold a second session through
	// Find: the fold's rebuildFTS fails, and publishAndSignal must mark the
	// mirror stale rather than leave the pre-fold healthy flag standing.
	origOpen := idx.openDB
	idx.mu.Lock()
	idx.openDB = func(_, _ string) (*sql.DB, error) { return nil, errors.New("db unavailable") }
	idx.mu.Unlock()
	const foldedID = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{
		ID:             foldedID,
		UpdatedAt:      time.Unix(1_700_000_100, 0).UTC(),
		OriginalPrompt: "fts-failed-fold-needle",
	})
	if _, ok := idx.Find(foldedID); !ok {
		t.Fatal("expected Find to surface the newly persisted session")
	}
	idx.mu.Lock()
	ftsMarkedStale := !idx.fts
	idx.openDB = origOpen
	idx.mu.Unlock()
	if !ftsMarkedStale {
		t.Fatal("a failed publish left i.fts true; Search's staleness gate cannot detect the loss")
	}

	// Restore the db; the next Search must repair the mirror and surface
	// the folded session through the FTS-only path.
	results, ok := idx.searchFTS("fts-failed-fold-needle")
	if ok && len(results) > 0 {
		t.Fatalf("FTS unexpectedly served the folded session while stale (results: %d)", len(results))
	}
	if search := idx.Search("fts-failed-fold-needle", 10, 0); !slices.ContainsFunc(search, func(e PastEntry) bool { return e.ID == foldedID }) {
		t.Fatalf("Search did not serve the folded session while the mirror was stale (results: %d entries)", len(search))
	}
	results, ok = idx.searchFTS("fts-failed-fold-needle")
	if !ok || !slices.ContainsFunc(results, func(e PastEntry) bool { return e.ID == foldedID }) {
		t.Fatalf("FTS did not surface the folded session after the repair (ok=%v results: %d entries)", ok, len(results))
	}
}

// fuzzScenarioPastIndex_RebuildDedupesDuplicateSessionIDs pins one row per
// id in i.all when the same session id is persisted under two projects:
// byID keeps the LAST project's entry, and i.all must agree. A duplicated
// row made UpdateMeta's remove-all-rows filter and the next Rebuild fight
// over the shape (2 rows → 1 → 2 …), firing onChange every cycle on
// unchanged disk.
func fuzzScenarioPastIndex_RebuildDedupesDuplicateSessionIDs(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "projects", "project-a-0123456789")
	projB := filepath.Join(root, "projects", "project-b-0123456789")
	for _, p := range []string{projA, projB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const dupID = "02wMz5Txv8Vo4rqb3QYZuV"
	// Different names, identical timestamps: byID and i.all must both keep
	// the LAST project's row, not just agree on cardinality — a first-row
	// i.all flaps against byID's last row every UpdateMeta/Rebuild round.
	writeMeta(t, projA, schema.SessionMeta{ID: dupID, Name: "from-a", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
	writeMeta(t, projB, schema.SessionMeta{ID: dupID, Name: "from-b", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if all := idx.All(); len(all) != 1 {
		t.Fatalf("index holds %d entries for one session id, want 1 (one row per id, last project wins)", len(all))
	}
	if all := idx.All(); all[0].Meta.Name != "from-b" {
		t.Fatalf("i.all kept %q, want the LAST project's row %q", all[0].Meta.Name, "from-b")
	}

	// Steady state: repeated UpdateMeta (RefreshOne's path) + Rebuild rounds
	// must not fire onChange — nothing on disk is changing.
	fired := 0
	idx.SetOnChange(func() { fired++ })
	for range 3 {
		idx.UpdateMeta(dupID, schema.SessionMeta{ID: dupID, Name: "from-b", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
		if _, err := idx.Rebuild(); err != nil {
			t.Fatal(err)
		}
	}
	if fired != 0 {
		t.Fatalf("onChange fired %d times over steady-state rounds on unchanged disk, want 0", fired)
	}
}

// fuzzScenarioPastIndex_FindSkipsUnlistableSessionsDir pins the probe's
// Rebuild-parity gate: a sessions dir that is traversable but not listable
// makes Rebuild's ListSessionMetas fail (project skipped) while reading the
// meta by its known path still succeeds. The probe must apply the same gate,
// or Find would fold a session Rebuild keeps dropping — flapping the index
// and firing onChange every 60s cycle on unchanged disk.
func fuzzScenarioPastIndex_FindSkipsUnlistableSessionsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on a directory is a no-op on Windows; the permission gate cannot be exercised")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	sessions := filepath.Join(proj, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeMeta(t, proj, schema.SessionMeta{ID: sessionID, UpdatedAt: time.Now()})
	if err := os.Chmod(sessions, 0o311); err != nil {
		t.Skipf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(sessions, 0o755) }()

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// Find must NOT surface the session Rebuild cannot list: the probe's
	// gate must fail the same way the rebuild did.
	if _, ok := idx.Find(sessionID); ok {
		t.Fatal("Find surfaced a session whose sessions dir Rebuild cannot list; the probe must apply Rebuild's gate")
	}
	if all := idx.All(); len(all) != 0 {
		t.Fatalf("index holds %d entries, want 0 (Rebuild skipped the project)", len(all))
	}
}

func fuzzScenarioPastIndex_RebuildFTSError(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix auth",
	})
	// Use a directory as the DB path so SQLite open fails.
	dbPath := filepath.Join(root, "index.db")
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// The Rebuild should succeed even though rebuildFTS failed.
	// FTS flag should be false.
	got := idx.Search("auth", 50, 0)
	if len(got) != 1 || got[0].ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("expected fallback search to work: got %v", got)
	}
}

func fuzzScenarioPastIndex_AllMetas(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		UpdatedAt: time.Now(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	metas := idx.AllMetas()
	if len(metas) != 1 || metas[0].ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("AllMetas: got %v", metas)
	}
}

func fuzzScenarioPastIndex_SearchFTSSpecialCharsOnly(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix auth",
	})
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".evener", "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// A special-char-only query yields no FTS tokens, so ftsQuery must return ""
	// to skip the FTS path entirely rather than emit a malformed MATCH string.
	// Observing this directly proves the fallback decision; the zero-results
	// check alone holds regardless of which path ran.
	if q := ftsQuery("!@#$%"); q != "" {
		t.Fatalf("ftsQuery(special-only): got %q want empty", q)
	}
	// Search then falls back to the in-memory substring scan, which finds no
	// match for a query with no alphanumeric content.
	got := idx.Search("!@#$%", 50, 0)
	if len(got) != 0 {
		t.Fatalf("expected no results for special-char-only query: got %v", got)
	}
}

func fuzzScenarioChmodSQLiteIndexFiles_Error(t *testing.T) {
	dir := t.TempDir()
	// Place the db path under a parent component that is a regular file, not a
	// directory. chmod on any path beneath it returns ENOTDIR regardless of uid,
	// so this exercises the error path even when the test runs as root.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("project-x-0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(blocker, "db")
	err := chmodSQLiteIndexFiles(dbPath)
	if err == nil {
		t.Fatal("expected error when a parent path component is a regular file")
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("expected ENOTDIR, got %v", err)
	}
}

func fuzzScenarioPastIndex_FindEmptyStateGlob(t *testing.T) {
	idx := NewPastIndex("")
	got, ok := idx.Find("something")
	if ok {
		t.Fatal("expected Find to return false when stateGlob is empty")
	}
	if got.ID != "" || got.StateDir != "" {
		t.Errorf("expected zero PastEntry on miss, got %+v", got)
	}
}

// TestPastIndex_FindEmptySessionIDSkipsRebuild pins the other half of Find's
// short-circuit guard: an empty session id must return false WITHOUT triggering
// a rebuild, even when the glob points at real on-disk sessions. Dropping the
// guard would route the empty id through Rebuild, which populates the index as a
// side effect — observable here as a non-empty All() snapshot.
func fuzzScenarioPastIndex_FindEmptySessionIDSkipsRebuild(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		UpdatedAt: time.Now(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	got, ok := idx.Find("")
	if ok {
		t.Fatal("expected Find to return false for an empty session id")
	}
	if got.ID != "" || got.StateDir != "" {
		t.Errorf("expected zero PastEntry on miss, got %+v", got)
	}
	if all := idx.All(); len(all) != 0 {
		t.Fatalf("Find(\"\") must not trigger a rebuild, but index holds %d entries", len(all))
	}
}

func fuzzScenarioPastIndexOnChangeFiresOnContentDeltaOnly(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "project-test-0123456789")
	writeMeta := func(id string) {
		m := schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
		if err := schema.SaveSessionMeta(proj, m); err != nil {
			t.Fatal(err)
		}
	}
	writeMeta("02wMz5Txv1C3Hut0M8GCeB")
	idx := NewPastIndex(filepath.Join(dir, "*"))
	fired := 0
	idx.SetOnChange(func() { fired++ })
	_, _ = idx.Rebuild() // first content load: delta vs empty → fires
	if fired != 1 {
		t.Fatalf("first rebuild with content should fire once, got %d", fired)
	}
	_, _ = idx.Rebuild() // identical content → no delta → no fire
	if fired != 1 {
		t.Fatalf("re-rebuild with no delta must not fire, got %d", fired)
	}
	writeMeta("02wMz5Txv2enqVTitaig6F")
	_, _ = idx.Rebuild() // new meta → delta → fires
	if fired != 2 {
		t.Fatalf("rebuild after new content should fire, got %d", fired)
	}
}

func fuzzScenarioUpdateMetaReordersAndPreservesStateDir(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "project-test-0123456789")
	for _, id := range []string{"02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv2enqVTitaig6F"} {
		m := schema.SessionMeta{ID: id, Name: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
		if err := schema.SaveSessionMeta(proj, m); err != nil {
			t.Fatal(err)
		}
	}
	idx := NewPastIndexWithDB(filepath.Join(dir, "*"), filepath.Join(dir, "index.db"))
	_, _ = idx.Rebuild()
	before, _ := idx.Find("02wMz5Txv1C3Hut0M8GCeB")
	renamed := before.Meta
	renamed.Name = "renamed-title"
	renamed.UpdatedAt = time.Unix(1_700_100_000, 0) // newer → sorts first
	idx.UpdateMeta("02wMz5Txv1C3Hut0M8GCeB", renamed)

	got, ok := idx.Find("02wMz5Txv1C3Hut0M8GCeB")
	if !ok || got.Meta.Name != "renamed-title" {
		t.Fatalf("UpdateMeta did not update the entry: %+v", got)
	}
	if got.StateDir != before.StateDir {
		t.Fatalf("StateDir must be preserved: %q != %q", got.StateDir, before.StateDir)
	}
	if all := idx.All(); all[0].ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("renamed newer entry should sort first, got %q", all[0].ID)
	}
	// FTS search must find the new title.
	if hits := idx.Search("renamed-title", 10, 0); len(hits) == 0 || hits[0].ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("search must reflect the new title, got %+v", hits)
	}
}

// TestUpdateMetaConcurrentWithRebuildIsRaceFree pins the fix for a latent
// data race: Rebuild publishes i.all and then, AFTER releasing the lock,
// keeps reading that same slice's backing array for its slow
// rebuildFTS/contentFingerprint work. UpdateMeta used to mutate i.all's
// backing array in place (re-slicing append for removal, append+copy for
// insert), so a concurrent UpdateMeta could write into the very array an
// in-flight, unlocked Rebuild was still reading. This test has no
// assertions of its own — under `go test -race` the race detector is the
// failure mode.
func fuzzScenarioUpdateMetaConcurrentWithRebuildIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "project-test-0123456789")
	for _, id := range []string{"02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv2enqVTitaig6F"} {
		m := schema.SessionMeta{ID: id, Name: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
		if err := schema.SaveSessionMeta(proj, m); err != nil {
			t.Fatal(err)
		}
	}
	idx := NewPastIndexWithDB(filepath.Join(dir, "*"), filepath.Join(dir, "index.db"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// Keep enough overlap for the race detector without multiplying full disk
	// rescans inside every fuzz input; the fuzz engine supplies repeated schedules.
	const iterations = 30
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iterations {
			_, _ = idx.Rebuild()
		}
	}()
	go func() {
		defer wg.Done()
		for n := range iterations {
			meta := schema.SessionMeta{
				ID:        "02wMz5Txv1C3Hut0M8GCeB",
				Name:      "renamed-title",
				UpdatedAt: time.Unix(1_700_000_000+int64(n), 0),
				EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/w"},
			}
			idx.UpdateMeta("02wMz5Txv1C3Hut0M8GCeB", meta)
		}
	}()
	wg.Wait()
}

func fuzzScenarioPastIndex_RecentModels_DedupesGlobalRecencyLastN(t *testing.T) {
	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", ProfileID: "anthropic", Model: "claude-opus-4-6", UpdatedAt: now.Add(-1 * time.Minute)},
		{ID: "02wMz5Txv2enqVTitaig6F", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "02wMz5Txv47YP64RR3B9YJ", ProfileID: "anthropic", Model: "claude-opus-4-6", UpdatedAt: now.Add(-3 * time.Minute)}, // dup of a, older — dropped
		{ID: "02wMz5Txv5aIxgf9yVdd0N", ProfileID: "openai", Model: "gpt-5-mini", UpdatedAt: now.Add(-4 * time.Minute)},
		{ID: "02wMz5Txv733WHFsVy66SR", ProfileID: "google", Model: "gemini-3-pro", UpdatedAt: now.Add(-5 * time.Minute)},
		{ID: "02wMz5Txv8Vo4rqb3QYZuV", ProfileID: "zai", Model: "glm-5.2", UpdatedAt: now.Add(-6 * time.Minute)},
		{ID: "02wMz5Txv9yYdSRJat13MZ", ProfileID: "mistral", Model: "mistral-large", UpdatedAt: now.Add(-7 * time.Minute)}, // 6th distinct — excluded by limit=5
	})
	got := idx.RecentModels(5)
	want := []appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
		{Provider: "openai", Model: "gpt-5.2"},
		{Provider: "openai", Model: "gpt-5-mini"},
		{Provider: "google", Model: "gemini-3-pro"},
		{Provider: "zai", Model: "glm-5.2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentModels(5) = %+v, want %+v", got, want)
	}
}

// fuzzScenarioPastIndex_RecentModels_SkipsSubagentSessions pins the machinery
// filter: a delegate session's (provider, model) pair never surfaces in
// RecentModels, and a delegate-only pair must not consume one of the limit
// slots — mirroring RecentProjectDirs' IsSubagent skip. A delegate's model is
// inherited or overridden at spawn, never chosen in the picker, so it is
// recents noise, not recents signal.
func fuzzScenarioPastIndex_RecentModels_SkipsSubagentSessions(t *testing.T) {
	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv3Kz7RbQ9pXwLd", ProfileID: "lunaroute", Model: "glm-5.3-flash", IsSubagent: true, UpdatedAt: now.Add(-1 * time.Minute)},
		{ID: "02wMz5Txv4Nq2WsE8vYuMa", ProfileID: "zai", Model: "glm-5.3", UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "02wMz5Txv5Tc6XgR1mZbPf", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: now.Add(-3 * time.Minute)},
	})
	// limit 2: without the skip the delegate pair takes slot 1 and pushes the
	// oldest genuine picker choice out of the group.
	got := idx.RecentModels(2)
	want := []appwire.ModelDescriptor{
		{Provider: "zai", Model: "glm-5.3"},
		{Provider: "openai", Model: "gpt-5.2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentModels(2) = %+v, want %+v (delegate pair skipped without consuming a slot)", got, want)
	}
}

// TestPastIndex_RefreshOneRereadsChangedMetaAndReorders is the regression test
// for the sidebar-ordering-freshness bug: a session's on-disk meta.json can be
// rewritten out-of-process (the daemon's own maybeAutoSave) between the
// index's 60s Rebuild ticks. RefreshOne re-reads just that one session's meta
// and feeds it through the existing UpdateMeta reorder path, without waiting
// for the next full Rebuild.
func fuzzScenarioPastIndex_RefreshOneRereadsChangedMetaAndReorders(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "project-test-0123456789")
	base := time.Unix(1_700_000_000, 0)
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: base, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv2enqVTitaig6F", Name: "02wMz5Txv2enqVTitaig6F", UpdatedAt: base.Add(time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndex(filepath.Join(dir, "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if all := idx.All(); len(all) != 2 || all[0].ID != "02wMz5Txv2enqVTitaig6F" {
		t.Fatalf("expected 01B first before refresh, got %+v", all)
	}

	// Out-of-process rewrite: 01A's UpdatedAt jumps ahead of 01B, exactly like a
	// daemon's maybeAutoSave touching its own meta.json.
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: base.Add(2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx.RefreshOne("02wMz5Txv1C3Hut0M8GCeB")

	all := idx.All()
	if len(all) != 2 || all[0].ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("expected 01A to sort first after RefreshOne, got %+v", all)
	}
	if !all[0].Meta.UpdatedAt.Equal(base.Add(2 * time.Minute)) {
		t.Fatalf("expected RefreshOne to pick up the new UpdatedAt, got %v", all[0].Meta.UpdatedAt)
	}
}

// TestPastIndex_RefreshOneOnChangeFires pins that RefreshOne's UpdateMeta call
// still fires the content-delta onChange hook (the version-bump path the tree
// memo depends on), same as a direct UpdateMeta call.
func fuzzScenarioPastIndex_RefreshOneOnChangeFires(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "project-test-0123456789")
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndex(filepath.Join(dir, "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	fired := 0
	idx.SetOnChange(func() { fired++ })

	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Unix(1_700_000_100, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	idx.RefreshOne("02wMz5Txv1C3Hut0M8GCeB")
	if fired != 1 {
		t.Fatalf("expected RefreshOne to fire onChange once for a genuine delta, got %d", fired)
	}
}

// TestPastIndex_RefreshOneNoOpsOnUntrackedID mirrors UpdateMeta's own
// untracked-ID no-op: an id RefreshOne has never indexed (e.g. a stale roster
// entry racing a rename) must not panic or mutate the index.
func fuzzScenarioPastIndex_RefreshOneNoOpsOnUntrackedID(t *testing.T) {
	idx := NewPastIndex(filepath.Join(t.TempDir(), "*"))
	idx.RefreshOne("nonexistent")
	if all := idx.All(); len(all) != 0 {
		t.Fatalf("expected no-op on untracked id, got %+v", all)
	}
}

// TestPastIndex_RefreshOneNoOpsOnMissingMetaFile covers the on-disk-renamed
// case: the id is indexed but its .meta.json is gone (renamed session dir,
// race with cleanup). LoadSessionMeta fails; RefreshOne must log and return,
// never panic or corrupt the existing entry.
func fuzzScenarioPastIndex_RefreshOneNoOpsOnMissingMetaFile(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "project-test-0123456789")
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndex(filepath.Join(dir, "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	before, _ := idx.Find("02wMz5Txv1C3Hut0M8GCeB")

	// Remove the underlying meta file out from under the index.
	if err := os.Remove(filepath.Join(proj, "sessions", "02wMz5Txv1C3Hut0M8GCeB.meta.json")); err != nil {
		t.Fatal(err)
	}

	idx.RefreshOne("02wMz5Txv1C3Hut0M8GCeB") // must not panic

	after, ok := idx.Find("02wMz5Txv1C3Hut0M8GCeB")
	if !ok {
		t.Fatal("expected the stale entry to remain indexed after a failed RefreshOne")
	}
	if !after.Meta.UpdatedAt.Equal(before.Meta.UpdatedAt) {
		t.Fatalf("expected entry to be unchanged after a failed RefreshOne: before=%v after=%v", before.Meta.UpdatedAt, after.Meta.UpdatedAt)
	}
}

func fuzzScenarioPastIndex_RecentModels_EmptyIndexReturnsNil(t *testing.T) {
	idx := NewPastIndex("")
	if got := idx.RecentModels(5); got != nil {
		t.Fatalf("RecentModels on empty index = %+v, want nil", got)
	}
}

func fuzzScenarioPastIndex_RecentModels_SkipsBlankProviderOrModel(t *testing.T) {
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", ProfileID: "", Model: "gpt-5.2", UpdatedAt: time.Now()},
		{ID: "02wMz5Txv2enqVTitaig6F", ProfileID: "openai", Model: "", UpdatedAt: time.Now()},
		{ID: "02wMz5Txv47YP64RR3B9YJ", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: time.Now()},
	})
	got := idx.RecentModels(5)
	want := []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentModels = %+v, want %+v (blank provider/model entries skipped)", got, want)
	}
}

type ftsMirrorRow struct {
	name string
	rank int
}

// ftsMirrorRows reads every row the FTS mirror holds directly from SQLite, so a
// test can see which rows a publish actually rewrote (by their stored
// sort_rank) rather than only what Search returns.
func ftsMirrorRows(t *testing.T, dbPath string) map[string]ftsMirrorRow {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT id, name, sort_rank FROM past_sessions_fts`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ftsMirrorRow{}
	for rows.Next() {
		var id, name string
		var rank int
		if err := rows.Scan(&id, &name, &rank); err != nil {
			t.Fatal(err)
		}
		out[id] = ftsMirrorRow{name: name, rank: rank}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertRanksUnchanged fails if any row carried over from before to after moved
// its stored sort_rank. wrote names the ids the operation was allowed to write
// (their rank may move); every other carried-over row must be untouched. A
// renumbered row is the observable symptom of a whole-table rewrite, which
// reassigns sort_rank by position.
func assertRanksUnchanged(t *testing.T, op string, before, after map[string]ftsMirrorRow, wrote ...string) {
	t.Helper()
	rewritten := make(map[string]bool, len(wrote))
	for _, id := range wrote {
		rewritten[id] = true
	}
	for id, prev := range before {
		if rewritten[id] {
			continue
		}
		got, ok := after[id]
		if !ok {
			t.Fatalf("%s dropped unchanged row %s from the mirror", op, id)
		}
		if got.rank != prev.rank {
			t.Fatalf("%s renumbered unchanged row %s (rank %d -> %d); the whole table was rewritten", op, id, prev.rank, got.rank)
		}
	}
}

// fuzzScenarioPastIndex_IncrementalPublishLeavesUnchangedRows pins the
// incremental FTS publish: a single-session fold or rename must write only the
// row it changes, not renumber (and therefore rewrite) every mirrored row. The
// whole-table DELETE-all + re-INSERT this replaces made every publish O(index)
// — ~275-437ms at a 13.5k-entry index — for a fold that inserts one session.
func fuzzScenarioPastIndex_IncrementalPublishLeavesUnchangedRows(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "index.db")
	base := time.Unix(1_700_000_000, 0)
	const alpha = "02wMz5Txv1C3Hut0M8GCeB"
	const bravo = "02wMz5Txv2enqVTitaig6F"
	const charlie = "02wMz5Txv47YP64RR3B9YJ"
	meta := func(id, name string, updated time.Time, prompt string) schema.SessionMeta {
		return schema.SessionMeta{ID: id, Name: name, UpdatedAt: updated, OriginalPrompt: prompt,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	}
	writeMeta(t, proj, meta(alpha, "alpha", base, "first needle"))
	writeMeta(t, proj, meta(bravo, "bravo", base.Add(time.Minute), "second needle"))

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	seed := ftsMirrorRows(t, dbPath)
	if len(seed) != 2 {
		t.Fatalf("mirror holds %d rows after Rebuild, want 2", len(seed))
	}

	// A probe fold of a session persisted after the index was built. It sorts to
	// the front (newest first), the case a tail-renumbering upsert could not
	// make cheaper; the incremental publish instead leaves the existing rows
	// exactly as they were.
	writeMeta(t, proj, meta(charlie, "charlie", base.Add(2*time.Minute), "third needle"))
	if _, ok := idx.Find(charlie); !ok {
		t.Fatal("expected Find to fold the newly persisted session")
	}
	afterFold := ftsMirrorRows(t, dbPath)
	assertRanksUnchanged(t, "fold", seed, afterFold, charlie)
	if got, ok := afterFold[charlie]; !ok || got.name != "charlie" {
		t.Fatalf("fold did not mirror the new row: %+v (ok=%v)", got, ok)
	}

	// A rename through UpdateMeta that moves the row to the front: only the
	// renamed row may be rewritten.
	idx.UpdateMeta(alpha, meta(alpha, "zulu", base.Add(3*time.Minute), "first needle"))
	afterRename := ftsMirrorRows(t, dbPath)
	assertRanksUnchanged(t, "rename", afterFold, afterRename, alpha)
	if got := afterRename[alpha]; got.name != "zulu" {
		t.Fatalf("rename did not update the mirrored row: %+v", got)
	}

	// The mirror still serves both the folded and the renamed session through
	// the FTS-only path.
	if got, ok := idx.searchFTS("charlie"); !ok || !slices.ContainsFunc(got, func(e PastEntry) bool { return e.ID == charlie }) {
		t.Fatalf("searchFTS did not serve the folded session (ok=%v, %d results)", ok, len(got))
	}
	if got, ok := idx.searchFTS("zulu"); !ok || !slices.ContainsFunc(got, func(e PastEntry) bool { return e.ID == alpha }) {
		t.Fatalf("searchFTS did not serve the renamed session (ok=%v, %d results)", ok, len(got))
	}
}

// fuzzScenarioPastIndex_IncrementalPublishRemovesRows covers the delta's
// removal branch: a session whose meta disappears between Rebuilds must be
// DELETEd from past_sessions_fts while every surviving row keeps its stored
// sort_rank. The leak is invisible to Search (searchFTS filters ids through
// i.byID), so only a direct read of the mirror catches it — and a leak would
// grow the FTS table without bound, eroding the per-publish cost this change
// bounds.
func fuzzScenarioPastIndex_IncrementalPublishRemovesRows(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "index.db")
	base := time.Unix(1_700_000_000, 0)
	const alpha = "02wMz5Txv1C3Hut0M8GCeB"
	const bravo = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{ID: alpha, Name: "alpha", UpdatedAt: base, OriginalPrompt: "keep", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	writeMeta(t, proj, schema.SessionMeta{ID: bravo, Name: "bravo", UpdatedAt: base.Add(time.Minute), OriginalPrompt: "drop", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	before := ftsMirrorRows(t, dbPath)
	if _, ok := before[bravo]; !ok {
		t.Fatal("mirror is missing the session about to be removed; test setup is wrong")
	}

	// The session's meta file disappears (session cleanup), then Rebuild drops
	// it from the snapshot; the delta must delete the mirrored row.
	if err := os.Remove(filepath.Join(proj, "sessions", bravo+".meta.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	after := ftsMirrorRows(t, dbPath)
	if _, leaked := after[bravo]; leaked {
		t.Fatal("removed session's row leaked in the FTS mirror; the delta's removal branch did not delete it")
	}
	if got, ok := after[alpha]; !ok || got.rank != before[alpha].rank {
		t.Fatalf("surviving row was rewritten across a removal: %+v (was rank %d)", got, before[alpha].rank)
	}
	if got, ok := idx.searchFTS("drop"); ok && slices.ContainsFunc(got, func(e PastEntry) bool { return e.ID == bravo }) {
		t.Fatal("searchFTS still served the removed session")
	}
}

// fuzzScenarioPastIndex_IncrementalPublishRecoversFromLostDB pins the delta's
// baseline guard: if the SQLite index file is deleted out from under the index,
// the tracked `published` snapshot no longer matches the (recreated, empty)
// table, so the delta must refuse and fall back to a full rebuild rather than
// insert only the changed row and mark a truncated mirror healthy — the
// self-healing the old whole-table rewrite provided.
func fuzzScenarioPastIndex_IncrementalPublishRecoversFromLostDB(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "index.db")
	base := time.Unix(1_700_000_000, 0)
	const alpha = "02wMz5Txv1C3Hut0M8GCeB"
	const bravo = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{ID: alpha, Name: "alpha", UpdatedAt: base, OriginalPrompt: "alpha needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	writeMeta(t, proj, schema.SessionMeta{ID: bravo, Name: "bravo", UpdatedAt: base.Add(time.Minute), OriginalPrompt: "bravo needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// Lose the DB file and its sidecars outside the index's lock.
	for _, p := range []string{dbPath, dbPath + "-journal", dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	// A rename must not leave a truncated mirror marked healthy: the guard sees
	// the empty table and rebuilds every row, unchanged ones included.
	idx.UpdateMeta(alpha, schema.SessionMeta{ID: alpha, Name: "alpha2", UpdatedAt: base.Add(2 * time.Minute), OriginalPrompt: "alpha needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	rows := ftsMirrorRows(t, dbPath)
	if len(rows) != 2 {
		t.Fatalf("mirror holds %d rows after a lost DB + rename, want 2 (baseline guard did not rebuild)", len(rows))
	}
	if _, ok := rows[bravo]; !ok {
		t.Fatal("unchanged row was permanently dropped after the DB file was lost")
	}
	if got := rows[alpha]; got.name != "alpha2" {
		t.Fatalf("renamed row not mirrored: %+v", got)
	}
}

// fuzzScenarioPastIndex_SupersededPublishAbandonsStaleRebuild pins the publish
// generation guard (roborev Medium: "FTS publish ordering races on lock
// acquisition, not snapshot freshness"). ftsMu only orders writes; without a
// freshness check a Rebuild whose scan predates a concurrent fold can take the
// lock after the fold published and rewrite the mirror back to its older
// snapshot, marking it healthy so Search never repairs it. The
// pastBeforePublishFTS seam parks the Rebuild at the top of publishFTS so the
// fold deterministically publishes first.
func fuzzScenarioPastIndex_SupersededPublishAbandonsStaleRebuild(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "index.db")
	base := time.Unix(1_700_000_000, 0)
	const seededID = "02wMz5Txv1C3Hut0M8GCeB"
	const foldedID = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{ID: seededID, Name: "seeded", UpdatedAt: base, OriginalPrompt: "seeded needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	reached := make(chan struct{})
	release := make(chan struct{})
	var parked atomic.Bool
	prev := pastBeforePublishFTS
	pastBeforePublishFTS = func() {
		if parked.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
	}
	defer func() { pastBeforePublishFTS = prev }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = idx.Rebuild()
	}()
	<-reached

	// The fold lands while the Rebuild's stale (1-session) snapshot is parked
	// before its write; it publishes the 2-session snapshot first.
	writeMeta(t, proj, schema.SessionMeta{ID: foldedID, Name: "folded", UpdatedAt: base.Add(time.Minute), OriginalPrompt: "folded needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	if _, ok := idx.Find(foldedID); !ok {
		t.Fatal("expected Find to fold the newly persisted session")
	}

	close(release)
	<-done

	// The superseded Rebuild must have abandoned its write, leaving the fold's
	// snapshot in the mirror rather than rewriting it back to 1 stale row.
	rows := ftsMirrorRows(t, dbPath)
	if _, ok := rows[foldedID]; !ok {
		t.Fatal("superseded Rebuild wiped the folded row from the FTS mirror")
	}
	if _, ok := rows[seededID]; !ok {
		t.Fatal("superseded Rebuild wiped the seeded row from the FTS mirror")
	}
	if len(rows) != 2 {
		t.Fatalf("mirror holds %d rows after the race, want 2", len(rows))
	}
	if got, ok := idx.searchFTS("folded"); !ok || !slices.ContainsFunc(got, func(e PastEntry) bool { return e.ID == foldedID }) {
		t.Fatal("searchFTS did not serve the folded session after the superseded Rebuild")
	}
}

// fuzzScenarioPastIndex_DeltaRejectsSameCardinalityForeignDB pins the second
// baseline check: a replaced index.db whose row count matches the tracked
// snapshot but whose rows are foreign must be detected and repaired with a full
// rebuild, not accepted because the count lines up. Without it the delta skips
// the "unchanged" ids, leaves the foreign text in a mirror marked healthy, and
// searchFTS can match content the session's real name/prompt does not have.
func fuzzScenarioPastIndex_DeltaRejectsSameCardinalityForeignDB(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "index.db")
	base := time.Unix(1_700_000_000, 0)
	const alpha = "02wMz5Txv1C3Hut0M8GCeB"
	const bravo = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{ID: alpha, Name: "alpha", UpdatedAt: base, OriginalPrompt: "alpha needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	writeMeta(t, proj, schema.SessionMeta{ID: bravo, Name: "bravo", UpdatedAt: base.Add(time.Minute), OriginalPrompt: "bravo needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// Replace the index with a same-cardinality foreign snapshot: 2 rows, wrong
	// ids/content, and a baseline state row from an earlier write of this index
	// (same owner, older seq) — exactly a restored same-size backup, whose count
	// and old token would both be accepted by a count-only check.
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM past_sessions_fts`); err != nil {
		t.Fatal(err)
	}
	stmt, err := db.Prepare(insertPastSessionsFTS)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, name, prompt string }{
		{"foreign000000000000000A", "foreign-one", "foreign text one"},
		{"foreign000000000000000B", "foreign-two", "foreign text two"},
	} {
		if _, err := stmt.Exec(row.id, row.name, row.prompt, "/foreign", "/foreign", 0); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if _, err := db.Exec(`DELETE FROM past_sessions_fts_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO past_sessions_fts_state(owner, seq) VALUES (?, ?)`, idx.ftsOwner, idx.lastSeq-1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A rename triggers a delta, which must notice the foreign baseline and
	// rebuild the whole mirror from the real index.
	idx.UpdateMeta(alpha, schema.SessionMeta{ID: alpha, Name: "alpha2", UpdatedAt: base.Add(2 * time.Minute), OriginalPrompt: "alpha needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	rows := ftsMirrorRows(t, dbPath)
	if len(rows) != 2 {
		t.Fatalf("mirror holds %d rows after the foreign-DB replacement, want 2 (baseline token not enforced)", len(rows))
	}
	if _, leaked := rows["foreign000000000000000A"]; leaked {
		t.Fatal("foreign row survived the delta; the same-cardinality replacement was not detected")
	}
	if _, leaked := rows["foreign000000000000000B"]; leaked {
		t.Fatal("foreign row survived the delta; the same-cardinality replacement was not detected")
	}
	if _, ok := rows[bravo]; !ok {
		t.Fatal("untouched real row was dropped when repairing the foreign DB")
	}
	if got := rows[alpha]; got.name != "alpha2" {
		t.Fatalf("renamed row not mirrored after repair: %+v", got)
	}
}

// fuzzScenarioPastIndex_RebuildRescanKeepsConcurrentFold pins Rebuild's
// scan-generation capture (roborev Medium: "Rebuild can overwrite a newer
// snapshot with stale disk state"). Rebuild scans unlocked; a fold landing
// during the scan must not be dropped when Rebuild swaps its older view. The
// pastBeforeRebuildSwap seam parks Rebuild after its scan and before the swap
// so the fold deterministically lands first.
func fuzzScenarioPastIndex_RebuildRescanKeepsConcurrentFold(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "index.db")
	base := time.Unix(1_700_000_000, 0)
	const seededID = "02wMz5Txv1C3Hut0M8GCeB"
	const foldedID = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{ID: seededID, Name: "seeded", UpdatedAt: base, OriginalPrompt: "seeded needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	reached := make(chan struct{})
	release := make(chan struct{})
	var parked atomic.Bool
	prev := pastBeforeRebuildSwap
	pastBeforeRebuildSwap = func() {
		if parked.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
	}
	defer func() { pastBeforeRebuildSwap = prev }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = idx.Rebuild()
	}()
	<-reached

	// The fold lands while the parked Rebuild holds a 1-session scan view.
	writeMeta(t, proj, schema.SessionMeta{ID: foldedID, Name: "folded", UpdatedAt: base.Add(time.Minute), OriginalPrompt: "folded needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})
	if _, ok := idx.Find(foldedID); !ok {
		t.Fatal("expected Find to fold the newly persisted session")
	}

	close(release)
	<-done

	if _, ok := idx.findCached(foldedID); !ok {
		t.Fatal("stale Rebuild scan dropped the folded session from the index")
	}
	if all := idx.All(); len(all) != 2 {
		t.Fatalf("index holds %d entries after the concurrent Rebuild, want 2 (fold dropped)", len(all))
	}
	if rows := ftsMirrorRows(t, dbPath); len(rows) != 2 {
		t.Fatalf("mirror holds %d rows after the concurrent Rebuild, want 2", len(rows))
	}
}

// fuzzScenarioPastIndex_FTSWriteUsesImmediateTransaction pins the fix for the
// deferred-transaction finding: the FTS writer reads its baseline before its
// first write, and index.db is shared with the archive/favorite/pin stores, so
// a deferred begin can hit SQLITE_BUSY_SNAPSHOT on the write upgrade when a
// sibling store commits in that window. writeFTSTx must open with
// _txlock=immediate so it takes the write lock before reading.
func fuzzScenarioPastIndex_FTSWriteUsesImmediateTransaction(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "alpha", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	orig := idx.openDB
	var dsns []string
	idx.openDB = func(driver, dsn string) (*sql.DB, error) {
		dsns = append(dsns, dsn)
		return orig(driver, dsn)
	}
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if len(dsns) == 0 {
		t.Fatal("Rebuild opened no FTS write connection")
	}
	for _, dsn := range dsns {
		if !strings.Contains(dsn, "_txlock=immediate") {
			t.Fatalf("FTS write opened a deferred transaction (%q); a concurrent sibling-store commit can fail the write upgrade with SQLITE_BUSY_SNAPSHOT", dsn)
		}
	}
}

// fuzzScenarioPastIndex_SupersededRebuildDoesNotReportSkips pins that a
// discarded Rebuild scan leaves the skip baseline alone. reportSkips must run
// only for the scan that is actually swapped in; otherwise a superseded
// attempt moves i.skipped and emits a "[hub] past index: skipped ..." line
// derived from a disk view that was never indexed.
func fuzzScenarioPastIndex_SupersededRebuildDoesNotReportSkips(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "alpha", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// A directory whose name fails the project-id validator becomes a skip.
	bad := filepath.Join(root, "projects", "bad-name")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}

	// Supersede every attempt's scan by bumping the generation from the seam, so
	// Rebuild never swaps a scan in.
	prev := pastBeforeRebuildSwap
	pastBeforeRebuildSwap = func() {
		idx.mu.Lock()
		idx.gen++
		idx.mu.Unlock()
	}
	defer func() { pastBeforeRebuildSwap = prev }()

	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	idx.mu.RLock()
	_, reported := idx.skipped[bad]
	idx.mu.RUnlock()
	if reported {
		t.Fatal("a discarded Rebuild scan reported its skip; skip diagnostics must come only from a swapped scan")
	}
}
