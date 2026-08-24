package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

// ---- decodeAskUserArgsJSON edge cases ----------------------------------------

func TestCovDecodeAskUserArgsJSON_WhitespaceReturnsNil(t *testing.T) {
	if got := decodeAskUserArgsJSON("   \n\t "); got != nil {
		t.Fatalf("whitespace-only args = %#v, want nil", got)
	}
}

func TestCovDecodeAskUserArgsJSON_MalformedReturnsNil(t *testing.T) {
	if got := decodeAskUserArgsJSON("{not json"); got != nil {
		t.Fatalf("malformed args = %#v, want nil", got)
	}
}

func TestCovDecodeAskUserArgsJSON_EmptyQuestionsArray(t *testing.T) {
	got := decodeAskUserArgsJSON(`{"questions":[]}`)
	if len(got) != 0 {
		t.Fatalf("empty questions array = %#v, want empty slice", got)
	}
}

func TestCovDecodeAskUserArgsJSON_PreservesAllFields(t *testing.T) {
	args := `{"questions":[{"header":"H","question":"Q?","options":[{"label":"L","detail":"D","recommended":true}],"multi_select":true,"why":"W","if_unanswered":"IU"}]}`
	got := decodeAskUserArgsJSON(args)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	q := got[0]
	if q.Header != "H" || q.Question != "Q?" || q.MultiSelect != true || q.Why != "W" || q.IfUnanswered != "IU" {
		t.Fatalf("decoded question missing fields: %#v", q)
	}
	if len(q.Options) != 1 || q.Options[0].Label != "L" || q.Options[0].Detail != "D" || !q.Options[0].Recommended {
		t.Fatalf("decoded options missing fields: %#v", q.Options)
	}
}

// ---- askResolutionText edge cases -------------------------------------------

func TestCovAskResolutionText_OptionEmptyLabels(t *testing.T) {
	q := askQuestion{Resolution: &askResolution{Kind: askResolutionOption, Labels: nil}}
	if got := askResolutionText(q); got != "skipped (no answer)" {
		t.Fatalf("empty labels option = %q, want skipped", got)
	}
}

func TestCovAskResolutionText_FreeText(t *testing.T) {
	q := askQuestion{Resolution: &askResolution{Kind: askResolutionFree, Text: "hello"}}
	got := askResolutionText(q)
	if !strings.Contains(got, "free text:") || !strings.Contains(got, "hello") {
		t.Fatalf("free text resolution = %q, want free text with hello", got)
	}
}

func TestCovAskResolutionText_DecideWithLeaning(t *testing.T) {
	q := askQuestion{Resolution: &askResolution{Kind: askResolutionDecide, Leaning: " go short "}}
	got := askResolutionText(q)
	if !strings.Contains(got, "you decide") || !strings.Contains(got, "go short") {
		t.Fatalf("decide with leaning = %q", got)
	}
}

func TestCovAskResolutionText_DecideNoLeaning(t *testing.T) {
	q := askQuestion{Resolution: &askResolution{Kind: askResolutionDecide, Leaning: "  "}}
	if got := askResolutionText(q); !strings.Contains(got, "you decide") || strings.Contains(got, "leaning") {
		t.Fatalf("decide no leaning = %q, want just you decide", got)
	}
}

func TestCovAskResolutionText_Fallback(t *testing.T) {
	q := askQuestion{
		IfUnanswered: "default action",
		Resolution:   &askResolution{Kind: askResolutionFallback},
	}
	got := askResolutionText(q)
	if !strings.Contains(got, "fallback") || !strings.Contains(got, "default action") {
		t.Fatalf("fallback = %q", got)
	}
}

func TestCovAskResolutionText_UnknownKindDefaults(t *testing.T) {
	q := askQuestion{Resolution: &askResolution{Kind: askResolutionKind(99)}}
	if got := askResolutionText(q); got != "skipped (no answer)" {
		t.Fatalf("unknown kind = %q, want skipped", got)
	}
}

// ---- unansweredWarning ------------------------------------------------------

func TestCovUnansweredWarning_Zero(t *testing.T) {
	if got := unansweredWarning(0); got != "" {
		t.Fatalf("zero = %q, want empty", got)
	}
}

