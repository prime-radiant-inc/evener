package main

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/appwire"
)

// notifyMethods is the set of wire notification methods applyHubNotification
// dispatches on. Indexing into it from the fuzzer reaches every json.Unmarshal
// branch.
var notifyMethods = []string{
	appwire.NotifyThreadStatusChanged,
	appwire.NotifyTurnStarted,
	appwire.NotifyTurnCompleted,
	appwire.NotifyItemStarted,
	appwire.NotifyItemCompleted,
	appwire.NotifyAgentMessageDelta,
	appwire.NotifyAgentMessageReset,
	appwire.NotifyReasoningSummaryDelta,
	appwire.NotifyToolOutputDelta,
	appwire.NotifyThreadQueueChanged,
	appwire.NotifySerfJobStarted,
	appwire.NotifySerfJobFinished,
	appwire.NotifySerfSteeringInjected,
	appwire.NotifyWarning,
	appwire.NotifySerfAuthUpdated,
	appwire.NotifySerfLaunchUpdated,
}

// FuzzApplyHubNotification drives the serf-tui hub's real notification-decode
// dispatcher. applyHubNotification switches on the wire method and json.Unmarshals
// the untrusted notification.Params into per-method param structs, then folds the
// result through the session transcript reducer. The model is built via the
// production newHubModel constructor (client=nil so no network/RPC fires) and put
// into session mode. Oracle: no-panic floor — a malformed or hostile params
// payload from the daemon must never crash the TUI.
func FuzzApplyHubNotification(f *testing.F) {
	seeds := []struct {
		method int
		params string
	}{
		{0, `{"status":{"type":"active"}}`},
		{1, `{"turn":{"id":"turn_1"}}`},
		{2, `{"turn":{"id":"turn_1","items":[{"type":"agentMessage","text":"hi"}]}}`},
		{3, `{"item":{"type":"commandExecution","tool_name":"delegate","raw":{"job_id":"j"}}}`},
		{4, `{"item":{"type":"reasoning"}}`},
		{5, `{"turnId":"turn_1","itemId":"i","delta":"abc"}`},
		{8, `{"itemId":"i","delta":"out"}`},
		{10, `{"job":{"job_id":"j","type":"delegate","status":"running"}}`},
		{12, `{"text":"steered"}`},
		{13, `{"message":"warn","source":"provider"}`},
		{14, `{}`},
		{0, `not json`},
		{2, `{"turn":null}`},
		{3, `{"item":{}}`},
	}
	for _, s := range seeds {
		f.Add(s.method, s.params)
	}

	f.Fuzz(func(t *testing.T, methodIdx int, params string) {
		if methodIdx < 0 {
			methodIdx = -methodIdx
		}
		method := notifyMethods[methodIdx%len(notifyMethods)]

		m := newHubModel(nil, "http://hub.test")
		m.mode = hubModeSession
		m.detail.Ref = "local:01SESSION"

		n := appwire.Notification{Method: method, Params: json.RawMessage(params)}
		// The dispatcher must never panic on any params payload; the returned
		// tea.Cmd is discarded (client is nil, so no command actually runs).
		_ = m.applyHubNotification(n)
	})
}
