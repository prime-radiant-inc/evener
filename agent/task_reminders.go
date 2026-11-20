package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/mdfence"
	"primeradiant.com/evener/agent/internal/sysmsg"
	taskpkg "primeradiant.com/evener/agent/task"
)

const (
	taskCompletionMachineBlockStart = "<evener-task-completion>"
	taskCompletionMachineBlockEnd   = "</evener-task-completion>"
)

// taskFieldLineBreaks flattens a model-authored task field rendered inline.
var taskFieldLineBreaks = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

// taskReminderDelimiterTags are the elements a task reminder wraps a task's
// model-authored fields in.
var taskReminderDelimiterTags = []string{"SYSTEM-REMINDER", "CURRENT-TASK", "TITLE", "INSTRUCTIONS"}

// taskFieldForModel returns the reminder's copy of one model-authored task
// field: every frame opener the shared escape knows, and every spelling of the
// reminder's own tags.
//
// The shared escape reads a frame only where a line starts, which is where the
// harness writes its frames. A task field is interpolated inside an element
// instead, so a closing tag in it ends that element early wherever it sits —
// inline as much as at a line start — and an opening tag nests one the harness
// never wrote.
func taskFieldForModel(text string) string {
	return sysmsg.EscapeModelAuthoredFrames(escapeTaskReminderDelimiters(text))
}

// escapeTaskReminderDelimiters rewrites the opening character of each spelling
// of one of the reminder's own tags in text, opening and closing alike and in
// any case, to "&lt;". The tag must end where a tag name ends, so a longer word
// that merely starts with one is left alone. The rewrite adds and removes no
// line, so the escaped text keeps the stored text's structure.
func escapeTaskReminderDelimiters(text string) string {
	if !strings.Contains(text, "<") {
		return text
	}
	var b strings.Builder
	changed := false
	for i := 0; i < len(text); i++ {
		if text[i] == '<' && taskReminderDelimiterAt(text[i:]) {
			if !changed {
				b.Grow(len(text))
				b.WriteString(text[:i])
				changed = true
			}
			b.WriteString("&lt;")
			continue
		}
		if changed {
			b.WriteByte(text[i])
		}
	}
	if !changed {
		return text
	}
	return b.String()
}

// taskReminderDelimiterAt reports whether s, which starts with "<", begins one
// of the reminder's tags, opening or closing.
func taskReminderDelimiterAt(s string) bool {
	rest := strings.TrimPrefix(s[1:], "/")
	for _, tag := range taskReminderDelimiterTags {
		if len(rest) < len(tag) || !strings.EqualFold(rest[:len(tag)], tag) {
			continue
		}
		if len(rest) == len(tag) {
			return true
		}
		switch rest[len(tag)] {
		case '>', '/', '=', ' ', '\t', '\r', '\n', '\v', '\f':
			return true
		}
	}
	return false
}

// taskReminderSteeringKind reports whether kind names a reminder whose text
// interpolates a task store's model-authored fields: the plan, the current
// step, and the inactivity re-statement of that step.
func taskReminderSteeringKind(kind string) bool {
	switch kind {
	case events.SteeringKindTaskList, events.SteeringKindCurrentTask, events.SteeringKindTaskInactive:
		return true
	}
	return false
}