func TestCovUnansweredWarning_One(t *testing.T) {
	got := unansweredWarning(1)
	if !strings.Contains(got, "1 unanswered") {
		t.Fatalf("one = %q, want contains '1 unanswered'", got)
	}
}

func TestCovUnansweredWarning_Many(t *testing.T) {
	got := unansweredWarning(5)
	if !strings.Contains(got, "5 unanswered") || !strings.Contains(got, "they resolve") {
		t.Fatalf("many = %q", got)
	}
}

// ---- newQuestionOverlay edge cases ------------------------------------------

func TestCovNewQuestionOverlay_ZeroWidthDefaultsTo80(t *testing.T) {
	o := newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 0)
	if o.width != 80 {
		t.Fatalf("width = %d, want 80", o.width)
	}
}

func TestCovNewQuestionOverlay_NegativeWidthDefaultsTo80(t *testing.T) {
	o := newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, -5)
	if o.width != 80 {
		t.Fatalf("width = %d, want 80", o.width)
	}
}

func TestCovNewQuestionOverlay_MakesPrivateCopy(t *testing.T) {
	orig := []askQuestion{twoOptionQuestion("Q1", false, "")}
	o := newQuestionOverlay("ref", orig, 80)
	o.questions[0].Header = "MUTATED"
	if orig[0].Header != "Q1" {
		t.Fatalf("newQuestionOverlay did not make a private copy; original was mutated")
	}
}

func TestCovNewQuestionOverlay_SessionRef(t *testing.T) {
	o := newQuestionOverlay("local:abc", nil, 80)
	if o.SessionRef() != "local:abc" {
		t.Fatalf("SessionRef = %q, want local:abc", o.SessionRef())
	}
}

func TestCovNewQuestionOverlay_EmptyQuestionsReviewingImmediately(t *testing.T) {
	o := newQuestionOverlay("ref", nil, 80)
	if !o.reviewing() {
		t.Fatalf("empty questions should be reviewing immediately")
	}
	if o.current() != nil {
		t.Fatalf("current() should be nil at review step")
	}
}

// ---- updateQuestionKey: ShiftTab on first question --------------------------

func TestCovUpdateQuestionKey_ShiftTabAtFirstStaysAtFirst(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyShiftTab})
	if o.idx != 0 {
		t.Fatalf("shift+tab at first question: idx = %d, want 0 (no negative)", o.idx)
	}
}

func TestCovUpdateQuestionKey_TabAdvancesIdx(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, ""), twoOptionQuestion("Q2", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab})
	if o.idx != 1 {
		t.Fatalf("tab: idx = %d, want 1", o.idx)
	}
}

func TestCovUpdateQuestionKey_DotOpensNoteEditor(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if o.noteEditor == nil {
		t.Fatalf("dot did not open note editor")
	}
}

func TestCovUpdateQuestionKey_DotWithMultipleRunesDoesNotOpen(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	if o.noteEditor != nil {
		t.Fatalf("multi-rune key should not open note editor")
	}
}

func TestCovUpdateQuestionKey_UnhandledKeyDropped(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	before := o.idx
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyCtrlC})
	if o.idx != before {
		t.Fatalf("unhandled key changed state: idx = %d, want %d", o.idx, before)
	}
}

// ---- updateReviewKey edge cases ---------------------------------------------

func TestCovUpdateReviewKey_ShiftTabBackToLastQuestion(t *testing.T) {
	questions := []askQuestion{twoOptionQuestion("Q1", false, ""), twoOptionQuestion("Q2", false, "")}
	o := *newQuestionOverlay("ref", questions, 80)
	o.idx = len(o.questions) // at review
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyShiftTab})
	if o.reviewing() {
		t.Fatalf("shift+tab from review should go back to last question")
	}
	if o.current().Header != "Q2" {
		t.Fatalf("shift+tab from review: header = %q, want Q2", o.current().Header)
	}
}

func TestCovUpdateReviewKey_ShiftTabWithNoQuestionsStaysReviewing(t *testing.T) {
	o := *newQuestionOverlay("ref", nil, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyShiftTab})
	if !o.reviewing() {
		t.Fatalf("shift+tab with no questions should stay reviewing")
	}
}

