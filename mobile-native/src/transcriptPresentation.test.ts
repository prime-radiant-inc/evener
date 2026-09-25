import { expect, it } from "vitest";
import {
	makeTranscriptDisplayConfig,
	presetContent,
	projectThread as sharedProjectThread,
	THREAD_ITEM_EVENT_KINDS,
} from "@evener/appwire-client";
import type {
	MobileConversation,
	MobileTimelineItem,
} from "./projectedRows";
import { projectConversation } from "./projectedRows";
import type {
	TranscriptDisplayAdvancedV1,
	TranscriptDisplayConfigV1,
	ThreadModel,
	TurnModel,
} from "@evener/appwire-client";
import { projectNativeTranscript, usageRows } from "./transcriptPresentation";

function conversation(
	items: MobileTimelineItem[],
	overrides: Partial<Pick<MobileConversation, "usage" | "turns" | "olderCursor" | "cost">> = {},
): MobileConversation {
	return {
		items,
		usage: { inputTokens: 10, outputTokens: 20 },
		turns: [],
		cost: "$1",
		...overrides,
	} as MobileConversation;
}

function usageTurn(id: string, inputTokens: number, outputTokens: number): TurnModel {
	return { id, status: "completed", items: [], usage: { inputTokens, outputTokens } };
}

const member = (
	id: string,
	description: string,
	item: number,
): NonNullable<
	Extract<MobileTimelineItem, { kind: "activity" }>["members"]
>[number] => ({
	id,
	label: "shell",
	family: "tool",
	state: "completed",
	detail: { description, output: id },
	transcriptKey: `key-${id}`,
	position: { entry: 1, item },
});

it.each([null, undefined])(
	"flattens every clustered member and preserves attachments when preferences are %s",
	(config) => {
		const items: MobileTimelineItem[] = [
			{
				kind: "activity",
				id: "cluster",
				label: "shell",
				family: "tool",
				state: "completed",
				detail: {},
				members: [member("a", "first", 0), member("b", "second", 1)],
			},
			{
				kind: "attachments",
				id: "b:attachments",
				sourceTranscriptKey: "key-b",
				items: [{ id: "image", src: "data:image/png;base64,x" }],
			},
		];
		const result = projectNativeTranscript(conversation(items), config);
		expect(result.items).toEqual([
			{ kind: "activity", ...member("a", "first", 0) },
			{ kind: "activity", ...member("b", "second", 1) },
			items[1],
		]);
		expect(result.activityPresentation).toEqual(
			new Map([
				["a", { mode: "full" }],
				["b", { mode: "full" }],
			]),
		);
		expect(result.items).not.toBe(items);
		expect(result.expandByDefault).toBe(false);
		expect(result.usage).toBeNull();
	},
);

it("unrolls members, applies preset content, and keeps source-linked attachments", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "activity",
			id: "cluster",
			label: "shell",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
		{
			kind: "attachments",
			id: "b:attachments",
			sourceTranscriptKey: "key-b",
			items: [{ id: "image", src: "data:image/png;base64,x" }],
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"a",
		"b",
		"b:attachments",
	]);
	expect(result.items[0]).toMatchObject({ position: { entry: 1, item: 0 } });
	expect(result.activityPresentation.get("a")).toEqual({ mode: "full" });
	expect(result.activityPresentation.get("b")).toEqual({ mode: "full" });
});

