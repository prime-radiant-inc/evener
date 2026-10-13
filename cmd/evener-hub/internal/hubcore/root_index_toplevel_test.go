package hubcore

import (
	"maps"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// metaPredicates is search's view of lineage over a plain meta map: a
// subagent's parent comes only from its own meta, a session is a subagent
// only when its meta says so, and an ancestor is known only when a meta
// exists for it.
func metaPredicates(metas map[string]schema.SessionMeta) (parentOf func(string) string, isSubagent func(string) bool, known func(string) bool) {
	parentOf = func(id string) string {
		m := metas[id]
		if m.IsSubagent && m.ParentSessionID != "" {
			return m.ParentSessionID
		}
		return ""
	}
	isSubagent = func(id string) bool {
		m, ok := metas[id]
		return ok && m.IsSubagent
	}
	known = func(id string) bool {
		_, ok := metas[id]
		return ok
	}
	return parentOf, isSubagent, known
}

// A nested delegate chain resolves to the root: C (a subagent of B, itself a
// subagent of A) renders under A, and the walk reports the chain it crossed.
func TestWalkToTopLevelResolvesNestedChainsToTheRoot(t *testing.T) {
	t.Parallel()
	metas := map[string]schema.SessionMeta{
		"A": {ID: "A"},
		"B": {ID: "B", IsSubagent: true, ParentSessionID: "A"},
		"C": {ID: "C", IsSubagent: true, ParentSessionID: "B"},
	}
	parentOf, isSubagent, known := metaPredicates(metas)
	walk, ok := NewRootIndex(slices.Collect(maps.Values(metas))).WalkToTopLevel("C", parentOf, isSubagent, known)
	if !ok {
		t.Fatal("walk failed, want the chain to reach A")
	}
	if walk.Ancestor != "A" || walk.Top != "A" {
		t.Fatalf("ancestor=%q top=%q, want A", walk.Ancestor, walk.Top)
	}
	if len(walk.Chain) != 2 || walk.Chain[0] != "C" || walk.Chain[1] != "B" {
		t.Fatalf("chain=%v, want [C B]", walk.Chain)
	}
}

// A subagent spawned under a fork original renders under the continuation
// that superseded it: the fork walk carries the answer past the original.
func TestWalkToTopLevelCarriesForkSupersession(t *testing.T) {
	t.Parallel()
	metas := map[string]schema.SessionMeta{
		"O": {ID: "O", ForkLabel: "old branch"},
		"K": {ID: "K", ParentSessionID: "O"},
		"S": {ID: "S", IsSubagent: true, ParentSessionID: "O"},
	}
	parentOf, isSubagent, known := metaPredicates(metas)
	walk, ok := NewRootIndex(slices.Collect(maps.Values(metas))).WalkToTopLevel("S", parentOf, isSubagent, known)
	if !ok {
		t.Fatal("walk failed, want the fork continuation")
	}
	if walk.Ancestor != "O" {
		t.Fatalf("ancestor=%q, want the fork original O", walk.Ancestor)
	}
	if walk.Top != "K" {
		t.Fatalf("top=%q, want the continuation K", walk.Top)
	}
}

// A parent cycle cannot loop the walk: it reports failure rather than
// resolving either side of the cycle.
func TestWalkToTopLevelStopsOnParentCycles(t *testing.T) {
	t.Parallel()
	metas := map[string]schema.SessionMeta{
		"X": {ID: "X", IsSubagent: true, ParentSessionID: "Y"},
		"Y": {ID: "Y", IsSubagent: true, ParentSessionID: "X"},
	}
	parentOf, isSubagent, known := metaPredicates(metas)
	if _, ok := NewRootIndex(slices.Collect(maps.Values(metas))).WalkToTopLevel("X", parentOf, isSubagent, known); ok {
		t.Fatal("walk resolved a parent cycle, want failure")
	}
}

// Missing ancestry does not resolve: neither a subagent whose parent has no
// meta at all, nor one whose chain breaks before a root.
func TestWalkToTopLevelReportsOrphans(t *testing.T) {
	t.Parallel()
	metas := map[string]schema.SessionMeta{
		"end":   {ID: "end", IsSubagent: true, ParentSessionID: "missing"},
		"mid":   {ID: "mid", IsSubagent: true, ParentSessionID: "gone"},
		"inner": {ID: "inner", IsSubagent: true, ParentSessionID: "mid"},
	}
	parentOf, isSubagent, known := metaPredicates(metas)
	roots := NewRootIndex(slices.Collect(maps.Values(metas)))
	for _, id := range []string{"end", "inner"} {
		if _, ok := roots.WalkToTopLevel(id, parentOf, isSubagent, known); ok {
			t.Fatalf("walk resolved %q without a known root, want failure", id)
		}
	}
}

// A known ancestor that is not known to the CALLER still fails: search treats
// a live roster-only parent as known, and this pins that the known predicate,
// not just the walk, decides resolvability.
func TestWalkToTopLevelHonorsTheKnownPredicate(t *testing.T) {
	t.Parallel()
	metas := map[string]schema.SessionMeta{
		"A":   {ID: "A"},
		"sub": {ID: "sub", IsSubagent: true, ParentSessionID: "A"},
	}
	parentOf, isSubagent, known := metaPredicates(metas)
	roots := NewRootIndex(slices.Collect(maps.Values(metas)))
	if _, ok := roots.WalkToTopLevel("sub", parentOf, isSubagent, known); !ok {
		t.Fatal("walk failed with the ancestor known, want it to resolve")
	}
	notKnown := func(string) bool { return false }
	if _, ok := roots.WalkToTopLevel("sub", parentOf, isSubagent, notKnown); ok {
		t.Fatal("walk resolved with the ancestor unknown to the caller, want failure")
	}
}

// Agreement with the tree, over the spec's whole corpus. Each shape sits in
// its own project so its rollup is observable alone: a live chain and a
// fork-superseded parent both attribute their live subagent's activity to the
// same root the helper resolves to, while a parent cycle and an orphan
// resolve nothing — the helper reports failure and the tree raises no
// working root, so a project whose only live work is unresolvable stays idle.
func TestWalkToTopLevelAgreesWithTheTreeRollup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	chainDir, forkDir, brokenDir := t.TempDir(), t.TempDir(), t.TempDir()
	metas := []schema.SessionMeta{
		// A live delegate chain: C under B under A, all live.
		{ID: "A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: chainDir}},
		{ID: "B", IsSubagent: true, ParentSessionID: "A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: chainDir}},
		{ID: "C", IsSubagent: true, ParentSessionID: "B", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: chainDir}},
		// A fork original superseded by K, with a live subagent under the
		// original: the activity belongs to K's task tree. The original is
		// live too — the tree's working-roots walk attributes a live child
		// only through an ancestor that is not ended (ended is sticky down
		// the chain), which is what makes this the fork path it exists for.
		{ID: "O", ForkLabel: "old branch", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: forkDir}},
		{ID: "K", ParentSessionID: "O", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: forkDir}},
		{ID: "S", IsSubagent: true, ParentSessionID: "O", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: forkDir}},
		// Unresolvable: a parent cycle (X and Y each the other's subagent),
		// an orphan whose parent has no meta, and an unrelated live root to
		// give the project a row at all.
		{ID: "R", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
		{ID: "X", IsSubagent: true, ParentSessionID: "Y", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
		{ID: "Y", IsSubagent: true, ParentSessionID: "X", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
		{ID: "O2", IsSubagent: true, ParentSessionID: "gone", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "A", WorkingDir: chainDir, Status: appwire.ThreadStatusIdle},
		{PID: 2, SessionID: "B", WorkingDir: chainDir, Status: appwire.ThreadStatusActive},
		{PID: 3, SessionID: "C", WorkingDir: chainDir, Status: appwire.ThreadStatusActive},
		{PID: 4, SessionID: "K", WorkingDir: forkDir, Status: appwire.ThreadStatusIdle},
		{PID: 5, SessionID: "S", WorkingDir: forkDir, Status: appwire.ThreadStatusActive},
		{PID: 6, SessionID: "O", WorkingDir: forkDir, Status: appwire.ThreadStatusIdle},
		{PID: 7, SessionID: "R", WorkingDir: brokenDir, Status: appwire.ThreadStatusIdle},
		{PID: 8, SessionID: "X", WorkingDir: brokenDir, Status: appwire.ThreadStatusActive},
		{PID: 9, SessionID: "Y", WorkingDir: brokenDir, Status: appwire.ThreadStatusActive},
		{PID: 10, SessionID: "O2", WorkingDir: brokenDir, Status: appwire.ThreadStatusActive},
	}

	metaMap := map[string]schema.SessionMeta{}
	for _, m := range metas {
		metaMap[m.ID] = m
	}
	parentOf, isSubagent, known := metaPredicates(metaMap)
	roots := NewRootIndex(metas)

	if walk, ok := roots.WalkToTopLevel("C", parentOf, isSubagent, known); !ok || walk.Top != "A" {
		t.Fatalf("chain walk=%+v ok=%t, want C to resolve to A", walk, ok)
	}
	if walk, ok := roots.WalkToTopLevel("S", parentOf, isSubagent, known); !ok || walk.Ancestor != "O" || walk.Top != "K" {
		t.Fatalf("fork walk=%+v ok=%t, want S to resolve through O to the continuation K", walk, ok)
	}
	for _, id := range []string{"X", "Y", "O2"} {
		if _, ok := roots.WalkToTopLevel(id, parentOf, isSubagent, known); ok {
			t.Fatalf("walk resolved %q, want failure for the unresolvable shapes", id)
		}
	}

	tree := BuildTreeAt(metas, live, nil, now)
	rollups := map[string]string{}
	for _, project := range tree.Projects {
		rollups[project.WorkingDir] = project.RollupState
	}
	if rollups[chainDir] != "active" || rollups[forkDir] != "active" {
		t.Fatalf("rollups=%v, want the chain's and the fork continuation's projects working", rollups)
	}
	if rollups[brokenDir] == "active" {
		t.Fatalf("rollups=%v, want the unresolvable project to stay idle: nothing working resolves to a row", rollups)
	}
	for _, row := range tree.Live {
		switch row.ID {
		case "B", "C", "S", "X", "Y", "O2":
			t.Fatalf("subagent %q has a top-level row", row.ID)
		case "A", "K", "R":
		}
	}
}
