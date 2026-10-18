// How a task's state reads on the phone, the same wherever a task shows: the
// Tasks sheet and a task_list step's checklist.
import type { SFSymbol } from "expo-symbols";
import type { TaskStatus } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { useColors } from "./ui";

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

/** The state's mark itself, a tinted SF Symbol sized to its row: the Tasks
 * sheet and a task_list step's checklist draw the same one beside a task's
 * description. The composed label around it reads the state in words. */
export function TaskStatusMark({ status, size, style }: { status: TaskStatus; size: number; style?: object }) {
	const { palette } = useColors();
	const mark = TASK_STATUS_MARK[status];
	return <SymbolView name={mark.name} tintColor={palette[mark.tint]} size={size} style={style} />;
}
