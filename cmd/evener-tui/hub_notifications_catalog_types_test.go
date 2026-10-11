package tui

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// TestApplyHubNotification_DecodesEveryCatalogedNotification is a
// characterization test for kata vbp3: applyHubNotification and
// reconcilePendingFromNotification used to decode several notifications into
// hand-rolled anonymous structs even though appwire's catalog already names
// their shape. This test drives each through the real dispatcher with
// realistic JSON and asserts the resulting model/pending-coordinator state,
// so a field-name slip made while swapping in the named appwire.*Params type
// (the exact failure mode vbp3 exists to catch — turn/started's real
// "turnId" bug from kata qrj4 was exactly this kind of drift) fails here
// instead of silently decoding a zero value. Ported to the read model's
// notifications (Task 21): the display rule for an open turn's id, a
// userMessage item and a steering item both now arrive via history/updated,
// and the turn-boundary/item-lifecycle notifications this test used to drive
// no longer exist.
func TestApplyHubNotification_DecodesEveryCatalogedNotification(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail.Ref = "local:01SESSION"

	send := func(method string, params string) {
		m.applyHubNotification(appwire.Notification{Method: method, Params: json.RawMessage(params)})
	}

	// thread/status/changed: ThreadStatusChangedParams.ActiveTurnID — the
	// read model's replacement for turn/started's ActiveTurnID display rule.
	send(appwire.NotifyThreadStatusChanged, `{"status":{"type":"active"},"activeTurnId":"turn_1"}`)
	if m.detail.ActiveTurnID != "turn_1" {
		t.Fatalf("ActiveTurnID = %q, want turn_1", m.detail.ActiveTurnID)
	}

	// history/updated: HistoryUpdatedParams.Items. Also exercises
	// reconcilePendingFromNotification's history/updated case (a userMessage
	// item reconciles a MethodTurnStart placeholder registered with the same
	// text).
	m.pending.Register(appwire.MethodTurnStart, "hi", "", "")
	send(appwire.NotifyHistoryUpdated, `{"items":[{"id":"i1","type":"userMessage","text":"hi"}]}`)
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
		t.Fatal("history/updated with a matching userMessage should have already reconciled the pending turn/start placeholder")
	}

	// evener/job/started: EvenerJobParams.Job. Job notifications are shell-only.
	send(appwire.NotifyEvenerJobStarted, `{"job":{"jobId":"j1","jobType":"shell","status":"running","background":true}}`)
	sawJob := false
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Subagent != nil && msg.Tool.Subagent.JobID == "j1" {
			sawJob = true
		}
	}
	if !sawJob {
		t.Fatalf("session.messages = %+v, want a shell row for job j1", m.session.messages)
	}

	// evener/delegate/updated: EvenerDelegateParams.Delegate.
	send(appwire.NotifyEvenerDelegateUpdated, `{"delegate":{"delegateId":"dlg1","status":"running","projectionRevision":1}}`)
	sawDelegate := false
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Subagent != nil && msg.Tool.Subagent.DelegateID == "dlg1" {
			sawDelegate = true
		}
	}
	if !sawDelegate {
		t.Fatalf("session.messages = %+v, want a stable delegate row for dlg1", m.session.messages)
	}

	// history/updated with a "steering" item: evener/steering/injected's
	// replacement. Also exercises reconcilePendingFromNotification's
	// steering case (MethodTurnSteer).
	m.pending.Register(appwire.MethodTurnSteer, "steered", "", "")
	send(appwire.NotifyHistoryUpdated, `{"items":[{"id":"i2","type":"steering","text":"steered"}]}`)
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
		t.Fatal("a recorded steering item should have already reconciled the pending turn/steer placeholder")
	}
}

// history/updated carries a recorded userMessage item; reconcilePendingFromNotification
// matches it against a pending turn/start placeholder by ClientMutationID
// (the read model's replacement for item/started|completed and
// turn/completed for this purpose), not by text, since the recorded text can
// differ from what was sent (an image placeholder).
func TestApplyHubNotification_HistoryUpdatedReconcilesPendingByMutationID(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail.Ref = "local:01SESSION"
	send := func(method, params string) {
		m.applyHubNotification(appwire.Notification{Method: method, Params: json.RawMessage(params)})
	}

	m.pending.Register(appwire.MethodTurnStart, "[image]", "", "mut-42")
	send(appwire.NotifyHistoryUpdated, `{"items":[{"id":"i1","type":"userMessage","text":"a caption the daemon rewrote","clientMutationId":"mut-42"}]}`)

	if m.pending.TryReconcileByMutationID(appwire.MethodTurnStart, "mut-42", "") {
		t.Fatal("history/updated with a matching clientMutationId should have already reconciled the pending turn/start placeholder")
	}
}

// TestHandleChildActivityFrame_DecodesHistoryUpdatedParams is a
// characterization test for the handleChildActivityFrame conversion (kata
// vbp3): it routes a watched subagent child's own history/updated frame
// (item/started|completed's read-model replacement) to the child's rail row
// via HistoryUpdatedParams.Items instead of a hand-rolled anonymous struct.
func TestHandleChildActivityFrame_DecodesHistoryUpdatedParams(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail.Ref = "local:01PARENT"
	m.watchedChildRefs = map[string]bool{"local:01CHILD": true}
	m.session.messages = []transcript.ChatMessage{{
		Kind: transcript.MsgTool,
		Tool: &transcript.ToolCallInfo{Subagent: &transcript.SubagentRunInfo{TranscriptRef: "local:01CHILD", Status: "running"}},
	}}

	m.applyHubNotification(appwire.Notification{
		Method: appwire.NotifyHistoryUpdated,
		Params: json.RawMessage(`{"ref":"local:01CHILD","items":[{"toolName":"shell","description":"building"}]}`),
	})

	run := m.session.messages[0].Tool.Subagent
	if run.Activity != "shell: building" {
		t.Fatalf("Subagent.Activity = %q, want %q", run.Activity, "shell: building")
	}
	if run.Steps != 1 {
		t.Fatalf("Subagent.Steps = %d, want 1", run.Steps)
	}
}