// D24-6: the presentation layer no longer decides which rows exist — the
// shared projector does, once, inside the store's seam. What survives here is
// the RENDERING mode of each row the seam produced: a summary-only row (the
// projector's intent entry; the operator's summary-only ruling) renders its
// summary line, a running or failed activity renders as attention, and every
// other activity renders in full — whatever the config, because the level was
// already applied upstream.
it("renders summary-only rows as intent, active or failed as critical, and never drops a row the seam produced", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "activity",
			id: "summarized",
			label: "shell",
			family: "tool",
			state: "completed",
			summaryOnly: true,
			detail: { description: "Inspect source" },
		},
		{
			kind: "activity",
			id: "settled",
			label: "shell",
			family: "tool",
			state: "completed",
			detail: { description: "Inspect source" },
		},
		{
			kind: "activity",
			id: "failed",
			label: "shell",
			family: "tool",
			state: "failed",
			detail: { error: "failed" },
		},
		{
			kind: "activity",
			id: "active",
			label: "shell",
			family: "tool",
			state: "running",
			detail: {},
		},
		{
			kind: "activity",
			id: "unknown",
			label: "Activity",
			family: "unknown",
			state: "completed",
			detail: {},
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "custom", ...presetContent("chat") }),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"summarized",
		"settled",
		"failed",
		"active",
		"unknown",
	]);
	expect(result.activityPresentation.get("summarized")).toEqual({
		mode: "intent",
		summary: "Inspect source",
	});
	// A settled row without the summary-only marker renders in full whatever
	// the config: the level was the projector's decision, not this layer's.
	expect(result.activityPresentation.get("settled")).toEqual({ mode: "full" });
	expect(result.activityPresentation.get("active")).toMatchObject({
		mode: "critical",
	});
	expect(result.activityPresentation.get("failed")).toMatchObject({
		mode: "critical",
	});
	expect(result.activityPresentation.get("unknown")).toEqual({ mode: "full" });
});

it.each(["chat", "intent", "tools", "activity", "full"] as const)(
	"honors preset %s content and expansion defaults",
	(level) => {
		const result = projectNativeTranscript(
			conversation([
				{
					kind: "activity",
					id: "reasoning",
					label: "reasoning",
					family: "reasoning",
					state: "completed",
					detail: {},
				},
			]),
			makeTranscriptDisplayConfig({ kind: "preset", level }),
		);
		expect(result.expandByDefault).toBe(presetContent(level).expandByDefault);
		// The reasoning row survives every level: whether it exists at all is
		// the projector's decision at the seam (D24-6), never this layer's.
		expect(result.items).toHaveLength(1);
	},
);

it("keeps every system-event row the seam produced and masks usage fields independently", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "notice",
			id: "prompt",
			origin: "system",
			family: "system-prelude",
			tone: "system",
			text: "prompt",
			eventKind: "prompt_loaded",
		},
		{
			kind: "notice",
			id: "hook",
			origin: "system",
			family: "diagnostic",
			tone: "system",
			text: "hook",
			eventKind: "hook_completed",
			exitCode: 4,
		},
		{
			kind: "notice",
			id: "routine",
			origin: "system",
			family: "diagnostic",
			tone: "system",
			text: "routine",
			eventKind: "environment",
		},
		{
			kind: "notice",
			id: "error",
			origin: "system",
			family: "warning",
			tone: "system",
			text: "error",
			eventKind: "error",
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig(
			{ kind: "preset", level: "chat" },
			{ promptEvents: true, tokenCounts: false, estimatedCost: false },
		),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"prompt",
		"hook",
			"routine",
		"error",
	]);
	expect(result.usage).toEqual({ usage: null, cost: null });
});

// tokenCounts gates the token aggregate and estimatedCost gates the cost, each
// on its own: a crossed gate or an always-null branch fails one of these rows.
it.each([
	{ tokenCounts: true, estimatedCost: true, usage: { inputTokens: 10, outputTokens: 20, scope: "session" }, cost: "$1" },
	{ tokenCounts: true, estimatedCost: false, usage: { inputTokens: 10, outputTokens: 20, scope: "session" }, cost: null },
	{ tokenCounts: false, estimatedCost: true, usage: null, cost: "$1" },
	{ tokenCounts: false, estimatedCost: false, usage: null, cost: null },
])(
	"passes usage through only under tokenCounts=$tokenCounts and cost only under estimatedCost=$estimatedCost",
	({ tokenCounts, estimatedCost, usage, cost }) => {
		const result = projectNativeTranscript(
			conversation([]),
			makeTranscriptDisplayConfig(
				{ kind: "preset", level: "chat" },
				{ tokenCounts, estimatedCost },
			),
		);
		expect(result.usage).toEqual({ usage, cost });
	},
);