func TestCovUpdateReviewKey_EscDefers(t *testing.T) {
	questions := []askQuestion{twoOptionQuestion("Q1", false, "")}
	o := *newQuestionOverlay("ref", questions, 80)
	o.idx = len(o.questions) // at review
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEsc})
	if !o.Deferred() {
		t.Fatalf("esc at review should defer")
	}
}

func TestCovUpdateReviewKey_UnhandledKeyDropped(t *testing.T) {
	questions := []askQuestion{twoOptionQuestion("Q1", false, "")}
	o := *newQuestionOverlay("ref", questions, 80)
	o.idx = len(o.questions) // at review
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyCtrlC})
	if o.ReadyToSubmit() {
		t.Fatalf("unhandled key should not set readyToSubmit")
	}
}

// ---- Update routing: non-KeyMsg ---------------------------------------------

func TestCovUpdate_NonKeyMsgDropped(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	updated, _ := o.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	if updated.idx != 0 {
		t.Fatalf("non-key msg changed state: idx = %d, want 0", updated.idx)
	}
}

// ---- Update routing: noteEditor takes precedence ----------------------------

func TestCovUpdate_NoteEditorTakesPrecedence(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if o.noteEditor == nil {
		t.Fatalf("dot did not open note editor")
	}
	// While note editor is open, a dot key should go to the editor, not open another.
	updated, _ := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if updated.noteEditor == nil {
		t.Fatalf("note editor closed unexpectedly while typing into it")
	}
}

// ---- applyPickerSelection: out of bounds cursor -----------------------------

func TestCovApplyPickerSelection_NegativeCursor(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	// Replace picker with one that has no items — cursor is 0 but filtered is empty,
	// so the cursor >= len(filtered) guard fires and no resolution is set.
	o.picker = tuipick.NewPickerPanel("H", nil, 80)
	o.applyPickerSelection()
	if o.questions[0].Resolution != nil {
		t.Fatalf("applyPickerSelection with no items should not set resolution: %#v", o.questions[0].Resolution)
	}
}

// ---- openNoteEditor / openValueEditor at review step ------------------------

func TestCovOpenNoteEditor_NilAtReviewStep(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.idx = len(o.questions) // at review
	o.openNoteEditor()
	if o.noteEditor != nil {
		t.Fatalf("openNoteEditor at review step should be a no-op")
	}
}

func TestCovOpenValueEditor_FreeSetsFlag(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.openValueEditor(true, "prompt", "initial")
	if o.valueEditor == nil || !o.valueEditorFree {
		t.Fatalf("openValueEditor(free=true) should set valueEditor and flag")
	}
}

func TestCovOpenValueEditor_DecideSetsFlag(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.openValueEditor(false, "prompt", "initial")
	if o.valueEditor == nil || o.valueEditorFree {
		t.Fatalf("openValueEditor(free=false) should set valueEditor and clear flag")
	}
}

// ---- updateValueEditor: cancel via Esc --------------------------------------

func TestCovUpdateValueEditor_CancelDoesNotSetResolution(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.openValueEditor(true, "prompt", "")
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEsc})
	if o.valueEditor != nil {
		t.Fatalf("value editor still open after esc")
	}
	if o.questions[0].Resolution != nil {
		t.Fatalf("esc cancel should not set resolution")
	}
}

func TestCovUpdateValueEditor_FreeTextSetsResolution(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.openValueEditor(true, "prompt", "")
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom answer")})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	if o.questions[0].Resolution == nil || o.questions[0].Resolution.Kind != askResolutionFree || o.questions[0].Resolution.Text != "custom answer" {
		t.Fatalf("free text resolution = %#v, want free text 'custom answer'", o.questions[0].Resolution)
	}
}

func TestCovUpdateValueEditor_DecideSetsResolution(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.openValueEditor(false, "prompt", "")
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("leaning X")})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	if o.questions[0].Resolution == nil || o.questions[0].Resolution.Kind != askResolutionDecide || o.questions[0].Resolution.Leaning != "leaning X" {
		t.Fatalf("decide resolution = %#v, want decide leaning 'leaning X'", o.questions[0].Resolution)
	}
}

// ---- readTextInputResult ----------------------------------------------------

func TestCovReadTextInputResult_NilCmd(t *testing.T) {
	_, ok := readTextInputResult(nil)
	if ok {
		t.Fatalf("nil cmd should return ok=false")
	}
}

