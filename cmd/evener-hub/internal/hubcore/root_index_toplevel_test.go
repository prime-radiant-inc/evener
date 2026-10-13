package hubcore

import (
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
	walk, ok := NewRootIndex(rootMetas(metas)).WalkToTopLevel("C", parentOf, isSubagent, known)
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
	walk, ok := NewRootIndex(rootMetas(metas)).WalkToTopLevel("S", parentOf, isSubagent, known)
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
	if _, ok := NewRootIndex(rootMetas(metas)).WalkToTopLevel("X", parentOf, isSubagent, known); ok {
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
	roots := NewRootIndex(rootMetas(metas))
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
	_, isSubagent, known := metaPredicates(metas)
	parentOf := func(id string) string {
		if metas[id].IsSubagent && metas[id].ParentSessionID != "" {
			return metas[id].ParentSessionID
		}
		return ""
	}
	roots := NewRootIndex(rootMetas(metas))
	if _, ok := roots.WalkToTopLevel("sub", parentOf, isSubagent, known); !ok {
		t.Fatal("walk failed with the ancestor known, want it to resolve")
	}
	notKnown := func(string) bool { return false }
	if _, ok := roots.WalkToTopLevel("sub", parentOf, isSubagent, notKnown); ok {
		t.Fatal("walk resolved with the ancestor unknown to the caller, want failure")
	}
}

// Agreement with the tree: on a corpus of a live root, a live mid-delegate
// and a live sub-delegate, the walk and BuildTree must attribute the
// sub-delegate's activity to the same root — the helper through Top, the
// tree through the project rollup its working-roots walk raises.
func TestWalkToTopLevelAgreesWithTheTreeRollup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	metas := []schema.SessionMeta{
		{ID: "A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: dir}},
		{ID: "B", IsSubagent: true, ParentSessionID: "A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: dir}},
		{ID: "C", IsSubagent: true, ParentSessionID: "B", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: dir}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "A", WorkingDir: dir, Status: appwire.ThreadStatusIdle},
		{PID: 2, SessionID: "B", WorkingDir: dir, Status: appwire.ThreadStatusActive},
		{PID: 3, SessionID: "C", WorkingDir: dir, Status: appwire.ThreadStatusActive},
	}

	metaMap := map[string]schema.SessionMeta{}
	for _, m := range metas {
		metaMap[m.ID] = m
	}
	parentOf, isSubagent, known := metaPredicates(metaMap)
	walk, ok := NewRootIndex(metas).WalkToTopLevel("C", parentOf, isSubagent, known)
	if !ok || walk.Top != "A" {
		t.Fatalf("walk=%+v ok=%t, want the live chain to resolve to A", walk, ok)
	}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.Projects) != 1 || tree.Projects[0].RollupState != "active" {
		t.Fatalf("projects=%+v, want C's activity to roll up to A's project as working", tree.Projects)
	}
	for _, row := range tree.Live {
		if row.ID == "B" || row.ID == "C" {
			t.Fatalf("subagent %q has a top-level row", row.ID)
		}
	}
}

func rootMetas(metas map[string]schema.SessionMeta) []schema.SessionMeta {
	out := make([]schema.SessionMeta, 0, len(metas))
	for _, m := range metas {
		out = append(out, m)
	}
	return out
}
