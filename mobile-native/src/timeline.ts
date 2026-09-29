import { ERROR_EVENT_KIND } from "@evener/appwire-client";
import type { AttachmentRef, MobileTimelineItem } from "./projectedRows";

type Notice = Extract<MobileTimelineItem, { kind: "notice" }>;

// A labelled notice folds to its label until opened: a daemon steer
// (steeringNotice) or a compaction's summary (systemNotice).
export function noticeLabel(item: Notice): string | undefined {
	return isCriticalNotice(item) ? undefined : item.label;
}

export function isCriticalNotice(item: Notice): boolean {
	return (
		item.tone === "warning" ||
		item.family === "warning" ||
		item.eventKind === ERROR_EVENT_KIND ||
		(item.eventKind === "hook_completed" && item.exitCode !== undefined && item.exitCode !== 0)
	);
}

export function isInterruptedNotice(item: Notice): boolean {
	return (
		item.origin === "steering" &&
		(item.steeringKind === "interrupted" || item.steeringKind === "interrupted-salvage") &&
		item.tone !== "warning"
	);
}
/** One step of a run: an activity row, with any images it produced. */
export type RunStep = Extract<MobileTimelineItem, { kind: "activity" }> & { images?: AttachmentRef[] };

export type TimelineRow =
	| MobileTimelineItem
	| {
			kind: "details";
			id: string;
			entries: Notice[];
	  }
	// Consecutive steps, folded into one line (spec 8.2). It keeps its first
	// step's identity, so a reading position saved on that step still resolves.
	| {
			kind: "run";
			id: string;
			steps: RunStep[];
			turnId?: string;
			transcriptKey?: string;
			position?: { entry: number; item: number };
	  }
	// A time marker before a turn that starts after a gap or on a new day.
	| { kind: "time"; id: string; turnId: string; at: number };

export function rowTurnId(row: TimelineRow): string | undefined {
	return row.kind === "details" ? row.entries[0]?.turnId : row.turnId;
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
		item.kind === "run" ||
		item.kind === "time" ||
		item.kind === "note" ||
		(item.kind === "notice" && noticeLabel(item) !== undefined);
	return routine(before) || routine(after) ? 8 : 24;
}

// Keep technical context available without putting it between the reader and the conversation.
export function groupTimeline(items: readonly MobileTimelineItem[]): TimelineRow[] {
	const rows: TimelineRow[] = [];
	for (const item of items) {
		const internal =
			item.kind === "notice" &&
			!isCriticalNotice(item) &&
			(item.family === "hidden-instruction" || item.family === "system-prelude" || item.family === "diagnostic");
		if (!internal) {
			rows.push(item);
			continue;
		}
		const previous = rows.at(-1);
		if (previous?.kind === "details") previous.entries.push(item);
		else rows.push({ kind: "details", id: `details:${item.id}`, entries: [item] });
	}
	return rows;
}
