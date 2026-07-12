//go:build serffuzz

package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	pendingpkg "primeradiant.com/serf/cmd/serf-tui/internal/pending"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// FuzzHubUpdateProgram drives the hub's external-result reducer through
// purpose-built state transitions. No returned command is executed, keeping
// the appwire and terminal boundaries inert during both replay and fuzzing.
func FuzzHubUpdateProgram(f *testing.F) {
	for program := 0; program < 92; program++ {
		f.Add(program)
	}
	f.Fuzz(func(t *testing.T, program int) {
		m := newHubModel(nil, "http://hub.invalid")
		m.width, m.height = 80, 24
		m.mode = hubModeSession
		m.detail = hubSessionDetail{Ref: "serf:current", SessionID: "s", State: "idle", Model: "m", Queue: appwire.QueueState{Preview: []string{"old"}}}
		m.session.sessionID = "s"
		m.sessionQueueRef = m.detail.Ref
		m.sessionQueue = []string{"old"}
		m.forkDraft = &hubForkDraft{Submitting: true}
		m.spawnSubmitting = true
		m.sessionDetailsRequested = true
		m.statusRefreshToken = 7
		m.pendingAttachments = []*clipboard.PastedImage{{Path: t.TempDir() + "/missing"}}

		fail := errors.New("fixture failure")
		partial := errors.New("turn/drainAsSteer partially completed: queued payload before steer failed")
		current := hubSessionDetail{Ref: "serf:current", SessionID: "s", State: "idle", Model: "m", Queue: appwire.QueueState{Preview: []string{"new"}}}
		other := hubSessionDetail{Ref: "serf:other", SessionID: "o", State: "idle", Model: "m"}
		attachment := []*clipboard.PastedImage{{Path: t.TempDir() + "/gone"}}
		entry := func(method, ref string) pendingpkg.PendingEntry {
			return pendingpkg.PendingEntry{ID: 1, Method: method, Text: "payload", Ref: ref, Pending: true}
		}
		base := m

		programs := [][]tea.Msg{
			{struct{}{}},
			{tea.WindowSizeMsg{Width: 2, Height: 3}},
			{hubTreeMsg{err: fail}},
			{hubTreeMsg{tree: hubTreeResponse{Live: []hubTreeNode{{Ref: "serf:one", Title: "one"}}}}},
			{hubSessionMsg{err: fail}},
			{hubSessionMsg{ref: "serf:other", expectedState: "running", expectedRefreshToken: 7, err: fail}},
			{hubSessionMsg{detail: current, ref: current.Ref, expectedState: "idle", expectedRefreshToken: 6}},
			{hubSessionMsg{detail: hubSessionDetail{Ref: current.Ref, State: "running"}, ref: current.Ref, expectedState: "idle", expectedRefreshToken: 7}},
			{hubSessionMsg{detail: current, messages: []transcript.ChatMessage{{Text: "hello"}}}},
			{hubSessionMsg{detail: current, expectedState: "idle", expectedRefreshToken: 7}},
			{hubSessionMsg{detail: other, messages: []transcript.ChatMessage{{Text: "other"}}}},
			{hubNotificationMsg{}},
			{pendingpkg.PendingRegisteredMsg{Entry: entry(appwire.MethodTurnSteer, "serf:other")}},
			{pendingpkg.PendingRegisteredMsg{Entry: entry(appwire.MethodTurnSteer, "serf:current")}},
			{pendingpkg.PendingRegisteredMsg{Entry: entry(appwire.MethodTurnStart, "")}},
			{pendingpkg.PendingRegisteredMsg{Entry: entry(appwire.MethodTurnDrainAsSteer, "")}},
			{pendingpkg.PendingRegisteredMsg{Entry: entry(appwire.MethodTurnQueue, "")}},
			{pendingpkg.PendingFailedMsg{Entry: entry(appwire.MethodTurnStart, "serf:other"), Reason: "no"}},
			{pendingpkg.PendingFailedMsg{Entry: entry(appwire.MethodTurnStart, ""), Reason: "no"}},
			{pendingpkg.PendingConfirmedMsg{Entry: entry(appwire.MethodTurnStart, "serf:other")}},
			{pendingpkg.PendingConfirmedMsg{Entry: entry(appwire.MethodTurnStart, "")}},
			{hubSendMsg{trackedAttachmentSubmit: true, submittedAttachments: attachment}},
			{hubSendMsg{ref: "serf:other", submittedAttachments: attachment}},
			{hubSendMsg{err: fail, text: "payload", draft: "draft", submittedAttachments: attachment}},
			{hubSendMsg{turnID: "turn", submittedAttachments: attachment}},
			{hubQueueMsg{trackedAttachmentSubmit: true, submittedAttachments: attachment}},
			{hubQueueMsg{ref: "serf:other", submittedAttachments: attachment}},
			{hubQueueMsg{err: fail, draft: "draft", submittedAttachments: attachment}},
			{hubQueueMsg{submittedAttachments: attachment}},
			{hubDrainAsSteerMsg{trackedAttachmentSubmit: true, submittedAttachments: attachment}},
			{hubDrainAsSteerMsg{ref: "serf:other", err: fail}},
			{hubDrainAsSteerMsg{ref: "serf:other", queued: true, err: fail, submittedAttachments: attachment}},
			{hubDrainAsSteerMsg{err: partial, queued: true, text: "queued", preQueueDepth: 1, submittedAttachments: attachment}},
			{hubDrainAsSteerMsg{err: partial, queued: true, hadAttachment: true, preQueueDepth: 2, submittedAttachments: attachment}},
			{hubDrainAsSteerMsg{err: fail, draft: "draft", submittedAttachments: attachment}},
			{hubDrainAsSteerMsg{submittedAttachments: attachment}},
			{hubTasksMsg{err: fail}}, {hubTasksMsg{}},
			{hubStatusMsg{err: fail}}, {hubStatusMsg{detail: current}},
			{hubEscalationResolvedMsg{}},
			{hubActionMsg{err: fail, action: "x"}},
			{hubActionMsg{action: "interrupt"}}, {hubActionMsg{action: "compact"}}, {hubActionMsg{action: "shutdown"}}, {hubActionMsg{action: "model"}}, {hubActionMsg{action: "steer"}},
			{hubUpgradeMsg{err: fail}},
			{func() tea.Msg { m.mode = hubModeDashboard; return hubUpgradeMsg{err: fail} }()},
			{hubUpgradeMsg{resp: appwire.UpgradeResponse{Release: "v1", Archive: "a", RestartMessage: "restart"}}},
			{func() tea.Msg {
				m.mode = hubModeDashboard
				return hubUpgradeMsg{resp: appwire.UpgradeResponse{Release: "v1", Archive: "a", RestartMessage: "restart"}}
			}()},
			{hubClearMsg{err: fail}}, {hubClearMsg{resp: hubRefResponse{Ref: "bad"}}}, {hubClearMsg{resp: hubRefResponse{Ref: "serf:ok"}}},
			{hubGoalMsg{err: fail}}, {hubGoalMsg{cleared: true}}, {hubGoalMsg{started: true}}, {hubGoalMsg{}},
			{hubForkMsg{err: fail}}, {hubForkMsg{resp: hubRefResponse{Ref: "bad"}}}, {hubForkMsg{resp: hubRefResponse{Ref: "serf:ok"}}},
			{hubSpawnMsg{err: fail}}, {hubSpawnMsg{resp: hubSpawnResponse{Ref: "bad"}}}, {hubSpawnMsg{resp: hubSpawnResponse{Ref: "serf:ok"}}},
			{hubModelsMsg{err: fail}},
			{func() tea.Msg {
				m.mode = hubModeSpawn
				m.spawnHarness = "other"
				return hubModelsMsg{harness: "h", err: fail}
			}()},
			{func() tea.Msg { m.mode = hubModeSpawn; return hubModelsMsg{harness: "h", models: nil} }()},
			{func() tea.Msg {
				m.mode = hubModeSpawn
				m.spawnHarness = "h"
				return hubModelsMsg{harness: "h", models: []tuipick.ModelPickerItem{{ID: "m"}}}
			}()},
			{func() tea.Msg {
				m.mode = hubModeSpawn
				return hubModelsMsg{models: []tuipick.ModelPickerItem{{ID: "m"}}}
			}()},
			{hubSessionModelsMsg{err: fail}}, {hubSessionModelsMsg{}}, {hubSessionModelsMsg{models: []tuipick.ModelPickerItem{{ID: "m"}}}},
			{hubTranscriptTargetsMsg{err: fail}}, {hubTranscriptTargetsMsg{}},
			{hubTranscriptTargetsMsg{targets: []appwire.ThreadTranscriptTarget{{Ref: "serf:current", Title: "current"}}}},
			{func() tea.Msg {
				m.transcriptView = &hubTranscriptViewState{Ref: "serf:view"}
				return hubTranscriptTargetsMsg{targets: []appwire.ThreadTranscriptTarget{{Ref: "serf:view", Title: "view"}}}
			}()},
			{hubTranscriptMsg{err: fail}}, {hubTranscriptMsg{target: appwire.ThreadTranscriptTarget{Ref: "serf:view", Title: "view"}}},
			{func() tea.Msg { m.mode = hubModeSpawn; return hubSpawnOptionsMsg{err: fail} }()},
			{hubSpawnOptionsMsg{}},
			{func() tea.Msg {
				m.mode = hubModeSpawn
				return hubSpawnOptionsMsg{harnesses: []string{"h"}, modelErr: fail}
			}()},
			{hubModelsMsg{harness: "codex", err: fail}},
			{hubModelsMsg{harness: "h"}},
		}
		m = base
		selected := absInt(program) % len(programs)
		switch selected {
		case 48, 50:
			m.mode = hubModeDashboard
		case 65:
			m.mode = hubModeSpawn
			m.spawnHarness = "other"
		case 66, 68, 78, 80:
			m.mode = hubModeSpawn
		case 67:
			m.mode = hubModeSpawn
			m.spawnHarness = "h"
		case 75:
			m.transcriptView = &hubTranscriptViewState{Ref: "serf:view"}
		case 81:
			m.mode = hubModeSpawn
			m.spawnHarness = "codex"
			m.spawnHarnessKinds = map[string]string{"codex": "codex"}
		case 82:
			m.mode = hubModeSpawn
			m.spawnHarness = "h"
		}
		for _, msg := range programs[selected] {
			model, _ := m.updateImpl(msg)
			m = model.(hubModel)
		}
	})
}

func absInt(v int) int {
	if v < 0 {
		return -(v + 1)
	}
	return v
}
