package agent

import (
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
	fmt.Fprintf(b, "<CURRENT-TASK id=\"%d\">\n", task.ID)
	fmt.Fprintf(b, "<TITLE>%s</TITLE>\n", task.Description)
	if task.Prompt != "" {
		b.WriteString("<INSTRUCTIONS>\n")
		b.WriteString(strings.TrimSpace(task.Prompt))
		b.WriteString("\n</INSTRUCTIONS>\n")
	}
	b.WriteString("</CURRENT-TASK>\n")
	if !canUseTaskList {
		fmt.Fprintf(b, "Work task %d now, and say so when this step is complete.\n", task.ID)
	}
	return finishSystemReminderBlock(b)
}

// taskReminderFull generates the full task list for post-compaction injection,
// wrapped as a SYSTEM-REMINDER so the model treats it as steering rather than
// conversational content.
func taskReminderFull(store *taskpkg.TaskStore) string {
	return renderTaskListReminder(store.View(), false, 0)
}

// taskReminderFullWithPrompts is the plan a delegate receives at spawn: every
// step's description, status, effort, dependencies, and prompt. A delegate
// works a multi-step job, and a later step's instructions often decide what the
// first step must build, so a step that sees only its own prompt cannot plan
// for the rest.
//
// skipPromptID is the step whose prompt the current-task reminder delivers in
// the next message, so the plan does not repeat it.
//
// Why the plan is announced: on a fixture whose later step needed a function
// only its own prompt named, delegates given the whole plan finished 30 of 30
// runs and delegates given only their first step finished 22 of 30 (Fisher
// exact, two-sided p=0.005), using 12% fewer tokens. One model, one fixture —
// enough to choose this default, not a general claim about task planning.
func taskReminderFullWithPrompts(tasks []taskpkg.Task, skipPromptID int) string {
	return renderTaskListReminder(tasks, true, skipPromptID)
}

// renderTaskListReminder renders tasks as steering. withPrompts adds
// each task's prompt under its line, which the spawn-time plan needs and the
// post-compaction reminder does not.
func renderTaskListReminder(tasks []taskpkg.Task, withPrompts bool, skipPromptID int) string {
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
				fmt.Fprintf(b, "    prompt: %s\n", prompt)
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

// steerDelegatePlan announces a delegate's seeded plan, then its current step.
// A delegate works the list in order, and a later step's prompt often decides
// what the first one must build; announcing only the first step makes the rest
// discoverable solely by asking for them. The current step's prompt is left to
// the current-task reminder rather than repeated in the plan.
//
// Spawn and cold restore both call this, so a resumed delegate's first turn
// matches a fresh spawn's.
func steerDelegatePlan(sess *Session, store *taskpkg.TaskStore) error {
	// One snapshot serves both messages; each store read clones every task.
	tasks := store.View()
	current := taskpkg.Summarize(tasks).Current
	skipPromptID := 0
	if current != nil {
		skipPromptID = current.ID
	}
	if plan := taskReminderFullWithPrompts(tasks, skipPromptID); plan != "" {
		if err := sess.SteerKind(plan, events.SteeringKindTaskList); err != nil {
			return err
		}
	}
	if current != nil {
		return sess.SteerKind(formatCurrentTaskSteering(*current, sess.canInstructTool("task_list")), events.SteeringKindCurrentTask)
	}
	return nil
}

// hasPendingPlanSteering reports whether the session's queue already carries a
// task-list announcement. A child restored after an unclean shutdown reloads
// the spawn-time steering from disk, and announcing again would put the whole
// plan and the current step into its first request twice.
func (s *Session) hasPendingPlanSteering() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.steeringQueue {
		if message.Kind == events.SteeringKindTaskList {
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
