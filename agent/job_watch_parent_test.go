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
	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:           "observe parent",
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
	if cfg.receiverDelegateID != res.DelegateID {
		t.Fatalf("receiverDelegateID = %q, want delegate %q", cfg.receiverDelegateID, res.DelegateID)
	}
}
