package agent

// Issue #5012: a nested delegate's OwnerSessionID names the physical root that
// admitted its runtime, while the session that ran its create is
// VisibleSessionID. The live and historical jobs-activity rosters must
// attribute the row to the creator, as job_list/job_status already do, not to
// the root.

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func delegateCreatedBySession(delegateID, parentID, ownerSessionID, visibleSessionID, childSessionID string) delegatestore.Event {
	return delegatestore.Event{
		Kind:       delegatestore.EventDelegateCreated,
		DelegateID: delegateID,
		Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:   childSessionID,
			TranscriptRef:    encodeRef("", childSessionID),
			ParentDelegateID: parentID,
			OwnerSessionID:   ownerSessionID,
			VisibleSessionID: visibleSessionID,
			Task:             "creator/owner distinction",
			AgentType:        "general",
			ToolNameCeiling:  []string{"communicate"},
			Resumable:        true,
		}},
	}
}

func TestLiveActivityBaseAttributesNestedDelegateToCreator(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	controller, _ := newDelegateControllerTestHarness(t, 10, 10)
	controller.rootRuntime = s
	s.delegateController = controller
	rootID := s.ID()

	controller.mu.Lock()
	_, err := controller.appendLocked(
		delegateCreatedBySession("dlg_middle", "", rootID, rootID, "child-middle"),
		delegateCreatedBySession("dlg_leaf", "dlg_middle", rootID, "child-middle", "child-leaf"),
	)
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("seed delegates: %v", err)
	}

	base, err := loadLiveActivityBase(s)
	if err != nil {
		t.Fatalf("loadLiveActivityBase: %v", err)
	}
	if _, ok := base.snapshot.Delegates["dlg_middle"]; !ok {
		t.Fatalf("root roster = %v, want the delegate it created", delegateIDs(base.snapshot.Delegates))
	}
	if _, ok := base.snapshot.Delegates["dlg_leaf"]; ok {
		t.Fatalf("root roster = %v, want the nested delegate attributed to its creator, not the physical owner",
			delegateIDs(base.snapshot.Delegates))
	}
}

func TestHistoricalDelegateRosterAttributesNestedDelegateToCreator(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := "creatorroot"
	middle := pastStableDescriptor(rootID, "childmiddle", "middle")
	leaf := pastStableDescriptor(rootID, "childleaf", "leaf")
	// The leaf hangs off the middle delegate and was created by the middle
	// delegate's session; only the physical owner is the root.
	leaf.ParentDelegateID = "dlg_middle"
	leaf.VisibleSessionID = "childmiddle"
	writePastDelegates(t, stateDir, rootID, middle, leaf)

	cache := newHistoricalActivityCache(context.Background(), "")
	rootRows, _, _, err := loadHistoricalStableActivity(cache, stateDir, rootID, rootID)
	if err != nil {
		t.Fatalf("loadHistoricalStableActivity(root): %v", err)
	}
	if _, ok := rootRows["dlg_middle"]; !ok {
		t.Fatalf("root roster = %v, want the delegate it created", delegateIDs(rootRows))
	}
	if _, ok := rootRows["dlg_leaf"]; ok {
		t.Fatalf("root roster = %v, want the nested delegate attributed to its creator", delegateIDs(rootRows))
	}

	creatorRows, _, _, err := loadHistoricalStableActivity(cache, stateDir, rootID, "childmiddle")
	if err != nil {
		t.Fatalf("loadHistoricalStableActivity(creator): %v", err)
	}
	if _, ok := creatorRows["dlg_leaf"]; !ok {
		t.Fatalf("creator roster = %v, want the delegate it created", delegateIDs(creatorRows))
	}
	if len(creatorRows) != 1 {
		t.Fatalf("creator roster = %v, want exactly its own child", delegateIDs(creatorRows))
	}
}

func delegateIDs(rows map[string]delegateSnapshot) []string {
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	return ids
}
