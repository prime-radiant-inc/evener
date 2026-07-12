//go:build serffuzz

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// FuzzSessionControls replays deterministic state programs for the session
// composer. Effects stay behind temporary files, a fake clipboard, and the
// package's in-process appwire test client.
func FuzzSessionControls(f *testing.F) {
	for i := 0; i < 8; i++ {
		f.Add(byte(i))
	}
	f.Fuzz(func(t *testing.T, program byte) {
		switch program % 8 {
		case 0:
			testAttachmentDefensiveBranches(t)
		case 1:
			testQueueAttachmentFailures(t)
		case 2:
			testSessionSimpleKeys(t)
		case 3:
			testSessionBrowseForkAuthKeys(t)
		case 4:
			testSessionPickersAndPanels(t)
		case 5:
			testSessionHelpers(t)
		case 6:
			testSessionSendBranches(t)
		case 7:
			testClipboardAndPasteBranches(t)
		}
	})
}

func testAttachmentDefensiveBranches(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addPendingAttachment(nil)
	a, b := &clipboard.PastedImage{MarkerN: 3}, &clipboard.PastedImage{MarkerN: 5}
	m.pendingAttachments = []*clipboard.PastedImage{nil, a, b}
	m.clearSubmittedAttachments([]*clipboard.PastedImage{nil, a}, false)
	m.clearSubmittedAttachments([]*clipboard.PastedImage{nil}, false)
	m.pendingAttachments = []*clipboard.PastedImage{a}
	m.clearSubmittedAttachments([]*clipboard.PastedImage{a}, true)
	m.restoreSubmittedAttachments([]*clipboard.PastedImage{nil, a, a, b})
	m = newSessionHubModel(nil)
	m.pendingAttachments = []*clipboard.PastedImage{b}
	m.restoreSubmittedAttachments([]*clipboard.PastedImage{nil, a, b})
	m.noteUnrestoredFailedComposerPayload("send", "", nil)
	m.noteUnrestoredFailedComposerPayload("send", "", []*clipboard.PastedImage{a})
	m.noteUnrestoredFailedComposerPayload("send", "", []*clipboard.PastedImage{a, b})
	m.noteUnrestoredFailedComposerPayload("send", " text ", nil)
	m.attachmentSubmitsInFlight = 2
	m.deferredAttachmentCleanup = []*clipboard.PastedImage{nil}
	m.finishAttachmentSubmit()
	m.finishAttachmentSubmit()
	cleanupPendingAttachmentFile(nil)
	cleanupPendingAttachmentFile(&clipboard.PastedImage{})
}

func testQueueAttachmentFailures(t *testing.T) {
	missing := &clipboard.PastedImage{Path: filepath.Join(t.TempDir(), "missing.png")}
	_ = sendHubQueue(nil, appwire.Ref{}, "x", "d", []*clipboard.PastedImage{missing})()
	_ = sendHubDrainAsSteer(nil, appwire.Ref{}, "x", "d", []*clipboard.PastedImage{missing})()
	path := filepath.Join(t.TempDir(), "image.bin")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := buildAttachmentItems([]*clipboard.PastedImage{nil, {Path: path}})
	if err != nil || len(items) != 1 || items[0].MediaType != "image/png" {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func testSessionSimpleKeys(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.input.MaxHeight = 3
	m.session.input.SetHeight(2)
	m.session.input.SetValue("")
	m.resizeSessionInputFrom(2)
	m.session.input.SetValue("a\nb\nc\nd")
	m.resizeSessionInputFrom(1)
	_ = clampSessionInputHeight(0, 3)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter, Alt: true}, {Type: tea.KeyCtrlJ}, {Type: tea.KeyCtrlP},
		{Type: tea.KeyCtrlL}, {Type: tea.KeyEsc}, {Type: tea.KeyPgUp},
		{Type: tea.KeyCtrlQ},
	} {
		m.updateSessionKey(key)
	}
	m = newSessionHubModel(nil)
	m.questionOverlay = newQuestionOverlay("bad", []askQuestion{twoOptionQuestion("Q", false, "")}, 40)
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = newSessionHubModel(nil)
	m.pendingAttachments = []*clipboard.PastedImage{{MarkerN: 1}}
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	m = newSessionHubModel(nil)
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("no.png"), Paste: true})
	m = newSessionHubModel(nil)
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	m.session.history = []string{"prior"}
	m.session.historyIdx = 0
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyDown})
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{}
	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlL})
	if cmd != nil {
		_ = cmd()
	}
	m.lastCtrlC = time.Now()
	_, _ = m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlC})
}

func testSessionBrowseForkAuthKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyEnter}} {
		m := newSessionHubModel(nil)
		m.forkDraft = &hubForkDraft{}
		m.updateSessionKey(key)
	}
	m := newSessionHubModel(nil)
	m.forkDraft = &hubForkDraft{Submitting: true}
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	m.forkDraft = &hubForkDraft{}
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	m.forkDraft = &hubForkDraft{}
	m.session.setInputValue("fork")
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyEnter}} {
		m = newSessionHubModel(nil)
		m.authLoginFlowID = "flow"
		m.authLoginProvider = "openai"
		m.updateSessionKey(key)
	}
	m = newSessionHubModel(nil)
	m.authLoginFlowID = "flow"
	m.session.setInputValue("redirect")
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	m.session.scrollMode = true
	for _, k := range []tea.KeyType{tea.KeyEsc, tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyPgUp, tea.KeyPgDown, tea.KeyEnter, tea.KeyCtrlT} {
		m.updateSessionKey(tea.KeyMsg{Type: k})
	}
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
}