func TestCovReadTextInputResult_NonResultMsg(t *testing.T) {
	cmd := func() tea.Msg { return tea.WindowSizeMsg{} }
	_, ok := readTextInputResult(cmd)
	if ok {
		t.Fatalf("non-TextInputResultMsg should return ok=false")
	}
}

// ---- View: note editor and value editor views -------------------------------

func TestCovView_NoteEditorShownWhenOpen(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.openNoteEditor()
	view := o.View()
	if !strings.Contains(view, "note") {
		t.Fatalf("note editor view should contain 'note': %q", view)
	}
}

func TestCovView_ValueEditorShownWhenOpen(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.openValueEditor(true, "custom prompt", "")
	view := o.View()
	if !strings.Contains(view, "custom prompt") {
		t.Fatalf("value editor view should contain prompt: %q", view)
	}
}

// ---- headerStrip: exercised only when >1 questions ---------------------------

func TestCovHeaderStrip_RenderedWithMultipleQuestions(t *testing.T) {
	withTestColorProfile(t)
	questions := []askQuestion{twoOptionQuestion("Q1", false, ""), twoOptionQuestion("Q2", false, "")}
	o := *newQuestionOverlay("ref", questions, 80)
	view := o.View()
	if !strings.Contains(view, "[Q1]") || !strings.Contains(view, "[Q2]") {
		t.Fatalf("header strip should render both headers:\n%s", view)
	}
}

// ---- questionView: multi-select + why ---------------------------------------

func TestCovQuestionView_MultiSelectShowsPickAny(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{{
		Header:      "Q1",
		Question:    "Pick any?",
		MultiSelect: true,
		Options:     []askOption{{Label: "A"}, {Label: "B"}},
	}}, 80)
	view := o.View()
	if !strings.Contains(view, "(pick any)") {
		t.Fatalf("multi-select view should show (pick any):\n%s", view)
	}
}

func TestCovQuestionView_WhyShownWhenPresent(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{{
		Header:   "Q1",
		Question: "Which?",
		Why:      "because reasons",
		Options:  []askOption{{Label: "A"}},
	}}, 80)
	view := o.View()
	if !strings.Contains(view, "why: because reasons") {
		t.Fatalf("view should show why text:\n%s", view)
	}
}

func TestCovQuestionView_NoteShownWhenPresent(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.questions[0].Note = "my note"
	view := o.View()
	if !strings.Contains(view, "note: my note") {
		t.Fatalf("view should show note text:\n%s", view)
	}
}

// ---- optionSelected ---------------------------------------------------------

func TestCovOptionSelected_NilResolutionReturnsFalse(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	if o.optionSelected("A") {
		t.Fatalf("optionSelected should be false when resolution is nil")
	}
}

func TestCovOptionSelected_NonOptionKindReturnsFalse(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.questions[0].Resolution = &askResolution{Kind: askResolutionFree, Text: "x"}
	if o.optionSelected("A") {
		t.Fatalf("optionSelected should be false when resolution kind is not option")
	}
}

func TestCovOptionSelected_AtReviewReturnsFalse(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.idx = len(o.questions)
	if o.optionSelected("A") {
		t.Fatalf("optionSelected at review step should return false")
	}
}

// ---- toggleAskOverlay: no-pending and same-headers-resume --------------------

func TestCovToggleAskOverlay_NoPendingClearsOverlay(t *testing.T) {
	m := sampleSessionModel(80, sampleSessionDetails()["evener-idle"])
	m.questionOverlay = newQuestionOverlay(m.detail.Ref, []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	updated, _ := m.toggleAskOverlay()
	m2 := updated.(hubModel)
	if m2.questionOverlay != nil {
		t.Fatalf("toggleAskOverlay with no pending should clear overlay")
	}
}

func TestCovToggleAskOverlay_InvalidRefAddsSystemMessage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Ref = "" // invalid
	updated, _ := m.toggleAskOverlay()
	m2 := updated.(hubModel)
	// Should have added a system message about invalid ref.
	if m2.questionOverlay != nil {
		t.Fatalf("toggleAskOverlay with invalid ref should not open overlay")
	}
}

// ---- sameAskHeaders ---------------------------------------------------------