// escapeRetainedTaskReminder escapes the model-authored task fields of a
// retained task reminder, reporting whether any byte changed.
//
// The render-time escape runs where a reminder is built from the task store, so
// a reminder an older build recorded still carries the raw interpolated title,
// prompt, and notes. Such a reminder replays as stored, and a reserved frame in
// one of those fields then arrives in the partner's channel. The retained text
// is the snapshot: its fields are escaped where they stand, never re-rendered,
// so a task that has since changed still arrives as it was recorded.
//
// The reminder's own text keeps every byte: the envelope, the CURRENT-TASK
// element with its TITLE and INSTRUCTIONS, a plan line's "  [status] #N: "
// prefix, and a prompt or note line's continuation prefix. Every span between
// them is the task's model-authored text and is escaped by the same rule the
// renderer uses, so both readers defuse the same spellings.
//
// A text whose envelope or element shape this rule cannot read is left exactly
// as stored: rewriting text whose provenance it cannot establish would be worse
// than the hole it leaves.
func escapeRetainedTaskReminder(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	last := len(lines) - 1
	if last < 2 ||
		strings.TrimSuffix(lines[0], "\r") != "<SYSTEM-REMINDER>" ||
		strings.TrimSuffix(lines[last], "\r") != "</SYSTEM-REMINDER>" {
		return text, false
	}
	body := make([]string, last-1)
	crlf := make([]bool, last-1)
	for i := range body {
		body[i] = lines[i+1]
		if trimmed, ok := strings.CutSuffix(body[i], "\r"); ok {
			body[i], crlf[i] = trimmed, true
		}
	}
	if !escapeRetainedReminderFields(body) {
		return text, false
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[0])
	for i, line := range body {
		if crlf[i] {
			line += "\r"
		}
		out = append(out, line)
	}
	out = append(out, lines[last])
	return strings.Join(out, "\n"), true
}

// escapeRetainedReminderFields escapes one reminder body's model-authored
// fields in place and reports whether any byte changed.
func escapeRetainedReminderFields(body []string) bool {
	open, closeIdx := retainedCurrentTaskElement(body)
	planEnd := len(body)
	if open >= 0 {
		planEnd = open
	}
	changed := escapeRetainedPlanLines(body[:planEnd])
	if open < 0 {
		return changed
	}
	return escapeRetainedCurrentTaskLines(body, open, closeIdx) || changed
}

// retainedCurrentTaskElement returns the line indexes bounding the CURRENT-TASK
// element a reminder carries, or -1 when it carries none.
//
// The renderer opens the element with a TITLE under it and closes it on a line
// of its own, with only harness text after it at most. That placement is what
// separates the harness's element from a field that spelled one: a field's text
// is interpolated INSIDE the harness's markup, so it can never follow the close
// the harness wrote.
func retainedCurrentTaskElement(body []string) (int, int) {
	for closeIdx := len(body) - 1; closeIdx >= 2; closeIdx-- {
		if body[closeIdx] != "</CURRENT-TASK>" {
			continue
		}
		for i := closeIdx - 2; i >= 0; i-- {
			if strings.HasPrefix(body[i], "<CURRENT-TASK ") && strings.HasPrefix(body[i+1], "<TITLE>") {
				return i, closeIdx
			}
		}
		return -1, -1
	}
	return -1, -1
}

// escapeRetainedPlanLines escapes the plan half of a retained reminder: each
// task's title, each step's prompt and notes, and any line a field spilled
// onto.
func escapeRetainedPlanLines(lines []string) bool {
	changed := false
	for i := 0; i < len(lines); {
		switch line := lines[i]; {
		case line == "Task list:":
			i++
		case line == "    note:":
			// A multiline note the current renderer wrote is fenced after this
			// line and already escaped when it was written; the fence and the
			// text under it are quoted and keep their bytes. A body that no
			// fence follows is a field's own spilled lines, escaped below.
			next := skipRetainedNoteFence(lines, i+1)
			if next > i+1 {
				i = next
				continue
			}
			i++
		case strings.HasPrefix(line, "    prompt: "):
			start := i
			for i < len(lines) && strings.HasPrefix(lines[i], "    prompt: ") {
				i++
			}
			changed = escapeRetainedPrefixedRun(lines[start:i], "    prompt: ") || changed
		case strings.HasPrefix(line, "    note: "):
			start := i
			for i < len(lines) && strings.HasPrefix(lines[i], "    note: ") {
				i++
			}
			changed = escapeRetainedPrefixedRun(lines[start:i], "    note: ") || changed
		case taskReminderPlanPrefix(line) > 0:
			prefix := taskReminderPlanPrefix(line)
			changed = escapeRetainedReminderRegion(lines, i, prefix, i, len(line)) || changed
			i++
		default:
			start := i
			for i < len(lines) && !retainedPlanHarnessLine(lines[i]) {
				i++
			}
			changed = escapeRetainedWholeLines(lines, start, i-1) || changed
		}
	}
	return changed
}

