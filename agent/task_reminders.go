package agent

import (
	"fmt"
	"strings"

	taskpkg "primeradiant.com/serf/agent/task"
)

// formatCurrentTaskSteering wraps a Task into a SYSTEM-REMINDER block that
// becomes the agent's next steering message. Variant: v09
//
// canUseTaskList is the caller's answer to "does this session serve task_list";
// false swaps the closing call instruction for wording the session can obey.
func formatCurrentTaskSteering(task taskpkg.Task, canUseTaskList bool) string {
	var b strings.Builder
	b.WriteString("<SYSTEM-REMINDER>\n")
	fmt.Fprintf(&b, "<CURRENT-TASK id=\"%d\">\n", task.ID)
	fmt.Fprintf(&b, "<TITLE>%s</TITLE>\n", task.Description)
	if task.Prompt != "" {
		b.WriteString("<INSTRUCTIONS>\n")
		b.WriteString(strings.TrimSpace(task.Prompt))
		b.WriteString("\n</INSTRUCTIONS>\n")
	}
	b.WriteString("</CURRENT-TASK>\n")
	if canUseTaskList {
		fmt.Fprintf(&b, "Call your next tool: use task_list to mark task %d as done when this step is complete.\n", task.ID)
	} else {
		fmt.Fprintf(&b, "Work task %d now, and say so when this step is complete.\n", task.ID)
	}
	b.WriteString("</SYSTEM-REMINDER>")
	return b.String()
}

// taskReminderFull generates the full task list for post-compaction injection,
// wrapped as a SYSTEM-REMINDER so the model treats it as steering rather than
// conversational content.
func taskReminderFull(store *taskpkg.TaskStore) string {
	tasks := store.View()
	if len(tasks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<SYSTEM-REMINDER>\n")
	b.WriteString("Task list:\n")
	for _, t := range tasks {
		fmt.Fprintf(&b, "  [%s] #%d: %s", t.Status, t.ID, t.Description)
		if t.ReasoningEffort != "" {
			fmt.Fprintf(&b, " [%s]", t.ReasoningEffort)
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(&b, " (depends_on: %v)", t.DependsOn)
		}
		b.WriteString("\n")
		for _, n := range t.Notes {
			fmt.Fprintf(&b, "    note: %s\n", n)
		}
	}
	b.WriteString("</SYSTEM-REMINDER>")
	return b.String()
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

// taskReminderNudge generates the one-time suggestion to use task_list, wrapped
// as a SYSTEM-REMINDER so all task reminders share a single envelope.
func taskReminderNudge() string {
	return "<SYSTEM-REMINDER>\n" +
		"You have a task_list tool available for organizing multi-step work. " +
		"Consider creating a task list to track your progress.\n" +
		"</SYSTEM-REMINDER>"
}
