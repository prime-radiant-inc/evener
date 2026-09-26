package tui

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// notifyMethods is the set of wire notification methods applyHubNotification
// dispatches on. Indexing into it from the fuzzer reaches every json.Unmarshal
// branch.
var notifyMethods = []string{
	appwire.NotifyThreadStatusChanged,
	appwire.NotifyThreadQueueChanged,
	appwire.NotifyEvenerJobStarted,
	appwire.NotifyEvenerJobFinished,
	appwire.NotifyEvenerDelegateUpdated,
	appwire.NotifyEvenerThreadModelRetry,
	appwire.NotifyWarning,
	appwire.NotifyEvenerAuthUpdated,
	appwire.NotifyEvenerLaunchUpdated,
	// These five are dispatched by applyHubNotification but were missing here,
	// so the fuzzer's "reaches every json.Unmarshal branch" was false for them.
	// Surfaced by TestEveryWireNotificationIsHandledOrExplicitlyIgnored.
	appwire.NotifyThreadModelChanged,
	appwire.NotifyThreadReasoningEffortChanged,
	appwire.NotifyThreadVisionModelChanged,
	// The session's display name (user rename, auto-namer, compaction refresh)
	// now folds onto the cached detail and the terminal title.
	appwire.NotifyThreadNameChanged,
	// Shared-notes pushes land on the cached session detail so the details
	// drawer re-renders (Task 8): real dispatch cases, not ignores.
	appwire.NotifyEvenerNotesUpdated,
	appwire.NotifyEvenerUrlsUpdated,
	appwire.NotifyEvenerMarketplaceUpdated,
	appwire.NotifyEvenerPluginUpdated,
	appwire.NotifyEvenerSandboxEscalationRequested,
	appwire.NotifyEvenerThreadResync,
	// The read model (Task 17): the TUI renders history/updated and the
	// overlay instead of turn/*/item/*.
	appwire.NotifyHistoryUpdated,
	appwire.NotifyOverlayUpserted,
	appwire.NotifyOverlayDelta,
	appwire.NotifyOverlayReset,
	appwire.NotifyOverlayEnd,
}

// FuzzApplyHubNotification drives the evener-tui hub's real notification-decode
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
		{2, `{"job":{"job_id":"j","type":"delegate","status":"running"}}`},
		{4, `{"delegate":{"delegateId":"dlg","status":"running"}}`},
		{6, `{"message":"warn","source":"provider"}`},
		{5, `{}`},
		{0, `not json`},
		{19, `{"items":[{"type":"agentMessage","id":"i1","text":"hi","version":1}],"turns":[{"id":"turn_1","status":"failed"}]}`},
		{20, `{"item":{"key":"stream:round_1/0:agentMessage","kind":"stream","item":{"type":"agentMessage","id":"stream:round_1/0:agentMessage","text":"hi"}}}`},
		{21, `{"key":"stream:round_1/0:agentMessage","field":"text","delta":"abc"}`},
		{22, `{"streamId":"round_1/0"}`},
		{23, `{"roundId":"round_1"}`},
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
