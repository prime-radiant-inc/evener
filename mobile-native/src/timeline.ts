import type { MobileTimelineItem } from "./projectedRows";

type Notice = Extract<MobileTimelineItem, { kind: "notice" }>;

// Task-control instructions stay available without crowding the conversation.
export function steeringNoticeLabel(item: Notice): string | undefined {
	if (item.origin !== "steering" || isCriticalNotice(item)) return undefined;
	switch (item.steeringKind) {
		case "interrupted":
			return "Interrupted";
		case "interrupted-salvage":
			return "Interrupted draft";
		case "tasks-done":
			return "Tasks complete";
		case "task-nudge":
			return "Task reminder";
		case "task-inactive":
			return "Task list idle";
		case "current-task":
			return "Current task";
		case "task-list":
			return "Task list";
		default:
			return undefined;
	}
}

export function isCriticalNotice(item: Notice): boolean {
	return (
		item.tone === "warning" ||
		item.family === "warning" ||
		item.eventKind === "error" ||
		item.eventKind === "tool_repair" ||
		(item.eventKind === "hook_completed" &&
			item.exitCode !== undefined &&
			item.exitCode !== 0)
	);
}

export function isInterruptedNotice(item: Notice): boolean {
	return (
		item.origin === "steering" &&
		(item.steeringKind === "interrupted" ||
			item.steeringKind === "interrupted-salvage") &&
		item.tone !== "warning"
	);
}
export type TimelineRow =
	| MobileTimelineItem
	| {
			kind: "details";
			id: string;
			entries: Notice[];
	  };

// A question option row's React key (TimelineItem.tsx's "question" case): the
// option's POSITION in the question, never its label. The store's publish
// bounds every label a timeline question row carries (project.ts's
// truncateItem through boundQuestion, at MAX_ITEM_BYTES), so two options whose
// labels share a prefix past the bound cut to the same string — a key that
// reads the label keys both rows identically. Options render in the order the
// ask offered them, so their position is the one field that cannot collide
// past the display bound.
export function questionOptionKey(
	questionKey: string,
	index: number,
): string {
	return `${questionKey}:${index}`;
}

export function timelineGap(before: TimelineRow, after?: TimelineRow): number {
	if (!after) return 0;
	const needsAttention = (item: TimelineRow) =>
		item.kind === "failure" ||
		item.kind === "question" ||
		(item.kind === "notice" && isCriticalNotice(item)) ||
		(item.kind === "activity" && item.state === "failed");
	if (needsAttention(before) || needsAttention(after)) return 24;
	const routine = (item: TimelineRow) =>
		item.kind === "details" ||
		(item.kind === "notice" && steeringNoticeLabel(item) !== undefined);
	return routine(before) || routine(after) ? 8 : 24;
}

// Keep technical context available without putting it between the reader and the conversation.
export function groupTimeline(
	items: readonly MobileTimelineItem[],
): TimelineRow[] {
	const rows: TimelineRow[] = [];
	for (const item of items) {
		const internal =
			item.kind === "notice" &&
			!isCriticalNotice(item) &&
			(item.family === "hidden-instruction" ||
				item.family === "system-prelude" ||
				item.family === "diagnostic");
		if (!internal) {
			rows.push(item);
			continue;
		}
		const previous = rows.at(-1);
		if (previous?.kind === "details") previous.entries.push(item);
		else
			rows.push({ kind: "details", id: `details:${item.id}`, entries: [item] });
	}
	return rows;
}