// retainedPlanHarnessLine reports whether a plan line is one the renderer wrote
// around a field rather than a field's own spilled line.
func retainedPlanHarnessLine(line string) bool {
	return line == "Task list:" || line == "    note:" ||
		strings.HasPrefix(line, "    prompt: ") || strings.HasPrefix(line, "    note: ") ||
		taskReminderPlanPrefix(line) > 0
}

// taskReminderPlanPrefix returns the length of the "  [status] #N: " prefix a
// rendered plan line writes, or 0 when line is not one.
func taskReminderPlanPrefix(line string) int {
	if !strings.HasPrefix(line, "  [") {
		return 0
	}
	status := strings.Index(line, "] #")
	if status < 0 {
		return 0
	}
	rest := line[status+3:]
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 || !strings.HasPrefix(rest[digits:], ": ") {
		return 0
	}
	return status + 3 + digits + 2
}

// skipRetainedNoteFence returns the index just past the fenced block starting
// at lines[i], or i when no block opens there.
func skipRetainedNoteFence(lines []string, i int) int {
	var fence mdfence.Tracker
	for i < len(lines) {
		if !fence.StepInBlock(strings.TrimPrefix(lines[i], "    ")) {
			break
		}
		i++
	}
	return i
}

// escapeRetainedCurrentTaskLines escapes the model-authored fields of the
// CURRENT-TASK element a retained reminder carries.
//
// The harness closes the title at the end of a line and opens the instructions
// on the line right after it, so only that pair ends the title. A close a field
// quotes under the instructions is text: the instructions body keeps the
// harness's markers and its own quoted spellings are escaped with it. A title
// the element closes with no instructions after it is the whole element, ended
// by its last close.
func escapeRetainedCurrentTaskLines(body []string, open, closeIdx int) bool {
	titleLine := open + 1
	titleOpen := strings.Index(body[titleLine], "<TITLE>")
	if titleOpen < 0 {
		return false
	}
	titleStartCol := titleOpen + len("<TITLE>")
	titleEndLine, titleEndCol, instrOpen := retainedTitleTransition(body, titleLine, closeIdx)
	titleStop := closeIdx
	if instrOpen >= 0 {
		titleStop = instrOpen
	}
	changed := false
	switch {
	case titleEndLine >= 0 && titleEndLine < titleStop:
		changed = escapeRetainedReminderRegion(body, titleLine, titleStartCol, titleEndLine, titleEndCol) || changed
		changed = escapeRetainedWholeLines(body, titleEndLine+1, titleStop-1) || changed
	case titleEndLine < 0:
		// The field never closed its own title, so everything under the opening
		// tag to the instructions is field text.
		changed = escapeRetainedReminderRegion(body, titleLine, titleStartCol, titleStop-1, len(body[titleStop-1])) || changed
	}
	if instrOpen < 0 {
		return changed
	}
	instrClose := -1
	for i := instrOpen + 1; i < closeIdx; i++ {
		if body[i] == "</INSTRUCTIONS>" {
			instrClose = i
		}
	}
	promptEnd := closeIdx
	if instrClose >= 0 {
		promptEnd = instrClose
	}
	if promptEnd-1 >= instrOpen {
		changed = escapeRetainedReminderRegion(body, instrOpen, len(body[instrOpen]), promptEnd-1, len(body[promptEnd-1])) || changed
	}
	if instrClose >= 0 {
		changed = escapeRetainedWholeLines(body, instrClose+1, closeIdx-1) || changed
	}
	return changed
}

// retainedTitleTransition returns the line and column where the element's title
// closes and the line index of its instructions opener, or -1 for a shape that
// carries no instructions.
//
// The harness writes the title's close at the end of a line and the
// instructions opener on the next one, so only that pair marks the end of the
// title; a close a field quotes under the instructions is text and is not the
// transition. A body with no such pair is read the way it was before: the last
// close before the element's end ends the title, and the first instructions
// opener after it opens the element.
func retainedTitleTransition(body []string, titleLine, closeIdx int) (int, int, int) {
	for i := titleLine; i+1 < closeIdx; i++ {
		if strings.HasSuffix(body[i], "</TITLE>") && body[i+1] == "<INSTRUCTIONS>" {
			return i, len(body[i]) - len("</TITLE>"), i + 1
		}
	}
	titleEndLine, titleEndCol := -1, -1
	for i := titleLine; i < closeIdx; i++ {
		if at := strings.LastIndex(body[i], "</TITLE>"); at >= 0 {
			titleEndLine, titleEndCol = i, at
		}
	}
	after := titleLine
	if titleEndLine >= 0 {
		after = titleEndLine + 1
	}
	for i := after; i < closeIdx; i++ {
		if body[i] == "<INSTRUCTIONS>" {
			return titleEndLine, titleEndCol, i
		}
	}
	return titleEndLine, titleEndCol, -1
}