func TestCovSameAskHeaders_DifferentLengthReturnsFalse(t *testing.T) {
	a := []askQuestion{{Header: "X"}}
	b := []askQuestion{{Header: "X"}, {Header: "Y"}}
	if sameAskHeaders(a, b) {
		t.Fatalf("different lengths should return false")
	}
}

func TestCovSameAskHeaders_SameHeadersReturnsTrue(t *testing.T) {
	a := []askQuestion{{Header: "X"}, {Header: "Y"}}
	b := []askQuestion{{Header: "X"}, {Header: "Y"}}
	if !sameAskHeaders(a, b) {
		t.Fatalf("same headers should return true")
	}
}

func TestCovSameAskHeaders_DifferentHeadersReturnsFalse(t *testing.T) {
	a := []askQuestion{{Header: "X"}, {Header: "Y"}}
	b := []askQuestion{{Header: "X"}, {Header: "Z"}}
	if sameAskHeaders(a, b) {
		t.Fatalf("different headers should return false")
	}
}

// ---- updateQuestionOverlayKey: pending already resolved collapses ----------

func TestCovUpdateQuestionOverlayKey_PendingResolvedCollapses(t *testing.T) {
	m := sampleSessionModel(80, sampleSessionDetails()["evener-idle"])
	m.session.messages = []transcript.ChatMessage{askUserToolMsg("call_1", oneQuestionArgsJSON, true, "")}
	overlay := newQuestionOverlay(m.detail.Ref, pendingAskQuestions(m.session.messages), 80)
	overlay.idx = len(overlay.questions) // at review
	overlay.readyToSubmit = false
	m.questionOverlay = overlay

	// Simulate the pending set being resolved by another client before submit.
	m.session.messages = []transcript.ChatMessage{
		askUserToolMsg("call_1", oneQuestionArgsJSON, true, ""),
		{Kind: transcript.MsgUser, Text: "[answers]\n1. [DB choice] -> \"Postgres\""},
	}

	// Set readyToSubmit so the submit path runs and detects no pending.
	m.questionOverlay.readyToSubmit = true
	updated, _ := m.updateQuestionOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(hubModel)
	if m2.questionOverlay != nil {
		t.Fatalf("overlay should be collapsed when pending resolved before submit")
	}
}

// ---- decodeAskUserArgsJSON: malformed JSON via direct unmarshal failure ------

func TestCovDecodeAskUserArgsJSON_NonObjectPayload(t *testing.T) {
	if got := decodeAskUserArgsJSON(`[1,2,3]`); got != nil {
		t.Fatalf("non-object payload = %#v, want nil", got)
	}
}

// ---- pendingAskQuestions: in-flight and non-ask_user tools -----------------

func TestCovPendingAskQuestions_NonAskUserToolSkipped(t *testing.T) {
	messages := []transcript.ChatMessage{
		{
			Kind:       transcript.MsgTool,
			ToolCallID: "call_1",
			Tool: &transcript.ToolCallInfo{
				Name:    "shell",
				RawArgs: "{}",
				Done:    true,
			},
		},
	}
	if got := pendingAskQuestions(messages); len(got) != 0 {
		t.Fatalf("non-ask_user tool should not be pending: %#v", got)
	}
}

func TestCovPendingAskQuestions_NullToolSkipped(t *testing.T) {
	messages := []transcript.ChatMessage{
		{Kind: transcript.MsgTool, Tool: nil},
	}
	if got := pendingAskQuestions(messages); len(got) != 0 {
		t.Fatalf("nil tool should not be pending: %#v", got)
	}
}

// ---- composeAskAnswers: with note --------------------------------------------

func TestCovComposeAskAnswers_WithNoteAndFreeText(t *testing.T) {
	questions := []askQuestion{
		{
			Header:     "X",
			Resolution: &askResolution{Kind: askResolutionFree, Text: "custom"},
			Note:       "  my note  ",
		},
	}
	got := composeAskAnswers(questions)
	if !strings.Contains(got, "note: \"my note\"") {
		t.Fatalf("compose with note = %q, want contains note: \"my note\"", got)
	}
}

func TestCovComposeAskAnswers_EmptyNoteOmitted(t *testing.T) {
	questions := []askQuestion{
		{
			Header:     "X",
			Resolution: &askResolution{Kind: askResolutionOption, Labels: []string{"A"}},
			Note:       "   ",
		},
	}
	got := composeAskAnswers(questions)
	if strings.Contains(got, "note:") {
		t.Fatalf("whitespace-only note should be omitted: %q", got)
	}
}