it("reads an unknown cost as null even when estimatedCost is on", () => {
	const result = projectNativeTranscript(
		{ ...conversation([]), cost: undefined },
		makeTranscriptDisplayConfig(
			{ kind: "preset", level: "chat" },
			{ tokenCounts: true, estimatedCost: true },
		),
	);
	expect(result.usage).toEqual({
		usage: { inputTokens: 10, outputTokens: 20, scope: "session" },
		cost: null,
	});
});

// A fork child's persisted meta carries no CumulativeUsage (agent/fork.go's
// writeForkChild never stamps one) even though every loaded turn has real
// usage, which is why the transcript's per-turn stamps rendered right beside
// a footer that showed nothing.
it("falls back to summing the loaded turns when the thread has no cumulative total", () => {
	const result = projectNativeTranscript(
		conversation([], { usage: undefined, turns: [usageTurn("t1", 6961, 73), usageTurn("t2", 1276, 47)] }),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, { tokenCounts: true, estimatedCost: false }),
	);
	expect(result.usage).toEqual({
		usage: { inputTokens: 8237, outputTokens: 120, scope: "session" },
		cost: null,
	});
});

// thread/read windows items via itemLimit and reports the truncation through
// olderCursor. A sum over that window is not the session total, so the scope
// says exactly what it counts instead of overstating it.
it("labels a derived total over a truncated turn window as covering only the loaded turns", () => {
	const result = projectNativeTranscript(
		conversation([], { usage: undefined, turns: [usageTurn("t1", 500, 20)], olderCursor: "cursor_1" }),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, { tokenCounts: true, estimatedCost: false }),
	);
	expect(result.usage).toEqual({
		usage: { inputTokens: 500, outputTokens: 20, scope: "loaded" },
		cost: null,
	});
});

// The wire's EvenerUsage permits a sparse cumulative object: cacheReadTokens
// or totalTokens alone, with no input/output pair at all. sessionTokens has
// no per-turn equivalent for either field, so accountingFor must not lose
// them just because the derived input/output pair came back empty.
it("keeps a cache-only cumulative breakdown even when sessionTokens finds no input/output data", () => {
	const result = projectNativeTranscript(
		conversation([], { usage: { cacheReadTokens: 42 }, turns: [] }),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, { tokenCounts: true, estimatedCost: false }),
	);
	expect(result.usage).toEqual({ usage: { cacheReadTokens: 42 }, cost: null });
});

it("keeps a total-only cumulative breakdown even when sessionTokens finds no input/output data", () => {
	const result = projectNativeTranscript(
		conversation([], { usage: { totalTokens: 500 }, turns: [] }),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, { tokenCounts: true, estimatedCost: false }),
	);
	expect(result.usage).toEqual({ usage: { totalTokens: 500 }, cost: null });
});

// D18 B3 round 6 (Low): a cumulative field's Go zero value signals absence,
// the same rule sessionTokens already applies to inputTokens/outputTokens
// (threadUsage.ts) - a real "0 tokens" for cacheReadTokens/totalTokens is
// indistinguishable from an unset field, so it must not render as data.
it("treats a zero cacheReadTokens/totalTokens the same as an absent one", () => {
	const result = projectNativeTranscript(
		conversation([], { usage: { cacheReadTokens: 0, totalTokens: 0 }, turns: [] }),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, { tokenCounts: true, estimatedCost: false }),
	);
	expect(result.usage).toEqual({ usage: null, cost: null });
});

// A sparse cumulative object (total-only, no input/output) alongside a
// truncated turn window: sessionTokens sums the turns and scopes the result
// "loaded", but the cumulative total is a whole-session figure and must
// carry no scope of its own - it is not itself a loaded-turn sum.
it("keeps the cumulative breakdown's scope independent of a turn-summed loaded result", () => {
	const result = projectNativeTranscript(
		conversation([], {
			usage: { totalTokens: 500 },
			turns: [usageTurn("t1", 60, 40)],
			olderCursor: "cursor_1",
		}),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, { tokenCounts: true, estimatedCost: false }),
	);
	expect(result.usage).toEqual({
		usage: { inputTokens: 60, outputTokens: 40, scope: "loaded", totalTokens: 500 },
		cost: null,
	});
	// The data alone doesn't show which unit each field renders with - that's
	// usageRows's job, and it must never stamp the whole-session Total row
	// with the derived pair's "loaded" scope.
	expect(usageRows(result.usage!.usage)).toEqual([
		{ label: "Input", value: 60, unit: "tokens (loaded turns)" },
		{ label: "Output", value: 40, unit: "tokens (loaded turns)" },
		{ label: "Total", value: 500, unit: "tokens" },
	]);
});

