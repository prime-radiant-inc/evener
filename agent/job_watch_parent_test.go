package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/llm"
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

func TestJobWatchParentSourcePublicClearRoutesToParent(t *testing.T) {
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

	clearOut, err := jobWatchTool(first.sess, map[string]any{
		"operation": "clear",
		"watch_id":  firstState.WatchID,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("public clear: %v", err)
	}
	clearState := clearOut.(tooldefs.StateResult).State.(jobWatchToolResult)
	if clearState.WatchID != firstState.WatchID || clearState.Watching {
		t.Fatalf("clear state = %+v, want watch %q cleared", clearState, firstState.WatchID)
	}
	assertParentWatchReceivers(t, parent, map[string]string{
		second.sess.ID(): secondDelegateID,
	})
}

func TestParentSourceWatchFrameDeliveredToChildWatcher(t *testing.T) {
	parent := newTestSession(t)
	sub, delegateID := createParentWatchChild(t, parent, "observe parent")

	out, err := jobWatchTool(sub.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
		"events":    []any{"assistant.tool"},
		"event_filter": map[string]any{
			"tool_name": "read_file",
			"status":    "ok",
		},
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool: %v", err)
	}
	state := out.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Send != nil {
		t.Fatalf("public watch result exposed internal send: %+v", state.Send)
	}

	parent.emit(events.EventToolCallEnd, events.ToolCallEndData{
		ToolName: "job_list",
		Output:   "{}",
	})
	parent.emit(events.EventToolCallEnd, events.ToolCallEndData{
		ToolName: "read_file",
		Error:    "permission denied",
	})
	if sends := parent.jobManager.pendingWatchSendDeliveries(nil); len(sends) != 0 {
		t.Fatalf("non-matching events created pending sends: %+v", sends)
	}

	parent.emit(events.EventToolCallEnd, events.ToolCallEndData{
		ToolName:      "read_file",
		CallID:        "call_read_file",
		ArgumentsJSON: `{"file_path":"notes.txt"}`,
		Output:        "ok",
	})

	sends := parent.jobManager.pendingWatchSendDeliveries(nil)
	if len(sends) != 1 {
		t.Fatalf("pending sends = %d, want 1", len(sends))
	}
	if sends[0].state.Key.ResolvedSendTo != delegateID {
		t.Fatalf("delivery target = %q, want delegate %q", sends[0].state.Key.ResolvedSendTo, delegateID)
	}
	if !strings.Contains(sends[0].state.Frame, "read_file") ||
		!strings.Contains(sends[0].state.Frame, "notes.txt") ||
		!strings.Contains(sends[0].state.Frame, "status: ok") {
		t.Fatalf("frame = %q, want matching read_file event content", sends[0].state.Frame)
	}
}

func TestRestoredParentSourcePendingSendPreservesReceiverRouting(t *testing.T) {
	const (
		parentSessionID    = "PARENT"
		receiverSessionID  = "child_observer"
		receiverDelegateID = "dlg_observer"
	)
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, parentSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}

	res, err := jm.configureWatch(watchArgs{
		Source:             "parent",
		Target:             runtimeMessageAliasCaller,
		ReceiverSessionID:  receiverSessionID,
		ReceiverDelegateID: receiverDelegateID,
		Events:             []string{"assistant.tool"},
	})
	if err != nil {
		t.Fatalf("configureWatch: %v", err)
	}
	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: parentSessionID,
		Data: events.ToolCallEndData{
			ToolName:      "read_file",
			CallID:        "call_read_file",
			ArgumentsJSON: `{"file_path":"notes.txt"}`,
			Output:        "ok",
		},
	})

	record, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("LoadWatchSends: %v", err)
	}
	if len(record.Pending) != 1 {
		t.Fatalf("pending sends = %d, want 1", len(record.Pending))
	}
	for _, state := range record.Pending {
		if state.ReceiverSessionID != receiverSessionID || state.ReceiverDelegateID != receiverDelegateID {
			t.Fatalf("pending receiver = %q/%q, want %q/%q", state.ReceiverSessionID, state.ReceiverDelegateID, receiverSessionID, receiverDelegateID)
		}
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close original store: %v", err)
	}

	restored, err := newJobManager(stateDir, parentSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("restore jobManager: %v", err)
	}
	t.Cleanup(func() { _ = restored.store.Close() })

	sends := restored.pendingWatchSendDeliveries(nil)
	if len(sends) != 1 {
		t.Fatalf("restored pending sends = %d, want 1", len(sends))
	}
	cfg := sends[0].cfg
	if cfg.receiverSessionID != receiverSessionID || cfg.receiverDelegateID != receiverDelegateID {
		t.Fatalf("restored cfg receiver = %q/%q, want %q/%q", cfg.receiverSessionID, cfg.receiverDelegateID, receiverSessionID, receiverDelegateID)
	}

	list := restored.watchListToolResultForReceiver(receiverSessionID, receiverDelegateID)
	if len(list.Watches) != 1 {
		t.Fatalf("restored receiver watch list length = %d, want 1", len(list.Watches))
	}
	if list.Watches[0].SendTo != "" {
		t.Fatalf("restored public list send_to = %q, want hidden internal send", list.Watches[0].SendTo)
	}
	inspect, ok := restored.inspectReceiverWatchByID(res.WatchID, receiverSessionID, receiverDelegateID)
	if !ok {
		t.Fatalf("restored receiver inspect %q not found", res.WatchID)
	}
	if inspect.SendTo != "" {
		t.Fatalf("restored public inspect send_to = %q, want hidden internal send", inspect.SendTo)
	}

	if _, err := restored.clearReceiverWatchByID(res.WatchID, receiverSessionID, receiverDelegateID); err != nil {
		t.Fatalf("clearReceiverWatchByID: %v", err)
	}
	if sends := restored.pendingWatchSendDeliveries(nil); len(sends) != 0 {
		t.Fatalf("pending sends after receiver clear = %d, want 0", len(sends))
	}
}

