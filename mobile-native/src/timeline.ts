import { ERROR_EVENT_KIND, isQuietYield } from "@evener/appwire-client";
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
			summary?: string;
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

// The key a step or run row keys by, shared by the list's row key (readerKey),
// the reading anchor match (matchesAnchor) and the disclosure store's member
// key (disclosureKeys.ts's memberKey), so all three move together as history
// records a live step. The identity is the call id, which is unique for the
// session: a provider names its calls uniquely, and one the provider leaves
// unnamed is minted a unique per-invocation id (agent/internal/tool's
// SynthesizedCallID). Undefined for a step with no call id, which keeps its row
// id instead; the step: prefix keeps it apart from an assistant reply's
// call:<callId>:agentMessage key.
//
// Why the call id and not the turn id: the turn id is not the same live and
// recorded for daemon-minted turns (internal/apptranscript/logical_turn.go's
// invariant), so keying on it moved a row's key when its item was recorded.
export function stepKey(step: { detail: { callId?: string } }): string | undefined {
	return step.detail.callId === undefined ? undefined : `step:${step.detail.callId}`;
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
		// Intent draws one line per action where a run would fold them: the
		// lines read as one list, so they keep the run's rhythm.
		(item.kind === "activity" && item.summaryOnly === true && item.state === "completed") ||
		(item.kind === "notice" && (noticeLabel(item) !== undefined || isQuietYield(item)));
	return routine(before) || routine(after) ? 8 : 24;
}

function isQuietYieldNotice(item: MobileTimelineItem | undefined): boolean {
	return item?.kind === "notice" && isQuietYield(item) && !isCriticalNotice(item);
}

function isNotificationSteeringNotice(item: MobileTimelineItem | undefined): boolean {
	return (
		item?.kind === "notice" &&
		item.origin === "steering" &&
		item.steeringKind !== "human-note" &&
		!item.fromParent &&
		!isCriticalNotice(item) &&
		(item.steeringKind === "notification" || item.notifications !== undefined)
	);
}

// Keep technical context available without putting it between the reader and the conversation.
export function groupTimeline(items: readonly MobileTimelineItem[]): TimelineRow[] {
	const nonQuietTurns = new Set<string>();
	const quietTurns = new Set<string>();
	for (const item of items) {
		if (item.turnId === undefined) continue;
		if (isQuietYieldNotice(item)) {
			quietTurns.add(item.turnId);
		} else if (!isNotificationSteeringNotice(item)) {
			nonQuietTurns.add(item.turnId);
		}
	}
	const isQuietWakeItem = (item: MobileTimelineItem | undefined): item is Notice => {
		if (item === undefined) return false;
		if (item.turnId !== undefined && (nonQuietTurns.has(item.turnId) || !quietTurns.has(item.turnId))) {
			return false;
		}
		return isQuietYieldNotice(item) || isNotificationSteeringNotice(item);
	};
	const rows: TimelineRow[] = [];
	let index = 0;
	while (index < items.length) {
		const item = items[index];
		if (!item) {
			index += 1;
			continue;
		}
		if (isQuietWakeItem(item)) {
			let end = index + 1;
			let lastQuiet = isQuietYieldNotice(item) ? index : -1;
			while (end < items.length) {
				if (!isQuietWakeItem(items[end])) break;
				if (isQuietYieldNotice(items[end])) lastQuiet = end;
				end += 1;
			}
			const groupEnd = lastQuiet >= index ? lastQuiet + 1 : index;
			const entries = items.slice(index, groupEnd) as Notice[];
			const quietCount = entries.filter((entry) => isQuietYieldNotice(entry)).length;
			if (quietCount >= 2) {
				rows.push({
					kind: "details",
					id: `quiet-yields:${item.id}`,
					entries,
					summary: `${quietCount} quiet yields`,
				});
				index = groupEnd;
				continue;
			}
		}
		const internal =
			item.kind === "notice" &&
			!isCriticalNotice(item) &&
			(item.family === "hidden-instruction" || item.family === "system-prelude" || item.family === "diagnostic");
		if (!internal) {
			rows.push(item);
			index += 1;
			continue;
		}
		const previous = rows.at(-1);
		if (previous?.kind === "details" && previous.summary === undefined) previous.entries.push(item);
		else rows.push({ kind: "details", id: `details:${item.id}`, entries: [item] });
		index += 1;
	}
	return rows;
}