it("does not mutate clustered members or source items while projecting", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "activity",
			id: "cluster",
			label: "tools",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
	];
	const before = structuredClone(items);
	projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "custom", ...presetContent("full") }),
	);
	expect(items).toEqual(before);
});

it("preserves canonical interleaving and defers source-linked attachments to members", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "notice",
			id: "before",
			origin: "steering",
			family: "informational",
			tone: "info",
			text: "before",
		},
		{
			kind: "attachments",
			id: "b:image",
			sourceTranscriptKey: "key-b",
			items: [{ id: "image", src: "data:image/png;base64,x" }],
		},
		{
			kind: "activity",
			id: "cluster",
			label: "tools",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
		{
			kind: "notice",
			id: "after",
			origin: "steering",
			family: "informational",
			tone: "info",
			text: "after",
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"before",
		"a",
		"b",
		"b:image",
		"after",
	]);
});

it("renders a missing-description tool call in full rather than second-guessing the level", () => {
	// D24-6 retired the presentation layer's own config-driven classification:
	// which rows exist is the projector's decision at the seam. A row like
	// this one — a completed call with no description — was re-classified
	// critical here under the old rule; the projector now either summarizes it
	// upstream (the row carries summaryOnly) or keeps it as an ordinary item,
	// and this layer renders exactly what the marker says.
	const item: MobileTimelineItem = {
		kind: "activity",
		id: "missing",
		label: "shell",
		family: "tool",
		state: "completed",
		detail: {},
	};
	const result = projectNativeTranscript(
		conversation([item]),
		makeTranscriptDisplayConfig({
			kind: "custom",
			toolIntent: false,
			toolCalls: false,
			reasoning: false,
			expandByDefault: false,
		}),
	);
	expect(result.activityPresentation.get("missing")).toEqual({ mode: "full" });
});

it.each([
	["completed", "wrote 26 bytes to /tmp/request-16.txt", "Write"],
	["failed", "permission denied", "Write"],
	["running", undefined, "Write"],
] as const)(
	"derives a bounded write_file action summary from its target path when description is absent (%s)",
	(state, output, label) => {
		const result = projectNativeTranscript(
			conversation([
				{
					kind: "activity",
					id: "write",
					label: "write_file",
					family: "tool",
					state,
					// A completed write_file at a compact level reaches the
					// renderer as the projector's summarized row; a failed or
					// running one is critical and keeps its full detail.
					...(state === "completed" ? { summaryOnly: true } : {}),
					detail: {
						arguments: JSON.stringify({
							file_path: "/tmp/request-16.txt",
							content: "private file contents",
						}),
						...(output ? { output } : {}),
						error: state === "failed" ? output : undefined,
					},
				},
			]),
			makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
		);
		const presentation = result.activityPresentation.get("write");
		expect(presentation?.summary).toBe(`${label} /tmp/request-16.txt`);
		expect(presentation?.summary).not.toContain("private file contents");
	},
);

