//go:build serffuzz

package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// FuzzWidgetPrograms is a deterministic replay corpus for the root TUI's
// leaf widgets and question-overlay state machine. It deliberately stays
// below terminal, filesystem, registry, provider, and transport boundaries.
func FuzzWidgetPrograms(f *testing.F) {
	programs := []func(){exerciseComposerWidgets, exerciseDetailsAndNotices, exerciseQuestionOverlay, exerciseStatusFormatting}
	for i := range programs {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, program int) {
		if program < 0 {
			program = -(program + 1)
		}
		programs[program%len(programs)]()
	})
}

func exerciseComposerWidgets() {
	models := []hubModel{
		{detail: hubSessionDetail{State: "active", Capabilities: hubSessionCapabilities{Send: true}}},
		{detail: hubSessionDetail{State: "active"}},
		{detail: hubSessionDetail{Capabilities: hubSessionCapabilities{Send: true}}},
	}
	for _, m := range models {
		_ = m.sessionComposerReadOnlyReason()
	}

	panels := []composerPanel{
		{Label: " ", ReadOnlyReason: " unavailable ", Width: 20},
		{ShowInput: true, Draft: "a very long logical line\n\nlast", Width: 8, MaxDraftLines: 1,
			Attachments: []*clipboard.PastedImage{nil, {Path: "", Width: 0}, {Path: "plain.png"}, {Path: `C:\\tmp\\image.png`, Width: 2, Height: 3}}},
		{ChipContext: composerContext{Harness: "h"}, Width: 5},
	}
	for _, panel := range panels {
		_ = panel.View()
	}
	_ = filepathBase("")
	_ = filepathBase("plain")
	_ = filepathBase("/tmp/")
	_ = itoa(0)
	_ = itoa(-42)
	_ = renderComposerDraft("", 1, 0)
	_ = renderComposerDraft("a\nb\nc", 20, 2)
	_ = renderQueuePreview(nil, 1)

	contexts := []composerContext{
		{Model: "m", Mode: "queue", Width: 0},
		{Harness: strings.Repeat("x", 100), HubAddr: "hub", Width: 8},
		{Harness: "h", Connected: true, HubAddr: strings.Repeat("z", 80), Mode: "fork", Width: 20},
		{Harness: "h", Mode: "AWAITING", Width: 20},
		{Harness: "h", Width: 1},
	}
	for _, ctx := range contexts {
		_ = renderComposerChipStrip(ctx)
	}
	_ = composerFooterHints("scroll-browse", 20, false)
}

func exerciseDetailsAndNotices() {
	_ = modelAndProfile("", "profile")
	var b strings.Builder
	writeModelOrProviderLine(&b, "", "profile")
	diag := &appwire.SerfDiagnostics{
		Tools:   []appwire.SerfToolInfo{{Name: "core", Source: "core"}, {Name: "mcp", Source: "mcp:server"}, {Name: "custom", Source: "plugin"}},
		MCP:     []appwire.SerfMCPServerInfo{{Name: "server", Tools: []string{"mcp"}, Status: "ready", Error: "old"}},
		Skills:  []appwire.SerfSkillInfo{{Name: "skill"}},
		Plugins: []appwire.SerfPluginInfo{{Name: "plugin"}},
		Hooks:   map[string]int{"turn": 1}, Jobs: []appwire.SerfJobInfo{{JobID: "job", JobType: "exec", Status: "done"}}, Agents: []string{"agent"},
	}
	writeSerfDiagnostics(&b, diag)
	_ = detailsDrawer{Detail: hubSessionDetail{Diagnostics: diag}}.View()
	_ = capabilityList(hubSessionCapabilities{Resume: true, ChangeModel: true})

	_ = (noticePanel{Title: "fallback"}).View()
	m := hubModel{}
	m.addNotice(noticePanel{})
	n := noticePanel{Title: "title", Category: "cat", Source: "source"}
	m.addNotice(n)
	m.addNotice(noticePanel{Title: "title", Category: "cat", Source: "source", Summary: "replacement"})
	m.dismissNotice()
	m.dismissNotice()
	m.clearNoticesByCategory("")
	m.notices = []noticePanel{{Category: "keep"}, {Category: " drop "}}
	m.clearNoticesByCategory("drop")
	_ = classifyWarningCategory("other", nil)
	_ = noticeCategoryForError(errors.New("plain"), "")
	_ = noticeCategoryForError(appwire.WireError{Data: appwire.ErrorData{SerfErrorInfo: appwire.ErrorHubLaunch}}, "")
	_ = noticeSummaryForError(errors.New("plain"), "")
	m.addActionUnavailableNotice("send", "summary", "")
}

