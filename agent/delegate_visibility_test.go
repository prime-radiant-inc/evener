package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
)

// A delegate created by a delegate belongs to its creator's own roster and to
// its ancestors' descendant trees. Creation records OwnerSessionID as the
// physical tree root and ParentDelegateID as the creating delegate, so row
// visibility must key on the descriptor's VisibleSessionID (the session that
// ran the create), never on the root that admitted the runtime.
func TestDelegateVisibility_NestedCreatorAndAncestorSeeCreatedDelegate(t *testing.T) {
	t.Parallel()
	tree := newRealDelegateTree(t, nil)

	// The creator's own roster lists the delegate it made, with no descendant
	// flag: it is a direct child of that delegate.
	creatorRows := jobListState(t, tree.parentRuntime, map[string]any{"type": []any{"delegate"}})
	if got := delegateVisibilityRowIDs(creatorRows); !slices.Equal(got, []string{tree.grandchildID}) {
		t.Errorf("creator job_list(delegate) rows = %v, want its own delegate %s", got, tree.grandchildID)
	}
	if _, err := delegateStatusTool(tree.parentRuntime, tree.grandchildID, 1<<20); err != nil {
		t.Errorf("creator job_status for its own delegate: %v", err)
	}

	// The ancestor's descendant tree reaches the delegate through its parent.
	ancestorRows := jobListState(t, tree.s, map[string]any{"type": []any{"delegate"}, "include_nested": true})
	wantAncestor := delegateVisibilitySortedIDs(tree.parent.DelegateID, tree.grandchildID)
	if got := delegateVisibilityRowIDs(ancestorRows); !slices.Equal(got, wantAncestor) {
		t.Errorf("ancestor job_list(include_nested, delegate) rows = %v, want %v", got, wantAncestor)
	}
	for _, row := range ancestorRows.Items {
		wantDepth := 0
		if row.ID == tree.grandchildID {
			wantDepth = 1
		}
		if row.Depth != wantDepth {
			t.Errorf("ancestor row %s depth = %d, want %d", row.ID, row.Depth, wantDepth)
		}
	}
	if _, err := delegateStatusTool(tree.s, tree.grandchildID, 1<<20); err != nil {
		t.Errorf("ancestor job_status for a descendant delegate: %v", err)
	}

	// The ancestor's plain roster stays its direct delegates only.
	baseRows := jobListState(t, tree.s, map[string]any{"type": []any{"delegate"}})
	wantBase := delegateVisibilitySortedIDs(tree.parent.DelegateID)
	if got := delegateVisibilityRowIDs(baseRows); !slices.Equal(got, wantBase) {
		t.Errorf("ancestor job_list(delegate) base rows = %v, want %v", got, wantBase)
	}

	// The same visibility rule is what lets a watch wait on a nested child:
	// the creator must resolve its own child session as a resumable delegate.
	if !tree.parentRuntime.childResumable(tree.grandchildSessionID) {
		t.Errorf("creator does not resolve its own delegate child session %s as resumable", tree.grandchildSessionID)
	}
}

// Cold restore rebuilds the controller from the durable journal. The creator
// rows stay visible to the root's descendant tree without any live runtime.
func TestDelegateVisibility_NestedRowsSurviveRootColdRestore(t *testing.T) {
	t.Parallel()
	tree := newRealDelegateTree(t, nil)
	if err := schema.SaveSessionMeta(tree.s.stateDir, tree.s.Meta()); err != nil {
		t.Fatalf("save root session meta: %v", err)
	}
	rootID := tree.s.ID()
	tree.s.Close()

	restored := restoreQueuePersistTestSession(t, tree.s.stateDir, rootID)
	t.Cleanup(restored.Close)

	rows := jobListState(t, restored, map[string]any{"type": []any{"delegate"}, "include_nested": true})
	want := delegateVisibilitySortedIDs(tree.parent.DelegateID, tree.grandchildID)
	if got := delegateVisibilityRowIDs(rows); !slices.Equal(got, want) {
		t.Errorf("restored root job_list(include_nested, delegate) rows = %v, want %v", got, want)
	}
	if _, err := delegateStatusTool(restored, tree.grandchildID, 1<<20); err != nil {
		t.Errorf("restored root job_status for a descendant delegate: %v", err)
	}
}

// Ownership edges: a row under a delegate's own slot belongs to that delegate
// even when a non-delegate subagent of it ran the create (ParentDelegateID is
// the logical owner), and the physical root keeps root-slot rows a subagent of
// it created (OwnerSessionID names the root).
func TestDelegateVisibility_CreatorSlotOwnershipIsTheLogicalOwner(t *testing.T) {
	t.Parallel()
	tree := newRealDelegateTree(t, nil)
	rootID := tree.s.ID()

	subagentChild := "dlg_visibility_subagent_child"
	seedVisibilityDelegate(t, tree.c, subagentChild, tree.parent.DelegateID, rootID, "subagent-session")
	creatorRows := jobListState(t, tree.parentRuntime, map[string]any{"type": []any{"delegate"}})
	if !slices.Contains(delegateVisibilityRowIDs(creatorRows), subagentChild) {
		t.Errorf("creator job_list(delegate) rows = %v, want the row created under its slot (%s)", delegateVisibilityRowIDs(creatorRows), subagentChild)
	}

	rootChild := "dlg_visibility_root_subagent_child"
	seedVisibilityDelegate(t, tree.c, rootChild, "", rootID, "subagent-session")
	rootRows := jobListState(t, tree.s, map[string]any{"type": []any{"delegate"}})
	if !slices.Contains(delegateVisibilityRowIDs(rootRows), rootChild) {
		t.Errorf("root job_list(delegate) rows = %v, want the root-slot row its subagent created (%s)", delegateVisibilityRowIDs(rootRows), rootChild)
	}
	if _, err := delegateStatusTool(tree.s, rootChild, 1<<20); err != nil {
		t.Errorf("root job_status for a root-slot row its subagent created: %v", err)
	}
}

func seedVisibilityDelegate(t *testing.T, c *delegateTreeController, id, parentID, ownerID, visibleSessionID string) {
	t.Helper()
	descriptor := delegatestore.Descriptor{
		ChildSessionID:   "child-" + id,
		TranscriptRef:    "local:child-" + id,
		ParentDelegateID: parentID,
		OwnerSessionID:   ownerID,
		VisibleSessionID: visibleSessionID,
		Task:             "visibility ownership edge",
		AgentType:        "general",
		ToolNameCeiling:  []string{"communicate"},
		ResolvedModel:    "gpt-5.2",
		Resumable:        true,
	}
	c.mu.Lock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateCreated,
		DelegateID: id,
		Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed visibility delegate %s: %v", id, err)
	}
}

func delegateVisibilityRowIDs(rows jobListResult) []string {
	ids := make([]string, 0, len(rows.Items))
	for _, row := range rows.Items {
		ids = append(ids, row.ID)
	}
	slices.Sort(ids)
	return ids
}

func delegateVisibilitySortedIDs(ids ...string) []string {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	return sorted
}