it("keeps each member attachment adjacent while messages and unkeyed warnings retain order", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "user",
			id: "user",
			text: "Start",
			position: { entry: 0, item: 0 },
		},
		{
			kind: "notice",
			id: "warning",
			origin: "system",
			family: "warning",
			tone: "warning",
			text: "Keep",
		},
		{
			kind: "attachments",
			id: "image-a",
			sourceTranscriptKey: "key-a",
			items: [],
		},
		{
			kind: "activity",
			id: "cluster",
			label: "Tools",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
		{
			kind: "attachments",
			id: "image-b",
			sourceTranscriptKey: "key-b",
			items: [],
		},
		{
			kind: "assistant",
			id: "reply",
			markdown: "Done",
			streaming: false,
			position: { entry: 2, item: 0 },
		},
	];
	const full = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "preset", level: "full" }),
	);
	expect(full.items.map((item) => item.id)).toEqual([
		"user",
		"warning",
		"a",
		"image-a",
		"b",
		"image-b",
		"reply",
	]);
	const hidden = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({
			kind: "custom",
			toolCalls: false,
			toolIntent: false,
			reasoning: false,
			expandByDefault: false,
		}),
	);
	// D24-6: the members survive whatever the config — a custom vector that
	// disables calls and intent would have the PROJECTOR drop these rows at
	// the seam, and the presentation layer no longer re-decides it here.
	expect(hidden.items.map((item) => item.id)).toEqual([
		"user",
		"warning",
		"a",
		"image-a",
		"b",
		"image-b",
		"reply",
	]);
});

it("falls back safely for malformed or empty write_file arguments", () => {
	for (const argumentsValue of [
		"{",
		"null",
		"{}",
		JSON.stringify({ file_path: "   " }),
		JSON.stringify({ file_path: 42 }),
		JSON.stringify({ path: "/tmp/unsupported-field.txt" }),
		JSON.stringify({ content: "private" }),
	]) {
		const result = projectNativeTranscript(
			conversation([
				{
					kind: "activity",
					id: argumentsValue,
					label: "write_file",
					family: "tool",
					state: "completed",
					// The shape the seam produces for a summarized write_file
					// at a compact level (the operator's summary-only ruling):
					// the presentation derives the summary line this test
					// exercises from exactly such a row.
					summaryOnly: true,
					detail: { arguments: argumentsValue },
				},
			]),
			makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
		);
		expect(result.activityPresentation.get(argumentsValue)?.summary).toBe(
			"Action summary unavailable",
		);
	}
});

it("bounds a derived write_file target without exposing its content", () => {
	const target = `/tmp/${"a".repeat(300)}`;
	const result = projectNativeTranscript(
		conversation([
			{
				kind: "activity",
				id: "write-long",
				label: "write_file",
				family: "tool",
				state: "completed",
				summaryOnly: true,
				detail: {
					arguments: JSON.stringify({ file_path: target, content: "private" }),
				},
			},
		]),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
	);
	const summary = result.activityPresentation.get("write-long")?.summary;
	expect(summary).toHaveLength("Write ".length + 256);
	expect(summary?.endsWith("...")).toBe(true);
	expect(summary).not.toContain("private");
});

it("keeps an authoritative write_file description ahead of derived details", () => {
	const result = projectNativeTranscript(
		conversation([
			{
				kind: "activity",
				id: "write-described",
				label: "write_file",
				family: "tool",
				state: "completed",
				summaryOnly: true,
				detail: {
					description: "Save the fixture",
					arguments: JSON.stringify({
						file_path: "/tmp/request.txt",
						content: "private",
					}),
				},
			},
		]),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
	);
	expect(result.activityPresentation.get("write-described")?.summary).toBe(
		"Save the fixture",
	);
});

it.each([null, undefined])(
	"keeps each clustered member's attachment beside it when preferences are %s",
	(config) => {
		const items: MobileTimelineItem[] = [
			{
				kind: "activity",
				id: "cluster",
				label: "shell",
				family: "tool",
				state: "completed",
				detail: {},
				members: [member("a", "first", 0), member("b", "second", 1)],
			},
			{
				kind: "attachments",
				id: "a:attachments",
				sourceTranscriptKey: "key-a",
				items: [{ id: "image-a", src: "data:image/png;base64,a" }],
			},
			{
				kind: "attachments",
				id: "b:attachments",
				sourceTranscriptKey: "key-b",
				items: [{ id: "image-b", src: "data:image/png;base64,b" }],
			},
		];
		const result = projectNativeTranscript(conversation(items), config);
		expect(result.items).toEqual([
			{ kind: "activity", ...member("a", "first", 0) },
			items[1],
			{ kind: "activity", ...member("b", "second", 1) },
			items[2],
		]);
	},
);

