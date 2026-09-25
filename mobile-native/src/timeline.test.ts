import { expect, it } from "vitest";
import type { AskQuestionRef } from "@evener/appwire-client";
import {
	boundQuestion,
	MAX_ITEM_BYTES,
	truncateText,
	type MobileTimelineItem,
} from "./projectedRows";
import {
	groupTimeline,
	isInterruptedNotice,
	questionOptionKey,
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
	const interruptedSalvage = {
		...interrupted,
		steeringKind: "interrupted-salvage" as const,
	};
	expect(isInterruptedNotice(interrupted)).toBe(true);
	expect(isInterruptedNotice(interruptedSalvage)).toBe(true);
	expect(isInterruptedNotice({ ...interrupted, tone: "warning" })).toBe(false);
	expect(isInterruptedNotice({ ...interruptedSalvage, tone: "warning" })).toBe(
		false,
	);
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
	"interrupted-salvage",
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

it("labels interrupted salvage as a draft", () => {
	const interruptedSalvage = {
		...setup,
		family: "informational" as const,
		origin: "steering" as const,
		steeringKind: "interrupted-salvage" as const,
	};
	expect(steeringNoticeLabel(interruptedSalvage)).toBe("Interrupted draft");
	expect(timelineGap(message, interruptedSalvage)).toBeLessThan(
		timelineGap(message, message),
	);
});

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

// The option rows TimelineItem renders come from the store's bounded publish
// (state/conversation.ts's truncateItem over project.ts's boundQuestion), so
// their labels are cut copies — the same display bound
// questionAnswers.ts's questionsIdentity works against. This pins the
// option-row React key against that bounding collision: keyed on the label,
// two options whose labels share a prefix past the display bound cut to the
// same string and both rows answer to one key (React's duplicate-key
// collision); keyed on the option's position, they cannot.
const ask: AskQuestionRef = {
	key: "call:0",
	callId: "call",
	header: "Choose",
	question: "Pick one",
	multiSelect: false,
	options: [],
};

function boundedLabel(label: string): string {
	return boundQuestion(
		{ ...ask, options: [{ label, detail: "" }] },
		(text) => truncateText(text, MAX_ITEM_BYTES),
	).options[0].label;
}

it("keys an ask's option rows by position, not by the bounded label", () => {
	const prefix = "x".repeat(MAX_ITEM_BYTES * 2);
	const first = `${prefix}-first-tail`;
	const second = `${prefix}-second-tail`;

	// The store's publish bounds both labels to the same cut copy — the
	// collision the position key exists to survive. (TimelineItem keyed the
	// option row on exactly this bounded label before the fix, so both rows
	// answered to one React key: `${question.key}:${option.label}`.)
	expect(boundedLabel(first)).toBe(boundedLabel(second));

	// The option-row key stays distinct where the bounded labels do not.
	expect(questionOptionKey("call:0", 0)).not.toBe(questionOptionKey("call:0", 1));
	// And across questions, as the key's question half already guaranteed.
	expect(questionOptionKey("call:0", 0)).not.toBe(questionOptionKey("call:1", 0));
	expect(questionOptionKey("call:0", 1)).toBe("call:0:1");
});
