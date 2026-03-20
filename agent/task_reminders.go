package agent

import (
	"fmt"
	"strings"
)

// taskReminderFull generates the full task list for post-compaction injection.
func taskReminderFull(store *TaskStore) string {
	tasks := store.View()
	if len(tasks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Task list:\n")
	for _, t := range tasks {
		b.WriteString(fmt.Sprintf("  [%s] #%d: %s", t.Status, t.ID, t.Description))
		if len(t.DependsOn) > 0 {
			b.WriteString(fmt.Sprintf(" (depends_on: %v)", t.DependsOn))
		}
		b.WriteString("\n")
		for _, n := range t.Notes {
			b.WriteString(fmt.Sprintf("    note: %s\n", n))
		}
	}
	return b.String()
}

// taskReminderForInactivity generates the periodic reminder when tasks exist
// but the tool hasn't been used recently.
func taskReminderForInactivity(store *TaskStore) string {
	tasks := store.View()
	if len(tasks) == 0 {
		return ""
	}

	total, done := store.Progress()

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Task reminder (Progress: %d/%d tasks complete):\n", done, total))

	// Show in-progress tasks.
	var hasInProgress bool
	for _, t := range tasks {
		if t.Status == TaskInProgress {
			b.WriteString(fmt.Sprintf("  Current: #%d — %s\n", t.ID, t.Description))
			hasInProgress = true
		}
	}

	// Show next eligible tasks (up to 3).
	eligible := store.NextEligible()
	if len(eligible) > 3 {
		eligible = eligible[:3]
	}
	if len(eligible) > 0 {
		if hasInProgress {
			b.WriteString("  Up next:\n")
		} else {
			b.WriteString("  Ready:\n")
		}
		for _, t := range eligible {
			b.WriteString(fmt.Sprintf("    #%d — %s\n", t.ID, t.Description))
		}
	}

	return b.String()
}

// taskReminderNudge generates the one-time suggestion to use task_list.
func taskReminderNudge() string {
	return "You have a task_list tool available for organizing multi-step work. " +
		"Consider creating a task list to track your progress."
}

// formatEligibleSummary appends progress and next-eligible task info to msg.
// Used by both append and update handlers to avoid a separate view call.
func formatEligibleSummary(msg *strings.Builder, store *TaskStore) {
	eligible := store.NextEligible()
	total, done := store.Progress()

	msg.WriteString(fmt.Sprintf("Progress: %d/%d tasks complete.\n", done, total))

	switch len(eligible) {
	case 0:
		if done == total {
			msg.WriteString("All tasks complete.")
		} else {
			msg.WriteString("No tasks are currently ready (remaining tasks have unsatisfied dependencies).")
		}
	case 1:
		msg.WriteString(fmt.Sprintf("\nNext task: #%d — %s.", eligible[0].ID, eligible[0].Description))
		if eligible[0].Prompt != "" {
			msg.WriteString(fmt.Sprintf("\nInstructions: %s", eligible[0].Prompt))
		}
		msg.WriteString("\nMark it in_progress to begin.")
	default:
		msg.WriteString("\nReady tasks:\n")
		for _, t := range eligible {
			msg.WriteString(fmt.Sprintf("  #%d — %s\n", t.ID, t.Description))
			if t.Prompt != "" {
				msg.WriteString(fmt.Sprintf("      Instructions: %s\n", t.Prompt))
			}
		}
		msg.WriteString("Pick one and mark it in_progress.")
	}
}