// tokenUnitLabel itself moved to appwire-client/typescript/threadUsage.ts
// (D18 B3 round 3): its tests moved with it, to threadUsage.test.ts.

// --- usage rows ----------------------------------------------------------

// usageRows is what TranscriptUsage renders: each row's own unit, never one
// unit borrowed from a different row's scope.
it("labels Input/Output with the derived pair's own scope and Cached/Total plainly, even when the derived pair is loaded-scoped", () => {
	expect(
		usageRows({ inputTokens: 60, outputTokens: 40, scope: "loaded", cacheReadTokens: 10, totalTokens: 500 }),
	).toEqual([
		{ label: "Input", value: 60, unit: "tokens (loaded turns)" },
		{ label: "Output", value: 40, unit: "tokens (loaded turns)" },
		{ label: "Cached", value: 10, unit: "tokens" },
		{ label: "Total", value: 500, unit: "tokens" },
	]);
});

it("renders only the cumulative Total row when there is no derived input/output pair", () => {
	expect(usageRows({ totalTokens: 500 })).toEqual([{ label: "Total", value: 500, unit: "tokens" }]);
});

it("renders no rows for null usage", () => {
	expect(usageRows(null)).toEqual([]);
});

// --- the shared projector owns system-event visibility ----------------------
//
// D24-2: the event-kind vocabulary and the visible/hidden/critical rule belong
// to the package's projector (transcriptProjector.ts:98-121). Native keeps neither
// a vocabulary copy nor its own gate table: this table drives the whole
// vocabulary through BOTH the native presentation layer and the shared
// projector under every gate combination and requires the same verdict. A kind
// native gates differently - notes-context absent from its hand-kept set, or
// loop_detection/turn_limit forced visible by a warning tone - fails here.
const GATE_CONFIGS: { name: string; config: TranscriptDisplayConfigV1 }[] = (
	[
		{
			name: "all off",
			advanced: { systemEvents: false, promptEvents: false, roundTimings: false, hookExits: "none" },
		},
		{
			name: "system events",
			advanced: { systemEvents: true, promptEvents: false, roundTimings: false, hookExits: "none" },
		},
		{
			name: "prompt events",
			advanced: { systemEvents: false, promptEvents: true, roundTimings: false, hookExits: "none" },
		},
		{
			name: "round timings",
			advanced: { systemEvents: false, promptEvents: false, roundTimings: true, hookExits: "none" },
		},
		{
			name: "hooks all",
			advanced: { systemEvents: false, promptEvents: false, roundTimings: false, hookExits: "all" },
		},
		{
			name: "hooks successful",
			advanced: { systemEvents: false, promptEvents: false, roundTimings: false, hookExits: "successful" },
		},
		{
			name: "all on",
			advanced: { systemEvents: true, promptEvents: true, roundTimings: true, hookExits: "all" },
		},
	] satisfies { name: string; advanced: Partial<TranscriptDisplayAdvancedV1> }[]
).map(({ name, advanced }) => ({
	name,
	config: makeTranscriptDisplayConfig({ kind: "preset", level: "full" }, advanced),
}));

const EVENT_KIND_CASES: { eventKind?: string; exitCode?: number }[] = [
	...THREAD_ITEM_EVENT_KINDS.map((eventKind) => ({ eventKind })),
	{ eventKind: "future-event" },
	{},
	// hook_completed is the one kind whose gate reads exitCode.
	{ eventKind: "hook_completed", exitCode: 0 },
	{ eventKind: "hook_completed", exitCode: 3 },
];

// One projector-parity check, over the SEAM's own projection (D24-6 moved the
// visibility decision from this presentation layer into the store's seam): does
// the seam's projectConversation keep or drop an event exactly as projectThread
// does, at the same config? The probe thread ids every entry "system-event-probe"
// and the seam's notice row carries the same id.
function expectSeamVisibilityLikeProjector(
	model: ThreadModel,
	config: TranscriptDisplayConfigV1,
): void {
	const seamVisible = projectConversation(model, undefined, config).items.some(
		(item) => item.id === "system-event-probe",
	);
	const sharedVisible = sharedProjectThread(model, config).turns.some((turn) =>
		turn.entries.some((entry) => entry.id === "system-event-probe"),
	);
	expect(seamVisible).toBe(sharedVisible);
}