// escapeRetainedReminderRegion escapes the text between the columns of the line
// range body[first..last], leaving the harness columns around it as written,
// and reports whether any byte changed.
//
// The range is escaped as one block, so a fenced example a field quoted is read
// as the block it is. The escape never adds or removes a line, so the escaped
// block splits back onto the lines it came from; a block that no longer has that
// shape is left as stored.
func escapeRetainedReminderRegion(body []string, first, firstCol, last, lastCol int) bool {
	if first < 0 || last < first || last >= len(body) ||
		firstCol < 0 || firstCol > len(body[first]) || lastCol < 0 || lastCol > len(body[last]) {
		return false
	}
	if first == last && firstCol > lastCol {
		return false
	}
	region := make([]string, 0, last-first+1)
	for i := first; i <= last; i++ {
		start, end := 0, len(body[i])
		if i == first {
			start = firstCol
		}
		if i == last {
			end = lastCol
		}
		if start > end {
			return false
		}
		region = append(region, body[i][start:end])
	}
	joined := strings.Join(region, "\n")
	escaped := taskFieldForModel(joined)
	if escaped == joined {
		return false
	}
	pieces := strings.Split(escaped, "\n")
	if len(pieces) != len(region) {
		return false
	}
	for k, piece := range pieces {
		i := first + k
		switch {
		case first == last:
			body[i] = body[i][:firstCol] + piece + body[i][lastCol:]
		case i == first:
			body[i] = body[i][:firstCol] + piece
		case i == last:
			body[i] = piece + body[i][lastCol:]
		default:
			body[i] = piece
		}
	}
	return true
}

// escapeRetainedWholeLines escapes every line of body[first..last] as
// model-authored text and reports whether any byte changed.
func escapeRetainedWholeLines(body []string, first, last int) bool {
	if first > last {
		return false
	}
	return escapeRetainedReminderRegion(body, first, 0, last, len(body[last]))
}

// escapeRetainedPrefixedRun escapes a run of continuation lines, each written
// with prefix in front of the field's own text, and reports whether any byte
// changed.
func escapeRetainedPrefixedRun(lines []string, prefix string) bool {
	if len(lines) == 0 {
		return false
	}
	region := make([]string, len(lines))
	for i, line := range lines {
		region[i] = strings.TrimPrefix(line, prefix)
	}
	joined := strings.Join(region, "\n")
	escaped := taskFieldForModel(joined)
	if escaped == joined {
		return false
	}
	pieces := strings.Split(escaped, "\n")
	if len(pieces) != len(lines) {
		return false
	}
	for i, piece := range pieces {
		lines[i] = prefix + piece
	}
	return true
}

// taskFieldInline renders one model-authored task field for a place that
// interpolates it into a line the harness writes.
//
// A task's description, prompt, and notes were written by a model through
// task_list or seeded from an agent definition, never by the human partner, so
// a frame opener in them must not arrive with the spelling a harness frame
// opens with.
//
// The stored task keeps its own bytes; this returns a copy.
func taskFieldInline(text string) string {
	if strings.ContainsAny(text, "\r\n") {
		// A line break inside an inline field would end the line the harness
		// surrounds it with, so the field is flattened to one line.
		text = taskFieldLineBreaks.Replace(text)
	}
	// The shape normalized above is the shape the model reads, so the escape
	// runs on it: trimming after the escape could lift an opener the escape
	// had read as indented code onto the line's start.
	return taskFieldForModel(strings.TrimSpace(text))
}

