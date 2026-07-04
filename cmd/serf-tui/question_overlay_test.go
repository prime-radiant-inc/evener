package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

// ---- pendingAskQuestions: the transcript pending-set scan (spec §6) -------

func askUserToolMsg(callID string, argsJSON string, done bool, errStr string) transcript.ChatMessage {
	return transcript.ChatMessage{
		Kind:       transcript.MsgTool,
		ToolCallID: callID,
		Tool: &transcript.ToolCallInfo{
			Name:    "ask_user",
			RawArgs: argsJSON,
			Done:    done,
			Error:   errStr,
		},
	}
}

const oneQuestionArgsJSON = `{"questions":[{"header":"DB choice","question":"Which datastore?","options":[{"label":"Postgres","detail":"matches prod","recommended":true},{"label":"SQLite","detail":"zero setup"}],"why":"the writer refactor depends on it","if_unanswered":"default to Postgres"}]}`

const secondQuestionArgsJSON = `{"questions":[{"header":"Naming","question":"Short or long names?","options":[{"label":"Short","detail":""},{"label":"Long","detail":""}]}]}`

func TestPendingAskQuestions_EmptyTranscriptHasNoPending(t *testing.T) {
	if got := pendingAskQuestions(nil); len(got) != 0 {
		t.Fatalf("pendingAskQuestions(nil) = %#v, want empty", got)
	}
}

func TestPendingAskQuestions_CompletedNonErrorAskIsPending(t *testing.T) {
	messages := []transcript.ChatMessage{
		{Kind: transcript.MsgAssistant, Text: "let me check"},
		askUserToolMsg("call_1", oneQuestionArgsJSON, true, ""),
	}
	got := pendingAskQuestions(messages)
	if len(got) != 1 {
		t.Fatalf("pending count = %d, want 1: %#v", len(got), got)
	}
	if got[0].Header != "DB choice" {
		t.Fatalf("pending[0].Header = %q, want %q", got[0].Header, "DB choice")
	}
	if len(got[0].Options) != 2 || got[0].Options[0].Label != "Postgres" || !got[0].Options[0].Recommended {
		t.Fatalf("pending[0].Options = %#v, want Postgres(recommended)+SQLite", got[0].Options)
	}
	if got[0].Why == "" || got[0].IfUnanswered == "" {
		t.Fatalf("pending[0] dropped why/if_unanswered: %#v", got[0])
	}
}

func TestPendingAskQuestions_UserReplyResolvesEverythingBeforeIt(t *testing.T) {
	messages := []transcript.ChatMessage{
		askUserToolMsg("call_1", oneQuestionArgsJSON, true, ""),
		{Kind: transcript.MsgUser, Text: "[answers]\n1. [DB choice] -> \"Postgres\""},
	}
	got := pendingAskQuestions(messages)
	if len(got) != 0 {
		t.Fatalf("pending after a user reply = %#v, want none", got)
	}
}

func TestPendingAskQuestions_ErroredOrIncompleteCallsExcluded(t *testing.T) {
	messages := []transcript.ChatMessage{
		askUserToolMsg("call_err", oneQuestionArgsJSON, true, "duplicate option labels"),
		askUserToolMsg("call_inflight", secondQuestionArgsJSON, false, ""),
	}
	got := pendingAskQuestions(messages)
	if len(got) != 0 {
		t.Fatalf("pending = %#v, want none (errored + in-flight calls both excluded)", got)
	}
}

func TestPendingAskQuestions_MultipleCallsInOneRoundConcatenateInPostingOrder(t *testing.T) {
	messages := []transcript.ChatMessage{
		askUserToolMsg("call_1", oneQuestionArgsJSON, true, ""),
		askUserToolMsg("call_2", secondQuestionArgsJSON, true, ""),
	}
	got := pendingAskQuestions(messages)
	if len(got) != 2 {
		t.Fatalf("pending count = %d, want 2 (spanning both calls)", len(got))
	}
	if got[0].Header != "DB choice" || got[1].Header != "Naming" {
		t.Fatalf("pending order = [%s, %s], want [DB choice, Naming] (posting order)", got[0].Header, got[1].Header)
	}
}