func TestWatchOriginCommunicateEndTurnResumesParentOnce(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("observer ready") },
		func(llm.Request) llm.Response {
			return communicateWithDefaultOutput("WATCH_OBSERVED read_file succeeded")
		},
	}})
	parent := newDelegateTestSession(t, c)
	sub, _ := createParentWatchChild(t, parent, "observe parent")
	child := sub.sess

	var steered []steeringMessage
	child.cfg.spawn.parentSteerDelivered = func(msg string, p *provenance.Causal) bool {
		steered = append(steered, steeringMessage{
			Text:       msg,
			Provenance: provenance.Clone(p),
		})
		return true
	}

	parentRun, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "watch callback owner", sub)
	if err != nil {
		t.Fatalf("attach delegate job: %v", err)
	}
	parentRun.fromWatch.Store(true)
	child.cfg.spawn.parentJobID = parentRun.rec.JobID
	child.cfg.spawn.parentMarkCallerCallbackDelivered = parent.jobManager.markWatchOriginCallerCallbackDelivered

	watchProvenance := provenance.WithWatch(nil, "watch_parent", "wg_1", "wd_1", parent.ID(), runtimeMessageAliasCaller)
	_, err = child.processInputKindWithProvenance(context.Background(), "Watch delivery frame", nil, EntryWatchDelivery, watchProvenance)
	if err != nil {
		t.Fatalf("watch delivery process: %v", err)
	}

	if len(steered) != 1 {
		t.Fatalf("parent steers = %d, want one callback", len(steered))
	}
	if !strings.Contains(steered[0].Text, "WATCH_OBSERVED") {
		t.Fatalf("callback = %q, want WATCH_OBSERVED", steered[0].Text)
	}
	if !provenance.ContainsWatch(steered[0].Provenance, "watch_parent", "wg_1") {
		t.Fatalf("callback provenance = %+v, want watch provenance", steered[0].Provenance)
	}

	if err := parent.finalizeDelegate(parentRun.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalize delegate: %v", err)
	}
	if got := parent.peekNotifications(); got != 0 {
		t.Fatalf("pending owner notifications = %d, want duplicate suppressed", got)
	}
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