func testSessionPickersAndPanels(t *testing.T) {
	m := newSessionHubModel(nil)
	p := launchconfig.NewLaunchOverridesModal()
	m.launchOverridesModal = &p
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	p = launchconfig.NewLaunchOverridesModal()
	modal := tuipick.NewTextInputModal("x", "tag")
	m.launchOverridesModal = &p
	m.followupModal = &modal
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = newSessionHubModel(nil)
	theme := tuipick.NewThemePicker()
	m.sessionThemePicker = &theme
	m.stateDir = t.TempDir()
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = newSessionHubModel(nil)
	theme = tuipick.NewThemePicker()
	m.sessionThemePicker = &theme
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	model := tuipick.NewModelPicker(nil, "", 80)
	m.sessionModelPicker = &model
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = newSessionHubModel(nil)
	model = tuipick.NewModelPicker([]tuipick.ModelPickerItem{{ID: "chosen", Display: "chosen"}}, "", 80)
	m.sessionModelPicker = &model
	m.detail.Ref = ""
	m.session.sessionID = ""
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	transcript := tuipick.NewTranscriptPicker(nil, "", 80)
	m.sessionTranscriptPicker = &transcript
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = newSessionHubModel(nil)
	transcript = tuipick.NewTranscriptPicker([]tuipick.ModelPickerItem{{ID: "gone", Display: "gone"}}, "", 80)
	m.sessionTranscriptPicker = &transcript
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	for _, target := range []appwire.ThreadTranscriptTarget{{Ref: "main", Kind: "main"}, {Ref: "child", Kind: "child"}} {
		m = newSessionHubModel(nil)
		transcript = tuipick.NewTranscriptPicker([]tuipick.ModelPickerItem{{ID: target.Ref, Display: target.Ref}}, "", 80)
		m.sessionTranscriptPicker = &transcript
		m.transcriptTargets = []appwire.ThreadTranscriptTarget{target}
		m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	m = newSessionHubModel(nil)
	m.sessionPanel = &hubSessionPanel{}
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = newSessionHubModel(nil)
	m.transcriptView = &hubTranscriptViewState{}
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = newSessionHubModel(nil)
	m.transcriptView = &hubTranscriptViewState{}
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyDown})
}

func testSessionHelpers(t *testing.T) {
	m := newSessionHubModel(nil)
	_ = m.restoreInstructionMessage()
	_ = sessionSendUnavailableReason("")
	_ = sessionSendUnavailableReason("why")
	m.hubURL = " "
	m.detail.Ref = " "
	m.session.sessionID = " "
	_ = m.restoreInstructionMessage()
	_ = isQueuedDrainPartial(errors.New("x"))
	_ = isQueuedDrainPartial(appwire.WireError{Data: appwire.ErrorData{SerfErrorInfo: appwire.ErrorQueuedDrainPartial}})
	_ = isQueuedDrainPartial(appwire.WireError{Data: map[string]any{"serfErrorInfo": string(appwire.ErrorQueuedDrainPartial)}})
	_ = isQueuedDrainPartial(appwire.WireError{Data: "x"})
	for _, key := range []tea.KeyMsg{{Alt: true}, {Alt: true, Type: tea.KeyEnter}, {Alt: true, Type: tea.KeyRunes}, {Alt: true, Type: tea.KeyRunes, Runes: []rune("vv")}, {Alt: true, Type: tea.KeyRunes, Runes: []rune("V")}} {
		_ = isAltVKey(key)
	}
	m.runHubSlashCommand("definitely-not-built-in", "x")
	m.detail.Ref = "bad ref"
	m.runHubSlashCommand("definitely-not-built-in", "")
}

func testSessionSendBranches(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Queue = true
	m.detail.Ref = "bad ref"
	m.session.setInputValue("x")
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	m.detail.Capabilities.Send = false
	m.session.setInputValue("x")
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	m.detail.Ref = "bad ref"
	m.session.setInputValue("x")
	m.updateSessionKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Queue = true
	m.handleSessionForceSteer()
	m.detail.Capabilities.Steer = false
	m.handleSessionForceSteer()
	m.detail.Capabilities.Steer = true
	m.detail.Ref = "bad ref"
	m.handleSessionForceSteer()
	m = newSessionHubModel(nil)
	m.handleSessionForceSteer()
	m = newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Queue = true
	m.detail.Capabilities.Steer = true
	m.sessionQueue = []string{"queued"}
	_, _ = m.handleSessionForceSteer()
}

func testClipboardAndPasteBranches(t *testing.T) {
	m := newSessionHubModel(nil)
	m.clipboardSource = &fakeKeybindClipboard{filesErr: errors.New("no"), imageBytes: nil}
	m.handleClipboardPaste()
	old := newSessionClipboardSource
	_ = old()
	newSessionClipboardSource = func() clipboard.ClipboardSource { return &fakeKeybindClipboard{filesErr: errors.New("no")} }
	t.Cleanup(func() { newSessionClipboardSource = old })
	m = newSessionHubModel(nil)
	m.handleClipboardPaste()
	dir := t.TempDir()
	m.handleBracketedPaste("")
	m.handleBracketedPaste(filepath.Join(dir, "x.txt"))
	m.handleBracketedPaste(filepath.Join(dir, "missing.png"))
	m.handleBracketedPaste(dir + "/fake.png")
	path := filepath.Join(dir, "ok.png")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.handleBracketedPaste(path)
}