func TestPendingAskQuestions_SteeringDoesNotResolve(t *testing.T) {
	messages := []transcript.ChatMessage{
		askUserToolMsg("call_1", oneQuestionArgsJSON, true, ""),
		{Kind: transcript.MsgSteering, Text: "keep going"},
	}
	got := pendingAskQuestions(messages)
	if len(got) != 1 {
		t.Fatalf("pending after steering = %#v, want still-pending (steering is not a resolving user turn)", got)
	}
}

// ---- composeAskAnswers: byte-exact port of spec §4.3 / the web's golden ---
// tests (cmd/serf-hub/jstest/test-ask-compose.js) ---------------------------

func TestComposeAskAnswers_SpecGoldenExample(t *testing.T) {
	questions := []askQuestion{
		{Header: "DB choice", Resolution: &askResolution{Kind: askResolutionOption, Labels: []string{"Postgres"}}, Note: "only the primary"},
		{Header: "Naming", Resolution: &askResolution{Kind: askResolutionDecide, Leaning: "short names"}, Note: "re-ask if it gets weird"},
		{Header: "CI matrix", Resolution: nil, Note: "irrelevant after #2"},
		{Header: "Endpoint", Resolution: &askResolution{Kind: askResolutionFree, Text: "use RDS, not self-hosted"}},
	}
	want := strings.Join([]string{
		`[answers]`,
		`1. [DB choice] → "Postgres" — note: "only the primary"`,
		`2. [Naming] → you decide — leaning: "short names" — note: "re-ask if it gets weird"`,
		`3. [CI matrix] → skipped (no answer) — note: "irrelevant after #2"`,
		`4. [Endpoint] → free text: "use RDS, not self-hosted"`,
	}, "\n")
	if got := composeAskAnswers(questions); got != want {
		t.Fatalf("composeAskAnswers golden mismatch:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestComposeAskAnswers_UnresolvedComposesAsSkipped(t *testing.T) {
	questions := []askQuestion{{Header: "X", Resolution: nil}}
	want := "[answers]\n1. [X] → skipped (no answer)"
	if got := composeAskAnswers(questions); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestComposeAskAnswers_FallbackEmbedsStatedIfUnanswered(t *testing.T) {
	questions := []askQuestion{{
		Header:       "X",
		IfUnanswered: "default to Postgres and note the assumption in the PR",
		Resolution:   &askResolution{Kind: askResolutionFallback},
	}}
	want := `[answers]` + "\n" + `1. [X] → do your stated fallback ("default to Postgres and note the assumption in the PR")`
	if got := composeAskAnswers(questions); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestComposeAskAnswers_MultiSelectJoinsQuotedLabels(t *testing.T) {
	questions := []askQuestion{{
		Header:     "Pick",
		Resolution: &askResolution{Kind: askResolutionOption, Labels: []string{"A, B", "C"}},
	}}
	want := `[answers]` + "\n" + `1. [Pick] → "A, B", "C"`
	if got := composeAskAnswers(questions); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// ---- questionOverlay: the keypress-driven reducer -------------------------

func twoOptionQuestion(header string, multiSelect bool, ifUnanswered string) askQuestion {
	return askQuestion{
		Header:       header,
		Question:     "Which one?",
		MultiSelect:  multiSelect,
		IfUnanswered: ifUnanswered,
		Options: []askOption{
			{Label: "A", Detail: "option A"},
			{Label: "B", Detail: "option B"},
		},
	}
}

func sendKey(o questionOverlay, msg tea.KeyMsg) questionOverlay {
	updated, _ := o.Update(msg)
	return updated
}

func TestQuestionOverlay_OptionPickSingleSelect(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown}) // move to option B
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})

	got := o.questions[0].Resolution
	if got == nil || got.Kind != askResolutionOption || len(got.Labels) != 1 || got.Labels[0] != "B" {
		t.Fatalf("resolution after pick = %#v, want option[B]", got)
	}
}

func TestQuestionOverlay_OptionPickMultiSelectToggles(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", true, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // toggle A on
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // toggle B on

	got := o.questions[0].Resolution
	if got == nil || got.Kind != askResolutionOption || len(got.Labels) != 2 {
		t.Fatalf("resolution after two toggles = %#v, want option[A,B]", got)
	}

	// Toggling A back off leaves only B.
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyUp})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	got = o.questions[0].Resolution
	if got == nil || len(got.Labels) != 1 || got.Labels[0] != "B" {
		t.Fatalf("resolution after toggling A off = %#v, want option[B]", got)
	}
}

