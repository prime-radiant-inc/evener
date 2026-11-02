package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	taskpkg "primeradiant.com/evener/agent/task"
)

const (
	taskCompletionMachineBlockStart = "<evener-task-completion>"
	taskCompletionMachineBlockEnd   = "</evener-task-completion>"
)

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
	fmt.Fprintf(b, "<TITLE>%s</TITLE>\n", task.Description)
	if task.Prompt != "" {
		b.WriteString("<INSTRUCTIONS>\n")
		b.WriteString(strings.TrimSpace(task.Prompt))
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
		fmt.Fprintf(b, "  [%s] #%d: %s", t.Status, t.ID, t.Description)
		if t.ReasoningEffort != "" {
			fmt.Fprintf(b, " [%s]", t.ReasoningEffort)
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(b, " (depends_on: %v)", t.DependsOn)
		}
		b.WriteString("\n")
		if withPrompts && t.ID != skipPromptID {
			if prompt := strings.TrimSpace(t.Prompt); prompt != "" {
				for line := range strings.SplitSeq(prompt, "\n") {
					fmt.Fprintf(b, "    prompt: %s\n", line)
				}
			}
		}
		for _, n := range t.Notes {
			fmt.Fprintf(b, "    note: %s\n", n)
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
	return sess.SteerKind(plan, events.SteeringKindTaskList)
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