// writeTaskNote renders one model-authored progress note under the task's own
// line.
//
// A note is written by the model through task_list, so a frame in it must not
// reach the model with the spelling a harness frame opens with. A note that
// carries no line break keeps the compact one-line rendering, since a line
// break there would end the line the harness writes around it. A note that
// carries line breaks is a list, a code sample, or both: flattening it loses
// the structure the note exists to carry, so it is rendered as a fenced block
// whose fence is longer than any backtick run the note holds. Fencing keeps
// the note's own lines and its own examples intact while keeping a frame line
// inside it from reading as harness markup.
//
// The stored note keeps its own bytes; this writes a copy.
func writeTaskNote(b *strings.Builder, note string) {
	if !strings.ContainsAny(note, "\r\n") {
		fmt.Fprintf(b, "    note: %s\n", taskFieldInline(note))
		return
	}
	body := taskFieldForModel(strings.TrimSpace(note))
	fence := mdfence.Pick(body)
	b.WriteString("    note:\n")
	fmt.Fprintf(b, "    %stext\n", fence)
	for line := range strings.SplitSeq(body, "\n") {
		fmt.Fprintf(b, "    %s\n", line)
	}
	fmt.Fprintf(b, "    %s\n", fence)
}

// formatCurrentTaskSteering wraps a Task into a SYSTEM-REMINDER block that
// becomes the agent's next steering message. Variant: v09
//
// canUseTaskList is the caller's answer to "does this session serve task_list".
// A session with task_list gets no closing instruction: agents close their tasks
// unprompted, and a per-step "mark it done" line pulled them into extra
// bookkeeping. A session without it is told to say when the step is complete.
func formatCurrentTaskSteering(task taskpkg.Task, canUseTaskList bool) string {
	b := systemReminderBlockBuilder()
	writeCurrentTaskBlock(b, task)
	if !canUseTaskList {
		fmt.Fprintf(b, "Work task %d now, and say so when this step is complete.\n", task.ID)
	}
	return finishSystemReminderBlock(b)
}

// writeCurrentTaskBlock renders one task as the CURRENT-TASK element that both
// the standalone reminder and the combined startup plan carry.
func writeCurrentTaskBlock(b *strings.Builder, task taskpkg.Task) {
	fmt.Fprintf(b, "<CURRENT-TASK id=\"%d\">\n", task.ID)
	fmt.Fprintf(b, "<TITLE>%s</TITLE>\n", taskFieldInline(task.Description))
	if task.Prompt != "" {
		b.WriteString("<INSTRUCTIONS>\n")
		// Trimmed before escaping so the escape reads the first line where
		// the harness writes it, at the start of a line.
		b.WriteString(taskFieldForModel(strings.TrimSpace(task.Prompt)))
		b.WriteString("\n</INSTRUCTIONS>\n")
	}
	b.WriteString("</CURRENT-TASK>\n")
}

// taskReminderFull generates the full task list for post-compaction injection,
// wrapped as a SYSTEM-REMINDER so the model treats it as steering rather than
// conversational content.
func taskReminderFull(store *taskpkg.TaskStore) string {
	return renderTaskListReminder(store.View(), false)
}

// taskReminderFullWithPrompts is the plan a delegate receives at spawn: every
// step's description, status, effort, and prompt. A delegate works a multi-step
// job, and a later step's instructions often decide what the first step must
// build, so a step that sees only its own prompt cannot plan for the rest.
//
// Why the plan is announced: on a fixture whose later step needed a function
// only its own prompt named, delegates given the whole plan finished 30 of 30
// runs and delegates given only their first step finished 22 of 30 (Fisher
// exact, two-sided p=0.005), using 12% fewer tokens. One model, one fixture —
// enough to choose this default, not a general claim about task planning.
func taskReminderFullWithPrompts(tasks []taskpkg.Task) string {
	return renderTaskListReminder(tasks, true)
}

// renderTaskListReminder renders tasks as steering. withPrompts adds
// each task's prompt under its line, which the spawn-time plan needs and the
// post-compaction reminder does not.
func renderTaskListReminder(tasks []taskpkg.Task, withPrompts bool) string {
	return renderTaskListReminderSkippingPrompt(tasks, withPrompts, 0)
}