func TestQuestionOverlay_NoteAttachesToWhicheverResolutionIsChosen(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // pick option A

	// "." opens the note field.
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if o.noteEditor == nil {
		t.Fatalf("\".\" did not open the note editor")
	}
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("only the primary")})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})

	if o.noteEditor != nil {
		t.Fatalf("note editor still open after enter")
	}
	if o.questions[0].Note != "only the primary" {
		t.Fatalf("Note = %q, want %q", o.questions[0].Note, "only the primary")
	}
	if o.questions[0].Resolution == nil || o.questions[0].Resolution.Kind != askResolutionOption {
		t.Fatalf("note-taking must not disturb the existing resolution: %#v", o.questions[0].Resolution)
	}
}

func TestQuestionOverlay_NoteEscCancelsWithoutChangingIt(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o.questions[0].Note = "existing note"
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEsc})
	if o.noteEditor != nil {
		t.Fatalf("note editor still open after esc")
	}
	if o.questions[0].Note != "existing note" {
		t.Fatalf("Note = %q, want unchanged %q after cancel", o.questions[0].Note, "existing note")
	}
}

func TestQuestionOverlay_YouDecideWithLeaning(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	// Rows: A(0), B(1), "Something else…"(2), "You decide"(3).
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	if o.valueEditor == nil {
		t.Fatalf("\"You decide\" did not open the leaning editor")
	}
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("short names")})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})

	got := o.questions[0].Resolution
	if got == nil || got.Kind != askResolutionDecide || got.Leaning != "short names" {
		t.Fatalf("resolution = %#v, want decide+leaning %q", got, "short names")
	}
}

func TestQuestionOverlay_YouDecideWithoutLeaningIsStillCommitted(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	// Rows: A(0), B(1), "Something else…"(2), "You decide"(3).
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyDown})
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // opens leaning editor, blank
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // submit blank

	got := o.questions[0].Resolution
	if got == nil || got.Kind != askResolutionDecide || got.Leaning != "" {
		t.Fatalf("resolution = %#v, want decide with no leaning", got)
	}
}

func TestQuestionOverlay_DoThatOnlyAvailableWhenIfUnanswered(t *testing.T) {
	withFallback := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "default to Postgres")}, 80)
	items := withFallback.picker.Filtered()
	// 2 options + "Something else…" + "You decide" + "do that (fallback)" = 5.
	if len(items) != 5 {
		t.Fatalf("picker rows with if_unanswered set = %d, want 5 (2 options + free + decide + fallback)", len(items))
	}
	// Navigate to the fallback row (last one) and pick it.
	for range items[:len(items)-1] {
		withFallback = sendKey(withFallback, tea.KeyMsg{Type: tea.KeyDown})
	}
	withFallback = sendKey(withFallback, tea.KeyMsg{Type: tea.KeyEnter})
	got := withFallback.questions[0].Resolution
	if got == nil || got.Kind != askResolutionFallback {
		t.Fatalf("resolution = %#v, want fallback", got)
	}

	withoutFallback := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	if got := len(withoutFallback.picker.Filtered()); got != 4 {
		t.Fatalf("picker rows without if_unanswered = %d, want 4 (2 options + free + decide, no fallback row)", got)
	}
}

func TestQuestionOverlay_SkipByLeavingUnansweredComposesAsSkipped(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab}) // move on without answering

	if o.questions[0].Resolution != nil {
		t.Fatalf("untouched question got a resolution: %#v", o.questions[0].Resolution)
	}
	got := composeAskAnswers(o.questions)
	want := "[answers]\n1. [Q1] → skipped (no answer)"
	if got != want {
		t.Fatalf("compose after skip = %q, want %q", got, want)
	}
}

func TestQuestionOverlay_TabPagesToNextQuestion(t *testing.T) {
	questions := []askQuestion{twoOptionQuestion("Q1", false, ""), twoOptionQuestion("Q2", false, "")}
	o := *newQuestionOverlay("ref1", questions, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // answer Q1 with option A
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab})

	if o.reviewing() {
		t.Fatalf("overlay jumped straight to review after one tab with two questions")
	}
	if o.current().Header != "Q2" {
		t.Fatalf("current question header = %q, want Q2", o.current().Header)
	}

	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab})
	if !o.reviewing() {
		t.Fatalf("overlay did not reach the review step after tabbing past the last question")
	}

	// shift+tab from review returns to the last question for editing.
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyShiftTab})
	if o.reviewing() || o.current().Header != "Q2" {
		t.Fatalf("shift+tab from review did not return to the last question")
	}
}

