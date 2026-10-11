import { expect, it } from "vitest";
import type { MobileTimelineItem } from "./projectedRows";
import {
	groupTimeline,
	isCriticalNotice,
	isInterruptedNotice,
	noticeLabel,
	timelineGap,
	type TimelineRow,
} from "./timeline";

const setup: MobileTimelineItem = {
	kind: "notice",
	id: "setup",
	origin: "system",
	family: "hidden-instruction",
	tone: "system",
	text: "instructions",
};
const diagnostic: MobileTimelineItem = {
	...setup,
	id: "diagnostic",
	family: "diagnostic",
	text: "details",
};
const message: MobileTimelineItem = {
	kind: "user",
	id: "message",
	text: "task",
};
const warning: MobileTimelineItem = {
	...setup,
	id: "warning",
	family: "warning",
	tone: "warning",
};

it("uses tighter rhythm around routine details without compressing warnings or decisions", () => {
	const ordinary = timelineGap(message, message);
	const details = groupTimeline([setup])[0];
	if (!details) throw new Error("missing details");
	expect(timelineGap(message, details)).toBeLessThan(ordinary);
	expect(timelineGap(details, message)).toBeLessThan(ordinary);
	expect(timelineGap(details, warning)).toBe(ordinary);
	expect(timelineGap(warning, details)).toBe(ordinary);
	expect(
		timelineGap(details, {
			kind: "failure",
			id: "failed",
			title: "Failed",
			detail: "reason",
		}),
	).toBe(ordinary);
	expect(timelineGap(details, undefined)).toBe(0);
});

it("treats a run and a time marker as routine, like details", () => {
	const ordinary = timelineGap(message, message);
	const run: TimelineRow = { kind: "run", id: "run:a", steps: [] };
	const time: TimelineRow = { kind: "time", id: "time:turn_1", turnId: "turn_1", at: 0 };
	expect(timelineGap(message, run)).toBeLessThan(ordinary);
	expect(timelineGap(run, message)).toBeLessThan(ordinary);
	expect(timelineGap(message, time)).toBeLessThan(ordinary);
	expect(timelineGap(time, message)).toBeLessThan(ordinary);
});

it("treats a saved note as routine, like a run or a time marker (spec 8.8)", () => {
	const ordinary = timelineGap(message, message);
	const note: TimelineRow = { kind: "note", id: "note:a", text: "Fix causes" };
	expect(timelineGap(message, note)).toBeLessThan(ordinary);
	expect(timelineGap(note, message)).toBeLessThan(ordinary);
});

it("collapses only typed non-warning interruption notices", () => {
	const interrupted = {
		...setup,
		origin: "steering" as const,
		steeringKind: "interrupted",
	};
	const interruptedSalvage = {
		...interrupted,
		steeringKind: "interrupted-salvage" as const,
	};
	expect(isInterruptedNotice(interrupted)).toBe(true);
	expect(isInterruptedNotice(interruptedSalvage)).toBe(true);
	expect(isInterruptedNotice({ ...interrupted, tone: "warning" })).toBe(false);
	expect(isInterruptedNotice({ ...interruptedSalvage, tone: "warning" })).toBe(false);
	expect(isInterruptedNotice({ ...interrupted, origin: "system" })).toBe(false);
	expect(
		isInterruptedNotice({
			...interrupted,
			steeringKind: undefined,
			text: "Interrupted",
		}),
	).toBe(false);
	expect(isInterruptedNotice({ ...interrupted, steeringKind: "notification" })).toBe(false);
});

it("groups consecutive internal entries without losing order or contents", () => {
	const input = [setup, diagnostic, message, { ...setup, id: "later" }];
	const rows = groupTimeline(input);
	expect(rows).toHaveLength(3);
	expect(rows[0]).toMatchObject({
		kind: "details",
		entries: [setup, diagnostic],
	});
	expect(rows[1]).toBe(message);
	expect(rows.flatMap((row) => (row.kind === "details" ? row.entries : [row]))).toEqual(input);
	expect(input).toHaveLength(4);
});

it("keeps warnings visible even when classified as internal details", () => {
	const internalWarning = {
		...diagnostic,
		id: "internal-warning",
		tone: "warning" as const,
	};
	expect(groupTimeline([setup, warning, internalWarning, diagnostic])).toMatchObject([
		{ kind: "details", entries: [setup] },
		warning,
		internalWarning,
		{ kind: "details", entries: [diagnostic] },
	]);
});

it("retains a disclosure identity as adjacent details arrive", () => {
	expect(groupTimeline([setup])[0]?.id).toBe(groupTimeline([setup, diagnostic])[0]?.id);
	expect(groupTimeline([])).toEqual([]);
});

it("folds a labelled notice to its label, unless it is critical", () => {
	const notice = {
		...setup,
		family: "informational" as const,
		origin: "steering" as const,
		steeringKind: "tasks-done",
		label: "Tasks complete",
	};
	expect(noticeLabel(notice)).toBe("Tasks complete");
	expect(timelineGap(message, notice)).toBeLessThan(timelineGap(message, message));
	expect(noticeLabel({ ...notice, tone: "warning" })).toBeUndefined();
	expect(noticeLabel({ ...notice, eventKind: "error" })).toBeUndefined();
	expect(noticeLabel({ ...notice, label: undefined })).toBeUndefined();
});

// A tool repair is a quiet system event (spec 8.2): the call ran, corrected.
it("never reads a tool repair as critical", () => {
	expect(isCriticalNotice({ ...setup, origin: "system" as const, eventKind: "tool_repair" })).toBe(false);
});

it.each([{ eventKind: "error" }, { eventKind: "hook_completed", exitCode: 3 }, { family: "warning" as const }])(
	"keeps typed critical notices outside collapsed diagnostic groups: %j",
	(metadata) => {
		const critical = { ...diagnostic, ...metadata };
		expect(groupTimeline([setup, critical])).toEqual([
			{ kind: "details", id: "details:setup", entries: [setup] },
			critical,
		]);
	},
);