// A one-item thread for classifying a single system event (the deleted
// project.ts's systemEventProbe, kept here as the sweep's local fixture):
// projectThread reads only `turns`, so the rest of the shape is inert.
function systemEventModel(
	eventKind: string | undefined,
	exitCode: number | undefined,
): ThreadModel {
	return {
		ref: "system-event-probe",
		threadId: "system-event-probe",
		name: "system-event-probe",
		status: { type: "idle" },
		modelProvider: "",
		model: "",
		visionModel: "",
		askPending: false,
		pendingEscalations: [],
		queue: null,
		tasks: null,
		jobsUpdatedAt: null,
		jobsTreeRevision: null,
		lastFrameAt: 0,
		capabilities: {
			send: false,
			steer: false,
			interrupt: false,
			compact: false,
			clear: false,
			forkFromTurn: false,
			shutdown: false,
			changeModel: false,
			changeVisionModel: false,
			sharedNotes: false,
			queue: false,
			goal: false,
			rename: false,
		},
		goal: null,
		humanNote: "",
		agentNote: "",
		sessionUrls: [],
		contextUsed: 0,
		contextWindow: 0,
		contextPressure: 0,
		usage: null,
		workMillis: 0,
		reasoningEffortLevels: [],
		supportsReasoning: false,
		cwd: "",
		turns: [
			{
				id: "system-event-probe",
				status: "completed",
				items: [
					{
						id: "system-event-probe",
						turnId: "system-event-probe",
						type: "systemMessage",
						text: "",
						eventKind,
						exitCode,
					},
				],
			},
		],
	};
}

it.each(
	EVENT_KIND_CASES.flatMap((kind) =>
		GATE_CONFIGS.map((gate) => ({
			title: `${kind.eventKind ?? "(none)"} exit=${kind.exitCode ?? "-"} gate=${gate.name}`,
			...kind,
			config: gate.config,
		})),
	),
)("gates $title the same as the shared projector", ({ eventKind, exitCode, config }) => {
	expectSeamVisibilityLikeProjector(systemEventModel(eventKind, exitCode), config);
});

// --- D24-6: the presentation layer owns no event-kind vocabulary or gate row --
//
// The sweep above drives the seam's own projection, which is where the gate
// matrix lives now. The guard below pins the retirement itself at the layer
// that no longer filters: a notice ROW the seam produced survives this layer
// whatever its event kind, exit code or the gates — re-filtering here would
// re-decide the projector's decision a second time.
const NEVER_EVENTS_CONFIG = makeTranscriptDisplayConfig(
	{ kind: "preset", level: "full" },
	{ systemEvents: false, promptEvents: false, roundTimings: false, hookExits: "none" },
);

it.each([
	{ eventKind: "notes-context" },
	{ eventKind: "environment" },
	{ eventKind: "round_timings" },
	{ eventKind: "hook_completed", exitCode: 0 },
	{ eventKind: "hook_completed", exitCode: 3 },
	{ eventKind: "error" },
	{ eventKind: "future-event" },
	{},
] satisfies { eventKind?: string; exitCode?: number }[])(
	"keeps a notice row for $eventKind exit=$exitCode whatever the gates decide",
	({ eventKind, exitCode }) => {
		const notice: MobileTimelineItem = {
			kind: "notice",
			id: "notice",
			origin: "system",
			family: "unknown-system",
			tone: "system",
			text: "",
			...(eventKind ? { eventKind } : {}),
			...(exitCode !== undefined ? { exitCode } : {}),
		};
		const result = projectNativeTranscript(
			conversation([notice]),
			NEVER_EVENTS_CONFIG,
		);
		expect(result.items.map((item) => item.id)).toEqual(["notice"]);
	},
);
