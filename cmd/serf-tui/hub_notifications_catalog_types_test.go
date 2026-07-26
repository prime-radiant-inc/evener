package main

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

// TestApplyHubNotification_DecodesEveryCatalogedNotification is a
// characterization test for kata vbp3: applyHubNotification and
// reconcilePendingFromNotification used to decode several notifications
// (turn/started, item/started, item/completed, serf/job/started,
// serf/steering/injected, turn/completed) into hand-rolled anonymous structs
// even though appwire's catalog already names their shape. This test drives
// each through the real dispatcher with realistic JSON and asserts the
// resulting model/pending-coordinator state, so a field-name slip made while
// swapping in the named appwire.*Params type (the exact failure mode vbp3
// exists to catch — turn/started's real "turnId" bug from kata qrj4 was
// exactly this kind of drift) fails here instead of silently decoding a zero
// value.
func TestApplyHubNotification_DecodesEveryCatalogedNotification(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail.Ref = "local:01SESSION"

	send := func(method string, params string) {
		m.applyHubNotification(appwire.Notification{Method: method, Params: json.RawMessage(params)})
	}

	// turn/started: TurnStartedParams.Turn.
	send(appwire.NotifyTurnStarted, `{"turn":{"id":"turn_1","status":"inProgress"}}`)
	if m.detail.ActiveTurnID != "turn_1" {
		t.Fatalf("ActiveTurnID = %q, want turn_1", m.detail.ActiveTurnID)
	}

	// item/started: ItemLifecycleParams.Item. Also exercises
	// reconcilePendingFromNotification's item/started case (a userMessage
	// item reconciles a MethodTurnStart placeholder registered with the same
	// text).
	handle := m.pending.Register(appwire.MethodTurnStart, "hi", "")
	send(appwire.NotifyItemStarted, `{"item":{"id":"i1","type":"userMessage","text":"hi"}}`)
	found := false
	for _, msg := range m.session.messages {
		if msg.ItemID == "i1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("session.messages = %+v, want an entry with ItemID i1", m.session.messages)
	}
	if m.pending.TryReconcile(appwire.MethodTurnStart, "hi", "") {
		t.Fatal("item/started with a matching userMessage should have already reconciled the pending turn/start placeholder")
	}
	_ = handle

	// item/completed: ItemLifecycleParams.Item (same struct as item/started).
	send(appwire.NotifyItemCompleted, `{"item":{"id":"i1","type":"userMessage","text":"hi","status":"completed"}}`)

	// serf/job/started: SerfJobParams.Job.
	send(appwire.NotifySerfJobStarted, `{"job":{"jobId":"j1","jobType":"delegate","status":"running"}}`)
	sawJob := false
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Subagent != nil && msg.Tool.Subagent.JobID == "j1" {
			sawJob = true
		}
	}
	if !sawJob {
		t.Fatalf("session.messages = %+v, want a subagent row for job j1", m.session.messages)
	}

	// serf/steering/injected: SerfSteeringInjectedParams.Text. Also exercises
	// reconcilePendingFromNotification's steering case (MethodTurnSteer).
	m.pending.Register(appwire.MethodTurnSteer, "steered", "")
	send(appwire.NotifySerfSteeringInjected, `{"text":"steered"}`)
	sawSteering := false
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgSteering && msg.Text == "steered" {
			sawSteering = true
		}
	}
	if !sawSteering {
		t.Fatalf("session.messages = %+v, want a MsgSteering entry with text %q", m.session.messages, "steered")
	}
	if m.pending.TryReconcile(appwire.MethodTurnSteer, "steered", "") {
		t.Fatal("serf/steering/injected should have already reconciled the pending turn/steer placeholder")
	}

	// turn/completed: TurnCompletedParams.Turn, clears ActiveTurnID. Also
	// exercises reconcilePendingFromNotification's turn/completed case (a
	// userMessage item nested in the turn reconciles a MethodTurnStart
	// placeholder).
	m.detail.ActiveTurnID = "turn_1"
	m.pending.Register(appwire.MethodTurnStart, "bye", "")
	send(appwire.NotifyTurnCompleted, `{"turn":{"id":"turn_1","status":"completed","items":[{"id":"i2","type":"userMessage","text":"bye"}]}}`)
	if m.detail.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID = %q after its turn completed, want empty", m.detail.ActiveTurnID)
	}
	if m.pending.TryReconcile(appwire.MethodTurnStart, "bye", "") {
		t.Fatal("turn/completed with a matching userMessage item should have already reconciled the pending turn/start placeholder")
	}
}

// TestHandleChildActivityFrame_DecodesItemLifecycleParams is a
// characterization test for the handleChildActivityFrame conversion (kata
// vbp3): it routes a watched subagent child's item/started frame to the
// child's rail row via ItemLifecycleParams.Item instead of a hand-rolled
// anonymous struct.
func TestHandleChildActivityFrame_DecodesItemLifecycleParams(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail.Ref = "local:01PARENT"
	m.watchedChildRefs = map[string]bool{"local:01CHILD": true}
	m.session.messages = []transcript.ChatMessage{{
		Kind: transcript.MsgTool,
		Tool: &transcript.ToolCallInfo{Subagent: &transcript.SubagentRunInfo{TranscriptRef: "local:01CHILD", Status: "running"}},
	}}

	m.applyHubNotification(appwire.Notification{
		Method: appwire.NotifyItemStarted,
		Params: json.RawMessage(`{"ref":"local:01CHILD","item":{"toolName":"shell","description":"building"}}`),
	})

	run := m.session.messages[0].Tool.Subagent
	if run.Activity != "shell: building" {
		t.Fatalf("Subagent.Activity = %q, want %q", run.Activity, "shell: building")
	}
	if run.Steps != 1 {
		t.Fatalf("Subagent.Steps = %d, want 1", run.Steps)
	}
}