func exerciseQuestionOverlay() {
	_ = decodeAskUserArgsJSON(" ")
	_ = decodeAskUserArgsJSON("{")
	_ = askResolutionText(askQuestion{Resolution: &askResolution{Kind: askResolutionOption}})
	_ = askResolutionText(askQuestion{Resolution: &askResolution{Kind: 99}})
	_ = unansweredWarning(1)
	_ = unansweredWarning(3)
	_ = unansweredWarning(0)

	questions := []askQuestion{
		{Header: "one", Question: "First?", Why: "because", MultiSelect: true, IfUnanswered: "fallback", Options: []askOption{{Label: "A", Detail: "detail", Recommended: true}, {Label: "B"}}},
		{Header: "two", Question: "Second?", Options: []askOption{{Label: "C"}}},
	}
	o := newQuestionOverlay("local:thread", questions, 0)
	_, _ = o.Update(struct{}{})
	_ = o.View()
	_ = o.headerStrip()
	o.applyPickerSelection()
	o.applyPickerSelection()
	o.questions[0].Resolution = &askResolution{Kind: askResolutionOption, Labels: []string{"A"}}
	o.questions[0].Note = "note"
	_ = o.questionView()
	_ = o.renderOptionRows()
	_ = o.optionSelected("A")
	for i := 0; i < 2; i++ {
		updatedOverlay, _ := o.Update(tea.KeyMsg{Type: tea.KeyDown})
		o = &updatedOverlay
	}
	o.questions[0].Resolution = &askResolution{Kind: askResolutionFree, Text: "old"}
	o.applyPickerSelection()
	_ = o.View()
	o.valueEditor = nil
	updatedOverlay, _ := o.Update(tea.KeyMsg{Type: tea.KeyDown})
	o = &updatedOverlay
	o.questions[0].Resolution = &askResolution{Kind: askResolutionDecide, Leaning: "lean"}
	o.applyPickerSelection()
	o.valueEditor = nil
	updatedOverlay, _ = o.Update(tea.KeyMsg{Type: tea.KeyDown})
	o = &updatedOverlay
	o.applyPickerSelection()
	invalid := newQuestionOverlay("local:thread", questions, 20)
	invalid.picker = tuipick.PickerPanel{}
	invalid.applyPickerSelection()
	o.idx = len(o.questions)
	o.applyPickerSelection()
	o.openNoteEditor()
	_ = o.optionSelected("A")
	_ = o.renderOptionRows()
	o.idx = 0
	o.openNoteEditor()
	_ = o.View()
	updated, _ := o.Update(tea.KeyMsg{Type: tea.KeyEsc})
	o = &updated
	o.openValueEditor(true, "free", "old")
	updated, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	o = &updated
	updated, _ = o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	o = &updated
	o.openValueEditor(false, "decide", "")
	updated, _ = o.Update(tea.KeyMsg{Type: tea.KeyEsc})
	o = &updated
	o.openValueEditor(true, "free", "")
	o.idx = len(o.questions)
	updated, _ = o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	o = &updated
	_, _ = readTextInputResult(nil)

	o.idx = 1
	for _, key := range []tea.KeyMsg{{Type: tea.KeyShiftTab}, {Type: tea.KeyTab}, {Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune{'x'}}, {Type: tea.KeyUp}, {Type: tea.KeyDown}, {Type: tea.KeyCtrlA}} {
		updated, _ = o.Update(key)
		o = &updated
	}
	o.idx = len(o.questions)
	_ = o.View()
	for _, key := range []tea.KeyMsg{{Type: tea.KeyShiftTab}, {Type: tea.KeyTab}, {Type: tea.KeyEsc}, {Type: tea.KeyEnter}} {
		updated, _ = o.Update(key)
		o = &updated
		o.idx = len(o.questions)
	}

	m := hubModel{detail: hubSessionDetail{Ref: "bad"}}
	_, _ = m.toggleAskOverlay()
	m.detail.Ref = "local:thread"
	_, _ = m.toggleAskOverlay()
	m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "ask_user", Done: true, RawArgs: `{"questions":[{"header":"one","question":"First?"}]}`}}}
	model, _ := m.toggleAskOverlay()
	m = model.(hubModel)
	_, _ = m.toggleAskOverlay()
	_ = sameAskHeaders(nil, questions)
	_ = sameAskHeaders(questions, []askQuestion{{Header: "different"}, {Header: "two"}})
	_, _ = (hubModel{}).updateQuestionOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.questionOverlay.readyToSubmit = true
	m.session.messages = nil
	_, _ = m.updateQuestionOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})

	m = hubModel{detail: hubSessionDetail{Ref: "bad"}, questionOverlay: newQuestionOverlay("bad", questions, 20)}
	m.questionOverlay.idx = len(questions)
	m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "ask_user", Done: true, RawArgs: `{"questions":[{"header":"one","question":"First?"}]}`}}}
	_, _ = m.updateQuestionOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
}

func exerciseStatusFormatting() {
	for _, detail := range []hubSessionDetail{
		{ContextUsed: 46000, ContextWindow: 200000, ContextPressure: 0.23},
		{ContextUsed: 195000, ContextWindow: 200000, ContextPressure: 0.98},
		{ContextUsed: 46000, ContextPressure: 0.23},
		{ContextPressure: 0.23},
		{},
	} {
		_ = formatContextFragment(detail)
	}
	_ = formatTokens(500)
	_ = formatTokens(1500)
	_ = formatTokens(15000)
}
