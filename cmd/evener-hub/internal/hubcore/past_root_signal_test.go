package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

const (
	rootSignalRootID = "02wMz5Txv1C3Hut0M8GCeB"
	rootSignalSubID  = "02wMz5Txv1C3Hut0M8GCeC"
)

// rootSignalIndex is a disk-backed index holding one root and one subagent of
// it, with both hooks counting their fires. The counters are read after each
// step through the returned func.
func rootSignalIndex(t *testing.T) (idx *PastIndex, project string, fires func() (full, root int)) {
	t.Helper()
	dir := t.TempDir()
	project = filepath.Join(dir, "projects", "project-x-0123456789")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	writeMeta(t, project, schema.SessionMeta{ID: rootSignalRootID, Name: "root", UpdatedAt: base})
	writeMeta(t, project, schema.SessionMeta{ID: rootSignalSubID, Name: "sub", UpdatedAt: base, IsSubagent: true, ParentSessionID: rootSignalRootID})
	idx = NewPastIndex(filepath.Join(dir, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	var full, root int
	idx.SetOnChange(func() { full++ })
	idx.SetOnRootChange(func() { root++ })
	return idx, project, func() (int, int) { return full, root }
}

func TestPastRootSignal_SubagentWritesDoNotFire(t *testing.T) {
	idx, project, fires := rootSignalIndex(t)
	later := time.Unix(1_700_000_100, 0).UTC()

	// An autosave moves UpdatedAt, which also reorders the subagent ahead of the
	// root in the index; neither may reach the root signal.
	writeMeta(t, project, schema.SessionMeta{ID: rootSignalSubID, Name: "sub", UpdatedAt: later, IsSubagent: true, ParentSessionID: rootSignalRootID})
	idx.RefreshOne(rootSignalSubID)
	// A title change on the subagent likewise.
	writeMeta(t, project, schema.SessionMeta{ID: rootSignalSubID, Name: "sub renamed", UpdatedAt: later.Add(time.Second), IsSubagent: true, ParentSessionID: rootSignalRootID})
	idx.RefreshOne(rootSignalSubID)

	full, root := fires()
	if full != 2 {
		t.Fatalf("full signal fired %d times for two subagent writes, want 2", full)
	}
	if root != 0 {
		t.Fatalf("root signal fired %d times for subagent-only writes, want 0", root)
	}
}

func TestPastRootSignal_RootChangesFire(t *testing.T) {
	idx, project, fires := rootSignalIndex(t)
	later := time.Unix(1_700_000_100, 0).UTC()

	writeMeta(t, project, schema.SessionMeta{ID: rootSignalRootID, Name: "renamed", UpdatedAt: later})
	idx.RefreshOne(rootSignalRootID)
	if _, root := fires(); root != 1 {
		t.Fatalf("root signal after a root rename = %d, want 1", root)
	}
	// An autosave of the root moves UpdatedAt, which its row shows.
	writeMeta(t, project, schema.SessionMeta{ID: rootSignalRootID, Name: "renamed", UpdatedAt: later.Add(time.Minute)})
	idx.RefreshOne(rootSignalRootID)
	if _, root := fires(); root != 2 {
		t.Fatalf("root signal after a root autosave = %d, want 2", root)
	}
}

func TestPastRootSignal_SubagentAddedOrRemovedFires(t *testing.T) {
	idx, project, fires := rootSignalIndex(t)
	const otherSubID = "02wMz5Txv1C3Hut0M8GCeD"
	base := time.Unix(1_700_000_000, 0).UTC()

	writeMeta(t, project, schema.SessionMeta{ID: otherSubID, Name: "other", UpdatedAt: base, IsSubagent: true, ParentSessionID: rootSignalRootID})
	if fired, err := idx.Rebuild(); err != nil || !fired {
		t.Fatalf("Rebuild after a subagent was added = %v, %v, want a hook fire", fired, err)
	}
	if _, root := fires(); root != 1 {
		t.Fatalf("root signal after a subagent was added = %d, want 1", root)
	}

	if err := os.Remove(filepath.Join(project, "sessions", otherSubID+".meta.json")); err != nil {
		t.Fatal(err)
	}
	// Session delete decides whether to notify navigation itself from this bool.
	if fired, err := idx.Rebuild(); err != nil || !fired {
		t.Fatalf("Rebuild after a subagent was removed = %v, %v, want a hook fire", fired, err)
	}
	if _, root := fires(); root != 2 {
		t.Fatalf("root signal after a subagent was removed = %d, want 2", root)
	}
}

// A live subagent that reached the index before its meta shows as a root; the
// meta arriving flips it to a subagent, which changes the subagent set.
func TestPastRootSignal_EntryBecomingSubagentFires(t *testing.T) {
	idx, project, fires := rootSignalIndex(t)
	const lateID = "02wMz5Txv1C3Hut0M8GCeE"
	base := time.Unix(1_700_000_000, 0).UTC()
	writeMeta(t, project, schema.SessionMeta{ID: lateID, UpdatedAt: base})
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, root := fires(); root != 1 {
		t.Fatalf("root signal after a root appeared = %d, want 1", root)
	}
	writeMeta(t, project, schema.SessionMeta{ID: lateID, UpdatedAt: base.Add(time.Second), IsSubagent: true, ParentSessionID: rootSignalRootID})
	idx.RefreshOne(lateID)
	if _, root := fires(); root != 2 {
		t.Fatalf("root signal after the entry became a subagent = %d, want 2", root)
	}
}

// A publisher superseded by a newer mutation abandons its tail, so the stale
// snapshot neither fires the root signal nor regresses its fingerprint.
func TestPastRootSignal_StalePublisherDoesNotFireOrRegress(t *testing.T) {
	idx, project, fires := rootSignalIndex(t)
	staleAll, staleGen := idx.snapshot()

	writeMeta(t, project, schema.SessionMeta{ID: rootSignalRootID, Name: "renamed", UpdatedAt: time.Unix(1_700_000_100, 0).UTC()})
	idx.RefreshOne(rootSignalRootID)
	if _, root := fires(); root != 1 {
		t.Fatalf("root signal after rename = %d, want 1", root)
	}

	if idx.publishAndSignal(staleAll, staleGen) {
		t.Fatal("a superseded publisher reported a change")
	}
	current, gen := idx.snapshot()
	if idx.publishAndSignal(current, gen) {
		t.Fatal("republishing the current snapshot reported a change: the stale publisher regressed the fingerprint")
	}
	if _, root := fires(); root != 1 {
		t.Fatalf("root signal = %d after a stale publish and a no-op republish, want still 1", root)
	}
}

// The bool Rebuild and UpdateMeta return says the change reached a hook, and a
// root-only subscriber must still get true for a root change and false for a
// subagent-only write, because callers use it to decide whether to notify
// navigation themselves.
func TestPastRootSignal_ReturnCoversRootHookOnly(t *testing.T) {
	idx, _, _ := rootSignalIndex(t)
	idx.SetOnChange(nil) // a root-only subscriber, as in production
	base := time.Unix(1_700_000_000, 0).UTC()

	sub, ok := idx.findCached(rootSignalSubID)
	if !ok {
		t.Fatal("subagent not indexed")
	}
	sub.Meta.UpdatedAt = base.Add(time.Minute)
	if idx.UpdateMeta(rootSignalSubID, sub.Meta) {
		t.Fatal("subagent autosave reported a hook fire")
	}
	root, _ := idx.findCached(rootSignalRootID)
	root.Meta.Name = "renamed"
	root.Meta.UpdatedAt = base.Add(time.Minute)
	if !idx.UpdateMeta(rootSignalRootID, root.Meta) {
		t.Fatal("root rename did not report a hook fire")
	}
}

// A shown field outside contentFingerprint (Model) still fires the root hook
// and is reported as a hook fire, while the full signal stays quiet.
func TestPastRootSignal_ShownFieldOutsideContentFingerprintFires(t *testing.T) {
	idx, _, fires := rootSignalIndex(t)
	root, _ := idx.findCached(rootSignalRootID)
	root.Meta.Model = "other-model"
	if !idx.UpdateMeta(rootSignalRootID, root.Meta) {
		t.Fatal("a Model change did not report a hook fire")
	}
	if full, rootFires := fires(); full != 0 || rootFires != 1 {
		t.Fatalf("fires full=%d root=%d after a Model-only change, want 0 and 1", full, rootFires)
	}
}
