package agent

import (
	"fmt"
	"strings"
)

// formatCurrentTaskSteering wraps a Task into a SYSTEM-REMINDER block that
// becomes the agent's next steering message. Variant: v09
func formatCurrentTaskSteering(task Task) string {
	var b strings.Builder
	b.WriteString("<SYSTEM-REMINDER>\n")
	b.WriteString(fmt.Sprintf("<CURRENT-TASK id=\"%d\">\n", task.ID))
	b.WriteString(fmt.Sprintf("<TITLE>%s</TITLE>\n", task.Description))
	if task.Prompt != "" {
		b.WriteString("<INSTRUCTIONS>\n")
		b.WriteString(strings.TrimSpace(task.Prompt))
		b.WriteString("\n</INSTRUCTIONS>\n")
	}
	b.WriteString("</CURRENT-TASK>\n")
	b.WriteString(fmt.Sprintf("Call your next tool: use task_list to mark task %d as done when this step is complete.\n", task.ID))
	b.WriteString("</SYSTEM-REMINDER>")
	return b.String()
}

// taskReminderFull generates the full task list for post-compaction injection,
// wrapped as a SYSTEM-REMINDER so the model treats it as steering rather than
// conversational content.
func taskReminderFull(store *TaskStore) string {
	tasks := store.View()
	if len(tasks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<SYSTEM-REMINDER>\n")
	b.WriteString("Task list:\n")
	for _, t := range tasks {
		b.WriteString(fmt.Sprintf("  [%s] #%d: %s", t.Status, t.ID, t.Description))
		if t.ReasoningEffort != "" {
			b.WriteString(fmt.Sprintf(" [%s]", t.ReasoningEffort))
		}
		if len(t.DependsOn) > 0 {
			b.WriteString(fmt.Sprintf(" (depends_on: %v)", t.DependsOn))
		}
		b.WriteString("\n")
		for _, n := range t.Notes {
			b.WriteString(fmt.Sprintf("    note: %s\n", n))
		}
	}
	b.WriteString("</SYSTEM-REMINDER>")
	return b.String()
}

// taskReminderForInactivity re-emits the current task's steering message when
// the agent has gone quiet but still has work in progress. Returns empty when
// nothing is in_progress — there is no "current step" to re-state.
func taskReminderForInactivity(store *TaskStore) string {
	current, ok := store.CurrentInProgress()
	if !ok {
		return ""
	}
	return formatCurrentTaskSteering(current)
}

// taskReminderNudge generates the one-time suggestion to use task_list, wrapped
// as a SYSTEM-REMINDER so all task reminders share a single envelope.
func taskReminderNudge() string {
	return "<SYSTEM-REMINDER>\n" +
		"You have a task_list tool available for organizing multi-step work. " +
		"Consider creating a task list to track your progress.\n" +
		"</SYSTEM-REMINDER>"
}
