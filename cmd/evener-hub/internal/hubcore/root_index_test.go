package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// forkOriginalWithTwoChildren is a fork original that two non-subagent
// sessions name as their parent. The tree nests it under its continuation
// however many children it has (nestedSessionIDs), so every top-level
// question must answer the same way.
func forkOriginalWithTwoChildren() []schema.SessionMeta {
	now := time.Now()
	return []schema.SessionMeta{
		{ID: "01ORIG", ForkLabel: "edited", UpdatedAt: now},
		{ID: "01KIDA", ParentSessionID: "01ORIG", DivergenceTurn: 1, UpdatedAt: now},
		{ID: "01KIDB", ParentSessionID: "01ORIG", DivergenceTurn: 1, UpdatedAt: now},
	}
}

func TestRootIndexAgreesWithTreeOnForkOriginalWithSeveralChildren(t *testing.T) {
	metas := forkOriginalWithTwoChildren()
	roots := NewRootIndex(metas)
	topLevel := TopLevelSessionIDs(metas)
	for id, want := range map[string]bool{"01ORIG": false, "01KIDA": true, "01KIDB": true} {
		if _, got := topLevel[id]; got != want {
			t.Errorf("TopLevelSessionIDs has %s = %v, want %v", id, got, want)
		}
		if got := roots.TopLevel(id); got != want {
			t.Errorf("TopLevel(%s) = %v, want %v", id, got, want)
		}
	}
	if roots.TopLevel("01ORIG") {
		t.Error("a fork original with two children must be nested, not top-level")
	}

	live := []LiveEntry{
		{PID: 1, SessionID: "01ORIG", Status: appwire.ThreadStatusAwaiting},
		{PID: 2, SessionID: "01KIDB", Status: appwire.ThreadStatusAwaiting},
	}
	attention, _ := DeriveAttention(metas, live, nil)
	if _, ok := attention["01ORIG"]; ok {
		t.Error("attention must skip the nested fork original")
	}
	if _, ok := attention["01KIDB"]; !ok {
		t.Error("attention must keep the continuation")
	}
	tree := BuildTree(metas, live, nil)
	for _, n := range tree.NeedsYou {
		if n.ID == "01ORIG" {
			t.Error("the tree's needs-you tier must skip the nested fork original")
		}
	}
}

// A live subagent whose meta never reached the navigation inputs (dropped, or
// not folded yet) has no persisted marker. Its parent's roster entry still
// reports it as an in-process child, and that alone keeps it off the
// needs-you list.
func TestGuardsHoldWhenSubagentMetaIsDropped(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{{ID: "01ROOT", UpdatedAt: now}}
	live := []LiveEntry{
		{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive, RunningSubagentIDs: []string{"01CHILD"}},
		{PID: 2, SessionID: "01CHILD", Status: appwire.ThreadStatusAwaiting},
	}

	attention, sum := DeriveAttention(metas, live, nil)
	if _, ok := attention["01CHILD"]; ok || sum.NeedsYou != 0 {
		t.Fatalf("meta-less subagent reached attention: %v %+v", attention, sum)
	}
	for _, n := range BuildTree(metas, live, nil).NeedsYou {
		if n.ID == "01CHILD" {
			t.Fatal("meta-less subagent reached the needs-you tier")
		}
	}

	// Roster before meta: once the meta folds in, the answer is unchanged.
	withMeta := append(metas, schema.SessionMeta{ID: "01CHILD", IsSubagent: true, ParentSessionID: "01ROOT", UpdatedAt: now})
	attention, sum = DeriveAttention(withMeta, live, nil)
	if _, ok := attention["01CHILD"]; ok || sum.NeedsYou != 0 {
		t.Fatalf("subagent with meta reached attention: %v %+v", attention, sum)
	}
}

// A crash-retained record keeps the child list its dead daemon reported; those
// children are not in-process anywhere, so they are not subagents by that
// entry's say-so (tree.go skips them the same way).
func TestCrashedRosterEntryDoesNotMarkItsChildrenAsSubagents(t *testing.T) {
	live := []LiveEntry{
		{PID: 1, SessionID: "01ROOT", Crashed: true, RunningSubagentIDs: []string{"01KEPT"}},
		{PID: 2, SessionID: "01KEPT", Status: appwire.ThreadStatusAwaiting},
	}
	attention, _ := DeriveAttention(nil, live, nil)
	if _, ok := attention["01KEPT"]; !ok {
		t.Fatalf("a crashed entry's stale child list excluded a session: %v", attention)
	}
}

func TestLookupDoesNotProbeDiskOrMutateTheIndex(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	writeMeta(t, proj, schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})

	genBefore := func() uint64 { _, g := idx.snapshot(); return g }
	before := genBefore()
	if _, ok := idx.Lookup(id); ok {
		t.Fatal("Lookup found an id that is on disk but not indexed; it must not probe")
	}
	if after := genBefore(); after != before || len(idx.All()) != 0 {
		t.Fatal("Lookup mutated the index")
	}
	// The row is still there for Find to probe.
	if _, ok := idx.Find(id); !ok {
		t.Fatal("Find should still probe the row Lookup left alone")
	}
	if got, ok := idx.Lookup(id); !ok || got.ID != id {
		t.Fatalf("Lookup after Find = %+v, %v", got, ok)
	}
}

func TestPastIndexRootIndexIsCachedPerGeneration(t *testing.T) {
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{{ID: "01A"}})
	first := idx.RootIndex()
	if idx.RootIndex() != first {
		t.Fatal("RootIndex rebuilt without an index change")
	}
	idx.SeedForTest([]schema.SessionMeta{{ID: "01A"}, {ID: "01SUB", IsSubagent: true, ParentSessionID: "01A"}})
	next := idx.RootIndex()
	if next == first || !next.IsSubagent("01SUB") {
		t.Fatal("RootIndex did not follow the index change")
	}
}