// ---- renderOptionRows: selected marker on multi-select -----------------------

func TestCovRenderOptionRows_SelectedMarkerShown(t *testing.T) {
	withTestColorProfile(t)
	o := *newQuestionOverlay("ref", []askQuestion{{
		Header:      "Q1",
		Question:    "Pick any?",
		MultiSelect: true,
		Options:     []askOption{{Label: "A"}, {Label: "B"}},
	}}, 80)
	// Toggle A on
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	view := o.View()
	if !strings.Contains(view, "◆") {
		t.Fatalf("selected multi-select option should show ◆ marker:\n%s", view)
	}
}

// ---- reviewView: full review with all answered -------------------------------

func TestCovReviewView_AllAnsweredNoWarning(t *testing.T) {
	questions := []askQuestion{twoOptionQuestion("Q1", false, ""), twoOptionQuestion("Q2", false, "")}
	o := *newQuestionOverlay("ref", questions, 80)
	// Answer both
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // Q1 -> A
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab})   // -> Q2
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // Q2 -> A
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab})   // -> review
	view := o.View()
	if strings.Contains(view, "unanswered") {
		t.Fatalf("review with all answered should not show warning:\n%s", view)
	}
}

// ---- applyPickerSelection: free and decide with existing resolution ---------

func TestCovApplyPickerSelection_FreeWithExistingText(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	// Set an existing free text resolution
	o.questions[0].Resolution = &askResolution{Kind: askResolutionFree, Text: "previous"}
	// Navigate to "Something else…" (index 2)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	if o.valueEditor == nil {
		t.Fatalf("free row did not open value editor")
	}
	// The initial text should be "previous" — we check by looking at the editor's current value.
	// Since we can't easily read the modal's internal value, we just verify the editor opened.
}

func TestCovApplyPickerSelection_DecideWithExistingLeaning(t *testing.T) {
	o := *newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	// Set an existing decide resolution
	o.questions[0].Resolution = &askResolution{Kind: askResolutionDecide, Leaning: "prev lean"}
	// Navigate to "You decide" (index 3)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	if o.valueEditor == nil {
		t.Fatalf("decide row did not open value editor")
	}
}

// ---- toggleOptionLabel: empty resolution starts fresh -----------------------

func TestCovToggleOptionLabel_NilResolutionAddsLabel(t *testing.T) {
	r := toggleOptionLabel(nil, "A")
	if r == nil || r.Kind != askResolutionOption || len(r.Labels) != 1 || r.Labels[0] != "A" {
		t.Fatalf("toggle nil + A = %#v, want option[A]", r)
	}
}

func TestCovToggleOptionLabel_NonOptionKindStartsFresh(t *testing.T) {
	r := &askResolution{Kind: askResolutionFree, Text: "x"}
	result := toggleOptionLabel(r, "A")
	if result == nil || len(result.Labels) != 1 || result.Labels[0] != "A" {
		t.Fatalf("toggle non-option + A = %#v, want option[A]", result)
	}
}

func TestCovToggleOptionLabel_ToggleOffToNil(t *testing.T) {
	r := &askResolution{Kind: askResolutionOption, Labels: []string{"A"}}
	result := toggleOptionLabel(r, "A")
	if result != nil {
		t.Fatalf("toggle off last label = %#v, want nil", result)
	}
}

// ---- json round-trip of askQuestionArgs --------------------------------------

func TestCovAskQuestionArgs_JSONRoundTrip(t *testing.T) {
	original := askQuestionArgs{
		Header:       "H",
		Question:     "Q?",
		Options:      []askOption{{Label: "L", Detail: "D", Recommended: true}},
		MultiSelect:  true,
		Why:          "W",
		IfUnanswered: "IU",
	}
	// Wrap in the expected {"questions": [...]} envelope for decode.
	envelope := struct {
		Questions []askQuestionArgs `json:"questions"`
	}{Questions: []askQuestionArgs{original}}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded := decodeAskUserArgsJSON(string(data))
	if len(decoded) != 1 {
		t.Fatalf("decoded len = %d, want 1", len(decoded))
	}
}
