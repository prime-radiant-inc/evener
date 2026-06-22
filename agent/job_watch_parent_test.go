package agent

import (
	"context"
	"strings"
	"testing"

	tooldefs "primeradiant.com/serf/agent/internal/tool"
)

func TestJobWatchParentSourceRequiresGrant(t *testing.T) {
	parent := newTestSession(t)
	childCfg := parent.cfg
	childCfg.spawn.parentSessionID = parent.ID()
	childCfg.spawn.depth = parent.depth + 1
	child, err := NewSession(parent.client, parent.currentProfile(), parent.env, childCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	_, err = jobWatchTool(child, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatal("jobWatchTool succeeded, want source_not_watchable")
	}
	if !strings.Contains(err.Error(), "watch_parent") {
		t.Fatalf("error = %v, want watch_parent guidance", err)
	}
}

func TestJobWatchParentSourceInstallsOnParentWithChildReceiver(t *testing.T) {
	parent := newTestSession(t)
	sub, delegateID := createParentWatchChild(t, parent, "observe parent")

	out, err := jobWatchTool(sub.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool: %v", err)
	}
	state := out.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Source != "parent" || !state.Watching {
		t.Fatalf("watch state = %+v, want source parent watching", state)
	}

	cfg := onlyWatchConfigForTest(t, parent.jobManager)
	if cfg.receiverSessionID != sub.sess.ID() {
		t.Fatalf("receiverSessionID = %q, want child %q", cfg.receiverSessionID, sub.sess.ID())
	}
	if cfg.receiverDelegateID != delegateID {
		t.Fatalf("receiverDelegateID = %q, want delegate %q", cfg.receiverDelegateID, delegateID)
	}
}

func TestJobWatchParentSourceIsDistinctPerChildReceiver(t *testing.T) {
	parent := newTestSession(t)
	first, firstDelegateID := createParentWatchChild(t, parent, "first observer")
	second, secondDelegateID := createParentWatchChild(t, parent, "second observer")

	firstOut, err := jobWatchTool(first.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("first jobWatchTool: %v", err)
	}
	firstState := firstOut.(tooldefs.StateResult).State.(jobWatchToolResult)

	secondOut, err := jobWatchTool(second.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("second jobWatchTool: %v", err)
	}
	secondState := secondOut.(tooldefs.StateResult).State.(jobWatchToolResult)

	if firstState.WatchID == secondState.WatchID {
		t.Fatalf("watch ids collided: first=%q second=%q", firstState.WatchID, secondState.WatchID)
	}
	wantReceivers := map[string]string{
		first.sess.ID():  firstDelegateID,
		second.sess.ID(): secondDelegateID,
	}
	assertParentWatchReceivers(t, parent, wantReceivers)

	againOut, err := jobWatchTool(first.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("first reinstall jobWatchTool: %v", err)
	}
	againState := againOut.(tooldefs.StateResult).State.(jobWatchToolResult)
	if againState.WatchID != firstState.WatchID {
		t.Fatalf("same receiver reinstall watch_id = %q, want original %q", againState.WatchID, firstState.WatchID)
	}
	assertParentWatchReceivers(t, parent, wantReceivers)
}

func TestJobWatchParentSourceReceiverScopedClearLeavesOtherReceivers(t *testing.T) {
	parent := newTestSession(t)
	first, firstDelegateID := createParentWatchChild(t, parent, "first observer")
	second, secondDelegateID := createParentWatchChild(t, parent, "second observer")

	if _, err := jobWatchTool(first.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar); err != nil {
		t.Fatalf("first jobWatchTool: %v", err)
	}
	if _, err := jobWatchTool(second.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar); err != nil {
		t.Fatalf("second jobWatchTool: %v", err)
	}
	assertParentWatchReceivers(t, parent, map[string]string{
		first.sess.ID():  firstDelegateID,
		second.sess.ID(): secondDelegateID,
	})

	if _, err := parent.jobManager.configureWatch(watchArgs{
		Target:             runtimeMessageAliasCaller,
		ReceiverSessionID:  first.sess.ID(),
		ReceiverDelegateID: firstDelegateID,
		Clear:              true,
	}); err != nil {
		t.Fatalf("receiver-scoped clear: %v", err)
	}
	assertParentWatchReceivers(t, parent, map[string]string{
		second.sess.ID(): secondDelegateID,
	})
}

func createParentWatchChild(t *testing.T, parent *Session, task string) (*subagent, string) {
	t.Helper()
	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:           task,
		WatchParent:    true,
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	delegates, err := parent.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	delegate := delegates[res.DelegateID]
	if delegate == nil || delegate.ChildSessionID == "" {
		t.Fatalf("delegate record for %s = %+v, want child session id", res.DelegateID, delegate)
	}
	sub := parent.subagents.get(delegate.ChildSessionID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing child session for %s", delegate.ChildSessionID)
	}
	return sub, res.DelegateID
}

func assertParentWatchReceivers(t *testing.T, parent *Session, want map[string]string) {
	t.Helper()
	parent.jobManager.mu.Lock()
	defer parent.jobManager.mu.Unlock()
	if len(parent.jobManager.watches) != len(want) {
		t.Fatalf("parent watch count = %d, want %d", len(parent.jobManager.watches), len(want))
	}
	for _, cfg := range parent.jobManager.watches {
		wantDelegateID, ok := want[cfg.receiverSessionID]
		if !ok {
			t.Fatalf("unexpected receiver session %q in cfg %+v", cfg.receiverSessionID, cfg)
		}
		if cfg.receiverDelegateID != wantDelegateID {
			t.Fatalf("receiverDelegateID for %q = %q, want %q", cfg.receiverSessionID, cfg.receiverDelegateID, wantDelegateID)
		}
	}
}