// renderTaskListReminderSkippingPrompt is renderTaskListReminder with one
// task's prompt left out. The startup plan uses it for the task the
// CURRENT-TASK element in the same message already spells out, so the delegate
// is not charged for that prompt twice.
func renderTaskListReminderSkippingPrompt(tasks []taskpkg.Task, withPrompts bool, skipPromptID int) string {
	if len(tasks) == 0 {
		return ""
	}

	b := systemReminderBlockBuilder()
	b.WriteString("Task list:\n")
	for _, t := range tasks {
		fmt.Fprintf(b, "  [%s] #%d: %s", t.Status, t.ID, taskFieldInline(t.Description))
		if t.ReasoningEffort != "" {
			fmt.Fprintf(b, " [%s]", t.ReasoningEffort)
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(b, " (depends_on: %v)", t.DependsOn)
		}
		b.WriteString("\n")
		if withPrompts && t.ID != skipPromptID {
			if prompt := strings.TrimSpace(t.Prompt); prompt != "" {
				// Escaped as one block so a fenced example inside the prompt
				// keeps its bytes, then split so each line stays on the
				// continuation line the harness indents.
				for line := range strings.SplitSeq(taskFieldForModel(prompt), "\n") {
					fmt.Fprintf(b, "    prompt: %s\n", line)
				}
			}
		}
		for _, n := range t.Notes {
			writeTaskNote(b, n)
		}
	}
	return finishSystemReminderBlock(b)
}

// taskReminderForInactivity re-emits the current task's steering message when
// the agent has gone quiet but still has work in progress. Returns empty when
// nothing is in_progress — there is no "current step" to re-state.
func taskReminderForInactivity(store *taskpkg.TaskStore, canUseTaskList bool) string {
	current, ok := store.CurrentInProgress()
	if !ok {
		return ""
	}
	return formatCurrentTaskSteering(current, canUseTaskList)
}

// steerDelegatePlan announces a delegate's seeded plan, and the step it should
// work first. A delegate works the list in order, and a later step's prompt
// often decides what the first one must build; announcing only the first step
// makes the rest discoverable solely by asking for them.
//
// The step to work first is the first in-progress task, which is not always the
// first task: a role's workflow can put its own steps before the parent_tasks
// slot. One message carries both, so a crash cannot leave the plan announced
// with no step to work.
func steerDelegatePlan(sess *Session, store *taskpkg.TaskStore) error {
	// One snapshot serves the plan and the current-step lookup; each store read
	// clones every task.
	tasks := store.View()
	if len(tasks) == 0 {
		return nil
	}
	var current *taskpkg.Task
	for i := range tasks {
		if tasks[i].Status == taskpkg.TaskInProgress {
			current = &tasks[i]
			break
		}
	}
	skipPromptID := 0
	if current != nil {
		skipPromptID = current.ID
	}
	plan := renderTaskListReminderSkippingPrompt(tasks, true, skipPromptID)
	if current != nil {
		plan = withCurrentStep(plan, *current)
	}
	return sess.steerKind(plan, events.SteeringKindTaskList)
}

// withCurrentStep adds the step to work now to a rendered plan, inside the same
// reminder block. A delegate that can use task_list gets no closing
// instruction: agents close their tasks unprompted.
func withCurrentStep(plan string, current taskpkg.Task) string {
	closing := "</SYSTEM-REMINDER>"
	body := strings.TrimSuffix(plan, closing)
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n")
	writeCurrentTaskBlock(&b, current)
	b.WriteString(closing)
	return b.String()
}

// hasTaskListAnnouncement reports whether this session has already been given a
// task-list announcement, from either durable record: a delivery recorded in
// its transcript, or steering still waiting in its queue. A crash can land
// between the task store write and the steering write, so a non-empty store
// alone does not prove the delegate was ever told its plan.
func (s *Session) hasTaskListAnnouncement() bool {
	s.mu.Lock()
	for _, message := range s.steeringQueue {
		if message.Kind == events.SteeringKindTaskList {
			s.mu.Unlock()
			return true
		}
	}
	s.mu.Unlock()

	if s.stateDir == "" || s.id == "" {
		return false
	}
	data, err := readTranscriptFull(context.Background(), transcriptPath(s.stateDir, s.id), s.stateDir)
	if err != nil {
		// An unreadable transcript cannot prove delivery. Announcing again is
		// the safer error: a repeated plan wastes context, a lost one costs the
		// delegate its plan.
		return false
	}
	for _, entry := range data.Entries {
		if entry.Turn.SteeringKind == events.SteeringKindTaskList {
			return true
		}
	}
	return false
}

