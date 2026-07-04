package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

// ---- Data model (spec §4.2/§4.3) ------------------------------------------

// askOption is one option of a pending ask_user question.
type askOption struct {
	Label       string `json:"label"`
	Detail      string `json:"detail"`
	Recommended bool   `json:"recommended"`
}

// askQuestion is one pending question the overlay answers, decoded from an
// ask_user call's raw ArgumentsJSON plus the in-progress answer state
// (Resolution/Note) the overlay accumulates as the user works through it.
type askQuestion struct {
	Header       string
	Question     string
	Options      []askOption
	MultiSelect  bool
	Why          string
	IfUnanswered string

	// Resolution is nil until the user picks one; a nil resolution composes
	// identically to an explicit skip (spec §4.3 — exactly 5 resolution
	// kinds, not a 6th "unanswered" kind).
	Resolution *askResolution
	// Note is the universal per-question annotation (spec §4.3): it attaches
	// to whichever resolution is finally chosen, so it lives on the question
	// rather than on any one resolution kind.
	Note string
}

type askResolutionKind int

const (
	askResolutionOption askResolutionKind = iota
	askResolutionFree
	askResolutionDecide
	askResolutionFallback
)

// askResolution is the single answer a question resolves to. Only one of
// Labels/Text/Leaning is meaningful, per Kind.
type askResolution struct {
	Kind    askResolutionKind
	Labels  []string // askResolutionOption; multi-select carries >1
	Text    string   // askResolutionFree
	Leaning string   // askResolutionDecide, optional
}

// ---- Decoding an ask_user call's raw ArgumentsJSON ------------------------

type askQuestionArgs struct {
	Header       string      `json:"header"`
	Question     string      `json:"question"`
	Options      []askOption `json:"options"`
	MultiSelect  bool        `json:"multi_select"`
	Why          string      `json:"why"`
	IfUnanswered string      `json:"if_unanswered"`
}

