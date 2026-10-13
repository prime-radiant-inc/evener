package hubcore

import "primeradiant.com/evener/agent/schema"

// RootIndex answers "is this session a top-level root?" from session lineage
// alone, without project resolution or a navigation snapshot. The tree, the
// attention summary and the pin resolver all ask it, so they cannot disagree
// about which sessions are roots. Immutable once built.
type RootIndex struct {
	subagents map[string]struct{} // metas with IsSubagent
	// nested and forkChildren are nestedSessionIDs' results: the rule for
	// which sessions render under another row is stated once, there.
	nested       map[string]struct{}
	forkChildren map[string]string
}

// NewRootIndex indexes metas in one pass.
func NewRootIndex(metas []schema.SessionMeta) *RootIndex {
	subagents := make(map[string]struct{})
	for _, m := range metas {
		if m.IsSubagent {
			subagents[m.ID] = struct{}{}
		}
	}
	nested, forkChildren := nestedSessionIDs(metas)
	return &RootIndex{subagents: subagents, nested: nested, forkChildren: forkChildren}
}

// IsSubagent reports whether a persisted meta marks id as a subagent.
func (r *RootIndex) IsSubagent(id string) bool {
	_, ok := r.subagents[id]
	return ok
}

// IsNested reports whether id renders under another row: a subagent with a
// parent, or a fork original superseded by a continuation.
func (r *RootIndex) IsNested(id string) bool {
	_, ok := r.nested[id]
	return ok
}

// TopLevel reports whether the metas make id an independently addressable
// row. An id the index has never seen is not excluded by it; callers that
// need "known" ask the past index.
func (r *RootIndex) TopLevel(id string) bool {
	return id != "" && !r.IsSubagent(id) && !r.IsNested(id)
}

// RunningSubagentIDs returns the sessions live entries report as their
// in-process children. It is the subagent marker for a live session whose meta
// is missing or has not been folded in yet. A crash-retained record keeps the
// child list its daemon reported before dying but runs nothing, so its
// children are not in-process anywhere and are skipped.
func RunningSubagentIDs(live []LiveEntry) map[string]bool {
	running := make(map[string]bool)
	for _, le := range live {
		if le.Crashed {
			continue
		}
		for _, childID := range le.RunningSubagentIDs {
			if childID != "" {
				running[childID] = true
			}
		}
	}
	return running
}

// RunningSubagentParents names, for each session live entries report as an
// in-process child, the live session that lists it — the only parent link a
// child has before its meta reaches the past index. The tree's rollup walk
// builds the same map fused with its other live indexes; search resolves
// grouped subagents' anchors through it too, so both views pass through a
// live delegate with no meta to the session it runs under. A crash-retained
// record runs nothing, so its children are skipped as in RunningSubagentIDs.
func RunningSubagentParents(live []LiveEntry) map[string]string {
	parents := make(map[string]string)
	for _, le := range live {
		if le.Crashed {
			continue
		}
		for _, childID := range le.RunningSubagentIDs {
			if childID != "" {
				parents[childID] = le.SessionID
			}
		}
	}
	return parents
}

// TopLevelWalk is RootIndex.WalkToTopLevel's answer: the subagent ancestry a
// session renders under. Chain runs from the walked id up through its
// subagent ancestors; Ancestor is the first non-subagent parent the chain
// reaches, before fork supersession applies; Top is Ancestor carried through
// fork supersessions, the top-level row the navigation tree would show.
type TopLevelWalk struct {
	Chain    []string
	Ancestor string
	Top      string
}

// WalkToTopLevel resolves the top-level session id renders under. parentOf
// names a subagent's parent ("" when unknown — the walk stops there);
// isSubagent and known say whether an ancestor keeps the walk going and
// whether a reached ancestor is resolvable at all. ok=false when the ancestry
// does not reach a known non-subagent row (orphan, cycle, or missing metas).
//
// This is the one implementation of the walk the tree's rollup and search's
// grouping share (tree.go's workingRoots block calls it with its own
// closures), so a session can never be top-level to one and nested to the
// other. The fork-supersession tail is tree.go's own rule, kept verbatim:
// an ancestor the tree nests (a fork original superseded by its continuation)
// resolves to the continuation the tree would show.
func (r *RootIndex) WalkToTopLevel(id string, parentOf func(string) string, isSubagent func(string) bool, known func(string) bool) (TopLevelWalk, bool) {
	walk := TopLevelWalk{Chain: []string{id}}
	seen := map[string]bool{id: true}
	ancestor := parentOf(id)
	for ancestor != "" && isSubagent(ancestor) && !seen[ancestor] {
		seen[ancestor] = true
		walk.Chain = append(walk.Chain, ancestor)
		ancestor = parentOf(ancestor)
	}
	if ancestor == "" || isSubagent(ancestor) || !known(ancestor) {
		return walk, false
	}
	walk.Ancestor = ancestor
	walk.Top = ancestor
	for seenForks := map[string]bool{}; r.IsNested(walk.Top) && !seenForks[walk.Top]; {
		seenForks[walk.Top] = true
		walk.Top = r.forkChildren[walk.Top]
	}
	return walk, true
}
