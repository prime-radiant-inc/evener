// How a task's state reads on the phone, the same wherever a task shows: the
// Tasks sheet and a task_list step's checklist.
import type { SFSymbol } from "expo-symbols";
import type { TaskStatus } from "@evener/appwire-client";

/** The word a task's state reads as, and its screen reader label. */
export const TASK_STATUS_LABEL: Record<TaskStatus, string> = {
	open: "Open",
	in_progress: "In progress",
	done: "Done",
	cancelled: "Cancelled",
};

/** The mark before a task's description: an SF Symbol in the state's tint, as
 * the Board's state marks pair shape with color (spec 13.1). */
export const TASK_STATUS_MARK: Record<TaskStatus, { name: SFSymbol; tint: TaskStatusTint }> = {
	open: { name: "circle", tint: "inkMid" },
	in_progress: { name: "circle.dotted", tint: "accentInk" },
	done: { name: "checkmark.circle.fill", tint: "inkLow" },
	cancelled: { name: "xmark.circle.fill", tint: "inkLow" },
};

export type TaskStatusTint = "inkMid" | "inkLow" | "accentInk";
