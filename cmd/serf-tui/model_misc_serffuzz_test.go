//go:build serffuzz

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// FuzzRootTUIModelMisc covers deterministic leaf branches that are awkward to
// reach through the full Bubble Tea event loop. It intentionally uses nil
// clients: commands are inspected but never executed, so no external boundary
// is involved.
func FuzzRootTUIModelMisc(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, selector byte) {
		m := newHubModel(nil, "memory:")
		m.width, m.height = 80, 24

		// model.go
		m.session.setInputValue("")
		m.session.setInputValue(strings.Repeat("x\n", 10))
		for i := 0; i <= 1005; i++ {
			m.session.addHistory("a\nb")
		}
		var b strings.Builder
		writeWrappedList(&b, "Items:", []string{"one", "two", "three"}, 15)
		writeWrappedList(&b, "Items:", []string{"one", "two"}, 100)
		_ = renderTasks([]task.Task{{ID: 1, Status: task.TaskStatus("odd")}}, 1)
		m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{}}, {Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{}}}
		m.session.focusedToolIdx = 99
		m.session.focusTool(1)
		m.session.focusTool(-1)

		// hub_types.go and hub_status.go
		_ = (hubTranscriptViewState{Title: "title"}).banner()
		_ = gitBranchFromThread(appwire.Thread{})
		_ = gitBranchFromThread(appwire.Thread{GitInfo: &appwire.GitInfo{Branch: "main"}})
		turns := []appwire.Turn{{Error: &appwire.TurnError{Message: "a"}}, {ID: "2", Error: &appwire.TurnError{}}, {ID: "3", Error: &appwire.TurnError{Message: "c"}}, {ID: "4", Error: &appwire.TurnError{Message: "d"}}, {ID: "5", Error: &appwire.TurnError{Message: "e"}}}
		_ = recentTurnErrors(appwire.Thread{Turns: turns})
		_ = projectNameFromCWD("/")
		_ = compactDuration(-time.Second)
		_ = compactDuration(0)
		_ = compactDuration(2*time.Hour + 3*time.Minute)
		_ = shortStatusJobID("job_short")
		_ = shortStatusJobID("123456789")
		_ = authSummary(appwire.AuthStatusResponse{Provider: "p", Supported: true})
		_ = authSummary(appwire.AuthStatusResponse{Supported: true, SignedIn: true, StoredEmail: "stored@example.test"})
		_ = authSummary(appwire.AuthStatusResponse{Provider: "p", Supported: true, SignedIn: true})
		_ = authProviderForStatus(hubSessionDetail{})
		_ = hubErrorReason(nil)
		_ = hubErrorReason(errors.New("plain"))
		_ = hubErrorReason(errors.New("appwire request: reason"))
		_ = renderHubSessionStatus(hubSessionDetail{SourceLabel: "serf"}, nil, appwire.AuthStatusResponse{}, errors.New("tasks"), errors.New("auth"), 0)
		ds := &appwire.SerfDiagnostics{Plugins: []appwire.SerfPluginInfo{{Name: "p"}}}
		var status strings.Builder
		appendDiagnosticsSections(&status, ds, 0)

		// Queue/pending helpers.
		m.applyQueueState("", appwire.QueueState{})
		m.applyQueueState("ref", appwire.QueueState{})
		m.session.messages = []transcript.ChatMessage{{PendingID: 1}, {PendingID: 2}}
		m.markPendingFailedByID(2, "failed")
		m.markPendingFailedByID(99, "missing")
		m.removePendingByID(1)
		m.removePendingByID(99)
		_ = m.notificationMatchesCurrentSession(appwire.Notification{Params: []byte(`{"threadId":"different"}`)})
		_ = m.notificationMatchesCurrentSession(appwire.Notification{Params: []byte(`!`)})

		m.mode = hubModeSession
		m.detail.Ref = "local:session"
		for _, n := range []appwire.Notification{
			{Method: appwire.NotifyTurnStarted, Params: []byte(`!`)},
			{Method: appwire.NotifyThreadStatusChanged, Params: []byte(`!`)},
			{Method: appwire.NotifyItemStarted, Params: []byte(`!`)},
			{Method: appwire.NotifyItemCompleted, Params: []byte(`!`)},
			{Method: appwire.NotifyAgentMessageDelta, Params: []byte(`!`)},
			{Method: appwire.NotifyReasoningSummaryDelta, Params: []byte(`!`)},
			{Method: appwire.NotifyAgentMessageReset, Params: []byte(`!`)},
			{Method: appwire.NotifyToolOutputDelta, Params: []byte(`!`)},
			{Method: appwire.NotifySerfJobStarted, Params: []byte(`!`)},
			{Method: appwire.NotifySerfSteeringInjected, Params: []byte(`!`)},
			{Method: appwire.NotifyThreadQueueChanged, Params: []byte(`!`)},
			{Method: appwire.NotifyWarning, Params: []byte(`!`)},
			{Method: appwire.NotifyTurnCompleted, Params: []byte(`!`)},
			{Method: appwire.NotifySerfSandboxEscalationRequested, Params: []byte(`!`)},
		} {
			_ = m.applyHubNotification(n)
		}

		// Child activity helpers and malformed routing.
		for _, item := range []appwire.ThreadItem{{ToolName: "shell", Description: "run"}, {ToolName: "shell"}, {Type: "reasoning"}, {Text: "text"}, {Status: "status"}} {
			_ = childActivityFromItem(item)
		}
		for _, s := range []string{"done", "failed", "cancelled", "stopped", "succeeded", "running"} {
			_ = runStillRunning(s)
		}
		m.watchedChildRefs = map[string]bool{"child": true}
		for _, n := range []appwire.Notification{{Method: "other"}, {Method: appwire.NotifyItemStarted, Params: []byte(`!`)}, {Method: appwire.NotifyItemStarted, Params: []byte(`{"ref":"other"}`)}, {Method: appwire.NotifyItemStarted, Params: []byte(`{"ref":"child","item":!}`)}, {Method: appwire.NotifyItemStarted, Params: []byte(`{"ref":"child","item":{"tool_name":"shell"}}`)}} {
			_, _ = m.handleChildActivityFrame(n)
		}
		m.watchedChildRefs = nil
		_, _ = m.handleChildActivityFrame(appwire.Notification{Method: appwire.NotifyItemStarted})
		m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Subagent: &transcript.SubagentRunInfo{TranscriptRef: "", Status: "running"}}}, {Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Subagent: &transcript.SubagentRunInfo{TranscriptRef: "child", Status: "done"}}}}
		_ = m.subscribeNewChildren()

		// Escalation guard/error branches.
		m.applySandboxEscalation(appwire.SandboxEscalationRequested{}, " ")
		m.removeEscalationAt("missing", -1)
		m.resolveHeadEscalation(false)
		m.detail.Ref = "ref"
		m.applySandboxEscalation(appwire.SandboxEscalationRequested{EscalationID: "e", Tool: "shell"}, "ref")
		m.resolveHeadEscalation(true)
		m.resolveHeadEscalation(true)
		m.handleEscalationResolved(hubEscalationResolvedMsg{ref: "ref", id: "missing"})

		// Overlay priority and dispatch, including completion-on-escape paths.
		m.mode = hubModeSession
		lo := launchconfig.NewLaunchOverridesModal()
		m.launchOverridesModal = &lo
		_ = topmostOverlayName(m)
		_, _ = m.dispatchOverlayKey("launch-overrides", tea.KeyMsg{Type: tea.KeyEsc})
		cp := launchconfig.NewCredentialsPanel()
		m.credentialsPanel = &cp
		_ = topmostOverlayName(m)
		_, _ = m.dispatchOverlayKey("credentials", tea.KeyMsg{Type: tea.KeyEsc})
		ls := launchconfig.NewLaunchSettingsPanel(nil, "")
		m.credentialsPanel = nil
		m.launchSettingsPanel = &ls
		_ = topmostOverlayName(m)
		_, _ = m.dispatchOverlayKey("launch-settings", tea.KeyMsg{Type: tea.KeyEsc})
		pp := launchconfig.NewPluginsPanel()
		m.launchSettingsPanel = nil
		m.pluginsPanel = &pp
		_ = topmostOverlayName(m)
		_, _ = m.dispatchOverlayKey("plugins", tea.KeyMsg{Type: tea.KeyEsc})
		fu := tuipick.NewTextInputModal("prompt", "tag")
		m.pluginsPanel = nil
		m.followupModal = &fu
		_ = topmostOverlayName(m)
		_, _ = m.dispatchOverlayKey("followup", tea.KeyMsg{Type: tea.KeyEsc})
		m.sessionPanel = &hubSessionPanel{}
		_, _ = m.dispatchOverlayKey("session-panel", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
		_, _ = m.dispatchOverlayKey("unknown", tea.KeyMsg{})
		_, _ = sampleRenderFromRealWidget("unknown", 80)
		_ = selector
	})
}
