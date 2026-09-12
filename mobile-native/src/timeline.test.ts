import { expect, it } from "vitest";
import type { MobileTimelineItem } from "../../mobile/src/conversation/model";
import {
	groupTimeline,
	isInterruptedNotice,
	steeringNoticeLabel,
	timelineGap,
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

it("collapses only typed non-warning interruption notices", () => {
	const interrupted = {
		...setup,
		origin: "steering" as const,
		steeringKind: "interrupted",
	};
	expect(isInterruptedNotice(interrupted)).toBe(true);
	expect(isInterruptedNotice({ ...interrupted, tone: "warning" })).toBe(false);
	expect(isInterruptedNotice({ ...interrupted, origin: "system" })).toBe(false);
	expect(
		isInterruptedNotice({
			...interrupted,
			steeringKind: undefined,
			text: "Interrupted",
		}),
	).toBe(false);
	expect(
		isInterruptedNotice({ ...interrupted, steeringKind: "notification" }),
	).toBe(false);
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
	expect(
		rows.flatMap((row) => (row.kind === "details" ? row.entries : [row])),
	).toEqual(input);
	expect(input).toHaveLength(4);
});

it("keeps warnings visible even when classified as internal details", () => {
	const internalWarning = {
		...diagnostic,
		id: "internal-warning",
		tone: "warning" as const,
	};
	expect(
		groupTimeline([setup, warning, internalWarning, diagnostic]),
	).toMatchObject([
		{ kind: "details", entries: [setup] },
		warning,
		internalWarning,
		{ kind: "details", entries: [diagnostic] },
	]);
});

it("retains a disclosure identity as adjacent details arrive", () => {
	expect(groupTimeline([setup])[0]?.id).toBe(
		groupTimeline([setup, diagnostic])[0]?.id,
	);
	expect(groupTimeline([])).toEqual([]);
});

it.each([
	"interrupted",
	"tasks-done",
	"task-nudge",
	"task-inactive",
	"current-task",
	"task-list",
])(
	"offers a compact disclosure only for noncritical typed %s steering",
	(steeringKind) => {
		const notice = {
			...setup,
			family: "informational" as const,
			origin: "steering" as const,
			steeringKind,
		};
		expect(steeringNoticeLabel(notice)).toEqual(expect.any(String));
		expect(timelineGap(message, notice)).toBeLessThan(
			timelineGap(message, message),
		);
		expect(
			steeringNoticeLabel({ ...notice, origin: "system" }),
		).toBeUndefined();
		expect(steeringNoticeLabel({ ...notice, tone: "warning" })).toBeUndefined();
	},
);

it("keeps unknown steering, untyped notices, and critical diagnostics visible", () => {
	const task = {
		...setup,
		origin: "steering" as const,
		steeringKind: "tasks-done",
	};
	for (const override of [
		{ steeringKind: undefined },
		{ steeringKind: "unknown" },
		{ steeringKind: "notification" },
		{ family: "warning" as const },
		{ eventKind: "error" },
		{ eventKind: "tool_repair" },
		{ eventKind: "hook_completed", exitCode: 3 },
	])
		expect(steeringNoticeLabel({ ...task, ...override })).toBeUndefined();
	expect(steeringNoticeLabel({ ...setup, text: "tasks-done" })).toBeUndefined();
});

it.each([
	{ eventKind: "error" },
	{ eventKind: "tool_repair" },
	{ eventKind: "hook_completed", exitCode: 3 },
	{ family: "warning" as const },
])(
	"keeps typed critical notices outside collapsed diagnostic groups: %j",
	(metadata) => {
		const critical = { ...diagnostic, ...metadata };
		expect(groupTimeline([setup, critical])).toEqual([
			{ kind: "details", id: "details:setup", entries: [setup] },
			critical,
		]);
	},
);
