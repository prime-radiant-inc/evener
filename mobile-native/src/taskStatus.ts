// How a task's state reads on the phone, the same wherever a task shows: the
// Tasks sheet and a task_list step's checklist.
import type { TaskStatus } from "@evener/appwire-client";

/** The word a task's state reads as, and its screen reader label. */
export const TASK_STATUS_LABEL: Record<TaskStatus, string> = {
	open: "Open",
	in_progress: "In progress",
	done: "Done",
	cancelled: "Cancelled",
};

/** The mark before a task's description. */
export const TASK_STATUS_GLYPH: Record<TaskStatus, string> = {
	open: "○",
	in_progress: "●",
	done: "✓",
	cancelled: "×",
};