// taskReminderAllDone signals that the agent has completed all tasks on its
// list and should either add new work or finish up. resultTool is the session's
// own name for the result tool, which a session may rename — "communicate" is
// a tool some sessions do not have.
func taskReminderAllDone(resultTool string) string {
	return "<SYSTEM-REMINDER>\n" +
		"You have completed all tasks on your task list. " +
		"If you have other work to do, add it to the task list now. " +
		"Otherwise, deliver your final output with the " + resultTool + " tool.\n" +
		"</SYSTEM-REMINDER>"
}

// taskReminderAllDoneWhileDelegatesRun keeps the completion signal from
// becoming a premature close instruction when this session is synchronously
// waiting for one or more delegates. The IDs come from the delegate
// controller's live inline waiters; background and terminal delegates are not
// included.
func taskReminderAllDoneWhileDelegatesRun(resultTool string, delegateIDs []string) string {
	var reminder string
	if len(delegateIDs) == 0 {
		reminder = taskReminderAllDone(resultTool)
	} else {
		reminder = "<SYSTEM-REMINDER>\n" +
			"You have completed all tasks on your task list, but delegate(s) " + strings.Join(delegateIDs, ", ") +
			" are still running. Wait for their result before deciding whether to finish. " +
			"If you have other work to do, add it to the task list after the delegate result arrives. " +
			"Otherwise, deliver your final output with the " + resultTool + " tool after that result.\n" +
			"</SYSTEM-REMINDER>"
	}
	return reminder + "\n" + formatTaskCompletionMachineBlock(taskCompletionSteeringData(delegateIDs))
}

// taskReminderTerminalWhileDelegatesRun preserves the completed-all-tasks
// reminder for a fully done list, while accurately describing a list whose
// terminal state includes cancelled tasks.
func taskReminderTerminalWhileDelegatesRun(resultTool string, delegateIDs []string, allDone bool) string {
	if allDone {
		return taskReminderAllDoneWhileDelegatesRun(resultTool, delegateIDs)
	}
	var reminder string
	if len(delegateIDs) == 0 {
		reminder = "<SYSTEM-REMINDER>\n" +
			"No actionable tasks remain on your task list. " +
			"If you have other work to do, add it to the task list now. " +
			"Otherwise, deliver your final output with the " + resultTool + " tool.\n" +
			"</SYSTEM-REMINDER>"
	} else {
		reminder = "<SYSTEM-REMINDER>\n" +
			"No actionable tasks remain on your task list, but delegate(s) " + strings.Join(delegateIDs, ", ") +
			" are still running. Wait for their result before deciding whether to finish. " +
			"If you have other work to do, add it to the task list after the delegate result arrives. " +
			"Otherwise, deliver your final output with the " + resultTool + " tool after that result.\n" +
			"</SYSTEM-REMINDER>"
	}
	return reminder + "\n" + formatTaskCompletionMachineBlock(taskCompletionSteeringData(delegateIDs))
}

func taskCompletionSteeringData(delegateIDs []string) events.TaskCompletionSteeringData {
	state := events.TaskCompletionReadyForFinalOutput
	if len(delegateIDs) != 0 {
		state = events.TaskCompletionWaitingForBlockingDelegates
	}
	return events.TaskCompletionSteeringData{
		CompletionState:     state,
		BlockingDelegateIDs: append([]string{}, delegateIDs...),
	}
}

func formatTaskCompletionMachineBlock(completion events.TaskCompletionSteeringData) string {
	payload, err := json.Marshal(completion)
	if err != nil {
		panic(fmt.Sprintf("marshal task completion steering: %v", err))
	}
	return taskCompletionMachineBlockStart + string(payload) + taskCompletionMachineBlockEnd
}
