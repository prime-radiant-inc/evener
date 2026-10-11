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