// decodeAskUserArgsJSON parses one ask_user call's raw ArgumentsJSON (spec
// §4.2) into its questions, in call order. Returns nil on any decode
// failure — a malformed call simply contributes nothing to the pending set
// rather than crashing the overlay.
func decodeAskUserArgsJSON(argsJSON string) []askQuestion {
	if strings.TrimSpace(argsJSON) == "" {
		return nil
	}
	var payload struct {
		Questions []askQuestionArgs `json:"questions"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
		return nil
	}
	out := make([]askQuestion, 0, len(payload.Questions))
	for _, q := range payload.Questions {
		out = append(out, askQuestion{
			Header:       q.Header,
			Question:     q.Question,
			Options:      q.Options,
			MultiSelect:  q.MultiSelect,
			Why:          q.Why,
			IfUnanswered: q.IfUnanswered,
		})
	}
	return out
}

// pendingAskQuestions scans a session's transcript for the currently
// pending ask_user question set (spec §6's pending rule): every question
// posted by a completed, non-error ask_user call since the last resolving
// user turn, in global posting order across every ask_user call sharing the
// set (spec §4.3). A MsgUser reply resolves everything asked before it;
// MsgSteering/MsgSystem/other kinds are not resolving turns and are skipped
// over — mirroring the daemon's own pending definition
// (agent/session_tools_ask.go deriveRestoredState) so cold-attach and
// live-attach agree. An ask_user call that is still in flight (not Done) or
// errored/denied is excluded exactly as the daemon excludes it.
func pendingAskQuestions(messages []transcript.ChatMessage) []askQuestion {
	var pending []askQuestion
	for _, msg := range messages {
		switch msg.Kind {
		case transcript.MsgUser:
			pending = nil
		case transcript.MsgTool:
			if msg.Tool == nil || msg.Tool.Name != "ask_user" || !msg.Tool.Done || msg.Tool.Error != "" {
				continue
			}
			pending = append(pending, decodeAskUserArgsJSON(msg.Tool.RawArgs)...)
		}
	}
	return pending
}

// ---- Compose: the §4.3 [answers] reply, byte-exact with the web's port ---
// (cmd/serf-hub/assets/renderer.js composeAskAnswers, golden-tested by
// cmd/serf-hub/jstest/test-ask-compose.js). strconv.Quote IS Go's %q verb,
// so — unlike the JS port — no hand-rolled escaping is needed here.

// composeAskAnswers renders the [answers] reply: global numbering in
// posting order, one resolution per line, every line carrying its header
// and an optional trailing note (spec §4.3 — the annotation is universal).
func composeAskAnswers(questions []askQuestion) string {
	lines := make([]string, 0, len(questions)+1)
	lines = append(lines, "[answers]")
	for i, q := range questions {
		line := fmt.Sprintf("%d. [%s] → %s", i+1, q.Header, askResolutionText(q))
		if note := strings.TrimSpace(q.Note); note != "" {
			line += " — note: " + strconv.Quote(note)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// askResolutionText renders one question's resolution per spec §4.3's exact
// vocabulary. A question with no resolution composes identically to a skip.
func askResolutionText(q askQuestion) string {
	r := q.Resolution
	if r == nil {
		return "skipped (no answer)"
	}
	switch r.Kind {
	case askResolutionOption:
		if len(r.Labels) == 0 {
			return "skipped (no answer)"
		}
		quoted := make([]string, len(r.Labels))
		for i, l := range r.Labels {
			quoted[i] = strconv.Quote(l)
		}
		return strings.Join(quoted, ", ")
	case askResolutionFree:
		return "free text: " + strconv.Quote(r.Text)
	case askResolutionDecide:
		s := "you decide"
		if leaning := strings.TrimSpace(r.Leaning); leaning != "" {
			s += " — leaning: " + strconv.Quote(leaning)
		}
		return s
	case askResolutionFallback:
		return "do your stated fallback (" + strconv.Quote(q.IfUnanswered) + ")"
	default:
		return "skipped (no answer)"
	}
}

// unansweredWarning renders the review step's unanswered-count warning, or
// "" when everything is answered.
func unansweredWarning(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "submit with 1 unanswered → it resolves as skipped"
	default:
		return fmt.Sprintf("submit with %d unanswered → they resolve as skipped", n)
	}
}

// ---- questionOverlay: the ctrl+q-opened answering flow (spec §6.2) --------

// Synthetic picker-row IDs for the escape rows appended after a question's
// real options. Namespaced so they can never collide with a model-supplied
// option label (the schema guarantees labels unique within a question, but
// says nothing about colliding with these reserved strings).
const (
	askRowFree     = "\x00ask:free"
	askRowDecide   = "\x00ask:decide"
	askRowFallback = "\x00ask:fallback"
)

// questionOverlay is the ctrl+q-opened answering flow: one question at a
// time via a PickerPanel-driven option list plus synthetic escape rows, a
// per-question note field, and a review step before submit. It is created
// and shown ONLY by the ctrl+q keypress (hub_session_keys.go toggleAskOverlay)
// — never from applyHubNotification or any other state change (spec §6.2's
// hard rule: every TUI overlay opens from a keypress).
type questionOverlay struct {
	sessionRef string
	questions  []askQuestion
	// idx indexes questions while < len(questions); idx == len(questions)
	// is the review step.
	idx    int
	picker tuipick.PickerPanel

	// noteEditor/valueEditor are nested text-entry modals. Built on
	// tuipick.TextInputModal per the design's seam facts, but driven
	// synchronously: TextInputModal reports Enter/Esc completion via
	// Done() immediately, and its returned tea.Cmd is a pure closure
	// (no I/O) that we call in place to read the committed
	// TextInputResultMsg rather than round-tripping it through the
	// top-level bubbletea message loop.
	noteEditor  *tuipick.TextInputModal
	valueEditor *tuipick.TextInputModal
	// valueEditorFree distinguishes what valueEditor is currently editing:
	// true = free text ("Something else…"), false = "you decide" leaning.
	valueEditorFree bool

	width int

	// deferred is Esc's effect: hide the overlay behind the composer chip
	// without discarding any answer (spec: "closes overlay back to chip,
	// answers kept"). toggleAskOverlay un-defers rather than rebuilding, as
	// long as the pending set on re-open still matches.
	deferred bool
	// readyToSubmit is a one-shot signal set by Enter at the review step;
	// the hubModel-level glue (updateQuestionOverlayKey) reads it once and
	// performs the actual submit discipline (re-check + send), since only
	// that glue has the client/ref/transcript-reducer access this pure
	// widget deliberately does not.
	readyToSubmit bool
}

// newQuestionOverlay builds a fresh overlay over an immutable snapshot of
// the given pending questions (a private copy, so mutating the overlay's
// in-progress answers never disturbs the transcript-derived slice the
// caller scanned).
func newQuestionOverlay(sessionRef string, questions []askQuestion, width int) *questionOverlay {
	if width <= 0 {
		width = 80
	}
	o := &questionOverlay{
		sessionRef: sessionRef,
		questions:  append([]askQuestion(nil), questions...),
		width:      width,
	}
	o.rebuildPicker()
	return o
}

func (o *questionOverlay) reviewing() bool { return o.idx >= len(o.questions) }

// current returns the question being answered, or nil at the review step.
func (o *questionOverlay) current() *askQuestion {
	if o.reviewing() {
		return nil
	}
	return &o.questions[o.idx]
}

// Deferred reports whether Esc hid the overlay (answers are kept).
func (o *questionOverlay) Deferred() bool { return o.deferred }

// ReadyToSubmit reports whether Enter at the review step has fired. One-shot:
// the hubModel glue clears the overlay (or re-defers it) after reading this.
func (o *questionOverlay) ReadyToSubmit() bool { return o.readyToSubmit }

// SessionRef returns the session this overlay's questions were scanned
// from — used by toggleAskOverlay to detect a stale overlay left over from
// a different session.
func (o *questionOverlay) SessionRef() string { return o.sessionRef }

// rebuildPicker rebuilds the current question's PickerPanel: its options in
// posted order (spec §4.2: the model places its recommended option first;
// the renderer trusts that order and only tags it), plus the always-offered
// "Something else…"/"You decide" rows and, only when the model stated a
// fallback, "do that (fallback)" (spec §6.2). A no-op at the review step.
func (o *questionOverlay) rebuildPicker() {
	q := o.current()
	if q == nil {
		return
	}
	items := make([]tuipick.PickerPanelItem, 0, len(q.Options)+3)
	for _, opt := range q.Options {
		detail := strings.TrimSpace(opt.Detail)
		if opt.Recommended {
			detail = strings.TrimSpace("(recommended) " + detail)
		}
		items = append(items, tuipick.PickerPanelItem{ID: opt.Label, Label: opt.Label, Detail: detail})
	}
	items = append(items,
		tuipick.PickerPanelItem{ID: askRowFree, Label: "Something else…"},
		tuipick.PickerPanelItem{ID: askRowDecide, Label: "You decide"},
	)
	if strings.TrimSpace(q.IfUnanswered) != "" {
		items = append(items, tuipick.PickerPanelItem{ID: askRowFallback, Label: "do that (fallback)", Detail: q.IfUnanswered})
	}
	o.picker = tuipick.NewPickerPanel(q.Header, items, o.width)
}

// Update routes one key message through the overlay. Unrecognized keys are
// dropped (matching focus_trap.go's "unhandled keys DROPPED" rule for a
// trapped overlay) rather than falling through to the composer.
func (o questionOverlay) Update(msg tea.Msg) (questionOverlay, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return o, nil
	}
	if o.noteEditor != nil {
		return o.updateNoteEditor(keyMsg)
	}
	if o.valueEditor != nil {
		return o.updateValueEditor(keyMsg)
	}
	if o.reviewing() {
		return o.updateReviewKey(keyMsg)
	}
	return o.updateQuestionKey(keyMsg)
}

func (o questionOverlay) updateQuestionKey(msg tea.KeyMsg) (questionOverlay, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyDown:
		updated, cmd := o.picker.Update(msg)
		o.picker = updated.(tuipick.PickerPanel)
		return o, cmd
	case tea.KeyEnter:
		o.applyPickerSelection()
		return o, nil
	case tea.KeyTab:
		o.idx++
		o.rebuildPicker()
		return o, nil
	case tea.KeyShiftTab:
		if o.idx > 0 {
			o.idx--
			o.rebuildPicker()
		}
		return o, nil
	case tea.KeyEsc:
		o.deferred = true
		return o, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 1 && msg.Runes[0] == '.' {
			o.openNoteEditor()
		}
		return o, nil
	}
	return o, nil
}

func (o questionOverlay) updateReviewKey(msg tea.KeyMsg) (questionOverlay, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		o.readyToSubmit = true
		return o, nil
	case tea.KeyShiftTab:
		if len(o.questions) > 0 {
			o.idx = len(o.questions) - 1
			o.rebuildPicker()
		}
		return o, nil
	case tea.KeyEsc:
		o.deferred = true
		return o, nil
	}
	return o, nil
}

// applyPickerSelection commits whatever row is highlighted when Enter is
// pressed on the per-question view. Exactly one resolution is ever active
// per question (spec §4.3): picking an option (or the free/decide/fallback
// rows) always overwrites whatever resolution existed before, except that a
// multi-select question's option rows toggle membership instead.
func (o *questionOverlay) applyPickerSelection() {
	q := o.current()
	if q == nil {
		return
	}
	filtered := o.picker.Filtered()
	cursor := o.picker.Cursor()
	if cursor < 0 || cursor >= len(filtered) {
		return
	}
	item := filtered[cursor]
	switch item.ID {
	case askRowFree:
		initial := ""
		if q.Resolution != nil && q.Resolution.Kind == askResolutionFree {
			initial = q.Resolution.Text
		}
		o.openValueEditor(true, fmt.Sprintf("[%s] something else — your answer:", q.Header), initial)
	case askRowDecide:
		initial := ""
		if q.Resolution != nil && q.Resolution.Kind == askResolutionDecide {
			initial = q.Resolution.Leaning
		}
		o.openValueEditor(false, fmt.Sprintf("[%s] you decide — optional leaning (blank for none):", q.Header), initial)
	case askRowFallback:
		q.Resolution = &askResolution{Kind: askResolutionFallback}
	default:
		// An option row: item.ID is the option's label (schema guarantees
		// labels unique within a question, spec §4.2).
		if q.MultiSelect {
			q.Resolution = toggleOptionLabel(q.Resolution, item.ID)
		} else {
			q.Resolution = &askResolution{Kind: askResolutionOption, Labels: []string{item.ID}}
		}
	}
}

// toggleOptionLabel adds or removes label from a multi-select resolution's
// label set, returning nil when the set becomes empty (an empty multi-select
// pick is indistinguishable from unanswered, same as skip).
func toggleOptionLabel(r *askResolution, label string) *askResolution {
	var labels []string
	if r != nil && r.Kind == askResolutionOption {
		labels = append(labels, r.Labels...)
	}
	if idx := indexOfString(labels, label); idx >= 0 {
		labels = append(labels[:idx], labels[idx+1:]...)
	} else {
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		return nil
	}
	return &askResolution{Kind: askResolutionOption, Labels: labels}
}

func indexOfString(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}

func (o *questionOverlay) openNoteEditor() {
	q := o.current()
	if q == nil {
		return
	}
	modal := tuipick.NewTextInputModalWithInput(fmt.Sprintf("[%s] note (optional):", q.Header), "note", q.Note)
	o.noteEditor = &modal
}

func (o *questionOverlay) openValueEditor(free bool, prompt, initial string) {
	modal := tuipick.NewTextInputModalWithInput(prompt, "value", initial)
	o.valueEditor = &modal
	o.valueEditorFree = free
}

func (o questionOverlay) updateNoteEditor(msg tea.KeyMsg) (questionOverlay, tea.Cmd) {
	updated, cmd := o.noteEditor.Update(msg)
	modal := updated.(tuipick.TextInputModal)
	o.noteEditor = &modal
	if !modal.Done() {
		return o, nil
	}
	o.noteEditor = nil
	if result, ok := readTextInputResult(cmd); ok && !result.Cancelled {
		if q := o.current(); q != nil {
			q.Note = result.Value
		}
	}
	return o, nil
}

func (o questionOverlay) updateValueEditor(msg tea.KeyMsg) (questionOverlay, tea.Cmd) {
	updated, cmd := o.valueEditor.Update(msg)
	modal := updated.(tuipick.TextInputModal)
	o.valueEditor = &modal
	if !modal.Done() {
		return o, nil
	}
	free := o.valueEditorFree
	o.valueEditor = nil
	result, ok := readTextInputResult(cmd)
	if !ok || result.Cancelled {
		return o, nil
	}
	q := o.current()
	if q == nil {
		return o, nil
	}
	if free {
		q.Resolution = &askResolution{Kind: askResolutionFree, Text: result.Value}
	} else {
		q.Resolution = &askResolution{Kind: askResolutionDecide, Leaning: result.Value}
	}
	return o, nil
}

// readTextInputResult extracts a TextInputModal's committed value. The
// modal's Update reports completion synchronously via Done(), but carries
// the actual value/cancelled-ness in the tea.Cmd it returns — a pure
// closure with no I/O (it just packages the modal's already-computed
// fields), so calling it directly here is safe and avoids round-tripping
// through the top-level bubbletea message loop for what is, in effect, a
// synchronous local result.
func readTextInputResult(cmd tea.Cmd) (tuipick.TextInputResultMsg, bool) {
	if cmd == nil {
		return tuipick.TextInputResultMsg{}, false
	}
	result, ok := cmd().(tuipick.TextInputResultMsg)
	return result, ok
}

// ---- Rendering -------------------------------------------------------------

func (o *questionOverlay) View() string {
	if o.noteEditor != nil {
		return o.noteEditor.View()
	}
	if o.valueEditor != nil {
		return o.valueEditor.View()
	}
	if o.reviewing() {
		return o.reviewView()
	}
	return o.questionView()
}

func (o *questionOverlay) questionView() string {
	q := o.current()
	th := tuitheme.ActiveTheme()
	dim := lipgloss.NewStyle().Foreground(th.TextDim)

	var b strings.Builder
	if len(o.questions) > 1 {
		b.WriteString(o.headerStrip())
		b.WriteString("\n\n")
	}
	b.WriteString(q.Question)
	if q.MultiSelect {
		b.WriteString("  " + dim.Render("(pick any)"))
	}
	b.WriteString("\n")
	if q.Why != "" {
		b.WriteString(dim.Render("why: " + q.Why))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(o.renderOptionRows())
	if note := strings.TrimSpace(q.Note); note != "" {
		b.WriteString("\n\n")
		b.WriteString(dim.Render("note: " + note))
	}

	footer := tuiprim.ActionBarForWidth(o.width,
		tuiprim.KbdHint("↑/↓", "choose"),
		tuiprim.KbdHint("enter", "answer"),
		tuiprim.KbdHint(".", "note"),
		tuiprim.KbdHint("tab", "next question"),
		tuiprim.KbdHint("esc", "defer"),
	)
	title := fmt.Sprintf("[%s] question %d/%d", q.Header, o.idx+1, len(o.questions))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: title, Width: o.width, Body: b.String(), Footer: footer})
}

// headerStrip renders the paged-questions header-chip strip (spec §6.2,
// shown when N>1): every question's header, with the current one picked out.
func (o *questionOverlay) headerStrip() string {
	th := tuitheme.ActiveTheme()
	active := lipgloss.NewStyle().Bold(true).Foreground(th.Accent)
	dim := lipgloss.NewStyle().Foreground(th.TextDim)
	parts := make([]string, len(o.questions))
	for i, q := range o.questions {
		label := "[" + q.Header + "]"
		if i == o.idx {
			label = active.Render(label)
		} else {
			label = dim.Render(label)
		}
		parts[i] = label
	}
	return strings.Join(parts, "  ")
}

// renderOptionRows renders the current question's PickerPanel rows: cursor
// navigation is delegated to the PickerPanel (Update, above), but rendering
// is bespoke rather than PickerPanel.View() — this overlay has no filter
// affordance (2-7 rows never need one) and its own footer, so the picker's
// stock "type to filter" chrome would be pure noise here.
func (o *questionOverlay) renderOptionRows() string {
	th := tuitheme.ActiveTheme()
	cursorStyle := lipgloss.NewStyle().Foreground(th.Accent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(th.TextDim)

	items := o.picker.Filtered()
	cursor := o.picker.Cursor()
	lines := make([]string, 0, len(items))
	for i, item := range items {
		marker := "  "
		label := item.Label
		if i == cursor {
			marker = "> "
			label = cursorStyle.Render(label)
		}
		if selected := o.optionSelected(item.ID); selected {
			marker = marker[:len(marker)-1] + "◆"
		}
		line := marker + label
		if item.Detail != "" {
			line += "  " + dim.Render(item.Detail)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// optionSelected reports whether optionID is part of the current question's
// resolution (used to mark checked options in a multi-select question).
func (o *questionOverlay) optionSelected(optionID string) bool {
	q := o.current()
	if q == nil || q.Resolution == nil || q.Resolution.Kind != askResolutionOption {
		return false
	}
	return indexOfString(q.Resolution.Labels, optionID) >= 0
}

// reviewView renders the pre-submit review step (spec §6.2): every
// question's header → answer line (the same vocabulary as the composed
// reply, so what the user sees here is what gets sent) plus a warning when
// any question is still unanswered.
func (o *questionOverlay) reviewView() string {
	th := tuitheme.ActiveTheme()
	warnStyle := lipgloss.NewStyle().Foreground(th.StateAwaiting).Bold(true)

	var b strings.Builder
	unanswered := 0
	for i, q := range o.questions {
		line := fmt.Sprintf("%d. [%s] → %s", i+1, q.Header, askResolutionText(q))
		if note := strings.TrimSpace(q.Note); note != "" {
			line += " — note: " + strconv.Quote(note)
		}
		if q.Resolution == nil {
			unanswered++
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if warning := unansweredWarning(unanswered); warning != "" {
		b.WriteString("\n")
		b.WriteString(warnStyle.Render(warning))
	}

	footer := tuiprim.ActionBarForWidth(o.width,
		tuiprim.KbdHint("enter", "submit"),
		tuiprim.KbdHint("shift+tab", "back to a question"),
		tuiprim.KbdHint("esc", "defer"),
	)
	return tuiprim.Overlay(tuiprim.OverlayOpts{
		Title:  "review answers",
		Width:  o.width,
		Body:   strings.TrimRight(b.String(), "\n"),
		Footer: footer,
	})
}

// ---- hubModel glue: opening (ctrl+q) and routing trapped keys -------------

// toggleAskOverlay is the ctrl+q keybinding (hub_session_keys.go): the ONLY
// place the question overlay ever opens (spec §6.2's hard rule — never from
// applyHubNotification or any other state change). Always re-scans the
// transcript fresh so a stale overlay (already resolved, or left over from
// a different session) never reopens; an in-progress overlay for the SAME
// still-pending set is resumed with its answers intact, matching the web's
// collapse/expand (never destructive) chip behavior.
func (m hubModel) toggleAskOverlay() (tea.Model, tea.Cmd) {
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return m, nil
	}
	pending := pendingAskQuestions(m.session.messages)
	if len(pending) == 0 {
		m.questionOverlay = nil
		m.addSessionSystem("No question is waiting.")
		return m, nil
	}
	if m.questionOverlay != nil && m.questionOverlay.SessionRef() == ref.String() && sameAskHeaders(m.questionOverlay.questions, pending) {
		m.questionOverlay.deferred = false
		return m, nil
	}
	m.questionOverlay = newQuestionOverlay(ref.String(), pending, m.width)
	return m, nil
}

// sameAskHeaders is a cheap same-pending-set check: the daemon guarantees
// the pending set cannot change shape while the session stays awaiting (a
// new ask_user call can't run until the current one is answered), so
// matching headers in order is enough to tell "still the same set, resume
// it" apart from "a different session, or a genuinely new set — rebuild".
func sameAskHeaders(a, b []askQuestion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Header != b[i].Header {
			return false
		}
	}
	return true
}

// updateQuestionOverlayKey routes a trapped key to the open question
// overlay. Reached two ways: dispatchOverlayKey's "question-overlay" case
// (every key the focus trap catches) and updateSessionKey's own early
// return (so Esc, which bypasses the trap entirely per keyAllowedThroughTrap,
// still reaches the overlay). Owns the submit discipline (spec §6.1/§6.2):
// re-check the transcript before sending, then send via the same
// sendHubInput path and draft the composer itself uses, so a Conflict falls
// back to the composer draft through the existing generic hubSendMsg
// failure handling — never auto-retried.
func (m hubModel) updateQuestionOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.questionOverlay == nil {
		return m, nil
	}
	updated, cmd := m.questionOverlay.Update(msg)
	m.questionOverlay = &updated

	if !m.questionOverlay.ReadyToSubmit() {
		return m, cmd
	}
	// Consume the one-shot signal immediately: whatever happens below (a
	// collapse, an invalid ref, or a real send), the NEXT keypress must not
	// re-trigger submit just because a resumed-from-deferred overlay still
	// carried readyToSubmit=true from before.
	m.questionOverlay.readyToSubmit = false

	// Re-check discipline: if the pending set has already resolved (another
	// client's reply landed, or this session somehow drained already),
	// collapse instead of sending — no send, no message.
	if len(pendingAskQuestions(m.session.messages)) == 0 {
		m.questionOverlay = nil
		return m, nil
	}
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return m, nil
	}
	composed := composeAskAnswers(m.questionOverlay.questions)
	reducer := m.sessionTranscriptReducer()
	reducer.ApplyUserMessageEcho(composed)
	m.applySessionTranscriptReducer(reducer)
	m.session.refreshViewport()
	// Defer rather than discard: on failure, hub_update.go's existing
	// generic hubSendMsg error path rolls back this optimistic echo and
	// restores `composed` into the composer draft (the draft argument
	// below IS the composed text) — the same recovery every composer send
	// already gets, satisfying "the composed text drops into the composer,
	// never auto-retried" with no ask-specific plumbing. Deferring (instead
	// of clearing) means ctrl+q can resume this exact overlay, answers
	// intact, if the send failed; if it succeeded, the chip's own
	// awaiting-state gate makes the dormant overlay unreachable in
	// practice, and the next ctrl+q self-heals via the empty-pending check
	// above.
	m.questionOverlay.deferred = true
	return m, sendHubInput(m.client, ref, composed, composed, nil)
}