func TestQuestionOverlay_EscDefersKeepingAnswers(t *testing.T) {
	o := *newQuestionOverlay("ref1", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // pick option A
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEsc})

	if !o.Deferred() {
		t.Fatalf("esc did not defer the overlay")
	}
	if o.questions[0].Resolution == nil {
		t.Fatalf("esc discarded the in-progress answer; spec requires answers kept")
	}
}

func TestQuestionOverlay_ReviewStepListsAnswersAndWarnsOnUnanswered(t *testing.T) {
	questions := []askQuestion{twoOptionQuestion("DB choice", false, ""), twoOptionQuestion("Naming", false, "")}
	o := *newQuestionOverlay("ref1", questions, 80)
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter}) // answer DB choice with option A
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab})   // -> Naming, left unanswered
	o = sendKey(o, tea.KeyMsg{Type: tea.KeyTab})   // -> review

	if !o.reviewing() {
		t.Fatalf("expected to be at the review step")
	}
	view := o.View()
	if !strings.Contains(view, `[DB choice] → "A"`) {
		t.Fatalf("review view missing the answered line:\n%s", view)
	}
	if !strings.Contains(view, `[Naming] → skipped (no answer)`) {
		t.Fatalf("review view missing the unanswered line:\n%s", view)
	}
	if !strings.Contains(view, "submit with 1 unanswered → it resolves as skipped") {
		t.Fatalf("review view missing the unanswered warning:\n%s", view)
	}

	o = sendKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	if !o.ReadyToSubmit() {
		t.Fatalf("enter at the review step did not mark the overlay ready to submit")
	}
}

// ---- updateQuestionOverlayKey: the hubModel-level submit glue ------------

func countUserMessages(messages []transcript.ChatMessage) int {
	n := 0
	for _, msg := range messages {
		if msg.Kind == transcript.MsgUser {
			n++
		}
	}
	return n
}

// TestUpdateQuestionOverlayKey_StaleReadyToSubmitDoesNotResendAfterRollback
// is a regression test: readyToSubmit is a one-shot signal that MUST be
// consumed the instant it is read, not left true on the overlay. Sequence
// reproduced: submit attempt 1 echoes optimistically and defers the
// overlay; the send then fails and hub_update.go's generic hubSendMsg
// error path rolls the echo back (simulated here directly); ctrl+q resumes
// the same still-pending overlay (un-defers without rebuilding); an
// unrelated keypress (up — unhandled at the review step) must NOT
// re-trigger a second send just because a stale readyToSubmit lingered.
func TestUpdateQuestionOverlayKey_StaleReadyToSubmitDoesNotResendAfterRollback(t *testing.T) {
	m := sampleSessionModel(80, sampleSessionDetails()["serf-idle"])
	m.session.messages = []transcript.ChatMessage{askUserToolMsg("call_1", oneQuestionArgsJSON, true, "")}
	overlay := newQuestionOverlay(m.detail.Ref, pendingAskQuestions(m.session.messages), 80)
	overlay.idx = len(overlay.questions) // at the review step
	m.questionOverlay = overlay

	updated, _ := m.updateQuestionOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if m.questionOverlay == nil || !m.questionOverlay.Deferred() {
		t.Fatalf("expected the overlay to be deferred after the first submit attempt")
	}
	if m.questionOverlay.ReadyToSubmit() {
		t.Fatalf("readyToSubmit must be consumed immediately after the first submit attempt")
	}
	if got := countUserMessages(m.session.messages); got != 1 {
		t.Fatalf("optimistic echo count = %d, want 1 after the first submit attempt", got)
	}

	// Simulate the send failing and hub_update.go's generic rollback, then
	// ctrl+q resuming the same overlay.
	m.session.messages = []transcript.ChatMessage{askUserToolMsg("call_1", oneQuestionArgsJSON, true, "")}
	m.questionOverlay.deferred = false

	updated2, _ := m.updateQuestionOverlayKey(tea.KeyMsg{Type: tea.KeyUp})
	m2 := updated2.(hubModel)
	if got := countUserMessages(m2.session.messages); got != 0 {
		t.Fatalf("an unrelated keypress re-sent the answer after a rollback: %d user messages, want 0", got)
	}
}
